package qa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// SQLGenerationResult wraps the output of SQL generation.
type SQLGenerationResult struct {
	SQL         string `json:"sql"`
	Unsupported bool   `json:"unsupported"`
	Reason      string `json:"reason,omitempty"`
}

var (
	recordIDRegex  = regexp.MustCompile(`(?i)\b(INT|SET|BNK)[0-9]+\b`)
	injectionRegex = regexp.MustCompile(`(?i)(ignore\s+previous|drop\s+table|delete\s+from|update\s+|insert\s+into|truncate|grant\s+|revoke\s+|union\s+select|select\s+pg_|information_schema)`)
)

// GenerateSQL converts a natural-language question into a single safe PostgreSQL query.
func GenerateSQL(ctx context.Context, question string, runID string, meta *DBMetadataContext, apiKey, baseURL, model string) (*SQLGenerationResult, error) {
	qTrim := strings.TrimSpace(question)
	if qTrim == "" {
		return &SQLGenerationResult{Unsupported: true, Reason: "Empty question"}, nil
	}

	if meta == nil {
		meta = &DBMetadataContext{
			RunID:           runID,
			ActiveMerchants: DefaultMerchants,
			Categories:      KnownCategories,
		}
	}

	// Immediate prompt-injection and mutation pre-check
	if injectionRegex.MatchString(qTrim) {
		return &SQLGenerationResult{
			Unsupported: true,
			Reason:      "Rejected: Prompt contains adversarial keywords, mutation attempts, or disallowed system functions.",
		}, nil
	}

	// 1. Deterministic-First: If question matches a specific record ID or known canonical controller intent,
	// generate the mathematically verified SQL query directly (0ms, 100% precision, zero hallucinations).
	if offRes, err := generateSQLOffline(qTrim, runID, meta); err == nil && !offRes.Unsupported {
		return offRes, nil
	}

	// 2. For ad-hoc/custom inquiries, invoke NVIDIA NIM with rich database context & schema guardrails
	if apiKey != "" {
		llmRes, err := generateSQLWithNIM(ctx, qTrim, runID, meta, apiKey, baseURL, model)
		if err == nil && llmRes != nil {
			return llmRes, nil
		}
	}

	// 3. Fallback to offline generator
	return generateSQLOffline(qTrim, runID, meta)
}

func generateSQLWithNIM(ctx context.Context, question string, runID string, meta *DBMetadataContext, apiKey, baseURL, model string) (*SQLGenerationResult, error) {
	if model == "" {
		model = "meta/llama-3.2-11b-vision-instruct"
	}
	if baseURL == "" {
		baseURL = "https://integrate.api.nvidia.com/v1"
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	if !strings.HasSuffix(baseURL, "/chat/completions") {
		baseURL = baseURL + "/chat/completions"
	}

	var merchantList strings.Builder
	for _, m := range meta.ActiveMerchants {
		merchantList.WriteString(fmt.Sprintf("- %s (Brand: \"%s\", City: \"%s\")\n", m.ID, m.BrandName, m.City))
	}

	resolvedMerchant := ResolveMerchant(question, meta.ActiveMerchants)
	entityHint := ""
	if resolvedMerchant != nil {
		entityHint = fmt.Sprintf("\nDETECTED ENTITY HINT:\n- The user question references merchant \"%s\".\n- The exact database merchant_id is '%s'.\n- You MUST filter using merchant_id = '%s' (or use ILIKE '%%%s%%'). NEVER use '%s' as merchant_id!\n",
			resolvedMerchant.BrandName, resolvedMerchant.ID, resolvedMerchant.ID, strings.ToLower(resolvedMerchant.BrandName), strings.ToLower(resolvedMerchant.BrandName))
	}

	systemPrompt := fmt.Sprintf(`You are an expert SQL generator for the AI Finance Controller reconciliation database running on PostgreSQL.
Target reconciliation run_id: '%s'.

DATABASE ENTITY REGISTRY & ACTIVE MERCHANTS:
%s%s
CONTROLLED VOCABULARIES & DOMAIN DEFINITIONS:
1. exceptions.category:
   - 'SETTLED_NOT_BANKED': Settlements awaiting bank credit statement / unbanked settlements / pending settlements (Hop 2).
   - 'ORPHAN_SETTLEMENT': Gateway settlement record with no internal gross ledger counterpart (Hop 1).
   - 'AMOUNT_MISMATCH': Internal transaction and gateway settlement amount differ beyond fee tolerance (Hop 1).
   - 'DUPLICATE_SETTLEMENT': Multiple gateway settlements matched to a single internal transaction (Hop 1).
   - 'PARTIAL_CREDIT': Bank credit amount was less than settlement batch total (Hop 2).
   - 'BANKED_NOT_SETTLED': Bank credit statement with no matching settlement batch (Hop 2).
   - 'NO_COUNTERPART': Internal transaction with no gateway settlement (Hop 1).
2. exceptions.ai_status: 'SUCCEEDED', 'PENDING', 'FAILED', 'NOT_REQUIRED'.
3. reconciliation_matches.reconciliation_status: 'FULL', 'PARTIAL', 'UNMATCHED'.
4. reconciliation_matches.hop1_rule: 'EXACT_REFERENCE', 'AMOUNT_DATE_WINDOW', 'EXACT_REFERENCE_EXTENDED_DATE', 'EXTENDED_DATE_WINDOW'.
5. reconciliation_matches.hop2_rule: 'BATCH_REFERENCE'.

APPROVED DATABASE SCHEMA:
- reconciliation_runs (run_id UUID, seed BIGINT, dataset_version TEXT, engine_version TEXT, internal_count INT, settlement_count INT, bank_count INT, hop1_matched_count INT, hop2_matched_count INT, full_chain_count INT, exception_count INT, unresolved_amount_paise BIGINT, engine_duration_ms BIGINT, source_records_processed INT, throughput_records_per_sec NUMERIC)
- internal_transactions (id TEXT, run_id UUID, amount_paise BIGINT, currency TEXT, transaction_date TIMESTAMPTZ, merchant_id TEXT, reference_id TEXT)
- settlement_records (id TEXT, run_id UUID, settled_amount_paise BIGINT, currency TEXT, settlement_date TIMESTAMPTZ, merchant_id TEXT, reference_id TEXT, batch_id TEXT)
- bank_statements (id TEXT, run_id UUID, credited_amount_paise BIGINT, currency TEXT, credit_date TIMESTAMPTZ, merchant_id TEXT, batch_reference TEXT, narration TEXT)
- reconciliation_matches (id UUID, run_id UUID, internal_id TEXT, settlement_id TEXT, bank_statement_id TEXT, hop1_rule TEXT, hop2_rule TEXT, hop1_confidence NUMERIC, hop2_confidence NUMERIC, reconciliation_status TEXT, fee_delta_paise BIGINT, expected_bank_amount_paise BIGINT, actual_bank_amount_paise BIGINT, bank_delta_paise BIGINT)
- exceptions (id UUID, run_id UUID, record_id TEXT, source TEXT ['internal','settlement','bank'], category TEXT, hop TEXT ['HOP1','HOP2'], reason TEXT, expected_amount_paise BIGINT, actual_amount_paise BIGINT, delta_paise BIGINT, exposure_paise BIGINT, ai_status TEXT, ai_summary TEXT, ai_action TEXT, ai_confidence TEXT)
- audit_log (decision_id UUID, run_id UUID, record_ids TEXT[], rule_applied TEXT, fields_compared JSONB, candidates_considered JSONB, outcome TEXT, ai_reasoning TEXT)

CRITICAL SQL RULES & JOINS:
1. REASONS / WHY / DISCREPANCIES / FAILURES / ISSUES:
   - When asked for "reasons", "why", "causes", "issues", "problems", or "discrepancies" (e.g. "what are reasons for zomato settlements?"):
     You MUST query the 'exceptions' table for category, reason, exposure_paise, and count!
     NEVER query 'settlement_records' or 'internal_transactions' alone because raw transaction ledgers do NOT have reasons!
   - EXACT JOIN PATTERN FOR MERCHANT EXCEPTIONS:
     SELECT e.category, e.reason, COUNT(*) as count, SUM(e.exposure_paise) as total_exposure_paise
     FROM exceptions e
     LEFT JOIN internal_transactions it ON e.record_id = it.id AND e.run_id = it.run_id
     LEFT JOIN settlement_records sr ON e.record_id = sr.id AND e.run_id = sr.run_id
     LEFT JOIN bank_statements bs ON e.record_id = bs.id AND e.run_id = bs.run_id
     WHERE e.run_id = '%s'
       AND COALESCE(it.merchant_id, sr.merchant_id, bs.merchant_id) = '<MERCH_ID>'
     GROUP BY e.category, e.reason
     ORDER BY count DESC, total_exposure_paise DESC LIMIT 50;
2. Merchant Filtering:
   - 'merchant_id' is stored on internal_transactions, settlement_records, and bank_statements.
   - It is NOT a direct column on 'exceptions'. Use the join pattern above.
   - NEVER use bare names like 'zomato' or 'swiggy' as merchant_id! Use the exact ID from ACTIVE MERCHANTS (e.g. 'MERCH_ZOMATO_DEL') or use 'merchant_id ILIKE '%%zomato%%''.
3. Record IDs: 'INT...', 'SET...', 'BNK...' are RECORD IDs (stored in exceptions.record_id or source table .id), NEVER merchant_id!
4. Pending settlements: "pending settlements" means category = 'SETTLED_NOT_BANKED'.
5. Always filter by run_id = '%s' when filtering table rows.
6. Never use CURRENT_DATE or relative date intervals (e.g. NOW() - INTERVAL '7 days'). Historical benchmark data has static dates.
7. If the question cannot be answered from the schema, respond with {"unsupported": true, "reason": "..."}.
8. Otherwise return raw JSON: {"sql": "SELECT ... LIMIT 50", "unsupported": false}. Output raw JSON ONLY.`,
		runID, merchantList.String(), entityHint, runID, runID)

	userPrompt := fmt.Sprintf("<user_question>\n%s\n</user_question>", question)

	reqBody := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": 0.0,
		"max_tokens":  350,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("NVIDIA NIM API returned status %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(respBytes, &chatResp); err != nil || len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("failed to parse NVIDIA NIM response")
	}

	rawContent := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	startIdx := strings.Index(rawContent, "{")
	endIdx := strings.LastIndex(rawContent, "}")
	if startIdx != -1 && endIdx != -1 && endIdx > startIdx {
		rawContent = rawContent[startIdx : endIdx+1]
	}

	var genResult SQLGenerationResult
	if err := json.Unmarshal([]byte(rawContent), &genResult); err != nil {
		return nil, fmt.Errorf("failed to parse generated SQL JSON from NIM: %w", err)
	}

	return &genResult, nil
}

func generateSQLOffline(question string, runID string, meta *DBMetadataContext) (*SQLGenerationResult, error) {
	lower := strings.ToLower(question)

	// Check if question specifies a record ID (INT..., SET..., BNK...)
	if match := recordIDRegex.FindString(question); match != "" {
		idUpper := strings.ToUpper(match)
		sql := fmt.Sprintf(`SELECT record_id, category, hop, reason, exposure_paise, ai_summary, ai_action, ai_confidence FROM exceptions WHERE run_id = '%s' AND record_id = '%s' LIMIT 1;`, runID, idUpper)
		return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
	}

	var merchants []MerchantInfo
	if meta != nil && len(meta.ActiveMerchants) > 0 {
		merchants = meta.ActiveMerchants
	} else {
		merchants = DefaultMerchants
	}
	resolvedMerchant := ResolveMerchant(question, merchants)

	hasReasonIntent := strings.Contains(lower, "reason") || strings.Contains(lower, "reson") ||
		strings.Contains(lower, "why") || strings.Contains(lower, "cause") ||
		strings.Contains(lower, "issue") || strings.Contains(lower, "problem") ||
		strings.Contains(lower, "discrepanc")

	// Merchant-specific queries
	if resolvedMerchant != nil {
		mid := resolvedMerchant.ID

		// 1. Failure reasons, causes, or issues for merchant settlements
		if hasReasonIntent {
			if strings.Contains(lower, "pending") || strings.Contains(lower, "not banked") {
				sql := fmt.Sprintf(`SELECT e.record_id, e.category, e.exposure_paise, e.reason, e.ai_summary, e.ai_action, sr.batch_id, sr.settlement_date FROM exceptions e JOIN settlement_records sr ON e.record_id = sr.id AND e.run_id = sr.run_id WHERE e.run_id = '%s' AND e.category = 'SETTLED_NOT_BANKED' AND sr.merchant_id = '%s' LIMIT 50;`, runID, mid)
				return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
			}
			sql := fmt.Sprintf(`SELECT e.category, e.reason, COUNT(*) AS count, SUM(e.exposure_paise) AS total_exposure_paise FROM exceptions e LEFT JOIN internal_transactions it ON e.record_id = it.id AND e.run_id = it.run_id LEFT JOIN settlement_records sr ON e.record_id = sr.id AND e.run_id = sr.run_id LEFT JOIN bank_statements bs ON e.record_id = bs.id AND e.run_id = bs.run_id WHERE e.run_id = '%s' AND COALESCE(it.merchant_id, sr.merchant_id, bs.merchant_id) = '%s' GROUP BY e.category, e.reason ORDER BY count DESC, total_exposure_paise DESC LIMIT 50;`, runID, mid)
			return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
		}

		// 2. Pending settlements for merchant (settled but not banked)
		if strings.Contains(lower, "pending settlement") ||
			(strings.Contains(lower, "pending") && strings.Contains(lower, "settlement")) ||
			strings.Contains(lower, "settled not banked") ||
			strings.Contains(lower, "settled_not_banked") {
			sql := fmt.Sprintf(`SELECT e.record_id, e.category, e.exposure_paise, e.reason, e.ai_summary, e.ai_action, sr.batch_id, sr.settlement_date FROM exceptions e JOIN settlement_records sr ON e.record_id = sr.id AND e.run_id = sr.run_id WHERE e.run_id = '%s' AND e.category = 'SETTLED_NOT_BANKED' AND sr.merchant_id = '%s' LIMIT 50;`, runID, mid)
			return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
		}

		// 3. Duplicate settlements for merchant
		if strings.Contains(lower, "duplicate") {
			sql := fmt.Sprintf(`SELECT e.record_id, e.category, e.exposure_paise, e.reason, e.ai_summary, e.ai_action FROM exceptions e JOIN settlement_records sr ON e.record_id = sr.id AND e.run_id = sr.run_id WHERE e.run_id = '%s' AND e.category = 'DUPLICATE_SETTLEMENT' AND sr.merchant_id = '%s' LIMIT 50;`, runID, mid)
			return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
		}

		// 4. Unresolved exposure / cash exposure for merchant
		if strings.Contains(lower, "exposure") || strings.Contains(lower, "unresolved") {
			sql := fmt.Sprintf(`SELECT COALESCE(it.merchant_id, sr.merchant_id, bs.merchant_id, 'UNKNOWN') AS merchant_id, COUNT(*) AS exception_count, SUM(e.exposure_paise) AS total_exposure_paise FROM exceptions e LEFT JOIN internal_transactions it ON e.record_id = it.id AND e.run_id = it.run_id LEFT JOIN settlement_records sr ON e.record_id = sr.id AND e.run_id = sr.run_id LEFT JOIN bank_statements bs ON e.record_id = bs.id AND e.run_id = bs.run_id WHERE e.run_id = '%s' AND COALESCE(it.merchant_id, sr.merchant_id, bs.merchant_id) = '%s' GROUP BY 1;`, runID, mid)
			return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
		}

		// 5. Raw settlements list for merchant (ONLY if explicitly asking to list/view/show, NOT reasons)
		isListQuery := strings.Contains(lower, "list") || strings.Contains(lower, "show") || strings.Contains(lower, "view") || strings.Contains(lower, "all settlements")
		if isListQuery {
			sql := fmt.Sprintf(`SELECT id, settled_amount_paise, currency, settlement_date, batch_id FROM settlement_records WHERE run_id = '%s' AND merchant_id = '%s' ORDER BY settlement_date DESC LIMIT 50;`, runID, mid)
			return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
		}

		// 6. General exceptions for merchant
		if strings.Contains(lower, "exception") || strings.Contains(lower, "how many") || strings.Contains(lower, "fail") {
			sql := fmt.Sprintf(`SELECT e.record_id, e.category, e.hop, e.reason, e.exposure_paise, e.ai_summary, e.ai_action FROM exceptions e LEFT JOIN internal_transactions it ON e.record_id = it.id AND e.run_id = it.run_id LEFT JOIN settlement_records sr ON e.record_id = sr.id AND e.run_id = sr.run_id LEFT JOIN bank_statements bs ON e.record_id = bs.id AND e.run_id = bs.run_id WHERE e.run_id = '%s' AND COALESCE(it.merchant_id, sr.merchant_id, bs.merchant_id) = '%s' LIMIT 50;`, runID, mid)
			return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
		}
	}

	// Question: Reasons / causes for exceptions (general)
	if hasReasonIntent {
		sql := fmt.Sprintf(`SELECT category, reason, COUNT(*) AS count, SUM(exposure_paise) AS total_exposure_paise FROM exceptions WHERE run_id = '%s' GROUP BY category, reason ORDER BY count DESC LIMIT 50;`, runID)
		return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
	}

	// Question: AI Investigation status / counts
	if strings.Contains(lower, "investigated by ai") || strings.Contains(lower, "ai investigated") || strings.Contains(lower, "ai status") || (strings.Contains(lower, "ai") && strings.Contains(lower, "investigat")) {
		sql := fmt.Sprintf(`SELECT ai_status, COUNT(*) AS count FROM exceptions WHERE run_id = '%s' AND ai_status != 'NOT_REQUIRED' GROUP BY ai_status ORDER BY count DESC;`, runID)
		return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
	}

	// Question: Merchant with most exceptions
	if strings.Contains(lower, "most unresolved") || strings.Contains(lower, "most exceptions") || strings.Contains(lower, "highest exception") {
		sql := fmt.Sprintf(`SELECT COALESCE(it.merchant_id, sr.merchant_id, bs.merchant_id, 'UNKNOWN') AS merchant_id, COUNT(*) AS exception_count, SUM(e.exposure_paise) AS total_exposure_paise FROM exceptions e LEFT JOIN internal_transactions it ON e.record_id = it.id AND e.run_id = it.run_id LEFT JOIN settlement_records sr ON e.record_id = sr.id AND e.run_id = sr.run_id LEFT JOIN bank_statements bs ON e.record_id = bs.id AND e.run_id = bs.run_id WHERE e.run_id = '%s' GROUP BY 1 ORDER BY exception_count DESC LIMIT 5;`, runID)
		return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
	}

	// Question: Largest unresolved cash exposure
	if strings.Contains(lower, "largest") && (strings.Contains(lower, "exposure") || strings.Contains(lower, "cash")) ||
		strings.Contains(lower, "highest exposure") {
		sql := fmt.Sprintf(`SELECT COALESCE(it.merchant_id, sr.merchant_id, bs.merchant_id, 'UNKNOWN') AS merchant_id, SUM(e.exposure_paise) AS total_exposure_paise, COUNT(*) AS exception_count FROM exceptions e LEFT JOIN internal_transactions it ON e.record_id = it.id AND e.run_id = it.run_id LEFT JOIN settlement_records sr ON e.record_id = sr.id AND e.run_id = sr.run_id LEFT JOIN bank_statements bs ON e.record_id = bs.id AND e.run_id = bs.run_id WHERE e.run_id = '%s' GROUP BY 1 ORDER BY total_exposure_paise DESC LIMIT 5;`, runID)
		return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
	}

	// Question: Total unresolved amount / cash exposure
	if strings.Contains(lower, "total unresolved") || strings.Contains(lower, "total cash exposure") || strings.Contains(lower, "total exposure") {
		sql := fmt.Sprintf(`SELECT unresolved_amount_paise, exception_count, full_chain_count, source_records_processed FROM reconciliation_runs WHERE run_id = '%s';`, runID)
		return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
	}

	// Question: Settled but not banked (general)
	if strings.Contains(lower, "settled but not banked") || strings.Contains(lower, "settled_not_banked") || strings.Contains(lower, "not banked") || strings.Contains(lower, "pending settlement") {
		sql := fmt.Sprintf(`SELECT COUNT(*) AS count, COALESCE(SUM(exposure_paise), 0) AS total_exposure_paise FROM exceptions WHERE run_id = '%s' AND category = 'SETTLED_NOT_BANKED';`, runID)
		return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
	}

	// Question: Banked but not settled
	if strings.Contains(lower, "banked not settled") || strings.Contains(lower, "banked_not_settled") || strings.Contains(lower, "unexplained bank") {
		sql := fmt.Sprintf(`SELECT record_id, expected_amount_paise, actual_amount_paise, exposure_paise, reason FROM exceptions WHERE run_id = '%s' AND category = 'BANKED_NOT_SETTLED' LIMIT 10;`, runID)
		return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
	}

	// Question: Reconciliation status counts (FULL, PARTIAL, UNMATCHED)
	if strings.Contains(lower, "reconciliation status") || strings.Contains(lower, "full chain") || strings.Contains(lower, "full match") || strings.Contains(lower, "unmatched") || strings.Contains(lower, "partial") || (strings.Contains(lower, "status") && !strings.Contains(lower, "ai status")) {
		sql := fmt.Sprintf(`SELECT reconciliation_status, COUNT(*) AS count FROM reconciliation_matches WHERE run_id = '%s' GROUP BY reconciliation_status ORDER BY count DESC;`, runID)
		return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
	}

	// Question: Category breakdown
	if strings.Contains(lower, "category") || strings.Contains(lower, "breakdown") || strings.Contains(lower, "taxonomy") {
		sql := fmt.Sprintf(`SELECT category, hop, COUNT(*) AS count, SUM(exposure_paise) AS total_exposure_paise FROM exceptions WHERE run_id = '%s' GROUP BY category, hop ORDER BY count DESC;`, runID)
		return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
	}

	// Question: Hop 1 vs Hop 2 / match summary
	if strings.Contains(lower, "hop 1") || strings.Contains(lower, "hop 2") || strings.Contains(lower, "hop1") || strings.Contains(lower, "hop2") || strings.Contains(lower, "summary") {
		sql := fmt.Sprintf(`SELECT hop1_matched_count, hop2_matched_count, full_chain_count, exception_count, throughput_records_per_sec FROM reconciliation_runs WHERE run_id = '%s';`, runID)
		return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
	}

	// Question: Batches / settlement batches
	if strings.Contains(lower, "batch") {
		sql := fmt.Sprintf(`SELECT batch_id, COUNT(*) AS settlement_count, SUM(settled_amount_paise) AS total_settled_paise FROM settlement_records WHERE run_id = '%s' GROUP BY batch_id ORDER BY total_settled_paise DESC LIMIT 10;`, runID)
		return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
	}

	// Generic Unsupported Filter
	unsupportedPhrases := []string{"weather", "poem", "joke", "who are you", "president", "capital of", "recipe"}
	for _, phrase := range unsupportedPhrases {
		if strings.Contains(lower, phrase) {
			return &SQLGenerationResult{
				Unsupported: true,
				Reason:      "Question cannot be answered from financial reconciliation data.",
			}, nil
		}
	}

	// Default to top exceptions summary if vague but financial
	if strings.Contains(lower, "exception") || strings.Contains(lower, "issue") || strings.Contains(lower, "fail") {
		sql := fmt.Sprintf(`SELECT category, count(*) as count, sum(exposure_paise) as exposure_paise FROM exceptions WHERE run_id = '%s' GROUP BY category ORDER BY count DESC LIMIT 5;`, runID)
		return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
	}

	return &SQLGenerationResult{
		Unsupported: true,
		Reason:      "Question is outside the scope of reconciliation records. Try asking about merchants, exceptions, failure reasons, exposure, or match counts.",
	}, nil
}

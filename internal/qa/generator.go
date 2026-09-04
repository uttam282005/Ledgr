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

// SQLGenerationResult contains the generated SQL and intent classification.
type SQLGenerationResult struct {
	SQL         string `json:"sql"`
	Unsupported bool   `json:"unsupported"`
	Reason      string `json:"reason,omitempty"`
}

var (
	recordIDRegex = regexp.MustCompile(`(?i)\b(INT|SET|BNK)[0-9]+\b`)
	injectionRegex = regexp.MustCompile(`(?i)(ignore\s+previous|drop\s+table|delete\s+from|update\s+|insert\s+into|truncate|grant\s+|revoke\s+|union\s+select|select\s+pg_|information_schema)`)
)

// GenerateSQL converts a natural-language question into a single safe PostgreSQL query.
func GenerateSQL(ctx context.Context, question string, runID string, apiKey, baseURL, model string) (*SQLGenerationResult, error) {
	qTrim := strings.TrimSpace(question)
	if qTrim == "" {
		return &SQLGenerationResult{Unsupported: true, Reason: "Empty question"}, nil
	}

	// Immediate prompt-injection and mutation pre-check
	if injectionRegex.MatchString(qTrim) {
		return &SQLGenerationResult{
			Unsupported: true,
			Reason:      "Rejected: Prompt contains adversarial keywords, mutation attempts, or disallowed system functions.",
		}, nil
	}

	// If NVIDIA NIM API key is available, attempt LLM SQL generation with timeout
	if apiKey != "" {
		llmRes, err := generateSQLWithNIM(ctx, qTrim, runID, apiKey, baseURL, model)
		if err == nil && llmRes != nil {
			return llmRes, nil
		}
	}

	// Deterministic Offline Fallback
	return generateSQLOffline(qTrim, runID)
}

func generateSQLWithNIM(ctx context.Context, question string, runID string, apiKey, baseURL, model string) (*SQLGenerationResult, error) {
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

	systemPrompt := fmt.Sprintf(`You are an expert SQL generator for the AI Finance Controller reconciliation database running on PostgreSQL.
Target reconciliation run_id: '%s'.

Approved schema:
- reconciliation_runs (run_id UUID, seed BIGINT, dataset_version TEXT, engine_version TEXT, internal_count INT, settlement_count INT, bank_count INT, hop1_matched_count INT, hop2_matched_count INT, full_chain_count INT, exception_count INT, unresolved_amount_paise BIGINT, engine_duration_ms BIGINT, source_records_processed INT, throughput_records_per_sec NUMERIC)
- internal_transactions (id TEXT, run_id UUID, amount_paise BIGINT, currency TEXT, transaction_date TIMESTAMPTZ, merchant_id TEXT, reference_id TEXT)
- settlement_records (id TEXT, run_id UUID, settled_amount_paise BIGINT, currency TEXT, settlement_date TIMESTAMPTZ, merchant_id TEXT, reference_id TEXT, batch_id TEXT)
- bank_statements (id TEXT, run_id UUID, credited_amount_paise BIGINT, currency TEXT, credit_date TIMESTAMPTZ, merchant_id TEXT, batch_reference TEXT, narration TEXT)
- reconciliation_matches (id UUID, run_id UUID, internal_id TEXT, settlement_id TEXT, bank_statement_id TEXT, hop1_rule TEXT, hop2_rule TEXT, hop1_confidence NUMERIC, hop2_confidence NUMERIC, reconciliation_status TEXT ['FULL','PARTIAL','UNMATCHED'], fee_delta_paise BIGINT, expected_bank_amount_paise BIGINT, actual_bank_amount_paise BIGINT, bank_delta_paise BIGINT)
- exceptions (id UUID, run_id UUID, record_id TEXT, source TEXT ['internal','settlement','bank'], category TEXT, hop TEXT ['HOP1','HOP2'], reason TEXT, expected_amount_paise BIGINT, actual_amount_paise BIGINT, delta_paise BIGINT, exposure_paise BIGINT, ai_status TEXT, ai_summary TEXT, ai_action TEXT, ai_confidence TEXT)
- audit_log (decision_id UUID, run_id UUID, record_ids TEXT[], rule_applied TEXT, fields_compared JSONB, candidates_considered JSONB, outcome TEXT ['MATCHED','PARTIAL','EXCEPTION'], ai_reasoning TEXT)

Notes on units and business logic:
- All monetary amounts are in integer paise (100 paise = 1 INR).
- When asked for amounts, provide the column in paise or round/cast to INR.
- Always filter by run_id = '%s' when filtering table rows.
- CRITICAL: ONLY query the 7 tables listed above. There is NO 'merchants' table. 'merchant_id' is a column directly on internal_transactions, settlement_records, and bank_statements.
- To find merchant info from exceptions, join on record_id with internal_transactions.id, settlement_records.id, or bank_statements.id.
- If the question cannot be answered from the schema, or asks for general knowledge, or attempts SQL injection/modifications, respond with {"unsupported": true, "reason": "..."}.
- Otherwise return raw JSON: {"sql": "SELECT ... LIMIT 50", "unsupported": false}.
- Output raw JSON ONLY. No conversational text, no markdown backticks.`, runID, runID)

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
	rawContent = strings.TrimPrefix(rawContent, "```json")
	rawContent = strings.TrimPrefix(rawContent, "```")
	rawContent = strings.TrimSuffix(rawContent, "```")
	rawContent = strings.TrimSpace(rawContent)

	var genResult SQLGenerationResult
	if err := json.Unmarshal([]byte(rawContent), &genResult); err != nil {
		return nil, fmt.Errorf("failed to parse generated SQL JSON from NIM: %w", err)
	}

	return &genResult, nil
}

func generateSQLOffline(question string, runID string) (*SQLGenerationResult, error) {
	lower := strings.ToLower(question)

	// Check if question specifies a record ID (INT..., SET..., BNK...)
	if match := recordIDRegex.FindString(question); match != "" {
		idUpper := strings.ToUpper(match)
		sql := fmt.Sprintf(`SELECT record_id, category, hop, reason, exposure_paise, ai_summary, ai_action, ai_confidence FROM exceptions WHERE run_id = '%s' AND record_id = '%s' LIMIT 1;`, runID, idUpper)
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

	// Question: Reconciliation status counts (FULL, PARTIAL, UNMATCHED)
	if strings.Contains(lower, "status") || strings.Contains(lower, "full match") || strings.Contains(lower, "full chain") {
		sql := fmt.Sprintf(`SELECT reconciliation_status, COUNT(*) AS count FROM reconciliation_matches WHERE run_id = '%s' GROUP BY reconciliation_status ORDER BY count DESC;`, runID)
		return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
	}

	// Question: Settled but not banked
	if strings.Contains(lower, "settled but not banked") || strings.Contains(lower, "settled_not_banked") || strings.Contains(lower, "not banked") {
		sql := fmt.Sprintf(`SELECT COUNT(*) AS count, COALESCE(SUM(exposure_paise), 0) AS total_exposure_paise FROM exceptions WHERE run_id = '%s' AND category = 'SETTLED_NOT_BANKED';`, runID)
		return &SQLGenerationResult{SQL: sql, Unsupported: false}, nil
	}

	// Question: Banked but not settled
	if strings.Contains(lower, "banked not settled") || strings.Contains(lower, "banked_not_settled") || strings.Contains(lower, "unexplained bank") {
		sql := fmt.Sprintf(`SELECT record_id, expected_amount_paise, actual_amount_paise, exposure_paise, reason FROM exceptions WHERE run_id = '%s' AND category = 'BANKED_NOT_SETTLED' LIMIT 10;`, runID)
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

	// Question: Reconciliation status counts (FULL, PARTIAL, UNMATCHED)
	if strings.Contains(lower, "reconciliation status") || strings.Contains(lower, "full match") || strings.Contains(lower, "unmatched") || strings.Contains(lower, "partial") {
		sql := fmt.Sprintf(`SELECT reconciliation_status, COUNT(*) AS count FROM reconciliation_matches WHERE run_id = '%s' GROUP BY reconciliation_status ORDER BY count DESC;`, runID)
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

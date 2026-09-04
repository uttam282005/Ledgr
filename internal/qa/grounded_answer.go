package qa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GenerateGroundedAnswer creates a strictly grounded natural-language answer
// from the executed SQL result rows, eliminating hallucinations.
func GenerateGroundedAnswer(ctx context.Context, question string, sql string, rows []map[string]interface{}, apiKey, baseURL, model string) string {
	if len(rows) == 0 {
		return "No matching records found in the reconciliation database for this query."
	}

	// If NVIDIA NIM API key is available, attempt LLM grounded synthesis
	if apiKey != "" {
		ans, err := generateGroundedAnswerWithNIM(ctx, question, sql, rows, apiKey, baseURL, model)
		if err == nil && ans != "" {
			return ans
		}
	}

	// Deterministic Offline Grounded Answer
	return generateGroundedAnswerOffline(question, rows)
}

func generateGroundedAnswerWithNIM(ctx context.Context, question string, sql string, rows []map[string]interface{}, apiKey, baseURL, model string) (string, error) {
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

	systemPrompt := `You are an expert financial controller reporting reconciliation truth.
You are given a user question, the exact read-only SQL executed, and the exact database result rows.
Rules:
1. Answer strictly from the provided result rows. Never hallucinate or invent numbers.
2. All monetary amounts have been accurately pre-converted to Indian Rupees in the '*_inr' fields (e.g. 'total_exposure_inr': '₹3,53,818.75'). ALWAYS quote these pre-calculated strings in your answer. Never attempt manual mental math or division on paise values.
3. When the results explain reasons, discrepancies, or failure causes (containing 'reason' columns), explain the underlying financial/operational reasons for each category (e.g., missing bank credit statements, fee delta exceeding 2.5%, duplicate candidates, or unlinked gateway records) along with counts and exposure amounts.
4. Be concise, direct, and structured. Bullet points are encouraged when listing multiple reasons or breakdown categories.
5. Do not speculate or extrapolate beyond the provided data.`

	// Enrich rows with pre-calculated formatted Indian Rupee strings
	enrichedRows := make([]map[string]interface{}, len(rows))
	for i, r := range rows {
		er := make(map[string]interface{})
		for k, v := range r {
			er[k] = v
			if strings.Contains(k, "paise") || strings.Contains(k, "amount") {
				inrKey := strings.TrimSuffix(k, "_paise") + "_inr"
				er[inrKey] = formatPaise(v)
			}
		}
		enrichedRows[i] = er
	}

	payload := map[string]interface{}{
		"question": question,
		"sql":      sql,
		"rows":     enrichedRows,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	reqBody := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": fmt.Sprintf("<reconciliation_context>\n%s\n</reconciliation_context>", string(payloadBytes))},
		},
		"temperature": 0.1,
		"max_tokens":  250,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("NVIDIA NIM status %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(respBytes, &chatResp); err != nil || len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("failed to parse NVIDIA NIM response")
	}

	return strings.TrimSpace(chatResp.Choices[0].Message.Content), nil
}

func generateGroundedAnswerOffline(question string, rows []map[string]interface{}) string {
	if len(rows) == 0 {
		return "No matching records found in the reconciliation database."
	}

	firstRow := rows[0]

	// Case 1: Specific record inspection or multi-record exception list (record_id, category, reason)
	if recordID, ok := firstRow["record_id"].(string); ok {
		if len(rows) == 1 {
			cat, _ := firstRow["category"].(string)
			hop, _ := firstRow["hop"].(string)
			reason, _ := firstRow["reason"].(string)
			exposure := formatPaise(firstRow["exposure_paise"])
			summary, _ := firstRow["ai_summary"].(string)
			action, _ := firstRow["ai_action"].(string)

			ans := fmt.Sprintf("Record %s was classified as %s (%s). Reason: %s. Exposure: %s.",
				recordID, cat, hop, reason, exposure)
			if summary != "" {
				ans += fmt.Sprintf(" AI Diagnosis: %s", summary)
			}
			if action != "" {
				ans += fmt.Sprintf(" Recommended action: %s", action)
			}
			return ans
		}

		// Multiple exception records (e.g. pending settlements for a merchant)
		cat, _ := firstRow["category"].(string)
		var totalExp int64
		var ids []string
		for _, r := range rows {
			if idStr, ok := r["record_id"].(string); ok {
				ids = append(ids, idStr)
			}
			if exp, ok := r["exposure_paise"].(int64); ok {
				totalExp += exp
			} else if expF, ok := r["exposure_paise"].(float64); ok {
				totalExp += int64(expF)
			}
		}

		label := cat
		if cat == "SETTLED_NOT_BANKED" {
			label = "pending settlement(s) (settled but not banked)"
		} else if cat != "" {
			label = fmt.Sprintf("%s record(s)", cat)
		} else {
			label = "exception record(s)"
		}

		idList := strings.Join(ids, ", ")
		if len(ids) > 6 {
			idList = fmt.Sprintf("%s, and %d more", strings.Join(ids[:6], ", "), len(ids)-6)
		}

		return fmt.Sprintf("Found %d %s totaling %s in unresolved cash exposure. Impacted records: %s.",
			len(rows), label, formatPaise(totalExp), idList)
	}

	// Case 2: Reconciliation status counts (FULL, PARTIAL, UNMATCHED)
	if _, ok := firstRow["reconciliation_status"]; ok {
		var parts []string
		for _, r := range rows {
			st := r["reconciliation_status"]
			cnt := r["count"]
			if cnt == nil {
				cnt = r["full_chain_count"]
			}
			parts = append(parts, fmt.Sprintf("%v: %v records", st, cnt))
		}
		return fmt.Sprintf("Full-chain reconciliation breakdown: %s.", strings.Join(parts, ", "))
	}

	// Case 3: AI investigation status counts
	if _, ok := firstRow["ai_status"]; ok {
		var parts []string
		for _, r := range rows {
			st := r["ai_status"]
			cnt := r["count"]
			parts = append(parts, fmt.Sprintf("%v: %v", st, cnt))
		}
		return fmt.Sprintf("AI investigation status across exceptions: %s.", strings.Join(parts, ", "))
	}

	// Case 4: Category and Reason breakdown
	if _, ok := firstRow["category"]; ok {
		if _, hasReason := firstRow["reason"]; hasReason {
			var parts []string
			for i, r := range rows {
				if i >= 6 {
					parts = append(parts, fmt.Sprintf("...and %d more reasons", len(rows)-6))
					break
				}
				c := r["category"]
				cnt := r["count"]
				rsn, _ := r["reason"].(string)
				expStr := ""
				if r["total_exposure_paise"] != nil {
					expStr = fmt.Sprintf(" (%s)", formatPaise(r["total_exposure_paise"]))
				} else if r["exposure_paise"] != nil {
					expStr = fmt.Sprintf(" (%s)", formatPaise(r["exposure_paise"]))
				}
				if cnt != nil {
					parts = append(parts, fmt.Sprintf("%v [%v records]: %s%s", c, cnt, rsn, expStr))
				} else {
					parts = append(parts, fmt.Sprintf("%v: %s%s", c, rsn, expStr))
				}
			}
			return fmt.Sprintf("Primary reasons for settlement exceptions: %s.", strings.Join(parts, "; "))
		}

		var parts []string
		for _, r := range rows {
			c := r["category"]
			cnt := r["count"]
			if r["total_exposure_paise"] != nil {
				exp := formatPaise(r["total_exposure_paise"])
				parts = append(parts, fmt.Sprintf("%v: %v records (%s)", c, cnt, exp))
			} else {
				parts = append(parts, fmt.Sprintf("%v: %v records", c, cnt))
			}
		}
		return fmt.Sprintf("Exception breakdown by category: %s.", strings.Join(parts, "; "))
	}

	// Case 5: Merchant aggregation
	if merchantID, ok := firstRow["merchant_id"].(string); ok {
		var b strings.Builder
		countVal, hasCount := firstRow["exception_count"]
		if !hasCount {
			countVal = firstRow["count"]
		}
		exposureVal := firstRow["total_exposure_paise"]

		b.WriteString(fmt.Sprintf("Top merchant is %s with %v unresolved exception(s)", merchantID, countVal))
		if exposureVal != nil {
			b.WriteString(fmt.Sprintf(" and total unresolved cash exposure of %s.", formatPaise(exposureVal)))
		} else {
			b.WriteString(".")
		}

		if len(rows) > 1 {
			b.WriteString(" Additional top merchants: ")
			var others []string
			for i := 1; i < len(rows) && i < 4; i++ {
				m := rows[i]
				mID, _ := m["merchant_id"].(string)
				c := m["exception_count"]
				if c == nil {
					c = m["count"]
				}
				others = append(others, fmt.Sprintf("%s (%v exceptions)", mID, c))
			}
			b.WriteString(strings.Join(others, ", ") + ".")
		}
		return b.String()
	}

	// Case 6: Reconciliation run totals (unresolved_amount_paise, exception_count, full_chain_count)
	if unres, ok := firstRow["unresolved_amount_paise"]; ok {
		exCount := firstRow["exception_count"]
		fullCount := firstRow["full_chain_count"]
		return fmt.Sprintf("Total unresolved cash exposure is %s across %v unresolved exception(s). Fully reconciled chain count is %v.",
			formatPaise(unres), exCount, fullCount)
	}

	// Case 7: Single count / sum result
	var parts []string
	for k, v := range firstRow {
		if strings.Contains(k, "paise") || strings.Contains(k, "amount") {
			parts = append(parts, fmt.Sprintf("%s = %s", k, formatPaise(v)))
		} else {
			parts = append(parts, fmt.Sprintf("%s = %v", k, v))
		}
	}
	return fmt.Sprintf("Reconciliation database returned: %s.", strings.Join(parts, ", "))
}

func formatPaise(v interface{}) string {
	if v == nil {
		return "₹0.00"
	}
	var paise int64
	switch val := v.(type) {
	case int64:
		paise = val
	case int:
		paise = int64(val)
	case float64:
		paise = int64(val)
	case string:
		var parsed int64
		fmt.Sscanf(val, "%d", &parsed)
		paise = parsed
	default:
		return fmt.Sprintf("%v paise", v)
	}

	rupees := float64(paise) / 100.0
	return fmt.Sprintf("₹%.2f", rupees)
}

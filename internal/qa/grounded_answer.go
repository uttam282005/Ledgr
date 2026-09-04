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
2. Present monetary amounts clearly in Indian Rupees (e.g., divide paise values by 100).
3. Be concise, direct, and professional (2-4 sentences max).
4. Do not speculate or extrapolate beyond the provided data.`

	payload := map[string]interface{}{
		"question": question,
		"sql":      sql,
		"rows":     rows,
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

	// Case 1: Specific record inspection (record_id, category, reason)
	if recordID, ok := firstRow["record_id"].(string); ok {
		cat, _ := firstRow["category"].(string)
		hop, _ := firstRow["hop"].(string)
		reason, _ := firstRow["reason"].(string)
		exposure := formatPaise(firstRow["exposure_paise"])
		summary, _ := firstRow["ai_summary"].(string)
		action, _ := firstRow["ai_action"].(string)

		ans := fmt.Sprintf("Record %s was classified as %s (%s). Reason: %s. Unresolved cash exposure: %s.",
			recordID, cat, hop, reason, exposure)
		if action != "" {
			ans += fmt.Sprintf(" Recommended action: %s", action)
		} else if summary != "" {
			ans += fmt.Sprintf(" Details: %s", summary)
		}
		return ans
	}

	// Case 2: Merchant aggregation
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

	// Case 3: Reconciliation run totals (unresolved_amount_paise, exception_count, full_chain_count)
	if unres, ok := firstRow["unresolved_amount_paise"]; ok {
		exCount := firstRow["exception_count"]
		fullCount := firstRow["full_chain_count"]
		return fmt.Sprintf("Total unresolved cash exposure is %s across %v unresolved exception(s). Fully reconciled chain count is %v.",
			formatPaise(unres), exCount, fullCount)
	}

	// Case 4: Status or category breakdown
	if cat, ok := firstRow["category"].(string); ok {
		cnt := firstRow["count"]
		exposure := formatPaise(firstRow["total_exposure_paise"])
		return fmt.Sprintf("Top exception category is %s with %v record(s) and %s in unresolved cash exposure.",
			cat, cnt, exposure)
	}

	// Case 5: Single count / sum result
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

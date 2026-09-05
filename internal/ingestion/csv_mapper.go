package ingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reUnix10    = regexp.MustCompile(`^\d{9,11}$`)
	reUnix13    = regexp.MustCompile(`^\d{12,14}$`)
	reUnixMicro = regexp.MustCompile(`^\d{15,17}$`)
	reUnixNano  = regexp.MustCompile(`^\d{18,20}$`)
	reUnixFloat = regexp.MustCompile(`^\d{9,14}\.\d+$`)
)

type AIMapperResponse struct {
	Mapping        map[string]*string `json:"mapping"`
	Confidence     map[string]string  `json:"confidence"`
	DateFormat     string             `json:"date_format"`
	ComputedFields []string           `json:"computed_fields"`
	Notes          string             `json:"notes"`
}

// InferColumnMapping uses NVIDIA NIM API with intelligent heuristic fallback to map
// uploaded CSV columns to canonical schema fields.
func InferColumnMapping(
	ctx context.Context,
	sourceType string,
	headers []string,
	sampleRows [][]string,
	apiKey string,
	baseURL string,
	model string,
) (*MappingResult, error) {
	fields, ok := CanonicalFields[sourceType]
	if !ok {
		return nil, fmt.Errorf("unknown source type '%s'", sourceType)
	}

	result := &MappingResult{
		SourceType:    sourceType,
		Headers:       headers,
		SampleRows:    sampleRows,
		Mapping:       make(map[string]string),
		Confidence:    make(map[string]string),
		ComputedFields: []string{},
		Warnings:      []string{},
		CanonicalDefs: fields,
	}

	// Try online NVIDIA NIM API if key is present
	var aiErr error
	if apiKey != "" {
		aiResp, err := callNvidiaNIMMapper(ctx, sourceType, fields, headers, sampleRows, apiKey, baseURL, model)
		if err == nil && aiResp != nil {
			// Populate from AI response
			for k, v := range aiResp.Mapping {
				if v != nil && *v != "" {
					result.Mapping[k] = *v
				} else {
					result.Mapping[k] = ""
				}
			}
			for k, c := range aiResp.Confidence {
				result.Confidence[k] = strings.ToUpper(strings.TrimSpace(c))
			}
			result.DateFormat = aiResp.DateFormat
			result.ComputedFields = aiResp.ComputedFields
			result.Notes = fmt.Sprintf("AI Mapped via NVIDIA NIM (%s): %s", model, aiResp.Notes)
		} else {
			aiErr = err
		}
	}

	// If AI didn't run or failed, run heuristic mapper
	if len(result.Mapping) == 0 {
		hMapping, hConf, hDate, hNotes := heuristicMapper(sourceType, fields, headers, sampleRows)
		result.Mapping = hMapping
		result.Confidence = hConf
		result.DateFormat = hDate
		result.Notes = hNotes
		if aiErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("NVIDIA NIM API failed (%v); fell back to deterministic heuristic mapper", aiErr))
		}
	}

	// Ensure all canonical fields are present in maps
	for _, f := range fields {
		if mappedCol, exists := result.Mapping[f.Name]; !exists || mappedCol == "" {
			result.Mapping[f.Name] = ""
			result.Confidence[f.Name] = "UNMAPPED"
			if f.Name == "batch_id" && sourceType == SourceSettlement {
				result.Warnings = append(result.Warnings, "Canonical field 'batch_id' is unmapped: Hop 2 batch matching will be degraded but Hop 1 will proceed")
			}
		} else {
			// Validate mapped column exists in headers
			found := false
			for _, h := range headers {
				if h == mappedCol {
					found = true
					break
				}
			}
			if !found {
				result.Mapping[f.Name] = ""
				result.Confidence[f.Name] = "UNMAPPED"
			}
		}
	}

	if result.DateFormat == "" {
		dateColIdx := getDateColumnIndex(sourceType, headers, result.Mapping)
		result.DateFormat = detectDateFormatForColumn(sampleRows, dateColIdx)
	}

	return result, nil
}

// callNvidiaNIMMapper calls NVIDIA NIM chat completions endpoint with structured JSON instructions.
func callNvidiaNIMMapper(
	ctx context.Context,
	sourceType string,
	fields []FieldDef,
	headers []string,
	sampleRows [][]string,
	apiKey string,
	baseURL string,
	model string,
) (*AIMapperResponse, error) {
	if baseURL == "" {
		baseURL = "https://integrate.api.nvidia.com/v1"
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	if !strings.HasSuffix(baseURL, "/chat/completions") {
		baseURL = baseURL + "/chat/completions"
	}

	// Build field definitions text
	var fieldsText strings.Builder
	for _, f := range fields {
		reqStr := "Optional"
		if f.Required {
			reqStr = "Required"
		}
		fieldsText.WriteString(fmt.Sprintf("- %s (%s): %s\n", f.Name, reqStr, f.Description))
	}

	// Build sample rows text
	var rowsText strings.Builder
	for i, r := range sampleRows {
		rowsText.WriteString(fmt.Sprintf("Row %d: %s\n", i+1, strings.Join(r, ", ")))
	}

	systemPrompt := `You are a strict data ingestion assistant for a financial payment reconciliation system.
Your job is to map arbitrary user CSV headers to canonical schema fields based on headers and sample values.
You MUST respond with valid JSON ONLY. Do not wrap in markdown tags.`

	userPrompt := fmt.Sprintf(`Target schema: %s
Canonical fields and their meaning:
%s
The user uploaded a CSV with these headers and sample rows:
Headers: %s
%s
Return ONLY a JSON object mapping each canonical field to the user's exact CSV column name, or null if no plausible match exists.
Also return a "confidence" object mapping each canonical field to "HIGH", "MEDIUM", or "LOW".
Also return a "date_format" field with the detected date format string (e.g. "DD/MM/YYYY", "YYYY-MM-DD", "RFC3339", "Unix timestamp").
Also return a "computed_fields" array listing any canonical fields that must be computed from multiple CSV columns (e.g. settled_amount = gross_amount - fee_amount).
Also return a "notes" field explaining key inferences.

Example response format:
{
  "mapping": {
    "id": "transaction_id",
    "amount": "gross_amount",
    "transaction_date": "created_at",
    "merchant_id": "mid",
    "currency": null,
    "reference_id": null
  },
  "confidence": {
    "id": "HIGH",
    "amount": "HIGH",
    "transaction_date": "HIGH",
    "merchant_id": "MEDIUM",
    "currency": "LOW",
    "reference_id": "LOW"
  },
  "date_format": "DD/MM/YYYY",
  "computed_fields": [],
  "notes": "Mapped amount from gross_amount and date from created_at"
}`, sourceType, fieldsText.String(), strings.Join(headers, ", "), rowsText.String())

	reqBody := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": 0.1,
		"max_tokens":  1024,
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed marshaling NIM request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL, bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("failed creating HTTP request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("NVIDIA NIM API call failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading NIM response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("NVIDIA NIM API returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return nil, fmt.Errorf("failed parsing NIM JSON: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("empty choices from NIM API")
	}

	content := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var aiResult AIMapperResponse
	if err := json.Unmarshal([]byte(content), &aiResult); err != nil {
		return nil, fmt.Errorf("failed unmarshaling AI mapping response: %w (content: %s)", err, content)
	}

	return &aiResult, nil
}

func isRowCounterHeader(h string) bool {
	clean := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(h), "_", ""), " ", ""), ".", ""))
	return clean == "slno" || clean == "sno" || clean == "srno" || clean == "rowno" || clean == "rownum" || clean == "serialno" || clean == "serial"
}

// heuristicMapper provides accurate deterministic fallback matching using domain synonyms.
func heuristicMapper(
	sourceType string,
	fields []FieldDef,
	headers []string,
	sampleRows [][]string,
) (map[string]string, map[string]string, string, string) {
	mapping := make(map[string]string)
	confidence := make(map[string]string)

	synonyms := map[string][]string{
		"id":                  {"id", "txn_id", "transaction_id", "internal_id", "record_id", "settlement_id", "settlement_ref", "settlementref", "settle_ref", "bank_id", "order_id", "payment_id", "ref_no", "entry_id", "line_id", "line", "stmt_line", "statement_line", "bank_statement_line", "txn_ref_id", "txn_ref"},
		"amount":              {"amount", "gross_amount", "amt", "total", "order_amount", "gross_amt", "value", "txn_amount", "paid_amount", "gross_paid_amount"},
		"settled_amount":      {"settled_amount", "net_amount", "payout_amount", "settled", "net_amt", "settlement_amount", "amount", "paid_out", "payout", "net_payout", "payout_inr", "net_payout_inr"},
		"credited_amount":     {"credited_amount", "deposit", "credit", "credit_amount", "amount", "cr_amount", "txn_amount", "deposit_amount", "credit_amount_deposit"},
		"transaction_date":    {"transaction_date", "tx_date", "date", "created_at", "timestamp", "time", "order_date", "tx_time", "txn_timestamp"},
		"settlement_date":     {"settlement_date", "settle_date", "date", "timestamp", "settled_at", "payout_date", "clearing_date", "settlement_clearing_date"},
		"credit_date":         {"credit_date", "value_date", "posting_date", "date", "txn_date", "timestamp", "bank_date"},
		"merchant_id":         {"merchant_id", "merchant", "mid", "store_id", "client_id", "account", "sub_merchant", "vendor_id", "merchant_code", "vendor_mid", "client_account"},
		"batch_id":            {"batch_id", "batch", "batch_reference", "batch_no", "batchno", "payout_id", "file_id", "batch_file_id", "settlement_batch_ref"},
		"batch_reference":     {"batch_reference", "batch_id", "batch", "utr", "payout_id", "reference", "batch_no", "batchno", "settlement_batch_ref", "batch_ref"},
		"reference_id":        {"reference_id", "ref_id", "reference", "gateway_ref", "order_ref", "ext_ref", "parent_id", "payment_reference", "gateway_order_ref", "payment_order_reference", "order_reference"},
		"currency":            {"currency", "curr", "ccy"},
		"narration":           {"narration", "description", "remarks", "particulars", "note", "narrative", "bank_particulars"},
	}

	usedHeaders := make(map[string]bool)

	// Step 1: Exact matches
	for _, f := range fields {
		for _, h := range headers {
			if f.Name == "id" && isRowCounterHeader(h) {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(h), f.Name) && !usedHeaders[h] {
				mapping[f.Name] = h
				confidence[f.Name] = "HIGH"
				usedHeaders[h] = true
				break
			}
		}
	}

	// Step 2: Synonym matches
	for _, f := range fields {
		if mapping[f.Name] != "" {
			continue
		}
		syns := synonyms[f.Name]
		for _, syn := range syns {
			matched := false
			for _, h := range headers {
				if f.Name == "id" && isRowCounterHeader(h) {
					continue
				}
				cleanH := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(h), "_", ""), " ", ""))
				cleanSyn := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(syn, "_", ""), " ", ""))
				if cleanH == cleanSyn && !usedHeaders[h] {
					mapping[f.Name] = h
					confidence[f.Name] = "HIGH"
					usedHeaders[h] = true
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
	}

	// Step 2.5: Special handling for ID column if still unmapped (prioritize headers ending in 'id')
	if mapping["id"] == "" {
		for _, h := range headers {
			if isRowCounterHeader(h) {
				continue
			}
			cleanH := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(h), "_", ""), " ", ""))
			if strings.HasSuffix(cleanH, "id") && !usedHeaders[h] {
				mapping["id"] = h
				confidence["id"] = "HIGH"
				usedHeaders[h] = true
				break
			}
		}
	}

	// Step 3: Partial substring matches
	for _, f := range fields {
		if mapping[f.Name] != "" {
			continue
		}
		syns := synonyms[f.Name]
		for _, syn := range syns {
			matched := false
			for _, h := range headers {
				cleanH := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(h), "_", ""), " ", ""))
				cleanSyn := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(syn, "_", ""), " ", ""))
				if len(cleanSyn) >= 3 && (strings.Contains(cleanH, cleanSyn) || strings.Contains(cleanSyn, cleanH)) && !usedHeaders[h] {
					mapping[f.Name] = h
					confidence[f.Name] = "MEDIUM"
					usedHeaders[h] = true
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
	}

	dateColIdx := getDateColumnIndex(sourceType, headers, mapping)
	dateFormat := detectDateFormatForColumn(sampleRows, dateColIdx)
	notes := "Inferred via domain semantic heuristic mapper"

	return mapping, confidence, dateFormat, notes
}

// getDateColumnIndex identifies the column index of the canonical date field.
func getDateColumnIndex(sourceType string, headers []string, mapping map[string]string) int {
	dateFieldName := "transaction_date"
	switch sourceType {
	case SourceSettlement:
		dateFieldName = "settlement_date"
	case SourceBank:
		dateFieldName = "credit_date"
	}

	mappedCol := mapping[dateFieldName]
	if mappedCol != "" {
		for i, h := range headers {
			if strings.EqualFold(strings.TrimSpace(h), strings.TrimSpace(mappedCol)) {
				return i
			}
		}
	}

	// Fallback: look for common date header names
	for i, h := range headers {
		cleanH := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(h), "_", ""), " ", ""))
		if strings.Contains(cleanH, "date") || strings.Contains(cleanH, "timestamp") || strings.Contains(cleanH, "txntime") || strings.Contains(cleanH, "time") {
			return i
		}
	}

	return -1
}

// isUnixTimestamp checks if string matches integer or decimal Unix timestamp patterns.
func isUnixTimestamp(v string) bool {
	return reUnix10.MatchString(v) || reUnix13.MatchString(v) || reUnixMicro.MatchString(v) || reUnixNano.MatchString(v) || reUnixFloat.MatchString(v)
}

// detectDateFormat analyzes sample date values to infer date formatting across all columns (legacy/fallback).
func detectDateFormat(sampleRows [][]string) string {
	return detectDateFormatForColumn(sampleRows, -1)
}

// detectDateFormatForColumn analyzes sample date values specifically for the target date column.
func detectDateFormatForColumn(sampleRows [][]string, colIdx int) string {
	var candidates []string
	if colIdx >= 0 {
		for _, row := range sampleRows {
			if colIdx < len(row) {
				v := strings.TrimSpace(row[colIdx])
				if v != "" {
					candidates = append(candidates, v)
				}
			}
		}
	} else {
		for _, row := range sampleRows {
			for _, val := range row {
				v := strings.TrimSpace(val)
				if v != "" {
					candidates = append(candidates, v)
				}
			}
		}
	}

	for _, v := range candidates {
		// Unix timestamp
		if isUnixTimestamp(v) {
			// If scanning all columns without a target date column, verify range to avoid false positives (e.g. phone numbers or numeric IDs)
			if colIdx < 0 {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					if (f >= 1e8 && f <= 4.2e9) || (f >= 1e11 && f <= 4.2e12) {
						return "Unix timestamp"
					}
				}
				continue
			}
			return "Unix timestamp"
		}

		// RFC3339 / ISO 8601
		if strings.Contains(v, "T") && (strings.HasSuffix(v, "Z") || strings.Contains(v, "+") || strings.Contains(v, "-")) {
			return "RFC3339"
		}

		// Slash formats
		if strings.Contains(v, "/") {
			parts := strings.Split(v, "/")
			if len(parts) >= 3 {
				if len(parts[0]) == 4 {
					return "YYYY/MM/DD"
				}
				p0, _ := strconv.Atoi(parts[0])
				if p0 > 12 {
					return "DD/MM/YYYY"
				}
				p1, _ := strconv.Atoi(parts[1])
				if p1 > 12 {
					return "MM/DD/YYYY"
				}
				return "DD/MM/YYYY" // Default for Indian payment contexts
			}
		}

		// Dash formats
		if strings.Contains(v, "-") {
			parts := strings.Split(v, "-")
			if len(parts) >= 3 {
				if len(parts[0]) == 4 {
					return "YYYY-MM-DD"
				}
				if len(parts[2]) >= 2 {
					return "DD-MM-YYYY"
				}
			}
		}
	}

	return "YYYY-MM-DD"
}

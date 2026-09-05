package ingestion

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/razorpay-hack/ai-finance-controller/internal/models"
)

var (
	reCurrencyPrefix = regexp.MustCompile(`(?i)^(₹|\$|€|£|rs\.?|inr|usd)\s*`)
	reParenthesesNeg = regexp.MustCompile(`^\s*\((.*)\)\s*$`)
	reDigitsOnly     = regexp.MustCompile(`^\d+$`)
)

// ParseAmountString parses decimal or integer rupee/paise values with comprehensive edge-case handling.
// Supports:
// - Currency symbols: "₹ 1,250.50", "$500", "Rs. 99.00"
// - Commas: "1,250,000.50"
// - Parentheses negatives: "(500.00)" -> -50000 paise
// - Trailing DR/CR: "500.00 DR" -> -50000 paise
func ParseAmountString(val string) (int64, error) {
	s := strings.TrimSpace(val)
	if s == "" {
		return 0, fmt.Errorf("amount value is empty")
	}

	isNegative := false

	// Handle parentheses negatives: (500.00) -> -500.00
	if m := reParenthesesNeg.FindStringSubmatch(s); len(m) > 1 {
		isNegative = true
		s = m[1]
	}

	// Handle trailing DR / CR
	upper := strings.ToUpper(s)
	if strings.HasSuffix(upper, " DR") {
		isNegative = true
		s = strings.TrimSuffix(s, " DR")
		s = strings.TrimSuffix(s, " dr")
	} else if strings.HasSuffix(upper, " CR") {
		s = strings.TrimSuffix(s, " CR")
		s = strings.TrimSuffix(s, " cr")
	}

	// Strip currency symbols and whitespace
	s = reCurrencyPrefix.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)

	// Strip commas
	s = strings.ReplaceAll(s, ",", "")

	// Check if already negative prefix
	if strings.HasPrefix(s, "-") {
		isNegative = true
		s = strings.TrimPrefix(s, "-")
		s = strings.TrimSpace(s)
	}

	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid numeric amount '%s': %w", val, err)
	}

	paise := int64(math.Round(f * 100.0))
	if isNegative {
		paise = -paise
	}
	return paise, nil
}

// ParseDateString parses a date string supporting Unix timestamps, RFC3339, and custom date formats.
func ParseDateString(val string, preferredFormat string) (time.Time, error) {
	s := strings.TrimSpace(val)
	if s == "" {
		return time.Time{}, fmt.Errorf("date value is empty")
	}

	// Normalize trailing lowercase am/pm to uppercase AM/PM
	sLower := strings.ToLower(s)
	if strings.HasSuffix(sLower, " am") {
		s = s[:len(s)-3] + " AM"
	} else if strings.HasSuffix(sLower, " pm") {
		s = s[:len(s)-3] + " PM"
	}

	prefUpper := strings.ToUpper(strings.TrimSpace(preferredFormat))

	// 1. Unix timestamp (integer or decimal/float)
	if reDigitsOnly.MatchString(s) {
		switch {
		case len(s) >= 9 && len(s) <= 11: // Seconds (e.g. 1725515000)
			if sec, err := strconv.ParseInt(s, 10, 64); err == nil && sec >= 100000000 && sec <= 99999999999 {
				return time.Unix(sec, 0).UTC(), nil
			}
		case len(s) >= 12 && len(s) <= 14: // Milliseconds (e.g. 1725515000000)
			if msec, err := strconv.ParseInt(s, 10, 64); err == nil {
				return time.UnixMilli(msec).UTC(), nil
			}
		case len(s) >= 15 && len(s) <= 17: // Microseconds (e.g. 1725515000000000)
			if usec, err := strconv.ParseInt(s, 10, 64); err == nil {
				return time.UnixMicro(usec).UTC(), nil
			}
		case len(s) >= 18 && len(s) <= 20: // Nanoseconds (e.g. 1725515000000000000)
			if nsec, err := strconv.ParseInt(s, 10, 64); err == nil {
				return time.Unix(0, nsec).UTC(), nil
			}
		}
	} else if strings.Contains(s, ".") && reUnixFloat.MatchString(s) {
		if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			if f >= 1e8 && f < 1e11 { // Seconds with fraction
				sec := int64(f)
				nsec := int64(math.Round((f - float64(sec)) * 1e9))
				return time.Unix(sec, nsec).UTC(), nil
			} else if f >= 1e11 && f < 1e14 { // Milliseconds with fraction
				msec := int64(f)
				usec := int64(math.Round((f - float64(msec)) * 1000.0))
				return time.UnixMilli(msec).Add(time.Duration(usec) * time.Microsecond).UTC(), nil
			}
		}
	}

	// If preferred format is Unix timestamp, attempt generic numeric conversion
	if prefUpper == "UNIX TIMESTAMP" || prefUpper == "UNIX" || prefUpper == "EPOCH" {
		if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			if f >= 1e8 && f < 1e11 {
				sec := int64(f)
				nsec := int64(math.Round((f - float64(sec)) * 1e9))
				return time.Unix(sec, nsec).UTC(), nil
			} else if f >= 1e11 && f < 1e14 {
				return time.UnixMilli(int64(f)).UTC(), nil
			} else if f >= 1e14 && f < 1e17 {
				return time.UnixMicro(int64(f)).UTC(), nil
			} else if f >= 1e17 && f < 1e20 {
				return time.Unix(0, int64(f)).UTC(), nil
			}
		}
	}

	// 2. Preferred format from user / AI
	var formatList []string
	switch prefUpper {
	case "DD/MM/YYYY":
		formatList = append(formatList,
			"02/01/2006",
			"02/01/2006 15:04:05",
			"02/01/2006 15:04",
			"02/01/2006 03:04:05 PM",
			"02/01/2006 03:04 PM",
			"02/01/2006 3:04:05 PM",
			"02/01/2006 3:04 PM",
			"2/1/2006",
			"2/1/2006 15:04:05",
			"2/1/2006 03:04:05 PM",
			"02/01/06",
			"02/01/06 15:04:05",
			"02/01/06 03:04:05 PM",
		)
	case "MM/DD/YYYY":
		formatList = append(formatList,
			"01/02/2006",
			"01/02/2006 15:04:05",
			"01/02/2006 15:04",
			"01/02/2006 03:04:05 PM",
			"01/02/2006 03:04 PM",
			"01/02/2006 3:04:05 PM",
			"01/02/2006 3:04 PM",
			"1/2/2006",
			"1/2/2006 15:04:05",
			"1/2/2006 03:04:05 PM",
			"01/02/06",
			"01/02/06 15:04:05",
			"01/02/06 03:04:05 PM",
		)
	case "YYYY-MM-DD":
		formatList = append(formatList,
			"2006-01-02",
			"2006-01-02 15:04:05",
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02T15:04:05.999999999",
			"2006-01-02T15:04:05",
			"2006-01-02 03:04:05 PM",
			"2006-01-02 03:04 PM",
			"2006-01-02 15:04",
		)
	case "DD-MM-YYYY":
		formatList = append(formatList,
			"02-01-2006",
			"02-01-2006 15:04:05",
			"02-01-2006 15:04",
			"02-01-2006 03:04:05 PM",
			"02-01-2006 03:04 PM",
			"02-01-06",
			"02-01-06 15:04:05",
			"02-01-06 03:04:05 PM",
		)
	case "YYYY/MM/DD":
		formatList = append(formatList,
			"2006/01/02",
			"2006/01/02 15:04:05",
			"2006/01/02 15:04",
			"2006/01/02 03:04:05 PM",
			"2006/01/02 03:04 PM",
		)
	case "RFC3339":
		formatList = append(formatList,
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02T15:04:05.999999999",
			"2006-01-02T15:04:05",
		)
	}

	// Standard fallbacks
	formatList = append(formatList,
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
		"02/01/2006 15:04:05",
		"02/01/2006 15:04",
		"02/01/2006",
		"02/01/2006 03:04:05 PM",
		"02/01/2006 03:04 PM",
		"01/02/2006 15:04:05",
		"01/02/2006",
		"01/02/2006 03:04:05 PM",
		"02-01-2006 15:04:05",
		"02-01-2006 15:04",
		"02-01-2006",
		"02-01-2006 03:04:05 PM",
		"2006/01/02 15:04:05",
		"2006/01/02",
		// Named month formats (Indian bank statement exports)
		"02-Jan-2006",
		"02-Jan-2006 15:04:05",
		"02-Jan-2006 03:04:05 PM",
		"02-Jan-2006 03:04 PM",
		"02 Jan 2006",
		"02 Jan 2006 15:04:05",
		"02 Jan 2006 03:04:05 PM",
		"Jan 02, 2006",
		"Jan 2, 2006",
		"02/Jan/2006",
		"02/Jan/2006 15:04:05",
		// 8-digit compact format
		"20060102",
	)

	for _, layout := range formatList {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}

	return time.Time{}, fmt.Errorf("unable to parse date '%s'", val)
}

// ParsedUploadData holds the domain entities parsed from the confirmed mapping.
type ParsedUploadData struct {
	Internals      []models.InternalTransaction
	Settlements    []models.SettlementRecord
	BankStatements []models.BankStatement
	RowsIngested   int
	RowsSkipped    int
	SkippedReasons []string
}

// ValidateAndParseCSV applies the user-confirmed column mapping across all CSV rows.
func ValidateAndParseCSV(
	content []byte,
	sourceType string,
	mapping map[string]string,
	dateFormat string,
	runID uuid.UUID,
) (*ParsedUploadData, error) {
	fields, ok := CanonicalFields[sourceType]
	if !ok {
		return nil, fmt.Errorf("unrecognized source type '%s'", sourceType)
	}

	// 1. Schema Validation: Check all required fields are mapped
	for _, f := range fields {
		if f.Required {
			col, exists := mapping[f.Name]
			if !exists || strings.TrimSpace(col) == "" {
				return nil, fmt.Errorf("validation error: required field '%s' is unmapped", f.Name)
			}
		}
	}

	reader := csv.NewReader(strings.NewReader(string(content)))
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1

	// Read header row
	headers, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("failed reading CSV header: %w", err)
	}

	// Build column index map: canonical field name -> header column index
	colIndex := make(map[string]int)
	for fName, mappedCol := range mapping {
		if mappedCol == "" {
			continue
		}
		found := false
		for i, h := range headers {
			if strings.TrimSpace(h) == strings.TrimSpace(mappedCol) {
				colIndex[fName] = i
				found = true
				break
			}
		}
		if !found && CanonicalFields[sourceType] != nil {
			// Check if required
			for _, reqF := range CanonicalFields[sourceType] {
				if reqF.Name == fName && reqF.Required {
					return nil, fmt.Errorf("validation error: mapped column '%s' for '%s' not found in CSV headers", mappedCol, fName)
				}
			}
		}
	}

	var internals []models.InternalTransaction
	var settlements []models.SettlementRecord
	var bankStatements []models.BankStatement

	seenIDs := make(map[string]int)
	dateFailures := 0
	totalDataRows := 0
	var firstDateError string
	var skippedReasons []string

	lineNum := 1
	for {
		lineNum++
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			skippedReasons = append(skippedReasons, fmt.Sprintf("line %d: CSV read syntax error: %v", lineNum, err))
			continue
		}

		// Skip empty lines
		isEmpty := true
		for _, cell := range row {
			if strings.TrimSpace(cell) != "" {
				isEmpty = false
				break
			}
		}
		if isEmpty {
			continue
		}

		totalDataRows++

		getVal := func(fieldName string) string {
			idx, ok := colIndex[fieldName]
			if !ok || idx >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[idx])
		}

		// 2. Validate and extract ID
		id := getVal("id")
		if id == "" {
			if sourceType == SourceBank {
				id = fmt.Sprintf("BNK%03d", totalDataRows)
			} else {
				skippedReasons = append(skippedReasons, fmt.Sprintf("line %d: skipped due to missing ID", lineNum))
				continue
			}
		}

		// Duplicate ID detection within upload
		if prevLine, seen := seenIDs[id]; seen {
			return nil, fmt.Errorf("validation error: duplicate ID '%s' found on row %d (first seen on row %d); IDs within source CSV must be unique", id, lineNum, prevLine)
		}
		seenIDs[id] = lineNum

		// 3. Validate and parse Amount
		var amountPaise int64
		switch sourceType {
		case SourceInternal:
			amtStr := getVal("amount")
			paise, err := ParseAmountString(amtStr)
			if err != nil {
				return nil, fmt.Errorf("validation error on row %d for field 'amount': %w", lineNum, err)
			}
			amountPaise = paise
		case SourceSettlement:
			amtStr := getVal("settled_amount")
			paise, err := ParseAmountString(amtStr)
			if err != nil {
				return nil, fmt.Errorf("validation error on row %d for field 'settled_amount': %w", lineNum, err)
			}
			amountPaise = paise
		case SourceBank:
			amtStr := getVal("credited_amount")
			paise, err := ParseAmountString(amtStr)
			if err != nil {
				return nil, fmt.Errorf("validation error on row %d for field 'credited_amount': %w", lineNum, err)
			}
			amountPaise = paise
		}

		// 4. Validate and parse Date
		var dateVal time.Time
		dateFieldName := "transaction_date"
		if sourceType == SourceSettlement {
			dateFieldName = "settlement_date"
		} else if sourceType == SourceBank {
			dateFieldName = "credit_date"
		}

		rawDateStr := getVal(dateFieldName)
		dt, err := ParseDateString(rawDateStr, dateFormat)
		if err != nil {
			dateFailures++
			if firstDateError == "" {
				firstDateError = fmt.Sprintf("row %d: %v", lineNum, err)
			}
			skippedReasons = append(skippedReasons, fmt.Sprintf("line %d: invalid date '%s'", lineNum, rawDateStr))
			continue
		}
		dateVal = dt

		// 5. Merchant ID
		merchantID := getVal("merchant_id")
		if merchantID == "" {
			return nil, fmt.Errorf("validation error on row %d: required field 'merchant_id' is empty", lineNum)
		}

		// 6. Currency (default INR)
		currency := getVal("currency")
		if currency == "" {
			currency = "INR"
		} else {
			currency = strings.ToUpper(currency)
		}

		now := time.Now().UTC()

		// Build record based on source type
		switch sourceType {
		case SourceInternal:
			var refID *string
			if rawRef := getVal("reference_id"); rawRef != "" {
				refID = &rawRef
			}
			internals = append(internals, models.InternalTransaction{
				ID:              id,
				RunID:           runID,
				AmountPaise:     amountPaise,
				Currency:        currency,
				TransactionDate: dateVal,
				MerchantID:      merchantID,
				ReferenceID:     refID,
				CreatedAt:       now,
			})

		case SourceSettlement:
			batchID := getVal("batch_id")
			if batchID == "" {
				batchID = "BATCH_UNASSIGNED"
			}
			var refID *string
			if rawRef := getVal("reference_id"); rawRef != "" {
				refID = &rawRef
			}
			settlements = append(settlements, models.SettlementRecord{
				ID:                 id,
				RunID:              runID,
				SettledAmountPaise: amountPaise,
				Currency:           currency,
				SettlementDate:     dateVal,
				MerchantID:         merchantID,
				ReferenceID:        refID,
				BatchID:            batchID,
				CreatedAt:          now,
			})

		case SourceBank:
			var batchRef *string
			if rawBatch := getVal("batch_reference"); rawBatch != "" {
				batchRef = &rawBatch
			}
			narration := getVal("narration")
			if narration == "" {
				narration = "Bank settlement deposit"
			}
			bankStatements = append(bankStatements, models.BankStatement{
				ID:                  id,
				RunID:               runID,
				CreditedAmountPaise: amountPaise,
				Currency:            currency,
				CreditDate:          dateVal,
				MerchantID:          merchantID,
				BatchReference:      batchRef,
				Narration:           narration,
				CreatedAt:           now,
			})
		}
	}

	if totalDataRows == 0 {
		return nil, fmt.Errorf("CSV contains 0 valid data rows")
	}

	// 7. Check > 5% date failure rate
	if totalDataRows > 0 {
		failPct := float64(dateFailures) / float64(totalDataRows) * 100.0
		if failPct > 5.0 {
			return nil, fmt.Errorf("date parsing validation failed: %.1f%% of rows (%d of %d) failed date parsing (first error: %s). Check the date format setting", failPct, dateFailures, totalDataRows, firstDateError)
		}
	}

	ingestedCount := len(internals) + len(settlements) + len(bankStatements)
	if ingestedCount == 0 {
		return nil, fmt.Errorf("0 records could be ingested due to validation failures")
	}

	return &ParsedUploadData{
		Internals:      internals,
		Settlements:    settlements,
		BankStatements: bankStatements,
		RowsIngested:   ingestedCount,
		RowsSkipped:    len(skippedReasons),
		SkippedReasons: skippedReasons,
	}, nil
}

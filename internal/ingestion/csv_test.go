package ingestion

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCleanAndInspectCSV_XLSXRejection(t *testing.T) {
	// ZIP/XLSX magic bytes
	fakeXLSX := []byte{0x50, 0x4B, 0x03, 0x04, 0x00, 0x00, 0x00}
	_, err := CleanAndInspectCSV(fakeXLSX)
	if err == nil || !strings.Contains(err.Error(), "Excel spreadsheet") {
		t.Fatalf("expected excel rejection error, got: %v", err)
	}
}

func TestCleanAndInspectCSV_BOMAndCRLF(t *testing.T) {
	raw := []byte("\xef\xbb\xbfid,amount,transaction_date,merchant_id\r\nINT01,100,2026-09-01,M1\r\nINT02,200,2026-09-01,M1\r\n")
	data, err := CleanAndInspectCSV(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data.Headers) != 4 || data.Headers[0] != "id" {
		t.Fatalf("headers not parsed cleanly: %v", data.Headers)
	}
	if data.TotalRows != 2 {
		t.Fatalf("expected 2 total rows, got: %d", data.TotalRows)
	}
}

func TestCleanAndInspectCSV_MergedHeader(t *testing.T) {
	csvText := "Bank Account Statement Export\nid,credited_amount,credit_date,merchant_id,batch_reference\nBNK01,1000,2026-09-01,M1,BATCH1\n"
	data, err := CleanAndInspectCSV([]byte(csvText))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data.Headers[0] != "id" {
		t.Fatalf("expected row 2 headers, got: %v", data.Headers)
	}
}

func TestParseAmountString(t *testing.T) {
	cases := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		{"1250.50", 125050, false},
		{"₹ 1,250.50", 125050, false},
		{"$500.00", 50000, false},
		{"Rs. 99.00", 9900, false},
		{"INR 1000", 100000, false},
		{"(500.00)", -50000, false},
		{"-250.75", -25075, false},
		{"100.50 DR", -10050, false},
		{"100.50 CR", 10050, false},
		{"invalid", 0, true},
		{"", 0, true},
	}

	for _, tc := range cases {
		got, err := ParseAmountString(tc.input)
		if (err != nil) != tc.wantErr {
			t.Errorf("input '%s' error mismatch: got err=%v, wantErr=%v", tc.input, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.expected {
			t.Errorf("input '%s': got %d, want %d", tc.input, got, tc.expected)
		}
	}
}

func TestParseDateString(t *testing.T) {
	cases := []struct {
		input  string
		format string
		wantYr int
		wantM  time.Month
		wantD  int
	}{
		{"2026-09-01T10:15:30Z", "RFC3339", 2026, time.September, 1},
		{"2026-09-01T10:15:30.123Z", "RFC3339", 2026, time.September, 1},
		{"2026-09-01T10:15:30.456+05:30", "RFC3339", 2026, time.September, 1},
		{"2026-09-05", "YYYY-MM-DD", 2026, time.September, 5},
		{"05/09/2026", "DD/MM/YYYY", 2026, time.September, 5},
		{"09/05/2026", "MM/DD/YYYY", 2026, time.September, 5},
		// Unix timestamps: integer seconds & milliseconds
		{"1725515000", "Unix timestamp", 2024, time.September, 5},
		{"1725515000000", "Unix timestamp", 2024, time.September, 5},
		// Unix timestamps: float seconds & milliseconds
		{"1725515000.0", "", 2024, time.September, 5},
		{"1725515000.123456", "Unix timestamp", 2024, time.September, 5},
		// Unix timestamps: microseconds (16 digits) & nanoseconds (19 digits)
		{"1725515000000000", "Unix timestamp", 2024, time.September, 5},
		{"1725515000000000000", "Unix timestamp", 2024, time.September, 5},
		// 9-digit Unix timestamp (Jan 1, 2000)
		{"946684800", "Unix timestamp", 2000, time.January, 1},
		// 12-hour format with AM/PM
		{"05/09/2026 02:30:15 PM", "DD/MM/YYYY", 2026, time.September, 5},
		{"05/09/2026 02:30 pm", "DD/MM/YYYY", 2026, time.September, 5},
		{"2026-09-05 10:15 AM", "YYYY-MM-DD", 2026, time.September, 5},
		// Named months
		{"05-Sep-2026", "", 2026, time.September, 5},
		{"05-SEP-2026 15:04:05", "", 2026, time.September, 5},
		{"Sep 05, 2026", "", 2026, time.September, 5},
	}

	for _, tc := range cases {
		tVal, err := ParseDateString(tc.input, tc.format)
		if err != nil {
			t.Errorf("failed parsing '%s' with format '%s': %v", tc.input, tc.format, err)
			continue
		}
		if tVal.Day() != tc.wantD || tVal.Month() != tc.wantM || tVal.Year() != tc.wantYr {
			t.Errorf("date '%s' parsed incorrectly: got %v, want year=%d month=%v day=%d", tc.input, tVal, tc.wantYr, tc.wantM, tc.wantD)
		}
	}
}

func TestDetectDateFormat_AvoidFalsePositiveFromIDColumn(t *testing.T) {
	// Column 0 is a 10-digit ID (e.g. 9876543210), column 2 is date (2026-09-01)
	headers := []string{"id", "amount", "transaction_date", "merchant_id"}
	sampleRows := [][]string{
		{"9876543210", "1250.00", "2026-09-01", "MERCH01"},
		{"9876543211", "2500.00", "2026-09-02", "MERCH01"},
	}

	ctx := context.Background()
	res, err := InferColumnMapping(ctx, SourceInternal, headers, sampleRows, "", "", "")
	if err != nil {
		t.Fatalf("InferColumnMapping failed: %v", err)
	}

	if res.DateFormat != "YYYY-MM-DD" {
		t.Errorf("expected DateFormat to be 'YYYY-MM-DD', got '%s' (falsely identified as Unix timestamp due to ID col)", res.DateFormat)
	}
}

func TestDetectDateFormat_FloatUnixTimestamp(t *testing.T) {
	headers := []string{"id", "amount", "transaction_date", "merchant_id"}
	sampleRows := [][]string{
		{"TXN01", "1250.00", "1725515000.500", "MERCH01"},
	}

	ctx := context.Background()
	res, err := InferColumnMapping(ctx, SourceInternal, headers, sampleRows, "", "", "")
	if err != nil {
		t.Fatalf("InferColumnMapping failed: %v", err)
	}

	if res.DateFormat != "Unix timestamp" {
		t.Errorf("expected DateFormat to be 'Unix timestamp', got '%s'", res.DateFormat)
	}
}

func TestInferColumnMapping_MessyInternalHeaders(t *testing.T) {
	headers := []string{"Txn Ref ID", "Gross Paid Amount", "Txn Timestamp", "Merchant Code", "Gateway Order Ref"}
	sampleRows := [][]string{
		{"INT_MESSY_001", "₹5,214.63", "01/09/2026 10:15:30", "MERCH_ZOMATO_DEL", "REF_ORD_001"},
	}

	ctx := context.Background()
	// Call without API key to test heuristic mapper
	res, err := InferColumnMapping(ctx, SourceInternal, headers, sampleRows, "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Mapping["id"] != "Txn Ref ID" {
		t.Errorf("expected id mapped to 'Txn Ref ID', got: %s", res.Mapping["id"])
	}
	if res.Mapping["amount"] != "Gross Paid Amount" {
		t.Errorf("expected amount mapped to 'Gross Paid Amount', got: %s", res.Mapping["amount"])
	}
	if res.Mapping["transaction_date"] != "Txn Timestamp" {
		t.Errorf("expected transaction_date mapped to 'Txn Timestamp', got: %s", res.Mapping["transaction_date"])
	}
	if res.Mapping["merchant_id"] != "Merchant Code" {
		t.Errorf("expected merchant_id mapped to 'Merchant Code', got: %s", res.Mapping["merchant_id"])
	}
	if res.DateFormat != "DD/MM/YYYY" {
		t.Errorf("expected date_format 'DD/MM/YYYY', got: %s", res.DateFormat)
	}
}

func TestValidateAndParseCSV_DuplicateIDDetection(t *testing.T) {
	csvText := "id,amount,transaction_date,merchant_id\nINT01,100,2026-09-01,M1\nINT01,200,2026-09-01,M1\n"
	mapping := map[string]string{
		"id":               "id",
		"amount":           "amount",
		"transaction_date": "transaction_date",
		"merchant_id":      "merchant_id",
	}

	runID := uuid.New()
	_, err := ValidateAndParseCSV([]byte(csvText), SourceInternal, mapping, "YYYY-MM-DD", runID)
	if err == nil || !strings.Contains(err.Error(), "duplicate ID") {
		t.Fatalf("expected duplicate ID error, got: %v", err)
	}
}

func TestValidateAndParseCSV_HighDateFailureRate(t *testing.T) {
	// 20 rows, where 2 rows fail date (> 5%)
	var b strings.Builder
	b.WriteString("id,amount,transaction_date,merchant_id\n")
	for i := 1; i <= 18; i++ {
		b.WriteString(strings.Replace("INT%d,100,2026-09-01,M1\n", "%d", string(rune('A'+i)), 1))
	}
	b.WriteString("INT_BAD1,100,INVALID_DATE_1,M1\n")
	b.WriteString("INT_BAD2,100,INVALID_DATE_2,M1\n")

	mapping := map[string]string{
		"id":               "id",
		"amount":           "amount",
		"transaction_date": "transaction_date",
		"merchant_id":      "merchant_id",
	}

	runID := uuid.New()
	_, err := ValidateAndParseCSV([]byte(b.String()), SourceInternal, mapping, "YYYY-MM-DD", runID)
	if err == nil || !strings.Contains(err.Error(), "date parsing validation failed") {
		t.Fatalf("expected high date failure rate error, got: %v", err)
	}
}

func TestGenerateMessySampleCSV(t *testing.T) {
	for _, src := range []string{SourceInternal, SourceSettlement, SourceBank} {
		data, err := GenerateMessySampleCSV(src)
		if err != nil {
			t.Fatalf("failed generating sample for %s: %v", src, err)
		}
		if len(data) == 0 {
			t.Fatalf("empty sample for %s", src)
		}
	}
}

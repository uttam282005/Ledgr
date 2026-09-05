package ingestion

// Source types
const (
	SourceInternal   = "internal"
	SourceSettlement = "settlement"
	SourceBank       = "bank"
)

// FieldDef defines a canonical schema field.
type FieldDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// CanonicalFields holds schema specifications for the three financial sources.
var CanonicalFields = map[string][]FieldDef{
	SourceInternal: {
		{Name: "id", Description: "Unique internal transaction ID (e.g. INT0001, txn_123)", Required: true},
		{Name: "amount", Description: "Gross transaction amount in rupees (e.g. 5214.63, ₹1,200.00)", Required: true},
		{Name: "transaction_date", Description: "Date/timestamp transaction was recorded", Required: true},
		{Name: "merchant_id", Description: "Merchant / store / account identifier", Required: true},
		{Name: "currency", Description: "3-letter currency code (defaults to INR)", Required: false},
		{Name: "reference_id", Description: "External order/payment reference shared with gateway", Required: false},
	},
	SourceSettlement: {
		{Name: "id", Description: "Unique settlement record ID (e.g. SET0001, pay_123)", Required: true},
		{Name: "settled_amount", Description: "Net settled amount after gateway deductions", Required: true},
		{Name: "settlement_date", Description: "Date/timestamp of gateway settlement", Required: true},
		{Name: "merchant_id", Description: "Merchant identifier in gateway report", Required: true},
		{Name: "batch_id", Description: "Gateway settlement/payout batch ID (e.g. BATCH_001)", Required: true},
		{Name: "currency", Description: "3-letter currency code (defaults to INR)", Required: false},
		{Name: "reference_id", Description: "Reference ID shared with internal transaction", Required: false},
	},
	SourceBank: {
		{Name: "id", Description: "Bank statement entry / credit reference ID", Required: false},
		{Name: "credited_amount", Description: "Actual amount credited to bank account", Required: true},
		{Name: "credit_date", Description: "Date bank account was credited", Required: true},
		{Name: "merchant_id", Description: "Merchant or corporate account identifier", Required: true},
		{Name: "batch_reference", Description: "Settlement batch reference from narration/advice", Required: true},
		{Name: "narration", Description: "Bank narration / remarks / particulars", Required: false},
	},
}

// MappingResult is returned from AI analysis for the user confirmation screen.
type MappingResult struct {
	FileID         string            `json:"file_id"`
	SourceType     string            `json:"source_type"`
	Filename       string            `json:"filename"`
	Headers        []string          `json:"headers"`
	SampleRows     [][]string        `json:"sample_rows"`
	Mapping        map[string]string `json:"mapping"`
	Confidence     map[string]string `json:"confidence"` // HIGH, MEDIUM, LOW, UNMAPPED
	DateFormat     string            `json:"date_format"`
	ComputedFields []string          `json:"computed_fields"`
	Notes          string            `json:"notes"`
	Warnings       []string          `json:"warnings"`
	CanonicalDefs  []FieldDef        `json:"canonical_defs"`
}

// CommitRequest is sent by the client after confirming/adjusting column mapping.
type CommitRequest struct {
	FileID          string            `json:"file_id"`
	SourceType      string            `json:"source_type"`
	Mapping         map[string]string `json:"mapping"`
	DateFormat      string            `json:"date_format"`
	RunID           string            `json:"run_id,omitempty"`
	ReplaceExisting bool              `json:"replace_existing"`
}

// SourcesStatus reports the upload completeness across the 3 financial sources for a run.
type SourcesStatus struct {
	RunID              string `json:"run_id"`
	InternalCount      int    `json:"internal_count"`
	SettlementCount    int    `json:"settlement_count"`
	BankCount          int    `json:"bank_count"`
	AllSourcesUploaded bool   `json:"all_sources_uploaded"`
}

// IngestSummary is returned to the user after ingestion completes.
type IngestSummary struct {
	Status             string   `json:"status"`
	RunID              string   `json:"run_id"`
	SourceType         string   `json:"source_type"`
	Filename           string   `json:"filename"`
	RowsIngested       int      `json:"rows_ingested"`
	RowsSkipped        int      `json:"rows_skipped"`
	SkippedReasons     []string `json:"skipped_reasons"`
	Message            string   `json:"message"`
	InternalCount      int      `json:"internal_count"`
	SettlementCount    int      `json:"settlement_count"`
	BankCount          int      `json:"bank_count"`
	AllSourcesUploaded bool     `json:"all_sources_uploaded"`
}

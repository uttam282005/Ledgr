package models

import (
	"time"

	"github.com/google/uuid"
)

// ReconciliationRun represents a single deterministic evaluation run.
type ReconciliationRun struct {
	RunID                   uuid.UUID  `json:"run_id" db:"run_id"`
	Seed                    int64      `json:"seed" db:"seed"`
	DatasetVersion          string     `json:"dataset_version" db:"dataset_version"`
	EngineVersion           string     `json:"engine_version" db:"engine_version"`
	StartedAt               time.Time  `json:"started_at" db:"started_at"`
	CompletedAt             *time.Time `json:"completed_at,omitempty" db:"completed_at"`
	InternalCount           int        `json:"internal_count" db:"internal_count"`
	SettlementCount         int        `json:"settlement_count" db:"settlement_count"`
	BankCount               int        `json:"bank_count" db:"bank_count"`
	Hop1MatchedCount        int        `json:"hop1_matched_count" db:"hop1_matched_count"`
	Hop2MatchedCount        int        `json:"hop2_matched_count" db:"hop2_matched_count"`
	FullChainCount          int        `json:"full_chain_count" db:"full_chain_count"`
	ExceptionCount          int        `json:"exception_count" db:"exception_count"`
	UnresolvedAmountPaise   int64      `json:"unresolved_amount_paise" db:"unresolved_amount_paise"`
	EngineDurationMs        int64      `json:"engine_duration_ms" db:"engine_duration_ms"`
	SourceRecordsProcessed  int        `json:"source_records_processed" db:"source_records_processed"`
	ThroughputRecordsPerSec float64    `json:"throughput_records_per_sec" db:"throughput_records_per_sec"`
}

// InternalTransaction represents a gross ledger record.
type InternalTransaction struct {
	ID              string    `json:"id" db:"id"`
	RunID           uuid.UUID `json:"run_id" db:"run_id"`
	AmountPaise     int64     `json:"amount_paise" db:"amount_paise"`
	Currency        string    `json:"currency" db:"currency"`
	TransactionDate time.Time `json:"transaction_date" db:"transaction_date"`
	MerchantID      string    `json:"merchant_id" db:"merchant_id"`
	ReferenceID     *string   `json:"reference_id,omitempty" db:"reference_id"`
	CreatedAt       time.Time `json:"created_at" db:"created_at"`
}

// SettlementRecord represents a gateway settlement record.
type SettlementRecord struct {
	ID                 string    `json:"id" db:"id"`
	RunID              uuid.UUID `json:"run_id" db:"run_id"`
	SettledAmountPaise int64     `json:"settled_amount_paise" db:"settled_amount_paise"`
	Currency           string    `json:"currency" db:"currency"`
	SettlementDate     time.Time `json:"settlement_date" db:"settlement_date"`
	MerchantID         string    `json:"merchant_id" db:"merchant_id"`
	ReferenceID        *string   `json:"reference_id,omitempty" db:"reference_id"`
	BatchID            string    `json:"batch_id" db:"batch_id"`
	CreatedAt          time.Time `json:"created_at" db:"created_at"`
}

// BankStatement represents a batch-level bank statement credit.
type BankStatement struct {
	ID                  string    `json:"id" db:"id"`
	RunID               uuid.UUID `json:"run_id" db:"run_id"`
	CreditedAmountPaise int64     `json:"credited_amount_paise" db:"credited_amount_paise"`
	Currency            string    `json:"currency" db:"currency"`
	CreditDate          time.Time `json:"credit_date" db:"credit_date"`
	MerchantID          string    `json:"merchant_id" db:"merchant_id"`
	BatchReference      *string   `json:"batch_reference,omitempty" db:"batch_reference"`
	Narration           string    `json:"narration" db:"narration"`
	CreatedAt           time.Time `json:"created_at" db:"created_at"`
}

// ReconciliationStatus represents terminal status for an internal case.
type ReconciliationStatus string

const (
	StatusFull      ReconciliationStatus = "FULL"
	StatusPartial   ReconciliationStatus = "PARTIAL"
	StatusUnmatched ReconciliationStatus = "UNMATCHED"
)

// ReconciliationMatch represents the joined reconciliation record.
type ReconciliationMatch struct {
	ID                       uuid.UUID            `json:"id" db:"id"`
	RunID                    uuid.UUID            `json:"run_id" db:"run_id"`
	InternalID               string               `json:"internal_id" db:"internal_id"`
	SettlementID             *string              `json:"settlement_id,omitempty" db:"settlement_id"`
	BankStatementID          *string              `json:"bank_statement_id,omitempty" db:"bank_statement_id"`
	Hop1Rule                 *string              `json:"hop1_rule,omitempty" db:"hop1_rule"`
	Hop2Rule                 *string              `json:"hop2_rule,omitempty" db:"hop2_rule"`
	Hop1Confidence           float64              `json:"hop1_confidence" db:"hop1_confidence"`
	Hop2Confidence           float64              `json:"hop2_confidence" db:"hop2_confidence"`
	ReconciliationStatus     ReconciliationStatus `json:"reconciliation_status" db:"reconciliation_status"`
	FeeDeltaPaise            *int64               `json:"fee_delta_paise,omitempty" db:"fee_delta_paise"`
	ExpectedBankAmountPaise  *int64               `json:"expected_bank_amount_paise,omitempty" db:"expected_bank_amount_paise"`
	ActualBankAmountPaise    *int64               `json:"actual_bank_amount_paise,omitempty" db:"actual_bank_amount_paise"`
	BankDeltaPaise           *int64               `json:"bank_delta_paise,omitempty" db:"bank_delta_paise"`
	CreatedAt                time.Time            `json:"created_at" db:"created_at"`
}

// AIStatus represents the lifecycle of AI investigation on an exception.
type AIStatus string

const (
	AINotRequired  AIStatus = "NOT_REQUIRED"
	AIPending      AIStatus = "PENDING"
	AISucceeded    AIStatus = "SUCCEEDED"
	AIFailed       AIStatus = "FAILED"
	AIUnavailable  AIStatus = "UNAVAILABLE"
)

// ExceptionCategory represents controlled vocabulary exception categories.
const (
	// Hop 1
	CategoryAmountMismatch      = "AMOUNT_MISMATCH"
	CategoryNoCounterpart       = "NO_COUNTERPART"
	CategoryDuplicateSettlement = "DUPLICATE_SETTLEMENT"
	CategoryDateOutOfRange      = "DATE_OUT_OF_RANGE"
	CategoryOrphanSettlement    = "ORPHAN_SETTLEMENT"

	// Hop 2
	CategorySettledNotBanked    = "SETTLED_NOT_BANKED"
	CategoryPartialCredit       = "PARTIAL_CREDIT"
	CategoryBankAmountMismatch  = "BANK_AMOUNT_MISMATCH"
	CategoryBankedNotSettled    = "BANKED_NOT_SETTLED"
	CategoryAmbiguousBatch      = "AMBIGUOUS_BATCH"
	CategoryAmbiguousBankCredit = "AMBIGUOUS_BANK_CREDIT"
)

// Exception represents an unresolved financial case.
type Exception struct {
	ID                  uuid.UUID `json:"id" db:"id"`
	RunID               uuid.UUID `json:"run_id" db:"run_id"`
	RecordID            string    `json:"record_id" db:"record_id"`
	Source              string    `json:"source" db:"source"` // 'internal', 'settlement', 'bank'
	Category            string    `json:"category" db:"category"`
	Hop                 string    `json:"hop" db:"hop"`       // 'HOP1', 'HOP2'
	Reason              string    `json:"reason" db:"reason"`
	ExpectedAmountPaise *int64    `json:"expected_amount_paise,omitempty" db:"expected_amount_paise"`
	ActualAmountPaise   *int64    `json:"actual_amount_paise,omitempty" db:"actual_amount_paise"`
	DeltaPaise          *int64    `json:"delta_paise,omitempty" db:"delta_paise"`
	ExposurePaise       *int64    `json:"exposure_paise,omitempty" db:"exposure_paise"`
	AIStatus            AIStatus  `json:"ai_status" db:"ai_status"`
	AISummary           *string   `json:"ai_summary,omitempty" db:"ai_summary"`
	AIAction            *string   `json:"ai_action,omitempty" db:"ai_action"`
	AIConfidence        *string   `json:"ai_confidence,omitempty" db:"ai_confidence"` // 'HIGH', 'MEDIUM', 'LOW'
	CreatedAt           time.Time `json:"created_at" db:"created_at"`
}

// AuditLog represents auditable decision evidence.
type AuditLog struct {
	DecisionID           uuid.UUID `json:"decision_id" db:"decision_id"`
	RunID                uuid.UUID `json:"run_id" db:"run_id"`
	RecordIDs            []string  `json:"record_ids" db:"record_ids"`
	RuleApplied          string    `json:"rule_applied" db:"rule_applied"`
	FieldsCompared       string    `json:"fields_compared" db:"fields_compared"`             // JSON string
	CandidatesConsidered string    `json:"candidates_considered" db:"candidates_considered"` // JSON string
	Outcome              string    `json:"outcome" db:"outcome"`                             // 'MATCHED', 'PARTIAL', 'EXCEPTION'
	AIReasoning          *string   `json:"ai_reasoning,omitempty" db:"ai_reasoning"`
	AIModel              *string   `json:"ai_model,omitempty" db:"ai_model"`
	AIPromptVersion      *string   `json:"ai_prompt_version,omitempty" db:"ai_prompt_version"`
	AILatencyMs          *int      `json:"ai_latency_ms,omitempty" db:"ai_latency_ms"`
	CreatedAt            time.Time `json:"created_at" db:"created_at"`
}

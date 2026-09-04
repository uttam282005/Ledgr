package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/razorpay-hack/ai-finance-controller/internal/models"
	"github.com/razorpay-hack/ai-finance-controller/internal/reconciliation"
)

// Repository handles database queries and persistence for reconciliation runs.
type Repository struct {
	db *sql.DB
}

// NewRepository creates a new Repository.
func NewRepository(database *sql.DB) *Repository {
	return &Repository{db: database}
}

// GetLatestRunID returns the run_id of the most recently created run.
func (r *Repository) GetLatestRunID(ctx context.Context) (uuid.UUID, error) {
	var runID uuid.UUID
	query := `SELECT run_id FROM reconciliation_runs ORDER BY started_at DESC LIMIT 1;`
	err := r.db.QueryRowContext(ctx, query).Scan(&runID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to get latest run_id: %w", err)
	}
	return runID, nil
}

// LoadSourceData fetches all internal transactions, settlements, and bank statements for a run.
func (r *Repository) LoadSourceData(
	ctx context.Context,
	runID uuid.UUID,
) ([]models.InternalTransaction, []models.SettlementRecord, []models.BankStatement, error) {
	// 1. Internals
	intQuery := `
		SELECT id, run_id, amount_paise, currency, transaction_date, merchant_id, reference_id, created_at
		FROM internal_transactions
		WHERE run_id = $1
		ORDER BY id ASC;
	`
	intRows, err := r.db.QueryContext(ctx, intQuery, runID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed querying internals: %w", err)
	}
	defer intRows.Close()

	var internals []models.InternalTransaction
	for intRows.Next() {
		var it models.InternalTransaction
		var ref sql.NullString
		if err := intRows.Scan(&it.ID, &it.RunID, &it.AmountPaise, &it.Currency, &it.TransactionDate, &it.MerchantID, &ref, &it.CreatedAt); err != nil {
			return nil, nil, nil, fmt.Errorf("failed scanning internal record: %w", err)
		}
		if ref.Valid {
			it.ReferenceID = &ref.String
		}
		internals = append(internals, it)
	}

	// 2. Settlements
	setQuery := `
		SELECT id, run_id, settled_amount_paise, currency, settlement_date, merchant_id, reference_id, batch_id, created_at
		FROM settlement_records
		WHERE run_id = $1
		ORDER BY id ASC;
	`
	setRows, err := r.db.QueryContext(ctx, setQuery, runID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed querying settlements: %w", err)
	}
	defer setRows.Close()

	var settlements []models.SettlementRecord
	for setRows.Next() {
		var s models.SettlementRecord
		var ref sql.NullString
		if err := setRows.Scan(&s.ID, &s.RunID, &s.SettledAmountPaise, &s.Currency, &s.SettlementDate, &s.MerchantID, &ref, &s.BatchID, &s.CreatedAt); err != nil {
			return nil, nil, nil, fmt.Errorf("failed scanning settlement record: %w", err)
		}
		if ref.Valid {
			s.ReferenceID = &ref.String
		}
		settlements = append(settlements, s)
	}

	// 3. Bank Statements
	bankQuery := `
		SELECT id, run_id, credited_amount_paise, currency, credit_date, merchant_id, batch_reference, narration, created_at
		FROM bank_statements
		WHERE run_id = $1
		ORDER BY id ASC;
	`
	bankRows, err := r.db.QueryContext(ctx, bankQuery, runID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed querying bank statements: %w", err)
	}
	defer bankRows.Close()

	var bankStatements []models.BankStatement
	for bankRows.Next() {
		var bs models.BankStatement
		var batchRef sql.NullString
		if err := bankRows.Scan(&bs.ID, &bs.RunID, &bs.CreditedAmountPaise, &bs.Currency, &bs.CreditDate, &bs.MerchantID, &batchRef, &bs.Narration, &bs.CreatedAt); err != nil {
			return nil, nil, nil, fmt.Errorf("failed scanning bank statement: %w", err)
		}
		if batchRef.Valid {
			bs.BatchReference = &batchRef.String
		}
		bankStatements = append(bankStatements, bs)
	}

	return internals, settlements, bankStatements, nil
}

// SaveReconciliationResults stores matches, exceptions, audit logs, and updates run summary in a single transaction.
func (r *Repository) SaveReconciliationResults(
	ctx context.Context,
	out *reconciliation.ReconciliationOutput,
) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	completedAt := time.Now().UTC()

	// 1. Update reconciliation_runs
	updateRunQuery := `
		UPDATE reconciliation_runs SET
			completed_at = $1,
			hop1_matched_count = $2,
			hop2_matched_count = $3,
			full_chain_count = $4,
			exception_count = $5,
			unresolved_amount_paise = $6,
			engine_duration_ms = $7,
			throughput_records_per_sec = $8
		WHERE run_id = $9;
	`
	_, err = tx.ExecContext(ctx, updateRunQuery,
		completedAt,
		out.Hop1MatchedCount,
		out.Hop2MatchedCount,
		out.FullChainCount,
		out.ExceptionCount,
		out.UnresolvedExposure,
		out.EngineDurationMs,
		out.ThroughputPerSecond,
		out.RunID,
	)
	if err != nil {
		return fmt.Errorf("failed updating reconciliation run: %w", err)
	}

	// 2. Bulk Insert reconciliation_matches
	matchStmt, err := tx.PrepareContext(ctx, pq.CopyIn(
		"reconciliation_matches",
		"id", "run_id", "internal_id", "settlement_id", "bank_statement_id",
		"hop1_rule", "hop2_rule", "hop1_confidence", "hop2_confidence",
		"reconciliation_status", "fee_delta_paise", "expected_bank_amount_paise",
		"actual_bank_amount_paise", "bank_delta_paise", "created_at",
	))
	if err != nil {
		return fmt.Errorf("failed preparing matches copy statement: %w", err)
	}
	defer matchStmt.Close()

	for _, m := range out.Matches {
		var setID, bankID, hop1Rule, hop2Rule interface{}
		var feeDelta, expBank, actBank, bankDelta interface{}

		if m.SettlementID != nil {
			setID = *m.SettlementID
		}
		if m.BankStatementID != nil {
			bankID = *m.BankStatementID
		}
		if m.Hop1Rule != nil {
			hop1Rule = *m.Hop1Rule
		}
		if m.Hop2Rule != nil {
			hop2Rule = *m.Hop2Rule
		}
		if m.FeeDeltaPaise != nil {
			feeDelta = *m.FeeDeltaPaise
		}
		if m.ExpectedBankAmountPaise != nil {
			expBank = *m.ExpectedBankAmountPaise
		}
		if m.ActualBankAmountPaise != nil {
			actBank = *m.ActualBankAmountPaise
		}
		if m.BankDeltaPaise != nil {
			bankDelta = *m.BankDeltaPaise
		}

		if _, err := matchStmt.ExecContext(ctx,
			m.ID, m.RunID, m.InternalID, setID, bankID,
			hop1Rule, hop2Rule, m.Hop1Confidence, m.Hop2Confidence,
			string(m.ReconciliationStatus), feeDelta, expBank, actBank, bankDelta, m.CreatedAt,
		); err != nil {
			return fmt.Errorf("failed queueing reconciliation match: %w", err)
		}
	}
	if _, err := matchStmt.ExecContext(ctx); err != nil {
		return fmt.Errorf("failed executing matches copy: %w", err)
	}

	// 3. Bulk Insert exceptions
	excStmt, err := tx.PrepareContext(ctx, pq.CopyIn(
		"exceptions",
		"id", "run_id", "record_id", "source", "category", "hop", "reason",
		"expected_amount_paise", "actual_amount_paise", "delta_paise", "exposure_paise",
		"ai_status", "ai_summary", "ai_action", "ai_confidence", "created_at",
	))
	if err != nil {
		return fmt.Errorf("failed preparing exceptions copy statement: %w", err)
	}
	defer excStmt.Close()

	for _, e := range out.Exceptions {
		var expAmt, actAmt, deltaAmt, expPaise interface{}
		var aiSum, aiAct, aiConf interface{}

		if e.ExpectedAmountPaise != nil {
			expAmt = *e.ExpectedAmountPaise
		}
		if e.ActualAmountPaise != nil {
			actAmt = *e.ActualAmountPaise
		}
		if e.DeltaPaise != nil {
			deltaAmt = *e.DeltaPaise
		}
		if e.ExposurePaise != nil {
			expPaise = *e.ExposurePaise
		}
		if e.AISummary != nil {
			aiSum = *e.AISummary
		}
		if e.AIAction != nil {
			aiAct = *e.AIAction
		}
		if e.AIConfidence != nil {
			aiConf = *e.AIConfidence
		}

		if _, err := excStmt.ExecContext(ctx,
			e.ID, e.RunID, e.RecordID, e.Source, e.Category, e.Hop, e.Reason,
			expAmt, actAmt, deltaAmt, expPaise,
			string(e.AIStatus), aiSum, aiAct, aiConf, e.CreatedAt,
		); err != nil {
			return fmt.Errorf("failed queueing exception: %w", err)
		}
	}
	if _, err := excStmt.ExecContext(ctx); err != nil {
		return fmt.Errorf("failed executing exceptions copy: %w", err)
	}

	// 4. Bulk Insert audit_log
	auditStmt, err := tx.PrepareContext(ctx, pq.CopyIn(
		"audit_log",
		"decision_id", "run_id", "record_ids", "rule_applied",
		"fields_compared", "candidates_considered", "outcome",
		"ai_reasoning", "ai_model", "ai_prompt_version", "ai_latency_ms", "created_at",
	))
	if err != nil {
		return fmt.Errorf("failed preparing audit copy statement: %w", err)
	}
	defer auditStmt.Close()

	for _, a := range out.AuditLogs {
		var aiReason, aiModel, aiPrompt interface{}
		var aiLatency interface{}

		if a.AIReasoning != nil {
			aiReason = *a.AIReasoning
		}
		if a.AIModel != nil {
			aiModel = *a.AIModel
		}
		if a.AIPromptVersion != nil {
			aiPrompt = *a.AIPromptVersion
		}
		if a.AILatencyMs != nil {
			aiLatency = *a.AILatencyMs
		}

		if _, err := auditStmt.ExecContext(ctx,
			a.DecisionID, a.RunID, pq.Array(a.RecordIDs), a.RuleApplied,
			a.FieldsCompared, a.CandidatesConsidered, a.Outcome,
			aiReason, aiModel, aiPrompt, aiLatency, a.CreatedAt,
		); err != nil {
			return fmt.Errorf("failed queueing audit log: %w", err)
		}
	}
	if _, err := auditStmt.ExecContext(ctx); err != nil {
		return fmt.Errorf("failed executing audit copy: %w", err)
	}

	return tx.Commit()
}

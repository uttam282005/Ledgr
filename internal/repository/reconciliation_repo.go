package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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

	// 0. Clean slate: remove previous matches, exceptions, and audit logs for this run
	if _, err := tx.ExecContext(ctx, `DELETE FROM reconciliation_matches WHERE run_id = $1;`, out.RunID); err != nil {
		return fmt.Errorf("failed deleting existing matches: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM exceptions WHERE run_id = $1;`, out.RunID); err != nil {
		return fmt.Errorf("failed deleting existing exceptions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM audit_log WHERE run_id = $1;`, out.RunID); err != nil {
		return fmt.Errorf("failed deleting existing audit logs: %w", err)
	}

	// 1. Update reconciliation_runs
	updateRunQuery := `
		UPDATE reconciliation_runs SET
			status = 'RECONCILED',
			last_reconciled_at = $1,
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

// RunListItem represents a summary item for a run.
type RunListItem struct {
	RunID                   uuid.UUID  `json:"run_id"`
	Name                    string     `json:"name"`
	Status                  string     `json:"status"`
	CreatedAt               time.Time  `json:"created_at"`
	LastReconciledAt        *time.Time `json:"last_reconciled_at,omitempty"`
	UploadCount             int        `json:"upload_count"`
	InternalCount           int        `json:"internal_count"`
	SettlementCount         int        `json:"settlement_count"`
	BankCount               int        `json:"bank_count"`
	Hop1MatchedCount        int        `json:"hop1_matched_count"`
	Hop2MatchedCount        int        `json:"hop2_matched_count"`
	FullChainCount          int        `json:"full_chain_count"`
	ExceptionCount          int        `json:"exception_count"`
	UnresolvedAmountPaise   int64      `json:"unresolved_amount_paise"`
	UnresolvedAmountINR     float64    `json:"unresolved_amount_inr"`
	ThroughputRecordsPerSec float64    `json:"throughput_records_per_sec"`
}

// RunDetail represents detailed metadata, metrics, uploads, and exception breakdown for a run.
type RunDetail struct {
	RunID                   uuid.UUID          `json:"run_id"`
	Name                    string             `json:"name"`
	Status                  string             `json:"status"`
	CreatedAt               time.Time          `json:"created_at"`
	LastReconciledAt        *time.Time         `json:"last_reconciled_at,omitempty"`
	InternalCount           int                `json:"internal_count"`
	SettlementCount         int                `json:"settlement_count"`
	BankCount               int                `json:"bank_count"`
	Hop1MatchedCount        int                `json:"hop1_matched_count"`
	Hop2MatchedCount        int                `json:"hop2_matched_count"`
	FullChainCount          int                `json:"full_chain_count"`
	ExceptionCount          int                `json:"exception_count"`
	UnresolvedAmountPaise   int64              `json:"unresolved_amount_paise"`
	UnresolvedAmountINR     float64            `json:"unresolved_amount_inr"`
	EngineDurationMs        int64              `json:"engine_duration_ms"`
	ThroughputRecordsPerSec float64            `json:"throughput_records_per_sec"`
	Uploads                 []models.RunUpload `json:"uploads"`
	ExceptionBreakdown      map[string]int     `json:"exception_breakdown"`
}

// CreateRun inserts a new isolated reconciliation run.
func (r *Repository) CreateRun(ctx context.Context, name string) (*models.ReconciliationRun, error) {
	runID := uuid.New()
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		trimmed = fmt.Sprintf("Run %s", time.Now().Format("02 Jan 15:04"))
	}

	query := `
		INSERT INTO reconciliation_runs (
			run_id, name, status, created_at, started_at, seed, dataset_version, engine_version
		) VALUES ($1, $2, 'DRAFT', NOW(), NOW(), 0, 'custom-upload', 'v1.0.0')
		RETURNING run_id, name, status, created_at, started_at;
	`
	var run models.ReconciliationRun
	err := r.db.QueryRowContext(ctx, query, runID, trimmed).Scan(
		&run.RunID, &run.Name, &run.Status, &run.CreatedAt, &run.StartedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed creating run: %w", err)
	}
	return &run, nil
}

// ListRuns returns all runs ordered by created_at DESC with upload counts.
func (r *Repository) ListRuns(ctx context.Context) ([]RunListItem, error) {
	query := `
		SELECT
			r.run_id,
			COALESCE(NULLIF(r.name, ''), 'Run ' || SUBSTRING(r.run_id::text, 1, 8)) AS name,
			r.status,
			r.created_at,
			r.last_reconciled_at,
			COALESCE(COUNT(u.upload_id), 0) AS upload_count,
			r.internal_count,
			r.settlement_count,
			r.bank_count,
			r.hop1_matched_count,
			r.hop2_matched_count,
			r.full_chain_count,
			r.exception_count,
			r.unresolved_amount_paise,
			r.throughput_records_per_sec
		FROM reconciliation_runs r
		LEFT JOIN run_uploads u ON r.run_id = u.run_id
		GROUP BY r.run_id
		ORDER BY r.created_at DESC;
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed querying runs list: %w", err)
	}
	defer rows.Close()

	var items []RunListItem
	for rows.Next() {
		var it RunListItem
		if err := rows.Scan(
			&it.RunID, &it.Name, &it.Status, &it.CreatedAt, &it.LastReconciledAt,
			&it.UploadCount, &it.InternalCount, &it.SettlementCount, &it.BankCount,
			&it.Hop1MatchedCount, &it.Hop2MatchedCount, &it.FullChainCount,
			&it.ExceptionCount, &it.UnresolvedAmountPaise, &it.ThroughputRecordsPerSec,
		); err != nil {
			return nil, fmt.Errorf("failed scanning run list item: %w", err)
		}
		it.UnresolvedAmountINR = float64(it.UnresolvedAmountPaise) / 100.0
		items = append(items, it)
	}
	return items, nil
}

// GetRunDetail returns full run information including its uploaded files.
func (r *Repository) GetRunDetail(ctx context.Context, runID uuid.UUID) (*RunDetail, error) {
	query := `
		SELECT
			run_id,
			COALESCE(NULLIF(name, ''), 'Run ' || SUBSTRING(run_id::text, 1, 8)) AS name,
			status,
			created_at,
			last_reconciled_at,
			internal_count,
			settlement_count,
			bank_count,
			hop1_matched_count,
			hop2_matched_count,
			full_chain_count,
			exception_count,
			unresolved_amount_paise,
			engine_duration_ms,
			throughput_records_per_sec
		FROM reconciliation_runs
		WHERE run_id = $1;
	`
	var d RunDetail
	err := r.db.QueryRowContext(ctx, query, runID).Scan(
		&d.RunID, &d.Name, &d.Status, &d.CreatedAt, &d.LastReconciledAt,
		&d.InternalCount, &d.SettlementCount, &d.BankCount,
		&d.Hop1MatchedCount, &d.Hop2MatchedCount, &d.FullChainCount,
		&d.ExceptionCount, &d.UnresolvedAmountPaise,
		&d.EngineDurationMs, &d.ThroughputRecordsPerSec,
	)
	if err != nil {
		return nil, err
	}
	d.UnresolvedAmountINR = float64(d.UnresolvedAmountPaise) / 100.0
	d.Uploads = []models.RunUpload{}
	d.ExceptionBreakdown = make(map[string]int)

	// Fetch uploads for this run
	upQuery := `
		SELECT upload_id, run_id, source_type, filename, column_mapping, row_count, uploaded_at
		FROM run_uploads
		WHERE run_id = $1
		ORDER BY uploaded_at ASC;
	`
	upRows, err := r.db.QueryContext(ctx, upQuery, runID)
	if err == nil {
		defer upRows.Close()
		for upRows.Next() {
			var u models.RunUpload
			if err := upRows.Scan(
				&u.UploadID, &u.RunID, &u.SourceType, &u.Filename,
				&u.ColumnMapping, &u.RowCount, &u.UploadedAt,
			); err == nil {
				d.Uploads = append(d.Uploads, u)
			}
		}
	}

	// Fetch exception category breakdown
	excQuery := `SELECT category, COUNT(*) FROM exceptions WHERE run_id = $1 GROUP BY category;`
	excRows, err := r.db.QueryContext(ctx, excQuery, runID)
	if err == nil {
		defer excRows.Close()
		for excRows.Next() {
			var cat string
			var count int
			if err := excRows.Scan(&cat, &count); err == nil {
				d.ExceptionBreakdown[cat] = count
			}
		}
	}

	return &d, nil
}

// DeleteRun deletes a run and all associated records via CASCADE.
func (r *Repository) DeleteRun(ctx context.Context, runID uuid.UUID) error {
	query := `DELETE FROM reconciliation_runs WHERE run_id = $1;`
	res, err := r.db.ExecContext(ctx, query, runID)
	if err != nil {
		return fmt.Errorf("failed deleting run: %w", err)
	}
	rowsAff, _ := res.RowsAffected()
	if rowsAff == 0 {
		return fmt.Errorf("run not found")
	}
	return nil
}

// RecordUpload records an uploaded file into run_uploads and marks the run STALE if previously RECONCILED.
func (r *Repository) RecordUpload(ctx context.Context, upload models.RunUpload) error {
	query := `
		INSERT INTO run_uploads (
			upload_id, run_id, source_type, filename, column_mapping, row_count, uploaded_at
		) VALUES ($1, $2, $3, $4, $5, $6, NOW());
	`
	if _, err := r.db.ExecContext(ctx, query,
		upload.UploadID, upload.RunID, upload.SourceType, upload.Filename,
		upload.ColumnMapping, upload.RowCount,
	); err != nil {
		return fmt.Errorf("failed recording upload: %w", err)
	}

	// Invalidate previous results: if run was RECONCILED, flip to STALE
	updateQuery := `
		UPDATE reconciliation_runs
		SET status = 'STALE'
		WHERE run_id = $1 AND status = 'RECONCILED';
	`
	_, _ = r.db.ExecContext(ctx, updateQuery, upload.RunID)
	return nil
}


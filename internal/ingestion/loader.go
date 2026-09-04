package ingestion

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lib/pq"
	"github.com/razorpay-hack/ai-finance-controller/internal/generator"
)

// LoadDataset stores the generated dataset records into PostgreSQL inside a single transaction.
func LoadDataset(ctx context.Context, database *sql.DB, dataset *generator.Dataset) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Insert reconciliation_runs
	runQuery := `
		INSERT INTO reconciliation_runs (
			run_id, seed, dataset_version, engine_version, started_at,
			internal_count, settlement_count, bank_count, source_records_processed
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);
	`
	_, err = tx.ExecContext(ctx, runQuery,
		dataset.Run.RunID,
		dataset.Run.Seed,
		dataset.Run.DatasetVersion,
		dataset.Run.EngineVersion,
		dataset.Run.StartedAt,
		dataset.Run.InternalCount,
		dataset.Run.SettlementCount,
		dataset.Run.BankCount,
		dataset.Run.SourceRecordsProcessed,
	)
	if err != nil {
		return fmt.Errorf("failed inserting reconciliation run: %w", err)
	}

	// 2. Bulk insert internal_transactions using pq.CopyIn
	intStmt, err := tx.PrepareContext(ctx, pq.CopyIn(
		"internal_transactions",
		"id", "run_id", "amount_paise", "currency", "transaction_date", "merchant_id", "reference_id", "created_at",
	))
	if err != nil {
		return fmt.Errorf("failed to prepare internal copy statement: %w", err)
	}
	defer intStmt.Close()

	for _, item := range dataset.Internals {
		var refVal interface{}
		if item.ReferenceID != nil {
			refVal = *item.ReferenceID
		}
		if _, err := intStmt.ExecContext(ctx, item.ID, item.RunID, item.AmountPaise, item.Currency, item.TransactionDate, item.MerchantID, refVal, item.CreatedAt); err != nil {
			return fmt.Errorf("failed to queue internal transaction: %w", err)
		}
	}
	if _, err := intStmt.ExecContext(ctx); err != nil {
		return fmt.Errorf("failed to execute internal copy: %w", err)
	}

	// 3. Bulk insert settlement_records using pq.CopyIn
	setStmt, err := tx.PrepareContext(ctx, pq.CopyIn(
		"settlement_records",
		"id", "run_id", "settled_amount_paise", "currency", "settlement_date", "merchant_id", "reference_id", "batch_id", "created_at",
	))
	if err != nil {
		return fmt.Errorf("failed to prepare settlement copy statement: %w", err)
	}
	defer setStmt.Close()

	for _, item := range dataset.Settlements {
		var refVal interface{}
		if item.ReferenceID != nil {
			refVal = *item.ReferenceID
		}
		if _, err := setStmt.ExecContext(ctx, item.ID, item.RunID, item.SettledAmountPaise, item.Currency, item.SettlementDate, item.MerchantID, refVal, item.BatchID, item.CreatedAt); err != nil {
			return fmt.Errorf("failed to queue settlement record: %w", err)
		}
	}
	if _, err := setStmt.ExecContext(ctx); err != nil {
		return fmt.Errorf("failed to execute settlement copy: %w", err)
	}

	// 4. Bulk insert bank_statements using pq.CopyIn
	bankStmt, err := tx.PrepareContext(ctx, pq.CopyIn(
		"bank_statements",
		"id", "run_id", "credited_amount_paise", "currency", "credit_date", "merchant_id", "batch_reference", "narration", "created_at",
	))
	if err != nil {
		return fmt.Errorf("failed to prepare bank copy statement: %w", err)
	}
	defer bankStmt.Close()

	for _, item := range dataset.BankStatements {
		var batchRefVal interface{}
		if item.BatchReference != nil {
			batchRefVal = *item.BatchReference
		}
		if _, err := bankStmt.ExecContext(ctx, item.ID, item.RunID, item.CreditedAmountPaise, item.Currency, item.CreditDate, item.MerchantID, batchRefVal, item.Narration, item.CreatedAt); err != nil {
			return fmt.Errorf("failed to queue bank statement: %w", err)
		}
	}
	if _, err := bankStmt.ExecContext(ctx); err != nil {
		return fmt.Errorf("failed to execute bank copy: %w", err)
	}

	return tx.Commit()
}

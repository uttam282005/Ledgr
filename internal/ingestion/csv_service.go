package ingestion

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// CachedFile represents an uploaded file waiting for mapping confirmation.
type CachedFile struct {
	ID         string
	SourceType string
	Filename   string
	Data       *RawCSVData
	CreatedAt  time.Time
}

// CSVService manages file analysis caching, AI mapping, and batch upsert into PostgreSQL.
type CSVService struct {
	db               *sql.DB
	nvidiaAPIKey     string
	nvidiaNIMBaseURL string
	nvidiaNIMModel   string
	engineVersion    string
	cacheMu          sync.RWMutex
	fileCache        map[string]*CachedFile
}

// NewCSVService creates a new CSV ingestion service.
func NewCSVService(
	db *sql.DB,
	nvidiaAPIKey string,
	nvidiaNIMBaseURL string,
	nvidiaNIMModel string,
	engineVersion string,
) *CSVService {
	s := &CSVService{
		db:               db,
		nvidiaAPIKey:     nvidiaAPIKey,
		nvidiaNIMBaseURL: nvidiaNIMBaseURL,
		nvidiaNIMModel:   nvidiaNIMModel,
		engineVersion:    engineVersion,
		fileCache:        make(map[string]*CachedFile),
	}

	// Periodic cleanup of expired cached files (older than 30 mins)
	go s.startCacheEviction(30 * time.Minute)

	return s
}

func (s *CSVService) startCacheEviction(ttl time.Duration) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.cacheMu.Lock()
		now := time.Now()
		for id, f := range s.fileCache {
			if now.Sub(f.CreatedAt) > ttl {
				delete(s.fileCache, id)
			}
		}
		s.cacheMu.Unlock()
	}
}

// AnalyzeCSV cleans raw CSV, inspects headers and sample rows, infers column mapping using AI,
// and caches the cleaned content for user confirmation.
func (s *CSVService) AnalyzeCSV(
	ctx context.Context,
	sourceType string,
	filename string,
	rawBytes []byte,
) (*MappingResult, error) {
	if _, ok := CanonicalFields[sourceType]; !ok {
		return nil, fmt.Errorf("invalid source_type '%s' (must be 'internal', 'settlement', or 'bank')", sourceType)
	}

	cleaned, err := CleanAndInspectCSV(rawBytes)
	if err != nil {
		return nil, fmt.Errorf("CSV inspection failed: %w", err)
	}

	// Run AI inference (NVIDIA NIM or heuristic fallback)
	mappingResult, err := InferColumnMapping(
		ctx,
		sourceType,
		cleaned.Headers,
		cleaned.SampleRows,
		s.nvidiaAPIKey,
		s.nvidiaNIMBaseURL,
		s.nvidiaNIMModel,
	)
	if err != nil {
		return nil, fmt.Errorf("AI mapping failed: %w", err)
	}

	fileID := uuid.New().String()
	mappingResult.FileID = fileID
	mappingResult.Filename = filename

	// Merge cleaner warnings
	mappingResult.Warnings = append(cleaned.Warnings, mappingResult.Warnings...)

	// Cache file for commit step
	s.cacheMu.Lock()
	s.fileCache[fileID] = &CachedFile{
		ID:         fileID,
		SourceType: sourceType,
		Filename:   filename,
		Data:       cleaned,
		CreatedAt:  time.Now(),
	}
	s.cacheMu.Unlock()

	return mappingResult, nil
}

// CommitCSV validates the mapping across all rows and batch upserts into PostgreSQL.
func (s *CSVService) CommitCSV(
	ctx context.Context,
	req CommitRequest,
) (*IngestSummary, error) {
	s.cacheMu.RLock()
	cached, ok := s.fileCache[req.FileID]
	s.cacheMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("file session expired or not found; please upload the file again")
	}

	// Determine or generate runID
	var runID uuid.UUID
	var err error
	if req.RunID != "" {
		runID, err = uuid.Parse(req.RunID)
		if err != nil {
			return nil, fmt.Errorf("invalid run_id: %w", err)
		}
	} else {
		runID = uuid.New()
	}

	// Ensure run row exists in reconciliation_runs
	if err := s.ensureRunExists(ctx, runID); err != nil {
		return nil, fmt.Errorf("failed ensuring run exists: %w", err)
	}

	// Validate and parse all rows
	parsed, err := ValidateAndParseCSV(
		cached.Data.CleanedContent,
		req.SourceType,
		req.Mapping,
		req.DateFormat,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// Batch upsert into PostgreSQL in chunks of 100
	if err := s.batchUpsertData(ctx, runID, req.SourceType, parsed); err != nil {
		return nil, fmt.Errorf("database upsert failed: %w", err)
	}

	// Record in run_uploads
	mappingJSON, _ := json.Marshal(req.Mapping)
	uploadID := uuid.New()
	insertUploadQuery := `
		INSERT INTO run_uploads (
			upload_id, run_id, source_type, filename, column_mapping, row_count, uploaded_at
		) VALUES ($1, $2, $3, $4, $5, $6, NOW());
	`
	_, _ = s.db.ExecContext(ctx, insertUploadQuery, uploadID, runID, req.SourceType, cached.Filename, mappingJSON, parsed.RowsIngested)

	// Invalidate previous results: if run was RECONCILED, flip to STALE
	_, _ = s.db.ExecContext(ctx, `UPDATE reconciliation_runs SET status = 'STALE' WHERE run_id = $1 AND status = 'RECONCILED';`, runID)

	// Update reconciliation_runs counts
	if err := s.updateRunCounts(ctx, runID); err != nil {
		return nil, fmt.Errorf("failed updating run statistics: %w", err)
	}

	status, _ := s.GetSourcesStatus(ctx, runID)
	intCount, setCount, bnkCount, allComplete := 0, 0, 0, false
	if status != nil {
		intCount = status.InternalCount
		setCount = status.SettlementCount
		bnkCount = status.BankCount
		allComplete = status.AllSourcesUploaded
	}

	// Delete from cache
	s.cacheMu.Lock()
	delete(s.fileCache, req.FileID)
	s.cacheMu.Unlock()

	summary := &IngestSummary{
		Status:             "success",
		RunID:              runID.String(),
		SourceType:         req.SourceType,
		Filename:           cached.Filename,
		RowsIngested:       parsed.RowsIngested,
		RowsSkipped:        parsed.RowsSkipped,
		SkippedReasons:     parsed.SkippedReasons,
		Message:            fmt.Sprintf("Successfully ingested %d %s record(s) into database for run %s", parsed.RowsIngested, req.SourceType, runID),
		InternalCount:      intCount,
		SettlementCount:    setCount,
		BankCount:          bnkCount,
		AllSourcesUploaded: allComplete,
	}

	return summary, nil
}

func (s *CSVService) ensureRunExists(ctx context.Context, runID uuid.UUID) error {
	query := `
		INSERT INTO reconciliation_runs (
			run_id, name, status, seed, dataset_version, engine_version, started_at, created_at
		) VALUES ($1, $2, 'DRAFT', 0, 'custom-csv-upload', $3, NOW(), NOW())
		ON CONFLICT (run_id) DO NOTHING;
	`
	defaultName := fmt.Sprintf("Run %s", time.Now().Format("02 Jan 15:04"))
	_, err := s.db.ExecContext(ctx, query, runID, defaultName, s.engineVersion)
	return err
}

func (s *CSVService) batchUpsertData(
	ctx context.Context,
	runID uuid.UUID,
	sourceType string,
	parsed *ParsedUploadData,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	const batchSize = 100

	switch sourceType {
	case SourceInternal:
		query := `
			INSERT INTO internal_transactions (
				id, run_id, amount_paise, currency, transaction_date, merchant_id, reference_id, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (run_id, id) DO UPDATE SET
				amount_paise = EXCLUDED.amount_paise,
				currency = EXCLUDED.currency,
				transaction_date = EXCLUDED.transaction_date,
				merchant_id = EXCLUDED.merchant_id,
				reference_id = EXCLUDED.reference_id,
				created_at = EXCLUDED.created_at;
		`
		stmt, err := tx.PrepareContext(ctx, query)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for i := 0; i < len(parsed.Internals); i += batchSize {
			end := i + batchSize
			if end > len(parsed.Internals) {
				end = len(parsed.Internals)
			}
			for _, it := range parsed.Internals[i:end] {
				var refVal interface{}
				if it.ReferenceID != nil {
					refVal = *it.ReferenceID
				}
				if _, err := stmt.ExecContext(ctx, it.ID, it.RunID, it.AmountPaise, it.Currency, it.TransactionDate, it.MerchantID, refVal, it.CreatedAt); err != nil {
					return fmt.Errorf("failed upserting internal transaction %s: %w", it.ID, err)
				}
			}
		}

	case SourceSettlement:
		query := `
			INSERT INTO settlement_records (
				id, run_id, settled_amount_paise, currency, settlement_date, merchant_id, reference_id, batch_id, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (run_id, id) DO UPDATE SET
				settled_amount_paise = EXCLUDED.settled_amount_paise,
				currency = EXCLUDED.currency,
				settlement_date = EXCLUDED.settlement_date,
				merchant_id = EXCLUDED.merchant_id,
				reference_id = EXCLUDED.reference_id,
				batch_id = EXCLUDED.batch_id,
				created_at = EXCLUDED.created_at;
		`
		stmt, err := tx.PrepareContext(ctx, query)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for i := 0; i < len(parsed.Settlements); i += batchSize {
			end := i + batchSize
			if end > len(parsed.Settlements) {
				end = len(parsed.Settlements)
			}
			for _, st := range parsed.Settlements[i:end] {
				var refVal interface{}
				if st.ReferenceID != nil {
					refVal = *st.ReferenceID
				}
				if _, err := stmt.ExecContext(ctx, st.ID, st.RunID, st.SettledAmountPaise, st.Currency, st.SettlementDate, st.MerchantID, refVal, st.BatchID, st.CreatedAt); err != nil {
					return fmt.Errorf("failed upserting settlement record %s: %w", st.ID, err)
				}
			}
		}

	case SourceBank:
		query := `
			INSERT INTO bank_statements (
				id, run_id, credited_amount_paise, currency, credit_date, merchant_id, batch_reference, narration, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (run_id, id) DO UPDATE SET
				credited_amount_paise = EXCLUDED.credited_amount_paise,
				currency = EXCLUDED.currency,
				credit_date = EXCLUDED.credit_date,
				merchant_id = EXCLUDED.merchant_id,
				batch_reference = EXCLUDED.batch_reference,
				narration = EXCLUDED.narration,
				created_at = EXCLUDED.created_at;
		`
		stmt, err := tx.PrepareContext(ctx, query)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for i := 0; i < len(parsed.BankStatements); i += batchSize {
			end := i + batchSize
			if end > len(parsed.BankStatements) {
				end = len(parsed.BankStatements)
			}
			for _, bs := range parsed.BankStatements[i:end] {
				var batchRefVal interface{}
				if bs.BatchReference != nil {
					batchRefVal = *bs.BatchReference
				}
				if _, err := stmt.ExecContext(ctx, bs.ID, bs.RunID, bs.CreditedAmountPaise, bs.Currency, bs.CreditDate, bs.MerchantID, batchRefVal, bs.Narration, bs.CreatedAt); err != nil {
					return fmt.Errorf("failed upserting bank statement %s: %w", bs.ID, err)
				}
			}
		}
	}

	return tx.Commit()
}

func (s *CSVService) updateRunCounts(ctx context.Context, runID uuid.UUID) error {
	query := `
		UPDATE reconciliation_runs SET
			internal_count = (SELECT COUNT(*) FROM internal_transactions WHERE run_id = $1),
			settlement_count = (SELECT COUNT(*) FROM settlement_records WHERE run_id = $1),
			bank_count = (SELECT COUNT(*) FROM bank_statements WHERE run_id = $1),
			source_records_processed = (
				(SELECT COUNT(*) FROM internal_transactions WHERE run_id = $1) +
				(SELECT COUNT(*) FROM settlement_records WHERE run_id = $1) +
				(SELECT COUNT(*) FROM bank_statements WHERE run_id = $1)
			)
		WHERE run_id = $1;
	`
	_, err := s.db.ExecContext(ctx, query, runID)
	return err
}

// GetSourcesStatus returns the ingestion completeness across the 3 sources for a given runID.
func (s *CSVService) GetSourcesStatus(ctx context.Context, runID uuid.UUID) (*SourcesStatus, error) {
	var intCount, setCount, bnkCount int
	query := `
		SELECT
			COALESCE((SELECT COUNT(*) FROM internal_transactions WHERE run_id = $1), 0),
			COALESCE((SELECT COUNT(*) FROM settlement_records WHERE run_id = $1), 0),
			COALESCE((SELECT COUNT(*) FROM bank_statements WHERE run_id = $1), 0);
	`
	if err := s.db.QueryRowContext(ctx, query, runID).Scan(&intCount, &setCount, &bnkCount); err != nil {
		return nil, fmt.Errorf("failed fetching source counts: %w", err)
	}

	return &SourcesStatus{
		RunID:              runID.String(),
		InternalCount:      intCount,
		SettlementCount:    setCount,
		BankCount:          bnkCount,
		AllSourcesUploaded: intCount > 0 && setCount > 0 && bnkCount > 0,
	}, nil
}

// UploadCSV directly processes, AI maps, validates, and commits an uploaded CSV into a run in a single atomic workflow.
func (s *CSVService) UploadCSV(
	ctx context.Context,
	runID uuid.UUID,
	sourceType string,
	filename string,
	data []byte,
) (*IngestSummary, error) {
	if err := s.ensureRunExists(ctx, runID); err != nil {
		return nil, fmt.Errorf("failed ensuring run exists: %w", err)
	}

	cleaned, err := CleanAndInspectCSV(data)
	if err != nil {
		return nil, err
	}

	// Infer column mapping with AI or heuristic fallback
	mapResult, err := InferColumnMapping(ctx, sourceType, cleaned.Headers, cleaned.SampleRows, s.nvidiaAPIKey, s.nvidiaNIMBaseURL, s.nvidiaNIMModel)
	if err != nil {
		return nil, fmt.Errorf("failed inferring column mapping: %w", err)
	}

	parsed, err := ValidateAndParseCSV(cleaned.CleanedContent, sourceType, mapResult.Mapping, mapResult.DateFormat, runID)
	if err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	if err := s.batchUpsertData(ctx, runID, sourceType, parsed); err != nil {
		return nil, fmt.Errorf("database upsert failed: %w", err)
	}

	// Record in run_uploads
	mappingJSON, _ := json.Marshal(mapResult.Mapping)
	uploadID := uuid.New()
	insertUploadQuery := `
		INSERT INTO run_uploads (
			upload_id, run_id, source_type, filename, column_mapping, row_count, uploaded_at
		) VALUES ($1, $2, $3, $4, $5, $6, NOW());
	`
	_, _ = s.db.ExecContext(ctx, insertUploadQuery, uploadID, runID, sourceType, filename, mappingJSON, parsed.RowsIngested)

	// Invalidate previous results: if run was RECONCILED, flip to STALE
	_, _ = s.db.ExecContext(ctx, `UPDATE reconciliation_runs SET status = 'STALE' WHERE run_id = $1 AND status = 'RECONCILED';`, runID)

	if err := s.updateRunCounts(ctx, runID); err != nil {
		return nil, fmt.Errorf("failed updating run statistics: %w", err)
	}

	status, _ := s.GetSourcesStatus(ctx, runID)
	intCount, setCount, bnkCount, allComplete := 0, 0, 0, false
	if status != nil {
		intCount = status.InternalCount
		setCount = status.SettlementCount
		bnkCount = status.BankCount
		allComplete = status.AllSourcesUploaded
	}

	return &IngestSummary{
		Status:             "success",
		RunID:              runID.String(),
		SourceType:         sourceType,
		Filename:           filename,
		RowsIngested:       parsed.RowsIngested,
		RowsSkipped:        parsed.RowsSkipped,
		SkippedReasons:     parsed.SkippedReasons,
		Message:            fmt.Sprintf("Successfully ingested %d %s record(s) into database for run %s", parsed.RowsIngested, sourceType, runID),
		InternalCount:      intCount,
		SettlementCount:    setCount,
		BankCount:          bnkCount,
		AllSourcesUploaded: allComplete,
	}, nil
}


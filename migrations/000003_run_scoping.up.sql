-- AI Finance Controller Schema
-- Migration 000003: Run Scoping, Run Uploads, and Composite Isolation

-- 1. Extend reconciliation_runs with run metadata and status
ALTER TABLE reconciliation_runs ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';
ALTER TABLE reconciliation_runs ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'RECONCILED', 'STALE'));
ALTER TABLE reconciliation_runs ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE reconciliation_runs ADD COLUMN IF NOT EXISTS last_reconciled_at TIMESTAMPTZ;
ALTER TABLE reconciliation_runs ALTER COLUMN seed SET DEFAULT 0;
ALTER TABLE reconciliation_runs ALTER COLUMN dataset_version SET DEFAULT 'custom-upload';
ALTER TABLE reconciliation_runs ALTER COLUMN engine_version SET DEFAULT 'v1.0.0';

-- Backfill existing runs
UPDATE reconciliation_runs SET created_at = started_at WHERE created_at IS NULL;
UPDATE reconciliation_runs SET status = 'RECONCILED' WHERE completed_at IS NOT NULL;
UPDATE reconciliation_runs SET last_reconciled_at = completed_at WHERE completed_at IS NOT NULL;
UPDATE reconciliation_runs SET name = 'Seed ' || seed WHERE (name = '' OR name IS NULL) AND seed > 0;
UPDATE reconciliation_runs SET name = 'Run ' || SUBSTRING(run_id::text, 1, 8) WHERE (name = '' OR name IS NULL);

-- 2. Create run_uploads table to record individual file uploads scoped to a run
CREATE TABLE IF NOT EXISTS run_uploads (
    upload_id UUID PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES reconciliation_runs(run_id) ON DELETE CASCADE,
    source_type TEXT NOT NULL CHECK (source_type IN ('internal', 'settlement', 'bank')),
    filename TEXT NOT NULL,
    column_mapping JSONB NOT NULL DEFAULT '{}'::jsonb,
    row_count INTEGER NOT NULL DEFAULT 0,
    uploaded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_run_uploads_run_id ON run_uploads(run_id);
CREATE INDEX IF NOT EXISTS idx_run_uploads_source_type ON run_uploads(source_type);

-- 3. Create runs view for clean querying
CREATE OR REPLACE VIEW runs AS
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
FROM reconciliation_runs;

-- 4. Scope source table uniqueness by (run_id, id)
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'reconciliation_matches_internal_fkey'
    ) THEN
        ALTER TABLE reconciliation_matches DROP CONSTRAINT IF EXISTS reconciliation_matches_internal_id_fkey;
        ALTER TABLE reconciliation_matches DROP CONSTRAINT IF EXISTS reconciliation_matches_settlement_id_fkey;
        ALTER TABLE reconciliation_matches DROP CONSTRAINT IF EXISTS reconciliation_matches_bank_statement_id_fkey;

        ALTER TABLE internal_transactions DROP CONSTRAINT IF EXISTS internal_transactions_pkey;
        ALTER TABLE settlement_records DROP CONSTRAINT IF EXISTS settlement_records_pkey;
        ALTER TABLE bank_statements DROP CONSTRAINT IF EXISTS bank_statements_pkey;

        ALTER TABLE internal_transactions ADD PRIMARY KEY (run_id, id);
        ALTER TABLE settlement_records ADD PRIMARY KEY (run_id, id);
        ALTER TABLE bank_statements ADD PRIMARY KEY (run_id, id);

        ALTER TABLE reconciliation_matches ADD CONSTRAINT reconciliation_matches_internal_fkey
            FOREIGN KEY (run_id, internal_id) REFERENCES internal_transactions(run_id, id) ON DELETE CASCADE;
        ALTER TABLE reconciliation_matches ADD CONSTRAINT reconciliation_matches_settlement_fkey
            FOREIGN KEY (run_id, settlement_id) REFERENCES settlement_records(run_id, id) ON DELETE SET NULL (settlement_id);
        ALTER TABLE reconciliation_matches ADD CONSTRAINT reconciliation_matches_bank_fkey
            FOREIGN KEY (run_id, bank_statement_id) REFERENCES bank_statements(run_id, id) ON DELETE SET NULL (bank_statement_id);
    END IF;
END $$;

-- 5. QA role permissions and schema compatibility views
CREATE OR REPLACE VIEW matches AS SELECT * FROM reconciliation_matches;
CREATE OR REPLACE VIEW transactions AS SELECT * FROM internal_transactions;
CREATE OR REPLACE VIEW settlements AS SELECT * FROM settlement_records;
CREATE OR REPLACE VIEW banks AS SELECT * FROM bank_statements;
CREATE OR REPLACE VIEW active_merchants AS SELECT DISTINCT merchant_id FROM (
    SELECT merchant_id FROM internal_transactions
    UNION
    SELECT merchant_id FROM settlement_records
    UNION
    SELECT merchant_id FROM bank_statements
) m WHERE merchant_id IS NOT NULL AND merchant_id != '';
CREATE OR REPLACE VIEW merchants AS SELECT * FROM active_merchants;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'qa_readonly') THEN
        GRANT SELECT ON run_uploads TO qa_readonly;
        GRANT SELECT ON runs TO qa_readonly;
        GRANT SELECT ON matches TO qa_readonly;
        GRANT SELECT ON transactions TO qa_readonly;
        GRANT SELECT ON settlements TO qa_readonly;
        GRANT SELECT ON banks TO qa_readonly;
        GRANT SELECT ON active_merchants TO qa_readonly;
        GRANT SELECT ON merchants TO qa_readonly;
    END IF;
END $$;


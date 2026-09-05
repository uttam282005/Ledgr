-- Migration 000003 Down
DROP VIEW IF EXISTS runs CASCADE;
DROP TABLE IF EXISTS run_uploads CASCADE;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'reconciliation_matches_internal_fkey'
    ) THEN
        ALTER TABLE reconciliation_matches DROP CONSTRAINT IF EXISTS reconciliation_matches_internal_fkey;
        ALTER TABLE reconciliation_matches DROP CONSTRAINT IF EXISTS reconciliation_matches_settlement_fkey;
        ALTER TABLE reconciliation_matches DROP CONSTRAINT IF EXISTS reconciliation_matches_bank_fkey;

        ALTER TABLE internal_transactions DROP CONSTRAINT IF EXISTS internal_transactions_pkey;
        ALTER TABLE settlement_records DROP CONSTRAINT IF EXISTS settlement_records_pkey;
        ALTER TABLE bank_statements DROP CONSTRAINT IF EXISTS bank_statements_pkey;

        ALTER TABLE internal_transactions ADD PRIMARY KEY (id);
        ALTER TABLE settlement_records ADD PRIMARY KEY (id);
        ALTER TABLE bank_statements ADD PRIMARY KEY (id);

        ALTER TABLE reconciliation_matches ADD CONSTRAINT reconciliation_matches_internal_id_fkey
            FOREIGN KEY (internal_id) REFERENCES internal_transactions(id) ON DELETE CASCADE;
        ALTER TABLE reconciliation_matches ADD CONSTRAINT reconciliation_matches_settlement_id_fkey
            FOREIGN KEY (settlement_id) REFERENCES settlement_records(id) ON DELETE SET NULL;
        ALTER TABLE reconciliation_matches ADD CONSTRAINT reconciliation_matches_bank_statement_id_fkey
            FOREIGN KEY (bank_statement_id) REFERENCES bank_statements(id) ON DELETE SET NULL;
    END IF;
END $$;

ALTER TABLE reconciliation_runs DROP COLUMN IF EXISTS name;
ALTER TABLE reconciliation_runs DROP COLUMN IF EXISTS status;
ALTER TABLE reconciliation_runs DROP COLUMN IF EXISTS created_at;
ALTER TABLE reconciliation_runs DROP COLUMN IF EXISTS last_reconciled_at;

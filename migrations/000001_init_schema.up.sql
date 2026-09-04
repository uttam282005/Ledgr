-- AI Finance Controller Schema
-- Migration 000001: Initial Schema

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Table 1: reconciliation_runs
CREATE TABLE IF NOT EXISTS reconciliation_runs (
    run_id UUID PRIMARY KEY,
    seed BIGINT NOT NULL,
    dataset_version TEXT NOT NULL,
    engine_version TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    internal_count INTEGER NOT NULL DEFAULT 0,
    settlement_count INTEGER NOT NULL DEFAULT 0,
    bank_count INTEGER NOT NULL DEFAULT 0,
    hop1_matched_count INTEGER NOT NULL DEFAULT 0,
    hop2_matched_count INTEGER NOT NULL DEFAULT 0,
    full_chain_count INTEGER NOT NULL DEFAULT 0,
    exception_count INTEGER NOT NULL DEFAULT 0,
    unresolved_amount_paise BIGINT NOT NULL DEFAULT 0,
    engine_duration_ms BIGINT NOT NULL DEFAULT 0,
    source_records_processed INTEGER NOT NULL DEFAULT 0,
    throughput_records_per_sec NUMERIC NOT NULL DEFAULT 0
);

-- Table 2: internal_transactions
CREATE TABLE IF NOT EXISTS internal_transactions (
    id TEXT PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES reconciliation_runs(run_id) ON DELETE CASCADE,
    amount_paise BIGINT NOT NULL,
    currency TEXT NOT NULL DEFAULT 'INR',
    transaction_date TIMESTAMPTZ NOT NULL,
    merchant_id TEXT NOT NULL,
    reference_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_internal_run_id ON internal_transactions(run_id);
CREATE INDEX IF NOT EXISTS idx_internal_merchant_id ON internal_transactions(merchant_id);
CREATE INDEX IF NOT EXISTS idx_internal_reference_id ON internal_transactions(reference_id);
CREATE INDEX IF NOT EXISTS idx_internal_tx_date ON internal_transactions(transaction_date);

-- Table 3: settlement_records
CREATE TABLE IF NOT EXISTS settlement_records (
    id TEXT PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES reconciliation_runs(run_id) ON DELETE CASCADE,
    settled_amount_paise BIGINT NOT NULL,
    currency TEXT NOT NULL DEFAULT 'INR',
    settlement_date TIMESTAMPTZ NOT NULL,
    merchant_id TEXT NOT NULL,
    reference_id TEXT,
    batch_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_settlement_run_id ON settlement_records(run_id);
CREATE INDEX IF NOT EXISTS idx_settlement_merchant_id ON settlement_records(merchant_id);
CREATE INDEX IF NOT EXISTS idx_settlement_reference_id ON settlement_records(reference_id);
CREATE INDEX IF NOT EXISTS idx_settlement_batch_id ON settlement_records(batch_id);
CREATE INDEX IF NOT EXISTS idx_settlement_date ON settlement_records(settlement_date);

-- Table 4: bank_statements
CREATE TABLE IF NOT EXISTS bank_statements (
    id TEXT PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES reconciliation_runs(run_id) ON DELETE CASCADE,
    credited_amount_paise BIGINT NOT NULL,
    currency TEXT NOT NULL DEFAULT 'INR',
    credit_date TIMESTAMPTZ NOT NULL,
    merchant_id TEXT NOT NULL,
    batch_reference TEXT,
    narration TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_bank_run_id ON bank_statements(run_id);
CREATE INDEX IF NOT EXISTS idx_bank_merchant_id ON bank_statements(merchant_id);
CREATE INDEX IF NOT EXISTS idx_bank_batch_ref ON bank_statements(batch_reference);
CREATE INDEX IF NOT EXISTS idx_bank_credit_date ON bank_statements(credit_date);

-- Table 5: reconciliation_matches
CREATE TABLE IF NOT EXISTS reconciliation_matches (
    id UUID PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES reconciliation_runs(run_id) ON DELETE CASCADE,
    internal_id TEXT NOT NULL REFERENCES internal_transactions(id) ON DELETE CASCADE,
    settlement_id TEXT REFERENCES settlement_records(id) ON DELETE SET NULL,
    bank_statement_id TEXT REFERENCES bank_statements(id) ON DELETE SET NULL,
    hop1_rule TEXT,
    hop2_rule TEXT,
    hop1_confidence NUMERIC NOT NULL DEFAULT 0,
    hop2_confidence NUMERIC NOT NULL DEFAULT 0,
    reconciliation_status TEXT NOT NULL CHECK (reconciliation_status IN ('FULL', 'PARTIAL', 'UNMATCHED')),
    fee_delta_paise BIGINT,
    expected_bank_amount_paise BIGINT,
    actual_bank_amount_paise BIGINT,
    bank_delta_paise BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_matches_run_id ON reconciliation_matches(run_id);
CREATE INDEX IF NOT EXISTS idx_matches_internal_id ON reconciliation_matches(internal_id);
CREATE INDEX IF NOT EXISTS idx_matches_settlement_id ON reconciliation_matches(settlement_id);
CREATE INDEX IF NOT EXISTS idx_matches_bank_id ON reconciliation_matches(bank_statement_id);
CREATE INDEX IF NOT EXISTS idx_matches_status ON reconciliation_matches(reconciliation_status);

-- Table 6: exceptions
CREATE TABLE IF NOT EXISTS exceptions (
    id UUID PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES reconciliation_runs(run_id) ON DELETE CASCADE,
    record_id TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('internal', 'settlement', 'bank')),
    category TEXT NOT NULL,
    hop TEXT NOT NULL CHECK (hop IN ('HOP1', 'HOP2')),
    reason TEXT NOT NULL,
    expected_amount_paise BIGINT,
    actual_amount_paise BIGINT,
    delta_paise BIGINT,
    exposure_paise BIGINT,
    ai_status TEXT NOT NULL CHECK (ai_status IN ('NOT_REQUIRED', 'PENDING', 'SUCCEEDED', 'FAILED', 'UNAVAILABLE')),
    ai_summary TEXT,
    ai_action TEXT,
    ai_confidence TEXT CHECK (ai_confidence IS NULL OR ai_confidence IN ('HIGH', 'MEDIUM', 'LOW')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_exceptions_run_id ON exceptions(run_id);
CREATE INDEX IF NOT EXISTS idx_exceptions_record_id ON exceptions(record_id);
CREATE INDEX IF NOT EXISTS idx_exceptions_category ON exceptions(category);
CREATE INDEX IF NOT EXISTS idx_exceptions_hop ON exceptions(hop);
CREATE INDEX IF NOT EXISTS idx_exceptions_ai_status ON exceptions(ai_status);

-- Table 7: audit_log
CREATE TABLE IF NOT EXISTS audit_log (
    decision_id UUID PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES reconciliation_runs(run_id) ON DELETE CASCADE,
    record_ids TEXT[] NOT NULL,
    rule_applied TEXT NOT NULL,
    fields_compared JSONB NOT NULL DEFAULT '{}'::jsonb,
    candidates_considered JSONB NOT NULL DEFAULT '[]'::jsonb,
    outcome TEXT NOT NULL CHECK (outcome IN ('MATCHED', 'PARTIAL', 'EXCEPTION')),
    ai_reasoning TEXT,
    ai_model TEXT,
    ai_prompt_version TEXT,
    ai_latency_ms INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_audit_run_id ON audit_log(run_id);
CREATE INDEX IF NOT EXISTS idx_audit_decision_id ON audit_log(decision_id);
CREATE INDEX IF NOT EXISTS idx_audit_outcome ON audit_log(outcome);

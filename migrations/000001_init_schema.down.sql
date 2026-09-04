-- Migration 000001 Down
DROP TABLE IF EXISTS audit_log CASCADE;
DROP TABLE IF EXISTS exceptions CASCADE;
DROP TABLE IF EXISTS reconciliation_matches CASCADE;
DROP TABLE IF EXISTS bank_statements CASCADE;
DROP TABLE IF EXISTS settlement_records CASCADE;
DROP TABLE IF EXISTS internal_transactions CASCADE;
DROP TABLE IF EXISTS reconciliation_runs CASCADE;

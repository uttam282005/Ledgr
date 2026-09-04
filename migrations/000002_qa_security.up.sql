-- AI Finance Controller Schema
-- Migration 000002: Dual-Role Security & Read-Only Q&A Permissions

DO $$
DECLARE
    curr_db text;
BEGIN
    SELECT current_database() INTO curr_db;

    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'finance_app') THEN
        CREATE ROLE finance_app WITH LOGIN PASSWORD 'finance_app_secret';
    END IF;

    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'qa_readonly') THEN
        CREATE ROLE qa_readonly WITH LOGIN PASSWORD 'qa_readonly_secret';
    END IF;

    EXECUTE format('GRANT CONNECT ON DATABASE %I TO finance_app', curr_db);
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO qa_readonly', curr_db);
END
$$;

-- Grants for finance_app (Read/Write)
GRANT USAGE, CREATE ON SCHEMA public TO finance_app;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO finance_app;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO finance_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO finance_app;

-- Grants for qa_readonly (Strictly Read-Only on Application Tables)
GRANT USAGE ON SCHEMA public TO qa_readonly;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM qa_readonly;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM qa_readonly;

GRANT SELECT ON TABLE
    reconciliation_runs,
    internal_transactions,
    settlement_records,
    bank_statements,
    reconciliation_matches,
    exceptions,
    audit_log
TO qa_readonly;

ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO qa_readonly;

-- Transfer table ownership to finance_app
ALTER TABLE reconciliation_runs OWNER TO finance_app;
ALTER TABLE internal_transactions OWNER TO finance_app;
ALTER TABLE settlement_records OWNER TO finance_app;
ALTER TABLE bank_statements OWNER TO finance_app;
ALTER TABLE reconciliation_matches OWNER TO finance_app;
ALTER TABLE exceptions OWNER TO finance_app;
ALTER TABLE audit_log OWNER TO finance_app;

-- Restrict execution parameters for qa_readonly
ALTER ROLE qa_readonly SET statement_timeout = '3000ms';
ALTER ROLE qa_readonly SET default_transaction_read_only = 'on';

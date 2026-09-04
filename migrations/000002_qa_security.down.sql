-- Migration 000002 Down
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM qa_readonly;
REVOKE ALL ON SCHEMA public FROM qa_readonly;
REVOKE CONNECT ON DATABASE current_database() FROM qa_readonly;

package qa

import (
	"strings"
	"testing"
)

func TestValidateSQL_ValidQueries(t *testing.T) {
	validQueries := []struct {
		name     string
		sql      string
		hasLimit bool
	}{
		{
			name:     "Simple select on exceptions",
			sql:      "SELECT id, category, exposure_paise FROM exceptions WHERE run_id = 'c4217196-26df-4581-91ea-271d4924b1a4'",
			hasLimit: false,
		},
		{
			name:     "Aggregate with group by",
			sql:      "SELECT merchant_id, COUNT(*) as cnt, SUM(exposure_paise) as exposure FROM exceptions GROUP BY merchant_id ORDER BY cnt DESC LIMIT 5",
			hasLimit: true,
		},
		{
			name:     "Join between reconciliation_matches and internal_transactions",
			sql:      "SELECT m.id, i.amount_paise, m.reconciliation_status FROM reconciliation_matches m JOIN internal_transactions i ON m.internal_id = i.id WHERE m.run_id = '123'",
			hasLimit: false,
		},
		{
			name:     "CTE query",
			sql:      "WITH ranked AS (SELECT merchant_id, count(*) as c FROM exceptions GROUP BY merchant_id) SELECT * FROM ranked LIMIT 10",
			hasLimit: true,
		},
	}

	for _, tc := range validQueries {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ValidateSQL(tc.sql)
			if err != nil {
				t.Fatalf("expected query to be valid, got err: %v", err)
			}
			if !res.Valid {
				t.Fatalf("expected valid=true, got error: %s", res.Error)
			}
			if !tc.hasLimit && !strings.Contains(res.SanitizedQuery, "LIMIT 50") {
				t.Errorf("expected LIMIT 50 to be appended, got: %s", res.SanitizedQuery)
			}
		})
	}
}

func TestValidateSQL_RejectedQueries(t *testing.T) {
	invalidQueries := []struct {
		name string
		sql  string
	}{
		{
			name: "Multi-statement injection",
			sql:  "SELECT * FROM exceptions; DROP TABLE exceptions;",
		},
		{
			name: "DROP TABLE",
			sql:  "DROP TABLE reconciliation_runs CASCADE",
		},
		{
			name: "UPDATE statement",
			sql:  "UPDATE reconciliation_matches SET reconciliation_status = 'FULL'",
		},
		{
			name: "DELETE statement",
			sql:  "DELETE FROM exceptions WHERE exposure_paise > 0",
		},
		{
			name: "INSERT statement",
			sql:  "INSERT INTO internal_transactions (id, amount_paise) VALUES ('INT999', 1000)",
		},
		{
			name: "SELECT INTO",
			sql:  "SELECT * INTO backup_exceptions FROM exceptions",
		},
		{
			name: "FOR UPDATE locking",
			sql:  "SELECT * FROM internal_transactions FOR UPDATE",
		},
		{
			name: "System catalog pg_shadow",
			sql:  "SELECT usename, passwd FROM pg_shadow",
		},
		{
			name: "Information schema access",
			sql:  "SELECT * FROM information_schema.tables",
		},
		{
			name: "Unallowed table",
			sql:  "SELECT * FROM secret_user_table",
		},
		{
			name: "Dangerous pg_sleep function",
			sql:  "SELECT pg_sleep(5) FROM exceptions",
		},
	}

	for _, tc := range invalidQueries {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ValidateSQL(tc.sql)
			if err == nil && res.Valid {
				t.Fatalf("expected query to be REJECTED, but it succeeded: %s", tc.sql)
			}
		})
	}
}

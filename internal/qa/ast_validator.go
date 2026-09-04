package qa

import (
	"fmt"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v5"
)

// AllowedTables defines the explicit application schema allowlist.
var AllowedTables = map[string]bool{
	"reconciliation_runs":    true,
	"internal_transactions":  true,
	"settlement_records":     true,
	"bank_statements":        true,
	"reconciliation_matches": true,
	"exceptions":             true,
	"audit_log":              true,
}

// ForbiddenFunctions lists dangerous or informational Postgres functions.
var ForbiddenFunctions = map[string]bool{
	"dblink":              true,
	"query_to_xml":        true,
	"query_to_xml_and_xmlschema": true,
	"cursor_to_xml":       true,
	"cursor_to_xmlschema": true,
	"schema_to_xml":       true,
	"database_to_xml":     true,
	"table_to_xml":        true,
}

// ValidationResult holds the result of SQL AST validation.
type ValidationResult struct {
	Valid          bool     `json:"valid"`
	SanitizedQuery string   `json:"sanitized_query"`
	Tables         []string `json:"tables"`
	Error          string   `json:"error,omitempty"`
}

// ValidateSQL parses a SQL string into an AST, asserts read-only constraints,
// validates against the table allowlist, and enforces row bounds.
func ValidateSQL(rawSQL string) (*ValidationResult, error) {
	trimmed := strings.TrimSpace(rawSQL)
	// Strip trailing semicolon if present
	trimmed = strings.TrimSuffix(trimmed, ";")
	trimmed = strings.TrimSpace(trimmed)

	if trimmed == "" {
		return &ValidationResult{Valid: false, Error: "empty SQL query"}, fmt.Errorf("empty SQL query")
	}

	tree, err := pg_query.Parse(trimmed)
	if err != nil {
		return &ValidationResult{Valid: false, Error: fmt.Sprintf("SQL syntax parse error: %v", err)}, err
	}

	// 1. Strict single-statement enforcement
	if len(tree.Stmts) != 1 {
		errStr := fmt.Sprintf("rejected: expected exactly 1 statement, found %d", len(tree.Stmts))
		return &ValidationResult{Valid: false, Error: errStr}, fmt.Errorf("%s", errStr)
	}

	rootStmt := tree.Stmts[0].Stmt
	selectStmt := rootStmt.GetSelectStmt()
	if selectStmt == nil {
		errStr := "rejected: only SELECT statements are permitted; mutations, DDL, and control statements are strictly forbidden"
		return &ValidationResult{Valid: false, Error: errStr}, fmt.Errorf("%s", errStr)
	}

	// 2. Reject SELECT INTO (creates tables)
	if selectStmt.IntoClause != nil {
		errStr := "rejected: SELECT INTO statement is not permitted"
		return &ValidationResult{Valid: false, Error: errStr}, fmt.Errorf("%s", errStr)
	}

	// 3. Reject Locking Clauses (FOR UPDATE, FOR SHARE)
	if len(selectStmt.LockingClause) > 0 {
		errStr := "rejected: row-level locking clauses (FOR UPDATE/SHARE) are not permitted"
		return &ValidationResult{Valid: false, Error: errStr}, fmt.Errorf("%s", errStr)
	}

	// 4. Walk AST to collect relations and functions
	var tables []string
	var funcs []string
	cteNames := make(map[string]bool)

	walkAST(rootStmt, &tables, &funcs, cteNames)

	// Validate tables against allowlist
	for _, tbl := range tables {
		lowerTbl := strings.ToLower(tbl)
		if strings.HasPrefix(lowerTbl, "pg_") || strings.HasPrefix(lowerTbl, "information_schema") {
			errStr := fmt.Sprintf("rejected: system catalog table '%s' is strictly forbidden", tbl)
			return &ValidationResult{Valid: false, Error: errStr}, fmt.Errorf("%s", errStr)
		}
		if !AllowedTables[lowerTbl] && !cteNames[lowerTbl] {
			errStr := fmt.Sprintf("rejected: table '%s' is not in the approved reconciliation schema allowlist", tbl)
			return &ValidationResult{Valid: false, Error: errStr}, fmt.Errorf("%s", errStr)
		}
	}

	// Validate functions
	for _, fn := range funcs {
		lowerFn := strings.ToLower(fn)
		if strings.HasPrefix(lowerFn, "pg_") || ForbiddenFunctions[lowerFn] {
			errStr := fmt.Sprintf("rejected: forbidden function '%s' cannot be executed", fn)
			return &ValidationResult{Valid: false, Error: errStr}, fmt.Errorf("%s", errStr)
		}
	}

	// 5. Enforce LIMIT <= 50
	sanitizedQuery := trimmed
	if selectStmt.LimitCount == nil {
		sanitizedQuery = sanitizedQuery + " LIMIT 50"
	}

	return &ValidationResult{
		Valid:          true,
		SanitizedQuery: sanitizedQuery,
		Tables:         tables,
	}, nil
}

func walkAST(node *pg_query.Node, tables *[]string, funcs *[]string, cteNames map[string]bool) {
	if node == nil {
		return
	}
	switch n := node.Node.(type) {
	case *pg_query.Node_SelectStmt:
		s := n.SelectStmt
		if s.WithClause != nil {
			for _, cteNode := range s.WithClause.Ctes {
				if cte := cteNode.GetCommonTableExpr(); cte != nil {
					cteNames[strings.ToLower(cte.Ctename)] = true
					walkAST(cte.Ctequery, tables, funcs, cteNames)
				}
			}
		}
		for _, from := range s.FromClause {
			walkAST(from, tables, funcs, cteNames)
		}
		for _, target := range s.TargetList {
			walkAST(target, tables, funcs, cteNames)
		}
		walkAST(s.WhereClause, tables, funcs, cteNames)
		walkAST(s.HavingClause, tables, funcs, cteNames)
		if s.Larg != nil {
			walkAST(&pg_query.Node{Node: &pg_query.Node_SelectStmt{SelectStmt: s.Larg}}, tables, funcs, cteNames)
		}
		if s.Rarg != nil {
			walkAST(&pg_query.Node{Node: &pg_query.Node_SelectStmt{SelectStmt: s.Rarg}}, tables, funcs, cteNames)
		}
	case *pg_query.Node_ResTarget:
		walkAST(n.ResTarget.Val, tables, funcs, cteNames)
	case *pg_query.Node_RangeVar:
		tbl := n.RangeVar.Relname
		lowerTbl := strings.ToLower(tbl)
		if !cteNames[lowerTbl] {
			*tables = append(*tables, tbl)
		}
	case *pg_query.Node_JoinExpr:
		walkAST(n.JoinExpr.Larg, tables, funcs, cteNames)
		walkAST(n.JoinExpr.Rarg, tables, funcs, cteNames)
		walkAST(n.JoinExpr.Quals, tables, funcs, cteNames)
	case *pg_query.Node_RangeSubselect:
		walkAST(n.RangeSubselect.Subquery, tables, funcs, cteNames)
	case *pg_query.Node_SubLink:
		walkAST(n.SubLink.Subselect, tables, funcs, cteNames)
		walkAST(n.SubLink.Testexpr, tables, funcs, cteNames)
	case *pg_query.Node_FuncCall:
		var name string
		for _, item := range n.FuncCall.Funcname {
			if s := item.GetString_(); s != nil {
				if name != "" {
					name += "."
				}
				name += s.Sval
			}
		}
		if name != "" {
			*funcs = append(*funcs, name)
		}
		for _, arg := range n.FuncCall.Args {
			walkAST(arg, tables, funcs, cteNames)
		}
	case *pg_query.Node_AExpr:
		walkAST(n.AExpr.Lexpr, tables, funcs, cteNames)
		walkAST(n.AExpr.Rexpr, tables, funcs, cteNames)
	case *pg_query.Node_BoolExpr:
		for _, arg := range n.BoolExpr.Args {
			walkAST(arg, tables, funcs, cteNames)
		}
	}
}

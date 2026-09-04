package qa

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

// QueryExecutionResult contains rows, column names, and timing.
type QueryExecutionResult struct {
	Columns      []string                 `json:"columns"`
	Rows         []map[string]interface{} `json:"rows"`
	RowCount     int                      `json:"row_count"`
	DurationMs   int64                    `json:"duration_ms"`
	SanitizedSQL string                   `json:"sanitized_sql"`
}

// ExecuteReadOnly executes a validated SQL query strictly using the read-only database pool.
func ExecuteReadOnly(ctx context.Context, db *sql.DB, query string) (*QueryExecutionResult, error) {
	startTime := time.Now()

	// Enforce 3-second hard timeout
	execCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	rows, err := db.QueryContext(execCtx, query)
	if err != nil {
		return nil, fmt.Errorf("read-only query execution failed: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch column names: %w", err)
	}

	resultRows := make([]map[string]interface{}, 0)

	for rows.Next() {
		// Limit to 50 rows defensively in executor
		if len(resultRows) >= 50 {
			break
		}

		values := make([]interface{}, len(cols))
		valPtrs := make([]interface{}, len(cols))
		for i := range values {
			valPtrs[i] = &values[i]
		}

		if err := rows.Scan(valPtrs...); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		rowMap := make(map[string]interface{})
		for i, col := range cols {
			val := values[i]
			// Format []byte strings cleanly
			if b, ok := val.([]byte); ok {
				rowMap[col] = string(b)
			} else {
				rowMap[col] = val
			}
		}
		resultRows = append(resultRows, rowMap)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating query rows: %w", err)
	}

	duration := time.Since(startTime).Milliseconds()

	return &QueryExecutionResult{
		Columns:      cols,
		Rows:         resultRows,
		RowCount:     len(resultRows),
		DurationMs:   duration,
		SanitizedSQL: query,
	}, nil
}

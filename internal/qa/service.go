package qa

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// QAService coordinates Question -> SQL Generation -> AST Validation -> Read-Only DB Execution -> Grounded Answer.
type QAService struct {
	readOnlyDB *sql.DB
	apiKey     string
	baseURL    string
	model      string
}

// NewQAService initializes a new QAService.
func NewQAService(readOnlyDB *sql.DB, apiKey, baseURL, model string) *QAService {
	return &QAService{
		readOnlyDB: readOnlyDB,
		apiKey:     apiKey,
		baseURL:    baseURL,
		model:      model,
	}
}

// QAResponse encapsulates the full response for a natural-language query.
type QAResponse struct {
	Question     string                   `json:"question"`
	SQL          string                   `json:"sql,omitempty"`
	ASTValid     bool                     `json:"ast_valid"`
	Rows         []map[string]interface{} `json:"rows,omitempty"`
	RowCount     int                      `json:"row_count"`
	Answer       string                   `json:"answer"`
	DurationMs   int64                    `json:"duration_ms"`
	Unsupported  bool                     `json:"unsupported"`
	ErrorMessage string                   `json:"error_message,omitempty"`
}

// Ask processes a natural-language financial inquiry.
func (s *QAService) Ask(ctx context.Context, runID string, question string) (*QAResponse, error) {
	startTime := time.Now()

	// 1. Fetch DB metadata context (active merchants & schema metadata for this run)
	meta := FetchDBMetadataContext(ctx, s.readOnlyDB, runID)

	// 2. Generate SQL
	genResult, err := GenerateSQL(ctx, question, runID, meta, s.apiKey, s.baseURL, s.model)
	if err != nil {
		return &QAResponse{
			Question:     question,
			Unsupported:  true,
			Answer:       "Failed to parse question into a financial query.",
			ErrorMessage: err.Error(),
			DurationMs:   time.Since(startTime).Milliseconds(),
		}, nil
	}

	if genResult.Unsupported {
		return &QAResponse{
			Question:    question,
			Unsupported: true,
			Answer:      genResult.Reason,
			DurationMs:  time.Since(startTime).Milliseconds(),
		}, nil
	}

	// 3. AST Validation & Security Checks
	valResult, err := ValidateSQL(genResult.SQL)
	if (err != nil || !valResult.Valid) && s.apiKey != "" {
		// Fallback to offline SQL generator if LLM generated an invalid/unapproved query
		offlineResult, offlineErr := generateSQLOffline(question, runID, meta)
		if offlineErr == nil && !offlineResult.Unsupported {
			if offVal, offErr := ValidateSQL(offlineResult.SQL); offErr == nil && offVal.Valid {
				genResult = offlineResult
				valResult = offVal
				err = nil
			}
		}
	}

	if err != nil || !valResult.Valid {
		errMsg := "SQL AST validation failed"
		if valResult != nil && valResult.Error != "" {
			errMsg = valResult.Error
		} else if err != nil {
			errMsg = err.Error()
		}
		return &QAResponse{
			Question:     question,
			SQL:          genResult.SQL,
			ASTValid:     false,
			Unsupported:  true,
			Answer:       fmt.Sprintf("Query rejected by security validator: %s", errMsg),
			ErrorMessage: errMsg,
			DurationMs:   time.Since(startTime).Milliseconds(),
		}, nil
	}

	// 4. Execute query on read-only database role
	execResult, err := ExecuteReadOnly(ctx, s.readOnlyDB, valResult.SanitizedQuery)
	if err != nil && s.apiKey != "" {
		// Fallback to offline SQL generator if LLM query failed DB execution
		offlineResult, offlineErr := generateSQLOffline(question, runID, meta)
		if offlineErr == nil && !offlineResult.Unsupported {
			if offVal, offErr := ValidateSQL(offlineResult.SQL); offErr == nil && offVal.Valid {
				if offExec, offExecErr := ExecuteReadOnly(ctx, s.readOnlyDB, offVal.SanitizedQuery); offExecErr == nil {
					valResult = offVal
					execResult = offExec
					err = nil
				}
			}
		}
	}

	if err != nil {
		return &QAResponse{
			Question:     question,
			SQL:          valResult.SanitizedQuery,
			ASTValid:     true,
			Answer:       "An error occurred executing the read-only query on PostgreSQL.",
			ErrorMessage: err.Error(),
			DurationMs:   time.Since(startTime).Milliseconds(),
		}, nil
	}

	// 4. Grounded Answer Synthesis
	answer := GenerateGroundedAnswer(ctx, question, valResult.SanitizedQuery, execResult.Rows, s.apiKey, s.baseURL, s.model)

	return &QAResponse{
		Question:    question,
		SQL:         valResult.SanitizedQuery,
		ASTValid:    true,
		Rows:        execResult.Rows,
		RowCount:    execResult.RowCount,
		Answer:      answer,
		DurationMs:  time.Since(startTime).Milliseconds(),
		Unsupported: false,
	}, nil
}

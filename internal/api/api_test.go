package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/razorpay-hack/ai-finance-controller/internal/config"
	"github.com/razorpay-hack/ai-finance-controller/internal/db"
	"github.com/razorpay-hack/ai-finance-controller/internal/qa"
)

func TestAPI_RunEndpoints(t *testing.T) {
	cfg := config.Load()
	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("Failed connecting to database: %v", err)
	}
	defer database.Close()

	qaService := qa.NewQAService(database, cfg.NvidiaAPIKey, cfg.NvidiaNIMBaseURL, cfg.NvidiaNIMModel)
	server := NewServer(database, qaService, cfg)

	// Fetch latest run_id from DB
	var runID uuid.UUID
	err = database.QueryRow(`SELECT run_id FROM reconciliation_runs ORDER BY started_at DESC LIMIT 1;`).Scan(&runID)
	if err != nil {
		t.Fatalf("No run found in DB: %v", err)
	}

	// 1. Summary Endpoint
	reqSummary := httptest.NewRequest(http.MethodGet, "/api/runs/"+runID.String()+"/summary", nil)
	recSummary := httptest.NewRecorder()
	server.ServeHTTP(recSummary, reqSummary)

	if recSummary.Code != http.StatusOK {
		t.Errorf("Expected 200 on summary, got %d: %s", recSummary.Code, recSummary.Body.String())
	}

	var summary map[string]interface{}
	if err := json.Unmarshal(recSummary.Body.Bytes(), &summary); err != nil {
		t.Fatalf("Failed unmarshaling summary: %v", err)
	}
	if summary["run_id"] != runID.String() {
		t.Errorf("Expected run_id %s, got %v", runID.String(), summary["run_id"])
	}
	if summary["internal_count"] != float64(500) {
		t.Errorf("Expected 500 internal cases, got %v", summary["internal_count"])
	}

	// 2. Reconciliation Matches Endpoint
	reqMatches := httptest.NewRequest(http.MethodGet, "/api/runs/"+runID.String()+"/reconciliation?limit=10", nil)
	recMatches := httptest.NewRecorder()
	server.ServeHTTP(recMatches, reqMatches)

	if recMatches.Code != http.StatusOK {
		t.Errorf("Expected 200 on matches, got %d", recMatches.Code)
	}

	var matchesResp map[string]interface{}
	if err := json.Unmarshal(recMatches.Body.Bytes(), &matchesResp); err != nil {
		t.Fatalf("Failed unmarshaling matches: %v", err)
	}
	matchesList, ok := matchesResp["matches"].([]interface{})
	if !ok || len(matchesList) == 0 {
		t.Errorf("Expected non-empty matches list")
	}

	// 3. Exceptions Endpoint
	reqExceptions := httptest.NewRequest(http.MethodGet, "/api/runs/"+runID.String()+"/exceptions?hop=HOP1", nil)
	recExceptions := httptest.NewRecorder()
	server.ServeHTTP(recExceptions, reqExceptions)

	if recExceptions.Code != http.StatusOK {
		t.Errorf("Expected 200 on exceptions, got %d", recExceptions.Code)
	}

	var excResp map[string]interface{}
	if err := json.Unmarshal(recExceptions.Body.Bytes(), &excResp); err != nil {
		t.Fatalf("Failed unmarshaling exceptions: %v", err)
	}
	excList, ok := excResp["exceptions"].([]interface{})
	if !ok || len(excList) == 0 {
		t.Errorf("Expected non-empty exceptions list")
	}

	// 4. Cash Position Endpoint
	reqCash := httptest.NewRequest(http.MethodGet, "/api/runs/"+runID.String()+"/cash-position", nil)
	recCash := httptest.NewRecorder()
	server.ServeHTTP(recCash, reqCash)

	if recCash.Code != http.StatusOK {
		t.Errorf("Expected 200 on cash position, got %d", recCash.Code)
	}

	var cashResp map[string]interface{}
	if err := json.Unmarshal(recCash.Body.Bytes(), &cashResp); err != nil {
		t.Fatalf("Failed unmarshaling cash position: %v", err)
	}
	if cashResp["unresolved_exposure_inr"] == nil {
		t.Errorf("Expected unresolved_exposure_inr in cash position")
	}

	// 5. Audit Log Item Endpoint
	reqAudit := httptest.NewRequest(http.MethodGet, "/api/runs/"+runID.String()+"/audit/item?record_id=INT0001", nil)
	recAudit := httptest.NewRecorder()
	server.ServeHTTP(recAudit, reqAudit)

	if recAudit.Code != http.StatusOK {
		t.Errorf("Expected 200 on audit item, got %d", recAudit.Code)
	}

	// 6. Settlement Q&A Endpoint
	qaBody := `{"question": "What is the total unresolved amount?"}`
	reqQA := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID.String()+"/qa", strings.NewReader(qaBody))
	reqQA.Header.Set("Content-Type", "application/json")
	recQA := httptest.NewRecorder()
	server.ServeHTTP(recQA, reqQA)

	if recQA.Code != http.StatusOK {
		t.Errorf("Expected 200 on Q&A, got %d: %s", recQA.Code, recQA.Body.String())
	}

	var qaResp map[string]interface{}
	if err := json.Unmarshal(recQA.Body.Bytes(), &qaResp); err != nil {
		t.Fatalf("Failed unmarshaling QA response: %v", err)
	}
	if qaResp["ast_valid"] != true {
		t.Errorf("Expected ast_valid to be true in Q&A response")
	}
	if qaResp["answer"] == nil || qaResp["answer"] == "" {
		t.Errorf("Expected non-empty grounded answer in Q&A response")
	}
}

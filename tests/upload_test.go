package tests

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/razorpay-hack/ai-finance-controller/internal/api"
	"github.com/razorpay-hack/ai-finance-controller/internal/config"
	"github.com/razorpay-hack/ai-finance-controller/internal/db"
	"github.com/razorpay-hack/ai-finance-controller/internal/ingestion"
	"github.com/razorpay-hack/ai-finance-controller/internal/qa"
)

func setupTestServer(t *testing.T) (http.Handler, func()) {
	cfg := config.Load()
	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		fallbackURL := "postgres://localhost:5432/ai_finance_db?sslmode=disable"
		database, err = db.Connect(fallbackURL)
		if err != nil {
			t.Fatalf("failed connecting to test database: %v", err)
		}
	}

	qaService := qa.NewQAService(database, cfg.NvidiaAPIKey, cfg.NvidiaNIMBaseURL, cfg.NvidiaNIMModel)
	server := api.NewServer(database, qaService, cfg)

	cleanup := func() {
		database.Close()
	}

	return server, cleanup
}

func createMultipartRequest(url, fieldName, filename string, fileContent []byte, extraFields map[string]string) (*http.Request, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile(fieldName, filename)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(fileContent); err != nil {
		return nil, err
	}

	for k, v := range extraFields {
		if err := writer.WriteField(k, v); err != nil {
			return nil, err
		}
	}

	if err := writer.Close(); err != nil {
		return nil, err
	}

	req := httptest.NewRequest("POST", url, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, nil
}

func TestSampleDownloadEndpoints(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	sources := []string{"internal", "settlement", "bank"}
	for _, src := range sources {
		// Clean sample
		req := httptest.NewRequest("GET", "/api/samples/"+src, nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for clean sample %s, got: %d", src, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "text/csv") {
			t.Errorf("expected text/csv content-type, got: %s", rec.Header().Get("Content-Type"))
		}

		// Messy sample
		reqMessy := httptest.NewRequest("GET", "/api/samples/messy/"+src, nil)
		recMessy := httptest.NewRecorder()
		server.ServeHTTP(recMessy, reqMessy)

		if recMessy.Code != http.StatusOK {
			t.Fatalf("expected 200 for messy sample %s, got: %d", src, recMessy.Code)
		}
	}
}

func TestAnalyzeAndCommitFlow(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// 1. Test XLSX rejection
	fakeXLSX := []byte{0x50, 0x4B, 0x03, 0x04, 0x00, 0x00}
	reqBad, err := createMultipartRequest("/api/ingest/analyze", "file", "test.csv", fakeXLSX, map[string]string{"source_type": "internal"})
	if err != nil {
		t.Fatalf("failed building multipart req: %v", err)
	}
	recBad := httptest.NewRecorder()
	server.ServeHTTP(recBad, reqBad)
	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for fake XLSX, got: %d", recBad.Code)
	}

	// 2. Test analyze on messy internal CSV
	messyInternalCSV := "Txn Ref ID,Gross Paid Amount,Txn Timestamp,Merchant Code,Gateway Order Ref\nINT_TEST_001,\"₹5,214.63\",01/09/2026 10:15:30,MERCH_ZOMATO_DEL,REF_ORD_001\n"
	reqAnalyze, err := createMultipartRequest("/api/ingest/analyze", "file", "internal_messy.csv", []byte(messyInternalCSV), map[string]string{"source_type": "internal"})
	if err != nil {
		t.Fatalf("failed building multipart req: %v", err)
	}
	recAnalyze := httptest.NewRecorder()
	server.ServeHTTP(recAnalyze, reqAnalyze)

	if recAnalyze.Code != http.StatusOK {
		t.Fatalf("analyze failed with code %d: %s", recAnalyze.Code, recAnalyze.Body.String())
	}

	var analyzeResult ingestion.MappingResult
	if err := json.Unmarshal(recAnalyze.Body.Bytes(), &analyzeResult); err != nil {
		t.Fatalf("failed decoding analyze response: %v", err)
	}

	if analyzeResult.FileID == "" {
		t.Fatalf("expected non-empty file_id in analyze result")
	}
	if analyzeResult.Mapping["id"] != "Txn Ref ID" {
		t.Errorf("expected id -> 'Txn Ref ID', got: %s", analyzeResult.Mapping["id"])
	}

	// 3. Test commit with missing required field
	badCommit := ingestion.CommitRequest{
		FileID:     analyzeResult.FileID,
		SourceType: "internal",
		Mapping: map[string]string{
			"id":     "Txn Ref ID",
			"amount": "", // missing required field
		},
	}
	badCommitJSON, _ := json.Marshal(badCommit)
	reqBadCommit := httptest.NewRequest("POST", "/api/ingest/commit", bytes.NewReader(badCommitJSON))
	reqBadCommit.Header.Set("Content-Type", "application/json")
	recBadCommit := httptest.NewRecorder()
	server.ServeHTTP(recBadCommit, reqBadCommit)

	if recBadCommit.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing required mapping, got: %d", recBadCommit.Code)
	}

	// 4. Test successful commit
	goodCommit := ingestion.CommitRequest{
		FileID:          analyzeResult.FileID,
		SourceType:      "internal",
		Mapping:         analyzeResult.Mapping,
		DateFormat:      analyzeResult.DateFormat,
		ReplaceExisting: false,
	}
	goodCommitJSON, _ := json.Marshal(goodCommit)
	reqGoodCommit := httptest.NewRequest("POST", "/api/ingest/commit", bytes.NewReader(goodCommitJSON))
	reqGoodCommit.Header.Set("Content-Type", "application/json")
	recGoodCommit := httptest.NewRecorder()
	server.ServeHTTP(recGoodCommit, reqGoodCommit)

	if recGoodCommit.Code != http.StatusOK {
		t.Fatalf("commit failed with code %d: %s", recGoodCommit.Code, recGoodCommit.Body.String())
	}

	var summary ingestion.IngestSummary
	if err := json.Unmarshal(recGoodCommit.Body.Bytes(), &summary); err != nil {
		t.Fatalf("failed unmarshaling commit summary: %v", err)
	}
	if summary.RowsIngested != 1 {
		t.Errorf("expected 1 row ingested, got: %d", summary.RowsIngested)
	}
	if summary.RunID == "" {
		t.Errorf("expected non-empty run_id in summary")
	}
}

func TestFullThreeSourceUploadAndReconcile(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Upload Internal
	intCSV, _ := ingestion.GenerateMessySampleCSV("internal")
	reqInt, _ := createMultipartRequest("/api/ingest/analyze", "file", "int.csv", intCSV, map[string]string{"source_type": "internal"})
	recInt := httptest.NewRecorder()
	server.ServeHTTP(recInt, reqInt)
	var intRes ingestion.MappingResult
	_ = json.Unmarshal(recInt.Body.Bytes(), &intRes)

	commitInt, _ := json.Marshal(ingestion.CommitRequest{
		FileID:     intRes.FileID,
		SourceType: "internal",
		Mapping:    intRes.Mapping,
		DateFormat: intRes.DateFormat,
	})
	reqCommitInt := httptest.NewRequest("POST", "/api/ingest/commit", bytes.NewReader(commitInt))
	reqCommitInt.Header.Set("Content-Type", "application/json")
	recCommitInt := httptest.NewRecorder()
	server.ServeHTTP(recCommitInt, reqCommitInt)

	if recCommitInt.Code != http.StatusOK {
		t.Fatalf("commit int failed %d: %s", recCommitInt.Code, recCommitInt.Body.String())
	}
	var intSummary ingestion.IngestSummary
	_ = json.Unmarshal(recCommitInt.Body.Bytes(), &intSummary)
	targetRunID := intSummary.RunID

	// Upload Settlement into same run
	setCSV, _ := ingestion.GenerateMessySampleCSV("settlement")
	reqSet, _ := createMultipartRequest("/api/ingest/analyze", "file", "set.csv", setCSV, map[string]string{"source_type": "settlement"})
	recSet := httptest.NewRecorder()
	server.ServeHTTP(recSet, reqSet)
	var setRes ingestion.MappingResult
	_ = json.Unmarshal(recSet.Body.Bytes(), &setRes)

	commitSet, _ := json.Marshal(ingestion.CommitRequest{
		FileID:     setRes.FileID,
		SourceType: "settlement",
		Mapping:    setRes.Mapping,
		DateFormat: setRes.DateFormat,
		RunID:      targetRunID,
	})
	reqCommitSet := httptest.NewRequest("POST", "/api/ingest/commit", bytes.NewReader(commitSet))
	reqCommitSet.Header.Set("Content-Type", "application/json")
	recCommitSet := httptest.NewRecorder()
	server.ServeHTTP(recCommitSet, reqCommitSet)
	if recCommitSet.Code != http.StatusOK {
		t.Fatalf("commit set failed %d: %s", recCommitSet.Code, recCommitSet.Body.String())
	}

	// Upload Bank into same run
	bnkCSV, _ := ingestion.GenerateMessySampleCSV("bank")
	reqBnk, _ := createMultipartRequest("/api/ingest/analyze", "file", "bnk.csv", bnkCSV, map[string]string{"source_type": "bank"})
	recBnk := httptest.NewRecorder()
	server.ServeHTTP(recBnk, reqBnk)
	var bnkRes ingestion.MappingResult
	_ = json.Unmarshal(recBnk.Body.Bytes(), &bnkRes)

	commitBnk, _ := json.Marshal(ingestion.CommitRequest{
		FileID:     bnkRes.FileID,
		SourceType: "bank",
		Mapping:    bnkRes.Mapping,
		DateFormat: bnkRes.DateFormat,
		RunID:      targetRunID,
	})
	reqCommitBnk := httptest.NewRequest("POST", "/api/ingest/commit", bytes.NewReader(commitBnk))
	reqCommitBnk.Header.Set("Content-Type", "application/json")
	recCommitBnk := httptest.NewRecorder()
	server.ServeHTTP(recCommitBnk, reqCommitBnk)
	if recCommitBnk.Code != http.StatusOK {
		t.Fatalf("commit bnk failed %d: %s", recCommitBnk.Code, recCommitBnk.Body.String())
	}

	// Trigger Reconcile on this Run
	reqReconcile := httptest.NewRequest("POST", "/api/runs/"+targetRunID+"/reconcile", nil)
	recReconcile := httptest.NewRecorder()
	server.ServeHTTP(recReconcile, reqReconcile)

	if recReconcile.Code != http.StatusOK {
		t.Fatalf("reconcile failed with code %d: %s", recReconcile.Code, recReconcile.Body.String())
	}

	body, _ := io.ReadAll(recReconcile.Body)
	var recOutput map[string]interface{}
	_ = json.Unmarshal(body, &recOutput)

	t.Logf("int mapping: %+v", intRes.Mapping)
	t.Logf("set mapping: %+v", setRes.Mapping)
	t.Logf("bnk mapping: %+v", bnkRes.Mapping)
	t.Logf("recOutput: %+v", recOutput)

	if recOutput["status"] != "success" {
		t.Fatalf("expected status success, got: %v", recOutput["status"])
	}
	if recOutput["hop1_matched_count"].(float64) < 4 {
		t.Errorf("expected at least 4 hop 1 matches, got: %v", recOutput["hop1_matched_count"])
	}
}

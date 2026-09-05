package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/razorpay-hack/ai-finance-controller/internal/models"
	"github.com/razorpay-hack/ai-finance-controller/internal/repository"
)

func TestRunCreationAndListing(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// 1. Create a run with explicit name
	runName := fmt.Sprintf("Q3 Test Run %s", uuid.New().String()[:8])
	payload, _ := json.Marshal(map[string]string{"name": runName})
	req := httptest.NewRequest("POST", "/runs", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got: %d (%s)", rec.Code, rec.Body.String())
	}

	var created models.ReconciliationRun
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("failed unmarshaling run: %v", err)
	}

	if created.Name != runName {
		t.Errorf("expected run name '%s', got '%s'", runName, created.Name)
	}
	if created.Status != models.RunStatusDraft {
		t.Errorf("expected initial status DRAFT, got '%s'", created.Status)
	}

	// 2. List all runs (GET /runs)
	reqList := httptest.NewRequest("GET", "/runs", nil)
	recList := httptest.NewRecorder()
	server.ServeHTTP(recList, reqList)

	if recList.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for GET /runs, got: %d", recList.Code)
	}

	var runs []repository.RunListItem
	if err := json.Unmarshal(recList.Body.Bytes(), &runs); err != nil {
		t.Fatalf("failed unmarshaling runs list: %v", err)
	}

	found := false
	for _, r := range runs {
		if r.RunID == created.RunID {
			found = true
			if r.Name != runName {
				t.Errorf("run in list has mismatched name: %s", r.Name)
			}
			if r.Status != models.RunStatusDraft {
				t.Errorf("expected list item status DRAFT, got: %s", r.Status)
			}
			break
		}
	}
	if !found {
		t.Errorf("created run %s not found in GET /runs", created.RunID)
	}
}

func TestRunIsolationSameIDs(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create Run 1
	req1 := httptest.NewRequest("POST", "/runs", bytes.NewReader([]byte(`{"name":"Run Alpha"}`)))
	rec1 := httptest.NewRecorder()
	server.ServeHTTP(rec1, req1)
	var run1 models.ReconciliationRun
	_ = json.Unmarshal(rec1.Body.Bytes(), &run1)

	// Create Run 2
	req2 := httptest.NewRequest("POST", "/runs", bytes.NewReader([]byte(`{"name":"Run Beta"}`)))
	rec2 := httptest.NewRecorder()
	server.ServeHTTP(rec2, req2)
	var run2 models.ReconciliationRun
	_ = json.Unmarshal(rec2.Body.Bytes(), &run2)

	// Both runs upload an internal transaction with the exact same ID "INT_COLLIDE_999" but different amounts
	csvAlpha := "id,amount,transaction_date,merchant_id\nINT_COLLIDE_999,1000.00,2026-09-01,MERCH_A\n"
	csvBeta := "id,amount,transaction_date,merchant_id\nINT_COLLIDE_999,5000.00,2026-09-01,MERCH_B\n"

	reqUpload1, _ := createMultipartRequest(fmt.Sprintf("/runs/%s/uploads", run1.RunID), "file", "alpha.csv", []byte(csvAlpha), map[string]string{"source_type": "internal"})
	recUpload1 := httptest.NewRecorder()
	server.ServeHTTP(recUpload1, reqUpload1)
	if recUpload1.Code != http.StatusOK {
		t.Fatalf("failed upload alpha: %d (%s)", recUpload1.Code, recUpload1.Body.String())
	}

	reqUpload2, _ := createMultipartRequest(fmt.Sprintf("/runs/%s/uploads", run2.RunID), "file", "beta.csv", []byte(csvBeta), map[string]string{"source_type": "internal"})
	recUpload2 := httptest.NewRecorder()
	server.ServeHTTP(recUpload2, reqUpload2)
	if recUpload2.Code != http.StatusOK {
		t.Fatalf("failed upload beta: %d (%s)", recUpload2.Code, recUpload2.Body.String())
	}

	// Fetch detail for Run 1
	reqD1 := httptest.NewRequest("GET", fmt.Sprintf("/runs/%s", run1.RunID), nil)
	recD1 := httptest.NewRecorder()
	server.ServeHTTP(recD1, reqD1)
	var d1 repository.RunDetail
	_ = json.Unmarshal(recD1.Body.Bytes(), &d1)

	// Fetch detail for Run 2
	reqD2 := httptest.NewRequest("GET", fmt.Sprintf("/runs/%s", run2.RunID), nil)
	recD2 := httptest.NewRecorder()
	server.ServeHTTP(recD2, reqD2)
	var d2 repository.RunDetail
	_ = json.Unmarshal(recD2.Body.Bytes(), &d2)

	if d1.InternalCount != 1 || d2.InternalCount != 1 {
		t.Fatalf("expected both runs to maintain their independent record, got d1=%d, d2=%d", d1.InternalCount, d2.InternalCount)
	}
	if len(d1.Uploads) != 1 || len(d2.Uploads) != 1 {
		t.Fatalf("expected 1 upload record each, got d1=%d, d2=%d", len(d1.Uploads), len(d2.Uploads))
	}
}

func TestRunStatusLifecycle(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// 1. Create run -> DRAFT
	reqRun := httptest.NewRequest("POST", "/runs", bytes.NewReader([]byte(`{"name":"Lifecycle Run"}`)))
	recRun := httptest.NewRecorder()
	server.ServeHTTP(recRun, reqRun)
	var run models.ReconciliationRun
	_ = json.Unmarshal(recRun.Body.Bytes(), &run)

	// 2. Upload internal and settlement
	intCSV := "id,amount,transaction_date,merchant_id,reference_id\nINT_LC_1,100.00,2026-09-01,MERCH_LC,REF_LC_1\n"
	setCSV := "id,settled_amount,settlement_date,merchant_id,reference_id,batch_id\nSET_LC_1,98.00,2026-09-01,MERCH_LC,REF_LC_1,BATCH_LC_1\n"

	reqInt, _ := createMultipartRequest(fmt.Sprintf("/runs/%s/uploads", run.RunID), "file", "int.csv", []byte(intCSV), map[string]string{"source_type": "internal"})
	recInt := httptest.NewRecorder()
	server.ServeHTTP(recInt, reqInt)

	reqSet, _ := createMultipartRequest(fmt.Sprintf("/runs/%s/uploads", run.RunID), "file", "set.csv", []byte(setCSV), map[string]string{"source_type": "settlement"})
	recSet := httptest.NewRecorder()
	server.ServeHTTP(recSet, reqSet)

	// Verify status is still DRAFT before reconciliation
	reqDetail := httptest.NewRequest("GET", fmt.Sprintf("/runs/%s", run.RunID), nil)
	recDetail := httptest.NewRecorder()
	server.ServeHTTP(recDetail, reqDetail)
	var d1 repository.RunDetail
	_ = json.Unmarshal(recDetail.Body.Bytes(), &d1)
	if d1.Status != models.RunStatusDraft {
		t.Fatalf("expected DRAFT status before reconcile, got: %s", d1.Status)
	}

	// 3. Trigger reconciliation -> RECONCILED
	reqRec := httptest.NewRequest("POST", fmt.Sprintf("/runs/%s/reconcile", run.RunID), nil)
	recRec := httptest.NewRecorder()
	server.ServeHTTP(recRec, reqRec)
	if recRec.Code != http.StatusOK {
		t.Fatalf("reconcile failed with code %d: %s", recRec.Code, recRec.Body.String())
	}

	reqDetail2 := httptest.NewRequest("GET", fmt.Sprintf("/runs/%s", run.RunID), nil)
	recDetail2 := httptest.NewRecorder()
	server.ServeHTTP(recDetail2, reqDetail2)
	var d2 repository.RunDetail
	_ = json.Unmarshal(recDetail2.Body.Bytes(), &d2)
	if d2.Status != models.RunStatusReconciled {
		t.Fatalf("expected RECONCILED status after reconcile, got: %s", d2.Status)
	}
	if d2.LastReconciledAt == nil {
		t.Fatalf("expected last_reconciled_at to be non-nil after reconciliation")
	}

	// 4. Upload bank statement -> Status flips to STALE
	bnkCSV := "id,credited_amount,credit_date,merchant_id,batch_reference,narration\nBNK_LC_1,98.00,2026-09-02,MERCH_LC,BATCH_LC_1,Payout\n"
	reqBnk, _ := createMultipartRequest(fmt.Sprintf("/runs/%s/uploads", run.RunID), "file", "bnk.csv", []byte(bnkCSV), map[string]string{"source_type": "bank"})
	recBnk := httptest.NewRecorder()
	server.ServeHTTP(recBnk, reqBnk)

	reqDetail3 := httptest.NewRequest("GET", fmt.Sprintf("/runs/%s", run.RunID), nil)
	recDetail3 := httptest.NewRecorder()
	server.ServeHTTP(recDetail3, reqDetail3)
	var d3 repository.RunDetail
	_ = json.Unmarshal(recDetail3.Body.Bytes(), &d3)
	if d3.Status != models.RunStatusStale {
		t.Fatalf("expected STALE status after new upload into reconciled run, got: %s", d3.Status)
	}

	// 5. Re-run reconciliation -> Status flips back to RECONCILED
	reqRec2 := httptest.NewRequest("POST", fmt.Sprintf("/runs/%s/reconcile", run.RunID), nil)
	recRec2 := httptest.NewRecorder()
	server.ServeHTTP(recRec2, reqRec2)

	reqDetail4 := httptest.NewRequest("GET", fmt.Sprintf("/runs/%s", run.RunID), nil)
	recDetail4 := httptest.NewRecorder()
	server.ServeHTTP(recDetail4, reqDetail4)
	var d4 repository.RunDetail
	_ = json.Unmarshal(recDetail4.Body.Bytes(), &d4)
	if d4.Status != models.RunStatusReconciled {
		t.Fatalf("expected RECONCILED status after second reconcile, got: %s", d4.Status)
	}
}

func TestZeroUploadsRejection(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create an empty run
	reqRun := httptest.NewRequest("POST", "/runs", bytes.NewReader([]byte(`{"name":"Empty Run"}`)))
	recRun := httptest.NewRecorder()
	server.ServeHTTP(recRun, reqRun)
	var run models.ReconciliationRun
	_ = json.Unmarshal(recRun.Body.Bytes(), &run)

	// Trigger reconcile on empty run
	reqRec := httptest.NewRequest("POST", fmt.Sprintf("/runs/%s/reconcile", run.RunID), nil)
	recRec := httptest.NewRecorder()
	server.ServeHTTP(recRec, reqRec)

	if recRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for zero uploads reconcile, got: %d", recRec.Code)
	}
	if !strings.Contains(recRec.Body.String(), "no source records found") {
		t.Errorf("expected clear error message, got: %s", recRec.Body.String())
	}
}

func TestPartialReconciliation(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Run with only internal + settlement (no bank statement)
	reqRun := httptest.NewRequest("POST", "/runs", bytes.NewReader([]byte(`{"name":"Partial Run"}`)))
	recRun := httptest.NewRecorder()
	server.ServeHTTP(recRun, reqRun)
	var run models.ReconciliationRun
	_ = json.Unmarshal(recRun.Body.Bytes(), &run)

	intCSV := "id,amount,transaction_date,merchant_id,reference_id\nINT_PART_1,100.00,2026-09-01,MERCH_P,REF_P_1\n"
	setCSV := "id,settled_amount,settlement_date,merchant_id,reference_id,batch_id\nSET_PART_1,97.50,2026-09-01,MERCH_P,REF_P_1,BATCH_P_1\n"

	reqInt, _ := createMultipartRequest(fmt.Sprintf("/runs/%s/uploads", run.RunID), "file", "int.csv", []byte(intCSV), map[string]string{"source_type": "internal"})
	recInt := httptest.NewRecorder()
	server.ServeHTTP(recInt, reqInt)

	reqSet, _ := createMultipartRequest(fmt.Sprintf("/runs/%s/uploads", run.RunID), "file", "set.csv", []byte(setCSV), map[string]string{"source_type": "settlement"})
	recSet := httptest.NewRecorder()
	server.ServeHTTP(recSet, reqSet)

	// Trigger reconciliation
	reqRec := httptest.NewRequest("POST", fmt.Sprintf("/runs/%s/reconcile", run.RunID), nil)
	recRec := httptest.NewRecorder()
	server.ServeHTTP(recRec, reqRec)

	if recRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for partial reconciliation, got: %d (%s)", recRec.Code, recRec.Body.String())
	}

	// Fetch matches (GET /runs/{runID}/matches)
	reqMatches := httptest.NewRequest("GET", fmt.Sprintf("/runs/%s/matches", run.RunID), nil)
	recMatches := httptest.NewRecorder()
	server.ServeHTTP(recMatches, reqMatches)

	if recMatches.Code != http.StatusOK {
		t.Fatalf("failed fetching matches: %d", recMatches.Code)
	}

	body, _ := io.ReadAll(recMatches.Body)
	var matchData struct {
		Matches []map[string]interface{} `json:"matches"`
	}
	_ = json.Unmarshal(body, &matchData)

	if len(matchData.Matches) == 0 {
		t.Fatalf("expected at least 1 match row, got: %s", string(body))
	}

	m0 := matchData.Matches[0]
	if m0["reconciliation_status"] != "PARTIAL" {
		t.Errorf("expected reconciliation_status = PARTIAL for partial run, got: %v", m0["reconciliation_status"])
	}
	hop2Rule, _ := m0["hop2_rule"].(string)
	if !strings.Contains(hop2Rule, "Hop 2 not evaluated") {
		t.Errorf("expected Hop 2 note in hop2_rule, got: %v", hop2Rule)
	}
}

func TestRunCascadeDelete(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create run
	reqRun := httptest.NewRequest("POST", "/runs", bytes.NewReader([]byte(`{"name":"To Be Deleted"}`)))
	recRun := httptest.NewRecorder()
	server.ServeHTTP(recRun, reqRun)
	var run models.ReconciliationRun
	_ = json.Unmarshal(recRun.Body.Bytes(), &run)

	// Upload file
	intCSV := "id,amount,transaction_date,merchant_id\nINT_DEL_1,100.00,2026-09-01,MERCH_DEL\n"
	reqInt, _ := createMultipartRequest(fmt.Sprintf("/runs/%s/uploads", run.RunID), "file", "del.csv", []byte(intCSV), map[string]string{"source_type": "internal"})
	recInt := httptest.NewRecorder()
	server.ServeHTTP(recInt, reqInt)

	// DELETE /runs/{runID}
	reqDel := httptest.NewRequest("DELETE", fmt.Sprintf("/runs/%s", run.RunID), nil)
	recDel := httptest.NewRecorder()
	server.ServeHTTP(recDel, reqDel)

	if recDel.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on delete run, got: %d", recDel.Code)
	}

	// Verify run is gone
	reqCheck := httptest.NewRequest("GET", fmt.Sprintf("/runs/%s", run.RunID), nil)
	recCheck := httptest.NewRecorder()
	server.ServeHTTP(recCheck, reqCheck)
	if recCheck.Code != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found after deletion, got: %d", recCheck.Code)
	}
}

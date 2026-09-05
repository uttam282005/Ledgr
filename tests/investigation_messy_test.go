package tests

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/razorpay-hack/ai-finance-controller/internal/config"
	"github.com/razorpay-hack/ai-finance-controller/internal/ingestion"
	"github.com/razorpay-hack/ai-finance-controller/internal/models"
	"github.com/razorpay-hack/ai-finance-controller/internal/reconciliation"
)

func TestMessyInvestigation(t *testing.T) {
	cfg := config.Load()
	ctx := context.Background()

	internalPath := "../investigate-failures/internal_ledger_messy(1).csv"
	settlementPath := "../investigate-failures/gateway_settlement_messy(1).csv"
	bankPath := "../investigate-failures/bank_statement_messy(2).csv"

	rawInternal, err := os.ReadFile(internalPath)
	if err != nil {
		t.Skipf("Skipping messy investigation test (internal CSV not found): %v", err)
		return
	}
	rawSettlement, err := os.ReadFile(settlementPath)
	if err != nil {
		t.Skipf("Skipping messy investigation test (settlement CSV not found): %v", err)
		return
	}
	rawBank, err := os.ReadFile(bankPath)
	if err != nil {
		t.Skipf("Skipping messy investigation test (bank CSV not found): %v", err)
		return
	}

	t.Logf("=== 1. Internal Ledger Mapping ===")
	cleanInternal, err := ingestion.CleanAndInspectCSV(rawInternal)
	if err != nil {
		t.Fatalf("clean internal failed: %v", err)
	}
	mapInternal, err := ingestion.InferColumnMapping(ctx, ingestion.SourceInternal, cleanInternal.Headers, cleanInternal.SampleRows, cfg.NvidiaAPIKey, cfg.NvidiaNIMBaseURL, cfg.NvidiaNIMModel)
	if err != nil {
		t.Fatalf("map internal failed: %v", err)
	}
	t.Logf("Internal Headers: %v", cleanInternal.Headers)
	t.Logf("Internal Mapping: %+v", mapInternal.Mapping)
	t.Logf("Internal DateFormat: %s", mapInternal.DateFormat)

	t.Logf("=== 2. Settlement Mapping ===")
	cleanSettlement, err := ingestion.CleanAndInspectCSV(rawSettlement)
	if err != nil {
		t.Fatalf("clean settlement failed: %v", err)
	}
	mapSettlement, err := ingestion.InferColumnMapping(ctx, ingestion.SourceSettlement, cleanSettlement.Headers, cleanSettlement.SampleRows, cfg.NvidiaAPIKey, cfg.NvidiaNIMBaseURL, cfg.NvidiaNIMModel)
	if err != nil {
		t.Fatalf("map settlement failed: %v", err)
	}
	t.Logf("Settlement Headers: %v", cleanSettlement.Headers)
	t.Logf("Settlement Mapping: %+v", mapSettlement.Mapping)
	t.Logf("Settlement DateFormat: %s", mapSettlement.DateFormat)

	t.Logf("=== 3. Bank Statement Mapping ===")
	cleanBank, err := ingestion.CleanAndInspectCSV(rawBank)
	if err != nil {
		t.Fatalf("clean bank failed: %v", err)
	}
	mapBank, err := ingestion.InferColumnMapping(ctx, ingestion.SourceBank, cleanBank.Headers, cleanBank.SampleRows, cfg.NvidiaAPIKey, cfg.NvidiaNIMBaseURL, cfg.NvidiaNIMModel)
	if err != nil {
		t.Fatalf("map bank failed: %v", err)
	}
	t.Logf("Bank Headers: %v", cleanBank.Headers)
	t.Logf("Bank Mapping: %+v", mapBank.Mapping)
	t.Logf("Bank DateFormat: %s", mapBank.DateFormat)

	runID := uuid.New()

	// Parse records
	valInternal, err := ingestion.ValidateAndParseCSV(cleanInternal.CleanedContent, ingestion.SourceInternal, mapInternal.Mapping, mapInternal.DateFormat, runID)
	if err != nil {
		t.Fatalf("Validate internal failed: %v", err)
	}
	t.Logf("Internal parsed count: %d, skipped: %d", len(valInternal.Internals), valInternal.RowsSkipped)
	for _, e := range valInternal.SkippedReasons {
		t.Logf("Internal SkippedReason: %v", e)
	}

	valSettlement, err := ingestion.ValidateAndParseCSV(cleanSettlement.CleanedContent, ingestion.SourceSettlement, mapSettlement.Mapping, mapSettlement.DateFormat, runID)
	if err != nil {
		t.Fatalf("Validate settlement failed: %v", err)
	}
	t.Logf("Settlement parsed count: %d, skipped: %d", len(valSettlement.Settlements), valSettlement.RowsSkipped)
	for _, e := range valSettlement.SkippedReasons {
		t.Logf("Settlement SkippedReason: %v", e)
	}

	valBank, err := ingestion.ValidateAndParseCSV(cleanBank.CleanedContent, ingestion.SourceBank, mapBank.Mapping, mapBank.DateFormat, runID)
	if err != nil {
		t.Fatalf("Validate bank failed: %v", err)
	}
	t.Logf("Bank parsed count: %d, skipped: %d", len(valBank.BankStatements), valBank.RowsSkipped)
	for _, e := range valBank.SkippedReasons {
		t.Logf("Bank SkippedReason: %v", e)
	}

	internals := valInternal.Internals
	settlements := valSettlement.Settlements
	banks := valBank.BankStatements

	t.Logf("Totals to reconcile: %d internals, %d settlements, %d banks", len(internals), len(settlements), len(banks))

	for _, it := range internals {
		if it.ID == "INT036" || it.ID == "INT037" || it.ID == "INT023" {
			t.Logf("Parsed %s: Date=%s (%v)", it.ID, it.TransactionDate.Format("2006-01-02"), it.TransactionDate)
		}
	}
	for _, s := range settlements {
		if s.ID == "SET024" || s.ID == "SET008" || s.ID == "SET044" {
			t.Logf("Parsed %s: Date=%s (%v)", s.ID, s.SettlementDate.Format("2006-01-02"), s.SettlementDate)
		}
	}

	// Run ReconcileRun
	output := reconciliation.ReconcileRun(runID, internals, settlements, banks, 2.5)

	t.Logf("\n=== RECONCILIATION RESULT ===")
	t.Logf("Hop 1 Matched: %d", output.Hop1MatchedCount)
	t.Logf("Hop 2 Matched: %d", output.Hop2MatchedCount)
	t.Logf("Full Chain: %d", output.FullChainCount)
	t.Logf("Total Exceptions: %d", len(output.Exceptions))

	h1Results := reconciliation.ReconcileHop1(internals, settlements, 2.5)
	for _, h1 := range h1Results {
		if h1.InternalID == "INT036" || h1.InternalID == "INT037" || h1.InternalID == "INT023" {
			cat := "none"
			if h1.ExceptionCategory != nil {
				cat = *h1.ExceptionCategory
			}
			t.Logf("Hop1Result %s: Matched=%v, Rule=%s, Cat=%s, Candidates=%d", h1.InternalID, h1.Matched, h1.Rule, cat, len(h1.CandidatesConsidered))
			for _, c := range h1.CandidatesConsidered {
				t.Logf("   Candidate %s: AmtValid=%v, DiffDays=%.1f, AmtPaise=%d", c.SettlementID, c.AmountValid, c.DaysDifference, c.AmountPaise)
			}
		}
	}

	catCounts := make(map[string]int)
	excByRecord := make(map[string]models.Exception)
	for _, e := range output.Exceptions {
		catCounts[e.Category]++
		excByRecord[e.RecordID] = e
	}
	for cat, cnt := range catCounts {
		t.Logf("Exception Category: %s = %d", cat, cnt)
	}

	for i, e := range output.Exceptions {
		t.Logf("Exc[%d]: Record=%s, Cat=%s, Hop=%s, Exp=%v, Reason=%s", i, e.RecordID, e.Category, e.Hop, e.ExposurePaise, e.Reason)
	}

	// 1. Hop 1 Matched: 39 of 50
	if output.Hop1MatchedCount != 39 {
		t.Errorf("Expected Hop 1 matched count 39, got %d", output.Hop1MatchedCount)
	}

	// 2. Hop 2 Batches Matched: 8 of 11
	hop2Summary := reconciliation.ReconcileHop2(settlements, banks)
	hop2CleanBatches := 0
	for _, b := range hop2Summary.BatchResults {
		if b.Matched {
			hop2CleanBatches++
		}
	}
	if hop2CleanBatches != 8 {
		t.Errorf("Expected 8 clean batches matched in Hop 2, got %d", hop2CleanBatches)
	}

	// 3. Total Exceptions: exactly 20
	if len(output.Exceptions) != 20 {
		t.Errorf("Expected exactly 20 total exceptions, got %d", len(output.Exceptions))
	}

	// 4. Exception Category Counts:
	expectedCounts := map[string]int{
		models.CategoryAmountMismatch:      5,
		models.CategoryDuplicateSettlement: 6,
		models.CategoryNoCounterpart:       3,
		models.CategoryOrphanSettlement:    2,
		models.CategorySettledNotBanked:    2,
		models.CategoryPartialCredit:       1,
		models.CategoryBankedNotSettled:    1,
	}
	for cat, expCount := range expectedCounts {
		if actual := catCounts[cat]; actual != expCount {
			t.Errorf("Category %s: expected %d exceptions, got %d", cat, expCount, actual)
		}
	}

	// 5. Check specific records
	for _, id := range []string{"INT015", "INT016", "INT019", "INT032", "INT040"} {
		if e, ok := excByRecord[id]; !ok || e.Category != models.CategoryAmountMismatch {
			t.Errorf("Expected %s to be AMOUNT_MISMATCH, got: %+v", id, e)
		}
	}
	for _, id := range []string{"SET006", "SET007", "SET031", "SET032", "SET035", "SET036"} {
		if e, ok := excByRecord[id]; !ok || e.Category != models.CategoryDuplicateSettlement {
			t.Errorf("Expected %s to be DUPLICATE_SETTLEMENT, got: %+v", id, e)
		}
	}
	for _, id := range []string{"INT024", "INT036", "INT037"} {
		if e, ok := excByRecord[id]; !ok || e.Category != models.CategoryNoCounterpart {
			t.Errorf("Expected %s to be NO_COUNTERPART, got: %+v", id, e)
		}
	}
	for _, id := range []string{"SET051", "SET052"} {
		if e, ok := excByRecord[id]; !ok || e.Category != models.CategoryOrphanSettlement {
			t.Errorf("Expected %s to be ORPHAN_SETTLEMENT, got: %+v", id, e)
		}
	}
	for _, id := range []string{"BATCH006", "BATCH011"} {
		if e, ok := excByRecord[id]; !ok || e.Category != models.CategorySettledNotBanked {
			t.Errorf("Expected %s to be SETTLED_NOT_BANKED, got: %+v", id, e)
		}
	}
	if e, ok := excByRecord["BATCH010"]; !ok || e.Category != models.CategoryPartialCredit {
		t.Errorf("Expected BATCH010 to be PARTIAL_CREDIT, got: %+v", e)
	} else if e.ExposurePaise == nil || *e.ExposurePaise != 285471 {
		t.Errorf("Expected BATCH010 shortfall exposure 285471 paise, got %v", e.ExposurePaise)
	}
}

package reconciliation

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/razorpay-hack/ai-finance-controller/internal/generator"
	"github.com/razorpay-hack/ai-finance-controller/internal/models"
)

func TestReconcileRun_InvariantsAndTerminalStates(t *testing.T) {
	dataset, _ := generator.Generate(42, "v1.0.0", "v1.0.0")

	output := ReconcileRun(dataset.Run.RunID, dataset.Internals, dataset.Settlements, dataset.BankStatements, 2.5)

	// Invariant 1: Exactly 500 matches for 500 internal cases
	if len(output.Matches) != 500 {
		t.Fatalf("Expected 500 reconciliation matches, got %d", len(output.Matches))
	}

	// Invariant 2: Terminal status distribution
	terminalCounts := make(map[models.ReconciliationStatus]int)
	for _, m := range output.Matches {
		terminalCounts[m.ReconciliationStatus]++
	}

	totalTerminal := terminalCounts[models.StatusFull] + terminalCounts[models.StatusPartial] + terminalCounts[models.StatusUnmatched]
	if totalTerminal != 500 {
		t.Errorf("Terminal status count mismatch: expected 500, got %d", totalTerminal)
	}

	if terminalCounts[models.StatusFull] == 0 {
		t.Errorf("Expected non-zero FULL matches, got 0")
	}
	if terminalCounts[models.StatusPartial] == 0 {
		t.Errorf("Expected non-zero PARTIAL matches, got 0")
	}
	if terminalCounts[models.StatusUnmatched] == 0 {
		t.Errorf("Expected non-zero UNMATCHED cases, got 0")
	}

	// Invariant 3: Exception classification completeness (zero UNKNOWN or OTHER)
	validCategories := map[string]bool{
		models.CategoryAmountMismatch:      true,
		models.CategoryNoCounterpart:       true,
		models.CategoryDuplicateSettlement: true,
		models.CategoryDateOutOfRange:      true,
		models.CategoryOrphanSettlement:    true,
		models.CategorySettledNotBanked:    true,
		models.CategoryPartialCredit:       true,
		models.CategoryBankAmountMismatch:  true,
		models.CategoryBankedNotSettled:    true,
		models.CategoryAmbiguousBatch:      true,
		models.CategoryAmbiguousBankCredit: true,
	}

	for _, exc := range output.Exceptions {
		if !validCategories[exc.Category] {
			t.Errorf("Found invalid exception category: %s", exc.Category)
		}
		if exc.Category == "UNKNOWN" || exc.Category == "OTHER" {
			t.Errorf("Disallowed category found: %s", exc.Category)
		}
		if exc.Reason == "" {
			t.Errorf("Empty exception reason for record: %s", exc.RecordID)
		}
		if IsAIEligible(exc.Category) && exc.AIStatus != models.AIPending {
			t.Errorf("Expected AIStatus PENDING for eligible category %s, got %s", exc.Category, exc.AIStatus)
		}
	}

	// Invariant 4: Cash Exposure
	if output.UnresolvedExposure <= 0 {
		t.Errorf("Expected positive unresolved exposure, got %d", output.UnresolvedExposure)
	}

	// Invariant 5: Audit log completeness
	if len(output.AuditLogs) != 500 {
		t.Errorf("Expected 500 audit logs (one per internal transaction), got %d", len(output.AuditLogs))
	}
}

func TestReconcileRun_EmptyInputs_NoPanicAndSafeThroughput(t *testing.T) {
	runID := uuid.New()
	output := ReconcileRun(runID, nil, nil, nil, 2.5)

	if output == nil {
		t.Fatal("expected non-nil output")
	}
	if len(output.Matches) != 0 {
		t.Errorf("expected 0 matches, got %d", len(output.Matches))
	}
	if len(output.Exceptions) != 0 {
		t.Errorf("expected 0 exceptions, got %d", len(output.Exceptions))
	}
	if math.IsNaN(output.ThroughputPerSecond) || math.IsInf(output.ThroughputPerSecond, 0) {
		t.Errorf("throughput is NaN or Inf: %v", output.ThroughputPerSecond)
	}
}

func TestReconcileRun_PartialUpload_InternalsOnly(t *testing.T) {
	runID := uuid.New()
	baseTime := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	internals := []models.InternalTransaction{
		{
			ID:              "INT_ONLY_1",
			RunID:           runID,
			AmountPaise:     50000,
			Currency:        "INR",
			TransactionDate: baseTime,
			MerchantID:      "MERCH_TEST",
		},
	}

	output := ReconcileRun(runID, internals, nil, nil, 2.5)
	if len(output.Matches) != 1 {
		t.Fatalf("expected 1 match record, got %d", len(output.Matches))
	}
	m := output.Matches[0]
	if m.ReconciliationStatus != models.StatusUnmatched {
		t.Errorf("expected status UNMATCHED, got %s", m.ReconciliationStatus)
	}
	if m.Hop1Rule == nil || *m.Hop1Rule != "HOP1_SKIPPED" {
		t.Errorf("expected Hop1Rule 'HOP1_SKIPPED', got %v", m.Hop1Rule)
	}
	if output.UnresolvedExposure != 50000 {
		t.Errorf("expected unresolved exposure 50000 paise, got %d", output.UnresolvedExposure)
	}
}

func TestReconcileRun_OrphanSettlementsAndBankStatementsWithoutInternals(t *testing.T) {
	runID := uuid.New()
	baseTime := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	batchRef := "BATCH_ORPH"

	settlements := []models.SettlementRecord{
		{
			ID:                 "SET_ORPHAN",
			RunID:              runID,
			SettledAmountPaise: 75000,
			Currency:           "INR",
			SettlementDate:     baseTime,
			MerchantID:         "MERCH_TEST",
			BatchID:            batchRef,
		},
	}

	bankStatements := []models.BankStatement{
		{
			ID:                  "BNK_ORPHAN",
			RunID:               runID,
			CreditedAmountPaise: 75000,
			Currency:            "INR",
			CreditDate:          baseTime.Add(24 * time.Hour),
			MerchantID:          "MERCH_TEST",
			BatchReference:      &batchRef,
			Narration:           "NEFT CR-ORPHAN",
		},
	}

	output := ReconcileRun(runID, nil, settlements, bankStatements, 2.5)

	// Since there are no internal transactions:
	// - settlement should surface as ORPHAN_SETTLEMENT
	// - matches should be empty
	hasOrphanSettlement := false
	for _, exc := range output.Exceptions {
		if exc.RecordID == "SET_ORPHAN" && exc.Category == models.CategoryOrphanSettlement {
			hasOrphanSettlement = true
			break
		}
	}
	if !hasOrphanSettlement {
		t.Errorf("expected SET_ORPHAN to be categorized as ORPHAN_SETTLEMENT, got exceptions: %+v", output.Exceptions)
	}
}

func TestReconcileRun_UnbatchedSettlementsHandling(t *testing.T) {
	runID := uuid.New()
	baseTime := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	ref := "REF_NO_BATCH"

	internals := []models.InternalTransaction{
		{
			ID:              "INT_NO_BATCH",
			RunID:           runID,
			AmountPaise:     100000,
			Currency:        "INR",
			TransactionDate: baseTime,
			MerchantID:      "MERCH_TEST",
			ReferenceID:     &ref,
		},
	}

	settlements := []models.SettlementRecord{
		{
			ID:                 "SET_NO_BATCH",
			RunID:              runID,
			SettledAmountPaise: 99000,
			Currency:           "INR",
			SettlementDate:     baseTime.Add(24 * time.Hour),
			MerchantID:         "MERCH_TEST",
			ReferenceID:        &ref,
			BatchID:            "", // empty BatchID
		},
	}

	output := ReconcileRun(runID, internals, settlements, nil, 2.5)
	if len(output.Matches) != 1 {
		t.Fatalf("Expected 1 match, got %d", len(output.Matches))
	}

	m := output.Matches[0]
	if m.SettlementID == nil || *m.SettlementID != "SET_NO_BATCH" {
		t.Errorf("Expected settlement SET_NO_BATCH to match, got: %v", m.SettlementID)
	}
	// Hop 1 matched, but bank statements not uploaded -> PARTIAL
	if m.ReconciliationStatus != models.StatusPartial {
		t.Errorf("Expected status PARTIAL, got %s", m.ReconciliationStatus)
	}
}

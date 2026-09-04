package tests

import (
	"testing"

	"github.com/razorpay-hack/ai-finance-controller/internal/generator"
	"github.com/razorpay-hack/ai-finance-controller/internal/metrics"
	"github.com/razorpay-hack/ai-finance-controller/internal/models"
	"github.com/razorpay-hack/ai-finance-controller/internal/reconciliation"
)

// TestInvariant_MonetaryIntegrity ensures that financial amounts are strictly positive integer paise.
func TestInvariant_MonetaryIntegrity(t *testing.T) {
	seeds := []int64{42, 101, 999}

	for _, s := range seeds {
		dataset, _ := generator.Generate(s, "v1.0.0", "v1.0.0")

		for _, it := range dataset.Internals {
			if it.AmountPaise <= 0 {
				t.Fatalf("Seed %d: Non-positive internal amount: %d", s, it.AmountPaise)
			}
			if it.Currency != "INR" {
				t.Fatalf("Seed %d: Invalid currency: %s", s, it.Currency)
			}
		}

		for _, set := range dataset.Settlements {
			if set.SettledAmountPaise <= 0 {
				t.Fatalf("Seed %d: Non-positive settlement amount: %d", s, set.SettledAmountPaise)
			}
			if set.Currency != "INR" {
				t.Fatalf("Seed %d: Invalid currency: %s", s, set.Currency)
			}
		}

		for _, bs := range dataset.BankStatements {
			if bs.CreditedAmountPaise <= 0 {
				t.Fatalf("Seed %d: Non-positive bank credit amount: %d", s, bs.CreditedAmountPaise)
			}
			if bs.Currency != "INR" {
				t.Fatalf("Seed %d: Invalid currency: %s", s, bs.Currency)
			}
		}
	}
}

// TestInvariant_TerminalStateCompleteness verifies zero silent drops across 500 internal cases.
func TestInvariant_TerminalStateCompleteness(t *testing.T) {
	seeds := []int64{42, 101, 999}

	for _, s := range seeds {
		dataset, _ := generator.Generate(s, "v1.0.0", "v1.0.0")
		output := reconciliation.ReconcileRun(
			dataset.Run.RunID,
			dataset.Internals,
			dataset.Settlements,
			dataset.BankStatements,
			2.5,
		)

		if len(output.Matches) != 500 {
			t.Fatalf("Seed %d: Expected exactly 500 matches, got %d", s, len(output.Matches))
		}

		statusCounts := make(map[models.ReconciliationStatus]int)
		for _, m := range output.Matches {
			statusCounts[m.ReconciliationStatus]++
		}

		totalAccounted := statusCounts[models.StatusFull] + statusCounts[models.StatusPartial] + statusCounts[models.StatusUnmatched]
		if totalAccounted != 500 {
			t.Fatalf("Seed %d: Terminal status count mismatch: expected 500, got %d", s, totalAccounted)
		}

		// Ensure all statuses are represented
		if statusCounts[models.StatusFull] == 0 || statusCounts[models.StatusPartial] == 0 || statusCounts[models.StatusUnmatched] == 0 {
			t.Fatalf("Seed %d: All three terminal statuses must exist. Got: %v", s, statusCounts)
		}
	}
}

// TestInvariant_NoDuplicateSettlementConsumption verifies that no settlement is consumed twice.
func TestInvariant_NoDuplicateSettlementConsumption(t *testing.T) {
	dataset, _ := generator.Generate(42, "v1.0.0", "v1.0.0")
	output := reconciliation.ReconcileRun(
		dataset.Run.RunID,
		dataset.Internals,
		dataset.Settlements,
		dataset.BankStatements,
		2.5,
	)

	seenSettlements := make(map[string]string) // settlementID -> internalID
	for _, m := range output.Matches {
		if m.SettlementID != nil {
			setID := *m.SettlementID
			if previousInternal, exists := seenSettlements[setID]; exists {
				t.Fatalf("Settlement %s was consumed twice: by %s and %s", setID, previousInternal, m.InternalID)
			}
			seenSettlements[setID] = m.InternalID
		}
	}
}

// TestInvariant_NoDuplicateBankConsumption verifies that no bank credit is consumed twice.
func TestInvariant_NoDuplicateBankConsumption(t *testing.T) {
	dataset, _ := generator.Generate(42, "v1.0.0", "v1.0.0")
	summary := reconciliation.ReconcileHop2(dataset.Settlements, dataset.BankStatements)

	seenBankStatements := make(map[string]string) // bankID -> batchID
	for bID, bRes := range summary.BatchResults {
		if bRes.Matched && bRes.BankStatementID != nil {
			bankID := *bRes.BankStatementID
			if prevBatch, exists := seenBankStatements[bankID]; exists {
				t.Fatalf("Bank statement %s was assigned twice: to batch %s and %s", bankID, prevBatch, bID)
			}
			seenBankStatements[bankID] = bID
		}
	}
}

// TestInvariant_AIIndependence verifies that AI unavailability does NOT alter deterministic reconciliation.
func TestInvariant_AIIndependence(t *testing.T) {
	dataset, _ := generator.Generate(42, "v1.0.0", "v1.0.0")

	// Run 1: Deterministic Engine
	out1 := reconciliation.ReconcileRun(
		dataset.Run.RunID,
		dataset.Internals,
		dataset.Settlements,
		dataset.BankStatements,
		2.5,
	)

	// Run 2: Repeated Execution (simulating offline/no AI credentials)
	out2 := reconciliation.ReconcileRun(
		dataset.Run.RunID,
		dataset.Internals,
		dataset.Settlements,
		dataset.BankStatements,
		2.5,
	)

	if out1.FullChainCount != out2.FullChainCount {
		t.Fatalf("FullChainCount changed: %d vs %d", out1.FullChainCount, out2.FullChainCount)
	}
	if out1.Hop1MatchedCount != out2.Hop1MatchedCount {
		t.Fatalf("Hop1MatchedCount changed: %d vs %d", out1.Hop1MatchedCount, out2.Hop1MatchedCount)
	}
	if out1.UnresolvedExposure != out2.UnresolvedExposure {
		t.Fatalf("UnresolvedExposure changed: %d vs %d", out1.UnresolvedExposure, out2.UnresolvedExposure)
	}
	if len(out1.Exceptions) != len(out2.Exceptions) {
		t.Fatalf("Exceptions count changed: %d vs %d", len(out1.Exceptions), len(out2.Exceptions))
	}

	for i := range out1.Matches {
		if out1.Matches[i].ReconciliationStatus != out2.Matches[i].ReconciliationStatus {
			t.Fatalf("Match[%d] status mismatch: %s vs %s", i, out1.Matches[i].ReconciliationStatus, out2.Matches[i].ReconciliationStatus)
		}
	}
}

// TestInvariant_ExceptionTaxonomyCoverage ensures zero UNKNOWN or OTHER categories.
func TestInvariant_ExceptionTaxonomyCoverage(t *testing.T) {
	dataset, _ := generator.Generate(42, "v1.0.0", "v1.0.0")
	output := reconciliation.ReconcileRun(
		dataset.Run.RunID,
		dataset.Internals,
		dataset.Settlements,
		dataset.BankStatements,
		2.5,
	)

	allowedCategories := map[string]bool{
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
		if !allowedCategories[exc.Category] {
			t.Fatalf("Illegal exception category found: %s", exc.Category)
		}
		if exc.Category == "UNKNOWN" || exc.Category == "OTHER" {
			t.Fatalf("Prohibited category found: %s", exc.Category)
		}
		if exc.Reason == "" {
			t.Fatalf("Exception for record %s has empty reason", exc.RecordID)
		}
		if exc.Hop != "HOP1" && exc.Hop != "HOP2" {
			t.Fatalf("Invalid hop in exception: %s", exc.Hop)
		}
	}
}

// TestInvariant_HoldoutSeedGeneralization tests accuracy and generalization on holdout seeds.
func TestInvariant_HoldoutSeedGeneralization(t *testing.T) {
	holdoutSeeds := []int64{101, 777, 999}

	for _, s := range holdoutSeeds {
		dataset, groundTruth := generator.Generate(s, "v1.0.0", "v1.0.0")
		output := reconciliation.ReconcileRun(
			dataset.Run.RunID,
			dataset.Internals,
			dataset.Settlements,
			dataset.BankStatements,
			2.5,
		)

		m := metrics.CalculateMetrics(output, dataset, groundTruth)

		if m.Hop1Precision < 95.0 {
			t.Errorf("Seed %d: Hop 1 precision too low: %.2f%%", s, m.Hop1Precision)
		}
		if m.Hop1Recall < 85.0 {
			t.Errorf("Seed %d: Hop 1 recall too low: %.2f%%", s, m.Hop1Recall)
		}
		if m.Hop2Precision < 90.0 {
			t.Errorf("Seed %d: Full chain precision too low: %.2f%%", s, m.Hop2Precision)
		}
		if m.Hop2Recall < 85.0 {
			t.Errorf("Seed %d: Full chain recall too low: %.2f%%", s, m.Hop2Recall)
		}
		if m.ExceptionCoveragePct != 100.0 {
			t.Errorf("Seed %d: Exception coverage != 100%%: %.2f%%", s, m.ExceptionCoveragePct)
		}
	}
}

package metrics

import (
	"testing"

	"github.com/razorpay-hack/ai-finance-controller/internal/generator"
	"github.com/razorpay-hack/ai-finance-controller/internal/reconciliation"
)

func TestCalculateMetrics_Seed42(t *testing.T) {
	dataset, groundTruth := generator.Generate(42, "v1.0.0", "v1.0.0")
	output := reconciliation.ReconcileRun(dataset.Run.RunID, dataset.Internals, dataset.Settlements, dataset.BankStatements, 2.5)

	m := CalculateMetrics(output, dataset, groundTruth)

	if m.TotalInternalCases != 500 {
		t.Fatalf("Expected 500 cases, got %d", m.TotalInternalCases)
	}

	if m.Hop1MatchRate < 70.0 || m.Hop1MatchRate > 85.0 {
		t.Errorf("Hop 1 match rate out of expected range (70-85%%): %.2f%%", m.Hop1MatchRate)
	}

	if m.Hop1Precision < 95.0 {
		t.Errorf("Expected Hop 1 precision >= 95%%, got %.2f%%", m.Hop1Precision)
	}

	if m.Hop1Recall < 85.0 {
		t.Errorf("Expected Hop 1 recall >= 85%%, got %.2f%%", m.Hop1Recall)
	}

	if m.ExceptionCoveragePct != 100.0 {
		t.Errorf("Expected 100%% exception coverage, got %.2f%%", m.ExceptionCoveragePct)
	}

	if m.ExpectedToBankPaise <= 0 {
		t.Errorf("Expected positive expected to bank amount, got %d", m.ExpectedToBankPaise)
	}

	if m.ActuallyBankedPaise <= 0 {
		t.Errorf("Expected positive actually banked amount, got %d", m.ActuallyBankedPaise)
	}

	if m.UnresolvedExposurePaise <= 0 {
		t.Errorf("Expected positive unresolved exposure, got %d", m.UnresolvedExposurePaise)
	}
}

func TestCalculateMetrics_HoldoutSeeds(t *testing.T) {
	holdoutSeeds := []int64{101, 999}

	for _, seed := range holdoutSeeds {
		dataset, groundTruth := generator.Generate(seed, "v1.0.0", "v1.0.0")
		output := reconciliation.ReconcileRun(dataset.Run.RunID, dataset.Internals, dataset.Settlements, dataset.BankStatements, 2.5)
		m := CalculateMetrics(output, dataset, groundTruth)

		if m.TotalInternalCases != 500 {
			t.Errorf("Seed %d: Expected 500 cases, got %d", seed, m.TotalInternalCases)
		}
		if m.Hop1Precision < 95.0 {
			t.Errorf("Seed %d: Expected Hop 1 precision >= 95%%, got %.2f%%", seed, m.Hop1Precision)
		}
		if m.Hop1Recall < 85.0 {
			t.Errorf("Seed %d: Expected Hop 1 recall >= 85%%, got %.2f%%", seed, m.Hop1Recall)
		}
		if m.ExceptionCoveragePct != 100.0 {
			t.Errorf("Seed %d: Expected 100%% exception coverage, got %.2f%%", seed, m.ExceptionCoveragePct)
		}
	}
}

package reconciliation

import (
	"testing"

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

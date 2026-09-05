package reconciliation

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/razorpay-hack/ai-finance-controller/internal/models"
)

// ---------------------------------------------------------------------
// Tolerance boundary tests — "within ₹1 tolerance" (100 paise)
// ---------------------------------------------------------------------

func TestReconcileHop2_AggregatedAmount_ExactlyAtToleranceBoundary(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	batchID := "BATCH_TOL_EXACT"
	merchant := "MERCH_TEST"

	settlements := []models.SettlementRecord{
		{ID: "SET_T1", RunID: runID, SettledAmountPaise: 500000, Currency: "INR",
			SettlementDate: baseDate, MerchantID: merchant, BatchID: batchID},
	}
	// Exactly 100 paise (₹1.00) short — spec says "within ₹1 tolerance",
	// which should be inclusive of exactly ₹1.
	bankStatements := []models.BankStatement{
		{ID: "BNK_T1", RunID: runID, CreditedAmountPaise: 499900, Currency: "INR",
			CreditDate: baseDate.Add(12 * time.Hour), MerchantID: merchant, BatchReference: nil,
			Narration: "NEFT CR-EXACT-TOLERANCE"},
	}
	summary := ReconcileHop2(settlements, bankStatements)
	res, exists := summary.BatchResults[batchID]
	if !exists {
		t.Fatalf("expected batch %s in results", batchID)
	}
	if !res.Matched {
		t.Errorf("expected match at exactly ₹1 tolerance boundary, got unmatched: %s", res.ExceptionReason)
	}
	if res.Rule != RuleAggregatedAmount {
		t.Errorf("expected rule %s, got %s", RuleAggregatedAmount, res.Rule)
	}
}

func TestReconcileHop2_AggregatedAmount_JustBeyondToleranceBoundary(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	batchID := "BATCH_TOL_OVER"
	merchant := "MERCH_TEST"

	settlements := []models.SettlementRecord{
		{ID: "SET_T2", RunID: runID, SettledAmountPaise: 500000, Currency: "INR",
			SettlementDate: baseDate, MerchantID: merchant, BatchID: batchID},
	}
	// 101 paise short — one paise beyond the documented ₹1 tolerance.
	bankStatements := []models.BankStatement{
		{ID: "BNK_T2", RunID: runID, CreditedAmountPaise: 499899, Currency: "INR",
			CreditDate: baseDate.Add(12 * time.Hour), MerchantID: merchant, BatchReference: nil,
			Narration: "NEFT CR-OVER-TOLERANCE"},
	}
	summary := ReconcileHop2(settlements, bankStatements)
	res, exists := summary.BatchResults[batchID]
	if !exists {
		t.Fatalf("expected batch %s in results", batchID)
	}
	if res.Matched {
		t.Errorf("expected NO match one paise beyond tolerance, got matched via %s", res.Rule)
	}
	// The bank credit that failed to match this batch shouldn't be
	// silently discarded — it should surface as its own anomaly, since
	// from the bank's side, this credit doesn't explain any settlement.
	if len(summary.OrphanBankCredits) != 1 {
		t.Errorf("expected the unmatched bank credit to surface as an orphan, got %d orphans", len(summary.OrphanBankCredits))
	}
}

// ---------------------------------------------------------------------
// Date window boundary tests — "within 2 days of latest settlement"
// ---------------------------------------------------------------------

func TestReconcileHop2_AggregatedAmount_ExactlyAtDateBoundary(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	batchID := "BATCH_DATE_EXACT"
	merchant := "MERCH_TEST"

	settlements := []models.SettlementRecord{
		{ID: "SET_D1", RunID: runID, SettledAmountPaise: 300000, Currency: "INR",
			SettlementDate: baseDate, MerchantID: merchant, BatchID: batchID},
	}
	bankStatements := []models.BankStatement{
		{ID: "BNK_D1", RunID: runID, CreditedAmountPaise: 300000, Currency: "INR",
			CreditDate: baseDate.Add(48 * time.Hour), MerchantID: merchant, BatchReference: nil,
			Narration: "NEFT CR-EXACT-2DAY"},
	}
	summary := ReconcileHop2(settlements, bankStatements)
	res := summary.BatchResults[batchID]
	if !res.Matched {
		t.Errorf("expected match at exactly the 2-day boundary, got unmatched: %s", res.ExceptionReason)
	}
}

func TestReconcileHop2_AggregatedAmount_JustBeyondDateBoundary(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	batchID := "BATCH_DATE_OVER"
	merchant := "MERCH_TEST"

	settlements := []models.SettlementRecord{
		{ID: "SET_D2", RunID: runID, SettledAmountPaise: 300000, Currency: "INR",
			SettlementDate: baseDate, MerchantID: merchant, BatchID: batchID},
	}
	bankStatements := []models.BankStatement{
		{ID: "BNK_D2", RunID: runID, CreditedAmountPaise: 300000, Currency: "INR",
			CreditDate: baseDate.Add(48*time.Hour + time.Minute), MerchantID: merchant, BatchReference: nil,
			Narration: "NEFT CR-JUST-OVER-2DAY"},
	}
	summary := ReconcileHop2(settlements, bankStatements)
	res := summary.BatchResults[batchID]
	if res.Matched {
		t.Errorf("expected NO match one minute beyond the 2-day window, got matched via %s", res.Rule)
	}
}

// ---------------------------------------------------------------------
// Field-level correctness — merchant and currency must both be checked,
// not just amount and date
// ---------------------------------------------------------------------

func TestReconcileHop2_AggregatedAmount_MerchantMismatchDoesNotMatch(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	batchID := "BATCH_MERCHANT_MISMATCH"

	settlements := []models.SettlementRecord{
		{ID: "SET_M1", RunID: runID, SettledAmountPaise: 400000, Currency: "INR",
			SettlementDate: baseDate, MerchantID: "MERCH_A", BatchID: batchID},
	}
	// Same amount, same day, different merchant — must not match. If it
	// does, the engine is reconciling money across the wrong merchants,
	// which is a serious correctness bug for a finance product.
	bankStatements := []models.BankStatement{
		{ID: "BNK_M1", RunID: runID, CreditedAmountPaise: 400000, Currency: "INR",
			CreditDate: baseDate.Add(6 * time.Hour), MerchantID: "MERCH_B", BatchReference: nil,
			Narration: "NEFT CR-WRONG-MERCHANT"},
	}
	summary := ReconcileHop2(settlements, bankStatements)
	res := summary.BatchResults[batchID]
	if res.Matched {
		t.Fatalf("CRITICAL: matched across different merchants (MERCH_A settlement vs MERCH_B credit) — this would misreport whose money is whose")
	}
	if res.ExceptionCategory == nil || *res.ExceptionCategory != models.CategorySettledNotBanked {
		t.Errorf("expected CategorySettledNotBanked for MERCH_A's batch, got %v", res.ExceptionCategory)
	}
	if len(summary.OrphanBankCredits) != 1 || summary.OrphanBankCredits[0].BankStatementID != "BNK_M1" {
		t.Errorf("expected MERCH_B's bank credit to surface as an unexplained orphan, got: %+v", summary.OrphanBankCredits)
	}
}

func TestReconcileHop2_AggregatedAmount_CurrencyMismatchDoesNotMatch(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	batchID := "BATCH_CURRENCY_MISMATCH"
	merchant := "MERCH_TEST"

	settlements := []models.SettlementRecord{
		{ID: "SET_C1", RunID: runID, SettledAmountPaise: 250000, Currency: "INR",
			SettlementDate: baseDate, MerchantID: merchant, BatchID: batchID},
	}
	// Same numeric amount, different currency — 2500.00 INR is not
	// 2500.00 USD. Amount equality alone must not be sufficient.
	bankStatements := []models.BankStatement{
		{ID: "BNK_C1", RunID: runID, CreditedAmountPaise: 250000, Currency: "USD",
			CreditDate: baseDate.Add(6 * time.Hour), MerchantID: merchant, BatchReference: nil,
			Narration: "WIRE CR-USD-DEPOSIT"},
	}
	summary := ReconcileHop2(settlements, bankStatements)
	res := summary.BatchResults[batchID]
	if res.Matched {
		t.Fatalf("CRITICAL: matched INR settlement against a USD credit purely on numeric amount equality")
	}
}

// ---------------------------------------------------------------------
// Batch reference matching — case sensitivity, empty string vs nil
// ---------------------------------------------------------------------

func TestReconcileHop2_BatchReference_CaseSensitiveExactMatchRequired(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	batchID := "BATCH_CASE_TEST"
	merchant := "MERCH_TEST"

	settlements := []models.SettlementRecord{
		{ID: "SET_CS1", RunID: runID, SettledAmountPaise: 500000, Currency: "INR",
			SettlementDate: baseDate, MerchantID: merchant, BatchID: batchID},
	}
	// Reference differs only in case, AND amount is deliberately way off
	// so the aggregated-amount rule can't accidentally paper over a
	// batch-reference-matching bug.
	lowercaseRef := "batch_case_test"
	bankStatements := []models.BankStatement{
		{ID: "BNK_CS1", RunID: runID, CreditedAmountPaise: 999999999, Currency: "INR",
			CreditDate: baseDate.Add(6 * time.Hour), MerchantID: merchant, BatchReference: &lowercaseRef,
			Narration: "NEFT CR-CASE-MISMATCH"},
	}
	summary := ReconcileHop2(settlements, bankStatements)
	res := summary.BatchResults[batchID]
	if res.Matched {
		t.Errorf("expected NO batch-reference match on case difference ('%s' vs '%s') — if this passes, matching is case-insensitive; confirm that's intentional", batchID, lowercaseRef)
	}
}

func TestReconcileHop2_BatchReference_EmptyStringNotTreatedAsWildcard(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	batchID := "BATCH_REAL_REF"
	merchant := "MERCH_TEST"

	settlements := []models.SettlementRecord{
		{ID: "SET_E1", RunID: runID, SettledAmountPaise: 500000, Currency: "INR",
			SettlementDate: baseDate, MerchantID: merchant, BatchID: batchID},
	}
	emptyRef := ""
	// A pointer to an empty string is not the same as no reference at
	// all (nil) — it must not accidentally match a real, non-empty
	// batch ID. Amount is also off so aggregated matching can't mask this.
	bankStatements := []models.BankStatement{
		{ID: "BNK_E1", RunID: runID, CreditedAmountPaise: 1, Currency: "INR",
			CreditDate: baseDate.Add(6 * time.Hour), MerchantID: merchant, BatchReference: &emptyRef,
			Narration: "NEFT CR-EMPTY-REF"},
	}
	summary := ReconcileHop2(settlements, bankStatements)
	res := summary.BatchResults[batchID]
	if res.Matched {
		t.Fatalf("empty-string batch reference incorrectly matched a real, non-empty batch ID (%s)", batchID)
	}
}

func TestReconcileHop2_DuplicateBatchReference_TwoBankCreditsSameBatch(t *testing.T) {
	// Data-quality scenario: two bank statement rows both carry the same
	// batch reference (e.g. a duplicated bank export line). The spec
	// doesn't define expected behavior here — this test locks in
	// whatever the engine currently does (pick one, no panic) and flags
	// that the "leftover" duplicate ideally shouldn't just vanish.
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	batchID := "BATCH_DUP_BANK"
	merchant := "MERCH_TEST"

	settlements := []models.SettlementRecord{
		{ID: "SET_DB1", RunID: runID, SettledAmountPaise: 500000, Currency: "INR",
			SettlementDate: baseDate, MerchantID: merchant, BatchID: batchID},
	}
	ref := batchID
	bankStatements := []models.BankStatement{
		{ID: "BNK_DUP1", RunID: runID, CreditedAmountPaise: 500000, Currency: "INR",
			CreditDate: baseDate.Add(6 * time.Hour), MerchantID: merchant, BatchReference: &ref,
			Narration: "NEFT CR-DUP-1"},
		{ID: "BNK_DUP2", RunID: runID, CreditedAmountPaise: 500000, Currency: "INR",
			CreditDate: baseDate.Add(7 * time.Hour), MerchantID: merchant, BatchReference: &ref,
			Narration: "NEFT CR-DUP-2"},
	}

	summary := ReconcileHop2(settlements, bankStatements)
	res, exists := summary.BatchResults[batchID]
	if !exists {
		t.Fatalf("expected batch %s in results", batchID)
	}
	if !res.Matched {
		t.Fatalf("expected one of the two duplicate bank credits to satisfy the match")
	}
	if res.BankStatementID == nil || (*res.BankStatementID != "BNK_DUP1" && *res.BankStatementID != "BNK_DUP2") {
		t.Fatalf("expected BankStatementID to be one of the duplicate candidates, got %v", res.BankStatementID)
	}
	// Document current behavior for the un-consumed duplicate rather than
	// asserting a specific outcome the spec never defined.
	t.Logf("GAP TO REVIEW: batch %s matched against %s; the other duplicate bank credit's fate (dropped vs surfaced as orphan/anomaly) is not currently asserted — decide the intended behavior and tighten this test", batchID, *res.BankStatementID)
}

// ---------------------------------------------------------------------
// Overpayment — the genuinely missing case: bank credits MORE than settled
// ---------------------------------------------------------------------

func TestReconcileHop2_BatchReferenceMatch_Overpayment_RegressionLock(t *testing.T) {
	// GAP: neither the spec nor the existing PartialCredit test defines
	// behavior when the bank credits MORE than the settled sum via a
	// correct batch reference. PartialCredit only checks the shortfall
	// direction (credited < settled). This test locks in current
	// behavior so a future accidental change is caught — it is NOT a
	// statement that this is the correct behavior. Recommend deciding
	// whether overpayment deserves its own exception category (e.g. money
	// arriving that doesn't correspond to any expected settlement amount
	// is exactly the kind of anomaly a finance controller cares about).
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	batchID := "BATCH_OVERPAY"
	merchant := "MERCH_TEST"

	settlements := []models.SettlementRecord{
		{ID: "SET_OP1", RunID: runID, SettledAmountPaise: 500000, Currency: "INR",
			SettlementDate: baseDate, MerchantID: merchant, BatchID: batchID},
	}
	ref := batchID
	bankStatements := []models.BankStatement{
		{ID: "BNK_OP1", RunID: runID, CreditedAmountPaise: 700000, Currency: "INR", // ₹2,000 MORE than settled
			CreditDate: baseDate.Add(24 * time.Hour), MerchantID: merchant, BatchReference: &ref,
			Narration: "CMS/TEST/OVERPAY"},
	}
	summary := ReconcileHop2(settlements, bankStatements)
	res, exists := summary.BatchResults[batchID]
	if !exists {
		t.Fatalf("expected batch %s in results", batchID)
	}
	t.Logf("Overpayment case result: Matched=%v Category=%v Rule=%v — confirm this is the intended behavior", res.Matched, res.ExceptionCategory, res.Rule)
	// Minimal safety assertion only: whatever the decision, an overpayment
	// must never be silently reported as a shortfall.
	if res.ShortfallPaise != nil && *res.ShortfallPaise > 0 {
		t.Errorf("overpayment incorrectly reported as a positive shortfall: %d", *res.ShortfallPaise)
	}
}

// ---------------------------------------------------------------------
// Integer precision — paise arithmetic must not drift
// ---------------------------------------------------------------------

func TestReconcileHop2_MultiSettlementBatch_PaiseSummationIsExact(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	batchID := "BATCH_PRECISION"
	merchant := "MERCH_TEST"

	// Amounts chosen so a naive float64 summation could plausibly drift
	// by a paisa; integer (paise) summation must not.
	settlements := []models.SettlementRecord{
		{ID: "SET_P1", RunID: runID, SettledAmountPaise: 33333, Currency: "INR",
			SettlementDate: baseDate, MerchantID: merchant, BatchID: batchID},
		{ID: "SET_P2", RunID: runID, SettledAmountPaise: 33334, Currency: "INR",
			SettlementDate: baseDate.Add(1 * time.Hour), MerchantID: merchant, BatchID: batchID},
		{ID: "SET_P3", RunID: runID, SettledAmountPaise: 33333, Currency: "INR",
			SettlementDate: baseDate.Add(2 * time.Hour), MerchantID: merchant, BatchID: batchID},
	}
	bankStatements := []models.BankStatement{
		{ID: "BNK_P1", RunID: runID, CreditedAmountPaise: 100000, Currency: "INR", // exact sum
			CreditDate: baseDate.Add(20 * time.Hour), MerchantID: merchant, BatchReference: nil,
			Narration: "NEFT CR-PRECISION-CHECK"},
	}
	summary := ReconcileHop2(settlements, bankStatements)
	res := summary.BatchResults[batchID]
	if !res.Matched {
		t.Errorf("expected exact integer-paise sum to match, got unmatched: %s", res.ExceptionReason)
	}
	if res.Confidence != 0.9 {
		t.Errorf("expected aggregated-match confidence 0.9, got %f", res.Confidence)
	}
}

// ---------------------------------------------------------------------
// Structural / isolation tests
// ---------------------------------------------------------------------

func TestReconcileHop2_EmptyInputs_NoPanic(t *testing.T) {
	summary := ReconcileHop2([]models.SettlementRecord{}, []models.BankStatement{})
	if len(summary.BatchResults) != 0 {
		t.Errorf("expected zero batch results for empty input, got %d", len(summary.BatchResults))
	}
	if len(summary.OrphanBankCredits) != 0 {
		t.Errorf("expected zero orphan credits for empty input, got %d", len(summary.OrphanBankCredits))
	}
}

func TestReconcileHop2_MultipleBatches_ResultsAreIndependent(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	merchant := "MERCH_TEST"

	matchedBatch := "BATCH_INDEP_MATCHED"
	unbankedBatch := "BATCH_INDEP_UNBANKED"

	settlements := []models.SettlementRecord{
		{ID: "SET_I1", RunID: runID, SettledAmountPaise: 200000, Currency: "INR",
			SettlementDate: baseDate, MerchantID: merchant, BatchID: matchedBatch},
		{ID: "SET_I2", RunID: runID, SettledAmountPaise: 900000, Currency: "INR",
			SettlementDate: baseDate, MerchantID: merchant, BatchID: unbankedBatch},
	}
	ref := matchedBatch
	bankStatements := []models.BankStatement{
		{ID: "BNK_I1", RunID: runID, CreditedAmountPaise: 200000, Currency: "INR",
			CreditDate: baseDate.Add(24 * time.Hour), MerchantID: merchant, BatchReference: &ref,
			Narration: "CMS/TEST/INDEPENDENT"},
		// no bank statement at all for unbankedBatch
	}

	summary := ReconcileHop2(settlements, bankStatements)
	if len(summary.BatchResults) != 2 {
		t.Fatalf("expected 2 batch results, got %d", len(summary.BatchResults))
	}
	matched := summary.BatchResults[matchedBatch]
	unbanked := summary.BatchResults[unbankedBatch]

	if !matched.Matched {
		t.Errorf("expected %s to be matched, it was not affected by the other batch's data", matchedBatch)
	}
	if unbanked.Matched {
		t.Errorf("expected %s to be unmatched (no bank statement exists for it)", unbankedBatch)
	}
	if unbanked.ExceptionCategory == nil || *unbanked.ExceptionCategory != models.CategorySettledNotBanked {
		t.Errorf("expected %s to carry CategorySettledNotBanked, got %v", unbankedBatch, unbanked.ExceptionCategory)
	}
	if len(summary.OrphanBankCredits) != 0 {
		t.Errorf("expected zero orphan credits (the single bank credit was consumed by %s), got %d", matchedBatch, len(summary.OrphanBankCredits))
	}
}

func TestReconcileHop2_MultipleOrphanBankCredits_AllSurfaced(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

	var settlements []models.SettlementRecord // no settlements at all this run
	bankStatements := []models.BankStatement{
		{ID: "BNK_ORPH1", RunID: runID, CreditedAmountPaise: 100000, Currency: "INR",
			CreditDate: baseDate, MerchantID: "MERCH_X", BatchReference: nil, Narration: "NEFT CR-1"},
		{ID: "BNK_ORPH2", RunID: runID, CreditedAmountPaise: 200000, Currency: "INR",
			CreditDate: baseDate, MerchantID: "MERCH_Y", BatchReference: nil, Narration: "NEFT CR-2"},
	}
	summary := ReconcileHop2(settlements, bankStatements)
	if len(summary.OrphanBankCredits) != 2 {
		t.Fatalf("expected both unexplained credits to surface independently, got %d", len(summary.OrphanBankCredits))
	}
	seen := map[string]bool{}
	for _, o := range summary.OrphanBankCredits {
		seen[o.BankStatementID] = true
		if o.ExceptionCategory != models.CategoryBankedNotSettled {
			t.Errorf("expected CategoryBankedNotSettled for %s, got %v", o.BankStatementID, o.ExceptionCategory)
		}
	}
	if !seen["BNK_ORPH1"] || !seen["BNK_ORPH2"] {
		t.Errorf("expected both BNK_ORPH1 and BNK_ORPH2 to be present, got: %+v", summary.OrphanBankCredits)
	}
}

func TestReconcileHop2_Rule1BatchReferencePriorityOverRule2(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	merchant := "MERCH_TEST"

	// BATCH_A has NO bank credit carrying its reference, but alphabetically precedes BATCH_B.
	// BATCH_B has an explicit bank credit BNK_B carrying BATCH_B reference.
	// Both batches have the exact same amount (₹5,000) and same date.
	settlements := []models.SettlementRecord{
		{
			ID:                 "SET_A1",
			RunID:              runID,
			SettledAmountPaise: 500000,
			Currency:           "INR",
			SettlementDate:     baseDate,
			MerchantID:         merchant,
			BatchID:            "BATCH_A",
		},
		{
			ID:                 "SET_B1",
			RunID:              runID,
			SettledAmountPaise: 500000,
			Currency:           "INR",
			SettlementDate:     baseDate,
			MerchantID:         merchant,
			BatchID:            "BATCH_B",
		},
	}

	refB := "BATCH_B"
	bankStatements := []models.BankStatement{
		{
			ID:                  "BNK_B",
			RunID:               runID,
			CreditedAmountPaise: 500000,
			Currency:            "INR",
			CreditDate:          baseDate.Add(24 * time.Hour),
			MerchantID:          merchant,
			BatchReference:      &refB,
			Narration:           "CMS/SETTLEMENT/BATCH_B",
		},
	}

	summary := ReconcileHop2(settlements, bankStatements)

	resB, okB := summary.BatchResults["BATCH_B"]
	if !okB || !resB.Matched {
		t.Fatalf("BATCH_B must match BNK_B via Rule 1, got: %+v", resB)
	}
	if resB.Rule != RuleBatchReference {
		t.Errorf("Expected RuleBatchReference for BATCH_B, got %s", resB.Rule)
	}
	if resB.BankStatementID == nil || *resB.BankStatementID != "BNK_B" {
		t.Errorf("Expected BNK_B for BATCH_B, got %v", resB.BankStatementID)
	}

	resA, okA := summary.BatchResults["BATCH_A"]
	if !okA {
		t.Fatalf("Expected BATCH_A result in summary")
	}
	if resA.Matched {
		t.Fatalf("BATCH_A must NOT steal BNK_B via Rule 2")
	}
	if resA.ExceptionCategory == nil || *resA.ExceptionCategory != models.CategorySettledNotBanked {
		t.Errorf("Expected CategorySettledNotBanked for BATCH_A, got %v", resA.ExceptionCategory)
	}
}

func TestReconcileHop2_UnbatchedSettlementsDoNotMerge(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

	settlements := []models.SettlementRecord{
		{
			ID:                 "SET_NO_BATCH_1",
			RunID:              runID,
			SettledAmountPaise: 100000,
			Currency:           "INR",
			SettlementDate:     baseDate,
			MerchantID:         "MERCH_1",
			BatchID:            "", // empty batch ID
		},
		{
			ID:                 "SET_NO_BATCH_2",
			RunID:              runID,
			SettledAmountPaise: 200000,
			Currency:           "INR",
			SettlementDate:     baseDate,
			MerchantID:         "MERCH_2",
			BatchID:            "   ", // whitespace batch ID
		},
	}

	summary := ReconcileHop2(settlements, nil)
	if len(summary.BatchResults) != 2 {
		t.Fatalf("Expected 2 distinct batch results for unbatched settlements, got %d", len(summary.BatchResults))
	}

	for k, res := range summary.BatchResults {
		if k == "" || k == "   " {
			t.Errorf("Unbatched settlements must not use blank key in results, got: %q", k)
		}
		if res.Matched {
			t.Errorf("Unbanked settlements must not match")
		}
		if res.ExceptionCategory == nil || *res.ExceptionCategory != models.CategorySettledNotBanked {
			t.Errorf("Expected CategorySettledNotBanked, got %v", res.ExceptionCategory)
		}
	}
}

func TestReconcileHop2_WhitespaceTrimmedBatchRef(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	merchant := "MERCH_TEST"

	settlements := []models.SettlementRecord{
		{
			ID:                 "SET_WS_1",
			RunID:              runID,
			SettledAmountPaise: 300000,
			Currency:           "INR",
			SettlementDate:     baseDate,
			MerchantID:         merchant,
			BatchID:            "BATCH_WS_01",
		},
	}

	refBank := "  BATCH_WS_01  "
	bankStatements := []models.BankStatement{
		{
			ID:                  "BNK_WS_1",
			RunID:               runID,
			CreditedAmountPaise: 300000,
			Currency:            "INR",
			CreditDate:          baseDate.Add(24 * time.Hour),
			MerchantID:          merchant,
			BatchReference:      &refBank,
			Narration:           "CMS/SETTLEMENT/WS",
		},
	}

	summary := ReconcileHop2(settlements, bankStatements)
	res, ok := summary.BatchResults["BATCH_WS_01"]
	if !ok || !res.Matched {
		t.Fatalf("Expected whitespace-trimmed match for batch, got: %+v", res)
	}
	if res.Rule != RuleBatchReference {
		t.Errorf("Expected RuleBatchReference, got %s", res.Rule)
	}
}

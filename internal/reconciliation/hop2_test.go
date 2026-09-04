package reconciliation

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/razorpay-hack/ai-finance-controller/internal/generator"
	"github.com/razorpay-hack/ai-finance-controller/internal/models"
)

func TestReconcileHop2_BatchReferenceMatch(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	batchID := "BATCH_TEST_01"
	merchant := "MERCH_TEST"

	settlements := []models.SettlementRecord{
		{
			ID:                 "SET01",
			RunID:              runID,
			SettledAmountPaise: 500000,
			Currency:           "INR",
			SettlementDate:     baseDate,
			MerchantID:         merchant,
			BatchID:            batchID,
		},
		{
			ID:                 "SET02",
			RunID:              runID,
			SettledAmountPaise: 300000,
			Currency:           "INR",
			SettlementDate:     baseDate.Add(2 * time.Hour),
			MerchantID:         merchant,
			BatchID:            batchID,
		},
	}

	ref := batchID
	bankStatements := []models.BankStatement{
		{
			ID:                  "BNK01",
			RunID:               runID,
			CreditedAmountPaise: 800000, // Exact sum
			Currency:            "INR",
			CreditDate:          baseDate.Add(24 * time.Hour),
			MerchantID:          merchant,
			BatchReference:      &ref,
			Narration:           "CMS/TEST/BATCH_TEST_01",
		},
	}

	summary := ReconcileHop2(settlements, bankStatements)
	res, exists := summary.BatchResults[batchID]
	if !exists {
		t.Fatalf("Expected batch %s in results", batchID)
	}

	if !res.Matched {
		t.Errorf("Expected batch match, got unmatched: %s", res.ExceptionReason)
	}
	if res.Rule != RuleBatchReference {
		t.Errorf("Expected rule %s, got %s", RuleBatchReference, res.Rule)
	}
	if res.Confidence != 1.0 {
		t.Errorf("Expected confidence 1.0, got %f", res.Confidence)
	}
	if *res.BankStatementID != "BNK01" {
		t.Errorf("Expected bank statement BNK01, got %s", *res.BankStatementID)
	}
}

func TestReconcileHop2_AggregatedAmountMatch(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	batchID := "BATCH_NO_REF"
	merchant := "MERCH_TEST"

	settlements := []models.SettlementRecord{
		{
			ID:                 "SET03",
			RunID:              runID,
			SettledAmountPaise: 450000,
			Currency:           "INR",
			SettlementDate:     baseDate,
			MerchantID:         merchant,
			BatchID:            batchID,
		},
	}

	// Bank credit has no batch reference, but exact amount and within ±2 days
	bankStatements := []models.BankStatement{
		{
			ID:                  "BNK02",
			RunID:               runID,
			CreditedAmountPaise: 450000,
			Currency:            "INR",
			CreditDate:          baseDate.Add(12 * time.Hour),
			MerchantID:          merchant,
			BatchReference:      nil,
			Narration:           "NEFT CR-MERCH_TEST-SETTLEMENT",
		},
	}

	summary := ReconcileHop2(settlements, bankStatements)
	res, exists := summary.BatchResults[batchID]
	if !exists {
		t.Fatalf("Expected batch %s in results", batchID)
	}

	if !res.Matched {
		t.Errorf("Expected aggregate match, got unmatched: %s", res.ExceptionReason)
	}
	if res.Rule != RuleAggregatedAmount {
		t.Errorf("Expected rule %s, got %s", RuleAggregatedAmount, res.Rule)
	}
	if res.Confidence != 0.9 {
		t.Errorf("Expected confidence 0.9, got %f", res.Confidence)
	}
}

func TestReconcileHop2_PartialCredit(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	batchID := "BATCH_PARTIAL"
	merchant := "MERCH_TEST"

	settlements := []models.SettlementRecord{
		{
			ID:                 "SET04",
			RunID:              runID,
			SettledAmountPaise: 1000000, // ₹10,000.00
			Currency:           "INR",
			SettlementDate:     baseDate,
			MerchantID:         merchant,
			BatchID:            batchID,
		},
	}

	ref := batchID
	bankStatements := []models.BankStatement{
		{
			ID:                  "BNK03",
			RunID:               runID,
			CreditedAmountPaise: 800000, // ₹8,000.00 (₹2,000.00 shortfall)
			Currency:            "INR",
			CreditDate:          baseDate.Add(24 * time.Hour),
			MerchantID:          merchant,
			BatchReference:      &ref,
			Narration:           "CMS/TEST/SHORTFALL",
		},
	}

	summary := ReconcileHop2(settlements, bankStatements)
	res, exists := summary.BatchResults[batchID]
	if !exists {
		t.Fatalf("Expected batch %s in results", batchID)
	}

	if res.Matched {
		t.Errorf("Expected partial credit to be marked non-matched (exception), got matched")
	}
	if res.ExceptionCategory == nil || *res.ExceptionCategory != models.CategoryPartialCredit {
		t.Errorf("Expected CategoryPartialCredit, got %v", res.ExceptionCategory)
	}
	if res.ShortfallPaise == nil || *res.ShortfallPaise != 200000 {
		t.Errorf("Expected shortfall 200000 paise, got %v", res.ShortfallPaise)
	}
	if res.ExposurePaise == nil || *res.ExposurePaise != 200000 {
		t.Errorf("Expected exposure 200000 paise, got %v", res.ExposurePaise)
	}
}

func TestReconcileHop2_SettledNotBanked(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	batchID := "BATCH_UNBANKED"
	merchant := "MERCH_TEST"

	settlements := []models.SettlementRecord{
		{
			ID:                 "SET05",
			RunID:              runID,
			SettledAmountPaise: 500000,
			Currency:           "INR",
			SettlementDate:     baseDate,
			MerchantID:         merchant,
			BatchID:            batchID,
		},
	}

	// No bank statements
	var bankStatements []models.BankStatement

	summary := ReconcileHop2(settlements, bankStatements)
	res, exists := summary.BatchResults[batchID]
	if !exists {
		t.Fatalf("Expected batch in results")
	}

	if res.Matched {
		t.Errorf("Expected unmatched batch")
	}
	if res.ExceptionCategory == nil || *res.ExceptionCategory != models.CategorySettledNotBanked {
		t.Errorf("Expected CategorySettledNotBanked, got %v", res.ExceptionCategory)
	}
	if res.ExposurePaise == nil || *res.ExposurePaise != 500000 {
		t.Errorf("Expected exposure 500000 paise, got %v", res.ExposurePaise)
	}
}

func TestReconcileHop2_BankedNotSettled(t *testing.T) {
	runID := uuid.New()
	baseDate := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

	var settlements []models.SettlementRecord

	bankStatements := []models.BankStatement{
		{
			ID:                  "BNK_ORPHAN",
			RunID:               runID,
			CreditedAmountPaise: 1250000,
			Currency:            "INR",
			CreditDate:          baseDate,
			MerchantID:          "MERCH_UNKNOWN",
			BatchReference:      nil,
			Narration:           "NEFT CR-UNKNOWN DEPOSIT",
		},
	}

	summary := ReconcileHop2(settlements, bankStatements)
	if len(summary.OrphanBankCredits) != 1 {
		t.Fatalf("Expected 1 orphan bank credit, got %d", len(summary.OrphanBankCredits))
	}

	orphan := summary.OrphanBankCredits[0]
	if orphan.BankStatementID != "BNK_ORPHAN" {
		t.Errorf("Expected BNK_ORPHAN, got %s", orphan.BankStatementID)
	}
	if orphan.ExceptionCategory != models.CategoryBankedNotSettled {
		t.Errorf("Expected CategoryBankedNotSettled, got %s", orphan.ExceptionCategory)
	}
	if orphan.ExposurePaise != 1250000 {
		t.Errorf("Expected exposure 1250000, got %d", orphan.ExposurePaise)
	}
}

func TestReconcileHop2_FullDatasetPerformance(t *testing.T) {
	dataset, _ := generator.Generate(42, "v1.0.0", "v1.0.0")

	start := time.Now()
	summary := ReconcileHop2(dataset.Settlements, dataset.BankStatements)
	duration := time.Since(start)

	t.Logf("Hop 2 processed %d batches and %d bank records in %v",
		len(summary.BatchResults), len(dataset.BankStatements), duration)

	matchedBatches := 0
	for _, b := range summary.BatchResults {
		if b.Matched {
			matchedBatches++
		}
	}

	t.Logf("Batches: Total %d, Matched %d (Match Rate: %.1f%%)",
		len(summary.BatchResults), matchedBatches, float64(matchedBatches)/float64(len(summary.BatchResults))*100.0)
	t.Logf("Orphan/Unexplained Bank Credits: %d", len(summary.OrphanBankCredits))

	if len(summary.BatchResults) == 0 {
		t.Errorf("Expected non-zero batches")
	}
	if matchedBatches == 0 {
		t.Errorf("Expected matched batches")
	}
}

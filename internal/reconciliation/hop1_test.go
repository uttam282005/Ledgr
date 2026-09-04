package reconciliation

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/razorpay-hack/ai-finance-controller/internal/generator"
	"github.com/razorpay-hack/ai-finance-controller/internal/models"
)

func TestReconcileHop1_ExactReference(t *testing.T) {
	runID := uuid.New()
	baseTime := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	ref := "REF_1001"

	internals := []models.InternalTransaction{
		{
			ID:              "INT001",
			RunID:           runID,
			AmountPaise:     100000, // ₹1,000.00
			Currency:        "INR",
			TransactionDate: baseTime,
			MerchantID:      "MERCH_TEST",
			ReferenceID:     &ref,
		},
	}

	settlements := []models.SettlementRecord{
		{
			ID:                 "SET001",
			RunID:              runID,
			SettledAmountPaise: 98500, // ₹985.00 (1.5% fee, within 2.5% tolerance)
			Currency:           "INR",
			SettlementDate:     baseTime.Add(24 * time.Hour), // 1 day later
			MerchantID:         "MERCH_TEST",
			ReferenceID:        &ref,
			BatchID:            "BATCH_01",
		},
	}

	results := ReconcileHop1(internals, settlements, 2.5)
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	res := results[0]
	if !res.Matched {
		t.Errorf("Expected match, got unmatched: %v", res.ExceptionReason)
	}
	if res.Rule != RuleExactReference {
		t.Errorf("Expected rule %s, got %s", RuleExactReference, res.Rule)
	}
	if res.Confidence != 1.0 {
		t.Errorf("Expected confidence 1.0, got %f", res.Confidence)
	}
	if *res.SettlementID != "SET001" {
		t.Errorf("Expected settlement SET001, got %s", *res.SettlementID)
	}
	if *res.FeeDeltaPaise != 1500 {
		t.Errorf("Expected fee delta 1500, got %d", *res.FeeDeltaPaise)
	}
}

func TestReconcileHop1_AmountDateWindow(t *testing.T) {
	runID := uuid.New()
	baseTime := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	internals := []models.InternalTransaction{
		{
			ID:              "INT002",
			RunID:           runID,
			AmountPaise:     200000, // ₹2,000.00
			Currency:        "INR",
			TransactionDate: baseTime,
			MerchantID:      "MERCH_TEST",
			ReferenceID:     nil,
		},
	}

	settlements := []models.SettlementRecord{
		{
			ID:                 "SET002",
			RunID:              runID,
			SettledAmountPaise: 196000, // ₹1,960.00 (2.0% fee)
			Currency:           "INR",
			SettlementDate:     baseTime.Add(48 * time.Hour), // 2 days later
			MerchantID:         "MERCH_TEST",
			ReferenceID:        nil,
			BatchID:            "BATCH_01",
		},
	}

	results := ReconcileHop1(internals, settlements, 2.5)
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	res := results[0]
	if !res.Matched {
		t.Errorf("Expected match, got unmatched: %v", res.ExceptionReason)
	}
	if res.Rule != RuleAmountDateWindow {
		t.Errorf("Expected rule %s, got %s", RuleAmountDateWindow, res.Rule)
	}
	if res.Confidence != 0.9 {
		t.Errorf("Expected confidence 0.9, got %f", res.Confidence)
	}
	if *res.SettlementID != "SET002" {
		t.Errorf("Expected settlement SET002, got %s", *res.SettlementID)
	}
}

func TestReconcileHop1_ExtendedDateWindow(t *testing.T) {
	runID := uuid.New()
	baseTime := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	internals := []models.InternalTransaction{
		{
			ID:              "INT003",
			RunID:           runID,
			AmountPaise:     50000,
			Currency:        "INR",
			TransactionDate: baseTime,
			MerchantID:      "MERCH_TEST",
		},
	}

	settlements := []models.SettlementRecord{
		{
			ID:                 "SET003",
			RunID:              runID,
			SettledAmountPaise: 49500,
			Currency:           "INR",
			SettlementDate:     baseTime.Add(6 * 24 * time.Hour), // 6 days later
			MerchantID:         "MERCH_TEST",
			BatchID:            "BATCH_01",
		},
	}

	results := ReconcileHop1(internals, settlements, 2.5)
	res := results[0]
	if !res.Matched {
		t.Errorf("Expected match, got unmatched: %v", res.ExceptionReason)
	}
	if res.Rule != RuleExtendedDateWindow {
		t.Errorf("Expected rule %s, got %s", RuleExtendedDateWindow, res.Rule)
	}
	if res.Confidence != 0.7 {
		t.Errorf("Expected confidence 0.7, got %f", res.Confidence)
	}
	if !res.FlagForReview {
		t.Errorf("Expected flag_for_review = true for extended date window")
	}
}

func TestReconcileHop1_AmountMismatch(t *testing.T) {
	runID := uuid.New()
	baseTime := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	internals := []models.InternalTransaction{
		{
			ID:              "INT004",
			RunID:           runID,
			AmountPaise:     100000,
			Currency:        "INR",
			TransactionDate: baseTime,
			MerchantID:      "MERCH_TEST",
		},
	}

	settlements := []models.SettlementRecord{
		{
			ID:                 "SET004",
			RunID:              runID,
			SettledAmountPaise: 95000, // 5% fee discount (exceeds 2.5% max)
			Currency:           "INR",
			SettlementDate:     baseTime.Add(24 * time.Hour),
			MerchantID:         "MERCH_TEST",
			BatchID:            "BATCH_01",
		},
	}

	results := ReconcileHop1(internals, settlements, 2.5)
	res := results[0]
	if res.Matched {
		t.Errorf("Expected amount mismatch exception, but got matched")
	}
	if res.ExceptionCategory == nil || *res.ExceptionCategory != models.CategoryAmountMismatch {
		t.Errorf("Expected CategoryAmountMismatch, got %v", res.ExceptionCategory)
	}
}

func TestReconcileHop1_DuplicateSettlement(t *testing.T) {
	runID := uuid.New()
	baseTime := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	internals := []models.InternalTransaction{
		{
			ID:              "INT005",
			RunID:           runID,
			AmountPaise:     100000,
			Currency:        "INR",
			TransactionDate: baseTime,
			MerchantID:      "MERCH_TEST",
		},
	}

	settlements := []models.SettlementRecord{
		{
			ID:                 "SET005A",
			RunID:              runID,
			SettledAmountPaise: 99000,
			Currency:           "INR",
			SettlementDate:     baseTime.Add(24 * time.Hour),
			MerchantID:         "MERCH_TEST",
			BatchID:            "BATCH_01",
		},
		{
			ID:                 "SET005B",
			RunID:              runID,
			SettledAmountPaise: 99000,
			Currency:           "INR",
			SettlementDate:     baseTime.Add(26 * time.Hour),
			MerchantID:         "MERCH_TEST",
			BatchID:            "BATCH_01",
		},
	}

	results := ReconcileHop1(internals, settlements, 2.5)
	res := results[0]
	if res.Matched {
		t.Errorf("Expected duplicate settlement exception, but got matched")
	}
	if res.ExceptionCategory == nil || *res.ExceptionCategory != models.CategoryDuplicateSettlement {
		t.Errorf("Expected CategoryDuplicateSettlement, got %v", res.ExceptionCategory)
	}
}

func TestReconcileHop1_SettlementSingleConsumption(t *testing.T) {
	runID := uuid.New()
	baseTime := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	internals := []models.InternalTransaction{
		{
			ID:              "INT006",
			RunID:           runID,
			AmountPaise:     100000,
			Currency:        "INR",
			TransactionDate: baseTime,
			MerchantID:      "MERCH_TEST",
		},
		{
			ID:              "INT007",
			RunID:           runID,
			AmountPaise:     100000,
			Currency:        "INR",
			TransactionDate: baseTime,
			MerchantID:      "MERCH_TEST",
		},
	}

	settlements := []models.SettlementRecord{
		{
			ID:                 "SET006",
			RunID:              runID,
			SettledAmountPaise: 99000,
			Currency:           "INR",
			SettlementDate:     baseTime.Add(24 * time.Hour),
			MerchantID:         "MERCH_TEST",
			BatchID:            "BATCH_01",
		},
	}

	results := ReconcileHop1(internals, settlements, 2.5)
	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}

	matchedCount := 0
	for _, r := range results {
		if r.Matched {
			matchedCount++
		}
	}

	if matchedCount != 1 {
		t.Errorf("Expected exactly 1 match (single consumption), got %d", matchedCount)
	}
}

func TestReconcileHop1_FullDatasetPerformance(t *testing.T) {
	dataset, groundTruth := generator.Generate(42, "v1.0.0", "v1.0.0")

	start := time.Now()
	results := ReconcileHop1(dataset.Internals, dataset.Settlements, 2.5)
	duration := time.Since(start)

	if len(results) != 500 {
		t.Fatalf("Expected 500 results, got %d", len(results))
	}

	throughout := float64(len(dataset.Internals)+len(dataset.Settlements)) / duration.Seconds()
	t.Logf("Hop 1 processed %d records in %v (Throughput: %.0f records/sec)",
		len(dataset.Internals)+len(dataset.Settlements), duration, throughout)

	// Compare with ground truth
	gtMap := make(map[string]generator.GroundTruthCase)
	for _, c := range groundTruth.Cases {
		gtMap[c.InternalID] = c
	}

	truePositives := 0
	falsePositives := 0
	falseNegatives := 0

	for _, r := range results {
		gt := gtMap[r.InternalID]
		isTrueMatch := gt.ExpectedHop1 == "MATCHED"

		if r.Matched && isTrueMatch {
			truePositives++
		} else if r.Matched && !isTrueMatch {
			falsePositives++
		} else if !r.Matched && isTrueMatch {
			falseNegatives++
		}
	}

	precision := float64(truePositives) / float64(truePositives+falsePositives) * 100.0
	recall := float64(truePositives) / float64(truePositives+falseNegatives) * 100.0

	t.Logf("Hop 1 Performance: True Positives: %d, False Positives: %d, False Negatives: %d", truePositives, falsePositives, falseNegatives)
	t.Logf("Hop 1 Precision: %.2f%%, Recall: %.2f%%", precision, recall)

	if precision < 95.0 {
		t.Errorf("Expected precision >= 95%%, got %.2f%%", precision)
	}
	if recall < 85.0 {
		t.Errorf("Expected recall >= 85%%, got %.2f%%", recall)
	}
}

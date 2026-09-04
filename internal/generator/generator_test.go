package generator

import (
	"encoding/json"
	"os"
	"testing"
)

func TestGeneratorDeterministicReproducibility(t *testing.T) {
	seed := int64(42)
	ds1, gt1 := Generate(seed, "v1.0.0", "v1.0.0")
	ds2, gt2 := Generate(seed, "v1.0.0", "v1.0.0")

	if len(ds1.Internals) != TotalInternalCases {
		t.Fatalf("Expected %d internals, got %d", TotalInternalCases, len(ds1.Internals))
	}
	if len(ds1.Internals) != len(ds2.Internals) {
		t.Fatalf("Internals length mismatch: %d vs %d", len(ds1.Internals), len(ds2.Internals))
	}
	if len(ds1.Settlements) != len(ds2.Settlements) {
		t.Fatalf("Settlements length mismatch: %d vs %d", len(ds1.Settlements), len(ds2.Settlements))
	}
	if len(ds1.BankStatements) != len(ds2.BankStatements) {
		t.Fatalf("BankStatements length mismatch: %d vs %d", len(ds1.BankStatements), len(ds2.BankStatements))
	}

	// Verify byte-for-byte consistency across records
	for i := range ds1.Internals {
		if ds1.Internals[i].AmountPaise != ds2.Internals[i].AmountPaise {
			t.Errorf("Internal[%d] amount mismatch: %d vs %d", i, ds1.Internals[i].AmountPaise, ds2.Internals[i].AmountPaise)
		}
		if ds1.Internals[i].MerchantID != ds2.Internals[i].MerchantID {
			t.Errorf("Internal[%d] merchant mismatch: %s vs %s", i, ds1.Internals[i].MerchantID, ds2.Internals[i].MerchantID)
		}
	}

	for i := range ds1.Settlements {
		if ds1.Settlements[i].SettledAmountPaise != ds2.Settlements[i].SettledAmountPaise {
			t.Errorf("Settlement[%d] amount mismatch: %d vs %d", i, ds1.Settlements[i].SettledAmountPaise, ds2.Settlements[i].SettledAmountPaise)
		}
	}

	// Verify ground truth cases match
	if len(gt1.Cases) != len(gt2.Cases) {
		t.Fatalf("Ground truth cases count mismatch: %d vs %d", len(gt1.Cases), len(gt2.Cases))
	}
	for i := range gt1.Cases {
		if gt1.Cases[i].ExpectedHop1 != gt2.Cases[i].ExpectedHop1 {
			t.Errorf("GT[%d] Hop1 mismatch: %s vs %s", i, gt1.Cases[i].ExpectedHop1, gt2.Cases[i].ExpectedHop1)
		}
		if gt1.Cases[i].ExpectedFinalStatus != gt2.Cases[i].ExpectedFinalStatus {
			t.Errorf("GT[%d] FinalStatus mismatch: %s vs %s", i, gt1.Cases[i].ExpectedFinalStatus, gt2.Cases[i].ExpectedFinalStatus)
		}
	}
}

func TestGeneratorDifferentSeeds(t *testing.T) {
	ds1, _ := Generate(42, "v1.0.0", "v1.0.0")
	ds2, _ := Generate(101, "v1.0.0", "v1.0.0")

	// Both must have 500 internals
	if len(ds1.Internals) != 500 || len(ds2.Internals) != 500 {
		t.Fatalf("Expected 500 internals each, got %d and %d", len(ds1.Internals), len(ds2.Internals))
	}

	// Must have different amounts
	differCount := 0
	for i := range ds1.Internals {
		if ds1.Internals[i].AmountPaise != ds2.Internals[i].AmountPaise {
			differCount++
		}
	}
	if differCount < 400 {
		t.Errorf("Expected distinct amounts across seeds, only %d differed", differCount)
	}
}

func TestGeneratorInvariants(t *testing.T) {
	ds, gt := Generate(42, "v1.0.0", "v1.0.0")

	if len(ds.Internals) != 500 {
		t.Fatalf("Expected exactly 500 internal cases, got %d", len(ds.Internals))
	}

	for _, item := range ds.Internals {
		if item.AmountPaise <= 0 {
			t.Errorf("Found invalid non-positive amount: %d", item.AmountPaise)
		}
		if item.Currency != "INR" {
			t.Errorf("Found invalid currency: %s", item.Currency)
		}
	}

	for _, item := range ds.Settlements {
		if item.SettledAmountPaise <= 0 {
			t.Errorf("Found invalid non-positive settled amount: %d", item.SettledAmountPaise)
		}
		if item.Currency != "INR" {
			t.Errorf("Found invalid currency: %s", item.Currency)
		}
	}

	for _, item := range ds.BankStatements {
		if item.CreditedAmountPaise <= 0 {
			t.Errorf("Found invalid non-positive credited amount: %d", item.CreditedAmountPaise)
		}
		if item.Currency != "INR" {
			t.Errorf("Found invalid currency: %s", item.Currency)
		}
	}

	// Test ground truth serialization
	tmpDir, err := os.MkdirTemp("", "gt_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	gtFile, err := SaveGroundTruth(gt, tmpDir)
	if err != nil {
		t.Fatalf("Failed to save ground truth: %v", err)
	}

	data, err := os.ReadFile(gtFile)
	if err != nil {
		t.Fatalf("Failed to read saved ground truth: %v", err)
	}

	var loaded GroundTruth
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Failed to unmarshal saved ground truth: %v", err)
	}

	if len(loaded.Cases) != 500 {
		t.Errorf("Expected 500 cases in loaded ground truth, got %d", len(loaded.Cases))
	}
}

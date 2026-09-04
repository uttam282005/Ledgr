package generator

import (
	"fmt"
	"math/rand"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/razorpay-hack/ai-finance-controller/internal/models"
)

const (
	TotalInternalCases = 500

	TargetCleanPct          = 70 // 350
	TargetAmountMismatchPct = 10 // 50
	TargetDateDriftPct      = 8  // 40
	TargetDuplicatePct      = 5  // 25
	TargetOrphanInternalPct = 4  // 20
	TargetOrphanSettlement  = 15 // 15 standalone settlement records
)

// Dataset encapsulates generated source data for a reconciliation run.
type Dataset struct {
	Run            models.ReconciliationRun
	Internals      []models.InternalTransaction
	Settlements    []models.SettlementRecord
	BankStatements []models.BankStatement
}

var merchants = []string{
	"MERCH_SWIGGY_BLR",
	"MERCH_ZOMATO_DEL",
	"MERCH_FLIPKART_BLR",
	"MERCH_AMAZON_MUM",
	"MERCH_NYKAA_MUM",
}

// Generate creates a fully deterministic synthetic dataset and corresponding ground truth.
func Generate(seed int64, datasetVersion, engineVersion string) (*Dataset, *GroundTruth) {
	rng := rand.New(rand.NewSource(seed))
	runID := uuid.New()
	baseDate := time.Date(2026, time.September, 1, 9, 0, 0, 0, time.UTC)

	// Step 1: Allocate case categories to internal transactions
	type caseCategory int
	const (
		catClean caseCategory = iota
		catAmountMismatch
		catDateDrift
		catDuplicate
		catOrphanInternal
	)

	// Build 500 cases with exact proportions
	categories := make([]caseCategory, 0, TotalInternalCases)
	for i := 0; i < 350; i++ {
		categories = append(categories, catClean)
	}
	for i := 0; i < 50; i++ {
		categories = append(categories, catAmountMismatch)
	}
	for i := 0; i < 40; i++ {
		categories = append(categories, catDateDrift)
	}
	for i := 0; i < 25; i++ {
		categories = append(categories, catDuplicate)
	}
	for i := 0; i < 35; i++ { // remaining to make 500 (350+50+40+25+35 = 500)
		categories = append(categories, catOrphanInternal)
	}

	// Shuffle deterministically
	rng.Shuffle(len(categories), func(i, j int) {
		categories[i], categories[j] = categories[j], categories[i]
	})

	var internals []models.InternalTransaction
	var settlements []models.SettlementRecord
	var gtCases []GroundTruthCase

	settlementSeq := 1
	nextSettlementID := func() string {
		id := fmt.Sprintf("SET%05d", settlementSeq)
		settlementSeq++
		return id
	}

	// Helper for batches
	batchCount := 30
	batches := make([]string, batchCount)
	for b := 0; b < batchCount; b++ {
		batches[b] = fmt.Sprintf("BATCH_%03d", b+1)
	}

	// Step 2: Generate internal records and corresponding settlement counterparts
	for i := 0; i < TotalInternalCases; i++ {
		caseNum := i + 1
		internalID := fmt.Sprintf("INT%04d", caseNum)
		cat := categories[i]

		merchant := merchants[rng.Intn(len(merchants))]
		// Gross amount: ₹50.00 to ₹10,000.00 in paise (5,000 to 1,000,000 paise)
		amountPaise := int64(rng.Intn(995000) + 5000)
		txDate := baseDate.Add(time.Duration(rng.Intn(14*24*60)) * time.Minute) // within 14 days

		var refID *string
		if rng.Float64() < 0.60 {
			ref := fmt.Sprintf("REF_%04d_%s", caseNum, merchant[:6])
			refID = &ref
		}

		internal := models.InternalTransaction{
			ID:              internalID,
			RunID:           runID,
			AmountPaise:     amountPaise,
			Currency:        "INR",
			TransactionDate: txDate,
			MerchantID:      merchant,
			ReferenceID:     refID,
			CreatedAt:       txDate,
		}
		internals = append(internals, internal)

		// Determine batch ID
		batchID := batches[i%batchCount]

		// Construct ground truth case
		gtCase := GroundTruthCase{
			CaseID:                  fmt.Sprintf("CASE-%04d", caseNum),
			InternalID:              internalID,
			ExpectedSettlementIDs:   nil,
			ExpectedBankStatementID: nil,
			ExpectedHop1:            "EXCEPTION",
			ExpectedHop2:            "N/A",
			ExpectedFinalStatus:     "UNMATCHED",
			ExpectedException:       nil,
		}

		switch cat {
		case catClean:
			// Normal fee: 0.5% - 2.0% (50 - 200 bps)
			feeBps := int64(rng.Intn(150) + 50)
			feePaise := (amountPaise * feeBps) / 10000
			settledPaise := amountPaise - feePaise
			settleDate := txDate.Add(time.Duration(rng.Intn(48)+24) * time.Hour) // 1–3 days later

			setID := nextSettlementID()
			settlement := models.SettlementRecord{
				ID:                 setID,
				RunID:              runID,
				SettledAmountPaise: settledPaise,
				Currency:           "INR",
				SettlementDate:     settleDate,
				MerchantID:         merchant,
				ReferenceID:        refID,
				BatchID:            batchID,
				CreatedAt:          settleDate,
			}
			settlements = append(settlements, settlement)

			gtCase.ExpectedSettlementIDs = []string{setID}
			gtCase.ExpectedHop1 = "MATCHED"

		case catAmountMismatch:
			// Fee outside tolerance (>2.5% discount e.g. 5% - 10%, or over-settled)
			var settledPaise int64
			if rng.Float64() < 0.70 {
				feeBps := int64(rng.Intn(600) + 350) // 3.5% to 9.5%
				settledPaise = amountPaise - ((amountPaise * feeBps) / 10000)
			} else {
				settledPaise = amountPaise + int64(rng.Intn(5000)+1000) // over amount
			}
			settleDate := txDate.Add(time.Duration(rng.Intn(48)+24) * time.Hour)

			setID := nextSettlementID()
			settlement := models.SettlementRecord{
				ID:                 setID,
				RunID:              runID,
				SettledAmountPaise: settledPaise,
				Currency:           "INR",
				SettlementDate:     settleDate,
				MerchantID:         merchant,
				ReferenceID:        refID,
				BatchID:            batchID,
				CreatedAt:          settleDate,
			}
			settlements = append(settlements, settlement)

			exc := models.CategoryAmountMismatch
			gtCase.ExpectedException = &exc

		case catDateDrift:
			// Normal fee, but settlement is 4–10 days later
			feeBps := int64(rng.Intn(150) + 50)
			settledPaise := amountPaise - ((amountPaise * feeBps) / 10000)
			settleDate := txDate.Add(time.Duration(rng.Intn(144)+96) * time.Hour) // 4–10 days later

			setID := nextSettlementID()
			settlement := models.SettlementRecord{
				ID:                 setID,
				RunID:              runID,
				SettledAmountPaise: settledPaise,
				Currency:           "INR",
				SettlementDate:     settleDate,
				MerchantID:         merchant,
				ReferenceID:        refID,
				BatchID:            batchID,
				CreatedAt:          settleDate,
			}
			settlements = append(settlements, settlement)

			gtCase.ExpectedSettlementIDs = []string{setID}
			gtCase.ExpectedHop1 = "MATCHED"

		case catDuplicate:
			// 2 plausible settlement records
			feeBps := int64(rng.Intn(150) + 50)
			settledPaise := amountPaise - ((amountPaise * feeBps) / 10000)
			settleDate := txDate.Add(time.Duration(rng.Intn(48)+24) * time.Hour)

			setID1 := nextSettlementID()
			setID2 := nextSettlementID()

			s1 := models.SettlementRecord{
				ID:                 setID1,
				RunID:              runID,
				SettledAmountPaise: settledPaise,
				Currency:           "INR",
				SettlementDate:     settleDate,
				MerchantID:         merchant,
				ReferenceID:        refID,
				BatchID:            batchID,
				CreatedAt:          settleDate,
			}
			s2 := models.SettlementRecord{
				ID:                 setID2,
				RunID:              runID,
				SettledAmountPaise: settledPaise,
				Currency:           "INR",
				SettlementDate:     settleDate.Add(2 * time.Hour),
				MerchantID:         merchant,
				ReferenceID:        refID,
				BatchID:            batchID,
				CreatedAt:          settleDate.Add(2 * time.Hour),
			}
			settlements = append(settlements, s1, s2)

			gtCase.ExpectedSettlementIDs = []string{setID1, setID2}
			exc := models.CategoryDuplicateSettlement
			gtCase.ExpectedException = &exc

		case catOrphanInternal:
			// No settlement record generated
			exc := models.CategoryNoCounterpart
			gtCase.ExpectedException = &exc
		}

		gtCases = append(gtCases, gtCase)
	}

	// Step 3: Generate Orphan Settlements (settlements with no internal transaction)
	var orphanSettlements []string
	for o := 0; o < TargetOrphanSettlement; o++ {
		orphanID := nextSettlementID()
		merchant := merchants[rng.Intn(len(merchants))]
		settledPaise := int64(rng.Intn(500000) + 10000)
		settleDate := baseDate.Add(time.Duration(rng.Intn(14*24*60)) * time.Minute)
		batchID := batches[rng.Intn(len(batches))]

		settlement := models.SettlementRecord{
			ID:                 orphanID,
			RunID:              runID,
			SettledAmountPaise: settledPaise,
			Currency:           "INR",
			SettlementDate:     settleDate,
			MerchantID:         merchant,
			ReferenceID:        nil,
			BatchID:            batchID,
			CreatedAt:          settleDate,
		}
		settlements = append(settlements, settlement)
		orphanSettlements = append(orphanSettlements, orphanID)
	}

	// Step 4: Group settlements by batch and create Hop 2 Bank Statements
	type batchSummary struct {
		batchID        string
		merchantID     string
		totalPaise     int64
		latestDate     time.Time
		settlementIDs  []string
		internalTxIDs  []string
	}

	batchMap := make(map[string]*batchSummary)
	// Track which internal transactions map to which settlement
	internalBySettlement := make(map[string]int)
	for i, gt := range gtCases {
		for _, sID := range gt.ExpectedSettlementIDs {
			internalBySettlement[sID] = i
		}
	}

	for _, s := range settlements {
		bs, exists := batchMap[s.BatchID]
		if !exists {
			bs = &batchSummary{
				batchID:    s.BatchID,
				merchantID: s.MerchantID,
				latestDate: s.SettlementDate,
			}
			batchMap[s.BatchID] = bs
		}
		bs.totalPaise += s.SettledAmountPaise
		if s.SettlementDate.After(bs.latestDate) {
			bs.latestDate = s.SettlementDate
		}
		bs.settlementIDs = append(bs.settlementIDs, s.ID)
		if idx, ok := internalBySettlement[s.ID]; ok {
			bs.internalTxIDs = append(bs.internalTxIDs, gtCases[idx].InternalID)
		}
	}

	var bankStatements []models.BankStatement
	var unexplainedBankCredits []string
	bankSeq := 1
	nextBankID := func() string {
		id := fmt.Sprintf("BNK%04d", bankSeq)
		bankSeq++
		return id
	}

	// Sort batches deterministically so iteration order is identical across runs
	batchIDs := make([]string, 0, len(batchMap))
	for bID := range batchMap {
		batchIDs = append(batchIDs, bID)
	}
	sort.Strings(batchIDs)

	// Allocate batch outcomes: 80% correctly banked, 10% missing, 7% unexplained, 3% partial
	for batchIndex, bID := range batchIDs {
		bs := batchMap[bID]
		bankID := nextBankID()
		creditDate := bs.latestDate.Add(24 * time.Hour)

		switch {
		case batchIndex < 24: // ~80% Correctly banked
			ref := bs.batchID
			narration := fmt.Sprintf("CMS/RAZORPAY/SETTLEMENT/%s/%s", bs.batchID, bs.merchantID)
			bankStmt := models.BankStatement{
				ID:                  bankID,
				RunID:               runID,
				CreditedAmountPaise: bs.totalPaise,
				Currency:            "INR",
				CreditDate:          creditDate,
				MerchantID:          bs.merchantID,
				BatchReference:      &ref,
				Narration:           narration,
				CreatedAt:           creditDate,
			}
			bankStatements = append(bankStatements, bankStmt)

			// Update ground truth for internal transactions in this batch
			for _, internalID := range bs.internalTxIDs {
				for i := range gtCases {
					if gtCases[i].InternalID == internalID && gtCases[i].ExpectedHop1 == "MATCHED" {
						gtCases[i].ExpectedBankStatementID = &bankID
						gtCases[i].ExpectedHop2 = "MATCHED"
						gtCases[i].ExpectedFinalStatus = "FULL"
					}
				}
			}

		case batchIndex < 27: // ~10% Missing from bank (SETTLED_NOT_BANKED)
			// No bank statement created
			for _, internalID := range bs.internalTxIDs {
				for i := range gtCases {
					if gtCases[i].InternalID == internalID && gtCases[i].ExpectedHop1 == "MATCHED" {
						gtCases[i].ExpectedHop2 = "EXCEPTION"
						gtCases[i].ExpectedFinalStatus = "PARTIAL"
						exc := models.CategorySettledNotBanked
						gtCases[i].ExpectedException = &exc
					}
				}
			}

		default: // ~10% Partial bank credit (PARTIAL_CREDIT)
			shortfall := int64(rng.Intn(50000) + 10000) // ₹100 - ₹500 shortfall
			credited := bs.totalPaise - shortfall
			if credited <= 0 {
				credited = bs.totalPaise / 2
			}
			ref := bs.batchID
			narration := fmt.Sprintf("RTGS/RAZORPAY/%s/SHORTFALL_ADJ", bs.batchID)
			bankStmt := models.BankStatement{
				ID:                  bankID,
				RunID:               runID,
				CreditedAmountPaise: credited,
				Currency:            "INR",
				CreditDate:          creditDate,
				MerchantID:          bs.merchantID,
				BatchReference:      &ref,
				Narration:           narration,
				CreatedAt:           creditDate,
			}
			bankStatements = append(bankStatements, bankStmt)

			for _, internalID := range bs.internalTxIDs {
				for i := range gtCases {
					if gtCases[i].InternalID == internalID && gtCases[i].ExpectedHop1 == "MATCHED" {
						gtCases[i].ExpectedBankStatementID = &bankID
						gtCases[i].ExpectedHop2 = "EXCEPTION"
						gtCases[i].ExpectedFinalStatus = "PARTIAL"
						exc := models.CategoryPartialCredit
						gtCases[i].ExpectedException = &exc
					}
				}
			}
		}
		batchIndex++
	}

	// Step 5: Add 2 unexplained bank credits (BANKED_NOT_SETTLED)
	for u := 0; u < 2; u++ {
		unexpID := nextBankID()
		merchant := merchants[rng.Intn(len(merchants))]
		creditDate := baseDate.Add(time.Duration(rng.Intn(14*24*60)) * time.Minute)
		creditedPaise := int64(rng.Intn(2000000) + 500000)
		narration := fmt.Sprintf("NEFT CR-CITI00012-UNKNOWN DEPOSIT-%02d", u+1)

		b := models.BankStatement{
			ID:                  unexpID,
			RunID:               runID,
			CreditedAmountPaise: creditedPaise,
			Currency:            "INR",
			CreditDate:          creditDate,
			MerchantID:          merchant,
			BatchReference:      nil,
			Narration:           narration,
			CreatedAt:           creditDate,
		}
		bankStatements = append(bankStatements, b)
		unexplainedBankCredits = append(unexplainedBankCredits, unexpID)
	}

	dataset := &Dataset{
		Run: models.ReconciliationRun{
			RunID:                  runID,
			Seed:                   seed,
			DatasetVersion:         datasetVersion,
			EngineVersion:          engineVersion,
			StartedAt:              time.Now().UTC(),
			InternalCount:          len(internals),
			SettlementCount:        len(settlements),
			BankCount:              len(bankStatements),
			SourceRecordsProcessed: len(internals) + len(settlements) + len(bankStatements),
		},
		Internals:      internals,
		Settlements:    settlements,
		BankStatements: bankStatements,
	}

	groundTruth := &GroundTruth{
		RunID:                  runID,
		Seed:                   seed,
		DatasetVersion:         datasetVersion,
		GeneratedAt:            time.Now().UTC(),
		Cases:                  gtCases,
		OrphanSettlements:      orphanSettlements,
		UnexplainedBankCredits: unexplainedBankCredits,
	}

	return dataset, groundTruth
}

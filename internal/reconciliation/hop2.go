package reconciliation

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/razorpay-hack/ai-finance-controller/internal/models"
)

// Hop2 Rules
const (
	RuleBatchReference   = "BATCH_REFERENCE"
	RuleAggregatedAmount = "AGGREGATED_AMOUNT"
)

// SettlementBatch represents aggregated settlement records under a single batch_id.
type SettlementBatch struct {
	BatchID                string
	MerchantID             string
	Merchants              map[string]bool
	Currency               string
	SettlementCount        int
	SettlementIDs          []string
	TotalAmountPaise       int64
	EarliestSettlementDate time.Time
	LatestSettlementDate   time.Time
}

// HasMerchant checks if a merchant ID is associated with this batch.
func (b *SettlementBatch) HasMerchant(mID string) bool {
	if b == nil {
		return false
	}
	m := strings.TrimSpace(mID)
	if m == "" || m == "MERCH_MULTI" {
		return true
	}
	if b.Merchants == nil {
		return b.MerchantID == m
	}
	return b.Merchants[m]
}

// BankCandidateEvaluation records evaluation of a candidate bank credit for a batch.
type BankCandidateEvaluation struct {
	BankStatementID     string  `json:"bank_statement_id"`
	BatchReferenceMatch bool    `json:"batch_reference_match"`
	CreditedAmountPaise int64   `json:"credited_amount_paise"`
	AmountDeltaPaise    int64   `json:"amount_delta_paise"`
	DaysDifference      float64 `json:"days_difference"`
	Accepted            bool    `json:"accepted"`
	RejectionReason     string  `json:"rejection_reason,omitempty"`
}

// Hop2BatchResult represents the reconciliation result for a settlement batch.
type Hop2BatchResult struct {
	BatchID              string
	BankStatementID      *string
	Matched              bool
	Rule                 string
	Confidence           float64
	ExpectedAmountPaise  int64
	ActualAmountPaise    *int64
	BankDeltaPaise       *int64
	ShortfallPaise       *int64
	ExceptionCategory    *string
	ExceptionReason      string
	ExposurePaise        *int64
	CandidatesConsidered []BankCandidateEvaluation
	FieldsCompared       map[string]interface{}
}

// Hop2OrphanBankCredit represents an unexplained bank credit (BANKED_NOT_SETTLED).
type Hop2OrphanBankCredit struct {
	BankStatementID   string
	CreditedAmount    int64
	MerchantID        string
	Narration         string
	CreditDate        time.Time
	ExceptionCategory string
	ExceptionReason   string
	ExposurePaise     int64
}

// Hop2Summary contains batch results and orphan bank credit tracking.
type Hop2Summary struct {
	BatchResults           map[string]*Hop2BatchResult
	OrphanBankCredits      []Hop2OrphanBankCredit
	ConsumedBankStatements map[string]string // bankStatementID -> batchID
}

// AggregateSettlementBatches groups settlements by batch_id into SettlementBatch structures.
func AggregateSettlementBatches(settlements []models.SettlementRecord) map[string]*SettlementBatch {
	batches := make(map[string]*SettlementBatch)

	for _, s := range settlements {
		batchKey := strings.TrimSpace(s.BatchID)
		if batchKey == "" {
			batchKey = fmt.Sprintf("__UNBATCHED_%s", s.ID)
		}

		b, exists := batches[batchKey]
		if !exists {
			b = &SettlementBatch{
				BatchID:                batchKey,
				MerchantID:             s.MerchantID,
				Merchants:              make(map[string]bool),
				Currency:               s.Currency,
				EarliestSettlementDate: s.SettlementDate,
				LatestSettlementDate:   s.SettlementDate,
			}
			batches[batchKey] = b
		}

		b.Merchants[s.MerchantID] = true
		b.SettlementCount++
		b.SettlementIDs = append(b.SettlementIDs, s.ID)
		b.TotalAmountPaise += s.SettledAmountPaise

		if s.SettlementDate.Before(b.EarliestSettlementDate) {
			b.EarliestSettlementDate = s.SettlementDate
		}
		if s.SettlementDate.After(b.LatestSettlementDate) {
			b.LatestSettlementDate = s.SettlementDate
		}
	}

	return batches
}

// ReconcileHop2 performs deterministic Hop 2 batch reconciliation between settlement batches and bank statements.
func ReconcileHop2(
	settlements []models.SettlementRecord,
	bankStatements []models.BankStatement,
) *Hop2Summary {
	batches := AggregateSettlementBatches(settlements)

	// Sort batch keys stably for deterministic processing order
	batchIDs := make([]string, 0, len(batches))
	for bID := range batches {
		batchIDs = append(batchIDs, bID)
	}
	sort.Strings(batchIDs)

	// Index bank statements by merchant and by batch_reference (normalized)
	bankByRef := make(map[string][]*models.BankStatement)
	bankByMerchant := make(map[string][]*models.BankStatement)

	for i := range bankStatements {
		bs := &bankStatements[i]
		bankByMerchant[bs.MerchantID] = append(bankByMerchant[bs.MerchantID], bs)
		if bs.BatchReference != nil {
			ref := strings.TrimSpace(*bs.BatchReference)
			if ref != "" {
				bankByRef[ref] = append(bankByRef[ref], bs)
			}
		}
	}

	consumedBankStatements := make(map[string]string) // bankID -> batchID
	batchResults := make(map[string]*Hop2BatchResult)

	// Step 1: Rule 1 — Batch Reference Rule across all batches
	for _, bID := range batchIDs {
		batch := batches[bID]
		refKey := strings.TrimSpace(batch.BatchID)
		refCandidates := bankByRef[refKey]
		var availableRefCandidates []*models.BankStatement
		for _, bc := range refCandidates {
			if consumedBy, consumed := consumedBankStatements[bc.ID]; !consumed || consumedBy == batch.BatchID {
				availableRefCandidates = append(availableRefCandidates, bc)
			}
		}

		if len(availableRefCandidates) >= 1 {
			fieldsCompared := map[string]interface{}{
				"batch_id":             batch.BatchID,
				"merchant_id":          batch.MerchantID,
				"settlement_count":     batch.SettlementCount,
				"expected_total_paise": batch.TotalAmountPaise,
				"latest_date":          batch.LatestSettlementDate.Format(time.RFC3339),
			}

			res := &Hop2BatchResult{
				BatchID:             batch.BatchID,
				ExpectedAmountPaise: batch.TotalAmountPaise,
				FieldsCompared:      fieldsCompared,
			}

			// If multiple bank credits claim the same batch reference, rank them deterministically:
			// 1. Merchant match
			// 2. Currency match
			// 3. Exact amount match (delta == 0)
			// 4. Smaller absolute delta
			// 5. Closeness in time to latest settlement
			// 6. Deterministic tie-breaker on bank ID
			if len(availableRefCandidates) > 1 {
				sort.SliceStable(availableRefCandidates, func(i, j int) bool {
					ci := availableRefCandidates[i]
					cj := availableRefCandidates[j]

					mMatchI := batch.HasMerchant(ci.MerchantID)
					mMatchJ := batch.HasMerchant(cj.MerchantID)
					if mMatchI != mMatchJ {
						return mMatchI
					}

					cMatchI := (batch.Currency == "" || ci.Currency == "" || strings.EqualFold(ci.Currency, batch.Currency))
					cMatchJ := (batch.Currency == "" || cj.Currency == "" || strings.EqualFold(cj.Currency, batch.Currency))
					if cMatchI != cMatchJ {
						return cMatchI
					}

					exactI := (ci.CreditedAmountPaise == batch.TotalAmountPaise)
					exactJ := (cj.CreditedAmountPaise == batch.TotalAmountPaise)
					if exactI != exactJ {
						return exactI
					}

					deltaI := int64(math.Abs(float64(ci.CreditedAmountPaise - batch.TotalAmountPaise)))
					deltaJ := int64(math.Abs(float64(cj.CreditedAmountPaise - batch.TotalAmountPaise)))
					if deltaI != deltaJ {
						return deltaI < deltaJ
					}

					daysI := math.Abs(ci.CreditDate.Sub(batch.LatestSettlementDate).Hours())
					daysJ := math.Abs(cj.CreditDate.Sub(batch.LatestSettlementDate).Hours())
					if daysI != daysJ {
						return daysI < daysJ
					}

					return ci.ID < cj.ID
				})
			}

			bc := availableRefCandidates[0]
			diffDays := bc.CreditDate.Sub(batch.LatestSettlementDate).Hours() / 24.0
			delta := bc.CreditedAmountPaise - batch.TotalAmountPaise

			eval := BankCandidateEvaluation{
				BankStatementID:     bc.ID,
				BatchReferenceMatch: true,
				CreditedAmountPaise: bc.CreditedAmountPaise,
				AmountDeltaPaise:    delta,
				DaysDifference:      diffDays,
				Accepted:            false,
			}

			// Validate merchant
			if !batch.HasMerchant(bc.MerchantID) {
				eval.RejectionReason = fmt.Sprintf("Merchant mismatch: %s not in batch merchants", bc.MerchantID)
				res.CandidatesConsidered = []BankCandidateEvaluation{eval}
				cat := models.CategorySettledNotBanked
				res.ExceptionCategory = &cat
				res.ExceptionReason = eval.RejectionReason
				batchResults[batch.BatchID] = res
				continue
			}

			// Validate currency
			if batch.Currency != "" && bc.Currency != "" && !strings.EqualFold(bc.Currency, batch.Currency) {
				eval.RejectionReason = fmt.Sprintf("Currency mismatch: %s vs %s", bc.Currency, batch.Currency)
				res.CandidatesConsidered = []BankCandidateEvaluation{eval}
				cat := models.CategorySettledNotBanked
				res.ExceptionCategory = &cat
				res.ExceptionReason = eval.RejectionReason
				batchResults[batch.BatchID] = res
				continue
			}

			var evaluations []BankCandidateEvaluation
			// Record candidate evaluations for any leftover duplicate candidates
			for _, dup := range availableRefCandidates[1:] {
				evaluations = append(evaluations, BankCandidateEvaluation{
					BankStatementID:     dup.ID,
					BatchReferenceMatch: true,
					CreditedAmountPaise: dup.CreditedAmountPaise,
					AmountDeltaPaise:    dup.CreditedAmountPaise - batch.TotalAmountPaise,
					DaysDifference:      dup.CreditDate.Sub(batch.LatestSettlementDate).Hours() / 24.0,
					Accepted:            false,
					RejectionReason:     "Duplicate batch reference; candidate not selected",
				})
			}

			// Evaluate amounts
			bankID := bc.ID
			res.BankStatementID = &bankID
			res.ActualAmountPaise = &bc.CreditedAmountPaise
			res.BankDeltaPaise = &delta

			if delta == 0 {
				// Perfect match on batch reference
				eval.Accepted = true
				evaluations = append([]BankCandidateEvaluation{eval}, evaluations...)
				res.CandidatesConsidered = evaluations
				res.Matched = true
				res.Rule = RuleBatchReference
				res.Confidence = 1.0
				consumedBankStatements[bc.ID] = batch.BatchID
				batchResults[batch.BatchID] = res
				continue
			} else if delta < 0 {
				// Partial credit: bank credited less than settlement batch total
				shortfall := -delta
				res.ShortfallPaise = &shortfall
				res.ExposurePaise = &shortfall
				eval.Accepted = true // Candidate recognized, but flagged with shortfall
				evaluations = append([]BankCandidateEvaluation{eval}, evaluations...)
				res.CandidatesConsidered = evaluations
				res.Matched = false
				res.Rule = RuleBatchReference
				res.Confidence = 0.5
				cat := models.CategoryPartialCredit
				res.ExceptionCategory = &cat
				res.ExceptionReason = fmt.Sprintf("Bank credit received (₹%.2f) has a shortfall of ₹%.2f against expected batch total ₹%.2f",
					float64(bc.CreditedAmountPaise)/100.0, float64(shortfall)/100.0, float64(batch.TotalAmountPaise)/100.0)
				consumedBankStatements[bc.ID] = batch.BatchID
				batchResults[batch.BatchID] = res
				continue
			} else {
				// Over-credit / amount mismatch
				absDelta := delta
				res.ExposurePaise = &absDelta
				eval.Accepted = true
				evaluations = append([]BankCandidateEvaluation{eval}, evaluations...)
				res.CandidatesConsidered = evaluations
				res.Matched = false
				res.Rule = RuleBatchReference
				res.Confidence = 0.5
				cat := models.CategoryBankAmountMismatch
				res.ExceptionCategory = &cat
				res.ExceptionReason = fmt.Sprintf("Bank credit received (₹%.2f) exceeds expected batch total ₹%.2f by ₹%.2f without explanation",
					float64(bc.CreditedAmountPaise)/100.0, float64(batch.TotalAmountPaise)/100.0, float64(delta)/100.0)
				consumedBankStatements[bc.ID] = batch.BatchID
				batchResults[batch.BatchID] = res
				continue
			}
		}
	}

	// Step 2: Rule 2 — Aggregated amount matching (no batch reference)
	for _, bID := range batchIDs {
		if _, evaluated := batchResults[bID]; evaluated {
			continue
		}
		batch := batches[bID]
		fieldsCompared := map[string]interface{}{
			"batch_id":             batch.BatchID,
			"merchant_id":          batch.MerchantID,
			"settlement_count":     batch.SettlementCount,
			"expected_total_paise": batch.TotalAmountPaise,
			"latest_date":          batch.LatestSettlementDate.Format(time.RFC3339),
		}

		res := &Hop2BatchResult{
			BatchID:             batch.BatchID,
			ExpectedAmountPaise: batch.TotalAmountPaise,
			FieldsCompared:      fieldsCompared,
		}

		var evaluations []BankCandidateEvaluation
		var candidateBankCredits []*models.BankStatement
		if len(batch.Merchants) > 0 {
			seenBankIDs := make(map[string]bool)
			for m := range batch.Merchants {
				for _, bc := range bankByMerchant[m] {
					if !seenBankIDs[bc.ID] {
						seenBankIDs[bc.ID] = true
						candidateBankCredits = append(candidateBankCredits, bc)
					}
				}
			}
		} else {
			candidateBankCredits = bankByMerchant[batch.MerchantID]
		}
		sort.SliceStable(candidateBankCredits, func(i, j int) bool {
			return candidateBankCredits[i].ID < candidateBankCredits[j].ID
		})

		var amountCandidates []*models.BankStatement

		for _, bc := range candidateBankCredits {
			if _, consumed := consumedBankStatements[bc.ID]; consumed {
				continue
			}

			// If bank credit has an explicit batch reference to another batch, do not steal it
			if bc.BatchReference != nil {
				ref := strings.ToUpper(strings.TrimSpace(*bc.BatchReference))
				if ref != "" && ref != strings.ToUpper(strings.TrimSpace(batch.BatchID)) {
					eval := BankCandidateEvaluation{
						BankStatementID:     bc.ID,
						BatchReferenceMatch: false,
						CreditedAmountPaise: bc.CreditedAmountPaise,
						AmountDeltaPaise:    bc.CreditedAmountPaise - batch.TotalAmountPaise,
						DaysDifference:      math.Abs(bc.CreditDate.Sub(batch.LatestSettlementDate).Hours() / 24.0),
						Accepted:            false,
						RejectionReason:     fmt.Sprintf("Bank credit has explicit reference (%s) to a different batch", *bc.BatchReference),
					}
					evaluations = append(evaluations, eval)
					continue
				}
			}

			// Validate currency
			if batch.Currency != "" && bc.Currency != "" && !strings.EqualFold(batch.Currency, bc.Currency) {
				eval := BankCandidateEvaluation{
					BankStatementID:     bc.ID,
					BatchReferenceMatch: false,
					CreditedAmountPaise: bc.CreditedAmountPaise,
					AmountDeltaPaise:    bc.CreditedAmountPaise - batch.TotalAmountPaise,
					DaysDifference:      math.Abs(bc.CreditDate.Sub(batch.LatestSettlementDate).Hours() / 24.0),
					Accepted:            false,
					RejectionReason:     fmt.Sprintf("Currency mismatch: %s vs %s", bc.Currency, batch.Currency),
				}
				evaluations = append(evaluations, eval)
				continue
			}

			diffDays := math.Abs(bc.CreditDate.Sub(batch.LatestSettlementDate).Hours() / 24.0)
			delta := int64(math.Abs(float64(bc.CreditedAmountPaise - batch.TotalAmountPaise)))

			eval := BankCandidateEvaluation{
				BankStatementID:     bc.ID,
				BatchReferenceMatch: false,
				CreditedAmountPaise: bc.CreditedAmountPaise,
				AmountDeltaPaise:    bc.CreditedAmountPaise - batch.TotalAmountPaise,
				DaysDifference:      diffDays,
				Accepted:            false,
			}

			// Aggregate match condition: within ±2 days and diff <= 100 paise (₹1)
			if diffDays <= 2.0 && delta <= 100 {
				amountCandidates = append(amountCandidates, bc)
				eval.Accepted = true
			}
			evaluations = append(evaluations, eval)
		}
		res.CandidatesConsidered = evaluations

		if len(amountCandidates) == 1 {
			bc := amountCandidates[0]
			bankID := bc.ID
			delta := bc.CreditedAmountPaise - batch.TotalAmountPaise
			res.BankStatementID = &bankID
			res.ActualAmountPaise = &bc.CreditedAmountPaise
			res.BankDeltaPaise = &delta
			res.Matched = true
			res.Rule = RuleAggregatedAmount
			res.Confidence = 0.9
			consumedBankStatements[bc.ID] = batch.BatchID
			batchResults[batch.BatchID] = res
			continue
		} else if len(amountCandidates) > 1 {
			cat := models.CategoryAmbiguousBatch
			res.Matched = false
			res.Rule = RuleAggregatedAmount
			res.ExceptionCategory = &cat
			res.ExceptionReason = fmt.Sprintf("Multiple candidate bank credits (%d) match aggregate amount ₹%.2f within ±2 days",
				len(amountCandidates), float64(batch.TotalAmountPaise)/100.0)
			batchResults[batch.BatchID] = res
			continue
		}
	}

	// Step 3: No matching bank credit found for remaining batches -> SETTLED_NOT_BANKED
	for _, bID := range batchIDs {
		if _, evaluated := batchResults[bID]; evaluated {
			continue
		}
		batch := batches[bID]
		fieldsCompared := map[string]interface{}{
			"batch_id":             batch.BatchID,
			"merchant_id":          batch.MerchantID,
			"settlement_count":     batch.SettlementCount,
			"expected_total_paise": batch.TotalAmountPaise,
			"latest_date":          batch.LatestSettlementDate.Format(time.RFC3339),
		}
		cat := models.CategorySettledNotBanked
		exposure := batch.TotalAmountPaise
		res := &Hop2BatchResult{
			BatchID:             batch.BatchID,
			ExpectedAmountPaise: batch.TotalAmountPaise,
			FieldsCompared:      fieldsCompared,
			Matched:             false,
			Rule:                RuleBatchReference,
			Confidence:          0.0,
			ExceptionCategory:   &cat,
			ExposurePaise:       &exposure,
			ExceptionReason: fmt.Sprintf("Settlement batch %s total ₹%.2f has no corresponding bank credit statement",
				batch.BatchID, float64(batch.TotalAmountPaise)/100.0),
		}
		batchResults[batch.BatchID] = res
	}

	// Step 4: Detect Unexplained Bank Credits (BANKED_NOT_SETTLED)
	var orphanBankCredits []Hop2OrphanBankCredit
	for _, bc := range bankStatements {
		if _, consumed := consumedBankStatements[bc.ID]; !consumed {
			orphanBankCredits = append(orphanBankCredits, Hop2OrphanBankCredit{
				BankStatementID:   bc.ID,
				CreditedAmount:    bc.CreditedAmountPaise,
				MerchantID:        bc.MerchantID,
				Narration:         bc.Narration,
				CreditDate:        bc.CreditDate,
				ExceptionCategory: models.CategoryBankedNotSettled,
				ExceptionReason:   fmt.Sprintf("Bank credit of ₹%.2f for merchant %s has no matching settlement batch", float64(bc.CreditedAmountPaise)/100.0, bc.MerchantID),
				ExposurePaise:     bc.CreditedAmountPaise,
			})
		}
	}

	return &Hop2Summary{
		BatchResults:           batchResults,
		OrphanBankCredits:      orphanBankCredits,
		ConsumedBankStatements: consumedBankStatements,
	}
}

// CandidatesJSON serializes candidates considered to JSON string.
func (r *Hop2BatchResult) CandidatesJSON() string {
	b, err := json.Marshal(r.CandidatesConsidered)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// FieldsJSON serializes compared fields to JSON string.
func (r *Hop2BatchResult) FieldsJSON() string {
	b, err := json.Marshal(r.FieldsCompared)
	if err != nil {
		return "{}"
	}
	return string(b)
}

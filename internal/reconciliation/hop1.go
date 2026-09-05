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

// DefaultMaxSettlementDiscountPct is 2.5%
const DefaultMaxSettlementDiscountPct = 2.5

// Hop1Rules
const (
	RuleExactReference          = "EXACT_REFERENCE"
	RuleExactReferenceExtDate   = "EXACT_REFERENCE_EXTENDED_DATE"
	RuleAmountDateWindow        = "AMOUNT_DATE_WINDOW"
	RuleExtendedDateWindow      = "EXTENDED_DATE_WINDOW"
	RuleNoCounterpart           = "NO_COUNTERPART"
)

// CandidateEvaluation records deterministic evaluation of a settlement candidate.
type CandidateEvaluation struct {
	SettlementID     string  `json:"settlement_id"`
	ReferenceMatch   bool    `json:"reference_match"`
	AmountPaise      int64   `json:"amount_paise"`
	AmountValid      bool    `json:"amount_valid"`
	AmountDeltaPaise int64   `json:"amount_delta_paise"`
	DaysDifference   float64 `json:"days_difference"`
	DateValid        bool    `json:"date_valid"`
	Accepted         bool    `json:"accepted"`
	RejectionReason  string  `json:"rejection_reason,omitempty"`
}

// Hop1Result represents the deterministic Hop 1 reconciliation result for one internal transaction.
type Hop1Result struct {
	InternalID             string
	SettlementID           *string
	DuplicateSettlementIDs []string
	Matched                bool
	Rule                   string
	Confidence             float64
	FlagForReview          bool
	FeeDeltaPaise          *int64
	ExceptionCategory      *string
	ExceptionReason        string
	CandidatesConsidered   []CandidateEvaluation
	FieldsCompared         map[string]interface{}
}

// ReconcileHop1 performs deterministic Hop 1 matching across internal ledger and settlement records.
func ReconcileHop1(
	internals []models.InternalTransaction,
	settlements []models.SettlementRecord,
	maxDiscountPct float64,
) []Hop1Result {
	if maxDiscountPct <= 0 {
		maxDiscountPct = DefaultMaxSettlementDiscountPct
	}
	discountBps := int64(maxDiscountPct * 100) // 2.5% -> 250 bps

	// Index settlements by merchant for fast deterministic candidate filtering
	settlementsByMerchant := make(map[string][]*models.SettlementRecord)
	for i := range settlements {
		s := &settlements[i]
		settlementsByMerchant[s.MerchantID] = append(settlementsByMerchant[s.MerchantID], s)
	}

	// Sort settlements within each merchant stably by ID
	for _, list := range settlementsByMerchant {
		sort.Slice(list, func(i, j int) bool {
			return list[i].ID < list[j].ID
		})
	}

	// Index internals by reference to detect duplicate internal reference IDs
	internalsByRef := make(map[string][]string)
	for _, it := range internals {
		if it.ReferenceID != nil {
			ref := strings.ToUpper(strings.TrimSpace(*it.ReferenceID))
			if ref != "" {
				internalsByRef[ref] = append(internalsByRef[ref], it.ID)
			}
		}
	}

	// Deterministic evaluation order: internal transactions with exact references
	// are evaluated first (Section 9.8 precedence) to prevent greedy Rule 2 consumption.
	sortedInternals := make([]models.InternalTransaction, len(internals))
	copy(sortedInternals, internals)
	sort.SliceStable(sortedInternals, func(i, j int) bool {
		hasRefI := sortedInternals[i].ReferenceID != nil && strings.TrimSpace(*sortedInternals[i].ReferenceID) != ""
		hasRefJ := sortedInternals[j].ReferenceID != nil && strings.TrimSpace(*sortedInternals[j].ReferenceID) != ""
		if hasRefI != hasRefJ {
			return hasRefI
		}
		return sortedInternals[i].ID < sortedInternals[j].ID
	})

	consumedSettlements := make(map[string]string) // settlementID -> internalID
	results := make([]Hop1Result, 0, len(sortedInternals))

	for _, internal := range sortedInternals {
		minAllowedPaise := internal.AmountPaise - (internal.AmountPaise*discountBps)/10000

		fieldsCompared := map[string]interface{}{
			"internal_id":           internal.ID,
			"merchant_id":           internal.MerchantID,
			"gross_amount_paise":    internal.AmountPaise,
			"min_allowed_paise":     minAllowedPaise,
			"discount_tolerance_pct": maxDiscountPct,
			"tx_date":               internal.TransactionDate.Format(time.RFC3339),
		}
		if internal.ReferenceID != nil {
			fieldsCompared["reference_id"] = *internal.ReferenceID
		}

		merchantSettlements := settlementsByMerchant[internal.MerchantID]

		// Step A: Evaluate all candidates for this merchant
		var refCandidates []*models.SettlementRecord
		var amount3DayCandidates []*models.SettlementRecord
		var extendedDateCandidates []*models.SettlementRecord
		var amountMismatchCandidates []*models.SettlementRecord
		var dateOutOfRangeCandidates []*models.SettlementRecord

		evaluations := make([]CandidateEvaluation, 0, len(merchantSettlements))

		for _, s := range merchantSettlements {
			// Skip if already consumed by another internal transaction
			if consumedBy, consumed := consumedSettlements[s.ID]; consumed && consumedBy != internal.ID {
				continue
			}

			diff := s.SettlementDate.Sub(internal.TransactionDate)
			diffDays := diff.Hours() / 24.0
			amountDelta := internal.AmountPaise - s.SettledAmountPaise
			amountValid := s.SettledAmountPaise <= internal.AmountPaise && s.SettledAmountPaise >= minAllowedPaise

			// Validate currency
			if internal.Currency != "" && s.Currency != "" && !strings.EqualFold(internal.Currency, s.Currency) {
				eval := CandidateEvaluation{
					SettlementID:     s.ID,
					ReferenceMatch:   false,
					AmountPaise:      s.SettledAmountPaise,
					AmountValid:      false,
					AmountDeltaPaise: amountDelta,
					DaysDifference:   diffDays,
					DateValid:        diffDays >= 0 && diffDays <= 10,
					Accepted:         false,
					RejectionReason:  fmt.Sprintf("Currency mismatch: %s vs %s", s.Currency, internal.Currency),
				}
				evaluations = append(evaluations, eval)
				continue
			}

			refMatch := false
			if internal.ReferenceID != nil && s.ReferenceID != nil {
				intRef := strings.TrimSpace(*internal.ReferenceID)
				setRef := strings.TrimSpace(*s.ReferenceID)
				if intRef != "" && setRef != "" && strings.EqualFold(intRef, setRef) {
					refMatch = true
				}
			}

			eval := CandidateEvaluation{
				SettlementID:     s.ID,
				ReferenceMatch:   refMatch,
				AmountPaise:      s.SettledAmountPaise,
				AmountValid:      amountValid,
				AmountDeltaPaise: amountDelta,
				DaysDifference:   diffDays,
				DateValid:        diffDays >= 0 && diffDays <= 10,
				Accepted:         false,
			}

			// Categorize candidate availability
			if refMatch {
				refCandidates = append(refCandidates, s)
			}

			if amountValid {
				if diffDays >= 0 && diffDays <= 3.0 {
					amount3DayCandidates = append(amount3DayCandidates, s)
				} else if diffDays > 3.0 && diffDays <= 10.0 {
					extendedDateCandidates = append(extendedDateCandidates, s)
				} else if diffDays < 0 || diffDays > 10.0 {
					dateOutOfRangeCandidates = append(dateOutOfRangeCandidates, s)
				}
			} else {
				// Amount invalid: candidate must be within 0-10 days
				// AND must be a plausible counterpart in amount (e.g. within plausible fee range:
				// fee discount up to 30%, or over-settled by up to 5% + ₹50).
				// Only consider unreferenced candidates if the internal transaction did NOT specify a reference ID.
				hasInternalRef := internal.ReferenceID != nil && strings.TrimSpace(*internal.ReferenceID) != ""
				isPlausibleAmount := s.SettledAmountPaise >= int64(float64(internal.AmountPaise)*0.70) &&
					s.SettledAmountPaise <= int64(float64(internal.AmountPaise)*1.05+5000)
				if diffDays >= 0 && diffDays <= 10.0 && isPlausibleAmount && !hasInternalRef {
					amountMismatchCandidates = append(amountMismatchCandidates, s)
				}
			}

			evaluations = append(evaluations, eval)
		}

		// Step B: Decision Logic based on Section 10 Classification Precedence
		var res Hop1Result
		res.InternalID = internal.ID
		res.CandidatesConsidered = evaluations
		res.FieldsCompared = fieldsCompared

		// Decision Branch 1: Reference-based matching
		if internal.ReferenceID != nil {
			refKey := strings.ToUpper(strings.TrimSpace(*internal.ReferenceID))
			if len(internalsByRef[refKey]) > 1 {
				cat := models.CategoryDuplicateSettlement
				res.Matched = false
				res.Rule = RuleExactReference
				res.ExceptionCategory = &cat
				res.ExceptionReason = fmt.Sprintf("Multiple internal transactions (%d) share reference ID %s", len(internalsByRef[refKey]), *internal.ReferenceID)
				results = append(results, res)
				continue
			}
		}

		if len(refCandidates) > 1 {
			// Multiple settlement candidates share the same reference -> DUPLICATE_SETTLEMENT
			cat := models.CategoryDuplicateSettlement
			res.Matched = false
			res.Rule = RuleExactReference
			res.ExceptionCategory = &cat
			res.ExceptionReason = fmt.Sprintf("Multiple settlements (%d candidates) share reference ID with internal transaction", len(refCandidates))
			for _, rc := range refCandidates {
				res.DuplicateSettlementIDs = append(res.DuplicateSettlementIDs, rc.ID)
			}
			results = append(results, res)
			continue
		}

		if len(refCandidates) == 1 {
			s := refCandidates[0]
			diff := s.SettlementDate.Sub(internal.TransactionDate)
			diffDays := diff.Hours() / 24.0
			amountDelta := internal.AmountPaise - s.SettledAmountPaise
			amountValid := s.SettledAmountPaise <= internal.AmountPaise && s.SettledAmountPaise >= minAllowedPaise

			if amountValid {
				if diffDays >= 0 && diffDays <= 3.0 {
					// Clean Rule 1 Match
					feeDelta := amountDelta
					setID := s.ID
					res.Matched = true
					res.SettlementID = &setID
					res.Rule = RuleExactReference
					res.Confidence = 1.0
					res.FeeDeltaPaise = &feeDelta
					consumedSettlements[s.ID] = internal.ID
					markAcceptedCandidate(&res.CandidatesConsidered, s.ID)
					results = append(results, res)
					continue
				} else if diffDays > 3.0 && diffDays <= 10.0 {
					// Extended Date Window Match with Reference
					feeDelta := amountDelta
					setID := s.ID
					res.Matched = true
					res.SettlementID = &setID
					res.Rule = RuleExactReferenceExtDate
					res.Confidence = 0.8
					res.FlagForReview = true
					res.FeeDeltaPaise = &feeDelta
					consumedSettlements[s.ID] = internal.ID
					markAcceptedCandidate(&res.CandidatesConsidered, s.ID)
					results = append(results, res)
					continue
				} else {
					// Date out of range
					cat := models.CategoryDateOutOfRange
					res.Matched = false
					res.Rule = RuleExactReference
					res.ExceptionCategory = &cat
					if diffDays < 0 {
						res.ExceptionReason = fmt.Sprintf("Exact reference candidate %s exists but settlement date (%s) is prior to transaction date (%s)", s.ID, s.SettlementDate.Format("2006-01-02"), internal.TransactionDate.Format("2006-01-02"))
					} else {
						res.ExceptionReason = fmt.Sprintf("Exact reference candidate %s exists but settlement date drift (%.1f days) exceeds 10-day limit", s.ID, diffDays)
					}
					results = append(results, res)
					continue
				}
			} else {
				// Amount outside tolerance despite exact reference
				cat := models.CategoryAmountMismatch
				res.Matched = false
				res.Rule = RuleExactReference
				res.ExceptionCategory = &cat
				setID := s.ID
				res.SettlementID = &setID
				res.ExceptionReason = fmt.Sprintf("Exact reference candidate %s found (amount: ₹%.2f), but delta ₹%.2f exceeds %.1f%% allowed fee tolerance",
					s.ID, float64(s.SettledAmountPaise)/100.0, float64(amountDelta)/100.0, maxDiscountPct)
				results = append(results, res)
				continue
			}
		}

		// Decision Branch 2: Amount + 3-Day Window (Rule 2)
		if len(amount3DayCandidates) == 1 {
			s := amount3DayCandidates[0]
			feeDelta := internal.AmountPaise - s.SettledAmountPaise
			setID := s.ID
			res.Matched = true
			res.SettlementID = &setID
			res.Rule = RuleAmountDateWindow
			res.Confidence = 0.9
			res.FeeDeltaPaise = &feeDelta
			consumedSettlements[s.ID] = internal.ID
			markAcceptedCandidate(&res.CandidatesConsidered, s.ID)
			results = append(results, res)
			continue
		}

		if len(amount3DayCandidates) > 1 {
			cat := models.CategoryDuplicateSettlement
			res.Matched = false
			res.Rule = RuleAmountDateWindow
			res.ExceptionCategory = &cat
			res.ExceptionReason = fmt.Sprintf("Multiple plausible settlements (%d candidates) match merchant, amount tolerance, and 3-day window", len(amount3DayCandidates))
			for _, ac := range amount3DayCandidates {
				res.DuplicateSettlementIDs = append(res.DuplicateSettlementIDs, ac.ID)
			}
			results = append(results, res)
			continue
		}

		// Decision Branch 3: Extended Date Window (4–10 days) (Rule 3)
		if len(extendedDateCandidates) == 1 {
			s := extendedDateCandidates[0]
			feeDelta := internal.AmountPaise - s.SettledAmountPaise
			setID := s.ID
			res.Matched = true
			res.SettlementID = &setID
			res.Rule = RuleExtendedDateWindow
			res.Confidence = 0.7
			res.FlagForReview = true
			res.FeeDeltaPaise = &feeDelta
			consumedSettlements[s.ID] = internal.ID
			markAcceptedCandidate(&res.CandidatesConsidered, s.ID)
			results = append(results, res)
			continue
		}

		if len(extendedDateCandidates) > 1 {
			cat := models.CategoryDuplicateSettlement
			res.Matched = false
			res.Rule = RuleExtendedDateWindow
			res.ExceptionCategory = &cat
			res.ExceptionReason = fmt.Sprintf("Multiple settlements (%d candidates) found in extended 4–10 day window", len(extendedDateCandidates))
			for _, ec := range extendedDateCandidates {
				res.DuplicateSettlementIDs = append(res.DuplicateSettlementIDs, ec.ID)
			}
			results = append(results, res)
			continue
		}

		// Decision Branch 4: Exceptions / No Counterpart (Section 10.2 Precedence)
		if len(amountMismatchCandidates) > 0 {
			// Find closest amount mismatch candidate for clear reason
			closest := findClosestSettlement(internal.AmountPaise, amountMismatchCandidates)
			cat := models.CategoryAmountMismatch
			delta := internal.AmountPaise - closest.SettledAmountPaise
			res.Matched = false
			res.Rule = RuleAmountDateWindow
			res.ExceptionCategory = &cat
			setID := closest.ID
			res.SettlementID = &setID
			res.ExceptionReason = fmt.Sprintf("Settlement candidate %s found (₹%.2f) but delta ₹%.2f exceeds %.1f%% maximum allowed fee discount",
				closest.ID, float64(closest.SettledAmountPaise)/100.0, float64(delta)/100.0, maxDiscountPct)
			results = append(results, res)
			continue
		}

		if len(dateOutOfRangeCandidates) > 0 {
			s := dateOutOfRangeCandidates[0]
			diffDays := s.SettlementDate.Sub(internal.TransactionDate).Hours() / 24.0
			cat := models.CategoryDateOutOfRange
			res.Matched = false
			res.Rule = RuleExtendedDateWindow
			res.ExceptionCategory = &cat
			if diffDays < 0 {
				res.ExceptionReason = fmt.Sprintf("Settlement candidate %s exists with matching amount but settlement date is prior to transaction date (%.1f days)", s.ID, diffDays)
			} else {
				res.ExceptionReason = fmt.Sprintf("Settlement candidate %s exists with matching amount but date drift (%.1f days) exceeds 10 days", s.ID, diffDays)
			}
			results = append(results, res)
			continue
		}

		// Rule 4: No counterpart found within merchant or window
		cat := models.CategoryNoCounterpart
		res.Matched = false
		res.Rule = RuleNoCounterpart
		res.Confidence = 0.0
		res.ExceptionCategory = &cat
		res.ExceptionReason = fmt.Sprintf("No plausible settlement counterpart exists for merchant %s within 10 days", internal.MerchantID)
		results = append(results, res)
	}

	// Ensure results are returned in deterministic InternalID order
	sort.Slice(results, func(i, j int) bool {
		return results[i].InternalID < results[j].InternalID
	})

	return results
}

func markAcceptedCandidate(candidates *[]CandidateEvaluation, settlementID string) {
	for i := range *candidates {
		if (*candidates)[i].SettlementID == settlementID {
			(*candidates)[i].Accepted = true
			return
		}
	}
}

func findClosestSettlement(targetPaise int64, candidates []*models.SettlementRecord) *models.SettlementRecord {
	if len(candidates) == 0 {
		return nil
	}
	closest := candidates[0]
	minDiff := int64(math.Abs(float64(targetPaise - closest.SettledAmountPaise)))
	for _, c := range candidates[1:] {
		diff := int64(math.Abs(float64(targetPaise - c.SettledAmountPaise)))
		if diff < minDiff {
			minDiff = diff
			closest = c
		}
	}
	return closest
}

// CandidatesJSON serializes candidates evaluation to JSON string for audit log.
func (r *Hop1Result) CandidatesJSON() string {
	b, err := json.Marshal(r.CandidatesConsidered)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// FieldsJSON serializes fields compared to JSON string for audit log.
func (r *Hop1Result) FieldsJSON() string {
	b, err := json.Marshal(r.FieldsCompared)
	if err != nil {
		return "{}"
	}
	return string(b)
}

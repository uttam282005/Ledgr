package reconciliation

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/razorpay-hack/ai-finance-controller/internal/models"
)

// ReconciliationOutput encapsulates all deterministic outputs for a run.
type ReconciliationOutput struct {
	RunID               uuid.UUID
	Matches             []models.ReconciliationMatch
	Exceptions          []models.Exception
	AuditLogs           []models.AuditLog
	FullChainCount      int
	Hop1MatchedCount    int
	Hop2MatchedCount    int
	ExceptionCount      int
	UnresolvedExposure  int64
	EngineDurationMs    int64
	ThroughputPerSecond float64
}

// IsAIEligible determines if an exception category qualifies for AI investigation.
func IsAIEligible(category string) bool {
	switch category {
	case models.CategoryDuplicateSettlement,
		models.CategoryAmountMismatch,
		models.CategorySettledNotBanked,
		models.CategoryPartialCredit,
		models.CategoryBankAmountMismatch:
		return true
	default:
		return false
	}
}

// ReconcileRun executes the complete deterministic reconciliation pipeline for a dataset.
func ReconcileRun(
	runID uuid.UUID,
	internals []models.InternalTransaction,
	settlements []models.SettlementRecord,
	bankStatements []models.BankStatement,
	maxDiscountPct float64,
) *ReconciliationOutput {
	engineStart := time.Now()

	skipHop1 := len(settlements) == 0
	skipHop2 := len(bankStatements) == 0

	// 1. Hop 1: Internal Ledger ↔ Settlement Records
	var hop1Results []Hop1Result
	if !skipHop1 {
		hop1Results = ReconcileHop1(internals, settlements, maxDiscountPct)
	} else {
		// When no settlements are uploaded, mark all internals as not evaluated for Hop 1
		for _, it := range internals {
			reason := "Hop 1 not evaluated — no settlement uploaded for this run"
			cat := models.CategoryNoCounterpart
			hop1Results = append(hop1Results, Hop1Result{
				InternalID:        it.ID,
				Matched:           false,
				Rule:              "HOP1_SKIPPED",
				Confidence:        0.0,
				ExceptionCategory: &cat,
				ExceptionReason:   reason,
				FieldsCompared: map[string]interface{}{
					"gross_amount_paise": it.AmountPaise,
				},
			})
		}
	}

	// 2. Hop 2: Settlement Batches ↔ Bank Statements
	var hop2Summary *Hop2Summary
	if !skipHop2 && !skipHop1 {
		hop2Summary = ReconcileHop2(settlements, bankStatements)
	} else {
		hop2Summary = &Hop2Summary{
			BatchResults:      make(map[string]*Hop2BatchResult),
			OrphanBankCredits: nil,
		}
	}

	// Index internals and settlements for quick lookup
	internalMap := make(map[string]*models.InternalTransaction, len(internals))
	for i := range internals {
		it := &internals[i]
		internalMap[it.ID] = it
	}

	settlementMap := make(map[string]*models.SettlementRecord, len(settlements))
	consumedSettlementIDs := make(map[string]bool)
	for i := range settlements {
		s := &settlements[i]
		settlementMap[s.ID] = s
	}

	// Prepare output containers
	matches := make([]models.ReconciliationMatch, 0, len(internals))
	var exceptions []models.Exception
	var auditLogs []models.AuditLog

	hop1MatchedCount := 0
	hop2MatchedCount := 0
	fullChainCount := 0
	var totalUnresolvedExposure int64

	// 3. Evaluate each internal transaction for full-chain terminal status
	for _, h1 := range hop1Results {
		matchID := uuid.New()
		decisionID := uuid.New()

		var involvedRecords []string
		involvedRecords = append(involvedRecords, h1.InternalID)

		match := models.ReconciliationMatch{
			ID:             matchID,
			RunID:          runID,
			InternalID:     h1.InternalID,
			Hop1Confidence: h1.Confidence,
			CreatedAt:      time.Now().UTC(),
		}

		if !h1.Matched {
			// Hop 1 Failed -> UNMATCHED
			match.ReconciliationStatus = models.StatusUnmatched
			if h1.Rule != "" {
				r := h1.Rule
				match.Hop1Rule = &r
			}

			cat := *h1.ExceptionCategory
			if cat == models.CategoryDuplicateSettlement && len(h1.DuplicateSettlementIDs) > 0 {
				// Surface the duplicate candidate settlements as DUPLICATE_SETTLEMENT exceptions
				for _, dupSetID := range h1.DuplicateSettlementIDs {
					consumedSettlementIDs[dupSetID] = true
					var actAmt *int64
					var expPaise *int64
					if sRec, ok := settlementMap[dupSetID]; ok {
						amt := sRec.SettledAmountPaise
						actAmt = &amt
						expPaise = &amt
						totalUnresolvedExposure += amt
					}
					excID := uuid.New()
					exc := models.Exception{
						ID:                  excID,
						RunID:               runID,
						RecordID:            dupSetID,
						Source:              "settlement",
						Category:            cat,
						Hop:                 "HOP1",
						Reason:              fmt.Sprintf("Duplicate settlement candidate (%s) competing for internal transaction %s", dupSetID, h1.InternalID),
						ExpectedAmountPaise: expPaise,
						ActualAmountPaise:   actAmt,
						ExposurePaise:       expPaise,
						AIStatus:            models.AIPending,
						CreatedAt:           time.Now().UTC(),
					}
					exceptions = append(exceptions, exc)
				}
			} else {
				var expPaise *int64
				var actPaise *int64
				if h1.SettlementID != nil {
					consumedSettlementIDs[*h1.SettlementID] = true
					if sRec, ok := settlementMap[*h1.SettlementID]; ok {
						amt := sRec.SettledAmountPaise
						actPaise = &amt
					}
				}
				// Find gross amount from fields compared or internalMap fallback
				if gross, ok := h1.FieldsCompared["gross_amount_paise"].(int64); ok {
					exp := gross
					expPaise = &exp
					totalUnresolvedExposure += exp
				} else if it, ok := internalMap[h1.InternalID]; ok {
					exp := it.AmountPaise
					expPaise = &exp
					totalUnresolvedExposure += exp
				}

				aiStatus := models.AINotRequired
				if IsAIEligible(cat) {
					aiStatus = models.AIPending
				}

				excID := uuid.New()
				exc := models.Exception{
					ID:                  excID,
					RunID:               runID,
					RecordID:            h1.InternalID,
					Source:              "internal",
					Category:            cat,
					Hop:                 "HOP1",
					Reason:              h1.ExceptionReason,
					ExpectedAmountPaise: expPaise,
					ActualAmountPaise:   actPaise,
					ExposurePaise:       expPaise,
					AIStatus:            aiStatus,
					CreatedAt:           time.Now().UTC(),
				}
				exceptions = append(exceptions, exc)
			}

			audit := models.AuditLog{
				DecisionID:           decisionID,
				RunID:                runID,
				RecordIDs:            involvedRecords,
				RuleApplied:          h1.Rule,
				FieldsCompared:       h1.FieldsJSON(),
				CandidatesConsidered: h1.CandidatesJSON(),
				Outcome:              "EXCEPTION",
				CreatedAt:            time.Now().UTC(),
			}
			auditLogs = append(auditLogs, audit)

		} else {
			// Hop 1 Matched!
			hop1MatchedCount++
			setID := *h1.SettlementID
			match.SettlementID = &setID
			match.Hop1Rule = &h1.Rule
			match.FeeDeltaPaise = h1.FeeDeltaPaise
			consumedSettlementIDs[setID] = true
			involvedRecords = append(involvedRecords, setID)

			// Check Hop 2 outcome for the batch of this settlement
			settlementRec := settlementMap[setID]
			bKey := strings.TrimSpace(settlementRec.BatchID)
			if bKey == "" {
				bKey = fmt.Sprintf("__UNBATCHED_%s", settlementRec.ID)
			}
			batchRes, hasBatch := hop2Summary.BatchResults[bKey]

			if skipHop2 {
				// Partial run: Bank statements not uploaded, Hop 2 skipped per spec
				match.ReconciliationStatus = models.StatusPartial
				hop2Note := "Hop 2 not evaluated — no bank statement uploaded for this run"
				match.Hop2Rule = &hop2Note
				match.Hop2Confidence = 0.0

				audit := models.AuditLog{
					DecisionID:           decisionID,
					RunID:                runID,
					RecordIDs:            involvedRecords,
					RuleApplied:          h1.Rule + " (Hop 2 Skipped)",
					FieldsCompared:       h1.FieldsJSON(),
					CandidatesConsidered: h1.CandidatesJSON(),
					Outcome:              "PARTIAL",
					CreatedAt:            time.Now().UTC(),
				}
				auditLogs = append(auditLogs, audit)
			} else if hasBatch && batchRes.Matched {
				// Hop 2 Matched! -> FULL terminal status
				hop2MatchedCount++
				fullChainCount++
				match.ReconciliationStatus = models.StatusFull
				match.BankStatementID = batchRes.BankStatementID
				match.Hop2Rule = &batchRes.Rule
				match.Hop2Confidence = batchRes.Confidence
				match.ExpectedBankAmountPaise = &batchRes.ExpectedAmountPaise
				match.ActualBankAmountPaise = batchRes.ActualAmountPaise
				match.BankDeltaPaise = batchRes.BankDeltaPaise

				if batchRes.BankStatementID != nil {
					involvedRecords = append(involvedRecords, *batchRes.BankStatementID)
				}

				audit := models.AuditLog{
					DecisionID:           decisionID,
					RunID:                runID,
					RecordIDs:            involvedRecords,
					RuleApplied:          h1.Rule + " + " + batchRes.Rule,
					FieldsCompared:       h1.FieldsJSON(),
					CandidatesConsidered: h1.CandidatesJSON(),
					Outcome:              "MATCHED",
					CreatedAt:            time.Now().UTC(),
				}
				auditLogs = append(auditLogs, audit)

			} else {
				// Hop 2 Unresolved -> PARTIAL terminal status
				match.ReconciliationStatus = models.StatusPartial
				if hasBatch {
					match.BankStatementID = batchRes.BankStatementID
					match.Hop2Rule = &batchRes.Rule
					match.Hop2Confidence = batchRes.Confidence
					match.ExpectedBankAmountPaise = &batchRes.ExpectedAmountPaise
					match.ActualBankAmountPaise = batchRes.ActualAmountPaise
					match.BankDeltaPaise = batchRes.BankDeltaPaise
					if batchRes.BankStatementID != nil {
						involvedRecords = append(involvedRecords, *batchRes.BankStatementID)
					}
				}

				audit := models.AuditLog{
					DecisionID:           decisionID,
					RunID:                runID,
					RecordIDs:            involvedRecords,
					RuleApplied:          h1.Rule,
					FieldsCompared:       h1.FieldsJSON(),
					CandidatesConsidered: h1.CandidatesJSON(),
					Outcome:              "PARTIAL",
					CreatedAt:            time.Now().UTC(),
				}
				auditLogs = append(auditLogs, audit)
			}
		}

		matches = append(matches, match)
	}

	// 4. Batch Exceptions from Hop 2
	if !skipHop2 && !skipHop1 {
		batchIDs := make([]string, 0, len(hop2Summary.BatchResults))
		for bID := range hop2Summary.BatchResults {
			batchIDs = append(batchIDs, bID)
		}
		sort.Strings(batchIDs)

		for _, bID := range batchIDs {
			bRes := hop2Summary.BatchResults[bID]
			if !bRes.Matched && bRes.ExceptionCategory != nil {
				cat := *bRes.ExceptionCategory
				aiStatus := models.AINotRequired
				if IsAIEligible(cat) {
					aiStatus = models.AIPending
				}
				var expPaise *int64
				if cat == models.CategoryPartialCredit && bRes.ShortfallPaise != nil {
					exp := *bRes.ShortfallPaise
					expPaise = &exp
					totalUnresolvedExposure += exp
				} else if bRes.ExposurePaise != nil {
					exp := *bRes.ExposurePaise
					expPaise = &exp
					totalUnresolvedExposure += exp
				} else {
					exp := bRes.ExpectedAmountPaise
					expPaise = &exp
					totalUnresolvedExposure += exp
				}

				exc := models.Exception{
					ID:                  uuid.New(),
					RunID:               runID,
					RecordID:            bRes.BatchID,
					Source:              "settlement",
					Category:            cat,
					Hop:                 "HOP2",
					Reason:              bRes.ExceptionReason,
					ExpectedAmountPaise: &bRes.ExpectedAmountPaise,
					ActualAmountPaise:   bRes.ActualAmountPaise,
					DeltaPaise:          bRes.BankDeltaPaise,
					ExposurePaise:       expPaise,
					AIStatus:            aiStatus,
					CreatedAt:           time.Now().UTC(),
				}
				exceptions = append(exceptions, exc)
			}
		}
	}

	// 5. Orphan Settlements: settlements that were never consumed or associated
	sortedSettlements := make([]models.SettlementRecord, len(settlements))
	copy(sortedSettlements, settlements)
	sort.SliceStable(sortedSettlements, func(i, j int) bool {
		return sortedSettlements[i].ID < sortedSettlements[j].ID
	})

	for _, s := range sortedSettlements {
		if !consumedSettlementIDs[s.ID] {
			cat := models.CategoryOrphanSettlement
			amt := s.SettledAmountPaise
			totalUnresolvedExposure += amt

			aiStatus := models.AINotRequired
			if IsAIEligible(cat) {
				aiStatus = models.AIPending
			}

			exc := models.Exception{
				ID:                  uuid.New(),
				RunID:               runID,
				RecordID:            s.ID,
				Source:              "settlement",
				Category:            cat,
				Hop:                 "HOP1",
				Reason:              "Settlement record exists in gateway with no matching internal ledger transaction",
				ActualAmountPaise:   &amt,
				ExposurePaise:       &amt,
				AIStatus:            aiStatus,
				CreatedAt:           time.Now().UTC(),
			}
			exceptions = append(exceptions, exc)
		}
	}

	// 5. Orphan / Unexplained Bank Credits
	for _, orphan := range hop2Summary.OrphanBankCredits {
		cat := orphan.ExceptionCategory
		amt := orphan.CreditedAmount
		totalUnresolvedExposure += amt

		aiStatus := models.AINotRequired
		if IsAIEligible(cat) {
			aiStatus = models.AIPending
		}

		exc := models.Exception{
			ID:                  uuid.New(),
			RunID:               runID,
			RecordID:            orphan.BankStatementID,
			Source:              "bank",
			Category:            cat,
			Hop:                 "HOP2",
			Reason:              orphan.ExceptionReason,
			ActualAmountPaise:   &amt,
			ExposurePaise:       &amt,
			AIStatus:            aiStatus,
			CreatedAt:           time.Now().UTC(),
		}
		exceptions = append(exceptions, exc)
	}

	engineDuration := time.Since(engineStart)
	totalSourceRecords := len(internals) + len(settlements) + len(bankStatements)
	var throughput float64
	if totalSourceRecords > 0 && engineDuration.Seconds() > 0 {
		throughput = float64(totalSourceRecords) / engineDuration.Seconds()
	}

	return &ReconciliationOutput{
		RunID:               runID,
		Matches:             matches,
		Exceptions:          exceptions,
		AuditLogs:           auditLogs,
		FullChainCount:      fullChainCount,
		Hop1MatchedCount:    hop1MatchedCount,
		Hop2MatchedCount:    hop2MatchedCount,
		ExceptionCount:      len(exceptions),
		UnresolvedExposure:  totalUnresolvedExposure,
		EngineDurationMs:    engineDuration.Milliseconds(),
		ThroughputPerSecond: throughput,
	}
}

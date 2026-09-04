package reconciliation

import (
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

	// 1. Hop 1: Internal Ledger ↔ Settlement Records
	hop1Results := ReconcileHop1(internals, settlements, maxDiscountPct)

	// 2. Hop 2: Settlement Batches ↔ Bank Statements
	hop2Summary := ReconcileHop2(settlements, bankStatements)

	// Index settlements for quick lookup
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

			// Exception
			cat := *h1.ExceptionCategory
			var expPaise *int64
			// Find gross amount from fields compared
			if gross, ok := h1.FieldsCompared["gross_amount_paise"].(int64); ok {
				exp := gross
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
				ExposurePaise:       expPaise,
				AIStatus:            aiStatus,
				CreatedAt:           time.Now().UTC(),
			}
			exceptions = append(exceptions, exc)

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
			batchRes, hasBatch := hop2Summary.BatchResults[settlementRec.BatchID]

			if hasBatch && batchRes.Matched {
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

				// Create Hop 2 Exception
				var cat string
				var reason string
				var expPaise *int64

				if hasBatch && batchRes.ExceptionCategory != nil {
					cat = *batchRes.ExceptionCategory
					reason = batchRes.ExceptionReason
					if cat == models.CategoryPartialCredit && batchRes.ShortfallPaise != nil {
						exp := *batchRes.ShortfallPaise
						expPaise = &exp
						totalUnresolvedExposure += exp
					} else if batchRes.ExposurePaise != nil {
						exp := settlementRec.SettledAmountPaise
						expPaise = &exp
						totalUnresolvedExposure += exp
					}
				} else {
					cat = models.CategorySettledNotBanked
					reason = "Settlement record was never credited in any bank statement"
					exp := settlementRec.SettledAmountPaise
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
					RecordID:            setID,
					Source:              "settlement",
					Category:            cat,
					Hop:                 "HOP2",
					Reason:              reason,
					ExpectedAmountPaise: &settlementRec.SettledAmountPaise,
					ActualAmountPaise:   batchRes.ActualAmountPaise,
					ExposurePaise:       expPaise,
					AIStatus:            aiStatus,
					CreatedAt:           time.Now().UTC(),
				}
				exceptions = append(exceptions, exc)

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

	// 4. Orphan Settlements: settlements that were never consumed by any internal transaction
	for _, s := range settlements {
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
	throughput := float64(totalSourceRecords) / engineDuration.Seconds()

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

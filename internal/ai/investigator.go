package ai

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/razorpay-hack/ai-finance-controller/internal/models"
)

var (
	reRecordID   = regexp.MustCompile(`(?i)\b(INT|SET|BNK)[0-9]+\b`)
	reRefID      = regexp.MustCompile(`(?i)\bREF_[A-Za-z0-9_-]+\b`)
	reBatchID    = regexp.MustCompile(`(?i)\bBATCH_[0-9]+\b`)
	reMerchantID = regexp.MustCompile(`(?i)\bMERCH_[A-Za-z0-9_]+\b`)
	reCandidates = regexp.MustCompile(`\([0-9]+\s+candidates\)`)
	reAmount     = regexp.MustCompile(`₹\s*[-+]?[0-9,]+(\.[0-9]+)?`)
)

// NormalizeReason strips record IDs, references, batches, merchants, and monetary figures
// to isolate the underlying structural failure archetype for high-efficiency caching.
func NormalizeReason(reason string) string {
	s := reRecordID.ReplaceAllString(reason, "<RECORD_ID>")
	s = reRefID.ReplaceAllString(s, "<REF_ID>")
	s = reBatchID.ReplaceAllString(s, "<BATCH_ID>")
	s = reMerchantID.ReplaceAllString(s, "<MERCHANT_ID>")
	s = reCandidates.ReplaceAllString(s, "(<N> candidates)")
	s = reAmount.ReplaceAllString(s, "<AMOUNT>")
	return strings.TrimSpace(s)
}

// InvestigationMetrics captures statistics for the AI investigation phase.
type InvestigationMetrics struct {
	EligibleCount   int     `json:"eligible_count"`
	AttemptedCount  int     `json:"attempted_count"`
	SucceededCount  int     `json:"succeeded_count"`
	FailedCount     int     `json:"failed_count"`
	CacheHits       int     `json:"cache_hits"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`
	TotalDurationMs int64   `json:"total_duration_ms"`
}

// Investigator manages the AI investigation lifecycle, concurrency, caching, and persistence.
type Investigator struct {
	client         Client
	fallbackClient Client
	db             *sql.DB
	cache          sync.Map // fingerprint -> *InvestigationResponse
	concurrency    int
	timeout        time.Duration
}

// NewInvestigator creates a new Investigator with automatic offline fallback and archetype caching.
func NewInvestigator(client Client, database *sql.DB) *Investigator {
	concurrency := 2
	if envC := os.Getenv("NIM_CONCURRENCY"); envC != "" {
		if c, err := strconv.Atoi(envC); err == nil && c > 0 {
			concurrency = c
		}
	}
	timeoutSec := 25
	if envT := os.Getenv("NIM_TIMEOUT_SECONDS"); envT != "" {
		if t, err := strconv.Atoi(envT); err == nil && t > 0 {
			timeoutSec = t
		}
	}
	return &Investigator{
		client:         client,
		fallbackClient: NewOfflineClient(),
		db:             database,
		concurrency:    concurrency,
		timeout:        time.Duration(timeoutSec) * time.Second,
	}
}

// ComputeFingerprint creates a stable SHA-256 hash for an exception's structural failure archetype.
func ComputeFingerprint(runID, category, recordID, reason string) string {
	normalized := NormalizeReason(reason)
	h := sha256.New()
	h.Write([]byte(runID))
	h.Write([]byte(":"))
	h.Write([]byte(category))
	h.Write([]byte(":"))
	h.Write([]byte(normalized))
	return hex.EncodeToString(h.Sum(nil))
}

// InvestigatePendingExceptions processes all eligible exceptions with status PENDING for a run.
func (inv *Investigator) InvestigatePendingExceptions(
	ctx context.Context,
	runID uuid.UUID,
) (*InvestigationMetrics, error) {
	startTime := time.Now()

	// Query pending exceptions
	query := `
		SELECT id, run_id, record_id, category, hop, reason, expected_amount_paise, exposure_paise
		FROM exceptions
		WHERE run_id = $1 AND ai_status = 'PENDING'
		ORDER BY created_at ASC;
	`
	rows, err := inv.db.QueryContext(ctx, query, runID)
	if err != nil {
		return nil, fmt.Errorf("failed querying pending exceptions: %w", err)
	}
	defer rows.Close()

	var pending []models.Exception
	for rows.Next() {
		var e models.Exception
		if err := rows.Scan(&e.ID, &e.RunID, &e.RecordID, &e.Category, &e.Hop, &e.Reason, &e.ExpectedAmountPaise, &e.ExposurePaise); err != nil {
			return nil, fmt.Errorf("failed scanning exception: %w", err)
		}
		pending = append(pending, e)
	}

	metrics := &InvestigationMetrics{
		EligibleCount: len(pending),
	}

	if len(pending) == 0 {
		return metrics, nil
	}

	log.Printf("[AI] Starting investigation of %d eligible exceptions with concurrency=%d (timeout=%s)...",
		len(pending), inv.concurrency, inv.timeout)

	jobs := make(chan models.Exception, len(pending))
	for _, e := range pending {
		jobs <- e
	}
	close(jobs)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var totalLatency int64

	for w := 0; w < inv.concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for exc := range jobs {
				req := InvestigationRequest{
					RunID:         exc.RunID.String(),
					RecordID:      exc.RecordID,
					Category:      exc.Category,
					Hop:           exc.Hop,
					Reason:        exc.Reason,
					AmountPaise:   exc.ExpectedAmountPaise,
					ExposurePaise: exc.ExposurePaise,
				}

				fingerprint := ComputeFingerprint(req.RunID, req.Category, req.RecordID, req.Reason)

				// Check cache for archetype diagnosis
				var resp *InvestigationResponse
				isCacheHit := false
				if val, found := inv.cache.Load(fingerprint); found {
					cached := val.(*InvestigationResponse)
					cp := *cached
					cp.LatencyMs = 0
					resp = &cp
					isCacheHit = true
				} else {
					// Call client with adaptive timeout
					callCtx, cancel := context.WithTimeout(ctx, inv.timeout)
					r, err := inv.client.InvestigateException(callCtx, req)
					cancel()
					if err != nil {
						// Adaptive backoff before retry
						time.Sleep(1000 * time.Millisecond)
						log.Printf("[AI] Retrying investigation for %s due to error: %v", exc.RecordID, err)
						retryCtx, retryCancel := context.WithTimeout(ctx, inv.timeout)
						r, err = inv.client.InvestigateException(retryCtx, req)
						retryCancel()
					}

					if err == nil {
						resp = r
						inv.cache.Store(fingerprint, resp)
					} else {
						// Graceful fallback to deterministic offline synthesizer
						log.Printf("[AI] Primary client failed for %s (%v); falling back to offline synthesizer", exc.RecordID, err)
						if inv.fallbackClient != nil {
							if fbResp, fbErr := inv.fallbackClient.InvestigateException(ctx, req); fbErr == nil {
								fbResp.Model = fbResp.Model + " (fallback)"
								resp = fbResp
								inv.cache.Store(fingerprint, resp)
							}
						}
					}
				}

				mu.Lock()
				metrics.AttemptedCount++
				if isCacheHit {
					metrics.CacheHits++
				}
				if resp != nil {
					metrics.SucceededCount++
					totalLatency += int64(resp.LatencyMs)
				} else {
					metrics.FailedCount++
				}
				mu.Unlock()

				// Update database
				writeCtx, writeCancel := context.WithTimeout(context.Background(), 5*time.Second)
				if resp != nil {
					updateQuery := `
						UPDATE exceptions SET
							ai_status = 'SUCCEEDED',
							ai_summary = $1,
							ai_action = $2,
							ai_confidence = $3
						WHERE id = $4;
					`
					_, _ = inv.db.ExecContext(writeCtx, updateQuery, resp.Diagnosis, resp.SuggestedAction, resp.Confidence, exc.ID)

					auditUpdateQuery := `
						UPDATE audit_log SET
							ai_reasoning = $1,
							ai_model = $2,
							ai_prompt_version = $3,
							ai_latency_ms = $4
						WHERE run_id = $5 AND $6 = ANY(record_ids);
					`
					_, _ = inv.db.ExecContext(writeCtx, auditUpdateQuery, resp.Diagnosis, resp.Model, resp.PromptVersion, resp.LatencyMs, exc.RunID, exc.RecordID)
				} else {
					failQuery := `UPDATE exceptions SET ai_status = 'FAILED' WHERE id = $1;`
					_, _ = inv.db.ExecContext(writeCtx, failQuery, exc.ID)
				}
				writeCancel()
			}
		}()
	}

	wg.Wait()
	metrics.TotalDurationMs = time.Since(startTime).Milliseconds()
	if metrics.SucceededCount > 0 {
		metrics.AvgLatencyMs = float64(totalLatency) / float64(metrics.SucceededCount)
	}

	log.Printf("[AI] Investigation complete: %d succeeded, %d failed in %d ms (avg latency: %.1f ms)",
		metrics.SucceededCount, metrics.FailedCount, metrics.TotalDurationMs, metrics.AvgLatencyMs)

	return metrics, nil
}

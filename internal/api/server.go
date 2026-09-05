package api

import (
	"database/sql"
	"io/fs"
	"net/http"

	"github.com/razorpay-hack/ai-finance-controller/dashboard"
	"github.com/razorpay-hack/ai-finance-controller/internal/config"
	"github.com/razorpay-hack/ai-finance-controller/internal/ingestion"
	"github.com/razorpay-hack/ai-finance-controller/internal/qa"
)

// NewServer builds the HTTP router with all API handlers and embedded dashboard UI.
func NewServer(database *sql.DB, qaService *qa.QAService, cfg *config.Config) http.Handler {
	mux := http.NewServeMux()
	csvService := ingestion.NewCSVService(database, cfg.NvidiaAPIKey, cfg.NvidiaNIMBaseURL, cfg.NvidiaNIMModel, cfg.EngineVersion)
	h := NewHandlers(database, qaService, csvService, cfg)

	// Health check
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		status := "connected"
		code := http.StatusOK
		if err := database.PingContext(r.Context()); err != nil {
			status = "unreachable"
			code = http.StatusServiceUnavailable
		}
		respondJSON(w, code, map[string]interface{}{
			"status":         "ok",
			"database":       status,
			"engine_version": cfg.EngineVersion,
		})
	})

	// Run Management & Scoping Routes (Supports both /runs and /api/runs)
	mux.HandleFunc("POST /runs", h.HandleCreateRun)
	mux.HandleFunc("POST /api/runs", h.HandleCreateRun)

	mux.HandleFunc("GET /runs", h.HandleListRuns)
	mux.HandleFunc("GET /api/runs", h.HandleListRuns)

	mux.HandleFunc("GET /runs/{runID}", h.HandleGetRunDetail)
	mux.HandleFunc("GET /api/runs/{runID}", h.HandleGetRunDetail)

	mux.HandleFunc("DELETE /runs/{runID}", h.HandleDeleteRun)
	mux.HandleFunc("DELETE /api/runs/{runID}", h.HandleDeleteRun)

	mux.HandleFunc("POST /runs/{runID}/uploads", h.HandleUploadToRun)
	mux.HandleFunc("POST /api/runs/{runID}/uploads", h.HandleUploadToRun)

	mux.HandleFunc("POST /runs/{runID}/reconcile", h.HandleReconcileRun)
	mux.HandleFunc("POST /api/runs/{runID}/reconcile", h.HandleReconcileRun)

	mux.HandleFunc("GET /runs/{runID}/matches", h.HandleGetMatches)
	mux.HandleFunc("GET /api/runs/{runID}/matches", h.HandleGetMatches)

	mux.HandleFunc("GET /runs/{runID}/exceptions", h.GetExceptions)
	mux.HandleFunc("GET /api/runs/{runID}/exceptions", h.GetExceptions)

	mux.HandleFunc("POST /runs/{runID}/query", h.HandleQA)
	mux.HandleFunc("POST /api/runs/{runID}/query", h.HandleQA)
	mux.HandleFunc("POST /api/runs/{runID}/qa", h.HandleQA)

	// Additional Run Detail & Analytics Routes
	mux.HandleFunc("GET /api/runs/latest", h.GetLatestRun)
	mux.HandleFunc("GET /api/runs/{runID}/summary", h.GetRunSummary)
	mux.HandleFunc("GET /api/runs/{runID}/reconciliation", h.GetReconciliationMatches)
	mux.HandleFunc("GET /api/runs/{runID}/audit/item", h.GetAuditLogForRecord)
	mux.HandleFunc("GET /api/runs/{runID}/cash-position", h.GetCashPosition)
	mux.HandleFunc("GET /api/runs/{runID}/sources-status", h.HandleGetRunSourcesStatus)

	// CSV Ingestion & AI Mapping Routes
	mux.HandleFunc("POST /api/ingest/analyze", h.HandleAnalyzeCSV)
	mux.HandleFunc("POST /api/ingest/commit", h.HandleCommitCSV)
	mux.HandleFunc("GET /api/samples/messy/{source}", h.HandleMessySampleCSV)
	mux.HandleFunc("GET /api/samples/{source}", h.HandleSampleCSV)

	// Serve Embedded Dashboard
	subFS, err := fs.Sub(dashboard.Assets, ".")
	if err == nil {
		fileServer := http.FileServer(http.FS(subFS))
		mux.Handle("/", fileServer)
	}

	return mux
}

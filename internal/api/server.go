package api

import (
	"database/sql"
	"io/fs"
	"net/http"

	"github.com/razorpay-hack/ai-finance-controller/dashboard"
	"github.com/razorpay-hack/ai-finance-controller/internal/config"
	"github.com/razorpay-hack/ai-finance-controller/internal/qa"
)

// NewServer builds the HTTP router with all API handlers and embedded dashboard UI.
func NewServer(database *sql.DB, qaService *qa.QAService, cfg *config.Config) http.Handler {
	mux := http.NewServeMux()
	h := NewHandlers(database, qaService)

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

	// API Routes
	mux.HandleFunc("GET /api/runs/latest", h.GetLatestRun)
	mux.HandleFunc("GET /api/runs/{runID}/summary", h.GetRunSummary)
	mux.HandleFunc("GET /api/runs/{runID}/reconciliation", h.GetReconciliationMatches)
	mux.HandleFunc("GET /api/runs/{runID}/exceptions", h.GetExceptions)
	mux.HandleFunc("GET /api/runs/{runID}/audit/item", h.GetAuditLogForRecord)
	mux.HandleFunc("GET /api/runs/{runID}/cash-position", h.GetCashPosition)
	mux.HandleFunc("POST /api/runs/{runID}/qa", h.HandleQA)

	// Serve Embedded Dashboard
	subFS, err := fs.Sub(dashboard.Assets, ".")
	if err == nil {
		fileServer := http.FileServer(http.FS(subFS))
		mux.Handle("/", fileServer)
	}

	return mux
}

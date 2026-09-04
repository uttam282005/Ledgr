package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/razorpay-hack/ai-finance-controller/internal/api"
	"github.com/razorpay-hack/ai-finance-controller/internal/config"
	"github.com/razorpay-hack/ai-finance-controller/internal/db"
	"github.com/razorpay-hack/ai-finance-controller/internal/qa"
)

var startTime = time.Now()

type HealthResponse struct {
	Status        string `json:"status"`
	Database      string `json:"database"`
	EngineVersion string `json:"engine_version"`
	Timestamp     string `json:"timestamp"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

func healthHandler(database *sql.DB, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dbStatus := "connected"
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := database.PingContext(ctx); err != nil {
			dbStatus = fmt.Sprintf("unreachable: %v", err)
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}

		resp := HealthResponse{
			Status:        "ok",
			Database:      dbStatus,
			EngineVersion: cfg.EngineVersion,
			Timestamp:     time.Now().UTC().Format(time.RFC3339),
			UptimeSeconds: int64(time.Since(startTime).Seconds()),
		}
		if dbStatus != "connected" {
			resp.Status = "unhealthy"
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func main() {
	cfg := config.Load()
	log.Printf("[API] Starting AI Finance Controller API (Engine: %s)...", cfg.EngineVersion)

	// Attempt connection to database
	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Printf("[API] Warning: Initial connection with DATABASE_URL failed: %v", err)
		log.Printf("[API] Retrying with local user fallback if available...")
		// Fallback for local dev when roles may not yet be created
		fallbackURL := "postgres://localhost:5432/ai_finance_db?sslmode=disable"
		database, err = db.Connect(fallbackURL)
		if err != nil {
			log.Fatalf("[API] Fatal: Unable to connect to database: %v", err)
		}
	}
	defer database.Close()
	log.Printf("[API] Connected to database.")

	// Connect to QA read-only database role
	qaDB, err := db.Connect(cfg.QADatabaseURL)
	if err != nil {
		log.Printf("[API] Notice: QA read-only role connection failed (%v); using primary DB for Q&A", err)
		qaDB = database
	} else {
		defer qaDB.Close()
	}
	qaService := qa.NewQAService(qaDB, cfg.NvidiaAPIKey, cfg.NvidiaNIMBaseURL, cfg.NvidiaNIMModel)

	serverHandler := api.NewServer(database, qaService, cfg)

	server := &http.Server{
		Addr:         ":" + cfg.HTTPPort,
		Handler:      serverHandler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("[API] Listening on http://localhost:%s", cfg.HTTPPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[API] Server error: %v", err)
		}
	}()

	<-stopChan
	log.Println("[API] Shutting down gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("[API] Error during shutdown: %v", err)
	}
	log.Println("[API] Server stopped.")
}

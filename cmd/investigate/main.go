package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/razorpay-hack/ai-finance-controller/internal/ai"
	"github.com/razorpay-hack/ai-finance-controller/internal/config"
	"github.com/razorpay-hack/ai-finance-controller/internal/db"
	"github.com/razorpay-hack/ai-finance-controller/internal/repository"
)

func main() {
	cfg := config.Load()

	runIDFlag := flag.String("run-id", "", "UUID of the reconciliation run (defaults to latest)")
	forceOfflineFlag := flag.Bool("offline", false, "Force offline deterministic synthesizer")
	flag.Parse()

	log.Printf("[INVESTIGATE] Connecting to database (%s)...", cfg.DatabaseURL)
	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Printf("[INVESTIGATE] Connecting with fallback...")
		fallbackURL := "postgres://localhost:5432/ai_finance_db?sslmode=disable"
		database, err = db.Connect(fallbackURL)
		if err != nil {
			log.Fatalf("[INVESTIGATE] Failed to connect to database: %v", err)
		}
	}
	defer database.Close()

	repo := repository.NewRepository(database)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var runID uuid.UUID
	if *runIDFlag != "" {
		parsed, err := uuid.Parse(*runIDFlag)
		if err != nil {
			log.Fatalf("[INVESTIGATE] Invalid UUID: %v", err)
		}
		runID = parsed
	} else {
		latest, err := repo.GetLatestRunID(ctx)
		if err != nil {
			log.Fatalf("[INVESTIGATE] Failed to get latest run: %v", err)
		}
		runID = latest
	}

	// Determine AI client (NVIDIA NIM or Offline fallback)
	var aiClient ai.Client
	if cfg.NvidiaAPIKey != "" && !*forceOfflineFlag {
		log.Printf("[INVESTIGATE] Using NVIDIA NIM (%s) with model %s", cfg.NvidiaNIMBaseURL, cfg.NvidiaNIMModel)
		aiClient = ai.NewNIMClient(cfg.NvidiaAPIKey, cfg.NvidiaNIMBaseURL, cfg.NvidiaNIMModel)
	} else {
		log.Printf("[INVESTIGATE] Using Offline Deterministic Synthesizer (NVIDIA_API_KEY empty or --offline flag set)")
		aiClient = ai.NewOfflineClient()
	}

	investigator := ai.NewInvestigator(aiClient, database)

	log.Printf("[INVESTIGATE] Investigating pending exceptions for run %s...", runID)
	metrics, err := investigator.InvestigatePendingExceptions(ctx, runID)
	if err != nil {
		log.Fatalf("[INVESTIGATE] Investigation error: %v", err)
	}

	fmt.Println("\n========================================================")
	fmt.Println("             AI EXCEPTION INVESTIGATION SUMMARY         ")
	fmt.Println("========================================================")
	fmt.Printf("Run ID:               %s\n", runID)
	fmt.Printf("Eligible Exceptions:  %d\n", metrics.EligibleCount)
	fmt.Printf("Attempted Calls:      %d\n", metrics.AttemptedCount)
	fmt.Printf("Succeeded Calls:      %d\n", metrics.SucceededCount)
	fmt.Printf("Failed Calls:         %d\n", metrics.FailedCount)
	cachePct := 0.0
	if metrics.AttemptedCount > 0 {
		cachePct = float64(metrics.CacheHits) / float64(metrics.AttemptedCount) * 100.0
	}
	fmt.Printf("Archetype Cache Hits: %d (%.1f%%)\n", metrics.CacheHits, cachePct)
	fmt.Printf("Average Latency:      %.1f ms\n", metrics.AvgLatencyMs)
	fmt.Printf("Total Duration:       %d ms\n", metrics.TotalDurationMs)
	fmt.Println("========================================================")
}

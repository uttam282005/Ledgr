package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/razorpay-hack/ai-finance-controller/internal/config"
	"github.com/razorpay-hack/ai-finance-controller/internal/db"
	"github.com/razorpay-hack/ai-finance-controller/internal/reconciliation"
	"github.com/razorpay-hack/ai-finance-controller/internal/repository"
)

func main() {
	cfg := config.Load()

	runIDFlag := flag.String("run-id", "", "UUID of the reconciliation run to process (defaults to latest)")
	discountPctFlag := flag.Float64("discount-pct", 2.5, "Maximum fee discount percentage tolerance")
	flag.Parse()

	log.Printf("[RECONCILE] Connecting to database (%s)...", cfg.DatabaseURL)
	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Printf("[RECONCILE] Connecting with fallback...")
		fallbackURL := "postgres://localhost:5432/ai_finance_db?sslmode=disable"
		database, err = db.Connect(fallbackURL)
		if err != nil {
			log.Fatalf("[RECONCILE] Failed connecting to database: %v", err)
		}
	}
	defer database.Close()

	repo := repository.NewRepository(database)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var runID uuid.UUID
	if *runIDFlag != "" {
		parsed, err := uuid.Parse(*runIDFlag)
		if err != nil {
			log.Fatalf("[RECONCILE] Invalid run-id UUID: %v", err)
		}
		runID = parsed
	} else {
		latest, err := repo.GetLatestRunID(ctx)
		if err != nil {
			log.Fatalf("[RECONCILE] Failed getting latest run: %v. (Did you run make seed?)", err)
		}
		runID = latest
	}

	log.Printf("[RECONCILE] Loading source data for run %s...", runID)
	loadStart := time.Now()
	internals, settlements, bankStatements, err := repo.LoadSourceData(ctx, runID)
	if err != nil {
		log.Fatalf("[RECONCILE] Failed loading source data: %v", err)
	}
	log.Printf("[RECONCILE] Loaded %d internals, %d settlements, %d bank statements in %v",
		len(internals), len(settlements), len(bankStatements), time.Since(loadStart))

	log.Printf("[RECONCILE] Running deterministic reconciliation engine (max discount: %.1f%%)...", *discountPctFlag)
	output := reconciliation.ReconcileRun(runID, internals, settlements, bankStatements, *discountPctFlag)

	log.Printf("[RECONCILE] Persisting matches, exceptions, and audit trails to PostgreSQL...")
	persistStart := time.Now()
	if err := repo.SaveReconciliationResults(ctx, output); err != nil {
		log.Fatalf("[RECONCILE] Failed saving reconciliation results: %v", err)
	}
	log.Printf("[RECONCILE] Persisted %d matches, %d exceptions, %d audit entries in %v",
		len(output.Matches), len(output.Exceptions), len(output.AuditLogs), time.Since(persistStart))

	// Group exceptions by category for honest breakdown
	excByCategory := make(map[string]int)
	for _, e := range output.Exceptions {
		excByCategory[e.Category]++
	}

	totalInternals := len(internals)
	hop1Rate := float64(output.Hop1MatchedCount) / float64(totalInternals) * 100.0
	fullChainRate := float64(output.FullChainCount) / float64(totalInternals) * 100.0

	fmt.Println("\n========================================================")
	fmt.Println("             DETERMINISTIC RECONCILIATION RESULT        ")
	fmt.Println("========================================================")
	fmt.Printf("Run ID:                     %s\n", runID)
	fmt.Printf("Internal Transactions:      %d\n", totalInternals)
	fmt.Printf("Settlement Records:         %d\n", len(settlements))
	fmt.Printf("Bank Statements:            %d\n", len(bankStatements))
	fmt.Println("--------------------------------------------------------")
	fmt.Printf("HOP 1 MATCHED:              %d / %d (%.1f%%)\n", output.Hop1MatchedCount, totalInternals, hop1Rate)
	fmt.Printf("HOP 2 MATCHED:              %d\n", output.Hop2MatchedCount)
	fmt.Printf("FULL CHAIN RECONCILED:      %d / %d (%.1f%%)\n", output.FullChainCount, totalInternals, fullChainRate)
	fmt.Printf("PARTIAL CHAIN:              %d\n", output.Hop1MatchedCount-output.FullChainCount)
	fmt.Printf("UNMATCHED (HOP 1 FAILED):   %d\n", totalInternals-output.Hop1MatchedCount)
	fmt.Println("--------------------------------------------------------")
	fmt.Printf("ENGINE WALL-CLOCK DURATION: %d ms\n", output.EngineDurationMs)
	fmt.Printf("DETERMINISTIC THROUGHPUT:   %.0f records/sec\n", output.ThroughputPerSecond)
	fmt.Printf("UNRESOLVED CASH EXPOSURE:   ₹%.2f (%d paise)\n", float64(output.UnresolvedExposure)/100.0, output.UnresolvedExposure)
	fmt.Printf("TOTAL EXCEPTIONS:           %d\n", output.ExceptionCount)
	fmt.Println("--------------------------------------------------------")
	fmt.Println("EXCEPTION BREAKDOWN:")
	for cat, count := range excByCategory {
		fmt.Printf("  - %-25s : %d\n", cat, count)
	}
	fmt.Println("========================================================")
}

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/razorpay-hack/ai-finance-controller/internal/config"
	"github.com/razorpay-hack/ai-finance-controller/internal/db"
	"github.com/razorpay-hack/ai-finance-controller/internal/generator"
	"github.com/razorpay-hack/ai-finance-controller/internal/ingestion"
)

func main() {
	cfg := config.Load()

	seedFlag := flag.Int64("seed", cfg.DatasetSeed, "Deterministic RNG seed")
	gtDirFlag := flag.String("gt-dir", "evaluation/ground_truth", "Path to write ground truth evaluation artifact")
	dryRunFlag := flag.Bool("dry-run", false, "Generate without loading into database")
	flag.Parse()

	log.Printf("[SEED] Generating synthetic dataset (seed=%d, version=%s)...", *seedFlag, cfg.DatasetVersion)

	startTime := time.Now()
	dataset, groundTruth := generator.Generate(*seedFlag, cfg.DatasetVersion, cfg.EngineVersion)
	genDuration := time.Since(startTime)

	// Save Ground Truth outside matcher
	gtPath, err := generator.SaveGroundTruth(groundTruth, *gtDirFlag)
	if err != nil {
		log.Fatalf("[SEED] Failed to save ground truth: %v", err)
	}

	log.Printf("[SEED] Dataset generated in %v", genDuration)
	log.Printf("[SEED] Run ID:          %s", dataset.Run.RunID)
	log.Printf("[SEED] Seed:            %d", dataset.Run.Seed)
	log.Printf("[SEED] Internal cases:  %d (Expected: 500)", len(dataset.Internals))
	log.Printf("[SEED] Settlements:     %d", len(dataset.Settlements))
	log.Printf("[SEED] Bank statements: %d", len(dataset.BankStatements))
	log.Printf("[SEED] Ground truth:    %s", gtPath)

	if *dryRunFlag {
		log.Println("[SEED] Dry run requested. Skipping database insertion.")
		return
	}

	// Connect to database to load dataset
	log.Printf("[SEED] Loading dataset into database (%s)...", cfg.DatabaseURL)
	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Printf("[SEED] Connecting with local fallback...")
		fallbackURL := "postgres://localhost:5432/ai_finance_db?sslmode=disable"
		database, err = db.Connect(fallbackURL)
		if err != nil {
			log.Fatalf("[SEED] Failed to connect to database: %v", err)
		}
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	loadStart := time.Now()
	if err := ingestion.LoadDataset(ctx, database, dataset); err != nil {
		log.Fatalf("[SEED] Failed to load dataset: %v", err)
	}
	log.Printf("[SEED] Successfully loaded %d total source records in %v",
		dataset.Run.SourceRecordsProcessed, time.Since(loadStart))

	fmt.Printf("\n--- DATASET GENERATION SUMMARY ---\n")
	fmt.Printf("Run ID:               %s\n", dataset.Run.RunID)
	fmt.Printf("Seed:                 %d\n", dataset.Run.Seed)
	fmt.Printf("Internal Records:     %d\n", len(dataset.Internals))
	fmt.Printf("Settlement Records:   %d\n", len(dataset.Settlements))
	fmt.Printf("Bank Statements:      %d\n", len(dataset.BankStatements))
	fmt.Printf("Total Source Records: %d\n", dataset.Run.SourceRecordsProcessed)
	fmt.Printf("Ground Truth:         %s\n", gtPath)
	fmt.Printf("----------------------------------\n\n")
}

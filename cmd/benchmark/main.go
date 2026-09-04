package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/razorpay-hack/ai-finance-controller/internal/generator"
	"github.com/razorpay-hack/ai-finance-controller/internal/metrics"
	"github.com/razorpay-hack/ai-finance-controller/internal/reconciliation"
)

func evaluateSeed(seed int64, discountPct float64) *metrics.BenchmarkMetrics {
	// 1. Generate dataset and isolated ground truth
	dataset, groundTruth := generator.Generate(seed, "v1.0.0", "v1.0.0")

	// 2. Reconcile strictly using source data (zero ground truth access)
	output := reconciliation.ReconcileRun(
		dataset.Run.RunID,
		dataset.Internals,
		dataset.Settlements,
		dataset.BankStatements,
		discountPct,
	)

	// 3. Evaluate predictions against ground truth
	return metrics.CalculateMetrics(output, dataset, groundTruth)
}

func main() {
	seedFlag := flag.Int64("seed", 42, "Seed to evaluate")
	allFlag := flag.Bool("all", false, "Run multi-seed benchmark across dev, validation, and holdout seeds")
	discountFlag := flag.Float64("discount-pct", 2.5, "Maximum fee discount percentage tolerance")
	outputJSONFlag := flag.String("output-json", "", "Optional path to export benchmark results as JSON")
	flag.Parse()

	if !*allFlag {
		log.Printf("[BENCHMARK] Running single-seed benchmark (seed=%d, tolerance=%.1f%%)...", *seedFlag, *discountFlag)
		m := evaluateSeed(*seedFlag, *discountFlag)
		metrics.PrintBenchmarkCard(m)

		if *outputJSONFlag != "" {
			b, _ := json.MarshalIndent(m, "", "  ")
			_ = os.WriteFile(*outputJSONFlag, b, 0644)
			log.Printf("[BENCHMARK] Metrics written to %s", *outputJSONFlag)
		}
		return
	}

	// Multi-Seed Holdout Evaluation Mode
	seeds := []struct {
		name string
		seed int64
	}{
		{"Development Seed", 42},
		{"Validation Seed", 101},
		{"Holdout Seed", 999},
	}

	fmt.Println("\n=========================================================================================")
	fmt.Println("               AI FINANCE CONTROLLER — MULTI-SEED HOLDOUT BENCHMARK                      ")
	fmt.Println("=========================================================================================")
	fmt.Printf("%-20s | %-6s | %-10s | %-12s | %-10s | %-10s | %-12s\n",
		"Dataset Seed", "Cases", "Hop 1 Match", "Full Chain", "Precision", "Recall", "Throughput")
	fmt.Println("-----------------------------------------------------------------------------------------")

	var allMetrics []*metrics.BenchmarkMetrics

	for _, s := range seeds {
		m := evaluateSeed(s.seed, *discountFlag)
		allMetrics = append(allMetrics, m)
		fmt.Printf("%-20s | %-6d | %9.1f%% | %11.1f%% | %9.1f%% | %9.1f%% | %8.0f rec/s\n",
			fmt.Sprintf("%s (%d)", s.name, s.seed),
			m.TotalInternalCases,
			m.Hop1MatchRate,
			m.FullChainRate,
			m.Hop1Precision,
			m.Hop1Recall,
			m.DeterministicThroughput,
		)
	}
	fmt.Println("=========================================================================================")

	if *outputJSONFlag != "" {
		b, _ := json.MarshalIndent(allMetrics, "", "  ")
		_ = os.WriteFile(*outputJSONFlag, b, 0644)
		log.Printf("[BENCHMARK] Multi-seed metrics written to %s", *outputJSONFlag)
	}
}

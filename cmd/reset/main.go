package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/razorpay-hack/ai-finance-controller/internal/config"
	"github.com/razorpay-hack/ai-finance-controller/internal/db"
)

func main() {
	cfg := config.Load()

	log.Println("[RESET] Connecting to PostgreSQL to truncate operational tables...")
	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Printf("[RESET] Connecting with local fallback...")
		fallbackURL := "postgres://localhost:5432/ai_finance_db?sslmode=disable"
		database, err = db.Connect(fallbackURL)
		if err != nil {
			log.Fatalf("[RESET] Failed to connect to database: %v", err)
		}
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	truncateQuery := `
		TRUNCATE TABLE 
			audit_log,
			exceptions,
			reconciliation_matches,
			bank_statements,
			settlement_records,
			internal_transactions,
			reconciliation_runs 
		CASCADE;
	`
	if _, err := database.ExecContext(ctx, truncateQuery); err != nil {
		log.Fatalf("[RESET] Failed to truncate tables: %v", err)
	}

	log.Println("[RESET] All operational reconciliation tables successfully truncated.")
	fmt.Println("Database reset completed.")
}

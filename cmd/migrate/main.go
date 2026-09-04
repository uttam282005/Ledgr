package main

import (
	"log"
	"os"

	"github.com/razorpay-hack/ai-finance-controller/internal/config"
	"github.com/razorpay-hack/ai-finance-controller/internal/db"
)

func main() {
	cfg := config.Load()
	migURL := os.Getenv("MIGRATION_DATABASE_URL")
	if migURL == "" {
		migURL = "postgres://localhost:5432/ai_finance_db?sslmode=disable"
	}
	log.Printf("[MIGRATE] Running migrations against %s...", migURL)

	database, err := db.Connect(migURL)
	if err != nil {
		log.Printf("[MIGRATE] Connecting with fallback to app database URL: %s", cfg.DatabaseURL)
		database, err = db.Connect(cfg.DatabaseURL)
		if err != nil {
			log.Fatalf("[MIGRATE] Fatal: Failed to connect to database: %v", err)
		}
	}
	defer database.Close()

	migrationsDir := "migrations"
	if envDir := os.Getenv("MIGRATIONS_DIR"); envDir != "" {
		migrationsDir = envDir
	}

	if err := db.RunMigrations(database, migrationsDir); err != nil {
		log.Fatalf("[MIGRATE] Migration error: %v", err)
	}

	log.Println("[MIGRATE] Migrations applied successfully.")
}

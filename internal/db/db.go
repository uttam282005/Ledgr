package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// Connect establishes a connection pool to PostgreSQL.
func Connect(connStr string) (*sql.DB, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open db: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping db: %w", err)
	}

	return db, nil
}

// RunMigrations applies all *.up.sql files in the migrations directory in alphabetical order.
func RunMigrations(db *sql.DB, migrationsDir string) error {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return fmt.Errorf("could not read migrations directory: %w", err)
	}

	var upFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
			upFiles = append(upFiles, filepath.Join(migrationsDir, entry.Name()))
		}
	}
	sort.Strings(upFiles)

	for _, file := range upFiles {
		log.Printf("[DB] Applying migration: %s", filepath.Base(file))
		content, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("failed reading migration file %s: %w", file, err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err = db.ExecContext(ctx, string(content))
		cancel()
		if err != nil {
			return fmt.Errorf("failed executing migration %s: %w", file, err)
		}
	}

	log.Printf("[DB] All %d migrations successfully applied.", len(upFiles))
	return nil
}

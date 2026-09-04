.PHONY: up down migrate build seed reconcile investigate benchmark benchmark-all test run-api demo health clean reset

# Default port and database URLs
DATABASE_URL ?= postgres://finance_app:finance_app_secret@127.0.0.1:5432/ai_finance_db?sslmode=disable
QA_DATABASE_URL ?= postgres://qa_readonly:qa_readonly_secret@127.0.0.1:5432/ai_finance_db?sslmode=disable
HTTP_PORT ?= 8080

up:
	@if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then \
		echo "[DOCKER] Starting PostgreSQL container via Docker Compose..."; \
		docker compose up -d postgres; \
	elif [ -d "./.pgdata" ]; then \
		echo "[LOCAL] Starting local PostgreSQL server..."; \
		pg_ctl -D ./.pgdata -l ./.pgdata/logfile -o "-p 5432 -k /tmp" start || true; \
	else \
		echo "[INIT] Initializing local PostgreSQL..."; \
		initdb -D ./.pgdata --auth=trust --no-instructions; \
		pg_ctl -D ./.pgdata -l ./.pgdata/logfile -o "-p 5432 -k /tmp" start; \
		psql -h 127.0.0.1 -p 5432 -U uttam -d postgres -c "CREATE DATABASE ai_finance_db;" || true; \
	fi

down:
	@if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then \
		docker compose down; \
	elif [ -d "./.pgdata" ]; then \
		pg_ctl -D ./.pgdata stop || true; \
	fi

migrate:
	@echo "[MIGRATE] Applying schema and security migrations..."
	@DATABASE_URL="$(DATABASE_URL)" go run ./cmd/migrate

reset:
	@echo "[RESET] Truncating operational reconciliation tables..."
	@go run ./cmd/reset

build:
	@mkdir -p bin
	go build -o bin/api ./cmd/api
	go build -o bin/migrate ./cmd/migrate
	go build -o bin/reset ./cmd/reset
	go build -o bin/seed ./cmd/seed
	go build -o bin/reconcile ./cmd/reconcile
	go build -o bin/benchmark ./cmd/benchmark
	go build -o bin/investigate ./cmd/investigate

seed:
	@echo "[SEED] Generating synthetic dataset and ingesting into PostgreSQL..."
	@go run ./cmd/seed

reconcile:
	@echo "[RECONCILE] Running deterministic reconciliation engine..."
	@go run ./cmd/reconcile

investigate:
	@echo "[INVESTIGATE] Running AI exception investigation..."
	@go run ./cmd/investigate

SEED ?= 42
benchmark:
	@go run ./cmd/benchmark -seed $(SEED)

benchmark-all:
	@go run ./cmd/benchmark -all

test:
	go test -v -race ./...

run-api:
	go run ./cmd/api

demo: up migrate reset seed reconcile investigate
	@echo ""
	@echo "==============================================================="
	@echo "  AI Finance Controller Demo is Ready!"
	@echo "  Dashboard URL: http://localhost:$(HTTP_PORT)"
	@echo "==============================================================="
	@echo ""
	@go run ./cmd/api

health:
	@curl -s http://localhost:$(HTTP_PORT)/api/health | jq . || curl -s http://localhost:$(HTTP_PORT)/api/health

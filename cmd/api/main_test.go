package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/razorpay-hack/ai-finance-controller/internal/api"
	"github.com/razorpay-hack/ai-finance-controller/internal/config"
	"github.com/razorpay-hack/ai-finance-controller/internal/db"
	"github.com/razorpay-hack/ai-finance-controller/internal/qa"
)

func TestServer_HealthAndEndpoints(t *testing.T) {
	cfg := config.Load()
	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("Failed to connect to db: %v", err)
	}
	defer database.Close()

	qaService := qa.NewQAService(database, cfg.NvidiaAPIKey, cfg.NvidiaNIMBaseURL, cfg.NvidiaNIMModel)
	server := api.NewServer(database, qaService, cfg)

	// 1. Health check
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200 on /api/health, got %d", rec.Code)
	}

	var healthResp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &healthResp); err != nil {
		t.Fatalf("Failed to decode health response: %v", err)
	}
	if healthResp["status"] != "ok" {
		t.Errorf("Expected status 'ok', got %v", healthResp["status"])
	}

	// 2. Latest Run API
	reqLatest := httptest.NewRequest(http.MethodGet, "/api/runs/latest", nil)
	recLatest := httptest.NewRecorder()
	server.ServeHTTP(recLatest, reqLatest)

	if recLatest.Code != http.StatusOK {
		t.Errorf("Expected status 200 on /api/runs/latest, got %d", recLatest.Code)
	}

	// 3. Embedded Dashboard UI
	reqUI := httptest.NewRequest(http.MethodGet, "/", nil)
	recUI := httptest.NewRecorder()
	server.ServeHTTP(recUI, reqUI)

	if recUI.Code != http.StatusOK {
		t.Errorf("Expected status 200 on /, got %d", recUI.Code)
	}
	if !strings.Contains(recUI.Body.String(), "AI Finance Controller") {
		t.Errorf("Dashboard did not contain expected title: %s", recUI.Body.String()[:100])
	}
}

package generator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// GroundTruthCase represents the expected evaluation outcome for a single internal transaction.
type GroundTruthCase struct {
	CaseID                  string   `json:"case_id"`
	InternalID              string   `json:"internal_id"`
	ExpectedSettlementIDs   []string `json:"expected_settlement_ids"`
	ExpectedBankStatementID *string  `json:"expected_bank_statement_id"`
	ExpectedHop1            string   `json:"expected_hop1"`         // 'MATCHED', 'EXCEPTION'
	ExpectedHop2            string   `json:"expected_hop2"`         // 'MATCHED', 'EXCEPTION', 'N/A'
	ExpectedFinalStatus     string   `json:"expected_final_status"` // 'FULL', 'PARTIAL', 'UNMATCHED'
	ExpectedException       *string  `json:"expected_exception"`
}

// GroundTruth represents the complete evaluation artifact for a run.
// This is strictly isolated from the reconciliation matching engine.
type GroundTruth struct {
	RunID                  uuid.UUID         `json:"run_id"`
	Seed                   int64             `json:"seed"`
	DatasetVersion         string            `json:"dataset_version"`
	GeneratedAt            time.Time         `json:"generated_at"`
	Cases                  []GroundTruthCase `json:"cases"`
	OrphanSettlements      []string          `json:"orphan_settlements"`
	UnexplainedBankCredits []string          `json:"unexplained_bank_credits"`
}

// SaveGroundTruth persists the ground truth JSON to the evaluation directory.
func SaveGroundTruth(gt *GroundTruth, outputDir string) (string, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create ground truth directory: %w", err)
	}

	filename := filepath.Join(outputDir, fmt.Sprintf("%s.json", gt.RunID.String()))
	data, err := json.MarshalIndent(gt, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal ground truth: %w", err)
	}

	if err := os.WriteFile(filename, data, 0644); err != nil {
		return "", fmt.Errorf("failed to write ground truth file: %w", err)
	}

	return filename, nil
}

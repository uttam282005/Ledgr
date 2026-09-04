package qa

import (
	"context"
	"database/sql"
	"strings"
)

// MerchantInfo contains metadata about a merchant recognized in the database.
type MerchantInfo struct {
	ID        string `json:"id"`         // e.g. "MERCH_ZOMATO_DEL"
	BrandName string `json:"brand_name"` // e.g. "Zomato"
	City      string `json:"city"`       // e.g. "Delhi"
}

// CategoryInfo contains metadata and business descriptions for exception categories.
type CategoryInfo struct {
	Category    string `json:"category"`
	Hop         string `json:"hop"`
	Description string `json:"description"`
}

// DBMetadataContext represents dynamic and schema metadata about the active reconciliation run.
type DBMetadataContext struct {
	RunID           string         `json:"run_id"`
	ActiveMerchants []MerchantInfo `json:"active_merchants"`
	Categories      []CategoryInfo `json:"categories"`
}

// DefaultMerchants represents the canonical merchant registry for the platform.
var DefaultMerchants = []MerchantInfo{
	{ID: "MERCH_ZOMATO_DEL", BrandName: "Zomato", City: "Delhi"},
	{ID: "MERCH_SWIGGY_BLR", BrandName: "Swiggy", City: "Bangalore"},
	{ID: "MERCH_FLIPKART_BLR", BrandName: "Flipkart", City: "Bangalore"},
	{ID: "MERCH_AMAZON_MUM", BrandName: "Amazon", City: "Mumbai"},
	{ID: "MERCH_NYKAA_MUM", BrandName: "Nykaa", City: "Mumbai"},
}

// KnownCategories contains controlled vocabulary definitions for reconciliation exceptions.
var KnownCategories = []CategoryInfo{
	{
		Category:    "SETTLED_NOT_BANKED",
		Hop:         "HOP2",
		Description: "Gateway settlements awaiting bank credit statement / unbanked settlements / pending settlements",
	},
	{
		Category:    "ORPHAN_SETTLEMENT",
		Hop:         "HOP1",
		Description: "Settlement record from gateway with no matching internal gross ledger transaction",
	},
	{
		Category:    "AMOUNT_MISMATCH",
		Hop:         "HOP1",
		Description: "Internal transaction and settlement amount differ beyond fee tolerance",
	},
	{
		Category:    "DUPLICATE_SETTLEMENT",
		Hop:         "HOP1",
		Description: "Multiple gateway settlements match a single internal transaction",
	},
	{
		Category:    "PARTIAL_CREDIT",
		Hop:         "HOP2",
		Description: "Bank credit amount is less than expected batch settlement total",
	},
	{
		Category:    "BANKED_NOT_SETTLED",
		Hop:         "HOP2",
		Description: "Bank statement deposit with no corresponding settlement batch record",
	},
	{
		Category:    "NO_COUNTERPART",
		Hop:         "HOP1",
		Description: "Internal transaction with no gateway settlement counterpart",
	},
}

// ParseMerchantID extracts brand name and city from a merchant ID like "MERCH_ZOMATO_DEL".
func ParseMerchantID(id string) MerchantInfo {
	parts := strings.Split(id, "_")
	if len(parts) >= 3 && parts[0] == "MERCH" {
		brand := strings.Title(strings.ToLower(parts[1]))
		city := parts[2]
		return MerchantInfo{
			ID:        id,
			BrandName: brand,
			City:      city,
		}
	}
	return MerchantInfo{
		ID:        id,
		BrandName: id,
		City:      "Unknown",
	}
}

// FetchDBMetadataContext loads active merchants for the run from the database.
func FetchDBMetadataContext(ctx context.Context, db *sql.DB, runID string) *DBMetadataContext {
	meta := &DBMetadataContext{
		RunID:      runID,
		Categories: KnownCategories,
	}

	if db == nil {
		meta.ActiveMerchants = DefaultMerchants
		return meta
	}

	// Query distinct active merchants for this run
	query := `
		SELECT DISTINCT merchant_id FROM (
			SELECT merchant_id FROM internal_transactions WHERE run_id = $1
			UNION
			SELECT merchant_id FROM settlement_records WHERE run_id = $1
			UNION
			SELECT merchant_id FROM bank_statements WHERE run_id = $1
		) m WHERE merchant_id IS NOT NULL AND merchant_id != '' ORDER BY merchant_id;
	`
	rows, err := db.QueryContext(ctx, query, runID)
	if err != nil {
		meta.ActiveMerchants = DefaultMerchants
		return meta
	}
	defer rows.Close()

	var merchants []MerchantInfo
	for rows.Next() {
		var mid string
		if err := rows.Scan(&mid); err == nil && mid != "" {
			merchants = append(merchants, ParseMerchantID(mid))
		}
	}

	if len(merchants) == 0 {
		meta.ActiveMerchants = DefaultMerchants
	} else {
		meta.ActiveMerchants = merchants
	}

	return meta
}

// ResolveMerchant searches a question for mentions of known merchant brands or IDs.
func ResolveMerchant(question string, merchants []MerchantInfo) *MerchantInfo {
	lowerQ := strings.ToLower(question)
	if len(merchants) == 0 {
		merchants = DefaultMerchants
	}

	for _, m := range merchants {
		brandLower := strings.ToLower(m.BrandName)
		idLower := strings.ToLower(m.ID)
		if strings.Contains(lowerQ, brandLower) || strings.Contains(lowerQ, idLower) {
			matched := m
			return &matched
		}
	}

	// Fallback mapping for common brand names
	common := map[string]string{
		"zomato":   "MERCH_ZOMATO_DEL",
		"swiggy":   "MERCH_SWIGGY_BLR",
		"flipkart": "MERCH_FLIPKART_BLR",
		"amazon":   "MERCH_AMAZON_MUM",
		"nykaa":    "MERCH_NYKAA_MUM",
	}
	for brand, id := range common {
		if strings.Contains(lowerQ, brand) {
			m := ParseMerchantID(id)
			return &m
		}
	}

	return nil
}

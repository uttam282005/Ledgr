package ingestion

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
)

// GenerateMessySampleCSV creates intentionally unstandardized CSV files to demonstrate AI column mapping.
// Features messy headers, currency symbols, commas, DD/MM/YYYY dates, and omitted optional columns.
func GenerateMessySampleCSV(sourceType string) ([]byte, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)

	switch strings.ToLower(sourceType) {
	case SourceInternal:
		_ = writer.Write([]string{"Txn Ref ID", "Gross Paid Amount", "Txn Timestamp", "Merchant Code", "Gateway Order Ref"})
		_ = writer.Write([]string{"INT_MESSY_001", "₹5,214.63", "01/09/2026 10:15:30", "MERCH_ZOMATO_DEL", "REF_ORD_001"})
		_ = writer.Write([]string{"INT_MESSY_002", "₹1,280.00", "01/09/2026 11:20:00", "MERCH_SWIGGY_BLR", "REF_ORD_002"})
		_ = writer.Write([]string{"INT_MESSY_003", "₹3,450.75", "01/09/2026 12:05:15", "MERCH_FLIPKART_BLR", "REF_ORD_003"})
		_ = writer.Write([]string{"INT_MESSY_004", "₹890.00", "01/09/2026 13:45:00", "MERCH_AMAZON_HYD", "REF_ORD_004"})
		_ = writer.Write([]string{"INT_MESSY_005", "₹15,000.00", "01/09/2026 14:30:20", "MERCH_ZOMATO_DEL", "REF_ORD_005"})

	case SourceSettlement:
		_ = writer.Write([]string{"Settlement Record ID", "Net Payout INR", "Settlement Clearing Date", "Vendor MID", "Batch File ID", "Payment Order Reference"})
		_ = writer.Write([]string{"SET_MESSY_001", "₹5,110.34", "02/09/2026 04:00:00", "MERCH_ZOMATO_DEL", "BATCH_MESSY_01", "REF_ORD_001"})
		_ = writer.Write([]string{"SET_MESSY_002", "₹1,254.40", "02/09/2026 04:00:00", "MERCH_SWIGGY_BLR", "BATCH_MESSY_01", "REF_ORD_002"})
		_ = writer.Write([]string{"SET_MESSY_003", "₹3,381.74", "02/09/2026 04:00:00", "MERCH_FLIPKART_BLR", "BATCH_MESSY_01", "REF_ORD_003"})
		_ = writer.Write([]string{"SET_MESSY_004", "₹872.20", "02/09/2026 04:00:00", "MERCH_AMAZON_HYD", "BATCH_MESSY_01", "REF_ORD_004"})
		_ = writer.Write([]string{"SET_MESSY_005", "₹15,000.00", "02/09/2026 04:00:00", "MERCH_ZOMATO_DEL", "BATCH_MESSY_02", "REF_ORD_005"})

	case SourceBank:
		_ = writer.Write([]string{"Bank Statement Line", "Credit Amount Deposit", "Posting Date", "Client Account", "Settlement Batch Ref", "Bank Particulars"})
		_ = writer.Write([]string{"BNK_MESSY_001", "₹10,618.68", "02/09/2026 10:30:00", "MERCH_MULTI", "BATCH_MESSY_01", "NODAL ACC CR BATCH_MESSY_01"})
		_ = writer.Write([]string{"BNK_MESSY_002", "₹15,000.00", "02/09/2026 11:00:00", "MERCH_ZOMATO_DEL", "BATCH_MESSY_02", "NODAL ACC CR BATCH_MESSY_02"})

	default:
		return nil, fmt.Errorf("unknown source type '%s'", sourceType)
	}

	writer.Flush()
	return buf.Bytes(), writer.Error()
}

// GenerateSampleCSV creates standard clean canonical CSV files for testing and template reference.
func GenerateSampleCSV(sourceType string) ([]byte, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)

	switch strings.ToLower(sourceType) {
	case SourceInternal, "ledger":
		_ = writer.Write([]string{"id", "amount", "currency", "transaction_date", "merchant_id", "reference_id"})
		_ = writer.Write([]string{"INT_SAMPLE_001", "5214.63", "INR", "2026-09-01T10:15:30Z", "MERCH_ZOMATO_DEL", "REF_ORD_001"})
		_ = writer.Write([]string{"INT_SAMPLE_002", "1280.00", "INR", "2026-09-01T11:20:00Z", "MERCH_SWIGGY_BLR", "REF_ORD_002"})
		_ = writer.Write([]string{"INT_SAMPLE_003", "3450.75", "INR", "2026-09-01T12:05:15Z", "MERCH_FLIPKART_BLR", "REF_ORD_003"})
		_ = writer.Write([]string{"INT_SAMPLE_004", "890.00", "INR", "2026-09-01T13:45:00Z", "MERCH_AMAZON_HYD", "REF_ORD_004"})
		_ = writer.Write([]string{"INT_SAMPLE_005", "15000.00", "INR", "2026-09-01T14:30:20Z", "MERCH_ZOMATO_DEL", "REF_ORD_005"})

	case SourceSettlement, "gateway":
		_ = writer.Write([]string{"id", "settled_amount", "currency", "settlement_date", "merchant_id", "reference_id", "batch_id"})
		_ = writer.Write([]string{"SET_SAMPLE_001", "5110.34", "INR", "2026-09-02T04:00:00Z", "MERCH_ZOMATO_DEL", "REF_ORD_001", "BATCH_DEMO_01"})
		_ = writer.Write([]string{"SET_SAMPLE_002", "1254.40", "INR", "2026-09-02T04:00:00Z", "MERCH_SWIGGY_BLR", "REF_ORD_002", "BATCH_DEMO_01"})
		_ = writer.Write([]string{"SET_SAMPLE_003", "3381.74", "INR", "2026-09-02T04:00:00Z", "MERCH_FLIPKART_BLR", "REF_ORD_003", "BATCH_DEMO_01"})
		_ = writer.Write([]string{"SET_SAMPLE_004", "872.20", "INR", "2026-09-02T04:00:00Z", "MERCH_AMAZON_HYD", "REF_ORD_004", "BATCH_DEMO_01"})
		_ = writer.Write([]string{"SET_SAMPLE_005", "15000.00", "INR", "2026-09-02T04:00:00Z", "MERCH_ZOMATO_DEL", "REF_ORD_005", "BATCH_DEMO_02"})

	case SourceBank, "statement":
		_ = writer.Write([]string{"id", "credited_amount", "currency", "credit_date", "merchant_id", "batch_reference", "narration"})
		_ = writer.Write([]string{"BNK_SAMPLE_001", "10618.68", "INR", "2026-09-02T10:30:00Z", "MERCH_MULTI", "BATCH_DEMO_01", "NODAL ACC CR BATCH_DEMO_01"})
		_ = writer.Write([]string{"BNK_SAMPLE_002", "15000.00", "INR", "2026-09-02T11:00:00Z", "MERCH_ZOMATO_DEL", "BATCH_DEMO_02", "NODAL ACC CR BATCH_DEMO_02"})

	default:
		return nil, fmt.Errorf("unknown source type '%s'", sourceType)
	}

	writer.Flush()
	return buf.Bytes(), writer.Error()
}


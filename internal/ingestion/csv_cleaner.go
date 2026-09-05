package ingestion

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// RawCSVData contains cleaned text data, detected encoding info, headers, and sample rows.
type RawCSVData struct {
	CleanedContent []byte
	Encoding       string
	Headers        []string
	SampleRows     [][]string
	TotalRows      int
	Warnings       []string
}

// CleanAndInspectCSV performs magic-byte validation, BOM removal, Latin-1 fallback,
// merged header detection, and extracts headers with the first 5 sample data rows.
func CleanAndInspectCSV(raw []byte) (*RawCSVData, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("uploaded file is empty (0 bytes)")
	}

	// 1. Magic bytes detection: detect XLSX disguised as CSV (ZIP archive magic PK\x03\x04)
	if len(raw) >= 4 && bytes.Equal(raw[:4], []byte{0x50, 0x4B, 0x03, 0x04}) {
		return nil, fmt.Errorf("file appears to be an Excel spreadsheet (.xlsx) renamed to .csv; please export and upload as standard CSV text")
	}

	var warnings []string
	encoding := "UTF-8"

	// 2. Strip UTF-8 BOM if present
	content := raw
	if bytes.HasPrefix(content, []byte{0xEF, 0xBB, 0xBF}) {
		content = content[3:]
		warnings = append(warnings, "UTF-8 BOM detected and stripped")
	}

	// 3. Fallback from non-UTF-8 to Latin-1 (ISO-8859-1)
	if !utf8.Valid(content) {
		encoding = "ISO-8859-1 (Latin-1)"
		warnings = append(warnings, "Non-UTF-8 encoding detected; converted from Latin-1 fallback")
		var buf bytes.Buffer
		for _, b := range content {
			buf.WriteRune(rune(b))
		}
		content = buf.Bytes()
	}

	// 4. Normalize line breaks (\r\n and \r -> \n)
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	// 5. Parse using encoding/csv with resilient options
	reader := csv.NewReader(strings.NewReader(text))
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1 // allow slight variance in trailing column counts

	var allRows [][]string
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Skip corrupted or unreadable individual lines if possible
			continue
		}

		// Filter out empty rows (all elements empty or whitespace)
		isEmpty := true
		for _, cell := range row {
			if strings.TrimSpace(cell) != "" {
				isEmpty = false
				break
			}
		}
		if !isEmpty {
			allRows = append(allRows, row)
		}
	}

	if len(allRows) == 0 {
		return nil, fmt.Errorf("CSV file contains no valid data rows")
	}

	// 6. Merged header row detection (e.g. Bank exports where Row 1 is a title/account banner)
	headerIdx := 0
	if len(allRows) >= 2 {
		nonEmptyRow0 := 0
		for _, c := range allRows[0] {
			if strings.TrimSpace(c) != "" {
				nonEmptyRow0++
			}
		}
		nonEmptyRow1 := 0
		for _, c := range allRows[1] {
			if strings.TrimSpace(c) != "" {
				nonEmptyRow1++
			}
		}

		// If Row 0 only has 1 filled cell (e.g. title) and Row 1 has multiple columns, use Row 1
		if nonEmptyRow0 <= 1 && nonEmptyRow1 >= 3 {
			headerIdx = 1
			warnings = append(warnings, "Detected title banner in row 1; using row 2 as column headers")
		}
	}

	rawHeaders := allRows[headerIdx]
	headers := make([]string, len(rawHeaders))
	for i, h := range rawHeaders {
		headers[i] = strings.TrimSpace(h)
	}

	// Collect first 5 sample data rows
	var sampleRows [][]string
	dataStart := headerIdx + 1
	for i := dataStart; i < len(allRows) && len(sampleRows) < 5; i++ {
		row := allRows[i]
		cleanRow := make([]string, len(row))
		for j, cell := range row {
			cleanRow[j] = strings.TrimSpace(cell)
		}
		sampleRows = append(sampleRows, cleanRow)
	}

	totalDataRows := len(allRows) - dataStart
	if totalDataRows <= 0 {
		return nil, fmt.Errorf("CSV has headers but contains 0 data rows")
	}

	return &RawCSVData{
		CleanedContent: []byte(text),
		Encoding:       encoding,
		Headers:        headers,
		SampleRows:     sampleRows,
		TotalRows:      totalDataRows,
		Warnings:       warnings,
	}, nil
}

// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package builtin

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
	"github.com/xuri/excelize/v2"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

const (
	// MaxDocumentSize is the maximum file size for document parsing (100MB)
	MaxDocumentSize = 100 * 1024 * 1024
	// MaxCSVRows is the default maximum number of CSV rows to parse
	MaxCSVRows = 10000
	// MaxPDFPages is the default maximum number of PDF pages to parse
	MaxPDFPages = 100
	// MaxExcelRows is the default maximum number of Excel rows per sheet
	MaxExcelRows = 10000
)

// DocumentParseTool provides document parsing capabilities for CSV, PDF, and Excel files.
type DocumentParseTool struct {
	baseDir string // Optional base directory for relative paths
}

// NewDocumentParseTool creates a new document parsing tool.
// If baseDir is empty, the current working directory is used.
func NewDocumentParseTool(baseDir string) *DocumentParseTool {
	if baseDir == "" {
		baseDir, _ = os.Getwd()
	}
	return &DocumentParseTool{baseDir: baseDir}
}

// Name returns the tool name.
func (t *DocumentParseTool) Name() string {
	return "parse_document"
}

// Description returns the tool description.
func (t *DocumentParseTool) Description() string {
	return `Parse and extract structured data from documents. Supports:
- CSV files: Headers, type inference, custom delimiters (max 10,000 rows)
- PDF files: Text extraction, metadata, page selection (max 100 pages)
- Excel files (.xlsx): Multi-sheet parsing, cell types, formulas (max 10,000 rows/sheet)

Parameters:
- file_path (required): Path to the document file
- format (optional): "auto" (default), "csv", "pdf", or "xlsx" - auto-detects from extension
- options (optional): Format-specific options:
  CSV: delimiter, has_headers, max_rows
  PDF: pages (array/string), max_pages, include_metadata
  Excel: sheets (array/string), max_rows, include_formulas, has_headers

Returns structured data including content, metadata, and statistics.`
}

// InputSchema returns the JSON schema for the tool's input.
func (t *DocumentParseTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"file_path": {
				Type:        "string",
				Description: "Path to the document file (CSV, PDF, or Excel)",
			},
			"format": {
				Type:        "string",
				Description: "Document format: 'auto' (default), 'csv', 'pdf', or 'xlsx'",
				Enum:        []interface{}{"auto", "csv", "pdf", "xlsx"},
			},
			"options": {
				Type:        "object",
				Description: "Format-specific parsing options",
				Properties: map[string]*shuttle.JSONSchema{
					"detailed_analysis": {
						Type:        "boolean",
						Description: "Enable enhanced statistical profiling and Teradata type inference (CSV only). Returns column statistics, data quality scores, and generated DDL.",
					},
					"database": {
						Type:        "string",
						Description: "Target Teradata database name (used with detailed_analysis)",
					},
					"table_name": {
						Type:        "string",
						Description: "Target Teradata table name (used with detailed_analysis). Defaults to file name without extension.",
					},
					"delimiter": {
						Type:        "string",
						Description: "CSV delimiter character (default: comma)",
					},
					"has_headers": {
						Type:        "boolean",
						Description: "Whether CSV has headers in first row (default: true)",
					},
					"max_rows": {
						Type:        "integer",
						Description: "Maximum number of rows to parse (CSV: 10000, Excel: 10000 per sheet)",
					},
					"pages": {
						Type:        "string",
						Description: "PDF pages to extract: 'all', 'first', 'last', '1-5', or array of page numbers",
					},
					"max_pages": {
						Type:        "integer",
						Description: "Maximum number of PDF pages to parse (default: 100)",
					},
					"include_metadata": {
						Type:        "boolean",
						Description: "Include PDF metadata (default: true)",
					},
					"sheets": {
						Type:        "string",
						Description: "Excel sheets to parse: 'all', 'first', or array of sheet names",
					},
					"include_formulas": {
						Type:        "boolean",
						Description: "Include Excel cell formulas (default: false)",
					},
				},
			},
		},
		Required: []string{"file_path"},
	}
}

// Backend returns the backend name (empty for builtin tools).
func (t *DocumentParseTool) Backend() string {
	return ""
}

// Execute parses a document and returns structured data.
func (t *DocumentParseTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	// Extract file path
	filePath, ok := params["file_path"].(string)
	if !ok || filePath == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_params",
				Message: "file_path parameter is required and must be a string",
			},
		}, nil
	}

	// Clean and validate path
	cleanPath, err := t.cleanPath(filePath)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_path",
				Message: err.Error(),
			},
		}, nil
	}

	// Check file exists and size
	fileInfo, err := os.Stat(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "file_not_found",
					Message: fmt.Sprintf("file not found: %s", cleanPath),
				},
			}, nil
		}
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "file_error",
				Message: fmt.Sprintf("error accessing file: %v", err),
			},
		}, nil
	}

	if fileInfo.Size() > MaxDocumentSize {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "file_too_large",
				Message: fmt.Sprintf("file size (%d bytes) exceeds maximum (%d bytes)", fileInfo.Size(), MaxDocumentSize),
			},
		}, nil
	}

	// Extract format
	format := "auto"
	if f, ok := params["format"].(string); ok {
		format = strings.ToLower(f)
	}

	// Auto-detect format if needed
	if format == "auto" {
		format = t.detectFormat(cleanPath)
		if format == "" {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "unsupported_format",
					Message: fmt.Sprintf("unable to detect format for file: %s", cleanPath),
				},
			}, nil
		}
	}

	// Extract options
	options := make(map[string]interface{})
	if opts, ok := params["options"].(map[string]interface{}); ok {
		options = opts
	}

	// Check for detailed_analysis option (CSV only)
	detailedAnalysis := false
	if da, ok := options["detailed_analysis"].(bool); ok {
		detailedAnalysis = da
	}

	// Route to appropriate parser
	var data map[string]interface{}
	switch format {
	case "csv":
		if detailedAnalysis {
			data, err = t.parseCSVWithAnalysis(cleanPath, options)
		} else {
			data, err = t.parseCSV(cleanPath, options)
		}
	case "pdf":
		data, err = t.parsePDF(cleanPath, options)
	case "xlsx":
		data, err = t.parseExcel(cleanPath, options)
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "unsupported_format",
				Message: fmt.Sprintf("unsupported format: %s (supported: csv, pdf, xlsx)", format),
			},
		}, nil
	}

	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "parse_error",
				Message: fmt.Sprintf("error parsing %s: %v", format, err),
			},
		}, nil
	}

	// Add common metadata
	data["file_path"] = cleanPath
	data["file_size"] = fileInfo.Size()
	data["format"] = format
	data["parsed_at"] = time.Now().Format(time.RFC3339)

	return &shuttle.Result{
		Success: true,
		Data:    data,
	}, nil
}

// cleanPath cleans and validates the file path.
func (t *DocumentParseTool) cleanPath(path string) (string, error) {
	// Handle absolute vs relative paths
	if !filepath.IsAbs(path) {
		path = filepath.Join(t.baseDir, path)
	}

	// Clean the path
	cleanPath := filepath.Clean(path)

	// Security check: prevent access to sensitive system directories
	blacklistedPaths := []string{
		"/etc",
		"/bin",
		"/sbin",
		"/usr/bin",
		"/usr/sbin",
		"/System",
		"/Library/Security",
		"/private/etc",
		"/private/var/root",
	}

	for _, blocked := range blacklistedPaths {
		if strings.HasPrefix(cleanPath, blocked) {
			return "", fmt.Errorf("access denied to system directory: %s", blocked)
		}
	}

	return cleanPath, nil
}

// detectFormat detects the document format from file extension.
func (t *DocumentParseTool) detectFormat(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".csv":
		return "csv"
	case ".pdf":
		return "pdf"
	case ".xlsx":
		return "xlsx"
	default:
		return ""
	}
}

// parseCSV parses a CSV file and returns structured data.
func (t *DocumentParseTool) parseCSV(filePath string, options map[string]interface{}) (map[string]interface{}, error) {
	// Open file
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Extract options
	delimiter := ','
	if d, ok := options["delimiter"].(string); ok && len(d) > 0 {
		delimiter = rune(d[0])
	}

	hasHeaders := true
	if h, ok := options["has_headers"].(bool); ok {
		hasHeaders = h
	}

	maxRows := MaxCSVRows
	if m, ok := options["max_rows"].(float64); ok {
		maxRows = int(m)
	} else if m, ok := options["max_rows"].(int); ok {
		maxRows = m
	}

	// Create CSV reader
	reader := csv.NewReader(file)
	reader.Comma = delimiter
	reader.TrimLeadingSpace = true

	// Read all records
	var headers []string
	var rows [][]string
	rowCount := 0

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("error reading CSV: %v", err)
		}

		if rowCount == 0 && hasHeaders {
			headers = record
		} else {
			rows = append(rows, record)
			if len(rows) >= maxRows {
				break
			}
		}
		rowCount++
	}

	// If no headers specified, generate default headers
	if !hasHeaders && len(rows) > 0 {
		for i := 0; i < len(rows[0]); i++ {
			headers = append(headers, fmt.Sprintf("column_%d", i+1))
		}
	}

	// Infer column types
	columnTypes := t.inferColumnTypes(rows)

	// Convert to structured format
	var structuredRows []map[string]interface{}
	for _, row := range rows {
		rowMap := make(map[string]interface{})
		for i, value := range row {
			if i < len(headers) {
				rowMap[headers[i]] = t.convertValue(value, columnTypes[i])
			}
		}
		structuredRows = append(structuredRows, rowMap)
	}

	return map[string]interface{}{
		"headers":      headers,
		"rows":         structuredRows,
		"row_count":    len(rows),
		"column_count": len(headers),
		"column_types": columnTypes,
		"has_headers":  hasHeaders,
		"delimiter":    string(delimiter),
	}, nil
}

// inferColumnTypes infers data types for each column in the CSV.
func (t *DocumentParseTool) inferColumnTypes(rows [][]string) []string {
	if len(rows) == 0 {
		return nil
	}

	columnCount := len(rows[0])
	types := make([]string, columnCount)

	for col := 0; col < columnCount; col++ {
		intCount := 0
		floatCount := 0
		boolCount := 0
		dateCount := 0
		totalCount := 0

		for _, row := range rows {
			if col >= len(row) {
				continue
			}
			value := strings.TrimSpace(row[col])
			if value == "" {
				continue
			}
			totalCount++

			// Check int
			if _, err := strconv.ParseInt(value, 10, 64); err == nil {
				intCount++
				continue
			}

			// Check float
			if _, err := strconv.ParseFloat(value, 64); err == nil {
				floatCount++
				continue
			}

			// Check bool
			lower := strings.ToLower(value)
			if lower == "true" || lower == "false" || lower == "yes" || lower == "no" || lower == "0" || lower == "1" {
				boolCount++
				continue
			}

			// Check date (simple heuristic)
			if _, err := time.Parse("2006-01-02", value); err == nil {
				dateCount++
				continue
			}
			if _, err := time.Parse("01/02/2006", value); err == nil {
				dateCount++
				continue
			}
		}

		// Determine type based on majority
		if totalCount == 0 {
			types[col] = "string"
		} else if intCount == totalCount {
			types[col] = "integer"
		} else if intCount+floatCount == totalCount {
			types[col] = "float"
		} else if boolCount == totalCount {
			types[col] = "boolean"
		} else if dateCount == totalCount {
			types[col] = "date"
		} else {
			types[col] = "string"
		}
	}

	return types
}

// convertValue converts a string value to the appropriate type.
func (t *DocumentParseTool) convertValue(value string, columnType string) interface{} {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	switch columnType {
	case "integer":
		if i, err := strconv.ParseInt(value, 10, 64); err == nil {
			return i
		}
	case "float":
		if f, err := strconv.ParseFloat(value, 64); err == nil {
			return f
		}
	case "boolean":
		lower := strings.ToLower(value)
		if lower == "true" || lower == "yes" || lower == "1" {
			return true
		}
		if lower == "false" || lower == "no" || lower == "0" {
			return false
		}
	case "date":
		if t, err := time.Parse("2006-01-02", value); err == nil {
			return t.Format("2006-01-02")
		}
		if t, err := time.Parse("01/02/2006", value); err == nil {
			return t.Format("2006-01-02")
		}
	}

	return value
}

// parsePDF parses a PDF file and extracts text and metadata.
func (t *DocumentParseTool) parsePDF(filePath string, options map[string]interface{}) (map[string]interface{}, error) {
	// Open PDF
	file, reader, err := pdf.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("error opening PDF: %v", err)
	}
	defer file.Close()

	totalPages := reader.NumPage()

	// Extract options
	maxPages := MaxPDFPages
	if m, ok := options["max_pages"].(float64); ok {
		maxPages = int(m)
	} else if m, ok := options["max_pages"].(int); ok {
		maxPages = m
	}

	includeMetadata := true
	if im, ok := options["include_metadata"].(bool); ok {
		includeMetadata = im
	}

	// Determine which pages to extract
	pagesToExtract := t.parsePageSelection(options["pages"], totalPages)
	if len(pagesToExtract) > maxPages {
		pagesToExtract = pagesToExtract[:maxPages]
	}

	// Extract text from pages
	var pages []map[string]interface{}
	totalChars := 0

	for _, pageNum := range pagesToExtract {
		if pageNum < 1 || pageNum > totalPages {
			continue
		}

		page := reader.Page(pageNum)
		if page.V.IsNull() {
			continue
		}

		text, err := page.GetPlainText(nil)
		if err != nil {
			// Continue with other pages if one fails
			continue
		}

		pages = append(pages, map[string]interface{}{
			"page_number": pageNum,
			"text":        text,
			"char_count":  len(text),
		})
		totalChars += len(text)
	}

	result := map[string]interface{}{
		"page_count":  totalPages,
		"pages":       pages,
		"total_chars": totalChars,
	}

	// Extract metadata if requested
	if includeMetadata {
		metadata := make(map[string]interface{})
		// Note: ledongthuc/pdf has limited metadata support
		// Most PDFs will have minimal metadata available
		result["metadata"] = metadata
	}

	return result, nil
}

// parsePageSelection parses the pages parameter and returns a list of page numbers.
func (t *DocumentParseTool) parsePageSelection(pagesParam interface{}, totalPages int) []int {
	if pagesParam == nil {
		// Default: all pages
		pages := make([]int, totalPages)
		for i := 0; i < totalPages; i++ {
			pages[i] = i + 1
		}
		return pages
	}

	// Handle string: "all", "first", "last", "1-5"
	if str, ok := pagesParam.(string); ok {
		switch strings.ToLower(str) {
		case "all":
			pages := make([]int, totalPages)
			for i := 0; i < totalPages; i++ {
				pages[i] = i + 1
			}
			return pages
		case "first":
			return []int{1}
		case "last":
			return []int{totalPages}
		default:
			// Try to parse range "1-5"
			if strings.Contains(str, "-") {
				parts := strings.Split(str, "-")
				if len(parts) == 2 {
					start, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
					end, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
					if err1 == nil && err2 == nil {
						var pages []int
						for i := start; i <= end && i <= totalPages; i++ {
							pages = append(pages, i)
						}
						return pages
					}
				}
			}
		}
	}

	// Handle array of page numbers
	if arr, ok := pagesParam.([]interface{}); ok {
		var pages []int
		for _, item := range arr {
			if num, ok := item.(float64); ok {
				pageNum := int(num)
				if pageNum >= 1 && pageNum <= totalPages {
					pages = append(pages, pageNum)
				}
			} else if num, ok := item.(int); ok {
				if num >= 1 && num <= totalPages {
					pages = append(pages, num)
				}
			}
		}
		return pages
	}

	// Default: all pages
	pages := make([]int, totalPages)
	for i := 0; i < totalPages; i++ {
		pages[i] = i + 1
	}
	return pages
}

// parseExcel parses an Excel (.xlsx) file and extracts data.
func (t *DocumentParseTool) parseExcel(filePath string, options map[string]interface{}) (map[string]interface{}, error) {
	// Open Excel file
	file, err := excelize.OpenFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("error opening Excel file: %v", err)
	}
	defer file.Close()

	// Extract options
	maxRows := MaxExcelRows
	if m, ok := options["max_rows"].(float64); ok {
		maxRows = int(m)
	} else if m, ok := options["max_rows"].(int); ok {
		maxRows = m
	}

	includeFormulas := false
	if f, ok := options["include_formulas"].(bool); ok {
		includeFormulas = f
	}

	hasHeaders := true
	if h, ok := options["has_headers"].(bool); ok {
		hasHeaders = h
	}

	// Determine which sheets to parse
	allSheetNames := file.GetSheetList()
	sheetsToProcess := t.parseSheetSelection(options["sheets"], allSheetNames)

	// Parse sheets
	var sheets []map[string]interface{}
	for _, sheetName := range sheetsToProcess {
		rows, err := file.GetRows(sheetName)
		if err != nil {
			continue
		}

		if len(rows) == 0 {
			continue
		}

		// Extract headers
		var headers []string
		startRow := 0
		if hasHeaders && len(rows) > 0 {
			headers = rows[0]
			startRow = 1
		} else {
			// Generate default headers
			if len(rows) > 0 {
				for i := 0; i < len(rows[0]); i++ {
					headers = append(headers, fmt.Sprintf("column_%d", i+1))
				}
			}
		}

		// Extract data rows
		var structuredRows []map[string]interface{}
		rowCount := 0
		for i := startRow; i < len(rows) && rowCount < maxRows; i++ {
			row := rows[i]
			rowMap := make(map[string]interface{})
			for j, cell := range row {
				if j < len(headers) {
					// Try to parse as number
					if num, err := strconv.ParseFloat(cell, 64); err == nil {
						rowMap[headers[j]] = num
					} else {
						rowMap[headers[j]] = cell
					}

					// Include formula if requested
					if includeFormulas {
						cellName, _ := excelize.CoordinatesToCellName(j+1, i+1)
						if formula, err := file.GetCellFormula(sheetName, cellName); err == nil && formula != "" {
							rowMap[headers[j]+"_formula"] = formula
						}
					}
				}
			}
			structuredRows = append(structuredRows, rowMap)
			rowCount++
		}

		sheets = append(sheets, map[string]interface{}{
			"name":         sheetName,
			"headers":      headers,
			"rows":         structuredRows,
			"row_count":    len(structuredRows),
			"column_count": len(headers),
		})
	}

	return map[string]interface{}{
		"sheet_count": len(sheets),
		"sheets":      sheets,
	}, nil
}

// parseSheetSelection parses the sheets parameter and returns a list of sheet names.
func (t *DocumentParseTool) parseSheetSelection(sheetsParam interface{}, allSheets []string) []string {
	if sheetsParam == nil {
		// Default: all sheets
		return allSheets
	}

	// Handle string: "all", "first"
	if str, ok := sheetsParam.(string); ok {
		switch strings.ToLower(str) {
		case "all":
			return allSheets
		case "first":
			if len(allSheets) > 0 {
				return []string{allSheets[0]}
			}
			return nil
		default:
			// Try to find sheet by name
			for _, sheet := range allSheets {
				if strings.EqualFold(sheet, str) {
					return []string{sheet}
				}
			}
		}
	}

	// Handle array of sheet names
	if arr, ok := sheetsParam.([]interface{}); ok {
		var sheets []string
		for _, item := range arr {
			if name, ok := item.(string); ok {
				// Find matching sheet (case-insensitive)
				for _, sheet := range allSheets {
					if strings.EqualFold(sheet, name) {
						sheets = append(sheets, sheet)
						break
					}
				}
			}
		}
		return sheets
	}

	// Default: all sheets
	return allSheets
}

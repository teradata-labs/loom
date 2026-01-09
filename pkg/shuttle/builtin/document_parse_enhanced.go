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
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ColumnStatistics contains detailed statistics for robust type inference and data quality assessment.
type ColumnStatistics struct {
	Name     string
	Position int

	// Type inference
	InferredCSVType string  // "integer", "float", "string", "date", "boolean", "mixed"
	TeradataType    string  // "INTEGER", "DECIMAL(18,2)", "VARCHAR(500)", etc.
	TypeConfidence  float64 // 0.0-1.0

	// NULL handling
	TotalRows      int
	NullCount      int
	NullPercentage float64

	// Numeric statistics (for integer/float)
	MinValue      float64
	MaxValue      float64
	AvgValue      float64
	DecimalPlaces int // Max observed decimal places

	// String statistics
	MinLength     int
	MaxLength     int
	AvgLength     float64
	DistinctCount int
	SampleValues  []string // First 10 unique values

	// Date statistics
	DateFormat       string
	InvalidDateCount int

	// Data quality
	DataQualityScore float64  // 0.0-1.0
	Issues           []string // ["3 invalid dates", "mixed types detected"]
}

// TeradataTypeMapping maps CSV types to Teradata types with precision.
type TeradataTypeMapping struct {
	TeradataType   string
	Nullable       bool
	DefaultValue   string
	Recommendation string
	Confidence     float64
}

// analyzeColumn performs statistical profiling on a single column.
func analyzeColumn(header string, position int, rows []map[string]interface{}) ColumnStatistics {
	stats := ColumnStatistics{
		Name:             header,
		Position:         position,
		TotalRows:        len(rows),
		MinLength:        math.MaxInt32,
		MaxLength:        0,
		DataQualityScore: 1.0,
		Issues:           []string{},
	}

	if len(rows) == 0 {
		return stats
	}

	// Collect values and compute statistics
	var values []interface{}
	distinctValues := make(map[string]bool)
	var numericValues []float64
	var stringLengths []int
	var dateCount int
	var intCount int
	var floatCount int
	var boolCount int
	var maxDecimals int

	for _, row := range rows {
		val := row[header]

		// Handle nil/null
		if val == nil {
			stats.NullCount++
			continue
		}

		values = append(values, val)

		// Convert to string for distinct counting
		strVal := fmt.Sprintf("%v", val)
		distinctValues[strVal] = true

		// Type detection and statistics
		switch v := val.(type) {
		case int64:
			intCount++
			numericValues = append(numericValues, float64(v))
		case float64:
			floatCount++
			numericValues = append(numericValues, v)
			// Count decimal places
			strFloat := fmt.Sprintf("%f", v)
			if strings.Contains(strFloat, ".") {
				decimals := len(strings.TrimRight(strings.Split(strFloat, ".")[1], "0"))
				if decimals > maxDecimals {
					maxDecimals = decimals
				}
			}
		case string:
			stringLengths = append(stringLengths, len(v))
			if len(v) < stats.MinLength {
				stats.MinLength = len(v)
			}
			if len(v) > stats.MaxLength {
				stats.MaxLength = len(v)
			}

			// Try parsing as date
			if _, err := time.Parse("2006-01-02", v); err == nil {
				dateCount++
			} else if _, err := time.Parse("01/02/2006", v); err == nil {
				dateCount++
			}

			// Try parsing as bool
			lower := strings.ToLower(strings.TrimSpace(v))
			if lower == "true" || lower == "false" || lower == "yes" || lower == "no" ||
				lower == "t" || lower == "f" || lower == "y" || lower == "n" ||
				lower == "0" || lower == "1" {
				boolCount++
			}
		case bool:
			boolCount++
		}
	}

	// Calculate null percentage
	stats.NullPercentage = float64(stats.NullCount) / float64(stats.TotalRows) * 100

	// Distinct count
	stats.DistinctCount = len(distinctValues)

	// Sample values (first 10)
	sampleCount := 0
	for val := range distinctValues {
		if sampleCount >= 10 {
			break
		}
		stats.SampleValues = append(stats.SampleValues, val)
		sampleCount++
	}
	sort.Strings(stats.SampleValues)

	// Numeric statistics
	if len(numericValues) > 0 {
		stats.MinValue = numericValues[0]
		stats.MaxValue = numericValues[0]
		sum := 0.0
		for _, v := range numericValues {
			if v < stats.MinValue {
				stats.MinValue = v
			}
			if v > stats.MaxValue {
				stats.MaxValue = v
			}
			sum += v
		}
		stats.AvgValue = sum / float64(len(numericValues))
		stats.DecimalPlaces = maxDecimals
	}

	// String statistics
	if len(stringLengths) > 0 {
		sum := 0
		for _, length := range stringLengths {
			sum += length
		}
		stats.AvgLength = float64(sum) / float64(len(stringLengths))
	}

	// Type inference with confidence
	nonNullCount := stats.TotalRows - stats.NullCount
	if nonNullCount == 0 {
		stats.InferredCSVType = "string"
		stats.TypeConfidence = 0.0
		stats.Issues = append(stats.Issues, "All values are NULL")
		stats.DataQualityScore = 0.0
	} else if intCount == nonNullCount {
		stats.InferredCSVType = "integer"
		stats.TypeConfidence = 1.0
	} else if intCount+floatCount == nonNullCount {
		stats.InferredCSVType = "float"
		stats.TypeConfidence = 1.0
	} else if dateCount == nonNullCount {
		stats.InferredCSVType = "date"
		stats.TypeConfidence = 0.95
		stats.InvalidDateCount = nonNullCount - dateCount
	} else if boolCount == nonNullCount {
		stats.InferredCSVType = "boolean"
		stats.TypeConfidence = 1.0
	} else if len(stringLengths) == nonNullCount {
		stats.InferredCSVType = "string"
		stats.TypeConfidence = 0.9
	} else {
		// Mixed types
		stats.InferredCSVType = "mixed"
		stats.TypeConfidence = 0.5
		stats.Issues = append(stats.Issues, "Mixed types detected")
		stats.DataQualityScore -= 0.3
	}

	// Data quality scoring
	if stats.NullPercentage > 50 {
		stats.Issues = append(stats.Issues, fmt.Sprintf("High NULL rate: %.1f%%", stats.NullPercentage))
		stats.DataQualityScore -= 0.2
	} else if stats.NullPercentage > 10 {
		stats.Issues = append(stats.Issues, fmt.Sprintf("Moderate NULL rate: %.1f%%", stats.NullPercentage))
		stats.DataQualityScore -= 0.05
	}

	if stats.InvalidDateCount > 0 {
		pct := float64(stats.InvalidDateCount) / float64(nonNullCount) * 100
		stats.Issues = append(stats.Issues, fmt.Sprintf("%d invalid dates (%.1f%%)", stats.InvalidDateCount, pct))
		stats.DataQualityScore -= 0.1
	}

	// Ensure score stays in bounds
	if stats.DataQualityScore < 0 {
		stats.DataQualityScore = 0
	}
	if stats.DataQualityScore > 1 {
		stats.DataQualityScore = 1
	}

	// Infer Teradata type based on CSV type
	mapping := inferTeradataType(stats)
	stats.TeradataType = mapping.TeradataType

	return stats
}

// inferTeradataType performs intelligent type mapping with data quality handling.
func inferTeradataType(stats ColumnStatistics) TeradataTypeMapping {
	mapping := TeradataTypeMapping{
		Nullable:   stats.NullCount > 0,
		Confidence: stats.TypeConfidence,
	}

	switch stats.InferredCSVType {
	case "integer":
		// Check value ranges for optimal type
		if stats.MinValue >= -32768 && stats.MaxValue <= 32767 {
			mapping.TeradataType = "SMALLINT"
			mapping.Confidence = 1.0
		} else if stats.MinValue >= -2147483648 && stats.MaxValue <= 2147483647 {
			mapping.TeradataType = "INTEGER"
			mapping.Confidence = 1.0
		} else {
			mapping.TeradataType = "BIGINT"
			mapping.Confidence = 1.0
		}

		// Handle data quality issues
		if stats.DataQualityScore < 0.95 {
			mapping.Recommendation = "Consider DECIMAL for mixed numeric types"
			mapping.Confidence = 0.8
		}

	case "float":
		// Determine precision based on observed data
		precision := 18 // Default
		scale := stats.DecimalPlaces

		if scale == 0 {
			scale = 2 // Default for currency-like values
		}
		if scale > 18 {
			scale = 18 // Teradata max
		}

		// Check if values fit in smaller precision
		if stats.MaxValue < 1e6 {
			precision = 10
		}

		mapping.TeradataType = fmt.Sprintf("DECIMAL(%d,%d)", precision, scale)
		mapping.Confidence = 0.95

		// Handle scientific notation or very large numbers
		if stats.MaxValue > 1e15 {
			mapping.TeradataType = "DOUBLE PRECISION"
			mapping.Recommendation = "Large values detected, using DOUBLE PRECISION"
		}

	case "string":
		// VARCHAR sizing strategy
		maxLen := stats.MaxLength
		minLen := stats.MinLength

		if maxLen == 0 {
			maxLen = 255 // Default if no data
		}

		// Fixed-length optimization (e.g., state codes, flags)
		if minLen == maxLen && maxLen <= 10 && maxLen > 0 {
			mapping.TeradataType = fmt.Sprintf("CHAR(%d)", maxLen)
			mapping.Recommendation = "Fixed-length data, using CHAR for efficiency"
			mapping.Confidence = 1.0
		} else {
			// Variable-length with buffer
			varcharLen := int(float64(maxLen)*1.2 + 10) // 20% buffer + 10 chars

			// Cap at Teradata VARCHAR limits
			if varcharLen <= 255 {
				mapping.TeradataType = fmt.Sprintf("VARCHAR(%d)", max(varcharLen, 50))
			} else if varcharLen <= 4000 {
				mapping.TeradataType = fmt.Sprintf("VARCHAR(%d)", varcharLen)
			} else if varcharLen <= 32000 {
				mapping.TeradataType = fmt.Sprintf("VARCHAR(%d)", varcharLen)
			} else {
				mapping.TeradataType = "VARCHAR(32000)"
				mapping.Recommendation = "Large text values, consider CLOB or truncation"
			}
			mapping.Confidence = 0.9
		}

		// Low cardinality suggestion
		if stats.DistinctCount < 100 && stats.TotalRows > 1000 && stats.DistinctCount > 0 {
			mapping.Recommendation = fmt.Sprintf(
				"Low cardinality (%d distinct values) - consider lookup table",
				stats.DistinctCount,
			)
		}

	case "date":
		mapping.TeradataType = "DATE"
		mapping.Confidence = 0.95

		// Handle invalid dates
		if stats.InvalidDateCount > 0 {
			invalidPct := float64(stats.InvalidDateCount) / float64(stats.TotalRows-stats.NullCount) * 100
			mapping.Recommendation = fmt.Sprintf(
				"Warning: %d invalid dates (%.1f%%) will be set to NULL",
				stats.InvalidDateCount,
				invalidPct,
			)
			mapping.Confidence = 0.8
		}

	case "boolean":
		// Teradata boolean options
		mapping.TeradataType = "BYTEINT" // 0/1
		mapping.DefaultValue = "0"
		mapping.Recommendation = "Using BYTEINT (0=false, 1=true)"
		mapping.Confidence = 1.0

	case "mixed":
		// Data quality issue: mixed types in column
		varcharLen := stats.MaxLength * 2
		if varcharLen == 0 {
			varcharLen = 255
		}
		if varcharLen > 32000 {
			varcharLen = 32000
		}
		mapping.TeradataType = fmt.Sprintf("VARCHAR(%d)", varcharLen)
		mapping.Recommendation = "Mixed types detected - using VARCHAR for safety. Consider data cleansing."
		mapping.Confidence = 0.5

	default:
		// Fallback
		mapping.TeradataType = "VARCHAR(255)"
		mapping.Recommendation = "Unknown type - using VARCHAR(255) for safety"
		mapping.Confidence = 0.3
	}

	return mapping
}

// generateTeradataDDL creates CREATE TABLE statement with inferred types.
func generateTeradataDDL(database string, tableName string, columnStats []ColumnStatistics) (string, []string) {
	var ddl strings.Builder
	var recommendations []string

	ddl.WriteString(fmt.Sprintf("CREATE TABLE %s.%s (\n", database, tableName))

	for i, stats := range columnStats {
		mapping := inferTeradataType(stats)

		// Column definition
		ddl.WriteString(fmt.Sprintf("  %s %s", stats.Name, mapping.TeradataType))

		// NULL constraint
		if !mapping.Nullable {
			ddl.WriteString(" NOT NULL")
		}

		// Default value
		if mapping.DefaultValue != "" {
			ddl.WriteString(fmt.Sprintf(" DEFAULT %s", mapping.DefaultValue))
		}

		// Comma
		if i < len(columnStats)-1 {
			ddl.WriteString(",")
		}

		ddl.WriteString("\n")

		// Collect recommendations
		if mapping.Recommendation != "" {
			recommendations = append(recommendations, fmt.Sprintf(
				"Column '%s': %s (confidence: %.0f%%)",
				stats.Name,
				mapping.Recommendation,
				mapping.Confidence*100,
			))
		}

		// Data quality issues
		for _, issue := range stats.Issues {
			recommendations = append(recommendations, fmt.Sprintf(
				"Column '%s': %s",
				stats.Name,
				issue,
			))
		}
	}

	ddl.WriteString(")")

	// Add PRIMARY INDEX suggestion (use first non-null column)
	for _, stats := range columnStats {
		if stats.NullPercentage == 0 {
			ddl.WriteString(fmt.Sprintf("\nPRIMARY INDEX (%s)", stats.Name))
			break
		}
	}

	// Overall quality summary
	totalQuality := 0.0
	for _, stats := range columnStats {
		totalQuality += stats.DataQualityScore
	}
	avgQuality := totalQuality / float64(len(columnStats))
	recommendations = append([]string{
		fmt.Sprintf("Overall data quality: %.1f%%", avgQuality*100),
	}, recommendations...)

	return ddl.String(), recommendations
}

// max returns the maximum of two integers.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// parseCSVWithAnalysis parses CSV with enhanced statistical analysis.
func (t *DocumentParseTool) parseCSVWithAnalysis(filePath string, options map[string]interface{}) (map[string]interface{}, error) {
	// First, do basic parsing
	basicResult, err := t.parseCSV(filePath, options)
	if err != nil {
		return nil, err
	}

	rows, ok := basicResult["rows"].([]map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid rows format")
	}

	headers, ok := basicResult["headers"].([]string)
	if !ok {
		return nil, fmt.Errorf("invalid headers format")
	}

	// Perform statistical analysis on each column
	columnStats := make([]ColumnStatistics, len(headers))
	for i, header := range headers {
		columnStats[i] = analyzeColumn(header, i, rows)
	}

	// Extract target database and table name from options
	targetDB := "your_user_db"
	if db, ok := options["database"].(string); ok && db != "" {
		targetDB = db
	}

	targetTable := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	targetTable = strings.ReplaceAll(targetTable, " ", "_")
	targetTable = strings.ReplaceAll(targetTable, "-", "_")
	if tbl, ok := options["table_name"].(string); ok && tbl != "" {
		targetTable = tbl
	}

	// Generate Teradata DDL
	ddl, recommendations := generateTeradataDDL(targetDB, targetTable, columnStats)

	// Calculate overall quality score
	totalQuality := 0.0
	for _, stats := range columnStats {
		totalQuality += stats.DataQualityScore
	}
	overallQuality := totalQuality / float64(len(columnStats))

	// Aggregate all issues
	var allIssues []string
	for _, stats := range columnStats {
		for _, issue := range stats.Issues {
			allIssues = append(allIssues, fmt.Sprintf("%s: %s", stats.Name, issue))
		}
	}

	// Return enhanced result
	return map[string]interface{}{
		// Basic info (from original parsing)
		"file_path":    filePath,
		"format":       "csv",
		"row_count":    basicResult["row_count"],
		"column_count": basicResult["column_count"],
		"headers":      headers,
		"column_types": basicResult["column_types"],
		"has_headers":  basicResult["has_headers"],
		"delimiter":    basicResult["delimiter"],

		// Enhanced statistics
		"column_statistics": columnStats,

		// Teradata-specific
		"target_database":       targetDB,
		"target_table":          targetTable,
		"generated_ddl":         ddl,
		"recommendations":       recommendations,
		"overall_quality_score": overallQuality,
		"data_quality_issues":   allIssues,

		// For backwards compatibility, keep the basic rows
		"rows": rows,
	}, nil
}

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
	"strings"
	"testing"
)

func TestDocumentParseTool_CSV_DetailedAnalysis_Clean(t *testing.T) {
	tool := NewDocumentParseTool("")
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"file_path": "testdata/sales_clean.csv",
		"format":    "csv",
		"options": map[string]interface{}{
			"detailed_analysis": true,
			"database":          "test_db",
			"table_name":        "sales_q1",
		},
	})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("Expected success, got error: %v", result.Error)
	}

	// Check basic fields
	data, ok := result.Data.(map[string]interface{})
	if !ok {
		t.Fatal("Expected Data to be map[string]interface{}")
	}

	if data["format"] != "csv" {
		t.Errorf("Expected format 'csv', got %v", data["format"])
	}

	// Check enhanced fields
	if _, ok := data["column_statistics"]; !ok {
		t.Error("Expected column_statistics field")
	}

	if _, ok := data["generated_ddl"]; !ok {
		t.Error("Expected generated_ddl field")
	}

	if _, ok := data["recommendations"]; !ok {
		t.Error("Expected recommendations field")
	}

	if _, ok := data["overall_quality_score"]; !ok {
		t.Error("Expected overall_quality_score field")
	}

	// Check data quality score (should be high for clean data)
	qualityScore := data["overall_quality_score"].(float64)
	if qualityScore < 0.9 {
		t.Errorf("Expected quality score > 0.9 for clean data, got %.2f", qualityScore)
	}

	// Check target database and table
	if data["target_database"] != "test_db" {
		t.Errorf("Expected target_database 'test_db', got %v", data["target_database"])
	}
	if data["target_table"] != "sales_q1" {
		t.Errorf("Expected target_table 'sales_q1', got %v", data["target_table"])
	}

	// Check DDL contains expected elements
	ddl := data["generated_ddl"].(string)
	if !strings.Contains(ddl, "CREATE TABLE") {
		t.Error("DDL should contain CREATE TABLE")
	}
	if !strings.Contains(ddl, "test_db.sales_q1") {
		t.Error("DDL should contain database and table name")
	}
	if !strings.Contains(ddl, "customer_id") {
		t.Error("DDL should contain customer_id column")
	}
	if !strings.Contains(ddl, "PRIMARY INDEX") {
		t.Error("DDL should contain PRIMARY INDEX")
	}

	// Check column statistics
	columnStats := data["column_statistics"].([]ColumnStatistics)
	if len(columnStats) != 5 {
		t.Errorf("Expected 5 columns, got %d", len(columnStats))
	}

	// Check customer_id column (should be INTEGER)
	custIDStats := columnStats[0]
	if custIDStats.Name != "customer_id" {
		t.Errorf("Expected first column to be customer_id, got %s", custIDStats.Name)
	}
	if custIDStats.InferredCSVType != "integer" {
		t.Errorf("Expected customer_id to be integer, got %s", custIDStats.InferredCSVType)
	}
	if !strings.Contains(custIDStats.TeradataType, "INT") {
		t.Errorf("Expected customer_id to map to INT type, got %s", custIDStats.TeradataType)
	}

	// Check amount column (should be DECIMAL)
	amountStats := columnStats[2]
	if amountStats.Name != "amount" {
		t.Errorf("Expected third column to be amount, got %s", amountStats.Name)
	}
	if amountStats.InferredCSVType != "float" {
		t.Errorf("Expected amount to be float, got %s", amountStats.InferredCSVType)
	}
	if !strings.Contains(amountStats.TeradataType, "DECIMAL") {
		t.Errorf("Expected amount to map to DECIMAL, got %s", amountStats.TeradataType)
	}

	// Check status column (should suggest lookup table due to low cardinality)
	statusStats := columnStats[4]
	if statusStats.Name != "status" {
		t.Errorf("Expected fifth column to be status, got %s", statusStats.Name)
	}
	if statusStats.DistinctCount > 10 {
		t.Errorf("Expected status to have low cardinality, got %d distinct values", statusStats.DistinctCount)
	}
}

func TestDocumentParseTool_CSV_DetailedAnalysis_WithNulls(t *testing.T) {
	tool := NewDocumentParseTool("")
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"file_path": "testdata/sales_with_nulls.csv",
		"format":    "csv",
		"options": map[string]interface{}{
			"detailed_analysis": true,
		},
	})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("Expected success, got error: %v", result.Error)
	}

	data, ok := result.Data.(map[string]interface{})
	if !ok {
		t.Fatal("Expected Data to be map[string]interface{}")
	}

	columnStats := data["column_statistics"].([]ColumnStatistics)

	// Check that null counts are detected
	hasNulls := false
	for _, stats := range columnStats {
		if stats.NullCount > 0 {
			hasNulls = true
			if stats.NullPercentage <= 0 {
				t.Errorf("Column %s has nulls but NullPercentage is %.2f", stats.Name, stats.NullPercentage)
			}
		}
	}

	if !hasNulls {
		t.Error("Expected to detect null values in the CSV")
	}

	// Check data quality score (should be lower due to nulls, but still reasonable)
	qualityScore := data["overall_quality_score"].(float64)
	if qualityScore >= 0.99 {
		t.Errorf("Expected quality score < 0.99 due to nulls, got %.2f", qualityScore)
	}

	// Check that issues are reported
	issues := data["data_quality_issues"].([]string)
	if len(issues) == 0 {
		t.Error("Expected data quality issues to be reported for CSV with nulls")
	}
}

func TestDocumentParseTool_CSV_DetailedAnalysis_MixedTypes(t *testing.T) {
	tool := NewDocumentParseTool("")
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"file_path": "testdata/sales_mixed_types.csv",
		"format":    "csv",
		"options": map[string]interface{}{
			"detailed_analysis": true,
		},
	})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("Expected success, got error: %v", result.Error)
	}

	data, ok := result.Data.(map[string]interface{})
	if !ok {
		t.Fatal("Expected Data to be map[string]interface{}")
	}

	columnStats := data["column_statistics"].([]ColumnStatistics)

	// Check that data is handled gracefully
	// Note: The CSV parser gracefully handles mixed types by treating problematic columns as strings
	// This is the correct behavior - it doesn't fail, it falls back to strings

	// Verify that columns that should be numeric but have mixed data are detected as strings
	var customerIDStats, amountStats *ColumnStatistics
	for i := range columnStats {
		if columnStats[i].Name == "customer_id" {
			customerIDStats = &columnStats[i]
		}
		if columnStats[i].Name == "amount" {
			amountStats = &columnStats[i]
		}
	}

	// customer_id column has "ABC" which is not a number, so it should be string
	if customerIDStats != nil && customerIDStats.InferredCSVType != "string" {
		t.Errorf("Expected customer_id with mixed data to be string, got %s", customerIDStats.InferredCSVType)
	}

	// amount column has "invalid" which is not a number, so it should be string
	if amountStats != nil && amountStats.InferredCSVType != "string" {
		t.Errorf("Expected amount with mixed data to be string, got %s", amountStats.InferredCSVType)
	}

	// The quality score should still be good since all data was successfully parsed
	// (Mixed types are handled by fallback to strings, not considered a quality issue in this parser)
	qualityScore := data["overall_quality_score"].(float64)
	if qualityScore < 0.5 {
		t.Errorf("Expected reasonable quality score for successfully parsed data, got %.2f", qualityScore)
	}

	t.Logf("Mixed types test: all columns parsed as strings (correct fallback behavior), quality score: %.2f", qualityScore)
}

func TestDocumentParseTool_CSV_DetailedAnalysis_LowCardinality(t *testing.T) {
	tool := NewDocumentParseTool("")
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"file_path": "testdata/products_low_cardinality.csv",
		"format":    "csv",
		"options": map[string]interface{}{
			"detailed_analysis": true,
		},
	})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("Expected success, got error: %v", result.Error)
	}

	data, ok := result.Data.(map[string]interface{})
	if !ok {
		t.Fatal("Expected Data to be map[string]interface{}")
	}

	columnStats := data["column_statistics"].([]ColumnStatistics)

	// Find category and region columns (should have low cardinality)
	var categoryStats, regionStats *ColumnStatistics
	for i := range columnStats {
		if columnStats[i].Name == "category" {
			categoryStats = &columnStats[i]
		}
		if columnStats[i].Name == "region" {
			regionStats = &columnStats[i]
		}
	}

	if categoryStats == nil {
		t.Fatal("Expected to find 'category' column")
	}
	if regionStats == nil {
		t.Fatal("Expected to find 'region' column")
	}

	// Check that low cardinality is detected
	if categoryStats.DistinctCount > 5 {
		t.Errorf("Expected category to have low cardinality, got %d distinct values", categoryStats.DistinctCount)
	}
	if regionStats.DistinctCount > 5 {
		t.Errorf("Expected region to have low cardinality, got %d distinct values", regionStats.DistinctCount)
	}

	// Note: Lookup table suggestion requires > 1000 rows, but our test data has only 15 rows
	// So we just verify that the distinct counts are correctly detected
	t.Logf("Category distinct count: %d, Region distinct count: %d",
		categoryStats.DistinctCount, regionStats.DistinctCount)

	// Verify they are VARCHAR/CHAR types
	if !strings.Contains(categoryStats.TeradataType, "VARCHAR") &&
		!strings.Contains(categoryStats.TeradataType, "CHAR") {
		t.Errorf("Expected category to be VARCHAR or CHAR, got %s", categoryStats.TeradataType)
	}
}

func TestDocumentParseTool_CSV_DetailedAnalysis_VariousTypes(t *testing.T) {
	tool := NewDocumentParseTool("")
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"file_path": "testdata/employees_various_types.csv",
		"format":    "csv",
		"options": map[string]interface{}{
			"detailed_analysis": true,
		},
	})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("Expected success, got error: %v", result.Error)
	}

	data, ok := result.Data.(map[string]interface{})
	if !ok {
		t.Fatal("Expected Data to be map[string]interface{}")
	}

	columnStats := data["column_statistics"].([]ColumnStatistics)

	// Check that various types are correctly inferred
	typeMap := make(map[string]string)
	for _, stats := range columnStats {
		typeMap[stats.Name] = stats.InferredCSVType
	}

	// employee_id should be integer (BIGINT range)
	if typeMap["employee_id"] != "integer" {
		t.Errorf("Expected employee_id to be integer, got %s", typeMap["employee_id"])
	}

	// salary should be float
	if typeMap["salary"] != "float" {
		t.Errorf("Expected salary to be float, got %s", typeMap["salary"])
	}

	// hire_date should be date
	if typeMap["hire_date"] != "date" {
		t.Errorf("Expected hire_date to be date, got %s", typeMap["hire_date"])
	}

	// is_active should be boolean
	if typeMap["is_active"] != "boolean" {
		t.Errorf("Expected is_active to be boolean, got %s", typeMap["is_active"])
	}

	// department_code should be string
	if typeMap["department_code"] != "string" {
		t.Errorf("Expected department_code to be string, got %s", typeMap["department_code"])
	}

	// Check Teradata type mappings
	for _, stats := range columnStats {
		switch stats.Name {
		case "employee_id":
			// Should map to INTEGER or BIGINT
			if !strings.Contains(stats.TeradataType, "INT") {
				t.Errorf("Expected employee_id to map to INT type, got %s", stats.TeradataType)
			}
		case "salary":
			// Should map to DECIMAL
			if !strings.Contains(stats.TeradataType, "DECIMAL") {
				t.Errorf("Expected salary to map to DECIMAL, got %s", stats.TeradataType)
			}
		case "hire_date":
			// Should map to DATE
			if stats.TeradataType != "DATE" {
				t.Errorf("Expected hire_date to map to DATE, got %s", stats.TeradataType)
			}
		case "is_active":
			// Should map to BYTEINT
			if stats.TeradataType != "BYTEINT" {
				t.Errorf("Expected is_active to map to BYTEINT, got %s", stats.TeradataType)
			}
		case "department_code":
			// Should map to CHAR (fixed length, 3 characters)
			if !strings.Contains(stats.TeradataType, "CHAR") {
				t.Errorf("Expected department_code to map to CHAR, got %s", stats.TeradataType)
			}
		}
	}
}

func TestDocumentParseTool_CSV_DetailedAnalysis_DefaultTableName(t *testing.T) {
	tool := NewDocumentParseTool("")
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"file_path": "testdata/sales_clean.csv",
		"format":    "csv",
		"options": map[string]interface{}{
			"detailed_analysis": true,
		},
	})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("Expected success, got error: %v", result.Error)
	}

	data, ok := result.Data.(map[string]interface{})
	if !ok {
		t.Fatal("Expected Data to be map[string]interface{}")
	}

	// Check that table name defaults to file name without extension
	tableName := data["target_table"].(string)
	if tableName != "sales_clean" {
		t.Errorf("Expected default table name 'sales_clean', got %s", tableName)
	}

	// Check that database defaults to user database
	database := data["target_database"].(string)
	if database != "your_user_db" {
		t.Errorf("Expected default database 'your_user_db', got %s", database)
	}
}

func TestDocumentParseTool_CSV_BasicMode_NoEnhancedFields(t *testing.T) {
	tool := NewDocumentParseTool("")
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"file_path": "testdata/sales_clean.csv",
		"format":    "csv",
		"options": map[string]interface{}{
			"detailed_analysis": false,
		},
	})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("Expected success, got error: %v", result.Error)
	}

	data, ok := result.Data.(map[string]interface{})
	if !ok {
		t.Fatal("Expected Data to be map[string]interface{}")
	}

	// In basic mode, enhanced fields should NOT be present
	if _, ok := data["column_statistics"]; ok {
		t.Error("Basic mode should not include column_statistics")
	}
	if _, ok := data["generated_ddl"]; ok {
		t.Error("Basic mode should not include generated_ddl")
	}
	if _, ok := data["recommendations"]; ok {
		t.Error("Basic mode should not include recommendations")
	}
	if _, ok := data["overall_quality_score"]; ok {
		t.Error("Basic mode should not include overall_quality_score")
	}

	// Basic fields should still be present
	if _, ok := data["headers"]; !ok {
		t.Error("Basic mode should include headers")
	}
	if _, ok := data["rows"]; !ok {
		t.Error("Basic mode should include rows")
	}
	if _, ok := data["column_types"]; !ok {
		t.Error("Basic mode should include column_types")
	}
}

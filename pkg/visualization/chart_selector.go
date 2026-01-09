// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package visualization

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// ChartSelector analyzes data and recommends appropriate chart types
type ChartSelector struct {
	styleConfig *StyleConfig
}

// NewChartSelector creates a new chart selector
func NewChartSelector(styleConfig *StyleConfig) *ChartSelector {
	if styleConfig == nil {
		styleConfig = DefaultStyleConfig()
	}
	return &ChartSelector{
		styleConfig: styleConfig,
	}
}

// AnalyzeDataset examines dataset structure and patterns
func (cs *ChartSelector) AnalyzeDataset(ds *Dataset) *DataPattern {
	if len(ds.Data) == 0 {
		return &DataPattern{
			DataPoints: 0,
		}
	}

	pattern := &DataPattern{
		DataPoints:   ds.RowCount,
		NumericCols:  []string{},
		CategoryCols: []string{},
		TimeCols:     []string{},
	}

	// Analyze schema
	for colName, colType := range ds.Schema {
		switch colType {
		case "int", "int64", "float", "float64", "number":
			pattern.NumericCols = append(pattern.NumericCols, colName)
		case "string", "text", "category":
			pattern.CategoryCols = append(pattern.CategoryCols, colName)
		case "time", "date", "datetime", "timestamp":
			pattern.TimeCols = append(pattern.TimeCols, colName)
		}
	}

	// Detect patterns by examining data
	firstRow := ds.Data[0]

	// Check for ranking indicators (frequency, count, score, rank)
	for key := range firstRow {
		keyLower := strings.ToLower(key)
		if strings.Contains(keyLower, "frequency") ||
			strings.Contains(keyLower, "count") ||
			strings.Contains(keyLower, "score") ||
			strings.Contains(keyLower, "rank") {
			pattern.HasRanking = true
		}
		if strings.Contains(keyLower, "time") ||
			strings.Contains(keyLower, "date") {
			pattern.HasTimeSeries = true
		}
	}

	// Has categories if there are categorical columns
	pattern.HasCategories = len(pattern.CategoryCols) > 0

	// Has continuous if there are numeric columns
	pattern.HasContinuous = len(pattern.NumericCols) > 0

	// Check for array fields
	for key, val := range firstRow {
		if _, isArray := val.([]interface{}); isArray {
			pattern.HasArrayFields = true
		}
		// Also check if key suggests array (plural forms)
		if strings.HasSuffix(key, "s") || strings.Contains(key, "list") {
			pattern.HasArrayFields = true
		}
	}

	// Cardinality is the number of unique items (for small datasets)
	pattern.Cardinality = len(ds.Data)
	if pattern.Cardinality > 1000 {
		pattern.Cardinality = 1000 // Cap for estimation
	}

	return pattern
}

// RecommendChart analyzes a dataset and recommends the best chart type
func (cs *ChartSelector) RecommendChart(ds *Dataset) *ChartRecommendation {
	// Defensive check for empty datasets
	if ds.RowCount == 0 || len(ds.Data) == 0 {
		return &ChartRecommendation{
			ChartType:  ChartTypeBar,
			Title:      ds.Name,
			Rationale:  "Empty dataset, defaulting to bar chart",
			Confidence: 0.5,
		}
	}

	pattern := cs.AnalyzeDataset(ds)

	// Decision tree for chart selection
	// Priority: Specialized patterns first, then generic patterns

	// 1. Time series data → Line chart
	if pattern.HasTimeSeries && len(pattern.TimeCols) > 0 {
		return &ChartRecommendation{
			ChartType:  ChartTypeTimeSeries,
			Title:      fmt.Sprintf("%s Over Time", toTitle(ds.Name)),
			Rationale:  "Data contains temporal ordering, best visualized as time series",
			Confidence: 0.9,
			Config: map[string]interface{}{
				"time_column": pattern.TimeCols[0],
				"smooth":      true,
			},
		}
	}

	// 2. Graph/Network data (source-target relationships) → Graph chart (HIGH PRIORITY)
	if pattern.HasRelations || (len(ds.Data) > 0 && hasSourceTargetFields(ds.Data[0])) {
		return &ChartRecommendation{
			ChartType:  ChartTypeGraph,
			Title:      fmt.Sprintf("%s Network", toTitle(ds.Name)),
			Rationale:  "Data contains relationships/edges, best shown as network graph",
			Confidence: 0.90,
			Config: map[string]interface{}{
				"layout": "force",
			},
		}
	}

	// 3. Statistical distribution data (box plot fields) → Box plot (HIGH PRIORITY)
	if len(ds.Data) > 0 && hasBoxPlotFields(ds.Data[0]) {
		return &ChartRecommendation{
			ChartType:  ChartTypeBoxPlot,
			Title:      fmt.Sprintf("%s Distribution Analysis", toTitle(ds.Name)),
			Rationale:  "Data contains statistical distribution metrics (min, q1, median, q3, max)",
			Confidence: 0.88,
			Config:     map[string]interface{}{},
		}
	}

	// 4. Ranking data with 5-50 items → Bar chart
	if pattern.HasRanking && pattern.Cardinality >= 5 && pattern.Cardinality <= 50 && len(ds.Data) > 0 {
		return &ChartRecommendation{
			ChartType:  ChartTypeBar,
			Title:      fmt.Sprintf("Top %d by %s", pattern.Cardinality, inferValueColumn(ds.Data[0])),
			Rationale:  fmt.Sprintf("Ranked data with %d items, ideal for bar chart comparison", pattern.Cardinality),
			Confidence: 0.95,
			Config: map[string]interface{}{
				"orientation": "horizontal",
				"sort":        "descending",
			},
		}
	}

	// 5. Multi-dimensional data (3+ numeric dimensions) → Radar chart
	if len(pattern.NumericCols) >= 3 && pattern.Cardinality <= 10 {
		return &ChartRecommendation{
			ChartType:  ChartTypeRadar,
			Title:      fmt.Sprintf("%s Multi-dimensional Analysis", toTitle(ds.Name)),
			Rationale:  fmt.Sprintf("Data has %d dimensions, ideal for radar chart comparison", len(pattern.NumericCols)),
			Confidence: 0.82,
			Config: map[string]interface{}{
				"dimensions": pattern.NumericCols,
			},
		}
	}

	// 6. Hierarchical data → TreeMap
	if pattern.HasArrayFields || (len(ds.Data) > 0 && hasHierarchicalStructure(ds.Data[0])) {
		return &ChartRecommendation{
			ChartType:  ChartTypeTreeMap,
			Title:      fmt.Sprintf("%s Hierarchy", toTitle(ds.Name)),
			Rationale:  "Data has hierarchical structure, visualized as treemap",
			Confidence: 0.78,
			Config:     map[string]interface{}{},
		}
	}

	// 7. Few categories (2-7) → Pie chart
	if pattern.HasCategories && pattern.Cardinality >= 2 && pattern.Cardinality <= 7 {
		return &ChartRecommendation{
			ChartType:  ChartTypePie,
			Title:      fmt.Sprintf("%s Distribution", toTitle(ds.Name)),
			Rationale:  fmt.Sprintf("Small number of categories (%d), shows proportions well", pattern.Cardinality),
			Confidence: 0.85,
			Config: map[string]interface{}{
				"show_percentage": true,
			},
		}
	}

	// 8. Many categories → Bar chart
	if pattern.HasCategories && pattern.Cardinality > 7 {
		return &ChartRecommendation{
			ChartType:  ChartTypeBar,
			Title:      fmt.Sprintf("%s Comparison", toTitle(ds.Name)),
			Rationale:  fmt.Sprintf("Multiple categories (%d items) for comparison", pattern.Cardinality),
			Confidence: 0.80,
			Config: map[string]interface{}{
				"orientation": "vertical",
			},
		}
	}

	// 9. Two numeric dimensions → Scatter plot
	if len(pattern.NumericCols) >= 2 {
		return &ChartRecommendation{
			ChartType:  ChartTypeScatter,
			Title:      fmt.Sprintf("%s vs %s", pattern.NumericCols[0], pattern.NumericCols[1]),
			Rationale:  "Multiple numeric dimensions, shows correlation/distribution",
			Confidence: 0.75,
			Config: map[string]interface{}{
				"x_axis": pattern.NumericCols[0],
				"y_axis": pattern.NumericCols[1],
			},
		}
	}

	// 10. Default fallback → Bar chart
	return &ChartRecommendation{
		ChartType:  ChartTypeBar,
		Title:      toTitle(ds.Name),
		Rationale:  "General purpose chart for data comparison",
		Confidence: 0.60,
		Config:     map[string]interface{}{},
	}
}

// RecommendChartsForDatasets recommends charts for multiple datasets
func (cs *ChartSelector) RecommendChartsForDatasets(datasets []*Dataset) []*ChartRecommendation {
	recommendations := make([]*ChartRecommendation, 0, len(datasets))
	for _, ds := range datasets {
		rec := cs.RecommendChart(ds)
		recommendations = append(recommendations, rec)
	}
	return recommendations
}

// inferValueColumn tries to find the main value column for sorting/display
func inferValueColumn(row map[string]interface{}) string {
	// Common value column names
	candidates := []string{"frequency", "count", "value", "score", "total", "sum"}

	for _, candidate := range candidates {
		for key := range row {
			if strings.Contains(strings.ToLower(key), candidate) {
				return key
			}
		}
	}

	// Fallback: find first numeric column
	for key, val := range row {
		switch val.(type) {
		case int, int64, float32, float64:
			return key
		}
	}

	return "value"
}

// ParseDataFromPresentationToolResult converts presentation tool result to Dataset
func ParseDataFromPresentationToolResult(result map[string]interface{}, name string) (*Dataset, error) {
	// Extract items array
	items, ok := result["items"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("result does not contain 'items' array")
	}

	// Convert to []map[string]interface{}
	data := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]interface{}); ok {
			data = append(data, m)
		}
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("no valid data items found")
	}

	// Infer schema from first row
	schema := make(map[string]string)
	for key, val := range data[0] {
		schema[key] = inferType(val)
	}

	// Extract metadata
	metadata := make(map[string]interface{})
	for key, val := range result {
		if key != "items" {
			metadata[key] = val
		}
	}

	return &Dataset{
		Name:     name,
		Data:     data,
		Schema:   schema,
		Source:   getStringOrDefault(result, "source_key", "unknown"),
		RowCount: len(data),
		Metadata: metadata,
	}, nil
}

// inferType infers the data type from a value
func inferType(val interface{}) string {
	switch v := val.(type) {
	case int, int64, int32:
		return "int"
	case float32, float64:
		return "float"
	case string:
		// Check if it looks like a date/time
		if _, err := time.Parse(time.RFC3339, v); err == nil {
			return "datetime"
		}
		if _, err := time.Parse("2006-01-02", v); err == nil {
			return "date"
		}
		return "string"
	case bool:
		return "bool"
	case []interface{}:
		return "array"
	case map[string]interface{}:
		return "object"
	default:
		return "unknown"
	}
}

// getStringOrDefault safely extracts a string value from a map
func getStringOrDefault(m map[string]interface{}, key, defaultVal string) string {
	if val, ok := m[key]; ok {
		if str, ok := val.(string); ok {
			return str
		}
	}
	return defaultVal
}

// hasSourceTargetFields checks if data has graph/network structure (source, target fields)
func hasSourceTargetFields(row map[string]interface{}) bool {
	_, hasSource := row["source"]
	_, hasTarget := row["target"]
	return hasSource && hasTarget
}

// hasBoxPlotFields checks if data has statistical distribution fields
func hasBoxPlotFields(row map[string]interface{}) bool {
	requiredFields := []string{"min", "q1", "median", "q3", "max"}
	foundCount := 0
	for _, field := range requiredFields {
		if _, ok := row[field]; ok {
			foundCount++
		}
	}
	// Require at least 3 of the 5 fields to be present
	return foundCount >= 3
}

// hasHierarchicalStructure checks if data has nested/hierarchical structure
func hasHierarchicalStructure(row map[string]interface{}) bool {
	// Check for common hierarchical field names
	hierarchicalKeys := []string{"parent", "children", "parent_id", "level", "depth"}
	for _, key := range hierarchicalKeys {
		if _, ok := row[key]; ok {
			return true
		}
	}
	// Check for nested objects or arrays
	for key, val := range row {
		keyLower := strings.ToLower(key)
		if _, isMap := val.(map[string]interface{}); isMap {
			return true
		}
		if _, isArray := val.([]interface{}); isArray {
			return true
		}
		if strings.Contains(keyLower, "child") || strings.Contains(keyLower, "parent") {
			return true
		}
	}
	return false
}

// toTitle converts a string to title case (first letter of each word capitalized).
// Replaces deprecated strings.Title.
func toTitle(s string) string {
	words := strings.Fields(s)
	for i, word := range words {
		if len(word) > 0 {
			runes := []rune(word)
			runes[0] = unicode.ToUpper(runes[0])
			words[i] = string(runes)
		}
	}
	return strings.Join(words, " ")
}

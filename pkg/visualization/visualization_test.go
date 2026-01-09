// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package visualization

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// TestChartSelector_AnalyzeDataset tests data pattern analysis
func TestChartSelector_AnalyzeDataset(t *testing.T) {
	cs := NewChartSelector(nil)

	tests := []struct {
		name     string
		dataset  *Dataset
		expected DataPattern
	}{
		{
			name: "dataset with ranking",
			dataset: &Dataset{
				Name: "top_patterns",
				Data: []map[string]interface{}{
					{"pattern": "A→B→C", "frequency": 100},
					{"pattern": "A→C", "frequency": 80},
				},
				Schema: map[string]string{
					"pattern":   "string",
					"frequency": "int",
				},
				RowCount: 2,
			},
			expected: DataPattern{
				HasRanking:    true,
				HasCategories: true,
				HasContinuous: true,
				DataPoints:    2,
				Cardinality:   2,
				NumericCols:   []string{"frequency"},
				CategoryCols:  []string{"pattern"},
			},
		},
		{
			name: "empty dataset",
			dataset: &Dataset{
				Name:     "empty",
				Data:     []map[string]interface{}{},
				Schema:   map[string]string{},
				RowCount: 0,
			},
			expected: DataPattern{
				DataPoints: 0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pattern := cs.AnalyzeDataset(tt.dataset)

			if pattern.HasRanking != tt.expected.HasRanking {
				t.Errorf("HasRanking = %v, want %v", pattern.HasRanking, tt.expected.HasRanking)
			}
			if pattern.DataPoints != tt.expected.DataPoints {
				t.Errorf("DataPoints = %d, want %d", pattern.DataPoints, tt.expected.DataPoints)
			}
		})
	}
}

// TestChartSelector_RecommendChart tests chart recommendation logic
func TestChartSelector_RecommendChart(t *testing.T) {
	cs := NewChartSelector(nil)

	tests := []struct {
		name          string
		dataset       *Dataset
		expectedType  ChartType
		minConfidence float64
	}{
		{
			name: "ranking data should recommend bar chart",
			dataset: &Dataset{
				Name: "top_50_patterns",
				Data: []map[string]interface{}{
					{"pattern": "A→B→C", "frequency": 100},
					{"pattern": "A→C", "frequency": 80},
					{"pattern": "B→C", "frequency": 70},
					{"pattern": "A→B", "frequency": 60},
					{"pattern": "C→D", "frequency": 50},
				},
				Schema: map[string]string{
					"pattern":   "string",
					"frequency": "int",
				},
				RowCount: 5,
			},
			expectedType:  ChartTypeBar,
			minConfidence: 0.8,
		},
		{
			name: "few categories should recommend pie chart",
			dataset: &Dataset{
				Name: "segment_distribution",
				Data: []map[string]interface{}{
					{"segment": "enterprise", "count": 100},
					{"segment": "mid-market", "count": 80},
					{"segment": "small", "count": 60},
				},
				Schema: map[string]string{
					"segment": "string",
					"count":   "int",
				},
				RowCount: 3,
			},
			expectedType:  ChartTypePie,
			minConfidence: 0.7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := cs.RecommendChart(tt.dataset)

			if rec.ChartType != tt.expectedType {
				t.Errorf("ChartType = %v, want %v", rec.ChartType, tt.expectedType)
			}
			if rec.Confidence < tt.minConfidence {
				t.Errorf("Confidence = %.2f, want >= %.2f", rec.Confidence, tt.minConfidence)
			}
			if rec.Title == "" {
				t.Error("Title should not be empty")
			}
			if rec.Rationale == "" {
				t.Error("Rationale should not be empty")
			}
		})
	}
}

// TestEChartsGenerator_Generate tests ECharts config generation
func TestEChartsGenerator_Generate(t *testing.T) {
	eg := NewEChartsGenerator(nil)

	dataset := &Dataset{
		Name: "top_patterns",
		Data: []map[string]interface{}{
			{"pattern": "A→B→C", "frequency": 100},
			{"pattern": "A→C", "frequency": 80},
		},
		Schema: map[string]string{
			"pattern":   "string",
			"frequency": "int",
		},
		RowCount: 2,
	}

	rec := &ChartRecommendation{
		ChartType:  ChartTypeBar,
		Title:      "Top Patterns",
		Rationale:  "Ranking data",
		Confidence: 0.9,
		Config:     map[string]interface{}{},
	}

	config, err := eg.Generate(dataset, rec)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Verify config is valid JSON
	if !strings.Contains(config, "{") || !strings.Contains(config, "}") {
		t.Error("Generated config is not valid JSON")
	}

	// Verify contains key ECharts properties
	requiredProps := []string{"backgroundColor", "animation", "series", "xAxis", "yAxis"}
	for _, prop := range requiredProps {
		if !strings.Contains(config, prop) {
			t.Errorf("Config missing required property: %s", prop)
		}
	}
}

// TestReportGenerator_GenerateReport tests report generation
func TestReportGenerator_GenerateReport(t *testing.T) {
	rg := NewReportGeneratorWithStyle(nil)

	datasets := []*Dataset{
		{
			Name: "top_patterns",
			Data: []map[string]interface{}{
				{"pattern": "A→B→C", "frequency": 100},
				{"pattern": "A→C", "frequency": 80},
			},
			Schema: map[string]string{
				"pattern":   "string",
				"frequency": "int",
			},
			Source:   "stage-9-npath-full-results",
			RowCount: 2,
			Metadata: map[string]interface{}{
				"total": 10000,
			},
		},
	}

	report, err := rg.GenerateReport(context.Background(), datasets, "Test Report", "Test Summary")
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}

	if report.Title != "Test Report" {
		t.Errorf("Title = %s, want Test Report", report.Title)
	}
	if report.Summary != "Test Summary" {
		t.Errorf("Summary = %s, want Test Summary", report.Summary)
	}
	if len(report.Visualizations) != 1 {
		t.Errorf("Visualizations count = %d, want 1", len(report.Visualizations))
	}
	if report.Metadata.RowsSource != 10000 {
		t.Errorf("RowsSource = %d, want 10000", report.Metadata.RowsSource)
	}
	if report.Metadata.RowsReduced != 2 {
		t.Errorf("RowsReduced = %d, want 2", report.Metadata.RowsReduced)
	}
	if report.Metadata.Reduction < 99 {
		t.Errorf("Reduction = %.2f%%, want >= 99%%", report.Metadata.Reduction)
	}
}

// TestReportGenerator_ExportHTML tests HTML export
func TestReportGenerator_ExportHTML(t *testing.T) {
	rg := NewReportGeneratorWithStyle(nil)

	report := &Report{
		Title:   "Test Report",
		Summary: "This is a test",
		Visualizations: []Visualization{
			{
				Type:          ChartTypeBar,
				Title:         "Test Chart",
				Description:   "Test description",
				EChartsConfig: `{"type":"bar"}`,
				Insight:       "Test insight",
				DataPoints:    10,
			},
		},
		GeneratedAt: "2025-11-25T10:00:00Z",
		Metadata: ReportMetadata{
			DataSource:  "test",
			RowsSource:  1000,
			RowsReduced: 10,
			Reduction:   99.0,
		},
	}

	html, err := rg.ExportHTML(report)
	if err != nil {
		t.Fatalf("ExportHTML failed: %v", err)
	}

	// Verify HTML structure
	requiredElements := []string{
		"<!DOCTYPE html>",
		"<html",
		"<head>",
		"<body>",
		"Test Report",
		"This is a test",
		"Test Chart",
		"echarts",
		"</html>",
	}

	for _, elem := range requiredElements {
		if !strings.Contains(html, elem) {
			t.Errorf("HTML missing required element: %s", elem)
		}
	}
}

// TestStyleGuideClient_FetchStyleWithFallback tests style fetching with fallback
func TestStyleGuideClient_FetchStyleWithFallback(t *testing.T) {
	// Test with no endpoint (should use defaults)
	sgc := NewStyleGuideClient("")
	style := sgc.FetchStyleWithFallback(context.Background(), "dark")

	if style == nil {
		t.Fatal("FetchStyleWithFallback returned nil")
	}
	if style.ColorPrimary == "" {
		t.Error("ColorPrimary should not be empty")
	}
	if style.FontFamily == "" {
		t.Error("FontFamily should not be empty")
	}
}

// TestStyleConfig_Validation tests style config validation
func TestStyleConfig_Validation(t *testing.T) {
	tests := []struct {
		name        string
		style       *StyleConfig
		expectError bool
	}{
		{
			name:        "nil style",
			style:       nil,
			expectError: true,
		},
		{
			name: "valid style",
			style: &StyleConfig{
				ColorPrimary:      "#f37021",
				FontFamily:        "IBM Plex Mono",
				AnimationDuration: 1500,
			},
			expectError: false,
		},
		{
			name: "missing color primary",
			style: &StyleConfig{
				FontFamily:        "IBM Plex Mono",
				AnimationDuration: 1500,
			},
			expectError: true,
		},
		{
			name: "invalid animation duration",
			style: &StyleConfig{
				ColorPrimary:      "#f37021",
				FontFamily:        "IBM Plex Mono",
				AnimationDuration: 0,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateStyle(tt.style)
			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

// TestParseDataFromPresentationToolResult tests parsing presentation tool results
func TestParseDataFromPresentationToolResult(t *testing.T) {
	result := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{
				"pattern":   "A→B→C",
				"frequency": float64(100),
			},
			map[string]interface{}{
				"pattern":   "A→C",
				"frequency": float64(80),
			},
		},
		"source_key": "stage-9-npath-full-results",
		"total":      10000,
	}

	ds, err := ParseDataFromPresentationToolResult(result, "top_patterns")
	if err != nil {
		t.Fatalf("ParseDataFromPresentationToolResult failed: %v", err)
	}

	if ds.Name != "top_patterns" {
		t.Errorf("Name = %s, want top_patterns", ds.Name)
	}
	if ds.RowCount != 2 {
		t.Errorf("RowCount = %d, want 2", ds.RowCount)
	}
	if ds.Source != "stage-9-npath-full-results" {
		t.Errorf("Source = %s, want stage-9-npath-full-results", ds.Source)
	}
	if len(ds.Data) != 2 {
		t.Errorf("Data length = %d, want 2", len(ds.Data))
	}
}

// TestConcurrentAccess tests thread safety with -race detector
func TestConcurrentAccess(t *testing.T) {
	cs := NewChartSelector(nil)
	eg := NewEChartsGenerator(nil)
	rg := NewReportGeneratorWithStyle(nil)

	dataset := &Dataset{
		Name: "test",
		Data: []map[string]interface{}{
			{"pattern": "A", "frequency": 100},
		},
		Schema: map[string]string{
			"pattern":   "string",
			"frequency": "int",
		},
		RowCount: 1,
	}

	// Run concurrent operations
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Chart selection
			rec := cs.RecommendChart(dataset)
			if rec == nil {
				t.Error("RecommendChart returned nil")
				return
			}

			// ECharts generation
			_, err := eg.Generate(dataset, rec)
			if err != nil {
				t.Errorf("Generate failed: %v", err)
				return
			}

			// Report generation
			_, err = rg.GenerateReport(context.Background(), []*Dataset{dataset}, "Test", "Summary")
			if err != nil {
				t.Errorf("GenerateReport failed: %v", err)
			}
		}()
	}

	wg.Wait()
}

// TestDefaultStyleConfig tests default style configuration
func TestDefaultStyleConfig(t *testing.T) {
	style := DefaultStyleConfig()

	if style.ColorPrimary != "#f37021" {
		t.Errorf("ColorPrimary = %s, want #f37021", style.ColorPrimary)
	}
	if style.FontFamily != "IBM Plex Mono, monospace" {
		t.Errorf("FontFamily = %s, want IBM Plex Mono, monospace", style.FontFamily)
	}
	if style.AnimationDuration != 1500 {
		t.Errorf("AnimationDuration = %d, want 1500", style.AnimationDuration)
	}
	if len(style.ColorPalette) == 0 {
		t.Error("ColorPalette should not be empty")
	}
}

// TestMergeStyles tests style merging
func TestMergeStyles(t *testing.T) {
	defaults := DefaultStyleConfig()
	custom := &StyleConfig{
		ColorPrimary:      "#ff0000",
		AnimationDuration: 2000,
	}

	merged := MergeStyles(custom, defaults)

	// Custom values should override
	if merged.ColorPrimary != "#ff0000" {
		t.Errorf("ColorPrimary = %s, want #ff0000", merged.ColorPrimary)
	}
	if merged.AnimationDuration != 2000 {
		t.Errorf("AnimationDuration = %d, want 2000", merged.AnimationDuration)
	}

	// Default values should be preserved for non-overridden fields
	if merged.FontFamily != defaults.FontFamily {
		t.Errorf("FontFamily = %s, want %s", merged.FontFamily, defaults.FontFamily)
	}
}

// TestGetThemeVariant tests theme variant generation
func TestGetThemeVariant(t *testing.T) {
	tests := []struct {
		variant         string
		expectedPrimary string
	}{
		{"dark", "#f37021"},
		{"light", "#f37021"},
		{"teradata", "#f37021"},
		{"minimal", "#6b7280"},
	}

	for _, tt := range tests {
		t.Run(tt.variant, func(t *testing.T) {
			style := GetThemeVariant(tt.variant)
			if style.ColorPrimary != tt.expectedPrimary {
				t.Errorf("ColorPrimary = %s, want %s", style.ColorPrimary, tt.expectedPrimary)
			}
		})
	}
}

// TestEChartsGenerator_GenerateRadarChart tests radar chart generation
func TestEChartsGenerator_GenerateRadarChart(t *testing.T) {
	gen := NewEChartsGenerator(nil)

	dataset := &Dataset{
		Name: "skill_assessment",
		Data: []map[string]interface{}{
			{"name": "Developer A", "coding": 90, "design": 75, "communication": 85},
			{"name": "Developer B", "coding": 80, "design": 90, "communication": 70},
		},
		Schema: map[string]string{
			"name":          "string",
			"coding":        "int",
			"design":        "int",
			"communication": "int",
		},
		RowCount: 2,
	}

	rec := &ChartRecommendation{
		ChartType: ChartTypeRadar,
		Title:     "Skill Assessment",
	}

	config, err := gen.Generate(dataset, rec)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(config, "radar") {
		t.Error("Config should contain 'radar' type")
	}
	if !strings.Contains(config, "indicator") {
		t.Error("Config should contain 'indicator' for radar axes")
	}
}

// TestEChartsGenerator_GenerateBoxPlotChart tests box plot generation
func TestEChartsGenerator_GenerateBoxPlotChart(t *testing.T) {
	gen := NewEChartsGenerator(nil)

	dataset := &Dataset{
		Name: "price_distribution",
		Data: []map[string]interface{}{
			{"category": "Product A", "min": 10.0, "q1": 20.0, "median": 30.0, "q3": 40.0, "max": 50.0},
			{"category": "Product B", "min": 15.0, "q1": 25.0, "median": 35.0, "q3": 45.0, "max": 55.0},
		},
		Schema: map[string]string{
			"category": "string",
			"min":      "float",
			"q1":       "float",
			"median":   "float",
			"q3":       "float",
			"max":      "float",
		},
		RowCount: 2,
	}

	rec := &ChartRecommendation{
		ChartType: ChartTypeBoxPlot,
		Title:     "Price Distribution",
	}

	config, err := gen.Generate(dataset, rec)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(config, "boxplot") {
		t.Error("Config should contain 'boxplot' type")
	}
	if !strings.Contains(config, "Product A") {
		t.Error("Config should contain category names")
	}
}

// TestEChartsGenerator_GenerateTreeMapChart tests treemap generation
func TestEChartsGenerator_GenerateTreeMapChart(t *testing.T) {
	gen := NewEChartsGenerator(nil)

	dataset := &Dataset{
		Name: "disk_usage",
		Data: []map[string]interface{}{
			{"name": "/home", "value": 5000},
			{"name": "/usr", "value": 3000},
			{"name": "/var", "value": 2000},
		},
		Schema: map[string]string{
			"name":  "string",
			"value": "int",
		},
		RowCount: 3,
	}

	rec := &ChartRecommendation{
		ChartType: ChartTypeTreeMap,
		Title:     "Disk Usage",
	}

	config, err := gen.Generate(dataset, rec)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(config, "treemap") {
		t.Error("Config should contain 'treemap' type")
	}
	if !strings.Contains(config, "/home") {
		t.Error("Config should contain item names")
	}
}

// TestEChartsGenerator_GenerateGraphChart tests graph/network generation
func TestEChartsGenerator_GenerateGraphChart(t *testing.T) {
	gen := NewEChartsGenerator(nil)

	dataset := &Dataset{
		Name: "service_dependencies",
		Data: []map[string]interface{}{
			{"source": "service-a", "target": "service-b", "value": 100},
			{"source": "service-b", "target": "service-c", "value": 80},
			{"source": "service-a", "target": "service-c", "value": 50},
		},
		Schema: map[string]string{
			"source": "string",
			"target": "string",
			"value":  "int",
		},
		RowCount: 3,
	}

	rec := &ChartRecommendation{
		ChartType: ChartTypeGraph,
		Title:     "Service Dependencies",
	}

	config, err := gen.Generate(dataset, rec)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !strings.Contains(config, "graph") {
		t.Error("Config should contain 'graph' type")
	}
	if !strings.Contains(config, "force") {
		t.Error("Config should contain 'force' layout")
	}
	if !strings.Contains(config, "service-a") {
		t.Error("Config should contain node names")
	}
}

// TestChartSelector_RecommendRadar tests radar chart recommendation
func TestChartSelector_RecommendRadar(t *testing.T) {
	cs := NewChartSelector(nil)

	// More than 7 items to avoid pie chart recommendation
	dataset := &Dataset{
		Name: "performance_metrics",
		Data: []map[string]interface{}{
			{"team": "Team A", "speed": 90, "quality": 85, "cost": 70, "innovation": 95},
			{"team": "Team B", "speed": 80, "quality": 90, "cost": 85, "innovation": 75},
			{"team": "Team C", "speed": 85, "quality": 80, "cost": 75, "innovation": 88},
			{"team": "Team D", "speed": 92, "quality": 88, "cost": 78, "innovation": 82},
			{"team": "Team E", "speed": 78, "quality": 92, "cost": 88, "innovation": 79},
			{"team": "Team F", "speed": 88, "quality": 78, "cost": 82, "innovation": 91},
			{"team": "Team G", "speed": 83, "quality": 85, "cost": 80, "innovation": 86},
			{"team": "Team H", "speed": 87, "quality": 83, "cost": 77, "innovation": 84},
		},
		Schema: map[string]string{
			"team":       "string",
			"speed":      "int",
			"quality":    "int",
			"cost":       "int",
			"innovation": "int",
		},
		RowCount: 8,
	}

	rec := cs.RecommendChart(dataset)
	if rec.ChartType != ChartTypeRadar {
		t.Errorf("Expected Radar chart, got %s (rationale: %s)", rec.ChartType, rec.Rationale)
	}
	if rec.Confidence < 0.8 {
		t.Errorf("Confidence too low: %f", rec.Confidence)
	}
}

// TestChartSelector_RecommendBoxPlot tests box plot recommendation
func TestChartSelector_RecommendBoxPlot(t *testing.T) {
	cs := NewChartSelector(nil)

	dataset := &Dataset{
		Name: "sales_distribution",
		Data: []map[string]interface{}{
			{"category": "Region A", "min": 100, "q1": 200, "median": 300, "q3": 400, "max": 500},
		},
		Schema: map[string]string{
			"category": "string",
			"min":      "int",
			"q1":       "int",
			"median":   "int",
			"q3":       "int",
			"max":      "int",
		},
		RowCount: 1,
	}

	rec := cs.RecommendChart(dataset)
	if rec.ChartType != ChartTypeBoxPlot {
		t.Errorf("Expected BoxPlot chart, got %s", rec.ChartType)
	}
	if rec.Confidence < 0.85 {
		t.Errorf("Confidence too low: %f", rec.Confidence)
	}
}

// TestChartSelector_RecommendGraph tests graph chart recommendation
func TestChartSelector_RecommendGraph(t *testing.T) {
	cs := NewChartSelector(nil)

	dataset := &Dataset{
		Name: "network_topology",
		Data: []map[string]interface{}{
			{"source": "node1", "target": "node2", "value": 10},
			{"source": "node2", "target": "node3", "value": 20},
			{"source": "node3", "target": "node4", "value": 15},
			{"source": "node1", "target": "node4", "value": 25},
			{"source": "node4", "target": "node5", "value": 30},
		},
		Schema: map[string]string{
			"source": "string",
			"target": "string",
			"value":  "int",
		},
		RowCount: 5,
	}

	rec := cs.RecommendChart(dataset)
	if rec.ChartType != ChartTypeGraph {
		t.Errorf("Expected Graph chart, got %s (rationale: %s)", rec.ChartType, rec.Rationale)
	}
	if rec.Confidence < 0.85 {
		t.Errorf("Confidence too low: %f", rec.Confidence)
	}
}

// TestChartSelector_RecommendTreeMap tests treemap recommendation
func TestChartSelector_RecommendTreeMap(t *testing.T) {
	cs := NewChartSelector(nil)

	dataset := &Dataset{
		Name: "file_system",
		Data: []map[string]interface{}{
			{"name": "folder1", "parent": "root", "value": 1000},
			{"name": "folder2", "parent": "root", "value": 2000},
			{"name": "folder3", "parent": "folder1", "value": 500},
			{"name": "folder4", "parent": "folder1", "value": 300},
			{"name": "folder5", "parent": "folder2", "value": 800},
			{"name": "folder6", "parent": "folder2", "value": 700},
			{"name": "folder7", "parent": "folder3", "value": 200},
			{"name": "folder8", "parent": "folder3", "value": 150},
		},
		Schema: map[string]string{
			"name":   "string",
			"parent": "string",
			"value":  "int",
		},
		RowCount: 8,
	}

	rec := cs.RecommendChart(dataset)
	if rec.ChartType != ChartTypeTreeMap {
		t.Errorf("Expected TreeMap chart, got %s (rationale: %s)", rec.ChartType, rec.Rationale)
	}
	if rec.Confidence < 0.75 {
		t.Errorf("Confidence too low: %f", rec.Confidence)
	}
}

// TestHelperFunctions tests the new helper functions
func TestHelperFunctions(t *testing.T) {
	t.Run("hasSourceTargetFields", func(t *testing.T) {
		row1 := map[string]interface{}{"source": "a", "target": "b"}
		if !hasSourceTargetFields(row1) {
			t.Error("Should detect source-target fields")
		}

		row2 := map[string]interface{}{"from": "a", "to": "b"}
		if hasSourceTargetFields(row2) {
			t.Error("Should not detect source-target when fields are different")
		}
	})

	t.Run("hasBoxPlotFields", func(t *testing.T) {
		row1 := map[string]interface{}{"min": 1, "q1": 2, "median": 3, "q3": 4, "max": 5}
		if !hasBoxPlotFields(row1) {
			t.Error("Should detect box plot fields")
		}

		row2 := map[string]interface{}{"min": 1, "max": 5}
		if hasBoxPlotFields(row2) {
			t.Error("Should require at least 3 fields")
		}
	})

	t.Run("hasHierarchicalStructure", func(t *testing.T) {
		row1 := map[string]interface{}{"name": "item", "parent": "root"}
		if !hasHierarchicalStructure(row1) {
			t.Error("Should detect hierarchical structure")
		}

		row2 := map[string]interface{}{"name": "item", "value": 100}
		if hasHierarchicalStructure(row2) {
			t.Error("Should not detect hierarchical structure")
		}
	})
}

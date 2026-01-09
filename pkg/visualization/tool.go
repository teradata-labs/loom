// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package visualization

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// VisualizationTool provides visualization capabilities for agents
type VisualizationTool struct {
	reportGen *ReportGenerator
}

// NewVisualizationTool creates a new visualization tool
func NewVisualizationTool() *VisualizationTool {
	return &VisualizationTool{
		reportGen: NewReportGeneratorWithStyle(nil),
	}
}

func (t *VisualizationTool) Name() string {
	return "generate_visualization"
}

func (t *VisualizationTool) Description() string {
	return `Generates interactive HTML reports with charts from presentation tool results.

This tool transforms aggregated data into beautiful visualizations:
- Automatically selects appropriate chart types (bar, pie, line, scatter)
- Applies Hawk StyleGuide aesthetic (Teradata Orange, IBM Plex Mono)
- Generates self-contained HTML with embedded ECharts
- Includes AI-generated insights per chart

Use this after using presentation tools (top_n_query, group_by_query) to
create visual reports from aggregated data.

Example workflow:
1. Stage 9: Execute queries → 10,000 rows
2. Stage 10: Use top_n_query → 50 rows (99.5% reduction)
3. Stage 11: Use generate_visualization → Interactive HTML report`
}

func (t *VisualizationTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for visualization generation",
		map[string]*shuttle.JSONSchema{
			"datasets": shuttle.NewArraySchema(
				"Array of presentation tool results to visualize (required)",
				shuttle.NewObjectSchema(
					"Dataset from presentation tool",
					map[string]*shuttle.JSONSchema{
						"name": shuttle.NewStringSchema("Dataset name (e.g., 'top_50_patterns')"),
						"data": shuttle.NewStringSchema("JSON string of presentation tool result"),
					},
					[]string{"name", "data"},
				),
			),
			"title":       shuttle.NewStringSchema("Report title (required)"),
			"summary":     shuttle.NewStringSchema("Executive summary (required)"),
			"output_path": shuttle.NewStringSchema("Path to save HTML file (required, e.g., '/tmp/report.html')"),
			"theme": shuttle.NewStringSchema("Theme variant: 'dark', 'light', 'teradata', 'minimal' (default: 'dark')").
				WithEnum("dark", "light", "teradata", "minimal").
				WithDefault("dark"),
		},
		[]string{"datasets", "title", "summary", "output_path"},
	)
}

func (t *VisualizationTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Extract parameters
	datasetsRaw, ok := params["datasets"].([]interface{})
	if !ok || len(datasetsRaw) == 0 {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "datasets is required and must be a non-empty array",
				Suggestion: "Provide array of presentation tool results with 'name' and 'data' fields",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	title, ok := params["title"].(string)
	if !ok || title == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "title is required",
				Suggestion: "Provide a descriptive title for the report",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	summary, ok := params["summary"].(string)
	if !ok || summary == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "summary is required",
				Suggestion: "Provide an executive summary for the report",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	outputPath, ok := params["output_path"].(string)
	if !ok || outputPath == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "output_path is required",
				Suggestion: "Provide path to save HTML file (e.g., '/tmp/report.html')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	theme := "dark"
	if themeVal, ok := params["theme"].(string); ok && themeVal != "" {
		theme = themeVal
	}

	// Parse datasets
	datasets := make([]*Dataset, 0, len(datasetsRaw))
	for i, dsRaw := range datasetsRaw {
		dsMap, ok := dsRaw.(map[string]interface{})
		if !ok {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "INVALID_DATASET",
					Message: fmt.Sprintf("Dataset %d is not a valid object", i),
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}

		name, ok := dsMap["name"].(string)
		if !ok || name == "" {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "INVALID_DATASET",
					Message: fmt.Sprintf("Dataset %d missing 'name' field", i),
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}

		dataStr, ok := dsMap["data"].(string)
		if !ok || dataStr == "" {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "INVALID_DATASET",
					Message: fmt.Sprintf("Dataset %d missing 'data' field", i),
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}

		// Parse JSON data
		var dataMap map[string]interface{}
		if err := json.Unmarshal([]byte(dataStr), &dataMap); err != nil {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "INVALID_JSON",
					Message: fmt.Sprintf("Dataset %d has invalid JSON: %v", i, err),
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}

		// Parse into Dataset
		ds, err := ParseDataFromPresentationToolResult(dataMap, name)
		if err != nil {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "PARSE_FAILED",
					Message: fmt.Sprintf("Failed to parse dataset %d: %v", i, err),
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}

		datasets = append(datasets, ds)
	}

	// Apply theme
	style := GetThemeVariant(theme)
	t.reportGen = NewReportGeneratorWithStyle(style)

	// Generate report
	report, err := t.reportGen.GenerateReport(ctx, datasets, title, summary)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "GENERATION_FAILED",
				Message:    fmt.Sprintf("Failed to generate report: %v", err),
				Retryable:  true,
				Suggestion: "Check dataset structure and try again",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Export to HTML
	html, err := t.reportGen.ExportHTML(report)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "EXPORT_FAILED",
				Message:    fmt.Sprintf("Failed to export HTML: %v", err),
				Retryable:  true,
				Suggestion: "Report generated but HTML export failed",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Save to file
	if err := os.WriteFile(outputPath, []byte(html), 0600); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "WRITE_FAILED",
				Message:    fmt.Sprintf("Failed to write file: %v", err),
				Retryable:  true,
				Suggestion: "Check output path permissions and disk space",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Build success result
	result := map[string]interface{}{
		"output_path":     outputPath,
		"file_size_bytes": len(html),
		"visualizations":  len(report.Visualizations),
		"theme":           theme,
		"reduction_pct":   report.Metadata.Reduction,
		"rows_source":     report.Metadata.RowsSource,
		"rows_reduced":    report.Metadata.RowsReduced,
		"chart_types":     extractChartTypes(report),
	}

	return &shuttle.Result{
		Success: true,
		Data:    result,
		Metadata: map[string]interface{}{
			"output_path":    outputPath,
			"visualizations": len(report.Visualizations),
			"theme":          theme,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *VisualizationTool) Backend() string {
	return "" // Backend-agnostic
}

// extractChartTypes extracts chart type names from report
func extractChartTypes(report *Report) []string {
	types := make([]string, 0, len(report.Visualizations))
	for _, viz := range report.Visualizations {
		types = append(types, string(viz.Type))
	}
	return types
}

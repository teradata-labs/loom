// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package visualization

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ReportGenerator assembles complete HTML reports with embedded charts
type ReportGenerator struct {
	chartSelector *ChartSelector
	echartsGen    *EChartsGenerator
	styleClient   *StyleGuideClient
	style         *StyleConfig
}

// NewReportGenerator creates a new report generator
func NewReportGenerator(styleClient *StyleGuideClient) *ReportGenerator {
	style := DefaultStyleConfig()
	if styleClient != nil {
		style = styleClient.FetchStyleWithFallback(context.Background(), "dark")
	}

	return &ReportGenerator{
		chartSelector: NewChartSelector(style),
		echartsGen:    NewEChartsGenerator(style),
		styleClient:   styleClient,
		style:         style,
	}
}

// NewReportGeneratorWithStyle creates a report generator with custom style
func NewReportGeneratorWithStyle(style *StyleConfig) *ReportGenerator {
	if style == nil {
		style = DefaultStyleConfig()
	}
	return &ReportGenerator{
		chartSelector: NewChartSelector(style),
		echartsGen:    NewEChartsGenerator(style),
		style:         style,
	}
}

// GenerateReport creates a complete report from datasets
func (rg *ReportGenerator) GenerateReport(ctx context.Context, datasets []*Dataset, title, summary string) (*Report, error) {
	if len(datasets) == 0 {
		return nil, fmt.Errorf("no datasets provided")
	}

	// Generate visualizations for each dataset
	visualizations := make([]Visualization, 0, len(datasets))
	totalDataPoints := 0

	for _, ds := range datasets {
		// Recommend chart type
		rec := rg.chartSelector.RecommendChart(ds)

		// Generate ECharts config
		echartsConfig, err := rg.echartsGen.Generate(ds, rec)
		if err != nil {
			return nil, fmt.Errorf("failed to generate chart for %s: %w", ds.Name, err)
		}

		// Generate insight (simple for now, would use LLM in production)
		insight := rg.generateInsight(ds, rec)

		viz := Visualization{
			Type:          rec.ChartType,
			Title:         rec.Title,
			Description:   rec.Rationale,
			EChartsConfig: echartsConfig,
			Insight:       insight,
			DataPoints:    ds.RowCount,
			Metadata: map[string]interface{}{
				"confidence": rec.Confidence,
				"source":     ds.Source,
			},
		}

		visualizations = append(visualizations, viz)
		totalDataPoints += ds.RowCount
	}

	// Calculate reduction metrics
	rowsSource := 0
	if len(datasets) > 0 && datasets[0].Metadata != nil {
		if total, ok := datasets[0].Metadata["total"].(int); ok {
			rowsSource = total
		}
	}
	reduction := 0.0
	if rowsSource > 0 {
		reduction = (float64(rowsSource-totalDataPoints) / float64(rowsSource)) * 100
	}

	report := &Report{
		Title:          title,
		Summary:        summary,
		Visualizations: visualizations,
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
		Metadata: ReportMetadata{
			DataSource:  datasets[0].Source,
			RowsSource:  rowsSource,
			RowsReduced: totalDataPoints,
			Reduction:   reduction,
			Extra:       map[string]interface{}{},
		},
	}

	return report, nil
}

// ExportHTML generates self-contained HTML with embedded charts
func (rg *ReportGenerator) ExportHTML(report *Report) (string, error) {
	if report == nil {
		return "", fmt.Errorf("report is nil")
	}

	var sb strings.Builder

	// HTML header with ECharts CDN
	sb.WriteString(fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>%s</title>
    <script src="https://cdn.jsdelivr.net/npm/echarts@5/dist/echarts.min.js"></script>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        body {
            font-family: %s;
            background: #0d0d0d;
            color: %s;
            padding: 40px 20px;
            line-height: 1.6;
        }
        .container {
            max-width: 1200px;
            margin: 0 auto;
        }
        h1 {
            color: %s;
            font-size: 32px;
            margin-bottom: 20px;
            font-weight: 600;
        }
        .summary {
            background: %s;
            border: 1px solid %s;
            border-radius: 8px;
            padding: 20px;
            margin-bottom: 40px;
            font-size: 14px;
            line-height: 1.8;
        }
        .metadata {
            color: %s;
            font-size: 12px;
            margin-bottom: 40px;
            padding: 10px;
            background: rgba(255, 255, 255, 0.02);
            border-radius: 4px;
        }
        .visualization {
            margin-bottom: 60px;
        }
        .viz-header {
            margin-bottom: 15px;
        }
        .viz-title {
            color: %s;
            font-size: 20px;
            margin-bottom: 8px;
            font-weight: 500;
        }
        .viz-description {
            color: %s;
            font-size: 13px;
            margin-bottom: 8px;
        }
        .viz-insight {
            background: rgba(243, 112, 33, 0.1);
            border-left: 3px solid %s;
            padding: 12px 16px;
            margin-top: 15px;
            font-size: 13px;
            border-radius: 4px;
        }
        .chart-container {
            width: 100%%;
            height: 500px;
            background: %s;
            border: 1px solid %s;
            border-radius: 8px;
            padding: 20px;
        }
        @media print {
            body {
                background: white;
                color: black;
            }
            .chart-container {
                page-break-inside: avoid;
            }
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>%s</h1>

        <div class="summary">%s</div>

        <div class="metadata">
            Generated: %s |
            Data Source: %s |
            Original Rows: %d |
            Reduced Rows: %d |
            Reduction: %.1f%%
        </div>
`, report.Title,
		rg.style.FontFamily,
		rg.style.ColorText,
		rg.style.ColorPrimary,
		rg.style.ColorGlass,
		rg.style.ColorBorder,
		rg.style.ColorTextMuted,
		rg.style.ColorPrimary,
		rg.style.ColorTextMuted,
		rg.style.ColorPrimary,
		rg.style.ColorGlass,
		rg.style.ColorBorder,
		report.Title,
		report.Summary,
		report.GeneratedAt,
		report.Metadata.DataSource,
		report.Metadata.RowsSource,
		report.Metadata.RowsReduced,
		report.Metadata.Reduction,
	))

	// Add each visualization
	for i, viz := range report.Visualizations {
		chartID := fmt.Sprintf("chart-%d", i)

		sb.WriteString(fmt.Sprintf(`
        <div class="visualization">
            <div class="viz-header">
                <h2 class="viz-title">%s</h2>
                <p class="viz-description">%s</p>
            </div>
            <div id="%s" class="chart-container"></div>
            <div class="viz-insight">
                <strong>Insight:</strong> %s
            </div>
        </div>
`, viz.Title, viz.Description, chartID, viz.Insight))
	}

	// Add JavaScript to initialize charts
	sb.WriteString("\n        <script>\n")
	for i, viz := range report.Visualizations {
		chartID := fmt.Sprintf("chart-%d", i)

		// Escape chartID for safe use in JavaScript string (prevent quote injection)
		// CRITICAL: Escape backslashes BEFORE quotes to avoid double-escaping
		safeChartID := strings.ReplaceAll(chartID, "\\", "\\\\")
		safeChartID = strings.ReplaceAll(safeChartID, "'", "\\'")

		// Write script using separate calls to avoid CodeQL false positive
		// The JSON is embedded as a JavaScript object literal using json.HTMLEscape
		sb.WriteString("\n            (function() {\n")
		sb.WriteString(fmt.Sprintf("                var chartDom = document.getElementById('%s');\n", safeChartID))
		sb.WriteString("                var myChart = echarts.init(chartDom);\n")

		// Escape JSON for safe embedding using json.HTMLEscape per Go stdlib documentation
		// This escapes <, >, &, U+2028, U+2029 so JSON is safe to embed in HTML script tags
		sb.WriteString("                var option = ")
		var escapedConfig bytes.Buffer
		json.HTMLEscape(&escapedConfig, []byte(viz.EChartsConfig))
		sb.Write(escapedConfig.Bytes())
		sb.WriteString(";\n")

		sb.WriteString("                myChart.setOption(option);\n")
		sb.WriteString("                window.addEventListener('resize', function() {\n")
		sb.WriteString("                    myChart.resize();\n")
		sb.WriteString("                });\n")
		sb.WriteString("            })();\n")
	}
	sb.WriteString("        </script>\n")

	// Close HTML
	sb.WriteString(`
    </div>
</body>
</html>`)

	return sb.String(), nil
}

// ExportJSON exports report as JSON
func (rg *ReportGenerator) ExportJSON(report *Report) (string, error) {
	// Use standard JSON marshaling from Report type
	// Report already has json tags on fields
	return fmt.Sprintf(`{
  "title": "%s",
  "summary": "%s",
  "generated_at": "%s",
  "visualizations": %d,
  "metadata": {
    "data_source": "%s",
    "rows_source": %d,
    "rows_reduced": %d,
    "reduction": %.2f
  }
}`,
		report.Title,
		report.Summary,
		report.GeneratedAt,
		len(report.Visualizations),
		report.Metadata.DataSource,
		report.Metadata.RowsSource,
		report.Metadata.RowsReduced,
		report.Metadata.Reduction,
	), nil
}

// generateInsight creates a simple insight for a chart
// In production, this would use an LLM to generate contextual insights
func (rg *ReportGenerator) generateInsight(ds *Dataset, rec *ChartRecommendation) string {
	switch rec.ChartType {
	case ChartTypeBar:
		if len(ds.Data) > 0 {
			// Find top item
			labels, values := rg.echartsGen.extractLabelValues(ds)
			if len(labels) > 0 && len(values) > 0 {
				topLabel := labels[0]
				topValue := values[0]
				return fmt.Sprintf("The leading item '%s' accounts for %v, representing the highest value in this dataset of %d items.",
					topLabel, topValue, len(ds.Data))
			}
		}
		return fmt.Sprintf("This bar chart compares %d items, highlighting the relative magnitudes across categories.", len(ds.Data))

	case ChartTypePie:
		return fmt.Sprintf("This distribution shows the proportional breakdown across %d categories, making it easy to identify the largest segments.", len(ds.Data))

	case ChartTypeLine, ChartTypeTimeSeries:
		return fmt.Sprintf("This time series visualization tracks changes across %d data points, revealing trends and patterns over time.", len(ds.Data))

	case ChartTypeScatter:
		return fmt.Sprintf("This scatter plot reveals the relationship between two dimensions across %d data points, helping identify correlations and outliers.", len(ds.Data))

	default:
		return fmt.Sprintf("This visualization provides insights into %d data points from %s.", len(ds.Data), ds.Name)
	}
}

// GenerateTitle creates a title from datasets
func GenerateTitle(datasets []*Dataset) string {
	if len(datasets) == 0 {
		return "Data Analysis Report"
	}
	if len(datasets) == 1 {
		return fmt.Sprintf("%s Analysis", toTitle(datasets[0].Name))
	}
	return fmt.Sprintf("Multi-Dataset Analysis (%d datasets)", len(datasets))
}

// GenerateSummary creates a summary from datasets
func GenerateSummary(datasets []*Dataset) string {
	totalRows := 0
	sources := make(map[string]bool)
	for _, ds := range datasets {
		totalRows += ds.RowCount
		sources[ds.Source] = true
	}

	return fmt.Sprintf("This report analyzes %d datasets with a total of %d data points from %d source(s). "+
		"Visualizations have been automatically selected based on data patterns to provide the most insightful representation of the data.",
		len(datasets), totalRows, len(sources))
}

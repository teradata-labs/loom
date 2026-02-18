// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package visualization

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// EChartsGenerator generates ECharts configurations with Hawk StyleGuide
type EChartsGenerator struct {
	style *StyleConfig
}

// NewEChartsGenerator creates a new ECharts config generator
func NewEChartsGenerator(style *StyleConfig) *EChartsGenerator {
	if style == nil {
		style = DefaultStyleConfig()
	}
	return &EChartsGenerator{style: style}
}

// Generate creates ECharts JSON configuration for a dataset and chart type
func (eg *EChartsGenerator) Generate(ds *Dataset, rec *ChartRecommendation) (string, error) {
	var config map[string]interface{}
	var err error

	switch rec.ChartType {
	case ChartTypeBar:
		config = eg.generateBarChart(ds, rec)
	case ChartTypeLine, ChartTypeTimeSeries:
		config = eg.generateLineChart(ds, rec)
	case ChartTypePie:
		config = eg.generatePieChart(ds, rec)
	case ChartTypeScatter:
		config = eg.generateScatterChart(ds, rec)
	case ChartTypeRadar:
		config = eg.generateRadarChart(ds, rec)
	case ChartTypeBoxPlot:
		config = eg.generateBoxPlotChart(ds, rec)
	case ChartTypeTreeMap:
		config = eg.generateTreeMapChart(ds, rec)
	case ChartTypeGraph:
		config = eg.generateGraphChart(ds, rec)
	default:
		// Default to bar chart
		config = eg.generateBarChart(ds, rec)
	}

	if err != nil {
		return "", err
	}

	jsonBytes, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("failed to marshal ECharts config: %w", err)
	}

	return string(jsonBytes), nil
}

// generateBarChart creates a bar chart configuration
func (eg *EChartsGenerator) generateBarChart(ds *Dataset, rec *ChartRecommendation) map[string]interface{} {
	// Extract labels and values
	labels, values := eg.extractLabelValues(ds)

	// Sort by value if ranking
	if len(values) > 0 {
		eg.sortByValue(labels, values)
	}

	orientation := "vertical"
	if orient, ok := rec.Config["orientation"].(string); ok {
		orientation = orient
	}

	var xAxis, yAxis map[string]interface{}
	if orientation == "horizontal" {
		// Horizontal bars (categories on Y-axis)
		xAxis = map[string]interface{}{
			"type": "value",
			"axisLine": map[string]interface{}{
				"lineStyle": map[string]interface{}{
					"color": eg.style.ColorBorder,
				},
			},
			"axisLabel": eg.axisLabelStyle(),
			"splitLine": eg.splitLineStyle(),
		}
		yAxis = map[string]interface{}{
			"type": "category",
			"data": labels,
			"axisLine": map[string]interface{}{
				"lineStyle": map[string]interface{}{
					"color": eg.style.ColorBorder,
				},
			},
			"axisLabel": eg.axisLabelStyle(),
		}
	} else {
		// Vertical bars (categories on X-axis)
		xAxis = map[string]interface{}{
			"type": "category",
			"data": labels,
			"axisLine": map[string]interface{}{
				"lineStyle": map[string]interface{}{
					"color": eg.style.ColorBorder,
				},
			},
			"axisLabel": eg.axisLabelStyle(),
		}
		yAxis = map[string]interface{}{
			"type": "value",
			"axisLine": map[string]interface{}{
				"lineStyle": map[string]interface{}{
					"color": eg.style.ColorBorder,
				},
			},
			"axisLabel": eg.axisLabelStyle(),
			"splitLine": eg.splitLineStyle(),
		}
	}

	return map[string]interface{}{
		"backgroundColor":   eg.style.ColorBackground,
		"animation":         true,
		"animationDuration": eg.style.AnimationDuration,
		"animationEasing":   eg.style.AnimationEasing,
		"grid":              eg.gridConfig(),
		"tooltip":           eg.tooltipConfig(),
		"xAxis":             xAxis,
		"yAxis":             yAxis,
		"series": []interface{}{
			map[string]interface{}{
				"type": "bar",
				"data": values,
				"itemStyle": map[string]interface{}{
					"color": map[string]interface{}{
						"type": "linear",
						"x":    0,
						"y":    0,
						"x2":   0,
						"y2":   1,
						"colorStops": []interface{}{
							map[string]interface{}{
								"offset": 0,
								"color":  eg.style.ColorPrimary,
							},
							map[string]interface{}{
								"offset": 1,
								"color":  darkenColor(eg.style.ColorPrimary, 0.2),
							},
						},
					},
					"borderRadius": []int{4, 4, 0, 0},
					"shadowBlur":   eg.style.ShadowBlur,
					"shadowColor":  fmt.Sprintf("%s66", eg.style.ColorPrimary),
				},
				"emphasis": map[string]interface{}{
					"itemStyle": map[string]interface{}{
						"shadowBlur":  eg.style.ShadowBlur * 2,
						"shadowColor": fmt.Sprintf("%s99", eg.style.ColorPrimary),
					},
				},
				"label": map[string]interface{}{
					"show":       true,
					"position":   "top",
					"color":      eg.style.ColorTextMuted,
					"fontFamily": eg.style.FontFamily,
					"fontSize":   eg.style.FontSizeLabel,
				},
			},
		},
	}
}

// generateLineChart creates a line chart configuration
func (eg *EChartsGenerator) generateLineChart(ds *Dataset, rec *ChartRecommendation) map[string]interface{} {
	labels, values := eg.extractLabelValues(ds)

	return map[string]interface{}{
		"backgroundColor":   eg.style.ColorBackground,
		"animation":         true,
		"animationDuration": eg.style.AnimationDuration,
		"animationEasing":   eg.style.AnimationEasing,
		"grid":              eg.gridConfig(),
		"tooltip":           eg.tooltipConfig(),
		"xAxis": map[string]interface{}{
			"type": "category",
			"data": labels,
			"axisLine": map[string]interface{}{
				"lineStyle": map[string]interface{}{
					"color": eg.style.ColorBorder,
				},
			},
			"axisLabel": eg.axisLabelStyle(),
		},
		"yAxis": map[string]interface{}{
			"type": "value",
			"axisLine": map[string]interface{}{
				"lineStyle": map[string]interface{}{
					"color": eg.style.ColorBorder,
				},
			},
			"axisLabel": eg.axisLabelStyle(),
			"splitLine": eg.splitLineStyle(),
		},
		"series": []interface{}{
			map[string]interface{}{
				"type":   "line",
				"data":   values,
				"smooth": true,
				"lineStyle": map[string]interface{}{
					"color":       eg.style.ColorPrimary,
					"width":       2,
					"shadowBlur":  eg.style.ShadowBlur,
					"shadowColor": fmt.Sprintf("%s66", eg.style.ColorPrimary),
				},
				"itemStyle": map[string]interface{}{
					"color": eg.style.ColorPrimary,
				},
				"areaStyle": map[string]interface{}{
					"color": map[string]interface{}{
						"type": "linear",
						"x":    0,
						"y":    0,
						"x2":   0,
						"y2":   1,
						"colorStops": []interface{}{
							map[string]interface{}{
								"offset": 0,
								"color":  fmt.Sprintf("%s66", eg.style.ColorPrimary),
							},
							map[string]interface{}{
								"offset": 1,
								"color":  fmt.Sprintf("%s00", eg.style.ColorPrimary),
							},
						},
					},
				},
				"emphasis": map[string]interface{}{
					"lineStyle": map[string]interface{}{
						"shadowBlur": eg.style.ShadowBlur * 2,
					},
				},
			},
		},
	}
}

// generatePieChart creates a pie chart configuration
func (eg *EChartsGenerator) generatePieChart(ds *Dataset, rec *ChartRecommendation) map[string]interface{} {
	labels, values := eg.extractLabelValues(ds)

	// Create data array for pie chart
	var data []interface{}
	for i, label := range labels {
		if i < len(values) {
			data = append(data, map[string]interface{}{
				"name":  label,
				"value": values[i],
			})
		}
	}

	return map[string]interface{}{
		"backgroundColor":   eg.style.ColorBackground,
		"animation":         true,
		"animationDuration": eg.style.AnimationDuration,
		"animationEasing":   eg.style.AnimationEasing,
		"tooltip":           eg.tooltipConfig(),
		"legend": map[string]interface{}{
			"orient": "vertical",
			"left":   "left",
			"textStyle": map[string]interface{}{
				"color":      eg.style.ColorText,
				"fontFamily": eg.style.FontFamily,
				"fontSize":   eg.style.FontSizeLabel,
			},
		},
		"series": []interface{}{
			map[string]interface{}{
				"type":   "pie",
				"radius": "55%",
				"center": []string{"50%", "50%"},
				"data":   data,
				"emphasis": map[string]interface{}{
					"itemStyle": map[string]interface{}{
						"shadowBlur":  eg.style.ShadowBlur * 2,
						"shadowColor": fmt.Sprintf("%s99", eg.style.ColorPrimary),
					},
				},
				"label": map[string]interface{}{
					"color":      eg.style.ColorText,
					"fontFamily": eg.style.FontFamily,
					"fontSize":   eg.style.FontSizeLabel,
				},
			},
		},
	}
}

// generateScatterChart creates a scatter plot configuration
func (eg *EChartsGenerator) generateScatterChart(ds *Dataset, rec *ChartRecommendation) map[string]interface{} {
	// Extract scatter data (x, y pairs)
	scatterData := eg.extractScatterData(ds, rec)

	return map[string]interface{}{
		"backgroundColor":   eg.style.ColorBackground,
		"animation":         true,
		"animationDuration": eg.style.AnimationDuration,
		"animationEasing":   eg.style.AnimationEasing,
		"grid":              eg.gridConfig(),
		"tooltip":           eg.tooltipConfig(),
		"xAxis": map[string]interface{}{
			"type":          "value",
			"name":          getStringOrDefault(rec.Config, "x_axis", "X"),
			"nameLocation":  "middle",
			"nameGap":       30,
			"nameTextStyle": eg.nameTextStyle(),
			"axisLine": map[string]interface{}{
				"lineStyle": map[string]interface{}{
					"color": eg.style.ColorBorder,
				},
			},
			"axisLabel": eg.axisLabelStyle(),
			"splitLine": eg.splitLineStyle(),
		},
		"yAxis": map[string]interface{}{
			"type":          "value",
			"name":          getStringOrDefault(rec.Config, "y_axis", "Y"),
			"nameLocation":  "middle",
			"nameGap":       40,
			"nameTextStyle": eg.nameTextStyle(),
			"axisLine": map[string]interface{}{
				"lineStyle": map[string]interface{}{
					"color": eg.style.ColorBorder,
				},
			},
			"axisLabel": eg.axisLabelStyle(),
			"splitLine": eg.splitLineStyle(),
		},
		"series": []interface{}{
			map[string]interface{}{
				"type":       "scatter",
				"symbolSize": 14,
				"data":       scatterData,
				"itemStyle": map[string]interface{}{
					"color":       eg.style.ColorPrimary,
					"shadowBlur":  eg.style.ShadowBlur,
					"shadowColor": fmt.Sprintf("%s66", eg.style.ColorPrimary),
				},
				"emphasis": map[string]interface{}{
					"itemStyle": map[string]interface{}{
						"shadowBlur":  eg.style.ShadowBlur * 2,
						"shadowColor": fmt.Sprintf("%s99", eg.style.ColorPrimary),
					},
				},
			},
		},
	}
}

// generateRadarChart creates a radar/spider chart configuration
func (eg *EChartsGenerator) generateRadarChart(ds *Dataset, rec *ChartRecommendation) map[string]interface{} {
	// Extract multi-dimensional data for radar chart
	radarData := eg.extractRadarData(ds)

	return map[string]interface{}{
		"backgroundColor":   eg.style.ColorBackground,
		"animation":         true,
		"animationDuration": eg.style.AnimationDuration,
		"animationEasing":   eg.style.AnimationEasing,
		"tooltip":           eg.tooltipConfig(),
		"legend": map[string]interface{}{
			"data": radarData["legend"],
			"textStyle": map[string]interface{}{
				"color":      eg.style.ColorText,
				"fontFamily": eg.style.FontFamily,
				"fontSize":   eg.style.FontSizeLabel,
			},
		},
		"radar": map[string]interface{}{
			"indicator": radarData["indicators"],
			"axisName": map[string]interface{}{
				"color":      eg.style.ColorTextMuted,
				"fontFamily": eg.style.FontFamily,
				"fontSize":   eg.style.FontSizeLabel,
			},
			"splitLine": map[string]interface{}{
				"lineStyle": map[string]interface{}{
					"color": eg.style.ColorBorder,
				},
			},
			"splitArea": map[string]interface{}{
				"areaStyle": map[string]interface{}{
					"color": []string{
						fmt.Sprintf("%s0d", eg.style.ColorText),
						"transparent",
					},
				},
			},
		},
		"series": []interface{}{
			map[string]interface{}{
				"type": "radar",
				"data": radarData["series"],
				"itemStyle": map[string]interface{}{
					"color": eg.style.ColorPrimary,
				},
				"areaStyle": map[string]interface{}{
					"color": fmt.Sprintf("%s33", eg.style.ColorPrimary),
				},
				"lineStyle": map[string]interface{}{
					"color":       eg.style.ColorPrimary,
					"width":       2,
					"shadowBlur":  eg.style.ShadowBlur,
					"shadowColor": fmt.Sprintf("%s66", eg.style.ColorPrimary),
				},
			},
		},
	}
}

// generateBoxPlotChart creates a box plot configuration for statistical distribution
func (eg *EChartsGenerator) generateBoxPlotChart(ds *Dataset, rec *ChartRecommendation) map[string]interface{} {
	// Extract statistical data (min, q1, median, q3, max)
	boxData := eg.extractBoxPlotData(ds)

	return map[string]interface{}{
		"backgroundColor":   eg.style.ColorBackground,
		"animation":         true,
		"animationDuration": eg.style.AnimationDuration,
		"animationEasing":   eg.style.AnimationEasing,
		"grid":              eg.gridConfig(),
		"tooltip": map[string]interface{}{
			"trigger":         "item",
			"backgroundColor": eg.style.ColorGlass,
			"borderColor":     eg.style.ColorPrimary,
			"borderWidth":     1,
			"textStyle": map[string]interface{}{
				"color":      eg.style.ColorText,
				"fontFamily": eg.style.FontFamily,
				"fontSize":   eg.style.FontSizeTooltip,
			},
		},
		"xAxis": map[string]interface{}{
			"type": "category",
			"data": boxData["categories"],
			"axisLine": map[string]interface{}{
				"lineStyle": map[string]interface{}{
					"color": eg.style.ColorBorder,
				},
			},
			"axisLabel": eg.axisLabelStyle(),
		},
		"yAxis": map[string]interface{}{
			"type": "value",
			"axisLine": map[string]interface{}{
				"lineStyle": map[string]interface{}{
					"color": eg.style.ColorBorder,
				},
			},
			"axisLabel": eg.axisLabelStyle(),
			"splitLine": eg.splitLineStyle(),
		},
		"series": []interface{}{
			map[string]interface{}{
				"type": "boxplot",
				"data": boxData["values"],
				"itemStyle": map[string]interface{}{
					"color":       eg.style.ColorPrimary,
					"borderColor": eg.style.ColorPrimary,
					"borderWidth": 2,
					"shadowBlur":  eg.style.ShadowBlur,
					"shadowColor": fmt.Sprintf("%s66", eg.style.ColorPrimary),
				},
			},
		},
	}
}

// generateTreeMapChart creates a treemap configuration for hierarchical data
func (eg *EChartsGenerator) generateTreeMapChart(ds *Dataset, rec *ChartRecommendation) map[string]interface{} {
	// Extract hierarchical tree data
	treeData := eg.extractTreeMapData(ds)

	return map[string]interface{}{
		"backgroundColor":   eg.style.ColorBackground,
		"animation":         true,
		"animationDuration": eg.style.AnimationDuration,
		"animationEasing":   eg.style.AnimationEasing,
		"tooltip":           eg.tooltipConfig(),
		"series": []interface{}{
			map[string]interface{}{
				"type": "treemap",
				"data": treeData,
				"label": map[string]interface{}{
					"show":       true,
					"formatter":  "{b}",
					"color":      eg.style.ColorText,
					"fontFamily": eg.style.FontFamily,
					"fontSize":   eg.style.FontSizeLabel,
				},
				"itemStyle": map[string]interface{}{
					"borderColor": eg.style.ColorBorder,
					"borderWidth": 2,
					"gapWidth":    2,
				},
				"levels": []interface{}{
					map[string]interface{}{
						"itemStyle": map[string]interface{}{
							"borderWidth": 0,
							"gapWidth":    5,
						},
					},
					map[string]interface{}{
						"itemStyle": map[string]interface{}{
							"gapWidth": 1,
						},
						"colorSaturation": []float64{0.35, 0.5},
					},
					map[string]interface{}{
						"itemStyle": map[string]interface{}{
							"gapWidth":              1,
							"borderColorSaturation": 0.6,
						},
						"colorSaturation": []float64{0.35, 0.5},
					},
				},
				"breadcrumb": map[string]interface{}{
					"show": true,
					"itemStyle": map[string]interface{}{
						"color":       eg.style.ColorGlass,
						"borderColor": eg.style.ColorBorder,
						"textStyle": map[string]interface{}{
							"color":      eg.style.ColorText,
							"fontFamily": eg.style.FontFamily,
							"fontSize":   eg.style.FontSizeLabel,
						},
					},
				},
			},
		},
	}
}

// generateGraphChart creates a graph/network chart configuration
func (eg *EChartsGenerator) generateGraphChart(ds *Dataset, rec *ChartRecommendation) map[string]interface{} {
	// Extract nodes and edges for graph visualization
	graphData := eg.extractGraphData(ds)

	return map[string]interface{}{
		"backgroundColor":   eg.style.ColorBackground,
		"animation":         true,
		"animationDuration": eg.style.AnimationDuration,
		"animationEasing":   eg.style.AnimationEasing,
		"tooltip":           eg.tooltipConfig(),
		"series": []interface{}{
			map[string]interface{}{
				"type":   "graph",
				"layout": "force",
				"data":   graphData["nodes"],
				"links":  graphData["edges"],
				"roam":   true,
				"label": map[string]interface{}{
					"show":       true,
					"position":   "right",
					"formatter":  "{b}",
					"color":      eg.style.ColorText,
					"fontFamily": eg.style.FontFamily,
					"fontSize":   eg.style.FontSizeLabel,
				},
				"itemStyle": map[string]interface{}{
					"color":       eg.style.ColorPrimary,
					"shadowBlur":  eg.style.ShadowBlur,
					"shadowColor": fmt.Sprintf("%s66", eg.style.ColorPrimary),
				},
				"lineStyle": map[string]interface{}{
					"color":   eg.style.ColorBorder,
					"width":   1,
					"opacity": 0.6,
				},
				"emphasis": map[string]interface{}{
					"focus": "adjacency",
					"itemStyle": map[string]interface{}{
						"shadowBlur":  eg.style.ShadowBlur * 2,
						"shadowColor": fmt.Sprintf("%s99", eg.style.ColorPrimary),
					},
					"lineStyle": map[string]interface{}{
						"width": 3,
					},
				},
				"force": map[string]interface{}{
					"repulsion":  1000,
					"edgeLength": 100,
				},
			},
		},
	}
}

// Helper methods for common config sections

func (eg *EChartsGenerator) gridConfig() map[string]interface{} {
	return map[string]interface{}{
		"left":         "10%",
		"right":        "5%",
		"bottom":       "10%",
		"top":          "10%",
		"containLabel": true,
	}
}

func (eg *EChartsGenerator) tooltipConfig() map[string]interface{} {
	return map[string]interface{}{
		"trigger":         "item",
		"backgroundColor": eg.style.ColorGlass,
		"borderColor":     eg.style.ColorPrimary,
		"borderWidth":     1,
		"textStyle": map[string]interface{}{
			"color":      eg.style.ColorText,
			"fontFamily": eg.style.FontFamily,
			"fontSize":   eg.style.FontSizeTooltip,
		},
	}
}

func (eg *EChartsGenerator) axisLabelStyle() map[string]interface{} {
	return map[string]interface{}{
		"color":      eg.style.ColorTextMuted,
		"fontFamily": eg.style.FontFamily,
		"fontSize":   eg.style.FontSizeLabel,
	}
}

func (eg *EChartsGenerator) nameTextStyle() map[string]interface{} {
	return map[string]interface{}{
		"color":      eg.style.ColorTextMuted,
		"fontFamily": eg.style.FontFamily,
		"fontSize":   eg.style.FontSizeLabel,
	}
}

func (eg *EChartsGenerator) splitLineStyle() map[string]interface{} {
	return map[string]interface{}{
		"lineStyle": map[string]interface{}{
			"color": fmt.Sprintf("%s0d", eg.style.ColorText), // Very transparent
			"type":  "dashed",
		},
	}
}

// extractLabelValues extracts labels and values from dataset
func (eg *EChartsGenerator) extractLabelValues(ds *Dataset) ([]string, []interface{}) {
	if len(ds.Data) == 0 {
		return []string{}, []interface{}{}
	}

	labels := make([]string, 0, len(ds.Data))
	values := make([]interface{}, 0, len(ds.Data))

	// Find label and value columns
	labelCol := eg.findLabelColumn(ds.Data[0])
	valueCol := eg.findValueColumn(ds.Data[0])

	for _, row := range ds.Data {
		// Extract label
		if labelVal, ok := row[labelCol]; ok {
			labels = append(labels, fmt.Sprintf("%v", labelVal))
		}
		// Extract value
		if valueVal, ok := row[valueCol]; ok {
			values = append(values, valueVal)
		}
	}

	return labels, values
}

// findLabelColumn finds the most likely label column
func (eg *EChartsGenerator) findLabelColumn(row map[string]interface{}) string {
	// Priority: name, label, category, pattern, path, id
	candidates := []string{"name", "label", "category", "pattern", "path", "id", "key"}
	for _, cand := range candidates {
		for key := range row {
			if key == cand {
				return key
			}
		}
	}
	// Fallback: first string column
	for key, val := range row {
		if _, ok := val.(string); ok {
			return key
		}
	}
	// Last resort: first column
	for key := range row {
		return key
	}
	return ""
}

// findValueColumn finds the most likely value column
func (eg *EChartsGenerator) findValueColumn(row map[string]interface{}) string {
	// Priority: frequency, count, value, score, total, sum
	candidates := []string{"frequency", "count", "value", "score", "total", "sum"}
	for _, cand := range candidates {
		for key := range row {
			if key == cand {
				return key
			}
		}
	}
	// Fallback: first numeric column
	for key, val := range row {
		switch val.(type) {
		case int, int64, float32, float64:
			return key
		}
	}
	return ""
}

// extractScatterData extracts (x, y) pairs for scatter plot
func (eg *EChartsGenerator) extractScatterData(ds *Dataset, rec *ChartRecommendation) []interface{} {
	if len(ds.Data) == 0 {
		return []interface{}{}
	}

	xCol := getStringOrDefault(rec.Config, "x_axis", "")
	yCol := getStringOrDefault(rec.Config, "y_axis", "")

	// Auto-detect if not specified
	if xCol == "" || yCol == "" {
		numCols := []string{}
		for key, val := range ds.Data[0] {
			switch val.(type) {
			case int, int64, float32, float64:
				numCols = append(numCols, key)
			}
		}
		if len(numCols) >= 2 {
			xCol = numCols[0]
			yCol = numCols[1]
		}
	}

	data := make([]interface{}, 0, len(ds.Data))
	for _, row := range ds.Data {
		xVal, xOk := row[xCol]
		yVal, yOk := row[yCol]
		if xOk && yOk {
			data = append(data, []interface{}{xVal, yVal})
		}
	}

	return data
}

// sortByValue sorts labels and values by value descending
func (eg *EChartsGenerator) sortByValue(labels []string, values []interface{}) {
	type pair struct {
		label string
		value interface{}
	}
	pairs := make([]pair, len(labels))
	for i := range labels {
		pairs[i] = pair{labels[i], values[i]}
	}

	sort.Slice(pairs, func(i, j int) bool {
		// Convert to float64 for comparison
		iVal := toFloat64(pairs[i].value)
		jVal := toFloat64(pairs[j].value)
		return iVal > jVal // Descending
	})

	for i := range pairs {
		labels[i] = pairs[i].label
		values[i] = pairs[i].value
	}
}

// toFloat64 converts interface{} to float64 for sorting
func toFloat64(val interface{}) float64 {
	switch v := val.(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case float32:
		return float64(v)
	case float64:
		return v
	default:
		return 0.0
	}
}

// darkenColor darkens a hex color by a percentage (0.0-1.0)
func darkenColor(hexColor string, amount float64) string {
	// Parse hex color (supports #RGB, #RRGGBB, RGB, RRGGBB)
	color := strings.TrimPrefix(hexColor, "#")

	var r, g, b uint8
	var err error

	if len(color) == 3 {
		// Short form (#RGB)
		var rgb uint64
		rgb, err = strconv.ParseUint(color, 16, 12)
		if err != nil {
			return hexColor // Return original on parse error
		}
		r = uint8((rgb >> 8) & 0xF) // #nosec G115 -- color value masked to 4 bits
		r = r*16 + r                // Expand 4-bit to 8-bit
		g = uint8((rgb >> 4) & 0xF) // #nosec G115 -- color value masked to 4 bits
		g = g*16 + g
		b = uint8(rgb & 0xF)
		b = b*16 + b
	} else if len(color) == 6 {
		// Full form (#RRGGBB)
		var rgb uint64
		rgb, err = strconv.ParseUint(color, 16, 24)
		if err != nil {
			return hexColor // Return original on parse error
		}
		r = uint8((rgb >> 16) & 0xFF) // #nosec G115 -- color value masked to 8 bits
		g = uint8((rgb >> 8) & 0xFF)  // #nosec G115 -- color value masked to 8 bits
		b = uint8(rgb & 0xFF)         // #nosec G115 -- color value masked to 8 bits
	} else {
		return hexColor // Invalid format, return original
	}

	// Darken by reducing each component
	r = uint8(float64(r) * (1.0 - amount))
	g = uint8(float64(g) * (1.0 - amount))
	b = uint8(float64(b) * (1.0 - amount))

	// Convert back to hex
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// extractRadarData extracts multi-dimensional data for radar chart
func (eg *EChartsGenerator) extractRadarData(ds *Dataset) map[string]interface{} {
	if len(ds.Data) == 0 {
		return map[string]interface{}{
			"legend":     []string{},
			"indicators": []interface{}{},
			"series":     []interface{}{},
		}
	}

	// Find all numeric columns as indicators (dimensions)
	indicators := []interface{}{}
	firstRow := ds.Data[0]
	var indicatorNames []string

	for key, val := range firstRow {
		switch val.(type) {
		case int, int64, float32, float64:
			indicators = append(indicators, map[string]interface{}{
				"name": key,
				"max":  100, // Default max, could be calculated from data
			})
			indicatorNames = append(indicatorNames, key)
		}
	}

	// Extract series data (one series per row or grouped by a category)
	seriesData := []interface{}{}
	legendData := []string{}

	for i, row := range ds.Data {
		values := []interface{}{}
		seriesName := fmt.Sprintf("Item %d", i+1)

		// Try to find a name/label field
		if labelCol := eg.findLabelColumn(row); labelCol != "" {
			if label, ok := row[labelCol]; ok {
				seriesName = fmt.Sprintf("%v", label)
			}
		}

		// Extract values for each indicator
		for _, indName := range indicatorNames {
			if val, ok := row[indName]; ok {
				values = append(values, val)
			} else {
				values = append(values, 0)
			}
		}

		seriesData = append(seriesData, map[string]interface{}{
			"value": values,
			"name":  seriesName,
		})
		legendData = append(legendData, seriesName)
	}

	return map[string]interface{}{
		"legend":     legendData,
		"indicators": indicators,
		"series":     seriesData,
	}
}

// extractBoxPlotData extracts statistical data for box plot
func (eg *EChartsGenerator) extractBoxPlotData(ds *Dataset) map[string]interface{} {
	if len(ds.Data) == 0 {
		return map[string]interface{}{
			"categories": []string{},
			"values":     []interface{}{},
		}
	}

	// Box plot data format: [[min, q1, median, q3, max], ...]
	// Expected input: rows with fields like "min", "q1", "median", "q3", "max"
	// Or rows with "category" and these statistical fields

	categories := []string{}
	boxValues := []interface{}{}

	for _, row := range ds.Data {
		// Try to find category/label
		category := "Data"
		if labelCol := eg.findLabelColumn(row); labelCol != "" {
			if label, ok := row[labelCol]; ok {
				category = fmt.Sprintf("%v", label)
			}
		}
		categories = append(categories, category)

		// Extract box plot values: [min, q1, median, q3, max]
		boxValue := []interface{}{
			getFloatOrDefault(row, "min", 0),
			getFloatOrDefault(row, "q1", 25),
			getFloatOrDefault(row, "median", 50),
			getFloatOrDefault(row, "q3", 75),
			getFloatOrDefault(row, "max", 100),
		}
		boxValues = append(boxValues, boxValue)
	}

	return map[string]interface{}{
		"categories": categories,
		"values":     boxValues,
	}
}

// extractTreeMapData extracts hierarchical data for treemap
func (eg *EChartsGenerator) extractTreeMapData(ds *Dataset) []interface{} {
	if len(ds.Data) == 0 {
		return []interface{}{}
	}

	// Treemap expects hierarchical data: {name, value, children: [...]}
	// For flat data, create a simple one-level tree

	treeData := []interface{}{}

	for _, row := range ds.Data {
		name := "Item"
		value := float64(0)

		// Find name/label
		if labelCol := eg.findLabelColumn(row); labelCol != "" {
			if label, ok := row[labelCol]; ok {
				name = fmt.Sprintf("%v", label)
			}
		}

		// Find value
		if valueCol := eg.findValueColumn(row); valueCol != "" {
			value = toFloat64(row[valueCol])
		}

		treeData = append(treeData, map[string]interface{}{
			"name":  name,
			"value": value,
		})
	}

	return treeData
}

// extractGraphData extracts nodes and edges for network graph
func (eg *EChartsGenerator) extractGraphData(ds *Dataset) map[string]interface{} {
	if len(ds.Data) == 0 {
		return map[string]interface{}{
			"nodes": []interface{}{},
			"edges": []interface{}{},
		}
	}

	nodes := []interface{}{}
	edges := []interface{}{}

	// Check if data has explicit node/edge structure
	// Expected formats:
	// 1. Rows with "source", "target", "value" (edges)
	// 2. Rows with "id", "name", "value" (nodes)

	hasSourceTarget := false
	if len(ds.Data) > 0 {
		firstRow := ds.Data[0]
		_, hasSource := firstRow["source"]
		_, hasTarget := firstRow["target"]
		hasSourceTarget = hasSource && hasTarget
	}

	if hasSourceTarget {
		// Edge-based data
		nodeSet := make(map[string]bool)

		for _, row := range ds.Data {
			source := fmt.Sprintf("%v", row["source"])
			target := fmt.Sprintf("%v", row["target"])
			value := toFloat64(row["value"])

			// Add nodes
			if !nodeSet[source] {
				nodes = append(nodes, map[string]interface{}{
					"id":   source,
					"name": source,
				})
				nodeSet[source] = true
			}
			if !nodeSet[target] {
				nodes = append(nodes, map[string]interface{}{
					"id":   target,
					"name": target,
				})
				nodeSet[target] = true
			}

			// Add edge
			edges = append(edges, map[string]interface{}{
				"source": source,
				"target": target,
				"value":  value,
			})
		}
	} else {
		// Node-based data - create simple linear graph
		for i, row := range ds.Data {
			name := fmt.Sprintf("Node %d", i)
			if labelCol := eg.findLabelColumn(row); labelCol != "" {
				if label, ok := row[labelCol]; ok {
					name = fmt.Sprintf("%v", label)
				}
			}

			value := float64(0)
			if valueCol := eg.findValueColumn(row); valueCol != "" {
				value = toFloat64(row[valueCol])
			}

			nodes = append(nodes, map[string]interface{}{
				"id":         fmt.Sprintf("node-%d", i),
				"name":       name,
				"value":      value,
				"symbolSize": 20 + (value / 10), // Scale node size by value
			})

			// Create edges to next node
			if i < len(ds.Data)-1 {
				edges = append(edges, map[string]interface{}{
					"source": fmt.Sprintf("node-%d", i),
					"target": fmt.Sprintf("node-%d", i+1),
				})
			}
		}
	}

	return map[string]interface{}{
		"nodes": nodes,
		"edges": edges,
	}
}

// getFloatOrDefault safely gets a float from a map with a default value
func getFloatOrDefault(m map[string]interface{}, key string, defaultVal float64) float64 {
	if val, ok := m[key]; ok {
		return toFloat64(val)
	}
	return defaultVal
}

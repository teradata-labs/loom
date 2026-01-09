// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package visualization

// ChartType represents the type of visualization
type ChartType string

const (
	ChartTypeBar        ChartType = "bar"
	ChartTypeLine       ChartType = "line"
	ChartTypePie        ChartType = "pie"
	ChartTypeScatter    ChartType = "scatter"
	ChartTypeRadar      ChartType = "radar"
	ChartTypeBoxPlot    ChartType = "boxplot"
	ChartTypeTreeMap    ChartType = "treemap"
	ChartTypeGraph      ChartType = "graph"
	ChartTypeTimeSeries ChartType = "timeseries"
)

// DataPattern represents the structure/pattern of data
type DataPattern struct {
	HasRanking     bool     // Data can be ranked (frequency, score, etc.)
	HasCategories  bool     // Data has categorical dimensions
	HasTimeSeries  bool     // Data has temporal ordering
	HasContinuous  bool     // Data has continuous distribution
	HasArrayFields bool     // Data has array/nested fields
	HasRelations   bool     // Data has relationships/edges
	Cardinality    int      // Number of unique items
	DataPoints     int      // Total data points
	NumericCols    []string // Numeric column names
	CategoryCols   []string // Categorical column names
	TimeCols       []string // Time/date column names
}

// ChartRecommendation represents a chart selection with confidence
type ChartRecommendation struct {
	ChartType  ChartType
	Title      string
	Rationale  string                 // Why this chart was recommended
	Config     map[string]interface{} // Chart-specific config
	Confidence float64                // 0.0 to 1.0
}

// Dataset represents aggregated data from presentation tools
type Dataset struct {
	Name     string                   // e.g., "top_50_patterns", "risk_distribution"
	Data     []map[string]interface{} // Array of data objects
	Schema   map[string]string        // Column name → type mapping
	Source   string                   // e.g., "stage-9-npath-full-results"
	RowCount int
	Metadata map[string]interface{}
}

// Visualization represents a single chart with embedded data
type Visualization struct {
	Type          ChartType              `json:"type"`
	Title         string                 `json:"title"`
	Description   string                 `json:"description"`
	EChartsConfig string                 `json:"echarts_config"` // JSON string
	Insight       string                 `json:"insight"`        // AI-generated caption
	DataPoints    int                    `json:"data_points"`
	Metadata      map[string]interface{} `json:"metadata"`
}

// Report represents a complete report with multiple visualizations
type Report struct {
	Title          string          `json:"title"`
	Summary        string          `json:"summary"` // Executive summary
	Visualizations []Visualization `json:"visualizations"`
	GeneratedAt    string          `json:"generated_at"`
	Metadata       ReportMetadata  `json:"metadata"`
}

// ReportMetadata contains metadata about report generation
type ReportMetadata struct {
	DataSource  string                 `json:"data_source"`
	WorkflowID  string                 `json:"workflow_id"`
	AgentID     string                 `json:"agent_id"`
	RowsSource  int                    `json:"rows_source"`  // Original row count
	RowsReduced int                    `json:"rows_reduced"` // Reduced row count
	Reduction   float64                `json:"reduction"`    // Percentage reduction
	Extra       map[string]interface{} `json:"extra"`
}

// StyleConfig holds Hawk StyleGuide design tokens
type StyleConfig struct {
	ColorPrimary    string   // Teradata Orange
	ColorBackground string   // Transparent or dark
	ColorText       string   // Light text
	ColorTextMuted  string   // Muted text
	ColorBorder     string   // Border color
	ColorGlass      string   // Glass morphism
	ColorPalette    []string // Color palette for series

	FontFamily      string // IBM Plex Mono
	FontSizeTitle   int    // Title font size
	FontSizeLabel   int    // Label font size
	FontSizeTooltip int    // Tooltip font size

	AnimationDuration int    // Animation duration in ms
	AnimationEasing   string // Easing function

	ShadowBlur    int     // Shadow blur radius
	GlowIntensity float64 // Glow intensity (0.0-1.0)
}

// DefaultStyleConfig returns Hawk StyleGuide defaults
func DefaultStyleConfig() *StyleConfig {
	return &StyleConfig{
		ColorPrimary:    "#f37021", // Teradata Orange
		ColorBackground: "transparent",
		ColorText:       "#f5f5f5",
		ColorTextMuted:  "#b5b5b5",
		ColorBorder:     "#ffffff1a",
		ColorGlass:      "rgba(26, 26, 26, 0.8)",
		ColorPalette: []string{
			"#f37021", // Teradata Orange
			"#60a5fa", // Blue
			"#8b5cf6", // Purple
			"#10b981", // Green
			"#f59e0b", // Amber
			"#ec4899", // Pink
			"#14b8a6", // Teal
		},
		FontFamily:        "IBM Plex Mono, monospace",
		FontSizeTitle:     14,
		FontSizeLabel:     11,
		FontSizeTooltip:   12,
		AnimationDuration: 1500,
		AnimationEasing:   "cubicOut",
		ShadowBlur:        15,
		GlowIntensity:     0.6,
	}
}

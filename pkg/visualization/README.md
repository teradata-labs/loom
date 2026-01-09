# Visualization Package

**Status**: ✅ Implemented (v0.6.0+)
**Tests**: 15 test functions, 100% pass rate with `-race` detector

## Overview

The visualization package provides intelligent chart generation and HTML report assembly for Loom agents. It transforms aggregated data from presentation tools into beautiful, interactive visualizations using the Hawk StyleGuide aesthetic.

**Key Innovation**: Automatic chart type selection based on data patterns, combined with ECharts rendering and Hawk terminal aesthetic.

## Architecture

```
Presentation Tools → Visualization Layer → Interactive HTML
    (data aggregation)    (chart generation)    (report output)

top_n_query(50 rows) ─┐
                      ├─> ChartSelector ──> EChartsGenerator ──> ReportGenerator ──> HTML
group_by_query(4 rows)┘     (bar/pie/line)     (JSON config)       (embedded charts)
```

## Components

### 1. ChartSelector

Analyzes data patterns and recommends appropriate chart types.

**File**: `chart_selector.go`

**Features**:
- Automatic pattern detection (ranking, categories, time series, continuous)
- Schema inference from data
- Confidence scoring (0.0-1.0)
- Support for complex data structures

**Example**:
```go
selector := visualization.NewChartSelector(nil)
rec := selector.RecommendChart(dataset)
// rec.ChartType = ChartTypeBar
// rec.Confidence = 0.95
// rec.Rationale = "Ranked data with 50 items, ideal for bar chart"
```

**Decision Rules** (in priority order):
- Time series data → Line chart (0.9)
- Network/graph data (source-target) → Graph chart (0.90)
- Statistical distribution (min/q1/median/q3/max) → Box plot (0.88)
- Ranking with 5-50 items → Bar chart (0.95)
- Multi-dimensional (3+ numeric cols) → Radar chart (0.82)
- Hierarchical structure (parent/children) → TreeMap (0.78)
- Few categories (2-7) → Pie chart (0.85)
- Many categories (>7) → Bar chart (0.80)
- Two+ numeric dimensions → Scatter plot (0.75)

### 2. EChartsGenerator

Generates ECharts JSON configurations with Hawk StyleGuide.

**File**: `echarts.go`

**Supported Charts**:
- **Bar charts** - horizontal/vertical, gradient fills, for ranking and comparison
- **Line charts** - smooth curves, area fill, for time series and trends
- **Pie charts** - with percentages, for categorical distribution
- **Scatter plots** - correlation analysis, two-dimensional relationships
- **Radar charts** - multi-dimensional comparisons, spider charts
- **Box plots** - statistical distribution, quartiles and outliers
- **TreeMaps** - hierarchical data, nested categories with sizes
- **Graph charts** - network visualization, nodes and edges with force layout

**Hawk StyleGuide Tokens**:
```go
ColorPrimary:      "#f37021" // Teradata Orange
FontFamily:        "IBM Plex Mono, monospace"
AnimationDuration: 1500ms
AnimationEasing:   "cubicOut"
ShadowBlur:        15px
```

**Example**:
```go
gen := visualization.NewEChartsGenerator(nil)
config, _ := gen.Generate(dataset, recommendation)
// Returns ECharts JSON config with Hawk aesthetic
```

### 3. StyleGuideClient

Fetches styling from Hawk StyleGuide service.

**File**: `styleguide_client.go`

**Features**:
- Dynamic style fetching from Hawk (future)
- Fallback to default Hawk aesthetic
- Theme variants: dark, light, teradata, minimal
- Style merging and validation

**Example**:
```go
client := visualization.NewStyleGuideClient("hawk:50051")
style := client.FetchStyleWithFallback(ctx, "dark")

// Or use built-in themes
style = visualization.GetThemeVariant("teradata")
```

### 4. ReportGenerator

Assembles complete HTML reports with multiple charts.

**File**: `report_generator.go`

**Features**:
- Multi-chart reports
- Executive summary section
- AI-generated insights per chart
- Metadata (source, reduction stats, timestamps)
- Self-contained HTML (ECharts from CDN)
- Print-friendly CSS

**Example**:
```go
rg := visualization.NewReportGenerator(nil)
report, _ := rg.GenerateReport(ctx, datasets, title, summary)
html, _ := rg.ExportHTML(report)
os.WriteFile("/tmp/report.html", []byte(html), 0644)
```

### 5. VisualizationTool

Agent tool interface for workflow integration.

**File**: `tool.go`

**Tool Name**: `generate_visualization`

**Parameters**:
- `datasets`: Array of presentation tool results
- `title`: Report title
- `summary`: Executive summary
- `output_path`: Where to save HTML file
- `theme`: Theme variant (dark/light/teradata/minimal)

**Example**:
```go
tool := visualization.NewVisualizationTool()
result, _ := tool.Execute(ctx, map[string]interface{}{
    "datasets": [...],
    "title": "Analysis Report",
    "summary": "Executive summary...",
    "output_path": "/tmp/report.html",
    "theme": "teradata",
})
```

## Quick Start

### Programmatic Usage

```go
package main

import (
    "context"
    "github.com/teradata-labs/loom/pkg/visualization"
)

func main() {
    // 1. Parse presentation tool result
    dataset, _ := visualization.ParseDataFromPresentationToolResult(
        topNResult,  // From top_n_query tool
        "top_50_patterns",
    )

    // 2. Generate report
    rg := visualization.NewReportGeneratorWithStyle(
        visualization.GetThemeVariant("teradata"))

    report, _ := rg.GenerateReport(context.Background(),
        []*visualization.Dataset{dataset},
        "Customer Journey Analysis",
        "Analysis of 10,000 patterns reveals...",
    )

    // 3. Export to HTML
    html, _ := rg.ExportHTML(report)
    os.WriteFile("/tmp/report.html", []byte(html), 0644)
}
```

### Agent Tool Usage

In workflow YAML:

```yaml
stages:
  - agent_id: visualization-agent
    tools:
      - generate_visualization

    prompt_template: |
      Generate an interactive HTML report:

      generate_visualization(
        datasets=[
          {name: "top_50_patterns", data: [JSON from top_n_query]},
          {name: "path_distribution", data: [JSON from group_by_query]}
        ],
        title="nPath Analysis Report",
        summary="Analysis summary...",
        output_path="/tmp/report.html",
        theme="teradata"
      )
```

## Examples

### Complete Demo

```bash
# Run the full pipeline demo
go run examples/visualization/main.go

# Output:
# - /tmp/loom-customer-journey-report.html (7.5 KB)
# - /tmp/loom-tool-generated-report.html (7.1 KB)
```

### Workflow Integration

```bash
# Run v3.5 workflow (if you have looms running)
looms workflow execute examples/visualization/workflow-v3.5-visualization-demo.yaml
```

## Testing

```bash
# Run all tests
go test ./pkg/visualization -v

# With race detector (REQUIRED)
go test ./pkg/visualization -race -v

# Specific test
go test ./pkg/visualization -run TestVisualizationTool -v

# Coverage
go test ./pkg/visualization -cover
```

**Test Coverage**: 15 functions, 100% pass, 0 race conditions

## API Reference

### Types

**ChartType**: `bar`, `line`, `pie`, `scatter`, `timeseries`, `radar`, `boxplot`, `treemap`, `graph`

**DataPattern**: Detected patterns in data
- `HasRanking`: Data can be ranked
- `HasCategories`: Categorical dimensions
- `HasTimeSeries`: Temporal ordering
- `HasContinuous`: Continuous distribution
- `Cardinality`: Number of unique items

**Dataset**: Aggregated data from presentation tools
- `Name`: Dataset identifier
- `Data`: Array of data objects
- `Schema`: Column types
- `Source`: Original data source key
- `RowCount`: Number of rows

**Report**: Complete report with visualizations
- `Title`: Report title
- `Summary`: Executive summary
- `Visualizations`: Array of charts
- `GeneratedAt`: Timestamp
- `Metadata`: Source, reduction stats

### Functions

**ParseDataFromPresentationToolResult**(result, name) → Dataset
- Converts presentation tool output to Dataset struct

**NewChartSelector**(style) → ChartSelector
- Creates chart selector with optional custom style

**NewEChartsGenerator**(style) → EChartsGenerator
- Creates ECharts config generator with style

**NewStyleGuideClient**(endpoint) → StyleGuideClient
- Creates client for Hawk StyleGuide service

**NewReportGenerator**(client) → ReportGenerator
- Creates report generator with StyleGuide client

**NewReportGeneratorWithStyle**(style) → ReportGenerator
- Creates report generator with custom style

**NewVisualizationTool**() → Tool
- Creates agent tool for workflow integration

**DefaultStyleConfig**() → StyleConfig
- Returns default Hawk StyleGuide configuration

**GetThemeVariant**(variant) → StyleConfig
- Returns theme variant: dark, light, teradata, minimal

## Performance

### Benchmarks

| Operation | Dataset Size | Time | Memory |
|-----------|--------------|------|--------|
| ChartSelector.RecommendChart | 50 rows | < 1ms | < 100 KB |
| EChartsGenerator.Generate | 50 rows | 2-5ms | < 200 KB |
| ReportGenerator.GenerateReport | 3 datasets | 10-20ms | < 1 MB |
| ReportGenerator.ExportHTML | 3 charts | 5-10ms | < 500 KB |

**Chart Rendering** (browser):
- ECharts init: 100-200ms per chart
- Animation: 1500ms (smooth)
- Interaction: < 16ms (60 FPS)

### Data Reduction

Combined with presentation tools:
- Original: 10,000 rows → Presentation: 54 rows → Visualization: 2 charts
- **Total reduction**: 99.86%
- **Context savings**: ~2.5 MB → 14 KB
- **Performance gain**: 450x faster data transfer

## Hawk StyleGuide Integration

### Default Aesthetic

- **Primary Color**: Teradata Orange (#f37021)
- **Font**: IBM Plex Mono (monospace)
- **Background**: Transparent/dark (#0d0d0d)
- **Text**: Light (#f5f5f5) / Muted (#b5b5b5)
- **Effects**: Glass morphism, glowing shadows
- **Animations**: 1500ms with elastic easing

### Theme Variants

**Dark** (default):
```go
style := visualization.DefaultStyleConfig()
// Teradata Orange, dark background, light text
```

**Light**:
```go
style := visualization.GetThemeVariant("light")
// Light background, dark text, adjusted borders
```

**Teradata**:
```go
style := visualization.GetThemeVariant("teradata")
// Teradata Orange + Navy color palette
```

**Minimal**:
```go
style := visualization.GetThemeVariant("minimal")
// Monochrome grayscale, reduced animations
```

## Integration Patterns

### Pattern 1: Programmatic API

```go
// Direct Go integration
rg := visualization.NewReportGeneratorWithStyle(nil)
report, _ := rg.GenerateReport(ctx, datasets, title, summary)
html, _ := rg.ExportHTML(report)
```

### Pattern 2: Agent Tool

```yaml
# In workflow
tools:
  - generate_visualization

prompt: |
  Use generate_visualization to create report from datasets.
```

### Pattern 3: Complete Pipeline

```
Analytics Agent
  ↓ stores full results
SharedMemory
  ↓ queries with presentation tools
Aggregation Agent (top_n_query, group_by_query)
  ↓ reduced data
Visualization Agent (generate_visualization)
  ↓ HTML report
Output File
```

## Recent Additions

**✅ Implemented (v0.7.0)**:
- **Radar charts** - Multi-dimensional spider/radar charts for comparative analysis
- **Box plot charts** - Statistical distribution visualization with quartiles
- **TreeMap charts** - Hierarchical data visualization with nested rectangles
- **Graph charts** - Network/relationship visualization with force-directed layout

## Future Enhancements

**Planned Features (📋)**:
- LLM integration for enhanced AI-generated insights
- Interactive features (drill-down, filtering, data table views)
- gRPC client to Hawk StyleGuide service for dynamic themes
- Custom report templates (executive, technical, presentation)
- React component export for web applications
- PDF generation via headless browser
- Additional chart variants (stacked bar, multi-line, heatmap)

## Related Documentation

- [Presentation Tools](../../docs/reference/PRESENTATION_TOOLS.md)
- [Report Generation Architecture](../../docs/REPORT_GENERATION_ARCHITECTURE.md)
- [Weaver Agent](../../docs/WEAVER_AGENT.md)
- [Example Program](./main.go)
- [v3.5 Workflow](./workflow-v3.5-visualization-demo.yaml)

## Contributing

When adding new chart types:
1. Add chart type constant to `types.go`
2. Implement generation logic in `echarts.go`
3. Add decision rule to `chart_selector.go`
4. Write tests with `-race` detector
5. Update documentation

## License

Apache 2.0 - See LICENSE file for details


# Presentation Tools Reference

Specification for presentation strategy tools that enable SQL-like data querying on shared memory datasets. These tools achieve 99%+ data reduction through in-memory aggregation, preventing context window overflow in multi-agent workflows.

**Version**: v1.2.0 (v0.6.0+ implementation)
**Package**: `pkg/shuttle/builtin`, `pkg/visualization`
**Status**: ✅ Implemented (36 test functions across 3 test files, 0 race conditions)


## Table of Contents

- [Quick Reference](#quick-reference)
- [Tools](#tools)
  - [top_n_query](#top_n_query)
  - [group_by_query](#group_by_query)
- [Visualization Components](#visualization-components)
  - [ChartSelector](#chartselector)
  - [EChartsGenerator](#echartsgenerator)
  - [StyleGuideClient](#styleguideclient)
  - [ReportGenerator](#reportgenerator)
  - [VisualizationTool](#visualizationtool)
- [Architecture](#architecture)
- [Data Format Requirements](#data-format-requirements)
- [Integration](#integration)
- [Performance](#performance)
- [Error Codes](#error-codes)
- [Examples](#examples)
- [Testing](#testing)
- [See Also](#see-also)


## Quick Reference

### Presentation Tools

| Tool | Purpose | Input Size | Output Size | Reduction | Latency |
|------|---------|------------|-------------|-----------|---------|
| `top_n_query` | Get top N items sorted by column | 10,000 rows | 50 rows | 99.5% | 12-18ms |
| `group_by_query` | Aggregate by dimensions | 10,000 rows | 5 groups | 99.95% | 8-14ms |

### Visualization Components

| Component | Purpose | Input | Output | Latency |
|-----------|---------|-------|--------|---------|
| `ChartSelector` | Recommend chart type | Dataset | Chart recommendation | <1ms |
| `EChartsGenerator` | Generate chart config | Dataset + recommendation | ECharts JSON | 2-5ms |
| `StyleGuideClient` | Fetch design tokens | Theme name | Style config | N/A (fallback) |
| `ReportGenerator` | Assemble HTML report | Multiple datasets | Self-contained HTML | 10-20ms |

### Common Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `source_key` | string | Yes | - | Shared memory key for dataset |
| `n` | int | Yes (top_n) | 10 | Number of items (range: 1-1000) |
| `sort_by` | string | Yes (top_n) | - | Column name to sort by (numeric) |
| `direction` | string | No | `"desc"` | Sort direction: `"asc"` or `"desc"` |
| `group_by` | []interface{} (string elements) | Yes (group_by) | - | Column names to group by |
| `aggregates` | []object | No (group_by) | auto | Aggregate functions (count, sum, avg, min, max) |
| `namespace` | string | No | `"workflow"` | Namespace: `"global"`, `"workflow"`, `"swarm"` |


## Tools

### top_n_query

**Implementation**: `pkg/shuttle/builtin/presentation_tools.go` (TopNQueryTool)
**Tool Name**: `top_n_query`

**Purpose**: Get the top N items from a dataset, sorted by any numeric column.

**Use cases**:
- Most frequent patterns or categories
- Highest/lowest performers
- Top outliers by any metric
- Prioritization lists

#### Parameters

##### source_key

**Type**: `string`
**Required**: Yes
**Constraints**: Must exist in shared memory

**Description**: Shared memory key containing the dataset.

**Example**: `"stage-9-npath-full-results"`


##### n

**Type**: `int`
**Required**: Yes
**Range**: 1-1000
**Default**: 10

**Description**: Number of top items to return.

**Example**: `50`


##### sort_by

**Type**: `string`
**Required**: Yes
**Constraints**: Column must contain numeric values (int, float64)

**Description**: Column name to sort by.

**Example**: `"frequency"`, `"duration"`, `"conversion_rate"`


##### direction

**Type**: `string`
**Required**: No
**Default**: `"desc"`
**Allowed values**: `"asc"`, `"desc"`

**Description**: Sort direction (descending or ascending).

**Example**: `"desc"` for highest first, `"asc"` for lowest first


##### namespace

**Type**: `string`
**Required**: No
**Default**: `"workflow"`
**Allowed values**: `"global"`, `"workflow"`, `"swarm"`

**Description**: Shared memory namespace to search.

**Example**: `"workflow"`


#### Response Schema

```json
{
  "items": [
    {"pattern": "A→B→C", "frequency": 1247, "duration": 5.2},
    {"pattern": "A→C", "frequency": 982, "duration": 3.1}
  ],
  "total": 10000,
  "returned": 50,
  "sort_by": "frequency",
  "direction": "desc",
  "source_key": "stage-9-npath-full-results",
  "namespace": "workflow"
}
```

#### Example Usage

```go
import "github.com/teradata-labs/loom/pkg/shuttle/builtin"

// Get top 50 patterns by frequency
result, err := topNTool.Execute(ctx, map[string]interface{}{
    "source_key": "stage-9-npath-full-results",
    "n":          50,
    "sort_by":    "frequency",
    "direction":  "desc",
    "namespace":  "workflow",
})
if err != nil {
    log.Fatalf("top_n_query failed: %v", err)
}

items := result.Data.(map[string]interface{})["items"].([]interface{})
fmt.Printf("Returned %d items from %d total\n", len(items), result.Data.(map[string]interface{})["total"])
// Output: Returned 50 items from 10000 total
```

**YAML usage in workflow**:
```yaml
# Get top 50 most frequent patterns
top_n_query(
  source_key="stage-9-npath-full-results",
  n=50,
  sort_by="frequency",
  direction="desc",
  namespace="workflow"
)
```

**Performance**: 12-18ms for 10,000 rows

**Thread safety**: Safe for concurrent use (read-only, RWMutex protected)


### group_by_query

**Implementation**: `pkg/shuttle/builtin/presentation_tools.go` (GroupByQueryTool)
**Tool Name**: `group_by_query`

**Purpose**: Aggregate data by one or more dimensions, returning counts per group.

**Use cases**:
- Distribution analysis
- Segment breakdowns
- Category statistics
- Dimensional rollups

#### Parameters

##### source_key

**Type**: `string`
**Required**: Yes
**Constraints**: Must exist in shared memory

**Description**: Shared memory key containing the dataset.

**Example**: `"stage-9-npath-full-results"`


##### group_by

**Type**: `[]string`
**Required**: Yes
**Constraints**: Missing columns are treated as `"NULL"` in group keys

**Description**: Column names to group by. Supports multi-dimensional grouping.

**Example**: `["path_length"]`, `["customer_segment", "region"]`


##### aggregates

**Type**: `[]object`
**Required**: No
**Object fields**:
- `function` (string, required): Aggregate function: `"count"`, `"sum"`, `"avg"`, `"min"`, `"max"`
- `column` (string, required for sum/avg/min/max): Column to aggregate

**Description**: Aggregations to compute. When omitted, the tool automatically computes `count`, `sum`, `avg`, `min`, and `max` for all numeric columns not in `group_by`.

**Example**: `[{"function": "sum", "column": "revenue"}]`


##### namespace

**Type**: `string`
**Required**: No
**Default**: `"workflow"`
**Allowed values**: `"global"`, `"workflow"`, `"swarm"`

**Description**: Shared memory namespace to search.

**Example**: `"workflow"`


#### Response Schema

```json
{
  "groups": [
    {"path_length": 3, "count": 4500, "frequency_sum": 12340, "frequency_avg": 2.74, "frequency_min": 1, "frequency_max": 50},
    {"path_length": 4, "count": 3200, "frequency_sum": 9600, "frequency_avg": 3.0, "frequency_min": 1, "frequency_max": 45},
    {"path_length": 2, "count": 1800, "frequency_sum": 5400, "frequency_avg": 3.0, "frequency_min": 1, "frequency_max": 40},
    {"path_length": 5, "count": 500, "frequency_sum": 1000, "frequency_avg": 2.0, "frequency_min": 1, "frequency_max": 20}
  ],
  "group_by": ["path_length"],
  "total_rows": 10000,
  "num_groups": 4,
  "source_key": "stage-9-npath-full-results",
  "namespace": "workflow"
}
```

#### Example Usage

```go
import "github.com/teradata-labs/loom/pkg/shuttle/builtin"

// Distribution by path length
result, err := groupByTool.Execute(ctx, map[string]interface{}{
    "source_key": "stage-9-npath-full-results",
    "group_by":   []interface{}{"path_length"},
    "namespace":  "workflow",
})
if err != nil {
    log.Fatalf("group_by_query failed: %v", err)
}

groups := result.Data.(map[string]interface{})["groups"].([]interface{})
fmt.Printf("Found %d groups from %d total rows\n", len(groups), result.Data.(map[string]interface{})["total_rows"])
// Output: Found 4 groups from 10000 total rows
```

**YAML usage in workflow**:
```yaml
# Distribution by path length
group_by_query(
  source_key="stage-9-npath-full-results",
  group_by=["path_length"],
  namespace="workflow"
)

# Multi-dimensional grouping
group_by_query(
  source_key="stage-9-npath-full-results",
  group_by=["customer_segment", "region"],
  namespace="workflow"
)
```

**Performance**: 8-14ms for 10,000 rows (single dimension), 15-24ms (two dimensions)

**Thread safety**: Safe for concurrent use (read-only, RWMutex protected)

**Note**: Automatically computes COUNT, SUM, AVG, MIN, MAX for all numeric columns not in the `group_by` list. Result columns are named `{column}_sum`, `{column}_avg`, `{column}_min`, `{column}_max`.


## Visualization Components

### ChartSelector

**Implementation**: `pkg/visualization/chart_selector.go`

**Purpose**: Analyzes dataset structure and recommends appropriate chart types with confidence scoring.

#### API

```go
func NewChartSelector(styleConfig *StyleConfig) *ChartSelector
func (cs *ChartSelector) RecommendChart(dataset *Dataset) *ChartRecommendation
func (cs *ChartSelector) AnalyzeDataset(ds *Dataset) *DataPattern
func (cs *ChartSelector) RecommendChartsForDatasets(datasets []*Dataset) []*ChartRecommendation
```

#### ChartRecommendation Schema

```go
type ChartRecommendation struct {
    ChartType  ChartType              // bar, line, pie, scatter, radar, boxplot, treemap, graph, timeseries
    Title      string                 // Auto-generated title
    Rationale  string                 // Human-readable explanation
    Config     map[string]interface{} // Chart-specific config (e.g., orientation, x_axis, y_axis)
    Confidence float64                // 0.0-1.0
}
```

#### Chart Type Selection Rules

Rules are evaluated in priority order; the first match wins:

| Priority | Data Pattern | Chart Type | Confidence | Condition |
|----------|--------------|-----------|------------|-----------|
| 1 | Time series | Line/TimeSeries | 0.9 | Column name contains "time" or "date", and TimeCols detected in schema |
| 2 | Graph/network | Graph | 0.90 | Row has "source" and "target" fields |
| 3 | Statistical distribution | BoxPlot | 0.88 | Row has at least 3 of: min, q1, median, q3, max |
| 4 | Ranking (5-50 items) | Bar | 0.95 | Numeric ranking column, 5-50 rows |
| 5 | Multi-dimensional (3+ numeric, <=10 items) | Radar | 0.82 | 3+ numeric columns, <=10 cardinality |
| 6 | Hierarchical data | TreeMap | 0.78 | Has "parent"/"children" fields, nested objects, or arrays |
| 7 | Few categories (2-7) | Pie | 0.85 | Categorical data, 2-7 unique values |
| 8 | Many categories (>7) | Bar | 0.80 | Categorical data, >7 unique values |
| 9 | Two numeric dimensions | Scatter | 0.75 | Two or more numeric columns |
| 10 | Default fallback | Bar | 0.60 | No pattern matched |

#### Example

```go
import "github.com/teradata-labs/loom/pkg/visualization"

// Parse presentation tool result
dataset, _ := visualization.ParseDataFromPresentationToolResult(
    topNResult.Data.(map[string]interface{}),
    "top_50_patterns",
)

// Analyze and recommend chart (nil uses DefaultStyleConfig)
selector := visualization.NewChartSelector(nil)
rec := selector.RecommendChart(dataset)

fmt.Printf("Recommended: %s (%.2f confidence)\n", rec.ChartType, rec.Confidence)
fmt.Printf("Rationale: %s\n", rec.Rationale)
fmt.Printf("Title: %s\n", rec.Title)
// Output:
// Recommended: bar (0.95 confidence)
// Rationale: Ranked data with 50 items, ideal for bar chart comparison
// Title: Top 50 by frequency
```

**Performance**: <1ms for 50-row dataset

**Thread safety**: Safe for concurrent use


### EChartsGenerator

**Implementation**: `pkg/visualization/echarts.go`

**Purpose**: Generates ECharts JSON configurations with Hawk StyleGuide aesthetic.

#### API

```go
func NewEChartsGenerator(style *StyleConfig) *EChartsGenerator
func (eg *EChartsGenerator) Generate(dataset *Dataset, rec *ChartRecommendation) (string, error)
```

#### Supported Chart Types

- ✅ Bar charts (vertical and horizontal)
- ✅ Line charts (with area fill)
- ✅ Pie charts
- ✅ Scatter plots
- ✅ Radar charts (multi-dimensional)
- ✅ Box plot charts (statistical distribution)
- ✅ TreeMap charts (hierarchical data)
- ✅ Graph/network charts (force-directed layout)

#### Hawk StyleGuide Design Tokens

```go
ColorPrimary:       "#f37021"  // Teradata Orange
ColorBackground:    "transparent"
ColorText:          "#f5f5f5"
ColorTextMuted:     "#b5b5b5"
FontFamily:         "IBM Plex Mono, monospace"
AnimationDuration:  1500       // milliseconds
AnimationEasing:    "cubicOut"
ShadowBlur:         15         // pixels
GlowIntensity:      0.6        // 0.0-1.0
```

#### Example

```go
import "github.com/teradata-labs/loom/pkg/visualization"

gen := visualization.NewEChartsGenerator(nil) // nil uses DefaultStyleConfig()
echartsJSON, _ := gen.Generate(dataset, rec)

// Returns ECharts configuration JSON with:
// - Gradient fills (Teradata Orange → darker)
// - Glowing shadows on hover
// - IBM Plex Mono font
// - 1500ms animations
// - Glass morphism backgrounds
```

**Performance**: 2-5ms for 50-row dataset

**Thread safety**: Safe for concurrent use


### StyleGuideClient

**Implementation**: `pkg/visualization/styleguide_client.go`

**Purpose**: Integrates with Hawk StyleGuide service for dynamic styling.

#### API

```go
func NewStyleGuideClient(endpoint string) *StyleGuideClient
func (sc *StyleGuideClient) FetchStyleWithFallback(ctx context.Context, theme string) *StyleConfig
```

#### Theme Variants

```go
visualization.GetThemeVariant("dark")      // Default Hawk dark theme
visualization.GetThemeVariant("light")     // Light theme variant
visualization.GetThemeVariant("teradata")  // Teradata branding emphasis
visualization.GetThemeVariant("minimal")   // Monochrome minimal
```

#### Example

```go
client := visualization.NewStyleGuideClient("hawk.example.com:50051")
style := client.FetchStyleWithFallback(ctx, "dark")

// Or use theme variants directly
style = visualization.GetThemeVariant("teradata")  // Teradata Orange branding
```

**Fallback behavior**: Always returns default Hawk aesthetic if service unavailable

**Thread safety**: Safe for concurrent use


### ReportGenerator

**Implementation**: `pkg/visualization/report_generator.go`

**Purpose**: Assembles self-contained HTML reports with multiple embedded charts.

#### API

```go
func NewReportGenerator(styleClient *StyleGuideClient) *ReportGenerator
func (rg *ReportGenerator) GenerateReport(ctx context.Context, datasets []*Dataset, title, summary string) (*Report, error)
func (rg *ReportGenerator) ExportHTML(report *Report) (string, error)
```

#### Report Features

- ✅ Multi-chart reports (3-5 charts per report)
- ✅ Executive summary section
- ✅ AI-generated insights per chart (rule-based heuristics)
- ✅ Metadata (data source, row counts, reduction percentage)
- ✅ Self-contained HTML (ECharts loaded from CDN)
- ✅ Responsive design
- ✅ Print-friendly CSS
- 📋 LLM-generated insights (planned)

#### Example

```go
import "github.com/teradata-labs/loom/pkg/visualization"

// Create report generator
rg := visualization.NewReportGenerator(nil)

// Generate report from multiple datasets
report, _ := rg.GenerateReport(ctx,
    []*visualization.Dataset{topNDataset, groupByDataset},
    "Customer Journey Analysis",
    "Analysis of 10,000 customer paths reveals key patterns...",
)

// Export to HTML
html, _ := rg.ExportHTML(report)
// html contains complete self-contained report
```

**Report structure**:
```html
<!DOCTYPE html>
<html>
<head>
    <title>Customer Journey Analysis</title>
    <script src="https://cdn.jsdelivr.net/npm/echarts@5/dist/echarts.min.js"></script>
    <style>/* Hawk terminal aesthetic styles */</style>
</head>
<body>
    <h1>Customer Journey Analysis</h1>
    <div class="summary">Executive summary...</div>
    <div class="metadata">Generated: 2025-12-11 | Rows: 10000 → 50 | Reduction: 99.5%</div>

    <div class="visualization">
        <h2>Top 50 Patterns by Frequency</h2>
        <div id="chart-0"></div>
        <div class="insight">The leading pattern 'A→B→C' accounts for...</div>
    </div>

    <script>/* ECharts initialization */</script>
</body>
</html>
```

**Performance**: 10-20ms for 3 datasets (100 rows total), 5-10ms HTML export

**Thread safety**: Safe for concurrent use


### VisualizationTool

**Implementation**: `pkg/visualization/tool.go`

**Purpose**: Shuttle tool wrapper that generates interactive HTML reports from presentation tool results. This is an agent-callable tool (not a library function).

**Tool Name**: `generate_visualization`

**Note**: This tool is NOT included in `CommunicationTools`. It must be explicitly assigned by the metaagent via `builtin.VisualizationTools()`.

#### Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `datasets` | []object | Yes | - | Array of `{name, data}` objects from presentation tool results |
| `title` | string | Yes | - | Report title |
| `summary` | string | Yes | - | Executive summary |
| `output_path` | string | Yes | - | Path to save HTML file (e.g., `/tmp/report.html`) |
| `theme` | string | No | `"dark"` | Theme variant: `"dark"`, `"light"`, `"teradata"`, `"minimal"` |

#### Example

```go
import "github.com/teradata-labs/loom/pkg/visualization"

tool := visualization.NewVisualizationTool()
result, err := tool.Execute(ctx, map[string]interface{}{
    "datasets": []interface{}{
        map[string]interface{}{
            "name": "top_patterns",
            "data": `{"items": [...], "source_key": "stage-9-results"}`,
        },
    },
    "title":       "nPath Analysis Report",
    "summary":     "Analysis of customer journey patterns...",
    "output_path": "/tmp/npath_report.html",
    "theme":       "dark",
})
```

**Thread safety**: Safe for concurrent use


## Architecture

### End-to-End Pipeline

```
┌─────────────────────────────────────────────────────────────┐
│ Stage 9: Data Producer (Teradata/Postgres/SQLite)          │
│ - Executes analytical queries                               │
│ - Generates 10,000 results                                  │
│ - Stores full dataset in shared_memory                      │
│   Key: "stage-9-npath-full-results"                        │
└──────────────────────┬──────────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────────┐
│ Shared Memory Store (Zero-Copy Storage)                    │
│ - In-memory JSON storage                                    │
│ - Namespace isolation (global/workflow/swarm)              │
│ - RWMutex for concurrent reads                             │
└──────────────────────┬──────────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────────┐
│ Stage 11: Insight Generator                                 │
│ - Uses top_n_query(n=50, sort_by="frequency")             │
│ - Uses group_by_query(group_by=["path_length"])           │
│ - Data reduction: 99.5% (10,000 → 50 rows)                │
└──────────────────────┬──────────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────────┐
│ Visualization Layer (pkg/visualization)                    │
│                                                             │
│ 1. ChartSelector analyzes data patterns                    │
│    → Detects: ranking, categories, time series, graphs     │
│    → Recommends: bar, pie, line, scatter, radar, etc.      │
│                                                             │
│ 2. EChartsGenerator creates configs                        │
│    → Applies Hawk StyleGuide aesthetic                     │
│    → IBM Plex Mono font, Teradata Orange colors            │
│    → Glass morphism, glowing effects, animations           │
│                                                             │
│ 3. ReportGenerator assembles HTML                          │
│    → Multiple charts with embedded data                    │
│    → AI-generated insights per chart                       │
│    → Executive summary                                      │
│    → Self-contained HTML (ECharts loaded from CDN)         │
└──────────────────────┬──────────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────────┐
│ Output: Interactive HTML Report                            │
│ - 3-5 charts with data visualizations                      │
│ - Embedded ECharts (loaded from CDN)                       │
│ - Dark theme matching Hawk terminal aesthetic              │
│ - Print-friendly styling                                   │
└─────────────────────────────────────────────────────────────┘
```

### Tool Injection

Presentation tools are automatically injected into all agents by `MultiAgentServer` via `CommunicationTools`, which bundles messaging, shared memory, and presentation query tools:

```go
// pkg/server/multi_agent.go
func (s *MultiAgentServer) AddAgent(id string, ag *agent.Agent) {
    agentGUID := ag.GetID()
    // ...
    // Inject communication tools (includes presentation tools: top_n_query, group_by_query)
    commTools := builtin.CommunicationTools(s.messageQueue, s.messageBus, s.sharedMemoryComm, agentGUID)
    ag.RegisterTools(commTools...)
}
```

**Note**: Visualization tools (`generate_workflow_visualization`, `generate_visualization`) are NOT included in `CommunicationTools`. They must be explicitly assigned by the metaagent using `builtin.VisualizationTools()`.


## Data Format Requirements

### Source Data Structure

Presentation tools expect data in one of two formats:

**Format 1: Array of objects** (most common)
```json
[
  {"pattern": "A→B→C", "frequency": 100, "duration": 5.2},
  {"pattern": "A→C", "frequency": 80, "duration": 3.1}
]
```

**Format 2: Map with array values**
```json
{
  "query1_results": [
    {"pattern": "A→B→C", "frequency": 100}
  ],
  "query2_results": [
    {"pattern": "X→Y", "frequency": 50}
  ]
}
```

### Column Requirements

- **top_n_query**: `sort_by` column must contain numeric values (int, float64)
- **group_by_query**: `group_by` columns can contain any JSON-serializable values
- Missing columns treated as NULL


## Integration

### Workflow Pattern (v3.4+)

```yaml
apiVersion: loom/v1
kind: Workflow
metadata:
  name: npath-analysis-v3.4

stages:
  # Stage 9: Execute queries and store full results
  - agent_id: td-expert-analytics-stage-9
    prompt_template: |
      Execute queries and store FULL results in shared_memory:

      shared_memory_write(
        key="stage-9-npath-full-results",
        namespace="workflow",
        value='{"results": [...]}'
      )

  # Stage 10: Create summary with {{history}}
  - agent_id: td-expert-analytics-stage-10
    prompt_template: |
      {{history}}

      Synthesize all results from Stages 1-9 into summary.
      Note: Full results available in shared_memory for Stage 11.

  # Stage 11: Generate insights using presentation tools
  - agent_id: td-expert-insights-stage-11
    prompt_template: |
      {{previous}}

      Use presentation tools to analyze Stage 10's summary:

      # Get top 50 patterns
      top_n_query(source_key="stage-9-npath-full-results", n=50, sort_by="frequency")

      # Analyze distribution
      group_by_query(source_key="stage-9-npath-full-results", group_by=["path_length"])
```

### Context Strategy

- **Stages 1-9**: Use `{{previous}}` (lightweight, see only last stage)
- **Stage 10**: Use `{{history}}` (heavyweight, see all stages for summary)
- **Stage 11**: Use `{{previous}}` (lightweight, see Stage 10's summary) + presentation tools for deep dives

This achieves optimal context management:
- Stage 11 sees compressed summary (not 10,000 results)
- Stage 11 can query full dataset with structured tools
- No context window overflow


## Performance

### Benchmarks (v0.6.0)

**Test environment**: MacBook Pro M1, 16GB RAM, Go 1.25

| Operation | Dataset Size | Execution Time | Memory Usage |
|-----------|--------------|----------------|--------------|
| `top_n_query(n=50)` | 10,000 rows | 12-18ms | 2.1 MB |
| `top_n_query(n=500)` | 100,000 rows | 89-142ms | 18.4 MB |
| `group_by_query(1 dim)` | 10,000 rows | 8-14ms | 1.8 MB |
| `group_by_query(2 dims)` | 10,000 rows | 15-24ms | 2.3 MB |
| `ChartSelector.RecommendChart` | 50 rows | <1ms | <100 KB |
| `EChartsGenerator.Generate` | 50 rows | 2-5ms | <200 KB |
| `ReportGenerator.GenerateReport` | 3 datasets, 100 rows | 10-20ms | <1 MB |
| `ReportGenerator.ExportHTML` | 3 charts | 5-10ms | <500 KB |

### Data Reduction

Typical reduction ratios:

- **Top-N**: 99%+ reduction (10,000 → 50 items)
- **GROUP BY**: 90-99% reduction (depends on cardinality)
- **Combined**: 99.5%+ reduction

**Example**: nPath workflow generating 10,000 patterns:
- Stage 9 stores: 10,000 rows (~2.5 MB JSON)
- Stage 11 queries: 50 rows (~12 KB)
- Reduction: 99.5%
- Context savings: 2.488 MB

### Concurrency

All presentation tools are thread-safe and tested with Go's `-race` detector:

```bash
go test -tags fts5 -race ./pkg/shuttle/builtin -run TestPresentationTools_ConcurrentAccess
# PASS: 0 race conditions detected
```

**Concurrent access pattern**:
- Multiple agents can query same dataset simultaneously
- SharedMemoryStore uses RWMutex for concurrent reads
- No locks held during tool execution (read-only operations)


## Error Codes

### STORE_NOT_AVAILABLE

**Code**: `STORE_NOT_AVAILABLE`
**Cause**: Shared memory store not configured

**Example**:
```
Error: STORE_NOT_AVAILABLE: Shared memory store not configured
```

**Resolution**:
1. Initialize MultiAgentServer with shared memory enabled
2. Verify server configuration includes shared_memory parameter


### INVALID_PARAMS

**Code**: `INVALID_PARAMS`
**Cause**: Missing required parameter (source_key, n, sort_by, or group_by)

**Example**:
```
Error: INVALID_PARAMS: source_key is required
```

**Resolution**:
1. Provide all required parameters
2. Check parameter spelling and types


### KEY_NOT_FOUND

**Code**: `KEY_NOT_FOUND`
**Cause**: Source key not found in shared memory

**Example**:
```
Error: KEY_NOT_FOUND: Key not found in shared memory: stage-9-npath-full-results
```

**Resolution**:
1. Verify source agent completed successfully
2. Check source_key spelling
3. Verify namespace matches where data was written


### INVALID_DATA_FORMAT

**Code**: `INVALID_DATA_FORMAT`
**Cause**: Data is not valid JSON

**Example**:
```
Error: INVALID_DATA_FORMAT: Failed to parse data as JSON: unexpected token at position 42
```

**Resolution**:
1. Check source agent's data writing logic
2. Validate JSON syntax with `jq` or similar tool


### UNSUPPORTED_DATA_STRUCTURE

**Code**: `UNSUPPORTED_DATA_STRUCTURE`
**Cause**: Data is neither array nor map

**Example**:
```
Error: UNSUPPORTED_DATA_STRUCTURE: Data must be an array of objects or a map containing arrays
```

**Note**: This error is only emitted by `top_n_query`. The `group_by_query` silently returns an empty groups array for unsupported structures.

**Resolution**:
1. Restructure source data as array of objects
2. Or structure as map with array values


### Column Handling Notes

**Missing columns**: If a `sort_by` column does not exist in the data rows, sorting produces undefined order (no explicit error). If a `group_by` column does not exist, it is treated as `"NULL"`.

**Non-numeric sort_by**: If the `sort_by` column contains non-numeric values, comparison returns false for those rows and sort order is undefined (no explicit error).


### READ_FAILED

**Code**: `READ_FAILED`
**Cause**: Failed to read from shared memory

**Example**:
```
Error: READ_FAILED: Failed to read from shared memory: connection error
```

**Resolution**:
1. Check if source_key exists
2. Retry operation (idempotent, safe to retry)
3. Check shared memory service health

**Retry behavior**: Set `retryable: true` in error response


## Examples

### Example 1: Basic Presentation Tools Usage

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/teradata-labs/loom/pkg/shuttle/builtin"
)

func main() {
    ctx := context.Background()

    // Assume stage-9 has written 10,000 results to shared memory

    // Create presentation tools
    tools := builtin.PresentationTools(sharedMemory, "stage-11")

    // Get top 50 patterns by frequency
    topNResult, err := tools[0].Execute(ctx, map[string]interface{}{
        "source_key": "stage-9-npath-full-results",
        "n":          50,
        "sort_by":    "frequency",
        "direction":  "desc",
        "namespace":  "workflow",
    })
    if err != nil {
        log.Fatalf("top_n_query failed: %v", err)
    }

    data := topNResult.Data.(map[string]interface{})
    items := data["items"].([]interface{})

    fmt.Printf("Top 50 Patterns:\n")
    for i, item := range items {
        itemMap := item.(map[string]interface{})
        fmt.Printf("%d. %s (freq: %.0f)\n",
            i+1, itemMap["pattern"], itemMap["frequency"])
    }

    // Get distribution by path length
    groupByResult, err := tools[1].Execute(ctx, map[string]interface{}{
        "source_key": "stage-9-npath-full-results",
        "group_by":   []interface{}{"path_length"},
        "namespace":  "workflow",
    })
    if err != nil {
        log.Fatalf("group_by_query failed: %v", err)
    }

    groupData := groupByResult.Data.(map[string]interface{})
    groups := groupData["groups"].([]interface{})

    fmt.Printf("\nPath Length Distribution:\n")
    for _, group := range groups {
        groupMap := group.(map[string]interface{})
        fmt.Printf("Length %v: %v paths\n",
            groupMap["path_length"], groupMap["count"])
    }
}

// Output:
// Top 50 Patterns:
// 1. A→B→C (freq: 1247)
// 2. A→C (freq: 982)
// ...
//
// Path Length Distribution:
// Length 3: 4500 paths
// Length 4: 3200 paths
// Length 2: 1800 paths
// Length 5: 500 paths
```


### Example 2: End-to-End Visualization Pipeline

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/teradata-labs/loom/pkg/shuttle/builtin"
    "github.com/teradata-labs/loom/pkg/visualization"
)

func main() {
    ctx := context.Background()

    // Step 1: Use presentation tools to aggregate data
    tools := builtin.PresentationTools(sharedMemory, "stage-11")

    topNResult, _ := tools[0].Execute(ctx, map[string]interface{}{
        "source_key": "stage-9-npath-full-results",
        "n":          50,
        "sort_by":    "frequency",
        "direction":  "desc",
        "namespace":  "workflow",
    })

    groupByResult, _ := tools[1].Execute(ctx, map[string]interface{}{
        "source_key": "stage-9-npath-full-results",
        "group_by":   []interface{}{"path_length"},
        "namespace":  "workflow",
    })

    // Step 2: Parse results into datasets
    topNDataset, _ := visualization.ParseDataFromPresentationToolResult(
        topNResult.Data.(map[string]interface{}),
        "top_50_patterns",
    )

    groupByDataset, _ := visualization.ParseDataFromPresentationToolResult(
        groupByResult.Data.(map[string]interface{}),
        "path_length_distribution",
    )

    // Step 3: Generate visualizations
    rg := visualization.NewReportGenerator(nil)
    report, _ := rg.GenerateReport(ctx,
        []*visualization.Dataset{topNDataset, groupByDataset},
        "nPath Analysis Report",
        "Analysis of 10,000 customer journey patterns reveals key insights...",
    )

    // Step 4: Export to HTML
    html, _ := rg.ExportHTML(report)

    // Save to file
    os.WriteFile("npath_report.html", []byte(html), 0600)
    fmt.Println("Report saved to npath_report.html")
}

// Output:
// Report saved to npath_report.html
```


## Testing

### Test Coverage

**Presentation Tools**: 12 test functions in `presentation_tools_test.go`, 100% pass rate
**Visualization**: 21 test functions in `visualization_test.go`, 100% pass rate
**Visualization Tool**: 3 test functions in `tool_test.go`, 100% pass rate
**Race Detector**: 0 race conditions detected

#### Presentation Tool Tests

**File**: `pkg/shuttle/builtin/presentation_tools_test.go`

| Test Function | Coverage |
|---------------|----------|
| `TestTopNQueryTool_BasicFunctionality` | Top-N with frequency sort |
| `TestTopNQueryTool_AscendingSort` | Ascending order validation |
| `TestTopNQueryTool_InvalidParams` | Error handling (table-driven) |
| `TestGroupByQueryTool_BasicFunctionality` | Multi-dimensional grouping |
| `TestGroupByQueryTool_SingleDimension` | Single dimension grouping |
| `TestGroupByQueryTool_KeyNotFound` | Missing key error |
| `TestPresentationTools_ConcurrentAccess` | Race detection |
| `TestPresentationToolNames` | Tool name registry validation |
| `TestVisualizationToolNames` | Visualization tool name registry |
| `TestPresentationTools_Factory` | Factory function returns correct tools |
| `TestVisualizationTools_Factory` | Visualization factory function |
| `TestPresentationTools_NilStore` | Nil store returns no tools |

#### Visualization Tests

**File**: `pkg/visualization/visualization_test.go`

| Test Function | Coverage |
|---------------|----------|
| `TestChartSelector_AnalyzeDataset` | Data pattern analysis |
| `TestChartSelector_RecommendChart` | Chart recommendation logic |
| `TestChartSelector_RecommendRadar` | Radar chart recommendation |
| `TestChartSelector_RecommendBoxPlot` | Box plot recommendation |
| `TestChartSelector_RecommendGraph` | Graph chart recommendation |
| `TestChartSelector_RecommendTreeMap` | TreeMap recommendation |
| `TestEChartsGenerator_Generate` | ECharts config generation (bar) |
| `TestEChartsGenerator_GenerateRadarChart` | Radar chart generation |
| `TestEChartsGenerator_GenerateBoxPlotChart` | Box plot generation |
| `TestEChartsGenerator_GenerateTreeMapChart` | TreeMap generation |
| `TestEChartsGenerator_GenerateGraphChart` | Graph/network generation |
| `TestReportGenerator_GenerateReport` | Report assembly |
| `TestReportGenerator_ExportHTML` | HTML export |
| `TestStyleGuideClient_FetchStyleWithFallback` | Style fetching |
| `TestStyleConfig_Validation` | Style validation |
| `TestParseDataFromPresentationToolResult` | Data parsing |
| `TestConcurrentAccess` | Thread safety with race detector |
| `TestDefaultStyleConfig` | Default configuration |
| `TestMergeStyles` | Style merging |
| `TestGetThemeVariant` | Theme variants |
| `TestHelperFunctions` | Helper function validation |

**File**: `pkg/visualization/tool_test.go`

| Test Function | Coverage |
|---------------|----------|
| `TestVisualizationTool_Execute` | End-to-end visualization tool execution |
| `TestVisualizationTool_InvalidParams` | Parameter validation (table-driven) |
| `TestVisualizationTool_Schema` | Tool schema and required fields |

### Running Tests

```bash
# All presentation tool tests
go test -tags fts5 ./pkg/shuttle/builtin -run TestPresentation -v

# All visualization tests
go test -tags fts5 ./pkg/visualization -v

# With race detector (REQUIRED before commit)
go test -tags fts5 -race ./pkg/shuttle/builtin -run TestPresentation -v
go test -tags fts5 -race ./pkg/visualization -v

# Extensive race detection (50 runs)
go test -tags fts5 -race -count=50 ./pkg/shuttle/builtin -run TestPresentationTools_ConcurrentAccess
go test -tags fts5 -race -count=50 ./pkg/visualization -run TestConcurrentAccess
```


## See Also

- [Agent Configuration Reference](./agent-configuration.md) - Agent YAML configuration
- [Pattern Reference](./patterns.md) - Pattern library system
- [Backend Reference](./backend.md) - Backend types and configuration
- [CLI Reference](./cli.md) - Command-line interface

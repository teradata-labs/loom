
# Pattern Reference

Specification for Loom's pattern library system. Patterns encode domain knowledge as YAML templates for LLM-guided tool execution.

**Version**: v1.2.0


## Table of Contents

- [Quick Reference](#quick-reference)
- [Pattern YAML Schema](#pattern-yaml-schema)
  - [Required Fields](#required-fields)
  - [Optional Fields](#optional-fields)
  - [Templates Section](#templates-section)
  - [Examples Section](#examples-section)
- [Pattern Categories](#pattern-categories)
- [Library API](#library-api)
  - [NewLibrary](#newlibrary)
  - [Load](#load)
  - [ListAll](#listall)
  - [FilterByCategory](#filterbycategory)
  - [FilterByBackendType](#filterbybackendtype)
  - [Search](#search)
  - [ClearCache](#clearcache)
  - [Register](#register)
  - [AddSearchPath](#addsearchpath)
  - [SetPatternsDir](#setpatternsdir)
  - [FilterByDifficulty](#filterbydiffculty)
- [Orchestrator API](#orchestrator-api)
  - [NewOrchestrator](#neworchestrator)
  - [ClassifyIntent](#classifyintent)
  - [RecommendPattern](#recommendpattern)
  - [GetRoutingRecommendation](#getroutingrecommendation)
  - [PlanExecution](#planexecution)
  - [SetIntentClassifier](#setintentclassifier)
  - [SetExecutionPlanner](#setexecutionplanner)
  - [SetLLMProvider](#setllmprovider)
  - [GetLibrary](#getlibrary)
  - [RecordPatternUsage](#recordpatternusage)
  - [NewLLMIntentClassifier](#newllmintentclassifier)
- [Intent Categories](#intent-categories)
- [Hot Reload](#hot-reload)
  - [NewHotReloader](#newhotreloader)
  - [Start](#start)
  - [Stop](#stop)
  - [ManualReload](#manualreload)
  - [FormatForLLM](#formatforllm)
  - [CreatePattern RPC](#createpattern-rpc)
- [Template Syntax](#template-syntax)
- [Performance Characteristics](#performance-characteristics)
- [Error Handling](#error-handling)
- [Examples](#examples)
- [Testing](#testing)
- [See Also](#see-also)


## Quick Reference

### Pattern Categories

| Category | Description | Example Use Cases |
|----------|-------------|-------------------|
| `analytics` | Aggregations, metrics, reporting | Revenue by region, sales trends |
| `ml` | Machine learning workflows | Model training, prediction |
| `timeseries` | Time-based analysis | Trend detection, forecasting |
| `text` | Natural language processing | Sentiment analysis, summarization |
| `data_quality` | Validation and quality checks | Schema validation, null checks |
| `rest_api` | API interaction patterns | REST calls, authentication |
| `document` | Document search and processing | Vector search, embeddings |
| `etl` | Extract, transform, load workflows | Incremental load, data sync |
| `prompt_engineering` | Prompt design patterns | Prompt templates, few-shot examples |
| `code` | Code generation and analysis | Code generation, refactoring |
| `debugging` | Debugging and diagnostics | Error analysis, troubleshooting |
| `vision` | Image and visual analysis | Image description, chart analysis |
| `evaluation` | Evaluation and assessment | Quality scoring, benchmarking |

### Intent Categories

| Intent | Description | Tool Guidance |
|--------|-------------|---------------|
| `IntentSchemaDiscovery` | Exploring data structure | Use schema discovery tools |
| `IntentRelationshipQuery` | Analyzing relationships | Use relationship analysis tools |
| `IntentDataQuality` | Quality validation | Use validation tools |
| `IntentDataTransform` | ETL operations | Use transformation tools |
| `IntentAnalytics` | Metrics and reporting | Use aggregation/query tools |
| `IntentQueryGeneration` | Query creation | Use code generation tools |
| `IntentDocumentSearch` | Document retrieval | Use vector search tools |
| `IntentAPICall` | API interaction | Use HTTP client tools |
| `IntentUnknown` | Cannot classify | Use general-purpose tools |

### Library Functions

| Function | Purpose | Returns |
|----------|---------|---------|
| `NewLibrary(fs, path)` | Create pattern library | `*Library` |
| `Load(name)` | Load pattern by name | `*Pattern, error` |
| `ListAll()` | Get all pattern summaries | `[]PatternSummary` |
| `FilterByCategory(cat)` | Filter by category | `[]PatternSummary` |
| `FilterByBackendType(typ)` | Filter by backend | `[]PatternSummary` |
| `FilterByDifficulty(d)` | Filter by difficulty | `[]PatternSummary` |
| `Search(query)` | Free-text search (ranked) | `[]PatternSummary` |
| `ClearCache()` | Clear pattern cache | - |
| `Register(pattern)` | Add pattern to cache | - |
| `AddSearchPath(path)` | Add custom search path | - |
| `SetPatternsDir(dir)` | Update patterns directory | - |

### Orchestrator Functions

| Function | Purpose | Returns |
|----------|---------|---------|
| `NewOrchestrator(library)` | Create orchestrator | `*Orchestrator` |
| `ClassifyIntent(msg, ctx)` | Classify user intent | `IntentCategory, float64` |
| `RecommendPattern(msg, intent)` | Recommend pattern (hybrid keyword + optional LLM) | `string, float64` |
| `GetRoutingRecommendation(intent)` | Get tool guidance | `string` |
| `PlanExecution(intent, msg, ctx)` | Generate execution plan | `*ExecutionPlan, error` |
| `SetIntentClassifier(fn)` | Override classifier | - |
| `SetExecutionPlanner(fn)` | Override execution planner | - |
| `SetLLMProvider(provider)` | Set LLM for hybrid re-ranking | - |
| `GetLibrary()` | Get underlying pattern library | `*Library` |
| `RecordPatternUsage(...)` | Record usage metrics | - |
| `NewLLMIntentClassifier(config)` | Create LLM-based classifier | `IntentClassifierFunc` |


## Pattern YAML Schema

### Required Fields

#### name

**Type**: `string`
**Required**: Yes
**Constraints**: Must be unique within library, alphanumeric with underscores

**Description**: Unique identifier for pattern lookup.

**Example**:
```yaml
name: revenue_aggregation
```


#### description

**Type**: `string`
**Required**: Yes
**Constraints**: None

**Description**: Human-readable description of what the pattern does.

**Example**:
```yaml
description: Aggregate revenue metrics by dimension with grouping
```


#### category

**Type**: `string`
**Required**: Yes
**Allowed values**: See [Pattern Categories](#pattern-categories)

**Description**: Pattern category for organizational grouping and filtering.

**Example**:
```yaml
category: analytics
```


#### backend_type

**Type**: `string`
**Required**: Yes
**Common values**: `sql`, `rest_api`, `document`, `text`, `workflow`, `vision`, `evaluation`, `debugging`, `postgres`, `mcp`, `file`, `graphql`

**Description**: Backend type this pattern applies to. Free-form string; the values listed above are conventions used in the existing pattern library.

**Example**:
```yaml
backend_type: sql
```


### Optional Fields

#### difficulty

**Type**: `string`
**Default**: `beginner`
**Allowed values**: `beginner`, `intermediate`, `advanced`

**Description**: Complexity level for pattern selection and documentation.

**Example**:
```yaml
difficulty: intermediate
```


#### backend_function

**Type**: `string`
**Default**: None
**Constraints**: Must match registered backend function name

**Description**: Specific backend function to invoke when pattern is used.

**Example**:
```yaml
backend_function: execute_aggregation_query
```


#### title

**Type**: `string`
**Required**: No (recommended)
**Constraints**: None

**Description**: Human-readable title for the pattern. Used in catalog listings and LLM formatting.

**Example**:
```yaml
title: Revenue Aggregation
```


#### use_cases

**Type**: `[]string`
**Default**: `[]`
**Constraints**: None

**Description**: Example use case descriptions for pattern matching and documentation.

**Example**:
```yaml
use_cases:
  - "revenue by region"
  - "sales aggregation"
  - "group by analysis"
```


#### related_patterns

**Type**: `[]string`
**Default**: `[]`

**Description**: Names of related patterns for cross-referencing.

**Example**:
```yaml
related_patterns:
  - sales_trend_analysis
  - kpi_dashboard
```


#### parameters

**Type**: `[]Parameter`
**Default**: `[]`

**Description**: Parameter definitions used in pattern templates. Each parameter includes name, type, required flag, description, example, and optional default value.

**Parameter struct**:
```go
type Parameter struct {
    Name         string `yaml:"name"`
    Type         string `yaml:"type"`         // "string", "number", "array", "object"
    Required     bool   `yaml:"required"`
    Description  string `yaml:"description"`
    Example      string `yaml:"example"`
    DefaultValue string `yaml:"default,omitempty"`
}
```

**Example**:
```yaml
parameters:
  - name: dimension
    type: string
    required: true
    description: Column to group by
    example: region
  - name: limit
    type: number
    required: false
    description: Max rows to return
    default: "100"
```


#### common_errors

**Type**: `[]CommonError`
**Default**: `[]`

**Description**: Frequently encountered errors and solutions. Included in `FormatForLLM()` output (up to 3) to help the LLM avoid mistakes.

**Example**:
```yaml
common_errors:
  - error: "Column not found"
    cause: "Misspelled column name"
    solution: "Verify column exists with schema discovery"
```


#### best_practices

**Type**: `string`
**Default**: `""`

**Description**: Best practices text for this pattern. Included in `FormatForLLM()` output.

**Example**:
```yaml
best_practices: |
  Always validate column names before executing.
  Use LIMIT for large datasets.
```


#### syntax

**Type**: `*Syntax` (optional)

**Description**: Backend-specific syntax documentation (e.g., nPath pattern operators, JSONPath syntax).

**Example**:
```yaml
syntax:
  description: "nPath pattern matching operators"
  operators:
    - symbol: "."
      meaning: "Match any single event"
      example: "A.B matches A followed by B"
    - symbol: "*"
      meaning: "Match zero or more events"
      example: "A* matches zero or more A events"
```


### Templates Section

**Type**: `map[string]Template`
**Required**: Yes (at least one template)
**Keys**: `basic`, `advanced`, `optimized`, or custom names

**Description**: Named templates with content, descriptions, and metadata. Templates support both simple string format and rich object format with additional fields.

**Template struct fields**:
- `description` (`string`, optional) - Description of template purpose
- `content` (`string`) - Template content (SQL, JSON, etc.) with `{{.variable}}` placeholders
- `sql` (`string`) - Alternative field name for `content` (for SQL patterns)
- `required_parameters` (`[]string`, optional) - List of required parameter names
- `output_format` (`string`, optional) - Output format: `table`, `json`, `text`

**Simple string format** (auto-mapped to `content`):
```yaml
templates:
  basic: |
    SELECT {{.dimension}}, SUM({{.metric}}) as total
    FROM {{.table}}
    GROUP BY {{.dimension}}
    ORDER BY total DESC
```

**Rich object format**:
```yaml
templates:
  basic:
    description: Simple aggregation query
    sql: |
      SELECT {{.dimension}}, SUM({{.metric}}) as total
      FROM {{.table}}
      GROUP BY {{.dimension}}
      ORDER BY total DESC
    required_parameters:
      - dimension
      - metric
      - table
    output_format: table

  optimized:
    description: Performance-optimized with index hints
    sql: |
      SELECT /*+ INDEX({{.table}} {{.index_name}}) */
        {{.dimension}}, SUM({{.metric}}) as total
      FROM {{.table}}
      GROUP BY {{.dimension}}
      ORDER BY total DESC
    required_parameters:
      - dimension
      - metric
      - table
      - index_name
```

**See**: [Template Syntax](#template-syntax) for full syntax reference


### Examples Section

**Type**: `[]map[string]interface{}`
**Required**: No (recommended)
**Constraints**: None

**Description**: Example parameter combinations with expected output.

**Schema**:
```yaml
examples:
  - name: string                # Example name
    description: string         # Example description
    parameters: map[string]any  # Template variables
    expected_result: string     # Optional expected result
    notes: string               # Optional notes
```

**Example**:
```yaml
examples:
  - name: Revenue by region
    description: Aggregate revenue grouped by geographic region
    parameters:
      dimension: region
      metric: revenue
      table: sales
    expected_result: |
      region  | total
      --------|--------
      West    | 2400000
      East    | 2100000

  - name: Sales by product
    description: Count quantities sold by product
    parameters:
      dimension: product_id
      metric: quantity
      table: orders
```


## Pattern Categories

### analytics

**Purpose**: Aggregations, metrics, reporting
**Backend types**: `sql`, `rest_api`
**Common patterns**: Revenue aggregation, sales trends, KPI dashboards


### ml

**Purpose**: Machine learning workflows
**Backend types**: `rest_api`, `document`
**Common patterns**: Model training, prediction, feature engineering


### timeseries

**Purpose**: Time-based analysis
**Backend types**: `sql`, `rest_api`
**Common patterns**: Trend detection, forecasting, anomaly detection


### text

**Purpose**: Natural language processing
**Backend types**: `document`, `rest_api`
**Common patterns**: Sentiment analysis, summarization, entity extraction


### data_quality

**Purpose**: Validation and quality checks
**Backend types**: `sql`, `rest_api`, `file`
**Common patterns**: Schema validation, null checks, outlier detection


### rest_api

**Purpose**: API interaction patterns
**Backend types**: `rest_api`, `mcp`
**Common patterns**: REST calls, authentication, pagination


### document

**Purpose**: Document search and processing
**Backend types**: `document`, `mcp`
**Common patterns**: Vector search, embeddings, document retrieval


### etl

**Purpose**: Extract, transform, load workflows
**Backend types**: `sql`, `rest_api`, `file`
**Common patterns**: Incremental load, data sync, schema migration


### prompt_engineering

**Purpose**: Prompt design patterns
**Backend types**: Various
**Common patterns**: Prompt templates, few-shot examples, chain-of-thought


### code

**Purpose**: Code generation and analysis
**Backend types**: Various
**Common patterns**: Code generation, refactoring, migration


### debugging

**Purpose**: Debugging and diagnostics
**Backend types**: Various
**Common patterns**: Error analysis, troubleshooting, log analysis


### vision

**Purpose**: Image and visual analysis
**Backend types**: Various
**Common patterns**: Image description, chart analysis


### evaluation

**Purpose**: Evaluation and assessment
**Backend types**: Various
**Common patterns**: Quality scoring, benchmarking


## Library API

### NewLibrary

```go
func NewLibrary(fs *embed.FS, path string) *Library
```

**Description**: Create new pattern library from filesystem or embedded FS.

**Parameters**:
- `fs` (`*embed.FS`) - Embedded filesystem (nil for disk-based)
- `path` (`string`) - Directory path containing pattern YAML files

**Returns**: `*Library`

**Example (filesystem)**:
```go
import "github.com/teradata-labs/loom/pkg/patterns"

library := patterns.NewLibrary(nil, "./patterns")
```

**Example (embedded)**:
```go
import (
    "embed"
    "github.com/teradata-labs/loom/pkg/patterns"
)

//go:embed patterns/*.yaml
var patternFS embed.FS

library := patterns.NewLibrary(&patternFS, "patterns")
```

**Thread safety**: Safe for concurrent use after creation


### Load

```go
func (l *Library) Load(name string) (*Pattern, error)
```

**Description**: Load pattern by name. Results are cached after first load.

**Parameters**:
- `name` (`string`) - Pattern name (matches `name` field in YAML)

**Returns**:
- `*Pattern` - Loaded pattern
- `error` - See [Error Codes](#error-codes)

**Errors** (returned as `fmt.Errorf` messages, not sentinel errors):
| Error message | Condition |
|---------------|-----------|
| `"pattern not found: %s"` | Pattern name doesn't exist in library |
| `"failed to parse pattern %s: ..."` | Pattern file has invalid YAML syntax |
| `"pattern path outside patterns directory: %s"` | Path traversal attempt detected |

**Example**:
```go
pattern, err := library.Load("revenue_aggregation")
if err != nil {
    log.Fatalf("Failed to load pattern: %v", err)
}

fmt.Println(pattern.Name)        // "revenue_aggregation"
fmt.Println(pattern.Description) // "Aggregate revenue metrics..."
fmt.Println(pattern.Templates["basic"].GetSQL()) // Template content string
```

**Performance**: Cached after first load (sub-millisecond retrieval)

**Thread safety**: Safe for concurrent use


### ListAll

```go
func (l *Library) ListAll() []PatternSummary
```

**Description**: Get metadata summaries for all available patterns. Results are cached after first call. Scans both embedded FS and filesystem, plus any dynamically registered patterns.

**Returns**: `[]PatternSummary` - All pattern summaries

**PatternSummary schema**:
```go
type PatternSummary struct {
    Name            string   `json:"name"`
    Title           string   `json:"title"`
    Description     string   `json:"description"` // Truncated to 200 chars
    Category        string   `json:"category"`
    Difficulty      string   `json:"difficulty"`
    BackendType     string   `json:"backend_type"`
    UseCases        []string `json:"use_cases"`
    BackendFunction string   `json:"backend_function,omitempty"`
}
```

**Example**:
```go
all := library.ListAll()
fmt.Printf("Found %d patterns\n", len(all))

for _, summary := range all {
    fmt.Printf("- %s (%s)\n", summary.Name, summary.Category)
}
```

**Performance**: Cached after first scan. First call walks filesystem; subsequent calls return cached index.

**Thread safety**: Safe for concurrent use


### FilterByCategory

```go
func (l *Library) FilterByCategory(category string) []PatternSummary
```

**Description**: Filter patterns by category. Case-insensitive matching. Returns all patterns if category is empty.

**Parameters**:
- `category` (`string`) - Category to filter by (see [Pattern Categories](#pattern-categories))

**Returns**: `[]PatternSummary` - Pattern summaries matching category

**Example**:
```go
analytics := library.FilterByCategory("analytics")
fmt.Printf("Found %d analytics patterns\n", len(analytics))
```

**Performance**: O(n) linear scan over `ListAll()` results

**Thread safety**: Safe for concurrent use


### FilterByBackendType

```go
func (l *Library) FilterByBackendType(backendType string) []PatternSummary
```

**Description**: Filter patterns by backend type. Case-insensitive matching. Returns all patterns if backendType is empty.

**Parameters**:
- `backendType` (`string`) - Backend type to filter by

**Returns**: `[]PatternSummary` - Pattern summaries matching backend type

**Example**:
```go
sqlPatterns := library.FilterByBackendType("sql")
fmt.Printf("Found %d SQL patterns\n", len(sqlPatterns))
```

**Performance**: O(n) linear scan over `ListAll()` results

**Thread safety**: Safe for concurrent use


### Search

```go
func (l *Library) Search(query string) []PatternSummary
```

**Description**: Free-text search across pattern metadata with relevance ranking. Tokenizes the query into keywords, filters stop words, and scores results by keyword match rate with boosts for name/title matches.

**Parameters**:
- `query` (`string`) - Search query (empty returns all patterns)

**Returns**: `[]PatternSummary` - Pattern summaries sorted by relevance score (highest first)

**Example**:
```go
results := library.Search("revenue aggregation")
fmt.Printf("Found %d matching patterns\n", len(results))
```

**Search behavior**:
- Case-insensitive
- Tokenizes query on whitespace, commas, semicolons, hyphens, underscores
- Filters stop words and terms shorter than 3 characters
- Searches: name, title, description, backend_function, use_cases[]
- Ranked by relevance score (keyword match rate + name/title boosts)

**Performance**: O(n*m) where n=patterns, m=keywords

**Thread safety**: Safe for concurrent use


### ClearCache

```go
func (l *Library) ClearCache()
```

**Description**: Clear pattern cache to force reload from disk on next Load().

**Use cases**:
- Testing with modified patterns
- Manual hot-reload trigger
- Memory management

**Example**:
```go
// Modify pattern file on disk
library.ClearCache()

// Next Load() reads from disk
pattern, _ := library.Load("revenue_aggregation")
```

**Performance**: O(1) operation

**Thread safety**: Safe for concurrent use (uses RWMutex)


### Register

```go
func (l *Library) Register(pattern *Pattern)
```

**Description**: Add a dynamically-created pattern directly to the library cache. Enables programmatic registration without requiring a file on disk. If a pattern with the same name already exists, it is overwritten. Invalidates the cached index so `ListAll()` re-scans.

**Parameters**:
- `pattern` (`*Pattern`) - Pattern to register

**Example**:
```go
pattern := &patterns.Pattern{
    Name:        "custom_query",
    Title:       "Custom Query Pattern",
    Description: "Dynamically registered pattern",
    Category:    "analytics",
    BackendType: "sql",
}

library.Register(pattern)
```

**Thread safety**: Safe for concurrent use


### AddSearchPath

```go
func (l *Library) AddSearchPath(path string)
```

**Description**: Add a custom search path for pattern discovery. Paths are relative to the patterns directory (filesystem) or embedded FS root.

**Parameters**:
- `path` (`string`) - Subdirectory path to search for pattern files

**Example**:
```go
library.AddSearchPath("custom/analytics")
library.AddSearchPath("vendor/patterns")
```

**Thread safety**: Safe for concurrent use


### SetPatternsDir

```go
func (l *Library) SetPatternsDir(dir string)
```

**Description**: Update the filesystem patterns directory. Used by `LoadPatterns` RPC to dynamically set the patterns source. When the directory changes, the pattern index is invalidated so the next `ListAll()` call re-indexes.

**Parameters**:
- `dir` (`string`) - New patterns directory path

**Example**:
```go
library.SetPatternsDir("/opt/loom/patterns")
```

**Thread safety**: Safe for concurrent use


### FilterByDifficulty

```go
func (l *Library) FilterByDifficulty(difficulty string) []PatternSummary
```

**Description**: Filter patterns by difficulty level. Case-insensitive matching. Returns all patterns if difficulty is empty.

**Parameters**:
- `difficulty` (`string`) - Difficulty level: `beginner`, `intermediate`, `advanced`

**Returns**: `[]PatternSummary` - Pattern summaries matching difficulty

**Example**:
```go
beginnerPatterns := library.FilterByDifficulty("beginner")
fmt.Printf("Found %d beginner patterns\n", len(beginnerPatterns))
```

**Performance**: O(n) linear scan over `ListAll()` results

**Thread safety**: Safe for concurrent use


## Orchestrator API

### NewOrchestrator

```go
func NewOrchestrator(library *Library) *Orchestrator
```

**Description**: Create intent classifier and pattern recommender.

**Parameters**:
- `library` (`*Library`) - Pattern library to use for recommendations

**Returns**: `*Orchestrator`

**Example**:
```go
orchestrator := patterns.NewOrchestrator(library)
```

**Thread safety**: Safe for concurrent use after creation


### ClassifyIntent

```go
func (o *Orchestrator) ClassifyIntent(message string, context map[string]interface{}) (IntentCategory, float64)
```

**Description**: Classify user message into intent category.

**Parameters**:
- `message` (`string`) - User message to classify
- `context` (`map[string]interface{}`) - Additional context (user_id, session_data, etc.)

**Returns**:
- `IntentCategory` - Classified intent (see [Intent Categories](#intent-categories))
- `float64` - Confidence score (0.0-1.0)

**Classification algorithm**:
- Keyword matching on intent-specific terms
- Context-aware scoring
- Default fallback to `IntentUnknown`

**Example**:
```go
intent, confidence := orchestrator.ClassifyIntent(
    "Show me total revenue by region",
    map[string]interface{}{"user_id": "user123"},
)

fmt.Printf("Intent: %v (%.2f confidence)\n", intent, confidence)
// Output: Intent: analytics (0.80 confidence)
```

**Performance**: O(1) keyword matching

**Thread safety**: Safe for concurrent use


### RecommendPattern

```go
func (o *Orchestrator) RecommendPattern(message string, intent IntentCategory) (string, float64)
```

**Description**: Recommend best pattern for user message and intent.

**Parameters**:
- `message` (`string`) - User message
- `intent` (`IntentCategory`) - Classified intent

**Returns**:
- `string` - Pattern name
- `float64` - Confidence score (0.0-1.0)

**Recommendation algorithm**:
1. Filter patterns by intent-appropriate category
2. Match message against pattern use_cases[]
3. Score by keyword overlap
4. Return highest-scoring pattern

**Example**:
```go
patternName, confidence := orchestrator.RecommendPattern(
    "Show me revenue by region",
    patterns.IntentAnalytics,
)

if confidence > 0.7 {
    pattern, _ := library.Load(patternName)
    fmt.Printf("Using pattern: %s\n", pattern.Name)
}
```

**Performance**: O(n*m) where n=patterns, m=message length

**Thread safety**: Safe for concurrent use


### GetRoutingRecommendation

```go
func (o *Orchestrator) GetRoutingRecommendation(intent IntentCategory) string
```

**Description**: Get tool guidance string for LLM system prompt.

**Parameters**:
- `intent` (`IntentCategory`) - Classified intent

**Returns**: `string` - Tool guidance text

**Example**:
```go
guidance := orchestrator.GetRoutingRecommendation(patterns.IntentAnalytics)
fmt.Println(guidance)
// Output: "Use aggregation and query tools to analyze metrics"

// Include in LLM system prompt
systemPrompt := fmt.Sprintf(`
You are an SQL agent.

Routing guidance: %s

Available tools: execute_query, get_schema
`, guidance)
```

**Performance**: O(1) map lookup

**Thread safety**: Safe for concurrent use


### PlanExecution

```go
func (o *Orchestrator) PlanExecution(intent IntentCategory, message string, context map[string]interface{}) (*ExecutionPlan, error)
```

**Description**: Generate execution plan from intent and message.

**Parameters**:
- `intent` (`IntentCategory`) - Classified intent
- `message` (`string`) - User message
- `context` (`map[string]interface{}`) - Execution context

**Returns**:
- `*ExecutionPlan` - Generated execution plan
- `error` - See [Error Codes](#error-codes)

**ExecutionPlan schema**:
```go
type ExecutionPlan struct {
    Intent      IntentCategory `json:"intent"`
    Description string         `json:"description"`
    Steps       []PlannedStep  `json:"steps"`
    Reasoning   string         `json:"reasoning"`
    PatternName string         `json:"pattern_name,omitempty"`
}

type PlannedStep struct {
    ToolName    string            `json:"tool_name"`
    Params      map[string]string `json:"params"`
    Description string            `json:"description"`
    PatternHint string            `json:"pattern_hint,omitempty"`
}
```

**Example**:
```go
plan, err := orchestrator.PlanExecution(
    patterns.IntentAnalytics,
    "Show me revenue by region",
    map[string]interface{}{"user_context": "sales_team"},
)

if err != nil {
    log.Fatalf("Planning failed: %v", err)
}

fmt.Println(plan.Description)
// Output: "Generate and execute analytics"

for i, step := range plan.Steps {
    fmt.Printf("%d. %s: %s\n", i+1, step.ToolName, step.Description)
}
// Output:
// 1. execute_query: Execute analytics query
```

**Performance**: O(1) plan generation

**Thread safety**: Safe for concurrent use


### SetIntentClassifier

```go
func (o *Orchestrator) SetIntentClassifier(classifier func(string, map[string]interface{}) (IntentCategory, float64))
```

**Description**: Override default intent classifier with custom function.

**Parameters**:
- `classifier` (`func(string, map[string]interface{}) (IntentCategory, float64)`) - Custom classifier function

**Use cases**:
- LLM-based classification
- Domain-specific intent detection
- ML model integration

**Example**:
```go
customClassifier := func(msg string, ctx map[string]interface{}) (patterns.IntentCategory, float64) {
    // Use LLM for classification
    if strings.Contains(strings.ToLower(msg), "teradata") {
        return patterns.IntentAnalytics, 0.95
    }
    return patterns.IntentUnknown, 0.0
}

orchestrator.SetIntentClassifier(customClassifier)
```

**Thread safety**: Not safe during concurrent use (set during initialization)


### SetExecutionPlanner

```go
func (o *Orchestrator) SetExecutionPlanner(planner ExecutionPlannerFunc)
```

**Description**: Override default execution planner with custom function. Backends can provide domain-specific planners for optimized execution strategies.

**Parameters**:
- `planner` (`ExecutionPlannerFunc`) - Custom planner: `func(IntentCategory, string, map[string]interface{}) (*ExecutionPlan, error)`

**Thread safety**: Not safe during concurrent use (set during initialization)


### SetLLMProvider

```go
func (o *Orchestrator) SetLLMProvider(provider types.LLMProvider)
```

**Description**: Set the LLM provider for pattern re-ranking. When set, enables a hybrid approach: fast keyword matching + LLM re-ranking for ambiguous cases where top candidates have close scores.

**Parameters**:
- `provider` (`types.LLMProvider`) - LLM provider for re-ranking

**Thread safety**: Not safe during concurrent use (set during initialization)


### GetLibrary

```go
func (o *Orchestrator) GetLibrary() *Library
```

**Description**: Get the underlying pattern library.

**Returns**: `*Library` - The library used by this orchestrator

**Thread safety**: Safe for concurrent use


### RecordPatternUsage

```go
func (o *Orchestrator) RecordPatternUsage(
    ctx context.Context,
    patternName string,
    agentID string,
    success bool,
    costUSD float64,
    latency time.Duration,
    errorType string,
    llmProvider string,
    llmModel string,
)
```

**Description**: Record pattern usage metrics to the effectiveness tracker. Should be called after a pattern is executed to capture success/failure, cost, latency. Extracts variant and domain from context using `GetPatternMetadata()`. No-op if no tracker is configured (via `WithTracker()`).

**Parameters**:
- `ctx` (`context.Context`) - Context containing pattern metadata (variant, domain)
- `patternName` (`string`) - Name of the executed pattern
- `agentID` (`string`) - ID of the executing agent
- `success` (`bool`) - Whether execution succeeded
- `costUSD` (`float64`) - Cost of execution in USD
- `latency` (`time.Duration`) - Execution duration
- `errorType` (`string`) - Type of error if failed (empty if success)
- `llmProvider` (`string`) - LLM provider used (e.g., "anthropic", "bedrock")
- `llmModel` (`string`) - LLM model used

**Thread safety**: Safe for concurrent use


### NewLLMIntentClassifier

```go
func NewLLMIntentClassifier(config *LLMClassifierConfig) IntentClassifierFunc
```

**Description**: Create an LLM-based intent classifier. Returns an `IntentClassifierFunc` that can be plugged into the orchestrator via `SetIntentClassifier()`. Uses the LLM to classify intent with higher accuracy than keyword matching.

**Parameters**:
- `config` (`*LLMClassifierConfig`) - LLM classifier configuration

**Returns**: `IntentClassifierFunc` - Pluggable classifier function

**Example**:
```go
classifier := patterns.NewLLMIntentClassifier(&patterns.LLMClassifierConfig{
    // Configure with LLM provider
})

orchestrator.SetIntentClassifier(classifier)
```


## Intent Categories

IntentCategory is a `string` type (not integer). Values are human-readable strings.

### IntentSchemaDiscovery

**Value**: `"schema_discovery"`
**Description**: Exploring data structure (tables, columns, relationships)
**Tool guidance**: Use schema discovery tools with caching

**Trigger keywords**: "what tables", "list tables", "show tables", "what columns", "schema", "table structure", "describe"


### IntentRelationshipQuery

**Value**: `"relationship_query"`
**Description**: Analyzing relationships between entities
**Tool guidance**: Use relationship inference tools with FK detection

**Trigger keywords**: "related", "foreign key", "relationship", "connected to", "references", "joins"


### IntentDataQuality

**Value**: `"data_quality"`
**Description**: Quality validation and data profiling
**Tool guidance**: Use validation tools and quality check workflows

**Trigger keywords**: "data quality", "duplicates", "null", "completeness", "validate", "check quality", "integrity"


### IntentDataTransform

**Value**: `"data_transform"`
**Description**: ETL operations and data transformation
**Tool guidance**: Use ETL workflow patterns with validation gates

**Trigger keywords**: "move data", "copy", "load data", "extract", "transform", "etl", "migrate", "transfer"


### IntentAnalytics

**Value**: `"analytics"`
**Description**: Metrics, aggregations, and reporting
**Tool guidance**: Validate and estimate cost before execution; use pattern library for advanced aggregations

**Trigger keywords**: "aggregate", "sum", "count", "average", "group by", "analyze", "report", "metrics", "statistics"


### IntentQueryGeneration

**Value**: `"query_generation"`
**Description**: Generate code or queries
**Tool guidance**: Validate syntax and estimate cost before execution; use patterns for structure

**Trigger keywords**: "write query", "generate query", "query for", "select", "find", "get data"


### IntentDocumentSearch

**Value**: `"document_search"`
**Description**: Document retrieval and search
**Tool guidance**: Use appropriate indexing and search patterns (full-text, vector, hybrid)

**Trigger keywords**: "search document", "find in document", "document query", "text search", "full text"


### IntentAPICall

**Value**: `"api_call"`
**Description**: External API interactions
**Tool guidance**: Validate request structure; use retry patterns for transient failures

**Trigger keywords**: "api call", "http request", "rest api", "endpoint", "webhook"


### IntentUnknown

**Value**: `"unknown"`
**Description**: Cannot classify intent
**Tool guidance**: Use general-purpose tools

**Trigger keywords**: None (default fallback, confidence 0.0)


## Hot Reload

### NewHotReloader

```go
func NewHotReloader(library *Library, config HotReloadConfig) (*HotReloader, error)
```

**Description**: Create automatic pattern file watcher for hot-reload.

**Parameters**:
- `library` (`*Library`) - Pattern library to reload
- `config` (`HotReloadConfig`) - Hot-reload configuration

**HotReloadConfig schema**:
```go
type HotReloadConfig struct {
    Enabled    bool                  // Enable hot-reload (default: false)
    DebounceMs int                   // Debounce delay in milliseconds (default: 500)
    Logger     *zap.Logger           // Logger for reload events
    OnUpdate   PatternUpdateCallback // Callback for pattern updates (optional)
}

// PatternUpdateCallback signature:
// func(eventType string, patternName string, filePath string, err error)
// eventType: "create", "modify", "delete", "validation_failed"
```

**Returns**:
- `*HotReloader` - Hot-reloader instance
- `error` - Configuration validation errors

**Example**:
```go
import (
    "github.com/teradata-labs/loom/pkg/patterns"
    "go.uber.org/zap"
)

logger, _ := zap.NewProduction()

hotReloader, err := patterns.NewHotReloader(library, patterns.HotReloadConfig{
    Enabled:    true,
    DebounceMs: 500,
    Logger:     logger,
})

if err != nil {
    log.Fatalf("Hot-reload setup failed: %v", err)
}
```

**Thread safety**: Safe for concurrent use after Start()


### Start

```go
func (hr *HotReloader) Start(ctx context.Context) error
```

**Description**: Start watching for pattern file changes.

**Parameters**:
- `ctx` (`context.Context`) - Context for cancellation

**Returns**: `error` - Filesystem watcher errors

**Behavior**:
- Watches all `.yaml` files in library path
- Debounces rapid changes (default 500ms)
- Validates patterns before reload
- Rejects invalid patterns (logs error, continues running)
- Atomic file operations (no partial reads)

**Example**:
```go
ctx := context.Background()

err := hotReloader.Start(ctx)
if err != nil {
    log.Fatalf("Failed to start hot-reload: %v", err)
}

defer func() { _ = hotReloader.Stop() }()

// Patterns automatically reload when .yaml files change
// Invalid patterns are rejected and logged
```

**Performance**: Debounced reloads take 5-15ms per pattern

**Thread safety**: Safe for concurrent use


### Stop

```go
func (hr *HotReloader) Stop() error
```

**Description**: Stop watching for pattern file changes. Idempotent -- safe to call multiple times. Waits up to 5 seconds for watch loop to finish before timing out.

**Returns**: `error` - Error from closing the filesystem watcher

**Example**:
```go
defer func() {
    if err := hotReloader.Stop(); err != nil {
        log.Printf("Error stopping hot-reloader: %v", err)
    }
}()
```

**Thread safety**: Safe for concurrent use


### ManualReload

```go
func (hr *HotReloader) ManualReload(patternName string) error
```

**Description**: Trigger a manual reload of a specific pattern. Useful for programmatic reload (e.g., after API-based pattern creation). Validates the pattern before reloading.

**Parameters**:
- `patternName` (`string`) - Name of the pattern to reload

**Returns**: `error` - File not found or validation errors

**Errors**:
| Error message | Condition |
|---------------|-----------|
| `"pattern file not found: %s"` | Pattern YAML file not found in any search path |
| `"validation failed: ..."` | Pattern file fails validation |

**Example**:
```go
err := hotReloader.ManualReload("revenue_aggregation")
if err != nil {
    log.Printf("Manual reload failed: %v", err)
}
```

**Thread safety**: Safe for concurrent use


### FormatForLLM

```go
func (p *Pattern) FormatForLLM() string
```

**Description**: Format the pattern for LLM injection. Returns a concise, actionable representation optimized for token efficiency (target: <2000 tokens per pattern). Includes title, description, use cases, parameters, up to 2 templates, best practices, and common errors.

**Returns**: `string` - Markdown-formatted pattern text for LLM system prompts

**Example**:
```go
pattern, _ := library.Load("revenue_aggregation")
llmText := pattern.FormatForLLM()
// Include in system prompt for the LLM
```

**Thread safety**: Safe for concurrent use (read-only)


### CreatePattern RPC

```proto
rpc CreatePattern(CreatePatternRequest) returns (CreatePatternResponse)
```

**Description**: Create pattern file via gRPC API with automatic hot-reload detection.

**Request**:
```proto
message CreatePatternRequest {
  string agent_id = 1;     // Required: Agent ID
  string name = 2;         // Required: Pattern name
  string yaml_content = 3; // Required: Pattern YAML
}
```

**Response**:
```proto
message CreatePatternResponse {
  bool success = 1;          // Pattern created successfully
  string pattern_name = 2;   // Created pattern name
  string error = 3;          // Error message (if failed)
  string file_path = 4;      // File path where pattern was written
}
```

**Errors**:
| Code | Error | Condition |
|------|-------|-----------|
| `INVALID_ARGUMENT` | `agent_id empty` | agent_id not provided |
| `INVALID_ARGUMENT` | `invalid yaml` | yaml_content has syntax errors |
| `INVALID_ARGUMENT` | `missing required field` | Pattern missing required field |
| `NOT_FOUND` | `agent not found` | agent_id doesn't exist |
| `ALREADY_EXISTS` | `pattern exists` | Pattern name already exists |

**Example (Go client)**:
```go
import loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"

patternYAML := `
name: my_pattern
description: Custom pattern
category: analytics
backend_type: sql
templates:
  basic: |
    SELECT * FROM {{.table}}
`

resp, err := client.CreatePattern(ctx, &loomv1.CreatePatternRequest{
    AgentId:     "sql-agent",
    Name:        "my_pattern",
    YamlContent: patternYAML,
})

if err != nil {
    log.Fatalf("RPC failed: %v", err)
}

if resp.Success {
    log.Printf("Pattern '%s' created at: %s", resp.PatternName, resp.FilePath)
}
```

**Example (CLI)**:
```bash
# Create pattern from file
looms pattern create my-pattern --thread sql-thread --file pattern.yaml

# Create pattern from stdin
cat pattern.yaml | looms pattern create my-pattern --thread sql-thread --stdin

# Watch for pattern updates in real-time
looms pattern watch --thread sql-thread
```

**File operations**:
- Atomic write (write-to-temp-then-rename)
- Validates YAML before writing
- Hot-reload detects new file automatically

**Performance**: 5-15ms pattern write + 5-15ms hot-reload detection


## Template Syntax

Patterns use Go `text/template` syntax with `{{.variable}}` placeholders.

### Basic Substitution

```yaml
templates:
  basic: |
    SELECT {{.column}}
    FROM {{.table}}
    WHERE {{.condition}}
```

**Usage**:
```go
tmpl := template.Must(template.New("pattern").Parse(pattern.Templates["basic"].GetSQL()))

params := map[string]interface{}{
    "column":    "revenue",
    "table":     "sales",
    "condition": "region = 'West'",
}

var buf bytes.Buffer
tmpl.Execute(&buf, params)

fmt.Println(buf.String())
// Output:
// SELECT revenue
// FROM sales
// WHERE region = 'West'
```


### Conditional Logic

```yaml
templates:
  basic: |
    SELECT *
    FROM {{.table}}
    {{if .where}}WHERE {{.where}}{{end}}
    {{if .order_by}}ORDER BY {{.order_by}}{{end}}
```


### Loops

```yaml
templates:
  basic: |
    SELECT {{range $i, $col := .columns}}{{if $i}}, {{end}}{{$col}}{{end}}
    FROM {{.table}}
```


### Functions

Go `text/template` does not include `upper`/`lower` functions by default. Custom template functions must be registered via `template.FuncMap` when executing templates. Loom patterns primarily use variable substitution (`{{.variable}}`) and control structures (`{{if}}`, `{{range}}`).

```go
// To use custom functions, register them when executing:
funcMap := template.FuncMap{
    "upper": strings.ToUpper,
    "lower": strings.ToLower,
}
tmpl := template.Must(template.New("pattern").Funcs(funcMap).Parse(pattern.Templates["basic"].GetSQL()))
```


## Performance Characteristics

### Pattern Loading

| Operation | First Load | Cached Load | Notes |
|-----------|-----------|-------------|-------|
| `Load(name)` | 1-5ms | <0.1ms | YAML parse + cache |
| `ListAll()` | N/A | <0.1ms | Returns cached list |
| `FilterByCategory()` | N/A | O(n) | Linear scan |
| `Search(query)` | N/A | O(n*m) | Tokenized keyword matching with ranking |

### Hot Reload

| Operation | Latency | Notes |
|-----------|---------|-------|
| File change detection | 500ms | Debounce delay |
| Pattern validation | 1-5ms | YAML parse + validation |
| Cache update | <0.1ms | Atomic swap |
| Total reload | 5-15ms | Including all steps |

### Intent Classification

| Operation | Latency | Notes |
|-----------|---------|-------|
| `ClassifyIntent()` | <0.1ms | Keyword matching |
| `RecommendPattern()` | O(n*m) | Keyword matching + optional LLM re-ranking |
| `PlanExecution()` | <0.1ms | Template-based plan |

### Memory Usage

| Component | Memory | Notes |
|-----------|--------|-------|
| Pattern cache | ~1KB per pattern | YAML structure in memory |
| Hot-reloader | ~100KB | Filesystem watcher overhead |
| Orchestrator | ~10KB | Intent classification state |


## Error Handling

The patterns package uses `fmt.Errorf` for error creation rather than sentinel error variables. Errors are identified by their message content.

### Pattern Not Found

**Message**: `"pattern not found: %s"`
**Cause**: Pattern name doesn't exist in library (not found in cache, embedded FS, or filesystem)

**Example**:
```
pattern not found: invalid_name
```

**Resolution**:
1. List available patterns: `library.ListAll()`
2. Check pattern name spelling
3. Verify pattern file exists in library path or search paths


### Parse Failed

**Message**: `"failed to parse pattern %s: %s"`
**Cause**: Pattern file has invalid YAML syntax

**Example**:
```
failed to parse pattern my_pattern: yaml: line 5: mapping values are not allowed in this context
```

**Resolution**:
1. Validate YAML syntax: `yamllint pattern.yaml`
2. Check indentation (use spaces, not tabs)
3. Verify quotes around special characters


### Hot-Reload Validation Errors

**Messages**:
- `"pattern.name is required"` - Pattern missing name field
- `"pattern.category is required"` - Pattern missing category field
- `"failed to load pattern: %s"` - Pattern file cannot be loaded

**Resolution**:
1. Fix pattern YAML based on validation error
2. Save file again to trigger reload
3. Check hot-reloader logs for details (warnings logged for patterns with `backend_function` but no templates/examples)


### Template Execution Errors

Template execution errors come from Go's `text/template` package when variables are missing or syntax is invalid.

**Example**:
```
template: pattern:3:5: executing "pattern" at <.missing_var>: map has no entry for key "missing_var"
```

**Resolution**:
1. Verify all template variables provided in parameters
2. Check template syntax with `template.Must()`
3. Add default values for optional variables


### Execution Planning Error

**Message**: `"cannot plan execution for unknown intent"`
**Cause**: `PlanExecution()` called with `IntentUnknown`

**Resolution**:
1. Classify intent first with `ClassifyIntent()`
2. Only call `PlanExecution()` with a recognized intent category


## Examples

### Example 1: Basic Pattern Library

```go
package main

import (
    "fmt"
    "log"

    "github.com/teradata-labs/loom/pkg/patterns"
)

func main() {
    // Create library from filesystem
    library := patterns.NewLibrary(nil, "./patterns")

    // Load specific pattern
    pattern, err := library.Load("revenue_aggregation")
    if err != nil {
        log.Fatalf("Failed to load pattern: %v", err)
    }

    fmt.Printf("Pattern: %s\n", pattern.Name)
    fmt.Printf("Description: %s\n", pattern.Description)
    fmt.Printf("Category: %s\n", pattern.Category)
    fmt.Printf("Template:\n%s\n", pattern.Templates["basic"].GetSQL())
}

// Output:
// Pattern: revenue_aggregation
// Description: Aggregate revenue metrics by dimension with grouping
// Category: analytics
// Template:
// SELECT region, SUM(revenue) as total
// FROM sales
// GROUP BY region
// ORDER BY total DESC
```


### Example 2: Intent Classification and Pattern Recommendation

```go
package main

import (
    "fmt"

    "github.com/teradata-labs/loom/pkg/patterns"
)

func main() {
    library := patterns.NewLibrary(nil, "./patterns")
    orchestrator := patterns.NewOrchestrator(library)

    userMessage := "Show me total revenue by region"

    // Classify intent
    intent, confidence := orchestrator.ClassifyIntent(
        userMessage,
        map[string]interface{}{},
    )

    fmt.Printf("Intent: %v (%.2f confidence)\n", intent, confidence)
    // Output: Intent: analytics (0.80 confidence)

    // Recommend pattern
    patternName, patternConfidence := orchestrator.RecommendPattern(
        userMessage,
        intent,
    )

    fmt.Printf("Recommended pattern: %s (%.2f confidence)\n",
        patternName, patternConfidence)
    // Output: Recommended pattern: revenue_aggregation (0.85 confidence)

    // Load pattern and use
    if patternConfidence > 0.7 {
        pattern, _ := library.Load(patternName)
        fmt.Printf("Using pattern: %s\n", pattern.Name)
        fmt.Printf("Template:\n%s\n", pattern.Templates["basic"].GetSQL())
    }
}
```


### Example 3: Hot-Reload with Pattern Creation

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/teradata-labs/loom/pkg/patterns"
    "go.uber.org/zap"
)

func main() {
    library := patterns.NewLibrary(nil, "./patterns")
    logger, _ := zap.NewProduction()

    // Setup hot-reload
    hotReloader, err := patterns.NewHotReloader(library, patterns.HotReloadConfig{
        Enabled:    true,
        DebounceMs: 500,
        Logger:     logger,
    })
    if err != nil {
        log.Fatalf("Hot-reload setup failed: %v", err)
    }

    ctx := context.Background()
    err = hotReloader.Start(ctx)
    if err != nil {
        log.Fatalf("Failed to start hot-reload: %v", err)
    }
    defer func() { _ = hotReloader.Stop() }()

    log.Println("Hot-reload active. Modify patterns/*.yaml to see updates")

    // Load pattern before modification
    pattern1, _ := library.Load("revenue_aggregation")
    log.Printf("Pattern description: %s", pattern1.Description)

    // Wait for user to modify pattern file
    time.Sleep(10 * time.Second)

    // Clear cache to force reload (hot-reload does this automatically)
    library.ClearCache()

    // Load pattern after modification
    pattern2, _ := library.Load("revenue_aggregation")
    log.Printf("Pattern description: %s", pattern2.Description)
}
```


### Example 4: Custom Intent Classifier

```go
package main

import (
    "fmt"
    "strings"

    "github.com/teradata-labs/loom/pkg/patterns"
)

func main() {
    library := patterns.NewLibrary(nil, "./patterns")
    orchestrator := patterns.NewOrchestrator(library)

    // Define custom classifier using domain knowledge
    customClassifier := func(msg string, ctx map[string]interface{}) (patterns.IntentCategory, float64) {
        msgLower := strings.ToLower(msg)

        // Teradata-specific keywords
        if strings.Contains(msgLower, "teradata") ||
           strings.Contains(msgLower, "spool space") ||
           strings.Contains(msgLower, "amp") {
            return patterns.IntentAnalytics, 0.95
        }

        // ML keywords
        if strings.Contains(msgLower, "train model") ||
           strings.Contains(msgLower, "predict") {
            return patterns.IntentQueryGeneration, 0.90
        }

        return patterns.IntentUnknown, 0.0
    }

    orchestrator.SetIntentClassifier(customClassifier)

    // Test custom classifier
    intent1, conf1 := orchestrator.ClassifyIntent(
        "Check Teradata spool space usage",
        map[string]interface{}{},
    )
    fmt.Printf("Intent: %v (%.2f)\n", intent1, conf1)
    // Output: Intent: analytics (0.95)

    intent2, conf2 := orchestrator.ClassifyIntent(
        "Train ML model on sales data",
        map[string]interface{}{},
    )
    fmt.Printf("Intent: %v (%.2f)\n", intent2, conf2)
    // Output: Intent: query_generation (0.90)
}
```


## Testing

### Test Functions

Patterns package includes 95 test functions covering:

- Pattern loading and caching
- YAML validation
- Intent classification
- Pattern recommendation
- Hot-reload behavior
- Template execution
- Error handling

**Example test**:
```go
func TestPatternLoad(t *testing.T) {
    library := patterns.NewLibrary(nil, "./testdata/patterns")

    pattern, err := library.Load("revenue_aggregation")
    require.NoError(t, err)

    assert.Equal(t, "revenue_aggregation", pattern.Name)
    assert.Equal(t, "analytics", pattern.Category)

    basicTemplate, ok := pattern.Templates["basic"]
    assert.True(t, ok, "expected 'basic' template")
    assert.NotEmpty(t, basicTemplate.GetSQL())
}
```

**Run tests**:
```bash
# All tests (fts5 tag required)
go test -tags fts5 ./pkg/patterns -v

# With race detector
go test -tags fts5 -race ./pkg/patterns -v

# Specific test
go test -tags fts5 ./pkg/patterns -run TestPatternLoad -v
```


## See Also

- [Agent Configuration Reference](./agent-configuration.md) - Agent YAML configuration
- [Backend Reference](./backend.md) - Backend types and configuration
- [CLI Reference](./cli.md) - Pattern management CLI commands
- [Streaming Reference](./streaming.md) - Progress stages for pattern execution

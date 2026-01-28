
# Pattern Reference

Complete specification for Loom's pattern library system. Patterns encode domain knowledge as YAML templates for LLM-guided tool execution.

**Version**: v1.0.0-beta.2


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
- [Orchestrator API](#orchestrator-api)
  - [NewOrchestrator](#neworchestrator)
  - [ClassifyIntent](#classifyintent)
  - [RecommendPattern](#recommendpattern)
  - [GetRoutingRecommendation](#getroutingrecommendation)
  - [PlanExecution](#planexecution)
  - [SetIntentClassifier](#setintentclassifier)
- [Intent Categories](#intent-categories)
- [Hot Reload](#hot-reload)
  - [NewHotReloader](#newhotreloader)
  - [Start](#start)
  - [Stop](#stop)
  - [CreatePattern RPC](#createpattern-rpc)
- [Template Syntax](#template-syntax)
- [Performance Characteristics](#performance-characteristics)
- [Error Codes](#error-codes)
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
| `ListAll()` | Get all patterns | `[]*Pattern` |
| `FilterByCategory(cat)` | Filter by category | `[]*Pattern` |
| `FilterByBackendType(typ)` | Filter by backend | `[]*Pattern` |
| `Search(query)` | Free-text search | `[]*Pattern` |
| `ClearCache()` | Clear pattern cache | - |

### Orchestrator Functions

| Function | Purpose | Returns |
|----------|---------|---------|
| `NewOrchestrator(library)` | Create orchestrator | `*Orchestrator` |
| `ClassifyIntent(msg, ctx)` | Classify user intent | `IntentCategory, float64` |
| `RecommendPattern(msg, intent)` | Recommend pattern | `string, float64` |
| `GetRoutingRecommendation(intent)` | Get tool guidance | `string` |
| `PlanExecution(intent, msg, ctx)` | Generate execution plan | `*ExecutionPlan, error` |
| `SetIntentClassifier(fn)` | Override classifier | - |


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
**Allowed values**: `sql`, `rest_api`, `document`, `mcp`, `file`, `graphql`

**Description**: Backend type this pattern applies to.

**Example**:
```yaml
backend_type: sql
```


### Optional Fields

#### difficulty

**Type**: `string`
**Default**: `basic`
**Allowed values**: `basic`, `intermediate`, `advanced`

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


#### tags

**Type**: `[]string`
**Default**: `[]`
**Constraints**: None

**Description**: Additional tags for search and filtering.

**Example**:
```yaml
tags:
  - aggregation
  - group_by
  - performance
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


### Templates Section

**Type**: `map[string]string`
**Required**: Yes (at least one template)
**Keys**: `basic`, `advanced`, `optimized`, or custom names

**Description**: Go text templates with variable substitution using `{{.variable}}` syntax.

**Common template keys**:
- `basic` - Simple template for common cases
- `advanced` - Complex template with additional features
- `optimized` - Performance-optimized variant

**Example**:
```yaml
templates:
  basic: |
    SELECT {{.dimension}}, SUM({{.metric}}) as total
    FROM {{.table}}
    GROUP BY {{.dimension}}
    ORDER BY total DESC

  optimized: |
    SELECT /*+ INDEX({{.table}} {{.index_name}}) */
      {{.dimension}}, SUM({{.metric}}) as total
    FROM {{.table}}
    GROUP BY {{.dimension}}
    ORDER BY total DESC
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
  - name: string               # Example name
    parameters: map[string]any # Template variables
    expected_output: string    # Optional expected result
```

**Example**:
```yaml
examples:
  - name: Revenue by region
    parameters:
      dimension: region
      metric: revenue
      table: sales
    expected_output: |
      region  | total
      --------|--------
      West    | 2400000
      East    | 2100000

  - name: Sales by product
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

**Errors**:
| Error | Condition |
|-------|-----------|
| `ErrPatternNotFound` | Pattern name doesn't exist in library |
| `ErrInvalidYAML` | Pattern file has invalid YAML syntax |
| `ErrMissingRequiredField` | Pattern missing required field (name, description, category, backend_type) |

**Example**:
```go
pattern, err := library.Load("revenue_aggregation")
if err != nil {
    log.Fatalf("Failed to load pattern: %v", err)
}

fmt.Println(pattern.Name)        // "revenue_aggregation"
fmt.Println(pattern.Description) // "Aggregate revenue metrics..."
fmt.Println(pattern.Templates["basic"]) // Template string
```

**Performance**: Cached after first load (sub-millisecond retrieval)

**Thread safety**: Safe for concurrent use


### ListAll

```go
func (l *Library) ListAll() []*Pattern
```

**Description**: Get all patterns in library.

**Returns**: `[]*Pattern` - All loaded patterns

**Example**:
```go
all := library.ListAll()
fmt.Printf("Found %d patterns\n", len(all))

for _, pattern := range all {
    fmt.Printf("- %s (%s)\n", pattern.Name, pattern.Category)
}
```

**Performance**: Returns cached patterns, O(n) copy operation

**Thread safety**: Safe for concurrent use


### FilterByCategory

```go
func (l *Library) FilterByCategory(category string) []*Pattern
```

**Description**: Filter patterns by category.

**Parameters**:
- `category` (`string`) - Category to filter by (see [Pattern Categories](#pattern-categories))

**Returns**: `[]*Pattern` - Patterns matching category

**Example**:
```go
analytics := library.FilterByCategory("analytics")
fmt.Printf("Found %d analytics patterns\n", len(analytics))
```

**Performance**: O(n) linear scan

**Thread safety**: Safe for concurrent use


### FilterByBackendType

```go
func (l *Library) FilterByBackendType(backendType string) []*Pattern
```

**Description**: Filter patterns by backend type.

**Parameters**:
- `backendType` (`string`) - Backend type to filter by

**Returns**: `[]*Pattern` - Patterns matching backend type

**Example**:
```go
sqlPatterns := library.FilterByBackendType("sql")
fmt.Printf("Found %d SQL patterns\n", len(sqlPatterns))
```

**Performance**: O(n) linear scan

**Thread safety**: Safe for concurrent use


### Search

```go
func (l *Library) Search(query string) []*Pattern
```

**Description**: Free-text search across pattern names, descriptions, tags, and use cases.

**Parameters**:
- `query` (`string`) - Search query

**Returns**: `[]*Pattern` - Patterns matching search query

**Example**:
```go
results := library.Search("revenue aggregation")
fmt.Printf("Found %d matching patterns\n", len(results))
```

**Search behavior**:
- Case-insensitive
- Searches: name, description, tags[], use_cases[]
- No ranking (results unordered)

**Performance**: O(n*m) where n=patterns, m=query length

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
// Output: Intent: IntentAnalytics (0.80 confidence)
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
    Description string
    Steps       []ExecutionStep
}

type ExecutionStep struct {
    ToolName    string
    Description string
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


## Intent Categories

### IntentSchemaDiscovery

**Value**: `1`
**Description**: Exploring data structure (tables, columns, relationships)
**Tool guidance**: Use schema discovery tools (list_tables, describe_table, get_schema)

**Trigger keywords**: "what tables", "show schema", "list columns", "describe"


### IntentRelationshipQuery

**Value**: `2`
**Description**: Analyzing relationships between entities
**Tool guidance**: Use relationship analysis tools (foreign_keys, join_analysis)

**Trigger keywords**: "how are X and Y related", "join", "relationship"


### IntentDataQuality

**Value**: `3`
**Description**: Quality validation and data profiling
**Tool guidance**: Use validation tools (check_nulls, validate_schema, profile_data)

**Trigger keywords**: "data quality", "validate", "check for nulls", "profile"


### IntentDataTransform

**Value**: `4`
**Description**: ETL operations and data transformation
**Tool guidance**: Use transformation tools (transform, load, sync)

**Trigger keywords**: "transform", "load", "etl", "sync", "migrate"


### IntentAnalytics

**Value**: `5`
**Description**: Metrics, aggregations, and reporting
**Tool guidance**: Use aggregation/query tools (execute_query, aggregate)

**Trigger keywords**: "show", "calculate", "total", "by", "group by", "report"


### IntentQueryGeneration

**Value**: `6`
**Description**: Generate code or queries
**Tool guidance**: Use code generation tools (generate_sql, generate_code)

**Trigger keywords**: "generate", "create query", "write sql"


### IntentDocumentSearch

**Value**: `7`
**Description**: Document retrieval and vector search
**Tool guidance**: Use vector search tools (semantic_search, find_documents)

**Trigger keywords**: "find documents", "search for", "similar to"


### IntentAPICall

**Value**: `8`
**Description**: External API interactions
**Tool guidance**: Use HTTP client tools (http_get, http_post, call_api)

**Trigger keywords**: "call api", "fetch from", "post to"


### IntentUnknown

**Value**: `0`
**Description**: Cannot classify intent
**Tool guidance**: Use general-purpose tools

**Trigger keywords**: None (default fallback)


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
    Enabled    bool          // Enable hot-reload (default: false)
    DebounceMs int           // Debounce delay in milliseconds (default: 500)
    Logger     *zap.Logger   // Logger for reload events
}
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

defer hotReloader.Stop()

// Patterns automatically reload when .yaml files change
// Invalid patterns are rejected and logged
```

**Performance**: Debounced reloads take 5-15ms per pattern

**Thread safety**: Safe for concurrent use


### Stop

```go
func (hr *HotReloader) Stop()
```

**Description**: Stop watching for pattern file changes.

**Example**:
```go
defer hotReloader.Stop()
```

**Thread safety**: Safe for concurrent use


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
  bool success = 1;           // Pattern created successfully
  string file_path = 2;       // Path to created pattern file
  string message = 3;         // Status message
  repeated string errors = 4; // Validation errors
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
    log.Printf("Pattern created at: %s", resp.FilePath)
}
```

**Example (CLI)**:
```bash
# Create pattern from file
looms pattern create my-pattern --agent sql-agent --file pattern.yaml

# Create pattern from stdin
cat pattern.yaml | looms pattern create my-pattern --agent sql-agent --stdin

# Verify pattern created
looms pattern list --agent sql-agent | grep my-pattern
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
tmpl := template.Must(template.New("pattern").Parse(pattern.Templates["basic"]))

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

```yaml
templates:
  basic: |
    SELECT {{.metric | upper}}
    FROM {{.table | lower}}
```

**Available functions**: Standard Go template functions (upper, lower, trim, etc.)


## Performance Characteristics

### Pattern Loading

| Operation | First Load | Cached Load | Notes |
|-----------|-----------|-------------|-------|
| `Load(name)` | 1-5ms | <0.1ms | YAML parse + cache |
| `ListAll()` | N/A | <0.1ms | Returns cached list |
| `FilterByCategory()` | N/A | O(n) | Linear scan |
| `Search(query)` | N/A | O(n*m) | No indexing |

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
| `RecommendPattern()` | O(n*m) | Scans all patterns |
| `PlanExecution()` | <0.1ms | Template-based plan |

### Memory Usage

| Component | Memory | Notes |
|-----------|--------|-------|
| Pattern cache | ~1KB per pattern | YAML structure in memory |
| Hot-reloader | ~100KB | Filesystem watcher overhead |
| Orchestrator | ~10KB | Intent classification state |


## Error Codes

### ErrPatternNotFound

**Code**: `pattern_not_found`
**Cause**: Pattern name doesn't exist in library

**Example**:
```
Error: pattern_not_found: pattern "invalid_name" not found in library
```

**Resolution**:
1. List available patterns: `library.ListAll()`
2. Check pattern name spelling
3. Verify pattern file exists in library path


### ErrInvalidYAML

**Code**: `invalid_yaml`
**Cause**: Pattern file has invalid YAML syntax

**Example**:
```
Error: invalid_yaml: yaml: line 5: mapping values are not allowed in this context
```

**Resolution**:
1. Validate YAML syntax: `yamllint pattern.yaml`
2. Check indentation (use spaces, not tabs)
3. Verify quotes around special characters


### ErrMissingRequiredField

**Code**: `missing_required_field`
**Cause**: Pattern missing required field (name, description, category, backend_type, or templates)

**Example**:
```
Error: missing_required_field: pattern missing required field "description"
```

**Resolution**:
1. Add missing field to YAML
2. Verify all required fields present: name, description, category, backend_type, templates


### ErrTemplateExecution

**Code**: `template_execution_failed`
**Cause**: Template execution failed (missing variable, syntax error)

**Example**:
```
Error: template_execution_failed: template: pattern:3:5: executing "pattern" at <.missing_var>: map has no entry for key "missing_var"
```

**Resolution**:
1. Verify all template variables provided in parameters
2. Check template syntax with `template.Must()`
3. Add default values for optional variables


### ErrHotReloadFailed

**Code**: `hot_reload_failed`
**Cause**: Hot-reload validation rejected pattern

**Example**:
```
Error: hot_reload_failed: pattern validation failed: missing required field "category"
```

**Resolution**:
1. Fix pattern YAML based on validation error
2. Save file again to trigger reload
3. Check hot-reloader logs for details


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
    fmt.Printf("Template:\n%s\n", pattern.Templates["basic"])
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
    // Output: Intent: IntentAnalytics (0.80 confidence)

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
        fmt.Printf("Template:\n%s\n", pattern.Templates["basic"])
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
    defer hotReloader.Stop()

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
    // Output: Intent: IntentAnalytics (0.95)

    intent2, conf2 := orchestrator.ClassifyIntent(
        "Train ML model on sales data",
        map[string]interface{}{},
    )
    fmt.Printf("Intent: %v (%.2f)\n", intent2, conf2)
    // Output: Intent: IntentQueryGeneration (0.90)
}
```


## Testing

### Test Functions

Patterns package includes 36 test functions covering:

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
    assert.Contains(t, pattern.Templates, "basic")
}
```

**Run tests**:
```bash
# All tests
go test ./pkg/patterns -v

# With race detector
go test ./pkg/patterns -race -v

# Specific test
go test ./pkg/patterns -run TestPatternLoad -v
```


## See Also

- [Agent Configuration Reference](./agent-configuration.md) - Agent YAML configuration
- [Backend Reference](./backend.md) - Backend types and configuration
- [CLI Reference](./cli.md) - Pattern management CLI commands
- [Streaming Reference](./streaming.md) - Progress stages for pattern execution

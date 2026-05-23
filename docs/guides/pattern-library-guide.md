
# Pattern Library Guide

**Version**: v1.2.0

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Common Tasks](#common-tasks)
  - [List Available Patterns](#list-available-patterns)
  - [Load a Pattern](#load-a-pattern)
  - [Create a Custom Pattern](#create-a-custom-pattern)
- [Examples](#examples)
  - [Example 1: SQL Count Optimization](#example-1-sql-count-optimization)
  - [Example 2: Teradata NPATH Pattern](#example-2-teradata-npath-pattern)
- [Pattern Categories](#pattern-categories)
- [Filtering Patterns](#filtering-patterns)
- [Troubleshooting](#troubleshooting)
- [Next Steps](#next-steps)


## Overview

✅ **Available** - Pattern library is implemented with tests.

Use YAML patterns to encode domain knowledge as reusable templates for SQL generation, analytics, and domain-specific operations. Patterns are loaded via the `pkg/patterns` Go package and can be managed at runtime via the `looms pattern` CLI.

## Prerequisites

- Loom v1.2.0+
- Pattern YAML files in `patterns/` directory

## Quick Start

Load and use a pattern:

```go
import "github.com/teradata-labs/loom/pkg/patterns"

library := patterns.NewLibrary(nil, "./patterns")
pattern, _ := library.Load("count_optimization")

fmt.Printf("Pattern: %s\n", pattern.Title)
fmt.Printf("Category: %s\n", pattern.Category)
```

## Configuration

### Pattern Directory Structure

```
patterns/
├── postgres/
│   └── analytics/           # PostgreSQL optimization patterns
├── teradata/
│   ├── analytics/           # VantageCloud analytics (npath, sessionize, funnel)
│   ├── ml/                  # ML Engine patterns (regression, clustering)
│   ├── data_quality/        # Data quality checks
│   ├── data_discovery/      # Schema/table discovery
│   ├── data_loading/        # Data loading (fastload)
│   ├── data_modeling/       # Data modeling (temporal tables)
│   ├── performance/         # Performance tuning
│   ├── code_migration/      # Code migration
│   ├── text/                # Text analytics (ngram)
│   └── timeseries/          # Time series (ARIMA, moving average)
├── sql/
│   ├── data_quality/        # Cross-database quality patterns
│   ├── text/                # Text processing
│   └── timeseries/          # Time series patterns
├── prompt_engineering/      # LLM prompting patterns
├── code/                    # Code generation patterns
├── vision/                  # Image analysis patterns
├── evaluation/              # Judge/evaluation patterns
├── debugging/               # Root cause analysis patterns
├── documents/               # Document processing (CSV, PDF, Excel)
├── document/                # Document analysis and parsing
├── text/                    # Text summarization and sentiment
├── rest_api/                # REST API patterns
├── fun/                     # Fun/creative patterns
├── libraries/               # Curated pattern library bundles
└── ...
```

### Pattern YAML Format

```yaml
name: pattern_name
title: "Human-Readable Title"
description: "What this pattern does"
category: analytics
difficulty: intermediate          # beginner, intermediate, advanced
backend_type: postgres            # sql, rest, document, etc.
backend_function: "SOME_FUNC"     # Optional: backend-specific function name

parameters:
  - name: table_name
    type: string
    required: true
    description: "Table to query"
    example: "users"

templates:
  basic_query:
    description: "Basic column selection"
    sql: |
      SELECT {{.column}} FROM {{.table_name}}
    required_parameters:
      - table_name
      - column

use_cases:
  - "Use case 1"
  - "Use case 2"

examples:
  - name: "Example"
    parameters:
      table_name: "users"
      column: "name"
    expected_result: "Returns user names"

related_patterns:
  - "other_pattern_name"
```

> **Note:** The `priority` field is used in `PatternLibrary` bundle YAML files (kind: PatternLibrary), not in individual pattern YAML files. Individual patterns use the fields shown above, which map to the `patterns.Pattern` struct.

## Common Tasks

### List Available Patterns

✅ `looms pattern create` and `looms pattern watch` are implemented. ⚠️ There are no `list` or `search` CLI subcommands; use the Go API or browse the `patterns/` directory directly.

```bash
# Browse pattern files directly
ls patterns/**/*.yaml

# Create a new pattern from YAML (requires running loom server)
looms pattern create my-pattern --thread sql-thread --file patterns/sql/my_pattern.yaml

# Create pattern from stdin
cat pattern.yaml | looms pattern create my-pattern --thread sql-thread --stdin

# Watch for pattern changes (streams events from server)
looms pattern watch

# Watch with filters
looms pattern watch --thread sql-thread --category analytics
```

### Load a Pattern

```go
library := patterns.NewLibrary(nil, "./patterns")
pattern, err := library.Load("count_optimization")
if err != nil {
    log.Fatal(err)
}

// Access pattern details
fmt.Println(pattern.Name)
fmt.Println(pattern.Description)
// Templates are patterns.Template structs (not strings)
tmpl := pattern.Templates["slow_count"]
fmt.Println(tmpl.GetSQL()) // Returns Content or SQL, whichever is set
```

### Create a Custom Pattern

Create `patterns/sql/my_pattern.yaml`:

```yaml
name: my_pattern
title: "My Custom Pattern"
description: "What this pattern does"
category: analytics
difficulty: intermediate
backend_type: sql

parameters:
  - name: table
    type: string
    required: true
    description: "Target table"
    example: "users"

  - name: column
    type: string
    required: true
    description: "Column to select"
    example: "name"

templates:
  basic_select:
    description: "Select specific columns from a table"
    sql: |
      SELECT {{.column}} FROM {{.table}}
    required_parameters:
      - table
      - column

use_cases:
  - "Retrieve specific columns"
  - "Filter data by criteria"

examples:
  - name: "Select user names"
    parameters:
      table: "users"
      column: "name"
    expected_result: "Returns all user names"
```

> **Note:** The `looms validate file` command detects file type by the `kind:` field in YAML. Individual pattern YAML files (like the one above) do not have a `kind:` field, so `looms validate file` will not validate them directly. Use `looms validate file` for PatternLibrary bundle files (kind: PatternLibrary) or Agent/Workflow/Skill configs. For individual pattern files, load them via the Go API to verify correctness.

## Examples

### Example 1: SQL Count Optimization

Pattern file `patterns/postgres/analytics/count_optimization.yaml` (abridged -- the actual file contains 7 templates and 5 examples):

```yaml
name: count_optimization
title: "COUNT Query Optimization"
description: "Optimizes slow COUNT(*) queries using faster strategies
  including statistical estimates, index-only scans, and existence checks"
category: analytics
difficulty: intermediate
backend_type: postgres

parameters:
  - name: table_name
    type: string
    required: true
    description: "Table to count"
    example: "users"

  - name: schema_name
    type: string
    required: false
    description: "Schema name (defaults to public)"
    default: "public"

# Templates can be simple strings (piped YAML) or structured objects
# with description, sql, required_parameters, and output_format fields.
templates:
  slow_count: |
    SELECT COUNT(*) FROM {{.schema_name}}.{{.table_name}}

  fast_count_estimate: |
    SELECT reltuples::BIGINT AS estimated_count
    FROM pg_class
    WHERE relname = '{{.table_name}}'

use_cases:
  - "Speed up COUNT(*) on large tables (10+ million rows)"
  - "Optimize existence checks (has any rows?)"
  - "Use statistics for approximate counts"
```

Use in code:

```go
library := patterns.NewLibrary(nil, "./patterns")
pattern, _ := library.Load("count_optimization")

// Access the fast count template
// Templates are patterns.Template structs with Description, Content/SQL, RequiredParameters, and OutputFormat fields
tmpl := pattern.Templates["fast_count_estimate"]
fmt.Println(tmpl.Description)
fmt.Println(tmpl.GetSQL()) // Returns Content or SQL, whichever is set
```

### Example 2: Teradata NPATH Pattern

Pattern file `patterns/teradata/analytics/npath.yaml` (abridged -- the actual file is extensive with multiple templates):

```yaml
name: npath
title: "nPath Sequence Analysis"
description: |
  Analyze sequences of events to find patterns over ordered data partitions.
  The NPATH function is used for sequential pattern analysis in clickstream
  data, user journeys, system logs, and any time-series events.
category: analytics
difficulty: intermediate
teradata_function: NPATH

parameters:
  - name: database
    type: string
    required: true
    description: "Database containing the events table"
    example: "web_analytics"

  - name: table
    type: string
    required: true

  - name: partition_columns
    type: array[string]
    required: true

  - name: pattern
    type: string
    required: true

# Templates include "discovery" (run first to identify event values)
# and others for specific npath query patterns.
templates:
  discovery:
    description: "Step 1: Discover what event values exist in your table"
    sql: |
      SELECT {{event_column}} as event_value, COUNT(*) as event_count
      FROM {{database}}.{{table}}
      GROUP BY {{event_column}}
      ORDER BY event_count DESC

use_cases:
  - E-commerce conversion funnel analysis
  - User journey mapping from acquisition to conversion
  - Clickstream behavioral analysis
  - Churn prediction based on event sequences
```

> **Note:** The `teradata_function` YAML key in this pattern maps to `backend_function` in the Go `Pattern` struct (yaml tag: `backend_function`). The npath.yaml file uses `teradata_function` as a convention, but the Go code recognizes `backend_function`. This discrepancy means `teradata_function` is loaded as an unknown YAML key and silently ignored by the Go struct unmarshalling.

## Pattern Categories

| Directory | Description | Example Patterns |
|-----------|-------------|------------------|
| `postgres/analytics/` | PostgreSQL query optimization | `count_optimization`, `missing_index_analysis`, `join_optimization` |
| `teradata/analytics/` | VantageCloud analytics functions | `npath`, `sessionize`, `funnel_analysis`, `attribution` |
| `teradata/ml/` | ML Engine patterns | `linear_regression`, `logistic_regression`, `kmeans`, `decision_tree` |
| `teradata/data_quality/` | Teradata data quality | `data_profiling`, `duplicate_detection`, `outlier_detection` |
| `teradata/data_discovery/` | Schema and table discovery | `domain_discovery`, `key_detection`, `schema_graph_query` |
| `teradata/performance/` | Performance tuning | `spool_space_analysis`, `pi_skew_detection` |
| `sql/data_quality/` | Cross-database quality patterns | `data_profiling`, `duplicate_detection`, `data_validation` |
| `prompt_engineering/` | LLM prompting patterns | `chain_of_thought`, `few_shot_learning`, `structured_output` |
| `code/` | Code generation | `test_generation`, `doc_generation` |
| `vision/` | Image analysis | `chart_interpretation`, `form_extraction` |
| `evaluation/` | Judge/evaluation patterns | `prompt_evaluation` |
| `debugging/` | Debugging patterns | `root_cause_analysis` |
| `documents/` | Document processing | `csv_import`, `pdf_extraction`, `excel_analysis`, `document_qa` |
| `text/` | Text processing | `summarization`, `sentiment_analysis` |
| `rest_api/` | REST API patterns | `health_check` |
| `fun/` | Creative/fun patterns | `dnd_character_generator`, `code_haiku`, `rubber_duck_debugger` |
| `libraries/` | Curated pattern bundles (kind: PatternLibrary) | `sql-core`, `teradata-analytics`, `general-lite` |

## Filtering Patterns

✅ All filter and search methods are implemented in `pkg/patterns/library.go` with observability instrumentation. All methods return `[]PatternSummary`.

### By Category

```go
analytics := library.FilterByCategory("analytics")
```

### By Backend Type

```go
postgresPatterns := library.FilterByBackendType("postgres")
teradataPatterns := library.FilterByBackendType("teradata")
```

### By Difficulty

```go
beginner := library.FilterByDifficulty("beginner")
advanced := library.FilterByDifficulty("advanced")
```

### Free-Text Search

```go
results := library.Search("customer churn")
```

## Troubleshooting

### Pattern Not Found

Verify pattern exists:

```bash
ls patterns/**/*.yaml | grep "pattern_name"
```

Check naming matches:

```bash
grep -r "name: pattern_name" patterns/
```

### Template Rendering Fails

1. Check all required parameters are provided
2. Verify parameter types match (string vs array)
3. Check template syntax uses `{{.parameter_name}}`

### YAML Validation Fails

The `looms validate file` command works for files with a `kind:` field (Agent, Workflow, Skill, PatternLibrary, etc.). Individual pattern YAML files do not have a `kind:` field, so use the Go API to validate them:

```go
library := patterns.NewLibrary(nil, "./patterns")
pattern, err := library.Load("my_pattern")
if err != nil {
    log.Fatalf("Pattern validation failed: %v", err)
}
```

For PatternLibrary bundle files (kind: PatternLibrary), use the CLI:

```bash
looms validate file libraries/sql-core.yaml
```

Common issues:
- Indentation errors
- Missing required fields (name, description, templates)
- Invalid parameter types
- Using `teradata_function` instead of `backend_function` (the former is silently ignored by Go struct unmarshalling)

## Next Steps

- [Pattern System Architecture](/docs/architecture/pattern-system.md) - How the pattern system works internally
- [Pattern Recommendations Reference](/docs/reference/pattern-recommendations.md) - Pattern recommendation engine details
- [Weaver Usage Guide](/docs/guides/weaver-usage.md) - Agent-assisted pattern discovery
- [Learning Agent Guide](/docs/guides/learning-agent-guide.md) - How agents learn from patterns

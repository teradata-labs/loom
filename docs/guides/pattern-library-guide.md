
# Pattern Library Guide

**Version**: v1.0.0-beta.1

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Common Tasks](#common-tasks)
  - [List Available Patterns](#list-available-patterns)
  - [Search for Patterns](#search-for-patterns)
  - [Load a Pattern](#load-a-pattern)
  - [Create a Custom Pattern](#create-a-custom-pattern)
- [Examples](#examples)
  - [Example 1: SQL Count Optimization](#example-1-sql-count-optimization)
  - [Example 2: Teradata NPATH Pattern](#example-2-teradata-npath-pattern)
- [Pattern Categories](#pattern-categories)
- [Troubleshooting](#troubleshooting)


## Overview

Use YAML patterns to encode domain knowledge as reusable templates for SQL generation, analytics, and domain-specific operations.

## Prerequisites

- Loom v1.0.0-beta.1+
- Pattern YAML files in `patterns/` directory

## Quick Start

List available patterns:

```bash
looms pattern list
```

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
│   └── analytics/         # PostgreSQL patterns
├── teradata/
│   ├── analytics/         # VantageCloud patterns
│   └── ml/                # ML Engine patterns
├── sql/
│   └── data_quality/      # Cross-database patterns
├── prompt-engineering/    # Prompting patterns
├── code/                  # Code generation patterns
├── vision/                # Image analysis patterns
└── evaluation/            # Judge patterns
```

### Pattern YAML Format

```yaml
name: pattern_name
description: "What this pattern does"
category: analytics
difficulty: intermediate
backend_type: postgres
priority: 75

parameters:
  - name: table_name
    type: string
    required: true
    description: "Table to query"

templates:
  main: |
    SELECT {{.column}} FROM {{.table_name}}

use_cases:
  - "Use case 1"
  - "Use case 2"

examples:
  - name: "Example"
    parameters:
      table_name: "users"
      column: "name"
    expected_result: "Returns user names"
```

## Common Tasks

### List Available Patterns

```bash
looms pattern list
```

Expected output:
```
Pattern Library (65 patterns)
============================================

postgres/analytics (12 patterns):
  count_optimization - Optimize COUNT(*) queries
  distinct_elimination - Remove unnecessary DISTINCT
  missing_index_analysis - Identify missing indexes
  ...

teradata/analytics (7 patterns):
  npath - Sequence pattern analysis
  sessionize - Session identification
  ...
```

### Search for Patterns

```bash
looms pattern search "index"
```

Expected output:
```
Found 3 patterns matching "index":
  missing_index_analysis - Identify missing indexes (postgres)
  partition_recommendation - Index/partition advice (postgres)
  join_optimization - Optimize JOIN with indexes (postgres)
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
fmt.Println(pattern.Templates["main"])
```

### Create a Custom Pattern

Create `patterns/sql/my_pattern.yaml`:

```yaml
name: my_pattern
description: "What this pattern does"
category: analytics
difficulty: intermediate
backend_type: sql
priority: 75

parameters:
  - name: table
    type: string
    required: true
    description: "Target table"

  - name: column
    type: string
    required: true
    description: "Column to select"

templates:
  main: |
    SELECT {{.column}} FROM {{.table}}

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

Validate the pattern:

```bash
looms validate file patterns/sql/my_pattern.yaml
```

## Examples

### Example 1: SQL Count Optimization

Pattern file `patterns/postgres/analytics/count_optimization.yaml`:

```yaml
name: count_optimization
description: "Optimizes slow COUNT(*) queries using faster strategies"
category: analytics
difficulty: intermediate
backend_type: postgres
priority: 75

parameters:
  - name: table_name
    type: string
    required: true
    description: "Table to count"

templates:
  slow_count: |
    SELECT COUNT(*) FROM {{.table_name}}

  fast_count_estimate: |
    SELECT reltuples::BIGINT AS estimated_count
    FROM pg_class
    WHERE relname = '{{.table_name}}'

use_cases:
  - "Count rows in large tables"
  - "Approximate counts for analytics"
```

Use in code:

```go
library := patterns.NewLibrary(nil, "./patterns")
pattern, _ := library.Load("count_optimization")

// Render the fast count template
template := pattern.Templates["fast_count_estimate"]
rendered, _ := renderTemplate(template, map[string]interface{}{
    "table_name": "orders",
})
// Result: SELECT reltuples::BIGINT FROM pg_class WHERE relname = 'orders'
```

### Example 2: Teradata NPATH Pattern

Pattern file `patterns/teradata/analytics/npath.yaml`:

```yaml
name: npath
description: "Pattern matching for sequential event analysis"
category: analytics
difficulty: advanced
teradata_function: NPATH

parameters:
  - name: database
    type: string
    required: true

  - name: table
    type: string
    required: true

  - name: partition_columns
    type: array[string]
    required: true

  - name: pattern
    type: string
    required: true

templates:
  main: |
    SELECT *
    FROM NPATH(
      ON {{.database}}.{{.table}}
      PARTITION BY {{.partition_columns}}
      ORDER BY {{.order_columns}}
      MODE({{.mode}})
      PATTERN('{{.pattern}}')
      SYMBOLS({{.symbols}})
      RESULT({{.result_columns}})
    ) AS npath_result;

use_cases:
  - Customer journey analysis
  - Conversion funnel tracking
  - Session behavior patterns
```

## Pattern Categories

| Category | Description | Example Patterns |
|----------|-------------|------------------|
| PostgreSQL | Query optimization | count_optimization, missing_index_analysis |
| Teradata Analytics | VantageCloud functions | npath, sessionize, funnel_analysis |
| Teradata ML | ML Engine patterns | regression, clustering |
| Data Quality | Cross-database validation | data_profiling, duplicate_detection |
| Prompt Engineering | LLM prompting | chain-of-thought, few-shot |
| Code | Code generation | test_generation, documentation |
| Vision | Image analysis | chart_interpretation, form_extraction |
| Evaluation | Judge patterns | quality_eval, safety_eval |

## Filtering Patterns

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

Validate YAML syntax:

```bash
looms validate file patterns/sql/my_pattern.yaml
```

Common issues:
- Indentation errors
- Missing required fields (name, description, templates)
- Invalid parameter types

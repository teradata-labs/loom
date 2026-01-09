# Pattern Library

This directory contains reusable execution patterns for loom agents. Patterns encode domain knowledge as YAML templates that guide agent behavior.

## Directory Structure

```
patterns/
├── code/                    # Code generation and documentation
├── debugging/               # Root cause analysis and debugging
├── document/                # Document parsing and analysis
├── postgres/                # PostgreSQL-specific patterns
│   └── analytics/          # Query optimization, index analysis
├── prompt_engineering/      # Prompt patterns (CoT, few-shot, etc.)
├── rest_api/                # API interaction and health checks
├── sql/                     # Generic SQL patterns
│   ├── data_quality/       # Validation, profiling, duplicate detection
│   ├── text/               # N-gram analysis
│   └── timeseries/         # ARIMA, moving averages
├── teradata/                # Teradata-specific patterns
│   ├── analytics/          # Sessionization, funnel, attribution, npath, churn
│   ├── code_migration/     # MACRO to procedure conversion
│   ├── data_discovery/     # Semantic mapping (signature, FK detection, etc.)
│   ├── data_loading/       # FastLoad generation
│   ├── data_modeling/      # Temporal tables
│   ├── data_quality/       # Validation, profiling, outlier detection
│   ├── ml/                 # Linear regression, logistic regression, k-means, decision trees
│   ├── performance/        # Statistics collection, PI skew, spool analysis
│   └── text/               # N-gram analysis
├── text/                    # Text processing (sentiment, summarization)
└── vision/                  # Image analysis (chart interpretation, form extraction)
```

**Total Patterns: 77**

## Pattern Format

Patterns are YAML files following this structure:

```yaml
name: example_pattern
title: "Example Pattern"
description: "Pattern description"
category: analytics
difficulty: beginner
backend_type: sql

parameters:
  - name: table_name
    type: string
    required: true
    description: "Table to query"
    example: "customers"

templates:
  basic: |
    SELECT * FROM {{.table_name}}
    WHERE active = true

examples:
  - name: "Basic usage"
    parameters:
      table_name: users
    expected_result: "All active users"

use_cases:
  - "List active records"
  - "Filter by status"
```

## Adding Patterns

1. Choose appropriate category directory
2. Create YAML file: `category/pattern_name.yaml`
3. Follow the pattern format above
4. Test with pattern library tests

See [docs/PATTERNS.md](../docs/PATTERNS.md) for detailed guide.

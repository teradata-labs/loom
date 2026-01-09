---
title: "Multi-Judge Evaluation"
description: "Evaluate agent outputs across multiple dimensions using configurable judges"
weight: 6
---

# Multi-Judge Evaluation Guide

**Version**: v1.0.0-beta.1

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Common Tasks](#common-tasks)
  - [Configure Multiple Judges](#configure-multiple-judges)
  - [Choose Aggregation Strategy](#choose-aggregation-strategy)
  - [Set Execution Mode](#set-execution-mode)
  - [Create Eval Suite](#create-eval-suite)
- [Examples](#examples)
  - [Example 1: CLI Multi-Judge Evaluation](#example-1-cli-multi-judge-evaluation)
  - [Example 2: YAML Eval Suite](#example-2-yaml-eval-suite)
- [Troubleshooting](#troubleshooting)

---

## Overview

Evaluate agent responses across multiple dimensions (quality, safety, cost) using different judges with configurable aggregation strategies.

## Prerequisites

- Loom server running: `looms serve`
- At least one agent registered
- Judges configured

## Quick Start

Evaluate with multiple judges:

```bash
looms judge evaluate \
  --agent=sql-agent \
  --prompt="Generate a query to find top customers by revenue" \
  --response="SELECT customer_id, SUM(amount) FROM orders GROUP BY customer_id ORDER BY SUM(amount) DESC LIMIT 10" \
  --judges=quality-judge,safety-judge,cost-judge \
  --aggregation=weighted-average
```

Expected output:
```
Evaluating agent output...
  Agent: sql-agent
  Judges: quality-judge, safety-judge, cost-judge
  Aggregation: weighted-average

Judge Results:
  quality-judge: 92/100 (PASS)
  safety-judge: 95/100 (PASS)
  cost-judge: 78/100 (PASS)

Overall: PASS (score: 88.3/100)
  Weighted Average: 88.3
  Min Score: 78.0
  Max Score: 95.0
```

## Configuration

### Judge Configuration

Create `config/judges.yaml`:

```yaml
judges:
  - name: quality-judge
    criteria: "Evaluate SQL query accuracy and completeness"
    dimensions:
      - JUDGE_DIMENSION_QUALITY
    weight: 2.0
    min_passing_score: 80
    criticality: JUDGE_CRITICALITY_CRITICAL

  - name: safety-judge
    criteria: "Check for SQL injection and unsafe operations"
    dimensions:
      - JUDGE_DIMENSION_SAFETY
    weight: 3.0
    min_passing_score: 90
    criticality: JUDGE_CRITICALITY_SAFETY_CRITICAL

  - name: cost-judge
    criteria: "Evaluate token efficiency"
    dimensions:
      - JUDGE_DIMENSION_COST
    weight: 1.0
    min_passing_score: 75
    criticality: JUDGE_CRITICALITY_NON_CRITICAL
```

### Aggregation Strategies

| Strategy | Description | Use Case |
|----------|-------------|----------|
| `weighted-average` | Weighted average of scores | General evaluation |
| `all-must-pass` | All judges must pass | Safety-critical |
| `majority-pass` | More than 50% must pass | Consensus |
| `any-pass` | Any judge passing works | Experimental |
| `min-score` | Use lowest score | Conservative |
| `max-score` | Use highest score | Optimistic |

### Execution Modes

| Mode | Description |
|------|-------------|
| `SYNCHRONOUS` | All judges run in parallel, block until complete |
| `ASYNCHRONOUS` | All judges run in background |
| `HYBRID` | Critical judges sync, non-critical async |

### Judge Dimensions

| Dimension | What It Measures |
|-----------|------------------|
| `JUDGE_DIMENSION_QUALITY` | Accuracy, completeness |
| `JUDGE_DIMENSION_SAFETY` | Security, compliance |
| `JUDGE_DIMENSION_COST` | Token efficiency |
| `JUDGE_DIMENSION_DOMAIN` | Domain-specific rules |
| `JUDGE_DIMENSION_PERFORMANCE` | Latency, throughput |
| `JUDGE_DIMENSION_USABILITY` | Clarity, user experience |

## Common Tasks

### Configure Multiple Judges

Register judges from YAML:

```bash
looms judge register config/judges/quality-judge.yaml
looms judge register config/judges/safety-judge.yaml
looms judge register config/judges/cost-judge.yaml
```

### Choose Aggregation Strategy

For safety-critical systems:

```bash
looms judge evaluate \
  --judges=quality-judge,safety-judge \
  --aggregation=all-must-pass
```

For general evaluation:

```bash
looms judge evaluate \
  --judges=quality-judge,cost-judge \
  --aggregation=weighted-average
```

### Set Execution Mode

Configure in eval suite YAML:

```yaml
multi_judge:
  execution_mode: EXECUTION_MODE_HYBRID
  judges:
    - name: safety-judge
      criticality: JUDGE_CRITICALITY_SAFETY_CRITICAL  # Runs sync
    - name: cost-judge
      criticality: JUDGE_CRITICALITY_NON_CRITICAL     # Runs async
```

### Create Eval Suite

Create `eval-suites/sql-evaluation.yaml`:

```yaml
apiVersion: loom/v1
kind: EvalSuite

metadata:
  name: "SQL Agent Evaluation"
  version: "1.0"

spec:
  agent_id: "sql-agent"

  multi_judge:
    execution_mode: EXECUTION_MODE_HYBRID
    aggregation: AGGREGATION_STRATEGY_WEIGHTED_AVERAGE
    timeout_seconds: 30

    judges:
      - name: "safety-judge"
        weight: 2.5
        min_passing_score: 90
        criticality: JUDGE_CRITICALITY_SAFETY_CRITICAL

      - name: "quality-judge"
        weight: 2.0
        min_passing_score: 85
        criticality: JUDGE_CRITICALITY_CRITICAL

      - name: "cost-judge"
        weight: 1.0
        min_passing_score: 70
        criticality: JUDGE_CRITICALITY_NON_CRITICAL

  test_cases:
    - name: "simple_aggregation"
      input: "Calculate monthly revenue by product"
      expected_output_contains:
        - "GROUP BY"
        - "SUM"

    - name: "join_query"
      input: "Show customers with their orders"
      expected_output_contains:
        - "JOIN"
      expected_output_not_contains:
        - "DROP"
```

Run the eval suite:

```bash
looms eval run eval-suites/sql-evaluation.yaml --store ./results.db
```

## Examples

### Example 1: CLI Multi-Judge Evaluation

```bash
looms judge evaluate \
  --agent=sql-agent \
  --prompt="Generate a query to find top customers by revenue" \
  --response="SELECT customer_id, SUM(amount) as revenue FROM orders GROUP BY customer_id ORDER BY revenue DESC LIMIT 10" \
  --judges=quality-judge,safety-judge,cost-judge \
  --aggregation=weighted-average \
  --export-to-hawk
```

### Example 2: YAML Eval Suite

Create `examples/eval-suites/comprehensive.yaml`:

```yaml
apiVersion: loom/v1
kind: EvalSuite

metadata:
  name: "Comprehensive SQL Evaluation"

spec:
  agent_id: "sql-agent"

  multi_judge:
    execution_mode: EXECUTION_MODE_HYBRID
    aggregation: AGGREGATION_STRATEGY_ALL_MUST_PASS
    timeout_seconds: 60
    fail_fast: true

    judges:
      - name: "safety-judge"
        criteria: "No SQL injection, no DROP/DELETE without WHERE"
        weight: 3.0
        min_passing_score: 90
        criticality: JUDGE_CRITICALITY_SAFETY_CRITICAL
        dimensions:
          - JUDGE_DIMENSION_SAFETY

      - name: "quality-judge"
        criteria: "Syntactically correct, proper joins"
        weight: 2.0
        min_passing_score: 85
        criticality: JUDGE_CRITICALITY_CRITICAL
        dimensions:
          - JUDGE_DIMENSION_QUALITY

  test_cases:
    - name: "aggregation"
      input: "Monthly revenue by category"
      max_cost_usd: 0.05

    - name: "join"
      input: "Customers with orders"

    - name: "filter"
      input: "Orders from last week"
```

Run:

```bash
looms eval run examples/eval-suites/comprehensive.yaml
```

View results:

```bash
sqlite3 ./results.db "SELECT test_name, passed, cost_usd FROM test_case_results"
```

## Troubleshooting

### All Judges Failing

1. Check judge criteria match your domain
2. Verify agent output format
3. Lower `min_passing_score` during calibration

### Timeout Errors

1. Increase `timeout_seconds` in config
2. Use ASYNC execution for slow judges
3. Simplify judge prompts

### Hawk Export Fails

Results still save to SQLite. Check Hawk endpoint configuration:

```bash
looms config show | grep hawk
```

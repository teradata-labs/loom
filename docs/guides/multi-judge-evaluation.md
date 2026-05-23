
# Multi-Judge Evaluation Guide

**Version**: v1.2.0

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
  - [Example 2: Streaming Multi-Judge Evaluation](#example-2-streaming-multi-judge-evaluation)
  - [Example 3: YAML Eval Suite](#example-3-yaml-eval-suite)
- [Troubleshooting](#troubleshooting)

## Feature Status

- ✅ Multi-judge evaluation via CLI (`looms judge evaluate`)
- ✅ Streaming evaluation via CLI (`looms judge evaluate-stream`)
- ✅ Judge registration from YAML (`looms judge register`)
- ✅ Judge evaluation history (`looms judge history`)
- ✅ 6 aggregation strategies (weighted-average, all-must-pass, majority-pass, any-pass, min-score, max-score)
- ✅ 3 execution modes (synchronous, asynchronous, hybrid)
- ✅ 7 judge dimensions (quality, cost, safety, domain, performance, usability, custom)
- ✅ Eval suite runner with multi-judge support (requires `hawk` build tag)
- ✅ Retry and circuit breaker support for judge registration

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

Each judge is defined in its own YAML file, matching the `JudgeConfig` proto message.
The `looms judge register` command accepts one file per judge.

Create `config/judges/quality-judge.yaml`:

```yaml
name: quality-judge
criteria: "Evaluate SQL query accuracy and completeness"
dimensions:
  - JUDGE_DIMENSION_QUALITY
weight: 2.0
min_passing_score: 80
criticality: JUDGE_CRITICALITY_CRITICAL
```

Create `config/judges/safety-judge.yaml`:

```yaml
name: safety-judge
criteria: "Check for SQL injection and unsafe operations"
dimensions:
  - JUDGE_DIMENSION_SAFETY
weight: 3.0
min_passing_score: 90
criticality: JUDGE_CRITICALITY_SAFETY_CRITICAL
```

Create `config/judges/cost-judge.yaml`:

```yaml
name: cost-judge
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
| `EXECUTION_MODE_SYNCHRONOUS` | All judges run synchronously (blocks response until complete) |
| `EXECUTION_MODE_ASYNCHRONOUS` | All judges run asynchronously (background) |
| `EXECUTION_MODE_HYBRID` | Critical judges sync, non-critical async (balanced) |

### Judge Dimensions

| Dimension | What It Measures |
|-----------|------------------|
| `JUDGE_DIMENSION_QUALITY` | Accuracy, completeness |
| `JUDGE_DIMENSION_COST` | Cost efficiency |
| `JUDGE_DIMENSION_SAFETY` | Security, compliance |
| `JUDGE_DIMENSION_DOMAIN` | Domain-specific rules |
| `JUDGE_DIMENSION_PERFORMANCE` | Latency, throughput |
| `JUDGE_DIMENSION_USABILITY` | Clarity, user experience |
| `JUDGE_DIMENSION_CUSTOM` | User-defined dimensions (requires `custom_dimension_name` field) |

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
  --agent=sql-agent \
  --prompt="Generate a query" \
  --response="SELECT * FROM users" \
  --judges=quality-judge,safety-judge \
  --aggregation=all-must-pass
```

For general evaluation:

```bash
looms judge evaluate \
  --agent=sql-agent \
  --prompt="Generate a query" \
  --response="SELECT * FROM users" \
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

Run the eval suite (requires `hawk` build tag):

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

### Example 2: Streaming Multi-Judge Evaluation

Use `evaluate-stream` for real-time progress updates during long-running evaluations:

```bash
looms judge evaluate-stream \
  --agent=sql-agent \
  --prompt="Generate a query to find top customers by revenue" \
  --response="SELECT customer_id, SUM(amount) as revenue FROM orders GROUP BY customer_id ORDER BY revenue DESC LIMIT 10" \
  --judges=quality-judge,safety-judge,cost-judge \
  --aggregation=weighted-average \
  --export-to-hawk
```

Expected output (streamed incrementally):
```
Streaming evaluation with 3 judges...
   Agent: sql-agent
   Judges: quality-judge, safety-judge, cost-judge
   Aggregation: weighted-average

────────────────────────────────────────
Judge quality-judge started (example 1)
Judge quality-judge completed (320ms, score: 92/100)
Judge safety-judge started (example 1)
Judge safety-judge completed (280ms, score: 95/100)
Judge cost-judge started (example 1)
Judge cost-judge completed (150ms, score: 78/100)
Example 1/1 completed (score: 88/100)

────────────────────────────────────────
Evaluation completed! (750ms total)
```

The streaming variant accepts the same flags as `evaluate` but provides incremental progress via gRPC server-side streaming (`EvaluateWithJudgesStream` RPC).

### Example 3: YAML Eval Suite

> **Note:** The `looms eval` command requires the `hawk` build tag. Build with: `go build -tags hawk,fts5 ./cmd/looms`

Create `examples/eval-suites/sql-safety-quality.yaml`:

```yaml
apiVersion: loom/v1
kind: EvalSuite

metadata:
  name: "SQL Safety and Quality Evaluation"

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
looms eval run examples/eval-suites/sql-safety-quality.yaml
```

View results (default store path is `./evals.db`):

```bash
sqlite3 ./evals.db "SELECT test_name, passed, cost_usd FROM test_case_results"
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

---
title: "Judge CLI Guide"
weight: 5
---

# Judge CLI Guide

**Version**: v1.0.0-beta.1

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Common Tasks](#common-tasks)
  - [Evaluate Agent Output](#evaluate-agent-output)
  - [Stream Evaluation Progress](#stream-evaluation-progress)
  - [Register a Judge](#register-a-judge)
  - [View Evaluation History](#view-evaluation-history)
- [Examples](#examples)
  - [Example 1: Single Judge Evaluation](#example-1-single-judge-evaluation)
  - [Example 2: Multi-Judge with Streaming](#example-2-multi-judge-with-streaming)
  - [Example 3: Evaluation from Files](#example-3-evaluation-from-files)
- [Troubleshooting](#troubleshooting)

---

## Overview

Evaluate agent outputs using CLI commands with support for multiple judges and streaming progress.

## Prerequisites

- Loom server running: `looms serve`
- At least one judge registered
- Agent with output to evaluate

## Quick Start

Evaluate an agent response:

```bash
looms judge evaluate \
  --agent=sql-agent \
  --prompt="Generate a SELECT query for all users" \
  --response="SELECT * FROM users" \
  --judges=quality-judge
```

Expected output:
```
Evaluating agent output...
  Agent: sql-agent
  Judges: quality-judge
  Aggregation: weighted-average

Judge Results:
  quality-judge: 85/100 (PASS)
    - Query is syntactically correct
    - Uses appropriate table name
    - Consider adding column selection instead of SELECT *

Overall: PASS (score: 85/100)
```

## Common Tasks

### Evaluate Agent Output

Basic evaluation with one judge:

```bash
looms judge evaluate \
  --agent=sql-agent \
  --prompt="Generate a SELECT query" \
  --response="SELECT * FROM users" \
  --judges=quality-judge
```

Evaluation with multiple judges:

```bash
looms judge evaluate \
  --agent=sql-agent \
  --prompt="Generate a query to find top customers" \
  --response="SELECT customer_id, SUM(amount) FROM orders GROUP BY customer_id ORDER BY SUM(amount) DESC LIMIT 10" \
  --judges=quality-judge,safety-judge,cost-judge \
  --aggregation=weighted-average
```

### Stream Evaluation Progress

For long evaluations, use streaming:

```bash
looms judge evaluate-stream \
  --agent=sql-agent \
  --prompt="Analyze quarterly sales trends" \
  --response="SELECT region, SUM(revenue)..." \
  --judges=quality-judge,safety-judge,cost-judge
```

Expected output:
```
Streaming evaluation with 3 judges...
--------------------------------------------------------------------------------
Judge quality-judge started (example 1)
Judge quality-judge completed (1245ms, score: 85/100)
Judge safety-judge started (example 1)
Judge safety-judge completed (980ms, score: 92/100)
Judge cost-judge started (example 1)
Judge cost-judge completed (756ms, score: 78/100)
--------------------------------------------------------------------------------

Evaluation completed! (2981ms total)

Overall Verdict: PASS (score: 85.0/100)
```

Press **Ctrl+C** to cancel.

### Register a Judge

Create a judge configuration file `config/judges/quality-judge.yaml`:

```yaml
name: quality-judge
criteria: "Evaluate SQL query accuracy and completeness"
dimensions:
  - JUDGE_DIMENSION_QUALITY
weight: 2.0
min_passing_score: 80
criticality: JUDGE_CRITICALITY_CRITICAL
type: JUDGE_TYPE_HAWK
model: claude-sonnet-4-5
```

Register the judge:

```bash
looms judge register config/judges/quality-judge.yaml
```

Register with retry and circuit breaker:

```bash
looms judge register config/judges/quality-judge.yaml \
  --max-attempts=3 \
  --circuit-breaker=true \
  --circuit-breaker-failure-threshold=5
```

### View Evaluation History

View all evaluations:

```bash
looms judge history
```

Filter by agent:

```bash
looms judge history --agent=sql-agent
```

Filter by time range:

```bash
looms judge history \
  --agent=sql-agent \
  --start-time=2025-12-01T00:00:00Z \
  --end-time=2025-12-10T23:59:59Z \
  --limit=50
```

## Examples

### Example 1: Single Judge Evaluation

```bash
looms judge evaluate \
  --agent=sql-agent \
  --prompt="Generate a SELECT query for all users" \
  --response="SELECT * FROM users" \
  --judges=quality-judge
```

### Example 2: Multi-Judge with Streaming

```bash
looms judge evaluate-stream \
  --agent=sql-agent \
  --prompt="Generate a complex analytics query" \
  --response="SELECT customer_id, SUM(amount) FROM orders GROUP BY customer_id" \
  --judges=quality-judge,safety-judge,cost-judge \
  --aggregation=all-must-pass \
  --export-to-hawk
```

### Example 3: Evaluation from Files

Create input files:

```bash
echo "Generate a query to find the top 10 customers by revenue" > prompt.txt
echo "SELECT customer_id, SUM(amount) as revenue FROM orders GROUP BY customer_id ORDER BY revenue DESC LIMIT 10" > response.txt
```

Run evaluation:

```bash
looms judge evaluate \
  --agent=sql-agent \
  --prompt-file=prompt.txt \
  --response-file=response.txt \
  --judges=quality-judge,safety-judge \
  --aggregation=all-must-pass \
  --export-to-hawk
```

## Aggregation Strategies

| Strategy | Description |
|----------|-------------|
| `weighted-average` | Weighted average of scores (default) |
| `all-must-pass` | All judges must pass threshold |
| `majority-pass` | More than 50% must pass |
| `any-pass` | Any judge passing is sufficient |
| `min-score` | Use lowest score |
| `max-score` | Use highest score |

## Troubleshooting

### Streaming Hangs

Check server connectivity:

```bash
looms judge history --limit=1
```

Verify judge exists:

```bash
looms judge history --judge=quality-judge --limit=1
```

### Circuit Breaker Opens

Increase failure threshold:

```bash
looms judge register config.yaml \
  --circuit-breaker-failure-threshold=10
```

### Evaluation Times Out

Increase timeout:

```bash
looms judge evaluate-stream \
  --timeout=120 \
  ...
```

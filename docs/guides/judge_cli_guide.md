
# Judge CLI Guide

**Version**: v1.2.0 | **Status**: ✅ Implemented

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Common Tasks](#common-tasks)
  - [Evaluate Agent Output](#evaluate-agent-output)
  - [Stream Evaluation Progress](#stream-evaluation-progress)
  - [Register a Judge](#register-a-judge)
  - [View Evaluation History](#view-evaluation-history)
- [CLI Flag Reference](#cli-flag-reference)
- [Examples](#examples)
  - [Example 1: Single Judge Evaluation](#example-1-single-judge-evaluation)
  - [Example 2: Multi-Judge with Streaming](#example-2-multi-judge-with-streaming)
  - [Example 3: Evaluation from Files](#example-3-evaluation-from-files)
- [Aggregation Strategies](#aggregation-strategies)
- [Troubleshooting](#troubleshooting)


## Overview

Evaluate agent outputs using CLI commands with support for multiple judges, streaming progress, and configurable aggregation strategies. The judge CLI communicates with the Loom server via gRPC (`JudgeService`).

## Prerequisites

- Loom server running: `looms serve`
- At least one judge registered (see [Register a Judge](#register-a-judge))
- Agent with output to evaluate
- Server accessible at the default address `localhost:60051` (or specify with `--server`)

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
🔍 Evaluating with 1 judges...
   Agent: sql-agent
   Judges: quality-judge
   Aggregation: weighted-average

✅ Overall Verdict: PASS (score: 85.0/100)
────────────────────────────────────────────────────────────────────────────────

📊 Judge Results (1 judges)

[1] ✅ quality-judge (claude-sonnet-4-5-20250929)
    Verdict: PASS (score: 85.0/100)
    Dimensions:
      - quality: 85.0/100
      - correctness: 90.0/100
      - completeness: 80.0/100
    Reasoning: Query is syntactically correct and uses appropriate table name.
    Suggestions:
      - Consider adding column selection instead of SELECT *
    Cost: $0.0032 | Latency: 1200ms
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

Expected output (streaming shows real-time progress per judge, then the final summary):
```
🔍 Streaming evaluation with 3 judges...
   Agent: sql-agent
   Judges: quality-judge, safety-judge, cost-judge
   Aggregation: weighted-average

────────────────────────────────────────────────────────────────────────────────
⏳ Judge quality-judge started (example 1)
✅ Judge quality-judge completed (1245ms, score: 85/100)
⏳ Judge safety-judge started (example 1)
✅ Judge safety-judge completed (980ms, score: 92/100)
⏳ Judge cost-judge started (example 1)
✅ Judge cost-judge completed (756ms, score: 78/100)
✅ Example 1/1 completed (score: 85/100)

────────────────────────────────────────────────────────────────────────────────

🎉 Evaluation completed! (2981ms total)

✅ Overall Verdict: PASS (score: 85.0/100)
────────────────────────────────────────────────────────────────────────────────

📊 Judge Results (3 judges)

[1] ✅ quality-judge (claude-sonnet-4-5-20250929)
    Verdict: PASS (score: 85.0/100)
    Dimensions:
      - quality: 85.0/100
      - correctness: 90.0/100
      - completeness: 80.0/100
    Cost: $0.0032 | Latency: 1245ms

[2] ✅ safety-judge (claude-sonnet-4-5-20250929)
    Verdict: PASS (score: 92.0/100)
    Cost: $0.0028 | Latency: 980ms

[3] ⚠️ cost-judge (claude-sonnet-4-5-20250929)
    Verdict: PARTIAL (score: 78.0/100)
    Cost: $0.0025 | Latency: 756ms

────────────────────────────────────────────────────────────────────────────────

💰 Metrics

   Pass Rate: 66.7% (WEIGHTED_AVERAGE)
   Score Range: 78.0 - 92.0 (avg: 85.0, σ: 5.7)
   Total Cost: $0.0085
   Total Time: 2981ms
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

## CLI Flag Reference

### Global Flags (all judge subcommands)

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `localhost:60051` | Loom server gRPC address |
| `--timeout` | `60` | Request timeout in seconds |

### `looms judge evaluate` / `looms judge evaluate-stream`

| Flag | Default | Required | Description |
|------|---------|----------|-------------|
| `--agent` | | Yes | Agent ID |
| `--prompt` | | No* | User prompt/input (inline) |
| `--prompt-file` | | No* | Read prompt from file |
| `--response` | | No* | Agent response/output (inline) |
| `--response-file` | | No* | Read response from file |
| `--judges` | | Yes | Judge IDs (comma-separated) |
| `--aggregation` | `weighted-average` | No | Aggregation strategy |
| `--export-to-hawk` | `false` | No | Export results to Hawk |
| `--fail-fast` | `false` | No | Abort if any critical judge fails |
| `--pattern` | | No | Pattern used (optional context) |

\* Either `--prompt` or `--prompt-file` is required. Either `--response` or `--response-file` is required.

### `looms judge register`

| Flag | Default | Description |
|------|---------|-------------|
| `--max-attempts` | `3` | Maximum retry attempts |
| `--initial-backoff-ms` | `1000` | Initial backoff in milliseconds |
| `--max-backoff-ms` | `8000` | Maximum backoff in milliseconds |
| `--backoff-multiplier` | `2.0` | Backoff multiplier |
| `--circuit-breaker` | `true` | Enable circuit breaker |
| `--circuit-breaker-failure-threshold` | `5` | Circuit breaker failure threshold |
| `--circuit-breaker-reset-timeout-ms` | `60000` | Circuit breaker reset timeout in ms |
| `--circuit-breaker-success-threshold` | `2` | Circuit breaker success threshold |

### `looms judge history`

| Flag | Default | Description |
|------|---------|-------------|
| `--agent` | | Filter by agent ID |
| `--judges` | | Filter by judge ID (uses first value) |
| `--pattern` | | Filter by pattern name |
| `--start-time` | | Start time (RFC3339 format) |
| `--end-time` | | End time (RFC3339 format) |
| `--limit` | `50` | Maximum number of results |
| `--offset` | `0` | Offset for pagination |

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

| Strategy | Aliases | Description |
|----------|---------|-------------|
| `weighted-average` | `weighted` | Weighted average of scores (default) |
| `all-must-pass` | `all` | All judges must pass |
| `majority-pass` | `majority` | Majority must pass (>50%) |
| `any-pass` | `any` | Any judge passing is sufficient |
| `min-score` | `min` | Use minimum score across all judges |
| `max-score` | `max` | Use maximum score across all judges |

## Troubleshooting

### Streaming Hangs

Check server connectivity:

```bash
looms judge history --limit=1
```

Verify judge exists:

```bash
looms judge history --judges=quality-judge --limit=1
```

### Circuit Breaker Opens

Increase failure threshold:

```bash
looms judge register config.yaml \
  --circuit-breaker-failure-threshold=10
```

### Evaluation Times Out

Increase timeout (default is 60 seconds):

```bash
looms judge evaluate-stream \
  --agent=sql-agent \
  --prompt="Analyze quarterly sales trends" \
  --response="SELECT region, SUM(revenue)..." \
  --judges=quality-judge,safety-judge \
  --timeout=120
```

### Server Connection Refused

Verify the server is running and reachable:

```bash
looms serve
```

If running on a non-default address, specify with `--server`:

```bash
looms judge evaluate \
  --server=myhost:60051 \
  --agent=sql-agent \
  --prompt="test" \
  --response="SELECT 1" \
  --judges=quality-judge
```

## Next Steps

- [Multi-Judge Evaluation Guide](/docs/guides/multi-judge-evaluation.md) - Multi-judge configurations and strategies
- [Judge DSPy Integration](/docs/guides/judge-dspy-integration.md) - Integrating judges with DSPy optimization
- [Judge DSPy Streaming](/docs/guides/judge-dspy-streaming.md) - Streaming evaluation with DSPy
- [Judge System Architecture](/docs/architecture/judge-system.md) - Internal architecture of the judge system

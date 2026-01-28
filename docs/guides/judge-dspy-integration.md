
# Judge DSPy Integration Guide

**Version**: v1.0.0-beta.1

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Common Tasks](#common-tasks)
  - [Create a Multi-Judge Metric](#create-a-multi-judge-metric)
  - [Bootstrap Few-Shot with Safety Filtering](#bootstrap-few-shot-with-safety-filtering)
  - [MIPRO with Cost Optimization](#mipro-with-cost-optimization)
  - [TextGrad Iterative Improvement](#textgrad-iterative-improvement)
- [Examples](#examples)
  - [Example 1: CLI Bootstrap](#example-1-cli-bootstrap)
  - [Example 2: CLI MIPRO](#example-2-cli-mipro)
  - [Example 3: Library Usage](#example-3-library-usage)
- [Troubleshooting](#troubleshooting)


## Overview

Use Loom judges as evaluation metrics for DSPy-style prompt optimization with BootstrapFewShot, MIPRO, and TextGrad teleprompters.

## Prerequisites

- Loom v1.0.0-beta.1+
- Loom server running: `looms serve`
- At least one judge registered
- Training examples in JSONL format

## Quick Start

Bootstrap few-shot demonstrations with quality and safety filtering:

```bash
looms teleprompter bootstrap \
  --agent=sql-agent \
  --trainset=examples.jsonl \
  --judges=quality-judge,safety-judge \
  --max-demos=5 \
  --min-confidence=0.85 \
  --output=demos.yaml
```

Trainset format (`examples.jsonl`):
```jsonl
{"id": "ex1", "inputs": {"query": "Get all users"}, "outputs": {"answer": "SELECT * FROM users"}}
{"id": "ex2", "inputs": {"query": "Count orders"}, "outputs": {"answer": "SELECT COUNT(*) FROM orders"}}
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
```

### Dimension Weights

Balance optimization across dimensions:

| Dimension | What It Measures | Typical Weight |
|-----------|------------------|----------------|
| `quality` | Accuracy, completeness | 0.4-0.6 |
| `safety` | Security, compliance | 0.2-0.4 |
| `cost` | Token efficiency | 0.1-0.3 |
| `domain` | Domain-specific rules | 0.1-0.3 |
| `performance` | Latency, throughput | 0.1-0.2 |
| `usability` | Clarity, UX | 0.1-0.2 |

### Aggregation Strategies

| Strategy | Use Case |
|----------|----------|
| `WEIGHTED_AVERAGE` | General evaluation (default) |
| `ALL_MUST_PASS` | Safety-critical systems |
| `MAJORITY_PASS` | Consensus-based |
| `MIN_SCORE` | Conservative evaluation |

## Common Tasks

### Create a Multi-Judge Metric

```go
import "github.com/teradata-labs/loom/pkg/metaagent/teleprompter"

metric, _ := teleprompter.NewMultiJudgeMetric(&teleprompter.MultiJudgeMetricConfig{
    Orchestrator: judgeOrchestrator,
    JudgeIDs:     []string{"quality-judge", "safety-judge", "cost-judge"},
    Aggregation:  loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
    DimensionWeights: map[string]float64{
        "quality": 0.6,
        "safety":  0.3,
        "cost":    0.1,
    },
    MinThreshold: 80.0,
})
```

### Bootstrap Few-Shot with Safety Filtering

CLI:

```bash
looms teleprompter bootstrap \
  --agent=sql-agent \
  --trainset=examples.jsonl \
  --judges=quality-judge,safety-judge \
  --aggregation=ALL_MUST_PASS \
  --min-confidence=0.9 \
  --output=demos.yaml
```

Library:

```go
metric, _ := teleprompter.NewMultiJudgeMetric(&teleprompter.MultiJudgeMetricConfig{
    Orchestrator: judgeOrchestrator,
    JudgeIDs:     []string{"quality-judge", "safety-judge"},
    Aggregation:  loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS,
    MinThreshold: 85.0,
})

bootstrap := teleprompter.NewBootstrapFewShot(tracer, registry)
result, _ := bootstrap.Compile(ctx, &teleprompter.CompileRequest{
    AgentID:  "sql-agent",
    Agent:    agent,
    Trainset: examples,
    Metric:   metric,
    Config: &loomv1.TeleprompterConfig{
        MaxBootstrappedDemos: 5,
        MinConfidence:        0.85,
    },
})

fmt.Printf("Selected %d demonstrations\n", len(result.Demonstrations))
```

### MIPRO with Cost Optimization

CLI:

```bash
looms teleprompter mipro \
  --agent=sql-agent \
  --trainset=examples.jsonl \
  --instructions=candidates.txt \
  --judges=quality-judge,cost-judge \
  --dimension-priorities='{"quality":1.0,"cost":3.0}' \
  --output=optimized.yaml
```

Instruction candidates (`candidates.txt`):
```
Generate accurate, secure SQL queries with proper validation.
Generate efficient SQL queries with clear documentation.
Create SQL queries following best practices.
```

Library:

```go
metric, _ := teleprompter.NewMultiJudgeMetric(&teleprompter.MultiJudgeMetricConfig{
    Orchestrator: judgeOrchestrator,
    JudgeIDs:     []string{"quality-judge", "cost-judge"},
    DimensionWeights: map[string]float64{
        "quality": 1.0,
        "cost":    3.0,  // Cost 3x more important
    },
})

mipro := teleprompter.NewMIPRO(tracer, registry, nil)
result, _ := mipro.Compile(ctx, &teleprompter.CompileRequest{
    AgentID:  "sql-agent",
    Agent:    agent,
    Trainset: examples,
    Metric:   metric,
    Config: &loomv1.TeleprompterConfig{
        Mipro: &loomv1.MIPROConfig{
            InstructionCandidates: []string{
                "Generate SQL queries with detailed validation...",
                "Generate SQL queries efficiently.",
                "Create SQL.",
            },
        },
    },
})

fmt.Printf("Best instruction: %s\n", result.OptimizedPrompts["system"])
```

### TextGrad Iterative Improvement

```go
engine, _ := teleprompter.NewJudgeGradientEngine(&teleprompter.JudgeGradientConfig{
    Orchestrator: judgeOrchestrator,
    JudgeIDs:     []string{"quality-judge", "safety-judge"},
    AutoApplyMode: loomv1.AutoApplyMode_AUTO_APPLY_MODE_VALIDATED,
    ValidationConfig: &loomv1.ValidationConfig{
        ValidationSet:     validationExamples,
        MinScoreDelta:     5.0,
        RollbackOnFailure: true,
    },
})

variables := []*teleprompter.Variable{
    {Name: "system_prompt", Value: "Your current prompt here"},
}

// Get textual gradients (dimension scores + suggestions)
engine.Backward(ctx, example, result, variables)

// Generate and apply improvements
improvements, _ := engine.Step(ctx, variables)

for _, imp := range improvements {
    fmt.Printf("Applied: %s (expected +%.1f%%)\n",
        imp.Description, imp.Details.ExpectedSuccessRateDelta*100)
}
```

## Examples

### Example 1: CLI Bootstrap

```bash
# Prepare training data
cat > examples.jsonl << 'EOF'
{"id": "1", "inputs": {"query": "Get all users"}, "outputs": {"answer": "SELECT * FROM users"}}
{"id": "2", "inputs": {"query": "Count orders"}, "outputs": {"answer": "SELECT COUNT(*) FROM orders"}}
{"id": "3", "inputs": {"query": "Top customers"}, "outputs": {"answer": "SELECT customer_id, SUM(amount) FROM orders GROUP BY customer_id ORDER BY SUM(amount) DESC LIMIT 10"}}
EOF

# Run bootstrap
looms teleprompter bootstrap \
  --agent=sql-agent \
  --trainset=examples.jsonl \
  --judges=quality-judge,safety-judge \
  --max-demos=5 \
  --output=demos.yaml

# View output
cat demos.yaml
```

### Example 2: CLI MIPRO

```bash
# Create instruction candidates
cat > candidates.txt << 'EOF'
Generate accurate, secure SQL queries with proper validation.
Generate efficient SQL queries with clear documentation.
Create SQL queries following best practices.
EOF

# Run MIPRO optimization
looms teleprompter mipro \
  --agent=sql-agent \
  --trainset=examples.jsonl \
  --instructions=candidates.txt \
  --judges=quality-judge,safety-judge,cost-judge \
  --dimension-priorities='{"quality":2.0,"safety":3.0,"cost":1.0}' \
  --output=optimized.yaml

# View result
cat optimized.yaml
```

### Example 3: Library Usage

```go
package main

import (
    "context"
    "fmt"
    "log"

    loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
    "github.com/teradata-labs/loom/pkg/metaagent/teleprompter"
    "github.com/teradata-labs/loom/pkg/observability"
)

func main() {
    ctx := context.Background()
    tracer := observability.NewNoOpTracer()

    // Create multi-judge metric
    metric, err := teleprompter.NewMultiJudgeMetric(&teleprompter.MultiJudgeMetricConfig{
        Orchestrator: judgeOrchestrator,
        JudgeIDs:     []string{"quality-judge", "safety-judge"},
        Aggregation:  loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
        DimensionWeights: map[string]float64{
            "quality": 0.7,
            "safety":  0.3,
        },
        MinThreshold: 80.0,
        Tracer:       tracer,
    })
    if err != nil {
        log.Fatal(err)
    }

    // Create MIPRO teleprompter
    mipro := teleprompter.NewMIPRO(tracer, teleprompter.NewRegistry(), nil)

    // Run optimization
    result, err := mipro.Compile(ctx, &teleprompter.CompileRequest{
        AgentID:  "sql-agent",
        Agent:    agent,
        Trainset: examples,
        Metric:   metric,
        Config: &loomv1.TeleprompterConfig{
            MinConfidence: 0.7,
            Mipro: &loomv1.MIPROConfig{
                InstructionCandidates: []string{
                    "Generate accurate SQL queries.",
                    "Create efficient SQL.",
                },
            },
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("Selected instruction: %s\n", result.OptimizedPrompts["system"])
    fmt.Printf("Trainset score: %.2f\n", result.TrainsetScore)
}
```

## Troubleshooting

### All Instructions Filtered

Lower the confidence threshold:

```bash
looms teleprompter mipro \
  --min-confidence=0.6 \  # Lower threshold
  ...
```

Check judge criteria aren't too strict.

### No Dimension Scores

Ensure judges have dimensions configured:

```yaml
# Wrong
- name: my-judge
  criteria: "..."

# Correct
- name: my-judge
  criteria: "..."
  dimensions:
    - JUDGE_DIMENSION_QUALITY
```

### Safety Filtering Too Aggressive

Change aggregation strategy:

```bash
# Less strict
looms teleprompter bootstrap \
  --aggregation=WEIGHTED_AVERAGE \  # Instead of ALL_MUST_PASS
  ...
```

Or lower safety weight:

```bash
looms teleprompter bootstrap \
  --dimension-weights='{"quality":0.8,"safety":0.2}' \
  ...
```

### Judge Timeouts

Increase timeout:

```go
Config: &loomv1.TeleprompterConfig{
    TimeoutSeconds: 60,  // Default is 30
}
```

---
title: "Learning Agent Guide"
weight: 9
---

# Learning Agent Guide

**Version**: v1.0.0-beta.1

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Common Tasks](#common-tasks)
  - [Analyze Pattern Effectiveness](#analyze-pattern-effectiveness)
  - [Tune Pattern Priorities](#tune-pattern-priorities)
  - [Stream Metrics](#stream-metrics)
- [Examples](#examples)
  - [Example 1: Dry Run Tuning](#example-1-dry-run-tuning)
  - [Example 2: Quality-Focused Optimization](#example-2-quality-focused-optimization)
- [Troubleshooting](#troubleshooting)

---

## Overview

Optimize pattern priorities based on real-world usage metrics using the Learning Agent CLI commands.

## Prerequisites

- Loom v1.0.0-beta.1+
- Loom server running: `looms serve`
- Pattern library YAML files
- Sufficient usage data (50+ samples per pattern recommended)

## Quick Start

Preview pattern tuning:

```bash
looms learning tune \
  --domain=sql \
  --strategy=moderate \
  --library=./pkg/patterns \
  --dry-run
```

Apply tuning:

```bash
looms learning tune \
  --domain=sql \
  --strategy=moderate \
  --library=./pkg/patterns
```

## Configuration

### Tuning Strategies

| Strategy | Adjustment Range | Use Case |
|----------|------------------|----------|
| `conservative` | +/-5 priority | Production, small tweaks |
| `moderate` | +/-15 priority | General use |
| `aggressive` | +/-30 priority | Development, major changes |

### Optimization Weights

| Weight | Default | Description |
|--------|---------|-------------|
| `quality-weight` | 0.5 | Success rate importance |
| `cost-weight` | 0.3 | LLM cost importance |
| `latency-weight` | 0.2 | Response time importance |

### Confidence Levels

| Confidence | Sample Size | Adjustment |
|------------|-------------|------------|
| LOW | <50 samples | 50% of strategy range |
| MEDIUM | 50-200 samples | 100% of strategy range |
| HIGH | >200 samples | 100% of strategy range |

## Common Tasks

### Analyze Pattern Effectiveness

View pattern performance metrics:

```bash
looms learning analyze --domain=sql --window=48
```

Expected output:
```
Pattern Analysis (12 patterns)
------------------------------
sequential_scan_detection
  Usage: 245 (233 success, 12 failures)
  Success Rate: 95.1%
  Avg Cost: $0.0023 | Avg Latency: 450ms
  Recommendation: PROMOTE

query_rewrite
  Usage: 67 (52 success, 15 failures)
  Success Rate: 77.6%
  Avg Cost: $0.0045 | Avg Latency: 890ms
  Recommendation: DEMOTE
```

### Tune Pattern Priorities

Preview changes first:

```bash
looms learning tune \
  --domain=sql \
  --strategy=moderate \
  --library=./pkg/patterns \
  --dry-run
```

Apply changes:

```bash
looms learning tune \
  --domain=sql \
  --strategy=moderate \
  --library=./pkg/patterns
```

### Stream Metrics

Monitor real-time pattern metrics:

```bash
looms learning stream --domain=sql
```

Press **Ctrl+C** to stop.

## Examples

### Example 1: Dry Run Tuning

```bash
looms learning tune \
  --domain=sql \
  --strategy=moderate \
  --library=./pkg/patterns \
  --dry-run

# Output:
# Pattern Tuning Configuration
#   Domain: sql
#   Strategy: moderate
#   Mode: DRY RUN
#
# sequential_scan_detection (sql)
#   Confidence: HIGH
#   priority: 60 -> 75
#   Rationale: High success rate 95%
#
# Tuning Summary
#   Patterns Analyzed: 12
#   Patterns Tuned: 8
#   Promoted: 5
#   Demoted: 2
#   Unchanged: 5
```

### Example 2: Quality-Focused Optimization

Prioritize accuracy over cost:

```bash
looms learning tune \
  --domain=sql \
  --strategy=aggressive \
  --quality-weight=0.8 \
  --cost-weight=0.1 \
  --latency-weight=0.1 \
  --library=./pkg/patterns
```

### Example 3: Cost-Focused Optimization

Minimize LLM costs:

```bash
looms learning tune \
  --domain=sql \
  --strategy=moderate \
  --quality-weight=0.3 \
  --cost-weight=0.6 \
  --latency-weight=0.1 \
  --library=./pkg/patterns
```

### Example 4: Production Workflow

```bash
# 1. Analyze current performance
looms learning analyze --domain=sql --window=168  # 1 week

# 2. Preview conservative tuning
looms learning tune \
  --domain=sql \
  --strategy=conservative \
  --library=/opt/loom/patterns \
  --dry-run

# 3. Apply if improvements are meaningful
looms learning tune \
  --domain=sql \
  --strategy=conservative \
  --library=/opt/loom/patterns

# 4. Monitor results
looms learning stream --domain=sql
```

## Troubleshooting

### No Patterns Found

Verify metrics collection is working:

```bash
looms learning analyze --domain=sql
```

Check library path:

```bash
ls ./pkg/patterns/**/*.yaml
```

### All Patterns Have LOW Confidence

Collect more usage data. Target 50+ usages per pattern for MEDIUM confidence.

```bash
# Use conservative strategy with low confidence
looms learning tune --strategy=conservative --library=./pkg/patterns
```

### Weights Sum Warning

The CLI auto-normalizes weights. Example:

```bash
# Input: 0.6 + 0.5 + 0.3 = 1.4
# Normalized: 0.43 + 0.36 + 0.21 = 1.0
```

### Pattern Name Mismatch

Check pattern names in metrics match YAML files:

```bash
looms learning analyze --domain=sql | grep pattern_name
grep -r "name: pattern-name" ./pkg/patterns/
```

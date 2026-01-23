# Quick Pattern Reference

## Pattern Selection Guide

```
┌─────────────────────────────────────────────────────────────┐
│  Need sequential data transformation with validation?       │
│  → 📊 PIPELINE PATTERN (01_pipeline.yaml)                   │
│     Extract → Validate → Transform → Load                   │
│     Use for: ETL, data quality, processing chains          │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│  Have completely independent tasks to run concurrently?      │
│  → ⚡ PARALLEL PATTERN (02_parallel.yaml)                   │
│     Task A ┐                                                 │
│     Task B ├─→ Collect Results                              │
│     Task C ┘                                                 │
│     Use for: Multi-channel analysis, independent gathering  │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│  Need multiple perspectives consolidated into one report?    │
│  → 🍴 FORK-JOIN PATTERN (03_fork_join.yaml)                 │
│          ┌─→ Perspective A ─┐                                │
│     Input├─→ Perspective B  ├─→ Consolidate → Report        │
│          └─→ Perspective C ─┘                                │
│     Use for: Code review, risk assessment, decisions        │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│  Need multi-level teams with specialists and synthesis?      │
│  → 🌳 HIERARCHICAL PATTERN (04_hierarchical.yaml)           │
│                  Executive                                   │
│          ┌──────────┼──────────┐                             │
│       Lead A     Lead B     Lead C                           │
│       ├──┤       ├──┤       ├──┤                             │
│      S1 S2      S3 S4      S5 S6  (Specialists)             │
│     Use for: Research, strategic planning, business cases   │
└─────────────────────────────────────────────────────────────┘
```

## Speed Comparison

**4 Tasks (5 min each):**
- Pipeline: 20 minutes (sequential)
- Parallel: 5 minutes (concurrent)
- Fork-Join: 7 minutes (concurrent + consolidation)
- Hierarchical: Varies by tree depth

## When to Use Each

| Need | Pattern |
|------|---------|
| Data quality pipeline | Pipeline |
| Multi-channel analytics | Parallel |
| Peer code review | Fork-Join |
| Strategic research | Hierarchical |
| ETL processing | Pipeline |
| Independent tests | Parallel |
| Risk assessment | Fork-Join |
| Business case | Hierarchical |

## Key Differences

```
DEPENDENCIES:
Pipeline      →→→  (high - linear chain)
Parallel      ≈≈≈  (none - independent)
Fork-Join     ≈→   (none, then join)
Hierarchical  ⇵⇵   (structured levels)

SPEED:
Pipeline      ★☆☆☆ (slowest)
Parallel      ★★★★ (fastest)
Fork-Join     ★★★☆ (fast + consolidation)
Hierarchical  ★★☆☆ (depends on depth)

COMPLEXITY:
Pipeline      ★☆☆☆ (simple)
Parallel      ★☆☆☆ (simple)
Fork-Join     ★★☆☆ (medium)
Hierarchical  ★★★☆ (complex)
```

## Quick Start

```bash
# 1. Pipeline - ETL workflow
loom weave workflow_examples/patterns/01_pipeline.yaml

# 2. Parallel - Marketing analysis
loom weave workflow_examples/patterns/02_parallel.yaml

# 3. Fork-Join - Code review
loom weave workflow_examples/patterns/03_fork_join.yaml \
  --set code_submission="$(cat code.py)"

# 4. Hierarchical - Research report
loom weave workflow_examples/patterns/04_hierarchical.yaml
```

## Decision Tree

```
Start Here
    │
    ├─ Tasks must run in order? ────YES──→ Pipeline
    │                            NO
    │                             ↓
    ├─ Need to combine results? ─NO───→ Parallel
    │                            YES
    │                             ↓
    ├─ Equal peers or hierarchy?
    │   ├─ Peers ─────────────────────→ Fork-Join
    │   └─ Hierarchy ─────────────────→ Hierarchical
```

**See `README.md` for full documentation and examples.**

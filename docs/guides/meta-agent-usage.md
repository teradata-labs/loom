
# Weaver Usage Guide

**Version**: v1.0.0-beta.1

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Common Tasks](#common-tasks)
  - [Create a Thread](#create-a-thread)
  - [List Threads](#list-threads)
  - [Preview Configuration](#preview-configuration)
  - [Connect to a Thread](#connect-to-a-thread)
- [Workflow Patterns](#workflow-patterns)
  - [Debate Mode](#debate-mode)
  - [Swarm Mode](#swarm-mode)
  - [Pipeline Mode](#pipeline-mode)
- [Examples](#examples)
  - [Example 1: File Analysis Thread](#example-1-file-analysis-thread)
  - [Example 2: SQL Performance Thread](#example-2-sql-performance-thread)
  - [Example 3: Multi-Agent Debate](#example-3-multi-agent-debate)
- [Troubleshooting](#troubleshooting)


## Overview

Create agents from natural language requirements. Describe what you need, and the weaver generates the YAML configuration automatically.

## Prerequisites

- Loom v1.0.0-beta.1+
- LLM API key configured
- `just build` completed

## Quick Start

Configure LLM provider:

```bash
bin/looms config init
bin/looms config set-key anthropic_api_key
```

Create your first thread:

```bash
bin/looms weave "I need a thread to read and analyze files"
```

Connect to the thread:

```bash
bin/loom --thread file-analyzer-abc123
```

## Common Tasks

### Create a Thread

Basic creation:

```bash
bin/looms weave "Build a PostgreSQL query analyzer"
```

Save to custom location:

```bash
bin/looms weave "Build a log analyzer" --output ./my-agents/log-analyzer.yaml
```

### List Threads

```bash
bin/looms weave list
```

Expected output:
```
NAME                    ID           STATUS    CREATED
file-analyzer-abc123    thread_123   running   2024-12-10 14:30
sql-optimizer-def456    thread_456   stopped   2024-12-09 10:15
```

### Preview Configuration

```bash
bin/looms weave "Build a SQL thread" --dry-run --show-yaml
```

### Connect to a Thread

```bash
bin/loom --thread file-analyzer-abc123 --server localhost:9090
```

## Workflow Patterns

The weaver detects keywords and generates appropriate multi-agent workflows.

### Debate Mode

**Trigger keywords**: "debate", "best", "optimize", "decide"

```bash
bin/looms weave "Build a SQL optimizer where multiple threads debate the best query plan"
```

Generates 3-5 expert threads with consensus-based resolution.

### Swarm Mode

**Trigger keywords**: "independently", "vote", "multiple reviewers"

```bash
bin/looms weave "Create a code review thread where 5 reviewers independently analyze code and vote"
```

Generates 5-7 independent evaluators with voting aggregation.

### Pipeline Mode

**Trigger keywords**: "extract", "transform", "load", "then"

```bash
bin/looms weave "Extract data from CSV, transform columns, then load into PostgreSQL"
```

Generates sequential stages with data flow between them.

### Pair Programming Mode

**Trigger keywords**: "writes code", "reviews"

```bash
bin/looms weave "Build a Go service where one thread writes code and another reviews it"
```

Generates driver and navigator agents with iterative feedback.

## Examples

### Example 1: File Analysis Thread

```bash
bin/looms weave "I need a thread to explore my codebase, read files, and search for patterns"
```

Generated configuration:
- Domain: `file`
- Backend: `./examples/backends/file.yaml`
- Tools: `read_file`, `write_file`, `list_files`

Connect and use:

```bash
bin/loom --thread file-explorer-abc123

> "Read the main.go file"
> "List all Go files in pkg/agent/"
> "Search for files containing 'ExecutionBackend'"
```

### Example 2: SQL Performance Thread

```bash
bin/looms weave "Build a PostgreSQL thread that analyzes slow queries and suggests indexes"
```

Generated configuration:
- Domain: `sql`
- Backend: `./examples/backends/postgres.yaml`
- Patterns: `sequential_scan_detection`, `missing_index_analysis`, `join_optimization`
- Tools: `execute_sql`, `get_schema`, `explain_plan`

Connect and use:

```bash
bin/loom --thread sql-postgres-performance-agent-def456

> "Analyze this query: SELECT * FROM orders JOIN customers ON orders.customer_id = customers.id"
> "Show me slow queries from the past hour"
> "What indexes should I add?"
```

### Example 3: Multi-Agent Debate

```bash
bin/looms weave "Build a SQL optimizer where 3 experts debate the best query plan" --spawn-window
```

Generated workflow:
- 3 expert threads (index, query rewrite, join strategy)
- 5 debate rounds
- Consensus merge strategy

The `--spawn-window` flag opens separate terminal windows for each agent.

## Backend Selection

The weaver selects backends based on keywords:

| Keywords | Selected Backend |
|----------|------------------|
| files, codebase, documents | `file.yaml` |
| PostgreSQL, Postgres, SQL | `postgres.yaml` |
| API, HTTP, REST | `public-api.yaml` |
| SQLite, local database | `sqlite.yaml` |

For API backends, set the base URL:

```bash
export API_BASE_URL=https://api.github.com
bin/looms weave "Create a GitHub repository explorer"
```

## Troubleshooting

### No LLM Provider Configured

```bash
bin/looms config init
bin/looms config set-key anthropic_api_key
```

### Backend Connection Failed

For API backends:

```bash
export API_BASE_URL=https://api.example.com
```

For SQL backends, verify connection in the agent config:

```bash
vim $LOOM_DATA_DIR/threads/<agent-name>.yaml
```

### Agent Spawn Failed

View validation errors:

```bash
bin/looms weave "<requirements>" --dry-run --show-yaml
```

### Pattern Not Found

List available patterns:

```bash
ls patterns/
```

Check pattern exists:

```bash
grep -r "name: <pattern-name>" patterns/
```


# Weaver Examples

**Version**: v1.2.0

## Table of Contents

- [Overview](#overview)
- [Single Thread Examples](#single-thread-examples)
  - [File Analysis](#file-analysis)
  - [PostgreSQL Performance](#postgresql-performance)
  - [GitHub API Explorer](#github-api-explorer)
  - [Data Quality](#data-quality)
- [Multi-Thread Workflow Examples](#multi-thread-workflow-examples)
  - [SQL Optimizer Debate](#sql-optimizer-debate)
  - [Code Review Swarm](#code-review-swarm)
  - [ETL Pipeline](#etl-pipeline)
- [Tips](#tips)
- [Next Steps](#next-steps)


## Overview

⚠️ **Note:** These examples show *hypothetical* weaver outputs. The weaver uses LLM-powered analysis to generate agent configurations, so actual generated configs may vary based on how you phrase your requirements and which LLM provider is active.

All examples use the weaver via the TUI:

```bash
# Start the server
looms serve

# In another terminal, connect to the weaver
loom --thread weaver
# Then type your requirement in the TUI chat interface
```


## Single Thread Examples

### File Analysis

**Requirement** (type in the weaver TUI):

```
I need a thread to explore my codebase, read files, and search for patterns
```

**Generated Config**:
- Domain: `file`
- Backend: `./examples/backends/file.yaml`
- Tools: `file_read`, `file_write`, `workspace`

**Usage**:

```bash
loom --thread file-explorer-abc123

> "Read the main.go file"
> "List all Go files in pkg/agent/"
> "Search for files containing 'ExecutionBackend'"
```


### PostgreSQL Performance

**Requirement** (type in the weaver TUI):

```
Build a PostgreSQL thread that analyzes slow queries, suggests indexes, and rewrites inefficient JOINs
```

**Generated Config**:
- Domain: `sql`
- Backend: `./examples/backends/postgres.yaml`
- Patterns (from `postgres/analytics/`): `sequential_scan_detection`, `missing_index_analysis`, `join_optimization`, `query_rewrite`

**Usage**:

```bash
loom --thread sql-postgres-performance-agent-def456

> "Analyze this query: SELECT * FROM orders JOIN customers ON orders.customer_id = customers.id WHERE orders.created_at > '2024-01-01'"
> "Show me slow queries from the past hour"
> "What indexes should I add to improve performance?"
```


### GitHub API Explorer

**Requirement** (type in the weaver TUI):

```
Create a GitHub repository explorer using the public API
```

> **Note:** Set `export API_BASE_URL=https://api.github.com` before starting the server.

**Generated Config**:
- Domain: `rest`
- Backend: `./examples/backends/public-api.yaml`
- Tools: `http_request`, `shell_execute`

**Usage**:

```bash
loom --thread api-github-explorer-ghi789

> "Search for repositories related to 'LLM agents'"
> "Show me the latest pull requests for teradata-labs/loom"
> "What are the open issues in the repository?"
```


### Data Quality

**Requirement** (type in the weaver TUI):

```
I need a SQL thread for data quality checks: duplicate detection, missing values, outliers, and data profiling
```

**Generated Config**:
- Domain: `sql`
- Backend: `./examples/backends/postgres.yaml`
- Patterns (from `sql/data_quality/`): `data_profiling`, `duplicate_detection`, `outlier_detection`, `missing_value_analysis`

**Usage**:

```bash
loom --thread sql-data-quality-agent-mno345

> "Profile the customers table"
> "Find duplicate records in orders table"
> "Detect outliers in sales_amount column"
> "Show me tables with missing values"
```


## Multi-Thread Workflow Examples

> The weaver supports multiple orchestration patterns including **debate**, **pipeline**, **parallel**, **fork-join**, **conditional**, **swarm**, **pair programming**, **iterative**, and **teacher-student**. The examples below cover debate, swarm, and pipeline. See `looms workflow --help` for details.

### SQL Optimizer Debate

**Requirement** (type in the weaver TUI):

```
Build a SQL optimizer where multiple threads debate the best query plan
```

**Generated Workflow**:
- 3 expert threads (index specialist, query rewrite specialist, join strategy specialist)
- 5 debate rounds
- Consensus merge strategy

**Usage**:

```bash
# Run the workflow
looms workflow run $LOOM_DATA_DIR/workflows/sql-optimizer-debate.yaml
```

**What Happens**:
1. Each expert proposes an optimization
2. Experts critique each other's proposals
3. Refined proposals based on feedback
4. Final arguments
5. Consensus building
6. Judge synthesizes best approach


### Code Review Swarm

**Requirement** (type in the weaver TUI):

```
Create a code review thread where 5 reviewers independently analyze code and vote on issues
```

**Generated Workflow**:
- 5 reviewers with different focuses (security, performance, quality, testing, documentation)
- Independent evaluation (no communication)
- Confidence-weighted voting
- 60% acceptance threshold

**Usage**:

```bash
# Run the workflow
looms workflow run $LOOM_DATA_DIR/workflows/code-review-swarm.yaml
```

**Expected Output**:
```
Issue: "Potential race condition at line 245" - 4/5 agree (HIGH)
Issue: "Missing error check at line 189" - 5/5 agree (CRITICAL)
Issue: "Could optimize loop at line 300" - 2/5 agree (LOW)
```


### ETL Pipeline

**Requirement** (type in the weaver TUI):

```
Extract data from CSV, transform column names to snake_case, then load into PostgreSQL
```

**Generated Workflow**:
- Stage 1: Extract (CSV reader)
- Stage 2: Transform (column renamer)
- Stage 3: Load (PostgreSQL loader)
- Sequential execution with error handling

**Usage**:

```bash
looms workflow run $LOOM_DATA_DIR/workflows/csv-to-postgres-pipeline.yaml
```

> **Note:** Workflow input variables (like CSV path and target table) are defined in the workflow YAML file itself, not passed via CLI flags.

**What Happens**:
1. Extract: Read CSV, parse 10,000 rows
2. Transform: `firstName` -> `first_name`, `lastName` -> `last_name`, etc.
3. Load: Insert into PostgreSQL `customers` table


## Tips

### Be Specific

**Good** (type in the weaver TUI):
```
Build a PostgreSQL thread that detects slow queries using EXPLAIN ANALYZE, suggests B-tree indexes, and rewrites nested subqueries
```

**Poor**:
```
Make a database thread
```

### Include Trigger Keywords

| For This Workflow | Use These Keywords |
|-------------------|-------------------|
| Debate | "debate", "best", "decide", "consensus" |
| Swarm | "independently", "vote", "multiple reviewers" |
| Pipeline | "extract", "transform", "then", "load" |
| Pair Programming | "write code", "review" |

### Preview Before Creating

Use the `/agent-plan` mode in the weaver TUI to review the plan before the weaver creates anything:

```
You: /agent-plan
Weaver: Let's plan your agent. What specific problem are you solving?
You: Build a code review team
```

The weaver walks you through requirements and shows a summary before creating any files.


## Next Steps

- [Weaver Usage Guide](./weaver-usage.md) - Full weaver documentation with /agent-plan mode, skills, and configuration details
- [Meta-Agent Usage Guide](./meta-agent-usage.md) - Architecture and internals of the meta-agent system
- [Pattern Library Guide](./pattern-library-guide.md) - Browse available patterns for different domains
- [TUI Guide](./tui-guide.md) - Keyboard shortcuts, slash commands, and TUI navigation

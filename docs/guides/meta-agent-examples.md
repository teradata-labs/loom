
# Weaver Examples

**Version**: v1.0.0-beta.1

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


## Overview

Examples of creating agents from natural language requirements using the weaver.

All examples use:

```bash
bin/looms weave "<your requirement>"
```


## Single Thread Examples

### File Analysis

**Requirement**:

```bash
bin/looms weave "I need a thread to explore my codebase, read files, and search for patterns"
```

**Generated Config**:
- Domain: `file`
- Backend: `./examples/backends/file.yaml`
- Tools: `read_file`, `write_file`, `list_files`

**Usage**:

```bash
bin/loom --thread file-explorer-abc123

> "Read the main.go file"
> "List all Go files in pkg/agent/"
> "Search for files containing 'ExecutionBackend'"
```


### PostgreSQL Performance

**Requirement**:

```bash
bin/looms weave "Build a PostgreSQL thread that analyzes slow queries, suggests indexes, and rewrites inefficient JOINs"
```

**Generated Config**:
- Domain: `sql`
- Backend: `./examples/backends/postgres.yaml`
- Patterns: `sequential_scan_detection`, `missing_index_analysis`, `join_optimization`, `query_rewrite`

**Usage**:

```bash
bin/loom --thread sql-postgres-performance-agent-def456

> "Analyze this query: SELECT * FROM orders JOIN customers ON orders.customer_id = customers.id WHERE orders.created_at > '2024-01-01'"
> "Show me slow queries from the past hour"
> "What indexes should I add to improve performance?"
```


### GitHub API Explorer

**Requirement**:

```bash
export API_BASE_URL=https://api.github.com
bin/looms weave "Create a GitHub repository explorer using the public API"
```

**Generated Config**:
- Domain: `rest_api`
- Backend: `./examples/backends/public-api.yaml`
- Tools: `http_get`, `http_post`, `parse_json`

**Usage**:

```bash
bin/loom --thread api-github-explorer-ghi789

> "Search for repositories related to 'LLM agents'"
> "Show me the latest pull requests for teradata-labs/loom"
> "What are the open issues in the repository?"
```


### Data Quality

**Requirement**:

```bash
bin/looms weave "I need a SQL thread for data quality checks: duplicate detection, missing values, outliers, and data profiling"
```

**Generated Config**:
- Domain: `sql`
- Backend: `./examples/backends/postgres.yaml`
- Patterns: `data_profiling`, `duplicate_detection`, `outlier_detection`, `missing_value_analysis`

**Usage**:

```bash
bin/loom --thread sql-data-quality-agent-mno345

> "Profile the customers table"
> "Find duplicate records in orders table"
> "Detect outliers in sales_amount column"
> "Show me tables with missing values"
```


## Multi-Thread Workflow Examples

### SQL Optimizer Debate

**Requirement**:

```bash
bin/looms weave "Build a SQL optimizer where multiple threads debate the best query plan"
```

**Generated Workflow**:
- 3 expert threads (index specialist, query rewrite specialist, join strategy specialist)
- 5 debate rounds
- Consensus merge strategy

**Usage**:

```bash
# With terminal windows
bin/looms weave "Build a SQL optimizer where multiple threads debate the best query plan" --spawn-window

# Or execute manually
looms workflow execute $LOOM_DATA_DIR/threads/workflows/sql/sql-optimizer-debate.yaml
```

**What Happens**:
1. Each expert proposes an optimization
2. Experts critique each other's proposals
3. Refined proposals based on feedback
4. Final arguments
5. Consensus building
6. Judge synthesizes best approach


### Code Review Swarm

**Requirement**:

```bash
bin/looms weave "Create a code review thread where 5 reviewers independently analyze code and vote on issues"
```

**Generated Workflow**:
- 5 reviewers with different focuses (security, performance, quality, testing, documentation)
- Independent evaluation (no communication)
- Confidence-weighted voting
- 60% acceptance threshold

**Usage**:

```bash
# With terminal windows
bin/looms weave "Create a code review thread where 5 reviewers independently analyze code and vote on issues" --spawn-window

# Review a file
loom --workflow code-review-swarm

> "Review pkg/agent/agent.go for issues"
```

**Expected Output**:
```
Issue: "Potential race condition at line 245" - 4/5 agree (HIGH)
Issue: "Missing error check at line 189" - 5/5 agree (CRITICAL)
Issue: "Could optimize loop at line 300" - 2/5 agree (LOW)
```


### ETL Pipeline

**Requirement**:

```bash
bin/looms weave "Extract data from CSV, transform column names to snake_case, then load into PostgreSQL"
```

**Generated Workflow**:
- Stage 1: Extract (CSV reader)
- Stage 2: Transform (column renamer)
- Stage 3: Load (PostgreSQL loader)
- Sequential execution with error handling

**Usage**:

```bash
looms workflow execute $LOOM_DATA_DIR/threads/workflows/etl/csv-to-postgres-pipeline.yaml \
  --var input_csv_path=./data/customers.csv \
  --var target_table=customers
```

**What Happens**:
1. Extract: Read CSV, parse 10,000 rows
2. Transform: `firstName` -> `first_name`, `lastName` -> `last_name`, etc.
3. Load: Insert into PostgreSQL `customers` table


## Tips

### Be Specific

**Good**:
```bash
bin/looms weave "Build a PostgreSQL thread that detects slow queries using EXPLAIN ANALYZE, suggests B-tree indexes, and rewrites nested subqueries"
```

**Poor**:
```bash
bin/looms weave "Make a database thread"
```

### Include Trigger Keywords

| For This Workflow | Use These Keywords |
|-------------------|-------------------|
| Debate | "debate", "best", "decide", "consensus" |
| Swarm | "independently", "vote", "multiple reviewers" |
| Pipeline | "extract", "transform", "then", "load" |
| Pair Programming | "write code", "review" |

### Preview Before Spawning

```bash
bin/looms weave "Build a code review team" --dry-run --show-yaml
```

### Use Terminal Spawning for Multi-Agent

```bash
bin/looms weave "Create a debate workflow" --spawn-window
```


# Embedded Observability Guide

**Version**: v1.0.2

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Common Tasks](#common-tasks)
  - [Memory Storage](#memory-storage)
  - [SQLite Storage](#sqlite-storage)
  - [Auto-Selection Mode](#auto-selection-mode)
- [Examples](#examples)
  - [Example 1: Development Setup](#example-1-development-setup)
  - [Example 2: Production Setup](#example-2-production-setup)
- [Troubleshooting](#troubleshooting)


## Overview

Use in-process trace storage instead of an external service. Embedded mode provides zero-setup tracing with no network latency and no external dependencies.

**Benefits:**
- Zero network latency for tracing
- Single binary deployment
- Offline capability
- No external services required
- Choice of memory or SQLite storage

## Prerequisites

- Loom v1.0.2+
- Build with FTS5 tag for SQLite: `go build -tags fts5`
- No external dependencies required

## Quick Start

### Memory Storage (Fast, Ephemeral)

```yaml
# looms.yaml
observability:
  enabled: true
  mode: embedded
  storage_type: memory
  flush_interval: 5s
```

### SQLite Storage (Persistent)

```yaml
# looms.yaml
observability:
  enabled: true
  mode: embedded
  storage_type: sqlite
  sqlite_path: ./traces.db
  flush_interval: 30s
```

Start the server:

```bash
looms serve --config looms.yaml
```


## Configuration

### EmbeddedConfig Options

```yaml
observability:
  enabled: true
  mode: embedded           # embedded, service, or none

  # Storage configuration
  storage_type: sqlite     # "memory" or "sqlite"
  sqlite_path: ./traces.db # Required for sqlite storage

  # Performance tuning
  flush_interval: 30s      # How often to flush metrics
```

### Configuration via YAML

```yaml
observability:
  enabled: true
  mode: embedded
  storage_type: sqlite
  sqlite_path: /var/lib/loom/traces.db
  flush_interval: 10s
```

### Environment Variables

```bash
export LOOM_OBSERVABILITY_MODE=embedded
export LOOM_OBSERVABILITY_STORAGE_TYPE=sqlite
export LOOM_OBSERVABILITY_SQLITE_PATH=./traces.db
```


## Common Tasks

### Memory Storage

For ephemeral traces (development, testing):

**Configuration**:
```yaml
observability:
  enabled: true
  mode: embedded
  storage_type: memory
```

**Characteristics**:
- ✅ Fast (no disk I/O)
- ✅ Zero setup
- ❌ Traces lost on restart
- ❌ Limited capacity (10,000 spans default)

**Use cases**:
- Local development
- Unit/integration tests
- Temporary debugging

### SQLite Storage

For persistent traces:

**Configuration**:
```yaml
observability:
  enabled: true
  mode: embedded
  storage_type: sqlite
  sqlite_path: ./traces.db
```

**Characteristics**:
- ✅ Persistent across restarts
- ✅ Queryable with SQL
- ✅ Indexed for fast lookups
- ❌ Slightly slower than memory (disk I/O)

**Use cases**:
- Production deployments
- Long-running sessions
- Trace analysis and debugging

**Query traces with SQL**:
```bash
sqlite3 ./traces.db

# View recent sessions
SELECT id, name, status, datetime(created_at, 'unixepoch') as created
FROM evals ORDER BY created_at DESC LIMIT 10;

# View run metrics
SELECT eval_id, total_runs, successful_runs,
       ROUND(success_rate, 4) as success_rate,
       ROUND(avg_execution_time_ms, 2) as avg_ms
FROM eval_metrics;

# View detailed runs
SELECT id, session_id, model, success, execution_time_ms, token_count,
       datetime(timestamp, 'unixepoch') as time
FROM eval_runs ORDER BY timestamp DESC LIMIT 20;
```

### Auto-Selection Mode

Let Loom choose the best mode based on configuration:

**Configuration**:
```yaml
observability:
  enabled: true
  # mode not specified - auto-selects based on other settings
  storage_type: sqlite
  sqlite_path: ./traces.db
```

**Selection logic**:
1. If `hawk_endpoint` is set → service mode
2. If `storage_type` is set → embedded mode
3. Default → embedded mode with memory storage


## Examples

### Example 1: Development Setup

```yaml
# dev.yaml - Fast in-memory tracing
server:
  port: 60053
  host: localhost

llm:
  provider: anthropic
  anthropic_api_key: ${ANTHROPIC_API_KEY}
  model: claude-sonnet-4-5

observability:
  enabled: true
  mode: embedded
  storage_type: memory
  flush_interval: 5s

logging:
  level: debug
```

Start server:
```bash
looms serve --config dev.yaml
```

### Example 2: Production Setup

```yaml
# prod.yaml - Persistent SQLite tracing
server:
  port: 60053
  host: 0.0.0.0

llm:
  provider: bedrock
  bedrock_region: us-west-2
  bedrock_model: anthropic.claude-3-5-sonnet-20241022-v2:0

database:
  path: /var/lib/loom/sessions.db
  driver: sqlite

observability:
  enabled: true
  mode: embedded
  storage_type: sqlite
  sqlite_path: /var/lib/loom/traces.db
  flush_interval: 30s

logging:
  level: info
  format: json
```

Start server:
```bash
looms serve --config prod.yaml
```

Query traces:
```bash
sqlite3 /var/lib/loom/traces.db "
SELECT eval_id, total_runs, successful_runs,
       ROUND(success_rate, 4) as success_rate
FROM eval_metrics;
"
```


## Troubleshooting

### FTS5 Build Error

**Error:** `undefined: sqlite3.FTS5`

**Solution:** Build with FTS5 tag:
```bash
go build -tags fts5 -o bin/looms ./cmd/looms
```

### Database Locked

**Error:** `database is locked`

**Solution:** Reduce flush interval or use memory storage:
```yaml
observability:
  storage_type: sqlite
  flush_interval: 10s  # Smaller interval
```

Or switch to memory:
```yaml
observability:
  storage_type: memory
```

### Missing Traces

**Problem:** Traces not appearing in storage

**Solution:** Verify configuration:
```yaml
observability:
  enabled: true  # Must be true
  mode: embedded # Must be embedded
  storage_type: sqlite
  sqlite_path: ./traces.db
```

Check file permissions:
```bash
ls -l ./traces.db
# Should be writable by looms process
```

Force flush on shutdown:
```yaml
observability:
  flush_interval: 5s  # More frequent flushes
```

### High Memory Usage

**Solution:** Reduce flush interval to write to disk more frequently:
```yaml
observability:
  storage_type: sqlite
  flush_interval: 10s  # More frequent writes
```

### SQLite File Not Created

**Problem:** `traces.db` file doesn't exist

**Causes:**
1. Path not writable
2. Parent directory doesn't exist
3. Observability not enabled

**Solution:**
```bash
# Create parent directory
mkdir -p /var/lib/loom

# Set permissions
chmod 755 /var/lib/loom

# Verify configuration
grep -A 5 "observability:" looms.yaml
```


## Storage Comparison

| Storage | Persistence | Speed | Queryable | Use Case |
|---------|-------------|-------|-----------|----------|
| Memory | None | Fastest | No | Development, testing |
| SQLite | File-based | Fast | Yes (SQL) | Production, analysis |


## Database Schema

When using SQLite storage, traces are stored in three tables:

**evals** - Sessions/conversations:
```sql
CREATE TABLE evals (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    suite TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
```

**eval_runs** - Individual spans/operations:
```sql
CREATE TABLE eval_runs (
    id TEXT PRIMARY KEY,
    eval_id TEXT NOT NULL,
    query TEXT,
    model TEXT,
    response TEXT,
    execution_time_ms INTEGER NOT NULL,
    token_count INTEGER NOT NULL,
    success INTEGER NOT NULL,
    error_message TEXT,
    session_id TEXT,
    timestamp INTEGER NOT NULL,
    FOREIGN KEY (eval_id) REFERENCES evals(id)
);
```

**eval_metrics** - Aggregated metrics:
```sql
CREATE TABLE eval_metrics (
    eval_id TEXT PRIMARY KEY,
    total_runs INTEGER NOT NULL,
    successful_runs INTEGER NOT NULL,
    failed_runs INTEGER NOT NULL,
    success_rate REAL NOT NULL,
    avg_execution_time_ms REAL NOT NULL,
    total_tokens INTEGER NOT NULL,
    avg_tokens_per_run REAL NOT NULL,
    total_cost REAL NOT NULL,
    FOREIGN KEY (eval_id) REFERENCES evals(id)
);
```

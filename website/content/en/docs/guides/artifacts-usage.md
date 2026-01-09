---
title: "Artifact Management Usage"
weight: 7
---

# Artifact Management Usage Guide

**Version**: v1.0.0
**Status**: ✅ Implemented

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Common Tasks](#common-tasks)
  - [Upload an Artifact](#upload-an-artifact)
  - [List Artifacts](#list-artifacts)
  - [Search Artifacts](#search-artifacts)
  - [Read Artifact Content](#read-artifact-content)
  - [Filter by Tags](#filter-by-tags)
  - [Upload Archive Files](#upload-archive-files)
- [Using Artifact Tools in Agents](#using-artifact-tools-in-agents)
  - [List Tool](#list-tool)
  - [Search Tool](#search-tool)
  - [Get Tool](#get-tool)
  - [Read Tool](#read-tool)
  - [Write Tool](#write-tool)
- [Examples](#examples)
  - [Example 1: Upload and Search CSV File](#example-1-upload-and-search-csv-file)
  - [Example 2: Agent-Generated Report](#example-2-agent-generated-report)
  - [Example 3: Archive Handling](#example-3-archive-handling)
- [Hot-Reload Behavior](#hot-reload-behavior)
- [Troubleshooting](#troubleshooting)
- [Next Steps](#next-steps)

---

## Overview

Artifact Management provides centralized storage for files that agents need to access or generate. All artifacts are stored in `~/.loom/artifacts/` with automatic metadata extraction and full-text search.

**What you can do:**
- Upload files via TUI or drop them in `~/.loom/artifacts/`
- Search artifacts using natural language queries
- Let agents read and write artifacts during conversations
- Archive support (ZIP, TAR, TAR.GZ) - stored as-is, no auto-extraction

## Prerequisites

- Loom v1.0.0+
- FTS5-enabled SQLite (included if you built with `just build`)
- Running Loom server or TUI client

Check your installation:

```bash
# Verify artifacts directory exists
ls -la ~/.loom/artifacts/

# Verify loom.db has artifacts table
sqlite3 ~/.loom/loom.db "SELECT COUNT(*) FROM artifacts;"
```

## Quick Start

Upload a file and search for it:

```bash
# Start Loom TUI
bin/loom

# In the TUI, type:
> /upload data.csv

# Agent can now search for it:
> "Find CSV files about sales"
```

The artifact system automatically:
- Detects content type (`text/csv`)
- Computes SHA-256 checksum
- Infers tags (`["csv", "data", "tabular"]`)
- Indexes for full-text search

---

## Common Tasks

### Upload an Artifact

**Via TUI**:

```bash
bin/loom

> /upload ~/Downloads/sales_report.csv
```

Expected output:
```
Uploading sales_report.csv...
✓ Artifact uploaded successfully
  ID: 550e8400-e29b-41d4-a716-446655440000
  Name: sales_report.csv
  Size: 1.2 MB
  Type: text/csv
  Tags: csv, data, tabular, sales, report
  Checksum: a3c2f1e9b...
```

**Via Filesystem (Hot-Reload)**:

```bash
# Copy file to artifacts directory
cp ~/Documents/config.yaml ~/.loom/artifacts/

# Wait 500ms (debounce period)
# File automatically indexed
```

Expected log output:
```
INFO  Artifact hot-reload started  directory=~/.loom/artifacts debounce_ms=500
INFO  New artifact detected  path=~/.loom/artifacts/config.yaml
INFO  Artifact indexed  id=660e8400-... name=config.yaml content_type=application/x-yaml size_bytes=2048
```

### List Artifacts

**List all artifacts**:

In conversation with agent:
```
User: "Show me all artifacts"

Agent uses list_artifacts tool with no filters.
```

Expected response:
```json
{
  "artifacts": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "name": "sales_report.csv",
      "source": "user",
      "content_type": "text/csv",
      "size_bytes": 1258291,
      "created_at": "2025-01-15T10:30:00Z",
      "tags": ["csv", "data", "tabular", "sales", "report"]
    },
    {
      "id": "660e8400-e29b-41d4-a716-446655440001",
      "name": "config.yaml",
      "source": "user",
      "content_type": "application/x-yaml",
      "size_bytes": 2048,
      "created_at": "2025-01-15T10:35:00Z",
      "tags": ["yaml", "config", "structured"]
    }
  ],
  "count": 2
}
```

**List with content type filter**:

```
User: "Show me all CSV files"

Agent uses list_artifacts with content_type="text/csv"
```

### Search Artifacts

**Full-text search**:

```
User: "Find files about sales"

Agent uses search_artifacts with query="sales"
```

Expected response:
```json
{
  "results": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "name": "sales_report.csv",
      "source": "user",
      "content_type": "text/csv",
      "size_bytes": 1258291,
      "purpose": "Q4 sales analysis",
      "tags": ["csv", "data", "sales", "report"]
    }
  ],
  "count": 1,
  "query": "sales"
}
```

**Note**: Search uses SQLite FTS5 with BM25 ranking. Results ranked by relevance (matches in name, purpose, and tags).

### Read Artifact Content

**Read text file**:

```
User: "Read the config.yaml file"

Agent uses read_artifact with name="config.yaml"
```

Expected response:
```json
{
  "artifact_id": "660e8400-e29b-41d4-a716-446655440001",
  "artifact_name": "config.yaml",
  "content_type": "application/x-yaml",
  "size_bytes": 2048,
  "encoding": "text",
  "content": "version: 1.0\nserver:\n  host: localhost\n  port: 8080\n..."
}
```

**Read binary file (base64 encoded)**:

```
User: "Read the logo.png file"

Agent uses read_artifact with name="logo.png"
```

Expected response:
```json
{
  "artifact_id": "770e8400-e29b-41d4-a716-446655440002",
  "artifact_name": "logo.png",
  "content_type": "image/png",
  "size_bytes": 45812,
  "encoding": "base64",
  "content": "iVBORw0KGgoAAAANSUhEUgAA..."
}
```

**Side effect**: Reading an artifact increments `access_count` and updates `last_accessed_at` timestamp.

### Filter by Tags

**Filter by single tag**:

```
User: "Show me all config files"

Agent uses list_artifacts with tags=["config"]
```

**Filter by multiple tags (AND logic)**:

```
User: "Show me CSV files that are reports"

Agent uses list_artifacts with tags=["csv", "report"]
```

**Note**: Multiple tags use AND logic (artifact must have ALL specified tags).

### Upload Archive Files

**Supported formats**: ZIP, TAR, TAR.GZ, TGZ, GZ

**Upload archive via TUI**:

```bash
> /upload backup.tar.gz
```

Expected output:
```
Uploading backup.tar.gz...
✓ Artifact uploaded successfully
  ID: 880e8400-e29b-41d4-a716-446655440003
  Name: backup.tar.gz
  Size: 5.3 MB
  Type: application/gzip
  Tags: archive, compressed, backup
  Checksum: b4d3e2f1a...

Note: Archive stored as-is. Use tar/unzip to extract if needed.
```

**Important**: Archives are NOT automatically extracted. This prevents security issues (zip slip attacks) and unpredictable disk usage.

**To extract manually**:

```bash
cd ~/.loom/artifacts/

# Extract tar.gz
tar -xzf backup.tar.gz

# Extract zip
unzip archive.zip

# Extracted files will be auto-indexed by hot-reload watcher
```

**Error: Cannot upload directory**:

```bash
> /upload ~/Documents/project/
```

Expected error:
```
Error: cannot upload directory: ~/Documents/project/
To upload multiple files, create a tar/zip archive first:
  tar -czf project.tar.gz ~/Documents/project/
Then upload: /upload project.tar.gz
```

---

## Using Artifact Tools in Agents

Agents have access to 5 artifact management tools. These tools are automatically registered when the server starts.

### List Tool

**Tool**: `list_artifacts`

**Purpose**: Discover available artifacts with optional filtering.

**Parameters**:
- `source` (optional): Filter by source (`"user"`, `"generated"`, `"agent"`)
- `content_type` (optional): Filter by MIME type (e.g., `"text/csv"`)
- `tags` (optional): Array of tags (all must match)
- `limit` (optional): Max results (default: 50)

**Example usage**:

```
User: "What CSV files do we have?"

Agent generates tool call:
{
  "tool": "list_artifacts",
  "parameters": {
    "content_type": "text/csv",
    "limit": 10
  }
}

Response: JSON array of artifact summaries
```

### Search Tool

**Tool**: `search_artifacts`

**Purpose**: Full-text search across artifact names, purposes, and tags.

**Parameters**:
- `query` (required): Search query (e.g., `"sales report"`)
- `limit` (optional): Max results (default: 20)

**Example usage**:

```
User: "Find anything related to Q4 performance"

Agent generates tool call:
{
  "tool": "search_artifacts",
  "parameters": {
    "query": "Q4 performance",
    "limit": 5
  }
}

Response: JSON array ranked by relevance (BM25)
```

**Query syntax**:
- `"sales"` - Simple term match
- `"sales report"` - OR match (either term)
- `"\"sales report\""` - Exact phrase match
- `"sales AND report"` - Both terms required
- `"sale*"` - Prefix match (sales, salesman, etc.)

### Get Tool

**Tool**: `get_artifact`

**Purpose**: Retrieve detailed metadata for a specific artifact.

**Parameters**:
- `id` (optional): Artifact UUID
- `name` (optional): Artifact filename (one of id/name required)

**Example usage**:

```
User: "Show me details about config.yaml"

Agent generates tool call:
{
  "tool": "get_artifact",
  "parameters": {
    "name": "config.yaml"
  }
}

Response: Full metadata (checksum, access_count, timestamps, etc.)
```

### Read Tool

**Tool**: `read_artifact`

**Purpose**: Read file content with automatic encoding detection.

**Parameters**:
- `id` (optional): Artifact UUID
- `name` (optional): Artifact filename (one of id/name required)
- `encoding` (optional): `"text"` or `"base64"` (auto-detected if not specified)
- `max_size_mb` (optional): Size limit in MB (default: 10)

**Example usage**:

```
User: "What's in the sales_report.csv file?"

Agent generates tool call:
{
  "tool": "read_artifact",
  "parameters": {
    "name": "sales_report.csv",
    "max_size_mb": 10
  }
}

Response: File content as text (CSV is text, so encoding="text")
```

**Size limits**:
- Default: 10MB max
- Configurable per request
- Prevents OOM from large files

**Side effects**:
- Increments `access_count`
- Updates `last_accessed_at` timestamp

### Write Tool

**Tool**: `write_artifact`

**Purpose**: Create new artifact from generated content.

**Parameters**:
- `name` (required): Filename (e.g., `"report.csv"`)
- `content` (required): File content (text or base64)
- `encoding` (optional): `"text"` or `"base64"` (default: `"text"`)
- `purpose` (optional): Description of artifact purpose
- `tags` (optional): Array of custom tags
- `overwrite` (optional): Replace existing file (default: `false`)

**Example usage**:

```
User: "Generate a summary report and save it"

Agent generates content, then:
{
  "tool": "write_artifact",
  "parameters": {
    "name": "summary_report.md",
    "content": "# Summary Report\n\n## Key Findings\n...",
    "purpose": "Q4 performance summary",
    "tags": ["report", "summary", "Q4"],
    "overwrite": false
  }
}

Response: Artifact ID, checksum, auto-inferred tags
```

**Auto-inferred tags**: System automatically adds tags based on content type and filename patterns.

**Overwrite protection**:
```
Agent tries to write existing file without overwrite=true:
Error: "artifact already exists: report.csv (use overwrite=true to replace)"
```

---

## Examples

### Example 1: Upload and Search CSV File

**Scenario**: Upload a sales dataset and search for it.

**Step 1: Upload via TUI**

```bash
bin/loom

> /upload ~/Downloads/q4_sales.csv
```

Output:
```
Uploading q4_sales.csv...
✓ Artifact uploaded successfully
  ID: 990e8400-e29b-41d4-a716-446655440004
  Name: q4_sales.csv
  Size: 2.5 MB
  Type: text/csv
  Tags: csv, data, tabular, sales
  Checksum: c5f4g3h2i...
```

**Step 2: Search for it**

```
User: "Find the Q4 sales data"

Agent: I found the Q4 sales file.

Tool call:
{
  "tool": "search_artifacts",
  "parameters": {
    "query": "Q4 sales",
    "limit": 5
  }
}

Response:
{
  "results": [
    {
      "id": "990e8400-e29b-41d4-a716-446655440004",
      "name": "q4_sales.csv",
      "source": "user",
      "content_type": "text/csv",
      "size_bytes": 2621440,
      "tags": ["csv", "data", "tabular", "sales"]
    }
  ],
  "count": 1
}
```

**Step 3: Read CSV content**

```
User: "Show me the first few rows"

Agent: Here are the first rows from q4_sales.csv:

Tool call:
{
  "tool": "read_artifact",
  "parameters": {
    "name": "q4_sales.csv",
    "max_size_mb": 10
  }
}

Response:
{
  "artifact_name": "q4_sales.csv",
  "content_type": "text/csv",
  "encoding": "text",
  "content": "date,product,quantity,revenue\n2024-10-01,Widget A,150,4500\n2024-10-01,Widget B,200,8000\n..."
}

Agent: The CSV contains 4 columns (date, product, quantity, revenue) with sales data from Q4 2024.
```

### Example 2: Agent-Generated Report

**Scenario**: Agent analyzes data and saves a markdown report.

**Step 1: Agent generates report**

```
User: "Analyze the sales data and create a summary report"

Agent: I'll analyze the sales data and generate a report.

Tool call 1 (read data):
{
  "tool": "read_artifact",
  "parameters": {
    "name": "q4_sales.csv"
  }
}

Agent processes data...

Tool call 2 (write report):
{
  "tool": "write_artifact",
  "parameters": {
    "name": "q4_sales_summary.md",
    "content": "# Q4 Sales Summary\n\n## Total Revenue: $1.2M\n\n### Top Products:\n1. Widget B - $400K\n2. Widget A - $350K\n3. Widget C - $250K\n\n### Trends:\n- Sales increased 15% month-over-month\n- Widget B showing strong growth\n- December peak: $450K\n",
    "purpose": "Q4 sales analysis summary",
    "tags": ["report", "summary", "sales", "Q4"],
    "overwrite": false
  }
}

Response:
{
  "success": true,
  "artifact_id": "aa0e8400-e29b-41d4-a716-446655440005",
  "name": "q4_sales_summary.md",
  "content_type": "text/markdown",
  "size_bytes": 342,
  "checksum": "d6g5h4i3j...",
  "tags": ["report", "summary", "sales", "Q4", "markdown", "documentation"]
}

Agent: I've created a summary report saved as q4_sales_summary.md. The report shows total revenue of $1.2M with Widget B as the top product.
```

**Step 2: User reads generated report**

```bash
# Via CLI
cat ~/.loom/artifacts/q4_sales_summary.md

# Or in next conversation
User: "Show me the summary report you created"

Agent: [reads and displays q4_sales_summary.md content]
```

### Example 3: Archive Handling

**Scenario**: Upload and extract a backup archive.

**Step 1: Create archive**

```bash
cd ~/Documents/project/
tar -czf project_backup.tar.gz src/ config/ data/
```

**Step 2: Upload archive**

```bash
bin/loom

> /upload ~/Documents/project_backup.tar.gz
```

Output:
```
Uploading project_backup.tar.gz...
✓ Artifact uploaded successfully
  ID: bb0e8400-e29b-41d4-a716-446655440006
  Name: project_backup.tar.gz
  Size: 15.7 MB
  Type: application/gzip
  Tags: archive, compressed, project, backup
  Checksum: e7h6i5j4k...

Note: Archive stored as-is. Extract manually if needed.
```

**Step 3: Extract manually**

```bash
cd ~/.loom/artifacts/
tar -xzf project_backup.tar.gz

# Files extracted:
# src/main.go
# src/util.go
# config/app.yaml
# data/sample.csv
```

**Step 4: Hot-reload indexes extracted files**

Expected logs:
```
INFO  New artifact detected  path=~/.loom/artifacts/src/main.go
INFO  Artifact indexed  id=cc0e8400-... name=main.go content_type=text/x-go
INFO  New artifact detected  path=~/.loom/artifacts/config/app.yaml
INFO  Artifact indexed  id=dd0e8400-... name=app.yaml content_type=application/x-yaml
INFO  New artifact detected  path=~/.loom/artifacts/data/sample.csv
INFO  Artifact indexed  id=ee0e8400-... name=sample.csv content_type=text/csv
```

**Step 5: Search extracted files**

```
User: "Show me all the files from the project backup"

Agent: I found 4 files from the backup:

Tool call:
{
  "tool": "search_artifacts",
  "parameters": {
    "query": "project backup",
    "limit": 20
  }
}

Response: [Lists extracted files + original archive]
```

---

## Hot-Reload Behavior

The artifact watcher monitors `~/.loom/artifacts/` for file changes and automatically indexes new or modified files.

**Configuration** (server startup):

```go
watcherConfig := artifacts.WatcherConfig{
    Enabled:    true,         // Enable hot-reload
    DebounceMs: 500,          // Wait 500ms after last change
    Logger:     logger,
}
```

**Event types**:

1. **CREATE**: New file dropped into directory
   - Triggers: `Analyzer.Analyze()` → `store.Index()`
   - Source: Set to `"user"`

2. **WRITE**: Existing file modified
   - Triggers: Re-analyze → `store.Update()`
   - Updates: `size_bytes`, `checksum`, `updated_at`

3. **REMOVE**: File deleted from filesystem
   - Triggers: `store.Delete(soft=true)`
   - Result: `deleted_at` timestamp set (soft delete)

**Debouncing**:

```
Editor auto-save behavior:
  t=0ms:   Write #1 → Timer started (500ms)
  t=50ms:  Write #2 → Timer canceled, restarted
  t=100ms: Write #3 → Timer canceled, restarted
  t=150ms: Write #4 → Timer canceled, restarted
  ...
  t=600ms: Timer fires → Index once

Result: 10+ rapid writes → 1 index operation
```

**Trade-off**: 500ms delay before indexing, but reduces CPU usage and database writes.

**Ignored files**:
- Hidden files (`.filename`)
- Directories
- `metadata.json` (reserved)

---

## Troubleshooting

### No Artifacts Directory

**Problem**: `~/.loom/artifacts/` does not exist.

**Solution**:

```bash
mkdir -p ~/.loom/artifacts
```

The directory is automatically created when you:
- Upload first artifact via TUI
- Start server with hot-reload enabled
- Call any artifact tool

### Upload Fails: "Cannot upload directory"

**Problem**: Tried to upload a directory instead of a file.

**Error**:
```
Error: cannot upload directory: ~/Documents/project/
To upload multiple files, create a tar/zip archive first:
  tar -czf project.tar.gz ~/Documents/project/
Then upload: /upload project.tar.gz
```

**Solution**:

```bash
# Create archive
cd ~/Documents/
tar -czf project.tar.gz project/

# Upload archive
bin/loom
> /upload ~/Documents/project.tar.gz
```

### Search Returns No Results

**Problem**: Search query returns empty results despite file existing.

**Debugging**:

```bash
# Check if artifact is indexed
sqlite3 ~/.loom/loom.db "SELECT name, tags FROM artifacts WHERE deleted_at IS NULL;"

# Check FTS5 index
sqlite3 ~/.loom/loom.db "SELECT * FROM artifacts_fts5 WHERE artifacts_fts5 MATCH 'your_query';"
```

**Common causes**:
1. File not indexed yet (wait 500ms for hot-reload)
2. Query terms don't match name/purpose/tags
3. File soft-deleted (check `deleted_at` column)

**Solution**:
- Wait for hot-reload debounce period
- Use broader search terms
- Use `list_artifacts` with filters instead

### Read Artifact: "Artifact too large"

**Problem**: File exceeds `max_size_mb` limit.

**Error**:
```
Error: artifact too large: 52428800 bytes (max: 10 MB)
```

**Solution**:

Increase `max_size_mb` parameter:

```
Agent tool call:
{
  "tool": "read_artifact",
  "parameters": {
    "name": "large_file.csv",
    "max_size_mb": 50
  }
}
```

**Caution**: Very large files (>100MB) may cause memory issues. Consider streaming or chunking for production use.

### Write Artifact: "Artifact already exists"

**Problem**: Tried to create artifact with existing name.

**Error**:
```
Error: artifact already exists: report.csv (use overwrite=true to replace)
```

**Solution**:

Set `overwrite=true`:

```
{
  "tool": "write_artifact",
  "parameters": {
    "name": "report.csv",
    "content": "...",
    "overwrite": true
  }
}
```

**Side effect**: Existing artifact is hard-deleted (file + database row removed) before writing new file.

### Hot-Reload Not Working

**Problem**: Files dropped into `~/.loom/artifacts/` are not indexed.

**Debugging**:

1. Check if hot-reload is enabled:
```bash
# Look for this log at server startup
grep "Artifact hot-reload started" ~/.loom/logs/server.log
```

2. Check for watcher errors:
```bash
grep "Artifact watcher error" ~/.loom/logs/server.log
```

**Common causes**:
1. Hot-reload disabled in config (`Enabled: false`)
2. fsnotify limit reached (Linux only, check `ulimit -n`)
3. Network filesystem (NFS, SMB) - fsnotify unreliable

**Solution**:
- Ensure `WatcherConfig.Enabled = true`
- Increase file descriptor limit (Linux): `ulimit -n 8192`
- Use local filesystem for `~/.loom/artifacts/`

---

## Next Steps

- **Architecture Details**: See [Artifact Management Architecture](../architecture/artifacts.md) for design rationale and performance characteristics
- **Agent Integration**: See [Zero-Code Implementation Guide](./zero-code-implementation-guide.md) for configuring agents with artifact tools
- **Pattern Library**: Combine artifacts with patterns for domain-specific workflows (e.g., SQL analysis, data transformation)
- **MCP Integration**: Expose artifact management via Model Context Protocol for Claude Desktop, Zed, etc.

**Related Guides**:
- [Weaver Usage](./weaver-usage.md) - Generate agents that use artifact tools
- [Pattern Library Guide](./pattern-library-guide.md) - Domain-specific artifact processing patterns

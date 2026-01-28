# Artifact Management Usage Guide

**Version**: v1.0.2
**Status**: ✅ Implemented
**Last Updated**: 2026-01-21

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
  - [Session-Based Organization](#session-based-organization)
  - [Artifacts vs Scratchpad](#artifacts-vs-scratchpad)
  - [Directory Structure](#directory-structure)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [CLI Commands](#cli-commands)
  - [List Artifacts](#list-artifacts)
  - [Search Artifacts](#search-artifacts)
  - [Show Artifact Details](#show-artifact-details)
  - [Upload Artifacts](#upload-artifacts)
  - [Download Artifacts](#download-artifacts)
  - [Delete Artifacts](#delete-artifacts)
  - [View Statistics](#view-statistics)
- [Agent Tools](#agent-tools)
  - [Workspace Tool](#workspace-tool)
  - [Shell Execute Tool](#shell-execute-tool)
- [Common Tasks](#common-tasks)
  - [Working with Session Artifacts](#working-with-session-artifacts)
  - [Using Scratchpad for Notes](#using-scratchpad-for-notes)
  - [Searching Across Artifacts](#searching-across-artifacts)
  - [Managing User Artifacts](#managing-user-artifacts)
- [Examples](#examples)
  - [Example 1: Agent Generates Report](#example-1-agent-generates-report)
  - [Example 2: Multi-Session Data Analysis](#example-2-multi-session-data-analysis)
  - [Example 3: Scratchpad for Ephemeral Notes](#example-3-scratchpad-for-ephemeral-notes)
  - [Example 4: Session Cleanup](#example-4-session-cleanup)
- [Troubleshooting](#troubleshooting)
- [Next Steps](#next-steps)

## Overview

Artifact Management provides **session-aware file storage** for agents and users. Every artifact is automatically organized by session, enabling:

- **Automatic organization** - Related artifacts grouped by session
- **Isolation** - Sessions cannot access each other's artifacts
- **Cleanup** - Delete session → artifacts automatically removed (CASCADE)
- **Search** - Full-text search with SQLite FTS5 + BM25 ranking
- **Dual storage** - Indexed artifacts vs ephemeral scratchpad

**What changed in v1.0.2:**
- ✅ Session-based directory structure
- ✅ Automatic CASCADE cleanup on session deletion
- ✅ Unified `workspace` tool (replaced 5+ separate tools)
- ✅ Scratchpad for ephemeral notes
- ✅ Shell sandboxing to session directories
- ✅ New `loom artifacts` CLI commands

## Architecture

### Session-Based Organization

Every artifact is associated with a **session ID**. When an agent creates an artifact, it's automatically saved to that session's directory:

```
$LOOM_DATA_DIR/artifacts/sessions/<session-id>/agent/<filename>
```

**Benefits:**
- No path management needed by agents - just use filenames
- Related artifacts automatically grouped
- Delete session → all artifacts deleted automatically
- Session isolation prevents cross-contamination

### Artifacts vs Scratchpad

| Feature | Artifacts | Scratchpad |
|---------|-----------|------------|
| **Purpose** | Data files, results, outputs | Notes, scratch work, drafts |
| **Indexing** | SQLite + FTS5 search | Filesystem only |
| **Persistence** | Permanent (until session deleted) | Ephemeral (session-only) |
| **Searchable** | Yes (FTS5 full-text search) | No |
| **Metadata** | Full (tags, purpose, checksums) | Minimal (filename only) |
| **Use case** | CSV files, reports, generated code | Brainstorming, temp calculations |

**Directory paths:**
- Artifacts: `$LOOM_DATA_DIR/artifacts/sessions/<session-id>/agent/<filename>`
- Scratchpad: `$LOOM_DATA_DIR/artifacts/sessions/<session-id>/scratchpad/<filename>`

### Directory Structure

```
$LOOM_DATA_DIR/artifacts/
├── user/                          # User-uploaded artifacts (no session)
│   ├── data.csv
│   └── report.pdf
├── sessions/                      # Session-namespaced artifacts
│   ├── <session-id-1>/
│   │   ├── agent/                # Agent-generated artifacts (indexed)
│   │   │   ├── analysis.txt
│   │   │   └── output.json
│   │   └── scratchpad/           # Ephemeral notes (not indexed)
│   │       └── notes.md
│   └── <session-id-2>/
│       └── agent/
│           └── result.txt
└── temp/                          # Fallback for no-session context
    └── <uuid>.tmp
```

## Prerequisites

- Loom v1.0.2+
- FTS5-enabled SQLite (included with `just build`)
- Running Loom server (`looms serve`)

**Verify installation:**

```bash
# Check Loom version
loom --version  # Should show v1.0.2 or later

# Verify artifacts directory exists
ls -la $LOOM_DATA_DIR/artifacts/

# Verify SQLite has artifacts table with session_id column
sqlite3 $LOOM_DATA_DIR/loom.db "PRAGMA table_info(artifacts);" | grep session_id
```

Expected output:
```
16|session_id|TEXT|0||0
```

## Quick Start

**Upload and search an artifact:**

```bash
# Upload a file
loom artifacts upload ~/data.csv --purpose "Q4 sales data" --tags sales,csv

# Search for it
loom artifacts search "sales data"

# View details
loom artifacts show data.csv
```

**Agent usage:**

```bash
# Start a session
loom --thread sql-agent

# Agent creates artifact
> "Analyze this data and save results to results.txt"

# Agent can read it later
> "Read results.txt and summarize key findings"
```

## CLI Commands

### List Artifacts

List all artifacts with optional filtering:

```bash
# List all artifacts
loom artifacts list

# Filter by source
loom artifacts list --source user
loom artifacts list --source agent

# Filter by content type
loom artifacts list --content-type "text/csv"

# Filter by tags
loom artifacts list --tags sql,report

# Include soft-deleted artifacts
loom artifacts list --include-deleted

# Pagination
loom artifacts list --limit 50 --offset 100
```

**Example output:**
```
ID                        NAME                           SOURCE          CONTENT TYPE    SIZE
-----------------------------------------------------------------------------------------------
acf3abbb-6249-4d0e-9f0c  test-artifact.txt              user            text/plain;     23 B
73b8b126-bbf9-4e15-aad1  analysis.csv                   agent           text/csv        1.2 KB
be77d98b-f66c-480e-9a89  report.txt                     agent           text/plain      350 B

Showing 3 of 3 artifact(s)
```

### Search Artifacts

Full-text search with FTS5 + BM25 ranking:

```bash
# Simple search
loom artifacts search "sales report"

# Boolean operators
loom artifacts search "excel AND quarterly"

# Limit results
loom artifacts search --limit 50 "csv data"
```

**Example output:**
```
ID                        NAME                           SOURCE          PURPOSE
-----------------------------------------------------------------------------------------------
be77d98b-f66c-480e-9a89  sales-report.txt               agent           Q4 sales analysis
acf3abbb-6249-4d0e-9f0c  sales-data.csv                 user            Sales data for analysis

Found 2 artifact(s) matching: sales report
```

### Show Artifact Details

Display full metadata for an artifact:

```bash
# By name
loom artifacts show data.csv

# By ID
loom artifacts show acf3abbb-6249-4d0e-9f0c-f1b501d19924
```

**Example output:**
```
ID: acf3abbb-6249-4d0e-9f0c-f1b501d19924
Name: data.csv
Path: $LOOM_DATA_DIR/artifacts/sessions/sess_abc123/agent/data.csv
Source: agent
Purpose: Q4 sales analysis
Content Type: text/csv
Size: 1.2 KB (1234 bytes)
Checksum: 42dc0e959923a7683972e22142271d4f892cd8399c2d357857f6e941fdb9f6d2
Created: 2026-01-21T15:30:00-08:00 (2 hours ago)
Updated: 2026-01-21T15:30:00-08:00 (2 hours ago)
Access Count: 5
Tags: csv, data, sales
```

### Upload Artifacts

Upload files with purpose and tags:

```bash
# Basic upload
loom artifacts upload ~/data.csv

# With metadata
loom artifacts upload report.pdf --purpose "Q4 sales report" --tags quarterly,sales,2024

# Multiple files (via archive)
tar -czf mydata.tar.gz mydata/
loom artifacts upload mydata.tar.gz --purpose "Data dump" --tags archive,backup
```

**Example output:**
```
Uploaded artifact: data.csv
  ID: acf3abbb-6249-4d0e-9f0c-f1b501d19924
  Size: 1.2 KB
  Content Type: text/csv
  Tags: csv, data, tabular
```

### Download Artifacts

Download artifact content to a file:

```bash
# Download by name (uses original filename)
loom artifacts download data.csv

# Download by ID with custom output path
loom artifacts download acf3abbb-6249-4d0e-9f0c-f1b501d19924 --output ~/Downloads/backup.csv

# Download to specific location
loom artifacts download report.pdf --output ~/Documents/q4-report.pdf
```

**Example output:**
```
Downloaded artifact to: data.csv
  Size: 1.2 KB
```

### Delete Artifacts

Soft or hard delete artifacts:

```bash
# Soft delete (30-day recovery window)
loom artifacts delete data.csv

# Hard delete (permanent, cannot be recovered)
loom artifacts delete old-data.csv --hard
```

**Example output:**
```
Soft deleted artifact: data.csv (can be recovered within 30 days)
```

### View Statistics

Show storage statistics:

```bash
loom artifacts stats
```

**Example output:**
```
Artifact Storage Statistics
==================================================
Total Files:     42
Total Size:      15.3 MB (16040960 bytes)
User Files:      8
Generated Files: 34
Deleted Files:   3 (soft-deleted, recoverable)
```

## Agent Tools

### Workspace Tool

**Unified interface for artifacts and scratchpad.** Replaces 5+ separate tools with one session-aware tool.

**Actions:**
- `write` - Create artifact or scratchpad entry
- `read` - Read artifact or scratchpad entry
- `list` - List artifacts or scratchpad entries
- `search` - Search artifacts (FTS5 full-text)
- `delete` - Delete artifact or scratchpad entry

**Parameters:**
```json
{
  "action": "write|read|list|search|delete",
  "scope": "artifact|scratchpad",  // Default: "artifact"
  "filename": "data.csv",
  "content": "...",
  "purpose": "Analysis results",
  "tags": ["analysis", "sql"]
}
```

**Examples:**

**Write artifact (indexed, searchable):**
```javascript
workspace({
  action: "write",
  scope: "artifact",
  filename: "analysis.txt",
  content: "Key findings: ...",
  purpose: "Analysis results",
  tags: ["analysis", "sql"]
})
```

**Write to scratchpad (ephemeral, not indexed):**
```javascript
workspace({
  action: "write",
  scope: "scratchpad",
  filename: "notes.md",
  content: "Brainstorming ideas..."
})
```

**List artifacts:**
```javascript
workspace({
  action: "list",
  scope: "artifact"
})
```

**Search artifacts:**
```javascript
workspace({
  action: "search",
  query: "sales report"
})
```

**Read artifact:**
```javascript
workspace({
  action: "read",
  filename: "data.csv"
})
```

### Shell Execute Tool

**Session-aware shell execution with path restrictions.**

**Features:**
- Default working directory: Session artifact directory
- Read access: Session artifacts + scratchpad + documentation
- Write access: Only session directories (artifacts, scratchpad)
- Automatic path validation - commands outside session boundaries are blocked

**Environment variables available:**
```bash
$LOOM_DATA_DIR             # Loom data directory
$SESSION_ARTIFACT_DIR      # $LOOM_DATA_DIR/artifacts/sessions/<session>/agent/
$SESSION_SCRATCHPAD_DIR    # $LOOM_DATA_DIR/artifacts/sessions/<session>/scratchpad/
```

**Examples:**

**Read artifact in current session:**
```bash
shell_execute(command="cat data.csv")  # ✅ Works (in session dir)
```

**Write to session artifact dir:**
```bash
shell_execute(command="echo 'results' > analysis.txt")  # ✅ Works
```

**Write to scratchpad:**
```bash
shell_execute(command="echo 'notes' > $SESSION_SCRATCHPAD_DIR/notes.md")  # ✅ Works
```

**Read documentation (always allowed):**
```bash
shell_execute(command="cat $LOOM_DATA_DIR/documentation/guides.md")  # ✅ Works
```

**Attempt to access other session (blocked):**
```bash
shell_execute(command="cat ../other-session/data.csv")  # ❌ Blocked
```

**Attempt to write outside session (blocked):**
```bash
shell_execute(command="echo 'test' > /tmp/file.txt")  # ❌ Blocked
```

## Common Tasks

### Working with Session Artifacts

**Start a session and create artifacts:**

```bash
# Start session with agent
loom --thread data-analyst

# Agent creates artifact
> "Analyze this data and save results to analysis.txt"

# Agent uses workspace tool:
# workspace({action: "write", filename: "analysis.txt", content: "..."})

# List artifacts in current session
> "List all artifacts in this session"

# Agent uses workspace tool:
# workspace({action: "list"})

# Results are automatically in:
# $LOOM_DATA_DIR/artifacts/sessions/<session-id>/agent/analysis.txt
```

### Using Scratchpad for Notes

**Keep ephemeral notes that don't need indexing:**

```bash
# Start session
loom --thread brainstorm-agent

# Agent keeps notes in scratchpad
> "Keep track of ideas we discuss in scratchpad"

# Agent uses workspace tool:
# workspace({action: "write", scope: "scratchpad", filename: "ideas.md", content: "..."})

# Scratchpad not indexed - faster writes, no search clutter
# File at: $LOOM_DATA_DIR/artifacts/sessions/<session-id>/scratchpad/ideas.md
```

### Searching Across Artifacts

**Find artifacts using FTS5 full-text search:**

```bash
# Search from CLI
loom artifacts search "sales report Q4"

# Search from agent conversation
> "Search for all CSV files about sales"

# Agent uses workspace tool:
# workspace({action: "search", query: "sales CSV"})
```

### Managing User Artifacts

**Upload files for agents to use:**

```bash
# Upload file for agent access
loom artifacts upload ~/customer-data.csv --purpose "Customer analysis" --tags customers,data

# Start session
loom --thread data-analyst

# Agent can now find and use it
> "Find customer data files and analyze them"

# Agent uses workspace tool to search and read:
# workspace({action: "search", query: "customer data"})
# workspace({action: "read", filename: "customer-data.csv"})
```

## Examples

### Example 1: Agent Generates Report

**Scenario:** Agent analyzes data and generates a report artifact.

```bash
# Start session
loom --thread sql-analyst --session my-analysis

# Agent conversation
User: "Analyze sales trends and generate a report"

Agent: "I'll analyze the sales data and create a comprehensive report."
# Agent internally uses:
# workspace({
#   action: "write",
#   filename: "sales-trend-report.md",
#   content: "# Sales Trend Analysis\n\n## Key Findings\n...",
#   purpose: "Q4 sales trend analysis",
#   tags: ["sales", "analysis", "report"]
# })

Agent: "Report saved to sales-trend-report.md"

# Verify artifact exists
loom artifacts show sales-trend-report.md

# Download for review
loom artifacts download sales-trend-report.md --output ~/Downloads/report.md
```

**Result:** Artifact at `$LOOM_DATA_DIR/artifacts/sessions/my-analysis/agent/sales-trend-report.md`

### Example 2: Multi-Session Data Analysis

**Scenario:** Run multiple analysis sessions, each with isolated artifacts.

```bash
# Session 1: Q3 analysis
loom --thread data-analyst --session q3-analysis
> "Analyze Q3 sales and save to q3-results.csv"
# Creates: $LOOM_DATA_DIR/artifacts/sessions/q3-analysis/agent/q3-results.csv

# Session 2: Q4 analysis (isolated from Q3)
loom --thread data-analyst --session q4-analysis
> "Analyze Q4 sales and save to q4-results.csv"
# Creates: $LOOM_DATA_DIR/artifacts/sessions/q4-analysis/agent/q4-results.csv

# List artifacts per session
loom artifacts list  # Shows artifacts from current session only

# Search across all artifacts
loom artifacts search "Q3 OR Q4"  # Finds both
```

**Result:** Each session has isolated artifacts, but all searchable.

### Example 3: Scratchpad for Ephemeral Notes

**Scenario:** Agent keeps temporary notes during problem-solving.

```bash
loom --thread debug-agent

User: "Debug this issue and track your thought process"

Agent: "I'll keep notes in scratchpad while debugging."
# Agent uses:
# workspace({
#   action: "write",
#   scope: "scratchpad",
#   filename: "debug-notes.md",
#   content: "## Hypothesis 1\n- Checking database connection..."
# })

Agent: "Found root cause. Creating final report."
# Agent uses:
# workspace({
#   action: "write",
#   scope: "artifact",
#   filename: "bug-report.md",
#   content: "# Bug Report\n\nRoot cause: Connection timeout..."
# })

Agent: "Bug report saved to bug-report.md (searchable), debug notes in scratchpad (not indexed)"
```

**Result:**
- Searchable: `$LOOM_DATA_DIR/artifacts/sessions/<session>/agent/bug-report.md`
- Not searchable: `$LOOM_DATA_DIR/artifacts/sessions/<session>/scratchpad/debug-notes.md`

### Example 4: Session Cleanup

**Scenario:** Delete a session and verify artifacts are removed.

```bash
# Create session with artifacts
loom --thread test-agent --session temp-session
> "Create test files: test1.txt, test2.txt, test3.txt"

# Verify artifacts exist
loom artifacts list  # Shows 3 files

# Delete session
loom sessions delete temp-session

# Verify artifacts are gone (CASCADE delete)
ls $LOOM_DATA_DIR/artifacts/sessions/temp-session/  # Directory removed

# Database records also removed (foreign key CASCADE)
sqlite3 $LOOM_DATA_DIR/loom.db "SELECT COUNT(*) FROM artifacts WHERE session_id='temp-session';"
# Returns: 0
```

**Result:** Session deletion automatically removes all associated artifacts.

## Troubleshooting

### Artifact Not Found

**Symptom:** `artifact not found: <filename>`

**Cause:** Artifact is in a different session or was deleted.

**Solution:**
```bash
# Search across all sessions
loom artifacts search "<filename>"

# List with deleted artifacts
loom artifacts list --include-deleted

# Check if session was deleted
loom sessions list
```

### Permission Denied Writing Files

**Symptom:** `permission denied` when agent tries to write files

**Cause:** Shell commands restricted to session directories.

**Solution:**
```bash
# Use workspace tool instead of shell commands
# ❌ shell_execute("echo 'data' > /tmp/file.txt")  # Blocked

# ✅ workspace({action: "write", filename: "file.txt", content: "data"})
```

### Artifacts Not Searchable

**Symptom:** Artifact exists but doesn't show up in search

**Cause:** File is in scratchpad (not indexed) or search index needs update.

**Solution:**
```bash
# Check if file is in scratchpad
ls $LOOM_DATA_DIR/artifacts/sessions/<session>/scratchpad/

# Move to artifacts scope (agents should use workspace tool)
# workspace({action: "write", scope: "artifact", filename: "...", content: "..."})

# Verify FTS5 index
sqlite3 $LOOM_DATA_DIR/loom.db "SELECT * FROM artifacts_fts5 WHERE name MATCH '<filename>';"
```

### Session Directory Not Created

**Symptom:** Agent can't create artifacts, directory missing

**Cause:** Session ID not propagated to tools.

**Solution:**
```bash
# Verify session is active
loom sessions list

# Check server logs
looms serve  # Look for "context propagation" errors

# Restart server with fresh session
looms serve &
loom --thread agent-name --session new-session
```

### Disk Space Issues

**Symptom:** Upload fails with disk space error

**Cause:** Artifact storage full.

**Solution:**
```bash
# Check storage usage
loom artifacts stats

# Clean up old sessions
loom sessions list
loom sessions delete <old-session-id>

# Hard delete soft-deleted artifacts
loom artifacts list --include-deleted
loom artifacts delete <artifact-id> --hard

# Check disk space
df -h $LOOM_DATA_DIR/
```

## Next Steps

- **[Artifact Architecture](../architecture/artifacts.md)** - Deep dive into implementation details
- **[Session Management](sessions.md)** - Managing conversation sessions
- **[Agent Configuration](agent-configuration.md)** - Configuring agent tools and permissions
- **[Multi-Agent Workflows](multi-agent-usage.md)** - Sharing artifacts between agents
- **[Shell Execute Guide](shell-execute.md)** - Advanced shell command usage with session sandboxing

---

**Documentation Version:** v1.0.2
**Last Updated:** 2026-01-21
**Verified:** ✅ All commands tested and working

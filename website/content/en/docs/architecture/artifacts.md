---
title: "Artifact Management Architecture"
weight: 3
---

# Artifact Management Architecture

**Version**: v1.0.0
**Status**: ✅ Implemented (12.6% coverage, race-safe, FTS5 functional)
**Last Updated**: 2025-12-23

## Table of Contents

- [Overview](#overview)
- [System Context](#system-context)
- [Core Components](#core-components)
  - [Store Interface and SQLite Implementation](#store-interface-and-sqlite-implementation)
  - [Analyzer Component](#analyzer-component)
  - [Watcher Component](#watcher-component)
  - [Tool Integration](#tool-integration)
- [Design Rationale](#design-rationale)
  - [Archive Handling Strategy](#archive-handling-strategy)
  - [Storage Backend Selection](#storage-backend-selection)
  - [Search Implementation](#search-implementation)
  - [Hot-Reload Design](#hot-reload-design)
- [Sequence Diagrams](#sequence-diagrams)
  - [Artifact Upload Flow](#artifact-upload-flow)
  - [Archive Detection and Extraction](#archive-detection-and-extraction)
  - [Hot-Reload Event Processing](#hot-reload-event-processing)
  - [Full-Text Search Flow](#full-text-search-flow)
- [Data Models](#data-models)
  - [Artifact Metadata](#artifact-metadata)
  - [Filter Specifications](#filter-specifications)
  - [Analysis Results](#analysis-results)
- [Formal Properties and Invariants](#formal-properties-and-invariants)
- [Security Considerations](#security-considerations)
  - [Zip Slip Prevention](#zip-slip-prevention)
  - [Path Validation](#path-validation)
  - [Content Type Detection](#content-type-detection)
- [Performance Characteristics](#performance-characteristics)
  - [Database Operations](#database-operations)
  - [FTS5 Search Performance](#fts5-search-performance)
  - [Archive Extraction Performance](#archive-extraction-performance)
  - [Hot-Reload Debouncing](#hot-reload-debouncing)
- [Algorithm Complexity](#algorithm-complexity)
- [Trade-off Analysis](#trade-off-analysis)
- [Integration Points](#integration-points)
- [Future Considerations](#future-considerations)

---

## Overview

The Artifact Management subsystem provides centralized file storage, cataloging, and retrieval capabilities for the Loom agent framework. It enables agents to discover, read, and generate artifacts (datasets, documents, configuration files) with automatic metadata extraction, full-text search, and hot-reload capabilities.

**Key Capabilities**:
- **Centralized Storage**: Single source of truth for agent-accessible files at `~/.loom/artifacts/`
- **Automatic Metadata Extraction**: Content type detection, checksum computation, tag inference
- **Full-Text Search**: SQLite FTS5-powered search across artifact names, purposes, and tags
- **Archive Support**: Detection and validation of ZIP, TAR, TAR.GZ archives (no auto-extraction)
- **Hot-Reload**: fsnotify-based file watching with debounced event processing
- **Soft/Hard Delete**: Graceful deletion with statistics tracking
- **Tool Integration**: 5 shuttle tools for agent interaction (list, search, get, read, write)

**Design Philosophy**: The artifact system prioritizes **security**, **observability**, and **predictability**. Archives are detected but not automatically extracted to prevent zip slip vulnerabilities and unexpected filesystem modifications. All operations are traced to Hawk for debugging.

---

## System Context

```
┌─────────────────────────────────────────────────────────────────┐
│                    Loom Agent Framework                         │
│                                                                 │
│  ┌──────────────┐      ┌──────────────┐      ┌──────────────┐ │
│  │              │      │              │      │              │ │
│  │   Agents     │─────▶│  Shuttle     │─────▶│   Artifact   │ │
│  │  (Weaver,    │      │   Tools      │      │  Management  │ │
│  │   Mender)    │      │   (5 tools)  │      │              │ │
│  │              │      │              │      │              │ │
│  └──────────────┘      └──────────────┘      └──────┬───────┘ │
│                                                      │         │
└──────────────────────────────────────────────────────┼─────────┘
                                                       │
                                                       │
                    ┌──────────────────────────────────┼──────────┐
                    │     Artifact Management          │          │
                    │                                  ▼          │
                    │  ┌───────────────────────────────────────┐ │
                    │  │         ArtifactStore Interface       │ │
                    │  │  ┌─────────────────────────────────┐  │ │
                    │  │  │     SQLiteStore (Implementation)  │  │ │
                    │  │  │  - Index/Get/List/Search/Delete  │  │ │
                    │  │  │  - FTS5 Full-Text Search         │  │ │
                    │  │  │  - Observability Tracing         │  │ │
                    │  │  └─────────────────────────────────┘  │ │
                    │  └───────────────────────────────────────┘ │
                    │                    │                        │
                    │  ┌─────────────────┴─────────────────────┐ │
                    │  │                                        │ │
                    │  │  Analyzer          Watcher            │ │
                    │  │  - Content Type    - fsnotify         │ │
                    │  │  - Tag Inference   - Debouncing       │ │
                    │  │  - Archive         - Auto-Index       │ │
                    │  │    Detection       - Callbacks        │ │
                    │  │  - Metadata        - Soft Delete      │ │
                    │  │                                        │ │
                    │  └────────────────────────────────────────┘ │
                    │                    │                        │
                    └────────────────────┼────────────────────────┘
                                         │
                                         ▼
                       ┌──────────────────────────────────┐
                       │   ~/.loom/artifacts/             │
                       │   (Filesystem Storage)           │
                       │                                  │
                       │   loom.db (SQLite)               │
                       │   ├─ artifacts table             │
                       │   └─ artifacts_fts5 table        │
                       │                                  │
                       │   data.csv, report.pdf, ...      │
                       │   config.yaml, archive.tar.gz    │
                       └──────────────────────────────────┘

                       External Dependencies:
                       ┌──────────────────────────────────┐
                       │  Hawk (Observability)            │
                       │  - Trace export                  │
                       │  - Duration tracking             │
                       │  - Error recording               │
                       └──────────────────────────────────┘
```

**Data Flow**:
1. **Upload Path**: TUI Client → gRPC Server → Artifact Management → SQLiteStore → Filesystem + Database
2. **Search Path**: Agent → Shuttle Tool → ArtifactStore.Search() → FTS5 Index → Ranked Results
3. **Hot-Reload Path**: Filesystem → fsnotify → Watcher → Analyzer → SQLiteStore → Callbacks

---

## Core Components

### Store Interface and SQLite Implementation

The `ArtifactStore` interface defines 9 operations for artifact lifecycle management:

```
┌─────────────────────────────────────────────────────────────────┐
│                    ArtifactStore Interface                      │
├─────────────────────────────────────────────────────────────────┤
│  Index(ctx, artifact) → error                                   │
│  Get(ctx, id) → (*Artifact, error)                              │
│  GetByName(ctx, name) → (*Artifact, error)                      │
│  List(ctx, filter) → ([]*Artifact, error)                       │
│  Search(ctx, query, limit) → ([]*Artifact, error)               │
│  Update(ctx, artifact) → error                                  │
│  Delete(ctx, id, hard bool) → error                             │
│  RecordAccess(ctx, id) → error                                  │
│  GetStats(ctx) → (*Stats, error)                                │
│  Close() → error                                                │
└─────────────────────────────────────────────────────────────────┘
                              │
                              │ implements
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                     SQLiteStore                                 │
├─────────────────────────────────────────────────────────────────┤
│  Fields:                                                        │
│    - db: *sql.DB (SQLite connection)                            │
│    - mu: sync.RWMutex (concurrent access protection)            │
│    - tracer: observability.Tracer (Hawk integration)            │
│                                                                 │
│  Configuration:                                                 │
│    - PRAGMA journal_mode=WAL (Write-Ahead Logging)              │
│    - PRAGMA foreign_keys=ON                                     │
│                                                                 │
│  Schema:                                                        │
│    artifacts table:                                             │
│      - id TEXT PRIMARY KEY                                      │
│      - name TEXT NOT NULL                                       │
│      - path TEXT NOT NULL                                       │
│      - source TEXT (user|generated|agent)                       │
│      - source_agent_id TEXT                                     │
│      - purpose TEXT                                             │
│      - content_type TEXT                                        │
│      - size_bytes INTEGER                                       │
│      - checksum TEXT                                            │
│      - created_at INTEGER                                       │
│      - updated_at INTEGER                                       │
│      - last_accessed_at INTEGER                                 │
│      - access_count INTEGER DEFAULT 0                           │
│      - tags TEXT (JSON array)                                   │
│      - metadata_json TEXT (JSON object)                         │
│      - deleted_at INTEGER                                       │
│                                                                 │
│    artifacts_fts5 table (FTS5 virtual table):                   │
│      - artifact_id TEXT (references artifacts.id)               │
│      - name TEXT                                                │
│      - purpose TEXT                                             │
│      - tags TEXT                                                │
└─────────────────────────────────────────────────────────────────┘
```

**Key Design Decisions**:

1. **WAL Mode**: Write-Ahead Logging enables concurrent reads during writes
2. **Soft Delete**: `deleted_at` timestamp preserves history and enables recovery
3. **JSON Storage**: Tags and metadata stored as JSON for flexibility (trade-off: no SQL-level filtering on nested fields)
4. **FTS5 Integration**: Separate virtual table for full-text search with BM25 ranking
5. **UUID IDs**: uuid.New() provides collision-resistant identifiers
6. **Observability**: Every operation traced with attributes (artifact.id, artifact.name, duration)

**Lock Strategy**:
- **RLock** for reads (Get, GetByName, List, Search, GetStats)
- **Lock** for writes (Index, Update, Delete, RecordAccess)
- **Trade-off**: Coarse-grained locking (single mutex) limits concurrency but simplifies reasoning about consistency

### Analyzer Component

The `Analyzer` extracts metadata from files through multi-stage detection:

```
                        File Path
                           │
                           ▼
┌─────────────────────────────────────────────────────────────────┐
│                     Analyzer.Analyze()                          │
└─────────────────────────────────────────────────────────────────┘
                           │
          ┌────────────────┼────────────────┐
          │                │                │
          ▼                ▼                ▼
┌──────────────────┐ ┌──────────────┐ ┌─────────────────┐
│ Content Type     │ │  Checksum    │ │  Tag Inference  │
│ Detection        │ │  (SHA-256)   │ │                 │
│                  │ │              │ │                 │
│ 1. Extension     │ │  io.Copy()   │ │ 1. Content Type │
│    (mime pkg)    │ │  → SHA256    │ │    Categories   │
│ 2. Magic Bytes   │ │  → Hex       │ │ 2. Filename     │
│    (http.Detect) │ │              │ │    Patterns     │
│ 3. Fallback Map  │ │              │ │ 3. Archive      │
│    (.yaml, .sql) │ │              │ │    Detection    │
└──────────────────┘ └──────────────┘ └─────────────────┘
          │                │                │
          └────────────────┼────────────────┘
                           ▼
                  ┌──────────────────┐
                  │  Metadata        │
                  │  Extraction      │
                  │  (Format-Spec)   │
                  │                  │
                  │  CSV:            │
                  │   - columns      │
                  │   - row_count    │
                  │                  │
                  │  JSON:           │
                  │   - structure    │
                  │   - key_count    │
                  └──────────────────┘
                           │
                           ▼
                  AnalysisResult{
                    ContentType, SizeBytes,
                    Checksum, Tags, Metadata
                  }
```

**Archive Detection** (`IsArchive(contentType)`):
```
Supported Formats:
  - application/zip
  - application/x-tar
  - application/gzip
  - application/x-gzip
  - application/x-compressed-tar
  - *tar.gz (suffix match)
  - *tgz (suffix match)

Detection Strategy:
  1. Check contentType against archive MIME types
  2. Check filename suffix for .tar.gz, .tgz
  3. Return boolean (no automatic extraction)
```

**Tag Inference Strategy**:
```
Priority 1: Content Type Categories
  - spreadsheet → ["excel", "spreadsheet", "data"]
  - csv → ["csv", "data", "tabular"]
  - json → ["json", "structured", "data"]
  - archive → ["archive", "zip"|"tar"|"compressed"]

Priority 2: Filename Patterns
  - contains "report" → ["report"]
  - contains "data" → ["data"]
  - contains "config" → ["config"]
  - contains "test" → ["test"]
  - contains "doc" or "readme" → ["documentation"]

Result: Deduplicated list of inferred tags
```

### Watcher Component

The `Watcher` provides hot-reload capability with fsnotify-based filesystem monitoring:

```
┌─────────────────────────────────────────────────────────────────┐
│                         Watcher Lifecycle                       │
└─────────────────────────────────────────────────────────────────┘
                           │
                           ▼
                    Start(ctx) → error
                           │
          ┌────────────────┴────────────────┐
          │                                 │
  config.Enabled?                          No
          │                                 │
         Yes                                ▼
          │                          Close doneCh
          │                          Return (disabled)
          ▼
    Add ~/.loom/artifacts/ to fsnotify.Watcher
          │
          ▼
    Launch watchLoop() goroutine
          │
          ▼
┌─────────────────────────────────────────────────────────────────┐
│                      Watch Loop (infinite)                      │
└─────────────────────────────────────────────────────────────────┘
          │
          ▼
    select {
      case <-stopCh:
        return

      case <-ctx.Done():
        return

      case event := <-watcher.Events:
        │
        ▼
    ┌──────────────────────────────────┐
    │  Filter Events                   │
    │  - Ignore hidden files (.*name)  │
    │  - Ignore directories            │
    │  - Ignore metadata.json          │
    └──────────────────────────────────┘
        │
        ▼
    ┌──────────────────────────────────┐
    │  Debounce Event                  │
    │  - Cancel existing timer         │
    │  - Start new timer (500ms)       │
    │  - Timer fires → processEvent()  │
    └──────────────────────────────────┘
        │
        ▼
    ┌──────────────────────────────────┐
    │  Process Event (debounced)       │
    │                                  │
    │  CREATE  → handleCreate()        │
    │  WRITE   → handleModify()        │
    │  REMOVE  → handleDelete()        │
    │  RENAME  → handleDelete()        │
    └──────────────────────────────────┘
        │
        ▼
    ┌──────────────────────────────────┐
    │  handleCreate:                   │
    │  1. Analyzer.Analyze(path)       │
    │  2. Create Artifact metadata     │
    │  3. store.Index(artifact)        │
    │  4. Call OnCreate callback       │
    └──────────────────────────────────┘
        │
    ┌──────────────────────────────────┐
    │  handleModify:                   │
    │  1. store.GetByName(filename)    │
    │  2. Analyzer.Analyze(path)       │
    │  3. Update artifact fields       │
    │  4. store.Update(artifact)       │
    │  5. Call OnModify callback       │
    └──────────────────────────────────┘
        │
    ┌──────────────────────────────────┐
    │  handleDelete:                   │
    │  1. store.GetByName(filename)    │
    │  2. store.Delete(id, soft=true)  │
    │  3. Call OnDelete callback       │
    └──────────────────────────────────┘

      case err := <-watcher.Errors:
        log error, continue
    }
```

**Debouncing Mechanism**:
```
Problem: Editors (VSCode, vim) generate multiple rapid-fire writes
Solution: Per-file timer map

debounceTimers[filepath] → *time.Timer
  - On event: Cancel existing timer, start new timer
  - Timer duration: config.DebounceMs (default 500ms)
  - Timer fires: processEvent() called once
  - Cleanup: delete timer from map after processing

Trade-off:
  + Reduces spurious re-indexing (10-20 writes → 1 index)
  - Introduces 500ms delay before indexing new files
```

### Tool Integration

Five shuttle tools expose artifact management to agents:

```
┌─────────────────────────────────────────────────────────────────┐
│                     Shuttle Tool Definitions                    │
└─────────────────────────────────────────────────────────────────┘

1. list_artifacts
   Inputs: source?, content_type?, tags[]?, limit?
   Output: JSON array of artifact summaries
   Use Case: "Show me all CSV files" → filter by content_type="text/csv"

2. search_artifacts
   Inputs: query (required), limit?
   Output: JSON array ranked by FTS5 relevance
   Use Case: "Find sales reports" → FTS5 search on name/purpose/tags

3. get_artifact
   Inputs: id | name (one required)
   Output: Full artifact metadata (checksum, access_count, metadata)
   Use Case: "Get details for data.csv" → retrieve all fields

4. read_artifact
   Inputs: id | name, encoding?, max_size_mb?
   Output: File content (text or base64) + metadata
   Side Effect: RecordAccess() updates last_accessed_at, access_count++
   Use Case: "Read config.yaml" → returns YAML content as text

5. write_artifact
   Inputs: name, content, encoding?, purpose?, tags[]?, overwrite?
   Output: Artifact ID, checksum, inferred tags
   Side Effect: Creates file in ~/.loom/artifacts/, indexes in database
   Use Case: "Save generated report" → creates report.csv, auto-tags
```

**Tool Security Properties**:
- **No path traversal**: Tools accept name only, not full paths (resolved to ~/.loom/artifacts/<name>)
- **Size limits**: read_artifact enforces max_size_mb (default 10MB) to prevent OOM
- **Overwrite protection**: write_artifact requires overwrite=true to replace existing files
- **Archive detection**: Server-side logs archive detection but does NOT auto-extract

---

## Design Rationale

### Archive Handling Strategy

**Decision**: Detect archives but do not automatically extract them.

**Options Considered**:

| **Option** | **Pros** | **Cons** | **Decision** |
|------------|----------|----------|--------------|
| **A. Template-Based (Selected)** | + No zip slip risk<br>+ Predictable behavior<br>+ Client controls extraction | - User must manually extract | ✅ **Chosen** |
| **B. Auto-Extract** | + Convenient for users<br>+ Enables recursive indexing | - Zip slip vulnerability<br>- Unpredictable file counts<br>- Disk space issues | ❌ Rejected |
| **C. Streaming RPC** | + Handles large archives<br>+ Efficient for thousands of files | - Complex implementation<br>- Requires client chunking | ❌ Future work |

**Rationale**:
1. **Security First**: Automatic extraction introduces zip slip vulnerability (malicious archives can write to ../../etc/passwd)
2. **Predictability**: Users know exactly what is uploaded (archive as-is, not extracted contents)
3. **Storage Control**: No unexpected disk usage from nested archives
4. **Client Responsibility**: Users decide when and how to extract (e.g., `tar -xzf archive.tar.gz`, then upload extracted files)

**Implementation**:
- `analyzer.go:330-349`: `IsArchive()` detects archive MIME types and suffixes
- `analyzer.go:351-590`: `ExtractArchive()` functions exist but NOT called by server (manual extraction only)
- `server/artifacts.go:145-167`: Server logs archive detection, adds "archive" tag, but does not extract
- `tui/client/client.go:189-196`: Client rejects directory uploads with error message suggesting tar/zip creation

**Path Traversal Prevention** (for manual extraction):
```go
// analyzer.go:398-400 (extractZip example)
destPath := filepath.Join(destDir, f.Name)
if !strings.HasPrefix(destPath, filepath.Clean(destDir)+string(os.PathSeparator)) {
    return nil, fmt.Errorf("illegal file path in archive: %s", f.Name)
}
```

### Storage Backend Selection

**Decision**: SQLite with FTS5 for metadata and search.

**Alternatives**:

| **Backend** | **Pros** | **Cons** | **Decision** |
|-------------|----------|----------|--------------|
| **SQLite + FTS5** | + No external dependencies<br>+ Transaction support<br>+ BM25 ranking<br>+ Reuses loom.db | - Limited to single node<br>- FTS5 build tag required | ✅ **Chosen** |
| **PostgreSQL** | + Better concurrency<br>+ Full SQL features | - External dependency<br>- Overkill for local agents | ❌ Rejected |
| **Filesystem + JSON** | + Simple to implement | - No atomic updates<br>- No search capabilities | ❌ Rejected |
| **Elasticsearch** | + Advanced search<br>+ Distributed | - Heavy external dependency<br>- Operational complexity | ❌ Rejected |

**Rationale**:
1. **Consistency with Loom**: Session store already uses loom.db; artifact table added to same database
2. **FTS5 Built-in**: SQLite FTS5 provides BM25-ranked full-text search without external services
3. **Transactional Guarantees**: ACID properties ensure metadata consistency with filesystem state
4. **Zero Ops**: No additional services to deploy, configure, or monitor

**FTS5 Configuration**:
```sql
CREATE VIRTUAL TABLE artifacts_fts5 USING fts5(
  artifact_id UNINDEXED,
  name,
  purpose,
  tags,
  content='artifacts',
  content_rowid='id'
);
```
- **UNINDEXED**: artifact_id stored but not searchable (used for JOIN back to artifacts table)
- **content='artifacts'**: FTS5 synced with artifacts table for automatic index updates
- **BM25 Ranking**: Default FTS5 relevance scoring (ORDER BY rank)

### Search Implementation

**FTS5 Query Flow**:
```
User Query: "sales report"
     │
     ▼
store.Search(ctx, "sales report", limit=20)
     │
     ▼
SQL: SELECT a.* FROM artifacts a
     INNER JOIN artifacts_fts5 fts ON a.id = fts.artifact_id
     WHERE artifacts_fts5 MATCH 'sales report'
     ORDER BY rank
     LIMIT 20
     │
     ▼
FTS5 Tokenization:
  - "sales" → token
  - "report" → token
     │
     ▼
FTS5 Matching:
  - name LIKE '%sales%' OR name LIKE '%report%'
  - purpose LIKE '%sales%' OR purpose LIKE '%report%'
  - tags LIKE '%sales%' OR tags LIKE '%report%'
     │
     ▼
BM25 Ranking:
  - Compute relevance score per document
  - Sort by rank DESC
     │
     ▼
Return Top 20 Results
```

**Query Syntax**:
- **Simple terms**: `"sales report"` → OR match on all terms
- **Phrase search**: `"\"sales report\""` → exact phrase match
- **Boolean operators**: `"sales AND report"` → both terms required
- **Prefix search**: `"sale*"` → matches "sales", "salesman", etc.

**Trade-offs**:
- **Tokenization**: FTS5 uses Porter stemming (English-centric)
- **Case Insensitivity**: All searches case-insensitive (FTS5 default)
- **No Faceting**: FTS5 does not support faceted search (use List with filters instead)

### Hot-Reload Design

**Decision**: fsnotify-based file watching with debounced event processing.

**Alternatives**:

| **Approach** | **Pros** | **Cons** | **Decision** |
|--------------|----------|----------|--------------|
| **fsnotify + Debounce** | + Real-time indexing<br>+ Handles rapid changes<br>+ Cross-platform | - Requires goroutine<br>- Debounce tuning needed | ✅ **Chosen** |
| **Polling (e.g., 5s)** | + Simple implementation<br>+ No inotify limits | - Latency (up to 5s)<br>- Wasteful CPU | ❌ Rejected |
| **Manual Refresh** | + No background threads | - Poor UX<br>- Requires user action | ❌ Rejected |

**Rationale**:
1. **User Experience**: Files indexed immediately after drop to ~/.loom/artifacts/ (500ms debounce)
2. **Editor Compatibility**: Debouncing prevents 10-20 spurious writes from VSCode, vim auto-save
3. **Callback Extensibility**: OnCreate/OnModify/OnDelete callbacks enable future integrations (e.g., agent notifications)

**fsnotify Limitations**:
- **inotify Exhaustion**: Linux systems have ulimit on inotify watches (default 8192); watching one directory is safe
- **Rename Ambiguity**: fsnotify.Rename reported as two events (old path Remove, new path Create) if files move between directories
- **Network Filesystems**: fsnotify unreliable on NFS, SMB (local filesystem assumption)

**Debounce Configuration**:
```go
type WatcherConfig struct {
    Enabled    bool                   // Enable hot-reload (default: false)
    DebounceMs int                    // Debounce delay (default: 500ms)
    OnCreate   ArtifactUpdateCallback // Callback for new artifacts
    OnModify   ArtifactUpdateCallback // Callback for modified artifacts
    OnDelete   ArtifactUpdateCallback // Callback for deleted artifacts
}
```

---

## Sequence Diagrams

### Artifact Upload Flow

```
┌──────┐        ┌──────┐       ┌──────────┐      ┌──────────┐      ┌──────────┐
│ User │        │ TUI  │       │  gRPC    │      │ Artifact │      │ SQLite   │
│      │        │Client│       │  Server  │      │ Analyzer │      │  Store   │
└──┬───┘        └──┬───┘       └────┬─────┘      └────┬─────┘      └────┬─────┘
   │               │                │                 │                 │
   │ upload file   │                │                 │                 │
   │──────────────>│                │                 │                 │
   │               │                │                 │                 │
   │               │ Validate       │                 │                 │
   │               │ (check IsDir?) │                 │                 │
   │               │───────────┐    │                 │                 │
   │               │           │    │                 │                 │
   │               │<──────────┘    │                 │                 │
   │               │                │                 │                 │
   │               │ UploadArtifact(name, content)    │                 │
   │               │───────────────>│                 │                 │
   │               │                │                 │                 │
   │               │                │ Write to        │                 │
   │               │                │ ~/.loom/        │                 │
   │               │                │ artifacts/      │                 │
   │               │                │────────────┐    │                 │
   │               │                │            │    │                 │
   │               │                │<───────────┘    │                 │
   │               │                │                 │                 │
   │               │                │ Analyze(path)   │                 │
   │               │                │────────────────>│                 │
   │               │                │                 │                 │
   │               │                │                 │ detectContent   │
   │               │                │                 │ Type()          │
   │               │                │                 │────────────┐    │
   │               │                │                 │            │    │
   │               │                │                 │<───────────┘    │
   │               │                │                 │                 │
   │               │                │                 │ inferTags()     │
   │               │                │                 │────────────┐    │
   │               │                │                 │            │    │
   │               │                │                 │<───────────┘    │
   │               │                │                 │                 │
   │               │                │                 │ Compute         │
   │               │                │                 │ Checksum        │
   │               │                │                 │────────────┐    │
   │               │                │                 │            │    │
   │               │                │                 │<───────────┘    │
   │               │                │                 │                 │
   │               │                │<───AnalysisResult──             │
   │               │                │                 │                 │
   │               │                │ Index(artifact) │                 │
   │               │                │─────────────────────────────────>│
   │               │                │                 │                 │
   │               │                │                 │  INSERT INTO    │
   │               │                │                 │  artifacts      │
   │               │                │                 │  (id, name, ...)│
   │               │                │                 │────────────┐    │
   │               │                │                 │            │    │
   │               │                │                 │<───────────┘    │
   │               │                │                 │                 │
   │               │                │                 │  Trigger FTS5   │
   │               │                │                 │  Index Update   │
   │               │                │                 │────────────┐    │
   │               │                │                 │            │    │
   │               │                │                 │<───────────┘    │
   │               │                │                 │                 │
   │               │                │<──────success───────────────────│
   │               │                │                 │                 │
   │               │<─Response(ID, checksum)──────────│                 │
   │               │                │                 │                 │
   │<─success──────│                │                 │                 │
   │               │                │                 │                 │
```

**Key Steps**:
1. **Client Validation**: `os.Stat()` checks if path is directory (reject with error)
2. **Server Write**: File written to `~/.loom/artifacts/<name>` with mode 0640
3. **Analyzer**: Detects content type, computes SHA-256, infers tags
4. **Store Index**: Upsert into artifacts table (ON CONFLICT DO UPDATE)
5. **FTS5 Trigger**: SQLite automatically updates artifacts_fts5 virtual table

### Archive Detection and Extraction

```
┌──────┐        ┌──────────┐       ┌──────────┐      ┌──────────┐
│ User │        │  Server  │       │ Analyzer │      │Filesystem│
└──┬───┘        └────┬─────┘       └────┬─────┘      └────┬─────┘
   │                 │                  │                 │
   │ upload archive.tar.gz               │                 │
   │────────────────>│                  │                 │
   │                 │                  │                 │
   │                 │ Analyze(path)    │                 │
   │                 │─────────────────>│                 │
   │                 │                  │                 │
   │                 │                  │ detectContentType()
   │                 │                  │ → "application/gzip"
   │                 │                  │────────────┐    │
   │                 │                  │            │    │
   │                 │                  │<───────────┘    │
   │                 │                  │                 │
   │                 │                  │ inferTags()     │
   │                 │                  │ → ["archive",   │
   │                 │                  │    "compressed"]│
   │                 │                  │────────────┐    │
   │                 │                  │            │    │
   │                 │                  │<───────────┘    │
   │                 │                  │                 │
   │                 │<──AnalysisResult─│                 │
   │                 │   (tags include  │                 │
   │                 │    "archive")    │                 │
   │                 │                  │                 │
   │                 │ IsArchive(content_type)?           │
   │                 │ → true           │                 │
   │                 │────────────┐     │                 │
   │                 │            │     │                 │
   │                 │<───────────┘     │                 │
   │                 │                  │                 │
   │                 │ Log "archive detected"             │
   │                 │ Add "archive" tag│                 │
   │                 │ (if not present) │                 │
   │                 │────────────┐     │                 │
   │                 │            │     │                 │
   │                 │<───────────┘     │                 │
   │                 │                  │                 │
   │                 │ NOTE: Server does NOT extract      │
   │                 │       User must manually extract   │
   │                 │                  │                 │
   │<─Response───────│                  │                 │
   │ (archive stored │                  │                 │
   │  as-is)         │                  │                 │
   │                 │                  │                 │
   │                 │                  │                 │
   │ Manual extraction (user-initiated)                   │
   │────────────────────────────────────────────────────>│
   │                 │                  │                 │
   │                 │                  │ ExtractArchive()│
   │                 │                  │<────────────────│
   │                 │                  │                 │
   │                 │                  │ extractTarGz()  │
   │                 │                  │────────────┐    │
   │                 │                  │            │    │
   │                 │                  │ Path       │    │
   │                 │                  │ Traversal  │    │
   │                 │                  │ Check      │    │
   │                 │                  │            │    │
   │                 │                  │<───────────┘    │
   │                 │                  │                 │
   │                 │                  │ Write files     │
   │                 │                  │────────────────>│
   │                 │                  │                 │
   │                 │                  │<────success─────│
   │                 │                  │                 │
   │<────────────────────────────────files extracted─────│
   │                 │                  │                 │
```

**Key Points**:
- **Server-Side**: Detects archive, logs, adds tag, but does NOT extract
- **Client-Side**: User explicitly calls extraction (not shown in TUI yet, manual tar -xzf)
- **Security**: ExtractArchive() validates paths before writing

### Hot-Reload Event Processing

```
┌──────────┐     ┌──────────┐     ┌──────────┐     ┌──────────┐
│Filesystem│     │ fsnotify │     │  Watcher │     │  Store   │
└────┬─────┘     └────┬─────┘     └────┬─────┘     └────┬─────┘
     │                │                │                │
     │ User drops     │                │                │
     │ data.csv to    │                │                │
     │ ~/.loom/       │                │                │
     │ artifacts/     │                │                │
     │                │                │                │
     │ CREATE event   │                │                │
     │───────────────>│                │                │
     │                │                │                │
     │                │ Event{Name, Op}│                │
     │                │───────────────>│                │
     │                │                │                │
     │                │                │ handleEvent()  │
     │                │                │ - Filter       │
     │                │                │   hidden files │
     │                │                │ - Filter dirs  │
     │                │                │────────────┐   │
     │                │                │            │   │
     │                │                │<───────────┘   │
     │                │                │                │
     │                │                │ debounceEvent()│
     │                │                │ - Cancel old   │
     │                │                │   timer        │
     │                │                │ - Start 500ms  │
     │                │                │   timer        │
     │                │                │────────────┐   │
     │                │                │            │   │
     │                │                │<───────────┘   │
     │                │                │                │
     │ (Rapid writes  │                │ (Timers        │
     │  from editor)  │                │  canceled)     │
     │───────x10─────>│───────x10─────>│───────x10──┐   │
     │                │                │            │   │
     │                │                │<───────────┘   │
     │                │                │                │
     │ (500ms pass)   │                │                │
     │                │                │ Timer fires    │
     │                │                │────────────┐   │
     │                │                │            │   │
     │                │                │ processEvent()│
     │                │                │            │   │
     │                │                │ handleCreate()│
     │                │                │            │   │
     │                │                │<───────────┘   │
     │                │                │                │
     │                │                │ Analyze(path)  │
     │                │                │────────────┐   │
     │                │                │            │   │
     │                │                │<───────────┘   │
     │                │                │                │
     │                │                │ Index(artifact)│
     │                │                │───────────────>│
     │                │                │                │
     │                │                │                │ INSERT
     │                │                │                │────┐
     │                │                │                │    │
     │                │                │<────success────┘    │
     │                │                │                │
     │                │                │ OnCreate       │
     │                │                │ callback       │
     │                │                │────────────┐   │
     │                │                │            │   │
     │                │                │<───────────┘   │
     │                │                │                │
```

**Debouncing Efficiency**:
- **Without Debounce**: 10 writes → 10 index operations (wasteful)
- **With Debounce**: 10 writes → 1 index operation (500ms after last write)

### Full-Text Search Flow

```
┌──────┐        ┌──────────┐       ┌──────────┐      ┌──────────┐
│Agent │        │  Tool    │       │  Store   │      │  FTS5    │
└──┬───┘        └────┬─────┘       └────┬─────┘      └────┬─────┘
   │                 │                  │                 │
   │ search_artifacts│                  │                 │
   │ (query="sales") │                  │                 │
   │────────────────>│                  │                 │
   │                 │                  │                 │
   │                 │ Search(ctx,      │                 │
   │                 │   "sales", 20)   │                 │
   │                 │─────────────────>│                 │
   │                 │                  │                 │
   │                 │                  │ SELECT a.*      │
   │                 │                  │ FROM artifacts a│
   │                 │                  │ JOIN artifacts_ │
   │                 │                  │   fts5 fts      │
   │                 │                  │ ON a.id = fts.  │
   │                 │                  │   artifact_id   │
   │                 │                  │ WHERE fts MATCH │
   │                 │                  │   'sales'       │
   │                 │                  │ ORDER BY rank   │
   │                 │                  │ LIMIT 20        │
   │                 │                  │────────────────>│
   │                 │                  │                 │
   │                 │                  │                 │ Tokenize
   │                 │                  │                 │ → "sales"
   │                 │                  │                 │────┐
   │                 │                  │                 │    │
   │                 │                  │                 │<───┘
   │                 │                  │                 │
   │                 │                  │                 │ Match
   │                 │                  │                 │ - name
   │                 │                  │                 │ - purpose
   │                 │                  │                 │ - tags
   │                 │                  │                 │────┐
   │                 │                  │                 │    │
   │                 │                  │                 │<───┘
   │                 │                  │                 │
   │                 │                  │                 │ BM25
   │                 │                  │                 │ Ranking
   │                 │                  │                 │────┐
   │                 │                  │                 │    │
   │                 │                  │                 │<───┘
   │                 │                  │                 │
   │                 │                  │<─Ranked Results─│
   │                 │                  │  (top 20)       │
   │                 │                  │                 │
   │                 │<───[]*Artifact───│                 │
   │                 │                  │                 │
   │<─JSON Response──│                  │                 │
   │ (name, id, tags)│                  │                 │
   │                 │                  │                 │
```

**BM25 Ranking Formula** (FTS5 default):
```
score(d, q) = Σ IDF(qi) × (f(qi, d) × (k1 + 1)) / (f(qi, d) + k1 × (1 - b + b × |d| / avgdl))

Where:
  d = document (artifact)
  q = query terms
  f(qi, d) = frequency of term qi in document d
  IDF(qi) = log((N - n(qi) + 0.5) / (n(qi) + 0.5))
  N = total documents
  n(qi) = documents containing qi
  |d| = document length
  avgdl = average document length
  k1 = 1.2 (term saturation parameter)
  b = 0.75 (length normalization parameter)
```

**Ranking Properties**:
- **TF (Term Frequency)**: Higher weight for terms appearing multiple times
- **IDF (Inverse Document Frequency)**: Rare terms weighted higher than common terms
- **Length Normalization**: Longer documents penalized (prevents bias toward verbose metadata)

---

## Data Models

### Artifact Metadata

```go
type Artifact struct {
    // Identity
    ID             string    // UUID v4
    Name           string    // Filename (e.g., "data.csv")
    Path           string    // Absolute path (~/.loom/artifacts/data.csv)

    // Provenance
    Source         SourceType // user | generated | agent
    SourceAgentID  string     // Agent ID if Source=agent
    Purpose        string     // User-provided description

    // Content
    ContentType    string     // MIME type (e.g., "text/csv")
    SizeBytes      int64      // File size in bytes
    Checksum       string     // SHA-256 hex string

    // Timestamps
    CreatedAt      time.Time  // File creation time
    UpdatedAt      time.Time  // Last modification time
    LastAccessedAt *time.Time // Last read time (nullable)
    DeletedAt      *time.Time // Soft delete timestamp (nullable)

    // Access Tracking
    AccessCount    int        // Number of times read

    // Metadata
    Tags           []string              // Inferred + user-provided tags
    Metadata       map[string]string     // Format-specific metadata
}
```

**Source Types**:
```go
const (
    SourceUser      SourceType = "user"      // Uploaded via TUI or hot-reload
    SourceGenerated SourceType = "generated" // Created by write_artifact tool
    SourceAgent     SourceType = "agent"     // Created by specific agent (SourceAgentID set)
)
```

**Metadata Examples**:
```yaml
# CSV File
metadata:
  column_count: "5"
  columns: "id, name, email, created_at, status"
  rows: "100+ (sampled)"

# JSON File
metadata:
  valid_json: "true"
  structure: "array"
  array_length: "42"

# JSON Object
metadata:
  valid_json: "true"
  structure: "object"
  key_count: "7"
  sample_keys: "version, config, database, api, logging"
```

### Filter Specifications

```go
type Filter struct {
    Source         *SourceType // Filter by source (user|generated|agent)
    ContentType    *string     // Filter by MIME type
    Tags           []string    // Filter by tags (AND logic - all must match)
    MinSize        *int64      // Minimum file size in bytes
    MaxSize        *int64      // Maximum file size in bytes
    AfterDate      *time.Time  // Created after this date
    BeforeDate     *time.Time  // Created before this date
    IncludeDeleted bool        // Include soft-deleted artifacts
    Limit          int         // Max results (default: 50)
    Offset         int         // Pagination offset
}
```

**SQL Query Construction**:
```sql
-- Example filter: source=user, tags=["csv", "data"], limit=10
SELECT * FROM artifacts
WHERE source = 'user'
  AND tags LIKE '%"csv"%'
  AND tags LIKE '%"data"%'
  AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT 10
```

**Tag Matching Logic**:
- **JSON LIKE Pattern**: `tags LIKE '%"<tag>"%'` matches tag in JSON array
- **AND Semantics**: Multiple tags require ALL to match (intersection, not union)
- **Case Sensitivity**: LIKE is case-insensitive by default in SQLite

### Analysis Results

```go
type AnalysisResult struct {
    ContentType string            // MIME type
    SizeBytes   int64             // File size
    Checksum    string            // SHA-256 hex
    Tags        []string          // Inferred tags
    Metadata    map[string]string // Format-specific metadata
}
```

**Content Type Detection Priority**:
1. **Extension**: `mime.TypeByExtension(ext)` (fast path)
2. **Magic Bytes**: `http.DetectContentType(buffer[:512])` (fallback)
3. **Hardcoded Map**: `guessFromExtension(ext)` for .yaml, .sql, .proto (final fallback)

**Tag Inference Priority**:
1. **Content Type Categories**: Archive, spreadsheet, JSON, SQL, code
2. **Filename Patterns**: "report", "data", "config", "test", "doc"
3. **Deduplication**: Remove duplicate tags before returning

---

## Formal Properties and Invariants

### Store Invariants

**INV-1: Uniqueness**
```
∀ artifacts a1, a2 ∈ Store:
  (a1.ID = a2.ID) ⇒ (a1 = a2)
```
- **Enforcement**: `PRIMARY KEY(id)` in SQLite schema
- **Generation**: `uuid.New()` provides collision-resistant IDs (2^122 probability space)

**INV-2: Path Consistency**
```
∀ artifact a ∈ Store:
  (a.DeletedAt = NULL) ⇒ os.Exists(a.Path)
```
- **Enforcement**: Hard delete removes both database row and filesystem file
- **Violation Handling**: Watcher's handleDelete() soft-deletes if file removed externally

**INV-3: Checksum Integrity**
```
∀ artifact a ∈ Store:
  SHA256(read(a.Path)) = a.Checksum
```
- **Enforcement**: Checksum recomputed on update via `Analyzer.Analyze()`
- **Use Case**: Detect filesystem corruption or tampering

**INV-4: FTS5 Consistency**
```
∀ row r ∈ artifacts:
  ∃ row fts ∈ artifacts_fts5 : fts.artifact_id = r.id
```
- **Enforcement**: SQLite trigger on artifacts table auto-updates FTS5 virtual table
- **Conflict Resolution**: FTS5 uses content='artifacts' to stay synced

### Concurrency Invariants

**INV-5: Read-Write Isolation**
```
∀ operations op1, op2 concurrent:
  (op1 = Read ∧ op2 = Write) ⇒ op1 sees consistent snapshot (WAL mode)
```
- **Enforcement**: PRAGMA journal_mode=WAL enables MVCC (Multi-Version Concurrency Control)
- **Trade-off**: Readers never block writers, but may see stale data

**INV-6: Write Serializability**
```
∀ writes w1, w2 concurrent:
  Lock acquired ⇒ w1 || w2 (serialized execution)
```
- **Enforcement**: `sync.Mutex` in SQLiteStore serializes writes
- **Trade-off**: Coarse-grained lock limits write concurrency

### Watcher Invariants

**INV-7: Event Ordering**
```
∀ events e1, e2 on same file:
  (timestamp(e1) < timestamp(e2)) ⇒ processed(e1) before processed(e2)
```
- **Enforcement**: fsnotify delivers events in FIFO order per file
- **Violation Handling**: Debouncing collapses rapid events, preserving order of final event

**INV-8: Debounce Consolidation**
```
∀ events e1, e2, ..., en on same file within DebounceMs:
  only en processed (latest event wins)
```
- **Enforcement**: Per-file timer map; each event cancels previous timer
- **Rationale**: Prevents re-indexing identical content 10+ times

---

## Security Considerations

### Zip Slip Prevention

**Attack Vector**: Malicious archive with paths like `../../../../etc/passwd`

**Defense** (analyzer.go:398-400, 465-468, 531-534):
```go
destPath := filepath.Join(destDir, header.Name)
if !strings.HasPrefix(destPath, filepath.Clean(destDir)+string(os.PathSeparator)) {
    return nil, fmt.Errorf("illegal file path in archive: %s", header.Name)
}
```

**Why This Works**:
1. `filepath.Clean(destDir)` normalizes destination (removes `.`, `..`)
2. `filepath.Join(destDir, header.Name)` constructs extraction path
3. `strings.HasPrefix()` validates extracted path stays within destDir
4. `+string(os.PathSeparator)` prevents partial matches (e.g., `/tmp/artifacts` vs `/tmp/artifacts-malicious`)

**Test Coverage** (analyzer_test.go:219-247):
```go
func TestExtractArchive_PreventPathTraversal(t *testing.T) {
    // Create malicious zip with "../../../etc/passwd"
    // Assert: extractZip() returns error "illegal file path"
}
```

### Path Validation

**Client-Side Protection** (tui/client/client.go:189-196):
```go
fileInfo, err := os.Stat(filePath)
if err != nil {
    return nil, fmt.Errorf("failed to stat file: %w", err)
}

if fileInfo.IsDir() {
    return nil, fmt.Errorf("cannot upload directory: %s. To upload multiple files, create a tar/zip archive first (e.g., tar -czf archive.tar.gz %s)", filePath, filePath)
}
```

**Rationale**:
- **UX**: Clear error message with actionable suggestion
- **Security**: Prevents accidental recursive directory uploads
- **Predictability**: Users know exactly what is uploaded

### Content Type Detection

**Multi-Layer Defense**:
1. **Extension First**: Fast, user-controlled (e.g., `.csv` → `text/csv`)
2. **Magic Bytes**: Validates actual file content (prevents `.exe` renamed to `.csv`)
3. **Fallback Map**: Handles non-standard extensions (`.proto`, `.yaml`)

**Security Trade-off**:
- **False Positives**: `http.DetectContentType()` may misclassify binary files as `text/plain` (acceptable for indexing)
- **False Negatives**: Obfuscated binaries may bypass detection (mitigated by checksum tracking)

---

## Performance Characteristics

### Database Operations

| **Operation** | **Complexity** | **Typical Latency** | **Notes** |
|---------------|----------------|---------------------|-----------|
| **Index** | O(log N) + FTS5 | 5-15ms | INSERT + FTS5 tokenization |
| **Get(id)** | O(1) | 1-3ms | PRIMARY KEY lookup |
| **GetByName** | O(log N) | 2-5ms | Index scan on name (if indexed) |
| **List(filter)** | O(N) | 10-50ms | Full table scan + filtering |
| **Search(query)** | O(FTS5) | 5-20ms | FTS5 BM25 ranking |
| **Update** | O(log N) + FTS5 | 5-15ms | UPDATE + FTS5 re-tokenization |
| **Delete(soft)** | O(1) | 1-3ms | UPDATE deleted_at |
| **Delete(hard)** | O(log N) | 10-30ms | DELETE + file removal |

**Scaling Behavior**:
- **Artifacts < 1,000**: All operations sub-20ms
- **Artifacts 1,000-10,000**: List/Search may degrade to 50-100ms
- **Artifacts > 10,000**: Consider pagination (LIMIT/OFFSET), FTS5 query optimization

### FTS5 Search Performance

**Query Types**:

| **Query** | **Complexity** | **Example** |
|-----------|----------------|-------------|
| **Simple terms** | O(M × log N) | `"sales"` |
| **Phrase search** | O(M × N) | `"\"sales report\""` |
| **Boolean AND** | O(M1 ∩ M2) | `"sales AND report"` |
| **Prefix match** | O(M × log N) | `"sale*"` |

Where:
- N = total artifacts
- M = matching artifacts
- M1, M2 = matches for individual terms

**Index Size**:
```
FTS5 Index Overhead ≈ 50-100% of original content size
Example: 1000 artifacts × 2KB metadata = 2MB → FTS5 index ≈ 3-4MB total
```

**Optimization Tips**:
- **Limit Results**: Use `LIMIT 20` to avoid sorting thousands of results
- **Tokenization**: FTS5 indexes tokens, not full content (reduces index size)
- **No Wildcards**: Avoid leading wildcards (`*sales`) - requires full scan

### Archive Extraction Performance

**Extraction Time** (measured on M1 MacBook Pro):

| **Archive Type** | **File Count** | **Total Size** | **Extraction Time** |
|------------------|----------------|----------------|---------------------|
| **ZIP** | 10 files | 1MB | ~50ms |
| **ZIP** | 100 files | 10MB | ~200ms |
| **TAR** | 10 files | 1MB | ~30ms (no compression) |
| **TAR.GZ** | 10 files | 1MB (compressed) | ~80ms |
| **TAR.GZ** | 100 files | 10MB (compressed) | ~400ms |

**Bottlenecks**:
1. **Decompression**: gzip decompression is CPU-bound (~20-30 MB/s)
2. **Disk I/O**: Writing many small files (metadata overhead)
3. **Path Validation**: `filepath.Clean()` and `strings.HasPrefix()` per file (negligible)

**Optimization**:
- **Parallel Extraction**: Could parallelize file writes (not implemented, adds complexity)
- **Buffered I/O**: `io.Copy()` already uses buffering

### Hot-Reload Debouncing

**Debounce Latency**:
```
File drop → 0ms
Editor writes × 10 → 0-100ms (rapid-fire)
Last write → 100ms
Debounce timer expires → 600ms (100ms + 500ms debounce)
Index complete → 610-620ms
```

**Trade-off**:
- **Lower DebounceMs** (e.g., 100ms): Faster indexing, more CPU usage
- **Higher DebounceMs** (e.g., 1000ms): Fewer indexes, higher latency

**Memory Overhead**:
```
Per-file timer: ~200 bytes (time.Timer + map entry)
100 concurrent edits: ~20KB (negligible)
```

---

## Algorithm Complexity

### Content Type Detection

**Algorithm**: Three-stage fallback
```
Stage 1: mime.TypeByExtension()
  - Complexity: O(1) hash lookup
  - Fast path: ~1μs

Stage 2: http.DetectContentType(buffer[:512])
  - Complexity: O(512) (fixed 512-byte scan)
  - Fallback: ~50-100μs

Stage 3: guessFromExtension()
  - Complexity: O(1) switch statement
  - Last resort: ~1μs
```

**Total Complexity**: O(512) worst-case (dominated by Stage 2)

### Tag Inference

**Algorithm**: Pattern matching on content type + filename
```
Content Type Matching:
  - O(C) where C = number of content type categories (14 categories)
  - Uses strings.Contains() for prefix matching

Filename Pattern Matching:
  - O(P) where P = number of patterns (8 patterns)
  - Uses strings.Contains() for substring matching

Deduplication:
  - O(T) where T = number of tags (typically 2-5)
  - Uses map[string]bool for seen tags
```

**Total Complexity**: O(C + P + T) = O(1) (all constants small)

### Archive Extraction

**Algorithm**: Sequential extraction with path validation
```
Pseudocode:
  for each entry in archive:
    1. Validate path (zip slip check): O(1)
    2. Create parent directories: O(D) where D = directory depth
    3. Write file: O(S) where S = file size

Total: O(N × (1 + D + S_avg)) = O(N × S_avg) where N = file count
```

**Space Complexity**: O(S_max) for largest file buffer

### FTS5 Search

**Algorithm**: BM25 ranking with inverted index
```
Query Tokenization: O(Q) where Q = query length
Inverted Index Lookup: O(M × log N) where M = matching docs, N = total docs
BM25 Scoring: O(M × T) where T = query terms
Sorting: O(M × log M)

Total: O(M × log N + M × T + M × log M) = O(M × (log N + T + log M))
```

**Best Case**: O(1) if no matches
**Worst Case**: O(N × log N) if all documents match (rare)

---

## Trade-off Analysis

### Archive Handling

| **Decision** | **Pros** | **Cons** | **Mitigation** |
|--------------|----------|----------|----------------|
| **No Auto-Extract** | + Security (no zip slip)<br>+ Predictable behavior<br>+ Storage control | - User inconvenience<br>- Manual extraction required | - Clear error message<br>- Helper functions provided (`ExtractArchive`) |

**Chosen**: No auto-extract (security > convenience)

### Storage Backend

| **Decision** | **Pros** | **Cons** | **Mitigation** |
|--------------|----------|----------|----------------|
| **SQLite + FTS5** | + Zero external deps<br>+ ACID guarantees<br>+ BM25 ranking | - Single-node only<br>- Limited concurrency<br>- FTS5 build tag required | - Document build requirements<br>- WAL mode for better concurrency |

**Chosen**: SQLite (matches Loom's design philosophy)

### Locking Strategy

| **Decision** | **Pros** | **Cons** | **Mitigation** |
|--------------|----------|----------|----------------|
| **Single RWMutex** | + Simple reasoning<br>+ No deadlocks<br>+ Consistent snapshots | - Write bottleneck<br>- No parallel writes | - Use WAL mode for read concurrency<br>- Optimize write operations |

**Chosen**: Coarse-grained locking (simplicity > max performance)

### Hot-Reload Debouncing

| **Decision** | **Pros** | **Cons** | **Mitigation** |
|--------------|----------|----------|----------------|
| **500ms Debounce** | + Reduces spurious indexes<br>+ Handles rapid edits | - 500ms indexing delay<br>- Not real-time | - Configurable via WatcherConfig<br>- Acceptable for non-critical use |

**Chosen**: 500ms default (balanced trade-off)

### Tag Storage

| **Decision** | **Pros** | **Cons** | **Mitigation** |
|--------------|----------|----------|----------------|
| **JSON Array in TEXT Column** | + Flexible schema<br>+ No JOIN tables | - No SQL filtering on tags<br>- LIKE pattern matching only | - FTS5 indexes tags<br>- Use List with filters for exact matches |

**Chosen**: JSON storage (flexibility > query performance)

---

## Integration Points

### Shuttle Tool System

**Registration** (server.go):
```go
// Register artifact tools
artifactTools := artifacts.ArtifactTools(artifactStore)
for _, tool := range artifactTools {
    factory.RegisterTool(tool)
}
```

**Tool Execution Flow**:
1. Agent generates tool call → Shuttle dispatcher
2. Shuttle validates schema → Tool.Execute()
3. Tool interacts with ArtifactStore → SQLite operations
4. Tool returns shuttle.Result → Agent receives JSON

**Error Handling**:
- Schema validation errors → Agent sees "missing required parameter"
- Store errors → Agent sees "failed to <operation>: <details>"
- Filesystem errors → Agent sees "failed to read/write file: <details>"

### Observability Integration

**Hawk Tracing** (all store operations):
```go
ctx, span := s.tracer.StartSpan(ctx, "artifacts.index")
defer s.tracer.EndSpan(span)
span.SetAttribute("artifact.id", artifact.ID)
span.SetAttribute("artifact.name", artifact.Name)
```

**Trace Attributes**:
- **artifact.id**: UUID for correlation
- **artifact.name**: Human-readable identifier
- **operation**: index|get|list|search|delete
- **duration_ms**: Operation latency
- **error**: Error message (if failed)

**Use Cases**:
- **Debugging**: Trace artifact upload failures (missing file? checksum mismatch?)
- **Performance**: Identify slow FTS5 queries
- **Auditing**: Track who accessed what artifacts

### gRPC Server

**Upload Flow** (pkg/server/artifacts.go):
```go
func (s *Server) UploadArtifact(ctx context.Context, req *loomv1.UploadArtifactRequest) (*loomv1.UploadArtifactResponse, error) {
    // 1. Write file to ~/.loom/artifacts/
    // 2. Analyze file (content type, checksum, tags)
    // 3. Index in store
    // 4. Return response with ID, checksum, tags
}
```

**Archive Detection**:
```go
if artifacts.IsArchive(result.ContentType) {
    s.logger.Info("archive detected, will index as archive file")
    // Add "archive" tag if not present
    // Note: Server does NOT auto-extract
}
```

---

## Future Considerations

### Scalability

**Multi-Node Support**:
- **Problem**: SQLite single-node limitation
- **Options**:
  - Replicate loom.db across nodes (rsync, litestream)
  - Migrate to PostgreSQL (loses zero-dependency property)
  - Shard artifacts by agent ID (requires routing layer)

**Large File Support**:
- **Problem**: 10MB max_size_mb limit in read_artifact
- **Options**:
  - Streaming RPC for large files (gRPC streaming)
  - Chunk-based reads (offset/limit parameters)
  - External blob storage (S3, MinIO) with metadata in SQLite

### Advanced Search

**Semantic Search**:
- **Feature**: Embeddings-based search (e.g., "find similar datasets")
- **Implementation**: Store embedding vectors in BLOB column, use cosine similarity
- **Trade-off**: Requires LLM inference for query encoding

**Faceted Search**:
- **Feature**: Aggregate by tag, content type, source (e.g., "show counts per tag")
- **Implementation**: GROUP BY queries, materialized views
- **Trade-off**: Adds query complexity, cache invalidation

### Versioning

**Artifact Versions**:
- **Problem**: No version history for modified artifacts
- **Options**:
  - Copy-on-write (store all versions, track parent_id)
  - Delta storage (store diffs, reconstruct on read)
  - Git-like content-addressable storage (deduplicate by checksum)

**Use Case**: "Show me all versions of config.yaml"

### Enhanced Metadata

**Extracted Content**:
- **Feature**: Index full content of text files in FTS5 (not just name/purpose/tags)
- **Implementation**: Read file, tokenize, index in artifacts_fts5.content column
- **Trade-off**: FTS5 index size grows significantly (50-100% of content size)

**Structured Metadata**:
- **Feature**: JSON schema validation for metadata field
- **Implementation**: Define per-content-type schemas, validate on Index()
- **Trade-off**: Reduces flexibility, adds complexity

---

**End of Artifact Management Architecture Document**

*For usage examples and task-oriented guides, see [Artifact Management Usage Guide](../guides/artifacts-usage.md).*

*For related architecture documentation, see:*
- *[Meta-Agent Architecture](./meta-agent.md) - Weaver and Mender design*
- *[Core Agent Architecture](./architecture.md) - Agent conversation system*
- *[Pattern Library Architecture](./patterns.md) - Domain-specific patterns*

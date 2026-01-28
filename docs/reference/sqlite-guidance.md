
# SQLite Best Practices Reference

**Version**: v1.0.0-beta.1

Complete best practices for SQLite usage in Loom - session storage, HITL persistence, reference storage, and performance optimization.


## Table of Contents

- [Quick Reference](#quick-reference)
- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Use Cases](#use-cases)
- [Session Storage](#session-storage)
- [HITL Request Storage](#hitl-request-storage)
- [Reference Storage](#reference-storage)
- [PRAGMA Configuration](#pragma-configuration)
- [Schema Design](#schema-design)
- [Performance Optimization](#performance-optimization)
- [Concurrency Management](#concurrency-management)
- [Backup Strategies](#backup-strategies)
- [Maintenance](#maintenance)
- [Monitoring](#monitoring)
- [Troubleshooting](#troubleshooting)
- [Best Practices](#best-practices)
- [Error Codes](#error-codes)
- [See Also](#see-also)


## Quick Reference

### Common PRAGMAs

| PRAGMA | Default | Recommended | Purpose |
|--------|---------|-------------|---------|
| `journal_mode` | `DELETE` | `WAL` | Write-ahead logging for concurrency |
| `foreign_keys` | `OFF` | `ON` | Enforce referential integrity |
| `synchronous` | `FULL` | `NORMAL` | Balance safety and performance |
| `cache_size` | `2000` | `10000` | In-memory page cache (pages) |
| `busy_timeout` | `0` | `5000` | Wait time for locks (ms) |
| `temp_store` | `DEFAULT` | `MEMORY` | Store temporary tables in RAM |
| `mmap_size` | `0` | `268435456` | Memory-mapped I/O (256MB) |

### Loom Storage Implementations

| Storage Type | Package | Database File | Purpose |
|--------------|---------|---------------|---------|
| Session Store | `pkg/agent` | `sessions.db` | Conversation history, messages, tool executions |
| HITL Store | `pkg/shuttle` | `hitl.db` | Human-in-the-loop approval requests |
| Reference Store | `pkg/communication` | `references.db` | Large content reference counting |

### File Paths

```bash
# Default storage locations
$LOOM_DATA_DIR/
├── sessions.db         # Agent sessions (SessionStore)
├── sessions.db-wal     # WAL file (auto-created)
├── sessions.db-shm     # Shared memory (auto-created)
├── hitl.db             # HITL requests (SQLiteHumanRequestStore)
└── references.db       # Reference storage (SQLiteStore)
```


## Overview

Loom uses **SQLite** for persistent storage of agent sessions, human-in-the-loop requests, and large content references. SQLite provides:

- **Zero-configuration**: Embedded database, no server process
- **ACID compliance**: Transactional integrity
- **Cross-platform**: Works on Linux, macOS, Windows
- **High performance**: ~10x faster than file I/O for structured data
- **Concurrency**: WAL mode enables concurrent readers + single writer

**Implementation**:
- `pkg/agent/session_store.go` (566 lines) - Session persistence
- `pkg/shuttle/human_store_sqlite.go` (521 lines) - HITL persistence
- `pkg/communication/sqlite_store.go` (353 lines) - Reference storage

**Available Since**: v0.7.0


## Prerequisites

### System Requirements

- **SQLite Version**: 3.8.0+ (Loom requires 3.24+ for UPSERT support)
- **Go Driver**: `github.com/mattn/go-sqlite3` (CGO-based)
- **Disk Space**: 100MB minimum (sessions grow ~1MB per 1000 messages)
- **File System**: POSIX-compliant (ext4, APFS, NTFS)

### Verification

```bash
# Check SQLite version
sqlite3 --version

# Expected: 3.24.0 or later
```

**Note**: CGO is required to compile `go-sqlite3`. For environments without CGO, consider using `modernc.org/sqlite` (pure Go, no CGO).


## Use Cases

### 1. Session Persistence

**Purpose**: Store agent conversations with full message history, tool executions, and cost tracking.

**Use When**:
- Users need to resume conversations across restarts
- Cost tracking requires historical data
- Audit trails for compliance
- Multi-turn conversations exceed memory limits

**Implementation**: `pkg/agent/session_store.go`


### 2. HITL Request Storage

**Purpose**: Persist human-in-the-loop approval requests with priority, timeout, and response tracking.

**Use When**:
- Sensitive operations require human approval
- Audit trail for approved/rejected actions
- Querying pending requests across sessions
- Compliance requires approval history

**Implementation**: `pkg/shuttle/human_store_sqlite.go`


### 3. Reference Storage

**Purpose**: Store large content (documents, images, query results) with reference counting and garbage collection.

**Use When**:
- LLM context exceeds token limits (>200k tokens)
- Documents shared across multiple messages
- Automatic cleanup of unused references
- Cost optimization (avoid re-sending large content)

**Implementation**: `pkg/communication/sqlite_store.go`


## Session Storage

### Schema

**File**: `sessions.db`

```sql
-- Sessions table
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,                 -- Session identifier (sess_abc123)
    context_json TEXT,                   -- Session context (user, goals)
    created_at INTEGER NOT NULL,         -- Unix timestamp
    updated_at INTEGER NOT NULL,         -- Unix timestamp
    total_cost_usd REAL DEFAULT 0,       -- Cumulative cost
    total_tokens INTEGER DEFAULT 0       -- Cumulative tokens
);

-- Messages table
CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,            -- Foreign key to sessions
    role TEXT NOT NULL,                  -- user | assistant | tool
    content TEXT,                        -- Message content
    tool_calls_json TEXT,                -- Tool calls (if assistant role)
    tool_use_id TEXT,                    -- Tool use ID (if tool role)
    tool_result_json TEXT,               -- Tool result (if tool role)
    timestamp INTEGER NOT NULL,          -- Unix timestamp
    token_count INTEGER DEFAULT 0,       -- Tokens in this message
    cost_usd REAL DEFAULT 0,             -- Cost of this message
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

-- Tool executions table
CREATE TABLE tool_executions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    input_json TEXT,                     -- Tool input parameters
    result_json TEXT,                    -- Tool result
    error TEXT,                          -- Error message (if failed)
    execution_time_ms INTEGER,           -- Execution duration
    timestamp INTEGER NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

-- Indexes
CREATE INDEX idx_messages_session ON messages(session_id);
CREATE INDEX idx_tool_executions_session ON tool_executions(session_id);
CREATE INDEX idx_sessions_updated ON sessions(updated_at);
```

### Usage

```go
import (
    "github.com/teradata-labs/loom/pkg/agent"
    "github.com/teradata-labs/loom/pkg/observability"
)

// Create session store
tracer := observability.NewNoOpTracer()
store, err := agent.NewSessionStore("sessions.db", tracer)
if err != nil {
    log.Fatalf("Failed to create session store: %v", err)
}
defer store.Close()

// Save session
session := &agent.Session{
    ID:        "sess_abc123",
    Context:   map[string]interface{}{"user": "john"},
    CreatedAt: time.Now(),
    UpdatedAt: time.Now(),
}
err = store.SaveSession(ctx, session)

// Load session
session, err := store.LoadSession(ctx, "sess_abc123")

// Save message
msg := agent.Message{
    Role:      "user",
    Content:   "Show sales by region",
    Timestamp: time.Now(),
}
err = store.SaveMessage(ctx, "sess_abc123", msg)

// Load messages
messages, err := store.LoadMessages(ctx, "sess_abc123")

// Get statistics
stats, err := store.GetStats(ctx)
fmt.Printf("Sessions: %d, Messages: %d, Cost: $%.2f\n",
    stats.SessionCount, stats.MessageCount, stats.TotalCostUSD)
```

**Expected Output**:
```
Sessions: 42, Messages: 1,234, Cost: $12.45
```


### Performance Characteristics

| Operation | Latency (P50) | Latency (P99) | Notes |
|-----------|---------------|---------------|-------|
| `SaveSession` | 5ms | 15ms | Upsert with WAL |
| `LoadSession` | 8ms | 20ms | Includes message load |
| `SaveMessage` | 3ms | 10ms | Insert only |
| `LoadMessages` | 10ms | 30ms | 100 messages |
| `DeleteSession` | 15ms | 40ms | CASCADE delete |

**Scaling**: Tested with 10,000 sessions, 1M messages, <100ms load time per session.


## HITL Request Storage

### Schema

**File**: `hitl.db`

```sql
CREATE TABLE human_requests (
    id TEXT PRIMARY KEY,                 -- Request identifier
    agent_id TEXT NOT NULL,              -- Requesting agent
    session_id TEXT NOT NULL,            -- Session context
    question TEXT NOT NULL,              -- Approval question
    context_json TEXT,                   -- Additional context
    request_type TEXT NOT NULL,          -- approval | input | selection
    priority TEXT NOT NULL,              -- low | medium | high | critical
    timeout_ms INTEGER NOT NULL,         -- Timeout duration
    created_at INTEGER NOT NULL,         -- Unix timestamp (ms)
    expires_at INTEGER NOT NULL,         -- Unix timestamp (ms)
    status TEXT NOT NULL,                -- pending | approved | rejected | expired
    response TEXT,                       -- User response
    response_data_json TEXT,             -- Structured response data
    responded_at INTEGER,                -- Response timestamp (ms)
    responded_by TEXT                    -- User identifier
);

-- Indexes
CREATE INDEX idx_human_requests_status ON human_requests(status);
CREATE INDEX idx_human_requests_session ON human_requests(session_id);
CREATE INDEX idx_human_requests_agent ON human_requests(agent_id);
CREATE INDEX idx_human_requests_priority ON human_requests(priority);
CREATE INDEX idx_human_requests_created ON human_requests(created_at);
CREATE INDEX idx_human_requests_expires ON human_requests(expires_at);
```

### Usage

```go
import "github.com/teradata-labs/loom/pkg/shuttle"

// Create HITL store
config := shuttle.SQLiteConfig{
    Path:   "hitl.db",
    Tracer: tracer,
}
store, err := shuttle.NewSQLiteHumanRequestStore(config)
if err != nil {
    log.Fatalf("Failed to create HITL store: %v", err)
}
defer store.Close()

// Store request
req := &shuttle.HumanRequest{
    ID:          "req_xyz789",
    AgentID:     "sql-agent",
    SessionID:   "sess_abc123",
    Question:    "Approve DELETE query?",
    RequestType: "approval",
    Priority:    "high",
    Timeout:     5 * time.Minute,
    Status:      "pending",
    CreatedAt:   time.Now(),
    ExpiresAt:   time.Now().Add(5 * time.Minute),
}
err = store.Store(ctx, req)

// List pending requests
requests, err := store.ListPending(ctx)

// Respond to request
err = store.RespondToRequest(ctx, "req_xyz789", "approved", "yes", "john", nil)

// List by session
requests, err := store.ListBySession(ctx, "sess_abc123")
```


## Reference Storage

### Schema

**File**: `references.db`

```sql
CREATE TABLE reference_store (
    id TEXT PRIMARY KEY,                 -- SHA-256 hash of content
    type INTEGER NOT NULL,               -- content | image | tool_result
    store INTEGER NOT NULL,              -- REFERENCE_STORE_SQLITE
    data BLOB NOT NULL,                  -- Stored content
    ref_count INTEGER NOT NULL DEFAULT 1, -- Reference count
    created_at INTEGER NOT NULL,         -- Unix timestamp
    expires_at INTEGER NOT NULL DEFAULT 0, -- Expiration (0 = never)
    size_bytes INTEGER NOT NULL          -- Content size
);

-- Indexes
CREATE INDEX idx_expires_at ON reference_store(expires_at);
CREATE INDEX idx_ref_count ON reference_store(ref_count);
```

### Usage

```go
import (
    "github.com/teradata-labs/loom/pkg/communication"
    loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Create reference store with garbage collection
store, err := communication.NewSQLiteStore("references.db", 5*time.Minute)
if err != nil {
    log.Fatalf("Failed to create reference store: %v", err)
}
defer store.Close()

// Store content
data := []byte("Large document content...")
opts := communication.StoreOptions{
    Type: loomv1.ReferenceType_REFERENCE_TYPE_CONTENT,
    TTL:  3600, // 1 hour expiration
}
ref, err := store.Store(ctx, data, opts)

// Resolve reference
data, err := store.Resolve(ctx, ref)

// Retain reference (increment ref_count)
err = store.Retain(ctx, ref.Id)

// Release reference (decrement ref_count, GC if 0)
err = store.Release(ctx, ref.Id)

// Get statistics
stats, err := store.Stats(ctx)
fmt.Printf("Active refs: %d, Size: %d bytes\n",
    stats.ActiveRefs, stats.CurrentBytes)
```

**Garbage Collection**: Runs every 5 minutes (configurable), removes:
- References with `ref_count <= 0`
- References with `expires_at < now()`


## PRAGMA Configuration

### Essential PRAGMAs

**Set at database open** (required for all Loom storage implementations):

```go
db, err := sql.Open("sqlite3", "sessions.db")
if err != nil {
    return err
}

// Enable WAL mode (critical for concurrency)
_, err = db.Exec("PRAGMA journal_mode=WAL")
if err != nil {
    return fmt.Errorf("failed to enable WAL: %w", err)
}

// Enable foreign keys (for referential integrity)
_, err = db.Exec("PRAGMA foreign_keys=ON")
if err != nil {
    return fmt.Errorf("failed to enable foreign keys: %w", err)
}
```


### Performance PRAGMAs

**Recommended for production**:

```sql
-- Increase cache size (default: 2000 pages = ~8MB)
PRAGMA cache_size = 10000;  -- 40MB cache

-- Memory-mapped I/O (faster reads)
PRAGMA mmap_size = 268435456;  -- 256MB

-- Busy timeout (wait for locks)
PRAGMA busy_timeout = 5000;  -- 5 seconds

-- Synchronous mode (balance safety/speed)
PRAGMA synchronous = NORMAL;  -- Not FULL (too slow) or OFF (unsafe)

-- Temporary tables in memory
PRAGMA temp_store = MEMORY;

-- Automatic index creation
PRAGMA automatic_index = ON;
```

**Apply at startup**:

```go
// Performance PRAGMAs
pragmas := []string{
    "PRAGMA cache_size = 10000",
    "PRAGMA mmap_size = 268435456",
    "PRAGMA busy_timeout = 5000",
    "PRAGMA synchronous = NORMAL",
    "PRAGMA temp_store = MEMORY",
    "PRAGMA automatic_index = ON",
}

for _, pragma := range pragmas {
    if _, err := db.Exec(pragma); err != nil {
        log.Printf("Warning: %s failed: %v", pragma, err)
    }
}
```


### Query Current Settings

```go
func checkPragmas(db *sql.DB) error {
    pragmas := []string{
        "journal_mode",
        "foreign_keys",
        "synchronous",
        "cache_size",
        "busy_timeout",
        "mmap_size",
    }

    for _, pragma := range pragmas {
        var value string
        query := fmt.Sprintf("PRAGMA %s", pragma)
        err := db.QueryRow(query).Scan(&value)
        if err != nil {
            return err
        }
        fmt.Printf("%s: %s\n", pragma, value)
    }

    return nil
}
```

**Expected Output**:
```
journal_mode: wal
foreign_keys: 1
synchronous: 1
cache_size: 10000
busy_timeout: 5000
mmap_size: 268435456
```


## Schema Design

### Best Practices

#### 1. Use Appropriate Primary Keys

```sql
-- Good: Natural key (session ID from application)
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,  -- sess_abc123
    ...
);

-- Good: Auto-increment for temporal data
CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    ...
);

-- Avoid: UUIDs as PRIMARY KEY (poor locality, bloated indexes)
-- Use TEXT with application-generated IDs instead
```


#### 2. Create Indexes for Query Patterns

```sql
-- Query: Load all messages for a session
CREATE INDEX idx_messages_session ON messages(session_id);

-- Query: Find pending HITL requests
CREATE INDEX idx_human_requests_status ON human_requests(status);

-- Query: Find sessions by update time (for listing recent)
CREATE INDEX idx_sessions_updated ON sessions(updated_at);

-- Composite index for multi-column queries
CREATE INDEX idx_messages_session_timestamp
    ON messages(session_id, timestamp);
```

**Verification**:
```sql
EXPLAIN QUERY PLAN
SELECT * FROM messages WHERE session_id = 'sess_abc123';

-- Expected: SEARCH TABLE messages USING INDEX idx_messages_session
```


#### 3. Use Foreign Keys for Integrity

```sql
CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    ...,
    FOREIGN KEY (session_id)
        REFERENCES sessions(id)
        ON DELETE CASCADE
);
```

**Requires**: `PRAGMA foreign_keys=ON` at database open.

**Benefits**:
- Automatic CASCADE delete (messages deleted when session deleted)
- Prevents orphaned records
- Database-level integrity (not application-level)


#### 4. Store JSON for Flexible Data

```sql
-- Store nested structures as JSON
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    context_json TEXT,  -- {"user": "john", "goals": [...]}
    ...
);

-- Store tool calls as JSON array
CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tool_calls_json TEXT,  -- [{"name": "query", "input": {...}}]
    ...
);
```

**Access JSON fields** (SQLite 3.38+):
```sql
-- Extract JSON field
SELECT json_extract(context_json, '$.user') AS user
FROM sessions
WHERE id = 'sess_abc123';

-- Filter by JSON field
SELECT * FROM sessions
WHERE json_extract(context_json, '$.user') = 'john';

-- Note: JSON queries are slower than indexed columns
```


#### 5. Use INTEGER for Timestamps

```sql
-- Store Unix timestamps (seconds or milliseconds)
CREATE TABLE sessions (
    created_at INTEGER NOT NULL,  -- Unix seconds
    updated_at INTEGER NOT NULL,  -- Unix seconds
    ...
);

-- Go code
createdAt := time.Now().Unix()  // Seconds
createdAtMs := time.Now().UnixMilli()  // Milliseconds

// Load timestamp
t := time.Unix(createdAt, 0)
tMs := time.UnixMilli(createdAtMs)
```

**Why not TEXT?** Integers are smaller (8 bytes vs 20+ bytes), faster to compare, and index-friendly.


## Performance Optimization

### 1. Batch Inserts with Transactions

**Problem**: Individual inserts are slow (disk sync per insert).

**Solution**: Wrap multiple inserts in a transaction.

```go
// Bad: Individual inserts (100 inserts = 100 disk syncs)
for _, msg := range messages {
    store.SaveMessage(ctx, sessionID, msg)  // Slow!
}

// Good: Batch in transaction (100 inserts = 1 disk sync)
tx, err := db.BeginTx(ctx, nil)
if err != nil {
    return err
}
defer tx.Rollback()  // Rollback on error

for _, msg := range messages {
    _, err := tx.ExecContext(ctx,
        "INSERT INTO messages (session_id, role, content, timestamp) VALUES (?, ?, ?, ?)",
        sessionID, msg.Role, msg.Content, msg.Timestamp.Unix())
    if err != nil {
        return err
    }
}

err = tx.Commit()  // Single disk sync
if err != nil {
    return err
}
```

**Performance**: 100x faster for bulk inserts (5000 inserts/sec → 500,000 inserts/sec).


### 2. Use Prepared Statements

```go
// Bad: Repeated query parsing
for _, msg := range messages {
    db.ExecContext(ctx, "INSERT INTO messages (...) VALUES (?, ?, ?)", msg...)
}

// Good: Prepare once, execute many times
stmt, err := db.PrepareContext(ctx,
    "INSERT INTO messages (session_id, role, content, timestamp) VALUES (?, ?, ?, ?)")
if err != nil {
    return err
}
defer stmt.Close()

for _, msg := range messages {
    _, err := stmt.ExecContext(ctx, sessionID, msg.Role, msg.Content, msg.Timestamp.Unix())
    if err != nil {
        return err
    }
}
```

**Performance**: 30% faster for repeated inserts.


### 3. Optimize Queries with EXPLAIN

```sql
-- Check query plan
EXPLAIN QUERY PLAN
SELECT * FROM messages
WHERE session_id = 'sess_abc123'
ORDER BY timestamp DESC
LIMIT 100;

-- Expected plan (using index)
SEARCH TABLE messages USING INDEX idx_messages_session (session_id=?)
USE TEMP B-TREE FOR ORDER BY

-- Bad plan (full table scan)
SCAN TABLE messages
```

**Fix**: Create composite index for common query patterns.

```sql
-- Optimize: session + timestamp queries
CREATE INDEX idx_messages_session_timestamp
    ON messages(session_id, timestamp DESC);
```


### 4. Limit Result Sets

```sql
-- Bad: Load all messages (unbounded memory)
SELECT * FROM messages WHERE session_id = ?;

-- Good: Paginate with LIMIT and OFFSET
SELECT * FROM messages
WHERE session_id = ?
ORDER BY timestamp DESC
LIMIT 100 OFFSET 0;

-- Better: Use cursor-based pagination (faster than OFFSET)
SELECT * FROM messages
WHERE session_id = ?
AND timestamp < ?  -- Last seen timestamp
ORDER BY timestamp DESC
LIMIT 100;
```

**Implementation**:
```go
func LoadMessagesPaginated(ctx context.Context, sessionID string, lastTimestamp time.Time, limit int) ([]Message, error) {
    query := `
        SELECT role, content, timestamp
        FROM messages
        WHERE session_id = ? AND timestamp < ?
        ORDER BY timestamp DESC
        LIMIT ?
    `
    rows, err := db.QueryContext(ctx, query, sessionID, lastTimestamp.Unix(), limit)
    // ... scan rows
}
```


### 5. Vacuum Regularly

**Problem**: Deleted records leave empty pages (fragmentation).

**Solution**: Run `VACUUM` periodically to reclaim space.

```go
// Manual vacuum (blocks writes)
_, err := db.ExecContext(ctx, "VACUUM")

// Incremental vacuum (less blocking)
_, err := db.ExecContext(ctx, "PRAGMA incremental_vacuum(1000)")  // Pages
```

**When to vacuum**:
- After bulk deletes (e.g., delete old sessions)
- When file size exceeds expected size by >30%
- During maintenance window (low traffic)

**Check fragmentation**:
```sql
PRAGMA page_count;
PRAGMA freelist_count;
-- fragmentation_ratio = freelist_count / page_count
```


## Concurrency Management

### WAL Mode (Write-Ahead Logging)

**Enabled by default in all Loom storage implementations**.

```go
_, err := db.Exec("PRAGMA journal_mode=WAL")
```

**Benefits**:
- **Concurrent reads**: Multiple readers + 1 writer simultaneously
- **Faster writes**: Append-only writes (no random seeks)
- **Crash-safe**: WAL file ensures durability

**Files created**:
- `sessions.db-wal` - Write-ahead log
- `sessions.db-shm` - Shared memory for coordination

**Checkpointing**: WAL merged into main database periodically.

```go
// Manual checkpoint (optional, automatic by default)
_, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
```


### Busy Timeout

**Problem**: Writer blocks when another transaction is active.

**Solution**: Wait for lock with `PRAGMA busy_timeout`.

```go
// Wait up to 5 seconds for lock
_, err := db.Exec("PRAGMA busy_timeout = 5000")
```

**Without timeout**: `database is locked` error immediately.

**With timeout**: Retry automatically for 5 seconds before failing.


### Thread Safety

**All Loom storage implementations are thread-safe**:

```go
type SessionStore struct {
    db     *sql.DB
    mu     sync.RWMutex  // Protects concurrent access
    tracer observability.Tracer
}

func (s *SessionStore) SaveSession(ctx context.Context, session *Session) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    // ... database operations
}

func (s *SessionStore) LoadSession(ctx context.Context, sessionID string) (*Session, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    // ... database operations
}
```

**Pattern**:
- **Write operations**: `mu.Lock()` (exclusive)
- **Read operations**: `mu.RLock()` (shared)
- **Database transactions**: Additional SQLite-level locking


## Backup Strategies

### 1. Online Backup (Hot Backup)

**Using SQLite Backup API** (via `go-sqlite3`):

```go
import "database/sql"

func BackupDatabase(srcPath, dstPath string) error {
    src, err := sql.Open("sqlite3", srcPath)
    if err != nil {
        return err
    }
    defer src.Close()

    dst, err := sql.Open("sqlite3", dstPath)
    if err != nil {
        return err
    }
    defer dst.Close()

    // Use backup API (not simple file copy!)
    // Note: go-sqlite3 supports backup via driver-specific API
    // This is pseudocode - actual implementation requires C extension

    return nil
}
```

**Advantages**:
- No downtime
- Consistent snapshot
- Safe during writes


### 2. Filesystem Backup (Cold Backup)

**Copy database files** (requires database closed or WAL checkpoint):

```bash
# Stop Loom server
looms stop

# Copy database files
cp $LOOM_DATA_DIR/sessions.db $LOOM_DATA_DIR/backups/sessions-2025-12-11.db
cp $LOOM_DATA_DIR/hitl.db $LOOM_DATA_DIR/backups/hitl-2025-12-11.db

# Restart Loom
looms serve
```

**⚠️ Warning**: Do not copy `.db-wal` and `.db-shm` files. They are transient.


### 3. Export to SQL

```bash
# Export schema and data to SQL
sqlite3 sessions.db .dump > sessions-backup.sql

# Restore from SQL
sqlite3 restored.db < sessions-backup.sql
```

**Advantages**:
- Human-readable
- Version control friendly
- Cross-platform

**Disadvantages**:
- Large file size
- Slower restore


### 4. Automated Backup Script

```bash
#!/bin/bash
# backup-loom.sh

BACKUP_DIR=$LOOM_DATA_DIR/backups
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

mkdir -p $BACKUP_DIR

# Checkpoint WAL (merge into main database)
sqlite3 $LOOM_DATA_DIR/sessions.db "PRAGMA wal_checkpoint(TRUNCATE)"

# Copy databases
cp $LOOM_DATA_DIR/sessions.db $BACKUP_DIR/sessions-$TIMESTAMP.db
cp $LOOM_DATA_DIR/hitl.db $BACKUP_DIR/hitl-$TIMESTAMP.db
cp $LOOM_DATA_DIR/references.db $BACKUP_DIR/references-$TIMESTAMP.db

# Delete backups older than 7 days
find $BACKUP_DIR -name "*.db" -mtime +7 -delete

echo "Backup completed: $TIMESTAMP"
```

**Cron job** (daily at 2 AM):
```bash
0 2 * * * /path/to/backup-loom.sh
```


## Maintenance

### 1. Database Integrity Check

```go
func CheckIntegrity(db *sql.DB) error {
    var result string
    err := db.QueryRow("PRAGMA integrity_check").Scan(&result)
    if err != nil {
        return err
    }

    if result != "ok" {
        return fmt.Errorf("integrity check failed: %s", result)
    }

    return nil
}
```

**Run periodically** (weekly):
```bash
sqlite3 sessions.db "PRAGMA integrity_check"
# Expected: ok
```


### 2. Analyze Statistics

**Update query planner statistics**:

```sql
ANALYZE;
```

**Run after**:
- Bulk inserts
- Schema changes
- Index creation

**Effect**: Improves query optimization (query planner uses accurate statistics).


### 3. Reindex

**Rebuild all indexes**:

```sql
REINDEX;
```

**Run after**:
- Index corruption (rare)
- Collation changes
- Database recovered from backup


### 4. Monitor Database Size

```go
func GetDatabaseSize(path string) (int64, error) {
    info, err := os.Stat(path)
    if err != nil {
        return 0, err
    }
    return info.Size(), nil
}

func main() {
    size, _ := GetDatabaseSize("sessions.db")
    fmt.Printf("Database size: %.2f MB\n", float64(size)/(1024*1024))
}
```

**Expected growth**:
- Sessions: ~1KB per session
- Messages: ~500 bytes per message
- References: Size of stored content


## Monitoring

### Database Statistics

```go
func MonitorDatabase(store *agent.SessionStore) {
    ctx := context.Background()
    stats, err := store.GetStats(ctx)
    if err != nil {
        log.Printf("Failed to get stats: %v", err)
        return
    }

    log.Printf("Sessions: %d", stats.SessionCount)
    log.Printf("Messages: %d", stats.MessageCount)
    log.Printf("Tool executions: %d", stats.ToolExecutionCount)
    log.Printf("Total cost: $%.2f", stats.TotalCostUSD)
    log.Printf("Total tokens: %d", stats.TotalTokens)
}
```


### Connection Pool Metrics

```go
func MonitorConnectionPool(db *sql.DB) {
    stats := db.Stats()

    log.Printf("Open connections: %d", stats.OpenConnections)
    log.Printf("In use: %d", stats.InUse)
    log.Printf("Idle: %d", stats.Idle)
    log.Printf("Wait count: %d", stats.WaitCount)
    log.Printf("Wait duration: %s", stats.WaitDuration)
    log.Printf("Max idle closed: %d", stats.MaxIdleClosed)
    log.Printf("Max lifetime closed: %d", stats.MaxLifetimeClosed)
}

// Configure connection pool
db.SetMaxOpenConns(25)
db.SetMaxIdleConns(10)
db.SetConnMaxLifetime(5 * time.Minute)
```


### Query Performance

```go
import "time"

func measureQuery(db *sql.DB, query string, args ...interface{}) time.Duration {
    start := time.Now()
    _, err := db.ExecContext(context.Background(), query, args...)
    duration := time.Since(start)

    if err != nil {
        log.Printf("Query failed: %v", err)
    }

    log.Printf("Query duration: %s", duration)
    return duration
}
```


## Troubleshooting

### Issue: "database is locked"

**Symptoms**:
- `database is locked` error during writes
- High contention on writes

**Causes**:
1. Multiple writers (SQLite only supports 1 writer at a time)
2. Long-running transactions
3. WAL mode not enabled

**Resolution**:
```go
// 1. Enable WAL mode (should already be enabled in Loom)
db.Exec("PRAGMA journal_mode=WAL")

// 2. Set busy timeout
db.Exec("PRAGMA busy_timeout = 5000")  // 5 seconds

// 3. Keep transactions short
tx, _ := db.Begin()
// ... quick operations only
tx.Commit()  // Commit ASAP

// 4. Reduce connection pool size (avoid contention)
db.SetMaxOpenConns(1)  // Single writer
```


### Issue: Slow Queries

**Symptoms**:
- Queries take >100ms
- High CPU usage

**Diagnosis**:
```sql
-- Check query plan
EXPLAIN QUERY PLAN
SELECT * FROM messages WHERE session_id = ?;

-- Look for "SCAN TABLE" (bad, full table scan)
-- Want "SEARCH TABLE ... USING INDEX" (good)
```

**Resolution**:
```sql
-- Create missing indexes
CREATE INDEX idx_messages_session ON messages(session_id);

-- Analyze query planner statistics
ANALYZE;

-- Check if index is used
EXPLAIN QUERY PLAN SELECT ...;
```


### Issue: Database File Growing Large

**Symptoms**:
- Database file exceeds expected size
- Disk space running out

**Causes**:
1. No cleanup of old sessions
2. Fragmentation from deletes
3. Large content in reference storage

**Resolution**:
```bash
# 1. Delete old sessions
sqlite3 sessions.db "DELETE FROM sessions WHERE updated_at < strftime('%s', 'now', '-30 days')"

# 2. Vacuum to reclaim space
sqlite3 sessions.db "VACUUM"

# 3. Check fragmentation
sqlite3 sessions.db "PRAGMA page_count; PRAGMA freelist_count;"

# 4. Monitor reference storage garbage collection
# (automatic in reference store every 5 minutes)
```


### Issue: WAL File Growing Large

**Symptoms**:
- `.db-wal` file exceeds database file size
- Slow reads

**Causes**:
- No checkpoint for long time
- Long-running transactions

**Resolution**:
```go
// Manual checkpoint
db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")

// Check WAL size
info, _ := os.Stat("sessions.db-wal")
fmt.Printf("WAL size: %d bytes\n", info.Size())

// Configure auto-checkpoint threshold
db.Exec("PRAGMA wal_autocheckpoint = 1000")  // Pages
```


### Issue: Foreign Key Violations

**Symptoms**:
- `FOREIGN KEY constraint failed` error
- Orphaned records

**Cause**:
- Foreign keys not enabled

**Resolution**:
```go
// Enable at database open (required!)
db.Exec("PRAGMA foreign_keys=ON")

// Verify enabled
var fk int
db.QueryRow("PRAGMA foreign_keys").Scan(&fk)
if fk != 1 {
    log.Fatal("Foreign keys not enabled!")
}
```


## Best Practices

### 1. Always Enable WAL Mode

```go
// REQUIRED: Enable WAL at database open
db, err := sql.Open("sqlite3", "sessions.db")
if err != nil {
    return err
}

_, err = db.Exec("PRAGMA journal_mode=WAL")
if err != nil {
    return fmt.Errorf("failed to enable WAL: %w", err)
}
```

**Why**: Enables concurrent reads + 1 writer, 10x performance improvement.


### 2. Enable Foreign Keys

```go
// REQUIRED: Enable foreign keys at database open
_, err = db.Exec("PRAGMA foreign_keys=ON")
if err != nil {
    return fmt.Errorf("failed to enable foreign keys: %w", err)
}
```

**Why**: Prevents orphaned records, ensures referential integrity.


### 3. Use Transactions for Batch Operations

```go
// Bad: 1000 individual inserts
for i := 0; i < 1000; i++ {
    db.Exec("INSERT INTO messages (...) VALUES (...)")
}

// Good: 1 transaction with 1000 inserts
tx, _ := db.Begin()
for i := 0; i < 1000; i++ {
    tx.Exec("INSERT INTO messages (...) VALUES (...)")
}
tx.Commit()
```

**Why**: 100x faster (1 disk sync instead of 1000).


### 4. Create Indexes for Query Patterns

```sql
-- Index foreign keys (for JOINs)
CREATE INDEX idx_messages_session ON messages(session_id);

-- Index query filters
CREATE INDEX idx_human_requests_status ON human_requests(status);

-- Composite indexes for common queries
CREATE INDEX idx_messages_session_timestamp
    ON messages(session_id, timestamp DESC);
```

**Why**: 1000x faster queries (index seek vs full table scan).


### 5. Set Busy Timeout

```go
db.Exec("PRAGMA busy_timeout = 5000")  // 5 seconds
```

**Why**: Automatic retry on lock contention instead of immediate error.


### 6. Configure Connection Pool

```go
db.SetMaxOpenConns(25)    // Limit concurrent connections
db.SetMaxIdleConns(10)    // Keep 10 idle connections
db.SetConnMaxLifetime(5 * time.Minute)  // Recycle connections
```

**Why**: Prevents resource exhaustion, ensures connection freshness.


### 7. Checkpoint WAL Periodically

```go
// Manual checkpoint (during low traffic)
db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")

// Or configure auto-checkpoint
db.Exec("PRAGMA wal_autocheckpoint = 1000")  // Every 1000 pages
```

**Why**: Prevents `.db-wal` file from growing too large.


### 8. Run Integrity Check Regularly

```bash
# Weekly cron job
0 3 * * 0 sqlite3 $LOOM_DATA_DIR/sessions.db "PRAGMA integrity_check"
```

**Why**: Detect corruption early before data loss.


### 9. Backup Before Schema Changes

```bash
# Backup before migration
cp sessions.db sessions.db.backup

# Apply schema change
sqlite3 sessions.db "ALTER TABLE sessions ADD COLUMN new_field TEXT"

# Test
# If OK, delete backup
# If fail, restore: mv sessions.db.backup sessions.db
```

**Why**: Schema changes cannot be rolled back.


### 10. Use Observability Tracing

**All Loom storage implementations trace operations to Hawk**:

```go
ctx, span := s.tracer.StartSpan(ctx, "session_store.save_message")
defer s.tracer.EndSpan(span)

span.SetAttribute("session_id", sessionID)
span.SetAttribute("role", msg.Role)

_, err := s.db.ExecContext(ctx, ...)
if err != nil {
    span.RecordError(err)
    return err
}

span.SetAttribute("tokens", fmt.Sprintf("%d", msg.TokenCount))
```

**Why**: Monitor performance, debug errors, audit operations.


## Error Codes

### ERR_DATABASE_LOCKED

**Code**: `database_locked`
**SQLite Error**: `SQLITE_BUSY` (5)
**HTTP Status**: 503 Service Unavailable
**gRPC Code**: `UNAVAILABLE`

**Cause**: Another process/thread holds exclusive lock.

**Example**:
```
Error: database is locked
```

**Resolution**:
1. Enable WAL mode: `PRAGMA journal_mode=WAL`
2. Set busy timeout: `PRAGMA busy_timeout = 5000`
3. Reduce transaction duration
4. Check for long-running transactions

**Retry behavior**: Retryable (wait and retry)


### ERR_FOREIGN_KEY_CONSTRAINT

**Code**: `foreign_key_constraint`
**SQLite Error**: `SQLITE_CONSTRAINT_FOREIGNKEY` (787)
**HTTP Status**: 400 Bad Request
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Foreign key violation (referencing non-existent record).

**Example**:
```
Error: FOREIGN KEY constraint failed
```

**Resolution**:
1. Verify foreign key is enabled: `PRAGMA foreign_keys=ON`
2. Check parent record exists before insert
3. Use CASCADE for automatic cleanup

**Retry behavior**: Not retryable (fix data)


### ERR_DATABASE_CORRUPT

**Code**: `database_corrupt`
**SQLite Error**: `SQLITE_CORRUPT` (11)
**HTTP Status**: 500 Internal Server Error
**gRPC Code**: `DATA_LOSS`

**Cause**: Database file corruption.

**Example**:
```
Error: database disk image is malformed
```

**Resolution**:
1. Run integrity check: `PRAGMA integrity_check`
2. Restore from backup
3. If no backup, export with `.recover`: `sqlite3 db.db ".recover" | sqlite3 recovered.db`

**Retry behavior**: Not retryable (restore from backup)


### ERR_DISK_FULL

**Code**: `disk_full`
**SQLite Error**: `SQLITE_FULL` (13)
**HTTP Status**: 507 Insufficient Storage
**gRPC Code**: `RESOURCE_EXHAUSTED`

**Cause**: No disk space available.

**Example**:
```
Error: database or disk is full
```

**Resolution**:
1. Free disk space
2. Delete old sessions: `DELETE FROM sessions WHERE updated_at < ...`
3. Vacuum database: `VACUUM`

**Retry behavior**: Retryable after freeing space


### ERR_SCHEMA_MISMATCH

**Code**: `schema_mismatch`
**SQLite Error**: N/A (application-level)
**HTTP Status**: 500 Internal Server Error
**gRPC Code**: `FAILED_PRECONDITION`

**Cause**: Database schema doesn't match expected version.

**Example**:
```
Error: schema version mismatch: expected 2, got 1
```

**Resolution**:
1. Run database migrations
2. Update Loom to compatible version

**Retry behavior**: Not retryable (run migrations)


## See Also

### Reference Documentation
- [Session Management](./sessions.md) - Session lifecycle and persistence
- [HITL Reference](./hitl.md) - Human-in-the-loop system
- [Communication System](./communication.md) - Reference storage protocol
- [CLI Reference](./cli.md) - `looms` commands for database management

### Guides
- [Agent Configuration Guide](../guides/agent-configuration.md) - Configure session storage
- [HITL Guide](../guides/hitl.md) - Configure HITL persistence

### Architecture Documentation
- [Storage Architecture](../architecture/storage-system.md) - Storage design decisions

### External Resources
- [SQLite Documentation](https://www.sqlite.org/docs.html) - Official SQLite docs
- [SQLite WAL Mode](https://www.sqlite.org/wal.html) - Write-ahead logging details
- [go-sqlite3 Documentation](https://github.com/mattn/go-sqlite3) - Go driver docs
- [SQLite Performance Tuning](https://www.sqlite.org/speed.html) - Performance guide

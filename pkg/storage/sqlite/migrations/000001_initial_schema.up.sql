-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- You may obtain a copy of the License at
--
--     http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing, software
-- distributed under the License is distributed on an "AS IS" BASIS,
-- WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
-- See the License for the specific language governing permissions and
-- limitations under the License.

-- 000001_initial_schema.up.sql
-- Consolidated baseline migration for SQLite storage backend.
-- Creates all tables, FTS5 virtual tables, triggers, and indexes
-- from the 4 SQLite stores: session, error, artifact, sql_result, and human_request.

-- ============================================================================
-- Schema migrations tracking table
-- ============================================================================

CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL,
    description TEXT
);

-- ============================================================================
-- Sessions table (from session_store.go)
-- ============================================================================

CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    name TEXT,
    agent_id TEXT,
    parent_session_id TEXT,
    context_json TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    total_cost_usd REAL DEFAULT 0,
    total_tokens INTEGER DEFAULT 0,
    FOREIGN KEY (parent_session_id) REFERENCES sessions(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_agent ON sessions(agent_id);
CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_session_id);
CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at);

-- ============================================================================
-- Messages table (from session_store.go, consolidated with inline ALTER TABLEs)
-- ============================================================================

CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    role TEXT NOT NULL,
    content TEXT,
    tool_calls_json TEXT,
    tool_use_id TEXT,
    tool_result_json TEXT,
    session_context TEXT DEFAULT 'direct',
    timestamp INTEGER NOT NULL,
    token_count INTEGER DEFAULT 0,
    cost_usd REAL DEFAULT 0,
    agent_id TEXT,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id);
CREATE INDEX IF NOT EXISTS idx_messages_context ON messages(session_context);
CREATE INDEX IF NOT EXISTS idx_messages_agent ON messages(agent_id);
CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON messages(session_id, timestamp);

-- ============================================================================
-- Tool executions table (from session_store.go)
-- ============================================================================

CREATE TABLE IF NOT EXISTS tool_executions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    input_json TEXT,
    result_json TEXT,
    error TEXT,
    execution_time_ms INTEGER,
    timestamp INTEGER NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_tool_executions_session ON tool_executions(session_id);

-- ============================================================================
-- Memory snapshots table (from session_store.go)
-- ============================================================================

CREATE TABLE IF NOT EXISTS memory_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    snapshot_type TEXT NOT NULL,
    content TEXT NOT NULL,
    token_count INTEGER DEFAULT 0,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_snapshots_session ON memory_snapshots(session_id, created_at);

-- ============================================================================
-- Artifacts table (from session_store.go, consolidated with inline ALTER TABLE)
-- ============================================================================

CREATE TABLE IF NOT EXISTS artifacts (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    source TEXT NOT NULL,
    source_agent_id TEXT,
    purpose TEXT,
    content_type TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    checksum TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    last_accessed_at INTEGER,
    access_count INTEGER DEFAULT 0,
    tags TEXT,
    metadata_json TEXT,
    deleted_at INTEGER,
    session_id TEXT,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_artifacts_name ON artifacts(name);
CREATE INDEX IF NOT EXISTS idx_artifacts_source ON artifacts(source);
CREATE INDEX IF NOT EXISTS idx_artifacts_content_type ON artifacts(content_type);
CREATE INDEX IF NOT EXISTS idx_artifacts_created ON artifacts(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_artifacts_deleted ON artifacts(deleted_at);
CREATE INDEX IF NOT EXISTS idx_artifacts_session ON artifacts(session_id);

-- ============================================================================
-- Agent errors table (from error_store.go)
-- ============================================================================

CREATE TABLE IF NOT EXISTS agent_errors (
    id TEXT PRIMARY KEY,
    timestamp INTEGER NOT NULL,
    session_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    raw_error TEXT NOT NULL,
    short_summary TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_agent_errors_session ON agent_errors(session_id);
CREATE INDEX IF NOT EXISTS idx_agent_errors_timestamp ON agent_errors(timestamp);
CREATE INDEX IF NOT EXISTS idx_agent_errors_tool ON agent_errors(tool_name);

-- ============================================================================
-- SQL result metadata table (from sql_result_store.go)
-- ============================================================================

CREATE TABLE IF NOT EXISTS sql_result_metadata (
    id TEXT PRIMARY KEY,
    table_name TEXT NOT NULL,
    row_count INTEGER NOT NULL,
    column_count INTEGER NOT NULL,
    columns_json TEXT NOT NULL,
    stored_at INTEGER NOT NULL,
    accessed_at INTEGER NOT NULL,
    size_bytes INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sql_result_metadata_stored_at ON sql_result_metadata(stored_at);

-- ============================================================================
-- Human requests table (from human_store_sqlite.go)
-- ============================================================================

CREATE TABLE IF NOT EXISTS human_requests (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    question TEXT NOT NULL,
    context_json TEXT,
    request_type TEXT NOT NULL,
    priority TEXT NOT NULL,
    timeout_ms INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    status TEXT NOT NULL,
    response TEXT,
    response_data_json TEXT,
    responded_at INTEGER,
    responded_by TEXT
);

CREATE INDEX IF NOT EXISTS idx_human_requests_status ON human_requests(status);
CREATE INDEX IF NOT EXISTS idx_human_requests_session ON human_requests(session_id);
CREATE INDEX IF NOT EXISTS idx_human_requests_agent ON human_requests(agent_id);
CREATE INDEX IF NOT EXISTS idx_human_requests_priority ON human_requests(priority);
CREATE INDEX IF NOT EXISTS idx_human_requests_created ON human_requests(created_at);
CREATE INDEX IF NOT EXISTS idx_human_requests_expires ON human_requests(expires_at);

-- ============================================================================
-- FTS5 virtual tables and sync triggers (requires -tags fts5 build tag)
-- ============================================================================

-- Messages FTS5 (BM25 ranking for semantic search)
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts5 USING fts5(
    message_id UNINDEXED,
    session_id UNINDEXED,
    role UNINDEXED,
    content,
    timestamp UNINDEXED,
    tokenize='porter unicode61'
);

-- Messages FTS5 sync triggers
CREATE TRIGGER IF NOT EXISTS messages_fts5_insert AFTER INSERT ON messages
BEGIN
    INSERT INTO messages_fts5(message_id, session_id, role, content, timestamp)
    VALUES (NEW.id, NEW.session_id, NEW.role, NEW.content, NEW.timestamp);
END;

CREATE TRIGGER IF NOT EXISTS messages_fts5_update AFTER UPDATE ON messages
BEGIN
    DELETE FROM messages_fts5 WHERE message_id = OLD.id;
    INSERT INTO messages_fts5(message_id, session_id, role, content, timestamp)
    VALUES (NEW.id, NEW.session_id, NEW.role, NEW.content, NEW.timestamp);
END;

CREATE TRIGGER IF NOT EXISTS messages_fts5_delete AFTER DELETE ON messages
BEGIN
    DELETE FROM messages_fts5 WHERE message_id = OLD.id;
END;

-- Artifacts FTS5 (search by name, purpose, tags)
CREATE VIRTUAL TABLE IF NOT EXISTS artifacts_fts5 USING fts5(
    artifact_id UNINDEXED,
    name,
    purpose,
    tags,
    tokenize='porter unicode61'
);

-- Artifacts FTS5 sync triggers
CREATE TRIGGER IF NOT EXISTS artifacts_fts5_insert AFTER INSERT ON artifacts
BEGIN
    INSERT INTO artifacts_fts5(artifact_id, name, purpose, tags)
    VALUES (NEW.id, NEW.name, NEW.purpose, NEW.tags);
END;

CREATE TRIGGER IF NOT EXISTS artifacts_fts5_update AFTER UPDATE ON artifacts
BEGIN
    DELETE FROM artifacts_fts5 WHERE artifact_id = OLD.id;
    INSERT INTO artifacts_fts5(artifact_id, name, purpose, tags)
    VALUES (NEW.id, NEW.name, NEW.purpose, NEW.tags);
END;

CREATE TRIGGER IF NOT EXISTS artifacts_fts5_delete AFTER DELETE ON artifacts
BEGIN
    DELETE FROM artifacts_fts5 WHERE artifact_id = OLD.id;
END;

-- ============================================================================
-- Record this migration
-- ============================================================================

INSERT INTO schema_migrations (version, applied_at, description)
VALUES (1, strftime('%s', 'now'), 'initial_schema');

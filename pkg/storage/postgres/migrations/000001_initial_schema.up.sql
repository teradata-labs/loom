-- 000001_initial_schema.up.sql
-- Core tables for Loom storage backend (PostgreSQL equivalent of SQLite schema)

-- Sessions table
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    agent_id TEXT,
    parent_session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    context_json JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    total_cost_usd NUMERIC(18,8) DEFAULT 0,
    total_tokens INTEGER DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_sessions_agent ON sessions(agent_id);
CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_session_id);
CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at);

-- Messages table
CREATE TABLE IF NOT EXISTS messages (
    id BIGSERIAL PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    content TEXT,
    tool_calls_json JSONB,
    tool_use_id TEXT,
    tool_result_json JSONB,
    session_context TEXT DEFAULT 'direct',
    timestamp TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    token_count INTEGER DEFAULT 0,
    cost_usd NUMERIC(18,8) DEFAULT 0,
    agent_id TEXT
);

CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id);
CREATE INDEX IF NOT EXISTS idx_messages_context ON messages(session_context);
CREATE INDEX IF NOT EXISTS idx_messages_agent ON messages(agent_id);
CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON messages(session_id, timestamp);

-- Tool executions table
CREATE TABLE IF NOT EXISTS tool_executions (
    id BIGSERIAL PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    tool_name TEXT NOT NULL,
    input_json JSONB,
    result_json JSONB,
    error TEXT,
    execution_time_ms INTEGER,
    timestamp TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tool_executions_session ON tool_executions(session_id);

-- Memory snapshots table
CREATE TABLE IF NOT EXISTS memory_snapshots (
    id BIGSERIAL PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    snapshot_type TEXT NOT NULL,
    content TEXT NOT NULL,
    token_count INTEGER DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_snapshots_session ON memory_snapshots(session_id, created_at);

-- Artifacts table
CREATE TABLE IF NOT EXISTS artifacts (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    source TEXT NOT NULL,
    source_agent_id TEXT,
    purpose TEXT,
    content_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    checksum TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_accessed_at TIMESTAMPTZ,
    access_count INTEGER DEFAULT 0,
    tags JSONB,
    metadata_json JSONB,
    deleted_at TIMESTAMPTZ,
    session_id TEXT REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_artifacts_name ON artifacts(name);
CREATE INDEX IF NOT EXISTS idx_artifacts_source ON artifacts(source);
CREATE INDEX IF NOT EXISTS idx_artifacts_content_type ON artifacts(content_type);
CREATE INDEX IF NOT EXISTS idx_artifacts_created ON artifacts(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_artifacts_deleted ON artifacts(deleted_at);
CREATE INDEX IF NOT EXISTS idx_artifacts_session ON artifacts(session_id);

-- Agent errors table
CREATE TABLE IF NOT EXISTS agent_errors (
    id TEXT PRIMARY KEY,
    timestamp TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    session_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    raw_error TEXT NOT NULL,
    short_summary TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_agent_errors_session ON agent_errors(session_id);
CREATE INDEX IF NOT EXISTS idx_agent_errors_timestamp ON agent_errors(timestamp);
CREATE INDEX IF NOT EXISTS idx_agent_errors_tool ON agent_errors(tool_name);

-- SQL result metadata table
CREATE TABLE IF NOT EXISTS sql_result_metadata (
    id TEXT PRIMARY KEY,
    table_name TEXT NOT NULL,
    row_count INTEGER NOT NULL,
    column_count INTEGER NOT NULL,
    columns_json JSONB NOT NULL,
    stored_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    accessed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    size_bytes BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sql_result_metadata_stored_at ON sql_result_metadata(stored_at);

-- Human requests table
CREATE TABLE IF NOT EXISTS human_requests (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    question TEXT NOT NULL,
    context_json JSONB,
    request_type TEXT NOT NULL,
    priority TEXT NOT NULL,
    timeout_ms INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL,
    response TEXT,
    response_data_json JSONB,
    responded_at TIMESTAMPTZ,
    responded_by TEXT
);

CREATE INDEX IF NOT EXISTS idx_human_requests_status ON human_requests(status);
CREATE INDEX IF NOT EXISTS idx_human_requests_session ON human_requests(session_id);
CREATE INDEX IF NOT EXISTS idx_human_requests_agent ON human_requests(agent_id);
CREATE INDEX IF NOT EXISTS idx_human_requests_priority ON human_requests(priority);
CREATE INDEX IF NOT EXISTS idx_human_requests_created ON human_requests(created_at);
CREATE INDEX IF NOT EXISTS idx_human_requests_expires ON human_requests(expires_at);

-- Schema migrations tracking table
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    description TEXT
);

INSERT INTO schema_migrations (version, description) VALUES (1, 'initial schema')
ON CONFLICT (version) DO NOTHING;

-- Rollback migration 000002: Remove permission_mode column
-- SQLite doesn't support DROP COLUMN directly, so we need to recreate the table

-- Create new sessions table without permission_mode
CREATE TABLE sessions_new (
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

-- Copy data from old table (excluding permission_mode)
INSERT INTO sessions_new
SELECT id, name, agent_id, parent_session_id, context_json, created_at, updated_at, total_cost_usd, total_tokens
FROM sessions;

-- Drop old table and rename new one
DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;

-- Recreate indexes
CREATE INDEX IF NOT EXISTS idx_sessions_agent ON sessions(agent_id);
CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_session_id);
CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at);

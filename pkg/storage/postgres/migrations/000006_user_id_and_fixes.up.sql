-- 000006_user_id_and_fixes.up.sql
-- Add user_id columns to all tenant-scoped tables, missing FKs, and schema fixes.

-- Add user_id to all tenant-scoped tables
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT 'default-user';
ALTER TABLE messages ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT 'default-user';
ALTER TABLE artifacts ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT 'default-user';
ALTER TABLE agent_errors ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT 'default-user';
ALTER TABLE human_requests ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT 'default-user';
ALTER TABLE sql_result_metadata ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT 'default-user';

-- Add missing foreign keys (agent_errors and human_requests lacked FK to sessions)
ALTER TABLE agent_errors
  ADD CONSTRAINT fk_agent_errors_session
  FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE;

ALTER TABLE human_requests
  ADD CONSTRAINT fk_human_requests_session
  FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE;

-- Add soft delete to tool_executions and memory_snapshots (were missing)
ALTER TABLE tool_executions ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE memory_snapshots ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

-- Enable RLS on sql_result_metadata (was missing from migration 000003)
ALTER TABLE sql_result_metadata ENABLE ROW LEVEL SECURITY;

-- Indexes for user_id columns
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_user_agent ON sessions(user_id, agent_id);
CREATE INDEX IF NOT EXISTS idx_messages_user_id ON messages(user_id);
CREATE INDEX IF NOT EXISTS idx_artifacts_user_id ON artifacts(user_id);
CREATE INDEX IF NOT EXISTS idx_agent_errors_user_id ON agent_errors(user_id);
CREATE INDEX IF NOT EXISTS idx_human_requests_user_id ON human_requests(user_id);
CREATE INDEX IF NOT EXISTS idx_sql_result_metadata_user_id ON sql_result_metadata(user_id);

-- Composite index for common human_requests query (pending requests by agent)
CREATE INDEX IF NOT EXISTS idx_human_requests_agent_status_expires
  ON human_requests(agent_id, status, expires_at);

-- Update purge_soft_deleted function to include tool_executions and memory_snapshots,
-- and remove redundant messages purge (cascade from sessions handles it).
CREATE OR REPLACE FUNCTION purge_soft_deleted(grace_interval INTERVAL)
RETURNS TABLE(table_name TEXT, deleted_count BIGINT) AS $$
DECLARE
    cutoff TIMESTAMPTZ := NOW() - grace_interval;
BEGIN
    -- Purge sessions (cascades to messages, tool_executions, memory_snapshots)
    DELETE FROM sessions WHERE deleted_at IS NOT NULL AND deleted_at < cutoff;
    table_name := 'sessions';
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN NEXT;

    -- Purge artifacts (not cascade-linked to sessions for soft delete)
    DELETE FROM artifacts WHERE deleted_at IS NOT NULL AND deleted_at < cutoff;
    table_name := 'artifacts';
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN NEXT;

    -- Purge orphaned tool_executions with soft delete
    DELETE FROM tool_executions WHERE deleted_at IS NOT NULL AND deleted_at < cutoff;
    table_name := 'tool_executions';
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN NEXT;

    -- Purge orphaned memory_snapshots with soft delete
    DELETE FROM memory_snapshots WHERE deleted_at IS NOT NULL AND deleted_at < cutoff;
    table_name := 'memory_snapshots';
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN NEXT;
END;
$$ LANGUAGE plpgsql;

INSERT INTO schema_migrations (version, description) VALUES (6, 'user_id columns and schema fixes')
ON CONFLICT (version) DO NOTHING;

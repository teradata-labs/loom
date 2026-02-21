-- 000004_soft_delete.up.sql
-- Soft delete support: sessions and messages get deleted_at columns.
-- Artifacts already have deleted_at from the initial schema.

-- Add deleted_at to sessions
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_sessions_deleted ON sessions(deleted_at) WHERE deleted_at IS NOT NULL;

-- Add deleted_at to messages
ALTER TABLE messages ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_messages_deleted ON messages(deleted_at) WHERE deleted_at IS NOT NULL;

-- Function to purge soft-deleted records past a grace period
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

    -- Purge artifacts
    DELETE FROM artifacts WHERE deleted_at IS NOT NULL AND deleted_at < cutoff;
    table_name := 'artifacts';
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN NEXT;

    -- Purge messages (those not already cascaded from sessions)
    DELETE FROM messages WHERE deleted_at IS NOT NULL AND deleted_at < cutoff;
    table_name := 'messages';
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN NEXT;
END;
$$ LANGUAGE plpgsql;

INSERT INTO schema_migrations (version, description) VALUES (4, 'soft delete support')
ON CONFLICT (version) DO NOTHING;

-- 000006_user_id_and_fixes.down.sql
-- Reverse: remove user_id columns, FKs, indexes, and soft delete on extra tables.

-- Drop composite index
DROP INDEX IF EXISTS idx_human_requests_agent_status_expires;

-- Drop user_id indexes
DROP INDEX IF EXISTS idx_sql_result_metadata_user_id;
DROP INDEX IF EXISTS idx_human_requests_user_id;
DROP INDEX IF EXISTS idx_agent_errors_user_id;
DROP INDEX IF EXISTS idx_artifacts_user_id;
DROP INDEX IF EXISTS idx_messages_user_id;
DROP INDEX IF EXISTS idx_sessions_user_agent;
DROP INDEX IF EXISTS idx_sessions_user_id;

-- Disable RLS on sql_result_metadata
ALTER TABLE sql_result_metadata DISABLE ROW LEVEL SECURITY;

-- Remove soft delete from tool_executions and memory_snapshots
ALTER TABLE memory_snapshots DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE tool_executions DROP COLUMN IF EXISTS deleted_at;

-- Remove foreign keys
ALTER TABLE human_requests DROP CONSTRAINT IF EXISTS fk_human_requests_session;
ALTER TABLE agent_errors DROP CONSTRAINT IF EXISTS fk_agent_errors_session;

-- Remove user_id columns
ALTER TABLE sql_result_metadata DROP COLUMN IF EXISTS user_id;
ALTER TABLE human_requests DROP COLUMN IF EXISTS user_id;
ALTER TABLE agent_errors DROP COLUMN IF EXISTS user_id;
ALTER TABLE artifacts DROP COLUMN IF EXISTS user_id;
ALTER TABLE messages DROP COLUMN IF EXISTS user_id;
ALTER TABLE sessions DROP COLUMN IF EXISTS user_id;

-- Restore original purge_soft_deleted function (from migration 000004)
CREATE OR REPLACE FUNCTION purge_soft_deleted(grace_interval INTERVAL)
RETURNS TABLE(table_name TEXT, deleted_count BIGINT) AS $$
DECLARE
    cutoff TIMESTAMPTZ := NOW() - grace_interval;
BEGIN
    DELETE FROM sessions WHERE deleted_at IS NOT NULL AND deleted_at < cutoff;
    table_name := 'sessions';
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN NEXT;

    DELETE FROM artifacts WHERE deleted_at IS NOT NULL AND deleted_at < cutoff;
    table_name := 'artifacts';
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN NEXT;

    DELETE FROM messages WHERE deleted_at IS NOT NULL AND deleted_at < cutoff;
    table_name := 'messages';
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN NEXT;
END;
$$ LANGUAGE plpgsql;

DELETE FROM schema_migrations WHERE version = 6;

-- 000008_tool_exec_snapshot_user_id.down.sql
-- Reverse: remove user_id columns from tool_executions and memory_snapshots,
-- restore JOIN-based RLS policies from migration 007.

-- Drop direct user_id-based policies
DROP POLICY IF EXISTS tool_executions_user_isolation ON tool_executions;
DROP POLICY IF EXISTS memory_snapshots_user_isolation ON memory_snapshots;

-- Restore JOIN-based RLS policies (from migration 007)
CREATE POLICY tool_executions_user_isolation ON tool_executions
    USING (session_id IN (
        SELECT id FROM sessions WHERE user_id = current_setting('app.current_user_id', true)
    ));

CREATE POLICY memory_snapshots_user_isolation ON memory_snapshots
    USING (session_id IN (
        SELECT id FROM sessions WHERE user_id = current_setting('app.current_user_id', true)
    ));

-- Drop indexes
DROP INDEX IF EXISTS idx_tool_executions_user_id;
DROP INDEX IF EXISTS idx_memory_snapshots_user_id;

-- Remove user_id columns
ALTER TABLE tool_executions DROP COLUMN IF EXISTS user_id;
ALTER TABLE memory_snapshots DROP COLUMN IF EXISTS user_id;

DELETE FROM schema_migrations WHERE version = 8;

-- 000008_tool_exec_snapshot_user_id.up.sql
-- Add user_id columns to tool_executions and memory_snapshots tables.
-- These tables were missed in migration 006 which added user_id to all other tables.
-- The Go code already writes user_id to these tables, causing runtime errors without this migration.

-- Add user_id column to tool_executions
ALTER TABLE tool_executions ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT 'default-user';

-- Add user_id column to memory_snapshots
ALTER TABLE memory_snapshots ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT 'default-user';

-- Backfill user_id from parent sessions table via JOIN
UPDATE tool_executions te
SET user_id = s.user_id
FROM sessions s
WHERE te.session_id = s.id
  AND te.user_id = 'default-user'
  AND s.user_id != 'default-user';

UPDATE memory_snapshots ms
SET user_id = s.user_id
FROM sessions s
WHERE ms.session_id = s.id
  AND ms.user_id = 'default-user'
  AND s.user_id != 'default-user';

-- Create indexes for user_id lookups
CREATE INDEX IF NOT EXISTS idx_tool_executions_user_id ON tool_executions(user_id);
CREATE INDEX IF NOT EXISTS idx_memory_snapshots_user_id ON memory_snapshots(user_id);

-- Drop old JOIN-based RLS policies from migration 007 (inefficient subquery approach)
DROP POLICY IF EXISTS tool_executions_user_isolation ON tool_executions;
DROP POLICY IF EXISTS memory_snapshots_user_isolation ON memory_snapshots;

-- Create direct user_id-based policies WITH CHECK clauses (no subquery needed now)
CREATE POLICY tool_executions_user_isolation ON tool_executions
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

CREATE POLICY memory_snapshots_user_isolation ON memory_snapshots
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

INSERT INTO schema_migrations (version, description) VALUES (8, 'tool_executions and memory_snapshots user_id columns')
ON CONFLICT (version) DO NOTHING;

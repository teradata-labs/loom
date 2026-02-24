-- 000005_rls_with_check.down.sql
-- Remove WITH CHECK clauses from all RLS policies by recreating them without WITH CHECK.
-- This restores the policies to their original state from migration 000003.

-- Drop and recreate sessions policy without WITH CHECK
DROP POLICY IF EXISTS sessions_tenant_isolation ON sessions;
CREATE POLICY sessions_tenant_isolation ON sessions
    USING (agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL);

-- Drop and recreate messages policy without WITH CHECK
DROP POLICY IF EXISTS messages_tenant_isolation ON messages;
CREATE POLICY messages_tenant_isolation ON messages
    USING (agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL);

-- Drop and recreate tool_executions policy without WITH CHECK
DROP POLICY IF EXISTS tool_executions_tenant_isolation ON tool_executions;
CREATE POLICY tool_executions_tenant_isolation ON tool_executions
    USING (session_id IN (
        SELECT id FROM sessions
        WHERE agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL
    ));

-- Drop and recreate memory_snapshots policy without WITH CHECK
DROP POLICY IF EXISTS memory_snapshots_tenant_isolation ON memory_snapshots;
CREATE POLICY memory_snapshots_tenant_isolation ON memory_snapshots
    USING (session_id IN (
        SELECT id FROM sessions
        WHERE agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL
    ));

-- Drop and recreate artifacts policy without WITH CHECK
DROP POLICY IF EXISTS artifacts_tenant_isolation ON artifacts;
CREATE POLICY artifacts_tenant_isolation ON artifacts
    USING (source_agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL);

-- Drop and recreate agent_errors policy without WITH CHECK
DROP POLICY IF EXISTS agent_errors_tenant_isolation ON agent_errors;
CREATE POLICY agent_errors_tenant_isolation ON agent_errors
    USING (session_id IN (
        SELECT id FROM sessions
        WHERE agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL
    ));

-- Drop and recreate human_requests policy without WITH CHECK
DROP POLICY IF EXISTS human_requests_tenant_isolation ON human_requests;
CREATE POLICY human_requests_tenant_isolation ON human_requests
    USING (agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL);

DELETE FROM schema_migrations WHERE version = 5;

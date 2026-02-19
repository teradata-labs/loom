-- 000007_user_rls_policies.down.sql
-- Reverse: restore agent_id-based RLS policies with bypass for NULL/empty.

-- Remove FORCE RLS
ALTER TABLE sql_result_metadata NO FORCE ROW LEVEL SECURITY;
ALTER TABLE human_requests NO FORCE ROW LEVEL SECURITY;
ALTER TABLE agent_errors NO FORCE ROW LEVEL SECURITY;
ALTER TABLE artifacts NO FORCE ROW LEVEL SECURITY;
ALTER TABLE memory_snapshots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE tool_executions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE messages NO FORCE ROW LEVEL SECURITY;
ALTER TABLE sessions NO FORCE ROW LEVEL SECURITY;

-- Drop user-scoped policies
DROP POLICY IF EXISTS sql_result_metadata_user_isolation ON sql_result_metadata;
DROP POLICY IF EXISTS human_requests_user_isolation ON human_requests;
DROP POLICY IF EXISTS agent_errors_user_isolation ON agent_errors;
DROP POLICY IF EXISTS artifacts_user_isolation ON artifacts;
DROP POLICY IF EXISTS memory_snapshots_user_isolation ON memory_snapshots;
DROP POLICY IF EXISTS tool_executions_user_isolation ON tool_executions;
DROP POLICY IF EXISTS messages_user_isolation ON messages;
DROP POLICY IF EXISTS sessions_user_isolation ON sessions;

-- Restore original agent_id-based policies (from migrations 000003 + 000005)
CREATE POLICY sessions_tenant_isolation ON sessions
    USING (agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL)
    WITH CHECK (agent_id = current_setting('app.current_tenant_id', true)
                OR current_setting('app.current_tenant_id', true) = ''
                OR current_setting('app.current_tenant_id', true) IS NULL);

CREATE POLICY messages_tenant_isolation ON messages
    USING (agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL)
    WITH CHECK (agent_id = current_setting('app.current_tenant_id', true)
                OR current_setting('app.current_tenant_id', true) = ''
                OR current_setting('app.current_tenant_id', true) IS NULL);

CREATE POLICY tool_executions_tenant_isolation ON tool_executions
    USING (session_id IN (
        SELECT id FROM sessions
        WHERE agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL
    ))
    WITH CHECK (session_id IN (
        SELECT id FROM sessions
        WHERE agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL
    ));

CREATE POLICY memory_snapshots_tenant_isolation ON memory_snapshots
    USING (session_id IN (
        SELECT id FROM sessions
        WHERE agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL
    ))
    WITH CHECK (session_id IN (
        SELECT id FROM sessions
        WHERE agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL
    ));

CREATE POLICY artifacts_tenant_isolation ON artifacts
    USING (source_agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL)
    WITH CHECK (source_agent_id = current_setting('app.current_tenant_id', true)
                OR current_setting('app.current_tenant_id', true) = ''
                OR current_setting('app.current_tenant_id', true) IS NULL);

CREATE POLICY agent_errors_tenant_isolation ON agent_errors
    USING (session_id IN (
        SELECT id FROM sessions
        WHERE agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL
    ))
    WITH CHECK (session_id IN (
        SELECT id FROM sessions
        WHERE agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL
    ));

CREATE POLICY human_requests_tenant_isolation ON human_requests
    USING (agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL)
    WITH CHECK (agent_id = current_setting('app.current_tenant_id', true)
                OR current_setting('app.current_tenant_id', true) = ''
                OR current_setting('app.current_tenant_id', true) IS NULL);

DELETE FROM schema_migrations WHERE version = 7;

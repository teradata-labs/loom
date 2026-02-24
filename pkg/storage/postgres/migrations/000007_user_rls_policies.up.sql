-- 000007_user_rls_policies.up.sql
-- Rewrite all RLS policies from agent_id-based to user_id-based.
-- Remove bypass for NULL/empty tenant ID (strict isolation).

-- Drop all old agent_id-based policies
DROP POLICY IF EXISTS sessions_tenant_isolation ON sessions;
DROP POLICY IF EXISTS messages_tenant_isolation ON messages;
DROP POLICY IF EXISTS tool_executions_tenant_isolation ON tool_executions;
DROP POLICY IF EXISTS memory_snapshots_tenant_isolation ON memory_snapshots;
DROP POLICY IF EXISTS artifacts_tenant_isolation ON artifacts;
DROP POLICY IF EXISTS agent_errors_tenant_isolation ON agent_errors;
DROP POLICY IF EXISTS human_requests_tenant_isolation ON human_requests;

-- New user-scoped policies (NO bypass for NULL/empty — strict isolation)
CREATE POLICY sessions_user_isolation ON sessions
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

CREATE POLICY messages_user_isolation ON messages
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

CREATE POLICY tool_executions_user_isolation ON tool_executions
    USING (session_id IN (
        SELECT id FROM sessions WHERE user_id = current_setting('app.current_user_id', true)
    ));

CREATE POLICY memory_snapshots_user_isolation ON memory_snapshots
    USING (session_id IN (
        SELECT id FROM sessions WHERE user_id = current_setting('app.current_user_id', true)
    ));

CREATE POLICY artifacts_user_isolation ON artifacts
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

CREATE POLICY agent_errors_user_isolation ON agent_errors
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

CREATE POLICY human_requests_user_isolation ON human_requests
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

CREATE POLICY sql_result_metadata_user_isolation ON sql_result_metadata
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

-- FORCE RLS even for table owners (admin uses superuser role to bypass when needed)
ALTER TABLE sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE messages FORCE ROW LEVEL SECURITY;
ALTER TABLE tool_executions FORCE ROW LEVEL SECURITY;
ALTER TABLE memory_snapshots FORCE ROW LEVEL SECURITY;
ALTER TABLE artifacts FORCE ROW LEVEL SECURITY;
ALTER TABLE agent_errors FORCE ROW LEVEL SECURITY;
ALTER TABLE human_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE sql_result_metadata FORCE ROW LEVEL SECURITY;

INSERT INTO schema_migrations (version, description) VALUES (7, 'user-scoped RLS policies')
ON CONFLICT (version) DO NOTHING;

-- 000005_rls_with_check.up.sql
-- Add WITH CHECK clauses to all RLS policies.
-- USING controls which rows are visible; WITH CHECK controls which rows can be inserted/updated.
-- Without WITH CHECK, INSERT/UPDATE operations can bypass tenant isolation.

-- Sessions
ALTER POLICY sessions_tenant_isolation ON sessions
    WITH CHECK (agent_id = current_setting('app.current_tenant_id', true)
                OR current_setting('app.current_tenant_id', true) = ''
                OR current_setting('app.current_tenant_id', true) IS NULL);

-- Messages
ALTER POLICY messages_tenant_isolation ON messages
    WITH CHECK (agent_id = current_setting('app.current_tenant_id', true)
                OR current_setting('app.current_tenant_id', true) = ''
                OR current_setting('app.current_tenant_id', true) IS NULL);

-- Tool executions
ALTER POLICY tool_executions_tenant_isolation ON tool_executions
    WITH CHECK (session_id IN (
        SELECT id FROM sessions
        WHERE agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL
    ));

-- Memory snapshots
ALTER POLICY memory_snapshots_tenant_isolation ON memory_snapshots
    WITH CHECK (session_id IN (
        SELECT id FROM sessions
        WHERE agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL
    ));

-- Artifacts
ALTER POLICY artifacts_tenant_isolation ON artifacts
    WITH CHECK (source_agent_id = current_setting('app.current_tenant_id', true)
                OR current_setting('app.current_tenant_id', true) = ''
                OR current_setting('app.current_tenant_id', true) IS NULL);

-- Agent errors
ALTER POLICY agent_errors_tenant_isolation ON agent_errors
    WITH CHECK (session_id IN (
        SELECT id FROM sessions
        WHERE agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL
    ));

-- Human requests
ALTER POLICY human_requests_tenant_isolation ON human_requests
    WITH CHECK (agent_id = current_setting('app.current_tenant_id', true)
                OR current_setting('app.current_tenant_id', true) = ''
                OR current_setting('app.current_tenant_id', true) IS NULL);

INSERT INTO schema_migrations (version, description) VALUES (5, 'rls with check clauses')
ON CONFLICT (version) DO NOTHING;

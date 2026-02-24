-- 000003_rls_policies.up.sql
-- Row-Level Security policies for multi-tenant isolation.
-- Requires the application to SET LOCAL app.current_tenant_id before queries.

-- Enable RLS on all main tables
ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE tool_executions ENABLE ROW LEVEL SECURITY;
ALTER TABLE memory_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE artifacts ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_errors ENABLE ROW LEVEL SECURITY;
ALTER TABLE human_requests ENABLE ROW LEVEL SECURITY;

-- Sessions: tenant isolation via agent_id
CREATE POLICY sessions_tenant_isolation ON sessions
    USING (agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL);

-- Messages: tenant isolation via agent_id
CREATE POLICY messages_tenant_isolation ON messages
    USING (agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL);

-- Tool executions: inherit from session
CREATE POLICY tool_executions_tenant_isolation ON tool_executions
    USING (session_id IN (
        SELECT id FROM sessions
        WHERE agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL
    ));

-- Memory snapshots: inherit from session
CREATE POLICY memory_snapshots_tenant_isolation ON memory_snapshots
    USING (session_id IN (
        SELECT id FROM sessions
        WHERE agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL
    ));

-- Artifacts: tenant isolation via source_agent_id
CREATE POLICY artifacts_tenant_isolation ON artifacts
    USING (source_agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL);

-- Agent errors: tenant isolation via session_id -> agent_id
CREATE POLICY agent_errors_tenant_isolation ON agent_errors
    USING (session_id IN (
        SELECT id FROM sessions
        WHERE agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL
    ));

-- Human requests: tenant isolation via agent_id
CREATE POLICY human_requests_tenant_isolation ON human_requests
    USING (agent_id = current_setting('app.current_tenant_id', true)
           OR current_setting('app.current_tenant_id', true) = ''
           OR current_setting('app.current_tenant_id', true) IS NULL);

INSERT INTO schema_migrations (version, description) VALUES (3, 'row-level security policies')
ON CONFLICT (version) DO NOTHING;

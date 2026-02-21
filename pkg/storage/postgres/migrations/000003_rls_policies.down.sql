-- 000003_rls_policies.down.sql

DROP POLICY IF EXISTS human_requests_tenant_isolation ON human_requests;
DROP POLICY IF EXISTS agent_errors_tenant_isolation ON agent_errors;
DROP POLICY IF EXISTS artifacts_tenant_isolation ON artifacts;
DROP POLICY IF EXISTS memory_snapshots_tenant_isolation ON memory_snapshots;
DROP POLICY IF EXISTS tool_executions_tenant_isolation ON tool_executions;
DROP POLICY IF EXISTS messages_tenant_isolation ON messages;
DROP POLICY IF EXISTS sessions_tenant_isolation ON sessions;

ALTER TABLE human_requests DISABLE ROW LEVEL SECURITY;
ALTER TABLE agent_errors DISABLE ROW LEVEL SECURITY;
ALTER TABLE artifacts DISABLE ROW LEVEL SECURITY;
ALTER TABLE memory_snapshots DISABLE ROW LEVEL SECURITY;
ALTER TABLE tool_executions DISABLE ROW LEVEL SECURITY;
ALTER TABLE messages DISABLE ROW LEVEL SECURITY;
ALTER TABLE sessions DISABLE ROW LEVEL SECURITY;

DELETE FROM schema_migrations WHERE version = 3;

-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000016_rls_honor_jwt_sub.up.sql
-- Let RLS recognize the caller via EITHER Loom's app.current_user_id GUC OR the
-- Supabase JWT `sub` claim, so an external Supabase-authenticated consumer
-- (e.g. Dreambase reading the analytics views in 000014) sees its own rows.
--
-- WHY: Loom's own DB layer sets app.current_user_id (see tenant.go); the prior
-- policies (000007) matched ONLY that GUC. PostgREST/Supabase callers never set
-- it — they authenticate with a JWT — so the security_invoker views returned
-- zero rows even though the data exists. Loom writes user_id = the Supabase
-- `sub` (the HTTP-MCP edge forwards it), so matching the JWT sub is exact and
-- stays per-user scoped: a caller still sees only their own rows.
--
-- PORTABLE: uses only the built-in current_setting(); the request.jwt.* GUCs are
-- set by PostgREST and are simply absent (NULL) on a plain Postgres test DB, so
-- this is a no-op there and the existing app.current_user_id path is unchanged.

-- Resolve the effective caller id from the first available identity source.
-- STABLE + SQL; mirrors Supabase's auth.uid() resolution but returns text
-- (user_id is text) and never errors on absent/empty settings.
CREATE OR REPLACE FUNCTION loom_current_user_id() RETURNS text
    LANGUAGE sql
    STABLE
AS $$
    SELECT COALESCE(
        NULLIF(current_setting('app.current_user_id', true), ''),
        NULLIF(current_setting('request.jwt.claim.sub', true), ''),
        NULLIF(current_setting('request.jwt.claims', true), '')::jsonb ->> 'sub'
    )
$$;

-- Direct user_id tables: match the resolved caller id.
DROP POLICY IF EXISTS sessions_user_isolation ON sessions;
CREATE POLICY sessions_user_isolation ON sessions
    USING (user_id = loom_current_user_id())
    WITH CHECK (user_id = loom_current_user_id());

DROP POLICY IF EXISTS messages_user_isolation ON messages;
CREATE POLICY messages_user_isolation ON messages
    USING (user_id = loom_current_user_id())
    WITH CHECK (user_id = loom_current_user_id());

DROP POLICY IF EXISTS artifacts_user_isolation ON artifacts;
CREATE POLICY artifacts_user_isolation ON artifacts
    USING (user_id = loom_current_user_id())
    WITH CHECK (user_id = loom_current_user_id());

DROP POLICY IF EXISTS agent_errors_user_isolation ON agent_errors;
CREATE POLICY agent_errors_user_isolation ON agent_errors
    USING (user_id = loom_current_user_id())
    WITH CHECK (user_id = loom_current_user_id());

DROP POLICY IF EXISTS human_requests_user_isolation ON human_requests;
CREATE POLICY human_requests_user_isolation ON human_requests
    USING (user_id = loom_current_user_id())
    WITH CHECK (user_id = loom_current_user_id());

DROP POLICY IF EXISTS sql_result_metadata_user_isolation ON sql_result_metadata;
CREATE POLICY sql_result_metadata_user_isolation ON sql_result_metadata
    USING (user_id = loom_current_user_id())
    WITH CHECK (user_id = loom_current_user_id());

-- session_id-scoped tables: match rows whose owning session belongs to the caller.
DROP POLICY IF EXISTS tool_executions_user_isolation ON tool_executions;
CREATE POLICY tool_executions_user_isolation ON tool_executions
    USING (session_id IN (
        SELECT id FROM sessions WHERE user_id = loom_current_user_id()
    ));

DROP POLICY IF EXISTS memory_snapshots_user_isolation ON memory_snapshots;
CREATE POLICY memory_snapshots_user_isolation ON memory_snapshots
    USING (session_id IN (
        SELECT id FROM sessions WHERE user_id = loom_current_user_id()
    ));

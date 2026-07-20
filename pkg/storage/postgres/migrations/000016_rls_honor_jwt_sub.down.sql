-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000016_rls_honor_jwt_sub.down.sql
-- Revert to the strict app.current_user_id-only policies from 000007 and drop
-- the helper function.

DROP POLICY IF EXISTS sessions_user_isolation ON sessions;
CREATE POLICY sessions_user_isolation ON sessions
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

DROP POLICY IF EXISTS messages_user_isolation ON messages;
CREATE POLICY messages_user_isolation ON messages
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

DROP POLICY IF EXISTS artifacts_user_isolation ON artifacts;
CREATE POLICY artifacts_user_isolation ON artifacts
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

DROP POLICY IF EXISTS agent_errors_user_isolation ON agent_errors;
CREATE POLICY agent_errors_user_isolation ON agent_errors
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

DROP POLICY IF EXISTS human_requests_user_isolation ON human_requests;
CREATE POLICY human_requests_user_isolation ON human_requests
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

DROP POLICY IF EXISTS sql_result_metadata_user_isolation ON sql_result_metadata;
CREATE POLICY sql_result_metadata_user_isolation ON sql_result_metadata
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

DROP POLICY IF EXISTS tool_executions_user_isolation ON tool_executions;
CREATE POLICY tool_executions_user_isolation ON tool_executions
    USING (session_id IN (
        SELECT id FROM sessions WHERE user_id = current_setting('app.current_user_id', true)
    ));

DROP POLICY IF EXISTS memory_snapshots_user_isolation ON memory_snapshots;
CREATE POLICY memory_snapshots_user_isolation ON memory_snapshots
    USING (session_id IN (
        SELECT id FROM sessions WHERE user_id = current_setting('app.current_user_id', true)
    ));

DROP FUNCTION IF EXISTS loom_current_user_id();

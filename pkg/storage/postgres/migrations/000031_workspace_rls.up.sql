-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000031_workspace_rls.up.sql
-- Workspaces belong to their owner, as sessions belong to theirs (000007,
-- 000016): a caller sees and changes only the workspaces it owns, and only
-- their repos. Same resolution as every other user table: loom_current_user_id().

ALTER TABLE workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE workspace_repos ENABLE ROW LEVEL SECURITY;

CREATE POLICY workspaces_user_isolation ON workspaces
    USING (owner_user_id = loom_current_user_id())
    WITH CHECK (owner_user_id = loom_current_user_id());

CREATE POLICY workspace_repos_user_isolation ON workspace_repos
    USING (workspace_id IN (SELECT id FROM workspaces WHERE owner_user_id = loom_current_user_id()))
    WITH CHECK (workspace_id IN (SELECT id FROM workspaces WHERE owner_user_id = loom_current_user_id()));

DROP POLICY IF EXISTS workspace_repos_user_isolation ON workspace_repos;
DROP POLICY IF EXISTS workspaces_user_isolation ON workspaces;
ALTER TABLE workspace_repos DISABLE ROW LEVEL SECURITY;
ALTER TABLE workspaces DISABLE ROW LEVEL SECURITY;

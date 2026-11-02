-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- 000030_workspaces.down.sql

DROP INDEX IF EXISTS idx_sessions_workspace;
ALTER TABLE sessions DROP COLUMN IF EXISTS workspace_id;
DROP TABLE IF EXISTS workspace_repos;
DROP INDEX IF EXISTS ws_personal;
DROP TABLE IF EXISTS workspaces;

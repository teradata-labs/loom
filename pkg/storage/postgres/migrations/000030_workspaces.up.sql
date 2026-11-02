-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- 000030_workspaces.up.sql
--
-- A workspace is where work lives: one body of work, the repositories it
-- spans, its sessions. Every session binds to exactly one, for its whole
-- life. This is cloud's workspace shape minus the users foreign key and RLS
-- (cloud's own layer, added on top), plus `kind`, which decides the shape of
-- the workspace directory.
--
-- Existing sessions are adopted into a personal workspace per user so that
-- sessions from before workspaces existed have somewhere to live.

CREATE TABLE IF NOT EXISTS workspaces (
    id             TEXT        PRIMARY KEY,
    owner_user_id  TEXT        NOT NULL DEFAULT 'default-user',
    name           TEXT        NOT NULL,
    kind           TEXT        NOT NULL,
    root_path      TEXT        NOT NULL DEFAULT '',
    is_personal    BOOLEAN     NOT NULL DEFAULT FALSE,
    status         TEXT        NOT NULL DEFAULT 'active',
    config_json    JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (owner_user_id, name)
);
CREATE UNIQUE INDEX IF NOT EXISTS ws_personal
    ON workspaces (owner_user_id) WHERE is_personal;

CREATE TABLE IF NOT EXISTS workspace_repos (
    workspace_id   TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    url            TEXT NOT NULL,
    ref            TEXT NOT NULL,
    worktree_path  TEXT NOT NULL,
    PRIMARY KEY (workspace_id, name)
);

ALTER TABLE sessions ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_sessions_workspace ON sessions (workspace_id);

-- One personal workspace per user who has unbound sessions.
INSERT INTO workspaces (id, owner_user_id, name, kind, root_path, is_personal, status)
SELECT gen_random_uuid()::text, u.user_id, 'default', 'scratch', '', TRUE, 'active'
FROM (SELECT DISTINCT user_id FROM sessions WHERE workspace_id = '') u
ON CONFLICT DO NOTHING;

UPDATE sessions s
SET    workspace_id = w.id
FROM   workspaces w
WHERE  w.owner_user_id = s.user_id
  AND  w.is_personal
  AND  s.workspace_id = '';

-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000009_task_created_via.up.sql
-- Records how a task came to exist. Without it a reader has to infer creator
-- provenance from owner_agent_id + claimed_by_session + parent_id, which is
-- ambiguous — a gap the task data model has had since the beginning.
--
-- Values: user, agent, decompose, skill_template, workflow.
-- See pkg/task/attribution.go for the constants.
--
-- NOTE ON SCOPE: the companion task_id join columns on `messages` and
-- `human_requests` are deliberately NOT added here. Both tables are created by
-- migration 000001, and the migrator BASELINES 000001 on a pre-migration
-- database (stamping it applied without running it — see
-- TestBootstrap_PreMigrationDB). On that path those tables may not exist, so an
-- ALTER against them fails the whole migration. `messages` additionally has a
-- second schema owner in pkg/agent/session_store.go, and two ALTERs adding the
-- same column collide because SQLite has no ADD COLUMN IF NOT EXISTS.
--
-- Those columns are therefore added by the code that owns each table, using the
-- pragma-guarded idempotent pattern already established there for agent_id,
-- session_context, and tool_use_id. `tasks` is safe here because it comes from
-- 000003, which is never baselined.

ALTER TABLE tasks ADD COLUMN created_via TEXT NOT NULL DEFAULT '';

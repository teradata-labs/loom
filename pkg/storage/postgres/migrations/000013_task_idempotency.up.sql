-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000013_task_idempotency.up.sql
-- Adds a nullable skill_idempotency_key column to tasks plus a partial
-- UNIQUE index that fires only on non-null values. Used by the skills
-- task emitter (Phase 6 of the skills overhaul) to dedupe concurrent
-- skill activations creating tasks for the same (skill, session, step).

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS skill_idempotency_key TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_skill_idempotency
    ON tasks(skill_idempotency_key)
    WHERE skill_idempotency_key IS NOT NULL;

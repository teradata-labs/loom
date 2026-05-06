-- Copyright 2026 Teradata

-- 000013_task_idempotency.down.sql

DROP INDEX IF EXISTS idx_tasks_skill_idempotency;
ALTER TABLE tasks DROP COLUMN IF EXISTS skill_idempotency_key;

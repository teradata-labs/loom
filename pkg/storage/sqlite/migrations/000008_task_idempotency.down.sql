-- Copyright 2026 Teradata

-- 000008_task_idempotency.down.sql
-- SQLite's reliable DROP COLUMN support is limited; the project convention
-- is to leave nullable columns in place on rollback (see 000005 down).
-- We drop the partial unique index so the column degrades to a passive
-- nullable field, but the column itself remains and is harmless.

DROP INDEX IF EXISTS idx_tasks_skill_idempotency;

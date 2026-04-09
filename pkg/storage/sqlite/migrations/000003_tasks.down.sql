-- 000003_tasks.down.sql
-- Rollback: remove task decomposition and kanban tables.

DROP TRIGGER IF EXISTS tasks_fts_delete;
DROP TRIGGER IF EXISTS tasks_fts_soft_delete;
DROP TRIGGER IF EXISTS tasks_fts_update;
DROP TRIGGER IF EXISTS tasks_fts_insert;
DROP TABLE IF EXISTS tasks_fts;
DROP TABLE IF EXISTS task_history;
DROP TABLE IF EXISTS task_dependencies;
DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS task_boards;

DELETE FROM schema_migrations WHERE version = 3;

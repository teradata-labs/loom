-- 000011_tasks.down.sql
-- Rollback: remove task decomposition and kanban tables.

DROP POLICY IF EXISTS task_history_via_tasks ON task_history;
DROP TABLE IF EXISTS task_history;
DROP POLICY IF EXISTS task_deps_via_tasks ON task_dependencies;
DROP TABLE IF EXISTS task_dependencies;
DROP POLICY IF EXISTS tasks_user_isolation ON tasks;
DROP TABLE IF EXISTS tasks;
DROP POLICY IF EXISTS task_boards_user_isolation ON task_boards;
DROP TABLE IF EXISTS task_boards;

DELETE FROM schema_migrations WHERE version = 11;

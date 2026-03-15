-- Rollback migration 000010: Remove permission_mode column

DROP INDEX IF EXISTS idx_sessions_permission_mode;
ALTER TABLE sessions DROP COLUMN permission_mode;

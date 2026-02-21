-- 000004_soft_delete.down.sql

DROP FUNCTION IF EXISTS purge_soft_deleted(INTERVAL);

DROP INDEX IF EXISTS idx_messages_deleted;
ALTER TABLE messages DROP COLUMN IF EXISTS deleted_at;

DROP INDEX IF EXISTS idx_sessions_deleted;
ALTER TABLE sessions DROP COLUMN IF EXISTS deleted_at;

DELETE FROM schema_migrations WHERE version = 4;

-- 000002_fts_indexes.down.sql

DROP INDEX IF EXISTS idx_artifacts_fts;
ALTER TABLE artifacts DROP COLUMN IF EXISTS artifact_search;

DROP INDEX IF EXISTS idx_messages_fts;
ALTER TABLE messages DROP COLUMN IF EXISTS content_search;

DELETE FROM schema_migrations WHERE version = 2;

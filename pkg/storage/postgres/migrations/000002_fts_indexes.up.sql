-- 000002_fts_indexes.up.sql
-- Full-text search using tsvector generated columns and GIN indexes.
-- PostgreSQL equivalent of SQLite FTS5 virtual tables.

-- Add tsvector generated column for message content search
ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS content_search tsvector
    GENERATED ALWAYS AS (to_tsvector('english', COALESCE(content, ''))) STORED;

CREATE INDEX IF NOT EXISTS idx_messages_fts ON messages USING GIN(content_search);

-- Add tsvector generated column for artifact search
-- Weighted: name=A, purpose=B, tags (as text)=C
ALTER TABLE artifacts
    ADD COLUMN IF NOT EXISTS artifact_search tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('english', COALESCE(name, '')), 'A') ||
        setweight(to_tsvector('english', COALESCE(purpose, '')), 'B') ||
        setweight(to_tsvector('english', COALESCE(tags::text, '')), 'C')
    ) STORED;

CREATE INDEX IF NOT EXISTS idx_artifacts_fts ON artifacts USING GIN(artifact_search);

INSERT INTO schema_migrations (version, description) VALUES (2, 'full-text search indexes')
ON CONFLICT (version) DO NOTHING;

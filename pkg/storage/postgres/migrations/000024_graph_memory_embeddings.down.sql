-- 000024_graph_memory_embeddings.down.sql
-- Rollback: remove pgvector-backed semantic recall columns and index.
--
-- The vector extension is intentionally left installed: other schemas or
-- databases in the same cluster may depend on it, and re-running the up
-- migration is idempotent (CREATE EXTENSION IF NOT EXISTS).

DROP INDEX IF EXISTS idx_graph_memories_embedding;

ALTER TABLE graph_memories
    DROP COLUMN IF EXISTS embedding,
    DROP COLUMN IF EXISTS embedding_model;

-- Remove migration record
DELETE FROM schema_migrations WHERE version = 24;

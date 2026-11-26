-- 000025_graph_memory_hnsw.down.sql
-- Rollback: drop the HNSW index and un-pin the embedding dimension.

DROP INDEX IF EXISTS idx_graph_memories_embedding_hnsw;

-- Revert to a dimension-agnostic vector column (matches 000024).
ALTER TABLE graph_memories
    ALTER COLUMN embedding TYPE vector;

-- Remove migration record
DELETE FROM schema_migrations WHERE version = 25;

-- 000024_graph_memory_embeddings.up.sql
-- Adds pgvector-backed semantic recall for graph_memories.
--
-- The embedding column is declared without a fixed dimension so it stays
-- model-agnostic (different embedders produce different dimensionalities).
-- VectorRecall always filters by embedding_model, so every candidate row in a
-- single query shares the query vector's dimension and the cosine-distance
-- operator (<=>) never sees a dimension mismatch.
--
-- No ANN index (ivfflat/hnsw) is created: those require a fixed dimension and
-- the workload here is per-agent exact search over a small candidate set.

CREATE EXTENSION IF NOT EXISTS vector;

ALTER TABLE graph_memories
    ADD COLUMN IF NOT EXISTS embedding vector,
    ADD COLUMN IF NOT EXISTS embedding_model TEXT;

-- Partial index to quickly restrict candidates to rows that actually carry an
-- embedding for a given agent/model before the exact cosine scan.
CREATE INDEX IF NOT EXISTS idx_graph_memories_embedding
    ON graph_memories(agent_id, embedding_model)
    WHERE embedding IS NOT NULL;

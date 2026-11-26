-- 000032_graph_memory_hnsw.up.sql
-- Enables an HNSW approximate-nearest-neighbor index for semantic recall.
--
-- HNSW requires a FIXED-dimension vector column, so this migration pins
-- graph_memories.embedding to vector(1024) — matching Amazon Titan Text
-- Embeddings v2 and Cohere Embed on Bedrock (the deployment target).
--
-- CHANGING THE DIMENSION:
--   If you switch to a model with a different dimensionality (e.g. Titan v1 or
--   OpenAI text-embedding-3-small = 1536), change BOTH the ALTER TYPE below and
--   drop/recreate the index. Rows whose embedding dimension differs from the
--   pinned value can no longer be inserted, so pin to the embedder you run.
--
-- TRADE-OFF:
--   HNSW is approximate (recall < 100%) but sub-linear. Combined with the
--   per-agent/per-model WHERE filters in VectorRecall, recall stays high. The
--   exact-scan behavior still works for correctness; HNSW just accelerates the
--   ORDER BY ... <=> ... LIMIT path.

-- Pin the embedding column to a fixed dimension (required for HNSW).
-- Safe on a fresh column: 000031 added `embedding` with no rows carrying a
-- conflicting dimension yet. If existing rows have a different dimension this
-- ALTER will fail loudly rather than silently corrupt the index.
ALTER TABLE graph_memories
    ALTER COLUMN embedding TYPE vector(1024);

-- Drop the plain B-tree filter index's role in vector ordering is unchanged;
-- keep it for the agent_id/embedding_model pre-filter. Add the HNSW index for
-- cosine distance (matches the `<=>` operator used by VectorRecall).
CREATE INDEX IF NOT EXISTS idx_graph_memories_embedding_hnsw
    ON graph_memories USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);

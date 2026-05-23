-- 000003_embedding_column.up.sql
-- Add embedding BLOB + model tracking for vector similarity search.
-- SQLite stores embeddings as raw little-endian float32 bytes.
-- PostgreSQL uses pgvector's vector type instead.
--
-- embedding_model tracks which model produced each embedding so that
-- model changes don't corrupt recall (incompatible vector spaces).

ALTER TABLE graph_memories ADD COLUMN embedding BLOB;
ALTER TABLE graph_memories ADD COLUMN embedding_model TEXT;

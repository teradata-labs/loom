-- 000010_graph_memory.down.sql
-- Rollback: remove graph memory tables, indexes, RLS policies.

-- Drop RLS policies
DROP POLICY IF EXISTS graph_memory_lineage_user_isolation ON graph_memory_lineage;
DROP POLICY IF EXISTS graph_memory_entities_user_isolation ON graph_memory_entities;
DROP POLICY IF EXISTS graph_memories_user_isolation ON graph_memories;
DROP POLICY IF EXISTS graph_edges_user_isolation ON graph_edges;
DROP POLICY IF EXISTS graph_entities_user_isolation ON graph_entities;

-- Drop tables in dependency order
DROP TABLE IF EXISTS graph_memory_lineage;
DROP TABLE IF EXISTS graph_memory_entities;
DROP TABLE IF EXISTS graph_memories;
DROP TABLE IF EXISTS graph_edges;
DROP TABLE IF EXISTS graph_entities;

-- Remove migration record
DELETE FROM schema_migrations WHERE version = 10;

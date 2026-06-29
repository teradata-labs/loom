-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000017_graph_rls_honor_jwt.up.sql
-- Extend the loom_current_user_id() RLS bridge (000016) to the graph-memory
-- tables (000010). Without this, a Supabase-authenticated consumer (Dreambase)
-- can't read the knowledge graph via PostgREST/SQL — the policies only matched
-- Loom's internal app.current_user_id GUC, which external JWT callers never set.
-- loom_current_user_id() resolves the caller from that GUC OR the Supabase JWT
-- sub, so both Loom and Dreambase see the user's own rows (still per-user scoped).

-- Direct user_id tables.
DROP POLICY IF EXISTS graph_entities_user_isolation ON graph_entities;
CREATE POLICY graph_entities_user_isolation ON graph_entities
    USING (user_id = loom_current_user_id())
    WITH CHECK (user_id = loom_current_user_id());

DROP POLICY IF EXISTS graph_edges_user_isolation ON graph_edges;
CREATE POLICY graph_edges_user_isolation ON graph_edges
    USING (user_id = loom_current_user_id())
    WITH CHECK (user_id = loom_current_user_id());

DROP POLICY IF EXISTS graph_memories_user_isolation ON graph_memories;
CREATE POLICY graph_memories_user_isolation ON graph_memories
    USING (user_id = loom_current_user_id())
    WITH CHECK (user_id = loom_current_user_id());

-- memory_id / new_memory_id scoped via graph_memories.
DROP POLICY IF EXISTS graph_memory_entities_user_isolation ON graph_memory_entities;
CREATE POLICY graph_memory_entities_user_isolation ON graph_memory_entities
    USING (memory_id IN (
        SELECT id FROM graph_memories WHERE user_id = loom_current_user_id()
    ));

DROP POLICY IF EXISTS graph_memory_lineage_user_isolation ON graph_memory_lineage;
CREATE POLICY graph_memory_lineage_user_isolation ON graph_memory_lineage
    USING (new_memory_id IN (
        SELECT id FROM graph_memories WHERE user_id = loom_current_user_id()
    ));

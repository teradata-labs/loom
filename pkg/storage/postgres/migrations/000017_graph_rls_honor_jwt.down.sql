-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000017_graph_rls_honor_jwt.down.sql
-- Revert the graph-memory policies to the strict app.current_user_id-only form.

DROP POLICY IF EXISTS graph_entities_user_isolation ON graph_entities;
CREATE POLICY graph_entities_user_isolation ON graph_entities
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

DROP POLICY IF EXISTS graph_edges_user_isolation ON graph_edges;
CREATE POLICY graph_edges_user_isolation ON graph_edges
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

DROP POLICY IF EXISTS graph_memories_user_isolation ON graph_memories;
CREATE POLICY graph_memories_user_isolation ON graph_memories
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

DROP POLICY IF EXISTS graph_memory_entities_user_isolation ON graph_memory_entities;
CREATE POLICY graph_memory_entities_user_isolation ON graph_memory_entities
    USING (memory_id IN (
        SELECT id FROM graph_memories WHERE user_id = current_setting('app.current_user_id', true)
    ));

DROP POLICY IF EXISTS graph_memory_lineage_user_isolation ON graph_memory_lineage;
CREATE POLICY graph_memory_lineage_user_isolation ON graph_memory_lineage
    USING (new_memory_id IN (
        SELECT id FROM graph_memories WHERE user_id = current_setting('app.current_user_id', true)
    ));

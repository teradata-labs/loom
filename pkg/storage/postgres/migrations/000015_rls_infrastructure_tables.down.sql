-- Copyright 2026 Teradata

-- 000015_rls_infrastructure_tables.down.sql

ALTER TABLE skill_index_nodes DISABLE ROW LEVEL SECURITY;
ALTER TABLE skill_indices DISABLE ROW LEVEL SECURITY;
ALTER TABLE schema_migrations DISABLE ROW LEVEL SECURITY;

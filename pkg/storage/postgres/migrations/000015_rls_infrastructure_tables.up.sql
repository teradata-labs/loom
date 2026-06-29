-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000015_rls_infrastructure_tables.up.sql
-- Enable row-level security on the three infrastructure tables that had it
-- disabled (flagged by the Supabase Security Advisor as "RLS Disabled in
-- Public"): schema_migrations, skill_indices, skill_index_nodes.
--
-- WHY: on Supabase, every table in the public schema is auto-exposed through
-- the PostgREST Data API, and the anon/authenticated roles hold default
-- grants. With RLS disabled, anyone holding the (intentionally public) anon
-- key could read AND write these tables over HTTPS:
--   - skill_indices / skill_index_nodes: writable-by-anon would allow
--     poisoning the skill index that influences agent skill activation.
--   - schema_migrations: writable-by-anon would allow tampering with
--     migration bookkeeping (e.g. inserting a fake future version).
--
-- Enabling RLS with NO policies is deny-by-default for every role that does
-- not own the table: PostgREST's anon/authenticated see zero rows and cannot
-- write. Loom itself is unaffected — it connects as the table owner
-- (postgres), and owners bypass RLS unless FORCE ROW LEVEL SECURITY is set
-- (deliberately not set here, matching Loom's other tables; see 000003).
--
-- These tables hold process-wide infrastructure state, not per-user rows, so
-- unlike sessions/messages/etc. (000003/000007) no user-scoped policies are
-- added: nothing but Loom should touch them.
--
-- schema_migrations is created by the migrator itself (ensureMigrationsTable)
-- before any migration runs, so it always exists by the time this executes.

ALTER TABLE schema_migrations ENABLE ROW LEVEL SECURITY;
ALTER TABLE skill_indices ENABLE ROW LEVEL SECURITY;
ALTER TABLE skill_index_nodes ENABLE ROW LEVEL SECURITY;

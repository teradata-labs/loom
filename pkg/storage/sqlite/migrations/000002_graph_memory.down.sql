-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- You may obtain a copy of the License at
--
--     http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing, software
-- distributed under the License is distributed on an "AS IS" BASIS,
-- WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
-- See the License for the specific language governing permissions and
-- limitations under the License.

-- 000002_graph_memory.down.sql
-- Rollback: remove graph memory tables, FTS, triggers, indexes.

-- Drop FTS triggers first
DROP TRIGGER IF EXISTS graph_entities_fts_delete;
DROP TRIGGER IF EXISTS graph_entities_fts_update;
DROP TRIGGER IF EXISTS graph_entities_fts_insert;
DROP TRIGGER IF EXISTS graph_memories_fts_delete;
DROP TRIGGER IF EXISTS graph_memories_fts_insert;

-- Drop FTS virtual tables
DROP TABLE IF EXISTS graph_entities_fts;
DROP TABLE IF EXISTS graph_memories_fts;

-- Drop tables in dependency order
DROP TABLE IF EXISTS graph_memory_lineage;
DROP TABLE IF EXISTS graph_memory_entities;
DROP TABLE IF EXISTS graph_memories;
DROP TABLE IF EXISTS graph_edges;
DROP TABLE IF EXISTS graph_entities;

-- Remove migration record
DELETE FROM schema_migrations WHERE version = 2;

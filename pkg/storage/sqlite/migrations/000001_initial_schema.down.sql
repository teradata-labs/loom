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

-- 000001_initial_schema.down.sql
-- Reverse the initial schema creation.
-- Drops all tables in reverse dependency order.

-- Drop FTS5 virtual tables first
DROP TABLE IF EXISTS artifacts_fts5;
DROP TABLE IF EXISTS messages_fts5;

-- Drop triggers (explicit, though they auto-drop with their parent tables in SQLite)
DROP TRIGGER IF EXISTS artifacts_fts5_delete;
DROP TRIGGER IF EXISTS artifacts_fts5_update;
DROP TRIGGER IF EXISTS artifacts_fts5_insert;
DROP TRIGGER IF EXISTS messages_fts5_delete;
DROP TRIGGER IF EXISTS messages_fts5_update;
DROP TRIGGER IF EXISTS messages_fts5_insert;

-- Drop regular tables (reverse dependency order)
DROP TABLE IF EXISTS human_requests;
DROP TABLE IF EXISTS sql_result_metadata;
DROP TABLE IF EXISTS agent_errors;
DROP TABLE IF EXISTS artifacts;
DROP TABLE IF EXISTS memory_snapshots;
DROP TABLE IF EXISTS tool_executions;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS sessions;
-- NOTE: schema_migrations is NOT dropped here. It is an infrastructure table
-- managed by the migrator itself, not part of any individual migration.

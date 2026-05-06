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

-- 000012_skill_index.up.sql
-- Hierarchical (PageIndex-style) router tree over the skill library.
-- Built offline by pkg/skills/index.Builder and consumed by
-- pkg/skills/index.Router during discovery.
--
-- The index is library-global (not per-user) because skills are loaded
-- from LOOM_SKILLS_DIR. No RLS policy attached for that reason; if a
-- future change makes skills per-user we'll add policies in a follow-up
-- migration.

-- ============================================================================
-- SKILL_INDICES: top-level index metadata (one row per built tree)
-- ============================================================================

CREATE TABLE IF NOT EXISTS skill_indices (
    id              TEXT PRIMARY KEY,
    root_id         TEXT NOT NULL,
    built_at_ms     BIGINT NOT NULL DEFAULT 0,
    built_by_model  TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_skill_indices_root ON skill_indices(root_id);

-- ============================================================================
-- SKILL_INDEX_NODES
-- ============================================================================

CREATE TABLE IF NOT EXISTS skill_index_nodes (
    id              TEXT PRIMARY KEY,
    root_id         TEXT NOT NULL,
    title           TEXT NOT NULL DEFAULT '',
    summary         TEXT NOT NULL DEFAULT '',
    children_json   JSONB NOT NULL DEFAULT '[]'::jsonb,
    skill_refs_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    depth           INTEGER NOT NULL DEFAULT 0,
    labels_json     JSONB NOT NULL DEFAULT '{}'::jsonb,
    content_hash    TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_skill_index_nodes_root ON skill_index_nodes(root_id);
CREATE INDEX IF NOT EXISTS idx_skill_index_nodes_hash ON skill_index_nodes(content_hash);
CREATE INDEX IF NOT EXISTS idx_skill_index_nodes_depth ON skill_index_nodes(depth);

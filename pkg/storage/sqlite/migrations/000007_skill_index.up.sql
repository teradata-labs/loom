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

-- 000007_skill_index.up.sql
-- Hierarchical (PageIndex-style) router tree over the skill library.
-- Built offline (or in a goroutine on first boot) by pkg/skills/index.Builder
-- and consumed by pkg/skills/index.Router during discovery.
--
-- The index is library-global rather than per-user: skills are loaded from
-- LOOM_SKILLS_DIR (filesystem), so the tree over them shares that scope.

-- ============================================================================
-- SKILL_INDICES: top-level index metadata (one row per built tree)
-- ============================================================================

CREATE TABLE IF NOT EXISTS skill_indices (
    id              TEXT PRIMARY KEY,                      -- content-version hash
    root_id         TEXT NOT NULL,                         -- root node id
    built_at_ms     INTEGER NOT NULL DEFAULT 0,
    built_by_model  TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_skill_indices_root ON skill_indices(root_id);

-- ============================================================================
-- SKILL_INDEX_NODES: tree nodes referenced by SkillIndex.id via root_id
-- ============================================================================

CREATE TABLE IF NOT EXISTS skill_index_nodes (
    id              TEXT PRIMARY KEY,                      -- stable hash of path
    root_id         TEXT NOT NULL,                         -- index this node belongs to
    title           TEXT NOT NULL DEFAULT '',
    summary         TEXT NOT NULL DEFAULT '',              -- LLM-authored router hint
    children_json   TEXT NOT NULL DEFAULT '[]',            -- []string of child ids
    skill_refs_json TEXT NOT NULL DEFAULT '[]',            -- []string of skill names
    depth           INTEGER NOT NULL DEFAULT 0,
    labels_json     TEXT NOT NULL DEFAULT '{}',
    content_hash    TEXT NOT NULL DEFAULT '',              -- per-node hot-reload key
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_skill_index_nodes_root ON skill_index_nodes(root_id);
CREATE INDEX IF NOT EXISTS idx_skill_index_nodes_hash ON skill_index_nodes(content_hash);
CREATE INDEX IF NOT EXISTS idx_skill_index_nodes_depth ON skill_index_nodes(depth);

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

-- 000002_graph_memory.up.sql
-- Salience-driven graph-backed episodic memory.
-- Entities (mutable) represent current state.
-- Memories (immutable/append-only) represent historical record.
-- memory_entities junction table bridges graph and episodic memory (many-to-many).

-- ============================================================================
-- ENTITIES: Mutable nodes representing current state
-- ============================================================================

CREATE TABLE IF NOT EXISTS graph_entities (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    name TEXT NOT NULL,
    entity_type TEXT NOT NULL DEFAULT 'concept',
    properties_json TEXT NOT NULL DEFAULT '{}',
    owner TEXT,
    user_id TEXT NOT NULL DEFAULT 'default-user',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    deleted_at TEXT,
    UNIQUE(agent_id, name)
);

CREATE INDEX IF NOT EXISTS idx_graph_entities_agent ON graph_entities(agent_id);
CREATE INDEX IF NOT EXISTS idx_graph_entities_type ON graph_entities(agent_id, entity_type);
CREATE INDEX IF NOT EXISTS idx_graph_entities_owner ON graph_entities(owner);
CREATE INDEX IF NOT EXISTS idx_graph_entities_user ON graph_entities(user_id);

-- ============================================================================
-- EDGES: Mutable directed relationships
-- ============================================================================

CREATE TABLE IF NOT EXISTS graph_edges (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    source_id TEXT NOT NULL REFERENCES graph_entities(id) ON DELETE CASCADE,
    target_id TEXT NOT NULL REFERENCES graph_entities(id) ON DELETE CASCADE,
    relation TEXT NOT NULL,
    properties_json TEXT NOT NULL DEFAULT '{}',
    user_id TEXT NOT NULL DEFAULT 'default-user',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    deleted_at TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_graph_edges_unique ON graph_edges(source_id, target_id, relation);
CREATE INDEX IF NOT EXISTS idx_graph_edges_source ON graph_edges(source_id);
CREATE INDEX IF NOT EXISTS idx_graph_edges_target ON graph_edges(target_id);
CREATE INDEX IF NOT EXISTS idx_graph_edges_agent ON graph_edges(agent_id);
CREATE INDEX IF NOT EXISTS idx_graph_edges_user ON graph_edges(user_id);

-- ============================================================================
-- MEMORIES: Immutable episodic records (NO updated_at — deliberately)
-- ============================================================================

CREATE TABLE IF NOT EXISTS graph_memories (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    content TEXT NOT NULL,
    summary TEXT,
    memory_type TEXT NOT NULL DEFAULT 'fact',
    source TEXT,
    source_id TEXT,
    owner TEXT,
    memory_agent_id TEXT,
    tags TEXT NOT NULL DEFAULT '[]',
    salience REAL NOT NULL DEFAULT 0.5,
    token_count INTEGER NOT NULL DEFAULT 0,
    summary_token_count INTEGER NOT NULL DEFAULT 0,
    access_count INTEGER NOT NULL DEFAULT 0,
    properties_json TEXT NOT NULL DEFAULT '{}',
    user_id TEXT NOT NULL DEFAULT 'default-user',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    accessed_at TEXT,
    expires_at TEXT,
    deleted_at TEXT
);
-- Note: NO updated_at column. Memories are immutable.

CREATE INDEX IF NOT EXISTS idx_graph_memories_agent ON graph_memories(agent_id);
CREATE INDEX IF NOT EXISTS idx_graph_memories_type ON graph_memories(agent_id, memory_type);
CREATE INDEX IF NOT EXISTS idx_graph_memories_salience ON graph_memories(agent_id, salience DESC);
CREATE INDEX IF NOT EXISTS idx_graph_memories_owner ON graph_memories(owner);
CREATE INDEX IF NOT EXISTS idx_graph_memories_source ON graph_memories(source);
CREATE INDEX IF NOT EXISTS idx_graph_memories_created ON graph_memories(created_at);
CREATE INDEX IF NOT EXISTS idx_graph_memories_expires ON graph_memories(expires_at);
CREATE INDEX IF NOT EXISTS idx_graph_memories_user ON graph_memories(user_id);
CREATE INDEX IF NOT EXISTS idx_graph_memories_agent_created ON graph_memories(agent_id, created_at DESC);

-- ============================================================================
-- MEMORY-ENTITY JUNCTION: Many-to-many bridge between memories and entities
-- ============================================================================

CREATE TABLE IF NOT EXISTS graph_memory_entities (
    memory_id TEXT NOT NULL REFERENCES graph_memories(id) ON DELETE CASCADE,
    entity_id TEXT NOT NULL REFERENCES graph_entities(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'about',
    PRIMARY KEY (memory_id, entity_id, role)
);

CREATE INDEX IF NOT EXISTS idx_graph_memory_entities_entity ON graph_memory_entities(entity_id);
CREATE INDEX IF NOT EXISTS idx_graph_memory_entities_memory ON graph_memory_entities(memory_id);

-- ============================================================================
-- MEMORY LINEAGE: Supersession + consolidation chains
-- ============================================================================

CREATE TABLE IF NOT EXISTS graph_memory_lineage (
    new_memory_id TEXT NOT NULL REFERENCES graph_memories(id) ON DELETE CASCADE,
    old_memory_id TEXT NOT NULL REFERENCES graph_memories(id) ON DELETE CASCADE,
    relation_type TEXT NOT NULL DEFAULT 'SUPERSEDES',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (new_memory_id, old_memory_id)
);

CREATE INDEX IF NOT EXISTS idx_graph_memory_lineage_old ON graph_memory_lineage(old_memory_id);

-- ============================================================================
-- FTS5: Memory content search (requires -tags fts5 build tag)
-- ============================================================================

-- Memory content FTS (content-synced via triggers)
CREATE VIRTUAL TABLE IF NOT EXISTS graph_memories_fts USING fts5(
    memory_id UNINDEXED,
    content,
    summary,
    tags,
    tokenize='porter unicode61'
);

-- INSERT trigger only (memories are immutable, no UPDATE trigger needed)
CREATE TRIGGER IF NOT EXISTS graph_memories_fts_insert AFTER INSERT ON graph_memories
BEGIN
    INSERT INTO graph_memories_fts(memory_id, content, summary, tags)
    VALUES (NEW.id, NEW.content, NEW.summary, NEW.tags);
END;

CREATE TRIGGER IF NOT EXISTS graph_memories_fts_delete AFTER DELETE ON graph_memories
BEGIN
    DELETE FROM graph_memories_fts WHERE memory_id = OLD.id;
END;

-- ============================================================================
-- FTS5: Entity name search
-- ============================================================================

CREATE VIRTUAL TABLE IF NOT EXISTS graph_entities_fts USING fts5(
    entity_id UNINDEXED,
    name,
    entity_type,
    properties_json,
    tokenize='porter unicode61'
);

-- Entity FTS triggers: INSERT + DELETE + UPDATE (entities are mutable)
CREATE TRIGGER IF NOT EXISTS graph_entities_fts_insert AFTER INSERT ON graph_entities
BEGIN
    INSERT INTO graph_entities_fts(entity_id, name, entity_type, properties_json)
    VALUES (NEW.id, NEW.name, NEW.entity_type, NEW.properties_json);
END;

CREATE TRIGGER IF NOT EXISTS graph_entities_fts_update AFTER UPDATE ON graph_entities
BEGIN
    DELETE FROM graph_entities_fts WHERE entity_id = OLD.id;
    INSERT INTO graph_entities_fts(entity_id, name, entity_type, properties_json)
    VALUES (NEW.id, NEW.name, NEW.entity_type, NEW.properties_json);
END;

CREATE TRIGGER IF NOT EXISTS graph_entities_fts_delete AFTER DELETE ON graph_entities
BEGIN
    DELETE FROM graph_entities_fts WHERE entity_id = OLD.id;
END;

-- ============================================================================
-- Record this migration
-- ============================================================================

INSERT INTO schema_migrations (version, applied_at, description)
VALUES (2, strftime('%s', 'now'), 'graph_memory');

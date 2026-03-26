-- 000010_graph_memory.up.sql
-- Salience-driven graph-backed episodic memory for PostgreSQL.
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
    properties_json JSONB NOT NULL DEFAULT '{}',
    owner TEXT,
    user_id TEXT NOT NULL DEFAULT 'default-user',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    UNIQUE(agent_id, name)
);

CREATE INDEX IF NOT EXISTS idx_graph_entities_agent ON graph_entities(agent_id);
CREATE INDEX IF NOT EXISTS idx_graph_entities_type ON graph_entities(agent_id, entity_type);
CREATE INDEX IF NOT EXISTS idx_graph_entities_owner ON graph_entities(owner);
CREATE INDEX IF NOT EXISTS idx_graph_entities_user ON graph_entities(user_id);
CREATE INDEX IF NOT EXISTS idx_graph_entities_deleted ON graph_entities(deleted_at) WHERE deleted_at IS NOT NULL;

-- Entity FTS: generated tsvector + GIN index
ALTER TABLE graph_entities
    ADD COLUMN IF NOT EXISTS entity_search tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('english', COALESCE(name, '')), 'A') ||
        setweight(to_tsvector('english', COALESCE(entity_type, '')), 'B') ||
        setweight(to_tsvector('english', COALESCE(properties_json::text, '')), 'C')
    ) STORED;

CREATE INDEX IF NOT EXISTS idx_graph_entities_fts ON graph_entities USING GIN(entity_search);

-- ============================================================================
-- EDGES: Mutable directed relationships
-- ============================================================================

CREATE TABLE IF NOT EXISTS graph_edges (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    source_id TEXT NOT NULL REFERENCES graph_entities(id) ON DELETE CASCADE,
    target_id TEXT NOT NULL REFERENCES graph_entities(id) ON DELETE CASCADE,
    relation TEXT NOT NULL,
    properties_json JSONB NOT NULL DEFAULT '{}',
    user_id TEXT NOT NULL DEFAULT 'default-user',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    UNIQUE(source_id, target_id, relation)
);

CREATE INDEX IF NOT EXISTS idx_graph_edges_source ON graph_edges(source_id);
CREATE INDEX IF NOT EXISTS idx_graph_edges_target ON graph_edges(target_id);
CREATE INDEX IF NOT EXISTS idx_graph_edges_agent ON graph_edges(agent_id);
CREATE INDEX IF NOT EXISTS idx_graph_edges_user ON graph_edges(user_id);
CREATE INDEX IF NOT EXISTS idx_graph_edges_deleted ON graph_edges(deleted_at) WHERE deleted_at IS NOT NULL;

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
    tags JSONB NOT NULL DEFAULT '[]',
    salience NUMERIC(5,4) NOT NULL DEFAULT 0.5000,
    token_count INTEGER NOT NULL DEFAULT 0,
    summary_token_count INTEGER NOT NULL DEFAULT 0,
    access_count INTEGER NOT NULL DEFAULT 0,
    properties_json JSONB NOT NULL DEFAULT '{}',
    user_id TEXT NOT NULL DEFAULT 'default-user',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    accessed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ
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
CREATE INDEX IF NOT EXISTS idx_graph_memories_deleted ON graph_memories(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_graph_memories_agent_created ON graph_memories(agent_id, created_at DESC);

-- Memory FTS: generated tsvector + GIN index
ALTER TABLE graph_memories
    ADD COLUMN IF NOT EXISTS memory_search tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('english', COALESCE(content, '')), 'A') ||
        setweight(to_tsvector('english', COALESCE(summary, '')), 'B') ||
        setweight(to_tsvector('english', COALESCE(tags::text, '')), 'C')
    ) STORED;

CREATE INDEX IF NOT EXISTS idx_graph_memories_fts ON graph_memories USING GIN(memory_search);

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
    new_memory_id TEXT NOT NULL REFERENCES graph_memories(id),
    old_memory_id TEXT NOT NULL REFERENCES graph_memories(id),
    relation_type TEXT NOT NULL DEFAULT 'SUPERSEDES',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (new_memory_id, old_memory_id)
);

CREATE INDEX IF NOT EXISTS idx_graph_memory_lineage_old ON graph_memory_lineage(old_memory_id);

-- ============================================================================
-- RLS Policies (user-scoped, matching existing pattern from migration 000007)
-- ============================================================================

ALTER TABLE graph_entities ENABLE ROW LEVEL SECURITY;
ALTER TABLE graph_entities FORCE ROW LEVEL SECURITY;
CREATE POLICY graph_entities_user_isolation ON graph_entities
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

ALTER TABLE graph_edges ENABLE ROW LEVEL SECURITY;
ALTER TABLE graph_edges FORCE ROW LEVEL SECURITY;
CREATE POLICY graph_edges_user_isolation ON graph_edges
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

ALTER TABLE graph_memories ENABLE ROW LEVEL SECURITY;
ALTER TABLE graph_memories FORCE ROW LEVEL SECURITY;
CREATE POLICY graph_memories_user_isolation ON graph_memories
    USING (user_id = current_setting('app.current_user_id', true))
    WITH CHECK (user_id = current_setting('app.current_user_id', true));

-- Junction and lineage tables use FK-based policies (matching tool_executions pattern)
ALTER TABLE graph_memory_entities ENABLE ROW LEVEL SECURITY;
ALTER TABLE graph_memory_entities FORCE ROW LEVEL SECURITY;
CREATE POLICY graph_memory_entities_user_isolation ON graph_memory_entities
    USING (memory_id IN (
        SELECT id FROM graph_memories WHERE user_id = current_setting('app.current_user_id', true)
    ));

ALTER TABLE graph_memory_lineage ENABLE ROW LEVEL SECURITY;
ALTER TABLE graph_memory_lineage FORCE ROW LEVEL SECURITY;
CREATE POLICY graph_memory_lineage_user_isolation ON graph_memory_lineage
    USING (new_memory_id IN (
        SELECT id FROM graph_memories WHERE user_id = current_setting('app.current_user_id', true)
    ));

-- ============================================================================
-- Record this migration
-- ============================================================================

INSERT INTO schema_migrations (version, description) VALUES (10, 'graph memory')
ON CONFLICT (version) DO NOTHING;

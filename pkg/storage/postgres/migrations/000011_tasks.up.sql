-- 000011_tasks.up.sql
-- Persistent, dependency-aware task decomposition and kanban boards.

-- ============================================================================
-- TASK BOARDS
-- ============================================================================

CREATE TABLE IF NOT EXISTS task_boards (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    workflow_id TEXT,
    lanes_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    user_id TEXT NOT NULL DEFAULT 'default-user',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_task_boards_user ON task_boards(user_id);

ALTER TABLE task_boards ENABLE ROW LEVEL SECURITY;
CREATE POLICY task_boards_user_isolation ON task_boards
    USING (user_id = current_setting('app.current_user_id', true)
        OR current_setting('app.current_user_id', true) = ''
        OR current_setting('app.current_user_id', true) IS NULL);

-- ============================================================================
-- TASKS
-- ============================================================================

CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    objective TEXT NOT NULL DEFAULT '',
    approach TEXT NOT NULL DEFAULT '',
    acceptance_criteria TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    status INTEGER NOT NULL DEFAULT 1,
    priority INTEGER NOT NULL DEFAULT 3,
    category INTEGER NOT NULL DEFAULT 0,
    tags_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    owner_agent_id TEXT NOT NULL DEFAULT '',
    assignee_agent_id TEXT,
    claimed_by_session TEXT,
    parent_id TEXT,
    board_id TEXT REFERENCES task_boards(id) ON DELETE SET NULL,
    entity_ids_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    compaction_level INTEGER NOT NULL DEFAULT 0,
    compacted_summary TEXT NOT NULL DEFAULT '',
    output_policy_json JSONB,
    estimated_effort TEXT NOT NULL DEFAULT '',
    user_id TEXT NOT NULL DEFAULT 'default-user',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at TIMESTAMPTZ,
    closed_at TIMESTAMPTZ,
    close_reason TEXT NOT NULL DEFAULT '',
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_tasks_board ON tasks(board_id);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_priority ON tasks(priority);
CREATE INDEX IF NOT EXISTS idx_tasks_board_status ON tasks(board_id, status);
CREATE INDEX IF NOT EXISTS idx_tasks_assignee ON tasks(assignee_agent_id);
CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_id);
CREATE INDEX IF NOT EXISTS idx_tasks_owner ON tasks(owner_agent_id);
CREATE INDEX IF NOT EXISTS idx_tasks_user ON tasks(user_id);
CREATE INDEX IF NOT EXISTS idx_tasks_created ON tasks(created_at DESC);

ALTER TABLE tasks ENABLE ROW LEVEL SECURITY;
CREATE POLICY tasks_user_isolation ON tasks
    USING (user_id = current_setting('app.current_user_id', true)
        OR current_setting('app.current_user_id', true) = ''
        OR current_setting('app.current_user_id', true) IS NULL);

-- GIN index for full-text search (PostgreSQL tsvector, not FTS5)
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS search_vector tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(description, '')), 'B') ||
        setweight(to_tsvector('english', coalesce(objective, '')), 'B') ||
        setweight(to_tsvector('english', coalesce(notes, '')), 'C')
    ) STORED;

CREATE INDEX IF NOT EXISTS idx_tasks_fts ON tasks USING GIN (search_vector);

-- ============================================================================
-- TASK DEPENDENCIES
-- ============================================================================

CREATE TABLE IF NOT EXISTS task_dependencies (
    from_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    to_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    type INTEGER NOT NULL DEFAULT 1,
    created_by TEXT NOT NULL DEFAULT '',
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (from_task_id, to_task_id)
);

CREATE INDEX IF NOT EXISTS idx_task_deps_from ON task_dependencies(from_task_id);
CREATE INDEX IF NOT EXISTS idx_task_deps_to ON task_dependencies(to_task_id);

ALTER TABLE task_dependencies ENABLE ROW LEVEL SECURITY;
-- Dependencies inherit visibility from the tasks they connect.
CREATE POLICY task_deps_via_tasks ON task_dependencies
    USING (
        EXISTS (SELECT 1 FROM tasks WHERE tasks.id = task_dependencies.from_task_id
            AND (tasks.user_id = current_setting('app.current_user_id', true)
                OR current_setting('app.current_user_id', true) = ''
                OR current_setting('app.current_user_id', true) IS NULL))
    );

-- ============================================================================
-- TASK HISTORY
-- ============================================================================

CREATE TABLE IF NOT EXISTS task_history (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    action TEXT NOT NULL,
    old_status TEXT NOT NULL DEFAULT '',
    new_status TEXT NOT NULL DEFAULT '',
    agent_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    details_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_task_history_task ON task_history(task_id);
CREATE INDEX IF NOT EXISTS idx_task_history_created ON task_history(created_at DESC);

ALTER TABLE task_history ENABLE ROW LEVEL SECURITY;
CREATE POLICY task_history_via_tasks ON task_history
    USING (
        EXISTS (SELECT 1 FROM tasks WHERE tasks.id = task_history.task_id
            AND (tasks.user_id = current_setting('app.current_user_id', true)
                OR current_setting('app.current_user_id', true) = ''
                OR current_setting('app.current_user_id', true) IS NULL))
    );

-- ============================================================================
-- Record migration
-- ============================================================================

INSERT INTO schema_migrations (version, applied_at, description)
VALUES (11, EXTRACT(EPOCH FROM NOW())::bigint, 'tasks');

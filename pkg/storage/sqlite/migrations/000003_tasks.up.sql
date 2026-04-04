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

-- 000003_tasks.up.sql
-- Persistent, dependency-aware task decomposition and kanban boards.
-- Tasks are domain-agnostic units of cognitive work.

-- ============================================================================
-- TASK BOARDS: Kanban boards grouping tasks into lanes
-- ============================================================================

CREATE TABLE IF NOT EXISTS task_boards (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    workflow_id TEXT,
    lanes_json TEXT NOT NULL DEFAULT '[]',
    metadata_json TEXT NOT NULL DEFAULT '{}',
    user_id TEXT NOT NULL DEFAULT 'default-user',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    deleted_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_task_boards_user ON task_boards(user_id);

-- ============================================================================
-- TASKS: Domain-agnostic units of cognitive work
-- ============================================================================

CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    objective TEXT NOT NULL DEFAULT '',
    approach TEXT NOT NULL DEFAULT '',
    acceptance_criteria TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    status INTEGER NOT NULL DEFAULT 1,          -- TaskStatus enum (1=OPEN)
    priority INTEGER NOT NULL DEFAULT 3,        -- TaskPriority enum (3=MEDIUM)
    category INTEGER NOT NULL DEFAULT 0,        -- TaskCategory enum (0=UNSPECIFIED)
    tags_json TEXT NOT NULL DEFAULT '[]',
    owner_agent_id TEXT NOT NULL DEFAULT '',
    assignee_agent_id TEXT,
    claimed_by_session TEXT,
    parent_id TEXT,
    board_id TEXT REFERENCES task_boards(id) ON DELETE SET NULL,
    entity_ids_json TEXT NOT NULL DEFAULT '[]',
    metadata_json TEXT NOT NULL DEFAULT '{}',
    compaction_level INTEGER NOT NULL DEFAULT 0,
    compacted_summary TEXT NOT NULL DEFAULT '',
    output_policy_json TEXT,
    estimated_effort TEXT NOT NULL DEFAULT '',
    user_id TEXT NOT NULL DEFAULT 'default-user',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    claimed_at TEXT,
    closed_at TEXT,
    close_reason TEXT NOT NULL DEFAULT '',
    deleted_at TEXT
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

-- ============================================================================
-- TASK DEPENDENCIES: Directed edges in the task dependency graph (DAG)
-- ============================================================================

CREATE TABLE IF NOT EXISTS task_dependencies (
    from_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    to_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    type INTEGER NOT NULL DEFAULT 1,            -- TaskDependencyType enum (1=BLOCKS)
    created_by TEXT NOT NULL DEFAULT '',
    metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (from_task_id, to_task_id)
);

CREATE INDEX IF NOT EXISTS idx_task_deps_from ON task_dependencies(from_task_id);
CREATE INDEX IF NOT EXISTS idx_task_deps_to ON task_dependencies(to_task_id);

-- ============================================================================
-- TASK HISTORY: Audit trail for task lifecycle events
-- ============================================================================

CREATE TABLE IF NOT EXISTS task_history (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    action TEXT NOT NULL,                        -- created, claimed, released, transitioned, closed, updated, etc.
    old_status TEXT NOT NULL DEFAULT '',
    new_status TEXT NOT NULL DEFAULT '',
    agent_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    details_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_task_history_task ON task_history(task_id);
CREATE INDEX IF NOT EXISTS idx_task_history_created ON task_history(created_at DESC);

-- ============================================================================
-- FTS5: Task content search (requires -tags fts5 build tag)
-- ============================================================================

CREATE VIRTUAL TABLE IF NOT EXISTS tasks_fts USING fts5(
    task_id UNINDEXED,
    title,
    description,
    objective,
    approach,
    acceptance_criteria,
    notes,
    tags,
    tokenize='porter unicode61'
);

-- INSERT trigger
CREATE TRIGGER IF NOT EXISTS tasks_fts_insert AFTER INSERT ON tasks
BEGIN
    INSERT INTO tasks_fts(task_id, title, description, objective, approach, acceptance_criteria, notes, tags)
    VALUES (NEW.id, NEW.title, NEW.description, NEW.objective, NEW.approach, NEW.acceptance_criteria, NEW.notes, NEW.tags_json);
END;

-- UPDATE trigger: only re-index if task is not soft-deleted
CREATE TRIGGER IF NOT EXISTS tasks_fts_update AFTER UPDATE ON tasks
WHEN NEW.deleted_at IS NULL
BEGIN
    DELETE FROM tasks_fts WHERE task_id = OLD.id;
    INSERT INTO tasks_fts(task_id, title, description, objective, approach, acceptance_criteria, notes, tags)
    VALUES (NEW.id, NEW.title, NEW.description, NEW.objective, NEW.approach, NEW.acceptance_criteria, NEW.notes, NEW.tags_json);
END;

-- Soft-delete trigger: remove from FTS when task is soft-deleted
CREATE TRIGGER IF NOT EXISTS tasks_fts_soft_delete AFTER UPDATE ON tasks
WHEN NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL
BEGIN
    DELETE FROM tasks_fts WHERE task_id = OLD.id;
END;

-- Hard DELETE trigger
CREATE TRIGGER IF NOT EXISTS tasks_fts_delete AFTER DELETE ON tasks
BEGIN
    DELETE FROM tasks_fts WHERE task_id = OLD.id;
END;

-- ============================================================================
-- Record this migration
-- ============================================================================

INSERT INTO schema_migrations (version, applied_at, description)
VALUES (3, strftime('%s', 'now'), 'tasks');

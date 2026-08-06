// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package task provides persistent, dependency-aware task decomposition and
// kanban-style work management. Tasks are domain-agnostic units of cognitive
// work (research, analysis, writing, decisions, implementation, review, etc.).
package task

import (
	"context"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// CreatedBySessionMetadataKey is the task-metadata key recording the
// conversation session that created a task (agent tool create/decompose and
// the skill task emitter all stamp it). It is attribution, not a claim:
// claimed_by_session is only written by ClaimTask when a session starts
// working the task. Callers can scope "tasks created in this conversation"
// by matching this key without disturbing the ready → claim workflow.
const CreatedBySessionMetadataKey = "created_by_session"

// TaskStore defines the storage interface for task management.
// Implementations exist for SQLite and PostgreSQL.
type TaskStore interface {
	// Task CRUD
	CreateTask(ctx context.Context, task *Task) (*Task, error)
	GetTask(ctx context.Context, id string) (*Task, error)
	// GetTaskByIdempotencyKey returns the existing task with the given
	// SkillIdempotencyKey, or (nil, nil) when no such task exists. Empty
	// keys always return (nil, nil); they are not stored as a unique
	// constraint and lookups by empty key are meaningless.
	GetTaskByIdempotencyKey(ctx context.Context, key string) (*Task, error)
	// HasOpenSkillTasks returns true when at least one task with the given
	// (skill, session) prefix in skill_idempotency_key is still in flight
	// (status not DONE and not CANCELLED). Used by the skills orchestrator
	// to keep skills sticky while they have open work on the board.
	HasOpenSkillTasks(ctx context.Context, skillName, sessionID string) (bool, error)
	// ListBySkillRun returns every non-deleted task whose
	// skill_idempotency_key matches the (skill, session) prefix, regardless
	// of status. Used by the end-of-turn hygiene auditor to inventory the
	// active skill's tasks. Returns an empty slice (never nil) when no
	// tasks match. Empty skillName or sessionID returns an empty slice.
	ListBySkillRun(ctx context.Context, skillName, sessionID string) ([]*Task, error)
	UpdateTask(ctx context.Context, task *Task, fields []string) (*Task, error)
	DeleteTask(ctx context.Context, id string) error
	ListTasks(ctx context.Context, opts ListTasksOpts) ([]*Task, int, error)

	// Workflow operations
	ClaimTask(ctx context.Context, taskID, agentID, sessionID string) (*Task, error)
	ReleaseTask(ctx context.Context, taskID, sessionID string) (*Task, error)
	CloseTask(ctx context.Context, taskID, reason string) (*Task, error)
	TransitionTask(ctx context.Context, taskID string, newStatus loomv1.TaskStatus) (*Task, error)

	// Dependencies
	AddDependency(ctx context.Context, dep *TaskDependency) error
	RemoveDependency(ctx context.Context, fromTaskID, toTaskID string) error
	GetDependencies(ctx context.Context, taskID string) ([]*TaskDependency, error)
	GetDependents(ctx context.Context, taskID string) ([]*TaskDependency, error)
	GetReadyFront(ctx context.Context, boardID string, opts ReadyFrontOpts) ([]*Task, error)
	GetBlockedTasks(ctx context.Context, boardID string) ([]*Task, error)

	// Boards
	CreateBoard(ctx context.Context, board *TaskBoard) (*TaskBoard, error)
	GetBoard(ctx context.Context, id string) (*TaskBoard, error)
	ListBoards(ctx context.Context) ([]*TaskBoard, error)

	// History
	RecordHistory(ctx context.Context, entry *TaskHistoryEntry) error
	GetHistory(ctx context.Context, taskID string) ([]*TaskHistoryEntry, error)

	Close() error
}

// Task is a domain-agnostic unit of cognitive work.
type Task struct {
	ID                 string
	Title              string
	Description        string
	Objective          string
	Approach           string
	AcceptanceCriteria string
	Notes              string
	Status             loomv1.TaskStatus
	Priority           loomv1.TaskPriority
	Category           loomv1.TaskCategory
	Tags               []string
	OwnerAgentID       string
	AssigneeAgentID    string
	ClaimedBySession   string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ClaimedAt          *time.Time
	ClosedAt           *time.Time
	CloseReason        string
	ParentID           string
	ChildIDs           []string
	EntityIDs          []string
	Metadata           map[string]string
	BoardID            string
	CompactionLevel    int
	CompactedSummary   string
	OutputPolicy       *loomv1.OutputPolicy
	EstimatedEffort    string

	// SkillIdempotencyKey is the optional dedup key set by the skills task
	// emitter. Empty for tasks created by other paths. The persistence layer
	// enforces uniqueness on non-empty values via a partial unique index.
	SkillIdempotencyKey string
}

// TaskDependency is a directed edge in the task dependency graph.
type TaskDependency struct {
	FromTaskID string
	ToTaskID   string
	Type       loomv1.TaskDependencyType
	CreatedAt  time.Time
	CreatedBy  string
	Metadata   map[string]string
}

// TaskBoard is a kanban board that groups tasks into lanes.
type TaskBoard struct {
	ID         string
	Name       string
	WorkflowID string
	Lanes      []TaskLane
	Metadata   map[string]string
	CreatedAt  time.Time
}

// TaskLane is a column in a kanban board mapped to a task status.
type TaskLane struct {
	Name     string
	Status   loomv1.TaskStatus
	TaskIDs  []string
	WIPLimit int
}

// TaskHistoryEntry records an audit trail event for a task.
type TaskHistoryEntry struct {
	ID          string
	TaskID      string
	Action      string
	OldStatus   string
	NewStatus   string
	AgentID     string
	SessionID   string
	Timestamp   time.Time
	DetailsJSON string
}

// ListTasksOpts configures task list queries.
type ListTasksOpts struct {
	BoardID         string
	Status          loomv1.TaskStatus
	Priority        loomv1.TaskPriority
	Category        loomv1.TaskCategory
	AssigneeAgentID string
	ParentID        string
	Query           string // full-text search
	Limit           int
	Offset          int
}

// ReadyFrontOpts configures ready front queries.
type ReadyFrontOpts struct {
	AgentID     string
	MinPriority loomv1.TaskPriority
	MaxResults  int
}

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
	"errors"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// ErrAcceptanceCriteriaLocked is returned (wrapped) by SetAcceptanceCriteria
// when the task already has different, non-empty acceptance criteria.
// Criteria are write-once: they define "done", and silently moving the
// goalposts mid-task defeats their purpose.
var ErrAcceptanceCriteriaLocked = errors.New("acceptance criteria are write-once and already set")

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
	// SetAcceptanceCriteria atomically sets a task's write-once acceptance
	// criteria with the guard enforced in the store (single conditional
	// UPDATE, no read-then-write window). It succeeds when the criteria are
	// still empty or already equal to the given value (idempotent retries),
	// and returns an error wrapping ErrAcceptanceCriteriaLocked when
	// different non-empty criteria are already set. criteria must be
	// non-empty.
	SetAcceptanceCriteria(ctx context.Context, taskID, criteria string) (*Task, error)
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

	// CreatedVia records how the task came to exist: one of the CreatedVia*
	// constants in pkg/taskctx (user, agent, decompose, skill_template,
	// workflow, implicit). Empty on tasks predating the column.
	//
	// It is not only provenance. CreatedViaImplicit marks a task the runtime
	// minted so a turn's work has somewhere to hang, and those are excluded
	// from the agent's own task context — see ListTasksOpts.ExcludeCreatedVia.
	CreatedVia string
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

	// SessionID filters to a session's working set: tasks claimed by the
	// session (claimed_by_session) or created in it (the
	// CreatedBySessionMetadataKey metadata attribution). Empty = no filter.
	SessionID string

	// Statuses filters to tasks whose status is any of the given values.
	// Empty = no filter. When both Status and Statuses are set, both apply
	// (AND), which matches nothing unless Status is also listed.
	Statuses []loomv1.TaskStatus

	// NewestFirst orders results by created_at DESC instead of the default
	// (priority ASC, created_at ASC). Windowed consumers that must keep the
	// most recent tasks when the window truncates set this.
	NewestFirst bool
	// ExcludeCreatedVia omits tasks whose created_via matches any of these
	// values. Filtering happens in SQL, not after the fetch, because the
	// caller that needs it most — the agent's per-turn context build — reads
	// with a limit of 1000 and would otherwise pay for rows it discards.
	//
	// This is the mechanism that keeps runtime-minted tasks out of the prompt.
	ExcludeCreatedVia []string
}

// StatusCounter is an OPTIONAL capability a TaskStore may implement to answer
// status counts with a single aggregate query instead of a row scan.
//
// It is deliberately not part of TaskStore. TaskStore has implementations
// outside this repository — avmo-tera-cloud's UserScopedTaskStore asserts
// `var _ task.TaskStore` — so adding a method to that interface breaks every
// downstream implementer at their next version bump. Optional capabilities are
// how a framework adds a fast path without that cost; the standard library uses
// the same shape for io.ReaderFrom and http.Flusher.
//
// Manager.CountByStatus type-asserts for this and falls back to paging when a
// store does not provide it, so correctness never depends on it — only speed.
type StatusCounter interface {
	CountByStatus(ctx context.Context, opts CountByStatusOpts) (StatusCounts, error)
}

// CountByStatusOpts scopes a status aggregate.
type CountByStatusOpts struct {
	// BoardID restricts the count to one board. Empty counts every board.
	BoardID string

	// ExcludeCreatedVia omits tasks by creation source, so the counts an agent
	// sees match the tasks an agent sees. See ListTasksOpts.ExcludeCreatedVia.
	ExcludeCreatedVia []string
}

// StatusCounts holds per-status task counts.
//
// A struct rather than a map: it maps one-to-one onto loomv1.TaskBoardStats and
// onto the prompt's stats line, needs no allocation, and cannot carry a status
// the callers do not know how to render.
type StatusCounts struct {
	Total      int
	Open       int
	InProgress int
	Blocked    int
	Done       int
	Deferred   int
	Cancelled  int
}

// Add records n tasks in the given status. Unknown statuses count toward Total
// only, so a status added to the enum later inflates no specific bucket.
func (c *StatusCounts) Add(status loomv1.TaskStatus, n int) {
	c.Total += n
	switch status {
	case loomv1.TaskStatus_TASK_STATUS_OPEN:
		c.Open += n
	case loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS:
		c.InProgress += n
	case loomv1.TaskStatus_TASK_STATUS_BLOCKED:
		c.Blocked += n
	case loomv1.TaskStatus_TASK_STATUS_DONE:
		c.Done += n
	case loomv1.TaskStatus_TASK_STATUS_DEFERRED:
		c.Deferred += n
	case loomv1.TaskStatus_TASK_STATUS_CANCELLED:
		c.Cancelled += n
	}
}

// ReadyFrontOpts configures ready front queries.
type ReadyFrontOpts struct {
	AgentID     string
	MinPriority loomv1.TaskPriority
	MaxResults  int

	// ExcludeCreatedVia omits tasks by creation source. See
	// ListTasksOpts.ExcludeCreatedVia.
	ExcludeCreatedVia []string
}

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

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/task"
)

// TaskStore implements task.TaskStore for SQLite.
type TaskStore struct {
	db     *sql.DB
	tracer observability.Tracer
}

// NewTaskStore creates a new SQLite-backed task store.
func NewTaskStore(db *sql.DB, tracer observability.Tracer) *TaskStore {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &TaskStore{db: db, tracer: tracer}
}

// Compile-time interface check.
var _ task.TaskStore = (*TaskStore)(nil)

// =============================================================================
// Task CRUD
// =============================================================================

func (s *TaskStore) CreateTask(ctx context.Context, t *task.Task) (*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.create")
	defer s.tracer.EndSpan(span)

	if t.ID == "" {
		t.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	t.CreatedAt = now
	t.UpdatedAt = now

	tagsJSON, _ := json.Marshal(t.Tags)
	entityIDsJSON, _ := json.Marshal(t.EntityIDs)
	metadataJSON, _ := json.Marshal(t.Metadata)

	var outputPolicyJSON *string
	if t.OutputPolicy != nil {
		b, _ := json.Marshal(t.OutputPolicy)
		s := string(b)
		outputPolicyJSON = &s
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tasks (
			id, title, description, objective, approach, acceptance_criteria, notes,
			status, priority, category, tags_json,
			owner_agent_id, assignee_agent_id, claimed_by_session,
			parent_id, board_id, entity_ids_json, metadata_json,
			compaction_level, compacted_summary, output_policy_json, estimated_effort,
			created_at, updated_at, close_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime(?), datetime(?), ?)`,
		t.ID, t.Title, t.Description, t.Objective, t.Approach, t.AcceptanceCriteria, t.Notes,
		int32(t.Status), int32(t.Priority), int32(t.Category), string(tagsJSON),
		t.OwnerAgentID, nilIfEmpty(t.AssigneeAgentID), nilIfEmpty(t.ClaimedBySession),
		nilIfEmpty(t.ParentID), nilIfEmpty(t.BoardID), string(entityIDsJSON), string(metadataJSON),
		t.CompactionLevel, t.CompactedSummary, outputPolicyJSON, t.EstimatedEffort,
		now.Format(time.RFC3339), now.Format(time.RFC3339), t.CloseReason,
	)
	if err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	return t, nil
}

func (s *TaskStore) GetTask(ctx context.Context, id string) (*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.get")
	defer s.tracer.EndSpan(span)

	row := s.db.QueryRowContext(ctx, `
		SELECT id, title, description, objective, approach, acceptance_criteria, notes,
			status, priority, category, tags_json,
			owner_agent_id, COALESCE(assignee_agent_id,''), COALESCE(claimed_by_session,''),
			COALESCE(parent_id,''), COALESCE(board_id,''), entity_ids_json, metadata_json,
			compaction_level, compacted_summary, output_policy_json, estimated_effort,
			created_at, updated_at, claimed_at, closed_at, close_reason
		FROM tasks WHERE id = ? AND deleted_at IS NULL`, id)

	return scanTask(row)
}

func (s *TaskStore) UpdateTask(ctx context.Context, t *task.Task, _ []string) (*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.update")
	defer s.tracer.EndSpan(span)

	now := time.Now().UTC()
	t.UpdatedAt = now

	tagsJSON, _ := json.Marshal(t.Tags)
	entityIDsJSON, _ := json.Marshal(t.EntityIDs)
	metadataJSON, _ := json.Marshal(t.Metadata)

	var outputPolicyJSON *string
	if t.OutputPolicy != nil {
		b, _ := json.Marshal(t.OutputPolicy)
		s := string(b)
		outputPolicyJSON = &s
	}

	result, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET
			title = ?, description = ?, objective = ?, approach = ?,
			acceptance_criteria = ?, notes = ?,
			status = ?, priority = ?, category = ?, tags_json = ?,
			owner_agent_id = ?, assignee_agent_id = ?, claimed_by_session = ?,
			parent_id = ?, board_id = ?,
			entity_ids_json = ?, metadata_json = ?,
			compaction_level = ?, compacted_summary = ?,
			output_policy_json = ?, estimated_effort = ?,
			close_reason = ?, claimed_at = ?, closed_at = ?,
			updated_at = datetime(?)
		WHERE id = ? AND deleted_at IS NULL`,
		t.Title, t.Description, t.Objective, t.Approach,
		t.AcceptanceCriteria, t.Notes,
		int32(t.Status), int32(t.Priority), int32(t.Category), string(tagsJSON),
		t.OwnerAgentID, nilIfEmpty(t.AssigneeAgentID), nilIfEmpty(t.ClaimedBySession),
		nilIfEmpty(t.ParentID), nilIfEmpty(t.BoardID),
		string(entityIDsJSON), string(metadataJSON),
		t.CompactionLevel, t.CompactedSummary,
		outputPolicyJSON, t.EstimatedEffort,
		t.CloseReason, formatNullableTime(t.ClaimedAt), formatNullableTime(t.ClosedAt),
		now.Format(time.RFC3339), t.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update task: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, fmt.Errorf("task not found: %s", t.ID)
	}
	return t, nil
}

func (s *TaskStore) DeleteTask(ctx context.Context, id string) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.delete")
	defer s.tracer.EndSpan(span)

	result, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET deleted_at = datetime('now') WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("task %s not found or already deleted", id)
	}
	return nil
}

func (s *TaskStore) ListTasks(ctx context.Context, opts task.ListTasksOpts) ([]*task.Task, int, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.list")
	defer s.tracer.EndSpan(span)

	var conditions []string
	var args []interface{}

	conditions = append(conditions, "t.deleted_at IS NULL")

	if opts.BoardID != "" {
		conditions = append(conditions, "t.board_id = ?")
		args = append(args, opts.BoardID)
	}
	if opts.Status != loomv1.TaskStatus_TASK_STATUS_UNSPECIFIED {
		conditions = append(conditions, "t.status = ?")
		args = append(args, int32(opts.Status))
	}
	if opts.Priority != loomv1.TaskPriority_TASK_PRIORITY_UNSPECIFIED {
		conditions = append(conditions, "t.priority = ?")
		args = append(args, int32(opts.Priority))
	}
	if opts.Category != loomv1.TaskCategory_TASK_CATEGORY_UNSPECIFIED {
		conditions = append(conditions, "t.category = ?")
		args = append(args, int32(opts.Category))
	}
	if opts.AssigneeAgentID != "" {
		conditions = append(conditions, "t.assignee_agent_id = ?")
		args = append(args, opts.AssigneeAgentID)
	}
	if opts.ParentID != "" {
		conditions = append(conditions, "t.parent_id = ?")
		args = append(args, opts.ParentID)
	}

	where := strings.Join(conditions, " AND ")

	// FTS query if provided
	if opts.Query != "" {
		where += " AND t.id IN (SELECT task_id FROM tasks_fts WHERE tasks_fts MATCH ?)"
		args = append(args, opts.Query)
	}

	// Count total
	var total int
	countArgs := make([]interface{}, len(args))
	copy(countArgs, args)
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tasks t WHERE "+where, countArgs...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("count tasks: %w", err)
	}

	// Fetch page — dynamic WHERE built from validated enum values; all user data via ? params
	query := `SELECT id, title, description, objective, approach, acceptance_criteria, notes,
			status, priority, category, tags_json,
			owner_agent_id, COALESCE(assignee_agent_id,''), COALESCE(claimed_by_session,''),
			COALESCE(parent_id,''), COALESCE(board_id,''), entity_ids_json, metadata_json,
			compaction_level, compacted_summary, output_policy_json, estimated_effort,
			created_at, updated_at, claimed_at, closed_at, close_reason
		FROM tasks t WHERE ` + where + // #nosec G202 -- where is built from validated enum conditions with ? placeholders
		` ORDER BY t.priority ASC, t.created_at ASC`

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, opts.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var tasks []*task.Task
	for rows.Next() {
		t, err := scanTaskRows(rows)
		if err != nil {
			return nil, 0, err
		}
		tasks = append(tasks, t)
	}
	return tasks, total, rows.Err()
}

// =============================================================================
// Workflow Operations
// =============================================================================

func (s *TaskStore) ClaimTask(ctx context.Context, taskID, agentID, sessionID string) (*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.claim")
	defer s.tracer.EndSpan(span)

	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET
			assignee_agent_id = ?, claimed_by_session = ?,
			status = ?, claimed_at = datetime(?), updated_at = datetime(?)
		WHERE id = ? AND status = ? AND claimed_by_session IS NULL AND deleted_at IS NULL`,
		agentID, sessionID,
		int32(loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS),
		now.Format(time.RFC3339), now.Format(time.RFC3339),
		taskID, int32(loomv1.TaskStatus_TASK_STATUS_OPEN),
	)
	if err != nil {
		return nil, fmt.Errorf("claim task: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, fmt.Errorf("task %s cannot be claimed (not OPEN or already claimed)", taskID)
	}
	return s.GetTask(ctx, taskID)
}

func (s *TaskStore) ReleaseTask(ctx context.Context, taskID, sessionID string) (*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.release")
	defer s.tracer.EndSpan(span)

	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET
			assignee_agent_id = NULL, claimed_by_session = NULL,
			status = ?, claimed_at = NULL, updated_at = datetime(?)
		WHERE id = ? AND claimed_by_session = ? AND deleted_at IS NULL`,
		int32(loomv1.TaskStatus_TASK_STATUS_OPEN),
		now.Format(time.RFC3339),
		taskID, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("release task: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, fmt.Errorf("task %s not claimed by session %s", taskID, sessionID)
	}
	return s.GetTask(ctx, taskID)
}

func (s *TaskStore) CloseTask(ctx context.Context, taskID, reason string) (*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.close")
	defer s.tracer.EndSpan(span)

	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET
			status = ?, close_reason = ?, closed_at = datetime(?),
			assignee_agent_id = NULL, claimed_by_session = NULL,
			updated_at = datetime(?)
		WHERE id = ? AND deleted_at IS NULL`,
		int32(loomv1.TaskStatus_TASK_STATUS_DONE), reason,
		now.Format(time.RFC3339), now.Format(time.RFC3339), taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("close task: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, fmt.Errorf("task %s not found or already deleted", taskID)
	}
	return s.GetTask(ctx, taskID)
}

func (s *TaskStore) TransitionTask(ctx context.Context, taskID string, newStatus loomv1.TaskStatus) (*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.transition")
	defer s.tracer.EndSpan(span)

	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET status = ?, updated_at = datetime(?)
		WHERE id = ? AND deleted_at IS NULL`,
		int32(newStatus), now.Format(time.RFC3339), taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("transition task: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, fmt.Errorf("task %s not found or already deleted", taskID)
	}
	return s.GetTask(ctx, taskID)
}

// =============================================================================
// Dependencies
// =============================================================================

func (s *TaskStore) AddDependency(ctx context.Context, dep *task.TaskDependency) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.add_dependency")
	defer s.tracer.EndSpan(span)

	now := time.Now().UTC()
	dep.CreatedAt = now
	metadataJSON, _ := json.Marshal(dep.Metadata)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO task_dependencies (from_task_id, to_task_id, type, created_by, metadata_json, created_at)
		VALUES (?, ?, ?, ?, ?, datetime(?))`,
		dep.FromTaskID, dep.ToTaskID, int32(dep.Type), dep.CreatedBy,
		string(metadataJSON), now.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("add dependency: %w", err)
	}
	return nil
}

func (s *TaskStore) RemoveDependency(ctx context.Context, fromTaskID, toTaskID string) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.remove_dependency")
	defer s.tracer.EndSpan(span)

	_, err := s.db.ExecContext(ctx,
		`DELETE FROM task_dependencies WHERE from_task_id = ? AND to_task_id = ?`,
		fromTaskID, toTaskID)
	return err
}

func (s *TaskStore) GetDependencies(ctx context.Context, taskID string) ([]*task.TaskDependency, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.get_dependencies")
	defer s.tracer.EndSpan(span)

	rows, err := s.db.QueryContext(ctx, `
		SELECT from_task_id, to_task_id, type, created_by, metadata_json, created_at
		FROM task_dependencies WHERE from_task_id = ?`, taskID)
	if err != nil {
		return nil, fmt.Errorf("get dependencies: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	return scanDependencies(rows)
}

func (s *TaskStore) GetDependents(ctx context.Context, taskID string) ([]*task.TaskDependency, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.get_dependents")
	defer s.tracer.EndSpan(span)

	rows, err := s.db.QueryContext(ctx, `
		SELECT from_task_id, to_task_id, type, created_by, metadata_json, created_at
		FROM task_dependencies WHERE to_task_id = ?`, taskID)
	if err != nil {
		return nil, fmt.Errorf("get dependents: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	return scanDependencies(rows)
}

func (s *TaskStore) GetReadyFront(ctx context.Context, boardID string, opts task.ReadyFrontOpts) ([]*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.get_ready_front")
	defer s.tracer.EndSpan(span)

	var conditions []string
	var args []interface{}

	conditions = append(conditions, "t.status = ?")
	args = append(args, int32(loomv1.TaskStatus_TASK_STATUS_OPEN))
	conditions = append(conditions, "t.deleted_at IS NULL")

	if boardID != "" {
		conditions = append(conditions, "t.board_id = ?")
		args = append(args, boardID)
	}

	if opts.MinPriority != loomv1.TaskPriority_TASK_PRIORITY_UNSPECIFIED {
		conditions = append(conditions, "t.priority <= ?")
		args = append(args, int32(opts.MinPriority))
	}

	// Exclude tasks that have unfinished blocking dependencies.
	conditions = append(conditions, `NOT EXISTS (
		SELECT 1 FROM task_dependencies d
		JOIN tasks blocker ON d.to_task_id = blocker.id
		WHERE d.from_task_id = t.id
			AND d.type = ?
			AND blocker.status NOT IN (?, ?, ?)
			AND blocker.deleted_at IS NULL
	)`)
	args = append(args,
		int32(loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_BLOCKS),
		int32(loomv1.TaskStatus_TASK_STATUS_DONE),
		int32(loomv1.TaskStatus_TASK_STATUS_DEFERRED),
		int32(loomv1.TaskStatus_TASK_STATUS_CANCELLED),
	)

	where := strings.Join(conditions, " AND ")
	limit := opts.MaxResults
	if limit <= 0 {
		limit = 20
	}

	// #nosec G201 -- where/limit are built from validated enum values with ? placeholders
	query := fmt.Sprintf(`SELECT id, title, description, objective, approach, acceptance_criteria, notes,
			status, priority, category, tags_json,
			owner_agent_id, COALESCE(assignee_agent_id,''), COALESCE(claimed_by_session,''),
			COALESCE(parent_id,''), COALESCE(board_id,''), entity_ids_json, metadata_json,
			compaction_level, compacted_summary, output_policy_json, estimated_effort,
			created_at, updated_at, claimed_at, closed_at, close_reason
		FROM tasks t WHERE %s
		ORDER BY t.priority ASC, t.created_at ASC
		LIMIT %d`, where, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get ready front: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var tasks []*task.Task
	for rows.Next() {
		t, err := scanTaskRows(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *TaskStore) GetBlockedTasks(ctx context.Context, boardID string) ([]*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.get_blocked")
	defer s.tracer.EndSpan(span)

	var args []interface{}
	boardFilter := ""
	if boardID != "" {
		boardFilter = "AND t.board_id = ?"
		args = append(args, boardID)
	}

	query := fmt.Sprintf(`
		SELECT id, title, description, objective, approach, acceptance_criteria, notes,
			status, priority, category, tags_json,
			owner_agent_id, COALESCE(assignee_agent_id,''), COALESCE(claimed_by_session,''),
			COALESCE(parent_id,''), COALESCE(board_id,''), entity_ids_json, metadata_json,
			compaction_level, compacted_summary, output_policy_json, estimated_effort,
			created_at, updated_at, claimed_at, closed_at, close_reason
		FROM tasks t WHERE t.status = ? AND t.deleted_at IS NULL %s
		ORDER BY t.priority ASC, t.created_at ASC`, boardFilter)

	args = append([]interface{}{int32(loomv1.TaskStatus_TASK_STATUS_BLOCKED)}, args...)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get blocked tasks: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var tasks []*task.Task
	for rows.Next() {
		t, err := scanTaskRows(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// =============================================================================
// Boards
// =============================================================================

func (s *TaskStore) CreateBoard(ctx context.Context, board *task.TaskBoard) (*task.TaskBoard, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.create_board")
	defer s.tracer.EndSpan(span)

	if board.ID == "" {
		board.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	board.CreatedAt = now

	lanesJSON, _ := json.Marshal(board.Lanes)
	metadataJSON, _ := json.Marshal(board.Metadata)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO task_boards (id, name, workflow_id, lanes_json, metadata_json, created_at)
		VALUES (?, ?, ?, ?, ?, datetime(?))`,
		board.ID, board.Name, nilIfEmpty(board.WorkflowID),
		string(lanesJSON), string(metadataJSON), now.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("create board: %w", err)
	}
	return board, nil
}

func (s *TaskStore) GetBoard(ctx context.Context, id string) (*task.TaskBoard, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.get_board")
	defer s.tracer.EndSpan(span)

	var board task.TaskBoard
	var lanesJSON, metadataJSON string
	var workflowID sql.NullString
	var createdAtStr string

	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, workflow_id, lanes_json, metadata_json, created_at
		FROM task_boards WHERE id = ? AND deleted_at IS NULL`, id).
		Scan(&board.ID, &board.Name, &workflowID, &lanesJSON, &metadataJSON, &createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("get board: %w", err)
	}

	board.WorkflowID = workflowID.String
	_ = json.Unmarshal([]byte(lanesJSON), &board.Lanes)
	_ = json.Unmarshal([]byte(metadataJSON), &board.Metadata)
	board.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
	// Also parse "2006-01-02 15:04:05" format used by SQLite datetime().
	if board.CreatedAt.IsZero() {
		board.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAtStr)
	}

	return &board, nil
}

func (s *TaskStore) ListBoards(ctx context.Context) ([]*task.TaskBoard, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.list_boards")
	defer s.tracer.EndSpan(span)

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, workflow_id, lanes_json, metadata_json, created_at
		FROM task_boards WHERE deleted_at IS NULL ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list boards: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var boards []*task.TaskBoard
	for rows.Next() {
		var board task.TaskBoard
		var lanesJSON, metadataJSON, createdAtStr string
		var workflowID sql.NullString
		if err := rows.Scan(&board.ID, &board.Name, &workflowID, &lanesJSON, &metadataJSON, &createdAtStr); err != nil {
			return nil, fmt.Errorf("scan board: %w", err)
		}
		board.WorkflowID = workflowID.String
		_ = json.Unmarshal([]byte(lanesJSON), &board.Lanes)
		_ = json.Unmarshal([]byte(metadataJSON), &board.Metadata)
		board.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		if board.CreatedAt.IsZero() {
			board.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAtStr)
		}
		boards = append(boards, &board)
	}
	return boards, rows.Err()
}

// =============================================================================
// History
// =============================================================================

func (s *TaskStore) RecordHistory(ctx context.Context, entry *task.TaskHistoryEntry) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.record_history")
	defer s.tracer.EndSpan(span)

	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	entry.Timestamp = time.Now().UTC()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO task_history (id, task_id, action, old_status, new_status, agent_id, session_id, details_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, datetime(?))`,
		entry.ID, entry.TaskID, entry.Action, entry.OldStatus, entry.NewStatus,
		entry.AgentID, entry.SessionID, entry.DetailsJSON, entry.Timestamp.Format(time.RFC3339),
	)
	return err
}

func (s *TaskStore) GetHistory(ctx context.Context, taskID string) ([]*task.TaskHistoryEntry, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.task.get_history")
	defer s.tracer.EndSpan(span)

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, task_id, action, old_status, new_status, agent_id, session_id, details_json, created_at
		FROM task_history WHERE task_id = ? ORDER BY created_at ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("get history: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var entries []*task.TaskHistoryEntry
	for rows.Next() {
		var e task.TaskHistoryEntry
		var createdAtStr string
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Action, &e.OldStatus, &e.NewStatus,
			&e.AgentID, &e.SessionID, &e.DetailsJSON, &createdAtStr); err != nil {
			return nil, fmt.Errorf("scan history: %w", err)
		}
		e.Timestamp, _ = time.Parse(time.RFC3339, createdAtStr)
		if e.Timestamp.IsZero() {
			e.Timestamp, _ = time.Parse("2006-01-02 15:04:05", createdAtStr)
		}
		entries = append(entries, &e)
	}
	return entries, rows.Err()
}

// Close closes the underlying database connection.
func (s *TaskStore) Close() error {
	return nil // Connection owned by SQLiteBackend, not us.
}

// =============================================================================
// Helpers
// =============================================================================

func scanTask(row *sql.Row) (*task.Task, error) {
	var t task.Task
	var status, priority, category int32
	var tagsJSON, entityIDsJSON, metadataJSON string
	var outputPolicyJSON sql.NullString
	var createdAtStr, updatedAtStr string
	var claimedAtStr, closedAtStr sql.NullString

	err := row.Scan(
		&t.ID, &t.Title, &t.Description, &t.Objective, &t.Approach,
		&t.AcceptanceCriteria, &t.Notes,
		&status, &priority, &category, &tagsJSON,
		&t.OwnerAgentID, &t.AssigneeAgentID, &t.ClaimedBySession,
		&t.ParentID, &t.BoardID, &entityIDsJSON, &metadataJSON,
		&t.CompactionLevel, &t.CompactedSummary, &outputPolicyJSON, &t.EstimatedEffort,
		&createdAtStr, &updatedAtStr, &claimedAtStr, &closedAtStr, &t.CloseReason,
	)
	if err != nil {
		return nil, fmt.Errorf("scan task: %w", err)
	}

	t.Status = loomv1.TaskStatus(status)
	t.Priority = loomv1.TaskPriority(priority)
	t.Category = loomv1.TaskCategory(category)
	_ = json.Unmarshal([]byte(tagsJSON), &t.Tags)
	_ = json.Unmarshal([]byte(entityIDsJSON), &t.EntityIDs)
	_ = json.Unmarshal([]byte(metadataJSON), &t.Metadata)

	if outputPolicyJSON.Valid {
		t.OutputPolicy = &loomv1.OutputPolicy{}
		_ = json.Unmarshal([]byte(outputPolicyJSON.String), t.OutputPolicy)
	}

	t.CreatedAt = parseTaskTime(createdAtStr)
	t.UpdatedAt = parseTaskTime(updatedAtStr)
	if claimedAtStr.Valid {
		ct := parseTaskTime(claimedAtStr.String)
		t.ClaimedAt = &ct
	}
	if closedAtStr.Valid {
		ct := parseTaskTime(closedAtStr.String)
		t.ClosedAt = &ct
	}

	return &t, nil
}

func scanTaskRows(rows *sql.Rows) (*task.Task, error) {
	var t task.Task
	var status, priority, category int32
	var tagsJSON, entityIDsJSON, metadataJSON string
	var outputPolicyJSON sql.NullString
	var createdAtStr, updatedAtStr string
	var claimedAtStr, closedAtStr sql.NullString

	err := rows.Scan(
		&t.ID, &t.Title, &t.Description, &t.Objective, &t.Approach,
		&t.AcceptanceCriteria, &t.Notes,
		&status, &priority, &category, &tagsJSON,
		&t.OwnerAgentID, &t.AssigneeAgentID, &t.ClaimedBySession,
		&t.ParentID, &t.BoardID, &entityIDsJSON, &metadataJSON,
		&t.CompactionLevel, &t.CompactedSummary, &outputPolicyJSON, &t.EstimatedEffort,
		&createdAtStr, &updatedAtStr, &claimedAtStr, &closedAtStr, &t.CloseReason,
	)
	if err != nil {
		return nil, fmt.Errorf("scan task: %w", err)
	}

	t.Status = loomv1.TaskStatus(status)
	t.Priority = loomv1.TaskPriority(priority)
	t.Category = loomv1.TaskCategory(category)
	_ = json.Unmarshal([]byte(tagsJSON), &t.Tags)
	_ = json.Unmarshal([]byte(entityIDsJSON), &t.EntityIDs)
	_ = json.Unmarshal([]byte(metadataJSON), &t.Metadata)

	if outputPolicyJSON.Valid {
		t.OutputPolicy = &loomv1.OutputPolicy{}
		_ = json.Unmarshal([]byte(outputPolicyJSON.String), t.OutputPolicy)
	}

	t.CreatedAt = parseTaskTime(createdAtStr)
	t.UpdatedAt = parseTaskTime(updatedAtStr)
	if claimedAtStr.Valid {
		ct := parseTaskTime(claimedAtStr.String)
		t.ClaimedAt = &ct
	}
	if closedAtStr.Valid {
		ct := parseTaskTime(closedAtStr.String)
		t.ClosedAt = &ct
	}

	return &t, nil
}

func scanDependencies(rows *sql.Rows) ([]*task.TaskDependency, error) {
	var deps []*task.TaskDependency
	for rows.Next() {
		var d task.TaskDependency
		var depType int32
		var metadataJSON, createdAtStr string
		if err := rows.Scan(&d.FromTaskID, &d.ToTaskID, &depType, &d.CreatedBy, &metadataJSON, &createdAtStr); err != nil {
			return nil, fmt.Errorf("scan dependency: %w", err)
		}
		d.Type = loomv1.TaskDependencyType(depType)
		_ = json.Unmarshal([]byte(metadataJSON), &d.Metadata)
		d.CreatedAt = parseTaskTime(createdAtStr)
		deps = append(deps, &d)
	}
	return deps, rows.Err()
}

func parseTaskTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// Fall back to SQLite datetime() format.
		t, _ = time.Parse("2006-01-02 15:04:05", s)
	}
	return t
}

func formatNullableTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339)
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

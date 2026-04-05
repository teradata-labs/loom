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
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/task"
)

// TaskStore implements task.TaskStore using PostgreSQL.
type TaskStore struct {
	pool   *pgxpool.Pool
	tracer observability.Tracer
}

// NewTaskStore creates a new PostgreSQL-backed task store.
func NewTaskStore(pool *pgxpool.Pool, tracer observability.Tracer) *TaskStore {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &TaskStore{pool: pool, tracer: tracer}
}

// Compile-time interface check.
var _ task.TaskStore = (*TaskStore)(nil)

// taskColumns is the standard SELECT list for tasks.
const taskColumns = `id, title, description, objective, approach, acceptance_criteria, notes,
	status, priority, category, tags_json,
	owner_agent_id, COALESCE(assignee_agent_id,''), COALESCE(claimed_by_session,''),
	COALESCE(parent_id,''), COALESCE(board_id,''), entity_ids_json, metadata_json,
	compaction_level, compacted_summary, output_policy_json, estimated_effort,
	created_at, updated_at, claimed_at, closed_at, close_reason`

// =============================================================================
// Task CRUD
// =============================================================================

func (s *TaskStore) CreateTask(ctx context.Context, t *task.Task) (*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.create")
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

	var outputPolicyJSON []byte
	if t.OutputPolicy != nil {
		outputPolicyJSON, _ = json.Marshal(t.OutputPolicy)
	}

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		_, err := tx.Exec(ctx, `
			INSERT INTO tasks (
				id, title, description, objective, approach, acceptance_criteria, notes,
				status, priority, category, tags_json,
				owner_agent_id, assignee_agent_id, claimed_by_session,
				parent_id, board_id, entity_ids_json, metadata_json,
				compaction_level, compacted_summary, output_policy_json, estimated_effort,
				user_id, created_at, updated_at, close_reason
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)`,
			t.ID, t.Title, t.Description, t.Objective, t.Approach, t.AcceptanceCriteria, t.Notes,
			int32(t.Status), int32(t.Priority), int32(t.Category), tagsJSON,
			t.OwnerAgentID, nilIfEmpty(t.AssigneeAgentID), nilIfEmpty(t.ClaimedBySession),
			nilIfEmpty(t.ParentID), nilIfEmpty(t.BoardID), entityIDsJSON, metadataJSON,
			t.CompactionLevel, t.CompactedSummary, outputPolicyJSON, t.EstimatedEffort,
			userID, now, now, t.CloseReason,
		)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	return t, nil
}

func (s *TaskStore) GetTask(ctx context.Context, id string) (*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.get")
	defer s.tracer.EndSpan(span)

	var result *task.Task
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = $1 AND deleted_at IS NULL`, id)
		var err error
		result, err = pgScanTask(row)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *TaskStore) UpdateTask(ctx context.Context, t *task.Task, _ []string) (*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.update")
	defer s.tracer.EndSpan(span)

	now := time.Now().UTC()
	t.UpdatedAt = now

	tagsJSON, _ := json.Marshal(t.Tags)
	entityIDsJSON, _ := json.Marshal(t.EntityIDs)
	metadataJSON, _ := json.Marshal(t.Metadata)

	var outputPolicyJSON []byte
	if t.OutputPolicy != nil {
		outputPolicyJSON, _ = json.Marshal(t.OutputPolicy)
	}

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE tasks SET
				title=$1, description=$2, objective=$3, approach=$4,
				acceptance_criteria=$5, notes=$6,
				status=$7, priority=$8, category=$9, tags_json=$10,
				owner_agent_id=$11, assignee_agent_id=$12, claimed_by_session=$13,
				parent_id=$14, board_id=$15,
				entity_ids_json=$16, metadata_json=$17,
				compaction_level=$18, compacted_summary=$19,
				output_policy_json=$20, estimated_effort=$21,
				close_reason=$22, updated_at=$23
			WHERE id=$24 AND deleted_at IS NULL`,
			t.Title, t.Description, t.Objective, t.Approach,
			t.AcceptanceCriteria, t.Notes,
			int32(t.Status), int32(t.Priority), int32(t.Category), tagsJSON,
			t.OwnerAgentID, nilIfEmpty(t.AssigneeAgentID), nilIfEmpty(t.ClaimedBySession),
			nilIfEmpty(t.ParentID), nilIfEmpty(t.BoardID),
			entityIDsJSON, metadataJSON,
			t.CompactionLevel, t.CompactedSummary,
			outputPolicyJSON, t.EstimatedEffort,
			t.CloseReason, now, t.ID,
		)
		if err != nil {
			return fmt.Errorf("update task: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("task not found: %s", t.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (s *TaskStore) DeleteTask(ctx context.Context, id string) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.delete")
	defer s.tracer.EndSpan(span)

	return execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE tasks SET deleted_at = NOW() WHERE id = $1 AND deleted_at IS NULL`, id)
		if err != nil {
			return fmt.Errorf("delete task: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("task %s not found or already deleted", id)
		}
		return nil
	})
}

func (s *TaskStore) ListTasks(ctx context.Context, opts task.ListTasksOpts) ([]*task.Task, int, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.list")
	defer s.tracer.EndSpan(span)

	var conditions []string
	var args []interface{}
	argN := 1

	conditions = append(conditions, "t.deleted_at IS NULL")

	if opts.BoardID != "" {
		conditions = append(conditions, fmt.Sprintf("t.board_id = $%d", argN))
		args = append(args, opts.BoardID)
		argN++
	}
	if opts.Status != loomv1.TaskStatus_TASK_STATUS_UNSPECIFIED {
		conditions = append(conditions, fmt.Sprintf("t.status = $%d", argN))
		args = append(args, int32(opts.Status))
		argN++
	}
	if opts.Priority != loomv1.TaskPriority_TASK_PRIORITY_UNSPECIFIED {
		conditions = append(conditions, fmt.Sprintf("t.priority = $%d", argN))
		args = append(args, int32(opts.Priority))
		argN++
	}
	if opts.Category != loomv1.TaskCategory_TASK_CATEGORY_UNSPECIFIED {
		conditions = append(conditions, fmt.Sprintf("t.category = $%d", argN))
		args = append(args, int32(opts.Category))
		argN++
	}
	if opts.AssigneeAgentID != "" {
		conditions = append(conditions, fmt.Sprintf("t.assignee_agent_id = $%d", argN))
		args = append(args, opts.AssigneeAgentID)
		argN++
	}
	if opts.ParentID != "" {
		conditions = append(conditions, fmt.Sprintf("t.parent_id = $%d", argN))
		args = append(args, opts.ParentID)
		argN++
	}
	if opts.Query != "" {
		conditions = append(conditions, fmt.Sprintf("t.search_vector @@ plainto_tsquery('english', $%d)", argN))
		args = append(args, opts.Query)
		argN++
	}

	where := strings.Join(conditions, " AND ")

	var total int
	var tasks []*task.Task

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		// Count
		err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM tasks t WHERE "+where, args...).Scan(&total)
		if err != nil {
			return fmt.Errorf("count tasks: %w", err)
		}

		// Fetch
		limit := opts.Limit
		if limit <= 0 {
			limit = 50
		}
		fetchArgs := make([]interface{}, len(args))
		copy(fetchArgs, args)
		fetchArgs = append(fetchArgs, limit, opts.Offset)

		query := fmt.Sprintf(`SELECT %s FROM tasks t WHERE %s
			ORDER BY t.priority ASC, t.created_at ASC
			LIMIT $%d OFFSET $%d`, taskColumns, where, argN, argN+1)

		rows, err := tx.Query(ctx, query, fetchArgs...)
		if err != nil {
			return fmt.Errorf("list tasks: %w", err)
		}
		defer rows.Close() //nolint:errcheck

		for rows.Next() {
			t, err := pgScanTaskRows(rows)
			if err != nil {
				return err
			}
			tasks = append(tasks, t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	return tasks, total, nil
}

// =============================================================================
// Workflow Operations
// =============================================================================

// ClaimTask atomically claims a task using FOR UPDATE SKIP LOCKED to prevent
// contention in multi-agent scenarios. If another agent holds the lock, this
// returns immediately with an error instead of blocking.
func (s *TaskStore) ClaimTask(ctx context.Context, taskID, agentID, sessionID string) (*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.claim")
	defer s.tracer.EndSpan(span)

	now := time.Now().UTC()

	var result *task.Task
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		// Acquire row lock with SKIP LOCKED — fails fast if another agent holds it.
		var lockedID string
		err := tx.QueryRow(ctx, `
			SELECT id FROM tasks
			WHERE id = $1 AND status = $2 AND claimed_by_session IS NULL AND deleted_at IS NULL
			FOR UPDATE SKIP LOCKED`,
			taskID, int32(loomv1.TaskStatus_TASK_STATUS_OPEN),
		).Scan(&lockedID)
		if err != nil {
			return fmt.Errorf("task %s cannot be claimed (not OPEN, already claimed, or locked): %w", taskID, err)
		}

		// Update the locked row.
		_, err = tx.Exec(ctx, `
			UPDATE tasks SET
				assignee_agent_id = $1, claimed_by_session = $2,
				status = $3, claimed_at = $4, updated_at = $4
			WHERE id = $5`,
			agentID, sessionID,
			int32(loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS), now, lockedID,
		)
		if err != nil {
			return fmt.Errorf("claim task update: %w", err)
		}

		// Read back the updated task within the same transaction.
		row := tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = $1`, lockedID)
		result, err = pgScanTask(row)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *TaskStore) ReleaseTask(ctx context.Context, taskID, sessionID string) (*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.release")
	defer s.tracer.EndSpan(span)

	now := time.Now().UTC()
	var result *task.Task
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE tasks SET
				assignee_agent_id = NULL, claimed_by_session = NULL,
				status = $1, claimed_at = NULL, updated_at = $2
			WHERE id = $3 AND claimed_by_session = $4 AND deleted_at IS NULL`,
			int32(loomv1.TaskStatus_TASK_STATUS_OPEN), now, taskID, sessionID,
		)
		if err != nil {
			return fmt.Errorf("release task: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("task %s not claimed by session %s", taskID, sessionID)
		}
		row := tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = $1`, taskID)
		result, err = pgScanTask(row)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *TaskStore) CloseTask(ctx context.Context, taskID, reason string) (*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.close")
	defer s.tracer.EndSpan(span)

	now := time.Now().UTC()
	var result *task.Task
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE tasks SET
				status = $1, close_reason = $2, closed_at = $3,
				assignee_agent_id = NULL, claimed_by_session = NULL,
				updated_at = $3
			WHERE id = $4 AND deleted_at IS NULL`,
			int32(loomv1.TaskStatus_TASK_STATUS_DONE), reason, now, taskID,
		)
		if err != nil {
			return fmt.Errorf("close task: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("task %s not found or already deleted", taskID)
		}
		row := tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = $1`, taskID)
		result, err = pgScanTask(row)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *TaskStore) TransitionTask(ctx context.Context, taskID string, newStatus loomv1.TaskStatus) (*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.transition")
	defer s.tracer.EndSpan(span)

	now := time.Now().UTC()
	var result *task.Task
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE tasks SET status = $1, updated_at = $2
			WHERE id = $3 AND deleted_at IS NULL`,
			int32(newStatus), now, taskID,
		)
		if err != nil {
			return fmt.Errorf("transition task: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("task %s not found or already deleted", taskID)
		}
		row := tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = $1`, taskID)
		result, err = pgScanTask(row)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// =============================================================================
// Dependencies
// =============================================================================

func (s *TaskStore) AddDependency(ctx context.Context, dep *task.TaskDependency) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.add_dependency")
	defer s.tracer.EndSpan(span)

	now := time.Now().UTC()
	dep.CreatedAt = now
	metadataJSON, _ := json.Marshal(dep.Metadata)

	return execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO task_dependencies (from_task_id, to_task_id, type, created_by, metadata_json, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			dep.FromTaskID, dep.ToTaskID, int32(dep.Type), dep.CreatedBy, metadataJSON, now,
		)
		if err != nil {
			return fmt.Errorf("add dependency: %w", err)
		}
		return nil
	})
}

func (s *TaskStore) RemoveDependency(ctx context.Context, fromTaskID, toTaskID string) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.remove_dependency")
	defer s.tracer.EndSpan(span)

	return execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM task_dependencies WHERE from_task_id = $1 AND to_task_id = $2`,
			fromTaskID, toTaskID)
		return err
	})
}

func (s *TaskStore) GetDependencies(ctx context.Context, taskID string) ([]*task.TaskDependency, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.get_dependencies")
	defer s.tracer.EndSpan(span)

	var deps []*task.TaskDependency
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT from_task_id, to_task_id, type, created_by, metadata_json, created_at
			FROM task_dependencies WHERE from_task_id = $1`, taskID)
		if err != nil {
			return fmt.Errorf("get dependencies: %w", err)
		}
		defer rows.Close() //nolint:errcheck

		deps, err = pgScanDependencies(rows)
		return err
	})
	return deps, err
}

func (s *TaskStore) GetDependents(ctx context.Context, taskID string) ([]*task.TaskDependency, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.get_dependents")
	defer s.tracer.EndSpan(span)

	var deps []*task.TaskDependency
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT from_task_id, to_task_id, type, created_by, metadata_json, created_at
			FROM task_dependencies WHERE to_task_id = $1`, taskID)
		if err != nil {
			return fmt.Errorf("get dependents: %w", err)
		}
		defer rows.Close() //nolint:errcheck

		deps, err = pgScanDependencies(rows)
		return err
	})
	return deps, err
}

func (s *TaskStore) GetReadyFront(ctx context.Context, boardID string, opts task.ReadyFrontOpts) ([]*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.get_ready_front")
	defer s.tracer.EndSpan(span)

	var conditions []string
	var args []interface{}
	argN := 1

	conditions = append(conditions, fmt.Sprintf("t.status = $%d", argN))
	args = append(args, int32(loomv1.TaskStatus_TASK_STATUS_OPEN))
	argN++

	conditions = append(conditions, "t.deleted_at IS NULL")

	if boardID != "" {
		conditions = append(conditions, fmt.Sprintf("t.board_id = $%d", argN))
		args = append(args, boardID)
		argN++
	}

	if opts.MinPriority != loomv1.TaskPriority_TASK_PRIORITY_UNSPECIFIED {
		conditions = append(conditions, fmt.Sprintf("t.priority <= $%d", argN))
		args = append(args, int32(opts.MinPriority))
		argN++
	}

	// Exclude blocked tasks
	conditions = append(conditions, fmt.Sprintf(`NOT EXISTS (
		SELECT 1 FROM task_dependencies d
		JOIN tasks blocker ON d.to_task_id = blocker.id
		WHERE d.from_task_id = t.id
			AND d.type = $%d
			AND blocker.status NOT IN ($%d, $%d, $%d)
			AND blocker.deleted_at IS NULL
	)`, argN, argN+1, argN+2, argN+3))
	args = append(args,
		int32(loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_BLOCKS),
		int32(loomv1.TaskStatus_TASK_STATUS_DONE),
		int32(loomv1.TaskStatus_TASK_STATUS_DEFERRED),
		int32(loomv1.TaskStatus_TASK_STATUS_CANCELLED),
	)
	argN += 4

	where := strings.Join(conditions, " AND ")
	limit := opts.MaxResults
	if limit <= 0 {
		limit = 20
	}

	query := fmt.Sprintf(`SELECT %s FROM tasks t WHERE %s
		ORDER BY t.priority ASC, t.created_at ASC
		LIMIT $%d`, taskColumns, where, argN)
	args = append(args, limit)

	var tasks []*task.Task
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("get ready front: %w", err)
		}
		defer rows.Close() //nolint:errcheck

		for rows.Next() {
			t, err := pgScanTaskRows(rows)
			if err != nil {
				return err
			}
			tasks = append(tasks, t)
		}
		return rows.Err()
	})
	return tasks, err
}

func (s *TaskStore) GetBlockedTasks(ctx context.Context, boardID string) ([]*task.Task, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.get_blocked")
	defer s.tracer.EndSpan(span)

	var args []interface{}
	argN := 1

	conditions := fmt.Sprintf("t.status = $%d AND t.deleted_at IS NULL", argN)
	args = append(args, int32(loomv1.TaskStatus_TASK_STATUS_BLOCKED))
	argN++

	if boardID != "" {
		conditions += fmt.Sprintf(" AND t.board_id = $%d", argN)
		args = append(args, boardID)
	}

	query := fmt.Sprintf(`SELECT %s FROM tasks t WHERE %s
		ORDER BY t.priority ASC, t.created_at ASC`, taskColumns, conditions)

	var tasks []*task.Task
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("get blocked tasks: %w", err)
		}
		defer rows.Close() //nolint:errcheck

		for rows.Next() {
			t, err := pgScanTaskRows(rows)
			if err != nil {
				return err
			}
			tasks = append(tasks, t)
		}
		return rows.Err()
	})
	return tasks, err
}

// =============================================================================
// Boards
// =============================================================================

func (s *TaskStore) CreateBoard(ctx context.Context, board *task.TaskBoard) (*task.TaskBoard, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.create_board")
	defer s.tracer.EndSpan(span)

	if board.ID == "" {
		board.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	board.CreatedAt = now

	lanesJSON, _ := json.Marshal(board.Lanes)
	metadataJSON, _ := json.Marshal(board.Metadata)

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		_, err := tx.Exec(ctx, `
			INSERT INTO task_boards (id, name, workflow_id, lanes_json, metadata_json, user_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			board.ID, board.Name, nilIfEmpty(board.WorkflowID),
			lanesJSON, metadataJSON, userID, now,
		)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("create board: %w", err)
	}
	return board, nil
}

func (s *TaskStore) GetBoard(ctx context.Context, id string) (*task.TaskBoard, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.get_board")
	defer s.tracer.EndSpan(span)

	var board task.TaskBoard
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var lanesJSON, metadataJSON []byte
		var workflowID *string

		err := tx.QueryRow(ctx, `
			SELECT id, name, workflow_id, lanes_json, metadata_json, created_at
			FROM task_boards WHERE id = $1 AND deleted_at IS NULL`, id).
			Scan(&board.ID, &board.Name, &workflowID, &lanesJSON, &metadataJSON, &board.CreatedAt)
		if err != nil {
			return fmt.Errorf("get board: %w", err)
		}

		if workflowID != nil {
			board.WorkflowID = *workflowID
		}
		_ = json.Unmarshal(lanesJSON, &board.Lanes)
		_ = json.Unmarshal(metadataJSON, &board.Metadata)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &board, nil
}

func (s *TaskStore) ListBoards(ctx context.Context) ([]*task.TaskBoard, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.list_boards")
	defer s.tracer.EndSpan(span)

	var boards []*task.TaskBoard
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, name, workflow_id, lanes_json, metadata_json, created_at
			FROM task_boards WHERE deleted_at IS NULL ORDER BY created_at ASC`)
		if err != nil {
			return fmt.Errorf("list boards: %w", err)
		}
		defer rows.Close() //nolint:errcheck

		for rows.Next() {
			var board task.TaskBoard
			var lanesJSON, metadataJSON []byte
			var workflowID *string
			if err := rows.Scan(&board.ID, &board.Name, &workflowID, &lanesJSON, &metadataJSON, &board.CreatedAt); err != nil {
				return fmt.Errorf("scan board: %w", err)
			}
			if workflowID != nil {
				board.WorkflowID = *workflowID
			}
			_ = json.Unmarshal(lanesJSON, &board.Lanes)
			_ = json.Unmarshal(metadataJSON, &board.Metadata)
			boards = append(boards, &board)
		}
		return rows.Err()
	})
	return boards, err
}

// =============================================================================
// History
// =============================================================================

func (s *TaskStore) RecordHistory(ctx context.Context, entry *task.TaskHistoryEntry) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.record_history")
	defer s.tracer.EndSpan(span)

	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	entry.Timestamp = time.Now().UTC()

	return execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO task_history (id, task_id, action, old_status, new_status, agent_id, session_id, details_json, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			entry.ID, entry.TaskID, entry.Action, entry.OldStatus, entry.NewStatus,
			entry.AgentID, entry.SessionID, entry.DetailsJSON, entry.Timestamp,
		)
		return err
	})
}

func (s *TaskStore) GetHistory(ctx context.Context, taskID string) ([]*task.TaskHistoryEntry, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.task.get_history")
	defer s.tracer.EndSpan(span)

	var entries []*task.TaskHistoryEntry
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, task_id, action, old_status, new_status, agent_id, session_id, details_json, created_at
			FROM task_history WHERE task_id = $1 ORDER BY created_at ASC`, taskID)
		if err != nil {
			return fmt.Errorf("get history: %w", err)
		}
		defer rows.Close() //nolint:errcheck

		for rows.Next() {
			var e task.TaskHistoryEntry
			if err := rows.Scan(&e.ID, &e.TaskID, &e.Action, &e.OldStatus, &e.NewStatus,
				&e.AgentID, &e.SessionID, &e.DetailsJSON, &e.Timestamp); err != nil {
				return fmt.Errorf("scan history: %w", err)
			}
			entries = append(entries, &e)
		}
		return rows.Err()
	})
	return entries, err
}

func (s *TaskStore) Close() error {
	return nil // Pool owned by Backend, not us.
}

// =============================================================================
// Helpers
// =============================================================================

func pgScanTask(row pgx.Row) (*task.Task, error) {
	var t task.Task
	var status, priority, category int32
	var tagsJSON, entityIDsJSON, metadataJSON []byte
	var outputPolicyJSON []byte
	var claimedAt, closedAt *time.Time

	err := row.Scan(
		&t.ID, &t.Title, &t.Description, &t.Objective, &t.Approach,
		&t.AcceptanceCriteria, &t.Notes,
		&status, &priority, &category, &tagsJSON,
		&t.OwnerAgentID, &t.AssigneeAgentID, &t.ClaimedBySession,
		&t.ParentID, &t.BoardID, &entityIDsJSON, &metadataJSON,
		&t.CompactionLevel, &t.CompactedSummary, &outputPolicyJSON, &t.EstimatedEffort,
		&t.CreatedAt, &t.UpdatedAt, &claimedAt, &closedAt, &t.CloseReason,
	)
	if err != nil {
		return nil, fmt.Errorf("scan task: %w", err)
	}

	t.Status = loomv1.TaskStatus(status)
	t.Priority = loomv1.TaskPriority(priority)
	t.Category = loomv1.TaskCategory(category)
	_ = json.Unmarshal(tagsJSON, &t.Tags)
	_ = json.Unmarshal(entityIDsJSON, &t.EntityIDs)
	_ = json.Unmarshal(metadataJSON, &t.Metadata)
	t.ClaimedAt = claimedAt
	t.ClosedAt = closedAt

	if len(outputPolicyJSON) > 0 {
		t.OutputPolicy = &loomv1.OutputPolicy{}
		_ = json.Unmarshal(outputPolicyJSON, t.OutputPolicy)
	}

	return &t, nil
}

func pgScanTaskRows(rows pgx.Rows) (*task.Task, error) {
	var t task.Task
	var status, priority, category int32
	var tagsJSON, entityIDsJSON, metadataJSON []byte
	var outputPolicyJSON []byte
	var claimedAt, closedAt *time.Time

	err := rows.Scan(
		&t.ID, &t.Title, &t.Description, &t.Objective, &t.Approach,
		&t.AcceptanceCriteria, &t.Notes,
		&status, &priority, &category, &tagsJSON,
		&t.OwnerAgentID, &t.AssigneeAgentID, &t.ClaimedBySession,
		&t.ParentID, &t.BoardID, &entityIDsJSON, &metadataJSON,
		&t.CompactionLevel, &t.CompactedSummary, &outputPolicyJSON, &t.EstimatedEffort,
		&t.CreatedAt, &t.UpdatedAt, &claimedAt, &closedAt, &t.CloseReason,
	)
	if err != nil {
		return nil, fmt.Errorf("scan task: %w", err)
	}

	t.Status = loomv1.TaskStatus(status)
	t.Priority = loomv1.TaskPriority(priority)
	t.Category = loomv1.TaskCategory(category)
	_ = json.Unmarshal(tagsJSON, &t.Tags)
	_ = json.Unmarshal(entityIDsJSON, &t.EntityIDs)
	_ = json.Unmarshal(metadataJSON, &t.Metadata)
	t.ClaimedAt = claimedAt
	t.ClosedAt = closedAt

	if len(outputPolicyJSON) > 0 {
		t.OutputPolicy = &loomv1.OutputPolicy{}
		_ = json.Unmarshal(outputPolicyJSON, t.OutputPolicy)
	}

	return &t, nil
}

func pgScanDependencies(rows pgx.Rows) ([]*task.TaskDependency, error) {
	var deps []*task.TaskDependency
	for rows.Next() {
		var d task.TaskDependency
		var depType int32
		var metadataJSON []byte
		if err := rows.Scan(&d.FromTaskID, &d.ToTaskID, &depType, &d.CreatedBy, &metadataJSON, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan dependency: %w", err)
		}
		d.Type = loomv1.TaskDependencyType(depType)
		_ = json.Unmarshal(metadataJSON, &d.Metadata)
		deps = append(deps, &d)
	}
	return deps, rows.Err()
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

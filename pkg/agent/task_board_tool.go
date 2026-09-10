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

package agent

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/task"
)

// CreatedBySessionMetadataKey records, in task metadata, the conversation
// session that created a task via this tool (create/decompose). Claims are a
// separate lifecycle event (see executeClaim); this key lets callers scope
// "tasks created in this conversation" without pre-claiming, which would
// break the ready → claim workflow (ClaimTask requires an unclaimed task).
const CreatedBySessionMetadataKey = task.CreatedBySessionMetadataKey

// TaskBoardTool provides agent-facing task decomposition and kanban operations.
// Actions: decompose, ready, claim, update, close, create, list, show, add_dep, board.
type TaskBoardTool struct {
	manager    *task.Manager
	decomposer *task.Decomposer
	agentID    string
	llm        LLMProvider
	config     *loomv1.TaskBoardConfig
}

// NewTaskBoardTool creates a new task board tool.
func NewTaskBoardTool(manager *task.Manager, decomposer *task.Decomposer, agentID string, llm LLMProvider, config *loomv1.TaskBoardConfig) *TaskBoardTool {
	return &TaskBoardTool{
		manager:    manager,
		decomposer: decomposer,
		agentID:    agentID,
		llm:        llm,
		config:     config,
	}
}

// Compile-time interface check.
var _ shuttle.Tool = (*TaskBoardTool)(nil)

func (t *TaskBoardTool) Name() string    { return "task_board" }
func (t *TaskBoardTool) Backend() string { return "" }
func (t *TaskBoardTool) Description() string {
	return `Manage tasks with dependency-aware decomposition and kanban tracking.

Actions:
1. decompose - Break a goal into a dependency DAG of subtasks using LLM
2. ready - Get the "ready front": tasks with all dependencies satisfied
3. claim - Atomically claim a task to work on
4. update - Update task notes, approach, status, or write-once acceptance criteria; status changes run the full lifecycle: done/cancelled close the task (recording closed_at + reason, releasing any claim, unblocking dependents), in_progress claims it (atomic, WIP-limited). Pass updates as an array (max 20) to batch transitions (finish one task and start the next in one call)
5. close - Mark a task as done with a reason
6. create - Create a single task manually
7. list - List tasks with filtering
8. show - Get full task details including dependencies
9. add_dep - Add a dependency between tasks
10. board - Get board overview with stats

Workflow: decompose → ready → claim → work → update notes → close → ready`
}

func (t *TaskBoardTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"action": {
				Type:        "string",
				Description: "Action: decompose, ready, claim, update, close, create, list, show, add_dep, board",
			},
			"goal": {
				Type:        "string",
				Description: "(decompose) High-level goal to break down",
			},
			"context": {
				Type:        "string",
				Description: "(decompose) Additional context for decomposition",
			},
			"strategy": {
				Type:        "string",
				Description: "(decompose) Strategy: backward (default), forward, or parallel",
			},
			"task_id": {
				Type:        "string",
				Description: "(claim/update/close/show/add_dep) Task ID",
			},
			"title": {
				Type:        "string",
				Description: "(create) Task title",
			},
			"description": {
				Type:        "string",
				Description: "(create/update) Task description",
			},
			"objective": {
				Type:        "string",
				Description: "(create) What done looks like",
			},
			"notes": {
				Type:        "string",
				Description: "(update) Append to task notes (progress, findings, blockers)",
			},
			"approach": {
				Type:        "string",
				Description: "(create/update) How to accomplish the objective",
			},
			"reason": {
				Type:        "string",
				Description: "(close/update) Completion summary, or the cancellation reason when status=cancelled",
			},
			"status": {
				Type:        "string",
				Description: "(update/list) Task status: open, in_progress, blocked, done, deferred, cancelled. On update, done/cancelled close the task (releasing claims, unblocking dependents) and in_progress claims it",
			},
			"acceptance_criteria": {
				Type:        "string",
				Description: "(create/update) Verifiable completion conditions — copy them from the task text verbatim where exactness matters. Write-once: settable at create or while still empty, then immutable; cancel the task (update with status=cancelled and a reason) and re-create it to change them.",
			},
			"updates": {
				Type:        "array",
				Description: "(update) Batch transitions applied independently in one call (max 20 entries) — e.g. mark the current task done and claim the next. Each entry is one update; per-entry results are reported.",
				Items: &shuttle.JSONSchema{
					Type: "object",
					Properties: map[string]*shuttle.JSONSchema{
						"task_id":             {Type: "string", Description: "Task ID (required)"},
						"status":              {Type: "string", Description: "open, in_progress, blocked, done, deferred, cancelled"},
						"reason":              {Type: "string", Description: "Close/cancel reason when status is done or cancelled"},
						"notes":               {Type: "string", Description: "Appended to task notes"},
						"description":         {Type: "string", Description: "Replaces description"},
						"approach":            {Type: "string", Description: "Replaces approach"},
						"acceptance_criteria": {Type: "string", Description: "Write-once; only while still empty"},
					},
					Required: []string{"task_id"},
				},
			},
			"priority": {
				Type:        "string",
				Description: "(create/list) Priority: P0-P4",
			},
			"category": {
				Type:        "string",
				Description: "(create) Category: research, analysis, implementation, review, writing, decision, investigation, planning",
			},
			"board_id": {
				Type:        "string",
				Description: "(decompose/ready/list/board/create) Board ID",
			},
			"parent_id": {
				Type:        "string",
				Description: "(create/decompose) Parent task ID for subtasks",
			},
			"depends_on": {
				Type:        "string",
				Description: "(add_dep) Task ID that this task depends on (blocker)",
			},
			"query": {
				Type:        "string",
				Description: "(list) Full-text search query",
			},
			"tags": {
				Type:        "array",
				Description: "(create) Freeform tags",
				Items:       &shuttle.JSONSchema{Type: "string"},
			},
			"estimated_effort": {
				Type:        "string",
				Description: "(create) Effort estimate (e.g., '30 min', '2 hours')",
			},
		},
		Required: []string{"action"},
	}
}

func (t *TaskBoardTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	action, ok := input["action"].(string)
	if !ok || action == "" {
		return errorResult("INVALID_PARAMETER", "action is required"), nil
	}

	switch action {
	case "decompose":
		return t.executeDecompose(ctx, input)
	case "ready":
		return t.executeReady(ctx, input)
	case "claim":
		return t.executeClaim(ctx, input)
	case "update":
		return t.executeUpdate(ctx, input)
	case "close":
		return t.executeClose(ctx, input)
	case "create":
		return t.executeCreate(ctx, input)
	case "list":
		return t.executeList(ctx, input)
	case "show":
		return t.executeShow(ctx, input)
	case "add_dep":
		return t.executeAddDep(ctx, input)
	case "board":
		return t.executeBoard(ctx, input)
	default:
		return errorResult("INVALID_ACTION",
			"unknown action: "+action+". Valid: decompose, ready, claim, update, close, create, list, show, add_dep, board"), nil
	}
}

// =============================================================================
// Action Implementations
// =============================================================================

func (t *TaskBoardTool) executeDecompose(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	goal := getStr(input, "goal")
	if goal == "" {
		return errorResult("INVALID_PARAMETER", "goal is required for decompose"), nil
	}

	strategy := parseDecomposeStrategy(getStr(input, "strategy"))
	boardID, err := t.resolveBoardForWrite(ctx, input)
	if err != nil {
		return errorResult("DECOMPOSE_ERROR", err.Error()), nil
	}

	var parentTask *task.Task
	if parentID := getStr(input, "parent_id"); parentID != "" {
		var err error
		parentTask, err = t.manager.GetTask(ctx, parentID)
		if err != nil {
			return errorResult("NOT_FOUND", fmt.Sprintf("parent task %s not found: %s", parentID, err)), nil
		}
	}

	maxDepth := defaultMaxDepth(t.config)
	resp, err := t.decomposer.Decompose(ctx, t.llm, &task.DecomposeRequest{
		Goal:       goal,
		Context:    getStr(input, "context"),
		BoardID:    boardID,
		ParentTask: parentTask,
		MaxDepth:   maxDepth,
		Strategy:   strategy,
		AgentID:    t.agentID,
		SessionID:  session.SessionIDFromContext(ctx),
	})
	if err != nil {
		return errorResult("DECOMPOSE_ERROR", err.Error()), nil
	}

	taskSummaries := make([]map[string]interface{}, 0, len(resp.Tasks))
	for _, tk := range resp.Tasks {
		taskSummaries = append(taskSummaries, map[string]interface{}{
			"id":       tk.ID,
			"title":    tk.Title,
			"priority": task.PriorityName(tk.Priority),
			"status":   task.StatusName(tk.Status),
		})
	}

	return jsonResult(map[string]interface{}{
		"action":        "decompose",
		"tasks_created": len(resp.Tasks),
		"tasks":         taskSummaries,
		"dependencies":  len(resp.Dependencies),
		"reasoning":     resp.Reasoning,
	})
}

func (t *TaskBoardTool) executeReady(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	boardID := t.resolveBoard(input)
	tasks, err := t.manager.GetReadyFront(ctx, boardID, task.ReadyFrontOpts{
		MaxResults: 10,
	})
	if err != nil {
		return errorResult("STORE_ERROR", err.Error()), nil
	}

	items := make([]map[string]interface{}, 0, len(tasks))
	for _, tk := range tasks {
		items = append(items, taskSummaryMap(tk))
	}

	return jsonResult(map[string]interface{}{
		"action":      "ready",
		"ready_count": len(tasks),
		"tasks":       items,
	})
}

func (t *TaskBoardTool) executeClaim(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	taskID := getStr(input, "task_id")
	if taskID == "" {
		return errorResult("INVALID_PARAMETER", "task_id is required for claim"), nil
	}

	claimed, err := t.manager.ClaimTask(ctx, taskID, t.agentID, t.claimSessionID(ctx))
	if err != nil {
		return errorResult("CLAIM_ERROR", err.Error()), nil
	}

	return jsonResult(map[string]interface{}{
		"action": "claim",
		"task":   taskDetailMap(claimed),
	})
}

// maxBatchUpdates caps the batch form of the update action, matching the
// batch caps on sibling tool surfaces.
const maxBatchUpdates = 20

func (t *TaskBoardTool) executeUpdate(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Batch form: apply each entry independently and report per-entry
	// outcomes, so completing the current task and starting the next is one
	// call and one partial failure does not roll back the rest.
	if rawValue, present := input["updates"]; present {
		rawUpdates, isArray := rawValue.([]interface{})
		if !isArray {
			return errorResult("INVALID_PARAMETER", "updates must be an array of update objects"), nil
		}
		if len(rawUpdates) == 0 {
			return errorResult("INVALID_PARAMETER", "updates must contain at least one entry"), nil
		}
		if len(rawUpdates) > maxBatchUpdates {
			return errorResult("INVALID_PARAMETER", fmt.Sprintf(
				"updates accepts at most %d entries per call (got %d)", maxBatchUpdates, len(rawUpdates))), nil
		}

		results := make([]map[string]interface{}, 0, len(rawUpdates))
		failed := 0
		for i, raw := range rawUpdates {
			entry, isMap := raw.(map[string]interface{})
			if !isMap {
				failed++
				results = append(results, map[string]interface{}{
					"index": i,
					"error": map[string]interface{}{
						"code":    "INVALID_PARAMETER",
						"message": "each update must be an object",
					},
				})
				continue
			}
			updated, applyErr := t.applyTaskUpdate(ctx, entry)
			if applyErr != nil {
				failed++
				results = append(results, map[string]interface{}{
					"index":   i,
					"task_id": getStr(entry, "task_id"),
					"error": map[string]interface{}{
						"code":    updateErrorCode(applyErr),
						"message": applyErr.Error(),
					},
				})
				continue
			}
			results = append(results, map[string]interface{}{
				"index": i, "task": taskDetailMap(updated),
			})
		}

		data := map[string]interface{}{
			"action":    "update",
			"batch":     true,
			"results":   results,
			"succeeded": len(rawUpdates) - failed,
			"failed":    failed,
		}
		if failed == len(rawUpdates) {
			// Every entry failed: the call as a whole failed. Per-entry
			// errors stay in Data for diagnosis.
			return &shuttle.Result{
				Success: false,
				Data:    data,
				Error: &shuttle.Error{
					Code:    "UPDATE_ERROR",
					Message: fmt.Sprintf("all %d update entries failed; see results for per-entry errors", failed),
				},
			}, nil
		}
		return jsonResult(data)
	}

	updated, err := t.applyTaskUpdate(ctx, input)
	if err != nil {
		return errorResult(updateErrorCode(err), err.Error()), nil
	}

	return jsonResult(map[string]interface{}{
		"action": "update",
		"task":   taskDetailMap(updated),
	})
}

// criteriaLockedError marks the write-once acceptance-criteria violation so
// the error code survives the shared apply path. Its recovery instruction
// names the real cancel path: update with status=cancelled (executeClose
// always closes as DONE).
type criteriaLockedError struct{ taskID string }

func (e *criteriaLockedError) Error() string {
	return "acceptance_criteria are write-once and already set on task " + e.taskID +
		": cancel the task (update with status=cancelled and a reason) and re-create it with corrected criteria"
}

// invalidParameterError marks caller-input problems so the shared apply path
// surfaces INVALID_PARAMETER instead of a generic UPDATE_ERROR.
type invalidParameterError struct{ msg string }

func (e *invalidParameterError) Error() string { return e.msg }

// taskNotFoundError marks lookup failures so the shared apply path surfaces
// NOT_FOUND instead of a generic UPDATE_ERROR.
type taskNotFoundError struct {
	taskID string
	err    error
}

func (e *taskNotFoundError) Error() string {
	return fmt.Sprintf("task %s not found: %s", e.taskID, e.err)
}
func (e *taskNotFoundError) Unwrap() error { return e.err }

func updateErrorCode(err error) string {
	var locked *criteriaLockedError
	if errors.As(err, &locked) {
		return "ACCEPTANCE_CRITERIA_LOCKED"
	}
	var invalid *invalidParameterError
	if errors.As(err, &invalid) {
		return "INVALID_PARAMETER"
	}
	var notFound *taskNotFoundError
	if errors.As(err, &notFound) {
		return "NOT_FOUND"
	}
	return "UPDATE_ERROR"
}

// claimSessionID resolves the session to record on claims: the real
// conversation session when the agent runs inside one (Chat sets it via
// session.WithSessionID), else the legacy synthetic id so
// standalone/headless usage keeps prior behavior.
func (t *TaskBoardTool) claimSessionID(ctx context.Context) string {
	if sid := session.SessionIDFromContext(ctx); sid != "" {
		return sid
	}
	return t.agentID + "-session"
}

// applyTaskUpdate performs one task update (shared by the single and batch
// forms of the update action). Status changes route through the manager
// operation that owns the transition's side effects, never through a raw
// status overwrite:
//
//   - done        → Manager.CloseTask (closed_at + reason, claim release,
//     dependent unblocking, parent auto-complete)
//   - cancelled   → Manager.CancelTask (same terminal bookkeeping, no
//     completion memory)
//   - in_progress → Manager.ClaimTask (atomic claim: exactly one concurrent
//     claimer wins, WIP limits enforced)
//   - open        → Manager.ReleaseTask when claimed (clears the claim so
//     the task is re-claimable), else Manager.TransitionTask
//   - blocked/deferred → Manager.TransitionTask (history + events)
//
// Field-only updates (notes/description/approach) persist via
// Manager.UpdateTask. Write-once acceptance criteria persist via the
// store-guarded Manager.SetAcceptanceCriteria — the guard is a conditional
// write in the store, so concurrent writers cannot both win.
func (t *TaskBoardTool) applyTaskUpdate(ctx context.Context, input map[string]interface{}) (*task.Task, error) {
	taskID := getStr(input, "task_id")
	if taskID == "" {
		return nil, &invalidParameterError{msg: "task_id is required for update"}
	}

	newStatus := loomv1.TaskStatus_TASK_STATUS_UNSPECIFIED
	if s := getStr(input, "status"); s != "" {
		newStatus = parseTaskStatus(s)
		if newStatus == loomv1.TaskStatus_TASK_STATUS_UNSPECIFIED {
			return nil, &invalidParameterError{msg: fmt.Sprintf(
				"invalid status %q; valid: open, in_progress, blocked, done, deferred, cancelled", s)}
		}
	}

	existing, err := t.manager.GetTask(ctx, taskID)
	if err != nil {
		return nil, &taskNotFoundError{taskID: taskID, err: err}
	}

	// Acceptance criteria are write-once: they define "done" and silently
	// moving the goalposts mid-task defeats their purpose. Re-sending the
	// identical value is an idempotent no-op; anything else is decided by
	// the store's atomic guard.
	if criteria := getStr(input, "acceptance_criteria"); criteria != "" && criteria != existing.AcceptanceCriteria {
		updated, setErr := t.manager.SetAcceptanceCriteria(ctx, taskID, criteria)
		if setErr != nil {
			if errors.Is(setErr, task.ErrAcceptanceCriteriaLocked) {
				return nil, &criteriaLockedError{taskID: taskID}
			}
			return nil, setErr
		}
		existing.AcceptanceCriteria = updated.AcceptanceCriteria
	}

	fieldsChanged := false
	// Append to notes (don't overwrite).
	if notes := getStr(input, "notes"); notes != "" {
		if existing.Notes != "" {
			existing.Notes += "\n" + notes
		} else {
			existing.Notes = notes
		}
		fieldsChanged = true
	}
	if desc := getStr(input, "description"); desc != "" {
		existing.Description = desc
		fieldsChanged = true
	}
	if approach := getStr(input, "approach"); approach != "" {
		existing.Approach = approach
		fieldsChanged = true
	}

	result := existing
	if fieldsChanged {
		result, err = t.manager.UpdateTask(ctx, existing, nil)
		if err != nil {
			return nil, err
		}
	}

	if newStatus == loomv1.TaskStatus_TASK_STATUS_UNSPECIFIED {
		return result, nil
	}
	// A same-status request is an idempotent no-op — except IN_PROGRESS,
	// which carries ownership semantics (see transitionViaLifecycle):
	// "already in progress" is only a success when this session holds the
	// claim.
	if newStatus == existing.Status && newStatus != loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS {
		return result, nil
	}
	return t.transitionViaLifecycle(ctx, existing, newStatus, getStr(input, "reason"))
}

// transitionViaLifecycle routes a status change to the manager operation
// that owns that transition's side effects (see applyTaskUpdate).
func (t *TaskBoardTool) transitionViaLifecycle(ctx context.Context, existing *task.Task, newStatus loomv1.TaskStatus, reason string) (*task.Task, error) {
	switch newStatus {
	case loomv1.TaskStatus_TASK_STATUS_DONE:
		if reason == "" {
			reason = "closed via task_board update"
		}
		return t.manager.CloseTask(ctx, existing.ID, reason)
	case loomv1.TaskStatus_TASK_STATUS_CANCELLED:
		if reason == "" {
			reason = "cancelled via task_board update"
		}
		return t.manager.CancelTask(ctx, existing.ID, reason)
	case loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS:
		// Claiming is the only path into IN_PROGRESS: atomic (exactly one
		// concurrent claimer wins) and WIP-limit checked. When the task is
		// already in progress, success is only honest if this session holds
		// the claim — reporting success while another session owns the task
		// would let two agents believe they are working the same task.
		sid := t.claimSessionID(ctx)
		if existing.Status == loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS {
			if existing.ClaimedBySession == sid {
				return existing, nil
			}
			if existing.ClaimedBySession == "" {
				return nil, fmt.Errorf("task %s is already in progress", existing.ID)
			}
			return nil, fmt.Errorf("task %s is already in progress (claimed by session %q)",
				existing.ID, existing.ClaimedBySession)
		}
		return t.manager.ClaimTask(ctx, existing.ID, t.agentID, sid)
	case loomv1.TaskStatus_TASK_STATUS_OPEN:
		if existing.ClaimedBySession != "" {
			// Returning a claimed task to OPEN is a release: the claim
			// fields must be cleared or the task can never be re-claimed.
			return t.manager.ReleaseTask(ctx, existing.ID, existing.ClaimedBySession)
		}
		return t.manager.TransitionTask(ctx, existing.ID, newStatus)
	default: // BLOCKED, DEFERRED
		return t.manager.TransitionTask(ctx, existing.ID, newStatus)
	}
}

func (t *TaskBoardTool) executeClose(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	taskID := getStr(input, "task_id")
	if taskID == "" {
		return errorResult("INVALID_PARAMETER", "task_id is required for close"), nil
	}

	reason := getStr(input, "reason")
	if reason == "" {
		reason = "completed"
	}

	closed, err := t.manager.CloseTask(ctx, taskID, reason)
	if err != nil {
		return errorResult("CLOSE_ERROR", err.Error()), nil
	}

	return jsonResult(map[string]interface{}{
		"action": "close",
		"task":   taskDetailMap(closed),
	})
}

func (t *TaskBoardTool) executeCreate(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	title := getStr(input, "title")
	if title == "" {
		return errorResult("INVALID_PARAMETER", "title is required for create"), nil
	}

	boardID, err := t.resolveBoardForWrite(ctx, input)
	if err != nil {
		return errorResult("CREATE_ERROR", err.Error()), nil
	}

	tk := &task.Task{
		Title:       title,
		Description: getStr(input, "description"),
		Objective:   getStr(input, "objective"),
		Approach:    getStr(input, "approach"),
		// Write-once: settable here (or on first update while empty), then
		// immutable — cancel and re-create to change criteria.
		AcceptanceCriteria: getStr(input, "acceptance_criteria"),
		Category:           task.ParseCategory(getStr(input, "category")),
		Priority:           task.ParsePriority(getStr(input, "priority")),
		EstimatedEffort:    getStr(input, "estimated_effort"),
		Tags:               getStrSlice(input, "tags"),
		Status:             loomv1.TaskStatus_TASK_STATUS_OPEN,
		OwnerAgentID:       t.agentID,
		BoardID:            boardID,
		ParentID:           getStr(input, "parent_id"),
	}
	// Attribute the task to the conversation that created it (metadata, not a
	// claim — pre-claiming would make the later ready → claim step fail).
	if sid := session.SessionIDFromContext(ctx); sid != "" {
		tk.Metadata = map[string]string{task.CreatedBySessionMetadataKey: sid}
	}

	created, err := t.manager.CreateTask(ctx, tk)
	if err != nil {
		return errorResult("CREATE_ERROR", err.Error()), nil
	}

	return jsonResult(map[string]interface{}{
		"action": "create",
		"task":   taskDetailMap(created),
	})
}

func (t *TaskBoardTool) executeList(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	boardID := t.resolveBoard(input)
	opts := task.ListTasksOpts{
		BoardID: boardID,
		Query:   getStr(input, "query"),
		Limit:   20,
	}

	if s := getStr(input, "status"); s != "" {
		opts.Status = parseTaskStatus(s)
	}
	if p := getStr(input, "priority"); p != "" {
		opts.Priority = task.ParsePriority(p)
	}

	tasks, total, err := t.manager.ListTasks(ctx, opts)
	if err != nil {
		return errorResult("STORE_ERROR", err.Error()), nil
	}

	items := make([]map[string]interface{}, 0, len(tasks))
	for _, tk := range tasks {
		items = append(items, taskSummaryMap(tk))
	}

	return jsonResult(map[string]interface{}{
		"action": "list",
		"total":  total,
		"tasks":  items,
	})
}

func (t *TaskBoardTool) executeShow(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	taskID := getStr(input, "task_id")
	if taskID == "" {
		return errorResult("INVALID_PARAMETER", "task_id is required for show"), nil
	}

	tk, err := t.manager.GetTask(ctx, taskID)
	if err != nil {
		return errorResult("NOT_FOUND", err.Error()), nil
	}

	// Get dependencies
	deps, _ := t.manager.Store().GetDependencies(ctx, taskID)
	dependents, _ := t.manager.Store().GetDependents(ctx, taskID)

	depList := make([]map[string]interface{}, 0, len(deps))
	for _, d := range deps {
		depList = append(depList, map[string]interface{}{
			"blocks_me": d.ToTaskID,
			"type":      d.Type.String(),
		})
	}
	dependentList := make([]map[string]interface{}, 0, len(dependents))
	for _, d := range dependents {
		dependentList = append(dependentList, map[string]interface{}{
			"blocked_by_me": d.FromTaskID,
			"type":          d.Type.String(),
		})
	}

	detail := taskDetailMap(tk)
	detail["dependencies"] = depList
	detail["dependents"] = dependentList
	detail["child_ids"] = tk.ChildIDs

	return jsonResult(map[string]interface{}{
		"action": "show",
		"task":   detail,
	})
}

func (t *TaskBoardTool) executeAddDep(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	taskID := getStr(input, "task_id")
	dependsOn := getStr(input, "depends_on")
	if taskID == "" || dependsOn == "" {
		return errorResult("INVALID_PARAMETER", "task_id and depends_on are required for add_dep"), nil
	}

	err := t.manager.AddDependency(ctx, &task.TaskDependency{
		FromTaskID: taskID,
		ToTaskID:   dependsOn,
		Type:       loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_BLOCKS,
		CreatedBy:  t.agentID,
	})
	if err != nil {
		return errorResult("DEPENDENCY_ERROR", err.Error()), nil
	}

	return jsonResult(map[string]interface{}{
		"action":     "add_dep",
		"task_id":    taskID,
		"depends_on": dependsOn,
	})
}

func (t *TaskBoardTool) executeBoard(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	boardID := t.resolveBoard(input)
	if boardID == "" {
		// List all boards
		boards, err := t.manager.ListBoards(ctx)
		if err != nil {
			return errorResult("STORE_ERROR", err.Error()), nil
		}
		boardList := make([]map[string]interface{}, 0, len(boards))
		for _, b := range boards {
			boardList = append(boardList, map[string]interface{}{
				"id":   b.ID,
				"name": b.Name,
			})
		}
		return jsonResult(map[string]interface{}{
			"action": "board",
			"boards": boardList,
		})
	}

	board, err := t.manager.GetBoard(ctx, boardID)
	if err != nil {
		return errorResult("NOT_FOUND", err.Error()), nil
	}

	// Get stats by counting tasks per status.
	allTasks, total, _ := t.manager.ListTasks(ctx, task.ListTasksOpts{BoardID: boardID, Limit: 1000})
	stats := map[string]int{"total": total}
	for _, tk := range allTasks {
		stats[task.StatusName(tk.Status)]++
	}

	return jsonResult(map[string]interface{}{
		"action": "board",
		"board": map[string]interface{}{
			"id":   board.ID,
			"name": board.Name,
		},
		"stats": stats,
	})
}

// =============================================================================
// Helpers
// =============================================================================

func (t *TaskBoardTool) resolveBoard(input map[string]interface{}) string {
	if boardID := getStr(input, "board_id"); boardID != "" {
		return boardID
	}
	if t.config != nil && t.config.DefaultBoardId != "" {
		return t.config.DefaultBoardId
	}
	return ""
}

// resolveBoardForWrite chooses the board id a write-path action
// (create / decompose) should target and guarantees the board exists before
// the caller invokes CreateTask. It extends the read-only resolveBoard
// fallback chain (input → config.default → "") with two protections against
// the silent FK-failure footgun the tasks.board_id REFERENCES task_boards(id)
// constraint creates:
//
//  1. If the LLM supplies a board_id that does not exist in storage, the
//     agent's configured DefaultBoardId is preferred (when set) so an LLM
//     that hallucinates a board id from a branch name or similar string
//     doesn't spawn orphan boards every turn.
//  2. If the chosen id still names a missing board, it is auto-created
//     with a generic name. This mirrors emitter.ensureBoard for the
//     skills overhaul Phase D path; without it the agent's task_board
//     create/decompose calls would FK-fail with no actionable signal to
//     the model (the error surface is just "FOREIGN KEY constraint failed").
//
// An empty resolution is permitted: tasks with NULL board_id are still
// queryable via owner. Callers that need a board must check for "".
func (t *TaskBoardTool) resolveBoardForWrite(ctx context.Context, input map[string]interface{}) (string, error) {
	requested := getStr(input, "board_id")
	fallback := ""
	if t.config != nil {
		fallback = t.config.DefaultBoardId
	}

	chosen := requested
	if chosen != "" {
		if _, err := t.manager.GetBoard(ctx, chosen); err == nil {
			return chosen, nil
		}
		// Requested board is missing. If the agent has a configured default
		// and it exists, rebind silently — an LLM-supplied id is best-effort
		// and a working configured board beats spawning orphan rows.
		if fallback != "" && fallback != requested {
			if _, err := t.manager.GetBoard(ctx, fallback); err == nil {
				zap.L().Info("task_board: rebinding missing board_id to configured default",
					zap.String("agent_id", t.agentID),
					zap.String("requested", requested),
					zap.String("default", fallback))
				return fallback, nil
			}
			// Configured default is also missing; create it instead of the
			// LLM's guess so operators see the documented board id.
			chosen = fallback
		}
		// chosen is either the (still-missing) original requested id or the
		// (still-missing) configured default. Fall through to auto-create.
	} else if fallback != "" {
		chosen = fallback
		if _, err := t.manager.GetBoard(ctx, chosen); err == nil {
			return chosen, nil
		}
	} else {
		// No board requested, no default configured — board-less is fine.
		return "", nil
	}

	name := fmt.Sprintf("auto-created by agent %q", t.agentID)
	if _, err := t.manager.CreateBoard(ctx, &task.TaskBoard{ID: chosen, Name: name}); err != nil {
		// Concurrent ensure may have created it; one more lookup decides.
		if _, gerr := t.manager.GetBoard(ctx, chosen); gerr == nil {
			return chosen, nil
		}
		return "", fmt.Errorf("ensure board %q: %w", chosen, err)
	}
	zap.L().Info("task_board: auto-created board",
		zap.String("agent_id", t.agentID),
		zap.String("board_id", chosen))
	return chosen, nil
}

func taskSummaryMap(tk *task.Task) map[string]interface{} {
	return map[string]interface{}{
		"id":       tk.ID,
		"title":    tk.Title,
		"status":   task.StatusName(tk.Status),
		"priority": task.PriorityName(tk.Priority),
		"assignee": tk.AssigneeAgentID,
	}
}

func taskDetailMap(tk *task.Task) map[string]interface{} {
	return map[string]interface{}{
		"id":                  tk.ID,
		"title":               tk.Title,
		"description":         tk.Description,
		"objective":           tk.Objective,
		"approach":            tk.Approach,
		"acceptance_criteria": tk.AcceptanceCriteria,
		"notes":               tk.Notes,
		"status":              task.StatusName(tk.Status),
		"priority":            task.PriorityName(tk.Priority),
		"category":            task.CategoryName(tk.Category),
		"estimated_effort":    tk.EstimatedEffort,
		"assignee":            tk.AssigneeAgentID,
		"parent_id":           tk.ParentID,
		"board_id":            tk.BoardID,
		"tags":                tk.Tags,
	}
}

func parseDecomposeStrategy(s string) loomv1.DecomposeStrategy {
	switch s {
	case "backward", "BACKWARD":
		return loomv1.DecomposeStrategy_DECOMPOSE_STRATEGY_BACKWARD
	case "forward", "FORWARD":
		return loomv1.DecomposeStrategy_DECOMPOSE_STRATEGY_FORWARD
	case "parallel", "PARALLEL":
		return loomv1.DecomposeStrategy_DECOMPOSE_STRATEGY_PARALLEL
	default:
		return loomv1.DecomposeStrategy_DECOMPOSE_STRATEGY_BACKWARD
	}
}

func parseTaskStatus(s string) loomv1.TaskStatus {
	switch s {
	case "open", "OPEN":
		return loomv1.TaskStatus_TASK_STATUS_OPEN
	case "in_progress", "IN_PROGRESS":
		return loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS
	case "blocked", "BLOCKED":
		return loomv1.TaskStatus_TASK_STATUS_BLOCKED
	case "done", "DONE":
		return loomv1.TaskStatus_TASK_STATUS_DONE
	case "deferred", "DEFERRED":
		return loomv1.TaskStatus_TASK_STATUS_DEFERRED
	case "cancelled", "CANCELLED":
		return loomv1.TaskStatus_TASK_STATUS_CANCELLED
	default:
		return loomv1.TaskStatus_TASK_STATUS_UNSPECIFIED
	}
}

func defaultMaxDepth(config *loomv1.TaskBoardConfig) int {
	if config != nil && config.MaxDepth > 0 {
		return int(config.MaxDepth)
	}
	return 3
}

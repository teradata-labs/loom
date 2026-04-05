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
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/task"
)

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

func (t *TaskBoardTool) Name() string    { return "task_board" }
func (t *TaskBoardTool) Backend() string { return "" }
func (t *TaskBoardTool) Description() string {
	return `Manage tasks with dependency-aware decomposition and kanban tracking.

Actions:
1. decompose - Break a goal into a dependency DAG of subtasks using LLM
2. ready - Get the "ready front": tasks with all dependencies satisfied
3. claim - Atomically claim a task to work on
4. update - Update task notes, approach, or status
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
				Description: "(close) Completion summary",
			},
			"status": {
				Type:        "string",
				Description: "(update/list) Task status: open, in_progress, blocked, done, deferred, cancelled",
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
	boardID := t.resolveBoard(input)

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

	claimed, err := t.manager.ClaimTask(ctx, taskID, t.agentID, t.agentID+"-session")
	if err != nil {
		return errorResult("CLAIM_ERROR", err.Error()), nil
	}

	return jsonResult(map[string]interface{}{
		"action": "claim",
		"task":   taskDetailMap(claimed),
	})
}

func (t *TaskBoardTool) executeUpdate(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	taskID := getStr(input, "task_id")
	if taskID == "" {
		return errorResult("INVALID_PARAMETER", "task_id is required for update"), nil
	}

	existing, err := t.manager.GetTask(ctx, taskID)
	if err != nil {
		return errorResult("NOT_FOUND", err.Error()), nil
	}

	// Append to notes (don't overwrite).
	if notes := getStr(input, "notes"); notes != "" {
		if existing.Notes != "" {
			existing.Notes += "\n" + notes
		} else {
			existing.Notes = notes
		}
	}
	if desc := getStr(input, "description"); desc != "" {
		existing.Description = desc
	}
	if approach := getStr(input, "approach"); approach != "" {
		existing.Approach = approach
	}

	updated, err := t.manager.UpdateTask(ctx, existing, nil)
	if err != nil {
		return errorResult("UPDATE_ERROR", err.Error()), nil
	}

	return jsonResult(map[string]interface{}{
		"action": "update",
		"task":   taskDetailMap(updated),
	})
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

	boardID := t.resolveBoard(input)

	tk := &task.Task{
		Title:           title,
		Description:     getStr(input, "description"),
		Objective:       getStr(input, "objective"),
		Approach:        getStr(input, "approach"),
		Category:        task.ParseCategory(getStr(input, "category")),
		Priority:        task.ParsePriority(getStr(input, "priority")),
		EstimatedEffort: getStr(input, "estimated_effort"),
		Tags:            getStrSlice(input, "tags"),
		Status:          loomv1.TaskStatus_TASK_STATUS_OPEN,
		OwnerAgentID:    t.agentID,
		BoardID:         boardID,
		ParentID:        getStr(input, "parent_id"),
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

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

package orchestration

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/task"
)

// TaskTrackedOrchestrator wraps an Orchestrator to persist workflow execution
// state via the task system. Each workflow pattern execution creates a board
// with tasks for each stage/agent, providing:
//
//   - Persistent state: survives server restarts (SQLite/PostgreSQL)
//   - Progress visibility: board shows stage status in real-time
//   - Audit trail: full history of stage transitions
//   - Stage output capture: stored in task notes
//   - Resume capability: find last completed stage on restart
//   - Graph memory: auto-creates memories for completed stages
type TaskTrackedOrchestrator struct {
	inner   *Orchestrator
	manager *task.Manager
	tracer  observability.Tracer
	logger  *zap.Logger
}

// NewTaskTrackedOrchestrator wraps an orchestrator with task tracking.
func NewTaskTrackedOrchestrator(inner *Orchestrator, manager *task.Manager, tracer observability.Tracer, logger *zap.Logger) *TaskTrackedOrchestrator {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &TaskTrackedOrchestrator{inner: inner, manager: manager, tracer: tracer, logger: logger}
}

// ExecutePattern wraps the inner orchestrator's ExecutePattern with task tracking.
// Before execution: creates a board and tasks from the pattern.
// After execution: closes tasks with stage outputs and records results.
func (t *TaskTrackedOrchestrator) ExecutePattern(ctx context.Context, pattern *loomv1.WorkflowPattern) (*loomv1.WorkflowResult, error) {
	ctx, span := t.tracer.StartSpan(ctx, "task_tracked.execute_pattern")
	defer t.tracer.EndSpan(span)

	patternType := GetPatternType(pattern)

	// Check for resumable board from a prior execution of this workflow.
	boardID, resumeStage := t.findResumableBoard(ctx, patternType)

	// Create board + tasks if no prior board exists.
	var stageTasks []*task.Task
	if boardID == "" {
		var err error
		_, stageTasks, err = t.createBoardFromPattern(ctx, patternType, pattern)
		if err != nil {
			t.logger.Warn("task tracking: failed to create board, executing without tracking",
				zap.Error(err))
			return t.inner.ExecutePattern(ctx, pattern)
		}
	} else {
		t.logger.Info("task tracking: resuming from prior execution",
			zap.String("board_id", boardID),
			zap.Int("resume_stage", resumeStage))
		// Load existing tasks for the board.
		var err error
		stageTasks, _, err = t.manager.ListTasks(ctx, task.ListTasksOpts{
			BoardID: boardID,
			Limit:   100,
		})
		if err != nil {
			t.logger.Warn("task tracking: failed to load resume tasks",
				zap.Error(err))
		}
	}

	// Mark IN_PROGRESS tasks that correspond to stages about to execute.
	t.markStagesInProgress(ctx, stageTasks, patternType)

	// Execute the actual workflow.
	result, err := t.inner.ExecutePattern(ctx, pattern)

	// Record results into tasks regardless of success/failure.
	t.recordResults(ctx, stageTasks, result, err)

	return result, err
}

// Orchestrator returns the wrapped orchestrator for direct access.
func (t *TaskTrackedOrchestrator) Orchestrator() *Orchestrator {
	return t.inner
}

// =============================================================================
// Board + Task Creation from Pattern
// =============================================================================

// createBoardFromPattern creates a task board and tasks that mirror the workflow structure.
func (t *TaskTrackedOrchestrator) createBoardFromPattern(
	ctx context.Context, patternType string, pattern *loomv1.WorkflowPattern,
) (*task.TaskBoard, []*task.Task, error) {

	boardName := fmt.Sprintf("workflow:%s:%s", patternType, time.Now().Format("20060102-150405"))
	board, err := t.manager.CreateBoard(ctx, &task.TaskBoard{
		Name: boardName,
		Metadata: map[string]string{
			"pattern_type": patternType,
			"created_by":   "task_tracked_orchestrator",
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create board: %w", err)
	}

	var tasks []*task.Task

	switch p := pattern.Pattern.(type) {
	case *loomv1.WorkflowPattern_Pipeline:
		tasks, err = t.createPipelineTasks(ctx, board.ID, p.Pipeline)
	case *loomv1.WorkflowPattern_ForkJoin:
		tasks, err = t.createForkJoinTasks(ctx, board.ID, p.ForkJoin)
	case *loomv1.WorkflowPattern_Parallel:
		tasks, err = t.createParallelTasks(ctx, board.ID, p.Parallel)
	case *loomv1.WorkflowPattern_Conditional:
		tasks, err = t.createConditionalTasks(ctx, board.ID, p.Conditional)
	case *loomv1.WorkflowPattern_Iterative:
		tasks, err = t.createPipelineTasks(ctx, board.ID, p.Iterative.Pipeline)
	case *loomv1.WorkflowPattern_Swarm:
		tasks, err = t.createSwarmTasks(ctx, board.ID, p.Swarm)
	default:
		// Unknown pattern — create a single task.
		tk, createErr := t.manager.CreateTask(ctx, &task.Task{
			Title:    fmt.Sprintf("%s workflow execution", patternType),
			BoardID:  board.ID,
			Category: loomv1.TaskCategory_TASK_CATEGORY_IMPLEMENTATION,
			Priority: loomv1.TaskPriority_TASK_PRIORITY_MEDIUM,
			Status:   loomv1.TaskStatus_TASK_STATUS_OPEN,
		})
		if createErr == nil {
			tasks = []*task.Task{tk}
		}
		err = createErr
	}

	if err != nil {
		return nil, nil, fmt.Errorf("create tasks for %s: %w", patternType, err)
	}

	t.logger.Info("task tracking: board created",
		zap.String("board_id", board.ID),
		zap.String("pattern", patternType),
		zap.Int("tasks", len(tasks)))

	return board, tasks, nil
}

// createPipelineTasks creates sequential tasks with dependencies.
func (t *TaskTrackedOrchestrator) createPipelineTasks(ctx context.Context, boardID string, pipeline *loomv1.PipelinePattern) ([]*task.Task, error) {
	var tasks []*task.Task
	var prevTaskID string

	for i, stage := range pipeline.Stages {
		tk, err := t.manager.CreateTask(ctx, &task.Task{
			Title:       fmt.Sprintf("Stage %d: %s", i+1, stage.AgentId),
			Description: stage.PromptTemplate,
			Objective:   fmt.Sprintf("Complete pipeline stage %d", i+1),
			BoardID:     boardID,
			Category:    loomv1.TaskCategory_TASK_CATEGORY_IMPLEMENTATION,
			Priority:    loomv1.TaskPriority_TASK_PRIORITY_HIGH,
			Status:      loomv1.TaskStatus_TASK_STATUS_OPEN,
			Tags:        []string{"workflow", "pipeline", fmt.Sprintf("stage-%d", i+1)},
			Metadata: map[string]string{
				"agent_id":    stage.AgentId,
				"stage_index": fmt.Sprintf("%d", i),
			},
		})
		if err != nil {
			return nil, fmt.Errorf("create stage %d task: %w", i+1, err)
		}
		tasks = append(tasks, tk)

		// Chain dependency: each stage depends on the previous.
		if prevTaskID != "" {
			err = t.manager.AddDependency(ctx, &task.TaskDependency{
				FromTaskID: tk.ID,
				ToTaskID:   prevTaskID,
				Type:       loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_BLOCKS,
			})
			if err != nil {
				return nil, fmt.Errorf("add pipeline dependency %d→%d: %w", i+1, i, err)
			}
		}
		prevTaskID = tk.ID
	}
	return tasks, nil
}

// createForkJoinTasks creates parallel tasks + a merge task that depends on all.
func (t *TaskTrackedOrchestrator) createForkJoinTasks(ctx context.Context, boardID string, fj *loomv1.ForkJoinPattern) ([]*task.Task, error) {
	var tasks []*task.Task
	var parallelIDs []string

	for i, agentID := range fj.AgentIds {
		tk, err := t.manager.CreateTask(ctx, &task.Task{
			Title:       fmt.Sprintf("Fork agent %d: %s", i+1, agentID),
			Description: fj.Prompt,
			Objective:   "Complete parallel execution",
			BoardID:     boardID,
			Category:    loomv1.TaskCategory_TASK_CATEGORY_ANALYSIS,
			Priority:    loomv1.TaskPriority_TASK_PRIORITY_HIGH,
			Status:      loomv1.TaskStatus_TASK_STATUS_OPEN,
			Tags:        []string{"workflow", "fork-join", "parallel"},
			Metadata:    map[string]string{"agent_id": agentID},
		})
		if err != nil {
			return nil, fmt.Errorf("create fork task %d: %w", i+1, err)
		}
		tasks = append(tasks, tk)
		parallelIDs = append(parallelIDs, tk.ID)
	}

	// Create merge task that depends on all parallel tasks.
	mergeTk, err := t.manager.CreateTask(ctx, &task.Task{
		Title:    fmt.Sprintf("Join: merge %d results (%s)", len(fj.AgentIds), fj.MergeStrategy.String()),
		BoardID:  boardID,
		Category: loomv1.TaskCategory_TASK_CATEGORY_REVIEW,
		Priority: loomv1.TaskPriority_TASK_PRIORITY_HIGH,
		Status:   loomv1.TaskStatus_TASK_STATUS_OPEN,
		Tags:     []string{"workflow", "fork-join", "merge"},
		Metadata: map[string]string{"merge_strategy": fj.MergeStrategy.String()},
	})
	if err != nil {
		return nil, fmt.Errorf("create merge task: %w", err)
	}
	tasks = append(tasks, mergeTk)

	for _, pid := range parallelIDs {
		err = t.manager.AddDependency(ctx, &task.TaskDependency{
			FromTaskID: mergeTk.ID,
			ToTaskID:   pid,
			Type:       loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_BLOCKS,
		})
		if err != nil {
			return nil, fmt.Errorf("add merge dependency: %w", err)
		}
	}

	return tasks, nil
}

// createParallelTasks creates independent tasks (no dependencies).
func (t *TaskTrackedOrchestrator) createParallelTasks(ctx context.Context, boardID string, par *loomv1.ParallelPattern) ([]*task.Task, error) {
	var tasks []*task.Task
	for i, agentTask := range par.Tasks {
		tk, err := t.manager.CreateTask(ctx, &task.Task{
			Title:       fmt.Sprintf("Parallel task %d: %s", i+1, agentTask.AgentId),
			Description: agentTask.Prompt,
			BoardID:     boardID,
			Category:    loomv1.TaskCategory_TASK_CATEGORY_IMPLEMENTATION,
			Priority:    loomv1.TaskPriority_TASK_PRIORITY_MEDIUM,
			Status:      loomv1.TaskStatus_TASK_STATUS_OPEN,
			Tags:        []string{"workflow", "parallel"},
			Metadata:    map[string]string{"agent_id": agentTask.AgentId},
		})
		if err != nil {
			return nil, fmt.Errorf("create parallel task %d: %w", i+1, err)
		}
		tasks = append(tasks, tk)
	}
	return tasks, nil
}

// createConditionalTasks creates a classifier task + branch tasks.
func (t *TaskTrackedOrchestrator) createConditionalTasks(ctx context.Context, boardID string, cond *loomv1.ConditionalPattern) ([]*task.Task, error) {
	var tasks []*task.Task

	// Classifier task.
	classifierTk, err := t.manager.CreateTask(ctx, &task.Task{
		Title:    fmt.Sprintf("Classify: %s", cond.ConditionAgentId),
		BoardID:  boardID,
		Category: loomv1.TaskCategory_TASK_CATEGORY_DECISION,
		Priority: loomv1.TaskPriority_TASK_PRIORITY_HIGH,
		Status:   loomv1.TaskStatus_TASK_STATUS_OPEN,
		Tags:     []string{"workflow", "conditional", "classifier"},
		Metadata: map[string]string{"agent_id": cond.ConditionAgentId},
	})
	if err != nil {
		return nil, fmt.Errorf("create classifier task: %w", err)
	}
	tasks = append(tasks, classifierTk)

	// Branch tasks (all depend on classifier).
	for branchKey := range cond.Branches {
		branchTk, err := t.manager.CreateTask(ctx, &task.Task{
			Title:    fmt.Sprintf("Branch: %s", branchKey),
			BoardID:  boardID,
			Category: loomv1.TaskCategory_TASK_CATEGORY_IMPLEMENTATION,
			Priority: loomv1.TaskPriority_TASK_PRIORITY_MEDIUM,
			Status:   loomv1.TaskStatus_TASK_STATUS_OPEN,
			Tags:     []string{"workflow", "conditional", "branch", branchKey},
			Metadata: map[string]string{"branch_key": branchKey},
		})
		if err != nil {
			return nil, fmt.Errorf("create branch task %s: %w", branchKey, err)
		}
		tasks = append(tasks, branchTk)

		err = t.manager.AddDependency(ctx, &task.TaskDependency{
			FromTaskID: branchTk.ID,
			ToTaskID:   classifierTk.ID,
			Type:       loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_BLOCKS,
		})
		if err != nil {
			return nil, fmt.Errorf("add branch dependency: %w", err)
		}
	}

	return tasks, nil
}

// createSwarmTasks creates a task per voting agent + a decision task.
func (t *TaskTrackedOrchestrator) createSwarmTasks(ctx context.Context, boardID string, swarm *loomv1.SwarmPattern) ([]*task.Task, error) {
	var tasks []*task.Task
	var voteIDs []string

	for i, agentID := range swarm.AgentIds {
		tk, err := t.manager.CreateTask(ctx, &task.Task{
			Title:       fmt.Sprintf("Vote %d: %s", i+1, agentID),
			Description: swarm.Question,
			BoardID:     boardID,
			Category:    loomv1.TaskCategory_TASK_CATEGORY_DECISION,
			Priority:    loomv1.TaskPriority_TASK_PRIORITY_HIGH,
			Status:      loomv1.TaskStatus_TASK_STATUS_OPEN,
			Tags:        []string{"workflow", "swarm", "vote"},
			Metadata:    map[string]string{"agent_id": agentID},
		})
		if err != nil {
			return nil, fmt.Errorf("create vote task %d: %w", i+1, err)
		}
		tasks = append(tasks, tk)
		voteIDs = append(voteIDs, tk.ID)
	}

	// Decision task depends on all votes.
	decisionTk, err := t.manager.CreateTask(ctx, &task.Task{
		Title:    fmt.Sprintf("Swarm decision: %s", swarm.Strategy.String()),
		BoardID:  boardID,
		Category: loomv1.TaskCategory_TASK_CATEGORY_DECISION,
		Priority: loomv1.TaskPriority_TASK_PRIORITY_CRITICAL,
		Status:   loomv1.TaskStatus_TASK_STATUS_OPEN,
		Tags:     []string{"workflow", "swarm", "decision"},
	})
	if err != nil {
		return nil, fmt.Errorf("create decision task: %w", err)
	}
	tasks = append(tasks, decisionTk)

	for _, vid := range voteIDs {
		err = t.manager.AddDependency(ctx, &task.TaskDependency{
			FromTaskID: decisionTk.ID,
			ToTaskID:   vid,
			Type:       loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_BLOCKS,
		})
		if err != nil {
			return nil, fmt.Errorf("add decision dependency: %w", err)
		}
	}

	return tasks, nil
}

// =============================================================================
// Result Recording
// =============================================================================

// recordResults maps workflow results back to tasks, closing them with outputs.
func (t *TaskTrackedOrchestrator) recordResults(
	ctx context.Context,
	stageTasks []*task.Task,
	result *loomv1.WorkflowResult,
	execErr error,
) {
	if result == nil {
		// Workflow failed entirely — mark all IN_PROGRESS tasks as failed.
		for _, tk := range stageTasks {
			if tk.Status == loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS {
				reason := "workflow execution failed"
				if execErr != nil {
					reason = fmt.Sprintf("workflow failed: %s", execErr.Error())
				}
				if _, err := t.manager.CloseTask(ctx, tk.ID, reason); err != nil {
					t.logger.Warn("task tracking: failed to close task on error",
						zap.String("task_id", tk.ID), zap.Error(err))
				}
			}
		}
		return
	}

	// Map agent results to tasks by index or agent_id.
	for i, agentResult := range result.AgentResults {
		if i >= len(stageTasks) {
			break
		}
		tk := stageTasks[i]

		// Skip tasks already closed (from resume).
		if tk.Status == loomv1.TaskStatus_TASK_STATUS_DONE {
			continue
		}

		// Update notes with the stage output.
		output := agentResult.Output
		if len(output) > 1000 {
			output = output[:1000] + "\n[output truncated]"
		}
		tk.Notes = fmt.Sprintf("[%s] Stage completed\nOutput: %s",
			time.Now().Format("2006-01-02 15:04"), output)
		if _, err := t.manager.UpdateTask(ctx, tk, nil); err != nil {
			t.logger.Warn("task tracking: failed to update task notes",
				zap.String("task_id", tk.ID), zap.Error(err))
		}

		// Close the task.
		reason := fmt.Sprintf("completed by agent %s", agentResult.AgentId)
		if _, err := t.manager.CloseTask(ctx, tk.ID, reason); err != nil {
			t.logger.Warn("task tracking: failed to close task",
				zap.String("task_id", tk.ID), zap.Error(err))
		}
	}
}

// =============================================================================
// Resume Support
// =============================================================================

// markStagesInProgress transitions OPEN tasks to IN_PROGRESS before execution.
func (t *TaskTrackedOrchestrator) markStagesInProgress(ctx context.Context, tasks []*task.Task, patternType string) {
	for _, tk := range tasks {
		if tk.Status == loomv1.TaskStatus_TASK_STATUS_OPEN {
			if _, err := t.manager.ClaimTask(ctx, tk.ID, "workflow:"+patternType, "workflow-executor"); err != nil {
				// Non-fatal — task may already be claimed or blocked.
				t.logger.Debug("task tracking: could not claim task",
					zap.String("task_id", tk.ID), zap.Error(err))
			}
		}
	}
}

// findResumableBoard looks for a board from a prior execution that has
// incomplete tasks. Returns the board ID and the index of the first
// non-DONE task (resume point), or ("", 0) if no resumable board exists.
func (t *TaskTrackedOrchestrator) findResumableBoard(ctx context.Context, patternType string) (string, int) {
	boards, err := t.manager.ListBoards(ctx)
	if err != nil {
		return "", 0
	}

	for _, b := range boards {
		if b.Metadata["pattern_type"] != patternType || b.Metadata["created_by"] != "task_tracked_orchestrator" {
			continue
		}

		tasks, _, err := t.manager.ListTasks(ctx, task.ListTasksOpts{
			BoardID: b.ID,
			Limit:   100,
		})
		if err != nil || len(tasks) == 0 {
			continue
		}

		// Check if there are incomplete tasks.
		hasIncomplete := false
		resumeIdx := 0
		for i, tk := range tasks {
			if tk.Status == loomv1.TaskStatus_TASK_STATUS_DONE {
				resumeIdx = i + 1
			} else {
				hasIncomplete = true
			}
		}

		if hasIncomplete && resumeIdx > 0 {
			// Found a board with some done and some incomplete — resumable.
			return b.ID, resumeIdx
		}
	}

	return "", 0
}

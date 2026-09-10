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
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/task"
	"github.com/teradata-labs/loom/pkg/taskctx"
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
		board, created, err := t.createBoardFromPattern(ctx, patternType, pattern)
		if err != nil {
			t.logger.Warn("task tracking: failed to create board, executing without tracking",
				zap.Error(err))
			return t.inner.ExecutePattern(ctx, pattern)
		}
		stageTasks = created
		// Keep the new board's id: the root-task lookup below is shared with
		// the resume path, and previously this board was discarded.
		if board != nil {
			boardID = board.ID
		}
	} else {
		t.logger.Info("task tracking: resuming from prior execution",
			zap.String("board_id", boardID),
			zap.Int("resume_stage", resumeStage))
		// Load existing tasks for the board — paged, because a single
		// Limit:100 read silently dropped stages 100+ from the mapping.
		var err error
		stageTasks, err = t.listAllBoardTasks(ctx, boardID)
		if err != nil {
			t.logger.Warn("task tracking: failed to load resume tasks",
				zap.Error(err))
		}
		// The root is excluded so it cannot appear in the stage slice at all,
		// and result mapping is by each row's stage_index metadata rather than
		// by position — the listing's (priority, created_at) order has no
		// unique tiebreak at second resolution, so position was undefined for
		// any same-second rows of equal priority and actively wrong whenever a
		// higher-priority structural row sorted first.
		stageTasks = excludeWorkflowRootTask(stageTasks)
	}

	// Mark IN_PROGRESS tasks that correspond to stages about to execute.
	t.markStagesInProgress(ctx, stageTasks, patternType)

	// Attribute the run's conversation to the root task, so the messages the
	// stage agents write are findable per task instead of only per session.
	// Without this the stage tasks show lifecycle transitions and nothing else.
	//
	// Installed only when a root exists — on both the fresh and resumed paths,
	// found by its marker rather than threaded through, so a resumed run
	// attributes to the same task the original run did.
	if root := t.findRootTask(ctx, boardID); root != nil {
		ctx = taskctx.ContextWithAttribution(ctx, taskctx.Attribution{
			TaskID:  root.ID,
			BoardID: root.BoardID,
			AgentID: root.OwnerAgentID,
		})
	}

	// Execute the actual workflow.
	result, err := t.inner.ExecutePattern(ctx, pattern)

	// A HITL gate suspension is not a failure — the run resumes later against
	// the same board, so leave task state untouched instead of closing
	// in-progress tasks as failed.
	var suspended *WorkflowSuspended
	if errors.As(err, &suspended) {
		t.logger.Info("task tracking: workflow suspended at HITL gate; board left open",
			zap.String("gated_stage", suspended.Checkpoint.GetPendingGate().GetStageAgentId()))
		return result, err
	}

	// Record results into tasks regardless of success/failure.
	t.recordResults(ctx, stageTasks, result, err)

	// Close the root task too. recordResults deliberately walks only stageTasks
	// — the root is excluded there so it cannot shift the by-index mapping from
	// agent results to stages — which left it IN_PROGRESS forever after its
	// children finished. Beyond looking wrong on a board, an open root makes
	// every stage that depends on it read as BLOCKED in the dependency-graph
	// query, so the count grew with every run.
	//
	// Both paths enforce that exclusion now: the fresh path because
	// createBoardFromPattern returns stages only and the root is appended
	// nowhere, the resume path because it filters the board listing through
	// excludeWorkflowRootTask before recording anything.
	t.closeRootTask(ctx, boardID, result, err)

	return result, err
}

// closeRootTask settles the run's terminal bookkeeping once its stages have
// been recorded: the root row, and any structural rows the run left open.
//
// On a bounded, cancellation-proof context: this runs after the workflow ended,
// and when the run ended BECAUSE the caller cancelled, the request context is
// already dead — findRootTask's ListTasks failed on it and returned nil, so
// every cancelled run leaked a board plus a permanently-IN_PROGRESS root that
// findResumableBoard could never reclaim. Same treatment the agent turn's
// close got (WithoutCancel keeps the context's values; the deadline keeps
// cleanup bounded).
//
// A failed run is CANCELLED, not closed: CloseTask records DONE unconditionally
// and feeds graph memory a completion, so a failed workflow taught the memory
// that this work succeeds and the board showed a green root over dead stages.
//
// Root only when still IN_PROGRESS, so a resumed run that already closed it is
// not reopened and re-closed. Errors are logged, not returned — but note the
// stages are only a truthful record of the failure because markStagesInProgress
// keeps the claimed rows fresh; with stale OPEN statuses the failure branch of
// recordResults cancelled nothing.
func (t *TaskTrackedOrchestrator) closeRootTask(
	ctx context.Context, boardID string, result *loomv1.WorkflowResult, execErr error,
) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rootCloseDeadline)
	defer cancel()

	failed := execErr != nil || result == nil

	// Structural rows (merge, decision, branches) close as a side effect of
	// the run completing — nothing else ever closes them, and while one stayed
	// open, findResumableBoard read the board as unfinished work and the next
	// run of the same pattern recorded nothing at all (reproduced for
	// fork_join, swarm and conditional). On failure they stay open: the board
	// is genuinely unfinished and resumable.
	if !failed {
		if all, err := t.listAllBoardTasks(ctx, boardID); err == nil {
			for _, tk := range all {
				if tk.Metadata[structuralMetadataKey] != "true" || task.IsTerminal(tk.Status) {
					continue
				}
				if _, err := t.manager.CloseTask(ctx, tk.ID, "completed as part of the workflow run"); err != nil {
					t.logger.Warn("task tracking: failed to close structural task",
						zap.String("task_id", tk.ID), zap.Error(err))
				}
			}
		}
	}

	root := t.findRootTask(ctx, boardID)
	if root == nil || root.Status != loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS {
		return
	}

	if failed {
		reason := "workflow execution failed"
		if execErr != nil {
			reason = fmt.Sprintf("workflow failed: %s", execErr.Error())
		}
		if _, err := t.manager.CancelTask(ctx, root.ID, reason); err != nil {
			t.logger.Warn("task tracking: failed to cancel workflow root task",
				zap.String("task_id", root.ID), zap.Error(err))
		}
		return
	}

	if _, err := t.manager.CloseTask(ctx, root.ID, "workflow completed"); err != nil {
		t.logger.Warn("task tracking: failed to close workflow root task",
			zap.String("task_id", root.ID), zap.Error(err))
	}
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

	// The root task owns the workflow's ACTIVITY; the stage tasks own its
	// STRUCTURE.
	//
	// Stage tasks already record lifecycle (opened, in progress, closed with
	// output), but nothing the stage agents actually did — the inner
	// orchestrator runs every stage under one context, so there is no single
	// stage a message could honestly be attributed to. Attributing the run to
	// one root task instead keeps attribution truthful and gives a reader one
	// timeline holding the whole run. Per-stage drill-down comes from the
	// AgentID already on each timeline event, since every stage is a distinct
	// agent.
	//
	// Failure here is not fatal: without a root task the run proceeds exactly
	// as it did before, with stage lifecycle and no activity attribution.
	root, rootErr := t.manager.CreateTask(ctx, &task.Task{
		Title:      fmt.Sprintf("%s workflow", patternType),
		BoardID:    board.ID,
		Category:   loomv1.TaskCategory_TASK_CATEGORY_IMPLEMENTATION,
		Priority:   loomv1.TaskPriority_TASK_PRIORITY_MEDIUM,
		Status:     loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
		CreatedVia: taskctx.CreatedViaImplicit,
		// ClaimedBySession is deliberately NOT set. A workflow run is not owned
		// by a conversation session, and an earlier version put the board NAME
		// in this field — which is a session-id column, so it silently broke any
		// session-scoped query that matched on it. The board name lives in
		// metadata instead, where it is a label rather than a false join key.
		Metadata: map[string]string{
			workflowRootMetadataKey: "true",
			"pattern_type":          patternType,
			"board_name":            boardName,
		},
	})
	if rootErr != nil {
		t.logger.Warn("task tracking: no workflow root task; stage lifecycle only",
			zap.String("board_id", board.ID), zap.Error(rootErr))
	} else {
		t.linkStagesToRoot(ctx, root, tasks)
	}

	t.logger.Info("task tracking: board created",
		zap.String("board_id", board.ID),
		zap.String("pattern", patternType),
		zap.Int("tasks", len(tasks)),
		zap.Bool("root_task", rootErr == nil))

	return board, tasks, nil
}

// agentLabel resolves an agent id to something a human can read.
//
// A pattern stage carries only the agent's ID, so titles read
// "Stage 1: 9abdafb6-879d-41f5-a2bf-6922f9991279" — technically accurate and
// useless on a board. The orchestrator already holds the registered agents (the
// caller registers them before ExecutePattern runs), so the name is one lookup
// away.
//
// Falls back to a SHORTENED id rather than the full UUID: if the agent is not
// registered, an eight-character prefix still distinguishes stages from each
// other without consuming the whole title.
func (t *TaskTrackedOrchestrator) agentLabel(ctx context.Context, agentID string) string {
	if agentID == "" {
		return "unassigned"
	}
	if t.inner != nil {
		if ag, err := t.inner.GetAgent(ctx, agentID); err == nil && ag != nil {
			if name := ag.GetName(); name != "" {
				return name
			}
		}
	}
	// This value lands in a task TITLE — a proto string field, and invalid
	// UTF-8 fails proto.Marshal for the whole list response, not just the row.
	return cutAtRuneBoundary(agentID, shortAgentIDLen)
}

// shortAgentIDLen is the prefix agentLabel falls back to for an unregistered
// agent: enough to tell stages apart without consuming the title.
const shortAgentIDLen = 8

// cutAtRuneBoundary returns s truncated to at most n bytes without splitting a
// multi-byte rune. Byte-slicing a string that carries LLM output or an id can
// cut a rune in half; the result is invalid UTF-8, and a proto string field
// holding it fails proto.Marshal for the whole message that contains it.
func cutAtRuneBoundary(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// workflowRootMetadataKey marks the task that owns a workflow run's activity,
// so a resumed run can find it again without a second lookup table.
const workflowRootMetadataKey = "workflow_root"

// stageIndexMetadataKey carries a task's position in the pattern's AGENT
// RESULT order. It is the one reliable mapping key between
// WorkflowResult.AgentResults and the board's rows: store listings order by
// (priority, created_at) with second-resolution timestamps and no unique
// tiebreak, so positional mapping over a listing mis-pairs whenever a
// same-second row of a different priority sorts first — a fork_join's HIGH
// merge task sorted ahead of the forks and consumed a fork's result.
// Only rows that an agent result closes carry it.
const stageIndexMetadataKey = "stage_index"

// agentIDMetadataKey is the writer/reader mapping key that pairs a stage row
// with the WorkflowResult.AgentResults entry that closes it (claimRow). It is
// load-bearing in the same way stageIndexMetadataKey is: a writer that spells
// it differently produces a row no result can ever claim.
const agentIDMetadataKey = "agent_id"

// rootCloseDeadline bounds closeRootTask's detached cleanup. The parent
// context is already cancelled or finished when this runs, so the bound is
// what stops a hung store from pinning the goroutine.
const rootCloseDeadline = 10 * time.Second

// maxStageOutputNotes caps the agent output copied into a stage task's Notes.
// Notes is a proto string field, so the cut must land on a rune boundary — see
// cutAtRuneBoundary.
const maxStageOutputNotes = 1000

// structuralMetadataKey marks rows that mirror the pattern's SHAPE rather than
// an agent's work: a fork_join's merge, a swarm's decision, a conditional's
// branches. No agent result maps to them, so a run is never "incomplete"
// because one is open — they close as a side effect of the run completing.
// Without this distinction, three of the seven pattern types re-read as
// resumable after every successful run, and the next run recorded nothing.
const structuralMetadataKey = "workflow_structural"

// isStageTask reports whether an agent result is expected to close this row.
func isStageTask(tk *task.Task) bool {
	if tk == nil || tk.Metadata == nil {
		return false
	}
	_, ok := tk.Metadata[stageIndexMetadataKey]
	return ok
}

// stageIndexOf returns the row's agent-result index, or -1.
func stageIndexOf(tk *task.Task) int {
	if tk == nil || tk.Metadata == nil {
		return -1
	}
	n, err := strconv.Atoi(tk.Metadata[stageIndexMetadataKey])
	if err != nil {
		return -1
	}
	return n
}

// listAllBoardTasks pages through a board's rows. The single Limit:100 read it
// replaces did two bad things at scale: findRootTask missed a MEDIUM root that
// sorted behind 100+ CRITICAL/HIGH stage rows, and the resume listing silently
// dropped stages 100+ from the mapping.
func (t *TaskTrackedOrchestrator) listAllBoardTasks(ctx context.Context, boardID string) ([]*task.Task, error) {
	const page = 100
	var all []*task.Task
	for offset := 0; ; offset += page {
		batch, _, err := t.manager.ListTasks(ctx, task.ListTasksOpts{
			BoardID: boardID, Limit: page, Offset: offset,
		})
		if err != nil {
			return all, err
		}
		all = append(all, batch...)
		if len(batch) < page {
			return all, nil
		}
	}
}

// isWorkflowRootTask reports whether a task is the run's root bookkeeping task
// rather than one of the pattern's stages.
//
// The single definition of that test: the resumability scan, the root lookup,
// and the resume path's stage filter all depend on it, and a second copy that
// drifted would leave them disagreeing about what counts as a stage.
func isWorkflowRootTask(tk *task.Task) bool {
	return tk != nil && tk.Metadata[workflowRootMetadataKey] == "true"
}

// excludeWorkflowRootTask returns the stage tasks of a board listing.
//
// Relative order is preserved: callers map workflow results onto the result by
// index, so reordering would corrupt exactly what dropping the root protects.
func excludeWorkflowRootTask(tasks []*task.Task) []*task.Task {
	stages := make([]*task.Task, 0, len(tasks))
	for _, tk := range tasks {
		if isWorkflowRootTask(tk) {
			continue
		}
		stages = append(stages, tk)
	}
	return stages
}

// linkStagesToRoot records each stage task as a child of the run's root task.
//
// PARENT_CHILD rather than BLOCKS: the stages are the run's structure, not its
// prerequisites. The pipeline dependencies between consecutive stages are
// separate edges and are left alone.
func (t *TaskTrackedOrchestrator) linkStagesToRoot(ctx context.Context, root *task.Task, stages []*task.Task) {
	for _, st := range stages {
		if st == nil || st.ID == root.ID {
			continue
		}
		if err := t.manager.AddDependency(ctx, &task.TaskDependency{
			FromTaskID: st.ID,
			ToTaskID:   root.ID,
			Type:       loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_PARENT_CHILD,
			Metadata:   map[string]string{"linked_by": "task_tracked_orchestrator"},
		}); err != nil {
			t.logger.Warn("task tracking: stage-to-root link failed",
				zap.String("stage_task_id", st.ID), zap.Error(err))
		}
	}
}

// findRootTask returns the task owning a board's workflow activity, or nil.
//
// Used on the resume path, where the root was created by an earlier process and
// only the board id survives. The marker lives on the task rather than in board
// metadata because the board is created before any task exists, so its metadata
// cannot name one.
func (t *TaskTrackedOrchestrator) findRootTask(ctx context.Context, boardID string) *task.Task {
	// Paged: the MEDIUM root sorts behind every CRITICAL/HIGH stage row, so a
	// single Limit:100 read missed it on any board with 100+ stages (a
	// 120-agent swarm) — the root was then neither attributed nor closed.
	tasks, err := t.listAllBoardTasks(ctx, boardID)
	if err != nil {
		return nil
	}
	for _, tk := range tasks {
		if isWorkflowRootTask(tk) {
			return tk
		}
	}
	return nil
}

// createPipelineTasks creates sequential tasks with dependencies.
func (t *TaskTrackedOrchestrator) createPipelineTasks(ctx context.Context, boardID string, pipeline *loomv1.PipelinePattern) ([]*task.Task, error) {
	var tasks []*task.Task
	var prevTaskID string

	for i, stage := range pipeline.Stages {
		tk, err := t.manager.CreateTask(ctx, &task.Task{
			Title:       fmt.Sprintf("Stage %d: %s", i+1, t.agentLabel(ctx, stage.AgentId)),
			Description: stage.PromptTemplate,
			Objective:   fmt.Sprintf("Complete pipeline stage %d", i+1),
			BoardID:     boardID,
			Category:    loomv1.TaskCategory_TASK_CATEGORY_IMPLEMENTATION,
			Priority:    loomv1.TaskPriority_TASK_PRIORITY_HIGH,
			Status:      loomv1.TaskStatus_TASK_STATUS_OPEN,
			Tags:        []string{"workflow", "pipeline", fmt.Sprintf("stage-%d", i+1)},
			Metadata: map[string]string{
				agentIDMetadataKey:    stage.AgentId,
				stageIndexMetadataKey: strconv.Itoa(i),
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
			Title:       fmt.Sprintf("Fork agent %d: %s", i+1, t.agentLabel(ctx, agentID)),
			Description: fj.Prompt,
			Objective:   "Complete parallel execution",
			BoardID:     boardID,
			Category:    loomv1.TaskCategory_TASK_CATEGORY_ANALYSIS,
			Priority:    loomv1.TaskPriority_TASK_PRIORITY_HIGH,
			Status:      loomv1.TaskStatus_TASK_STATUS_OPEN,
			Tags:        []string{"workflow", "fork-join", "parallel"},
			Metadata: map[string]string{
				agentIDMetadataKey:    agentID,
				stageIndexMetadataKey: strconv.Itoa(i),
			},
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
		Metadata: map[string]string{
			"merge_strategy":      fj.MergeStrategy.String(),
			structuralMetadataKey: "true",
		},
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
			Title:       fmt.Sprintf("Parallel task %d: %s", i+1, t.agentLabel(ctx, agentTask.AgentId)),
			Description: agentTask.Prompt,
			BoardID:     boardID,
			Category:    loomv1.TaskCategory_TASK_CATEGORY_IMPLEMENTATION,
			Priority:    loomv1.TaskPriority_TASK_PRIORITY_MEDIUM,
			Status:      loomv1.TaskStatus_TASK_STATUS_OPEN,
			Tags:        []string{"workflow", "parallel"},
			Metadata: map[string]string{
				agentIDMetadataKey:    agentTask.AgentId,
				stageIndexMetadataKey: strconv.Itoa(i),
			},
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
		Title:    fmt.Sprintf("Classify: %s", t.agentLabel(ctx, cond.ConditionAgentId)),
		BoardID:  boardID,
		Category: loomv1.TaskCategory_TASK_CATEGORY_DECISION,
		Priority: loomv1.TaskPriority_TASK_PRIORITY_HIGH,
		Status:   loomv1.TaskStatus_TASK_STATUS_OPEN,
		Tags:     []string{"workflow", "conditional", "classifier"},
		Metadata: map[string]string{
			agentIDMetadataKey:    cond.ConditionAgentId,
			stageIndexMetadataKey: "0",
		},
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
			// Structural: a branch is a nested PATTERN, not one agent's stage —
			// no single agent result maps to it, and the range over a Go map
			// above makes branch creation order random, so positional mapping
			// here was nondeterministic even on a fresh run. Branch rows close
			// as a side effect of the run completing.
			Metadata: map[string]string{
				"branch_key":          branchKey,
				structuralMetadataKey: "true",
			},
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
			Title:       fmt.Sprintf("Vote %d: %s", i+1, t.agentLabel(ctx, agentID)),
			Description: swarm.Question,
			BoardID:     boardID,
			Category:    loomv1.TaskCategory_TASK_CATEGORY_DECISION,
			Priority:    loomv1.TaskPriority_TASK_PRIORITY_HIGH,
			Status:      loomv1.TaskStatus_TASK_STATUS_OPEN,
			Tags:        []string{"workflow", "swarm", "vote"},
			Metadata: map[string]string{
				agentIDMetadataKey:    agentID,
				stageIndexMetadataKey: strconv.Itoa(i),
			},
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
		Metadata: map[string]string{structuralMetadataKey: "true"},
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
		// Workflow failed entirely. CANCEL rather than close: CloseTask records
		// DONE and feeds graph memory a completion, so a failed run taught the
		// memory that this work succeeds.
		for _, tk := range stageTasks {
			if tk.Status == loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS {
				reason := "workflow execution failed"
				if execErr != nil {
					reason = fmt.Sprintf("workflow failed: %s", execErr.Error())
				}
				if _, err := t.manager.CancelTask(ctx, tk.ID, reason); err != nil {
					t.logger.Warn("task tracking: failed to cancel task on error",
						zap.String("task_id", tk.ID), zap.Error(err))
				}
			}
		}
		return
	}

	// Map agent results to tasks by the result's OWN agent id, never by
	// position in a listing. Two orderings conspired against positional
	// mapping: the listing orders by (priority, created_at) with no unique
	// tiebreak, so a fork_join's HIGH merge row sorted ahead of the forks and
	// consumed fork agent 1's result (the round-4 reproduction); and for the
	// concurrent patterns AgentResults arrive in COMPLETION order, so even a
	// correctly-sorted stage slice mis-pairs whenever the second agent
	// finishes first. The result names its agent; stage rows record theirs at
	// creation; stage_index only breaks ties when one agent appears in
	// several stages (each result then claims the earliest unclaimed row).
	stageRows := make([]*task.Task, 0, len(stageTasks))
	for _, tk := range stageTasks {
		if isStageTask(tk) {
			stageRows = append(stageRows, tk)
		}
	}
	sort.Slice(stageRows, func(a, b int) bool { return stageIndexOf(stageRows[a]) < stageIndexOf(stageRows[b]) })
	claimRow := func(res *loomv1.AgentResult) *task.Task {
		// PRIMARY key: the producer's own task_index AND its agent id. The
		// index disambiguates one agent id appearing in several stages —
		// matching by agent id alone, each duplicate-agent result claimed
		// whichever of the twin rows was still free, writing each output into
		// the other stage's task on the wrong completion order. The agent-id
		// conjunct is what keeps the index trustworthy: task_index values
		// also arrive from OUTSIDE this board's stage set — a conditional
		// returns its selected branch's results as its own (a Parallel
		// branch's index 0 would otherwise claim the classifier's row), and
		// ParallelExecutor merges AgentTask.Metadata over its own task_index
		// key, so a pattern reusing that key redirects the claim. A non-match
		// falls through to the fallback below.
		if idxStr := res.GetMetadata()["task_index"]; idxStr != "" {
			if idx, err := strconv.Atoi(idxStr); err == nil {
				for i, tk := range stageRows {
					if tk != nil && stageIndexOf(tk) == idx && tk.Metadata[agentIDMetadataKey] == res.AgentId {
						stageRows[i] = nil
						return tk
					}
				}
			}
		}
		// FALLBACK for producers that do not stamp task_index (fork_join and
		// swarm executors today): first unclaimed row for the agent, in stage
		// order — exact for distinct agents, first-free for duplicates.
		for i, tk := range stageRows {
			if tk == nil || tk.Metadata[agentIDMetadataKey] != res.AgentId {
				continue
			}
			stageRows[i] = nil
			return tk
		}
		return nil
	}
	for _, agentResult := range result.AgentResults {
		tk := claimRow(agentResult)
		if tk == nil {
			continue
		}

		// Skip tasks already closed (from resume).
		if tk.Status == loomv1.TaskStatus_TASK_STATUS_DONE {
			continue
		}

		// Update notes with the stage output.
		output := agentResult.Output
		if len(output) > maxStageOutputNotes {
			// Rune-safe: Notes is a proto string, and LLM output is far
			// likelier than an agent id to carry multi-byte runes.
			output = cutAtRuneBoundary(output, maxStageOutputNotes) + "\n[output truncated]"
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

// markStagesInProgress transitions OPEN stage tasks to IN_PROGRESS before
// execution. Structural rows (merge, decision, branches) are left OPEN — no
// agent is about to execute them, and claiming them made a completed run's
// board read as abandoned work.
//
// The claimed row is written BACK into the slice: discarding ClaimTask's
// return left every element's Status a stale OPEN, so recordResults' failure
// branch — which only cancels rows it believes are IN_PROGRESS — cancelled
// nothing on a fresh failed run, and a DONE root sat over OPEN stages.
func (t *TaskTrackedOrchestrator) markStagesInProgress(ctx context.Context, tasks []*task.Task, patternType string) {
	for i, tk := range tasks {
		if !isStageTask(tk) || tk.Status != loomv1.TaskStatus_TASK_STATUS_OPEN {
			continue
		}
		claimed, err := t.manager.ClaimTask(ctx, tk.ID, "workflow:"+patternType, "workflow-executor")
		if err != nil {
			// Non-fatal — task may already be claimed or blocked.
			t.logger.Debug("task tracking: could not claim task",
				zap.String("task_id", tk.ID), zap.Error(err))
			continue
		}
		if claimed != nil {
			tasks[i] = claimed
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

		tasks, err := t.listAllBoardTasks(ctx, b.ID)
		if err != nil || len(tasks) == 0 {
			continue
		}

		// Check if there are incomplete STAGE tasks.
		//
		// The root task is skipped on both counts. It is bookkeeping about the
		// run rather than a step of it, so:
		//
		//   - an open root must not make a board look resumable. It is created
		//     IN_PROGRESS and closed only after the stages are recorded, so a
		//     crashed, cancelled, or pre-fix run leaves one open forever — and
		//     that board would then hijack every later run of the same pattern,
		//     which produced a run with no board and no tasks at all.
		//   - it must not advance resumeIdx either. resumeIdx is an index into
		//     the pattern's STAGES; counting a non-stage row would resume the
		//     next run one stage too far along and silently skip work.
		// Only STAGE rows (those an agent result closes) decide resumability.
		// Structural rows — a fork_join's merge, a swarm's decision, a
		// conditional's branches — have no agent result and used to stay open
		// after a successful run, so those three pattern types read as
		// resumable forever: the next run adopted the finished board, its
		// outputs mapped onto already-DONE rows, and it recorded nothing.
		hasIncomplete := false
		resumeIdx := 0
		for _, tk := range tasks {
			if !isStageTask(tk) {
				continue
			}
			if tk.Status == loomv1.TaskStatus_TASK_STATUS_DONE {
				resumeIdx++
			} else {
				hasIncomplete = true
			}
		}

		if hasIncomplete && resumeIdx > 0 {
			// Found a board with some done and some incomplete — resumable.
			// resumeIdx is the COUNT of done stage rows, diagnostic only: since
			// results map by agent identity rather than position, nothing
			// resumes "at an index" any more — the count is logged so a human
			// can see how far the prior run got, and its exact value is not
			// load-bearing for out-of-order (Parallel) completion.
			return b.ID, resumeIdx
		}
	}

	return "", 0
}

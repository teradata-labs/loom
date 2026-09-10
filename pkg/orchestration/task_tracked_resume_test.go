// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/task"
)

func TestTaskTrackedResume_RootTaskExcludedFromStageMapping(t *testing.T) {
	ctx := context.Background()

	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "tasks.db")+"?_fk=1&_journal_mode=WAL")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mig, err := sqlite.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, mig.MigrateUp(ctx))

	// The RAW store, deliberately: an earlier version wrapped it in
	// resumeOrderStore, which re-sorted stage tasks into creation order before
	// the code under test saw them — manufacturing exactly the invariant
	// production lacks, so the test passed on both the broken and the fixed
	// mapping (round-4 review finding). Result mapping is now keyed on
	// stage_index metadata, which needs no ordering help.
	mgr := task.NewManager(sqlite.NewTaskStore(db, observability.NewNoOpTracer()), nil, observability.NewNoOpTracer(), zaptest.NewLogger(t))

	o := NewOrchestrator(Config{
		LLMProvider: newMockLLMProvider("merged"),
		TaskManager: mgr,
		Tracer:      observability.NewNoOpTracer(),
		Logger:      zaptest.NewLogger(t),
	})

	// Distinct per-agent outputs: without them a shifted mapping could still
	// satisfy the assertions below.
	o.RegisterAgent("alpha", createMockAgent(t, "alpha", newMockLLMProvider("ALPHA-OUTPUT")))
	o.RegisterAgent("beta", createMockAgent(t, "beta", newMockLLMProvider("BETA-OUTPUT")))
	o.RegisterAgent("gamma", createMockAgent(t, "gamma", newMockLLMProvider("GAMMA-OUTPUT")))

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Parallel{
			Parallel: &loomv1.ParallelPattern{
				Tasks: []*loomv1.AgentTask{
					{AgentId: "alpha", Prompt: "stage one"},
					{AgentId: "beta", Prompt: "stage two"},
					{AgentId: "gamma", Prompt: "stage three"},
				},
				MergeStrategy: loomv1.MergeStrategy_CONCATENATE,
			},
		},
	}

	// The board a crashed earlier run left behind, built by the production
	// creator so the stage and root shape is not a test-local invention.
	// Closing the first stage is what makes the board resumable: findResumableBoard
	// wants at least one DONE stage and one that is not.
	tracked := NewTaskTrackedOrchestrator(o, mgr, observability.NewNoOpTracer(), zaptest.NewLogger(t))
	_, stages, err := tracked.createBoardFromPattern(ctx, GetPatternType(pattern), pattern)
	require.NoError(t, err)
	require.Len(t, stages, 3)
	_, err = mgr.CloseTask(ctx, stages[0].ID, "completed by agent alpha")
	require.NoError(t, err)

	root := tracked.findRootTask(ctx, stages[0].BoardID)
	require.NotNil(t, root, "prior run should have left a workflow root task")

	// Driven through the public entry point: that is what stamps the context as
	// already tracked and what a resumed run really calls.
	result, err := o.ExecutePattern(ctx, pattern)
	require.NoError(t, err)
	require.Len(t, result.AgentResults, 3)

	// Parallel results arrive in completion order, so the expected pairing is
	// read off the result the run actually produced, not off the pattern order.
	seen := make(map[string]bool, len(result.AgentResults))
	for i, r := range result.AgentResults {
		require.NotEmpty(t, r.Output, "agent result %d has no output", i)
		require.False(t, seen[r.Output], "agent outputs must be distinct")
		seen[r.Output] = true
	}

	get := func(id string) *task.Task {
		tk, getErr := mgr.GetTask(ctx, id)
		require.NoError(t, getErr)
		return tk
	}
	stage1, stage2, stage3 := get(stages[0].ID), get(stages[1].ID), get(stages[2].ID)
	rootAfter := get(root.ID)

	// The root is bookkeeping about the run, not a step of it: no stage output
	// may be recorded on it, and it must be closed by closeRootTask rather than
	// by a stage result it swallowed.
	require.Empty(t, rootAfter.Notes, "stage output leaked into the workflow root task")
	require.Equal(t, "workflow completed", rootAfter.CloseReason,
		"root task was closed by a stage result instead of by closeRootTask")

	// Stage 1 completed in the earlier run, so recordResults skips it and its
	// slot in the mapping consumes result 0.
	require.Empty(t, stage1.Notes)
	require.Equal(t, "completed by agent alpha", stage1.CloseReason)

	// Each stage records ITS OWN agent's output — the identity contract.
	// Parallel results arrive in COMPLETION order, so the earlier positional
	// expectation (stage i pairs with AgentResults[i]) was itself the bug this
	// PR's round-4 review found: whichever agent finished first claimed the
	// first stage row. Mapping is by the result's agent id now.
	require.Contains(t, stage2.Notes, "BETA-OUTPUT",
		"stage 2 belongs to beta and must record beta's output")
	require.Contains(t, stage3.Notes, "GAMMA-OUTPUT",
		"stage 3 belongs to gamma and must record gamma's output")
	require.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, stage3.Status,
		"last stage was never recorded: the mapping shifted off the end")
}

// newTrackedRig builds a tracked orchestrator over a real migrated SQLite task
// store with three distinct-output mock agents — the round-4 review rig shape.
func newTrackedRig(t *testing.T) (*TaskTrackedOrchestrator, *Orchestrator, *task.Manager) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "tasks.db")+"?_fk=1&_journal_mode=WAL")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mig, err := sqlite.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, mig.MigrateUp(context.Background()))
	mgr := task.NewManager(sqlite.NewTaskStore(db, observability.NewNoOpTracer()), nil, observability.NewNoOpTracer(), zaptest.NewLogger(t))
	o := NewOrchestrator(Config{
		LLMProvider: newMockLLMProvider("merged"),
		TaskManager: mgr,
		Tracer:      observability.NewNoOpTracer(),
		Logger:      zaptest.NewLogger(t),
	})
	o.RegisterAgent("f1", createMockAgent(t, "f1", newMockLLMProvider("FORK-ONE-OUTPUT")))
	o.RegisterAgent("f2", createMockAgent(t, "f2", newMockLLMProvider("FORK-TWO-OUTPUT")))
	tracked := NewTaskTrackedOrchestrator(o, mgr, observability.NewNoOpTracer(), zaptest.NewLogger(t))
	return tracked, o, mgr
}

func forkJoinPattern() *loomv1.WorkflowPattern {
	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_ForkJoin{
			ForkJoin: &loomv1.ForkJoinPattern{
				AgentIds:      []string{"f1", "f2"},
				Prompt:        "fork the work",
				MergeStrategy: loomv1.MergeStrategy_CONCATENATE,
			},
		},
	}
}

func pipelinePattern() *loomv1.WorkflowPattern {
	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				Stages: []*loomv1.PipelineStage{
					{AgentId: "f1", PromptTemplate: "one"},
					{AgentId: "f2", PromptTemplate: "two"},
				},
			},
		},
	}
}

// TestTaskTrackedResume_ForkJoinRunsDoNotAdoptACompletedBoard is the round-4
// reviewer's three-run reproduction. A fork_join creates MORE rows than there
// are agent results (the merge task), and nothing ever closed the extras — so
// a finished board read as resumable forever: run 2 adopted run 1's board, its
// outputs mapped onto already-DONE rows, and it recorded NOTHING (observed:
// after run 2, still boards=1 tasks=4). Structural rows now close as a side
// effect of the run completing, and resumability counts only stage rows.
//
// The same run also pins the mapping corruption: the HIGH merge row sorted
// ahead of the forks in the (priority, created_at) listing, so positional
// mapping handed fork agent 1's output to the MERGE task. Mapping is now by
// stage_index metadata; the merge row must end with empty notes.
func TestTaskTrackedResume_ForkJoinRunsDoNotAdoptACompletedBoard(t *testing.T) {
	tracked, o, mgr := newTrackedRig(t)
	ctx := context.Background()

	countAll := func() (boards, tasks int) {
		bs, err := mgr.ListBoards(ctx)
		require.NoError(t, err)
		for _, b := range bs {
			ts, listErr := tracked.listAllBoardTasks(ctx, b.ID)
			require.NoError(t, listErr)
			tasks += len(ts)
		}
		return len(bs), tasks
	}

	_, err := o.ExecutePattern(ctx, forkJoinPattern())
	require.NoError(t, err)
	boards1, tasks1 := countAll()
	require.Equal(t, 1, boards1)
	require.Equal(t, 4, tasks1, "2 forks + merge + root")

	// After a successful run, nothing on that board may read as resumable.
	boardID, _ := tracked.findResumableBoard(ctx, GetPatternType(forkJoinPattern()))
	require.Empty(t, boardID, "a completed fork_join board must not read as resumable — the merge row held it open")

	// The mapping did not hand a fork's output to the merge row.
	bs, _ := mgr.ListBoards(ctx)
	rows, err := tracked.listAllBoardTasks(ctx, bs[0].ID)
	require.NoError(t, err)
	for _, tk := range rows {
		switch {
		case tk.Metadata[structuralMetadataKey] == "true":
			require.Empty(t, tk.Notes, "the merge task consumed a fork agent's result: %q", tk.Notes)
			require.Equal(t, "completed as part of the workflow run", tk.CloseReason)
		case isStageTask(tk):
			require.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, tk.Status)
			want := "FORK-ONE-OUTPUT"
			if tk.Metadata["agent_id"] == "f2" {
				want = "FORK-TWO-OUTPUT"
			}
			require.Contains(t, tk.Notes, want, "fork task recorded the wrong agent's output")
		}
	}

	// Run 2 must be a FRESH board that records its own work.
	_, err = o.ExecutePattern(ctx, forkJoinPattern())
	require.NoError(t, err)
	boards2, tasks2 := countAll()
	require.Equal(t, 2, boards2, "run 2 adopted run 1's completed board and recorded nothing")
	require.Equal(t, 8, tasks2)
}

// TestTaskTrackedResume_StructuralRowsDoNotHoldSwarmOrConditionalOpen pins the
// other two patterns from the reviewer's dump (decision task and branch rows
// left OPEN) at the findResumableBoard level, with boards built by the real
// creators.
func TestTaskTrackedResume_StructuralRowsDoNotHoldSwarmOrConditionalOpen(t *testing.T) {
	tracked, _, mgr := newTrackedRig(t)
	ctx := context.Background()

	swarm := &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Swarm{Swarm: &loomv1.SwarmPattern{
		AgentIds: []string{"f1", "f2"}, Question: "vote"}}}
	cond := &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Conditional{Conditional: &loomv1.ConditionalPattern{
		ConditionAgentId: "f1",
		Branches: map[string]*loomv1.WorkflowPattern{
			"yes": forkJoinPattern(), "no": forkJoinPattern(),
		}}}}

	for name, pattern := range map[string]*loomv1.WorkflowPattern{"swarm": swarm, "conditional": cond} {
		_, stages, err := tracked.createBoardFromPattern(ctx, GetPatternType(pattern), pattern)
		require.NoError(t, err, name)
		// Close every STAGE row the way a completed run does; leave structural
		// rows exactly as the pre-fix executor left them (open).
		for _, st := range stages {
			if isStageTask(st) {
				_, err = mgr.CloseTask(ctx, st.ID, "completed by agent")
				require.NoError(t, err, name)
			}
		}
		boardID, _ := tracked.findResumableBoard(ctx, GetPatternType(pattern))
		require.Empty(t, boardID,
			"%s: open structural rows must not make a completed board resumable", name)
	}
}

// TestCloseRootTask_SurvivesCancellationAndRecordsFailureHonestly pins the two
// root-close findings: a run cancelled mid-flight used to leave the root
// IN_PROGRESS forever (the close ran on the already-dead request context), and
// a FAILED run's root was recorded DONE — feeding graph memory a completion
// for a workflow that failed.
func TestCloseRootTask_SurvivesCancellationAndRecordsFailureHonestly(t *testing.T) {
	tracked, _, mgr := newTrackedRig(t)
	ctx := context.Background()

	t.Run("cancelled context still settles the root", func(t *testing.T) {
		board, _, err := tracked.createBoardFromPattern(ctx, "pipeline", pipelinePattern())
		require.NoError(t, err)
		dead, cancel := context.WithCancel(ctx)
		cancel()
		tracked.closeRootTask(dead, board.ID, nil, context.Canceled)
		root := tracked.findRootTask(ctx, board.ID)
		require.NotNil(t, root)
		require.NotEqual(t, loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS, root.Status,
			"a cancelled run must not leak a permanently-IN_PROGRESS root")
	})

	t.Run("a failed run is CANCELLED, not DONE", func(t *testing.T) {
		board, _, err := tracked.createBoardFromPattern(ctx, "pipeline", pipelinePattern())
		require.NoError(t, err)
		tracked.closeRootTask(ctx, board.ID, nil, fmt.Errorf("agent not found for stage 0: ghost"))
		root := tracked.findRootTask(ctx, board.ID)
		require.NotNil(t, root)
		require.Equal(t, loomv1.TaskStatus_TASK_STATUS_CANCELLED, root.Status,
			"a failed workflow's root must not be recorded as a completion")
		require.Contains(t, root.CloseReason, "workflow failed")
		_ = mgr
	})
}

// TestFindRootTask_FindsTheRootBehind100Stages: the MEDIUM root sorts behind
// every HIGH stage row, so the old single Limit:100 read returned rows that
// never included it — on a 120-stage board the root was neither attributed nor
// closed. The listing is paged now.
func TestFindRootTask_FindsTheRootBehind100Stages(t *testing.T) {
	tracked, _, mgr := newTrackedRig(t)
	ctx := context.Background()

	board, err := mgr.CreateBoard(ctx, &task.TaskBoard{ID: "big-board", Name: "big",
		Metadata: map[string]string{"pattern_type": "swarm", "created_by": "task_tracked_orchestrator"}})
	require.NoError(t, err)
	_ = board
	for i := 0; i < 120; i++ {
		_, err = mgr.CreateTask(ctx, &task.Task{
			Title: fmt.Sprintf("Vote %d", i), BoardID: "big-board",
			Priority: loomv1.TaskPriority_TASK_PRIORITY_HIGH,
			Status:   loomv1.TaskStatus_TASK_STATUS_OPEN,
			Metadata: map[string]string{stageIndexMetadataKey: strconv.Itoa(i)},
		})
		require.NoError(t, err)
	}
	_, err = mgr.CreateTask(ctx, &task.Task{
		Title: "swarm workflow", BoardID: "big-board",
		Priority: loomv1.TaskPriority_TASK_PRIORITY_MEDIUM,
		Status:   loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
		Metadata: map[string]string{workflowRootMetadataKey: "true"},
	})
	require.NoError(t, err)

	root := tracked.findRootTask(ctx, "big-board")
	require.NotNil(t, root, "the root behind 100+ higher-priority stage rows was invisible to the unpaged read")
	require.True(t, isWorkflowRootTask(root))
}

// TestTaskTrackedResume_DuplicateAgentParallelMapsByTaskIndex is MINOR-001's
// regression: with the SAME agent id in two parallel stages, agent-id matching
// alone claimed whichever twin row was still free — completion order decided
// which stage got which output. ParallelExecutor stamps task_index on every
// result, and that is the only unambiguous key for duplicate agents; the
// mapping must follow it regardless of completion order.
func TestTaskTrackedResume_DuplicateAgentParallelMapsByTaskIndex(t *testing.T) {
	tracked, o, mgr := newTrackedRig(t)
	ctx := context.Background()

	// One agent, two stages, distinguishable outputs per call: the mock LLM
	// serves its scripted responses in call order.
	o.RegisterAgent("dup", createMockAgent(t, "dup",
		newMockLLMProvider("OUTPUT-FIRST-CALL", "OUTPUT-SECOND-CALL")))

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Parallel{
			Parallel: &loomv1.ParallelPattern{
				Tasks: []*loomv1.AgentTask{
					{AgentId: "dup", Prompt: "stage zero"},
					{AgentId: "dup", Prompt: "stage one"},
				},
				MergeStrategy: loomv1.MergeStrategy_CONCATENATE,
			},
		},
	}

	result, err := o.ExecutePattern(ctx, pattern)
	require.NoError(t, err)
	require.Len(t, result.AgentResults, 2)

	// Ground truth from the results themselves: task_index -> output.
	wantByIndex := map[string]string{}
	for _, r := range result.AgentResults {
		idx := r.GetMetadata()["task_index"]
		require.NotEmpty(t, idx, "ParallelExecutor stamps task_index on every result")
		wantByIndex[idx] = r.Output
	}

	boards, err := mgr.ListBoards(ctx)
	require.NoError(t, err)
	require.Len(t, boards, 1)
	rows, err := tracked.listAllBoardTasks(ctx, boards[0].ID)
	require.NoError(t, err)
	checked := 0
	for _, tk := range rows {
		if !isStageTask(tk) {
			continue
		}
		idx := tk.Metadata[stageIndexMetadataKey]
		require.Contains(t, tk.Notes, wantByIndex[idx],
			"stage %s recorded another stage's output — duplicate-agent mapping must follow task_index, not claim order", idx)
		checked++
	}
	require.Equal(t, 2, checked)
}

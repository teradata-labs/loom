// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/task"
)

// resumeOrderStore fixes the order of a board listing: the workflow root first,
// then the stage tasks in creation order.
//
// Neither store defines the order inside a (priority, created_at) tie, and a
// Parallel board is one big tie: every stage is MEDIUM, the root is MEDIUM, and
// created_at is stored at second precision. Both halves of the fixture matter.
// Putting the root first is the permutation the resume path must survive, and
// would otherwise show up only when the database happened to return it. Pinning
// the stages to creation order is what makes an expected pairing computable at
// all — the store is free to hand those three back in any order too.
//
// The marker is read through the shared constant rather than through the
// production helper: this decorator sets up the condition under test and must
// not depend on the code under test.
type resumeOrderStore struct {
	task.TaskStore

	// stageOrder holds stage task ids in creation order. Written once before
	// the run under test, so later readers see it through goroutine creation.
	stageOrder []string
}

func (s *resumeOrderStore) ListTasks(ctx context.Context, opts task.ListTasksOpts) ([]*task.Task, int, error) {
	tasks, total, err := s.TaskStore.ListTasks(ctx, opts)
	if err != nil {
		return nil, 0, err
	}

	rank := make(map[string]int, len(s.stageOrder))
	for i, id := range s.stageOrder {
		rank[id] = i
	}

	ordered := make([]*task.Task, 0, len(tasks))
	for _, tk := range tasks {
		if tk != nil && tk.Metadata[workflowRootMetadataKey] == "true" {
			ordered = append(ordered, tk)
		}
	}
	staged := make([]*task.Task, len(s.stageOrder))
	var rest []*task.Task
	for _, tk := range tasks {
		if tk == nil || tk.Metadata[workflowRootMetadataKey] == "true" {
			continue
		}
		if i, ok := rank[tk.ID]; ok {
			staged[i] = tk
			continue
		}
		rest = append(rest, tk)
	}
	for _, tk := range staged {
		if tk != nil {
			ordered = append(ordered, tk)
		}
	}
	return append(ordered, rest...), total, nil
}

// TestTaskTrackedResume_RootTaskExcludedFromStageMapping resumes a board whose
// root task does not sort last and checks that each agent result still lands on
// the stage task it belongs to.
//
// recordResults maps result i onto stage task i. The fresh path is safe because
// createBoardFromPattern returns stages only, but the resume path reloads the
// whole board — root included — so a root sitting anywhere but last consumes a
// stage's output, closes itself on it, and shifts every later stage by one,
// dropping the final stage's output.
func TestTaskTrackedResume_RootTaskExcludedFromStageMapping(t *testing.T) {
	ctx := context.Background()

	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "tasks.db")+"?_fk=1&_journal_mode=WAL")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mig, err := sqlite.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, mig.MigrateUp(ctx))

	store := &resumeOrderStore{TaskStore: sqlite.NewTaskStore(db, observability.NewNoOpTracer())}
	mgr := task.NewManager(store, nil, observability.NewNoOpTracer(), zaptest.NewLogger(t))

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
	for _, st := range stages {
		store.stageOrder = append(store.stageOrder, st.ID)
	}
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

	require.Contains(t, stage2.Notes, result.AgentResults[1].Output,
		"stage 2 recorded the wrong agent's output")
	require.Contains(t, stage3.Notes, result.AgentResults[2].Output,
		"stage 3 recorded the wrong agent's output")
	require.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, stage3.Status,
		"last stage was never recorded: the mapping shifted off the end")
}

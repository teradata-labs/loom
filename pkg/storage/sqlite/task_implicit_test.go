// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/task"
	"github.com/teradata-labs/loom/pkg/taskctx"
)

func TestCreatedVia_RoundTrips(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()

	created, err := store.CreateTask(ctx, &task.Task{
		Title:      "implicit turn",
		Status:     loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
		CreatedVia: taskctx.CreatedViaImplicit,
	})
	require.NoError(t, err)

	got, err := store.GetTask(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, taskctx.CreatedViaImplicit, got.CreatedVia)

	// A task created without provenance reads back empty, not as an error.
	bare, err := store.CreateTask(ctx, &task.Task{
		Title:  "legacy",
		Status: loomv1.TaskStatus_TASK_STATUS_OPEN,
	})
	require.NoError(t, err)
	gotBare, err := store.GetTask(ctx, bare.ID)
	require.NoError(t, err)
	assert.Equal(t, "", gotBare.CreatedVia)
}

// TestExcludeCreatedVia_KeepsImplicitTasksOutOfAgentQueries is the
// context-window guarantee, verified in SQL rather than asserted.
//
// The agent's per-turn context block runs four queries. If implicit tasks
// appeared in any of them, a long conversation would inject its own past turns
// into the prompt on every later turn.
func TestExcludeCreatedVia_KeepsImplicitTasksOutOfAgentQueries(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()

	board, err := store.CreateBoard(ctx, &task.TaskBoard{ID: "b1", Name: "board"})
	require.NoError(t, err)

	// One real task the agent is working.
	_, err = store.CreateTask(ctx, &task.Task{
		Title: "real work", BoardID: board.ID,
		Status:     loomv1.TaskStatus_TASK_STATUS_OPEN,
		CreatedVia: taskctx.CreatedViaAgent,
	})
	require.NoError(t, err)

	// Twenty turns of runtime bookkeeping, half of them already closed —
	// exactly what fills the "recent completions" slots.
	for i := 0; i < 20; i++ {
		status := loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS
		if i%2 == 0 {
			status = loomv1.TaskStatus_TASK_STATUS_DONE
		}
		_, err := store.CreateTask(ctx, &task.Task{
			Title: fmt.Sprintf("turn %d", i), BoardID: board.ID,
			Status:     status,
			CreatedVia: taskctx.CreatedViaImplicit,
		})
		require.NoError(t, err)
	}

	exclude := task.ResolveImplicitPolicy(nil).ExcludedCreatedVia()
	require.NotEmpty(t, exclude, "default policy must exclude implicit tasks")

	// Board stats query: total must count only real work.
	all, total, err := store.ListTasks(ctx, task.ListTasksOpts{
		BoardID: board.ID, Limit: 1000, ExcludeCreatedVia: exclude,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, total, "board total must not be inflated by bookkeeping")
	require.Len(t, all, 1)
	assert.Equal(t, "real work", all[0].Title)

	// Recent-completions query: the three slots must not be taken by turns.
	done, _, err := store.ListTasks(ctx, task.ListTasksOpts{
		BoardID: board.ID, Status: loomv1.TaskStatus_TASK_STATUS_DONE,
		Limit: 3, ExcludeCreatedVia: exclude,
	})
	require.NoError(t, err)
	assert.Empty(t, done, "closed implicit tasks must not occupy the recent-completions slots")

	// Ready front: the agent must not be offered its own bookkeeping to claim.
	ready, err := store.GetReadyFront(ctx, board.ID, task.ReadyFrontOpts{
		MaxResults: 10, ExcludeCreatedVia: exclude,
	})
	require.NoError(t, err)
	require.Len(t, ready, 1)
	assert.Equal(t, "real work", ready[0].Title)

	// Without the exclusion everything is visible — proving the filter is what
	// does the work, not some other accident of the fixture.
	_, totalAll, err := store.ListTasks(ctx, task.ListTasksOpts{BoardID: board.ID, Limit: 1000})
	require.NoError(t, err)
	assert.Equal(t, 21, totalAll)
}

// TestImplicitEmitter_EndToEnd exercises the emitter against a real store.
func TestImplicitEmitter_EndToEnd(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	mgr := task.NewManager(store, nil, observability.NewNoOpTracer(), nil)
	ctx := context.Background()

	_, err := store.CreateBoard(ctx, &task.TaskBoard{ID: "b1", Name: "board"})
	require.NoError(t, err)

	em := task.NewImplicitEmitter(mgr, task.ResolveImplicitPolicy(nil), nil, nil)
	req := task.TurnRequest{
		SessionID: "s1", AgentID: "a1", BoardID: "b1", TurnIndex: 0,
		Trigger:     loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_TOOL_CALL,
		UserMessage: "Find the slow queries",
	}

	turnCtx, boundCtx := taskctx.ContextWithBinding(ctx)
	_ = boundCtx

	// First trigger mints the task and attributes the context.
	newCtx, created, err := em.EnsureForTurn(turnCtx, req)
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, "Find the slow queries", created.Title)
	assert.Equal(t, taskctx.CreatedViaImplicit, created.CreatedVia)

	got, ok := taskctx.AttributionFromContext(newCtx)
	require.True(t, ok, "the minted task must be attributed on the context")
	assert.Equal(t, created.ID, got.TaskID)

	// The binding makes it visible through the ORIGINAL turn context too.
	viaBinding, ok := taskctx.AttributionFromContext(turnCtx)
	require.True(t, ok, "the pre-creation context must observe the late binding")
	assert.Equal(t, created.ID, viaBinding.TaskID)

	// A second trigger in the same turn reuses it rather than minting again.
	_, second, err := em.EnsureForTurn(turnCtx, req)
	require.NoError(t, err)
	assert.Nil(t, second, "a second trigger in the same turn must not mint")

	_, total, err := store.ListTasks(ctx, task.ListTasksOpts{BoardID: "b1", Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, 1, total, "exactly one task per turn")

	// A later turn mints a new one.
	req2 := req
	req2.TurnIndex = 1
	req2.UserMessage = "Now fix them"
	ctx2, _ := taskctx.ContextWithBinding(ctx)
	_, third, err := em.EnsureForTurn(ctx2, req2)
	require.NoError(t, err)
	require.NotNil(t, third)
	assert.Equal(t, "Now fix them", third.Title)
}

// TestImplicitEmitter_DeclinesQuietly covers every path where the policy says
// no. None of them is an error: a turn must never fail over bookkeeping.
func TestImplicitEmitter_DeclinesQuietly(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	mgr := task.NewManager(store, nil, observability.NewNoOpTracer(), nil)
	ctx := context.Background()
	_, err := store.CreateBoard(ctx, &task.TaskBoard{ID: "b1", Name: "b"})
	require.NoError(t, err)

	base := task.TurnRequest{SessionID: "s1", AgentID: "a1", BoardID: "b1",
		Trigger: loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_TOOL_CALL}

	t.Run("disabled", func(t *testing.T) {
		em := task.NewImplicitEmitter(mgr, task.ResolveImplicitPolicy(
			&loomv1.ImplicitTaskConfig{Mode: loomv1.ImplicitTaskMode_IMPLICIT_TASK_MODE_DISABLED}), nil, nil)
		_, got, err := em.EnsureForTurn(ctx, base)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("trigger not allowed", func(t *testing.T) {
		em := task.NewImplicitEmitter(mgr, task.ResolveImplicitPolicy(
			&loomv1.ImplicitTaskConfig{Triggers: []loomv1.ImplicitTaskTrigger{
				loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_HUMAN_REQUEST}}), nil, nil)
		_, got, err := em.EnsureForTurn(ctx, base)
		require.NoError(t, err)
		assert.Nil(t, got, "a tool call must not mint when only HUMAN_REQUEST is enabled")
	})

	t.Run("session cap", func(t *testing.T) {
		em := task.NewImplicitEmitter(mgr, task.ResolveImplicitPolicy(
			&loomv1.ImplicitTaskConfig{MaxPerSession: 2}), nil, nil)
		for turn := 0; turn < 5; turn++ {
			r := base
			r.SessionID = "capped"
			r.TurnIndex = turn
			c, _ := taskctx.ContextWithBinding(ctx)
			_, _, err := em.EnsureForTurn(c, r)
			require.NoError(t, err, "hitting the cap must not error")
		}
		_, total, err := store.ListTasks(ctx, task.ListTasksOpts{BoardID: "b1", Limit: 100})
		require.NoError(t, err)
		assert.Equal(t, 2, total, "the cap must bound board growth over a long session")
	})

	t.Run("existing attribution is never shadowed", func(t *testing.T) {
		em := task.NewImplicitEmitter(mgr, task.ResolveImplicitPolicy(nil), nil, nil)
		claimed := taskctx.ContextWithAttribution(ctx,
			taskctx.Attribution{TaskID: "agent-claimed-task"})
		r := base
		r.SessionID = "s-claimed"
		gotCtx, got, err := em.EnsureForTurn(claimed, r)
		require.NoError(t, err)
		assert.Nil(t, got, "a real claimed task must not be shadowed by bookkeeping")
		a, _ := taskctx.AttributionFromContext(gotCtx)
		assert.Equal(t, "agent-claimed-task", a.TaskID)
	})
}

// BenchmarkContextQueriesWithImplicitTasks measures the four queries the agent's
// per-turn context block runs, on a board carrying 1000 turns of implicit
// bookkeeping alongside 20 real tasks.
//
// This is the context-window concern made measurable. "excluded" is what the
// agent actually pays every turn; "included" is what it would pay — and inject
// into the prompt — without the filter.
func BenchmarkContextQueriesWithImplicitTasks(b *testing.B) {
	// Two fixture sizes on purpose. 100 is one session's worth under the
	// default max_per_session cap — the realistic day-one state. 1000 is many
	// sessions sharing a board over months, i.e. the tail, not the norm.
	for _, implicitCount := range []int{100, 1000} {
		b.Run(fmt.Sprintf("implicit-%d", implicitCount), func(b *testing.B) {
			benchContextQueries(b, implicitCount)
		})
	}
}

func benchContextQueries(b *testing.B, implicitCount int) {
	db := migratedDBB(b)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()
	if _, err := store.CreateBoard(ctx, &task.TaskBoard{ID: "b1", Name: "b"}); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := store.CreateTask(ctx, &task.Task{
			Title: fmt.Sprintf("real %d", i), BoardID: "b1",
			Status: loomv1.TaskStatus_TASK_STATUS_OPEN, CreatedVia: taskctx.CreatedViaAgent,
		}); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < implicitCount; i++ {
		st := loomv1.TaskStatus_TASK_STATUS_DONE
		if i%3 == 0 {
			st = loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS
		}
		if _, err := store.CreateTask(ctx, &task.Task{
			Title: fmt.Sprintf("turn %d", i), BoardID: "b1",
			Status: st, CreatedVia: taskctx.CreatedViaImplicit,
		}); err != nil {
			b.Fatal(err)
		}
	}

	run := func(b *testing.B, exclude []string) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			// The four queries buildTaskContext performs each turn.
			if _, _, err := store.ListTasks(ctx, task.ListTasksOpts{
				BoardID: "b1", Status: loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
				Limit: 5, ExcludeCreatedVia: exclude}); err != nil {
				b.Fatal(err)
			}
			if _, err := store.GetReadyFront(ctx, "b1", task.ReadyFrontOpts{
				MaxResults: 5, ExcludeCreatedVia: exclude}); err != nil {
				b.Fatal(err)
			}
			all, total, err := store.ListTasks(ctx, task.ListTasksOpts{
				BoardID: "b1", Limit: 1000, ExcludeCreatedVia: exclude})
			if err != nil {
				b.Fatal(err)
			}
			if i == 0 {
				b.ReportMetric(float64(total), "board_total")
				b.ReportMetric(float64(len(all)), "rows_fetched")
			}
			if _, _, err := store.ListTasks(ctx, task.ListTasksOpts{
				BoardID: "b1", Status: loomv1.TaskStatus_TASK_STATUS_DONE,
				Limit: 3, ExcludeCreatedVia: exclude}); err != nil {
				b.Fatal(err)
			}
		}
	}

	b.Run("excluded", func(b *testing.B) {
		run(b, task.ResolveImplicitPolicy(nil).ExcludedCreatedVia())
	})
	b.Run("included", func(b *testing.B) { run(b, nil) })
}

// migratedDBB is migratedDB for benchmarks (testing.B is not a *testing.T).
func migratedDBB(b *testing.B) *sql.DB {
	b.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(b.TempDir(), "bench.db")+"?_fk=1&_journal_mode=WAL")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	mig, err := NewMigrator(db, observability.NewNoOpTracer())
	if err != nil {
		b.Fatal(err)
	}
	if err := mig.MigrateUp(context.Background()); err != nil {
		b.Fatal(err)
	}
	return db
}

// TestCountByStatus_IsExactAboveTheOldLimit is the correctness half of the fix.
// The previous implementation fetched at most 1000 rows and counted them, so a
// board holding more than that reported truncated numbers with no error.
func TestCountByStatus_IsExactAboveTheOldLimit(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()
	_, err := store.CreateBoard(ctx, &task.TaskBoard{ID: "big", Name: "big"})
	require.NoError(t, err)

	// 1200 tasks — past the old 1000-row ceiling.
	const open, done = 700, 500
	for i := 0; i < open; i++ {
		_, err := store.CreateTask(ctx, &task.Task{
			Title: fmt.Sprintf("o%d", i), BoardID: "big",
			Status: loomv1.TaskStatus_TASK_STATUS_OPEN, CreatedVia: taskctx.CreatedViaAgent})
		require.NoError(t, err)
	}
	for i := 0; i < done; i++ {
		_, err := store.CreateTask(ctx, &task.Task{
			Title: fmt.Sprintf("d%d", i), BoardID: "big",
			Status: loomv1.TaskStatus_TASK_STATUS_DONE, CreatedVia: taskctx.CreatedViaAgent})
		require.NoError(t, err)
	}

	counts, err := store.CountByStatus(ctx, task.CountByStatusOpts{BoardID: "big"})
	require.NoError(t, err)
	assert.Equal(t, open+done, counts.Total, "total must be exact past the old limit")
	assert.Equal(t, open, counts.Open)
	assert.Equal(t, done, counts.Done)

	// The old path, for contrast: capped at its limit.
	_, listTotal, err := store.ListTasks(ctx, task.ListTasksOpts{BoardID: "big", Limit: 1000})
	require.NoError(t, err)
	assert.Equal(t, open+done, listTotal, "the COUNT(*) was always right; the per-row histogram was not")
}

func TestCountByStatus_RespectsScopeAndExclusion(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()
	for _, id := range []string{"b1", "b2"} {
		_, err := store.CreateBoard(ctx, &task.TaskBoard{ID: id, Name: id})
		require.NoError(t, err)
	}

	mk := func(board string, st loomv1.TaskStatus, via string) {
		_, err := store.CreateTask(ctx, &task.Task{
			Title: "t", BoardID: board, Status: st, CreatedVia: via})
		require.NoError(t, err)
	}
	mk("b1", loomv1.TaskStatus_TASK_STATUS_OPEN, taskctx.CreatedViaAgent)
	mk("b1", loomv1.TaskStatus_TASK_STATUS_DONE, taskctx.CreatedViaImplicit)
	mk("b1", loomv1.TaskStatus_TASK_STATUS_BLOCKED, taskctx.CreatedViaImplicit)
	mk("b2", loomv1.TaskStatus_TASK_STATUS_OPEN, taskctx.CreatedViaAgent)

	// Board scope.
	b1, err := store.CountByStatus(ctx, task.CountByStatusOpts{BoardID: "b1"})
	require.NoError(t, err)
	assert.Equal(t, 3, b1.Total)

	// Exclusion — the counts an agent sees must match the tasks it sees.
	agentView, err := store.CountByStatus(ctx, task.CountByStatusOpts{
		BoardID: "b1", ExcludeCreatedVia: []string{taskctx.CreatedViaImplicit}})
	require.NoError(t, err)
	assert.Equal(t, 1, agentView.Total)
	assert.Equal(t, 1, agentView.Open)
	assert.Zero(t, agentView.Done)
	assert.Zero(t, agentView.Blocked)

	// No board scope counts everything.
	all, err := store.CountByStatus(ctx, task.CountByStatusOpts{})
	require.NoError(t, err)
	assert.Equal(t, 4, all.Total)
}

// BenchmarkBoardStats compares the aggregate against the 1000-row histogram it
// replaced, on a board with 1200 tasks.
func BenchmarkBoardStats(b *testing.B) {
	db := migratedDBB(b)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()
	if _, err := store.CreateBoard(ctx, &task.TaskBoard{ID: "b1", Name: "b"}); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 1200; i++ {
		st := loomv1.TaskStatus_TASK_STATUS_OPEN
		if i%2 == 0 {
			st = loomv1.TaskStatus_TASK_STATUS_DONE
		}
		if _, err := store.CreateTask(ctx, &task.Task{
			Title: fmt.Sprintf("t%d", i), BoardID: "b1", Status: st}); err != nil {
			b.Fatal(err)
		}
	}

	b.Run("aggregate", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := store.CountByStatus(ctx, task.CountByStatusOpts{BoardID: "b1"}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("fetch-1000-and-histogram", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			rows, _, err := store.ListTasks(ctx, task.ListTasksOpts{BoardID: "b1", Limit: 1000})
			if err != nil {
				b.Fatal(err)
			}
			counts := map[loomv1.TaskStatus]int{}
			for _, t := range rows {
				counts[t.Status]++
			}
		}
	})
}

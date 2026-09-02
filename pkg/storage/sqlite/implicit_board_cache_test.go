// Copyright 2026 Teradata

package sqlite

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/task"
	"github.com/teradata-labs/loom/pkg/taskctx"
)

// countingBoardStore counts GetBoard calls so a test can prove a round trip did
// not happen, which is the whole point of the board cache.
type countingBoardStore struct {
	task.TaskStore
	getBoardCalls atomic.Int64
}

func (c *countingBoardStore) GetBoard(ctx context.Context, id string) (*task.TaskBoard, error) {
	c.getBoardCalls.Add(1)
	return c.TaskStore.GetBoard(ctx, id)
}

// TestImplicitEmitter_BoardCacheSkipsRepeatProbe pins the optimization.
//
// ensureBoard probed GetBoard on every mint. After a session's first turn that
// probe always hits and always returns the same answer, so it was one of four
// round trips spent re-learning an unchanging fact. Measured against local
// PostgreSQL, removing it took a mint from ~1078us to ~685us.
//
// The test asserts the round trip is GONE, not that the timing improved —
// timing is environment-dependent, call count is not.
func TestImplicitEmitter_BoardCacheSkipsRepeatProbe(t *testing.T) {
	db := migratedDB(t)
	inner := NewTaskStore(db, observability.NewNoOpTracer())
	counting := &countingBoardStore{TaskStore: inner}
	mgr := task.NewManager(counting, nil, observability.NewNoOpTracer(), nil)
	ctx := context.Background()

	_, err := inner.CreateBoard(ctx, &task.TaskBoard{ID: "cache-board", Name: "board"})
	require.NoError(t, err)

	em := task.NewImplicitEmitter(mgr, task.ResolveImplicitPolicy(nil), nil, nil)
	mint := func(session string, turn int) *task.Task {
		t.Helper()
		turnCtx, _ := taskctx.ContextWithBinding(ctx)
		_, created, err := em.EnsureForTurn(turnCtx, task.TurnRequest{
			SessionID: session, AgentID: "a1", BoardID: "cache-board", TurnIndex: turn,
			Trigger:     loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_TOOL_CALL,
			UserMessage: "do the work",
		})
		require.NoError(t, err)
		require.NotNil(t, created, "emitter declined; the test would prove nothing")
		return created
	}

	// First mint learns the board exists — one probe.
	mint("s-1", 0)
	afterFirst := counting.getBoardCalls.Load()
	assert.Equal(t, int64(1), afterFirst, "the first mint should probe the board once")

	// Every later mint, across turns AND across sessions, must not probe again.
	mint("s-1", 1)
	mint("s-1", 2)
	mint("s-2", 0)
	assert.Equal(t, afterFirst, counting.getBoardCalls.Load(),
		"later mints must reuse the cached board instead of re-probing")

	// The tasks are still real, so the saving did not come from skipping work.
	_, total, err := inner.ListTasks(ctx, task.ListTasksOpts{BoardID: "cache-board", Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, 4, total, "all four turns should still have minted a task")
}

// TestImplicitEmitter_BoardCacheSelfHealsAfterCreateFailure covers the risk the
// cache introduces: it vouches for a board from memory, so if that board goes
// away the cache would keep asserting it and every later turn would fail the
// same way. A create failure clears the entry.
func TestImplicitEmitter_BoardCacheSelfHealsAfterCreateFailure(t *testing.T) {
	db := migratedDB(t)
	inner := NewTaskStore(db, observability.NewNoOpTracer())
	counting := &countingBoardStore{TaskStore: inner}
	mgr := task.NewManager(counting, nil, observability.NewNoOpTracer(), nil)
	ctx := context.Background()

	_, err := inner.CreateBoard(ctx, &task.TaskBoard{ID: "heal-board", Name: "board"})
	require.NoError(t, err)

	em := task.NewImplicitEmitter(mgr, task.ResolveImplicitPolicy(nil), nil, nil)
	req := func(turn int) task.TurnRequest {
		return task.TurnRequest{
			SessionID: "s-heal", AgentID: "a1", BoardID: "heal-board", TurnIndex: turn,
			Trigger:     loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_TOOL_CALL,
			UserMessage: "do the work",
		}
	}

	turnCtx, _ := taskctx.ContextWithBinding(ctx)
	_, created, err := em.EnsureForTurn(turnCtx, req(0))
	require.NoError(t, err)
	require.NotNil(t, created)
	probesAfterWarm := counting.getBoardCalls.Load()

	// Remove the board behind the cache's back, and the tasks that reference it
	// so the delete is not blocked by the foreign key.
	_, err = db.ExecContext(ctx, `DELETE FROM tasks WHERE board_id = ?`, "heal-board")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `DELETE FROM task_boards WHERE id = ?`, "heal-board")
	require.NoError(t, err)

	// This turn trusts the cache, so it skips the probe and the create fails.
	// A failed mint is not an error — bookkeeping never fails a turn.
	ctx2, _ := taskctx.ContextWithBinding(ctx)
	_, second, err := em.EnsureForTurn(ctx2, req(1))
	require.NoError(t, err, "a failed mint must not surface as a turn error")
	assert.Nil(t, second, "the mint should have failed against the missing board")
	assert.Equal(t, probesAfterWarm, counting.getBoardCalls.Load(),
		"the failing turn trusted the cache, which is why it failed")

	// The failure cleared the entry, so the next turn re-probes and recreates.
	ctx3, _ := taskctx.ContextWithBinding(ctx)
	_, third, err := em.EnsureForTurn(ctx3, req(2))
	require.NoError(t, err)
	require.NotNil(t, third, "the emitter should recover once the cache is cleared")
	assert.Greater(t, counting.getBoardCalls.Load(), probesAfterWarm,
		"recovery requires re-probing the board")
}

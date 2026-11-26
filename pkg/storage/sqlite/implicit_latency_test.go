// Copyright 2026 Teradata

package sqlite

import (
	"context"
	"fmt"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/task"
	"github.com/teradata-labs/loom/pkg/taskctx"
)

// BenchmarkImplicitEmitLatency measures what the implicit emitter adds to a
// turn, because that cost is paid SYNCHRONOUSLY.
//
// It has to be synchronous: the task must exist before the turn's messages are
// written, or they land with a NULL task_id and the timeline has nothing to
// read. So unlike the skill emitter — which is fire-and-forget — this cost is
// on the user's critical path and needs a number, not an assurance.
//
// Two cases, because they are the two that actually occur:
//
//	Mint      the first qualifying trigger of a turn: 4 round trips
//	          (GetBoard, GetTaskByIdempotencyKey, CreateTask, recordHistory)
//	Memoized  every later trigger in the same turn: an in-memory map hit,
//	          checked before any query runs
func BenchmarkImplicitEmitLatency(b *testing.B) {
	setup := func(b *testing.B) (*task.ImplicitEmitter, context.Context) {
		b.Helper()
		db := migratedDBB(b)
		store := NewTaskStore(db, observability.NewNoOpTracer())
		mgr := task.NewManager(store, nil, observability.NewNoOpTracer(), nil)
		ctx := context.Background()
		if _, err := store.CreateBoard(ctx, &task.TaskBoard{ID: "bench-board", Name: "bench"}); err != nil {
			b.Fatalf("create board: %v", err)
		}
		return task.NewImplicitEmitter(mgr, task.ResolveImplicitPolicy(nil), nil, nil), ctx
	}

	req := func(session string, turn int) task.TurnRequest {
		return task.TurnRequest{
			SessionID: session, AgentID: "a1", BoardID: "bench-board", TurnIndex: turn,
			Trigger:     loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_TOOL_CALL,
			UserMessage: "find the slow queries",
		}
	}

	b.Run("mint_first_trigger_of_turn", func(b *testing.B) {
		em, ctx := setup(b)
		// A fresh session per iteration keeps every iteration a real mint and
		// stays under the 100-per-session cap, which would otherwise start
		// declining and measure the wrong path.
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			turnCtx, _ := taskctx.ContextWithBinding(ctx)
			if _, _, err := em.EnsureForTurn(turnCtx, req(fmt.Sprintf("s-%d", i), 0)); err != nil {
				b.Fatalf("ensure: %v", err)
			}
		}
	})

	b.Run("memoized_later_trigger_same_turn", func(b *testing.B) {
		em, ctx := setup(b)
		turnCtx, _ := taskctx.ContextWithBinding(ctx)
		// Mint once outside the timer; every timed call is a repeat trigger.
		if _, _, err := em.EnsureForTurn(turnCtx, req("s-memo", 0)); err != nil {
			b.Fatalf("seed: %v", err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, _, err := em.EnsureForTurn(turnCtx, req("s-memo", 0)); err != nil {
				b.Fatalf("ensure: %v", err)
			}
		}
	})

	b.Run("declined_recording_disabled", func(b *testing.B) {
		db := migratedDBB(b)
		store := NewTaskStore(db, observability.NewNoOpTracer())
		mgr := task.NewManager(store, nil, observability.NewNoOpTracer(), nil)
		policy := task.ResolveImplicitPolicy(nil)
		policy.Enabled = false
		em := task.NewImplicitEmitter(mgr, policy, nil, nil)
		ctx := context.Background()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, _, err := em.EnsureForTurn(ctx, req("s-off", i)); err != nil {
				b.Fatalf("ensure: %v", err)
			}
		}
	})
}

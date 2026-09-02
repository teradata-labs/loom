// Copyright 2026 Teradata

//go:build integration || !unit

package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/task"
	"github.com/teradata-labs/loom/pkg/taskctx"
)

// BenchmarkImplicitEmitLatencyPostgres measures the SYNCHRONOUS cost the
// implicit emitter adds to a turn against real PostgreSQL.
//
// The SQLite benchmark (pkg/storage/sqlite) measures the same thing on a local
// file, where there is no network. This one exists because the mint is four
// SERIAL round trips — GetBoard, GetTaskByIdempotencyKey, CreateTask,
// recordHistory — so its real cost tracks round-trip time, and extrapolating
// from SQLite would be arithmetic dressed up as measurement.
//
// Requires TEST_POSTGRES_URL. Point it at a THROWAWAY database: this writes
// tasks, boards and history rows.
func BenchmarkImplicitEmitLatencyPostgres(b *testing.B) {
	dsn := os.Getenv("TEST_POSTGRES_URL")
	if dsn == "" {
		b.Skip("TEST_POSTGRES_URL not set; skipping PostgreSQL emit benchmark")
	}

	// loom's Postgres store requires a user ID in context for RLS; without
	// one every write fails and the emitter declines QUIETLY, which would make
	// this benchmark time the failure path instead of the work.
	ctx := ContextWithUserID(context.Background(), "bench-user")
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(b, err)
	defer pool.Close()

	migrator, err := NewMigrator(pool, observability.NewNoOpTracer())
	require.NoError(b, err)
	require.NoError(b, migrator.MigrateUp(ctx))

	store := NewTaskStore(pool, observability.NewNoOpTracer())
	mgr := task.NewManager(store, nil, observability.NewNoOpTracer(), nil)

	_, err = store.CreateBoard(ctx, &task.TaskBoard{ID: "bench-board-pg", Name: "bench-pg"})
	if err != nil {
		// Board may survive a prior run; a duplicate is fine.
		b.Logf("create board (continuing): %v", err)
	}

	req := func(session string, turn int) task.TurnRequest {
		return task.TurnRequest{
			SessionID: session, AgentID: "a1", BoardID: "bench-board-pg", TurnIndex: turn,
			Trigger:     loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_TOOL_CALL,
			UserMessage: "find the slow queries",
		}
	}

	b.Run("mint_first_trigger_of_turn", func(b *testing.B) {
		em := task.NewImplicitEmitter(mgr, task.ResolveImplicitPolicy(nil), nil, nil)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			turnCtx, _ := taskctx.ContextWithBinding(ctx)
			_, created, err := em.EnsureForTurn(turnCtx, req(fmt.Sprintf("pg-s-%d-%d", b.N, i), 0))
			if err != nil {
				b.Fatalf("ensure: %v", err)
			}
			if created == nil {
				b.Fatal("emitter declined; this would time the decline path, not a mint")
			}
		}
	})

	b.Run("memoized_later_trigger_same_turn", func(b *testing.B) {
		em := task.NewImplicitEmitter(mgr, task.ResolveImplicitPolicy(nil), nil, nil)
		turnCtx, _ := taskctx.ContextWithBinding(ctx)
		_, _, err := em.EnsureForTurn(turnCtx, req("pg-memo", 0))
		require.NoError(b, err)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, _, err := em.EnsureForTurn(turnCtx, req("pg-memo", 0)); err != nil {
				b.Fatalf("ensure: %v", err)
			}
		}
	})
}

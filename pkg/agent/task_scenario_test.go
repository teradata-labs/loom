// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	sqlitestore "github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/task"
	"github.com/teradata-labs/loom/pkg/taskctx"
	"github.com/teradata-labs/loom/pkg/types"
)

// TestScenario_EndToEnd walks a realistic turn end to end and PRINTS the
// timeline a human would see. Run with:
//
//	go test -tags fts5 -run TestScenario -v ./pkg/agent/
//
// It exercises every piece built for task-as-rendering-layer: implicit task
// minting on the first tool call, attribution flowing to the message writer,
// tool call and result reconstruction, a human-in-the-loop approval, lifecycle
// transitions, and the merged read model.
func TestScenario_EndToEnd(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// --- Storage: one SQLite file for tasks, one for the session store. -----
	taskDB := openMigratedTaskDB(t, filepath.Join(dir, "tasks.db"))
	taskStore := sqlitestore.NewTaskStore(taskDB, observability.NewNoOpTracer())
	mgr := task.NewManager(taskStore, nil, observability.NewNoOpTracer(), nil)

	sessions, err := NewSessionStore(filepath.Join(dir, "sessions.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	defer func() { _ = sessions.Close() }()

	_, err = taskStore.CreateBoard(ctx, &task.TaskBoard{ID: "board-1", Name: "Agent work"})
	require.NoError(t, err)
	require.NoError(t, sessions.SaveSession(ctx, &Session{
		ID: "sess-42", AgentID: "sql-optimizer", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	// --- The turn begins. Nothing is created yet: the runtime does not know
	// whether this turn will do real work. -----------------------------------
	policy := task.ResolveImplicitPolicy(nil) // default = opt-out, i.e. ON
	emitter := task.NewImplicitEmitter(mgr, policy, nil, nil)

	turnCtx, _ := taskctx.ContextWithBinding(ctx)
	base := time.Now().Truncate(time.Second)

	userMsg := "Why is the orders report slow?"
	require.NoError(t, sessions.SaveMessage(turnCtx, "sess-42", &Message{
		Role: "user", Content: userMsg, Timestamp: base,
	}, true))

	// At this point no task exists — a turn that only chats gets no board row.
	_, totalBefore, err := taskStore.ListTasks(ctx, task.ListTasksOpts{BoardID: "board-1", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 0, totalBefore, "a turn that has not yet done work must mint nothing")

	// --- First tool call: THIS is what mints the task. ----------------------
	turnCtx, minted, err := emitter.EnsureForTurn(turnCtx, task.TurnRequest{
		SessionID: "sess-42", AgentID: "sql-optimizer", BoardID: "board-1",
		TurnIndex: 0, UserMessage: userMsg,
		Trigger: loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_TOOL_CALL,
	})
	require.NoError(t, err)
	require.NotNil(t, minted, "the first tool call must mint the turn's task")

	// Back-fill: the user message was written before the task existed, so it
	// carries a NULL task_id. Without this the timeline starts at the agent's
	// first action and never shows what was asked.
	claimed, err := sessions.AttributeTurnMessages(turnCtx, "sess-42", minted.ID, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), claimed, "the turn's user message must be claimed retroactively")

	sched := []struct {
		offset  time.Duration
		role    string
		content string
		call    *types.ToolCall
		result  *shuttle.Result
		useID   string
	}{
		{offset: 2 * time.Second, role: "assistant", content: "Let me look at the query plan.",
			call: &types.ToolCall{ID: "c1", Name: "explain_query",
				Input: map[string]interface{}{"sql": "SELECT * FROM orders o JOIN customers c ON o.cid = c.id"}}},
		{offset: 4 * time.Second, role: "tool", useID: "c1",
			result: &shuttle.Result{Success: true, ExecutionTimeMs: 214,
				Data: map[string]interface{}{"plan": "full table scan on orders (12.4M rows)"}}},
		{offset: 6 * time.Second, role: "assistant", content: "Full scan on orders. Checking for an index.",
			call: &types.ToolCall{ID: "c2", Name: "list_indexes",
				Input: map[string]interface{}{"table": "orders"}}},
		{offset: 7 * time.Second, role: "tool", useID: "c2",
			result: &shuttle.Result{Success: false, ExecutionTimeMs: 9,
				Error: &shuttle.Error{Message: "permission denied on DBC.IndicesV"}}},
		{offset: 9 * time.Second, role: "assistant", content: "I lack catalog access. I will propose an index instead."},
	}
	for _, m := range sched {
		msg := Message{Role: m.role, Content: m.content, AgentID: "sql-optimizer",
			ToolUseID: m.useID, ToolResult: m.result, Timestamp: base.Add(m.offset)}
		if m.call != nil {
			msg.ToolCalls = []types.ToolCall{*m.call}
		}
		// turnStart=false: the user message above already opened this turn.
		require.NoError(t, sessions.SaveMessage(turnCtx, "sess-42", &msg, false))
	}

	// --- The agent asks a human to approve a change. ------------------------
	answered := base.Add(95 * time.Second)
	hitl := NewHITLTimelineSource(&scenarioHITL{reqs: []*shuttle.HumanRequest{{
		ID: "req-7", AgentID: "sql-optimizer", SessionID: "sess-42",
		Question:    "Create index orders_cid_idx on orders(cid)? Est. 4 min, 12.4M rows.",
		RequestType: "approval", Status: "approved",
		CreatedAt:   base.Add(11 * time.Second),
		RespondedAt: &answered, RespondedBy: "josh.schoen",
		TaskID: minted.ID,
	}}})

	// --- Turn ends: the task closes. ---------------------------------------
	_, err = mgr.CloseTask(ctx, minted.ID, "Recommended orders_cid_idx; approved by josh.schoen")
	require.NoError(t, err)

	// --- Read it back the way a UI would. ----------------------------------
	reader := task.NewTimelineReader(nil, nil, sessions, hitl, task.NewHistorySource(taskStore))
	res, err := reader.Read(ctx, minted.ID, task.TimelineOpts{})
	require.NoError(t, err)
	require.Empty(t, res.PartialSources)

	t.Logf("\n"+
		"╭─ TASK %s\n"+
		"│  %s\n"+
		"│  created_via=%s  board=%s  session=%s\n"+
		"╰─ %d events from %d sources\n\n%s",
		minted.ID[:12], minted.Title, minted.CreatedVia, minted.BoardID, "sess-42",
		len(res.Events), 3,
		task.RenderTimeline(res, task.RenderOpts{DetailBytes: 120, Now: base}))

	// --- Assert the shape rather than just eyeballing the print. -----------
	kinds := map[task.TimelineEventKind]int{}
	for _, e := range res.Events {
		kinds[e.Kind]++
	}
	require.Equal(t, 2, kinds[task.TimelineKindToolCall], "both tool calls recovered")
	require.Equal(t, 2, kinds[task.TimelineKindToolResult], "both results recovered")
	require.Equal(t, 1, kinds[task.TimelineKindHumanRequest])
	require.Equal(t, 1, kinds[task.TimelineKindHumanResponse])
	require.GreaterOrEqual(t, kinds[task.TimelineKindLifecycle], 2, "created + closed")
	require.Equal(t, 1, kinds[task.TimelineKindUser])

	// One failed tool, with its error, recovered from the stored shuttle.Result.
	var failures int
	for _, e := range res.Events {
		if e.Kind == task.TimelineKindToolResult && e.Success != nil && !*e.Success {
			failures++
			require.Contains(t, e.Error, "permission denied")
			require.Equal(t, int64(9), e.DurationMs)
		}
	}
	require.Equal(t, 1, failures)

	// --- And the agent's own context must NOT see this bookkeeping. --------
	counts, err := mgr.CountByStatus(ctx, task.CountByStatusOpts{
		BoardID:           "board-1",
		ExcludeCreatedVia: policy.ExcludedCreatedVia(),
	})
	require.NoError(t, err)
	require.Equal(t, 0, counts.Total,
		"the implicit task must be invisible to the agent's own board stats")

	withBookkeeping, err := mgr.CountByStatus(ctx, task.CountByStatusOpts{BoardID: "board-1"})
	require.NoError(t, err)
	require.Equal(t, 1, withBookkeeping.Total, "but a human-facing board query sees it")
	t.Logf("agent-visible board total: %d   human-visible board total: %d",
		counts.Total, withBookkeeping.Total)
}

type scenarioHITL struct{ reqs []*shuttle.HumanRequest }

func (s *scenarioHITL) ListByTask(context.Context, string) ([]*shuttle.HumanRequest, error) {
	return s.reqs, nil
}

func openMigratedTaskDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path+"?_fk=1&_journal_mode=WAL")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mig, err := sqlitestore.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, mig.MigrateUp(context.Background()))
	return db
}

// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

// The task lifecycle across a park and a resume.
//
// A parked turn is one turn that spans two calls, and every piece of the
// implicit task machinery assumed one turn was one call:
//
//  1. maybeParkBatch returned before dispatch, so a turn whose FIRST action
//     needed a human decision never reached the TOOL_CALL trigger and recorded
//     nothing — no row for the one turn shape a person was personally involved
//     in.
//  2. chat()'s deferred close ran unconditionally, so a task recorded earlier in
//     the turn was closed while the turn was still waiting. implicitCloseReason
//     reads TurnParkedError as a failure, so the board reported the work
//     finished, and finished badly, before the approved action had run.
//  3. ResumeChat built its context with no binding, no turn index and no user
//     message, so everything written after the approval carried a NULL task_id —
//     including the tool row for the action a human had explicitly authorised.
//
// These tests drive the real Chat/ResumeChat API against an approval-gated tool
// and read the persisted rows, because every one of those three was invisible
// from anywhere else.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	sqlitestore "github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/task"
)

// parkTaskRig is newParkFixture's shape plus a real task board: the same
// park-enabled agent, ask-gated on export_csv, with the emitter NewAgent wires
// for itself so the tests read the configuration path a real agent takes.
//
// A real session store is mandatory twice over. Park refuses to raise a request
// against a non-durable batch, and the task_id these tests read is a column on
// the messages table — it is stamped at write time from the ambient attribution
// and never written back onto the in-memory Message, so an in-memory assertion
// would be reading a field nothing sets.
type parkTaskRig struct {
	ag       *Agent
	park     *shuttle.InMemoryHumanRequestStore
	sessions *SessionStore
	tasks    *task.Manager
	tools    map[string]*countingTool
}

func newParkTaskRig(t *testing.T, responses []mockLLMResponse, toolNames ...string) *parkTaskRig {
	t.Helper()

	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	sessions, err := NewSessionStore(filepath.Join(dir, "sessions.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sessions.Close() })

	mgr := task.NewManager(
		sqlitestore.NewTaskStore(openMigratedTaskDB(t, filepath.Join(dir, "tasks.db")),
			observability.NewNoOpTracer()),
		nil, observability.NewNoOpTracer(), nil)

	parkStore := shuttle.NewInMemoryHumanRequestStore()
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{responses: responses},
		WithConfig(cfg),
		WithMemory(NewMemoryWithStore(sessions)),
		WithHITLPark(parkStore, 0, NewProgressNotifier()),
		WithAdmissionHooks(shuttle.NewChain([]shuttle.Hook{scopedAskHook{tool: "export_csv"}}, nil, nil)),
		WithTaskBoard(mgr, nil, &loomv1.TaskBoardConfig{}),
	)

	tools := make(map[string]*countingTool, len(toolNames))
	for _, n := range toolNames {
		ct := &countingTool{name: n}
		ag.RegisterTool(ct)
		tools[n] = ct
	}
	return &parkTaskRig{ag: ag, park: parkStore, sessions: sessions, tasks: mgr, tools: tools}
}

// parkAndAssert runs the turn, requires it to park, and returns the request.
func (r *parkTaskRig) parkAndAssert(t *testing.T, sessionID, message string) *shuttle.HumanRequest {
	t.Helper()
	_, err := r.ag.Chat(context.Background(), sessionID, message)
	var parked *TurnParkedError
	require.True(t, errors.As(err, &parked), "Chat error = %v, want *TurnParkedError", err)

	reqs, listErr := r.park.ListBySession(context.Background(), sessionID)
	require.NoError(t, listErr)
	for _, req := range reqs {
		if req.Status == "pending" && req.RequestType == "parked" {
			return req
		}
	}
	t.Fatalf("no pending parked request for %s", sessionID)
	return nil
}

// boardTasks returns the session's recorded tasks. The board id is the session
// id: no DefaultBoardId is configured, which is what maybeRecordImplicitTask
// falls back to. Keyed by board rather than by session because
// claimed_by_session is released when a task closes.
func (r *parkTaskRig) boardTasks(t *testing.T, sessionID string) []*task.Task {
	t.Helper()
	tasks, _, err := r.tasks.ListTasks(context.Background(), task.ListTasksOpts{
		BoardID: sessionID,
		Limit:   100,
	})
	require.NoError(t, err)
	return tasks
}

// onlyTask requires exactly one recorded task and returns it. The count is part
// of the assertion everywhere it is used: one turn records at most one task, and
// a resume that re-minted instead of rebinding would show up here.
func (r *parkTaskRig) onlyTask(t *testing.T, sessionID string) *task.Task {
	t.Helper()
	tasks := r.boardTasks(t, sessionID)
	require.Len(t, tasks, 1, "one turn records exactly one task")
	return tasks[0]
}

// taskIDByToolUse maps each persisted tool row's tool_use_id to the task_id
// column it was stamped with, read back from the store.
func (r *parkTaskRig) taskIDByToolUse(t *testing.T, sessionID string) map[string]string {
	t.Helper()
	msgs, err := r.sessions.LoadMessages(context.Background(), sessionID)
	require.NoError(t, err)
	out := map[string]string{}
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolUseID != "" {
			out[m.ToolUseID] = m.TaskID
		}
	}
	return out
}

// firstActionParkScript: the turn's very first action is the ask-gated call, so
// nothing dispatches before the park.
func firstActionParkScript() []mockLLMResponse {
	return []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{
			{ID: "c-write", Name: "export_csv", Input: map[string]interface{}{"v": "w"}},
		}},
		{content: "exported"},
	}
}

// workThenParkScript: an ungoverned call runs (firing TOOL_CALL and recording
// the turn's task), then the next batch parks on the ask.
func workThenParkScript() []mockLLMResponse {
	return []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{
			{ID: "c-read", Name: "read_table", Input: map[string]interface{}{"v": "1"}},
		}},
		{toolCalls: []llmtypes.ToolCall{
			{ID: "c-write", Name: "export_csv", Input: map[string]interface{}{"v": "w"}},
		}},
		{content: "exported"},
	}
}

// TestPark_FirstActionParkRecordsTask: a turn that parks on its first action
// must still be on the board. Before this it recorded nothing at all, so the
// human being asked to decide had no timeline to decide against.
func TestPark_FirstActionParkRecordsTask(t *testing.T) {
	r := newParkTaskRig(t, firstActionParkScript(), "export_csv")

	r.parkAndAssert(t, "s-first", "export the table")

	tasks := r.boardTasks(t, "s-first")
	if len(tasks) == 0 {
		t.Fatal("a turn whose first action parked recorded no task: maybeParkBatch returns " +
			"before dispatch, so its TOOL_CALL trigger never fires and HUMAN_REQUEST must")
	}
	require.Len(t, tasks, 1)
	require.Equal(t, "implicit", tasks[0].CreatedVia)
	require.Equal(t, loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_HUMAN_REQUEST.String(),
		tasks[0].Metadata["implicit_trigger"],
		"the recording trigger must be the human request, not a tool call that never happened")
	require.Zero(t, r.tools["export_csv"].runs.Load(), "rig sanity: nothing ran before the decision")
}

// TestPark_ParkedTaskStaysOpenWhileTheHumanDecides: a park is not the end of a
// turn. Closing at the park reported the work finished — and, because
// implicitCloseReason reads TurnParkedError as a failure, finished badly —
// before the action the human was still deciding about had run.
func TestPark_ParkedTaskStaysOpenWhileTheHumanDecides(t *testing.T) {
	r := newParkTaskRig(t, workThenParkScript(), "read_table", "export_csv")

	r.parkAndAssert(t, "s-open", "read then export")
	require.EqualValues(t, 1, r.tools["read_table"].runs.Load(), "rig sanity: the pre-park work ran")

	tk := r.onlyTask(t, "s-open")
	if task.IsTerminal(tk.Status) {
		t.Fatalf("parked task is %s with close_reason %q: the turn is waiting for a human, "+
			"so its task must stay open until the resumed turn finishes",
			tk.Status, tk.CloseReason)
	}
	require.Equal(t, loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS, tk.Status)
	require.Empty(t, tk.CloseReason,
		"a parked turn must not be captioned as ended, least of all as an error")
}

// TestPark_ApprovedWorkKeepsTheTurnsTaskID is the row the record used to lose.
// The tool row written after the approval is the one a person authorised; it has
// to carry the same task id as the work that preceded the park, or the two
// halves of one turn land in different places.
func TestPark_ApprovedWorkKeepsTheTurnsTaskID(t *testing.T) {
	r := newParkTaskRig(t, workThenParkScript(), "read_table", "export_csv")

	hr := r.parkAndAssert(t, "s-approve", "read then export")
	before := r.onlyTask(t, "s-approve")

	resp, err := r.ag.ResumeChat(context.Background(), "s-approve",
		ParkDecision{RequestID: hr.ID, Approved: true}, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.EqualValues(t, 1, r.tools["export_csv"].runs.Load(),
		"rig sanity: the approved action must actually have run")

	byUse := r.taskIDByToolUse(t, "s-approve")
	require.NotEmpty(t, byUse["c-read"], "rig sanity: the pre-park row was attributed")
	if byUse["c-write"] == "" {
		t.Fatal("the approved action's tool row carries no task_id: ResumeChat must carry the " +
			"parked turn's binding, or the one row a human explicitly authorised falls out " +
			"of the record")
	}
	require.Equal(t, byUse["c-read"], byUse["c-write"],
		"both halves of one turn belong to one task")
	require.Equal(t, before.ID, byUse["c-write"],
		"the resume must rebind to the task the parked half recorded, not mint a new one")

	// And the turn that finally ended is the one that closes the task.
	after := r.onlyTask(t, "s-approve")
	require.Equal(t, before.ID, after.ID)
	require.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, after.Status,
		"the resumed turn terminated, so its task closes")
	require.NotEmpty(t, after.CloseReason)
	require.NotContains(t, after.CloseReason, "error",
		"the turn completed after the approval; nothing about it failed")
}

// TestPark_RejectedResumeClosesTheTurnsTask: a refusal is still a decision, and
// the turn that carries it still ends. The synthesized refusal row is part of
// the same task's record.
func TestPark_RejectedResumeClosesTheTurnsTask(t *testing.T) {
	r := newParkTaskRig(t, workThenParkScript(), "read_table", "export_csv")

	hr := r.parkAndAssert(t, "s-reject", "read then export")
	before := r.onlyTask(t, "s-reject")

	resp, err := r.ag.ResumeChat(context.Background(), "s-reject",
		ParkDecision{RequestID: hr.ID, Approved: false, Reason: "not this quarter"}, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Zero(t, r.tools["export_csv"].runs.Load(), "a rejection runs no tool bodies")

	byUse := r.taskIDByToolUse(t, "s-reject")
	require.Equal(t, before.ID, byUse["c-write"],
		"the refusal synthesized for the rejected call belongs to the turn's task")

	after := r.onlyTask(t, "s-reject")
	require.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, after.Status,
		"a rejected turn still ended, so its task must not be left in progress")
	require.NotEmpty(t, after.CloseReason)
}

// TestPark_FirstActionParkThenApproveClosesTheTask joins the two halves for the
// shape that recorded nothing at all: the task appears at the park, survives the
// wait, owns the approved row, and closes when the resumed turn ends.
func TestPark_FirstActionParkThenApproveClosesTheTask(t *testing.T) {
	r := newParkTaskRig(t, firstActionParkScript(), "export_csv")

	hr := r.parkAndAssert(t, "s-first-approve", "export the table")
	parkedTask := r.onlyTask(t, "s-first-approve")
	require.False(t, task.IsTerminal(parkedTask.Status))

	_, err := r.ag.ResumeChat(context.Background(), "s-first-approve",
		ParkDecision{RequestID: hr.ID, Approved: true}, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, r.tools["export_csv"].runs.Load())

	byUse := r.taskIDByToolUse(t, "s-first-approve")
	require.Equal(t, parkedTask.ID, byUse["c-write"],
		"the approved action is the turn's only work and must carry the turn's task")

	after := r.onlyTask(t, "s-first-approve")
	require.Equal(t, parkedTask.ID, after.ID, "the resume rebinds; it does not re-mint")
	require.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, after.Status)
}

// TestInlineContactHuman_FiresHumanRequestTrigger covers the NON-parked HITL
// path: no WithHITLPark, so maybeParkBatch's gate (a.hitlPark != nil) never
// admits the batch and contact_human dispatches inline, answered in-turn by a
// resolver polling the request store.
//
// That path fired no trigger at all: maybeParkBatch's HUMAN_REQUEST emission is
// behind the park gate, and dispatchOneCall's TOOL_CALL emission sits on the
// non-HITL branch of the contact_human check. So a supported configuration
// never fired the default HUMAN_REQUEST trigger, and a turn whose FIRST action
// asked a human recorded nothing — the same first-action gap the park path had.
func TestInlineContactHuman_FiresHumanRequestTrigger(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	sessions, err := NewSessionStore(filepath.Join(dir, "sessions.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sessions.Close() })

	mgr := task.NewManager(
		sqlitestore.NewTaskStore(openMigratedTaskDB(t, filepath.Join(dir, "tasks.db")),
			observability.NewNoOpTracer()),
		nil, observability.NewNoOpTracer(), nil)

	// Deliberately NO WithHITLPark: this is the legacy in-turn configuration.
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{
			{ID: "c-ask", Name: "contact_human", Input: map[string]interface{}{"question": "which db?"}},
		}},
		{content: "done, used staging"},
	}},
		WithConfig(cfg),
		WithMemory(NewMemoryWithStore(sessions)),
		WithTaskBoard(mgr, nil, &loomv1.TaskBoardConfig{}),
	)

	hrStore := shuttle.NewInMemoryHumanRequestStore()
	ag.RegisterTool(shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{
		Store:        hrStore,
		Notifier:     NewProgressNotifier(),
		Timeout:      3 * time.Second,
		PollInterval: 5 * time.Millisecond,
	}))
	// Answer the pending request from the background, the way a reviewer would.
	// (respondOnce lives in the external agent_test package; this file is the
	// internal one.)
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if pending, err := hrStore.ListPending(context.Background()); err == nil && len(pending) > 0 {
				_ = hrStore.RespondToRequest(context.Background(),
					pending[0].ID, "responded", "use staging", "reviewer@example.com", nil)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	_, err = ag.Chat(context.Background(), "s-inline", "which database should I use?")
	require.NoError(t, err, "the resolver answers in-turn; the turn completes normally")

	tasks, _, err := mgr.ListTasks(context.Background(), task.ListTasksOpts{
		BoardID: "s-inline", Limit: 10})
	require.NoError(t, err)
	if len(tasks) == 0 {
		t.Fatal("an in-turn contact_human as the turn's first action recorded no task: " +
			"neither park's HUMAN_REQUEST (gate not armed) nor dispatch's TOOL_CALL " +
			"(other branch) fired")
	}
	require.Len(t, tasks, 1, "one turn records exactly one task")
	require.Equal(t, "implicit", tasks[0].CreatedVia)
	require.Equal(t, loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_HUMAN_REQUEST.String(),
		tasks[0].Metadata["implicit_trigger"],
		"the recording trigger names what happened: a human was asked")
	require.True(t, task.IsTerminal(tasks[0].Status),
		"the turn finished normally, so its task is closed")
}

// TestPark_ResumeRestoresTheDurableTaskID pins the resume's PRIMARY identity
// source: the task id on the park row itself, not a fresh policy-gated
// emission.
//
// The emitter rebind re-derives at resume time what the park already decided,
// and a policy that declines on the RESUMING agent — implicit recording turned
// off after a restart, a spent session cap, an excluded trigger — left the
// approved rows unattributed and the parked task open forever. The scenario
// here is the restart shape: agent B, restored over agent A's stores with
// implicit recording disabled, resumes A's parked turn. B could never re-mint;
// only the durable id on the row can restore the identity.
func TestPark_ResumeRestoresTheDurableTaskID(t *testing.T) {
	r := newParkTaskRig(t, workThenParkScript(), "read_table", "export_csv")

	hr := r.parkAndAssert(t, "s-restore", "read then export")
	before := r.onlyTask(t, "s-restore")
	require.Equal(t, before.ID, hr.TaskID,
		"rig sanity: the park row carries the task it blocked — the write side "+
			"stamps it from the turn's attribution")

	// Agent B: the same session store, park store and task manager — a restart —
	// but with implicit recording DISABLED, so the emitter fallback can mint
	// nothing and the durable id is the only identity source.
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	restored := NewAgent(&mockBackend{}, &mockToolCallingLLM{responses: []mockLLMResponse{
		{content: "exported"},
	}},
		WithConfig(cfg),
		WithMemory(NewMemoryWithStore(r.sessions)),
		WithHITLPark(r.park, 0, NewProgressNotifier()),
		WithAdmissionHooks(shuttle.NewChain([]shuttle.Hook{scopedAskHook{tool: "export_csv"}}, nil, nil)),
		WithTaskBoard(r.tasks, nil, &loomv1.TaskBoardConfig{
			ImplicitTasks: &loomv1.ImplicitTaskConfig{
				Mode: loomv1.ImplicitTaskMode_IMPLICIT_TASK_MODE_DISABLED,
			},
		}),
	)
	ct := &countingTool{name: "export_csv"}
	restored.RegisterTool(ct)
	restored.RegisterTool(&countingTool{name: "read_table"})

	resp, err := restored.ResumeChat(context.Background(), "s-restore",
		ParkDecision{RequestID: hr.ID, Approved: true}, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.EqualValues(t, 1, ct.runs.Load(), "rig sanity: the approved action ran on the restored agent")

	byUse := r.taskIDByToolUse(t, "s-restore")
	if byUse["c-write"] == "" {
		t.Fatal("the approved row is unattributed on a restored agent whose policy declines: " +
			"the resume must seed the binding from the park row's durable task id, " +
			"not re-derive it through the policy-gated emitter")
	}
	require.Equal(t, before.ID, byUse["c-write"],
		"the durable id restores the SAME task, not a fresh one")

	after := r.onlyTask(t, "s-restore")
	require.Equal(t, before.ID, after.ID, "no second task was minted")
	require.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, after.Status,
		"the resumed turn terminated, so the parked task finally closes")
}

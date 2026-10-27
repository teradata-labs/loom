// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/teradata-labs/loom/pkg/eventlog"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// crashingTool executes the first N calls normally, then panics-as-crash on
// the (N+1)th call by returning a sentinel the test's outer harness treats
// as "the process died here" — this test simulates the DURABLE STATE a crash
// would leave behind (an intent with no result) directly against the store,
// since actually panicking mid-test is not how Go tests express "the process
// died" cleanly. See TestResumeSession_ToolIntentNoResult_SurfacesGap
// below for how that state is constructed.
type crashingTool struct{ calls int }

func (c *crashingTool) Name() string        { return "crashy" }
func (c *crashingTool) Description() string { return "always succeeds; used to build resume fixtures" }
func (c *crashingTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (c *crashingTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	c.calls++
	return &shuttle.Result{Success: true, Data: "ok"}, nil
}
func (c *crashingTool) Backend() string { return "" }

func TestResumeSession_NothingParked_IsNoOp(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithEventLog(recorder))

	if _, err := ag.Chat(context.Background(), "s-resume-noop", "hello"); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if _, err := ag.ResumeSession(context.Background(), "s-resume-noop"); !errors.Is(err, ErrNothingParked) {
		t.Errorf("ResumeSession on a fully-completed session = %v, want ErrNothingParked", err)
	}
}

func TestResumeSession_ToolNeverAttempted_DispatchesNormally(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithEventLog(recorder))
	tool := &crashingTool{}
	ag.tools.Register(tool)

	sess := ag.memory.GetOrCreateSessionWithAgent(context.Background(), "s-resume-dispatch", ag.config.Name, "")
	ag.appendMessage(context.Background(), sess, Message{
		Role:    "user",
		Content: "run crashy",
	}, true)
	ag.appendMessage(context.Background(), sess, Message{
		Role:      "assistant",
		ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{}}},
	}, false)
	recordTailBatch(t, recorder, sess, 0)
	// No tool row, no execution_events row: this call was NEVER attempted.

	resp, err := ag.ResumeSession(context.Background(), "s-resume-dispatch")
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if resp == nil {
		t.Fatal("ResumeSession returned nil Response")
	}
	if tool.calls != 1 {
		t.Errorf("crashingTool.calls = %d, want 1 (dispatched exactly once)", tool.calls)
	}
}

func TestResumeSession_ToolIntentWithResult_BackfillsWithoutReexecuting(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithEventLog(recorder))
	tool := &crashingTool{}
	ag.tools.Register(tool)

	sess := ag.memory.GetOrCreateSessionWithAgent(context.Background(), "s-resume-backfill", ag.config.Name, "")
	ag.appendMessage(context.Background(), sess, Message{Role: "user", Content: "run crashy"}, true)
	ag.appendMessage(context.Background(), sess, Message{
		Role:      "assistant",
		ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{}}},
	}, false)
	recordTailBatch(t, recorder, sess, 0)
	// Simulate: the tool ACTUALLY ran and its result was durably recorded to
	// the event log, but the crash happened before the `tool` message row
	// was appended (the narrow window RecordResult's best-effort write and
	// appendMessage's own best-effort write can independently fail across).
	ctx := context.Background()
	if err := recorder.RecordIntent(ctx, sess.ID, 0, 0, eventlog.EventToolCallIntent, "c1", []byte(`{"tool_name":"crashy"}`)); err != nil {
		t.Fatalf("fixture RecordIntent: %v", err)
	}
	if err := recorder.RecordResult(ctx, sess.ID, 0, 0, eventlog.EventToolCallResult, "c1", []byte(`{"result":{"success":true,"data":"ok"},"error":""}`)); err != nil {
		t.Fatalf("fixture RecordResult: %v", err)
	}

	if _, err := ag.ResumeSession(ctx, "s-resume-backfill"); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if tool.calls != 0 {
		t.Errorf("crashingTool.calls = %d, want 0 — a known-complete call must be backfilled, never re-executed", tool.calls)
	}
}

// TestResumeSession_AllRowsPresentNoFinalReply_ResumesLoop covers Finding 1:
// every tool call in the tail batch already has a row, but the turn crashed
// before the LLM produced its synthesizing reply. locateParkedBatch legitimately
// returns an empty rowless slice for this (see its own comment, park.go), and
// ResumeSession must still re-enter the conversation loop to produce that
// reply — not refuse with ErrNothingParked, which would strand the turn's
// answer permanently.
func TestResumeSession_AllRowsPresentNoFinalReply_ResumesLoop(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithEventLog(recorder))

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-resume-norow-nofinal", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "run crashy"}, true)
	ag.appendMessage(ctx, sess, Message{
		Role:      "assistant",
		ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{}}},
	}, false)
	recordTailBatch(t, recorder, sess, 0)
	ag.appendMessage(ctx, sess, Message{
		Role:       "tool",
		ToolUseID:  "c1",
		ToolResult: &shuttle.Result{Success: true, Data: "ok"},
	}, false)
	// Every row is present (the batch was applied) but there is no trailing
	// final assistant reply: the turn died between the last tool result and
	// its continuation.

	resp, err := ag.ResumeSession(ctx, "s-resume-norow-nofinal")
	if err != nil {
		t.Fatalf("ResumeSession: %v, want a real *Response (the loop must re-enter to produce the missing reply)", err)
	}
	if resp == nil {
		t.Fatal("ResumeSession returned nil Response")
	}
	if resp.Content != "Mock response" {
		t.Errorf("resp.Content = %q, want the resumed loop's synthesized reply", resp.Content)
	}
}

// TestResumeSession_NeverAttemptedHITLCall_ReParks covers Finding 3: a call
// that crashed before its intent was even recorded, and is HITL-classified
// (here, a registered contact_human), must re-park for a human decision on
// resume rather than being dispatched straight to Executor.Execute (which
// bypasses the pre-dispatch park scan entirely and would block on a
// configured askResolver or fail closed to Deny).
func TestResumeSession_NeverAttemptedHITLCall_ReParks(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	sessions, err := NewSessionStore(filepath.Join(t.TempDir(), "resume-repark.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	t.Cleanup(func() { _ = sessions.Close() })

	store := shuttle.NewInMemoryHumanRequestStore()
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{},
		WithConfig(cfg),
		WithMemory(NewMemoryWithStore(sessions)),
		WithHITLPark(store, 0, NewProgressNotifier()),
		WithEventLog(recorder),
	)
	tool := &countingTool{name: "contact_human"}
	ag.RegisterTool(tool)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-resume-repark", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "deploy it"}, true)
	ag.appendMessage(ctx, sess, Message{
		Role: "assistant",
		ToolCalls: []llmtypes.ToolCall{
			{ID: "c-q", Name: "contact_human", Input: map[string]interface{}{"question": "prod or staging?"}},
		},
	}, false)
	recordTailBatch(t, recorder, sess, 0)
	// No tool row, no execution_events row: this call was NEVER attempted —
	// the process crashed before even its intent was durably recorded.

	_, err = ag.ResumeSession(ctx, "s-resume-repark")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("ResumeSession error = %v, want *TurnParkedError", err)
	}
	if got := tool.runs.Load(); got != 0 {
		t.Errorf("contact_human body ran %d times, want 0 (must re-park, not execute)", got)
	}

	reqs, lerr := store.ListBySession(ctx, "s-resume-repark")
	if lerr != nil {
		t.Fatalf("ListBySession: %v", lerr)
	}
	var hr *shuttle.HumanRequest
	for _, r := range reqs {
		if r.Status == "pending" && r.RequestType == "parked" {
			hr = r
		}
	}
	if hr == nil {
		t.Fatalf("no pending parked HumanRequest persisted for the never-attempted call")
	}
	if hr.ID != parked.RequestID {
		t.Errorf("hr.ID = %q, parked.RequestID = %q, want match", hr.ID, parked.RequestID)
	}
	if hr.Kind != "question" {
		t.Errorf("hr.Kind = %q, want %q", hr.Kind, "question")
	}
	if _, ok := hr.Params["c-q"]; !ok {
		t.Errorf("params missing item c-q: %#v", hr.Params)
	}

	// A SECOND resume of the same session must now be refused, not re-parked.
	// The session is legitimately HITL-parked (waiting on a human, not
	// crashed), and parking happens pre-dispatch — so the tail batch still
	// looks "never attempted" to every reconciliation branch above and the
	// re-park branch would mint a duplicate card for one decision, leaving the
	// session blocked until that duplicate's TTL lapses. ResumeSession takes
	// the same guardParkedTail gate Chat does for exactly this reason.
	_, err = ag.ResumeSession(ctx, "s-resume-repark")
	var sessionParked *SessionParkedError
	if !errors.As(err, &sessionParked) {
		t.Fatalf("second ResumeSession on a HITL-parked session = %v, want *SessionParkedError", err)
	}
	if sessionParked.RequestID != hr.ID {
		t.Errorf("SessionParkedError.RequestID = %q, want the existing pending request %q", sessionParked.RequestID, hr.ID)
	}
	reqs, lerr = store.ListBySession(ctx, "s-resume-repark")
	if lerr != nil {
		t.Fatalf("ListBySession: %v", lerr)
	}
	pending := 0
	for _, r := range reqs {
		if r.Status == "pending" && r.RequestType == "parked" {
			pending++
		}
	}
	if pending != 1 {
		t.Errorf("pending parked requests after a second resume = %d, want 1 (no duplicate HITL card)", pending)
	}
}

func TestResumeSession_ToolIntentNoResult_SurfacesGap(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithEventLog(recorder))
	tool := &crashingTool{}
	ag.tools.Register(tool)

	sess := ag.memory.GetOrCreateSessionWithAgent(context.Background(), "s-resume-gap", ag.config.Name, "")
	ag.appendMessage(context.Background(), sess, Message{Role: "user", Content: "run crashy"}, true)
	ag.appendMessage(context.Background(), sess, Message{
		Role:      "assistant",
		ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{}}},
	}, false)
	recordTailBatch(t, recorder, sess, 0)
	ctx := context.Background()
	if err := recorder.RecordIntent(ctx, sess.ID, 0, 0, eventlog.EventToolCallIntent, "c1", []byte(`{"tool_name":"crashy"}`)); err != nil {
		t.Fatalf("fixture RecordIntent: %v", err)
	}
	// No matching result event: genuinely unknown outcome.

	_, err := ag.ResumeSession(ctx, "s-resume-gap")
	var gap *ExecutionGap
	if !errors.As(err, &gap) {
		t.Fatalf("ResumeSession error = %v, want *ExecutionGap", err)
	}
	if gap.ToolUseID != "c1" {
		t.Errorf("gap.ToolUseID = %q, want %q", gap.ToolUseID, "c1")
	}
	if tool.calls != 0 {
		t.Errorf("crashingTool.calls = %d, want 0 — an unknown outcome must never be auto-retried", tool.calls)
	}
}

// TestResumeSession_PriorAppliedParkDoesNotBlockCrashResume guards the narrow
// pending-only guard. A session that had a HITL park approved and fully applied
// earlier keeps that row at status "approved" until its TTL lapses (7 days by
// default). A LATER, unrelated turn that crashes mid-batch must still be
// resumable; refusing it because of the old decided row would leave the
// session dead, since ResumeChat, ExpireRequest and Chat are all closed to it.
func TestResumeSession_PriorAppliedParkDoesNotBlockCrashResume(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	sessions, err := NewSessionStore(filepath.Join(t.TempDir(), "resume-prior-park.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	t.Cleanup(func() { _ = sessions.Close() })

	store := shuttle.NewInMemoryHumanRequestStore()
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{},
		WithConfig(cfg),
		WithMemory(NewMemoryWithStore(sessions)),
		WithHITLPark(store, 0, NewProgressNotifier()),
		WithEventLog(recorder),
	)
	tool := &crashingTool{}
	ag.tools.Register(tool)

	ctx := context.Background()
	const sid = "s-resume-prior-park"
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, sid, ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "run crashy"}, true)
	ag.appendMessage(ctx, sess, Message{
		Role:      "assistant",
		ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{}}},
	}, false)
	recordTailBatch(t, recorder, sess, 0)

	// An earlier park, already approved and applied, still inside its TTL.
	now := time.Now()
	if err := store.Store(ctx, &shuttle.HumanRequest{
		ID: "old-park", AgentID: ag.id, SessionID: sid, Question: "old", RequestType: "parked",
		Kind: "approval", Status: "approved", CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(100 * time.Hour),
	}); err != nil {
		t.Fatalf("seed applied park: %v", err)
	}

	if _, err := ag.ResumeSession(ctx, sid); err != nil {
		var sp *SessionParkedError
		if errors.As(err, &sp) {
			t.Fatalf("ResumeSession refused by an old applied park (%s); only a PENDING request may block it", sp.RequestID)
		}
		t.Fatalf("ResumeSession: %v", err)
	}
	if tool.calls != 1 {
		t.Errorf("crashingTool.calls = %d, want 1 (the never-attempted call must be dispatched)", tool.calls)
	}
}

// TestFindExecutionGap_CallWithARowIsNotAGap pins which gap is named when a
// call already has a tool row but its tool_call_result event is missing (the
// result write is fail-open, so this really happens). Call 0 ran and its output
// was kept as a tool row; only call 1 is genuinely unknown. Both
// FindExecutionGap (what `looms agent resolve-gap` uses) and ResumeSession
// must name call 1 — scanning the event log alone would name call 0.
func TestFindExecutionGap_CallWithARowIsNotAGap(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithEventLog(recorder))
	tool := &crashingTool{}
	ag.tools.Register(tool)

	ctx := context.Background()
	const sid = "s-gap-row"
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, sid, ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "run crashy twice"}, true)
	ag.appendMessage(ctx, sess, Message{
		Role: "assistant",
		ToolCalls: []llmtypes.ToolCall{
			{ID: "c0", Name: "crashy", Input: map[string]interface{}{}},
			{ID: "c1", Name: "crashy", Input: map[string]interface{}{}},
		},
	}, false)
	recordTailBatch(t, recorder, sess, 0)
	ag.appendMessage(ctx, sess, Message{Role: "tool", ToolUseID: "c0", Content: "ok"}, false)
	for pos, id := range []string{"c0", "c1"} {
		if err := recorder.RecordIntent(ctx, sid, 0, pos, eventlog.EventToolCallIntent, id, []byte(`{"tool_name":"crashy"}`)); err != nil {
			t.Fatalf("fixture RecordIntent %d: %v", pos, err)
		}
	}
	events, err := recorder.LoadEvents(ctx, sid)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}

	gap := FindExecutionGap(sess, events)
	if gap == nil {
		t.Fatal("FindExecutionGap = nil, want the gap at position 1")
	}
	if gap.BatchPosition != 1 || gap.ToolUseID != "c1" {
		t.Errorf("FindExecutionGap named position %d (call %q), want position 1 (call \"c1\")", gap.BatchPosition, gap.ToolUseID)
	}

	_, err = ag.ResumeSession(ctx, sid)
	var got *ExecutionGap
	if !errors.As(err, &got) {
		t.Fatalf("ResumeSession error = %v, want *ExecutionGap", err)
	}
	if got.BatchPosition != gap.BatchPosition || got.ToolUseID != gap.ToolUseID {
		t.Errorf("resume named position %d (%q), resolve-gap would name %d (%q): they must agree",
			got.BatchPosition, got.ToolUseID, gap.BatchPosition, gap.ToolUseID)
	}
	if tool.calls != 0 {
		t.Errorf("crashingTool.calls = %d, want 0 (a gap must not dispatch anything)", tool.calls)
	}

	// Once the genuine gap is resolved there is none left.
	if err := recorder.RecordResult(ctx, sid, 0, 1, eventlog.EventToolCallResult, "c1", []byte(`{"result":{"success":true},"error":""}`)); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	events, _ = recorder.LoadEvents(ctx, sid)
	if g := FindExecutionGap(sess, events); g != nil {
		t.Errorf("FindExecutionGap after resolving = %+v, want nil", g)
	}
}

// recordTailBatch records, at step, the LLM call that produced the session's
// tail tool-call batch, the way the conversation loop records it before
// dispatching. Resume acts only on a batch whose step it finds in the log.
func recordTailBatch(t *testing.T, store eventlog.EventLogStore, sess *Session, step int) {
	t.Helper()
	batch, _, _, err := locateParkedBatch(sess, nil)
	if err != nil {
		t.Fatalf("fixture: no tail batch to record: %v", err)
	}
	payload, err := json.Marshal(LLMResponse{ToolCalls: batch.ToolCalls})
	if err != nil {
		t.Fatalf("fixture: marshal LLM response: %v", err)
	}
	ctx := context.Background()
	if err := store.RecordIntent(ctx, sess.ID, step, 0, eventlog.EventLLMCallIntent, "", []byte(`{"model":"test"}`)); err != nil {
		t.Fatalf("fixture: record LLM intent: %v", err)
	}
	if err := store.RecordResult(ctx, sess.ID, step, 0, eventlog.EventLLMCallResult, "", payload); err != nil {
		t.Fatalf("fixture: record LLM result: %v", err)
	}
}

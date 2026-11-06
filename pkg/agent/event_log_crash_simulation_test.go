// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teradata-labs/loom/pkg/eventlog"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

func TestResumeSession_ConcurrentResumes_ExactlyOneDispatches(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithEventLog(recorder))
	tool := &crashingTool{}
	ag.tools.Register(tool)

	sess := ag.memory.GetOrCreateSessionWithAgent(context.Background(), "s-resume-race", ag.config.Name, "")
	ag.appendMessage(context.Background(), sess, Message{Role: "user", Content: "run crashy"}, true)
	ag.appendMessage(context.Background(), sess, Message{
		Role:      "assistant",
		ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{}}},
	}, false)
	recordTailBatch(t, recorder, sess, 0)

	const attempts = 8
	var wg sync.WaitGroup
	results := make([]error, attempts)
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, results[i] = ag.ResumeSession(context.Background(), "s-resume-race")
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("%d concurrent ResumeSession calls succeeded, want exactly 1", winners)
	}
	if tool.calls != 1 {
		t.Errorf("crashingTool.calls = %d after %d concurrent resumes, want 1 (claim must prevent double-dispatch)", tool.calls, attempts)
	}
}

func TestEventLog_LLMCallResult_PreservesThoughtSignature(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{},
		&mockToolCallingLLM{responses: []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{{ID: "search", Name: "search", ThoughtSignature: "opaque-gemini-token-xyz"}}},
		}},
		WithConfig(cfg), WithEventLog(recorder))
	ag.tools.Register(&mockSearchTool{})

	if _, err := ag.Chat(context.Background(), "s-thought-sig", "search for something"); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// This step fully resolves inline (tool call dispatched, LLM gives a
	// final text answer) so Task 7 retention prunes it from the live store
	// by the time Chat returns — read the never-pruned audit trail instead,
	// same as TestRunConversationLoop_RecordsLLMAndToolEvents does.
	events := recorder.AllRecordedEvents("s-thought-sig")
	found := false
	for _, e := range events {
		if e.EventType != eventlog.EventLLMCallResult {
			continue
		}
		var resp llmtypes.LLMResponse
		if err := json.Unmarshal(e.Payload, &resp); err != nil {
			t.Fatalf("unmarshal llm_call_result: %v", err)
		}
		for _, tc := range resp.ToolCalls {
			if tc.ThoughtSignature == "opaque-gemini-token-xyz" {
				found = true
			}
		}
	}
	if !found {
		t.Error("ThoughtSignature did not round-trip through the recorded llm_call_result event")
	}
}

func TestEventLog_ToolCallCorrelation_UnaffectedByEmptyOrCollidingToolCallID(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	// Two parallel calls to the SAME tool with the SAME (colliding) ID —
	// the Gemini case parkItems documents — plus an empty ID (Ollama).
	ag := NewAgent(&mockBackend{},
		&mockToolCallingLLM{responses: []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{
				{ID: "search", Name: "search", Input: map[string]interface{}{"q": "first"}},
				{ID: "search", Name: "search", Input: map[string]interface{}{"q": "second"}},
				{ID: "", Name: "search", Input: map[string]interface{}{"q": "third"}},
			}},
		}},
		WithConfig(cfg), WithEventLog(recorder))
	ag.tools.Register(&mockSearchTool{})

	if _, err := ag.Chat(context.Background(), "s-collide", "search three times"); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// Same pruning consideration as above: this step fully resolves inline,
	// so read the never-pruned audit trail rather than the live store.
	events := recorder.AllRecordedEvents("s-collide")
	positions := map[int]bool{}
	for _, e := range events {
		if e.EventType == eventlog.EventToolCallIntent {
			positions[e.BatchPosition] = true
		}
	}
	if len(positions) != 3 {
		t.Errorf("distinct batch_position values for 3 calls with colliding/empty IDs = %d, want 3 — correlation must not depend on ToolCall.ID", len(positions))
	}
}

// toolRowsWithData returns the Data of every `tool` row in the session, in
// append order — the observable a backfill leaves behind.
func toolRowsWithData(sess *Session) []interface{} {
	var out []interface{}
	for _, m := range rawSessionMessages(sess) {
		if m.Role != "tool" {
			continue
		}
		if m.ToolResult == nil {
			out = append(out, nil)
			continue
		}
		out = append(out, m.ToolResult.Data)
	}
	return out
}

// TestResumeSession_CollidingToolCallIDs_BackfillEachCallsOwnResult is the
// READ-side counterpart to
// TestEventLog_ToolCallCorrelation_UnaffectedByEmptyOrCollidingToolCallID
// above, which only proves the WRITE side (three distinct batch_position
// values get recorded for three calls with colliding/empty IDs). It proves a
// later resume reconciles each of those calls against ITS OWN recorded event.
//
// The batch below is the Gemini/Ollama shape park.go documents: positions 0
// and 1 share the ID "search", position 2 has no ID at all. Each position has
// a distinct recorded result. Deriving a call's batch_position from
// ToolCall.ID via a map (the bug this guards) collapses positions 0 and 1 onto
// whichever was written last, so the call at position 0 silently gets position
// 1's recorded result backfilled onto it — data corruption with no error.
func TestResumeSession_CollidingToolCallIDs_BackfillEachCallsOwnResult(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithEventLog(recorder))
	tool := &crashingTool{}
	ag.tools.Register(tool)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-collide-read", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "run crashy three times"}, true)
	ag.appendMessage(ctx, sess, Message{
		Role: "assistant",
		ToolCalls: []llmtypes.ToolCall{
			{ID: "search", Name: "crashy", Input: map[string]interface{}{"q": "first"}},
			{ID: "search", Name: "crashy", Input: map[string]interface{}{"q": "second"}},
			{ID: "", Name: "crashy", Input: map[string]interface{}{"q": "third"}},
		},
	}, false)
	recordTailBatch(t, recorder, sess, 0)
	// No tool rows: the crash happened after every call ran and every result
	// was recorded, but before any `tool` message landed. Each position's
	// recorded result carries a position-specific sentinel so a cross-wired
	// backfill is visible.
	sentinels := []string{"alpha-pos0", "bravo-pos1", "charlie-pos2"}
	for pos, sentinel := range sentinels {
		if err := recorder.RecordIntent(ctx, sess.ID, 0, pos, eventlog.EventToolCallIntent, "", []byte(`{"tool_name":"crashy"}`)); err != nil {
			t.Fatalf("fixture RecordIntent pos %d: %v", pos, err)
		}
		payload := []byte(`{"result":{"success":true,"data":"` + sentinel + `"},"error":""}`)
		if err := recorder.RecordResult(ctx, sess.ID, 0, pos, eventlog.EventToolCallResult, "", payload); err != nil {
			t.Fatalf("fixture RecordResult pos %d: %v", pos, err)
		}
	}

	if _, err := ag.ResumeSession(ctx, "s-collide-read"); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if tool.calls != 0 {
		t.Errorf("crashingTool.calls = %d, want 0 — every call's result was already recorded", tool.calls)
	}

	got := toolRowsWithData(sess)
	if len(got) != len(sentinels) {
		t.Fatalf("backfilled tool rows = %d (%v), want %d", len(got), got, len(sentinels))
	}
	for i, want := range sentinels {
		if got[i] != want {
			t.Errorf("tool row %d carries %v, want %q — each call must be reconciled against the event at ITS OWN batch_position, never a colliding sibling's",
				i, got[i], want)
		}
	}
}

// TestResumeSession_CollidingToolCallIDs_GapDoesNotMaskSiblingResult is the
// same correlation guarantee on the GAP branch: with two calls sharing an ID,
// the one whose result IS recorded must still be backfilled, and only the
// genuinely unresolved one may surface as an ExecutionGap. Keying position by
// ToolCall.ID makes both calls resolve to the gap's position, so the sibling's
// perfectly good recorded result is never applied at all.
func TestResumeSession_CollidingToolCallIDs_GapDoesNotMaskSiblingResult(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithEventLog(recorder))
	tool := &crashingTool{}
	ag.tools.Register(tool)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-collide-gap", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "run crashy twice"}, true)
	ag.appendMessage(ctx, sess, Message{
		Role: "assistant",
		ToolCalls: []llmtypes.ToolCall{
			{ID: "dup", Name: "crashy", Input: map[string]interface{}{"q": "first"}},
			{ID: "dup", Name: "crashy", Input: map[string]interface{}{"q": "second"}},
		},
	}, false)
	recordTailBatch(t, recorder, sess, 0)
	// Position 0 completed and was recorded; position 1's intent landed but
	// its result never did (the crash window).
	if err := recorder.RecordIntent(ctx, sess.ID, 0, 0, eventlog.EventToolCallIntent, "dup", []byte(`{"tool_name":"crashy"}`)); err != nil {
		t.Fatalf("fixture RecordIntent pos 0: %v", err)
	}
	if err := recorder.RecordResult(ctx, sess.ID, 0, 0, eventlog.EventToolCallResult, "dup", []byte(`{"result":{"success":true,"data":"alpha-pos0"},"error":""}`)); err != nil {
		t.Fatalf("fixture RecordResult pos 0: %v", err)
	}
	if err := recorder.RecordIntent(ctx, sess.ID, 0, 1, eventlog.EventToolCallIntent, "dup", []byte(`{"tool_name":"crashy"}`)); err != nil {
		t.Fatalf("fixture RecordIntent pos 1: %v", err)
	}

	_, err := ag.ResumeSession(ctx, "s-collide-gap")
	var gap *ExecutionGap
	if !errors.As(err, &gap) {
		t.Fatalf("ResumeSession error = %v, want *ExecutionGap", err)
	}
	if gap.BatchPosition != 1 {
		t.Errorf("gap.BatchPosition = %d, want 1 (the genuinely unresolved call)", gap.BatchPosition)
	}
	if tool.calls != 0 {
		t.Errorf("crashingTool.calls = %d, want 0", tool.calls)
	}

	got := toolRowsWithData(sess)
	if len(got) != 1 || got[0] != "alpha-pos0" {
		t.Errorf("backfilled tool rows = %v, want exactly [alpha-pos0] — position 0's recorded result must be applied to position 0's call before the position-1 gap is reported, not discarded because an ID-keyed lookup pointed both calls at position 1",
			got)
	}
}

// TestEventLog_StepIndex_MonotonicAcrossChatAndResumeSession asserts
// NextStepIndex continues from a live (unresolved) step rather than
// restarting at 0 — the "chat() then later ResumeSession" scenario.
//
// Deviation from the Task 9 brief's literal fixture, noted explicitly: the
// brief's original version drove this through a single ag.Chat call with
// mockSimpleLLM (no tool calls). Under Task 7 retention (already landed and
// exercised by TestRunConversationLoop_RecordsLLMAndToolEvents above), a
// text-only response is "resolved" the instant the LLM replies and
// pruneStepIfResolved deletes its step 0 rows before Chat returns — so
// NextStepIndex legitimately (and correctly) returns 0 again afterward,
// since nothing live remains to continue from. That makes the brief's
// literal assertion (next == maxFirst+1, i.e. 1) false in a way that
// reflects correct pruning behavior, not a bug: the monotonic-continuation
// guarantee is only meaningful while a step's rows are still live. This
// version uses the same simulated-crash fixture already established
// throughout resume_session_test.go (manual appendMessage + RecordIntent,
// standing in for a crash between the intent write and the result write) to
// leave step 0 genuinely unresolved, so the continuation it asserts is real.
func TestEventLog_StepIndex_MonotonicAcrossChatAndResumeSession(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithEventLog(recorder))
	tool := &crashingTool{}
	ag.tools.Register(tool)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-monotonic", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "run crashy"}, true)
	ag.appendMessage(ctx, sess, Message{
		Role:      "assistant",
		ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{}}},
	}, false)
	// Simulate a crash: step 0's tool_call_intent landed durably but the
	// process died before any result was recorded — step 0 stays live
	// (unresolved, unpruned) in the event log.
	if err := recorder.RecordIntent(ctx, sess.ID, 0, 0, eventlog.EventToolCallIntent, "c1", []byte(`{"tool_name":"crashy"}`)); err != nil {
		t.Fatalf("fixture RecordIntent: %v", err)
	}

	firstEvents, _ := recorder.LoadEvents(ctx, "s-monotonic")
	maxFirst := 0
	for _, e := range firstEvents {
		maxFirst = max(maxFirst, e.StepIndex)
	}

	next, err := recorder.NextStepIndex(ctx, "s-monotonic")
	if err != nil {
		t.Fatalf("NextStepIndex: %v", err)
	}
	if next != maxFirst+1 {
		t.Errorf("NextStepIndex with a live unresolved step = %d, want %d (continuing, not restarting from 0)", next, maxFirst+1)
	}
}

// overlapProbe records whether another probe started while it was running.
type overlapProbe struct {
	name    string
	running *atomic.Int32
	overlap *atomic.Bool
}

func (p *overlapProbe) Name() string                     { return p.name }
func (p *overlapProbe) Description() string              { return "detects concurrent dispatch" }
func (p *overlapProbe) InputSchema() *shuttle.JSONSchema { return &shuttle.JSONSchema{Type: "object"} }
func (p *overlapProbe) Backend() string                  { return "" }
func (p *overlapProbe) Execute(ctx context.Context, _ map[string]interface{}) (*shuttle.Result, error) {
	if p.running.Add(1) > 1 {
		p.overlap.Store(true)
	}
	// Stay running long enough for a concurrently dispatched sibling to
	// start; with sequential dispatch none can.
	time.Sleep(50 * time.Millisecond)
	p.running.Add(-1)
	return &shuttle.Result{Success: true, Data: p.name}, nil
}

// TestDispatchOneCall_BatchLoop_IsSequentialNotConcurrent pins that a batch's
// calls run one at a time. Each call's batch_position is its loop index and
// its intent is recorded just before it runs; concurrent dispatch would let a
// crash leave several calls mid-flight and break the "at most the call that
// was running is unknown" property resume relies on.
func TestDispatchOneCall_BatchLoop_IsSequentialNotConcurrent(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	var running atomic.Int32
	var overlap atomic.Bool
	ag := NewAgent(&mockBackend{},
		&mockToolCallingLLM{responses: []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{
				{ID: "a", Name: "probe_a", Input: map[string]interface{}{}},
				{ID: "b", Name: "probe_b", Input: map[string]interface{}{}},
				{ID: "c", Name: "probe_c", Input: map[string]interface{}{}},
			}},
		}},
		WithConfig(cfg), WithEventLog(recorder))
	for _, name := range []string{"probe_a", "probe_b", "probe_c"} {
		ag.tools.Register(&overlapProbe{name: name, running: &running, overlap: &overlap})
	}

	if _, err := ag.Chat(context.Background(), "s-sequential", "run all three"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if overlap.Load() {
		t.Fatal("two calls of one batch ran at the same time")
	}
	var positions []int
	for _, e := range recorder.AllRecordedEvents("s-sequential") {
		if e.EventType == eventlog.EventToolCallIntent {
			positions = append(positions, e.BatchPosition)
		}
	}
	if len(positions) != 3 || positions[0] != 0 || positions[1] != 1 || positions[2] != 2 {
		t.Fatalf("tool intents recorded at positions %v, want [0 1 2] in order", positions)
	}
}

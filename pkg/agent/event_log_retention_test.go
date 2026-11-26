// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package agent

import (
	"context"
	"testing"

	"github.com/teradata-labs/loom/pkg/eventlog"
)

func TestRunConversationLoop_PrunesResolvedSteps(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithEventLog(recorder))

	if _, err := ag.Chat(context.Background(), "s-prune-1", "hello"); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	events, err := recorder.LoadEvents(context.Background(), "s-prune-1")
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("events after a fully-completed, single-step conversation = %d, want 0 (pruned)", len(events))
	}
}

// TestPruneStepIfResolved_UnresolvedGap_IsNotPruned is the negative direction
// of retention, which the positive case above cannot cover: a step whose
// tool_call_intent has NO matching tool_call_result must survive, even when
// pruneStepIfResolved is called on it directly.
//
// The caller always calls it believing the step resolved, but recordToolResult,
// PersistToolExecution and appendMessage are all fail-open — any of them can
// silently fail, and the loop-tail call site is additionally reached when a
// MaxToolExecutions break stopped the batch partway. Pruning on that belief
// deletes the one record ResumeSession's backfill/gap branches read, so the
// safety net vanishes at the exact moment it exists for.
//
// The fixture is the realistic shape: the LLM call of this step DID resolve
// (llm_call_intent + llm_call_result, both at batch_position 0 — the literal
// position recordLLMIntent/recordLLMResult use) while tool call 0 — also at
// batch_position 0 — did not. Pairing intents to results by batch_position
// alone would let the llm_call_result vouch for the missing tool_call_result
// and prune anyway.
func TestPruneStepIfResolved_UnresolvedGap_IsNotPruned(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithEventLog(recorder))

	ctx := context.Background()
	const sid = "s-prune-gap"
	if err := recorder.RecordIntent(ctx, sid, 0, 0, eventlog.EventLLMCallIntent, "", []byte(`{"model":"mock"}`)); err != nil {
		t.Fatalf("fixture llm intent: %v", err)
	}
	if err := recorder.RecordResult(ctx, sid, 0, 0, eventlog.EventLLMCallResult, "", []byte(`{"content":""}`)); err != nil {
		t.Fatalf("fixture llm result: %v", err)
	}
	if err := recorder.RecordIntent(ctx, sid, 0, 0, eventlog.EventToolCallIntent, "c0", []byte(`{"tool_name":"crashy"}`)); err != nil {
		t.Fatalf("fixture tool intent pos 0: %v", err)
	}
	if err := recorder.RecordIntent(ctx, sid, 0, 1, eventlog.EventToolCallIntent, "c1", []byte(`{"tool_name":"crashy"}`)); err != nil {
		t.Fatalf("fixture tool intent pos 1: %v", err)
	}
	if err := recorder.RecordResult(ctx, sid, 0, 1, eventlog.EventToolCallResult, "c1", []byte(`{"result":{"success":true},"error":""}`)); err != nil {
		t.Fatalf("fixture tool result pos 1: %v", err)
	}
	before, err := recorder.LoadEvents(ctx, sid)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}

	ag.pruneStepIfResolved(ctx, sid, 0, true)

	after, err := recorder.LoadEvents(ctx, sid)
	if err != nil {
		t.Fatalf("LoadEvents after prune: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("events after pruning a step with an unresolved tool_call_intent = %d, want %d (unchanged) — a step with a gap must stay authoritative for resume",
			len(after), len(before))
	}

	// Control: once the missing result lands, the same call does prune — so
	// the test above is proving the gap check, not a dead code path.
	if err := recorder.RecordResult(ctx, sid, 0, 0, eventlog.EventToolCallResult, "c0", []byte(`{"result":{"success":true},"error":""}`)); err != nil {
		t.Fatalf("fixture late tool result pos 0: %v", err)
	}
	ag.pruneStepIfResolved(ctx, sid, 0, true)
	resolved, err := recorder.LoadEvents(ctx, sid)
	if err != nil {
		t.Fatalf("LoadEvents after second prune: %v", err)
	}
	if len(resolved) != 0 {
		t.Errorf("events after pruning a fully-resolved step = %d, want 0", len(resolved))
	}
}

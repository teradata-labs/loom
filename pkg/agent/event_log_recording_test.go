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
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// recordingEventLog is an in-memory eventlog.EventLogStore that records
// every call made to it, for asserting exactly what the live loop wrote.
//
// events is the "live" store: PruneResolvedStep actually deletes from it,
// same as a real backend, so LoadEvents reflects retention. allEvents is a
// separate, append-only audit trail that retention never touches — tests
// that need to assert something WAS recorded at some point (e.g. the
// originating step_index of an LLM call, or that a tool result was ever
// written), even though Task 7 retention has since pruned it from the live
// store, read allEvents instead of LoadEvents.
type recordingEventLog struct {
	mu        sync.Mutex
	events    []eventlog.ExecutionEvent
	allEvents []eventlog.ExecutionEvent
	claims    map[string]string
}

func (r *recordingEventLog) RecordIntent(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	evt := eventlog.ExecutionEvent{SessionID: sessionID, StepIndex: stepIndex, BatchPosition: batchPosition, EventType: eventType, ToolUseID: toolUseID, Payload: payload}
	r.events = append(r.events, evt)
	r.allEvents = append(r.allEvents, evt)
	return nil
}

// AllRecordedEvents returns every event ever recorded for sessionID,
// regardless of any later PruneResolvedStep call — see the allEvents doc
// comment above.
func (r *recordingEventLog) AllRecordedEvents(sessionID string) []eventlog.ExecutionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []eventlog.ExecutionEvent
	for _, e := range r.allEvents {
		if e.SessionID == sessionID {
			out = append(out, e)
		}
	}
	return out
}
func (r *recordingEventLog) RecordResult(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	return r.RecordIntent(ctx, sessionID, stepIndex, batchPosition, eventType, toolUseID, payload)
}
func (r *recordingEventLog) LoadEvents(ctx context.Context, sessionID string) ([]eventlog.ExecutionEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []eventlog.ExecutionEvent
	for _, e := range r.events {
		if e.SessionID == sessionID {
			out = append(out, e)
		}
	}
	return out, nil
}
func (r *recordingEventLog) NextStepIndex(ctx context.Context, sessionID string) (int, error) {
	events, _ := r.LoadEvents(ctx, sessionID)
	max := -1
	for _, e := range events {
		if e.StepIndex > max {
			max = e.StepIndex
		}
	}
	return max + 1, nil
}
func (r *recordingEventLog) AcquireSessionLease(ctx context.Context, sessionID, holder string, ttl time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claims == nil {
		r.claims = make(map[string]string)
	}
	if existing, ok := r.claims[sessionID]; ok && existing != holder {
		return false, nil
	}
	r.claims[sessionID] = holder
	return true, nil
}
func (r *recordingEventLog) RenewSessionLease(ctx context.Context, sessionID, holder string, ttl time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claims[sessionID] == holder, nil
}
func (r *recordingEventLog) ReleaseSessionLease(ctx context.Context, sessionID, holder string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claims[sessionID] == holder {
		delete(r.claims, sessionID)
	}
	return nil
}
func (r *recordingEventLog) PruneStepsBefore(ctx context.Context, sessionID string, stepIndex int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.events[:0]
	for _, e := range r.events {
		if e.SessionID == sessionID && e.StepIndex < stepIndex {
			continue
		}
		kept = append(kept, e)
	}
	r.events = kept
	return nil
}
func (r *recordingEventLog) PruneResolvedStep(ctx context.Context, sessionID string, stepIndex int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.events[:0]
	for _, e := range r.events {
		if e.SessionID == sessionID && e.StepIndex == stepIndex {
			continue
		}
		kept = append(kept, e)
	}
	r.events = kept
	return nil
}

var _ eventlog.EventLogStore = (*recordingEventLog)(nil)

func TestRunConversationLoop_RecordsLLMAndToolEvents(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	ag := NewAgent(&mockBackend{},
		&mockToolCallingLLM{responses: []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "calculator", Input: map[string]interface{}{"expression": "1+1"}}}},
		}},
		WithConfig(cfg), WithEventLog(recorder))
	ag.tools.Register(&mockCalculatorTool{})

	_, err := ag.Chat(context.Background(), "s-record-1", "compute something")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// This step fully resolves inline (tool call dispatched, LLM gives a
	// final text answer) so Task 7 retention prunes it from the live store
	// by the time Chat returns — read the never-pruned audit trail instead
	// to assert what WAS recorded during the call.
	events := recorder.AllRecordedEvents("s-record-1")

	var sawLLMIntent, sawLLMResult, sawToolIntent, sawToolResult bool
	for _, e := range events {
		switch e.EventType {
		case eventlog.EventLLMCallIntent:
			sawLLMIntent = true
		case eventlog.EventLLMCallResult:
			sawLLMResult = true
			var resp llmtypes.LLMResponse
			if err := json.Unmarshal(e.Payload, &resp); err != nil {
				t.Errorf("llm_call_result payload did not unmarshal to LLMResponse: %v", err)
			}
		case eventlog.EventToolCallIntent:
			sawToolIntent = true
			if e.ToolUseID != "c1" {
				t.Errorf("tool_call_intent ToolUseID = %q, want %q", e.ToolUseID, "c1")
			}
		case eventlog.EventToolCallResult:
			sawToolResult = true
		}
	}
	if !sawLLMIntent || !sawLLMResult || !sawToolIntent || !sawToolResult {
		t.Errorf("missing expected event types, got: %+v", events)
	}
}

func TestRunConversationLoop_NoEventLog_NoPanicNoRecording(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg)) // no WithEventLog

	if _, err := ag.Chat(context.Background(), "s-no-log", "hello"); err != nil {
		t.Fatalf("Chat without an event log configured must still work: %v", err)
	}
}

// conflictingEventLog reports every intent as conflicting with a different
// event already recorded at the same key.
type conflictingEventLog struct {
	recordingEventLog
	attempts atomic.Int32
}

func (c *conflictingEventLog) RecordIntent(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	c.attempts.Add(1)
	return eventlog.ErrEventConflict
}

// A conflict means another writer recorded a different call at this step;
// retrying cannot fix that, so the intent fails on the first attempt.
func TestRecordIntentWithRetry_ConflictIsNotRetried(t *testing.T) {
	store := &conflictingEventLog{}
	a := &Agent{eventLog: store}
	err := a.recordIntentWithRetry(context.Background(), "s", 0, 0, eventlog.EventToolCallIntent, "c", []byte(`{}`))
	if !errors.Is(err, eventlog.ErrEventConflict) {
		t.Fatalf("got %v, want ErrEventConflict", err)
	}
	if n := store.attempts.Load(); n != 1 {
		t.Fatalf("RecordIntent called %d times, want 1", n)
	}
}

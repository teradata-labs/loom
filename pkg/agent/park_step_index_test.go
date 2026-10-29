// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package agent

// completeParkedBatch (park.go) builds its own batchState literal to dispatch
// a parked batch's calls through the shared dispatchOneCall ceremony. That
// literal must carry the step_index of the LLM call that actually produced
// the parked batch — not the Go zero value — or every tool call completed out
// of a park records its execution_events at step 0 regardless of which step
// the batch belongs to, defeating crash-resume correlation on exactly the
// human-wait path where it matters.
//
// This test drives the park to land on a NON-zero step (a prior, unparked
// iteration consumes step 0 first) so a wrong-but-still-a-coincidence zero
// cannot pass silently, then asserts the parked batch's tool_call_intent/
// result events were recorded under the SAME step_index as the llm_call
// events of the iteration that actually produced them.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/teradata-labs/loom/pkg/eventlog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Review round 2, B5: the loop records a call's events at its index in the LLM
// response, but a saved assistant message drops query_tool_result calls, so the
// call's index in the batch the session holds is smaller. completeParkedBatch
// must record at the response's index, or a crash inside it leaves intents at
// positions that crash-resume reads as never attempted.
//
// Driven directly: through ResumeChat a session reloaded without the
// query_tool_result call is refused first, because a park request's item seq is
// the call's index in the full response (verifyItemBinding).
func TestCompleteParkedBatch_RecordsAtResponsePosition(t *testing.T) {
	recorder := &recordingEventLog{}
	tool := &crashingTool{}
	ag := newEventLogAgent(t, &mockSimpleLLM{}, nil, recorder, tool)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-pos", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "go"}, true)
	// The session holds the batch as the store keeps it: no query_tool_result.
	saved := []llmtypes.ToolCall{{ID: "c-write", Name: "crashy", Input: map[string]interface{}{"v": "w"}}}
	ag.appendMessage(ctx, sess, Message{Role: "assistant", ToolCalls: saved}, false)
	// The log holds the LLM response as the loop saw it.
	recorded := []llmtypes.ToolCall{
		{ID: "c-query", Name: queryToolResultName, Input: map[string]interface{}{"message_id": float64(1)}},
		saved[0],
	}
	payload, err := json.Marshal(LLMResponse{ToolCalls: recorded})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := recorder.RecordResult(ctx, sess.ID, 4, 0, eventlog.EventLLMCallResult, "", payload); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}

	batch, rowless, positions, err := locateParkedBatch(sess, nil)
	if err != nil {
		t.Fatalf("locateParkedBatch: %v", err)
	}
	if positions[0] != 0 {
		t.Fatalf("setup: the call's index in the saved batch is %d, want 0", positions[0])
	}
	agentCtx := &agentContext{Context: ctx, session: sess, tracer: ag.tracer}
	ag.completeParkedBatch(agentCtx, sess, batch, rowless, positions, ParkDecision{Approved: true}, nil, nil)

	if tool.calls != 1 {
		t.Fatalf("crashy ran %d times, want 1", tool.calls)
	}
	var seen int
	for _, e := range recorder.AllRecordedEvents(sess.ID) {
		if e.ToolUseID != "c-write" {
			continue
		}
		seen++
		if e.StepIndex != 4 || e.BatchPosition != 1 {
			t.Errorf("%s for c-write recorded at step %d position %d, want step 4 position 1 (its place in the recorded LLM response)",
				e.EventType, e.StepIndex, e.BatchPosition)
		}
	}
	if seen != 2 {
		t.Fatalf("recorded %d events for c-write, want an intent and a result", seen)
	}
}

func TestPark_CompletedBatchRecordsAtOriginatingStepIndex(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	sessions, err := NewSessionStore(filepath.Join(t.TempDir(), "park-step.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	t.Cleanup(func() { _ = sessions.Close() })

	store := shuttle.NewInMemoryHumanRequestStore()
	recorder := &recordingEventLog{}

	// Iteration 1 (step 0): an ungoverned call, dispatches normally, loop
	// continues. Iteration 2 (step 1): an ask-governed call — parks here,
	// so the parked batch's originating LLM call is NOT at step 0.
	script := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-first", Name: "read_table", Input: map[string]interface{}{"v": "r"}},
		}},
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-write", Name: "export_csv", Input: map[string]interface{}{"v": "w"}},
		}},
		{content: "continued"},
	}

	ag := NewAgent(&mockBackend{},
		&mockToolCallingLLM{responses: script},
		WithConfig(cfg),
		WithMemory(NewMemoryWithStore(sessions)),
		WithHITLPark(store, 0, NewProgressNotifier()),
		WithAdmissionHooks(shuttle.NewChain([]shuttle.Hook{scopedAskHook{tool: "export_csv"}}, nil, nil)),
		WithEventLog(recorder),
	)
	ag.RegisterTool(&countingTool{name: "read_table"})
	ag.RegisterTool(&countingTool{name: "export_csv"})

	ctx := context.Background()
	_, err = ag.Chat(ctx, "s-stepidx", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}

	reqs, err := store.ListBySession(ctx, "s-stepidx")
	if err != nil || len(reqs) != 1 {
		t.Fatalf("ListBySession: %v, %d requests", err, len(reqs))
	}
	hr := reqs[0]
	itemIDs := make([]string, 0, len(hr.Params))
	for k := range hr.Params {
		itemIDs = append(itemIDs, k)
	}

	// Find the step_index the SECOND llm_call_intent (the parking iteration)
	// was recorded under — this is the step the parked batch actually belongs
	// to, derived independently of the fix under test. Iteration 1 (step 0)
	// fully resolved inline (no park), so Task 7 retention has already
	// pruned it from the live store by the time Chat returns — read the
	// never-pruned audit trail instead so both iterations are visible.
	events := recorder.AllRecordedEvents("s-stepidx")
	var llmSteps []int
	for _, e := range events {
		if e.EventType == eventlog.EventLLMCallIntent {
			llmSteps = append(llmSteps, e.StepIndex)
		}
	}
	if len(llmSteps) != 2 {
		t.Fatalf("expected 2 llm_call_intent events (one per iteration), got %v", llmSteps)
	}
	originatingStep := llmSteps[1]
	if originatingStep == 0 {
		t.Fatalf("test setup bug: parking iteration landed on step 0, which cannot " +
			"distinguish the fix from the zero-value bug")
	}

	// Complete the park.
	resp, err := ag.ResumeChat(ctx, "s-stepidx", ParkDecision{
		RequestID: hr.ID,
		ItemIDs:   itemIDs,
		Approved:  true,
	}, nil)
	if err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	if resp.Content != "continued" {
		t.Fatalf("resume content = %q, want %q", resp.Content, "continued")
	}

	// The completed batch's tool_call_intent/result events for c-write must
	// carry originatingStep, not 0. The finished turn's events are pruned once
	// its reply is saved, so read everything that was ever recorded.
	events = recorder.AllRecordedEvents("s-stepidx")
	var sawIntent, sawResult bool
	for _, e := range events {
		if e.ToolUseID != "c-write" {
			continue
		}
		switch e.EventType {
		case eventlog.EventToolCallIntent:
			sawIntent = true
			if e.StepIndex != originatingStep {
				t.Errorf("tool_call_intent for c-write recorded at step %d, want %d (the parked batch's originating step)",
					e.StepIndex, originatingStep)
			}
		case eventlog.EventToolCallResult:
			sawResult = true
			if e.StepIndex != originatingStep {
				t.Errorf("tool_call_result for c-write recorded at step %d, want %d (the parked batch's originating step)",
					e.StepIndex, originatingStep)
			}
		}
	}
	if !sawIntent || !sawResult {
		t.Fatalf("missing tool_call_intent/result for c-write: %+v", events)
	}
}

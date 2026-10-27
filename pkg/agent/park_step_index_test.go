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
	"errors"
	"path/filepath"
	"testing"

	"github.com/teradata-labs/loom/pkg/eventlog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

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
	// carry originatingStep, not 0.
	events, err = recorder.LoadEvents(ctx, "s-stepidx")
	if err != nil {
		t.Fatalf("LoadEvents after resume: %v", err)
	}
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

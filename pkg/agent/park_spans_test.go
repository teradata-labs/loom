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

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// TestPark_ResumeBatchSpansNestUnderResume checks the span tree of a resumed
// turn: agent.conversation.resume carries the parked turn's index as
// session.turn, agent.resume_batch is its child, and the tool executions of
// the resumed batch are children of agent.resume_batch (as a normal turn's
// are children of agent.conversation_loop), not siblings of it.
func TestPark_ResumeBatchSpansNestUnderResume(t *testing.T) {
	tracer := observability.NewMockTracer()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	sessions, err := NewSessionStore(filepath.Join(t.TempDir(), "park.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	t.Cleanup(func() { _ = sessions.Close() })
	store := shuttle.NewInMemoryHumanRequestStore()
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{responses: twoCallBatch()},
		WithConfig(cfg),
		WithMemory(NewMemoryWithStore(sessions)),
		WithHITLPark(store, 0, NewProgressNotifier()),
		WithAdmissionHooks(shuttle.NewChain([]shuttle.Hook{scopedAskHook{tool: "export_csv"}}, nil, nil)),
		WithTracer(tracer),
	)
	ag.RegisterTool(&countingTool{name: "read_table"})
	ag.RegisterTool(&countingTool{name: "export_csv"})

	_, err = ag.Chat(context.Background(), "s-spans", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	reqs, err := store.ListBySession(context.Background(), "s-spans")
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	var hr *shuttle.HumanRequest
	for _, r := range reqs {
		if r.Status == "pending" && r.RequestType == "parked" {
			hr = r
		}
	}
	if hr == nil {
		t.Fatalf("no pending parked request")
	}
	sess := ag.memory.GetOrCreateSessionWithAgent(context.Background(), "s-spans", ag.config.Name, "")
	parkedTurn, _ := parkedTurnIdentity(sess)

	tracer.Reset()
	if _, err := ag.ResumeChat(context.Background(), "s-spans", ParkDecision{
		RequestID: hr.ID,
		ItemIDs:   paramKeys(hr),
		Approved:  true,
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}

	resume := tracer.GetSpanByName("agent.conversation.resume")
	if resume == nil {
		t.Fatalf("no agent.conversation.resume span")
	}
	if got := resume.Attributes["session.turn"]; got != parkedTurn {
		t.Fatalf("session.turn on the resume span = %v (%T), want %d", got, got, parkedTurn)
	}
	batch := tracer.GetSpanByName("agent.resume_batch")
	if batch == nil {
		t.Fatalf("no agent.resume_batch span")
	}
	if batch.ParentID != resume.SpanID {
		t.Fatalf("agent.resume_batch parent = %s, want the resume span %s", batch.ParentID, resume.SpanID)
	}
	toolSpans := tracer.GetSpansByName("agent.tool_execution")
	if len(toolSpans) != 2 {
		t.Fatalf("agent.tool_execution spans = %d, want 2 (the resumed batch)", len(toolSpans))
	}
	for _, s := range toolSpans {
		if s.ParentID != batch.SpanID {
			t.Fatalf("tool span %s parent = %s, want agent.resume_batch %s", s.Attributes[observability.AttrToolName], s.ParentID, batch.SpanID)
		}
	}
}

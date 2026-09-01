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

// Per-TURN work must happen once per turn, not once per loop entry — a parked
// turn enters the loop twice. And the card the human is shown must describe
// what the model actually asked for. Each test here was written against a
// deletion that the suite previously accepted in silence.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// metricRecorder wraps a tracer and remembers the metric names it recorded.
// The package's other recording tracer lives in package agent_test and so is
// not reachable from here.
type metricRecorder struct {
	observability.Tracer
	mu    sync.Mutex
	names []string
}

func (m *metricRecorder) RecordMetric(name string, value float64, labels map[string]string) {
	m.mu.Lock()
	m.names = append(m.names, name)
	m.mu.Unlock()
	m.Tracer.RecordMetric(name, value, labels)
}

func (m *metricRecorder) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.names)
}

func (m *metricRecorder) namesAfter(n int) map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]bool{}
	for _, name := range m.names[n:] {
		out[name] = true
	}
	return out
}

// TestPark_ResumeDoesNotReinjectGraphMemoryContext — the loop injects graph
// memory context at ENTRY, and a resumed turn enters it a second time for the
// same turn. Without a resume marker the block is injected twice, duplicating
// memory text in the prompt and paying a second recall round-trip per resume.
func TestPark_ResumeDoesNotReinjectGraphMemoryContext(t *testing.T) {
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, twoCallBatch(),
		"read_table", "export_csv")
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-graph", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-graph")

	countBlocks := func() int {
		sess := f.ag.memory.GetOrCreateSessionWithAgent(ctx, "s-graph", f.ag.config.Name, "")
		n := 0
		for _, m := range rawSessionMessages(sess) {
			if strings.Contains(m.Content, "[Graph Memory Context]") {
				n++
			}
		}
		return n
	}
	before := countBlocks()

	if _, err := f.ag.ResumeChat(ctx, "s-graph", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}

	if after := countBlocks(); after != before {
		t.Errorf("graph memory context blocks went %d → %d across a resume; "+
			"per-turn entry work ran twice for one turn", before, after)
	}
	if !isResumedTurn(contextWithResumedTurn(ctx)) {
		t.Error("contextWithResumedTurn/isResumedTurn do not round-trip")
	}
}

// TestPark_ResumedTurnIsCounted — a resumed turn is the one carrying the
// human-approved actions. If it records none of the conversation metrics,
// exactly the highest-risk turns go missing from the metrics backend.
func TestPark_ResumedTurnIsCounted(t *testing.T) {
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, twoCallBatch(),
		"read_table", "export_csv")
	tracer := &metricRecorder{Tracer: f.ag.tracer}
	f.ag.tracer = tracer
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-metrics", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-metrics")

	// The park exit records nothing (the turn has not completed), so anything
	// seen after this point belongs to the resume.
	beforeResume := tracer.count()

	if _, err := f.ag.ResumeChat(ctx, "s-metrics", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}

	seen := tracer.namesAfter(beforeResume)
	for _, want := range []string{
		"agent.turns.total",
		"agent.tool_executions.total",
		"agent.cost.usd",
		"agent.tokens.total",
	} {
		if !seen[want] {
			t.Errorf("resumed turn recorded no %q — the turns carrying human-approved "+
				"actions are missing from the metrics backend", want)
		}
	}
}

// TestPark_AllQuestionParkRendersAsAQuestionCard — a card renderer keys its
// shape off Kind. A park whose items are all questions must not arrive as an
// approve/reject prompt, or the human is asked to approve where the model
// asked them something.
func TestPark_AllQuestionParkRendersAsAQuestionCard(t *testing.T) {
	script := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-q", Name: "contact_human", Input: map[string]interface{}{"question": "prod or staging?"}},
		}},
		{content: "done"},
	}
	f := newParkFixture(t, nil, script, "contact_human")
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-qcard", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-qcard")

	if hr.Kind != "question" {
		t.Errorf("card Kind = %q, want \"question\" for an all-question park", hr.Kind)
	}
	if hr.Question != "prod or staging?" {
		t.Errorf("card Question = %q, want the model's own question verbatim", hr.Question)
	}
}

// TestPark_MixedBatchRendersAsAnApprovalCard — anything other than an
// all-question batch is an approval over the batch.
func TestPark_MixedBatchRendersAsAnApprovalCard(t *testing.T) {
	script := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-q", Name: "contact_human", Input: map[string]interface{}{"question": "which env?"}},
			{ID: "c-w", Name: "export_csv", Input: map[string]interface{}{"v": "w"}},
		}},
		{content: "done"},
	}
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, script,
		"contact_human", "export_csv")
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-mixcard", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-mixcard")

	if hr.Kind != "approval" {
		t.Errorf("mixed-batch card Kind = %q, want \"approval\"", hr.Kind)
	}
	if !strings.Contains(hr.Question, "2") {
		t.Errorf("mixed-batch card Question = %q, want it to name both pending actions", hr.Question)
	}
}

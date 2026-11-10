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
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// projectionProbeTool records whether its call's context carried an
// advertised projection.
type projectionProbeTool struct {
	calls         int
	hadProjection bool
}

func (p *projectionProbeTool) Name() string        { return "probe" }
func (p *projectionProbeTool) Description() string { return "records the call's projection" }
func (p *projectionProbeTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (p *projectionProbeTool) Execute(ctx context.Context, _ map[string]interface{}) (*shuttle.Result, error) {
	p.calls++
	_, p.hadProjection = advertisedProjectionFromContext(ctx)
	return &shuttle.Result{Success: true, Data: "ok"}, nil
}
func (p *projectionProbeTool) Backend() string { return "" }

// A crashed batch has no recorded projection, so ResumeSession dispatches its
// never-attempted calls with none and a script engine fails closed.
func TestResumeSession_DispatchesWithoutAProjection(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithEventLog(recorder))
	probe := &projectionProbeTool{}
	ag.tools.Register(probe)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-resume-noproj", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "probe"}, true)
	ag.appendMessage(ctx, sess, Message{
		Role:      "assistant",
		ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "probe", Input: map[string]interface{}{}}},
	}, false)
	recordTailBatch(t, recorder, sess, 0)

	if _, err := ag.ResumeSession(ctx, "s-resume-noproj"); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if probe.calls != 1 {
		t.Fatalf("probe ran %d times, want 1", probe.calls)
	}
	if probe.hadProjection {
		t.Error("a call dispatched by ResumeSession carried a projection; want none (fail closed)")
	}
}

// A ResumeSession re-park records no projection, so the later ResumeChat
// takes the no-record path instead of granting the resume-time tool set.
func TestResumeSession_ReParkRecordsNoProjection(t *testing.T) {
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	sessions, err := NewSessionStore(filepath.Join(t.TempDir(), "resume-repark-noproj.db"), observability.NewNoOpTracer())
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
	ag.RegisterTool(&countingTool{name: "contact_human"})

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-repark-noproj", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "deploy it"}, true)
	ag.appendMessage(ctx, sess, Message{
		Role: "assistant",
		ToolCalls: []llmtypes.ToolCall{
			{ID: "c-q", Name: "contact_human", Input: map[string]interface{}{"question": "prod or staging?"}},
		},
	}, false)
	recordTailBatch(t, recorder, sess, 0)

	_, err = ag.ResumeSession(ctx, "s-repark-noproj")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("ResumeSession error = %v, want *TurnParkedError", err)
	}
	hr, err := store.Get(ctx, parked.RequestID)
	if err != nil {
		t.Fatalf("Get parked request: %v", err)
	}
	if names, ok := parkedProjection(hr); ok {
		t.Errorf("re-park recorded projection %v; want none", names)
	}
	if got := hr.Context["kind"]; got != "parked" {
		t.Errorf("Context[kind] = %v, want parked", got)
	}
}

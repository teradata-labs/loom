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

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// An ending tool that parked for approval ends the turn when it runs on
// resume: the model is not asked again, and the reply is what was said with it.
func TestAnEndingToolApprovedOnResumeEndsTheTurn(t *testing.T) {
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "hand_off"}}, []mockLLMResponse{
		{content: "Taking you back.", toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "hand_off", Input: map[string]interface{}{}}}},
		{content: "said to nobody"},
	})
	h := &handOff{}
	f.ag.RegisterTool(h)
	_, err := f.ag.Chat(context.Background(), "s1", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("want park, got %v", err)
	}
	hr := f.pendingParked(t, "s1")
	resp, err := f.ag.ResumeChat(context.Background(), "s1", ParkDecision{RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 {
		t.Fatalf("hand_off ran %d times", h.calls)
	}
	if resp.Content != "Taking you back." {
		t.Fatalf("reply = %q: the model was asked again after the tool ended the turn", resp.Content)
	}
	if resp.Metadata["ended_by"] != "hand_off" {
		t.Errorf("the reply does not name what ended the turn: %v", resp.Metadata)
	}
	if n := countAssistant(f.ag, "s1", "Taking you back."); n != 1 {
		t.Errorf("the turn's words are stored %d times, want once", n)
	}
}

// A turn that a tool ends inside a resumed loop stores its words, and their
// cost, once.
func TestATurnEndedInAResumedLoopStoresItsWordsOnce(t *testing.T) {
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "export_csv", Input: map[string]interface{}{"v": "w"}}}},
		{content: "Bye now.", toolCalls: []llmtypes.ToolCall{{ID: "c2", Name: "hand_off", Input: map[string]interface{}{}}}},
		{content: "never"},
	}, "export_csv")
	f.ag.RegisterTool(&handOff{})
	_, err := f.ag.Chat(context.Background(), "s2", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("want park, got %v", err)
	}
	hr := f.pendingParked(t, "s2")
	resp, err := f.ag.ResumeChat(context.Background(), "s2", ParkDecision{RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Bye now." {
		t.Fatalf("reply = %q", resp.Content)
	}
	if n := countAssistant(f.ag, "s2", "Bye now."); n != 1 {
		t.Errorf("the turn's words are stored %d times, want once", n)
	}
}

// An ending tool that ran in the same batch as a call held for a resource
// ends the turn once the held call's result arrives on resume.
func TestAnEndingToolBeforeAResourceParkEndsTheTurnOnResume(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	sessions, err := NewSessionStore(filepath.Join(t.TempDir(), "ended-resource.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.Close() })
	mem := shuttle.NewInMemoryHumanRequestStore()
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		{content: "Started it; back to you.", toolCalls: []llmtypes.ToolCall{
			{ID: "c-job", Name: "start_job", Input: map[string]interface{}{"v": "j"}},
			{ID: "c-off", Name: "hand_off", Input: map[string]interface{}{}},
		}},
		{content: "said to nobody"},
	}}
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithMemory(NewMemoryWithStore(sessions)),
		WithHITLPark(mem, 0, NewProgressNotifier()), WithResourceAwait(&fakeAwaitHandler{}))
	ag.RegisterTool(&awaitingTool{name: "start_job", uri: "gdp://jobs/42"})
	ag.RegisterTool(&handOff{})

	_, err = ag.Chat(context.Background(), "s3", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("want a resource park, got %v", err)
	}
	hr := pendingResourcePark(t, mem, "s3")
	resp, err := ag.ResumeChat(context.Background(), "s3", ParkDecision{
		RequestID: hr.ID, Approved: true,
		Results: map[string]interface{}{"c-job": map[string]interface{}{"status": "completed"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Started it; back to you." {
		t.Fatalf("reply = %q: the model was asked again after the turn was ended", resp.Content)
	}
	if llm.currentIdx != 1 {
		t.Fatalf("the model was asked %d times, want once", llm.currentIdx)
	}
}

// A turn a tool ended reports why the last call stopped, as every ended turn does.
func TestAnEndedTurnReportsItsStopReason(t *testing.T) {
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		{content: "Taking you back.", toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "hand_off", Input: map[string]interface{}{}}}},
	}}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	ag.RegisterTool(&handOff{})
	resp, err := ag.Chat(context.Background(), "s4", "take me back")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resp.Metadata["stop_reason"]; !ok {
		t.Errorf("an ended turn's metadata has no stop_reason: %v", resp.Metadata)
	}
}

func countAssistant(ag *Agent, sessionID, content string) int {
	n := 0
	for _, m := range rawSessionMessages(ag.memory.GetOrCreateSessionWithAgent(context.Background(), sessionID, ag.config.Name, "")) {
		if m.Role == "assistant" && m.Content == content {
			n++
		}
	}
	return n
}

// A call id reused by a later turn (some providers reuse them) does not make
// the earlier turn's ending tool end the resumed one: only the parked batch's
// own rows count.
func TestAReusedCallIDDoesNotEndAResumedTurn(t *testing.T) {
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, []mockLLMResponse{
		{content: "Taking you back.", toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "hand_off", Input: map[string]interface{}{}}}},
		{content: "exporting", toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "export_csv", Input: map[string]interface{}{"v": "w"}}}},
		{content: "Export done."},
	}, "export_csv")
	f.ag.RegisterTool(&handOff{})
	if _, err := f.ag.Chat(context.Background(), "r1", "first"); err != nil {
		t.Fatal(err)
	}
	_, err := f.ag.Chat(context.Background(), "r1", "second")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("want park, got %v", err)
	}
	hr := f.pendingParked(t, "r1")
	resp, err := f.ag.ResumeChat(context.Background(), "r1", ParkDecision{RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Export done." {
		t.Fatalf("the resumed turn was ended by an earlier turn's tool: content=%q meta=%v", resp.Content, resp.Metadata)
	}
}

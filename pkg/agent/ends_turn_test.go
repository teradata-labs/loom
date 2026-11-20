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
	"strings"
	"testing"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// handOff is a tool whose result ends the turn it ran in.
type handOff struct{ calls int }

func (h *handOff) Name() string                     { return "hand_off" }
func (h *handOff) Description() string              { return "hands the conversation off" }
func (h *handOff) InputSchema() *shuttle.JSONSchema { return &shuttle.JSONSchema{Type: "object"} }
func (h *handOff) Backend() string                  { return "" }
func (h *handOff) Execute(context.Context, map[string]interface{}) (*shuttle.Result, error) {
	h.calls++
	return &shuttle.Result{Success: true, Data: "handed off",
		Metadata: map[string]interface{}{EndsTurnKey: true}}, nil
}

// A tool that ends the turn ends it: the model is not asked for anything
// more, and the turn's reply is what it said alongside the call.
func TestAToolThatEndsTheTurnEndsIt(t *testing.T) {
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		{content: "Taking you back.", toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "hand_off", Input: map[string]interface{}{}}}},
		{content: "Handed back — here is everything again."},
	}}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	tool := &handOff{}
	ag.RegisterTool(tool)

	resp, err := ag.Chat(context.Background(), "s-ends", "take me back")
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls != 1 {
		t.Fatalf("the tool ran %d times", tool.calls)
	}
	if resp.Content != "Taking you back." {
		t.Fatalf("the turn's reply is %q, want what was said with the call", resp.Content)
	}
	if llm.currentIdx != 1 {
		t.Fatalf("the model was asked %d times; nothing is asked after the turn ends", llm.currentIdx)
	}
	if resp.Metadata["ended_by"] != "hand_off" {
		t.Errorf("the reply does not say what ended the turn: %v", resp.Metadata)
	}
	// Its words are stored once, with the call.
	msgs, _ := ag.memory.GetOrCreateSession(context.Background(), "s-ends"), 0
	count := 0
	for _, m := range msgs.GetMessages() {
		if m.Role == "assistant" && m.Content == "Taking you back." {
			count++
		}
	}
	if count != 1 {
		t.Errorf("the turn's words are stored %d times, want once", count)
	}
}

// bigHandOff ends the turn and returns more than the result bound holds.
type bigHandOff struct{ handOff }

func (h *bigHandOff) Execute(context.Context, map[string]interface{}) (*shuttle.Result, error) {
	h.calls++
	return &shuttle.Result{Success: true, Data: strings.Repeat("handed off row\n", 20000),
		Metadata: map[string]interface{}{EndsTurnKey: true}}, nil
}

// A tool ends the turn whatever the size of what it returned, on the resume
// path as in a turn that never parked: the word that it ended the turn
// outlives the record too large to store.
func TestAnEndingToolWithALargeResultEndsTheTurnOnResume(t *testing.T) {
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "hand_off"}}, []mockLLMResponse{
		{content: "Taking you back.", toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "hand_off", Input: map[string]interface{}{}}}},
		{content: "said to nobody"},
	})
	f.ag.RegisterTool(&bigHandOff{})
	_, err := f.ag.Chat(context.Background(), "s1", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("want a park, got %v", err)
	}
	hr := f.pendingParked(t, "s1")

	resp, err := f.ag.ResumeChat(context.Background(), "s1", ParkDecision{RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Taking you back." {
		t.Fatalf("reply = %q: the model was asked again after the tool ended the turn", resp.Content)
	}
	if resp.Metadata["ended_by"] != "hand_off" {
		t.Fatalf("ended_by = %v, want hand_off", resp.Metadata["ended_by"])
	}
}

// A turn a tool ended is finished. A crash resume of the session finds
// nothing to resume, and does not ask the model for the reply the tool ended.
func TestResumeSession_ATurnAToolEndedHasNothingToResume(t *testing.T) {
	recorder := &recordingEventLog{}
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		{content: "Taking you back.", toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "hand_off", Input: map[string]interface{}{}}}},
		{content: "said to nobody"},
	}}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithEventLog(recorder))
	ag.RegisterTool(&handOff{})
	if _, err := ag.Chat(context.Background(), "s-ended", "take me back"); err != nil {
		t.Fatal(err)
	}
	if llm.currentIdx != 1 {
		t.Fatalf("fixture: the model was asked %d times", llm.currentIdx)
	}

	_, err := ag.ResumeSession(context.Background(), "s-ended")
	if !errors.Is(err, ErrNothingParked) {
		t.Fatalf("ResumeSession on a turn a tool ended = %v, want ErrNothingParked", err)
	}
	if llm.currentIdx != 1 {
		t.Fatalf("the model was asked again after the tool ended the turn (%d calls)", llm.currentIdx)
	}
}

// A crashed batch whose call that never ran is the ending tool: the resume
// runs it, and the turn ends there.
func TestResumeSession_AnEndingToolRunOnResumeEndsTheTurn(t *testing.T) {
	recorder := &recordingEventLog{}
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{{content: "said to nobody"}}}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithEventLog(recorder))
	h := &handOff{}
	ag.RegisterTool(h)
	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-ended-on-resume", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "go"}, true)
	ag.appendMessage(ctx, sess, Message{Role: "assistant", Content: "Taking you back.",
		ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "hand_off", Input: map[string]interface{}{}}}}, false)
	recordTailBatch(t, recorder, sess, 0)

	resp, err := ag.ResumeSession(ctx, "s-ended-on-resume")
	if err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 {
		t.Fatalf("hand_off ran %d times", h.calls)
	}
	if llm.currentIdx != 0 {
		t.Fatalf("the model was asked %d times after the ending tool ran; reply = %q", llm.currentIdx, resp.Content)
	}
	if resp.Content != "Taking you back." || resp.Metadata["ended_by"] != "hand_off" {
		t.Fatalf("response = %q ended_by %v, want the batch's words and hand_off", resp.Content, resp.Metadata["ended_by"])
	}
}

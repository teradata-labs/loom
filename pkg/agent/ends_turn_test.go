package agent

import (
	"context"
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

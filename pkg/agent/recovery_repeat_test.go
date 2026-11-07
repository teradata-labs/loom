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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// repeatHintCfg is the opt-in config: the agent asked for the repeat-call note.
func repeatHintCfg() *Config {
	patternCfg := DefaultPatternConfig()
	patternCfg.UseLLMClassifier = false
	return &Config{
		MaxTurns:          25,
		MaxToolExecutions: 50,
		MaxIterations:     10,
		PatternConfig:     patternCfg,
		EnableSelfHealing: true,
		RecoveryConfig:    &RecoveryConfig{RepeatHintThresholds: repeatHintThresholds},
	}
}

func TestNoteToolCall_HintsOnlyAtThresholds(t *testing.T) {
	r := newRecoveryOrchestrator(&RecoveryConfig{RepeatHintThresholds: repeatHintThresholds}, nil)
	for i := 1; i <= 9; i++ {
		count, hint := r.noteToolCall("k")
		assert.Equal(t, i, count)
		assert.Equal(t, i == 3 || i == 5 || i == 8, hint, "call %d", i)
	}
}

func TestNoteToolCall_ResetsOnDifferentCall(t *testing.T) {
	r := newRecoveryOrchestrator(&RecoveryConfig{RepeatHintThresholds: repeatHintThresholds}, nil)
	for _, k := range []string{"a", "b", "a", "b", "a", "b"} {
		count, hint := r.noteToolCall(k)
		assert.Equal(t, 1, count)
		assert.False(t, hint)
	}
	r.noteToolCall("a")
	r.noteToolCall("a")
	count, hint := r.noteToolCall("a")
	assert.Equal(t, 3, count)
	assert.True(t, hint)
}

func TestNoteToolCall_NilReceiver(t *testing.T) {
	count, hint := (*recoveryOrchestrator)(nil).noteToolCall("k")
	assert.Equal(t, 0, count)
	assert.False(t, hint)
}

func TestNoteToolCall_EmptyThresholdsDisable(t *testing.T) {
	r := newRecoveryOrchestrator(&RecoveryConfig{}, nil)
	for i := 1; i <= 10; i++ {
		_, hint := r.noteToolCall("k")
		assert.False(t, hint, "call %d", i)
	}
}

// The hint is opt-in: the defaults and an agent with no opt-in never hint.
func TestRepeatHint_OffByDefault(t *testing.T) {
	assert.Empty(t, DefaultRecoveryConfig().RepeatHintThresholds)
	assert.Nil(t, repeatHintRecoveryConfig(nil))
	assert.Nil(t, repeatHintRecoveryConfig(map[string]string{"_repeat_call_hint": "false"}))

	r := newRecoveryOrchestrator(DefaultRecoveryConfig(), nil)
	for i := 1; i <= 10; i++ {
		_, hint := r.noteToolCall("k")
		assert.False(t, hint, "call %d", i)
	}
	assert.Equal(t, []int{3, 5, 8}, repeatHintRecoveryConfig(map[string]string{"_repeat_call_hint": "true"}).RepeatHintThresholds)
}

// The note is one-shot: taken once, then empty until the next threshold.
func TestTakeRepeatHint_OneShot(t *testing.T) {
	r := newRecoveryOrchestrator(&RecoveryConfig{RepeatHintThresholds: repeatHintThresholds}, nil)
	assert.Equal(t, "", r.takeRepeatHint())
	r.setRepeatHint("note")
	assert.Equal(t, "note", r.takeRepeatHint())
	assert.Equal(t, "", r.takeRepeatHint())

	var nilRec *recoveryOrchestrator
	nilRec.setRepeatHint("ignored")
	assert.Equal(t, "", nilRec.takeRepeatHint())
}

// The YAML key is the only way to opt in; metadata is stashed only when true.
func TestYAMLRepeatCallHint_StashesMetadata(t *testing.T) {
	yamlFor := func(extra string) string {
		return `
agent:
  name: hint_agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20241022
  behavior:
` + extra
	}
	cfg, err := LoadConfigFromString(yamlFor("    repeat_call_hint: true\n"))
	require.NoError(t, err)
	assert.Equal(t, "true", cfg.Metadata["_repeat_call_hint"])

	cfg, err = LoadConfigFromString(yamlFor("    repeat_call_hint: false\n"))
	require.NoError(t, err)
	assert.NotContains(t, cfg.Metadata, "_repeat_call_hint")

	cfg, err = LoadConfigFromString(yamlFor("    max_turns: 5\n"))
	require.NoError(t, err)
	assert.NotContains(t, cfg.Metadata, "_repeat_call_hint")
}

// recordingToolLLM records every message list it is sent.
type recordingToolLLM struct {
	*mockToolCallingLLM
	recMu sync.Mutex
	seen  [][]llmtypes.Message
}

func (r *recordingToolLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	r.recMu.Lock()
	r.seen = append(r.seen, append([]llmtypes.Message(nil), messages...))
	r.recMu.Unlock()
	return r.mockToolCallingLLM.Chat(ctx, messages, tools)
}

func threeCallsThenDone(tool string) *recordingToolLLM {
	call := func(id string) []llmtypes.ToolCall {
		return []llmtypes.ToolCall{{ID: id, Name: tool, Input: map[string]interface{}{"expression": "2+2"}}}
	}
	return &recordingToolLLM{mockToolCallingLLM: &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{toolCalls: call("call_1")},
			{toolCalls: call("call_2")},
			{toolCalls: call("call_3")},
			{content: "done"},
		},
	}}
}

// hintsIn returns the note-carrying messages of one provider request.
func hintsIn(msgs []llmtypes.Message) []llmtypes.Message {
	var out []llmtypes.Message
	for _, m := range msgs {
		if strings.Contains(m.Content, "in a row to") {
			out = append(out, m)
		}
	}
	return out
}

// The note reaches exactly one provider call, as a transient trailing message,
// and never a tool row. Both checks matter: a note in a row breaks JSON and
// tabular results, and a stored note is replayed in later turns.
func TestAgent_RepeatHintReachesNextCallOnly(t *testing.T) {
	llm := threeCallsThenDone("calculator")
	ag := NewAgent(&mockBackend{}, llm, WithConfig(repeatHintCfg()))
	var mu sync.Mutex
	var count int
	ag.RegisterTool(&mockCountingCalculatorTool{mu: &mu, count: &count})

	_, err := ag.Chat(context.Background(), "repeat_hint", "add 2+2 three times")
	require.NoError(t, err)

	llm.recMu.Lock()
	defer llm.recMu.Unlock()
	require.Len(t, llm.seen, 4)

	for i := 0; i < 3; i++ {
		assert.Empty(t, hintsIn(llm.seen[i]), "request %d must not carry a note yet", i+1)
	}
	last := hintsIn(llm.seen[3])
	require.Len(t, last, 1, "exactly one note on the call after the third identical call")
	assert.Equal(t, "user", last[0].Role)
	assert.Contains(t, last[0].Content, "<system-reminder>")
	assert.Contains(t, last[0].Content, "call 3 in a row to calculator")

	for _, req := range llm.seen {
		for _, m := range req {
			if m.ToolUseID != "" {
				assert.NotContains(t, m.Content, "in a row", "tool row %s must stay untouched", m.ToolUseID)
			}
		}
	}
}

// Without the opt-in no request ever carries the note.
func TestAgent_RepeatHintOffByDefault(t *testing.T) {
	llm := threeCallsThenDone("calculator")
	cfg := repeatHintCfg()
	cfg.RecoveryConfig = nil
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	var mu sync.Mutex
	var count int
	ag.RegisterTool(&mockCountingCalculatorTool{mu: &mu, count: &count})

	_, err := ag.Chat(context.Background(), "repeat_hint_off", "add 2+2 three times")
	require.NoError(t, err)

	llm.recMu.Lock()
	defer llm.recMu.Unlock()
	for i, req := range llm.seen {
		assert.Empty(t, hintsIn(req), "request %d", i+1)
	}
}

// bigResultTool returns a result well above the offload threshold.
type bigResultTool struct{ mockCountingCalculatorTool }

func (b *bigResultTool) Execute(context.Context, map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: strings.Repeat("x", 5000)}, nil
}

// A large repeated result is offloaded to a stub at render time. The stub must
// stay intact (no note inside it) and the note still reaches the model.
func TestAgent_RepeatHintLeavesOffloadedRowIntact(t *testing.T) {
	llm := threeCallsThenDone("calculator")
	ag := NewAgent(&mockBackend{}, llm, WithConfig(repeatHintCfg()))
	ag.SetSharedMemoryThreshold(1024)
	ag.RegisterTool(&bigResultTool{})

	_, err := ag.Chat(context.Background(), "repeat_hint_big", "add 2+2 three times")
	require.NoError(t, err)

	llm.recMu.Lock()
	defer llm.recMu.Unlock()
	byID := map[string]string{}
	for _, m := range llm.seen[len(llm.seen)-1] {
		if m.ToolUseID != "" {
			byID[m.ToolUseID] = m.Content
		}
	}
	require.Contains(t, byID, "call_3")
	assert.Less(t, len(byID["call_3"]), 1024, "the 5KB result must have been stubbed")
	assert.NotContains(t, byID["call_3"], "[repeat:")
	assert.Len(t, hintsIn(llm.seen[len(llm.seen)-1]), 1)
}

// failingTool errors on every call.
type failingTool struct{ mockCountingCalculatorTool }

func (f *failingTool) Execute(context.Context, map[string]interface{}) (*shuttle.Result, error) {
	return nil, errors.New("boom")
}

// A run of identical failing calls gets no note. The model already sees each
// failure; a note on top would add noise, and a row that failed has no result
// to comment on.
func TestAgent_RepeatHintSkipsFailedCalls(t *testing.T) {
	llm := threeCallsThenDone("calculator")
	ag := NewAgent(&mockBackend{}, llm, WithConfig(repeatHintCfg()))
	ag.RegisterTool(&failingTool{})

	_, err := ag.Chat(context.Background(), "repeat_hint_fail", "add 2+2 three times")
	require.NoError(t, err)

	llm.recMu.Lock()
	defer llm.recMu.Unlock()
	for i, req := range llm.seen {
		assert.Empty(t, hintsIn(req), "request %d", i+1)
	}
}

// The note is looked up under guidance.self_correction.<id>, the namespace the
// prompt file registers. The plain key stays first for direct registrations.
func TestGetGuidanceMessage_ResolvesSelfCorrectionNamespace(t *testing.T) {
	reg := &keyedPrompts{values: map[string]string{
		"guidance.self_correction.repeated_tool_call": "SELF-CORRECTION {{.count}}",
	}}
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithPrompts(reg))
	assert.Equal(t, "SELF-CORRECTION 3", ag.getGuidanceMessage(context.Background(), "repeated_tool_call", map[string]interface{}{"count": "3"}))

	reg.values["guidance.repeated_tool_call"] = "PLAIN"
	assert.Equal(t, "PLAIN", ag.getGuidanceMessage(context.Background(), "repeated_tool_call", nil))
}

// keyedPrompts answers only the keys it holds, with no interpolation.
type keyedPrompts struct{ values map[string]string }

func (k *keyedPrompts) Get(_ context.Context, key string, vars map[string]interface{}) (string, error) {
	v, ok := k.values[key]
	if !ok {
		return "", errors.New("prompt not found: " + key)
	}
	if c, ok := vars["count"].(string); ok {
		v = strings.ReplaceAll(v, "{{.count}}", c)
	}
	return v, nil
}

func (k *keyedPrompts) GetWithVariant(ctx context.Context, key, _ string, vars map[string]interface{}) (string, error) {
	return k.Get(ctx, key, vars)
}

func (k *keyedPrompts) GetMetadata(context.Context, string) (*prompts.PromptMetadata, error) {
	return nil, errors.New("unsupported")
}

func (k *keyedPrompts) List(context.Context, map[string]string) ([]string, error) { return nil, nil }

func (k *keyedPrompts) Reload(context.Context) error { return nil }

func (k *keyedPrompts) Watch(context.Context) (<-chan prompts.PromptUpdate, error) {
	return nil, errors.New("unsupported")
}

// The shipped YAML must resolve under the namespace the lookup uses.
func TestGetGuidanceMessage_ShippedYAMLResolves(t *testing.T) {
	reg := prompts.NewFileRegistry("../../prompts")
	require.NoError(t, reg.Reload(context.Background()))
	ctx := context.Background()

	// The file registers its entries under the namespaced key only.
	_, err := reg.Get(ctx, "guidance.self_correction.repeated_tool_call", map[string]interface{}{"count": "3", "tool_name": "t"})
	require.NoError(t, err)
	_, err = reg.Get(ctx, "guidance.repeated_tool_call", map[string]interface{}{"count": "3", "tool_name": "t"})
	require.Error(t, err)

	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithPrompts(reg))
	msg := ag.getGuidanceMessage(ctx, "repeated_tool_call",
		map[string]interface{}{"count": "3", "tool_name": "t"})
	assert.Contains(t, msg, "call 3 in a row to t")
	assert.Contains(t, msg, "data can change between calls")
}

func TestGetGuidanceMessage_RepeatedToolCallFallback(t *testing.T) {
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{})
	msg := ag.getGuidanceMessage(context.Background(), "repeated_tool_call",
		map[string]interface{}{"count": "3", "tool_name": "t"})
	assert.Contains(t, msg, "call 3 in a row to t")
	assert.Contains(t, msg, "Read this result", "the note sits on the newest result")
	assert.Contains(t, msg, "data can change between calls")
	assert.Contains(t, msg, "polling a job")

	msg = ag.getGuidanceMessage(context.Background(), "repeated_tool_call", nil)
	assert.Contains(t, msg, "call several in a row to this tool")
}

// The namespace fix also reaches the existing YAML entries. Their text must
// stay the same as the hard-coded fallbacks, and a missing required variable
// must fall back instead of rendering a placeholder.
func TestGetGuidanceMessage_ExistingEntriesKeepText(t *testing.T) {
	reg := prompts.NewFileRegistry("../../prompts")
	require.NoError(t, reg.Reload(context.Background()))
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithPrompts(reg))
	ctx := context.Background()

	assert.Equal(t,
		"I apologize, but I've reached my processing limit. Please try rephrasing your request or breaking it into smaller steps.",
		ag.getGuidanceMessage(ctx, "max_turns_reached", nil))
	assert.Equal(t,
		"I encountered an error while processing your request. Please try again or rephrase your question.",
		ag.getGuidanceMessage(ctx, "llm_call_failed", nil))
	assert.Equal(t,
		"The tool execution failed: boom. Let me try a different approach.",
		ag.getGuidanceMessage(ctx, "tool_execution_failed", map[string]interface{}{"error": "boom"}))
	assert.Equal(t,
		"The tool execution failed. Let me try a different approach.",
		ag.getGuidanceMessage(ctx, "tool_execution_failed", nil))
}

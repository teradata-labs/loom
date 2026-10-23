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
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

func TestNoteToolCall_HintsOnlyAtThresholds(t *testing.T) {
	r := newRecoveryOrchestrator(nil, nil)
	for i := 1; i <= 9; i++ {
		count, hint := r.noteToolCall("k")
		assert.Equal(t, i, count)
		assert.Equal(t, i == 3 || i == 5 || i == 8, hint, "call %d", i)
	}
}

func TestNoteToolCall_ResetsOnDifferentCall(t *testing.T) {
	r := newRecoveryOrchestrator(nil, nil)
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

func TestDefaultRecoveryConfig_RepeatHintThresholds(t *testing.T) {
	assert.Equal(t, []int{3, 5, 8}, DefaultRecoveryConfig().RepeatHintThresholds)
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

func TestAgent_RepeatHintAppendedAcrossResponses(t *testing.T) {
	call := func(id string) []llmtypes.ToolCall {
		return []llmtypes.ToolCall{{ID: id, Name: "calculator", Input: map[string]interface{}{"expression": "2+2"}}}
	}
	llm := &recordingToolLLM{mockToolCallingLLM: &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{toolCalls: call("call_1")},
			{toolCalls: call("call_2")},
			{toolCalls: call("call_3")},
			{content: "done"},
		},
	}}

	patternCfg := DefaultPatternConfig()
	patternCfg.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, WithConfig(&Config{
		MaxTurns:          25,
		MaxToolExecutions: 50,
		MaxIterations:     10,
		PatternConfig:     patternCfg,
		EnableSelfHealing: true,
	}))
	var mu sync.Mutex
	var count int
	ag.RegisterTool(&mockCountingCalculatorTool{mu: &mu, count: &count})

	_, err := ag.Chat(context.Background(), "repeat_hint", "add 2+2 three times")
	require.NoError(t, err)

	llm.recMu.Lock()
	defer llm.recMu.Unlock()
	require.NotEmpty(t, llm.seen)
	byID := map[string]string{}
	for _, m := range llm.seen[len(llm.seen)-1] {
		if m.ToolUseID != "" {
			byID[m.ToolUseID] = m.Content
		}
	}
	require.Contains(t, byID, "call_3")
	assert.Contains(t, byID["call_3"], "call 3 in a row to calculator")
	require.Contains(t, byID, "call_1")
	require.Contains(t, byID, "call_2")
	assert.NotContains(t, byID["call_1"], "in a row")
	assert.NotContains(t, byID["call_2"], "in a row")
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

// bigResultTool returns a result well above the offload threshold.
type bigResultTool struct{ mockCountingCalculatorTool }

func (b *bigResultTool) Execute(context.Context, map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: strings.Repeat("x", 5000)}, nil
}

// A large repeated result is offloaded to a stub at render time; the hint must
// still reach the model (the >16KB loop case).
func TestAgent_RepeatHintSurvivesOffloadStub(t *testing.T) {
	call := func(id string) []llmtypes.ToolCall {
		return []llmtypes.ToolCall{{ID: id, Name: "calculator", Input: map[string]interface{}{"expression": "2+2"}}}
	}
	llm := &recordingToolLLM{mockToolCallingLLM: &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{toolCalls: call("call_1")},
			{toolCalls: call("call_2")},
			{toolCalls: call("call_3")},
			{content: "done"},
		},
	}}
	patternCfg := DefaultPatternConfig()
	patternCfg.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, WithConfig(&Config{
		MaxTurns:          25,
		MaxToolExecutions: 50,
		MaxIterations:     10,
		PatternConfig:     patternCfg,
		EnableSelfHealing: true,
	}))
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
	assert.Contains(t, byID["call_3"], repeatHintPrefix+"(note: this is call 3 in a row")
	require.Contains(t, byID, "call_1")
	assert.NotContains(t, byID["call_1"], repeatHintPrefix)
}

func TestHarvestTails_KeepsRepeatLine(t *testing.T) {
	content := "data...\n[harvested: 3 rows]\n\n" + repeatHintPrefix + "call 3 in a row]"
	got := harvestTails(content)
	assert.Contains(t, got, "[harvested: 3 rows]")
	assert.Contains(t, got, repeatHintPrefix+"call 3 in a row]")
	assert.Empty(t, harvestTails("data...\nplain last line"))
}

func TestTruncateToolRowContent_KeepsRepeatLine(t *testing.T) {
	hint := repeatHintPrefix + strings.Repeat("h", 100) + "]"
	content := strings.Repeat("x", 5000) + "\n\n" + hint

	got := truncateToolRowContent(content, 1024)
	assert.LessOrEqual(t, len(got), 1024)
	assert.True(t, strings.HasSuffix(got, hint), "repeat line kept as the last line")
	assert.Contains(t, got, "[truncated: ")

	// The line cannot fit beside a minThreshold core: dropped, bound holds.
	big := repeatHintPrefix + strings.Repeat("h", 400) + "]"
	got = truncateToolRowContent(strings.Repeat("x", 5000)+"\n\n"+big, 600)
	assert.LessOrEqual(t, len(got), 600)
	assert.NotContains(t, got, repeatHintPrefix)

	// Dropping the line can leave content within the bound: returned whole.
	got = truncateToolRowContent(strings.Repeat("x", 300)+"\n\n"+big, 600)
	assert.LessOrEqual(t, len(got), 600)
}

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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// usageScriptLLM returns scripted responses, each with its own usage, so a
// test can assert how the loop aggregates them. Past the script it returns a
// terminal text response with zero usage.
type usageScriptLLM struct {
	mu        sync.Mutex
	responses []*LLMResponse
	idx       int
}

func (m *usageScriptLLM) Chat(_ context.Context, _ []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.responses) {
		return &LLMResponse{Content: "done", StopReason: "end_turn"}, nil
	}
	r := m.responses[m.idx]
	m.idx++
	cp := *r
	return &cp, nil
}

func (m *usageScriptLLM) Name() string  { return "usage-script" }
func (m *usageScriptLLM) Model() string { return "usage-v1" }

func newUsageTestAgent(llm LLMProvider) *Agent {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	ag.RegisterTool(&shuttle.MockTool{
		MockName:        "echo_tool",
		MockDescription: "Echoes its input",
		MockExecute: func(_ context.Context, params map[string]interface{}) (*shuttle.Result, error) {
			return &shuttle.Result{Success: true, Data: params}, nil
		},
	})
	return ag
}

// TestResponse_TurnUsageSumsEveryLLMCall pins the contract the two usage
// fields carry: Usage is the FINAL call's share (what the persisted assistant
// row records as its own TokenCount/CostUSD), TurnUsage is the sum of every
// call the turn made — what the provider actually billed. Before TurnUsage
// existed an embedder metering a turn from Usage saw 1/N of a tool-heavy one.
func TestResponse_TurnUsageSumsEveryLLMCall(t *testing.T) {
	llm := &usageScriptLLM{responses: []*LLMResponse{
		{
			ToolCalls:  []llmtypes.ToolCall{{ID: "c1", Name: "echo_tool", Input: map[string]interface{}{"n": 1}}},
			StopReason: "tool_use",
			Usage:      llmtypes.Usage{InputTokens: 1000, OutputTokens: 20, TotalTokens: 1020, CacheReadInputTokens: 300, CostUSD: 0.10},
		},
		{
			ToolCalls:  []llmtypes.ToolCall{{ID: "c2", Name: "echo_tool", Input: map[string]interface{}{"n": 2}}},
			StopReason: "tool_use",
			Usage:      llmtypes.Usage{InputTokens: 1100, OutputTokens: 25, TotalTokens: 1125, CacheReadInputTokens: 900, CacheCreationInputTokens: 50, CostUSD: 0.05},
		},
		{
			Content:    "final answer",
			StopReason: "end_turn",
			Usage:      llmtypes.Usage{InputTokens: 1200, OutputTokens: 30, TotalTokens: 1230, CacheReadInputTokens: 1000, CostUSD: 0.02},
		},
	}}
	ag := newUsageTestAgent(llm)

	resp, err := ag.Chat(context.Background(), "turn-usage-session", "run the tool twice")
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "final answer", resp.Content)
	assert.Len(t, resp.ToolExecutions, 2)

	// Usage: the final call only — unchanged contract.
	assert.Equal(t, llmtypes.Usage{InputTokens: 1200, OutputTokens: 30, TotalTokens: 1230, CacheReadInputTokens: 1000, CostUSD: 0.02}, resp.Usage)

	// TurnUsage: every call.
	want := llmtypes.Usage{
		InputTokens:              1000 + 1100 + 1200,
		OutputTokens:             20 + 25 + 30,
		TotalTokens:              1020 + 1125 + 1230,
		CacheReadInputTokens:     300 + 900 + 1000,
		CacheCreationInputTokens: 50,
	}
	got := resp.TurnUsage
	assert.InDelta(t, 0.17, got.CostUSD, 1e-9)
	got.CostUSD = 0
	assert.Equal(t, want, got)
}

// A single-call turn reports the same figures on both fields, so consumers
// switching from Usage to TurnUsage see no change for the simple case.
func TestResponse_TurnUsageEqualsUsageForSingleCall(t *testing.T) {
	llm := &usageScriptLLM{responses: []*LLMResponse{{
		Content:    "hi",
		StopReason: "end_turn",
		Usage:      llmtypes.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CostUSD: 0.001},
	}}}
	ag := newUsageTestAgent(llm)

	resp, err := ag.Chat(context.Background(), "single-call-session", "hello")
	require.NoError(t, err)
	assert.Equal(t, resp.Usage, resp.TurnUsage)
	assert.Equal(t, 15, resp.TurnUsage.TotalTokens)
}

// The empty-response retry is an extra billed call; TurnUsage carries it,
// Usage does not.
func TestResponse_TurnUsageIncludesEmptyResponseRetry(t *testing.T) {
	llm := &usageScriptLLM{responses: []*LLMResponse{
		{Content: "", StopReason: "end_turn", Usage: llmtypes.Usage{InputTokens: 100, OutputTokens: 0, TotalTokens: 100, CostUSD: 0.01}},
		{Content: "second try", StopReason: "end_turn", Usage: llmtypes.Usage{InputTokens: 120, OutputTokens: 5, TotalTokens: 125, CostUSD: 0.012}},
	}}
	ag := newUsageTestAgent(llm)

	resp, err := ag.Chat(context.Background(), "empty-retry-session", "hello")
	require.NoError(t, err)
	assert.Equal(t, "second try", resp.Content)
	assert.Equal(t, 125, resp.Usage.TotalTokens)
	assert.Equal(t, 225, resp.TurnUsage.TotalTokens)
	assert.InDelta(t, 0.022, resp.TurnUsage.CostUSD, 1e-9)
}

func TestAddUsage(t *testing.T) {
	var dst Usage
	addUsage(&dst, Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3, CacheReadInputTokens: 4, CacheCreationInputTokens: 5, CostUSD: 0.5})
	addUsage(&dst, Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30, CacheReadInputTokens: 40, CacheCreationInputTokens: 50, CostUSD: 0.25})
	assert.Equal(t, Usage{InputTokens: 11, OutputTokens: 22, TotalTokens: 33, CacheReadInputTokens: 44, CacheCreationInputTokens: 55, CostUSD: 0.75}, dst)
}

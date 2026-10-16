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
package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/builder"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

func oneSpan(t *testing.T, tracer *observability.MockTracer, name string) *observability.Span {
	t.Helper()
	spans := tracer.GetSpansByName(name)
	require.Len(t, spans, 1, "spans named %s", name)
	return spans[0]
}

// TestTurnSpans covers the span tree and identity of one tool-calling turn:
// LLM and tool spans nest under agent.conversation_loop (S2.1), spans a tool
// starts nest under agent.tool_execution, which carries the call (S2.2), and
// agent.chat carries the agent identity, OpenInference metadata and the trace
// ID the response reports (S2.4).
func TestTurnSpans(t *testing.T) {
	tracer := observability.NewMockTracer()
	llmProvider := &mockLLMProvider{
		name:  "test-llm",
		model: "test-model",
		responses: []agent.LLMResponse{
			{StopReason: "tool_use", ToolCalls: []llmtypes.ToolCall{
				{ID: "call_1", Name: "lookup", Input: map[string]interface{}{"q": "x"}},
			}},
			{Content: "done", StopReason: "end_turn"},
		},
	}
	tool := &shuttle.MockTool{
		MockName:        "lookup",
		MockDescription: "looks a value up",
		MockBackend:     "test",
		MockExecute: func(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
			_, inner := tracer.StartSpan(ctx, "test.tool.inner")
			tracer.EndSpan(inner)
			return &shuttle.Result{Success: true, Data: "found it", ExecutionTimeMs: 3}, nil
		},
	}

	cfg := agent.DefaultConfig()
	cfg.Name = "span-agent"
	cfg.PatternConfig = agent.DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := builder.NewInstrumentedAgent(&mockBackend{}, llmProvider, tracer, agent.WithConfig(cfg))
	ag.RegisterTool(tool)

	ctx := observability.ContextWithSpanMetadata(context.Background(), map[string]string{"condition": "memory_on"})
	resp, err := ag.Chat(ctx, "span-session", "look it up")
	require.NoError(t, err)

	chat := oneSpan(t, tracer, "agent.chat")
	loop := oneSpan(t, tracer, "agent.conversation_loop")
	toolSpan := oneSpan(t, tracer, "agent.tool_execution")
	inner := oneSpan(t, tracer, "test.tool.inner")

	t.Run("hierarchy", func(t *testing.T) {
		assert.Equal(t, chat.SpanID, loop.ParentID, "the loop span is a child of agent.chat")
		llmSpans := tracer.GetSpansByName(observability.SpanLLMCompletion)
		require.Len(t, llmSpans, 2)
		for _, s := range llmSpans {
			assert.Equal(t, loop.SpanID, s.ParentID, "llm.completion is a child of the loop span")
		}
		assert.Equal(t, loop.SpanID, toolSpan.ParentID, "the tool span is a child of the loop span")
		assert.Equal(t, toolSpan.SpanID, inner.ParentID, "a span the tool starts is a child of the tool span")
	})

	t.Run("tool span", func(t *testing.T) {
		a := toolSpan.Attributes
		assert.Equal(t, "lookup", a[observability.AttrToolName])
		assert.Equal(t, "lookup", a["tool_name"])
		assert.Equal(t, "execute_tool", a["gen_ai.operation.name"])
		assert.Equal(t, "call_1", a["gen_ai.tool.call.id"])
		assert.Equal(t, `{"q":"x"}`, a["input.value"])
		assert.Equal(t, "application/json", a["input.mime_type"])
		assert.Equal(t, "found it", a["output.value"])
		assert.Equal(t, true, a["success"])
		assert.IsType(t, int64(0), a["execution_time_ms"], "the executor's measured time, as a number")
		assert.False(t, toolSpan.EndTime.IsZero())
	})

	t.Run("agent identity and trace id", func(t *testing.T) {
		a := chat.Attributes
		assert.Equal(t, ag.GetID(), a["agent.id"])
		assert.Equal(t, "span-agent", a["agent.name"])
		assert.Equal(t, int64(1), a["session.turn"])
		var md map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(a["metadata"].(string)), &md))
		assert.Equal(t, map[string]interface{}{"graph_memory_enabled": false, "condition": "memory_on"}, md)
		assert.Equal(t, chat.TraceID, resp.Metadata["trace_id"])
	})
}

// TestResponse_LLMCallsCountsTurnUsageCalls checks that Metadata["llm_calls"]
// counts the calls TurnUsage sums, including the final synthesis call made
// when the loop runs out of turns.
func TestResponse_LLMCallsCountsTurnUsageCalls(t *testing.T) {
	llmProvider := &mockLLMProvider{
		name:  "test-llm",
		model: "test-model",
		responses: []agent.LLMResponse{
			{StopReason: "tool_use", ToolCalls: []llmtypes.ToolCall{{ID: "call_1", Name: "lookup", Input: map[string]interface{}{}}},
				Usage: llmtypes.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CostUSD: 0.001}},
			{Content: "summary", StopReason: "end_turn",
				Usage: llmtypes.Usage{InputTokens: 20, OutputTokens: 5, TotalTokens: 25, CostUSD: 0.002}},
		},
	}
	tool := &shuttle.MockTool{MockName: "lookup", MockBackend: "test",
		MockExecute: func(context.Context, map[string]interface{}) (*shuttle.Result, error) {
			return &shuttle.Result{Success: true, Data: "x"}, nil
		}}
	cfg := agent.DefaultConfig()
	cfg.MaxTurns = 1
	cfg.PatternConfig = agent.DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := builder.NewInstrumentedAgent(&mockBackend{}, llmProvider, observability.NewMockTracer(), agent.WithConfig(cfg))
	ag.RegisterTool(tool)

	resp, err := ag.Chat(context.Background(), "calls-session", "look it up")
	require.NoError(t, err)
	assert.Equal(t, true, resp.Metadata["synthesized"])
	assert.Equal(t, 2, resp.Metadata["llm_calls"], "the loop's call plus the synthesis call")
	assert.Equal(t, 40, resp.TurnUsage.TotalTokens)
}

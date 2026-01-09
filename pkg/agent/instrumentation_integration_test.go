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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/builder"
	"github.com/teradata-labs/loom/pkg/fabric"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// TestInstrumentedAgent_EndToEndTracing verifies that the full instrumentation stack
// captures traces and metrics for conversations, LLM calls, and tool executions.
func TestInstrumentedAgent_EndToEndTracing(t *testing.T) {
	// Create mock tracer to capture all traces
	tracer := newTestTracer()

	// Create mock backend
	backend := &mockBackend{}

	// Create mock LLM provider
	llmProvider := &mockLLMProvider{
		name:  "test-llm",
		model: "test-model-v1",
		responses: []agent.LLMResponse{
			// First response: LLM requests tool use
			{
				Content:    "",
				StopReason: "tool_use",
				ToolCalls: []llmtypes.ToolCall{
					{
						ID:   "call_1",
						Name: "get_data",
						Input: map[string]interface{}{
							"query": "SELECT * FROM users",
						},
					},
				},
				Usage: llmtypes.Usage{
					InputTokens:  100,
					OutputTokens: 50,
					TotalTokens:  150,
					CostUSD:      0.001,
				},
			},
			// Second response: LLM provides final answer
			{
				Content:    "Here are the results from the database query.",
				StopReason: "end_turn",
				ToolCalls:  []llmtypes.ToolCall{},
				Usage: llmtypes.Usage{
					InputTokens:  200,
					OutputTokens: 75,
					TotalTokens:  275,
					CostUSD:      0.002,
				},
			},
		},
	}

	// Create instrumented agent using our helper
	ag := builder.NewInstrumentedAgent(backend, llmProvider, tracer)

	// Register a mock tool
	mockTool := &shuttle.MockTool{
		MockName:        "get_data",
		MockDescription: "Retrieves data from backend",
		MockBackend:     "test",
		MockExecute: func(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
			return &shuttle.Result{
				Success: true,
				Data:    []map[string]interface{}{{"id": 1, "name": "Alice"}},
				Metadata: map[string]interface{}{
					"rows": 1,
				},
				ExecutionTimeMs: 42,
			}, nil
		},
	}
	ag.RegisterTool(mockTool)

	// Execute conversation
	ctx := context.Background()
	response, err := ag.Chat(ctx, "test-session", "Show me the users")

	// Verify conversation succeeded
	require.NoError(t, err)
	require.NotNil(t, response)
	assert.Equal(t, "Here are the results from the database query.", response.Content)
	assert.Equal(t, 1, len(response.ToolExecutions))

	// Verify traces captured
	tracer.mu.Lock()
	spans := tracer.spans
	metrics := tracer.metrics
	tracer.mu.Unlock()

	// Should have spans for:
	// 1. agent.conversation (top-level)
	// 2. llm.completion (first LLM call)
	// 3. llm.completion (second LLM call)
	// Note: tool execution spans would be captured if executor is instrumented
	assert.GreaterOrEqual(t, len(spans), 3, "Expected at least 3 spans (conversation + 2 LLM calls)")

	// Verify conversation span
	var conversationSpan *observability.Span
	for _, span := range spans {
		if span.Name == observability.SpanAgentConversation {
			conversationSpan = span
			break
		}
	}
	require.NotNil(t, conversationSpan, "Expected conversation span")

	// Verify conversation span attributes
	assert.Equal(t, "test-session", conversationSpan.Attributes[observability.AttrSessionID])
	assert.Equal(t, "test-llm", conversationSpan.Attributes["llm.provider"])
	assert.Equal(t, "test-model-v1", conversationSpan.Attributes["llm.model"])
	assert.Equal(t, observability.StatusOK, conversationSpan.Status.Code)

	// Verify conversation metrics captured
	assert.Equal(t, 2, conversationSpan.Attributes["conversation.turns"])
	assert.Equal(t, 1, conversationSpan.Attributes["conversation.tool_executions"])

	// Verify LLM spans
	llmSpanCount := 0
	for _, span := range spans {
		if span.Name == observability.SpanLLMCompletion {
			llmSpanCount++
			// Verify LLM span has required attributes
			assert.NotNil(t, span.Attributes[observability.AttrLLMProvider])
			assert.NotNil(t, span.Attributes[observability.AttrLLMModel])
		}
	}
	assert.Equal(t, 2, llmSpanCount, "Expected 2 LLM completion spans")

	// Verify metrics emitted
	assert.Greater(t, len(metrics), 0, "Expected metrics to be emitted")

	// Check for key metrics
	metricNames := make(map[string]int)
	for _, m := range metrics {
		metricNames[m.name]++
	}

	// Should have conversation metrics
	assert.Greater(t, metricNames[observability.MetricAgentConversations], 0)
	assert.Greater(t, metricNames[observability.MetricAgentConversationDuration], 0)

	// Should have LLM metrics
	assert.Greater(t, metricNames[observability.MetricLLMCalls], 0)
	assert.Greater(t, metricNames[observability.MetricLLMLatency], 0)
	assert.Greater(t, metricNames[observability.MetricLLMTokensInput], 0)
	assert.Greater(t, metricNames[observability.MetricLLMTokensOutput], 0)
	assert.Greater(t, metricNames[observability.MetricLLMCost], 0)
}

// TestInstrumentedAgent_ErrorTracing verifies that errors are properly traced.
func TestInstrumentedAgent_ErrorTracing(t *testing.T) {
	tracer := newTestTracer()
	backend := &mockBackend{}

	// LLM provider that returns error
	llmProvider := &mockLLMProvider{
		name:  "test-llm",
		model: "test-model",
		err:   assert.AnError,
	}

	ag := builder.NewInstrumentedAgent(backend, llmProvider, tracer)

	// Execute conversation
	ctx := context.Background()
	response, err := ag.Chat(ctx, "test-session", "This will fail")

	// Verify error
	require.Error(t, err)
	assert.Nil(t, response)

	// Verify error span captured
	tracer.mu.Lock()
	spans := tracer.spans
	metrics := tracer.metrics
	tracer.mu.Unlock()

	// Should have conversation span with error status
	var conversationSpan *observability.Span
	for _, span := range spans {
		if span.Name == observability.SpanAgentConversation {
			conversationSpan = span
			break
		}
	}
	require.NotNil(t, conversationSpan)
	assert.Equal(t, observability.StatusError, conversationSpan.Status.Code)

	// Verify error metric emitted
	foundErrorMetric := false
	for _, m := range metrics {
		if m.name == "agent.conversations.failed" {
			foundErrorMetric = true
			assert.Equal(t, float64(1), m.value)
		}
	}
	assert.True(t, foundErrorMetric, "Expected error metric")
}

// TestInstrumentedAgent_CostTracking verifies accurate cost aggregation.
func TestInstrumentedAgent_CostTracking(t *testing.T) {
	tracer := newTestTracer()
	backend := &mockBackend{}

	llmProvider := &mockLLMProvider{
		name:  "test-llm",
		model: "test-model",
		responses: []agent.LLMResponse{
			{
				Content:    "Response 1",
				StopReason: "end_turn",
				Usage: llmtypes.Usage{
					InputTokens:  1000,
					OutputTokens: 500,
					TotalTokens:  1500,
					CostUSD:      0.050, // $0.05
				},
			},
		},
	}

	ag := builder.NewInstrumentedAgent(backend, llmProvider, tracer)

	ctx := context.Background()
	response, err := ag.Chat(ctx, "test-session", "Test message")

	require.NoError(t, err)
	require.NotNil(t, response)

	// Verify cost captured in response
	assert.Equal(t, 0.050, response.Usage.CostUSD)

	// Verify cost captured in span
	tracer.mu.Lock()
	spans := tracer.spans
	tracer.mu.Unlock()

	var conversationSpan *observability.Span
	for _, span := range spans {
		if span.Name == observability.SpanAgentConversation {
			conversationSpan = span
			break
		}
	}
	require.NotNil(t, conversationSpan)
	assert.Equal(t, 0.050, conversationSpan.Attributes["conversation.cost.usd"])
	assert.Equal(t, 1500, conversationSpan.Attributes["conversation.tokens.total"])
}

// testTracer is a test implementation that captures all traces and metrics
type testTracer struct {
	mu      sync.Mutex
	spans   []*observability.Span
	metrics []testMetric
}

type testMetric struct {
	name   string
	value  float64
	labels map[string]string
}

func newTestTracer() *testTracer {
	return &testTracer{
		spans:   make([]*observability.Span, 0),
		metrics: make([]testMetric, 0),
	}
}

func (t *testTracer) StartSpan(ctx context.Context, name string, opts ...observability.SpanOption) (context.Context, *observability.Span) {
	span := &observability.Span{
		Name:       name,
		StartTime:  time.Now(),
		Attributes: make(map[string]interface{}),
		Events:     make([]observability.Event, 0),
	}
	for _, opt := range opts {
		opt(span)
	}

	t.mu.Lock()
	t.spans = append(t.spans, span)
	t.mu.Unlock()

	return ctx, span
}

func (t *testTracer) EndSpan(span *observability.Span) {
	span.EndTime = time.Now()
}

func (t *testTracer) RecordMetric(name string, value float64, labels map[string]string) {
	t.mu.Lock()
	t.metrics = append(t.metrics, testMetric{
		name:   name,
		value:  value,
		labels: labels,
	})
	t.mu.Unlock()
}

func (t *testTracer) RecordEvent(ctx context.Context, name string, attributes map[string]interface{}) {
	// No-op
}

func (t *testTracer) Flush(ctx context.Context) error {
	return nil
}

// mockBackend implements fabric.ExecutionBackend for testing
type mockBackend struct{}

func (m *mockBackend) Name() string {
	return "mock"
}

func (m *mockBackend) ExecuteQuery(ctx context.Context, query string) (*fabric.QueryResult, error) {
	return &fabric.QueryResult{
		Type:     "rows",
		Rows:     []map[string]interface{}{{"result": "ok"}},
		RowCount: 1,
	}, nil
}

func (m *mockBackend) GetSchema(ctx context.Context, resource string) (*fabric.Schema, error) {
	return &fabric.Schema{
		Name: resource,
		Type: "table",
		Fields: []fabric.Field{
			{Name: "id", Type: "int", Nullable: false},
		},
	}, nil
}

func (m *mockBackend) ListResources(ctx context.Context, filters map[string]string) ([]fabric.Resource, error) {
	return []fabric.Resource{
		{Name: "test_table", Type: "table"},
	}, nil
}

func (m *mockBackend) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	return map[string]interface{}{
		"row_count": 100,
	}, nil
}

func (m *mockBackend) Ping(ctx context.Context) error {
	return nil
}

func (m *mockBackend) Capabilities() *fabric.Capabilities {
	return &fabric.Capabilities{
		SupportsTransactions: false,
		SupportsConcurrency:  true,
		SupportsStreaming:    false,
		MaxConcurrentOps:     10,
		SupportedOperations:  []string{"query", "schema"},
	}
}

func (m *mockBackend) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	return nil, nil
}

func (m *mockBackend) Close() error {
	return nil
}

// mockLLMProvider for testing
type mockLLMProvider struct {
	mu            sync.Mutex
	name          string
	model         string
	responses     []agent.LLMResponse
	responseIndex int
	err           error
}

func (m *mockLLMProvider) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.err != nil {
		return nil, m.err
	}

	if m.responseIndex >= len(m.responses) {
		// Return a default response if we run out
		return &llmtypes.LLMResponse{
			Content:    "Default response",
			StopReason: "end_turn",
			Usage:      llmtypes.Usage{},
		}, nil
	}

	resp := m.responses[m.responseIndex]
	m.responseIndex++
	return &resp, nil
}

func (m *mockLLMProvider) Name() string {
	return m.name
}

func (m *mockLLMProvider) Model() string {
	return m.model
}

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
package llm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// mockLLMProvider is a mock implementation for testing
type mockLLMProvider struct {
	mu           sync.Mutex
	name         string
	model        string
	response     *llmtypes.LLMResponse
	err          error
	callCount    int
	lastMessages []llmtypes.Message
	lastTools    []shuttle.Tool
}

func (m *mockLLMProvider) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	m.callCount++
	m.lastMessages = messages
	m.lastTools = tools
	m.mu.Unlock()

	if m.err != nil {
		return nil, m.err
	}

	return m.response, nil
}

func (m *mockLLMProvider) Name() string {
	return m.name
}

func (m *mockLLMProvider) Model() string {
	return m.model
}

// mockTracer is a testing tracer that captures spans
type mockTracer struct {
	mu      sync.Mutex
	spans   []*observability.Span
	metrics []mockMetric
}

type mockMetric struct {
	name   string
	value  float64
	labels map[string]string
}

func newMockTracer() *mockTracer {
	return &mockTracer{
		spans:   make([]*observability.Span, 0),
		metrics: make([]mockMetric, 0),
	}
}

func (m *mockTracer) StartSpan(ctx context.Context, name string, opts ...observability.SpanOption) (context.Context, *observability.Span) {
	span := &observability.Span{
		Name:       name,
		StartTime:  time.Now(),
		Attributes: make(map[string]interface{}),
		Events:     make([]observability.Event, 0),
	}
	// Apply options
	for _, opt := range opts {
		opt(span)
	}

	m.mu.Lock()
	m.spans = append(m.spans, span)
	m.mu.Unlock()

	return ctx, span
}

func (m *mockTracer) EndSpan(span *observability.Span) {
	span.EndTime = time.Now()
}

func (m *mockTracer) RecordMetric(name string, value float64, labels map[string]string) {
	m.mu.Lock()
	m.metrics = append(m.metrics, mockMetric{
		name:   name,
		value:  value,
		labels: labels,
	})
	m.mu.Unlock()
}

func (m *mockTracer) RecordEvent(ctx context.Context, name string, attributes map[string]interface{}) {
	// No-op for testing
}

func (m *mockTracer) Flush(ctx context.Context) error {
	return nil
}

func TestInstrumentedProvider_Success(t *testing.T) {
	// Create mock provider with successful response
	mockProvider := &mockLLMProvider{
		name:  "test-provider",
		model: "test-model",
		response: &llmtypes.LLMResponse{
			Content:    "Hello, world!",
			StopReason: "end_turn",
			Usage: llmtypes.Usage{
				InputTokens:  10,
				OutputTokens: 20,
				TotalTokens:  30,
				CostUSD:      0.001,
			},
		},
	}

	tracer := newMockTracer()
	instrumented := NewInstrumentedProvider(mockProvider, tracer)

	ctx := context.Background()

	messages := []llmtypes.Message{
		{Role: "user", Content: "Hello"},
	}

	tools := []shuttle.Tool{}

	// Execute
	resp, err := instrumented.Chat(ctx, messages, tools)

	// Verify no error
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify response
	assert.Equal(t, "Hello, world!", resp.Content)
	assert.Equal(t, 10, resp.Usage.InputTokens)
	assert.Equal(t, 20, resp.Usage.OutputTokens)

	// Verify mock was called
	assert.Equal(t, 1, mockProvider.callCount)
	assert.Equal(t, messages, mockProvider.lastMessages)

	// Verify span was created
	require.Len(t, tracer.spans, 1)
	span := tracer.spans[0]
	assert.Equal(t, observability.SpanLLMCompletion, span.Name)
	assert.Equal(t, observability.StatusOK, span.Status.Code)

	// Verify span attributes
	assert.Equal(t, "test-provider", span.Attributes[observability.AttrLLMProvider])
	assert.Equal(t, "test-model", span.Attributes[observability.AttrLLMModel])
	assert.Equal(t, 1, span.Attributes["llm.messages.count"])
	assert.Equal(t, 0, span.Attributes["llm.tools.count"])
	assert.Equal(t, 10, span.Attributes["llm.tokens.input"])
	assert.Equal(t, 20, span.Attributes["llm.tokens.output"])
	assert.Equal(t, 30, span.Attributes["llm.tokens.total"])
	assert.Equal(t, 0.001, span.Attributes["llm.cost.usd"])
	assert.Equal(t, "end_turn", span.Attributes["llm.stop_reason"])

	// Verify events
	require.Len(t, span.Events, 2)
	assert.Equal(t, "llm.call.started", span.Events[0].Name)
	assert.Equal(t, "llm.call.completed", span.Events[1].Name)

	// Verify metrics
	assert.True(t, len(tracer.metrics) >= 5, "Expected at least 5 metrics")

	// Check for key metrics
	metricNames := make(map[string]bool)
	for _, m := range tracer.metrics {
		metricNames[m.name] = true
	}
	assert.True(t, metricNames[observability.MetricLLMCalls])
	assert.True(t, metricNames[observability.MetricLLMLatency])
	assert.True(t, metricNames[observability.MetricLLMTokensInput])
	assert.True(t, metricNames[observability.MetricLLMTokensOutput])
	assert.True(t, metricNames[observability.MetricLLMCost])
}

func TestInstrumentedProvider_WithToolCalls(t *testing.T) {
	// Create mock provider with tool calls
	mockProvider := &mockLLMProvider{
		name:  "test-provider",
		model: "test-model",
		response: &llmtypes.LLMResponse{
			Content:    "",
			StopReason: "tool_use",
			ToolCalls: []llmtypes.ToolCall{
				{ID: "call_1", Name: "get_weather", Input: map[string]interface{}{"city": "NYC"}},
				{ID: "call_2", Name: "get_time", Input: map[string]interface{}{}},
			},
			Usage: llmtypes.Usage{
				InputTokens:  15,
				OutputTokens: 25,
				TotalTokens:  40,
				CostUSD:      0.002,
			},
		},
	}

	tracer := newMockTracer()
	instrumented := NewInstrumentedProvider(mockProvider, tracer)

	ctx := context.Background()

	messages := []llmtypes.Message{
		{Role: "user", Content: "What's the weather?"},
	}

	// Create mock tools
	mockTool1 := &shuttle.MockTool{MockName: "get_weather"}
	mockTool2 := &shuttle.MockTool{MockName: "get_time"}
	tools := []shuttle.Tool{mockTool1, mockTool2}

	// Execute
	resp, err := instrumented.Chat(ctx, messages, tools)

	// Verify
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Len(t, resp.ToolCalls, 2)

	// Verify span
	require.Len(t, tracer.spans, 1)
	span := tracer.spans[0]

	// Verify tool-related attributes
	assert.Equal(t, 2, span.Attributes["llm.tools.count"])
	toolNames := span.Attributes["llm.tools.names"].([]string)
	assert.Contains(t, toolNames, "get_weather")
	assert.Contains(t, toolNames, "get_time")

	assert.Equal(t, 2, span.Attributes["llm.tool_calls.count"])
	toolCallNames := span.Attributes["llm.tool_calls.names"].([]string)
	assert.Contains(t, toolCallNames, "get_weather")
	assert.Contains(t, toolCallNames, "get_time")
}

func TestInstrumentedProvider_Error(t *testing.T) {
	// Create mock provider that returns error
	testErr := errors.New("API rate limit exceeded")
	mockProvider := &mockLLMProvider{
		name:  "test-provider",
		model: "test-model",
		err:   testErr,
	}

	tracer := newMockTracer()
	instrumented := NewInstrumentedProvider(mockProvider, tracer)

	ctx := context.Background()

	messages := []llmtypes.Message{
		{Role: "user", Content: "Hello"},
	}

	// Execute
	resp, err := instrumented.Chat(ctx, messages, []shuttle.Tool{})

	// Verify error
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, testErr, err)

	// Verify span
	require.Len(t, tracer.spans, 1)
	span := tracer.spans[0]
	assert.Equal(t, observability.StatusError, span.Status.Code)
	assert.Equal(t, testErr.Error(), span.Status.Message)

	// Verify error attributes
	assert.Equal(t, "*errors.errorString", span.Attributes[observability.AttrErrorType])
	assert.Equal(t, testErr.Error(), span.Attributes[observability.AttrErrorMessage])

	// Verify error event
	var foundErrorEvent bool
	for _, event := range span.Events {
		if event.Name == "llm.call.failed" {
			foundErrorEvent = true
			break
		}
	}
	assert.True(t, foundErrorEvent, "Expected error event")

	// Verify error metric
	var foundErrorMetric bool
	for _, m := range tracer.metrics {
		if m.name == observability.MetricLLMErrors {
			foundErrorMetric = true
			assert.Equal(t, float64(1), m.value)
			break
		}
	}
	assert.True(t, foundErrorMetric, "Expected error metric")
}

func TestInstrumentedProvider_Name(t *testing.T) {
	mockProvider := &mockLLMProvider{
		name:  "anthropic",
		model: "claude-3-5-sonnet",
	}

	tracer := newMockTracer()
	instrumented := NewInstrumentedProvider(mockProvider, tracer)

	assert.Equal(t, "anthropic", instrumented.Name())
}

func TestInstrumentedProvider_Model(t *testing.T) {
	mockProvider := &mockLLMProvider{
		name:  "anthropic",
		model: "claude-3-5-sonnet",
	}

	tracer := newMockTracer()
	instrumented := NewInstrumentedProvider(mockProvider, tracer)

	assert.Equal(t, "claude-3-5-sonnet", instrumented.Model())
}

func TestInstrumentedProvider_MultipleMessages(t *testing.T) {
	mockProvider := &mockLLMProvider{
		name:  "test-provider",
		model: "test-model",
		response: &llmtypes.LLMResponse{
			Content:    "Multi-turn response",
			StopReason: "end_turn",
			Usage: llmtypes.Usage{
				InputTokens:  100,
				OutputTokens: 50,
				TotalTokens:  150,
				CostUSD:      0.01,
			},
		},
	}

	tracer := newMockTracer()
	instrumented := NewInstrumentedProvider(mockProvider, tracer)

	ctx := context.Background()

	// Multiple messages (conversation history)
	messages := []llmtypes.Message{
		{Role: "user", Content: "What is 2+2?"},
		{Role: "assistant", Content: "4"},
		{Role: "user", Content: "What about 3+3?"},
	}

	// Execute
	resp, err := instrumented.Chat(ctx, messages, []shuttle.Tool{})

	// Verify
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify span captures message count
	require.Len(t, tracer.spans, 1)
	span := tracer.spans[0]
	assert.Equal(t, 3, span.Attributes["llm.messages.count"])
}

func TestInstrumentedProvider_ConcurrentCalls(t *testing.T) {
	mockProvider := &mockLLMProvider{
		name:  "test-provider",
		model: "test-model",
		response: &llmtypes.LLMResponse{
			Content:    "Response",
			StopReason: "end_turn",
			Usage: llmtypes.Usage{
				InputTokens:  10,
				OutputTokens: 10,
				TotalTokens:  20,
				CostUSD:      0.001,
			},
		},
	}

	tracer := newMockTracer()
	instrumented := NewInstrumentedProvider(mockProvider, tracer)

	// Run 10 concurrent calls
	concurrency := 10
	done := make(chan bool, concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			ctx := context.Background()

			messages := []llmtypes.Message{
				{Role: "user", Content: "Hello"},
			}

			_, err := instrumented.Chat(ctx, messages, []shuttle.Tool{})
			assert.NoError(t, err)

			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < concurrency; i++ {
		<-done
	}

	// Verify all calls were made
	assert.Equal(t, concurrency, mockProvider.callCount)

	// Verify spans (should have one per call)
	assert.Equal(t, concurrency, len(tracer.spans))
}

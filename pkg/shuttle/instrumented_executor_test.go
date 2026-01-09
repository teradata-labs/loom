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
package shuttle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
)

// mockTracer for testing
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
	// No-op
}

func (m *mockTracer) Flush(ctx context.Context) error {
	return nil
}

func TestInstrumentedExecutor_ExecuteSuccess(t *testing.T) {
	// Setup
	registry := NewRegistry()
	executor := NewExecutor(registry)
	tracer := newMockTracer()
	instrumented := NewInstrumentedExecutor(executor, tracer)

	// Create and register a mock tool
	mockTool := &MockTool{
		MockName:        "test_tool",
		MockDescription: "Test tool",
		MockBackend:     "test_backend",
		MockExecute: func(ctx context.Context, params map[string]interface{}) (*Result, error) {
			return &Result{
				Success: true,
				Data:    "test result",
				Metadata: map[string]interface{}{
					"rows": 10,
				},
			}, nil
		},
	}
	registry.Register(mockTool)

	// Execute
	ctx := context.Background()
	params := map[string]interface{}{
		"input": "test",
	}

	result, err := instrumented.Execute(ctx, "test_tool", params)

	// Verify
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.Equal(t, "test result", result.Data)

	// Verify span
	require.Len(t, tracer.spans, 1)
	span := tracer.spans[0]
	assert.Equal(t, observability.SpanToolExecute, span.Name)
	assert.Equal(t, observability.StatusOK, span.Status.Code)

	// Verify span attributes
	assert.Equal(t, "test_tool", span.Attributes[observability.AttrToolName])
	assert.Equal(t, "test_backend", span.Attributes["tool.backend"])
	assert.Equal(t, "Test tool", span.Attributes["tool.description"])
	assert.False(t, span.Attributes["tool.cache_hit"].(bool))

	// Verify events
	require.Len(t, span.Events, 2)
	assert.Equal(t, "tool.execution.started", span.Events[0].Name)
	assert.Equal(t, "tool.execution.completed", span.Events[1].Name)

	// Verify metrics
	assert.True(t, len(tracer.metrics) >= 2)

	// Check for execution and duration metrics
	var foundExecution, foundDuration bool
	for _, m := range tracer.metrics {
		if m.name == observability.MetricToolExecutions {
			foundExecution = true
			assert.Equal(t, float64(1), m.value)
			assert.Equal(t, "test_tool", m.labels[observability.AttrToolName])
			assert.Equal(t, "success", m.labels["status"])
		}
		if m.name == observability.MetricToolDuration {
			foundDuration = true
			assert.GreaterOrEqual(t, m.value, float64(0))
		}
	}
	assert.True(t, foundExecution, "Expected execution metric")
	assert.True(t, foundDuration, "Expected duration metric")
}

func TestInstrumentedExecutor_ExecuteToolError(t *testing.T) {
	// Setup
	registry := NewRegistry()
	executor := NewExecutor(registry)
	tracer := newMockTracer()
	instrumented := NewInstrumentedExecutor(executor, tracer)

	// Create tool that returns error
	mockTool := &MockTool{
		MockName:    "failing_tool",
		MockBackend: "test",
		MockExecute: func(ctx context.Context, params map[string]interface{}) (*Result, error) {
			return &Result{
				Success: false,
				Error: &Error{
					Code:       "validation_failed",
					Message:    "Invalid input",
					Retryable:  true,
					Suggestion: "Check your parameters",
				},
			}, nil
		},
	}
	registry.Register(mockTool)

	// Execute
	ctx := context.Background()
	result, err := instrumented.Execute(ctx, "failing_tool", map[string]interface{}{})

	// Verify no executor error
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Success)

	// Verify span
	require.Len(t, tracer.spans, 1)
	span := tracer.spans[0]
	assert.Equal(t, observability.StatusError, span.Status.Code)
	assert.Equal(t, "Invalid input", span.Status.Message)

	// Verify error attributes
	assert.Equal(t, "validation_failed", span.Attributes["tool.error.code"])
	assert.Equal(t, "Invalid input", span.Attributes["tool.error.message"])
	assert.Equal(t, true, span.Attributes["tool.error.retryable"])
	assert.Equal(t, "Check your parameters", span.Attributes["tool.error.suggestion"])

	// Verify error event
	var foundErrorEvent bool
	for _, event := range span.Events {
		if event.Name == "tool.execution.error" {
			foundErrorEvent = true
			assert.Equal(t, "validation_failed", event.Attributes["error_code"])
			assert.Equal(t, true, event.Attributes["retryable"])
		}
	}
	assert.True(t, foundErrorEvent)

	// Verify error metric
	var foundErrorMetric bool
	for _, m := range tracer.metrics {
		if m.name == observability.MetricToolErrors {
			foundErrorMetric = true
			assert.Equal(t, float64(1), m.value)
			assert.Equal(t, "tool_error", m.labels["error_type"])
			assert.Equal(t, "validation_failed", m.labels["error_code"])
		}
	}
	assert.True(t, foundErrorMetric)
}

func TestInstrumentedExecutor_ExecutorError(t *testing.T) {
	// Setup - don't register any tool
	registry := NewRegistry()
	executor := NewExecutor(registry)
	tracer := newMockTracer()
	instrumented := NewInstrumentedExecutor(executor, tracer)

	// Execute with non-existent tool
	ctx := context.Background()
	result, err := instrumented.Execute(ctx, "nonexistent_tool", map[string]interface{}{})

	// Verify executor error
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "tool not found")

	// Verify span
	require.Len(t, tracer.spans, 1)
	span := tracer.spans[0]
	assert.Equal(t, observability.StatusError, span.Status.Code)
	assert.Contains(t, span.Status.Message, "tool not found")

	// Verify error attributes
	assert.Contains(t, span.Attributes[observability.AttrErrorMessage], "tool not found")

	// Verify error event
	var foundErrorEvent bool
	for _, event := range span.Events {
		if event.Name == "tool.execution.failed" {
			foundErrorEvent = true
		}
	}
	assert.True(t, foundErrorEvent)

	// Verify error metric
	var foundErrorMetric bool
	for _, m := range tracer.metrics {
		if m.name == observability.MetricToolErrors {
			foundErrorMetric = true
			assert.Equal(t, "executor_error", m.labels["error_type"])
		}
	}
	assert.True(t, foundErrorMetric)
}

func TestInstrumentedExecutor_ExecuteWithTool(t *testing.T) {
	// Setup
	registry := NewRegistry()
	executor := NewExecutor(registry)
	tracer := newMockTracer()
	instrumented := NewInstrumentedExecutor(executor, tracer)

	// Create tool (don't register it)
	mockTool := &MockTool{
		MockName:        "direct_tool",
		MockDescription: "Directly executed tool",
		MockBackend:     "direct",
		MockExecute: func(ctx context.Context, params map[string]interface{}) (*Result, error) {
			return &Result{
				Success: true,
				Data:    params["value"],
			}, nil
		},
	}

	// Execute directly
	ctx := context.Background()
	params := map[string]interface{}{
		"value": "test_value",
	}

	result, err := instrumented.ExecuteWithTool(ctx, mockTool, params)

	// Verify
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.Equal(t, "test_value", result.Data)

	// Verify tool was called
	assert.Equal(t, 1, mockTool.ExecuteCount)

	// Verify span
	require.Len(t, tracer.spans, 1)
	span := tracer.spans[0]
	assert.Equal(t, observability.SpanToolExecute, span.Name)
	assert.Equal(t, "direct_tool", span.Attributes[observability.AttrToolName])
	assert.Equal(t, "direct", span.Attributes["tool.backend"])
	assert.Equal(t, observability.StatusOK, span.Status.Code)
}

func TestInstrumentedExecutor_CachedResult(t *testing.T) {
	// Setup
	registry := NewRegistry()
	executor := NewExecutor(registry)
	tracer := newMockTracer()
	instrumented := NewInstrumentedExecutor(executor, tracer)

	// Create tool that returns cached result
	mockTool := &MockTool{
		MockName:    "cached_tool",
		MockBackend: "test",
		MockExecute: func(ctx context.Context, params map[string]interface{}) (*Result, error) {
			return &Result{
				Success:  true,
				Data:     "cached data",
				CacheHit: true,
			}, nil
		},
	}
	registry.Register(mockTool)

	// Execute
	ctx := context.Background()
	result, err := instrumented.Execute(ctx, "cached_tool", map[string]interface{}{})

	// Verify
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.CacheHit)

	// Verify span attributes
	span := tracer.spans[0]
	assert.True(t, span.Attributes["tool.cache_hit"].(bool))

	// Verify event includes cache_hit
	var foundCompleteEvent bool
	for _, event := range span.Events {
		if event.Name == "tool.execution.completed" {
			foundCompleteEvent = true
			assert.True(t, event.Attributes["cache_hit"].(bool))
		}
	}
	assert.True(t, foundCompleteEvent)

	// Verify duration metric includes cache_hit label
	var foundDurationMetric bool
	for _, m := range tracer.metrics {
		if m.name == observability.MetricToolDuration {
			foundDurationMetric = true
			assert.Equal(t, "true", m.labels["cache_hit"])
		}
	}
	assert.True(t, foundDurationMetric)
}

func TestInstrumentedExecutor_ConcurrentExecutions(t *testing.T) {
	// Setup
	registry := NewRegistry()
	executor := NewExecutor(registry)
	tracer := newMockTracer()
	instrumented := NewInstrumentedExecutor(executor, tracer)

	// Create tool
	mockTool := &MockTool{
		MockName:    "concurrent_tool",
		MockBackend: "test",
		MockExecute: func(ctx context.Context, params map[string]interface{}) (*Result, error) {
			// Simulate some work
			time.Sleep(1 * time.Millisecond)
			return &Result{
				Success: true,
				Data:    params["id"],
			}, nil
		},
	}
	registry.Register(mockTool)

	// Execute concurrently
	concurrency := 10
	done := make(chan bool, concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			ctx := context.Background()
			params := map[string]interface{}{
				"id": id,
			}

			result, err := instrumented.Execute(ctx, "concurrent_tool", params)
			assert.NoError(t, err)
			assert.True(t, result.Success)

			done <- true
		}(i)
	}

	// Wait for all
	for i := 0; i < concurrency; i++ {
		<-done
	}

	// Verify spans (one per execution)
	assert.Equal(t, concurrency, len(tracer.spans))

	// Verify tool was called correct number of times
	assert.Equal(t, concurrency, mockTool.ExecuteCount)
}

func TestInstrumentedExecutor_LargeParameters(t *testing.T) {
	// Setup
	registry := NewRegistry()
	executor := NewExecutor(registry)
	tracer := newMockTracer()
	instrumented := NewInstrumentedExecutor(executor, tracer)

	// Create tool
	mockTool := &MockTool{
		MockName: "large_param_tool",
		MockExecute: func(ctx context.Context, params map[string]interface{}) (*Result, error) {
			return &Result{Success: true}, nil
		},
	}
	registry.Register(mockTool)

	// Execute with large parameters (should truncate)
	ctx := context.Background()
	largeString := string(make([]byte, 2000)) // Larger than 1000 byte limit
	params := map[string]interface{}{
		"large_data": largeString,
	}

	result, err := instrumented.Execute(ctx, "large_param_tool", params)

	// Verify success
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify span doesn't have full params (too large)
	span := tracer.spans[0]
	// Should have args count instead of full args
	_, hasArgs := span.Attributes[observability.AttrToolArgs]
	argsCount, hasCount := span.Attributes["tool.args.count"]
	// Either has args (if JSON is under limit) or has count
	assert.True(t, hasArgs || hasCount)
	if hasCount {
		assert.Equal(t, 1, argsCount)
	}
}

func TestInstrumentedExecutor_Metadata(t *testing.T) {
	// Setup
	registry := NewRegistry()
	executor := NewExecutor(registry)
	tracer := newMockTracer()
	instrumented := NewInstrumentedExecutor(executor, tracer)

	// Create tool with metadata
	mockTool := &MockTool{
		MockName: "metadata_tool",
		MockExecute: func(ctx context.Context, params map[string]interface{}) (*Result, error) {
			return &Result{
				Success: true,
				Data:    "result",
				Metadata: map[string]interface{}{
					"rows_affected": 42,
					"query_time_ms": 123,
					"cache_key":     "abc123",
				},
			}, nil
		},
	}
	registry.Register(mockTool)

	// Execute
	ctx := context.Background()
	result, err := instrumented.Execute(ctx, "metadata_tool", map[string]interface{}{})

	// Verify
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify span captures metadata
	span := tracer.spans[0]
	assert.Equal(t, 3, span.Attributes["tool.metadata.count"])
	assert.Equal(t, 42, span.Attributes["tool.metadata.rows_affected"])
	assert.Equal(t, 123, span.Attributes["tool.metadata.query_time_ms"])
	assert.Equal(t, "abc123", span.Attributes["tool.metadata.cache_key"])
}

func TestInstrumentedExecutor_ExecuteWithToolError(t *testing.T) {
	// Setup
	registry := NewRegistry()
	executor := NewExecutor(registry)
	tracer := newMockTracer()
	instrumented := NewInstrumentedExecutor(executor, tracer)

	// Create tool that returns error via ExecuteWithTool
	mockTool := &MockTool{
		MockName: "direct_error_tool",
		MockExecute: func(ctx context.Context, params map[string]interface{}) (*Result, error) {
			return nil, errors.New("direct execution failed")
		},
	}

	// Execute directly
	ctx := context.Background()
	result, err := instrumented.ExecuteWithTool(ctx, mockTool, map[string]interface{}{})

	// Verify executor handles the error
	require.NoError(t, err) // Executor wraps errors in Result
	require.NotNil(t, result)
	assert.False(t, result.Success)
	assert.Equal(t, "execution_failed", result.Error.Code)

	// Verify span shows error
	span := tracer.spans[0]
	assert.Equal(t, observability.StatusError, span.Status.Code)
}

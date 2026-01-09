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
	"encoding/json"
	"fmt"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
)

// InstrumentedExecutor wraps an Executor with observability instrumentation.
// It captures detailed traces and metrics for every tool execution, including:
// - Tool name and parameters
// - Execution duration and success/failure
// - Result data and errors
// - Backend information
//
// This wrapper is transparent and can wrap any Executor.
type InstrumentedExecutor struct {
	// executor is the underlying tool executor
	executor *Executor

	// tracer is used for creating spans
	tracer observability.Tracer
}

// NewInstrumentedExecutor creates a new instrumented tool executor.
func NewInstrumentedExecutor(executor *Executor, tracer observability.Tracer) *InstrumentedExecutor {
	return &InstrumentedExecutor{
		executor: executor,
		tracer:   tracer,
	}
}

// Execute executes a tool by name with observability instrumentation.
func (e *InstrumentedExecutor) Execute(ctx context.Context, toolName string, params map[string]interface{}) (*Result, error) {
	// Start span
	_, span := e.tracer.StartSpan(ctx, observability.SpanToolExecute)
	defer e.tracer.EndSpan(span)

	// Start timing
	start := time.Now()

	// Set span attributes
	span.SetAttribute(observability.AttrToolName, toolName)

	// Capture parameters (sanitized to avoid PII in traces)
	if len(params) > 0 {
		// Convert params to JSON for logging (limit size)
		if paramsJSON, err := json.Marshal(params); err == nil && len(paramsJSON) < 1000 {
			span.SetAttribute(observability.AttrToolArgs, string(paramsJSON))
		} else {
			span.SetAttribute("tool.args.count", len(params))
		}
	}

	// Get tool info for additional context
	if tool, ok := e.executor.registry.Get(toolName); ok {
		span.SetAttribute("tool.backend", tool.Backend())
		span.SetAttribute("tool.description", tool.Description())
	}

	// Record event: Tool execution started
	span.AddEvent("tool.execution.started", map[string]interface{}{
		"tool": toolName,
	})

	// Execute the tool
	result, err := e.executor.Execute(ctx, toolName, params)

	// Calculate duration
	duration := time.Since(start)

	// Handle error case (executor-level error, not tool error)
	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorType, fmt.Sprintf("%T", err))
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		// Record error event
		span.AddEvent("tool.execution.failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": duration.Milliseconds(),
		})

		// Emit error metric
		e.tracer.RecordMetric(observability.MetricToolErrors, 1, map[string]string{
			observability.AttrToolName: toolName,
			"error_type":               "executor_error",
		})

		return nil, err
	}

	// Check tool execution result
	if result.Success {
		span.Status = observability.Status{
			Code:    observability.StatusOK,
			Message: "",
		}

		// Record success event
		span.AddEvent("tool.execution.completed", map[string]interface{}{
			"duration_ms": duration.Milliseconds(),
			"cache_hit":   result.CacheHit,
		})

		// Emit success metric
		e.tracer.RecordMetric(observability.MetricToolExecutions, 1, map[string]string{
			observability.AttrToolName: toolName,
			"status":                   "success",
		})
	} else {
		// Tool returned failure
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: result.Error.Message,
		}

		span.SetAttribute("tool.error.code", result.Error.Code)
		span.SetAttribute("tool.error.message", result.Error.Message)
		span.SetAttribute("tool.error.retryable", result.Error.Retryable)

		if result.Error.Suggestion != "" {
			span.SetAttribute("tool.error.suggestion", result.Error.Suggestion)
		}

		// Record failure event
		span.AddEvent("tool.execution.error", map[string]interface{}{
			"error_code":  result.Error.Code,
			"error_msg":   result.Error.Message,
			"retryable":   result.Error.Retryable,
			"duration_ms": duration.Milliseconds(),
		})

		// Emit error metric
		e.tracer.RecordMetric(observability.MetricToolErrors, 1, map[string]string{
			observability.AttrToolName: toolName,
			"error_type":               "tool_error",
			"error_code":               result.Error.Code,
			"retryable":                fmt.Sprintf("%t", result.Error.Retryable),
		})
	}

	// Capture result metadata
	span.SetAttribute("tool.execution_time_ms", result.ExecutionTimeMs)
	span.SetAttribute("tool.cache_hit", result.CacheHit)

	if len(result.Metadata) > 0 {
		span.SetAttribute("tool.metadata.count", len(result.Metadata))
		// Selectively add important metadata
		for key, value := range result.Metadata {
			// Only add simple metadata (avoid nested structures)
			switch v := value.(type) {
			case string, int, int64, float64, bool:
				span.SetAttribute(fmt.Sprintf("tool.metadata.%s", key), v)
			}
		}
	}

	// Emit duration metric
	e.tracer.RecordMetric(observability.MetricToolDuration, float64(result.ExecutionTimeMs), map[string]string{
		observability.AttrToolName: toolName,
		"cache_hit":                fmt.Sprintf("%t", result.CacheHit),
	})

	return result, nil
}

// ExecuteWithTool executes a specific tool instance with observability.
func (e *InstrumentedExecutor) ExecuteWithTool(ctx context.Context, tool Tool, params map[string]interface{}) (*Result, error) {
	// Start span
	_, span := e.tracer.StartSpan(ctx, observability.SpanToolExecute)
	defer e.tracer.EndSpan(span)

	// Start timing
	start := time.Now()

	// Set span attributes
	span.SetAttribute(observability.AttrToolName, tool.Name())
	span.SetAttribute("tool.backend", tool.Backend())
	span.SetAttribute("tool.description", tool.Description())

	// Capture parameters (sanitized)
	if len(params) > 0 {
		if paramsJSON, err := json.Marshal(params); err == nil && len(paramsJSON) < 1000 {
			span.SetAttribute(observability.AttrToolArgs, string(paramsJSON))
		} else {
			span.SetAttribute("tool.args.count", len(params))
		}
	}

	// Record event: Tool execution started
	span.AddEvent("tool.execution.started", map[string]interface{}{
		"tool": tool.Name(),
	})

	// Execute the tool
	result, err := e.executor.ExecuteWithTool(ctx, tool, params)

	// Calculate duration
	duration := time.Since(start)

	// Handle error case
	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorType, fmt.Sprintf("%T", err))
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		span.AddEvent("tool.execution.failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": duration.Milliseconds(),
		})

		e.tracer.RecordMetric(observability.MetricToolErrors, 1, map[string]string{
			observability.AttrToolName: tool.Name(),
			"error_type":               "executor_error",
		})

		return nil, err
	}

	// Check result
	if result.Success {
		span.Status = observability.Status{
			Code:    observability.StatusOK,
			Message: "",
		}

		span.AddEvent("tool.execution.completed", map[string]interface{}{
			"duration_ms": duration.Milliseconds(),
			"cache_hit":   result.CacheHit,
		})

		e.tracer.RecordMetric(observability.MetricToolExecutions, 1, map[string]string{
			observability.AttrToolName: tool.Name(),
			"status":                   "success",
		})
	} else {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: result.Error.Message,
		}

		span.SetAttribute("tool.error.code", result.Error.Code)
		span.SetAttribute("tool.error.message", result.Error.Message)
		span.SetAttribute("tool.error.retryable", result.Error.Retryable)

		span.AddEvent("tool.execution.error", map[string]interface{}{
			"error_code":  result.Error.Code,
			"error_msg":   result.Error.Message,
			"retryable":   result.Error.Retryable,
			"duration_ms": duration.Milliseconds(),
		})

		e.tracer.RecordMetric(observability.MetricToolErrors, 1, map[string]string{
			observability.AttrToolName: tool.Name(),
			"error_type":               "tool_error",
			"error_code":               result.Error.Code,
		})
	}

	// Capture metadata
	span.SetAttribute("tool.execution_time_ms", result.ExecutionTimeMs)
	span.SetAttribute("tool.cache_hit", result.CacheHit)

	e.tracer.RecordMetric(observability.MetricToolDuration, float64(result.ExecutionTimeMs), map[string]string{
		observability.AttrToolName: tool.Name(),
		"cache_hit":                fmt.Sprintf("%t", result.CacheHit),
	})

	return result, nil
}

// ListAvailableTools delegates to the underlying executor.
func (e *InstrumentedExecutor) ListAvailableTools() []Tool {
	return e.executor.ListAvailableTools()
}

// ListToolsByBackend delegates to the underlying executor.
func (e *InstrumentedExecutor) ListToolsByBackend(backend string) []Tool {
	return e.executor.ListToolsByBackend(backend)
}

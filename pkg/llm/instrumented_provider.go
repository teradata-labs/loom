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
	"fmt"
	"time"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// InstrumentedProvider wraps any LLMProvider with observability instrumentation.
// It captures detailed traces and metrics for every LLM call, including:
// - Request/response details (messages, tool calls)
// - Token usage and cost tracking
// - Latency measurements
// - Error tracking
//
// This wrapper is transparent and can wrap any LLMProvider implementation.
type InstrumentedProvider struct {
	// provider is the underlying LLM provider
	provider llmtypes.LLMProvider

	// tracer is used for creating spans
	tracer observability.Tracer
}

// NewInstrumentedProvider creates a new instrumented LLM provider.
func NewInstrumentedProvider(provider llmtypes.LLMProvider, tracer observability.Tracer) *InstrumentedProvider {
	return &InstrumentedProvider{
		provider: provider,
		tracer:   tracer,
	}
}

// Name returns the underlying provider name.
func (p *InstrumentedProvider) Name() string {
	return p.provider.Name()
}

// Model returns the underlying model identifier.
func (p *InstrumentedProvider) Model() string {
	return p.provider.Model()
}

// Chat sends a conversation to the LLM and captures detailed observability data.
func (p *InstrumentedProvider) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	// Start span
	_, span := p.tracer.StartSpan(ctx, observability.SpanLLMCompletion)
	defer p.tracer.EndSpan(span)

	// Start timing
	start := time.Now()

	// Set span attributes - basic info
	span.SetAttribute(observability.AttrLLMProvider, p.provider.Name())
	span.SetAttribute(observability.AttrLLMModel, p.provider.Model())

	// Capture request details
	span.SetAttribute("llm.messages.count", len(messages))
	span.SetAttribute("llm.tools.count", len(tools))

	// Capture tool names for traceability
	if len(tools) > 0 {
		toolNames := make([]string, len(tools))
		for i, tool := range tools {
			toolNames[i] = tool.Name()
		}
		span.SetAttribute("llm.tools.names", toolNames)
	}

	// Record event: LLM call started
	span.AddEvent("llm.call.started", map[string]interface{}{
		"provider": p.provider.Name(),
		"model":    p.provider.Model(),
		"messages": len(messages),
		"tools":    len(tools),
	})

	// Call the underlying provider (use original ctx, not spanCtx)
	resp, err := p.provider.Chat(ctx, messages, tools)

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

		// Record error event
		span.AddEvent("llm.call.failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": duration.Milliseconds(),
		})

		// Emit error metric
		p.tracer.RecordMetric(observability.MetricLLMErrors, 1, map[string]string{
			observability.AttrLLMProvider: p.provider.Name(),
			observability.AttrLLMModel:    p.provider.Model(),
			observability.AttrErrorType:   fmt.Sprintf("%T", err),
		})

		return nil, err
	}

	// Success - capture response details
	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: "",
	}

	// Capture usage metrics
	span.SetAttribute("llm.tokens.input", resp.Usage.InputTokens)
	span.SetAttribute("llm.tokens.output", resp.Usage.OutputTokens)
	span.SetAttribute("llm.tokens.total", resp.Usage.TotalTokens)
	span.SetAttribute("llm.cost.usd", resp.Usage.CostUSD)
	span.SetAttribute("llm.stop_reason", resp.StopReason)
	span.SetAttribute("llm.duration_ms", duration.Milliseconds())

	// Capture tool calls if present
	if len(resp.ToolCalls) > 0 {
		span.SetAttribute("llm.tool_calls.count", len(resp.ToolCalls))
		toolCallNames := make([]string, len(resp.ToolCalls))
		for i, tc := range resp.ToolCalls {
			toolCallNames[i] = tc.Name
		}
		span.SetAttribute("llm.tool_calls.names", toolCallNames)
	}

	// Capture content length (for analysis)
	span.SetAttribute("llm.content.length", len(resp.Content))

	// Record success event
	span.AddEvent("llm.call.completed", map[string]interface{}{
		"duration_ms":   duration.Milliseconds(),
		"input_tokens":  resp.Usage.InputTokens,
		"output_tokens": resp.Usage.OutputTokens,
		"cost_usd":      resp.Usage.CostUSD,
		"stop_reason":   resp.StopReason,
		"tool_calls":    len(resp.ToolCalls),
	})

	// Emit metrics
	p.tracer.RecordMetric(observability.MetricLLMCalls, 1, map[string]string{
		observability.AttrLLMProvider: p.provider.Name(),
		observability.AttrLLMModel:    p.provider.Model(),
	})

	p.tracer.RecordMetric(observability.MetricLLMLatency, float64(duration.Milliseconds()), map[string]string{
		observability.AttrLLMProvider: p.provider.Name(),
		observability.AttrLLMModel:    p.provider.Model(),
	})

	p.tracer.RecordMetric(observability.MetricLLMTokensInput, float64(resp.Usage.InputTokens), map[string]string{
		observability.AttrLLMProvider: p.provider.Name(),
		observability.AttrLLMModel:    p.provider.Model(),
	})

	p.tracer.RecordMetric(observability.MetricLLMTokensOutput, float64(resp.Usage.OutputTokens), map[string]string{
		observability.AttrLLMProvider: p.provider.Name(),
		observability.AttrLLMModel:    p.provider.Model(),
	})

	p.tracer.RecordMetric(observability.MetricLLMCost, resp.Usage.CostUSD, map[string]string{
		observability.AttrLLMProvider: p.provider.Name(),
		observability.AttrLLMModel:    p.provider.Model(),
	})

	return resp, nil
}

// ChatStream streams tokens as they're generated from the LLM with full observability.
// Returns error if the underlying provider doesn't support streaming.
func (p *InstrumentedProvider) ChatStream(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool, tokenCallback llmtypes.TokenCallback) (*llmtypes.LLMResponse, error) {
	// Check if underlying provider supports streaming
	streamingProvider, ok := p.provider.(llmtypes.StreamingLLMProvider)
	if !ok {
		return nil, fmt.Errorf("provider %s does not support streaming", p.provider.Name())
	}

	// Start span
	_, span := p.tracer.StartSpan(ctx, observability.SpanLLMCompletion)
	defer p.tracer.EndSpan(span)

	// Start timing
	start := time.Now()
	var firstTokenTime time.Time
	var ttft time.Duration
	tokenCount := 0
	firstTokenReceived := false

	// Set span attributes - basic info
	span.SetAttribute(observability.AttrLLMProvider, p.provider.Name())
	span.SetAttribute(observability.AttrLLMModel, p.provider.Model())
	span.SetAttribute("llm.streaming", true)

	// Capture request details
	span.SetAttribute("llm.messages.count", len(messages))
	span.SetAttribute("llm.tools.count", len(tools))

	// Capture tool names for traceability
	if len(tools) > 0 {
		toolNames := make([]string, len(tools))
		for i, tool := range tools {
			toolNames[i] = tool.Name()
		}
		span.SetAttribute("llm.tools.names", toolNames)
	}

	// Record event: streaming started
	span.AddEvent("stream.started", map[string]interface{}{
		"provider": p.provider.Name(),
		"model":    p.provider.Model(),
		"messages": len(messages),
		"tools":    len(tools),
	})

	// Wrap tokenCallback to capture TTFT and token count
	instrumentedCallback := func(token string) {
		// Track first token timing
		if !firstTokenReceived {
			firstTokenTime = time.Now()
			ttft = firstTokenTime.Sub(start)
			firstTokenReceived = true

			// Record first token event
			span.AddEvent("stream.first_token", map[string]interface{}{
				"ttft_ms": ttft.Milliseconds(),
			})

			// Emit TTFT metric
			p.tracer.RecordMetric("llm.streaming.ttft_ms", float64(ttft.Milliseconds()), map[string]string{
				observability.AttrLLMProvider: p.provider.Name(),
				observability.AttrLLMModel:    p.provider.Model(),
			})
		}

		tokenCount++

		// Call the original callback
		if tokenCallback != nil {
			tokenCallback(token)
		}
	}

	// Call the underlying streaming provider
	resp, err := streamingProvider.ChatStream(ctx, messages, tools, instrumentedCallback)

	// Calculate total duration
	duration := time.Since(start)

	// Handle error case
	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorType, fmt.Sprintf("%T", err))
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		// Record error event
		span.AddEvent("stream.failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": duration.Milliseconds(),
			"tokens":      tokenCount,
		})

		// Emit error metric
		p.tracer.RecordMetric(observability.MetricLLMErrors, 1, map[string]string{
			observability.AttrLLMProvider: p.provider.Name(),
			observability.AttrLLMModel:    p.provider.Model(),
		})

		return nil, err
	}

	// Success - capture response details
	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: "",
	}

	// Capture usage metrics
	span.SetAttribute("llm.tokens.input", resp.Usage.InputTokens)
	span.SetAttribute("llm.tokens.output", resp.Usage.OutputTokens)
	span.SetAttribute("llm.tokens.total", resp.Usage.TotalTokens)
	span.SetAttribute("llm.cost.usd", resp.Usage.CostUSD)
	span.SetAttribute("llm.stop_reason", resp.StopReason)
	span.SetAttribute("llm.duration_ms", duration.Milliseconds())
	span.SetAttribute("llm.ttft_ms", ttft.Milliseconds())
	span.SetAttribute("llm.streaming.chunks", tokenCount)

	// Calculate throughput (tokens per second)
	if duration.Seconds() > 0 {
		throughput := float64(resp.Usage.OutputTokens) / duration.Seconds()
		span.SetAttribute("llm.streaming.throughput", throughput)

		// Emit throughput metric
		p.tracer.RecordMetric("llm.streaming.throughput", throughput, map[string]string{
			observability.AttrLLMProvider: p.provider.Name(),
			observability.AttrLLMModel:    p.provider.Model(),
		})
	}

	// Capture tool calls if present
	if len(resp.ToolCalls) > 0 {
		span.SetAttribute("llm.tool_calls.count", len(resp.ToolCalls))
		toolCallNames := make([]string, len(resp.ToolCalls))
		for i, tc := range resp.ToolCalls {
			toolCallNames[i] = tc.Name
		}
		span.SetAttribute("llm.tool_calls.names", toolCallNames)
	}

	// Capture content length (for analysis)
	span.SetAttribute("llm.content.length", len(resp.Content))

	// Record success event
	span.AddEvent("stream.completed", map[string]interface{}{
		"duration_ms":   duration.Milliseconds(),
		"ttft_ms":       ttft.Milliseconds(),
		"input_tokens":  resp.Usage.InputTokens,
		"output_tokens": resp.Usage.OutputTokens,
		"cost_usd":      resp.Usage.CostUSD,
		"stop_reason":   resp.StopReason,
		"tool_calls":    len(resp.ToolCalls),
		"chunks":        tokenCount,
	})

	// Emit standard LLM metrics (same as non-streaming)
	p.tracer.RecordMetric(observability.MetricLLMCalls, 1, map[string]string{
		observability.AttrLLMProvider: p.provider.Name(),
		observability.AttrLLMModel:    p.provider.Model(),
	})

	p.tracer.RecordMetric(observability.MetricLLMLatency, float64(duration.Milliseconds()), map[string]string{
		observability.AttrLLMProvider: p.provider.Name(),
		observability.AttrLLMModel:    p.provider.Model(),
	})

	p.tracer.RecordMetric(observability.MetricLLMTokensInput, float64(resp.Usage.InputTokens), map[string]string{
		observability.AttrLLMProvider: p.provider.Name(),
		observability.AttrLLMModel:    p.provider.Model(),
	})

	p.tracer.RecordMetric(observability.MetricLLMTokensOutput, float64(resp.Usage.OutputTokens), map[string]string{
		observability.AttrLLMProvider: p.provider.Name(),
		observability.AttrLLMModel:    p.provider.Model(),
	})

	p.tracer.RecordMetric(observability.MetricLLMCost, resp.Usage.CostUSD, map[string]string{
		observability.AttrLLMProvider: p.provider.Name(),
		observability.AttrLLMModel:    p.provider.Model(),
	})

	// Emit streaming-specific metric
	p.tracer.RecordMetric("llm.streaming.chunks.total", float64(tokenCount), map[string]string{
		observability.AttrLLMProvider: p.provider.Name(),
		observability.AttrLLMModel:    p.provider.Model(),
	})

	return resp, nil
}

// Ensure InstrumentedProvider implements LLMProvider interface
var _ llmtypes.LLMProvider = (*InstrumentedProvider)(nil)

// Ensure InstrumentedProvider implements StreamingLLMProvider interface
var _ llmtypes.StreamingLLMProvider = (*InstrumentedProvider)(nil)

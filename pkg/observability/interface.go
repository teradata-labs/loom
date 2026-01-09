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
package observability

import "context"

// Tracer is the main interface for instrumenting Loom operations.
//
// Implementations export traces to observability backends (Hawk, OTLP, etc.)
// or provide no-op tracing for testing.
//
// Thread-safe: All methods can be called concurrently.
type Tracer interface {
	// StartSpan creates a new span and returns a context containing it.
	// The span is automatically linked to its parent via context propagation.
	//
	// Example:
	//   ctx, span := tracer.StartSpan(ctx, "llm.completion",
	//       WithAttribute("llm.model", "claude-3-5-sonnet"))
	//   defer tracer.EndSpan(span)
	StartSpan(ctx context.Context, name string, opts ...SpanOption) (context.Context, *Span)

	// EndSpan completes a span, calculates duration, and exports it.
	// Always call this via defer after StartSpan.
	EndSpan(span *Span)

	// RecordMetric records a point-in-time metric value with labels.
	// Use for counters, gauges, histograms (e.g., token counts, costs, latencies).
	//
	// Example:
	//   tracer.RecordMetric("llm.tokens.input", 1234, map[string]string{
	//       "provider": "anthropic",
	//       "model": "claude-3-5-sonnet",
	//   })
	RecordMetric(name string, value float64, labels map[string]string)

	// RecordEvent records a standalone event not tied to a span.
	// Use sparingly - most events should be span.AddEvent().
	RecordEvent(ctx context.Context, name string, attributes map[string]interface{})

	// Flush forces immediate export of buffered traces and metrics.
	// Blocks until export completes or times out.
	// Automatically called on graceful shutdown.
	Flush(ctx context.Context) error
}

// SpanFromContext retrieves the current span from context, if any.
// Returns nil if no span exists in context.
func SpanFromContext(ctx context.Context) *Span {
	if span, ok := ctx.Value(spanContextKey).(*Span); ok {
		return span
	}
	return nil
}

// ContextWithSpan returns a new context with the span attached.
func ContextWithSpan(ctx context.Context, span *Span) context.Context {
	return context.WithValue(ctx, spanContextKey, span)
}

type contextKey string

const spanContextKey contextKey = "loom.span"

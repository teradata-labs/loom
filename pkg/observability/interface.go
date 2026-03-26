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

// SpanExporter exports completed spans to an external store (e.g., PostgreSQL).
// Implementations must be goroutine-safe.
//
// The tracer calls ExportSpans synchronously on each EndSpan with a single-span
// batch. Implementations that buffer spans internally should flush on
// ForceFlush and drain remaining spans on Shutdown.
type SpanExporter interface {
	// ExportSpans sends a batch of completed spans to the external store.
	// Called by the tracer on EndSpan (or on flush for batched exporters).
	ExportSpans(ctx context.Context, spans []*Span) error

	// ForceFlush requests the exporter to flush any buffered spans immediately.
	// Returns when the flush completes or the context is cancelled.
	ForceFlush(ctx context.Context) error

	// Shutdown gracefully shuts down the exporter, flushing any buffered spans.
	Shutdown(ctx context.Context) error
}

// TraceIDFromContext retrieves the current trace ID from context, if any.
// Returns empty string if no span (and thus no trace) exists in context.
func TraceIDFromContext(ctx context.Context) string {
	if span := SpanFromContext(ctx); span != nil {
		return span.TraceID
	}
	return ""
}

type contextKey string

const (
	spanContextKey    contextKey = "loom.span"
	traceIDContextKey contextKey = "loom.trace_id"
)

// ContextWithTraceID returns a new context with an explicit trace ID.
// When StartSpan creates a root span (no parent span in context), it will
// use this trace ID instead of generating a new one. If a parent span exists
// in the context, the parent's trace ID takes priority and this value is ignored.
func ContextWithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDContextKey, traceID)
}

// traceIDFromContextOverride retrieves an explicitly-set trace ID from context.
func traceIDFromContextOverride(ctx context.Context) string {
	if id, ok := ctx.Value(traceIDContextKey).(string); ok {
		return id
	}
	return ""
}

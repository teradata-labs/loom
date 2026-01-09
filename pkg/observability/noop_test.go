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

import (
	"context"
	"testing"
	"time"
)

func TestNoOpTracer(t *testing.T) {
	tracer := NewNoOpTracer()

	t.Run("StartSpan creates minimal span", func(t *testing.T) {
		ctx := context.Background()
		ctx, span := tracer.StartSpan(ctx, "test_span",
			WithAttribute("key", "value"),
			WithSpanKind("test"),
		)

		if span == nil {
			t.Fatal("Expected span to be created")
		}
		if span.Name != "test_span" {
			t.Errorf("Expected name 'test_span', got %q", span.Name)
		}
		if span.TraceID == "" {
			t.Error("Expected TraceID to be set")
		}
		if span.SpanID == "" {
			t.Error("Expected SpanID to be set")
		}
		if span.Attributes["key"] != "value" {
			t.Errorf("Expected attribute key=value, got %v", span.Attributes["key"])
		}
		if span.Attributes["span.kind"] != "test" {
			t.Errorf("Expected span.kind=test, got %v", span.Attributes["span.kind"])
		}

		// Verify span is in context
		retrieved := SpanFromContext(ctx)
		if retrieved != span {
			t.Error("Span not properly stored in context")
		}
	})

	t.Run("Nested spans have correct parent relationship", func(t *testing.T) {
		ctx := context.Background()

		// Create parent span
		ctx, parent := tracer.StartSpan(ctx, "parent")

		// Create child span
		_, child := tracer.StartSpan(ctx, "child")

		if child.TraceID != parent.TraceID {
			t.Errorf("Child TraceID %s doesn't match parent %s", child.TraceID, parent.TraceID)
		}
		if child.ParentID != parent.SpanID {
			t.Errorf("Child ParentID %s doesn't match parent SpanID %s", child.ParentID, parent.SpanID)
		}
	})

	t.Run("EndSpan calculates duration", func(t *testing.T) {
		ctx := context.Background()
		_, span := tracer.StartSpan(ctx, "timed_span")

		// Simulate work
		time.Sleep(10 * time.Millisecond)

		tracer.EndSpan(span)

		if span.EndTime.IsZero() {
			t.Error("EndTime not set")
		}
		if span.Duration == 0 {
			t.Error("Duration not calculated")
		}
		if span.Duration < 10*time.Millisecond {
			t.Errorf("Duration %v less than expected 10ms", span.Duration)
		}
	})

	t.Run("RecordMetric doesn't panic", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("RecordMetric panicked: %v", r)
			}
		}()
		tracer.RecordMetric("test.metric", 42.0, map[string]string{"label": "value"})
	})

	t.Run("RecordEvent doesn't panic", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("RecordEvent panicked: %v", r)
			}
		}()
		tracer.RecordEvent(context.Background(), "test_event", map[string]interface{}{"key": "value"})
	})

	t.Run("Flush doesn't error", func(t *testing.T) {
		if err := tracer.Flush(context.Background()); err != nil {
			t.Errorf("Flush returned error: %v", err)
		}
	})
}

func TestSpanFromContext(t *testing.T) {
	t.Run("Returns nil for empty context", func(t *testing.T) {
		ctx := context.Background()
		span := SpanFromContext(ctx)
		if span != nil {
			t.Errorf("Expected nil, got %v", span)
		}
	})

	t.Run("Returns span from context", func(t *testing.T) {
		ctx := context.Background()
		originalSpan := &Span{SpanID: "test-123"}
		ctx = ContextWithSpan(ctx, originalSpan)

		retrieved := SpanFromContext(ctx)
		if retrieved != originalSpan {
			t.Error("Retrieved span doesn't match original")
		}
		if retrieved.SpanID != "test-123" {
			t.Errorf("Expected SpanID test-123, got %s", retrieved.SpanID)
		}
	})
}

// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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

	"go.uber.org/zap"
)

func TestNewEmbeddedTracer_Memory(t *testing.T) {
	config := &EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0, // Disable periodic flushing for tests
	}

	tracer, err := NewEmbeddedTracer(config)
	if err != nil {
		t.Fatalf("Failed to create embedded tracer: %v", err)
	}
	defer tracer.Close()

	if tracer.storage == nil {
		t.Fatal("Expected storage to be initialized")
	}
}

func TestEmbeddedTracer_StartEndSpan(t *testing.T) {
	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
	})
	if err != nil {
		t.Fatalf("Failed to create tracer: %v", err)
	}
	defer tracer.Close()

	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "test.operation",
		WithAttribute("test_key", "test_value"),
	)

	if span == nil {
		t.Fatal("Expected span to be created")
	}
	if span.Name != "test.operation" {
		t.Errorf("Expected name 'test.operation', got %q", span.Name)
	}
	if span.Attributes["test_key"] != "test_value" {
		t.Error("Expected attribute to be set")
	}

	// Simulate some work
	time.Sleep(10 * time.Millisecond)

	tracer.EndSpan(span)

	if span.Duration == 0 {
		t.Error("Expected duration to be calculated")
	}
}

func TestEmbeddedTracer_SpanHierarchy(t *testing.T) {
	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
	})
	if err != nil {
		t.Fatalf("Failed to create tracer: %v", err)
	}
	defer tracer.Close()

	ctx := context.Background()

	// Create parent span
	ctx, parentSpan := tracer.StartSpan(ctx, "parent")
	if parentSpan.ParentID != "" {
		t.Error("Expected parent span to have no parent")
	}

	// Create child span
	_, childSpan := tracer.StartSpan(ctx, "child")
	if childSpan.ParentID != parentSpan.SpanID {
		t.Errorf("Expected child parent ID %s, got %s", parentSpan.SpanID, childSpan.ParentID)
	}
	if childSpan.TraceID != parentSpan.TraceID {
		t.Error("Expected child to inherit parent's trace ID")
	}

	tracer.EndSpan(childSpan)
	tracer.EndSpan(parentSpan)
}

func TestEmbeddedTracer_ErrorRecording(t *testing.T) {
	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
	})
	if err != nil {
		t.Fatalf("Failed to create tracer: %v", err)
	}
	defer tracer.Close()

	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "test.operation")

	// Record error
	span.RecordError(context.DeadlineExceeded)

	if span.Status.Code != StatusError {
		t.Error("Expected status code to be StatusError")
	}

	tracer.EndSpan(span)
}

func TestEmbeddedTracer_MetricsCalculation(t *testing.T) {
	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
	})
	if err != nil {
		t.Fatalf("Failed to create tracer: %v", err)
	}
	defer tracer.Close()

	ctx := context.Background()

	// Create successful spans
	for i := 0; i < 3; i++ {
		var span *Span
		ctx, span = tracer.StartSpan(ctx, "test.operation")
		time.Sleep(5 * time.Millisecond)
		tracer.EndSpan(span)
	}

	// Create failed span
	var failSpan *Span
	ctx, failSpan = tracer.StartSpan(ctx, "test.operation")
	failSpan.RecordError(context.Canceled)
	tracer.EndSpan(failSpan)

	// Flush metrics
	if err := tracer.Flush(ctx); err != nil {
		t.Fatalf("Failed to flush metrics: %v", err)
	}

	// Metrics are calculated and stored (internal verification via Flush success)
}

func TestEmbeddedTracer_ClosedTracer(t *testing.T) {
	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
	})
	if err != nil {
		t.Fatalf("Failed to create tracer: %v", err)
	}

	// Close tracer
	if err := tracer.Close(); err != nil {
		t.Fatalf("Failed to close tracer: %v", err)
	}

	// Operations on closed tracer should not panic
	_, span := tracer.StartSpan(context.Background(), "test")
	tracer.EndSpan(span)

	// Second close should be safe
	if err := tracer.Close(); err != nil {
		t.Error("Second close should not error")
	}
}

func TestEmbeddedTracer_ConcurrentSpans(t *testing.T) {
	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
	})
	if err != nil {
		t.Fatalf("Failed to create tracer: %v", err)
	}
	defer tracer.Close()

	const numSpans = 10

	// Create spans concurrently
	done := make(chan struct{})
	for i := 0; i < numSpans; i++ {
		go func(idx int) {
			defer func() { done <- struct{}{} }()
			_, span := tracer.StartSpan(context.Background(), "concurrent.operation")
			time.Sleep(time.Millisecond)
			span.SetAttribute("index", idx)
			tracer.EndSpan(span)
		}(i)
	}

	// Wait for all spans
	for i := 0; i < numSpans; i++ {
		<-done
	}

	// All spans processed without errors
}

func TestEmbeddedTracer_SetEvalID(t *testing.T) {
	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
	})
	if err != nil {
		t.Fatalf("Failed to create tracer: %v", err)
	}
	defer tracer.Close()

	// Set custom eval ID
	customEvalID := "custom-eval-123"
	tracer.SetEvalID(customEvalID)

	// Create span
	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "test.operation")
	tracer.EndSpan(span)

	// Verify eval ID was set
	tracer.mu.RLock()
	currentID := tracer.currentEvalID
	tracer.mu.RUnlock()

	if currentID != customEvalID {
		t.Errorf("Expected eval ID %s, got %s", customEvalID, currentID)
	}
}

func TestEmbeddedTracer_RecordMetric(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	})
	if err != nil {
		t.Fatalf("Failed to create tracer: %v", err)
	}
	defer tracer.Close()

	// RecordMetric should not panic (logged only)
	tracer.RecordMetric("test.metric", 42.0, map[string]string{
		"label": "value",
	})
}

func TestEmbeddedTracer_RecordEvent(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	})
	if err != nil {
		t.Fatalf("Failed to create tracer: %v", err)
	}
	defer tracer.Close()

	// RecordEvent should not panic (logged only)
	tracer.RecordEvent(context.Background(), "test.event", map[string]interface{}{
		"key": "value",
	})
}

func TestDefaultEmbeddedConfig(t *testing.T) {
	config := DefaultEmbeddedConfig()

	if config.StorageType != "memory" {
		t.Errorf("Expected storage type 'memory', got %q", config.StorageType)
	}
	if config.MaxMemoryTraces != 10000 {
		t.Errorf("Expected max memory traces 10000, got %d", config.MaxMemoryTraces)
	}
	if config.FlushInterval != 30*time.Second {
		t.Errorf("Expected flush interval 30s, got %v", config.FlushInterval)
	}
}

// Backward compatibility test
func TestNewEmbeddedHawkTracer_BackwardCompat(t *testing.T) {
	config := &EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
	}

	// Old function name should still work
	tracer, err := NewEmbeddedHawkTracer(config)
	if err != nil {
		t.Fatalf("Failed to create embedded tracer with old name: %v", err)
	}
	defer tracer.Close()

	if tracer.storage == nil {
		t.Fatal("Expected storage to be initialized")
	}
}

// Benchmark tests
func BenchmarkEmbeddedTracer_StartEndSpan(b *testing.B) {
	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
	})
	if err != nil {
		b.Fatalf("Failed to create tracer: %v", err)
	}
	defer tracer.Close()

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var span *Span
		ctx, span = tracer.StartSpan(ctx, "benchmark.operation")
		tracer.EndSpan(span)
	}
}

func BenchmarkEmbeddedTracer_WithAttributes(b *testing.B) {
	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
	})
	if err != nil {
		b.Fatalf("Failed to create tracer: %v", err)
	}
	defer tracer.Close()

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var span *Span
		ctx, span = tracer.StartSpan(ctx, "benchmark.operation",
			WithAttribute("key1", "value1"),
			WithAttribute("key2", 42),
			WithAttribute("key3", true),
		)
		tracer.EndSpan(span)
	}
}

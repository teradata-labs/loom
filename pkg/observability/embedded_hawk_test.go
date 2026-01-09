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
//go:build hawk && !fts5

package observability

import (
	"context"
	"testing"
	"time"

	"github.com/teradata-labs/hawk/pkg/core"
	"go.uber.org/zap"
)

func TestNewEmbeddedHawkTracer_Memory(t *testing.T) {
	config := &EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0, // Disable periodic flushing for tests
	}

	tracer, err := NewEmbeddedHawkTracer(config)
	if err != nil {
		t.Fatalf("Failed to create embedded tracer: %v", err)
	}
	defer tracer.Close()

	if tracer.storage == nil {
		t.Fatal("Expected storage to be initialized")
	}
}

func TestEmbeddedHawkTracer_StartEndSpan(t *testing.T) {
	tracer, err := NewEmbeddedHawkTracer(&EmbeddedConfig{
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

func TestEmbeddedHawkTracer_SpanHierarchy(t *testing.T) {
	tracer, err := NewEmbeddedHawkTracer(&EmbeddedConfig{
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

func TestEmbeddedHawkTracer_SpanStorage(t *testing.T) {
	tracer, err := NewEmbeddedHawkTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
	})
	if err != nil {
		t.Fatalf("Failed to create tracer: %v", err)
	}
	defer tracer.Close()

	ctx := context.Background()
	ctx, span := tracer.StartSpan(ctx, "test.llm_call",
		WithAttribute(AttrLLMModel, "claude-3-5-sonnet"),
		WithAttribute(AttrSessionID, "session-123"),
		WithAttribute("query", "test query"),
	)

	span.SetAttribute("response", "test response")
	span.SetAttribute("llm.tokens.total", int32(100))

	tracer.EndSpan(span)

	// Verify span was stored
	storage := tracer.GetStorage()
	evalID := tracer.currentEvalID

	runs, err := storage.GetEvalRunsByEvalID(ctx, evalID)
	if err != nil {
		t.Fatalf("Failed to get eval runs: %v", err)
	}

	if len(runs) != 1 {
		t.Fatalf("Expected 1 eval run, got %d", len(runs))
	}

	run := runs[0]
	if run.Model != "claude-3-5-sonnet" {
		t.Errorf("Expected model 'claude-3-5-sonnet', got %q", run.Model)
	}
	if run.SessionID != "session-123" {
		t.Errorf("Expected session ID 'session-123', got %q", run.SessionID)
	}
	if run.Query != "test query" {
		t.Errorf("Expected query 'test query', got %q", run.Query)
	}
	if run.TokenCount != 100 {
		t.Errorf("Expected token count 100, got %d", run.TokenCount)
	}
	if !run.Success {
		t.Error("Expected success to be true")
	}
}

func TestEmbeddedHawkTracer_ErrorRecording(t *testing.T) {
	tracer, err := NewEmbeddedHawkTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
	})
	if err != nil {
		t.Fatalf("Failed to create tracer: %v", err)
	}
	defer tracer.Close()

	ctx := context.Background()
	ctx, span := tracer.StartSpan(ctx, "test.operation")

	// Record error
	span.RecordError(context.DeadlineExceeded)

	tracer.EndSpan(span)

	// Verify error was recorded
	storage := tracer.GetStorage()
	evalID := tracer.currentEvalID

	runs, err := storage.GetEvalRunsByEvalID(ctx, evalID)
	if err != nil {
		t.Fatalf("Failed to get eval runs: %v", err)
	}

	if len(runs) == 0 {
		t.Fatal("Expected at least 1 eval run")
	}

	run := runs[0]
	if run.Success {
		t.Error("Expected success to be false for errored span")
	}
	if run.ErrorMessage == "" {
		t.Error("Expected error message to be set")
	}
}

func TestEmbeddedHawkTracer_MetricsCalculation(t *testing.T) {
	tracer, err := NewEmbeddedHawkTracer(&EmbeddedConfig{
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

	// Verify metrics
	storage := tracer.GetStorage()
	evalID := tracer.currentEvalID

	metrics, err := storage.GetEvalMetrics(ctx, evalID)
	if err != nil {
		t.Fatalf("Failed to get metrics: %v", err)
	}

	if metrics.TotalRuns != 4 {
		t.Errorf("Expected 4 total runs, got %d", metrics.TotalRuns)
	}
	if metrics.SuccessfulRuns != 3 {
		t.Errorf("Expected 3 successful runs, got %d", metrics.SuccessfulRuns)
	}
	if metrics.FailedRuns != 1 {
		t.Errorf("Expected 1 failed run, got %d", metrics.FailedRuns)
	}

	expectedSuccessRate := 0.75 // Hawk returns decimal (0.75 = 75%)
	if metrics.SuccessRate != expectedSuccessRate {
		t.Errorf("Expected success rate %.2f, got %.2f", expectedSuccessRate, metrics.SuccessRate)
	}
}

func TestEmbeddedHawkTracer_ClosedTracer(t *testing.T) {
	tracer, err := NewEmbeddedHawkTracer(&EmbeddedConfig{
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

func TestEmbeddedHawkTracer_ConcurrentSpans(t *testing.T) {
	tracer, err := NewEmbeddedHawkTracer(&EmbeddedConfig{
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

	// Verify all spans were stored
	storage := tracer.GetStorage()
	evalID := tracer.currentEvalID

	runs, err := storage.GetEvalRunsByEvalID(context.Background(), evalID)
	if err != nil {
		t.Fatalf("Failed to get eval runs: %v", err)
	}

	if len(runs) != numSpans {
		t.Errorf("Expected %d runs, got %d", numSpans, len(runs))
	}
}

func TestEmbeddedHawkTracer_SetEvalID(t *testing.T) {
	tracer, err := NewEmbeddedHawkTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
	})
	if err != nil {
		t.Fatalf("Failed to create tracer: %v", err)
	}
	defer tracer.Close()

	// Set custom eval ID
	customEvalID := "custom-eval-123"

	// Create eval first
	storage := tracer.GetStorage()
	eval := &core.Eval{
		ID:        customEvalID,
		Name:      "Custom Eval",
		Suite:     "test",
		Status:    "running",
		CreatedAt: time.Now().Unix(),
	}
	if err := storage.CreateEval(context.Background(), eval); err != nil {
		t.Fatalf("Failed to create eval: %v", err)
	}

	tracer.SetEvalID(customEvalID)

	// Create span
	ctx := context.Background()
	ctx, span := tracer.StartSpan(ctx, "test.operation")
	tracer.EndSpan(span)

	// Verify span was associated with custom eval
	runs, err := storage.GetEvalRunsByEvalID(ctx, customEvalID)
	if err != nil {
		t.Fatalf("Failed to get eval runs: %v", err)
	}

	if len(runs) != 1 {
		t.Fatalf("Expected 1 run, got %d", len(runs))
	}

	if runs[0].EvalID != customEvalID {
		t.Errorf("Expected eval ID %s, got %s", customEvalID, runs[0].EvalID)
	}
}

func TestEmbeddedHawkTracer_RecordMetric(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracer, err := NewEmbeddedHawkTracer(&EmbeddedConfig{
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

func TestEmbeddedHawkTracer_RecordEvent(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracer, err := NewEmbeddedHawkTracer(&EmbeddedConfig{
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

// Benchmark tests
func BenchmarkEmbeddedHawkTracer_StartEndSpan(b *testing.B) {
	tracer, err := NewEmbeddedHawkTracer(&EmbeddedConfig{
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

func BenchmarkEmbeddedHawkTracer_WithAttributes(b *testing.B) {
	tracer, err := NewEmbeddedHawkTracer(&EmbeddedConfig{
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

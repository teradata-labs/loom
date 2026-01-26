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
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestAutoSelectTracer_EmbeddedMemory verifies embedded memory storage works via auto-selection
func TestAutoSelectTracer_EmbeddedMemory(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	config := &AutoSelectConfig{
		Mode:                TracerModeEmbedded,
		EmbeddedStorageType: "memory",
		Logger:              logger,
	}

	tracer, err := NewAutoSelectTracer(config)
	if err != nil {
		t.Fatalf("Failed to create tracer: %v", err)
	}

	// Verify it's an embedded tracer and get concrete type for Close
	embeddedTracer, ok := tracer.(*EmbeddedTracer)
	if !ok {
		t.Errorf("Expected EmbeddedTracer, got %T", tracer)
		return
	}
	defer embeddedTracer.Close()

	// Test basic span operations
	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "test.operation")
	span.SetAttribute("test_key", "test_value")
	time.Sleep(10 * time.Millisecond)
	tracer.EndSpan(span)

	if span.Duration == 0 {
		t.Error("Expected duration to be calculated")
	}
}

// TestAutoSelectTracer_EmbeddedSQLite verifies embedded SQLite storage works via auto-selection
func TestAutoSelectTracer_EmbeddedSQLite(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	// Create temp database path
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test-traces.db")

	config := &AutoSelectConfig{
		Mode:                TracerModeEmbedded,
		EmbeddedStorageType: "sqlite",
		EmbeddedSQLitePath:  dbPath,
		Logger:              logger,
	}

	tracer, err := NewAutoSelectTracer(config)
	if err != nil {
		t.Fatalf("Failed to create tracer: %v", err)
	}

	// Verify it's an embedded tracer and get concrete type for Close
	embeddedTracer, ok := tracer.(*EmbeddedTracer)
	if !ok {
		t.Errorf("Expected EmbeddedTracer, got %T", tracer)
		return
	}
	defer embeddedTracer.Close()

	// Test basic span operations
	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "test.sqlite.operation")
	span.SetAttribute("backend", "sqlite")
	time.Sleep(10 * time.Millisecond)
	tracer.EndSpan(span)

	// Verify database file was created
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Errorf("SQLite database file was not created at %s", dbPath)
	}
}

// TestAutoSelectTracer_Auto_PreferEmbedded verifies auto-selection prefers embedded
func TestAutoSelectTracer_Auto_PreferEmbedded(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	config := &AutoSelectConfig{
		Mode:                TracerModeAuto,
		PreferEmbedded:      true,
		EmbeddedStorageType: "memory",
		Logger:              logger,
	}

	tracer, err := NewAutoSelectTracer(config)
	if err != nil {
		t.Fatalf("Failed to create tracer: %v", err)
	}

	// Should select embedded tracer
	embeddedTracer, ok := tracer.(*EmbeddedTracer)
	if !ok {
		t.Errorf("Expected EmbeddedTracer with PreferEmbedded=true, got %T", tracer)
		return
	}
	defer embeddedTracer.Close()
}

// TestAutoSelectTracer_None verifies none mode returns NoOpTracer
func TestAutoSelectTracer_None(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	config := &AutoSelectConfig{
		Mode:   TracerModeNone,
		Logger: logger,
	}

	tracer, err := NewAutoSelectTracer(config)
	if err != nil {
		t.Fatalf("Failed to create tracer: %v", err)
	}

	// Should return NoOpTracer
	if _, ok := tracer.(*NoOpTracer); !ok {
		t.Errorf("Expected NoOpTracer, got %T", tracer)
	}
}

// TestEmbeddedTracer_EndToEnd simulates full agent tracing workflow
func TestEmbeddedTracer_EndToEnd(t *testing.T) {
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

	ctx := context.Background()

	// Simulate agent conversation with multiple turns
	for turn := 0; turn < 3; turn++ {
		// LLM call span
		_, llmSpan := tracer.StartSpan(ctx, "llm.completion",
			WithAttribute(AttrLLMModel, "claude-sonnet-4-5"),
			WithAttribute(AttrSessionID, "test-session-123"),
			WithAttribute("turn", turn),
		)

		time.Sleep(5 * time.Millisecond)

		llmSpan.SetAttribute("llm.tokens.total", int32(100+turn*10))
		llmSpan.SetAttribute("response", "Test response")

		tracer.EndSpan(llmSpan)

		// Tool execution span
		_, toolSpan := tracer.StartSpan(ctx, "tool.execute",
			WithAttribute("tool.name", "calculator"),
			WithAttribute("turn", turn),
		)

		time.Sleep(2 * time.Millisecond)

		toolSpan.SetAttribute("tool.result", "42")
		tracer.EndSpan(toolSpan)
	}

	// Flush metrics
	if err := tracer.Flush(ctx); err != nil {
		t.Fatalf("Failed to flush metrics: %v", err)
	}

	// Verify tracer collected spans without errors
	t.Log("Successfully traced full agent workflow with embedded storage")
}

// Benchmark embedded tracer throughput
func BenchmarkEmbeddedTracer_HighThroughput(b *testing.B) {
	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:     "memory",
		MaxMemoryTraces: 100000,
		FlushInterval:   0,
	})
	if err != nil {
		b.Fatalf("Failed to create tracer: %v", err)
	}
	defer tracer.Close()

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, span := tracer.StartSpan(ctx, "benchmark.operation",
			WithAttribute("iteration", i),
			WithAttribute(AttrLLMModel, "test-model"),
		)
		span.SetAttribute("llm.tokens.total", int32(100))
		tracer.EndSpan(span)
	}
	b.StopTimer()

	// Report throughput
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "spans/sec")
}

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
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestMockSpanExporter_ExportSpans(t *testing.T) {
	exporter := NewMockSpanExporter()

	span1 := &Span{TraceID: "t1", SpanID: "s1", Name: "op1"}
	span2 := &Span{TraceID: "t1", SpanID: "s2", Name: "op2"}

	err := exporter.ExportSpans(context.Background(), []*Span{span1, span2})
	require.NoError(t, err)

	exported := exporter.GetExportedSpans()
	assert.Len(t, exported, 2)
	assert.Equal(t, "op1", exported[0].Name)
	assert.Equal(t, "op2", exported[1].Name)
}

func TestMockSpanExporter_ExportError(t *testing.T) {
	exporter := NewMockSpanExporter()
	testErr := errors.New("export failed")
	exporter.SetExportError(testErr)

	err := exporter.ExportSpans(context.Background(), []*Span{{Name: "op"}})
	assert.ErrorIs(t, err, testErr)
	assert.Empty(t, exporter.GetExportedSpans())
}

func TestMockSpanExporter_Shutdown(t *testing.T) {
	exporter := NewMockSpanExporter()
	assert.False(t, exporter.IsShutdown())

	err := exporter.Shutdown(context.Background())
	require.NoError(t, err)
	assert.True(t, exporter.IsShutdown())
}

func TestMockSpanExporter_Reset(t *testing.T) {
	exporter := NewMockSpanExporter()
	_ = exporter.ExportSpans(context.Background(), []*Span{{Name: "op"}})
	_ = exporter.Shutdown(context.Background())

	exporter.Reset()
	assert.Empty(t, exporter.GetExportedSpans())
	assert.False(t, exporter.IsShutdown())
}

func TestMockSpanExporter_ConcurrentAccess(t *testing.T) {
	exporter := NewMockSpanExporter()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = exporter.ExportSpans(context.Background(), []*Span{{Name: "concurrent"}})
		}()
	}
	wg.Wait()

	assert.Len(t, exporter.GetExportedSpans(), 100)
}

func TestEmbeddedTracer_WithSpanExporter(t *testing.T) {
	exporter := NewMockSpanExporter()
	logger := zaptest.NewLogger(t)

	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	}, WithSpanExporter(exporter))
	require.NoError(t, err)
	defer func() { _ = tracer.Close() }()

	ctx := context.Background()
	ctx, span := tracer.StartSpan(ctx, "test.operation",
		WithAttribute("key", "value"),
	)
	tracer.EndSpan(span)

	// Verify span was exported with all lifecycle fields
	exported := exporter.GetExportedSpans()
	require.Len(t, exported, 1)
	assert.Equal(t, "test.operation", exported[0].Name)
	assert.Equal(t, "value", exported[0].Attributes["key"])
	assert.NotZero(t, exported[0].Duration)
	assert.NotEmpty(t, exported[0].TraceID)
	assert.NotEmpty(t, exported[0].SpanID)
	assert.Empty(t, exported[0].ParentID, "root span should have no parent")
	assert.False(t, exported[0].StartTime.IsZero())
	assert.False(t, exported[0].EndTime.IsZero())
}

func TestEmbeddedTracer_WithSpanExporter_ParentChild(t *testing.T) {
	exporter := NewMockSpanExporter()
	logger := zaptest.NewLogger(t)

	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	}, WithSpanExporter(exporter))
	require.NoError(t, err)
	defer func() { _ = tracer.Close() }()

	ctx := context.Background()

	// Create parent span
	ctx, parent := tracer.StartSpan(ctx, "parent.op")
	// Create child span (context carries parent)
	_, child := tracer.StartSpan(ctx, "child.op")

	// End child first, then parent
	tracer.EndSpan(child)
	tracer.EndSpan(parent)

	exported := exporter.GetExportedSpans()
	require.Len(t, exported, 2)

	// Child should reference parent
	assert.Equal(t, parent.TraceID, child.TraceID)
	assert.Equal(t, parent.SpanID, child.ParentID)
}

func TestEmbeddedTracer_WithSpanExporter_ExportErrorDoesNotBlock(t *testing.T) {
	exporter := NewMockSpanExporter()
	exporter.SetExportError(errors.New("export failed"))
	logger := zaptest.NewLogger(t)

	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	}, WithSpanExporter(exporter))
	require.NoError(t, err)
	defer func() { _ = tracer.Close() }()

	// Should not panic or block even when export fails
	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "test.op")
	tracer.EndSpan(span) // This logs an error but does not propagate it
}

func TestEmbeddedTracer_CloseShutdownsExporter(t *testing.T) {
	exporter := NewMockSpanExporter()
	logger := zaptest.NewLogger(t)

	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	}, WithSpanExporter(exporter))
	require.NoError(t, err)

	assert.False(t, exporter.IsShutdown())
	err = tracer.Close()
	require.NoError(t, err)
	assert.True(t, exporter.IsShutdown())
}

func TestEmbeddedTracer_WithDefaultResourceAttributes(t *testing.T) {
	exporter := NewMockSpanExporter()
	logger := zaptest.NewLogger(t)

	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	}, WithSpanExporter(exporter), WithDefaultResourceAttributes(map[string]string{
		ResourceAttrServiceName:    "loom-cloud",
		ResourceAttrServiceVersion: "1.0.0",
	}))
	require.NoError(t, err)
	defer func() { _ = tracer.Close() }()

	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "test.op")
	tracer.EndSpan(span)

	exported := exporter.GetExportedSpans()
	require.Len(t, exported, 1)
	assert.Equal(t, "loom-cloud", exported[0].ResourceAttributes[ResourceAttrServiceName])
	assert.Equal(t, "1.0.0", exported[0].ResourceAttributes[ResourceAttrServiceVersion])
}

func TestEmbeddedTracer_SpanResourceAttributeOverride(t *testing.T) {
	exporter := NewMockSpanExporter()
	logger := zaptest.NewLogger(t)

	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	}, WithSpanExporter(exporter), WithDefaultResourceAttributes(map[string]string{
		ResourceAttrServiceName: "default-service",
	}))
	require.NoError(t, err)
	defer func() { _ = tracer.Close() }()

	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "test.op",
		WithResourceAttributes(map[string]string{
			ResourceAttrUserID: "user-123",
		}),
	)
	tracer.EndSpan(span)

	exported := exporter.GetExportedSpans()
	require.Len(t, exported, 1)
	// Default should be present
	assert.Equal(t, "default-service", exported[0].ResourceAttributes[ResourceAttrServiceName])
	// Span-specific override should also be present
	assert.Equal(t, "user-123", exported[0].ResourceAttributes[ResourceAttrUserID])
}

func TestContextWithTraceID(t *testing.T) {
	logger := zaptest.NewLogger(t)
	exporter := NewMockSpanExporter()

	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	}, WithSpanExporter(exporter))
	require.NoError(t, err)
	defer func() { _ = tracer.Close() }()

	// Set an explicit trace ID on the context
	customTraceID := "custom-trace-id-12345"
	ctx := ContextWithTraceID(context.Background(), customTraceID)

	// Root span should pick up the custom trace ID
	_, span := tracer.StartSpan(ctx, "root.op")
	tracer.EndSpan(span)

	exported := exporter.GetExportedSpans()
	require.Len(t, exported, 1)
	assert.Equal(t, customTraceID, exported[0].TraceID)
}

func TestContextWithTraceID_ParentOverrides(t *testing.T) {
	logger := zaptest.NewLogger(t)
	exporter := NewMockSpanExporter()

	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	}, WithSpanExporter(exporter))
	require.NoError(t, err)
	defer func() { _ = tracer.Close() }()

	// Set a context trace ID, then create parent span which generates its own
	ctx := ContextWithTraceID(context.Background(), "override-id")
	ctx, parent := tracer.StartSpan(ctx, "parent.op")
	assert.Equal(t, "override-id", parent.TraceID)

	// Child should inherit parent's trace ID, not the context override
	_, child := tracer.StartSpan(ctx, "child.op")
	tracer.EndSpan(child)
	tracer.EndSpan(parent)

	exported := exporter.GetExportedSpans()
	require.Len(t, exported, 2)
	// Both should have the same trace ID (from parent, which used override)
	assert.Equal(t, "override-id", exported[0].TraceID)
	assert.Equal(t, "override-id", exported[1].TraceID)
}

func TestTraceIDFromContext(t *testing.T) {
	// No span in context
	assert.Empty(t, TraceIDFromContext(context.Background()))

	// With span
	ctx := ContextWithSpan(context.Background(), &Span{TraceID: "abc-123"})
	assert.Equal(t, "abc-123", TraceIDFromContext(ctx))
}

func TestSpan_ResourceAttributes(t *testing.T) {
	span := &Span{Name: "test"}

	// SetResourceAttribute initializes map and sets value
	span.SetResourceAttribute("key1", "val1")
	assert.Equal(t, "val1", span.ResourceAttributes["key1"])

	// Second call doesn't reset the map
	span.SetResourceAttribute("key2", "val2")
	assert.Equal(t, "val1", span.ResourceAttributes["key1"])
	assert.Equal(t, "val2", span.ResourceAttributes["key2"])
}

func TestNoOpTracer_RespectsContextTraceID(t *testing.T) {
	tracer := NewNoOpTracer()

	ctx := ContextWithTraceID(context.Background(), "noop-trace-id")
	_, span := tracer.StartSpan(ctx, "test.op")
	tracer.EndSpan(span)
	assert.Equal(t, "noop-trace-id", span.TraceID)
}

func TestMockTracer_RespectsContextTraceID(t *testing.T) {
	tracer := NewMockTracer()

	ctx := ContextWithTraceID(context.Background(), "mock-trace-id")
	_, span := tracer.StartSpan(ctx, "test.op")
	tracer.EndSpan(span)
	assert.Equal(t, "mock-trace-id", span.TraceID)
}

func TestEmbeddedTracer_ConcurrentWithExporter(t *testing.T) {
	exporter := NewMockSpanExporter()
	logger := zaptest.NewLogger(t)

	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	}, WithSpanExporter(exporter))
	require.NoError(t, err)
	defer func() { _ = tracer.Close() }()

	const goroutines = 50
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			ctx, span := tracer.StartSpan(ctx, "concurrent.op")
			tracer.EndSpan(span)
		}()
	}
	wg.Wait()

	exported := exporter.GetExportedSpans()
	assert.Len(t, exported, goroutines)
}

func TestEmbeddedTracer_ExportAfterClose(t *testing.T) {
	exporter := NewMockSpanExporter()
	logger := zaptest.NewLogger(t)

	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	}, WithSpanExporter(exporter))
	require.NoError(t, err)

	// Start a span before close
	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "pre-close.op")

	// Close the tracer (shuts down exporter)
	err = tracer.Close()
	require.NoError(t, err)
	assert.True(t, exporter.IsShutdown())

	// EndSpan on already-closed tracer should not panic or export
	tracer.EndSpan(span)

	// Only the close-time flush should have been called; span should not be exported
	// because the tracer was closed
	exported := exporter.GetExportedSpans()
	assert.Empty(t, exported, "span should not be exported after tracer is closed")
}

func TestEmbeddedTracer_NilSpanExporterOption(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Passing nil exporter via option should not panic
	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	}, WithSpanExporter(nil))
	require.NoError(t, err)

	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "test.op")
	tracer.EndSpan(span) // should not panic

	err = tracer.Close()
	require.NoError(t, err)
}

func TestEmbeddedTracer_ResourceAttributeKeyConflict(t *testing.T) {
	exporter := NewMockSpanExporter()
	logger := zaptest.NewLogger(t)

	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	}, WithSpanExporter(exporter), WithDefaultResourceAttributes(map[string]string{
		ResourceAttrServiceName: "default-service",
		ResourceAttrUserID:      "default-user",
	}))
	require.NoError(t, err)
	defer func() { _ = tracer.Close() }()

	ctx := context.Background()
	// Span option overrides the same key set by default
	_, span := tracer.StartSpan(ctx, "test.op",
		WithResourceAttributes(map[string]string{
			ResourceAttrServiceName: "override-service",
		}),
	)
	tracer.EndSpan(span)

	exported := exporter.GetExportedSpans()
	require.Len(t, exported, 1)
	// Span-level should win for conflicting key
	assert.Equal(t, "override-service", exported[0].ResourceAttributes[ResourceAttrServiceName])
	// Non-conflicting default should survive
	assert.Equal(t, "default-user", exported[0].ResourceAttributes[ResourceAttrUserID])
}

func TestEmbeddedTracer_ChildInheritsParentResourceAttributes(t *testing.T) {
	exporter := NewMockSpanExporter()
	logger := zaptest.NewLogger(t)

	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	}, WithSpanExporter(exporter), WithDefaultResourceAttributes(map[string]string{
		ResourceAttrServiceName:    "loom-cloud",
		ResourceAttrServiceVersion: "1.2.0",
		ResourceAttrUserID:         "user-42",
	}))
	require.NoError(t, err)
	defer func() { _ = tracer.Close() }()

	ctx := context.Background()

	// Parent gets default resource attributes
	ctx, parent := tracer.StartSpan(ctx, "parent.op")
	// Child should inherit from parent
	_, child := tracer.StartSpan(ctx, "child.op")

	tracer.EndSpan(child)
	tracer.EndSpan(parent)

	exported := exporter.GetExportedSpans()
	require.Len(t, exported, 2)

	// Both should have same resource attributes
	assert.Equal(t, "loom-cloud", parent.ResourceAttributes[ResourceAttrServiceName])
	assert.Equal(t, "1.2.0", parent.ResourceAttributes[ResourceAttrServiceVersion])
	assert.Equal(t, "user-42", parent.ResourceAttributes[ResourceAttrUserID])

	assert.Equal(t, "loom-cloud", child.ResourceAttributes[ResourceAttrServiceName])
	assert.Equal(t, "1.2.0", child.ResourceAttributes[ResourceAttrServiceVersion])
	assert.Equal(t, "user-42", child.ResourceAttributes[ResourceAttrUserID])
}

func TestEmbeddedTracer_ChildExplicitResourceAttrsNotOverwritten(t *testing.T) {
	exporter := NewMockSpanExporter()
	logger := zaptest.NewLogger(t)

	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	}, WithSpanExporter(exporter), WithDefaultResourceAttributes(map[string]string{
		ResourceAttrServiceName: "loom-cloud",
	}))
	require.NoError(t, err)
	defer func() { _ = tracer.Close() }()

	ctx := context.Background()

	// Parent has default resource attrs
	ctx, parent := tracer.StartSpan(ctx, "parent.op")
	// Child explicitly sets its own resource attrs — should NOT be overwritten by parent
	_, child := tracer.StartSpan(ctx, "child.op",
		WithResourceAttributes(map[string]string{
			ResourceAttrServiceName: "child-service",
		}),
	)

	tracer.EndSpan(child)
	tracer.EndSpan(parent)

	exported := exporter.GetExportedSpans()
	require.Len(t, exported, 2)

	assert.Equal(t, "loom-cloud", parent.ResourceAttributes[ResourceAttrServiceName])
	// Child's explicit value should be preserved
	assert.Equal(t, "child-service", child.ResourceAttributes[ResourceAttrServiceName])
}

func TestNoOpTracer_ChildInheritsParentResourceAttributes(t *testing.T) {
	tracer := NewNoOpTracer()

	ctx := context.Background()

	// Create parent with resource attributes
	ctx, parent := tracer.StartSpan(ctx, "parent.op",
		WithResourceAttributes(map[string]string{
			ResourceAttrServiceName: "test-svc",
			ResourceAttrAgentID:     "agent-1",
		}),
	)

	// Child should inherit
	_, child := tracer.StartSpan(ctx, "child.op")
	tracer.EndSpan(child)
	tracer.EndSpan(parent)

	assert.Equal(t, "test-svc", child.ResourceAttributes[ResourceAttrServiceName])
	assert.Equal(t, "agent-1", child.ResourceAttributes[ResourceAttrAgentID])
}

func TestMockTracer_ChildInheritsParentResourceAttributes(t *testing.T) {
	tracer := NewMockTracer()

	ctx := context.Background()

	ctx, parent := tracer.StartSpan(ctx, "parent.op",
		WithResourceAttributes(map[string]string{
			ResourceAttrServiceName: "mock-svc",
		}),
	)

	_, child := tracer.StartSpan(ctx, "child.op")
	tracer.EndSpan(child)
	tracer.EndSpan(parent)

	assert.Equal(t, "mock-svc", child.ResourceAttributes[ResourceAttrServiceName])
	assert.Equal(t, parent.SpanID, child.ParentID)
}

func TestMockSpanExporter_ResetClearsExportError(t *testing.T) {
	exporter := NewMockSpanExporter()
	exporter.SetExportError(errors.New("fail"))

	err := exporter.ExportSpans(context.Background(), []*Span{{Name: "op"}})
	assert.Error(t, err)

	exporter.Reset()

	// After reset, export should succeed
	err = exporter.ExportSpans(context.Background(), []*Span{{Name: "op2"}})
	require.NoError(t, err)
	assert.Len(t, exporter.GetExportedSpans(), 1)
}

func TestNoOpTracer_EndSpanNil(t *testing.T) {
	tracer := NewNoOpTracer()
	tracer.EndSpan(nil) // should not panic
}

func TestEmbeddedTracer_EndSpanNil(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tracer, err := NewEmbeddedTracer(&EmbeddedConfig{
		StorageType:   "memory",
		FlushInterval: 0,
		Logger:        logger,
	})
	require.NoError(t, err)
	defer func() { _ = tracer.Close() }()

	tracer.EndSpan(nil) // should not panic
}

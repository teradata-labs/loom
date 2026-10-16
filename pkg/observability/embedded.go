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

// Package observability provides embedded trace storage for in-process observability.
//
// EmbeddedTracer stores traces in-process using self-contained storage (memory or SQLite),
// providing high-performance tracing without external dependencies.
package observability

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/teradata-labs/loom/pkg/observability/storage"
	"go.uber.org/zap"
)

// EmbeddedConfig configures the embedded tracer
type EmbeddedConfig struct {
	// StorageType: "memory" (default) or "sqlite"
	StorageType string

	// SQLitePath: Path to SQLite database file (required if StorageType = "sqlite")
	SQLitePath string

	// MaxMemoryTraces: Maximum traces to keep in memory storage (default: 10,000)
	MaxMemoryTraces int

	// Logger for embedded tracer (optional)
	Logger *zap.Logger

	// FlushInterval: How often to flush metrics (default: 30s)
	FlushInterval time.Duration
}

// DefaultEmbeddedConfig returns sensible defaults for embedded mode
func DefaultEmbeddedConfig() *EmbeddedConfig {
	return &EmbeddedConfig{
		StorageType:     "memory",
		MaxMemoryTraces: 10000,
		FlushInterval:   30 * time.Second,
	}
}

// EmbeddedTracerOption configures an EmbeddedTracer.
type EmbeddedTracerOption func(*EmbeddedTracer)

// WithSpanExporter attaches a SpanExporter to the EmbeddedTracer.
// The exporter receives completed spans on EndSpan in addition to the
// embedded storage. This enables exporting spans to external systems
// (e.g., PostgreSQL via CloudSpanExporter) while keeping local storage.
func WithSpanExporter(exporter SpanExporter) EmbeddedTracerOption {
	return func(t *EmbeddedTracer) {
		t.spanExporter = exporter
	}
}

// WithDefaultResourceAttributes sets resource attributes that are
// automatically applied to every span created by this tracer.
func WithDefaultResourceAttributes(attrs map[string]string) EmbeddedTracerOption {
	return func(t *EmbeddedTracer) {
		t.defaultResourceAttrs = attrs
	}
}

// EmbeddedTracer implements Tracer interface using embedded storage
type EmbeddedTracer struct {
	recorder             *EvalRunRecorder // eval_runs storage, eval ID and metrics flush
	config               *EmbeddedConfig
	logger               *zap.Logger
	mu                   sync.RWMutex
	activeSpans          map[string]*Span
	closed               bool
	spanExporter         SpanExporter
	defaultResourceAttrs map[string]string
}

// NewEmbeddedTracer creates a new embedded tracer with in-process storage.
// Options can be used to attach a SpanExporter or set default resource attributes.
func NewEmbeddedTracer(config *EmbeddedConfig, opts ...EmbeddedTracerOption) (*EmbeddedTracer, error) {
	if config == nil {
		config = DefaultEmbeddedConfig()
	}

	// Create logger if not provided
	logger := config.Logger
	if logger == nil {
		var err error
		logger, err = zap.NewProduction()
		if err != nil {
			return nil, fmt.Errorf("failed to create logger: %w", err)
		}
	}

	recorder, err := NewEvalRunRecorder(config, logger)
	if err != nil {
		return nil, err
	}

	tracer := &EmbeddedTracer{
		recorder:    recorder,
		config:      config,
		logger:      logger,
		activeSpans: make(map[string]*Span),
	}

	// Apply options
	for _, opt := range opts {
		opt(tracer)
	}

	logger.Info("embedded tracer initialized",
		zap.String("storage_type", config.StorageType),
		zap.String("sqlite_path", config.SQLitePath),
		zap.Bool("has_span_exporter", tracer.spanExporter != nil),
	)

	return tracer, nil
}

// NewEmbeddedHawkTracer is deprecated - use NewEmbeddedTracer instead.
// Kept for backward compatibility.
func NewEmbeddedHawkTracer(config *EmbeddedConfig) (*EmbeddedTracer, error) {
	return NewEmbeddedTracer(config)
}

// StartSpan creates a new tracing span
func (t *EmbeddedTracer) StartSpan(ctx context.Context, name string, opts ...SpanOption) (context.Context, *Span) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		// Return no-op span if closed
		return NewNoOpTracer().StartSpan(ctx, name, opts...)
	}

	// Determine trace ID: parent > context override > new UUID
	traceID := uuid.New().String()
	if override := traceIDFromContextOverride(ctx); override != "" {
		traceID = override
	}

	// Create span
	span := &Span{
		TraceID:    traceID,
		SpanID:     uuid.New().String(),
		Name:       name,
		StartTime:  time.Now(),
		Attributes: make(map[string]interface{}),
	}

	// Apply default resource attributes
	if len(t.defaultResourceAttrs) > 0 {
		span.ResourceAttributes = make(map[string]string, len(t.defaultResourceAttrs))
		for k, v := range t.defaultResourceAttrs {
			span.ResourceAttributes[k] = v
		}
	}

	// Apply options (may override resource attributes)
	for _, opt := range opts {
		opt(span)
	}

	// Link to parent if exists (parent trace ID takes priority)
	if parent := SpanFromContext(ctx); parent != nil {
		span.TraceID = parent.TraceID
		span.ParentID = parent.SpanID
		// Inherit resource attributes from parent when child has none set
		if len(parent.ResourceAttributes) > 0 && len(span.ResourceAttributes) == 0 {
			span.ResourceAttributes = make(map[string]string, len(parent.ResourceAttributes))
			for k, v := range parent.ResourceAttributes {
				span.ResourceAttributes[k] = v
			}
		}
	}

	// Store active span
	t.activeSpans[span.SpanID] = span

	// Add span to context
	newCtx := ContextWithSpan(ctx, span)

	return newCtx, span
}

// EndSpan completes a tracing span and stores it.
// The span exporter is called after releasing the lock to avoid blocking
// concurrent StartSpan/EndSpan calls during potentially slow network I/O.
func (t *EmbeddedTracer) EndSpan(span *Span) {
	if span == nil {
		return
	}

	t.mu.Lock()

	if t.closed {
		t.mu.Unlock()
		return
	}

	// Calculate duration
	span.EndTime = time.Now()
	span.Duration = span.EndTime.Sub(span.StartTime)

	// Remove from active spans
	delete(t.activeSpans, span.SpanID)

	// Capture exporter ref (set once at construction, safe to read under lock)
	exporter := t.spanExporter

	t.mu.Unlock()

	// Store in embedded storage (outside lock — the recorder synchronizes itself)
	t.recorder.Record(span)
	ctx := context.Background()

	// Export to external exporter if configured (outside lock to avoid
	// blocking concurrent spans during potentially slow network I/O)
	if exporter != nil {
		if err := exporter.ExportSpans(ctx, []*Span{span}); err != nil {
			t.logger.Error("failed to export span",
				zap.String("span_id", span.SpanID),
				zap.Error(err),
			)
		}
	}

	t.logger.Debug("span completed",
		zap.String("span_id", span.SpanID),
		zap.String("operation", span.Name),
		zap.Duration("duration", span.Duration),
	)
}

// SetEvalID sets the current evaluation ID for grouping traces
func (t *EmbeddedTracer) SetEvalID(evalID string) {
	t.recorder.SetEvalID(evalID)
}

// RecordMetric records a point-in-time metric
func (t *EmbeddedTracer) RecordMetric(name string, value float64, labels map[string]string) {
	t.logger.Debug("metric recorded",
		zap.String("name", name),
		zap.Float64("value", value),
		zap.Any("labels", labels),
	)
}

// RecordEvent records a standalone event
func (t *EmbeddedTracer) RecordEvent(ctx context.Context, name string, attributes map[string]interface{}) {
	t.logger.Debug("event recorded",
		zap.String("name", name),
		zap.Any("attributes", attributes),
	)
}

// Flush forces a metrics update for the current evaluation
func (t *EmbeddedTracer) Flush(ctx context.Context) error {
	return t.recorder.Flush(ctx)
}

// Close shuts down the embedded tracer
func (t *EmbeddedTracer) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()

	// Flush and shutdown span exporter if configured (with timeout to avoid hanging)
	if t.spanExporter != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := t.spanExporter.ForceFlush(shutdownCtx); err != nil {
			t.logger.Error("failed to flush span exporter", zap.Error(err))
		}
		if err := t.spanExporter.Shutdown(shutdownCtx); err != nil {
			t.logger.Error("failed to shutdown span exporter", zap.Error(err))
		}
		cancel()
	}

	// Final metrics, mark the eval completed, close storage.
	if err := t.recorder.Close(); err != nil {
		return err
	}

	t.logger.Info("embedded tracer closed")
	return nil
}

// GetStorage returns the underlying storage (for testing/inspection)
func (t *EmbeddedTracer) GetStorage() storage.Storage {
	return t.recorder.Storage()
}

// Compile-time interface check
var _ Tracer = (*EmbeddedTracer)(nil)

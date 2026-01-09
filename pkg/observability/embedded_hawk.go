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
//go:build hawk

// Package observability provides embedded Hawk integration for in-process trace storage.
//
// EmbeddedHawkTracer stores traces in-process using Hawk's core storage (memory or SQLite),
// providing 10,000x faster performance than service mode while maintaining full compatibility.
package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/teradata-labs/hawk/pkg/core"
	"go.uber.org/zap"
)

// EmbeddedConfig configures the embedded Hawk tracer
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

// EmbeddedHawkTracer implements Tracer interface using embedded Hawk storage
type EmbeddedHawkTracer struct {
	storage       core.Storage
	config        *EmbeddedConfig
	logger        *zap.Logger
	mu            sync.RWMutex
	activeSpans   map[string]*Span
	closed        bool
	flushTicker   *time.Ticker
	flushDone     chan struct{}
	currentEvalID string // Current evaluation session
}

// NewEmbeddedHawkTracer creates a new embedded Hawk tracer with in-process storage
func NewEmbeddedHawkTracer(config *EmbeddedConfig) (*EmbeddedHawkTracer, error) {
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

	// Create storage based on type
	storageConfig := &core.StorageConfig{
		Type: config.StorageType,
	}

	if config.StorageType == "sqlite" {
		if config.SQLitePath == "" {
			return nil, fmt.Errorf("sqlite_path required when storage_type = 'sqlite'")
		}
		storageConfig.Path = config.SQLitePath
	}

	storage, err := core.NewStorage(storageConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}

	tracer := &EmbeddedHawkTracer{
		storage:     storage,
		config:      config,
		logger:      logger,
		activeSpans: make(map[string]*Span),
		flushDone:   make(chan struct{}),
	}

	// Start flush ticker for periodic metrics updates
	if config.FlushInterval > 0 {
		tracer.flushTicker = time.NewTicker(config.FlushInterval)
		go tracer.flushLoop()
	}

	logger.Info("embedded hawk tracer initialized",
		zap.String("storage_type", config.StorageType),
		zap.String("sqlite_path", config.SQLitePath),
	)

	return tracer, nil
}

// StartSpan creates a new tracing span
func (t *EmbeddedHawkTracer) StartSpan(ctx context.Context, name string, opts ...SpanOption) (context.Context, *Span) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		// Return no-op span if closed
		return NewNoOpTracer().StartSpan(ctx, name, opts...)
	}

	// Create span
	span := &Span{
		TraceID:    uuid.New().String(),
		SpanID:     uuid.New().String(),
		Name:       name,
		StartTime:  time.Now(),
		Attributes: make(map[string]interface{}),
	}

	// Apply options
	for _, opt := range opts {
		opt(span)
	}

	// Link to parent if exists
	if parent := SpanFromContext(ctx); parent != nil {
		span.TraceID = parent.TraceID
		span.ParentID = parent.SpanID
	}

	// Store active span
	t.activeSpans[span.SpanID] = span

	// Add span to context
	newCtx := ContextWithSpan(ctx, span)

	return newCtx, span
}

// EndSpan completes a tracing span and stores it
func (t *EmbeddedHawkTracer) EndSpan(span *Span) {
	if span == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return
	}

	// Calculate duration
	span.EndTime = time.Now()
	span.Duration = span.EndTime.Sub(span.StartTime)

	// Remove from active spans
	delete(t.activeSpans, span.SpanID)

	// Convert to Hawk EvalRun format
	evalRun := t.spanToEvalRun(span)

	// Store in Hawk storage
	ctx := context.Background()
	if err := t.storage.CreateEvalRun(ctx, evalRun); err != nil {
		t.logger.Error("failed to store eval run",
			zap.String("span_id", span.SpanID),
			zap.Error(err),
		)
		return
	}

	t.logger.Debug("span completed",
		zap.String("span_id", span.SpanID),
		zap.String("operation", span.Name),
		zap.Duration("duration", span.Duration),
	)
}

// spanToEvalRun converts a Span to Hawk's EvalRun format
func (t *EmbeddedHawkTracer) spanToEvalRun(span *Span) *core.EvalRun {
	// Extract metadata from span attributes
	query, _ := span.Attributes["query"].(string)
	model, _ := span.Attributes[AttrLLMModel].(string)
	response, _ := span.Attributes["response"].(string)
	sessionID, _ := span.Attributes[AttrSessionID].(string)

	// Success based on status
	success := span.Status.Code != StatusError
	errorMsg := ""
	if !success {
		errorMsg = span.Status.Message
	}

	// Token count - check multiple possible attribute names
	tokenCount := int32(0)
	if tokens, ok := span.Attributes["llm.tokens.total"].(int); ok {
		tokenCount = int32(tokens)
	} else if tokens, ok := span.Attributes["llm.tokens.total"].(int32); ok {
		tokenCount = tokens
	} else if tokens, ok := span.Attributes["llm.tokens.total"].(float64); ok {
		tokenCount = int32(tokens)
	} else if tokens, ok := span.Attributes["token_count"].(int); ok {
		tokenCount = int32(tokens)
	} else if tokens, ok := span.Attributes["token_count"].(int32); ok {
		tokenCount = tokens
	}

	// Serialize configuration
	configJSON, _ := json.Marshal(span.Attributes)

	return &core.EvalRun{
		ID:                span.SpanID,
		EvalID:            t.getCurrentEvalID(),
		Query:             query,
		Model:             model,
		ConfigurationJSON: string(configJSON),
		Response:          response,
		ExecutionTimeMS:   span.Duration.Milliseconds(),
		TokenCount:        tokenCount,
		Success:           success,
		ErrorMessage:      errorMsg,
		SessionID:         sessionID,
		Timestamp:         span.StartTime.Unix(),
	}
}

// getCurrentEvalID returns the current evaluation ID (or creates one)
func (t *EmbeddedHawkTracer) getCurrentEvalID() string {
	if t.currentEvalID != "" {
		return t.currentEvalID
	}

	// Create a default eval for this session
	evalID := fmt.Sprintf("loom-session-%d", time.Now().Unix())
	t.currentEvalID = evalID

	eval := &core.Eval{
		ID:        evalID,
		Name:      "Loom Agent Session",
		Suite:     "embedded",
		Status:    "running",
		CreatedAt: time.Now().Unix(),
	}

	ctx := context.Background()
	if err := t.storage.CreateEval(ctx, eval); err != nil {
		t.logger.Warn("failed to create eval",
			zap.String("eval_id", evalID),
			zap.Error(err),
		)
	}

	return evalID
}

// SetEvalID sets the current evaluation ID for grouping traces
func (t *EmbeddedHawkTracer) SetEvalID(evalID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.currentEvalID = evalID
}

// RecordMetric records a point-in-time metric (not stored in Hawk currently)
func (t *EmbeddedHawkTracer) RecordMetric(name string, value float64, labels map[string]string) {
	// Could be extended to store metrics in Hawk if needed
	t.logger.Debug("metric recorded",
		zap.String("name", name),
		zap.Float64("value", value),
		zap.Any("labels", labels),
	)
}

// RecordEvent records a standalone event (not stored in Hawk currently)
func (t *EmbeddedHawkTracer) RecordEvent(ctx context.Context, name string, attributes map[string]interface{}) {
	// Could be extended to store events in Hawk if needed
	t.logger.Debug("event recorded",
		zap.String("name", name),
		zap.Any("attributes", attributes),
	)
}

// Flush forces a metrics update for the current evaluation
func (t *EmbeddedHawkTracer) Flush(ctx context.Context) error {
	t.mu.RLock()
	evalID := t.currentEvalID
	t.mu.RUnlock()

	if evalID == "" {
		return nil // No active eval
	}

	// Calculate metrics for current eval
	metrics, err := t.storage.CalculateEvalMetrics(ctx, evalID)
	if err != nil {
		return fmt.Errorf("failed to calculate metrics: %w", err)
	}

	// Update metrics
	if err := t.storage.UpsertEvalMetrics(ctx, metrics); err != nil {
		return fmt.Errorf("failed to update metrics: %w", err)
	}

	t.logger.Debug("metrics flushed",
		zap.String("eval_id", evalID),
		zap.Float64("success_rate", metrics.SuccessRate),
		zap.Int32("total_runs", metrics.TotalRuns),
	)

	return nil
}

// flushLoop periodically flushes metrics
func (t *EmbeddedHawkTracer) flushLoop() {
	for {
		select {
		case <-t.flushTicker.C:
			if err := t.Flush(context.Background()); err != nil {
				t.logger.Error("periodic flush failed", zap.Error(err))
			}
		case <-t.flushDone:
			return
		}
	}
}

// Close shuts down the embedded tracer
func (t *EmbeddedHawkTracer) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true

	// Stop flush ticker
	if t.flushTicker != nil {
		t.flushTicker.Stop()
		close(t.flushDone)
	}

	evalID := t.currentEvalID
	t.mu.Unlock()

	// Final flush (must release lock first to avoid deadlock)
	if evalID != "" {
		ctx := context.Background()
		if err := t.Flush(ctx); err != nil {
			t.logger.Error("final flush failed", zap.Error(err))
		}

		// Mark eval as completed
		if err := t.storage.UpdateEvalStatus(ctx, evalID, "completed"); err != nil {
			t.logger.Error("failed to update eval status", zap.Error(err))
		}
	}

	// Close storage
	if err := t.storage.Close(); err != nil {
		t.logger.Error("failed to close storage", zap.Error(err))
		return err
	}

	t.logger.Info("embedded hawk tracer closed")
	return nil
}

// GetStorage returns the underlying storage (for testing/inspection)
func (t *EmbeddedHawkTracer) GetStorage() core.Storage {
	return t.storage
}

// Compile-time interface check
var _ Tracer = (*EmbeddedHawkTracer)(nil)

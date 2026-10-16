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
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/observability/storage"
	"go.uber.org/zap"
)

// EvalRunRecorder writes completed spans to embedded storage as eval_runs
// rows, one row per span, and keeps the per-session evals row and its
// eval_metrics up to date. EmbeddedTracer records every span through it;
// OTelTracer uses one when OTLP export must also keep the local record
// (observability.otlp_record_eval_runs).
//
// Rows hold the span as Loom recorded it: Loom-native attribute names,
// before export redaction and span filtering.
type EvalRunRecorder struct {
	storage storage.Storage
	logger  *zap.Logger

	mu            sync.Mutex
	currentEvalID string
	closed        bool

	flushTicker *time.Ticker
	flushDone   chan struct{}
}

// NewEvalRunRecorder opens the embedded storage described by config
// (StorageType, SQLitePath, MaxMemoryTraces) and starts the periodic
// eval_metrics flush when config.FlushInterval > 0.
func NewEvalRunRecorder(config *EmbeddedConfig, logger *zap.Logger) (*EvalRunRecorder, error) {
	if config == nil {
		config = DefaultEmbeddedConfig()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	if config.StorageType == "sqlite" && config.SQLitePath == "" {
		return nil, fmt.Errorf("sqlite_path required when storage_type = 'sqlite'")
	}
	stor, err := storage.NewStorage(&storage.StorageConfig{
		Type:            config.StorageType,
		Path:            config.SQLitePath,
		MaxMemoryTraces: config.MaxMemoryTraces,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}
	r := &EvalRunRecorder{
		storage:   stor,
		logger:    logger,
		flushDone: make(chan struct{}),
	}
	if config.FlushInterval > 0 {
		r.flushTicker = time.NewTicker(config.FlushInterval)
		go r.flushLoop()
	}
	return r, nil
}

// Record stores span as an eval_runs row. Storage errors are logged, never
// returned: recording must not fail the operation that produced the span.
func (r *EvalRunRecorder) Record(span *Span) {
	if r == nil || span == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	run := r.spanToEvalRun(span)
	r.mu.Unlock()

	if err := r.storage.CreateEvalRun(context.Background(), run); err != nil {
		r.logger.Error("failed to store eval run",
			zap.String("span_id", span.SpanID),
			zap.Error(err),
		)
	}
}

// SetEvalID sets the evaluation ID that later rows are grouped under.
func (r *EvalRunRecorder) SetEvalID(evalID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.currentEvalID = evalID
}

// CurrentEvalID returns the evaluation ID rows are grouped under, or "" if
// no row has been recorded and none was set.
func (r *EvalRunRecorder) CurrentEvalID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.currentEvalID
}

// Storage returns the underlying storage (for testing/inspection).
func (r *EvalRunRecorder) Storage() storage.Storage {
	return r.storage
}

// Flush recomputes eval_metrics for the current evaluation.
func (r *EvalRunRecorder) Flush(ctx context.Context) error {
	evalID := r.CurrentEvalID()
	if evalID == "" {
		return nil // No active eval
	}

	metrics, err := r.storage.CalculateEvalMetrics(ctx, evalID)
	if err != nil {
		return fmt.Errorf("failed to calculate metrics: %w", err)
	}
	if err := r.storage.UpsertEvalMetrics(ctx, metrics); err != nil {
		return fmt.Errorf("failed to update metrics: %w", err)
	}

	r.logger.Debug("metrics flushed",
		zap.String("eval_id", evalID),
		zap.Float64("success_rate", metrics.SuccessRate),
		zap.Int32("total_runs", metrics.TotalRuns),
	)
	return nil
}

// Close stops the periodic flush, writes final metrics, marks the current
// evaluation completed and closes the storage. It is safe to call twice.
func (r *EvalRunRecorder) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	if r.flushTicker != nil {
		r.flushTicker.Stop()
		close(r.flushDone)
	}
	evalID := r.currentEvalID
	r.mu.Unlock()

	if evalID != "" {
		ctx := context.Background()
		if err := r.Flush(ctx); err != nil {
			r.logger.Error("final flush failed", zap.Error(err))
		}
		if err := r.storage.UpdateEvalStatus(ctx, evalID, "completed"); err != nil {
			r.logger.Error("failed to update eval status", zap.Error(err))
		}
	}

	if err := r.storage.Close(); err != nil {
		r.logger.Error("failed to close storage", zap.Error(err))
		return err
	}
	return nil
}

func (r *EvalRunRecorder) flushLoop() {
	for {
		select {
		case <-r.flushTicker.C:
			if err := r.Flush(context.Background()); err != nil {
				r.logger.Error("periodic flush failed", zap.Error(err))
			}
		case <-r.flushDone:
			return
		}
	}
}

// spanToEvalRun converts a Span to storage EvalRun format. Callers hold r.mu.
func (r *EvalRunRecorder) spanToEvalRun(span *Span) *storage.EvalRun {
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
		tokenCount = safeInt32(tokens)
	} else if tokens, ok := span.Attributes["llm.tokens.total"].(int32); ok {
		tokenCount = tokens
	} else if tokens, ok := span.Attributes["llm.tokens.total"].(float64); ok {
		tokenCount = safeInt32FromFloat64(tokens)
	} else if tokens, ok := span.Attributes["token_count"].(int); ok {
		tokenCount = safeInt32(tokens)
	} else if tokens, ok := span.Attributes["token_count"].(int32); ok {
		tokenCount = tokens
	}

	// Serialize configuration
	configJSON, _ := json.Marshal(span.Attributes)

	return &storage.EvalRun{
		ID:                span.SpanID,
		EvalID:            r.evalIDLocked(),
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

// evalIDLocked returns the current evaluation ID, creating the per-session
// evals row on first use. Callers hold r.mu.
func (r *EvalRunRecorder) evalIDLocked() string {
	if r.currentEvalID != "" {
		return r.currentEvalID
	}

	evalID := fmt.Sprintf("loom-session-%d", time.Now().Unix())
	r.currentEvalID = evalID

	eval := &storage.Eval{
		ID:        evalID,
		Name:      "Loom Agent Session",
		Suite:     "embedded",
		Status:    "running",
		CreatedAt: time.Now().Unix(),
	}
	if err := r.storage.CreateEval(context.Background(), eval); err != nil {
		r.logger.Warn("failed to create eval",
			zap.String("eval_id", evalID),
			zap.Error(err),
		)
	}
	return evalID
}

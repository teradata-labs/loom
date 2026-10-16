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
// before export redaction and span filtering. Each row is written on the
// caller's goroutine, so rows keep the order in which spans ended.
type EvalRunRecorder struct {
	storage storage.Storage
	logger  *zap.Logger

	mu            sync.Mutex
	currentEvalID string
	closed        bool
	// inFlight counts the Record and Flush calls that passed the closed
	// check. Close waits for them before its final flush and before it
	// closes the storage.
	inFlight sync.WaitGroup

	// createEvalMu serializes creation of the per-session evals row and is
	// held across that one storage write. mu never is, so SetEvalID,
	// CurrentEvalID and Close do not wait on storage I/O.
	createEvalMu sync.Mutex

	flushTicker *time.Ticker
	flushDone   chan struct{}
	flushExited chan struct{}
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
		storage:     stor,
		logger:      logger,
		flushDone:   make(chan struct{}),
		flushExited: make(chan struct{}),
	}
	if config.FlushInterval > 0 {
		r.flushTicker = time.NewTicker(config.FlushInterval)
		go r.flushLoop()
	} else {
		close(r.flushExited)
	}
	return r, nil
}

// Record stores span as an eval_runs row. Storage errors are logged, never
// returned: recording must not fail the operation that produced the span.
// After Close it does nothing.
func (r *EvalRunRecorder) Record(span *Span) {
	if r == nil || span == nil || !r.begin() {
		return
	}
	defer r.inFlight.Done()

	run := spanToEvalRun(span, r.evalID())
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

// Flush recomputes eval_metrics for the current evaluation. After Close it
// does nothing.
func (r *EvalRunRecorder) Flush(ctx context.Context) error {
	if !r.begin() {
		return nil
	}
	defer r.inFlight.Done()
	return r.flushMetrics(ctx)
}

// Close stops the periodic flush and waits for it and for the Record and
// Flush calls already under way, then writes final metrics, marks the
// current evaluation completed and closes the storage. It is safe to call
// twice.
func (r *EvalRunRecorder) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()

	if r.flushTicker != nil {
		r.flushTicker.Stop()
		close(r.flushDone)
	}
	<-r.flushExited
	r.inFlight.Wait()

	if evalID := r.CurrentEvalID(); evalID != "" {
		ctx := context.Background()
		if err := r.flushMetrics(ctx); err != nil {
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

// begin registers a Record or Flush call with Close. It reports false once
// the recorder is closed; otherwise the caller must call r.inFlight.Done.
func (r *EvalRunRecorder) begin() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	r.inFlight.Add(1)
	return true
}

func (r *EvalRunRecorder) flushMetrics(ctx context.Context) error {
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

func (r *EvalRunRecorder) flushLoop() {
	defer close(r.flushExited)
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

// evalID returns the evaluation ID rows are grouped under, creating the
// per-session evals row on first use. Concurrent first callers wait for
// that one write, so no row is stored before its evals row.
func (r *EvalRunRecorder) evalID() string {
	if id := r.CurrentEvalID(); id != "" {
		return id
	}
	r.createEvalMu.Lock()
	defer r.createEvalMu.Unlock()
	if id := r.CurrentEvalID(); id != "" {
		return id // created by a concurrent caller, or set by SetEvalID
	}

	evalID := fmt.Sprintf("loom-session-%d", time.Now().Unix())
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

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentEvalID == "" {
		r.currentEvalID = evalID
	}
	return r.currentEvalID
}

// spanToEvalRun converts a Span to the storage EvalRun row grouped under
// evalID.
func spanToEvalRun(span *Span, evalID string) *storage.EvalRun {
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
	if tokens, ok := attrInt64(span.Attributes, "llm.tokens.total"); ok {
		tokenCount = safeInt32FromInt64(tokens)
	} else if tokens, ok := attrInt64(span.Attributes, "token_count"); ok {
		tokenCount = safeInt32FromInt64(tokens)
	}

	// Serialize configuration
	configJSON, _ := json.Marshal(span.Attributes)

	return &storage.EvalRun{
		ID:                span.SpanID,
		EvalID:            evalID,
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

// safeInt32FromInt64 converts an int64 to int32, capping at MaxInt32/MinInt32
// to prevent overflow. This is a local copy to avoid an import cycle with
// pkg/types.
func safeInt32FromInt64(n int64) int32 {
	const maxInt32 = 2147483647  // math.MaxInt32
	const minInt32 = -2147483648 // math.MinInt32
	if n > maxInt32 {
		return maxInt32
	}
	if n < minInt32 {
		return minInt32
	}
	return int32(n) // #nosec G115 -- bounds checked above
}

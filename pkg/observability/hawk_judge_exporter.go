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

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
)

// HawkJudgeExporter exports judge verdicts to Hawk with batching and buffering.
// It runs a background worker that flushes verdicts periodically or when the buffer fills.
//
// Thread-safe: All methods can be called concurrently.
type HawkJudgeExporter struct {
	endpoint      string
	apiKey        string
	httpClient    *http.Client
	buffer        chan *loomv1.JudgeResult
	batchSize     int
	flushInterval time.Duration
	logger        *zap.Logger
	tracer        Tracer
	wg            sync.WaitGroup
	stopCh        chan struct{}
	mu            sync.Mutex
	stopped       bool
}

// HawkJudgeExporterConfig configures the Hawk judge verdict exporter.
type HawkJudgeExporterConfig struct {
	// Endpoint is the Hawk API endpoint (default: HAWK_ENDPOINT env or http://localhost:8080)
	Endpoint string

	// APIKey for authentication (default: HAWK_API_KEY env)
	APIKey string

	// BatchSize is the number of verdicts to batch before sending (default: 10)
	BatchSize int

	// FlushInterval is the max time between flushes (default: 5s)
	FlushInterval time.Duration

	// BufferSize is the size of the verdict buffer channel (default: 100)
	BufferSize int

	// Timeout for HTTP requests (default: 10s)
	Timeout time.Duration

	// Logger for diagnostic output (default: nop logger)
	Logger *zap.Logger

	// Tracer for observability (default: nop tracer)
	Tracer Tracer
}

// NewHawkJudgeExporter creates and starts a new Hawk judge verdict exporter.
// The exporter runs a background goroutine that periodically flushes batched verdicts.
//
// Call Stop(ctx) to gracefully shut down and flush remaining verdicts.
func NewHawkJudgeExporter(config *HawkJudgeExporterConfig) *HawkJudgeExporter {
	if config == nil {
		config = &HawkJudgeExporterConfig{}
	}

	// Apply defaults
	if config.Endpoint == "" {
		config.Endpoint = os.Getenv("HAWK_ENDPOINT")
		if config.Endpoint == "" {
			config.Endpoint = "http://localhost:8080"
		}
	}

	if config.APIKey == "" {
		config.APIKey = os.Getenv("HAWK_API_KEY")
	}

	if config.BatchSize <= 0 {
		config.BatchSize = 10
	}

	if config.FlushInterval == 0 {
		config.FlushInterval = 5 * time.Second
	}

	if config.BufferSize <= 0 {
		config.BufferSize = 100
	}

	if config.Timeout == 0 {
		config.Timeout = 10 * time.Second
	}

	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}

	if config.Tracer == nil {
		config.Tracer = NewNoOpTracer()
	}

	exporter := &HawkJudgeExporter{
		endpoint:      config.Endpoint,
		apiKey:        config.APIKey,
		httpClient:    &http.Client{Timeout: config.Timeout},
		buffer:        make(chan *loomv1.JudgeResult, config.BufferSize),
		batchSize:     config.BatchSize,
		flushInterval: config.FlushInterval,
		logger:        config.Logger,
		tracer:        config.Tracer,
		stopCh:        make(chan struct{}),
	}

	return exporter
}

// Start begins the background flush worker.
// Must be called before ExportJudgeResult.
func (e *HawkJudgeExporter) Start(ctx context.Context) {
	e.wg.Add(1)
	go e.flushWorker(ctx)
}

// ExportJudgeResult adds a judge result to the export buffer (non-blocking).
// Returns an error if the exporter is stopped or the buffer is full.
func (e *HawkJudgeExporter) ExportJudgeResult(ctx context.Context, result *loomv1.JudgeResult) error {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return fmt.Errorf("exporter is stopped")
	}
	e.mu.Unlock()

	// Check context cancellation first
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Non-blocking send
	select {
	case e.buffer <- result:
		e.logger.Debug("Judge verdict buffered for export",
			zap.String("judge_id", result.JudgeId),
			zap.String("verdict", result.Verdict),
		)
		return nil
	default:
		// Buffer full, log warning but don't block
		e.logger.Warn("Judge verdict buffer full, dropping verdict",
			zap.String("judge_id", result.JudgeId),
		)
		return fmt.Errorf("buffer full")
	}
}

// Stop gracefully shuts down the exporter, flushing remaining verdicts.
// Blocks until all buffered verdicts are exported or context is cancelled.
func (e *HawkJudgeExporter) Stop(ctx context.Context) error {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return nil
	}
	e.stopped = true
	e.mu.Unlock()

	e.logger.Info("Stopping Hawk judge exporter, flushing remaining verdicts")

	// Signal stop
	close(e.stopCh)

	// Wait for worker to finish
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		e.logger.Info("Hawk judge exporter stopped")
		return nil
	case <-ctx.Done():
		e.logger.Warn("Hawk judge exporter stop timed out")
		return ctx.Err()
	}
}

// flushWorker is the background goroutine that flushes batched verdicts.
func (e *HawkJudgeExporter) flushWorker(ctx context.Context) {
	defer e.wg.Done()

	ticker := time.NewTicker(e.flushInterval)
	defer ticker.Stop()

	batch := make([]*loomv1.JudgeResult, 0, e.batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}

		if err := e.flush(ctx, batch); err != nil {
			e.logger.Warn("Failed to flush judge verdicts to Hawk",
				zap.Int("batch_size", len(batch)),
				zap.Error(err),
			)
		} else {
			e.logger.Debug("Flushed judge verdicts to Hawk",
				zap.Int("batch_size", len(batch)),
			)
		}

		// Clear batch
		batch = batch[:0]
	}

	for {
		select {
		case <-e.stopCh:
			// Final flush on shutdown
			flush()
			// Drain remaining buffer
			for {
				select {
				case result := <-e.buffer:
					batch = append(batch, result)
					if len(batch) >= e.batchSize {
						flush()
					}
				default:
					// Buffer drained, final flush
					flush()
					return
				}
			}

		case <-ticker.C:
			// Timer-triggered flush
			flush()

		case result := <-e.buffer:
			// Add to batch
			batch = append(batch, result)

			// Flush if batch full
			if len(batch) >= e.batchSize {
				flush()
			}
		}
	}
}

// flush sends a batch of verdicts to Hawk.
func (e *HawkJudgeExporter) flush(ctx context.Context, verdicts []*loomv1.JudgeResult) error {
	if len(verdicts) == 0 {
		return nil
	}

	// Start tracing
	ctx, span := e.tracer.StartSpan(ctx, SpanHawkJudgeVerdictExport)
	defer e.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("hawk.verdict_count", len(verdicts))
		span.SetAttribute("hawk.endpoint", e.endpoint)
	}

	// Construct Hawk judge verdict endpoint
	hawkURL := fmt.Sprintf("%s/v1/judge-verdicts", e.endpoint)

	// Create batch payload
	payload := map[string]interface{}{
		"verdicts": verdicts,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		if span != nil {
			span.SetAttribute("error", true)
			span.SetAttribute("error.message", err.Error())
		}
		return fmt.Errorf("failed to marshal verdicts: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", hawkURL, bytes.NewReader(payloadBytes))
	if err != nil {
		if span != nil {
			span.SetAttribute("error", true)
			span.SetAttribute("error.message", err.Error())
		}
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", e.apiKey))
	}
	req.Header.Set("User-Agent", "loom-judge-exporter/1.0")

	// Send request
	resp, err := e.httpClient.Do(req)
	if err != nil {
		if span != nil {
			span.SetAttribute("error", true)
			span.SetAttribute("error.message", err.Error())
		}
		return fmt.Errorf("failed to export verdicts to Hawk: %w", err)
	}
	defer resp.Body.Close()

	if span != nil {
		span.SetAttribute("http.status_code", resp.StatusCode)
	}

	// Check response
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		if span != nil {
			span.SetAttribute("error", true)
			span.SetAttribute("error.message", fmt.Sprintf("HTTP %d: %s", resp.StatusCode, resp.Status))
		}
		return fmt.Errorf("hawk judge verdict export failed with status %d: %s", resp.StatusCode, resp.Status)
	}

	e.logger.Info("Exported judge verdicts to Hawk",
		zap.Int("count", len(verdicts)),
		zap.String("endpoint", hawkURL),
		zap.Int("status_code", resp.StatusCode),
	)

	return nil
}

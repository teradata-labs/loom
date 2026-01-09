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
package learning

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
)

// PatternEffectivenessTracker tracks pattern usage effectiveness in real-time.
// Runs as a background goroutine, aggregating metrics and flushing to database
// and MessageBus periodically.
//
// Thread-safe for concurrent RecordUsage calls from multiple agents.
type PatternEffectivenessTracker struct {
	db     *sql.DB
	tracer observability.Tracer
	bus    *communication.MessageBus

	windowSize    time.Duration // Aggregation window (default: 1 hour)
	flushInterval time.Duration // Batch write interval (default: 5 minutes)

	// In-memory buffer: key = "pattern:variant:agent:window_start" → stats
	buffer   map[string]*patternStats
	bufferMu sync.RWMutex

	// Background goroutine control
	stopChan chan struct{}
	wg       sync.WaitGroup
	started  bool
	mu       sync.Mutex // Protects started flag
}

// patternStats holds aggregated statistics for a pattern variant during a time window.
type patternStats struct {
	PatternName string
	Variant     string
	Domain      string
	AgentID     string
	WindowStart time.Time
	WindowEnd   time.Time

	TotalUsages    int
	SuccessCount   int
	FailureCount   int
	TotalCostUSD   float64
	TotalLatencyMS int64
	ErrorTypes     map[string]int
	LLMProvider    string
	LLMModel       string

	// Judge evaluation metrics (optional)
	JudgeEvaluationsCount int                // Number of judge evaluations
	JudgePassCount        int                // Number that passed judges
	JudgeTotalScore       float64            // Sum of all judge scores
	JudgeCriterionScores  map[string]float64 // Sum of scores per criterion (safety, cost, etc.)
	JudgeCriterionCounts  map[string]int     // Count per criterion (for averaging)
}

// NewPatternEffectivenessTracker creates a new pattern effectiveness tracker.
//
// Parameters:
//   - db: SQLite database with pattern_effectiveness table (see schema.go)
//   - tracer: Observability tracer for instrumentation
//   - bus: MessageBus for publishing aggregated metrics (optional, can be nil)
//   - windowSize: Aggregation window (e.g., 1 hour)
//   - flushInterval: How often to batch-write to DB (e.g., 5 minutes)
func NewPatternEffectivenessTracker(
	db *sql.DB,
	tracer observability.Tracer,
	bus *communication.MessageBus,
	windowSize time.Duration,
	flushInterval time.Duration,
) *PatternEffectivenessTracker {
	if windowSize == 0 {
		windowSize = 1 * time.Hour
	}
	if flushInterval == 0 {
		flushInterval = 5 * time.Minute
	}

	return &PatternEffectivenessTracker{
		db:            db,
		tracer:        tracer,
		bus:           bus,
		windowSize:    windowSize,
		flushInterval: flushInterval,
		buffer:        make(map[string]*patternStats),
		stopChan:      make(chan struct{}),
	}
}

// Start begins the background goroutine for periodic flushing.
// Safe to call multiple times (subsequent calls are no-ops).
func (t *PatternEffectivenessTracker) Start(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.started {
		return nil // Already started
	}

	_, span := t.tracer.StartSpan(ctx, "metaagent.learning.pattern_tracker.start")
	defer t.tracer.EndSpan(span)

	t.wg.Add(1)
	go t.flushLoop()

	t.started = true
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Pattern tracker started"}
	return nil
}

// Stop gracefully stops the background goroutine and flushes remaining data.
// Blocks until all pending data is flushed to database.
func (t *PatternEffectivenessTracker) Stop(ctx context.Context) error {
	t.mu.Lock()
	if !t.started {
		t.mu.Unlock()
		return nil // Not started, nothing to stop
	}
	// Mark as stopped before releasing lock to prevent double-stop
	t.started = false
	t.mu.Unlock()

	ctx, span := t.tracer.StartSpan(ctx, "metaagent.learning.pattern_tracker.stop")
	defer t.tracer.EndSpan(span)

	// Signal stop
	close(t.stopChan)

	// Wait for background goroutine to finish
	t.wg.Wait()

	// Final flush
	if err := t.flush(ctx); err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to flush on stop: %w", err)
	}

	span.Status = observability.Status{Code: observability.StatusOK, Message: "Pattern tracker stopped"}
	return nil
}

// RecordUsage records a single pattern usage event.
// Variant is extracted from context or defaults to "default".
// judgeResult is optional - if provided, judge evaluation metrics will be tracked.
//
// Thread-safe for concurrent calls.
func (t *PatternEffectivenessTracker) RecordUsage(
	ctx context.Context,
	patternName string,
	variant string,
	domain string,
	agentID string,
	success bool,
	costUSD float64,
	latency time.Duration,
	errorType string,
	llmProvider string,
	llmModel string,
	judgeResult *loomv1.EvaluateResponse, // Optional: multi-judge evaluation result
) {
	_, span := t.tracer.StartSpan(ctx, "metaagent.learning.pattern_tracker.record_usage")
	defer t.tracer.EndSpan(span)

	// Normalize variant
	if variant == "" {
		variant = "default"
	}

	// Calculate time window
	now := time.Now()
	windowStart := now.Truncate(t.windowSize)
	windowEnd := windowStart.Add(t.windowSize)

	// Generate buffer key: pattern:variant:agent:window_start
	key := fmt.Sprintf("%s:%s:%s:%d", patternName, variant, agentID, windowStart.Unix())

	// Update in-memory buffer
	t.bufferMu.Lock()
	defer t.bufferMu.Unlock()

	stats, exists := t.buffer[key]
	if !exists {
		stats = &patternStats{
			PatternName:          patternName,
			Variant:              variant,
			Domain:               domain,
			AgentID:              agentID,
			WindowStart:          windowStart,
			WindowEnd:            windowEnd,
			ErrorTypes:           make(map[string]int),
			LLMProvider:          llmProvider,
			LLMModel:             llmModel,
			JudgeCriterionScores: make(map[string]float64),
			JudgeCriterionCounts: make(map[string]int),
		}
		t.buffer[key] = stats
	}

	// Aggregate metrics
	stats.TotalUsages++
	if success {
		stats.SuccessCount++
	} else {
		stats.FailureCount++
		if errorType != "" {
			stats.ErrorTypes[errorType]++
		}
	}
	stats.TotalCostUSD += costUSD
	stats.TotalLatencyMS += latency.Milliseconds()

	// Aggregate judge evaluation metrics if provided
	if judgeResult != nil {
		stats.JudgeEvaluationsCount++
		if judgeResult.Passed {
			stats.JudgePassCount++
		}
		stats.JudgeTotalScore += judgeResult.FinalScore

		// Aggregate dimension scores
		for dimension, score := range judgeResult.DimensionScores {
			stats.JudgeCriterionScores[dimension] += score
			stats.JudgeCriterionCounts[dimension]++
		}
	}

	// Update span attributes
	span.SetAttribute("pattern_name", patternName)
	span.SetAttribute("variant", variant)
	span.SetAttribute("domain", domain)
	span.SetAttribute("agent_id", agentID)
	span.SetAttribute("success", success)
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Usage recorded"}

	t.tracer.RecordMetric("metaagent.pattern_tracker.usage_recorded", 1.0, map[string]string{
		"pattern": patternName,
		"variant": variant,
		"success": fmt.Sprintf("%t", success),
	})
}

// flushLoop runs in background, periodically flushing buffered metrics.
func (t *PatternEffectivenessTracker) flushLoop() {
	defer t.wg.Done()

	ticker := time.NewTicker(t.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ctx := context.Background()
			if err := t.flush(ctx); err != nil {
				errCtx, span := t.tracer.StartSpan(ctx, "metaagent.learning.pattern_tracker.flush_error")
				_ = errCtx // Context returned from tracing
				span.RecordError(err)
				span.Status = observability.Status{Code: observability.StatusError, Message: err.Error()}
				t.tracer.EndSpan(span)
			}
		case <-t.stopChan:
			return
		}
	}
}

// flush writes buffered metrics to database and publishes to MessageBus.
// Clears buffer after successful flush.
func (t *PatternEffectivenessTracker) flush(ctx context.Context) error {
	ctx, span := t.tracer.StartSpan(ctx, "metaagent.learning.pattern_tracker.flush")
	defer t.tracer.EndSpan(span)

	// Snapshot and clear buffer
	t.bufferMu.Lock()
	snapshot := t.buffer
	t.buffer = make(map[string]*patternStats)
	t.bufferMu.Unlock()

	if len(snapshot) == 0 {
		span.Status = observability.Status{Code: observability.StatusOK, Message: "No data to flush"}
		return nil
	}

	// Begin transaction for batch insert
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback() // Rollback is safe to call even after commit
	}()

	// Prepare statement
	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO pattern_effectiveness (
			pattern_name, variant, domain, agent_id,
			window_start, window_end,
			total_usages, success_count, failure_count, success_rate,
			avg_cost_usd, avg_latency_ms, error_types_json,
			judge_pass_rate, judge_avg_score, judge_criterion_scores_json,
			llm_provider, llm_model
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	// Insert all buffered stats
	recordsWritten := 0
	for _, stats := range snapshot {
		// Calculate success rate
		successRate := 0.0
		if stats.TotalUsages > 0 {
			successRate = float64(stats.SuccessCount) / float64(stats.TotalUsages)
		}

		// Calculate averages
		avgCost := 0.0
		if stats.TotalUsages > 0 {
			avgCost = stats.TotalCostUSD / float64(stats.TotalUsages)
		}

		avgLatency := int64(0)
		if stats.TotalUsages > 0 {
			avgLatency = stats.TotalLatencyMS / int64(stats.TotalUsages)
		}

		// Serialize error types to JSON
		errorTypesJSON, err := json.Marshal(stats.ErrorTypes)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to marshal error types: %w", err)
		}

		// Calculate judge metrics
		var judgePassRate *float64
		var judgeAvgScore *float64
		var judgeCriterionScoresJSON *string

		if stats.JudgeEvaluationsCount > 0 {
			// Judge pass rate
			passRate := float64(stats.JudgePassCount) / float64(stats.JudgeEvaluationsCount)
			judgePassRate = &passRate

			// Judge average score
			avgScore := stats.JudgeTotalScore / float64(stats.JudgeEvaluationsCount)
			judgeAvgScore = &avgScore

			// Average criterion scores
			avgCriterionScores := make(map[string]float64)
			for criterion, sum := range stats.JudgeCriterionScores {
				count := stats.JudgeCriterionCounts[criterion]
				if count > 0 {
					avgCriterionScores[criterion] = sum / float64(count)
				}
			}

			// Serialize to JSON
			criterionJSON, err := json.Marshal(avgCriterionScores)
			if err != nil {
				span.RecordError(err)
				return fmt.Errorf("failed to marshal judge criterion scores: %w", err)
			}
			criterionJSONStr := string(criterionJSON)
			judgeCriterionScoresJSON = &criterionJSONStr
		}

		// Execute insert
		_, err = stmt.ExecContext(ctx,
			stats.PatternName,
			stats.Variant,
			stats.Domain,
			stats.AgentID,
			stats.WindowStart.Unix(),
			stats.WindowEnd.Unix(),
			stats.TotalUsages,
			stats.SuccessCount,
			stats.FailureCount,
			successRate,
			avgCost,
			avgLatency,
			string(errorTypesJSON),
			judgePassRate,
			judgeAvgScore,
			judgeCriterionScoresJSON,
			stats.LLMProvider,
			stats.LLMModel,
		)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to insert pattern effectiveness: %w", err)
		}

		recordsWritten++

		// Publish to MessageBus if available
		if t.bus != nil {
			if err := t.publishMetric(ctx, stats, successRate, avgCost, avgLatency); err != nil {
				// Log error but don't fail the flush
				span.RecordError(err)
			}
		}
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	span.SetAttribute("records_written", recordsWritten)
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Flush completed"}
	t.tracer.RecordMetric("metaagent.pattern_tracker.records_flushed", float64(recordsWritten), nil)

	return nil
}

// publishMetric publishes an aggregated metric to the MessageBus.
func (t *PatternEffectivenessTracker) publishMetric(
	ctx context.Context,
	stats *patternStats,
	successRate float64,
	avgCost float64,
	avgLatency int64,
) error {
	ctx, span := t.tracer.StartSpan(ctx, "metaagent.learning.pattern_tracker.publish_metric")
	defer t.tracer.EndSpan(span)

	// Build PatternMetric proto message
	metric := &loomv1.PatternMetric{
		PatternName:  stats.PatternName,
		Variant:      stats.Variant,
		Domain:       stats.Domain,
		TotalUsages:  int32(stats.TotalUsages),
		SuccessCount: int32(stats.SuccessCount),
		FailureCount: int32(stats.FailureCount),
		SuccessRate:  successRate,
		AvgCostUsd:   avgCost,
		AvgLatencyMs: avgLatency,
		ErrorTypes:   convertErrorTypes(stats.ErrorTypes),
		LlmProvider:  stats.LLMProvider,
		LlmModel:     stats.LLMModel,
	}

	// Serialize to JSON for payload
	metricJSON, err := json.Marshal(metric)
	if err != nil {
		return fmt.Errorf("failed to marshal metric: %w", err)
	}

	// Create BusMessage
	busMsg := &loomv1.BusMessage{
		Id:        uuid.New().String(),
		Topic:     "meta.pattern.effectiveness",
		FromAgent: "learning-agent",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{
				Value: metricJSON,
			},
		},
		Metadata: map[string]string{
			"pattern":      stats.PatternName,
			"variant":      stats.Variant,
			"domain":       stats.Domain,
			"agent_id":     stats.AgentID,
			"window_start": fmt.Sprintf("%d", stats.WindowStart.Unix()),
		},
		Timestamp: time.Now().UnixMilli(),
	}

	// Publish
	_, _, err = t.bus.Publish(ctx, "meta.pattern.effectiveness", busMsg)
	if err != nil {
		return fmt.Errorf("failed to publish to message bus: %w", err)
	}

	return nil
}

// convertErrorTypes converts Go map to proto map format.
func convertErrorTypes(errorTypes map[string]int) map[string]int32 {
	result := make(map[string]int32, len(errorTypes))
	for k, v := range errorTypes {
		result[k] = int32(v)
	}
	return result
}

// GetMessageBus returns the MessageBus instance used by this tracker.
// Returns nil if no MessageBus was configured.
func (t *PatternEffectivenessTracker) GetMessageBus() *communication.MessageBus {
	return t.bus
}

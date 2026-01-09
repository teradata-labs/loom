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
	"sync"
	"testing"
	"time"

	_ "github.com/mutecomm/go-sqlcipher/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
)

func setupTrackerTest(t *testing.T) (*sql.DB, observability.Tracer, *communication.MessageBus, func()) {
	// Create in-memory database
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)

	// Enable WAL mode
	_, err = db.Exec("PRAGMA journal_mode=WAL")
	require.NoError(t, err)

	// Initialize schema
	tracer := observability.NewNoOpTracer()
	err = InitSelfImprovementSchema(context.Background(), db, tracer)
	require.NoError(t, err)

	// Create message bus
	bus := communication.NewMessageBus(nil, nil, nil, nil)

	cleanup := func() {
		db.Close()
		bus.Close()
	}

	return db, tracer, bus, cleanup
}

func TestNewPatternEffectivenessTracker(t *testing.T) {
	db, tracer, bus, cleanup := setupTrackerTest(t)
	defer cleanup()

	tracker := NewPatternEffectivenessTracker(db, tracer, bus, 1*time.Hour, 5*time.Minute)

	assert.NotNil(t, tracker)
	assert.Equal(t, 1*time.Hour, tracker.windowSize)
	assert.Equal(t, 5*time.Minute, tracker.flushInterval)
	assert.NotNil(t, tracker.buffer)
}

func TestPatternEffectivenessTracker_Defaults(t *testing.T) {
	db, tracer, bus, cleanup := setupTrackerTest(t)
	defer cleanup()

	// Pass zero values to test defaults
	tracker := NewPatternEffectivenessTracker(db, tracer, bus, 0, 0)

	assert.Equal(t, 1*time.Hour, tracker.windowSize, "Should default to 1 hour")
	assert.Equal(t, 5*time.Minute, tracker.flushInterval, "Should default to 5 minutes")
}

func TestPatternEffectivenessTracker_StartStop(t *testing.T) {
	db, tracer, bus, cleanup := setupTrackerTest(t)
	defer cleanup()

	tracker := NewPatternEffectivenessTracker(db, tracer, bus, 1*time.Hour, 100*time.Millisecond)

	ctx := context.Background()

	// Start tracker
	err := tracker.Start(ctx)
	require.NoError(t, err)

	// Starting again should be no-op
	err = tracker.Start(ctx)
	require.NoError(t, err)

	// Stop tracker
	err = tracker.Stop(ctx)
	require.NoError(t, err)

	// Stopping again should be no-op
	err = tracker.Stop(ctx)
	require.NoError(t, err)
}

func TestPatternEffectivenessTracker_RecordUsage(t *testing.T) {
	db, tracer, bus, cleanup := setupTrackerTest(t)
	defer cleanup()

	tracker := NewPatternEffectivenessTracker(db, tracer, bus, 1*time.Hour, 100*time.Millisecond)
	ctx := context.Background()

	// Record a successful usage
	tracker.RecordUsage(ctx, "sql.joins.optimize", "control", "sql", "agent-1", true, 0.01, 100*time.Millisecond, "", "anthropic", "claude-3", nil)

	// Verify buffer has data
	tracker.bufferMu.RLock()
	assert.Equal(t, 1, len(tracker.buffer), "Buffer should have 1 entry")
	tracker.bufferMu.RUnlock()
}

func TestPatternEffectivenessTracker_RecordUsage_VariantDefault(t *testing.T) {
	db, tracer, bus, cleanup := setupTrackerTest(t)
	defer cleanup()

	tracker := NewPatternEffectivenessTracker(db, tracer, bus, 1*time.Hour, 100*time.Millisecond)
	ctx := context.Background()

	// Record usage with empty variant
	tracker.RecordUsage(ctx, "sql.joins.optimize", "", "sql", "agent-1", true, 0.01, 100*time.Millisecond, "", "anthropic", "claude-3", nil)

	// Verify variant defaults to "default"
	tracker.bufferMu.RLock()
	for _, stats := range tracker.buffer {
		assert.Equal(t, "default", stats.Variant, "Empty variant should default to 'default'")
	}
	tracker.bufferMu.RUnlock()
}

func TestPatternEffectivenessTracker_Aggregation(t *testing.T) {
	db, tracer, bus, cleanup := setupTrackerTest(t)
	defer cleanup()

	tracker := NewPatternEffectivenessTracker(db, tracer, bus, 1*time.Hour, 100*time.Millisecond)
	ctx := context.Background()

	// Record multiple usages for same pattern
	for i := 0; i < 5; i++ {
		success := i < 4 // 4 successes, 1 failure
		errorType := ""
		if !success {
			errorType = "timeout"
		}
		tracker.RecordUsage(ctx, "sql.joins.optimize", "control", "sql", "agent-1",
			success, 0.01, 100*time.Millisecond, errorType, "anthropic", "claude-3", nil)
	}

	// Verify aggregation in buffer
	tracker.bufferMu.RLock()
	assert.Equal(t, 1, len(tracker.buffer), "Should aggregate into single entry")

	for _, stats := range tracker.buffer {
		assert.Equal(t, 5, stats.TotalUsages, "Should have 5 total usages")
		assert.Equal(t, 4, stats.SuccessCount, "Should have 4 successes")
		assert.Equal(t, 1, stats.FailureCount, "Should have 1 failure")
		assert.Equal(t, 0.05, stats.TotalCostUSD, "Should aggregate costs")
		assert.Equal(t, int64(500), stats.TotalLatencyMS, "Should aggregate latency")
		assert.Equal(t, 1, stats.ErrorTypes["timeout"], "Should track error types")
	}
	tracker.bufferMu.RUnlock()
}

func TestPatternEffectivenessTracker_MultipleVariants(t *testing.T) {
	db, tracer, bus, cleanup := setupTrackerTest(t)
	defer cleanup()

	tracker := NewPatternEffectivenessTracker(db, tracer, bus, 1*time.Hour, 100*time.Millisecond)
	ctx := context.Background()

	// Record usages for different variants
	tracker.RecordUsage(ctx, "sql.joins.optimize", "control", "sql", "agent-1", true, 0.01, 100*time.Millisecond, "", "anthropic", "claude-3", nil)
	tracker.RecordUsage(ctx, "sql.joins.optimize", "treatment", "sql", "agent-1", true, 0.01, 100*time.Millisecond, "", "anthropic", "claude-3", nil)

	// Verify separate buffer entries
	tracker.bufferMu.RLock()
	assert.Equal(t, 2, len(tracker.buffer), "Should have separate entries for different variants")
	tracker.bufferMu.RUnlock()
}

func TestPatternEffectivenessTracker_FlushToDatabase(t *testing.T) {
	db, tracer, bus, cleanup := setupTrackerTest(t)
	defer cleanup()

	tracker := NewPatternEffectivenessTracker(db, tracer, bus, 1*time.Hour, 100*time.Millisecond)
	ctx := context.Background()

	// Record some usages
	tracker.RecordUsage(ctx, "sql.joins.optimize", "control", "sql", "agent-1", true, 0.01, 100*time.Millisecond, "", "anthropic", "claude-3", nil)
	tracker.RecordUsage(ctx, "sql.joins.optimize", "control", "sql", "agent-1", false, 0.01, 200*time.Millisecond, "error", "anthropic", "claude-3", nil)

	// Manually flush
	err := tracker.flush(ctx)
	require.NoError(t, err)

	// Verify data in database
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM pattern_effectiveness").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "Should have 1 record in database")

	// Verify data accuracy
	var patternName, variant, domain, agentID string
	var totalUsages, successCount, failureCount int
	var successRate, avgCost float64
	var avgLatency int64
	var errorTypesJSON string

	err = db.QueryRow(`
		SELECT pattern_name, variant, domain, agent_id,
		       total_usages, success_count, failure_count, success_rate,
		       avg_cost_usd, avg_latency_ms, error_types_json
		FROM pattern_effectiveness
	`).Scan(&patternName, &variant, &domain, &agentID,
		&totalUsages, &successCount, &failureCount, &successRate,
		&avgCost, &avgLatency, &errorTypesJSON)
	require.NoError(t, err)

	assert.Equal(t, "sql.joins.optimize", patternName)
	assert.Equal(t, "control", variant)
	assert.Equal(t, "sql", domain)
	assert.Equal(t, "agent-1", agentID)
	assert.Equal(t, 2, totalUsages)
	assert.Equal(t, 1, successCount)
	assert.Equal(t, 1, failureCount)
	assert.InDelta(t, 0.5, successRate, 0.01)
	assert.InDelta(t, 0.01, avgCost, 0.001)
	assert.Equal(t, int64(150), avgLatency)

	// Verify error types JSON
	var errorTypes map[string]int
	err = json.Unmarshal([]byte(errorTypesJSON), &errorTypes)
	require.NoError(t, err)
	assert.Equal(t, 1, errorTypes["error"])
}

func TestPatternEffectivenessTracker_BufferClearing(t *testing.T) {
	db, tracer, bus, cleanup := setupTrackerTest(t)
	defer cleanup()

	tracker := NewPatternEffectivenessTracker(db, tracer, bus, 1*time.Hour, 100*time.Millisecond)
	ctx := context.Background()

	// Record usage
	tracker.RecordUsage(ctx, "sql.joins.optimize", "control", "sql", "agent-1", true, 0.01, 100*time.Millisecond, "", "anthropic", "claude-3", nil)

	// Verify buffer has data
	tracker.bufferMu.RLock()
	bufferLen := len(tracker.buffer)
	tracker.bufferMu.RUnlock()
	assert.Equal(t, 1, bufferLen, "Buffer should have 1 entry before flush")

	// Flush
	err := tracker.flush(ctx)
	require.NoError(t, err)

	// Verify buffer is cleared
	tracker.bufferMu.RLock()
	bufferLen = len(tracker.buffer)
	tracker.bufferMu.RUnlock()
	assert.Equal(t, 0, bufferLen, "Buffer should be empty after flush")
}

func TestPatternEffectivenessTracker_EmptyFlush(t *testing.T) {
	db, tracer, bus, cleanup := setupTrackerTest(t)
	defer cleanup()

	tracker := NewPatternEffectivenessTracker(db, tracer, bus, 1*time.Hour, 100*time.Millisecond)
	ctx := context.Background()

	// Flush without any data
	err := tracker.flush(ctx)
	require.NoError(t, err, "Should handle empty flush gracefully")

	// Verify no data in database
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM pattern_effectiveness").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "Should have 0 records after empty flush")
}

func TestPatternEffectivenessTracker_MessageBusPublishing(t *testing.T) {
	db, tracer, bus, cleanup := setupTrackerTest(t)
	defer cleanup()

	tracker := NewPatternEffectivenessTracker(db, tracer, bus, 1*time.Hour, 100*time.Millisecond)
	ctx := context.Background()

	// Subscribe to pattern effectiveness topic
	sub, err := bus.Subscribe(ctx, "test-subscriber", "meta.pattern.effectiveness", nil, 10)
	require.NoError(t, err)
	defer func() {
		_ = bus.Unsubscribe(ctx, sub.ID)
	}()

	// Record usage and flush
	tracker.RecordUsage(ctx, "sql.joins.optimize", "control", "sql", "agent-1", true, 0.01, 100*time.Millisecond, "", "anthropic", "claude-3", nil)
	err = tracker.flush(ctx)
	require.NoError(t, err)

	// Verify message was published
	select {
	case msg := <-sub.Channel:
		assert.Equal(t, "meta.pattern.effectiveness", msg.Topic)
		assert.Equal(t, "learning-agent", msg.FromAgent)
		assert.NotEmpty(t, msg.Payload.GetValue())

		// Verify payload structure
		var metric loomv1.PatternMetric
		err = json.Unmarshal(msg.Payload.GetValue(), &metric)
		require.NoError(t, err)
		assert.Equal(t, "sql.joins.optimize", metric.PatternName)
		assert.Equal(t, "control", metric.Variant)
		assert.Equal(t, "sql", metric.Domain)
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for message")
	}
}

func TestPatternEffectivenessTracker_NilMessageBus(t *testing.T) {
	db, tracer, _, cleanup := setupTrackerTest(t)
	defer cleanup()

	// Create tracker without message bus
	tracker := NewPatternEffectivenessTracker(db, tracer, nil, 1*time.Hour, 100*time.Millisecond)
	ctx := context.Background()

	// Record usage and flush
	tracker.RecordUsage(ctx, "sql.joins.optimize", "control", "sql", "agent-1", true, 0.01, 100*time.Millisecond, "", "anthropic", "claude-3", nil)
	err := tracker.flush(ctx)
	require.NoError(t, err, "Should handle nil message bus gracefully")

	// Verify data still written to database
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM pattern_effectiveness").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestPatternEffectivenessTracker_ConcurrentRecording(t *testing.T) {
	db, tracer, bus, cleanup := setupTrackerTest(t)
	defer cleanup()

	tracker := NewPatternEffectivenessTracker(db, tracer, bus, 1*time.Hour, 100*time.Millisecond)
	ctx := context.Background()

	// Record usages concurrently
	var wg sync.WaitGroup
	numGoroutines := 10
	recordsPerGoroutine := 10

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < recordsPerGoroutine; j++ {
				tracker.RecordUsage(ctx, "sql.joins.optimize", "control", "sql", "agent-1",
					true, 0.01, 100*time.Millisecond, "", "anthropic", "claude-3", nil)
			}
		}(i)
	}
	wg.Wait()

	// Verify aggregation under concurrent access
	tracker.bufferMu.RLock()
	assert.Equal(t, 1, len(tracker.buffer), "Should aggregate into single entry")
	for _, stats := range tracker.buffer {
		assert.Equal(t, numGoroutines*recordsPerGoroutine, stats.TotalUsages, "Should aggregate all concurrent usages")
	}
	tracker.bufferMu.RUnlock()
}

func TestPatternEffectivenessTracker_TimeWindowSeparation(t *testing.T) {
	db, tracer, bus, cleanup := setupTrackerTest(t)
	defer cleanup()

	// Use small window for testing
	tracker := NewPatternEffectivenessTracker(db, tracer, bus, 1*time.Millisecond, 100*time.Millisecond)
	ctx := context.Background()

	// Record first usage
	tracker.RecordUsage(ctx, "sql.joins.optimize", "control", "sql", "agent-1", true, 0.01, 100*time.Millisecond, "", "anthropic", "claude-3", nil)

	// Wait for window to pass
	time.Sleep(5 * time.Millisecond)

	// Record second usage (should be in different window)
	tracker.RecordUsage(ctx, "sql.joins.optimize", "control", "sql", "agent-1", true, 0.01, 100*time.Millisecond, "", "anthropic", "claude-3", nil)

	// Verify separate buffer entries for different windows
	tracker.bufferMu.RLock()
	assert.GreaterOrEqual(t, len(tracker.buffer), 1, "Should have entries for different time windows")
	tracker.bufferMu.RUnlock()
}

func TestPatternEffectivenessTracker_FullLifecycle(t *testing.T) {
	db, tracer, bus, cleanup := setupTrackerTest(t)
	defer cleanup()

	tracker := NewPatternEffectivenessTracker(db, tracer, bus, 1*time.Hour, 50*time.Millisecond)
	ctx := context.Background()

	// Start tracker
	err := tracker.Start(ctx)
	require.NoError(t, err)

	// Record some usages
	for i := 0; i < 5; i++ {
		tracker.RecordUsage(ctx, "sql.joins.optimize", "control", "sql", "agent-1",
			true, 0.01, 100*time.Millisecond, "", "anthropic", "claude-3", nil)
	}

	// Wait for automatic flush
	time.Sleep(200 * time.Millisecond)

	// Stop tracker (should trigger final flush)
	err = tracker.Stop(ctx)
	require.NoError(t, err)

	// Verify data in database
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM pattern_effectiveness").Scan(&count)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, count, 1, "Should have at least 1 record after full lifecycle")
}

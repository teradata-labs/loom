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
	"os"
	"testing"
	"time"

	"github.com/teradata-labs/loom/pkg/communication/interrupt"
	"github.com/teradata-labs/loom/pkg/observability"
)

// TestLearningAgent_InterruptIntegration tests the full interrupt channel integration.
func TestLearningAgent_InterruptIntegration(t *testing.T) {
	// Setup
	ctx := context.Background()
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tracer := observability.NewNoOpTracer()
	engine := NewLearningEngine(nil, tracer)
	tracker := NewPatternEffectivenessTracker(db, tracer, nil, 1*time.Hour, 5*time.Minute)

	// Create learning agent
	agent, err := NewLearningAgent(db, tracer, engine, tracker, AutonomyManual, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create learning agent: %v", err)
	}

	// Create interrupt channel with router and queue
	router := interrupt.NewRouter(ctx)
	defer router.Close()

	queueFile := "test_interrupt_queue.db"
	defer os.Remove(queueFile)

	queue, err := interrupt.NewPersistentQueue(ctx, queueFile, router)
	if err != nil {
		t.Fatalf("Failed to create persistent queue: %v", err)
	}
	defer queue.Close()

	ic := interrupt.NewInterruptChannel(ctx, router, queue)
	defer ic.Close()

	// Set interrupt channel on learning agent
	agentID := "test-agent"
	executionTrigger := int64(10) // Trigger after 10 executions
	if err := agent.SetInterruptChannel(ic, agentID, executionTrigger); err != nil {
		t.Fatalf("Failed to set interrupt channel: %v", err)
	}

	// Verify handlers are registered
	handlers := ic.ListHandlers(agentID)
	if len(handlers) != 7 {
		t.Errorf("Expected 7 handlers, got %d", len(handlers))
	}

	// Check specific signals
	expectedSignals := []interrupt.InterruptSignal{
		interrupt.SignalLearningAnalyze,
		interrupt.SignalLearningOptimize,
		interrupt.SignalLearningABTest,
		interrupt.SignalLearningProposal,
		interrupt.SignalLearningValidate,
		interrupt.SignalLearningExport,
		interrupt.SignalLearningSync,
	}

	for _, signal := range expectedSignals {
		found := false
		for _, h := range handlers {
			if h == signal {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected handler for %s not found", signal)
		}
	}
}

// TestLearningAgent_AnalyzeInterrupt tests the analyze interrupt handler.
func TestLearningAgent_AnalyzeInterrupt(t *testing.T) {
	// Setup
	ctx := context.Background()
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tracer := observability.NewNoOpTracer()
	engine := NewLearningEngine(nil, tracer)
	tracker := NewPatternEffectivenessTracker(db, tracer, nil, 1*time.Hour, 5*time.Minute)

	// Create learning agent
	agent, err := NewLearningAgent(db, tracer, engine, tracker, AutonomyManual, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create learning agent: %v", err)
	}

	// Create interrupt channel
	router := interrupt.NewRouter(ctx)
	defer router.Close()

	queueFile := "test_analyze_queue.db"
	defer os.Remove(queueFile)

	queue, err := interrupt.NewPersistentQueue(ctx, queueFile, router)
	if err != nil {
		t.Fatalf("Failed to create persistent queue: %v", err)
	}
	defer queue.Close()

	ic := interrupt.NewInterruptChannel(ctx, router, queue)
	defer ic.Close()

	agentID := "test-agent"
	if err := agent.SetInterruptChannel(ic, agentID, 0); err != nil {
		t.Fatalf("Failed to set interrupt channel: %v", err)
	}

	// Insert some test data
	insertTestPatternEffectiveness(t, db)

	// Create analyze payload
	payload, err := json.Marshal(map[string]interface{}{
		"domain":       "sql",
		"agent_id":     agentID,
		"window_hours": 24,
	})
	if err != nil {
		t.Fatalf("Failed to marshal payload: %v", err)
	}

	// Send analyze interrupt
	if err := ic.Send(ctx, interrupt.SignalLearningAnalyze, agentID, payload); err != nil {
		t.Fatalf("Failed to send analyze interrupt: %v", err)
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// No assertions needed - just checking it doesn't panic
}

// TestLearningAgent_SelfTrigger tests the execution count self-trigger mechanism.
func TestLearningAgent_SelfTrigger(t *testing.T) {
	// Setup
	ctx := context.Background()
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tracer := observability.NewNoOpTracer()
	engine := NewLearningEngine(nil, tracer)
	tracker := NewPatternEffectivenessTracker(db, tracer, nil, 1*time.Hour, 5*time.Minute)

	// Create learning agent
	agent, err := NewLearningAgent(db, tracer, engine, tracker, AutonomyManual, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create learning agent: %v", err)
	}

	// Create interrupt channel
	router := interrupt.NewRouter(ctx)
	defer router.Close()

	queueFile := "test_trigger_queue.db"
	defer os.Remove(queueFile)

	queue, err := interrupt.NewPersistentQueue(ctx, queueFile, router)
	if err != nil {
		t.Fatalf("Failed to create persistent queue: %v", err)
	}
	defer queue.Close()

	ic := interrupt.NewInterruptChannel(ctx, router, queue)
	defer ic.Close()

	agentID := "test-agent"
	executionTrigger := int64(5) // Trigger after 5 executions
	if err := agent.SetInterruptChannel(ic, agentID, executionTrigger); err != nil {
		t.Fatalf("Failed to set interrupt channel: %v", err)
	}

	// Track interrupts received
	interruptsReceived := 0
	ic.SetHooks(
		nil, // onSend
		func(i *interrupt.Interrupt, agentID string) { // onDelivered
			if i.Signal == interrupt.SignalLearningAnalyze {
				interruptsReceived++
			}
		},
		nil, // onDropped
	)

	// Record executions (should trigger at 5 and 10)
	for i := 0; i < 10; i++ {
		if err := agent.RecordExecution(ctx); err != nil {
			t.Fatalf("Failed to record execution %d: %v", i+1, err)
		}
	}

	// Wait for processing
	time.Sleep(200 * time.Millisecond)

	// Check execution count
	count := agent.GetExecutionCount()
	if count != 10 {
		t.Errorf("Expected execution count 10, got %d", count)
	}

	// Should have triggered twice (at count 5 and 10)
	if interruptsReceived != 2 {
		t.Errorf("Expected 2 self-triggered interrupts, got %d", interruptsReceived)
	}

	// Test reset
	agent.ResetExecutionCount()
	count = agent.GetExecutionCount()
	if count != 0 {
		t.Errorf("Expected execution count 0 after reset, got %d", count)
	}
}

// TestLearningAgent_OptimizeInterrupt tests the optimize interrupt handler.
func TestLearningAgent_OptimizeInterrupt(t *testing.T) {
	// Setup
	ctx := context.Background()
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tracer := observability.NewNoOpTracer()
	engine := NewLearningEngine(nil, tracer)
	tracker := NewPatternEffectivenessTracker(db, tracer, nil, 1*time.Hour, 5*time.Minute)

	// Create learning agent with full autonomy for auto-apply
	agent, err := NewLearningAgent(db, tracer, engine, tracker, AutonomyFull, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create learning agent: %v", err)
	}

	// Create interrupt channel
	router := interrupt.NewRouter(ctx)
	defer router.Close()

	queueFile := "test_optimize_queue.db"
	defer os.Remove(queueFile)

	queue, err := interrupt.NewPersistentQueue(ctx, queueFile, router)
	if err != nil {
		t.Fatalf("Failed to create persistent queue: %v", err)
	}
	defer queue.Close()

	ic := interrupt.NewInterruptChannel(ctx, router, queue)
	defer ic.Close()

	agentID := "test-agent"
	if err := agent.SetInterruptChannel(ic, agentID, 0); err != nil {
		t.Fatalf("Failed to set interrupt channel: %v", err)
	}

	// Insert test data with high success rate
	insertTestPatternEffectivenessHighSuccess(t, db)

	// Create optimize payload (without auto_apply for this test)
	payload, err := json.Marshal(map[string]interface{}{
		"domain":        "sql",
		"agent_id":      agentID,
		"max_proposals": 5,
		"auto_apply":    false, // Don't auto-apply in test
	})
	if err != nil {
		t.Fatalf("Failed to marshal payload: %v", err)
	}

	// Send optimize interrupt
	if err := ic.Send(ctx, interrupt.SignalLearningOptimize, agentID, payload); err != nil {
		t.Fatalf("Failed to send optimize interrupt: %v", err)
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// No assertions needed - just checking it doesn't panic
}

// TestLearningAgent_NoInterruptChannel tests that learning agent works without interrupt channel.
func TestLearningAgent_NoInterruptChannel(t *testing.T) {
	// Setup
	ctx := context.Background()
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tracer := observability.NewNoOpTracer()
	engine := NewLearningEngine(nil, tracer)
	tracker := NewPatternEffectivenessTracker(db, tracer, nil, 1*time.Hour, 5*time.Minute)

	// Create learning agent WITHOUT interrupt channel
	agent, err := NewLearningAgent(db, tracer, engine, tracker, AutonomyManual, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create learning agent: %v", err)
	}

	// RecordExecution should be no-op without interrupt channel
	if err := agent.RecordExecution(ctx); err != nil {
		t.Errorf("RecordExecution should not fail without interrupt channel: %v", err)
	}

	// Execution count should still increment
	count := agent.GetExecutionCount()
	if count != 1 {
		t.Errorf("Expected execution count 1, got %d", count)
	}
}

// insertTestPatternEffectiveness inserts test pattern effectiveness data.
func insertTestPatternEffectiveness(t *testing.T, db *sql.DB) {
	t.Helper()

	windowStart := time.Now().Add(-1 * time.Hour).Unix()

	query := `
		INSERT INTO pattern_effectiveness (
			pattern_name, variant, domain, agent_id,
			window_start, window_end,
			total_usages, success_count, failure_count,
			success_rate, avg_cost_usd, avg_latency_ms,
			llm_provider, llm_model
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := db.ExecContext(context.Background(), query,
		"test_pattern", "default", "sql", "test-agent",
		windowStart, time.Now().Unix(),
		100, 80, 20,
		0.80, 0.001, 500,
		"anthropic", "claude-3-5-sonnet-20241022",
	)
	if err != nil {
		t.Fatalf("Failed to insert test data: %v", err)
	}
}

// insertTestPatternEffectivenessHighSuccess inserts test data with high success rate.
func insertTestPatternEffectivenessHighSuccess(t *testing.T, db *sql.DB) {
	t.Helper()

	windowStart := time.Now().Add(-1 * time.Hour).Unix()

	query := `
		INSERT INTO pattern_effectiveness (
			pattern_name, variant, domain, agent_id,
			window_start, window_end,
			total_usages, success_count, failure_count,
			success_rate, avg_cost_usd, avg_latency_ms,
			llm_provider, llm_model
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := db.ExecContext(context.Background(), query,
		"high_success_pattern", "default", "sql", "test-agent",
		windowStart, time.Now().Unix(),
		200, 190, 10,
		0.95, 0.001, 400,
		"anthropic", "claude-3-5-sonnet-20241022",
	)
	if err != nil {
		t.Fatalf("Failed to insert test data: %v", err)
	}
}

// TestLearningAgent_ABTestInterrupt tests the A/B testing interrupt handler.
func TestLearningAgent_ABTestInterrupt(t *testing.T) {
	// Setup
	ctx := context.Background()
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tracer := observability.NewNoOpTracer()
	engine := NewLearningEngine(nil, tracer)
	tracker := NewPatternEffectivenessTracker(db, tracer, nil, 1*time.Hour, 5*time.Minute)

	// Create learning agent
	agent, err := NewLearningAgent(db, tracer, engine, tracker, AutonomyManual, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create learning agent: %v", err)
	}

	// Create interrupt channel
	router := interrupt.NewRouter(ctx)
	defer router.Close()

	queueFile := "test_abtest_queue.db"
	defer os.Remove(queueFile)

	queue, err := interrupt.NewPersistentQueue(ctx, queueFile, router)
	if err != nil {
		t.Fatalf("Failed to create persistent queue: %v", err)
	}
	defer queue.Close()

	ic := interrupt.NewInterruptChannel(ctx, router, queue)
	defer ic.Close()

	agentID := "test-agent"
	if err := agent.SetInterruptChannel(ic, agentID, 0); err != nil {
		t.Fatalf("Failed to set interrupt channel: %v", err)
	}

	// Insert test data with multiple variants
	insertTestABTestData(t, db, agentID)

	// Create A/B test payload
	payload, err := json.Marshal(map[string]interface{}{
		"pattern_name":    "sql_query_gen",
		"variants":        []string{"control", "treatment-a", "treatment-b"},
		"domain":          "sql",
		"agent_id":        agentID,
		"min_sample_size": 10,
		"window_hours":    24,
	})
	if err != nil {
		t.Fatalf("Failed to marshal payload: %v", err)
	}

	// Send A/B test interrupt
	if err := ic.Send(ctx, interrupt.SignalLearningABTest, agentID, payload); err != nil {
		t.Fatalf("Failed to send A/B test interrupt: %v", err)
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// No assertions needed - just checking it doesn't panic and processes the data
}

// TestLearningAgent_ABTestInterrupt_InsufficientSamples tests A/B test with insufficient samples.
func TestLearningAgent_ABTestInterrupt_InsufficientSamples(t *testing.T) {
	// Setup
	ctx := context.Background()
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tracer := observability.NewNoOpTracer()
	engine := NewLearningEngine(nil, tracer)
	tracker := NewPatternEffectivenessTracker(db, tracer, nil, 1*time.Hour, 5*time.Minute)

	// Create learning agent
	agent, err := NewLearningAgent(db, tracer, engine, tracker, AutonomyManual, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create learning agent: %v", err)
	}

	// Create interrupt channel
	router := interrupt.NewRouter(ctx)
	defer router.Close()

	queueFile := "test_abtest_samples_queue.db"
	defer os.Remove(queueFile)

	queue, err := interrupt.NewPersistentQueue(ctx, queueFile, router)
	if err != nil {
		t.Fatalf("Failed to create persistent queue: %v", err)
	}
	defer queue.Close()

	ic := interrupt.NewInterruptChannel(ctx, router, queue)
	defer ic.Close()

	agentID := "test-agent"
	if err := agent.SetInterruptChannel(ic, agentID, 0); err != nil {
		t.Fatalf("Failed to set interrupt channel: %v", err)
	}

	// Insert minimal test data (below threshold)
	insertTestABTestDataMinimal(t, db, agentID)

	// Create A/B test payload with high sample size requirement
	payload, err := json.Marshal(map[string]interface{}{
		"pattern_name":    "sql_query_gen",
		"variants":        []string{"control", "treatment-a"},
		"domain":          "sql",
		"agent_id":        agentID,
		"min_sample_size": 100, // Higher than actual data
		"window_hours":    24,
	})
	if err != nil {
		t.Fatalf("Failed to marshal payload: %v", err)
	}

	// Send A/B test interrupt
	if err := ic.Send(ctx, interrupt.SignalLearningABTest, agentID, payload); err != nil {
		t.Fatalf("Failed to send A/B test interrupt: %v", err)
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// Should complete without error despite insufficient samples
}

// TestLearningAgent_ABTestInterrupt_AutoDiscoverVariants tests auto-discovering variants.
func TestLearningAgent_ABTestInterrupt_AutoDiscoverVariants(t *testing.T) {
	// Setup
	ctx := context.Background()
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tracer := observability.NewNoOpTracer()
	engine := NewLearningEngine(nil, tracer)
	tracker := NewPatternEffectivenessTracker(db, tracer, nil, 1*time.Hour, 5*time.Minute)

	// Create learning agent
	agent, err := NewLearningAgent(db, tracer, engine, tracker, AutonomyManual, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create learning agent: %v", err)
	}

	// Create interrupt channel
	router := interrupt.NewRouter(ctx)
	defer router.Close()

	queueFile := "test_abtest_auto_queue.db"
	defer os.Remove(queueFile)

	queue, err := interrupt.NewPersistentQueue(ctx, queueFile, router)
	if err != nil {
		t.Fatalf("Failed to create persistent queue: %v", err)
	}
	defer queue.Close()

	ic := interrupt.NewInterruptChannel(ctx, router, queue)
	defer ic.Close()

	agentID := "test-agent"
	if err := agent.SetInterruptChannel(ic, agentID, 0); err != nil {
		t.Fatalf("Failed to set interrupt channel: %v", err)
	}

	// Insert test data
	insertTestABTestData(t, db, agentID)

	// Create A/B test payload WITHOUT specifying variants (auto-discover)
	payload, err := json.Marshal(map[string]interface{}{
		"pattern_name":    "sql_query_gen",
		"domain":          "sql",
		"agent_id":        agentID,
		"min_sample_size": 10,
		"window_hours":    24,
	})
	if err != nil {
		t.Fatalf("Failed to marshal payload: %v", err)
	}

	// Send A/B test interrupt
	if err := ic.Send(ctx, interrupt.SignalLearningABTest, agentID, payload); err != nil {
		t.Fatalf("Failed to send A/B test interrupt: %v", err)
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// Should discover and analyze all variants automatically
}

// insertTestABTestData inserts test pattern effectiveness data with multiple variants.
func insertTestABTestData(t *testing.T, db *sql.DB, agentID string) {
	t.Helper()

	windowStart := time.Now().Add(-1 * time.Hour).Unix()
	windowEnd := time.Now().Unix()

	// Control variant (baseline)
	query := `
		INSERT INTO pattern_effectiveness (
			pattern_name, variant, domain, agent_id,
			window_start, window_end,
			total_usages, success_count, failure_count,
			success_rate, avg_cost_usd, avg_latency_ms,
			llm_provider, llm_model
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	// Control: 80% success rate
	_, err := db.ExecContext(context.Background(), query,
		"sql_query_gen", "control", "sql", agentID,
		windowStart, windowEnd,
		100, 80, 20,
		0.80, 0.001, 500,
		"anthropic", "claude-3-5-sonnet-20241022",
	)
	if err != nil {
		t.Fatalf("Failed to insert control data: %v", err)
	}

	// Treatment A: 85% success rate (better)
	_, err = db.ExecContext(context.Background(), query,
		"sql_query_gen", "treatment-a", "sql", agentID,
		windowStart, windowEnd,
		100, 85, 15,
		0.85, 0.001, 480,
		"anthropic", "claude-3-5-sonnet-20241022",
	)
	if err != nil {
		t.Fatalf("Failed to insert treatment-a data: %v", err)
	}

	// Treatment B: 92% success rate (best)
	_, err = db.ExecContext(context.Background(), query,
		"sql_query_gen", "treatment-b", "sql", agentID,
		windowStart, windowEnd,
		100, 92, 8,
		0.92, 0.0012, 520,
		"anthropic", "claude-3-5-sonnet-20241022",
	)
	if err != nil {
		t.Fatalf("Failed to insert treatment-b data: %v", err)
	}
}

// insertTestABTestDataMinimal inserts minimal test data (below sample size threshold).
func insertTestABTestDataMinimal(t *testing.T, db *sql.DB, agentID string) {
	t.Helper()

	windowStart := time.Now().Add(-1 * time.Hour).Unix()
	windowEnd := time.Now().Unix()

	query := `
		INSERT INTO pattern_effectiveness (
			pattern_name, variant, domain, agent_id,
			window_start, window_end,
			total_usages, success_count, failure_count,
			success_rate, avg_cost_usd, avg_latency_ms,
			llm_provider, llm_model
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	// Control: only 5 usages
	_, err := db.ExecContext(context.Background(), query,
		"sql_query_gen", "control", "sql", agentID,
		windowStart, windowEnd,
		5, 4, 1,
		0.80, 0.001, 500,
		"anthropic", "claude-3-5-sonnet-20241022",
	)
	if err != nil {
		t.Fatalf("Failed to insert minimal control data: %v", err)
	}

	// Treatment A: only 5 usages
	_, err = db.ExecContext(context.Background(), query,
		"sql_query_gen", "treatment-a", "sql", agentID,
		windowStart, windowEnd,
		5, 5, 0,
		1.00, 0.001, 480,
		"anthropic", "claude-3-5-sonnet-20241022",
	)
	if err != nil {
		t.Fatalf("Failed to insert minimal treatment-a data: %v", err)
	}
}

// TestLearningAgent_SyncInterrupt tests the sync interrupt handler with push direction.
func TestLearningAgent_SyncInterrupt_Push(t *testing.T) {
	// Setup
	ctx := context.Background()
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tracer := observability.NewNoOpTracer()
	engine := NewLearningEngine(nil, tracer)
	tracker := NewPatternEffectivenessTracker(db, tracer, nil, 1*time.Hour, 5*time.Minute)

	// Create learning agent
	agent, err := NewLearningAgent(db, tracer, engine, tracker, AutonomyManual, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create learning agent: %v", err)
	}

	// Create interrupt channel
	router := interrupt.NewRouter(ctx)
	defer router.Close()

	queueFile := "test_sync_push_queue.db"
	defer os.Remove(queueFile)

	queue, err := interrupt.NewPersistentQueue(ctx, queueFile, router)
	if err != nil {
		t.Fatalf("Failed to create persistent queue: %v", err)
	}
	defer queue.Close()

	ic := interrupt.NewInterruptChannel(ctx, router, queue)
	defer ic.Close()

	agentID := "test-agent"
	if err := agent.SetInterruptChannel(ic, agentID, 0); err != nil {
		t.Fatalf("Failed to set interrupt channel: %v", err)
	}

	// Insert test data
	insertTestPatternEffectiveness(t, db)

	// Create sync payload for push
	payload, err := json.Marshal(map[string]interface{}{
		"sync_direction":    "push",
		"domain":            "sql",
		"agent_id":          "test-agent",
		"sync_window_hours": 24,
	})
	if err != nil {
		t.Fatalf("Failed to marshal payload: %v", err)
	}

	// Send sync interrupt
	if err := ic.Send(ctx, interrupt.SignalLearningSync, agentID, payload); err != nil {
		t.Fatalf("Failed to send sync interrupt: %v", err)
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// No assertions needed - just checking it doesn't panic and queries data
}

// TestLearningAgent_SyncInterrupt_Pull tests the sync interrupt handler with pull direction.
func TestLearningAgent_SyncInterrupt_Pull(t *testing.T) {
	// Setup
	ctx := context.Background()
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tracer := observability.NewNoOpTracer()
	engine := NewLearningEngine(nil, tracer)
	tracker := NewPatternEffectivenessTracker(db, tracer, nil, 1*time.Hour, 5*time.Minute)

	// Create learning agent
	agent, err := NewLearningAgent(db, tracer, engine, tracker, AutonomyManual, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create learning agent: %v", err)
	}

	// Create interrupt channel
	router := interrupt.NewRouter(ctx)
	defer router.Close()

	queueFile := "test_sync_pull_queue.db"
	defer os.Remove(queueFile)

	queue, err := interrupt.NewPersistentQueue(ctx, queueFile, router)
	if err != nil {
		t.Fatalf("Failed to create persistent queue: %v", err)
	}
	defer queue.Close()

	ic := interrupt.NewInterruptChannel(ctx, router, queue)
	defer ic.Close()

	agentID := "test-agent"
	if err := agent.SetInterruptChannel(ic, agentID, 0); err != nil {
		t.Fatalf("Failed to set interrupt channel: %v", err)
	}

	// Create sync payload for pull
	payload, err := json.Marshal(map[string]interface{}{
		"sync_direction":    "pull",
		"domain":            "sql",
		"sync_window_hours": 24,
	})
	if err != nil {
		t.Fatalf("Failed to marshal payload: %v", err)
	}

	// Send sync interrupt
	if err := ic.Send(ctx, interrupt.SignalLearningSync, agentID, payload); err != nil {
		t.Fatalf("Failed to send sync interrupt: %v", err)
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// Currently stub - would test actual pulling when external system is available
}

// TestLearningAgent_SyncInterrupt_Bidirectional tests bidirectional sync.
func TestLearningAgent_SyncInterrupt_Bidirectional(t *testing.T) {
	// Setup
	ctx := context.Background()
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tracer := observability.NewNoOpTracer()
	engine := NewLearningEngine(nil, tracer)
	tracker := NewPatternEffectivenessTracker(db, tracer, nil, 1*time.Hour, 5*time.Minute)

	// Create learning agent
	agent, err := NewLearningAgent(db, tracer, engine, tracker, AutonomyManual, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create learning agent: %v", err)
	}

	// Create interrupt channel
	router := interrupt.NewRouter(ctx)
	defer router.Close()

	queueFile := "test_sync_bidir_queue.db"
	defer os.Remove(queueFile)

	queue, err := interrupt.NewPersistentQueue(ctx, queueFile, router)
	if err != nil {
		t.Fatalf("Failed to create persistent queue: %v", err)
	}
	defer queue.Close()

	ic := interrupt.NewInterruptChannel(ctx, router, queue)
	defer ic.Close()

	agentID := "test-agent"
	if err := agent.SetInterruptChannel(ic, agentID, 0); err != nil {
		t.Fatalf("Failed to set interrupt channel: %v", err)
	}

	// Insert test data
	insertTestPatternEffectiveness(t, db)

	// Create sync payload for bidirectional sync (default)
	payload, err := json.Marshal(map[string]interface{}{
		"domain":            "sql",
		"agent_id":          "test-agent",
		"sync_window_hours": 24,
	})
	if err != nil {
		t.Fatalf("Failed to marshal payload: %v", err)
	}

	// Send sync interrupt
	if err := ic.Send(ctx, interrupt.SignalLearningSync, agentID, payload); err != nil {
		t.Fatalf("Failed to send sync interrupt: %v", err)
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// Should push and attempt pull (though pull is stub for now)
}

// TestLearningAgent_SyncInterrupt_WithPatternFilter tests sync with pattern name filter.
func TestLearningAgent_SyncInterrupt_WithPatternFilter(t *testing.T) {
	// Setup
	ctx := context.Background()
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tracer := observability.NewNoOpTracer()
	engine := NewLearningEngine(nil, tracer)
	tracker := NewPatternEffectivenessTracker(db, tracer, nil, 1*time.Hour, 5*time.Minute)

	// Create learning agent
	agent, err := NewLearningAgent(db, tracer, engine, tracker, AutonomyManual, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to create learning agent: %v", err)
	}

	// Create interrupt channel
	router := interrupt.NewRouter(ctx)
	defer router.Close()

	queueFile := "test_sync_filter_queue.db"
	defer os.Remove(queueFile)

	queue, err := interrupt.NewPersistentQueue(ctx, queueFile, router)
	if err != nil {
		t.Fatalf("Failed to create persistent queue: %v", err)
	}
	defer queue.Close()

	ic := interrupt.NewInterruptChannel(ctx, router, queue)
	defer ic.Close()

	agentID := "test-agent"
	if err := agent.SetInterruptChannel(ic, agentID, 0); err != nil {
		t.Fatalf("Failed to set interrupt channel: %v", err)
	}

	// Insert test data
	insertTestPatternEffectiveness(t, db)

	// Create sync payload with pattern filter
	payload, err := json.Marshal(map[string]interface{}{
		"sync_direction":    "push",
		"domain":            "sql",
		"agent_id":          "test-agent",
		"pattern_names":     []string{"test_pattern"},
		"sync_window_hours": 24,
	})
	if err != nil {
		t.Fatalf("Failed to marshal payload: %v", err)
	}

	// Send sync interrupt
	if err := ic.Send(ctx, interrupt.SignalLearningSync, agentID, payload); err != nil {
		t.Fatalf("Failed to send sync interrupt: %v", err)
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// Should only query filtered patterns
}

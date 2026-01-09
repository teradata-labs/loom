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
	"testing"
	"time"

	_ "github.com/mutecomm/go-sqlcipher/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

func setupTestLearningAgent(t *testing.T) (*LearningAgent, *sql.DB, func()) {
	// Create in-memory database
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)

	// Initialize schema
	err = InitSelfImprovementSchema(context.Background(), db, observability.NewNoOpTracer())
	require.NoError(t, err)

	// Create dependencies
	collector, err := NewMetricsCollector(":memory:", observability.NewNoOpTracer())
	require.NoError(t, err)

	engine := NewLearningEngine(collector, observability.NewNoOpTracer())
	tracker := NewPatternEffectivenessTracker(
		db,
		observability.NewNoOpTracer(),
		nil, // No message bus
		1*time.Hour,
		5*time.Minute,
	)

	// Create learning agent
	agent, err := NewLearningAgent(
		db,
		observability.NewNoOpTracer(),
		engine,
		tracker,
		AutonomyManual,
		1*time.Hour,
	)
	if err != nil {
		t.Fatalf("Failed to create learning agent: %v", err)
	}

	cleanup := func() {
		collector.Close()
		db.Close()
	}

	return agent, db, cleanup
}

func TestNewLearningAgent(t *testing.T) {
	agent, _, cleanup := setupTestLearningAgent(t)
	defer cleanup()

	assert.NotNil(t, agent)
	assert.NotNil(t, agent.db)
	assert.NotNil(t, agent.tracer)
	assert.NotNil(t, agent.engine)
	assert.NotNil(t, agent.tracker)
	assert.NotNil(t, agent.circuitBreaker)
	assert.Equal(t, AutonomyManual, agent.autonomyLevel)
	assert.Equal(t, 1*time.Hour, agent.analysisInterval)
	assert.False(t, agent.started)
}

func TestLearningAgent_StartStop(t *testing.T) {
	agent, _, cleanup := setupTestLearningAgent(t)
	defer cleanup()

	ctx := context.Background()

	// Start agent
	err := agent.Start(ctx)
	require.NoError(t, err)
	assert.True(t, agent.started)

	// Starting again should be no-op
	err = agent.Start(ctx)
	require.NoError(t, err)

	// Stop agent
	err = agent.Stop(ctx)
	require.NoError(t, err)
	assert.False(t, agent.started)

	// Stopping again should be no-op
	err = agent.Stop(ctx)
	require.NoError(t, err)
}

func TestLearningAgent_AnalyzePatternEffectiveness(t *testing.T) {
	agent, db, cleanup := setupTestLearningAgent(t)
	defer cleanup()

	ctx := context.Background()

	// Insert test data
	_, err := db.Exec(`
		INSERT INTO pattern_effectiveness
			(pattern_name, variant, domain, agent_id, window_start, window_end,
			 total_usages, success_count, failure_count, success_rate,
			 avg_cost_usd, avg_latency_ms, error_types_json, llm_provider, llm_model)
		VALUES
			('test.pattern.high', 'default', 'sql', 'agent-1', ?, ?, 10, 9, 1, 0.9, 0.001, 100, '{}', 'anthropic', 'claude'),
			('test.pattern.low', 'default', 'sql', 'agent-1', ?, ?, 10, 3, 7, 0.3, 0.002, 150, '{}', 'anthropic', 'claude')
	`, time.Now().Add(-1*time.Hour).Unix(), time.Now().Unix(),
		time.Now().Add(-1*time.Hour).Unix(), time.Now().Unix())
	require.NoError(t, err)

	// Analyze
	resp, err := agent.AnalyzePatternEffectiveness(ctx, &loomv1.AnalyzePatternEffectivenessRequest{
		Domain:      "sql",
		WindowHours: 24,
	})

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Len(t, resp.Patterns, 2)
	assert.NotNil(t, resp.Summary)
	assert.Equal(t, int32(2), resp.Summary.TotalPatternsAnalyzed)
}

func TestLearningAgent_GenerateImprovements(t *testing.T) {
	agent, db, cleanup := setupTestLearningAgent(t)
	defer cleanup()

	ctx := context.Background()

	// Insert test data with patterns that need improvement
	_, err := db.Exec(`
		INSERT INTO pattern_effectiveness
			(pattern_name, variant, domain, agent_id, window_start, window_end,
			 total_usages, success_count, failure_count, success_rate,
			 avg_cost_usd, avg_latency_ms, error_types_json, llm_provider, llm_model)
		VALUES
			('test.pattern.excellent', 'default', 'sql', 'agent-1', ?, ?, 20, 19, 1, 0.95, 0.001, 100, '{}', 'anthropic', 'claude'),
			('test.pattern.poor', 'default', 'sql', 'agent-1', ?, ?, 20, 6, 14, 0.3, 0.002, 150, '{}', 'anthropic', 'claude')
	`, time.Now().Add(-1*time.Hour).Unix(), time.Now().Unix(),
		time.Now().Add(-1*time.Hour).Unix(), time.Now().Unix())
	require.NoError(t, err)

	// Generate improvements
	resp, err := agent.GenerateImprovements(ctx, &loomv1.GenerateImprovementsRequest{
		Domain:       "sql",
		MaxProposals: 10,
	})

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Greater(t, len(resp.Improvements), 0)
	assert.Equal(t, int32(len(resp.Improvements)), resp.TotalProposed)

	// Verify improvements were stored in database
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM improvement_history").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, len(resp.Improvements), count)
}

func TestLearningAgent_ApplyImprovement_ManualMode(t *testing.T) {
	agent, _, cleanup := setupTestLearningAgent(t)
	defer cleanup()

	ctx := context.Background()

	// Create an improvement
	improvement := &loomv1.Improvement{
		Id:            "test-improvement-1",
		Type:          loomv1.ImprovementType_IMPROVEMENT_PATTERN_ADD,
		Description:   "Test improvement",
		Confidence:    0.8,
		Impact:        loomv1.ImpactLevel_IMPACT_LOW,
		TargetPattern: "test.pattern",
		Domain:        "sql",
		Status:        loomv1.ImprovementStatus_IMPROVEMENT_PENDING,
		CreatedAt:     time.Now().UnixMilli(),
	}

	err := agent.storeImprovement(ctx, improvement)
	require.NoError(t, err)

	// Try to apply without force (should be rejected)
	resp, err := agent.ApplyImprovement(ctx, &loomv1.ApplyImprovementRequest{
		ImprovementId: improvement.Id,
		Force:         false,
	})

	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "Manual approval required")

	// Apply with force (should succeed)
	resp, err = agent.ApplyImprovement(ctx, &loomv1.ApplyImprovementRequest{
		ImprovementId: improvement.Id,
		Force:         true,
	})

	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, loomv1.ImprovementStatus_IMPROVEMENT_APPLIED, resp.Improvement.Status)
}

func TestLearningAgent_ApplyImprovement_AutoMode(t *testing.T) {
	agent, _, cleanup := setupTestLearningAgent(t)
	defer cleanup()

	ctx := context.Background()

	// Set autonomy to full
	agent.autonomyLevel = AutonomyFull

	// Create an improvement
	improvement := &loomv1.Improvement{
		Id:            "test-improvement-2",
		Type:          loomv1.ImprovementType_IMPROVEMENT_PATTERN_ADD,
		Description:   "Test auto improvement",
		Confidence:    0.8,
		Impact:        loomv1.ImpactLevel_IMPACT_LOW,
		TargetPattern: "test.pattern",
		Domain:        "sql",
		Status:        loomv1.ImprovementStatus_IMPROVEMENT_PENDING,
		CreatedAt:     time.Now().UnixMilli(),
	}

	err := agent.storeImprovement(ctx, improvement)
	require.NoError(t, err)

	// Apply (should succeed automatically)
	resp, err := agent.ApplyImprovement(ctx, &loomv1.ApplyImprovementRequest{
		ImprovementId: improvement.Id,
	})

	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, loomv1.ImprovementStatus_IMPROVEMENT_APPLIED, resp.Improvement.Status)
}

func TestLearningAgent_RollbackImprovement(t *testing.T) {
	agent, _, cleanup := setupTestLearningAgent(t)
	defer cleanup()

	ctx := context.Background()

	// Create and apply an improvement
	improvement := &loomv1.Improvement{
		Id:            "test-improvement-3",
		Type:          loomv1.ImprovementType_IMPROVEMENT_PATTERN_ADD,
		Description:   "Test rollback",
		Confidence:    0.8,
		Impact:        loomv1.ImpactLevel_IMPACT_LOW,
		TargetPattern: "test.pattern",
		Domain:        "sql",
		Status:        loomv1.ImprovementStatus_IMPROVEMENT_APPLIED,
		CreatedAt:     time.Now().UnixMilli(),
		AppliedAt:     time.Now().UnixMilli(),
		AppliedBy:     "learning-agent",
	}

	err := agent.storeImprovement(ctx, improvement)
	require.NoError(t, err)

	// Rollback
	resp, err := agent.RollbackImprovement(ctx, &loomv1.RollbackImprovementRequest{
		ImprovementId: improvement.Id,
		Reason:        "Test rollback",
	})

	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.NotZero(t, resp.RollbackCompletedAt)

	// Verify status changed in database
	retrieved, err := agent.getImprovement(ctx, improvement.Id)
	require.NoError(t, err)
	assert.Equal(t, loomv1.ImprovementStatus_IMPROVEMENT_ROLLED_BACK, retrieved.Status)
}

func TestLearningAgent_GetImprovementHistory(t *testing.T) {
	agent, _, cleanup := setupTestLearningAgent(t)
	defer cleanup()

	ctx := context.Background()

	// Create multiple improvements
	improvements := []*loomv1.Improvement{
		{
			Id:            "improvement-1",
			Type:          loomv1.ImprovementType_IMPROVEMENT_PATTERN_ADD,
			Description:   "First improvement",
			Confidence:    0.8,
			Impact:        loomv1.ImpactLevel_IMPACT_LOW,
			TargetPattern: "test.pattern.1",
			TargetAgentId: "agent-1",
			Domain:        "sql",
			Status:        loomv1.ImprovementStatus_IMPROVEMENT_APPLIED,
			CreatedAt:     time.Now().UnixMilli(),
		},
		{
			Id:            "improvement-2",
			Type:          loomv1.ImprovementType_IMPROVEMENT_PATTERN_REMOVE,
			Description:   "Second improvement",
			Confidence:    0.9,
			Impact:        loomv1.ImpactLevel_IMPACT_MEDIUM,
			TargetPattern: "test.pattern.2",
			TargetAgentId: "agent-1",
			Domain:        "sql",
			Status:        loomv1.ImprovementStatus_IMPROVEMENT_PENDING,
			CreatedAt:     time.Now().UnixMilli(),
		},
		{
			Id:            "improvement-3",
			Type:          loomv1.ImprovementType_IMPROVEMENT_PARAMETER_TUNE,
			Description:   "Third improvement",
			Confidence:    0.7,
			Impact:        loomv1.ImpactLevel_IMPACT_HIGH,
			TargetPattern: "test.pattern.3",
			TargetAgentId: "agent-2",
			Domain:        "rest",
			Status:        loomv1.ImprovementStatus_IMPROVEMENT_ROLLED_BACK,
			CreatedAt:     time.Now().UnixMilli(),
		},
	}

	for _, imp := range improvements {
		err := agent.storeImprovement(ctx, imp)
		require.NoError(t, err)
	}

	// Get all history
	resp, err := agent.GetImprovementHistory(ctx, &loomv1.GetImprovementHistoryRequest{
		Limit: 50,
	})

	require.NoError(t, err)
	assert.Len(t, resp.Improvements, 3)
	assert.Equal(t, int32(3), resp.TotalCount)

	// Filter by agent
	resp, err = agent.GetImprovementHistory(ctx, &loomv1.GetImprovementHistoryRequest{
		AgentId: "agent-1",
		Limit:   50,
	})

	require.NoError(t, err)
	assert.Len(t, resp.Improvements, 2)

	// Filter by domain
	resp, err = agent.GetImprovementHistory(ctx, &loomv1.GetImprovementHistoryRequest{
		Domain: "rest",
		Limit:  50,
	})

	require.NoError(t, err)
	assert.Len(t, resp.Improvements, 1)

	// Filter by status
	resp, err = agent.GetImprovementHistory(ctx, &loomv1.GetImprovementHistoryRequest{
		Status: loomv1.ImprovementStatus_IMPROVEMENT_PENDING,
		Limit:  50,
	})

	require.NoError(t, err)
	assert.Len(t, resp.Improvements, 1)
}

func TestCircuitBreaker_FailureThreshold(t *testing.T) {
	agent, _, cleanup := setupTestLearningAgent(t)
	defer cleanup()

	// Circuit breaker should start closed
	assert.Equal(t, "closed", agent.circuitBreaker.state)
	assert.True(t, agent.canProceed())

	// Record failures up to threshold
	for i := 0; i < agent.circuitBreaker.threshold; i++ {
		agent.recordCircuitBreakerFailure()
	}

	// Circuit breaker should now be open
	assert.Equal(t, "open", agent.circuitBreaker.state)
	assert.False(t, agent.canProceed())
}

func TestCircuitBreaker_Cooldown(t *testing.T) {
	agent, _, cleanup := setupTestLearningAgent(t)
	defer cleanup()

	// Set short cooldown for testing
	agent.circuitBreaker.cooldownPeriod = 100 * time.Millisecond

	// Open circuit breaker
	for i := 0; i < agent.circuitBreaker.threshold; i++ {
		agent.recordCircuitBreakerFailure()
	}
	assert.Equal(t, "open", agent.circuitBreaker.state)
	assert.False(t, agent.canProceed())

	// Wait for cooldown
	time.Sleep(150 * time.Millisecond)

	// Should transition to half-open
	assert.True(t, agent.canProceed())
	assert.Equal(t, "half-open", agent.circuitBreaker.state)
}

func TestCircuitBreaker_HalfOpen(t *testing.T) {
	agent, _, cleanup := setupTestLearningAgent(t)
	defer cleanup()

	// Force circuit breaker to half-open
	agent.circuitBreaker.state = "half-open"
	agent.circuitBreaker.successCount = 0

	// Record 3 successes
	for i := 0; i < 3; i++ {
		agent.recordCircuitBreakerSuccess()
	}

	// Should transition to closed
	assert.Equal(t, "closed", agent.circuitBreaker.state)
	assert.Equal(t, 0, agent.circuitBreaker.failureCount)
}

func TestCircuitBreaker_HalfOpenFailure(t *testing.T) {
	agent, _, cleanup := setupTestLearningAgent(t)
	defer cleanup()

	// Force circuit breaker to half-open
	agent.circuitBreaker.state = "half-open"

	// Record a failure
	agent.recordCircuitBreakerFailure()

	// Should immediately reopen
	assert.Equal(t, "open", agent.circuitBreaker.state)
}

func TestLearningAgent_AutonomyLevels(t *testing.T) {
	tests := []struct {
		name          string
		autonomyLevel AutonomyLevel
		expectManual  bool
	}{
		{"manual mode", AutonomyManual, true},
		{"human approval mode", AutonomyHumanApproval, false},
		{"full auto mode", AutonomyFull, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, _, cleanup := setupTestLearningAgent(t)
			defer cleanup()

			agent.autonomyLevel = tt.autonomyLevel

			// Create improvement
			improvement := &loomv1.Improvement{
				Id:            "test-autonomy",
				Type:          loomv1.ImprovementType_IMPROVEMENT_PATTERN_ADD,
				Description:   "Test autonomy",
				Confidence:    0.8,
				Impact:        loomv1.ImpactLevel_IMPACT_LOW,
				TargetPattern: "test.pattern",
				Domain:        "sql",
				Status:        loomv1.ImprovementStatus_IMPROVEMENT_PENDING,
				CreatedAt:     time.Now().UnixMilli(),
			}

			ctx := context.Background()
			err := agent.storeImprovement(ctx, improvement)
			require.NoError(t, err)

			// Try to apply without force
			resp, err := agent.ApplyImprovement(ctx, &loomv1.ApplyImprovementRequest{
				ImprovementId: improvement.Id,
				Force:         false,
			})

			require.NoError(t, err)

			if tt.expectManual {
				assert.False(t, resp.Success)
				assert.Contains(t, resp.Message, "Manual approval required")
			} else {
				assert.True(t, resp.Success)
			}
		})
	}
}

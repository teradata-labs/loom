// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package learning

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

// TestLearningAgent_WithJudgeFeedback validates Phase 5 integration:
// End-to-end flow from pattern usage with judge evaluation to improvement generation
func TestLearningAgent_WithJudgeFeedback(t *testing.T) {
	t.Run("safety failure triggers improvement", func(t *testing.T) {
		// Setup test database
		db, cleanup := setupTestDB(t)
		defer cleanup()

		// Create pattern tracker and learning agent
		tracker := setupPatternTracker(t, db)
		defer func() {
			require.NoError(t, tracker.Stop(context.Background()))
		}()

		learningAgent := setupLearningAgent(t, db, tracker)

		// Allow tracker to fully initialize (prevents flaky test)
		time.Sleep(50 * time.Millisecond)

		// Record pattern usage with FAILING safety judge
		ctx := context.Background()
		judgeResult := &loomv1.EvaluateResponse{
			Passed:     false,
			FinalScore: 65.0, // Below 70% threshold
			DimensionScores: map[string]float64{
				"safety":  0.60, // 60% - Below 70% threshold!
				"quality": 0.85, // 85% - Above 80% threshold
				"cost":    0.80, // 80% - Above 75% threshold
			},
			Verdicts: []*loomv1.JudgeResult{
				{
					JudgeId:      "safety-judge",
					OverallScore: 65.0,
					DimensionScores: map[string]float64{
						"safety":  60.0,
						"quality": 85.0,
						"cost":    80.0,
					},
				},
			},
		}

		tracker.RecordUsage(
			ctx,
			"sql-query-generation",
			"default",
			"sql",
			"test-agent",
			false, // Failed due to safety
			0.01,
			100*time.Millisecond,
			"safety_violation",
			"anthropic",
			"claude-sonnet-4",
			judgeResult,
		)

		// Flush tracker to database
		time.Sleep(100 * time.Millisecond) // Allow buffer to accumulate
		require.NoError(t, tracker.flush(ctx))

		// Analyze patterns - should detect safety failure
		resp, err := learningAgent.AnalyzePatternEffectiveness(ctx, &loomv1.AnalyzePatternEffectivenessRequest{
			Domain:      "sql",
			AgentId:     "test-agent",
			WindowHours: 24,
		})
		require.NoError(t, err)
		require.NotNil(t, resp)

		// Verify pattern metrics captured judge scores
		require.GreaterOrEqual(t, len(resp.Patterns), 1, "Should have at least one pattern")
		pattern := resp.Patterns[0]
		assert.Equal(t, "sql-query-generation", pattern.PatternName)
		assert.Contains(t, pattern.JudgeCriterionScores, "safety")
		assert.Less(t, pattern.JudgeCriterionScores["safety"], 0.70, "Safety score should be below threshold")

		// Generate improvements - should create safety improvement
		impResp, err := learningAgent.GenerateImprovements(ctx, &loomv1.GenerateImprovementsRequest{
			Domain:       "sql",
			AgentId:      "test-agent",
			MaxProposals: 10,
		})
		require.NoError(t, err)
		require.NotNil(t, impResp)
		require.GreaterOrEqual(t, len(impResp.Improvements), 1, "Should generate at least one improvement")

		// Find safety improvement
		var safetyImprovement *loomv1.Improvement
		for _, imp := range impResp.Improvements {
			if imp.TargetPattern == "sql-query-generation" && imp.Impact == loomv1.ImpactLevel_IMPACT_CRITICAL {
				safetyImprovement = imp
				break
			}
		}

		require.NotNil(t, safetyImprovement, "Should generate safety improvement")
		assert.Contains(t, safetyImprovement.Description, "safety")
		assert.Equal(t, loomv1.ImpactLevel_IMPACT_CRITICAL, safetyImprovement.Impact)
		assert.Equal(t, loomv1.ImprovementStatus_IMPROVEMENT_PENDING, safetyImprovement.Status)

		t.Logf("Safety improvement generated: %s", safetyImprovement.Description)
	})

	t.Run("cost inefficiency triggers optimization", func(t *testing.T) {
		db, cleanup := setupTestDB(t)
		defer cleanup()

		tracker := setupPatternTracker(t, db)
		defer func() {
			require.NoError(t, tracker.Stop(context.Background()))
		}()

		learningAgent := setupLearningAgent(t, db, tracker)

		// Record pattern usage with FAILING cost judge
		ctx := context.Background()
		judgeResult := &loomv1.EvaluateResponse{
			Passed:     false,
			FinalScore: 70.0,
			DimensionScores: map[string]float64{
				"safety":  0.90, // 90% - Good
				"quality": 0.85, // 85% - Good
				"cost":    0.65, // 65% - Below 75% threshold! (expensive)
			},
		}

		tracker.RecordUsage(
			ctx,
			"complex-analysis",
			"default",
			"sql",
			"test-agent",
			false, // Failed due to cost
			0.25,  // Expensive!
			2000*time.Millisecond,
			"cost_exceeded",
			"anthropic",
			"claude-opus-4",
			judgeResult,
		)

		require.NoError(t, tracker.flush(ctx))

		// Generate improvements
		impResp, err := learningAgent.GenerateImprovements(ctx, &loomv1.GenerateImprovementsRequest{
			Domain:       "sql",
			AgentId:      "test-agent",
			MaxProposals: 10,
		})
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(impResp.Improvements), 1)

		// Find cost improvement
		var costImprovement *loomv1.Improvement
		for _, imp := range impResp.Improvements {
			if imp.TargetPattern == "complex-analysis" && imp.Impact == loomv1.ImpactLevel_IMPACT_MEDIUM {
				costImprovement = imp
				break
			}
		}

		require.NotNil(t, costImprovement, "Should generate cost improvement")
		assert.Contains(t, costImprovement.Description, "cost")
		assert.Equal(t, loomv1.ImpactLevel_IMPACT_MEDIUM, costImprovement.Impact)

		t.Logf("Cost improvement generated: %s", costImprovement.Description)
	})

	t.Run("quality issue triggers template adjustment", func(t *testing.T) {
		db, cleanup := setupTestDB(t)
		defer cleanup()

		tracker := setupPatternTracker(t, db)
		defer func() {
			require.NoError(t, tracker.Stop(context.Background()))
		}()

		learningAgent := setupLearningAgent(t, db, tracker)

		// Record pattern usage with FAILING quality judge
		ctx := context.Background()
		judgeResult := &loomv1.EvaluateResponse{
			Passed:     false,
			FinalScore: 75.0,
			DimensionScores: map[string]float64{
				"safety":  0.90, // 90% - Good
				"quality": 0.72, // 72% - Below 80% threshold!
				"cost":    0.85, // 85% - Good
			},
		}

		tracker.RecordUsage(
			ctx,
			"data-extraction",
			"default",
			"sql",
			"test-agent",
			false, // Failed due to quality
			0.01,
			150*time.Millisecond,
			"incorrect_output",
			"anthropic",
			"claude-sonnet-4",
			judgeResult,
		)

		require.NoError(t, tracker.flush(ctx))

		// Generate improvements
		impResp, err := learningAgent.GenerateImprovements(ctx, &loomv1.GenerateImprovementsRequest{
			Domain:       "sql",
			AgentId:      "test-agent",
			MaxProposals: 10,
		})
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(impResp.Improvements), 1)

		// Find quality improvement
		var qualityImprovement *loomv1.Improvement
		for _, imp := range impResp.Improvements {
			if imp.TargetPattern == "data-extraction" && imp.Impact == loomv1.ImpactLevel_IMPACT_HIGH {
				qualityImprovement = imp
				break
			}
		}

		require.NotNil(t, qualityImprovement, "Should generate quality improvement")
		assert.Contains(t, qualityImprovement.Description, "quality")
		assert.Equal(t, loomv1.ImpactLevel_IMPACT_HIGH, qualityImprovement.Impact)
		assert.Equal(t, loomv1.ImprovementType_IMPROVEMENT_TEMPLATE_ADJUST, qualityImprovement.Type)

		t.Logf("Quality improvement generated: %s", qualityImprovement.Description)
	})

	t.Run("domain compliance failure triggers guidance improvement", func(t *testing.T) {
		db, cleanup := setupTestDB(t)
		defer cleanup()

		tracker := setupPatternTracker(t, db)
		defer func() {
			require.NoError(t, tracker.Stop(context.Background()))
		}()

		learningAgent := setupLearningAgent(t, db, tracker)

		// Record pattern usage with FAILING domain judge
		ctx := context.Background()
		judgeResult := &loomv1.EvaluateResponse{
			Passed:     false,
			FinalScore: 70.0,
			DimensionScores: map[string]float64{
				"safety":  0.90, // 90% - Good
				"quality": 0.85, // 85% - Good
				"domain":  0.65, // 65% - Below 75% threshold! (not Teradata-compliant)
			},
		}

		tracker.RecordUsage(
			ctx,
			"teradata-optimization",
			"default",
			"sql",
			"test-agent",
			false, // Failed domain compliance
			0.01,
			120*time.Millisecond,
			"non_teradata_syntax",
			"anthropic",
			"claude-sonnet-4",
			judgeResult,
		)

		require.NoError(t, tracker.flush(ctx))

		// Generate improvements
		impResp, err := learningAgent.GenerateImprovements(ctx, &loomv1.GenerateImprovementsRequest{
			Domain:       "sql",
			AgentId:      "test-agent",
			MaxProposals: 10,
		})
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(impResp.Improvements), 1)

		// Find domain improvement
		var domainImprovement *loomv1.Improvement
		for _, imp := range impResp.Improvements {
			if imp.TargetPattern == "teradata-optimization" {
				domainImprovement = imp
				break
			}
		}

		require.NotNil(t, domainImprovement, "Should generate domain improvement")
		assert.Contains(t, domainImprovement.Description, "domain")
		assert.Equal(t, loomv1.ImpactLevel_IMPACT_HIGH, domainImprovement.Impact)

		t.Logf("Domain improvement generated: %s", domainImprovement.Description)
	})

	t.Run("all dimensions passing - no improvements", func(t *testing.T) {
		db, cleanup := setupTestDB(t)
		defer cleanup()

		tracker := setupPatternTracker(t, db)
		defer func() {
			require.NoError(t, tracker.Stop(context.Background()))
		}()

		learningAgent := setupLearningAgent(t, db, tracker)

		// Record pattern usage with ALL judges passing
		ctx := context.Background()
		judgeResult := &loomv1.EvaluateResponse{
			Passed:     true,
			FinalScore: 90.0,
			DimensionScores: map[string]float64{
				"safety":  0.95, // 95% - Excellent
				"quality": 0.90, // 90% - Excellent
				"cost":    0.85, // 85% - Good
				"domain":  0.90, // 90% - Excellent
			},
		}

		tracker.RecordUsage(
			ctx,
			"well-performing-pattern",
			"default",
			"sql",
			"test-agent",
			true, // Success!
			0.005,
			50*time.Millisecond,
			"",
			"anthropic",
			"claude-sonnet-4",
			judgeResult,
		)

		require.NoError(t, tracker.flush(ctx))

		// Generate improvements - should not create any for this pattern
		impResp, err := learningAgent.GenerateImprovements(ctx, &loomv1.GenerateImprovementsRequest{
			Domain:       "sql",
			AgentId:      "test-agent",
			MaxProposals: 10,
		})
		require.NoError(t, err)

		// Check that no improvements generated for this pattern (all dimensions passing)
		for _, imp := range impResp.Improvements {
			assert.NotEqual(t, "well-performing-pattern", imp.TargetPattern,
				"Should not generate improvements for pattern with all dimensions passing thresholds")
		}

		t.Logf("No improvements generated for well-performing pattern (expected)")
	})

	t.Run("multi-dimensional failure prioritization", func(t *testing.T) {
		db, cleanup := setupTestDB(t)
		defer cleanup()

		tracker := setupPatternTracker(t, db)
		defer func() {
			require.NoError(t, tracker.Stop(context.Background()))
		}()

		learningAgent := setupLearningAgent(t, db, tracker)

		// Record pattern usage with MULTIPLE dimension failures
		ctx := context.Background()
		judgeResult := &loomv1.EvaluateResponse{
			Passed:     false,
			FinalScore: 62.0,
			DimensionScores: map[string]float64{
				"safety":  0.65, // 65% - Below 70% (CRITICAL)
				"quality": 0.72, // 72% - Below 80% (HIGH)
				"cost":    0.50, // 50% - Below 75% (MEDIUM)
			},
		}

		tracker.RecordUsage(
			ctx,
			"problematic-pattern",
			"default",
			"sql",
			"test-agent",
			false,
			0.50, // Expensive
			5000*time.Millisecond,
			"multiple_issues",
			"anthropic",
			"claude-opus-4",
			judgeResult,
		)

		require.NoError(t, tracker.flush(ctx))

		// Generate improvements
		impResp, err := learningAgent.GenerateImprovements(ctx, &loomv1.GenerateImprovementsRequest{
			Domain:       "sql",
			AgentId:      "test-agent",
			MaxProposals: 10,
		})
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(impResp.Improvements), 3, "Should generate improvements for each failing dimension")

		// Verify safety improvement has highest priority (CRITICAL)
		var safetyImp, qualityImp, costImp *loomv1.Improvement
		for _, imp := range impResp.Improvements {
			if imp.TargetPattern != "problematic-pattern" {
				continue
			}
			if imp.Impact == loomv1.ImpactLevel_IMPACT_CRITICAL {
				safetyImp = imp
			} else if imp.Impact == loomv1.ImpactLevel_IMPACT_HIGH {
				qualityImp = imp
			} else if imp.Impact == loomv1.ImpactLevel_IMPACT_MEDIUM {
				costImp = imp
			}
		}

		assert.NotNil(t, safetyImp, "Should have safety improvement (CRITICAL)")
		assert.NotNil(t, qualityImp, "Should have quality improvement (HIGH)")
		assert.NotNil(t, costImp, "Should have cost improvement (MEDIUM)")

		t.Logf("Multi-dimensional improvements generated:")
		t.Logf("  Safety (CRITICAL): %s", safetyImp.Description)
		t.Logf("  Quality (HIGH): %s", qualityImp.Description)
		t.Logf("  Cost (MEDIUM): %s", costImp.Description)
	})
}

// setupTestDB creates an in-memory SQLite database for testing
func setupTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()

	// Create temp database file
	dbFile := t.TempDir() + "/test.db"
	db, err := sql.Open("sqlite3", dbFile)
	require.NoError(t, err)

	// Initialize schema
	ctx := context.Background()
	tracer := observability.NewNoOpTracer()
	require.NoError(t, InitSelfImprovementSchema(ctx, db, tracer))

	cleanup := func() {
		db.Close()
		os.Remove(dbFile)
	}

	return db, cleanup
}

// setupPatternTracker creates a pattern tracker for testing
func setupPatternTracker(t *testing.T, db *sql.DB) *PatternEffectivenessTracker {
	t.Helper()

	tracer := observability.NewNoOpTracer()
	bus := communication.NewMessageBus(
		nil, // refStore
		nil, // policy
		tracer,
		zap.NewNop(),
	)

	tracker := NewPatternEffectivenessTracker(
		db,
		tracer,
		bus,
		1*time.Hour,          // windowSize
		100*time.Millisecond, // flushInterval (fast for testing)
	)

	// Start tracker's flush loop
	ctx := context.Background()
	err := tracker.Start(ctx)
	require.NoError(t, err)

	return tracker
}

// setupLearningAgent creates a learning agent for testing
func setupLearningAgent(t *testing.T, db *sql.DB, tracker *PatternEffectivenessTracker) *LearningAgent {
	t.Helper()

	tracer := observability.NewNoOpTracer()

	// Create metrics collector (needed for learning engine)
	dbPath := t.TempDir() + "/metrics.db"
	collector, err := NewMetricsCollector(dbPath, tracer)
	require.NoError(t, err)

	// Create learning engine
	engine := NewLearningEngine(collector, tracer)

	agent, err := NewLearningAgent(
		db,
		tracer,
		engine,
		tracker,
		AutonomyManual, // Manual approval for testing
		1*time.Hour,
	)
	require.NoError(t, err)

	return agent
}

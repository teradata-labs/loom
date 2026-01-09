// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package learning

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestMultiDimensionalPatternTuning validates Phase 6: dimension-weighted pattern tuning
func TestMultiDimensionalPatternTuning(t *testing.T) {
	t.Run("quality-focused tuning prioritizes quality dimension", func(t *testing.T) {
		// Setup
		db, cleanup := setupTestDB(t)
		defer cleanup()

		tracker := setupPatternTracker(t, db)
		defer func() {
			require.NoError(t, tracker.Stop(context.Background()))
		}()

		learningAgent := setupLearningAgent(t, db, tracker)
		time.Sleep(50 * time.Millisecond) // Tracker initialization

		ctx := context.Background()

		// Pattern A: High quality (90%), low cost efficiency (60%)
		patternAJudge := &loomv1.EvaluateResponse{
			Passed:     true,
			FinalScore: 75.0,
			DimensionScores: map[string]float64{
				"quality": 0.90, // Excellent
				"cost":    0.60, // Poor
				"safety":  0.80,
			},
		}

		for i := 0; i < 100; i++ {
			tracker.RecordUsage(ctx, "pattern-a", "default", "test", "test-agent",
				true, 0.02, 100*time.Millisecond, "", "test", "test", patternAJudge)
		}

		// Pattern B: Low quality (60%), high cost efficiency (90%)
		patternBJudge := &loomv1.EvaluateResponse{
			Passed:     false,
			FinalScore: 75.0,
			DimensionScores: map[string]float64{
				"quality": 0.60, // Poor
				"cost":    0.90, // Excellent
				"safety":  0.80,
			},
		}

		for i := 0; i < 100; i++ {
			tracker.RecordUsage(ctx, "pattern-b", "default", "test", "test-agent",
				false, 0.005, 100*time.Millisecond, "error", "test", "test", patternBJudge)
		}

		require.NoError(t, tracker.flush(ctx))
		time.Sleep(50 * time.Millisecond)

		// Tune with QUALITY focus (quality weight >> cost weight)
		tuneResp, err := learningAgent.TunePatterns(ctx, &loomv1.TunePatternsRequest{
			Domain:   "test",
			Strategy: loomv1.TuningStrategy_TUNING_MODERATE,
			DryRun:   true,
			DimensionWeights: map[string]float64{
				"quality": 0.8, // 80% weight on quality
				"cost":    0.2, // 20% weight on cost
			},
		})
		require.NoError(t, err)
		require.NotNil(t, tuneResp)

		// Find tunings for both patterns
		var patternATuning, patternBTuning *loomv1.PatternTuning
		for _, tuning := range tuneResp.Tunings {
			if tuning.PatternName == "pattern-a" {
				patternATuning = tuning
			} else if tuning.PatternName == "pattern-b" {
				patternBTuning = tuning
			}
		}

		require.NotNil(t, patternATuning, "Pattern A should have tuning")
		require.NotNil(t, patternBTuning, "Pattern B should have tuning")

		// Extract priority deltas
		patternAPriorityDelta := getPriorityDelta(patternATuning)
		patternBPriorityDelta := getPriorityDelta(patternBTuning)

		t.Logf("Pattern A (high quality, low cost): priority delta = %d", patternAPriorityDelta)
		t.Logf("Pattern B (low quality, high cost): priority delta = %d", patternBPriorityDelta)

		// With quality-focused weights, Pattern A should have higher priority increase
		assert.Greater(t, patternAPriorityDelta, patternBPriorityDelta,
			"Quality-focused tuning should prioritize high-quality pattern (A) over cost-efficient pattern (B)")
	})

	t.Run("cost-focused tuning prioritizes cost dimension", func(t *testing.T) {
		// Setup
		db, cleanup := setupTestDB(t)
		defer cleanup()

		tracker := setupPatternTracker(t, db)
		defer func() {
			require.NoError(t, tracker.Stop(context.Background()))
		}()

		learningAgent := setupLearningAgent(t, db, tracker)
		time.Sleep(50 * time.Millisecond)

		ctx := context.Background()

		// Pattern A: High quality (90%), low cost efficiency (60%)
		patternAJudge := &loomv1.EvaluateResponse{
			Passed:     true,
			FinalScore: 75.0,
			DimensionScores: map[string]float64{
				"quality": 0.90,
				"cost":    0.60,
				"safety":  0.80,
			},
		}

		for i := 0; i < 100; i++ {
			tracker.RecordUsage(ctx, "pattern-a", "default", "test", "test-agent",
				true, 0.02, 100*time.Millisecond, "", "test", "test", patternAJudge)
		}

		// Pattern B: Low quality (60%), high cost efficiency (90%)
		patternBJudge := &loomv1.EvaluateResponse{
			Passed:     false,
			FinalScore: 75.0,
			DimensionScores: map[string]float64{
				"quality": 0.60,
				"cost":    0.90,
				"safety":  0.80,
			},
		}

		for i := 0; i < 100; i++ {
			tracker.RecordUsage(ctx, "pattern-b", "default", "test", "test-agent",
				false, 0.005, 100*time.Millisecond, "error", "test", "test", patternBJudge)
		}

		require.NoError(t, tracker.flush(ctx))
		time.Sleep(50 * time.Millisecond)

		// Tune with COST focus (cost weight >> quality weight)
		tuneResp, err := learningAgent.TunePatterns(ctx, &loomv1.TunePatternsRequest{
			Domain:   "test",
			Strategy: loomv1.TuningStrategy_TUNING_MODERATE,
			DryRun:   true,
			DimensionWeights: map[string]float64{
				"quality": 0.2, // 20% weight on quality
				"cost":    0.8, // 80% weight on cost
			},
		})
		require.NoError(t, err)
		require.NotNil(t, tuneResp)

		// Find tunings
		var patternATuning, patternBTuning *loomv1.PatternTuning
		for _, tuning := range tuneResp.Tunings {
			if tuning.PatternName == "pattern-a" {
				patternATuning = tuning
			} else if tuning.PatternName == "pattern-b" {
				patternBTuning = tuning
			}
		}

		require.NotNil(t, patternATuning)
		require.NotNil(t, patternBTuning)

		patternAPriorityDelta := getPriorityDelta(patternATuning)
		patternBPriorityDelta := getPriorityDelta(patternBTuning)

		t.Logf("Pattern A (high quality, low cost): priority delta = %d", patternAPriorityDelta)
		t.Logf("Pattern B (low quality, high cost): priority delta = %d", patternBPriorityDelta)

		// With cost-focused weights, Pattern B should have higher priority increase
		assert.Greater(t, patternBPriorityDelta, patternAPriorityDelta,
			"Cost-focused tuning should prioritize cost-efficient pattern (B) over high-quality pattern (A)")
	})

	t.Run("balanced tuning uses equal weights", func(t *testing.T) {
		// Setup
		db, cleanup := setupTestDB(t)
		defer cleanup()

		tracker := setupPatternTracker(t, db)
		defer func() {
			require.NoError(t, tracker.Stop(context.Background()))
		}()

		learningAgent := setupLearningAgent(t, db, tracker)
		time.Sleep(50 * time.Millisecond)

		ctx := context.Background()

		// Pattern with balanced scores
		patternJudge := &loomv1.EvaluateResponse{
			Passed:     true,
			FinalScore: 80.0,
			DimensionScores: map[string]float64{
				"quality": 0.80,
				"cost":    0.80,
				"safety":  0.80,
			},
		}

		for i := 0; i < 100; i++ {
			tracker.RecordUsage(ctx, "balanced-pattern", "default", "test", "test-agent",
				true, 0.01, 100*time.Millisecond, "", "test", "test", patternJudge)
		}

		require.NoError(t, tracker.flush(ctx))
		time.Sleep(50 * time.Millisecond)

		// Tune with balanced weights
		tuneResp, err := learningAgent.TunePatterns(ctx, &loomv1.TunePatternsRequest{
			Domain:   "test",
			Strategy: loomv1.TuningStrategy_TUNING_MODERATE,
			DryRun:   true,
			DimensionWeights: map[string]float64{
				"quality": 0.33,
				"cost":    0.33,
				"safety":  0.34,
			},
		})
		require.NoError(t, err)
		require.NotNil(t, tuneResp)
		require.GreaterOrEqual(t, len(tuneResp.Tunings), 1)

		tuning := tuneResp.Tunings[0]
		priorityDelta := getPriorityDelta(tuning)

		t.Logf("Balanced pattern: priority delta = %d", priorityDelta)

		// Pattern with 0.8 scores should get positive adjustment (above 0.5 neutral point)
		assert.Greater(t, priorityDelta, int32(0), "Balanced high-scoring pattern should get priority increase")
	})
}

// getPriorityDelta extracts priority change from tuning adjustments
func getPriorityDelta(tuning *loomv1.PatternTuning) int32 {
	for _, adj := range tuning.Adjustments {
		if adj.ParameterName == "priority" {
			var oldVal, newVal int32
			_, _ = fmt.Sscanf(adj.OldValue, "%d", &oldVal)
			_, _ = fmt.Sscanf(adj.NewValue, "%d", &newVal)
			return newVal - oldVal
		}
	}
	return 0
}

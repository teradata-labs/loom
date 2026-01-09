// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package learning

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestJudgeDataFlow_Debug validates data flow from RecordUsage -> DB -> AnalyzePatternEffectiveness
func TestJudgeDataFlow_Debug(t *testing.T) {
	// Setup
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tracker := setupPatternTracker(t, db)
	defer func() {
		require.NoError(t, tracker.Stop(context.Background()))
	}()

	ctx := context.Background()

	// Record a single usage with judge result
	judgeResult := &loomv1.EvaluateResponse{
		Passed:     false,
		FinalScore: 65.0,
		DimensionScores: map[string]float64{
			"safety":  0.60, // 60%
			"quality": 0.85, // 85%
		},
	}

	tracker.RecordUsage(
		ctx,
		"test-pattern",
		"default",
		"test",
		"test-agent",
		false,
		0.01,
		100*time.Millisecond,
		"test_error",
		"test-provider",
		"test-model",
		judgeResult,
	)

	t.Log("✅ RecordUsage called")

	// Check buffer
	tracker.bufferMu.RLock()
	bufferSize := len(tracker.buffer)
	t.Logf("📊 Buffer size: %d", bufferSize)
	for key, stats := range tracker.buffer {
		t.Logf("  Key: %s", key)
		t.Logf("    JudgeEvaluationsCount: %d", stats.JudgeEvaluationsCount)
		t.Logf("    JudgeCriterionScores: %v", stats.JudgeCriterionScores)
		t.Logf("    JudgeCriterionCounts: %v", stats.JudgeCriterionCounts)
	}
	tracker.bufferMu.RUnlock()

	// Flush to database
	t.Log("🔄 Flushing to database...")
	require.NoError(t, tracker.flush(ctx))
	t.Log("✅ Flush completed")

	// Check database directly
	t.Log("🔍 Querying database...")
	rows, err := db.QueryContext(ctx, `
		SELECT
			pattern_name,
			judge_pass_rate,
			judge_avg_score,
			judge_criterion_scores_json
		FROM pattern_effectiveness
	`)
	require.NoError(t, err)
	defer rows.Close()

	rowCount := 0
	for rows.Next() {
		var (
			patternName              string
			judgePassRate            sql.NullFloat64
			judgeAvgScore            sql.NullFloat64
			judgeCriterionScoresJSON sql.NullString
		)
		require.NoError(t, rows.Scan(&patternName, &judgePassRate, &judgeAvgScore, &judgeCriterionScoresJSON))
		rowCount++

		t.Logf("📄 Row %d:", rowCount)
		t.Logf("  Pattern: %s", patternName)
		t.Logf("  JudgePassRate: %v", judgePassRate)
		t.Logf("  JudgeAvgScore: %v", judgeAvgScore)
		t.Logf("  JudgeCriterionScoresJSON: %v", judgeCriterionScoresJSON)
	}

	t.Logf("📊 Total rows in DB: %d", rowCount)
	require.Greater(t, rowCount, 0, "Should have at least one row in database")

	// Now try AnalyzePatternEffectiveness
	learningAgent := setupLearningAgent(t, db, tracker)

	resp, err := learningAgent.AnalyzePatternEffectiveness(ctx, &loomv1.AnalyzePatternEffectivenessRequest{
		Domain:      "test",
		AgentId:     "test-agent",
		WindowHours: 24,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	t.Logf("📊 Patterns returned: %d", len(resp.Patterns))
	for _, pattern := range resp.Patterns {
		t.Logf("  Pattern: %s", pattern.PatternName)
		t.Logf("    JudgeCriterionScores: %v", pattern.JudgeCriterionScores)
	}

	// Verify
	require.GreaterOrEqual(t, len(resp.Patterns), 1, "Should return at least one pattern")
	pattern := resp.Patterns[0]
	require.Equal(t, "test-pattern", pattern.PatternName)
	require.NotNil(t, pattern.JudgeCriterionScores, "JudgeCriterionScores should not be nil")
	require.Contains(t, pattern.JudgeCriterionScores, "safety")
	require.Contains(t, pattern.JudgeCriterionScores, "quality")
}

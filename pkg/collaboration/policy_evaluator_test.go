// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package collaboration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap/zaptest"
)

func TestPolicyEvaluator_ShouldSpawn_Always(t *testing.T) {
	evaluator := NewPolicyEvaluator(zaptest.NewLogger(t))
	ctx := context.Background()

	policy := &loomv1.EphemeralAgentPolicy{
		Role: "always-spawn",
		Trigger: &loomv1.SpawnTrigger{
			Type: loomv1.SpawnTriggerType_ALWAYS,
		},
		MaxSpawns:    0, // unlimited
		CostLimitUsd: 0, // unlimited
	}

	evalCtx := NewEvaluationContext()
	shouldSpawn, reason := evaluator.ShouldSpawn(ctx, policy, evalCtx)

	assert.True(t, shouldSpawn)
	assert.Empty(t, reason)
}

func TestPolicyEvaluator_ShouldSpawn_ConsensusNotReached(t *testing.T) {
	evaluator := NewPolicyEvaluator(zaptest.NewLogger(t))
	ctx := context.Background()

	policy := &loomv1.EphemeralAgentPolicy{
		Role: "judge",
		Trigger: &loomv1.SpawnTrigger{
			Type:      loomv1.SpawnTriggerType_CONSENSUS_NOT_REACHED,
			Threshold: 0.67,
		},
	}

	tests := []struct {
		name             string
		consensusReached bool
		expectedSpawn    bool
	}{
		{
			name:             "consensus not reached - should spawn",
			consensusReached: false,
			expectedSpawn:    true,
		},
		{
			name:             "consensus reached - should not spawn",
			consensusReached: true,
			expectedSpawn:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evalCtx := &EvaluationContext{
				ConsensusReached: tt.consensusReached,
			}

			shouldSpawn, _ := evaluator.ShouldSpawn(ctx, policy, evalCtx)
			assert.Equal(t, tt.expectedSpawn, shouldSpawn)
		})
	}
}

func TestPolicyEvaluator_ShouldSpawn_ConfidenceBelow(t *testing.T) {
	evaluator := NewPolicyEvaluator(zaptest.NewLogger(t))
	ctx := context.Background()

	policy := &loomv1.EphemeralAgentPolicy{
		Role: "expert",
		Trigger: &loomv1.SpawnTrigger{
			Type:      loomv1.SpawnTriggerType_CONFIDENCE_BELOW,
			Threshold: 0.7,
		},
	}

	tests := []struct {
		name          string
		confidence    *float32
		expectedSpawn bool
	}{
		{
			name:          "confidence below threshold",
			confidence:    ptrFloat32(0.5),
			expectedSpawn: true,
		},
		{
			name:          "confidence above threshold",
			confidence:    ptrFloat32(0.8),
			expectedSpawn: false,
		},
		{
			name:          "confidence at threshold",
			confidence:    ptrFloat32(0.7),
			expectedSpawn: false,
		},
		{
			name:          "no confidence data",
			confidence:    nil,
			expectedSpawn: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evalCtx := &EvaluationContext{
				AverageConfidence: tt.confidence,
			}

			shouldSpawn, _ := evaluator.ShouldSpawn(ctx, policy, evalCtx)
			assert.Equal(t, tt.expectedSpawn, shouldSpawn)
		})
	}
}

func TestPolicyEvaluator_ShouldSpawn_TieDetected(t *testing.T) {
	evaluator := NewPolicyEvaluator(zaptest.NewLogger(t))
	ctx := context.Background()

	policy := &loomv1.EphemeralAgentPolicy{
		Role: "tie-breaker",
		Trigger: &loomv1.SpawnTrigger{
			Type: loomv1.SpawnTriggerType_TIE_DETECTED,
		},
	}

	tests := []struct {
		name          string
		tieDetected   bool
		expectedSpawn bool
	}{
		{
			name:          "tie detected",
			tieDetected:   true,
			expectedSpawn: true,
		},
		{
			name:          "no tie",
			tieDetected:   false,
			expectedSpawn: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evalCtx := &EvaluationContext{
				TieDetected: tt.tieDetected,
			}

			shouldSpawn, _ := evaluator.ShouldSpawn(ctx, policy, evalCtx)
			assert.Equal(t, tt.expectedSpawn, shouldSpawn)
		})
	}
}

func TestPolicyEvaluator_MaxSpawns(t *testing.T) {
	evaluator := NewPolicyEvaluator(zaptest.NewLogger(t))
	ctx := context.Background()

	policy := &loomv1.EphemeralAgentPolicy{
		Role: "limited-agent",
		Trigger: &loomv1.SpawnTrigger{
			Type: loomv1.SpawnTriggerType_ALWAYS,
		},
		MaxSpawns:    2,
		CostLimitUsd: 10.0,
	}

	evalCtx := NewEvaluationContext()

	// First spawn - should succeed
	shouldSpawn, reason := evaluator.ShouldSpawn(ctx, policy, evalCtx)
	assert.True(t, shouldSpawn)
	assert.Empty(t, reason)
	evaluator.RecordSpawn("limited-agent", 0.10)

	// Second spawn - should succeed
	shouldSpawn, reason = evaluator.ShouldSpawn(ctx, policy, evalCtx)
	assert.True(t, shouldSpawn)
	assert.Empty(t, reason)
	evaluator.RecordSpawn("limited-agent", 0.10)

	// Third spawn - should fail (max 2)
	shouldSpawn, reason = evaluator.ShouldSpawn(ctx, policy, evalCtx)
	assert.False(t, shouldSpawn)
	assert.Contains(t, reason, "max spawns")

	// Verify stats
	count, cost := evaluator.GetSpawnStats("limited-agent")
	assert.Equal(t, 2, count)
	assert.InDelta(t, 0.20, cost, 0.001)
}

func TestPolicyEvaluator_CostLimit(t *testing.T) {
	evaluator := NewPolicyEvaluator(zaptest.NewLogger(t))
	ctx := context.Background()

	policy := &loomv1.EphemeralAgentPolicy{
		Role: "expensive-agent",
		Trigger: &loomv1.SpawnTrigger{
			Type: loomv1.SpawnTriggerType_ALWAYS,
		},
		MaxSpawns:    10,
		CostLimitUsd: 0.50,
	}

	evalCtx := NewEvaluationContext()

	// Spawn 1: $0.20 - should succeed
	shouldSpawn, reason := evaluator.ShouldSpawn(ctx, policy, evalCtx)
	assert.True(t, shouldSpawn)
	assert.Empty(t, reason)
	evaluator.RecordSpawn("expensive-agent", 0.20)

	// Spawn 2: $0.30 - should succeed (total $0.50, exactly at limit)
	shouldSpawn, reason = evaluator.ShouldSpawn(ctx, policy, evalCtx)
	assert.True(t, shouldSpawn, "Should allow spawn when under limit")
	assert.Empty(t, reason)
	evaluator.RecordSpawn("expensive-agent", 0.30)

	// Spawn 3: total is now $0.50, at limit - should fail
	shouldSpawn, reason = evaluator.ShouldSpawn(ctx, policy, evalCtx)
	assert.False(t, shouldSpawn, "Should block spawn when at cost limit")
	assert.Contains(t, reason, "cost limit")

	// Verify total cost
	count, cost := evaluator.GetSpawnStats("expensive-agent")
	assert.Equal(t, 2, count)
	assert.InDelta(t, 0.50, cost, 0.001)
}

func TestPolicyEvaluator_Reset(t *testing.T) {
	evaluator := NewPolicyEvaluator(zaptest.NewLogger(t))

	// Record some spawns
	evaluator.RecordSpawn("role1", 0.10)
	evaluator.RecordSpawn("role1", 0.15)
	evaluator.RecordSpawn("role2", 0.20)

	count1, cost1 := evaluator.GetSpawnStats("role1")
	assert.Equal(t, 2, count1)
	assert.InDelta(t, 0.25, cost1, 0.001)

	count2, cost2 := evaluator.GetSpawnStats("role2")
	assert.Equal(t, 1, count2)
	assert.InDelta(t, 0.20, cost2, 0.001)

	// Reset
	evaluator.Reset()

	// Verify all counters cleared
	count1, cost1 = evaluator.GetSpawnStats("role1")
	assert.Equal(t, 0, count1)
	assert.Equal(t, 0.0, cost1)

	count2, cost2 = evaluator.GetSpawnStats("role2")
	assert.Equal(t, 0, count2)
	assert.Equal(t, 0.0, cost2)
}

func TestFromSwarmResult(t *testing.T) {
	result := &loomv1.SwarmResult{
		Votes: []*loomv1.SwarmVote{
			{AgentId: "a1", Choice: "option1", Confidence: 0.8},
			{AgentId: "a2", Choice: "option2", Confidence: 0.7},
			{AgentId: "a3", Choice: "option1", Confidence: 0.9},
		},
		VoteDistribution: map[string]int32{
			"option1": 2,
			"option2": 1,
		},
		AverageConfidence: 0.8,
		ThresholdMet:      true,
	}

	evalCtx := FromSwarmResult(result)

	assert.True(t, evalCtx.ConsensusReached)
	require.NotNil(t, evalCtx.AverageConfidence)
	assert.Equal(t, float32(0.8), *evalCtx.AverageConfidence)
	assert.False(t, evalCtx.TieDetected)
	require.NotNil(t, evalCtx.WinningVoteCount)
	assert.Equal(t, int32(2), *evalCtx.WinningVoteCount)
	require.NotNil(t, evalCtx.TotalVotes)
	assert.Equal(t, int32(3), *evalCtx.TotalVotes)
}

func TestFromSwarmResult_TieDetection(t *testing.T) {
	result := &loomv1.SwarmResult{
		Votes: []*loomv1.SwarmVote{
			{AgentId: "a1", Choice: "option1"},
			{AgentId: "a2", Choice: "option2"},
		},
		VoteDistribution: map[string]int32{
			"option1": 1,
			"option2": 1,
		},
		ThresholdMet: false,
	}

	evalCtx := FromSwarmResult(result)

	assert.True(t, evalCtx.TieDetected, "Should detect tie when two options have same vote count")
}

func TestFromDebateResult(t *testing.T) {
	result := &loomv1.DebateResult{
		Rounds: []*loomv1.DebateRound{
			{
				RoundNumber: 1,
				Positions: []*loomv1.AgentPosition{
					{AgentId: "a1", Confidence: 0.7},
					{AgentId: "a2", Confidence: 0.9},
				},
				ConsensusReached: true,
			},
		},
		ConsensusAchieved: true,
	}

	evalCtx := FromDebateResult(result)

	assert.True(t, evalCtx.ConsensusReached)
	require.NotNil(t, evalCtx.AverageConfidence)
	assert.InDelta(t, 0.8, *evalCtx.AverageConfidence, 0.01) // (0.7 + 0.9) / 2 = 0.8
}

func TestPolicyEvaluator_ConcurrentSpawns(t *testing.T) {
	evaluator := NewPolicyEvaluator(zaptest.NewLogger(t))

	// Test concurrent spawn recording (race detector will catch issues)
	done := make(chan bool)

	for i := 0; i < 10; i++ {
		go func(id int) {
			role := "concurrent-role"
			evaluator.RecordSpawn(role, 0.01)
			count, cost := evaluator.GetSpawnStats(role)
			_ = count
			_ = cost
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	count, cost := evaluator.GetSpawnStats("concurrent-role")
	assert.Equal(t, 10, count)
	assert.InDelta(t, 0.10, cost, 0.001)
}

// Helper function
func ptrFloat32(v float32) *float32 {
	return &v
}

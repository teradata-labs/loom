// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package collaboration

import (
	"context"
	"fmt"
	"sync"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
)

// PolicyEvaluator evaluates ephemeral agent spawn policies.
// It tracks spawns, costs, and trigger conditions.
type PolicyEvaluator struct {
	mu     sync.RWMutex
	logger *zap.Logger

	// Policy state tracking per workflow execution
	spawnCounts map[string]int     // role -> count
	spawnCosts  map[string]float64 // role -> total cost USD
}

// NewPolicyEvaluator creates a new policy evaluator.
func NewPolicyEvaluator(logger *zap.Logger) *PolicyEvaluator {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PolicyEvaluator{
		logger:      logger,
		spawnCounts: make(map[string]int),
		spawnCosts:  make(map[string]float64),
	}
}

// ShouldSpawn evaluates whether an ephemeral agent should be spawned based on policy and context.
func (e *PolicyEvaluator) ShouldSpawn(ctx context.Context, policy *loomv1.EphemeralAgentPolicy, evalCtx *EvaluationContext) (bool, string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	role := policy.Role

	// Check max spawns
	if policy.MaxSpawns > 0 && e.spawnCounts[role] >= int(policy.MaxSpawns) {
		e.logger.Debug("Ephemeral spawn blocked: max spawns reached",
			zap.String("role", role),
			zap.Int("current", e.spawnCounts[role]),
			zap.Int32("max", policy.MaxSpawns))
		return false, fmt.Sprintf("max spawns (%d) reached for role %s", policy.MaxSpawns, role)
	}

	// Check cost limit
	if policy.CostLimitUsd > 0 && e.spawnCosts[role] >= float64(policy.CostLimitUsd) {
		e.logger.Debug("Ephemeral spawn blocked: cost limit reached",
			zap.String("role", role),
			zap.Float64("current_cost", e.spawnCosts[role]),
			zap.Float32("limit", policy.CostLimitUsd))
		return false, fmt.Sprintf("cost limit ($%.2f) reached for role %s", policy.CostLimitUsd, role)
	}

	// Evaluate trigger
	if !e.evaluateTrigger(policy.Trigger, evalCtx) {
		e.logger.Debug("Ephemeral spawn blocked: trigger not satisfied",
			zap.String("role", role),
			zap.String("trigger_type", policy.Trigger.Type.String()))
		return false, fmt.Sprintf("trigger %s not satisfied", policy.Trigger.Type.String())
	}

	e.logger.Info("Ephemeral spawn approved",
		zap.String("role", role),
		zap.Int("spawn_count", e.spawnCounts[role]+1))

	return true, ""
}

// RecordSpawn records that an ephemeral agent was spawned.
func (e *PolicyEvaluator) RecordSpawn(role string, costUSD float64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.spawnCounts[role]++
	e.spawnCosts[role] += costUSD

	e.logger.Debug("Recorded ephemeral spawn",
		zap.String("role", role),
		zap.Int("total_spawns", e.spawnCounts[role]),
		zap.Float64("total_cost", e.spawnCosts[role]))
}

// GetSpawnStats returns current spawn statistics.
func (e *PolicyEvaluator) GetSpawnStats(role string) (count int, costUSD float64) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.spawnCounts[role], e.spawnCosts[role]
}

// Reset clears all tracking state (call at workflow start).
func (e *PolicyEvaluator) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.spawnCounts = make(map[string]int)
	e.spawnCosts = make(map[string]float64)

	e.logger.Debug("Policy evaluator reset")
}

// evaluateTrigger checks if a spawn trigger is satisfied.
func (e *PolicyEvaluator) evaluateTrigger(trigger *loomv1.SpawnTrigger, ctx *EvaluationContext) bool {
	if trigger == nil {
		return false
	}

	switch trigger.Type {
	case loomv1.SpawnTriggerType_ALWAYS:
		return true

	case loomv1.SpawnTriggerType_CONSENSUS_NOT_REACHED:
		// Check if consensus threshold was met
		return ctx != nil && !ctx.ConsensusReached

	case loomv1.SpawnTriggerType_CONFIDENCE_BELOW:
		// Check if average confidence is below threshold
		if ctx == nil || ctx.AverageConfidence == nil {
			return false
		}
		return *ctx.AverageConfidence < trigger.Threshold

	case loomv1.SpawnTriggerType_TIE_DETECTED:
		// Check if there's a tie in voting
		return ctx != nil && ctx.TieDetected

	case loomv1.SpawnTriggerType_ESCALATION_REQUESTED:
		// Check if explicit escalation was requested
		return ctx != nil && ctx.EscalationRequested

	case loomv1.SpawnTriggerType_CUSTOM:
		// Custom trigger evaluation via expression
		if trigger.Condition == "" {
			e.logger.Warn("Custom trigger has empty condition")
			return false
		}

		result, err := evaluateExpression(trigger.Condition, ctx)
		if err != nil {
			e.logger.Error("Failed to evaluate custom trigger condition",
				zap.String("condition", trigger.Condition),
				zap.Error(err))
			return false
		}

		e.logger.Debug("Custom trigger evaluated",
			zap.String("condition", trigger.Condition),
			zap.Bool("result", result))
		return result

	default:
		e.logger.Warn("Unknown trigger type",
			zap.String("type", trigger.Type.String()))
		return false
	}
}

// EvaluationContext provides context for trigger evaluation.
type EvaluationContext struct {
	// Consensus tracking
	ConsensusReached  bool
	AverageConfidence *float32

	// Voting tracking
	TieDetected      bool
	VoteDistribution map[string]int32
	WinningVoteCount *int32
	TotalVotes       *int32

	// Explicit signals
	EscalationRequested bool

	// Custom fields for expression evaluation
	CustomFields map[string]interface{}
}

// NewEvaluationContext creates a new evaluation context.
func NewEvaluationContext() *EvaluationContext {
	return &EvaluationContext{
		CustomFields: make(map[string]interface{}),
	}
}

// FromSwarmResult creates evaluation context from swarm voting result.
func FromSwarmResult(result *loomv1.SwarmResult) *EvaluationContext {
	ctx := NewEvaluationContext()
	ctx.ConsensusReached = result.ThresholdMet
	ctx.AverageConfidence = &result.AverageConfidence
	ctx.VoteDistribution = result.VoteDistribution

	// Detect tie (two or more choices with same highest vote count)
	maxVotes := int32(0)
	maxCount := 0
	for _, count := range result.VoteDistribution {
		if count > maxVotes {
			maxVotes = count
			maxCount = 1
		} else if count == maxVotes {
			maxCount++
		}
	}
	ctx.TieDetected = maxCount > 1
	ctx.WinningVoteCount = &maxVotes
	totalVotes := int32(len(result.Votes))
	ctx.TotalVotes = &totalVotes

	return ctx
}

// FromDebateResult creates evaluation context from debate result.
func FromDebateResult(result *loomv1.DebateResult) *EvaluationContext {
	ctx := NewEvaluationContext()
	ctx.ConsensusReached = result.ConsensusAchieved

	// Calculate average confidence from final round
	if len(result.Rounds) > 0 {
		lastRound := result.Rounds[len(result.Rounds)-1]
		if len(lastRound.Positions) > 0 {
			var totalConf float32
			for _, pos := range lastRound.Positions {
				totalConf += pos.Confidence
			}
			avgConf := totalConf / float32(len(lastRound.Positions))
			ctx.AverageConfidence = &avgConf
		}
	}

	return ctx
}

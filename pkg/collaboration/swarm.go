// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package collaboration

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

// SwarmOrchestrator manages swarm intelligence voting patterns.
type SwarmOrchestrator struct {
	provider        AgentProvider
	tracer          observability.Tracer
	logger          *zap.Logger
	policyEvaluator *PolicyEvaluator
}

// NewSwarmOrchestrator creates a new swarm orchestrator.
func NewSwarmOrchestrator(provider AgentProvider) *SwarmOrchestrator {
	return NewSwarmOrchestratorWithObservability(provider, observability.NewNoOpTracer(), zap.NewNop())
}

// NewSwarmOrchestratorWithObservability creates a new swarm orchestrator with observability.
func NewSwarmOrchestratorWithObservability(provider AgentProvider, tracer observability.Tracer, logger *zap.Logger) *SwarmOrchestrator {
	return &SwarmOrchestrator{
		provider:        provider,
		tracer:          tracer,
		logger:          logger,
		policyEvaluator: NewPolicyEvaluator(logger),
	}
}

// Execute runs a swarm voting process.
func (s *SwarmOrchestrator) Execute(ctx context.Context, config *loomv1.SwarmPattern) (*loomv1.WorkflowResult, error) {
	startTime := time.Now()

	// Reset policy evaluator for this workflow execution
	s.policyEvaluator.Reset()

	// Start tracing
	ctx, span := s.tracer.StartSpan(ctx, observability.SpanSwarmExecution)
	defer s.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("swarm.question", config.Question)
		span.SetAttribute("swarm.strategy", config.Strategy.String())
		span.SetAttribute("swarm.agent_count", fmt.Sprintf("%d", len(config.AgentIds)))
	}

	s.logger.Info("Starting swarm execution",
		zap.String("question", config.Question),
		zap.String("strategy", config.Strategy.String()),
		zap.Int("agents", len(config.AgentIds)))

	if len(config.AgentIds) < 2 {
		return nil, fmt.Errorf("swarm requires at least 2 agents, got %d", len(config.AgentIds))
	}

	result := &loomv1.WorkflowResult{
		PatternType:  "swarm",
		AgentResults: make([]*loomv1.AgentResult, 0),
		Metadata:     make(map[string]string),
		Cost:         &loomv1.WorkflowCost{AgentCostsUsd: make(map[string]float64)},
	}

	swarmResult := &loomv1.SwarmResult{
		Votes:            make([]*loomv1.SwarmVote, 0),
		VoteDistribution: make(map[string]int32),
	}

	// Phase 1: Collect votes from all agents
	shareVotes := config.ShareVotes
	var firstPassVotes []*loomv1.SwarmVote

	if shareVotes {
		// First pass: collect initial votes
		for _, agentID := range config.AgentIds {
			vote, err := s.collectVote(ctx, agentID, config.Question, nil)
			if err != nil {
				return nil, fmt.Errorf("agent %s failed: %w", agentID, err)
			}
			firstPassVotes = append(firstPassVotes, vote)
		}

		// Second pass: allow agents to see others' votes and reconsider
		for _, agentID := range config.AgentIds {
			vote, err := s.collectVote(ctx, agentID, config.Question, firstPassVotes)
			if err != nil {
				return nil, fmt.Errorf("agent %s failed in second pass: %w", agentID, err)
			}
			swarmResult.Votes = append(swarmResult.Votes, vote)
		}
	} else {
		// Single pass: independent voting
		for _, agentID := range config.AgentIds {
			vote, err := s.collectVote(ctx, agentID, config.Question, nil)
			if err != nil {
				return nil, fmt.Errorf("agent %s failed: %w", agentID, err)
			}
			swarmResult.Votes = append(swarmResult.Votes, vote)
		}
	}

	// Aggregate votes
	s.aggregateVotes(swarmResult)

	// Apply voting strategy
	decision, thresholdMet := s.applyVotingStrategy(swarmResult, config)
	swarmResult.Decision = decision
	swarmResult.ThresholdMet = thresholdMet

	// If threshold not met, consider judge escalation
	if !thresholdMet {
		// Create evaluation context for policy decisions
		evalCtx := FromSwarmResult(swarmResult)

		// Try to use judge (pre-registered or ephemeral based on policies)
		judgeDecision, judgeUsed, err := s.judgeBreakTie(ctx, config, swarmResult, evalCtx)
		if err != nil {
			s.logger.Error("Judge escalation failed", zap.Error(err))
			// Don't fail the whole swarm, just use the current decision
			swarmResult.ConsensusAnalysis = fmt.Sprintf("Judge escalation failed: %s", err.Error())
		} else if judgeUsed {
			swarmResult.Decision = judgeDecision
			swarmResult.ThresholdMet = true
			swarmResult.ConsensusAnalysis = fmt.Sprintf("Decision by judge (threshold not met with strategy %s)", config.Strategy)
		} else {
			swarmResult.ConsensusAnalysis = s.analyzeConsensus(swarmResult, config)
		}
	} else {
		swarmResult.ConsensusAnalysis = s.analyzeConsensus(swarmResult, config)
	}

	// Build agent results
	for _, vote := range swarmResult.Votes {
		agentResult := &loomv1.AgentResult{
			AgentId:         vote.AgentId,
			Output:          vote.Choice,
			ConfidenceScore: vote.Confidence,
			Metadata: map[string]string{
				"reasoning":          vote.Reasoning,
				"alternatives_count": fmt.Sprintf("%d", len(vote.Alternatives)),
			},
		}
		result.AgentResults = append(result.AgentResults, agentResult)
	}

	// Set merged output
	result.MergedOutput = swarmResult.Decision

	// Attach swarm-specific result
	result.CollaborationResult = &loomv1.WorkflowResult_SwarmResult{
		SwarmResult: swarmResult,
	}

	// Calculate metrics
	result.Metrics = s.calculateMetrics(swarmResult)

	// Set metadata
	result.Metadata["question"] = config.Question
	result.Metadata["strategy"] = config.Strategy.String()
	result.Metadata["agent_count"] = fmt.Sprintf("%d", len(config.AgentIds))
	result.Metadata["threshold_met"] = fmt.Sprintf("%t", swarmResult.ThresholdMet)

	duration := time.Since(startTime)
	s.logger.Info("Swarm completed",
		zap.Duration("duration", duration),
		zap.String("decision", swarmResult.Decision),
		zap.Bool("threshold_met", swarmResult.ThresholdMet))

	return result, nil
}

// collectVote gets a vote from an agent.
func (s *SwarmOrchestrator) collectVote(ctx context.Context, agentID, question string, existingVotes []*loomv1.SwarmVote) (*loomv1.SwarmVote, error) {
	a, err := s.provider.GetAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("agent not found: %s: %w", agentID, err)
	}

	prompt := s.buildVotePrompt(question, existingVotes)

	resp, err := a.Chat(ctx, "", prompt)
	if err != nil {
		return nil, fmt.Errorf("agent execution failed: %w", err)
	}

	// Parse vote
	choice, confidence, reasoning, alternatives := s.parseVote(resp.Content)

	return &loomv1.SwarmVote{
		AgentId:      agentID,
		Choice:       choice,
		Confidence:   confidence,
		Reasoning:    reasoning,
		Alternatives: alternatives,
	}, nil
}

// buildVotePrompt constructs the voting prompt.
func (s *SwarmOrchestrator) buildVotePrompt(question string, existingVotes []*loomv1.SwarmVote) string {
	prompt := fmt.Sprintf("# Decision Question\n\n%s\n\n", question)

	if len(existingVotes) > 0 {
		prompt += "## Other Agents' Votes\n\n"
		for _, vote := range existingVotes {
			prompt += fmt.Sprintf("- Agent %s chose \"%s\" (confidence: %.0f%%)\n  Reasoning: %s\n\n",
				vote.AgentId, vote.Choice, vote.Confidence*100, vote.Reasoning)
		}
		prompt += "After considering other agents' votes, "
	} else {
		prompt += "## Your Vote\n\n"
	}

	prompt += `provide your decision in this format:

CHOICE: [Your clear decision/choice]

CONFIDENCE: [0-100]

REASONING: [Brief explanation of your choice]

ALTERNATIVES: [Other options you considered, comma-separated]

Be specific and evidence-based in your reasoning.`

	return prompt
}

// parseVote extracts vote components from agent response.
func (s *SwarmOrchestrator) parseVote(text string) (string, float32, string, []string) {
	choice := ""
	confidence := float32(0.75)
	reasoning := ""
	alternatives := make([]string, 0)

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(strings.ToUpper(line), "CHOICE:") {
			choice = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "CHOICE:"), "choice:"))
		} else if strings.HasPrefix(strings.ToUpper(line), "CONFIDENCE:") {
			confStr := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "CONFIDENCE:"), "confidence:"))
			var conf float64
			_, _ = fmt.Sscanf(confStr, "%f", &conf)
			confidence = float32(conf) / 100.0
		} else if strings.HasPrefix(strings.ToUpper(line), "REASONING:") {
			reasoning = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "REASONING:"), "reasoning:"))
		} else if strings.HasPrefix(strings.ToUpper(line), "ALTERNATIVES:") {
			altStr := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "ALTERNATIVES:"), "alternatives:"))
			for _, alt := range strings.Split(altStr, ",") {
				alt = strings.TrimSpace(alt)
				if alt != "" {
					alternatives = append(alternatives, alt)
				}
			}
		}
	}

	// Fallback
	if choice == "" {
		choice = text
	}

	return choice, confidence, reasoning, alternatives
}

// aggregateVotes counts vote distribution and calculates average confidence.
func (s *SwarmOrchestrator) aggregateVotes(result *loomv1.SwarmResult) {
	totalConfidence := float32(0.0)

	for _, vote := range result.Votes {
		// Normalize choice (case-insensitive, trim)
		normalizedChoice := strings.ToLower(strings.TrimSpace(vote.Choice))
		result.VoteDistribution[normalizedChoice]++
		totalConfidence += vote.Confidence
	}

	if len(result.Votes) > 0 {
		result.AverageConfidence = totalConfidence / float32(len(result.Votes))
	}
}

// applyVotingStrategy determines winning choice based on strategy.
func (s *SwarmOrchestrator) applyVotingStrategy(result *loomv1.SwarmResult, config *loomv1.SwarmPattern) (string, bool) {
	if len(result.Votes) == 0 {
		return "", false
	}

	switch config.Strategy {
	case loomv1.VotingStrategy_MAJORITY:
		return s.applyMajority(result, 0.5)
	case loomv1.VotingStrategy_SUPERMAJORITY:
		return s.applyMajority(result, 0.67)
	case loomv1.VotingStrategy_UNANIMOUS:
		return s.applyMajority(result, 1.0)
	case loomv1.VotingStrategy_WEIGHTED:
		return s.applyWeighted(result, config.ConfidenceThreshold)
	case loomv1.VotingStrategy_RANKED_CHOICE:
		return s.applyRankedChoice(result, config.ConfidenceThreshold)
	default:
		return s.applyMajority(result, 0.5)
	}
}

// applyMajority applies majority/supermajority/unanimous voting.
func (s *SwarmOrchestrator) applyMajority(result *loomv1.SwarmResult, threshold float64) (string, bool) {
	totalVotes := len(result.Votes)
	// Use ceiling for required votes (supermajority needs actual majority)
	requiredVotes := int32(float64(totalVotes) * threshold)
	if float64(totalVotes)*threshold > float64(requiredVotes) {
		requiredVotes++
	}

	// Find choice with most votes
	maxVotes := int32(0)
	winningChoice := ""

	for choice, count := range result.VoteDistribution {
		if count > maxVotes {
			maxVotes = count
			winningChoice = choice
		}
	}

	thresholdMet := maxVotes >= requiredVotes
	return winningChoice, thresholdMet
}

// applyWeighted applies confidence-weighted voting.
func (s *SwarmOrchestrator) applyWeighted(result *loomv1.SwarmResult, confidenceThreshold float32) (string, bool) {
	// Weight votes by confidence scores
	weightedScores := make(map[string]float32)

	for _, vote := range result.Votes {
		normalizedChoice := strings.ToLower(strings.TrimSpace(vote.Choice))
		weightedScores[normalizedChoice] += vote.Confidence
	}

	// Find highest weighted choice
	maxScore := float32(0.0)
	winningChoice := ""

	for choice, score := range weightedScores {
		if score > maxScore {
			maxScore = score
			winningChoice = choice
		}
	}

	// Check if winning choice meets confidence threshold
	thresholdMet := maxScore/float32(len(result.Votes)) >= confidenceThreshold

	return winningChoice, thresholdMet
}

// applyRankedChoice applies ranked choice voting using alternatives.
func (s *SwarmOrchestrator) applyRankedChoice(result *loomv1.SwarmResult, confidenceThreshold float32) (string, bool) {
	// Build ranked preferences from votes
	allChoices := make(map[string]float32)

	for _, vote := range result.Votes {
		// First choice gets full confidence score
		normalizedChoice := strings.ToLower(strings.TrimSpace(vote.Choice))
		allChoices[normalizedChoice] += vote.Confidence

		// Alternatives get fraction of confidence
		for i, alt := range vote.Alternatives {
			normalizedAlt := strings.ToLower(strings.TrimSpace(alt))
			weight := vote.Confidence * float32(len(vote.Alternatives)-i) / float32(len(vote.Alternatives)+1)
			allChoices[normalizedAlt] += weight
		}
	}

	// Find highest scoring choice
	maxScore := float32(0.0)
	winningChoice := ""

	for choice, score := range allChoices {
		if score > maxScore {
			maxScore = score
			winningChoice = choice
		}
	}

	thresholdMet := maxScore/float32(len(result.Votes)) >= confidenceThreshold

	return winningChoice, thresholdMet
}

// judgeBreakTie uses judge agent to break a tie or resolve low confidence.
// Supports both pre-registered judges (via judge_agent_id) and ephemeral judges
// (created on-demand if provider implements AgentFactory).
// Returns: (decision, judgeUsed, error)
func (s *SwarmOrchestrator) judgeBreakTie(ctx context.Context, config *loomv1.SwarmPattern, result *loomv1.SwarmResult, evalCtx *EvaluationContext) (string, bool, error) {
	var judge *agent.Agent
	var ephemeral bool
	var err error

	// Try pre-registered judge first
	if config.JudgeAgentId != "" {
		judge, err = s.provider.GetAgent(ctx, config.JudgeAgentId)
		if err != nil {
			return "", false, fmt.Errorf("judge agent not found: %s: %w", config.JudgeAgentId, err)
		}
		s.logger.Info("Using pre-registered judge", zap.String("judge_id", config.JudgeAgentId))
	} else if factory, ok := s.provider.(AgentFactory); ok {
		// Check if we should spawn ephemeral judge based on policies
		// For now, use a default policy for judge role
		defaultJudgePolicy := &loomv1.EphemeralAgentPolicy{
			Role: "judge",
			Trigger: &loomv1.SpawnTrigger{
				Type:      loomv1.SpawnTriggerType_CONSENSUS_NOT_REACHED,
				Threshold: 0.67,
			},
			MaxSpawns:    1,
			CostLimitUsd: 0.50,
		}

		// Evaluate if we should spawn
		shouldSpawn, reason := s.policyEvaluator.ShouldSpawn(ctx, defaultJudgePolicy, evalCtx)
		if !shouldSpawn {
			s.logger.Info("Judge spawn blocked by policy", zap.String("reason", reason))
			return "", false, nil // Don't use judge, but not an error
		}

		// Create ephemeral judge on-demand
		judge, err = factory.CreateEphemeralAgent(ctx, "judge")
		if err != nil {
			return "", false, fmt.Errorf("failed to create ephemeral judge: %w", err)
		}
		ephemeral = true
		s.logger.Info("Created ephemeral judge agent")
	} else {
		s.logger.Debug("No judge available: no judge_agent_id and provider doesn't support ephemeral agents")
		return "", false, nil // No judge available, but not an error
	}

	// Build summary of votes
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Decision Question\n\n%s\n\n", config.Question))
	sb.WriteString("## All Votes\n\n")

	for _, vote := range result.Votes {
		sb.WriteString(fmt.Sprintf("**Agent %s** chose \"%s\" (confidence: %.0f%%)\n",
			vote.AgentId, vote.Choice, vote.Confidence*100))
		sb.WriteString(fmt.Sprintf("Reasoning: %s\n\n", vote.Reasoning))
	}

	sb.WriteString("\n## Vote Distribution\n\n")
	type voteCount struct {
		choice string
		count  int32
	}
	counts := make([]voteCount, 0)
	for choice, count := range result.VoteDistribution {
		counts = append(counts, voteCount{choice, count})
	}
	sort.Slice(counts, func(i, j int) bool {
		return counts[i].count > counts[j].count
	})
	for _, vc := range counts {
		sb.WriteString(fmt.Sprintf("- %s: %d votes\n", vc.choice, vc.count))
	}

	prompt := fmt.Sprintf(`%s

Based on these votes and reasoning, make the final decision. Consider:
- Weight of evidence in reasoning
- Confidence levels
- Distribution of votes
- Practical implications

Provide the final decision as a single clear choice.`, sb.String())

	resp, err := judge.Chat(ctx, "", prompt)
	if err != nil {
		return "", true, fmt.Errorf("judge execution failed: %w", err)
	}

	// Record spawn if ephemeral (with actual cost from response)
	if ephemeral {
		costUSD := resp.Usage.CostUSD
		s.policyEvaluator.RecordSpawn("judge", costUSD)
		s.logger.Info("Recorded ephemeral judge spawn",
			zap.Float64("cost_usd", costUSD))
	}

	return strings.TrimSpace(resp.Content), true, nil
}

// analyzeConsensus provides analysis of the voting consensus.
func (s *SwarmOrchestrator) analyzeConsensus(result *loomv1.SwarmResult, config *loomv1.SwarmPattern) string {
	totalVotes := len(result.Votes)
	if totalVotes == 0 {
		return "No votes collected"
	}

	winningCount := result.VoteDistribution[strings.ToLower(result.Decision)]
	percentage := float64(winningCount) / float64(totalVotes) * 100

	analysis := fmt.Sprintf("Decision: %s (%d/%d votes, %.1f%%)\n",
		result.Decision, winningCount, totalVotes, percentage)

	analysis += fmt.Sprintf("Strategy: %s\n", config.Strategy)
	analysis += fmt.Sprintf("Average confidence: %.1f%%\n", result.AverageConfidence*100)

	if result.ThresholdMet {
		analysis += "Threshold: MET"
	} else {
		analysis += "Threshold: NOT MET"
	}

	return analysis
}

// calculateMetrics computes collaboration quality metrics.
func (s *SwarmOrchestrator) calculateMetrics(result *loomv1.SwarmResult) *loomv1.CollaborationMetrics {
	if len(result.Votes) == 0 {
		return &loomv1.CollaborationMetrics{}
	}

	// Calculate perspective diversity (how many different choices)
	diversityScore := float32(len(result.VoteDistribution)) / float32(len(result.Votes))

	// Calculate agreement level (inverse of diversity)
	agreementLevel := 1.0 - diversityScore

	// Calculate confidence variance
	var confidenceSum, confidenceVarSum float32
	for _, vote := range result.Votes {
		confidenceSum += vote.Confidence
	}
	avgConf := confidenceSum / float32(len(result.Votes))

	for _, vote := range result.Votes {
		diff := vote.Confidence - avgConf
		confidenceVarSum += diff * diff
	}
	confidenceVariance := confidenceVarSum / float32(len(result.Votes))

	return &loomv1.CollaborationMetrics{
		PerspectiveDiversity: diversityScore,
		AgreementLevel:       agreementLevel,
		AvgResponseLength:    0, // Not applicable for voting
		InteractionCount:     int32(len(result.Votes)),
		TimeToConsensusMs:    0, // Would need timing
		ConfidenceVariance:   confidenceVariance,
	}
}

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
)

// SwarmExecutor executes a swarm pattern (collective voting).
type SwarmExecutor struct {
	orchestrator *Orchestrator
	pattern      *loomv1.SwarmPattern
}

// NewSwarmExecutor creates a new swarm executor.
func NewSwarmExecutor(orchestrator *Orchestrator, pattern *loomv1.SwarmPattern) *SwarmExecutor {
	return &SwarmExecutor{
		orchestrator: orchestrator,
		pattern:      pattern,
	}
}

// Execute runs the swarm pattern and returns the voting result.
func (e *SwarmExecutor) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
	startTime := time.Now()

	// Generate unique workflow ID
	workflowID := fmt.Sprintf("swarm-%s", uuid.New().String()[:8])

	// Start workflow-level span
	ctx, workflowSpan := e.orchestrator.tracer.StartSpan(ctx, "workflow.swarm")
	defer e.orchestrator.tracer.EndSpan(workflowSpan)

	if workflowSpan != nil {
		workflowSpan.SetAttribute("workflow.type", "swarm")
		workflowSpan.SetAttribute("workflow.id", workflowID)
		workflowSpan.SetAttribute("swarm.question", truncateForLog(e.pattern.Question, 100))
		workflowSpan.SetAttribute("swarm.agent_count", fmt.Sprintf("%d", len(e.pattern.AgentIds)))
		workflowSpan.SetAttribute("swarm.strategy", e.pattern.Strategy.String())
		workflowSpan.SetAttribute("swarm.share_votes", fmt.Sprintf("%t", e.pattern.ShareVotes))
	}

	e.orchestrator.logger.Info("Starting swarm voting execution",
		zap.String("question", truncateForLog(e.pattern.Question, 100)),
		zap.Int("agents", len(e.pattern.AgentIds)),
		zap.String("strategy", e.pattern.Strategy.String()))

	// Validate agents exist
	for _, agentID := range e.pattern.AgentIds {
		if _, err := e.orchestrator.GetAgent(ctx, agentID); err != nil {
			return nil, fmt.Errorf("agent not found: %s: %w", agentID, err)
		}
	}

	// Execute voting (parallel or sequential based on share_votes)
	var votes []*loomv1.SwarmVote
	var modelsUsed map[string]string
	var voteCosts map[string]*loomv1.AgentExecutionCost
	var err error

	if e.pattern.ShareVotes {
		// Sequential voting - agents see previous votes
		votes, modelsUsed, voteCosts, err = e.executeSequentialVoting(ctx, workflowID)
	} else {
		// Parallel voting - independent votes
		votes, modelsUsed, voteCosts, err = e.executeVoting(ctx, workflowID)
	}

	if err != nil {
		return nil, fmt.Errorf("voting execution failed: %w", err)
	}

	// Aggregate votes based on strategy
	result, err := e.aggregateVotes(ctx, votes)
	if err != nil {
		return nil, fmt.Errorf("vote aggregation failed: %w", err)
	}

	// Format swarm result as agent results
	agentResults := e.formatAgentResults(votes, voteCosts)

	// Calculate total cost
	cost := e.calculateCost(agentResults)

	duration := time.Since(startTime)
	e.orchestrator.logger.Info("Swarm voting completed",
		zap.Duration("duration", duration),
		zap.String("decision", result.Decision),
		zap.Bool("threshold_met", result.ThresholdMet),
		zap.Float64("total_cost_usd", cost.TotalCostUsd))

	return &loomv1.WorkflowResult{
		PatternType:  "swarm",
		AgentResults: agentResults,
		MergedOutput: result.Decision,
		Metadata: map[string]string{
			"agent_count":        fmt.Sprintf("%d", len(e.pattern.AgentIds)),
			"voting_strategy":    e.pattern.Strategy.String(),
			"threshold_met":      fmt.Sprintf("%t", result.ThresholdMet),
			"average_confidence": fmt.Sprintf("%.2f", result.AverageConfidence),
		},
		DurationMs: duration.Milliseconds(),
		Cost:       cost,
		ModelsUsed: modelsUsed,
	}, nil
}

// executeSequentialVoting runs agents sequentially, sharing previous votes (collaborative voting).
func (e *SwarmExecutor) executeSequentialVoting(ctx context.Context, workflowID string) ([]*loomv1.SwarmVote, map[string]string, map[string]*loomv1.AgentExecutionCost, error) {
	votes := make([]*loomv1.SwarmVote, 0, len(e.pattern.AgentIds))
	modelsUsed := make(map[string]string)
	voteCosts := make(map[string]*loomv1.AgentExecutionCost)

	// Vote sequentially
	for idx, agentID := range e.pattern.AgentIds {
		// Create vote span
		voteCtx, voteSpan := e.orchestrator.tracer.StartSpan(ctx, fmt.Sprintf("swarm.vote.%d", idx+1))
		if voteSpan != nil {
			voteSpan.SetAttribute("vote.number", fmt.Sprintf("%d", idx+1))
			voteSpan.SetAttribute("vote.agent_id", agentID)
			voteSpan.SetAttribute("collaborative", "true")
		}

		// Collect vote with previous votes visible
		vote, model, cost, err := e.collectVoteWithContext(voteCtx, workflowID, agentID, idx+1, votes)
		e.orchestrator.tracer.EndSpan(voteSpan)

		if err != nil {
			e.orchestrator.logger.Error("Collaborative vote collection failed",
				zap.String("agent_id", agentID),
				zap.Int("vote_number", idx+1),
				zap.Error(err))
			return nil, nil, nil, fmt.Errorf("agent %s: %w", agentID, err)
		}

		votes = append(votes, vote)
		voteCosts[agentID] = cost

		if model != "" {
			modelsUsed[agentID] = model
		}

		e.orchestrator.logger.Debug("Collaborative vote collected",
			zap.String("agent_id", agentID),
			zap.String("choice", vote.Choice),
			zap.Float32("confidence", vote.Confidence))
	}

	return votes, modelsUsed, voteCosts, nil
}

// executeVoting runs all agents in parallel to collect votes (independent voting).
func (e *SwarmExecutor) executeVoting(ctx context.Context, workflowID string) ([]*loomv1.SwarmVote, map[string]string, map[string]*loomv1.AgentExecutionCost, error) {
	var wg sync.WaitGroup
	var modelsMu sync.Mutex
	votesChan := make(chan *loomv1.SwarmVote, len(e.pattern.AgentIds))
	errorsChan := make(chan error, len(e.pattern.AgentIds))
	costsChan := make(chan struct {
		agentID string
		cost    *loomv1.AgentExecutionCost
	}, len(e.pattern.AgentIds))
	modelsUsed := make(map[string]string)

	// Launch goroutine for each agent
	for idx, agentID := range e.pattern.AgentIds {
		wg.Add(1)
		go func(voteIdx int, id string) {
			defer wg.Done()

			// Create vote span
			voteCtx, voteSpan := e.orchestrator.tracer.StartSpan(ctx, fmt.Sprintf("swarm.vote.%d", voteIdx+1))
			if voteSpan != nil {
				voteSpan.SetAttribute("vote.number", fmt.Sprintf("%d", voteIdx+1))
				voteSpan.SetAttribute("vote.agent_id", id)
			}

			vote, model, cost, err := e.collectVote(voteCtx, workflowID, id, voteIdx+1)

			e.orchestrator.tracer.EndSpan(voteSpan)

			if err != nil {
				e.orchestrator.logger.Error("Vote collection failed",
					zap.String("agent_id", id),
					zap.Error(err))
				errorsChan <- fmt.Errorf("agent %s: %w", id, err)
				return
			}

			votesChan <- vote
			costsChan <- struct {
				agentID string
				cost    *loomv1.AgentExecutionCost
			}{id, cost}

			// Track model used
			if model != "" {
				modelsMu.Lock()
				modelsUsed[id] = model
				modelsMu.Unlock()
			}
		}(idx, agentID)
	}

	// Wait for all votes
	wg.Wait()
	close(votesChan)
	close(errorsChan)
	close(costsChan)

	// Check for errors
	if len(errorsChan) > 0 {
		return nil, nil, nil, <-errorsChan
	}

	// Collect all votes
	votes := make([]*loomv1.SwarmVote, 0, len(e.pattern.AgentIds))
	for vote := range votesChan {
		votes = append(votes, vote)
	}

	// Collect all costs
	voteCosts := make(map[string]*loomv1.AgentExecutionCost)
	for costInfo := range costsChan {
		voteCosts[costInfo.agentID] = costInfo.cost
	}

	return votes, modelsUsed, voteCosts, nil
}

// collectVoteWithContext executes an agent with previous votes visible (collaborative voting).
func (e *SwarmExecutor) collectVoteWithContext(ctx context.Context, workflowID, agentID string, voteNumber int, previousVotes []*loomv1.SwarmVote) (*loomv1.SwarmVote, string, *loomv1.AgentExecutionCost, error) {
	// Get agent
	agt, err := e.orchestrator.GetAgent(ctx, agentID)
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to get agent: %w", err)
	}

	// Build voting prompt with previous votes
	prompt := e.buildCollaborativeVotingPrompt(agentID, voteNumber, previousVotes)

	// Create unique session ID
	sessionID := fmt.Sprintf("%s-vote-%d", workflowID, voteNumber)

	// Execute agent
	ctx, agentSpan := e.orchestrator.tracer.StartSpan(ctx, fmt.Sprintf("swarm.agent.%s", agentID))
	if agentSpan != nil {
		agentSpan.SetAttribute("agent.id", agentID)
		agentSpan.SetAttribute("vote.number", fmt.Sprintf("%d", voteNumber))
		agentSpan.SetAttribute("previous_votes", fmt.Sprintf("%d", len(previousVotes)))
	}

	response, err := agt.Chat(ctx, sessionID, prompt)
	e.orchestrator.tracer.EndSpan(agentSpan)

	if err != nil {
		return nil, "", nil, fmt.Errorf("agent chat failed: %w", err)
	}

	// Parse vote from output
	vote := e.parseVote(agentID, response.Content)

	// Get model info from agent
	modelName := agt.GetLLMModel()

	// Build cost info
	cost := &loomv1.AgentExecutionCost{
		TotalTokens:  int32(response.Usage.TotalTokens),
		InputTokens:  int32(response.Usage.InputTokens),
		OutputTokens: int32(response.Usage.OutputTokens),
		CostUsd:      response.Usage.CostUSD,
	}

	return vote, modelName, cost, nil
}

// collectVote executes a single agent to collect its vote (independent voting).
func (e *SwarmExecutor) collectVote(ctx context.Context, workflowID, agentID string, voteNumber int) (*loomv1.SwarmVote, string, *loomv1.AgentExecutionCost, error) {
	// Get agent
	agt, err := e.orchestrator.GetAgent(ctx, agentID)
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to get agent: %w", err)
	}

	// Build voting prompt
	prompt := e.buildVotingPrompt(agentID, voteNumber)

	// Create unique session ID
	sessionID := fmt.Sprintf("%s-vote-%d", workflowID, voteNumber)

	// Execute agent
	ctx, agentSpan := e.orchestrator.tracer.StartSpan(ctx, fmt.Sprintf("swarm.agent.%s", agentID))
	if agentSpan != nil {
		agentSpan.SetAttribute("agent.id", agentID)
		agentSpan.SetAttribute("vote.number", fmt.Sprintf("%d", voteNumber))
	}

	response, err := agt.Chat(ctx, sessionID, prompt)
	e.orchestrator.tracer.EndSpan(agentSpan)

	if err != nil {
		return nil, "", nil, fmt.Errorf("agent chat failed: %w", err)
	}

	// Parse vote from output
	vote := e.parseVote(agentID, response.Content)

	// Get model info from agent
	modelName := agt.GetLLMModel()

	// Build cost info
	cost := &loomv1.AgentExecutionCost{
		TotalTokens:  int32(response.Usage.TotalTokens),
		InputTokens:  int32(response.Usage.InputTokens),
		OutputTokens: int32(response.Usage.OutputTokens),
		CostUsd:      response.Usage.CostUSD,
	}

	return vote, modelName, cost, nil
}

// buildCollaborativeVotingPrompt constructs a prompt with previous votes visible.
func (e *SwarmExecutor) buildCollaborativeVotingPrompt(agentID string, voteNumber int, previousVotes []*loomv1.SwarmVote) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("You are participating in a collaborative swarm voting process (Vote #%d).\n\n", voteNumber))
	sb.WriteString(fmt.Sprintf("Question: %s\n\n", e.pattern.Question))

	// Include previous votes
	if len(previousVotes) > 0 {
		sb.WriteString("Previous votes from other agents:\n")
		for i, vote := range previousVotes {
			sb.WriteString(fmt.Sprintf("%d. Agent %s voted: %s (confidence: %.2f)\n", i+1, vote.AgentId, vote.Choice, vote.Confidence))
			if vote.Reasoning != "" {
				// Truncate long reasoning
				reasoning := vote.Reasoning
				if len(reasoning) > 100 {
					reasoning = reasoning[:100] + "..."
				}
				sb.WriteString(fmt.Sprintf("   Reasoning: %s\n", reasoning))
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Now provide your vote, considering the previous votes:\n\n")
	sb.WriteString("VOTE: <your choice>\n")
	sb.WriteString("CONFIDENCE: <0.0-1.0>\n")
	sb.WriteString("REASONING: <your reasoning>\n\n")
	sb.WriteString("Your choice should be a single, clear answer to the question.\n")
	sb.WriteString("Confidence should be a number between 0.0 (no confidence) and 1.0 (complete confidence).\n")
	sb.WriteString("Provide detailed reasoning for your vote, taking into account the previous votes.\n")

	return sb.String()
}

// buildVotingPrompt constructs the prompt for an agent to cast an independent vote.
func (e *SwarmExecutor) buildVotingPrompt(agentID string, voteNumber int) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("You are participating in a swarm voting process (Vote #%d).\n\n", voteNumber))
	sb.WriteString(fmt.Sprintf("Question: %s\n\n", e.pattern.Question))
	sb.WriteString("Please evaluate this question and provide your vote in the following format:\n\n")
	sb.WriteString("VOTE: <your choice>\n")
	sb.WriteString("CONFIDENCE: <0.0-1.0>\n")
	sb.WriteString("REASONING: <your reasoning>\n\n")
	sb.WriteString("Your choice should be a single, clear answer to the question.\n")
	sb.WriteString("Confidence should be a number between 0.0 (no confidence) and 1.0 (complete confidence).\n")
	sb.WriteString("Provide detailed reasoning for your vote.\n")

	return sb.String()
}

// parseVote extracts voting information from agent output.
func (e *SwarmExecutor) parseVote(agentID, output string) *loomv1.SwarmVote {
	vote := &loomv1.SwarmVote{
		AgentId:    agentID,
		Choice:     "abstain",
		Confidence: 0.5,
		Reasoning:  output,
	}

	lines := strings.Split(output, "\n")
	reasoningLines := make([]string, 0)
	inReasoning := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		lineUpper := strings.ToUpper(line)

		if strings.HasPrefix(lineUpper, "VOTE:") {
			// Extract choice (case-insensitive)
			vote.Choice = strings.TrimSpace(line[5:]) // Skip "VOTE:"
			inReasoning = false
		} else if strings.HasPrefix(lineUpper, "CONFIDENCE:") {
			// Extract confidence (case-insensitive)
			confStr := strings.TrimSpace(line[11:]) // Skip "CONFIDENCE:"
			var conf float32
			n, _ := fmt.Sscanf(confStr, "%f", &conf)
			// Only update confidence if parsing succeeded and value is valid
			if n == 1 && conf >= 0.0 && conf <= 1.0 {
				vote.Confidence = conf
			}
			inReasoning = false
		} else if strings.HasPrefix(lineUpper, "REASONING:") {
			// Extract reasoning (case-insensitive)
			reasoningText := strings.TrimSpace(line[10:]) // Skip "REASONING:"
			if reasoningText != "" {
				reasoningLines = append(reasoningLines, reasoningText)
			}
			inReasoning = true
		} else if inReasoning && line != "" {
			// Continue collecting multiline reasoning
			reasoningLines = append(reasoningLines, line)
		}
	}

	// Join multiline reasoning
	if len(reasoningLines) > 0 {
		vote.Reasoning = strings.Join(reasoningLines, "\n")
	}

	return vote
}

// aggregateVotes applies the voting strategy to determine the result.
func (e *SwarmExecutor) aggregateVotes(ctx context.Context, votes []*loomv1.SwarmVote) (*loomv1.SwarmResult, error) {
	if len(votes) == 0 {
		return nil, fmt.Errorf("no votes collected")
	}

	// Count votes and track distribution
	voteDistribution := make(map[string]int32)
	var totalConfidence float32
	for _, vote := range votes {
		voteDistribution[vote.Choice]++
		totalConfidence += vote.Confidence
	}

	avgConfidence := totalConfidence / float32(len(votes))

	// Apply voting strategy
	decision, thresholdMet := e.applyVotingStrategy(votes, voteDistribution)

	// Check for tie and invoke judge if needed
	if e.pattern.JudgeAgentId != "" && e.hasTie(voteDistribution) {
		e.orchestrator.logger.Info("Tie detected, invoking judge agent",
			zap.String("judge_agent_id", e.pattern.JudgeAgentId))

		judgeDecision, err := e.invokeJudge(ctx, votes, voteDistribution)
		if err != nil {
			e.orchestrator.logger.Warn("Judge invocation failed, using original decision",
				zap.Error(err))
		} else {
			e.orchestrator.logger.Info("Judge decision received",
				zap.String("original_decision", decision),
				zap.String("judge_decision", judgeDecision))
			decision = judgeDecision
			// Recalculate threshold for judge's decision
			if count, ok := voteDistribution[decision]; ok {
				thresholdMet = count > (int32(len(votes)) / 2)
			}
		}
	}

	// Generate consensus analysis
	consensusAnalysis := e.generateConsensusAnalysis(votes, voteDistribution, decision)

	return &loomv1.SwarmResult{
		Decision:          decision,
		Votes:             votes,
		VoteDistribution:  voteDistribution,
		AverageConfidence: avgConfidence,
		ThresholdMet:      thresholdMet,
		ConsensusAnalysis: consensusAnalysis,
	}, nil
}

// applyVotingStrategy determines the winning choice based on the configured strategy.
func (e *SwarmExecutor) applyVotingStrategy(votes []*loomv1.SwarmVote, distribution map[string]int32) (string, bool) {
	totalVotes := int32(len(votes))

	// Find the choice with most votes
	var winningChoice string
	var maxVotes int32
	for choice, count := range distribution {
		if count > maxVotes {
			maxVotes = count
			winningChoice = choice
		}
	}

	// Apply strategy-specific threshold
	var thresholdMet bool
	switch e.pattern.Strategy {
	case loomv1.VotingStrategy_MAJORITY:
		// Simple majority (>50%)
		thresholdMet = maxVotes > (totalVotes / 2)

	case loomv1.VotingStrategy_SUPERMAJORITY:
		// Supermajority (>=2/3)
		thresholdMet = maxVotes >= ((totalVotes * 2) / 3)

	case loomv1.VotingStrategy_UNANIMOUS:
		// Unanimous (100%)
		thresholdMet = maxVotes == totalVotes

	case loomv1.VotingStrategy_WEIGHTED:
		// Weighted by confidence - check if confidence threshold met
		var weightedScore float32
		for _, vote := range votes {
			if vote.Choice == winningChoice {
				weightedScore += vote.Confidence
			}
		}
		avgWeightedConf := weightedScore / float32(maxVotes)
		thresholdMet = avgWeightedConf >= e.pattern.ConfidenceThreshold

	case loomv1.VotingStrategy_RANKED_CHOICE:
		// Ranked choice - select choice with highest cumulative confidence score
		// (different from WEIGHTED which checks average confidence of winner against threshold)
		confidenceScores := make(map[string]float32)
		for _, vote := range votes {
			confidenceScores[vote.Choice] += vote.Confidence
		}

		// Find choice with highest total confidence
		var highestScore float32
		rankedWinner := winningChoice
		for choice, score := range confidenceScores {
			if score > highestScore {
				highestScore = score
				rankedWinner = choice
			}
		}

		// Use ranked winner instead of count-based winner
		winningChoice = rankedWinner

		// Threshold met if winner has >50% of total confidence points available
		totalConfidenceAvailable := float32(totalVotes) // Max possible confidence is 1.0 per vote
		thresholdMet = highestScore > (totalConfidenceAvailable / 2)

	default:
		// Simple majority as fallback
		thresholdMet = maxVotes > (totalVotes / 2)
	}

	return winningChoice, thresholdMet
}

// hasTie checks if there's a tie in the vote distribution (two or more choices with same max votes).
func (e *SwarmExecutor) hasTie(distribution map[string]int32) bool {
	var maxVotes int32
	maxCount := 0

	for _, count := range distribution {
		if count > maxVotes {
			maxVotes = count
			maxCount = 1
		} else if count == maxVotes {
			maxCount++
		}
	}

	return maxCount > 1
}

// invokeJudge calls the judge agent to break a tie.
func (e *SwarmExecutor) invokeJudge(ctx context.Context, votes []*loomv1.SwarmVote, distribution map[string]int32) (string, error) {
	// Get judge agent
	judge, err := e.orchestrator.GetAgent(ctx, e.pattern.JudgeAgentId)
	if err != nil {
		return "", fmt.Errorf("failed to get judge agent: %w", err)
	}

	// Build prompt for judge
	var sb strings.Builder
	sb.WriteString("You are acting as a judge to break a tie in a swarm voting process.\n\n")
	sb.WriteString(fmt.Sprintf("Question: %s\n\n", e.pattern.Question))
	sb.WriteString("Vote distribution (tied):\n")

	// Show tied options
	for choice, count := range distribution {
		sb.WriteString(fmt.Sprintf("- %s: %d votes\n", choice, count))
	}
	sb.WriteString("\n")

	// Show all votes with reasoning
	sb.WriteString("Individual votes:\n")
	for i, vote := range votes {
		sb.WriteString(fmt.Sprintf("%d. %s (confidence: %.2f)\n", i+1, vote.Choice, vote.Confidence))
		if vote.Reasoning != "" {
			reasoning := vote.Reasoning
			if len(reasoning) > 150 {
				reasoning = reasoning[:150] + "..."
			}
			sb.WriteString(fmt.Sprintf("   Reasoning: %s\n", reasoning))
		}
	}
	sb.WriteString("\n")
	sb.WriteString("As the judge, please make the final decision. Respond with only the choice you select, nothing else.\n")

	// Execute judge
	sessionID := fmt.Sprintf("swarm-judge-%s", uuid.New().String()[:8])
	response, err := judge.Chat(ctx, sessionID, sb.String())
	if err != nil {
		return "", fmt.Errorf("judge chat failed: %w", err)
	}

	// Parse judge's decision (should be a simple choice)
	decision := strings.TrimSpace(response.Content)

	// Validate that judge picked one of the actual choices
	if _, exists := distribution[decision]; !exists {
		// Judge didn't pick a valid choice, log and return error
		e.orchestrator.logger.Warn("Judge selected invalid choice",
			zap.String("judge_choice", decision))
		return "", fmt.Errorf("judge selected invalid choice: %s", decision)
	}

	return decision, nil
}

// generateConsensusAnalysis creates a summary of the voting results.
func (e *SwarmExecutor) generateConsensusAnalysis(votes []*loomv1.SwarmVote, distribution map[string]int32, decision string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Swarm voting completed with %d agents.\n", len(votes)))
	sb.WriteString(fmt.Sprintf("Winning decision: %s\n\n", decision))
	sb.WriteString("Vote distribution:\n")
	for choice, count := range distribution {
		percentage := float32(count) / float32(len(votes)) * 100
		sb.WriteString(fmt.Sprintf("  - %s: %d votes (%.1f%%)\n", choice, count, percentage))
	}
	return sb.String()
}

// formatAgentResults converts swarm votes to agent results for workflow result.
func (e *SwarmExecutor) formatAgentResults(votes []*loomv1.SwarmVote, voteCosts map[string]*loomv1.AgentExecutionCost) []*loomv1.AgentResult {
	results := make([]*loomv1.AgentResult, len(votes))
	for i, vote := range votes {
		results[i] = &loomv1.AgentResult{
			AgentId: vote.AgentId,
			Output:  fmt.Sprintf("VOTE: %s\nCONFIDENCE: %.2f\nREASONING: %s", vote.Choice, vote.Confidence, vote.Reasoning),
			Cost:    voteCosts[vote.AgentId],
		}
	}
	return results
}

// calculateCost sums up the costs from all agent results.
func (e *SwarmExecutor) calculateCost(results []*loomv1.AgentResult) *loomv1.WorkflowCost {
	var totalCostUsd float64
	var totalTokens int32

	for _, result := range results {
		if result.Cost != nil {
			totalCostUsd += result.Cost.CostUsd
			totalTokens += result.Cost.TotalTokens
		}
	}

	return &loomv1.WorkflowCost{
		TotalCostUsd:  totalCostUsd,
		TotalTokens:   totalTokens,
		AgentCostsUsd: make(map[string]float64), // Could populate if needed
		LlmCalls:      int32(len(results)),
	}
}

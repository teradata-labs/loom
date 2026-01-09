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
	"github.com/teradata-labs/loom/pkg/agent"
	"go.uber.org/zap"
)

// ForkJoinExecutor executes a fork-join pattern.
type ForkJoinExecutor struct {
	orchestrator *Orchestrator
	pattern      *loomv1.ForkJoinPattern
}

// NewForkJoinExecutor creates a new fork-join executor.
func NewForkJoinExecutor(orchestrator *Orchestrator, pattern *loomv1.ForkJoinPattern) *ForkJoinExecutor {
	return &ForkJoinExecutor{
		orchestrator: orchestrator,
		pattern:      pattern,
	}
}

// Execute runs the fork-join pattern and returns the result.
func (e *ForkJoinExecutor) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
	startTime := time.Now()

	// Generate unique workflow ID for session tracking
	workflowID := fmt.Sprintf("fork-join-%s", uuid.New().String()[:8])

	// Start workflow-level span
	ctx, workflowSpan := e.orchestrator.tracer.StartSpan(ctx, "workflow.fork_join")
	defer e.orchestrator.tracer.EndSpan(workflowSpan)

	if workflowSpan != nil {
		workflowSpan.SetAttribute("workflow.type", "fork_join")
		workflowSpan.SetAttribute("workflow.id", workflowID)
		workflowSpan.SetAttribute("fork_join.prompt", truncateForLog(e.pattern.Prompt, 100))
		workflowSpan.SetAttribute("fork_join.agent_count", fmt.Sprintf("%d", len(e.pattern.AgentIds)))
		workflowSpan.SetAttribute("fork_join.merge_strategy", e.pattern.MergeStrategy.String())
	}

	e.orchestrator.logger.Info("Starting fork-join execution",
		zap.String("prompt", truncateForLog(e.pattern.Prompt, 100)),
		zap.Int("agents", len(e.pattern.AgentIds)))

	// Validate agents exist
	for _, agentID := range e.pattern.AgentIds {
		if _, err := e.orchestrator.GetAgent(ctx, agentID); err != nil {
			return nil, fmt.Errorf("agent not found: %s: %w", agentID, err)
		}
	}

	// Create timeout context if specified
	executeCtx := ctx
	if e.pattern.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		executeCtx, cancel = context.WithTimeout(ctx, time.Duration(e.pattern.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	// Execute all agents in parallel with fork span
	ctx, forkSpan := e.orchestrator.tracer.StartSpan(ctx, "fork_join.fork")
	if forkSpan != nil {
		forkSpan.SetAttribute("branch.count", fmt.Sprintf("%d", len(e.pattern.AgentIds)))
	}

	results, modelsUsed, err := e.executeFork(executeCtx, workflowID)
	e.orchestrator.tracer.EndSpan(forkSpan)

	if err != nil {
		return nil, fmt.Errorf("fork execution failed: %w", err)
	}

	// Merge results using specified strategy with join span
	ctx, joinSpan := e.orchestrator.tracer.StartSpan(ctx, "fork_join.join")
	if joinSpan != nil {
		joinSpan.SetAttribute("merge.strategy", e.pattern.MergeStrategy.String())
		joinSpan.SetAttribute("branch.count", fmt.Sprintf("%d", len(results)))
	}

	mergedOutput, err := e.mergeResults(ctx, workflowID, results)
	e.orchestrator.tracer.EndSpan(joinSpan)

	if err != nil {
		return nil, fmt.Errorf("failed to merge fork-join results: %w", err)
	}

	// Calculate total cost
	cost := e.calculateCost(results)

	duration := time.Since(startTime)
	e.orchestrator.logger.Info("Fork-join completed",
		zap.Duration("duration", duration),
		zap.Float64("total_cost_usd", cost.TotalCostUsd))

	return &loomv1.WorkflowResult{
		PatternType:  "fork_join",
		AgentResults: results,
		MergedOutput: mergedOutput,
		Metadata: map[string]string{
			"agent_count":    fmt.Sprintf("%d", len(e.pattern.AgentIds)),
			"merge_strategy": e.pattern.MergeStrategy.String(),
		},
		DurationMs: duration.Milliseconds(),
		Cost:       cost,
		ModelsUsed: modelsUsed,
	}, nil
}

// executeFork runs all agents in parallel and collects their results.
func (e *ForkJoinExecutor) executeFork(ctx context.Context, workflowID string) ([]*loomv1.AgentResult, map[string]string, error) {
	var wg sync.WaitGroup
	var modelsMu sync.Mutex
	resultsChan := make(chan *loomv1.AgentResult, len(e.pattern.AgentIds))
	errorsChan := make(chan error, len(e.pattern.AgentIds))
	modelsUsed := make(map[string]string)

	// Launch goroutine for each agent
	for idx, agentID := range e.pattern.AgentIds {
		wg.Add(1)
		go func(branchIdx int, id string) {
			defer wg.Done()

			// Create branch span
			branchCtx, branchSpan := e.orchestrator.tracer.StartSpan(ctx, fmt.Sprintf("fork_join.branch.%d", branchIdx+1))
			if branchSpan != nil {
				branchSpan.SetAttribute("branch.number", fmt.Sprintf("%d", branchIdx+1))
				branchSpan.SetAttribute("branch.agent_id", id)
			}

			result, model, err := e.executeAgentWithSpan(branchCtx, workflowID, id, e.pattern.Prompt, branchIdx+1)

			e.orchestrator.tracer.EndSpan(branchSpan)

			if err != nil {
				e.orchestrator.logger.Error("Agent execution failed in fork-join",
					zap.String("agent_id", id),
					zap.Error(err))
				errorsChan <- fmt.Errorf("agent %s failed: %w", id, err)
				// Still send a result with error information
				result = &loomv1.AgentResult{
					AgentId: id,
					Output:  fmt.Sprintf("Error: %s", err.Error()),
					Metadata: map[string]string{
						"error": err.Error(),
					},
					ConfidenceScore: 0.0,
				}
			} else if model != "" {
				// Track model used (thread-safe)
				modelsMu.Lock()
				modelsUsed[id] = model
				modelsMu.Unlock()
			}
			resultsChan <- result
		}(idx, agentID)
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(resultsChan)
	close(errorsChan)

	// Collect results
	results := make([]*loomv1.AgentResult, 0, len(e.pattern.AgentIds))
	for result := range resultsChan {
		results = append(results, result)
	}

	// Check for errors
	errors := make([]error, 0)
	for err := range errorsChan {
		errors = append(errors, err)
	}

	// If all agents failed, return error
	if len(errors) == len(e.pattern.AgentIds) {
		return nil, nil, fmt.Errorf("all agents failed: %v", errors)
	}

	// Log partial failures
	if len(errors) > 0 {
		e.orchestrator.logger.Warn("Some agents failed in fork-join",
			zap.Int("failed_count", len(errors)),
			zap.Int("total_count", len(e.pattern.AgentIds)))
	}

	return results, modelsUsed, nil
}

// executeAgentWithSpan runs a single agent with comprehensive observability.
func (e *ForkJoinExecutor) executeAgentWithSpan(ctx context.Context, workflowID string, agentID string, prompt string, branchNum int) (*loomv1.AgentResult, string, error) {
	startTime := time.Now()

	// Get agent from orchestrator
	ag, err := e.orchestrator.GetAgent(ctx, agentID)
	if err != nil {
		return nil, "", err
	}

	// Create trace span for agent execution
	ctx, agentSpan := e.orchestrator.tracer.StartSpan(ctx, fmt.Sprintf("fork_join.agent.%s", agentID))
	defer e.orchestrator.tracer.EndSpan(agentSpan)

	// Generate unique session ID for this fork-join branch
	sessionID := fmt.Sprintf("%s-branch%d-%s", workflowID, branchNum, agentID)

	if agentSpan != nil {
		agentSpan.SetAttribute("agent.id", agentID)
		agentSpan.SetAttribute("agent.name", ag.GetName())
		agentSpan.SetAttribute("agent.session_id", sessionID)
		agentSpan.SetAttribute("branch.number", fmt.Sprintf("%d", branchNum))
	}

	// Execute agent conversation with session ID for database persistence
	response, err := ag.Chat(ctx, sessionID, prompt)
	if err != nil {
		return nil, "", fmt.Errorf("agent chat failed: %w", err)
	}

	// Get model information
	model := ag.GetLLMModel()
	provider := ag.GetLLMProviderName()

	// Track model and tools in span
	if agentSpan != nil {
		if model != "" {
			agentSpan.SetAttribute("agent.model", model)
			agentSpan.SetAttribute("agent.provider", provider)
		}
		if len(response.ToolExecutions) > 0 {
			agentSpan.SetAttribute("tools_used", fmt.Sprintf("%d", len(response.ToolExecutions)))
			toolNames := extractToolNames(response.ToolExecutions)
			agentSpan.SetAttribute("tool_names", strings.Join(toolNames, ","))
		}
	}

	duration := time.Since(startTime)

	// Build result
	result := &loomv1.AgentResult{
		AgentId: agentID,
		Output:  response.Content,
		Metadata: map[string]string{
			"agent_name": ag.GetName(),
		},
		ConfidenceScore: 1.0,
		DurationMs:      duration.Milliseconds(),
		Cost: &loomv1.AgentExecutionCost{
			TotalTokens:  int32(response.Usage.TotalTokens),
			InputTokens:  int32(response.Usage.InputTokens),
			OutputTokens: int32(response.Usage.OutputTokens),
			CostUsd:      response.Usage.CostUSD,
		},
	}

	return result, model, nil
}

// mergeResults merges agent results using the specified strategy.
func (e *ForkJoinExecutor) mergeResults(ctx context.Context, workflowID string, results []*loomv1.AgentResult) (string, error) {
	switch e.pattern.MergeStrategy {
	case loomv1.MergeStrategy_FIRST:
		if len(results) > 0 {
			return results[0].Output, nil
		}
		return "", nil

	case loomv1.MergeStrategy_CONCATENATE:
		var builder strings.Builder
		for i, result := range results {
			builder.WriteString(fmt.Sprintf("=== Agent %s ===\n", result.AgentId))
			builder.WriteString(result.Output)
			if i < len(results)-1 {
				builder.WriteString("\n\n")
			}
		}
		return builder.String(), nil

	case loomv1.MergeStrategy_CONSENSUS, loomv1.MergeStrategy_SUMMARY, loomv1.MergeStrategy_VOTING, loomv1.MergeStrategy_BEST:
		// These strategies require LLM-based merging
		return e.llmMerge(ctx, workflowID, results)

	default:
		return "", fmt.Errorf("unsupported merge strategy: %s", e.pattern.MergeStrategy)
	}
}

// llmMerge uses LLM to merge results (consensus, voting, summary, best).
func (e *ForkJoinExecutor) llmMerge(ctx context.Context, workflowID string, results []*loomv1.AgentResult) (string, error) {
	if e.orchestrator.llmProvider == nil {
		return "", fmt.Errorf("LLM provider required for %s merge strategy", e.pattern.MergeStrategy)
	}

	// Build merge prompt based on strategy
	var mergePrompt string
	switch e.pattern.MergeStrategy {
	case loomv1.MergeStrategy_CONSENSUS:
		mergePrompt = e.buildConsensusPrompt(results)
	case loomv1.MergeStrategy_VOTING:
		mergePrompt = e.buildVotingPrompt(results)
	case loomv1.MergeStrategy_SUMMARY:
		mergePrompt = e.buildSummaryPrompt(results)
	case loomv1.MergeStrategy_BEST:
		mergePrompt = e.buildBestPrompt(results)
	}

	// Generate unique session ID for merge operation
	sessionID := fmt.Sprintf("%s-merge-%s", workflowID, e.pattern.MergeStrategy.String())

	// Create a wrapper context that satisfies agent.Context interface
	mergeSession := &agent.Session{
		ID:        sessionID,
		Messages:  []agent.Message{},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	agentCtx := &mergeContext{
		Context: ctx,
		session: mergeSession,
		tracer:  e.orchestrator.tracer,
	}

	// Build messages for LLM
	messages := []agent.Message{
		{
			Role:      "user",
			Content:   mergePrompt,
			Timestamp: time.Now(),
		},
	}

	// Call LLM for merge
	response, err := e.orchestrator.llmProvider.Chat(agentCtx, messages, nil)
	if err != nil {
		return "", fmt.Errorf("LLM merge failed: %w", err)
	}

	return response.Content, nil
}

// buildConsensusPrompt creates a prompt for consensus building.
func (e *ForkJoinExecutor) buildConsensusPrompt(results []*loomv1.AgentResult) string {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Original prompt: %s\n\n", e.pattern.Prompt))
	builder.WriteString("The following agents provided responses:\n\n")

	for i, result := range results {
		builder.WriteString(fmt.Sprintf("Agent %d:\n%s\n\n", i+1, result.Output))
	}

	builder.WriteString("Synthesize these perspectives into a consensus view that incorporates the strongest points from each agent.")
	return builder.String()
}

// buildVotingPrompt creates a prompt for voting on best answer.
func (e *ForkJoinExecutor) buildVotingPrompt(results []*loomv1.AgentResult) string {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Original prompt: %s\n\n", e.pattern.Prompt))
	builder.WriteString("Review these responses and select the most compelling answer:\n\n")

	for i, result := range results {
		builder.WriteString(fmt.Sprintf("Option %d:\n%s\n\n", i+1, result.Output))
	}

	builder.WriteString("Identify which option is most convincing and explain why.")
	return builder.String()
}

// buildSummaryPrompt creates a prompt for summarizing results.
func (e *ForkJoinExecutor) buildSummaryPrompt(results []*loomv1.AgentResult) string {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Original prompt: %s\n\n", e.pattern.Prompt))
	builder.WriteString("Summarize these agent responses concisely:\n\n")

	for i, result := range results {
		builder.WriteString(fmt.Sprintf("Agent %d: %s\n\n", i+1, result.Output))
	}

	builder.WriteString("Provide a concise summary highlighting key points and conclusions.")
	return builder.String()
}

// buildBestPrompt creates a prompt for selecting best response.
func (e *ForkJoinExecutor) buildBestPrompt(results []*loomv1.AgentResult) string {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Original prompt: %s\n\n", e.pattern.Prompt))
	builder.WriteString("Evaluate these responses and select the highest quality one:\n\n")

	for i, result := range results {
		builder.WriteString(fmt.Sprintf("Response %d:\n%s\n\n", i+1, result.Output))
	}

	builder.WriteString("Select and return the best response based on clarity, accuracy, and depth.")
	return builder.String()
}

// calculateCost aggregates costs from all agent results.
func (e *ForkJoinExecutor) calculateCost(results []*loomv1.AgentResult) *loomv1.WorkflowCost {
	cost := &loomv1.WorkflowCost{
		AgentCostsUsd: make(map[string]float64),
	}

	for _, result := range results {
		if result.Cost != nil {
			cost.TotalCostUsd += result.Cost.CostUsd
			cost.TotalTokens += result.Cost.TotalTokens
			cost.LlmCalls++

			// Aggregate by agent
			if existing, ok := cost.AgentCostsUsd[result.AgentId]; ok {
				cost.AgentCostsUsd[result.AgentId] = existing + result.Cost.CostUsd
			} else {
				cost.AgentCostsUsd[result.AgentId] = result.Cost.CostUsd
			}
		}
	}

	return cost
}

// truncateForLog truncates a string for logging.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

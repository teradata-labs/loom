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

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
)

// ParallelExecutor executes a parallel pattern.
type ParallelExecutor struct {
	orchestrator *Orchestrator
	pattern      *loomv1.ParallelPattern
}

// NewParallelExecutor creates a new parallel executor.
func NewParallelExecutor(orchestrator *Orchestrator, pattern *loomv1.ParallelPattern) *ParallelExecutor {
	return &ParallelExecutor{
		orchestrator: orchestrator,
		pattern:      pattern,
	}
}

// Execute runs the parallel pattern and returns the result.
func (e *ParallelExecutor) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
	startTime := time.Now()

	// Start workflow-level span
	ctx, workflowSpan := e.orchestrator.tracer.StartSpan(ctx, "workflow.parallel")
	defer e.orchestrator.tracer.EndSpan(workflowSpan)

	if workflowSpan != nil {
		workflowSpan.SetAttribute("workflow.type", "parallel")
		workflowSpan.SetAttribute("parallel.task_count", fmt.Sprintf("%d", len(e.pattern.Tasks)))
		workflowSpan.SetAttribute("parallel.merge_strategy", e.pattern.MergeStrategy.String())
	}

	e.orchestrator.logger.Info("Starting parallel execution",
		zap.Int("tasks", len(e.pattern.Tasks)))

	// Validate all agents exist
	for i, task := range e.pattern.Tasks {
		if _, err := e.orchestrator.GetAgent(ctx, task.AgentId); err != nil {
			return nil, fmt.Errorf("agent not found for task %d: %s: %w", i, task.AgentId, err)
		}
	}

	// Create timeout context if specified
	executeCtx := ctx
	if e.pattern.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		executeCtx, cancel = context.WithTimeout(ctx, time.Duration(e.pattern.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	// Execute all tasks in parallel with parallel span
	ctx, parallelSpan := e.orchestrator.tracer.StartSpan(ctx, "parallel.execute")
	if parallelSpan != nil {
		parallelSpan.SetAttribute("task.count", fmt.Sprintf("%d", len(e.pattern.Tasks)))
	}

	results, modelsUsed, err := e.executeParallel(executeCtx)
	e.orchestrator.tracer.EndSpan(parallelSpan)

	if err != nil {
		return nil, fmt.Errorf("parallel execution failed: %w", err)
	}

	// Merge results using specified strategy with merge span
	ctx, mergeSpan := e.orchestrator.tracer.StartSpan(ctx, "parallel.merge")
	if mergeSpan != nil {
		mergeSpan.SetAttribute("merge.strategy", e.pattern.MergeStrategy.String())
		mergeSpan.SetAttribute("task.count", fmt.Sprintf("%d", len(results)))
	}

	mergedOutput, err := e.mergeResults(ctx, results)
	e.orchestrator.tracer.EndSpan(mergeSpan)

	if err != nil {
		return nil, fmt.Errorf("failed to merge parallel results: %w", err)
	}

	// Calculate total cost
	cost := e.calculateCost(results)

	duration := time.Since(startTime)
	e.orchestrator.logger.Info("Parallel execution completed",
		zap.Duration("duration", duration),
		zap.Float64("total_cost_usd", cost.TotalCostUsd))

	return &loomv1.WorkflowResult{
		PatternType:  "parallel",
		AgentResults: results,
		MergedOutput: mergedOutput,
		Metadata: map[string]string{
			"task_count":     fmt.Sprintf("%d", len(e.pattern.Tasks)),
			"merge_strategy": e.pattern.MergeStrategy.String(),
		},
		DurationMs: duration.Milliseconds(),
		Cost:       cost,
		ModelsUsed: modelsUsed,
	}, nil
}

// executeParallel runs all tasks in parallel and collects their results.
func (e *ParallelExecutor) executeParallel(ctx context.Context) ([]*loomv1.AgentResult, map[string]string, error) {
	var wg sync.WaitGroup
	var modelsMu sync.Mutex
	resultsChan := make(chan *loomv1.AgentResult, len(e.pattern.Tasks))
	errorsChan := make(chan error, len(e.pattern.Tasks))
	modelsUsed := make(map[string]string)

	// Launch goroutine for each task
	for i, task := range e.pattern.Tasks {
		wg.Add(1)
		go func(taskIndex int, t *loomv1.AgentTask) {
			defer wg.Done()

			// Acquire LLM semaphore to limit concurrent LLM calls
			if e.orchestrator.llmSemaphore != nil {
				e.orchestrator.logger.Debug("Parallel task acquiring LLM semaphore",
					zap.String("agent_id", t.AgentId),
					zap.Int("task", taskIndex+1))
				e.orchestrator.llmSemaphore <- struct{}{}
				defer func() {
					<-e.orchestrator.llmSemaphore
					e.orchestrator.logger.Debug("Parallel task released LLM semaphore",
						zap.String("agent_id", t.AgentId),
						zap.Int("task", taskIndex+1))
				}()
				e.orchestrator.logger.Debug("Parallel task acquired LLM semaphore",
					zap.String("agent_id", t.AgentId),
					zap.Int("task", taskIndex+1))
			}

			// Create task span
			taskCtx, taskSpan := e.orchestrator.tracer.StartSpan(ctx, fmt.Sprintf("parallel.task.%d", taskIndex+1))
			if taskSpan != nil {
				taskSpan.SetAttribute("task.number", fmt.Sprintf("%d", taskIndex+1))
				taskSpan.SetAttribute("task.agent_id", t.AgentId)
			}

			result, model, err := e.executeTaskWithSpan(taskCtx, t, taskIndex)

			e.orchestrator.tracer.EndSpan(taskSpan)

			if err != nil {
				e.orchestrator.logger.Error("Task execution failed in parallel",
					zap.Int("task_index", taskIndex),
					zap.String("agent_id", t.AgentId),
					zap.Error(err))
				errorsChan <- fmt.Errorf("task %d (agent %s) failed: %w", taskIndex, t.AgentId, err)
				// Still send a result with error information
				result = &loomv1.AgentResult{
					AgentId: t.AgentId,
					Output:  fmt.Sprintf("Error: %s", err.Error()),
					Metadata: map[string]string{
						"task_index": fmt.Sprintf("%d", taskIndex),
						"error":      err.Error(),
					},
					ConfidenceScore: 0.0,
				}
			} else if model != "" {
				// Track model used (thread-safe)
				modelsMu.Lock()
				modelsUsed[t.AgentId] = model
				modelsMu.Unlock()
			}
			resultsChan <- result
		}(i, task)
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(resultsChan)
	close(errorsChan)

	// Collect results
	results := make([]*loomv1.AgentResult, 0, len(e.pattern.Tasks))
	for result := range resultsChan {
		results = append(results, result)
	}

	// Check for errors
	errors := make([]error, 0)
	for err := range errorsChan {
		errors = append(errors, err)
	}

	// If all tasks failed, return error
	if len(errors) == len(e.pattern.Tasks) {
		return nil, nil, fmt.Errorf("all tasks failed: %v", errors)
	}

	// Log partial failures
	if len(errors) > 0 {
		e.orchestrator.logger.Warn("Some tasks failed in parallel execution",
			zap.Int("failed_count", len(errors)),
			zap.Int("total_count", len(e.pattern.Tasks)))
	}

	return results, modelsUsed, nil
}

// executeTaskWithSpan runs a single task with comprehensive observability.
func (e *ParallelExecutor) executeTaskWithSpan(ctx context.Context, task *loomv1.AgentTask, taskIndex int) (*loomv1.AgentResult, string, error) {
	startTime := time.Now()

	// Get agent from orchestrator
	ag, err := e.orchestrator.GetAgent(ctx, task.AgentId)
	if err != nil {
		return nil, "", err
	}

	// Create trace span for agent execution
	ctx, agentSpan := e.orchestrator.tracer.StartSpan(ctx, fmt.Sprintf("parallel.agent.%s", task.AgentId))
	defer e.orchestrator.tracer.EndSpan(agentSpan)

	if agentSpan != nil {
		agentSpan.SetAttribute("agent.id", task.AgentId)
		agentSpan.SetAttribute("agent.name", ag.GetName())
		agentSpan.SetAttribute("task.number", fmt.Sprintf("%d", taskIndex+1))
	}

	// Execute agent conversation
	// Use a unique session ID for this task
	sessionID := fmt.Sprintf("parallel_%s_task_%d_%d", task.AgentId, taskIndex, time.Now().UnixNano())
	response, err := ag.Chat(ctx, sessionID, task.Prompt)
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

	// Build result with task metadata
	resultMetadata := map[string]string{
		"task_index": fmt.Sprintf("%d", taskIndex),
		"agent_name": ag.GetName(),
	}
	// Merge in task-specific metadata
	for k, v := range task.Metadata {
		resultMetadata[k] = v
	}

	result := &loomv1.AgentResult{
		AgentId:         task.AgentId,
		Output:          response.Content,
		Metadata:        resultMetadata,
		ConfidenceScore: 1.0,
		DurationMs:      duration.Milliseconds(),
		Cost: &loomv1.AgentExecutionCost{
			TotalTokens:  types.SafeInt32(response.Usage.TotalTokens),
			InputTokens:  types.SafeInt32(response.Usage.InputTokens),
			OutputTokens: types.SafeInt32(response.Usage.OutputTokens),
			CostUsd:      response.Usage.CostUSD,
		},
	}

	return result, model, nil
}

// mergeResults merges task results using the specified strategy.
func (e *ParallelExecutor) mergeResults(ctx context.Context, results []*loomv1.AgentResult) (string, error) {
	switch e.pattern.MergeStrategy {
	case loomv1.MergeStrategy_FIRST:
		if len(results) > 0 {
			return results[0].Output, nil
		}
		return "", nil

	case loomv1.MergeStrategy_CONCATENATE:
		var builder strings.Builder
		for i, result := range results {
			// Try to use task metadata for labeling
			taskIndex := result.Metadata["task_index"]
			if taskIndex != "" {
				builder.WriteString(fmt.Sprintf("=== Task %s (Agent %s) ===\n", taskIndex, result.AgentId))
			} else {
				builder.WriteString(fmt.Sprintf("=== Agent %s ===\n", result.AgentId))
			}
			builder.WriteString(result.Output)
			if i < len(results)-1 {
				builder.WriteString("\n\n")
			}
		}
		return builder.String(), nil

	case loomv1.MergeStrategy_CONSENSUS, loomv1.MergeStrategy_SUMMARY, loomv1.MergeStrategy_VOTING, loomv1.MergeStrategy_BEST:
		// These strategies require LLM-based merging
		return e.llmMerge(ctx, results)

	default:
		return "", fmt.Errorf("unsupported merge strategy: %s", e.pattern.MergeStrategy)
	}
}

// llmMerge uses LLM to merge results (consensus, voting, summary, best).
func (e *ParallelExecutor) llmMerge(ctx context.Context, results []*loomv1.AgentResult) (string, error) {
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

	// Create a wrapper context that satisfies agent.Context interface
	mergeSession := &agent.Session{
		ID:        "merge_parallel_" + fmt.Sprintf("%d", time.Now().UnixNano()),
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
func (e *ParallelExecutor) buildConsensusPrompt(results []*loomv1.AgentResult) string {
	var builder strings.Builder
	builder.WriteString("The following agents completed independent tasks:\n\n")

	for i, result := range results {
		builder.WriteString(fmt.Sprintf("Task %d (Agent %s):\n%s\n\n",
			i+1, result.AgentId, result.Output))
	}

	builder.WriteString("Synthesize these results into a consensus view that incorporates the key points from each task.")
	return builder.String()
}

// buildVotingPrompt creates a prompt for voting on best result.
func (e *ParallelExecutor) buildVotingPrompt(results []*loomv1.AgentResult) string {
	var builder strings.Builder
	builder.WriteString("Review these task results and select the most valuable contribution:\n\n")

	for i, result := range results {
		builder.WriteString(fmt.Sprintf("Option %d:\n%s\n\n", i+1, result.Output))
	}

	builder.WriteString("Identify which option is most valuable and explain why.")
	return builder.String()
}

// buildSummaryPrompt creates a prompt for summarizing results.
func (e *ParallelExecutor) buildSummaryPrompt(results []*loomv1.AgentResult) string {
	var builder strings.Builder
	builder.WriteString("Summarize these independent task results concisely:\n\n")

	for i, result := range results {
		builder.WriteString(fmt.Sprintf("Task %d: %s\n\n", i+1, result.Output))
	}

	builder.WriteString("Provide a concise summary highlighting key findings across all tasks.")
	return builder.String()
}

// buildBestPrompt creates a prompt for selecting best result.
func (e *ParallelExecutor) buildBestPrompt(results []*loomv1.AgentResult) string {
	var builder strings.Builder
	builder.WriteString("Evaluate these task results and select the highest quality one:\n\n")

	for i, result := range results {
		builder.WriteString(fmt.Sprintf("Result %d:\n%s\n\n", i+1, result.Output))
	}

	builder.WriteString("Select and return the best result based on quality, completeness, and insight.")
	return builder.String()
}

// calculateCost aggregates costs from all task results.
func (e *ParallelExecutor) calculateCost(results []*loomv1.AgentResult) *loomv1.WorkflowCost {
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

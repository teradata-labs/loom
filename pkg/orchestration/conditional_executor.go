// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"go.uber.org/zap"
)

// ConditionalExecutor executes a conditional pattern.
type ConditionalExecutor struct {
	orchestrator *Orchestrator
	pattern      *loomv1.ConditionalPattern
}

// NewConditionalExecutor creates a new conditional executor.
func NewConditionalExecutor(orchestrator *Orchestrator, pattern *loomv1.ConditionalPattern) *ConditionalExecutor {
	return &ConditionalExecutor{
		orchestrator: orchestrator,
		pattern:      pattern,
	}
}

// Execute runs the conditional pattern and returns the result.
func (e *ConditionalExecutor) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
	startTime := time.Now()

	// Start workflow-level span
	ctx, workflowSpan := e.orchestrator.tracer.StartSpan(ctx, "workflow.conditional")
	defer e.orchestrator.tracer.EndSpan(workflowSpan)

	if workflowSpan != nil {
		workflowSpan.SetAttribute("workflow.type", "conditional")
		workflowSpan.SetAttribute("conditional.condition_prompt", truncateForLog(e.pattern.ConditionPrompt, 100))
		workflowSpan.SetAttribute("conditional.branch_count", fmt.Sprintf("%d", len(e.pattern.Branches)))
		workflowSpan.SetAttribute("conditional.has_default", fmt.Sprintf("%t", e.pattern.DefaultBranch != nil))
	}

	e.orchestrator.logger.Info("Starting conditional execution",
		zap.Int("branches", len(e.pattern.Branches)))

	// Validate condition agent exists
	conditionAgent, err := e.orchestrator.GetAgent(ctx, e.pattern.ConditionAgentId)
	if err != nil {
		return nil, fmt.Errorf("condition agent not found: %s: %w", e.pattern.ConditionAgentId, err)
	}

	// Execute condition evaluation with span
	ctx, evalSpan := e.orchestrator.tracer.StartSpan(ctx, "conditional.evaluate")
	if evalSpan != nil {
		evalSpan.SetAttribute("condition.agent_id", e.pattern.ConditionAgentId)
	}

	e.orchestrator.logger.Info("Evaluating condition",
		zap.String("agent_id", e.pattern.ConditionAgentId))

	conditionResult, model, err := e.evaluateConditionWithSpan(ctx, conditionAgent)

	if evalSpan != nil {
		evalSpan.SetAttribute("condition.result", conditionResult)
		if model != "" {
			evalSpan.SetAttribute("agent.model", model)
		}
	}
	e.orchestrator.tracer.EndSpan(evalSpan)

	if err != nil {
		return nil, fmt.Errorf("condition evaluation failed: %w", err)
	}

	// Select branch based on condition result
	selectedBranch, branchKey := e.selectBranch(conditionResult)
	if selectedBranch == nil {
		return nil, fmt.Errorf("no matching branch found for condition: %s", conditionResult)
	}

	e.orchestrator.logger.Info("Selected branch",
		zap.String("condition_result", conditionResult),
		zap.String("branch_key", branchKey))

	if workflowSpan != nil {
		workflowSpan.SetAttribute("conditional.result", conditionResult)
		workflowSpan.SetAttribute("conditional.selected_branch", branchKey)
	}

	// Execute selected branch with branch span
	ctx, branchSpan := e.orchestrator.tracer.StartSpan(ctx, fmt.Sprintf("conditional.branch.%s", branchKey))
	if branchSpan != nil {
		branchSpan.SetAttribute("branch.key", branchKey)
	}

	branchResult, err := e.orchestrator.ExecutePattern(ctx, selectedBranch)
	e.orchestrator.tracer.EndSpan(branchSpan)

	if err != nil {
		return nil, fmt.Errorf("branch execution failed: %w", err)
	}

	duration := time.Since(startTime)
	e.orchestrator.logger.Info("Conditional execution completed",
		zap.Duration("duration", duration),
		zap.String("selected_branch", branchKey))

	// Wrap the branch result with conditional metadata
	return &loomv1.WorkflowResult{
		PatternType:  "conditional",
		AgentResults: branchResult.AgentResults,
		MergedOutput: branchResult.MergedOutput,
		Metadata: map[string]string{
			"condition_result": conditionResult,
			"selected_branch":  branchKey,
			"branch_pattern":   branchResult.PatternType,
			"condition_agent":  e.pattern.ConditionAgentId,
		},
		DurationMs: duration.Milliseconds(),
		Cost:       branchResult.Cost, // Inherit cost from branch execution
	}, nil
}

// evaluateConditionWithSpan runs the condition agent with comprehensive observability.
func (e *ConditionalExecutor) evaluateConditionWithSpan(ctx context.Context, conditionAgent *agent.Agent) (string, string, error) {
	startTime := time.Now()

	// Create trace span for agent execution
	ctx, agentSpan := e.orchestrator.tracer.StartSpan(ctx, fmt.Sprintf("conditional.agent.%s", e.pattern.ConditionAgentId))
	defer e.orchestrator.tracer.EndSpan(agentSpan)

	if agentSpan != nil {
		agentSpan.SetAttribute("agent.id", e.pattern.ConditionAgentId)
		agentSpan.SetAttribute("agent.name", conditionAgent.GetName())
		agentSpan.SetAttribute("agent.role", "condition_evaluator")
	}

	// Execute condition agent
	sessionID := fmt.Sprintf("conditional_%s_%d", e.pattern.ConditionAgentId, time.Now().UnixNano())
	response, err := conditionAgent.Chat(ctx, sessionID, e.pattern.ConditionPrompt)
	if err != nil {
		return "", "", fmt.Errorf("condition agent chat failed: %w", err)
	}

	// Get model information
	model := conditionAgent.GetLLMModel()
	provider := conditionAgent.GetLLMProviderName()

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
	e.orchestrator.logger.Debug("Condition evaluated",
		zap.Duration("duration", duration),
		zap.String("result", truncateForLog(response.Content, 100)))

	// Extract condition result from response
	// The response should be a simple string that matches a branch key
	// We'll normalize it by trimming whitespace and converting to lowercase
	condition := strings.TrimSpace(strings.ToLower(response.Content))

	return condition, model, nil
}

// selectBranch selects the appropriate workflow branch based on the condition result.
func (e *ConditionalExecutor) selectBranch(conditionResult string) (*loomv1.WorkflowPattern, string) {
	// Normalize condition result for matching
	normalized := strings.TrimSpace(strings.ToLower(conditionResult))

	// Try exact match first
	if branch, ok := e.pattern.Branches[normalized]; ok {
		return branch, normalized
	}

	// Try case-insensitive match
	for key, branch := range e.pattern.Branches {
		if strings.ToLower(key) == normalized {
			return branch, key
		}
	}

	// Try substring match (if condition contains the branch key)
	for key, branch := range e.pattern.Branches {
		if strings.Contains(normalized, strings.ToLower(key)) {
			return branch, key
		}
	}

	// Fall back to default branch if available
	if e.pattern.DefaultBranch != nil {
		return e.pattern.DefaultBranch, "default"
	}

	return nil, ""
}

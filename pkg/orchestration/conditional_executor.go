// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"
	"sort"
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
	workflowID   string
}

// NewConditionalExecutor creates a new conditional executor.
func NewConditionalExecutor(orchestrator *Orchestrator, pattern *loomv1.ConditionalPattern, workflowID string) *ConditionalExecutor {
	return &ConditionalExecutor{
		orchestrator: orchestrator,
		pattern:      pattern,
		workflowID:   workflowID,
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

	// If no match, try output coercion (cheap, no LLM call)
	if selectedBranch == nil {
		if coerced, ok := coerceBranchKey(conditionResult, e.getBranchKeys()); ok {
			selectedBranch, branchKey = e.selectBranch(coerced)
			if selectedBranch != nil {
				e.orchestrator.logger.Info("Branch matched after output coercion",
					zap.String("original", conditionResult),
					zap.String("coerced", coerced))
			}
		}
	}

	// If still no match and retry policy configured, retry with feedback
	if selectedBranch == nil && e.pattern.RetryPolicy != nil && e.pattern.RetryPolicy.MaxRetries > 0 {
		var retryBranch *loomv1.WorkflowPattern
		var retryKey, retryResult string
		retryBranch, retryKey, retryResult = e.retryConditionEvaluation(ctx, conditionAgent, conditionResult)
		if retryBranch != nil {
			selectedBranch = retryBranch
			branchKey = retryKey
			conditionResult = retryResult
		}
	}

	// Fall back to default branch after coercion and retry have been attempted
	if selectedBranch == nil && e.pattern.DefaultBranch != nil {
		selectedBranch = e.pattern.DefaultBranch
		branchKey = "default"
		e.orchestrator.logger.Info("Using default branch after all matching attempts",
			zap.String("condition_result", conditionResult))
	}

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

	// Execute condition agent with deterministic session ID
	sessionID := fmt.Sprintf("%s-condition-%s", e.workflowID, e.pattern.ConditionAgentId)
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

	// NOTE: Default branch is NOT checked here. It is handled in Execute()
	// after coercion and retry, so retry_policy is not silently ignored
	// when default_branch is also configured.
	return nil, ""
}

// getBranchKeys returns a sorted list of branch keys for deterministic ordering.
func (e *ConditionalExecutor) getBranchKeys() []string {
	keys := make([]string, 0, len(e.pattern.Branches))
	for k := range e.pattern.Branches {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// maxOutputRetries is the hard cap on retry attempts to prevent runaway costs.
const maxOutputRetries = 10

// cappedRetries returns the retry count capped at maxOutputRetries.
func cappedRetries(requested int32) int {
	n := int(requested)
	if n > maxOutputRetries {
		return maxOutputRetries
	}
	return n
}

// retryConditionEvaluation retries the condition agent with a prompt that lists
// valid branch keys and the agent's previous failed output. Each retry uses a
// fresh session ID to avoid anchoring on previous bad output.
func (e *ConditionalExecutor) retryConditionEvaluation(
	ctx context.Context,
	conditionAgent *agent.Agent,
	lastResult string,
) (*loomv1.WorkflowPattern, string, string) {
	branchKeys := e.getBranchKeys()
	maxRetries := cappedRetries(e.pattern.RetryPolicy.MaxRetries)

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, "", lastResult
		}
		// Build retry prompt with valid branch keys
		retryPrompt := e.buildConditionRetryPrompt(lastResult, branchKeys, attempt, maxRetries)

		// Use fresh session ID for each retry
		sessionID := fmt.Sprintf("%s-condition-%s-retry%d",
			e.workflowID, e.pattern.ConditionAgentId, attempt)

		e.orchestrator.logger.Info("Retrying condition evaluation",
			zap.Int("attempt", attempt),
			zap.Int("max_retries", maxRetries),
			zap.String("last_result", truncateForLog(lastResult, 100)))

		response, err := conditionAgent.Chat(ctx, sessionID, retryPrompt)
		if err != nil {
			e.orchestrator.logger.Warn("Condition retry failed",
				zap.Int("attempt", attempt),
				zap.Error(err))
			continue
		}

		result := strings.TrimSpace(strings.ToLower(response.Content))

		// Try coercion on the retry output
		if coerced, ok := coerceBranchKey(result, branchKeys); ok {
			result = strings.ToLower(coerced)
		}

		branch, key := e.selectBranch(result)
		if branch != nil {
			e.orchestrator.logger.Info("Condition matched after retry",
				zap.Int("attempt", attempt),
				zap.String("branch", key))
			return branch, key, result
		}
		lastResult = result
	}

	e.orchestrator.logger.Warn("Condition retry exhausted, no branch matched",
		zap.Int("attempts", maxRetries),
		zap.String("last_result", truncateForLog(lastResult, 100)))
	return nil, "", lastResult
}

// buildConditionRetryPrompt constructs a retry prompt that explains what went wrong
// and lists the valid branch values the agent should output.
func (e *ConditionalExecutor) buildConditionRetryPrompt(lastResult string, branchKeys []string, attempt, maxRetries int) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Your previous response was: %q\n\n", lastResult))
	sb.WriteString("This output could not be matched to any valid workflow branch.\n\n")
	sb.WriteString("REASON: The condition evaluator must respond with exactly one of the allowed\n")
	sb.WriteString("branch values. Your response did not match any of them (even after\n")
	sb.WriteString("case-insensitive and substring matching).\n\n")

	// For conditionals, valid values are always included — they ARE the instruction.
	// The include_valid_values field has no effect here (unlike pipeline/swarm).
	sb.WriteString("VALID VALUES (respond with exactly one of these, nothing else):\n")
	for _, key := range branchKeys {
		sb.WriteString(fmt.Sprintf("- %s\n", key))
	}
	sb.WriteString("\nRULES:\n")
	sb.WriteString("1. Respond with ONLY one of the valid values above.\n")
	sb.WriteString("2. No explanation, no formatting, no punctuation, no quotes.\n")
	sb.WriteString("3. Just the single word/phrase from the list.\n\n")
	sb.WriteString(fmt.Sprintf("This is retry %d of %d.\n", attempt, maxRetries))

	return sb.String()
}

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

	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"go.uber.org/zap"
)

// PipelineExecutor executes a pipeline pattern.
type PipelineExecutor struct {
	orchestrator *Orchestrator
	pattern      *loomv1.PipelinePattern
}

// NewPipelineExecutor creates a new pipeline executor.
func NewPipelineExecutor(orchestrator *Orchestrator, pattern *loomv1.PipelinePattern) *PipelineExecutor {
	return &PipelineExecutor{
		orchestrator: orchestrator,
		pattern:      pattern,
	}
}

// Execute runs the pipeline and returns the result.
func (e *PipelineExecutor) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
	startTime := time.Now()

	// Generate unique workflow ID for session tracking
	workflowID := fmt.Sprintf("pipeline-%s", uuid.New().String()[:8])

	// Start workflow-level span
	ctx, workflowSpan := e.orchestrator.tracer.StartSpan(ctx, "workflow.pipeline")
	defer e.orchestrator.tracer.EndSpan(workflowSpan)

	if workflowSpan != nil {
		workflowSpan.SetAttribute("workflow.type", "pipeline")
		workflowSpan.SetAttribute("workflow.id", workflowID)
		workflowSpan.SetAttribute("pipeline.initial_prompt", truncateForLog(e.pattern.InitialPrompt, 100))
		workflowSpan.SetAttribute("pipeline.stage_count", fmt.Sprintf("%d", len(e.pattern.Stages)))
		workflowSpan.SetAttribute("pipeline.pass_full_history", fmt.Sprintf("%t", e.pattern.PassFullHistory))
	}

	e.orchestrator.logger.Info("Starting pipeline execution",
		zap.Int("stages", len(e.pattern.Stages)))

	// Check for empty pipeline
	if len(e.pattern.Stages) == 0 {
		return nil, fmt.Errorf("pipeline has no stages")
	}

	// Validate all agents exist
	for i, stage := range e.pattern.Stages {
		if _, err := e.orchestrator.GetAgent(ctx, stage.AgentId); err != nil {
			return nil, fmt.Errorf("agent not found for stage %d: %s: %w", i, stage.AgentId, err)
		}
	}

	// Execute pipeline stages sequentially
	allResults := make([]*loomv1.AgentResult, 0, len(e.pattern.Stages))
	stageOutputs := make([]string, 0, len(e.pattern.Stages))
	currentInput := e.pattern.InitialPrompt
	modelsUsed := make(map[string]string)

	for i, stage := range e.pattern.Stages {
		stageNum := i + 1

		// Start stage-level span
		ctx, stageSpan := e.orchestrator.tracer.StartSpan(ctx, fmt.Sprintf("pipeline.stage.%d", stageNum))
		if stageSpan != nil {
			stageSpan.SetAttribute("stage.number", fmt.Sprintf("%d", stageNum))
			stageSpan.SetAttribute("stage.agent_id", stage.AgentId)
		}

		e.orchestrator.logger.Info("Executing pipeline stage",
			zap.Int("stage", stageNum),
			zap.Int("total_stages", len(e.pattern.Stages)),
			zap.String("agent_id", stage.AgentId))

		// Build prompt for this stage
		prompt := e.buildStagePrompt(stage, currentInput, stageOutputs)

		// Execute stage with agent span
		result, model, err := e.executeStageWithSpan(ctx, workflowID, stage, prompt, stageNum)

		e.orchestrator.tracer.EndSpan(stageSpan)

		if err != nil {
			return nil, fmt.Errorf("stage %d failed: %w", stageNum, err)
		}

		// Track model used
		if model != "" {
			modelsUsed[stage.AgentId] = model
		}

		allResults = append(allResults, result)
		stageOutputs = append(stageOutputs, result.Output)

		// Validate output if validation prompt is provided
		if stage.ValidationPrompt != "" {
			valid, err := e.validateStageOutput(ctx, workflowID, stage, result.Output, stageNum)
			if err != nil {
				e.orchestrator.logger.Warn("Stage validation failed",
					zap.Int("stage", i+1),
					zap.Error(err))
				// Continue anyway, but log the validation failure
			} else if !valid {
				return nil, fmt.Errorf("stage %d output validation failed", i+1)
			}
		}

		// Update input for next stage
		currentInput = result.Output
	}

	// The final output is the last stage's output
	finalOutput := stageOutputs[len(stageOutputs)-1]

	// Calculate total cost
	cost := e.calculateCost(allResults)

	duration := time.Since(startTime)
	e.orchestrator.logger.Info("Pipeline completed",
		zap.Duration("duration", duration),
		zap.Float64("total_cost_usd", cost.TotalCostUsd))

	return &loomv1.WorkflowResult{
		PatternType:  "pipeline",
		AgentResults: allResults,
		MergedOutput: finalOutput,
		Metadata: map[string]string{
			"stage_count":       fmt.Sprintf("%d", len(e.pattern.Stages)),
			"pass_full_history": fmt.Sprintf("%t", e.pattern.PassFullHistory),
		},
		DurationMs: duration.Milliseconds(),
		Cost:       cost,
		ModelsUsed: modelsUsed,
	}, nil
}

// buildStagePrompt constructs the prompt for a pipeline stage.
func (e *PipelineExecutor) buildStagePrompt(stage *loomv1.PipelineStage, previousOutput string, allOutputs []string) string {
	return e.buildStagePromptWithContext(stage, previousOutput, allOutputs, nil)
}

// buildStagePromptWithContext constructs the prompt with optional structured context.
func (e *PipelineExecutor) buildStagePromptWithContext(stage *loomv1.PipelineStage, previousOutput string, allOutputs []string, structuredCtx *StructuredContext) string {
	prompt := stage.PromptTemplate

	// Replace {{previous}} placeholder with the previous stage's output
	if strings.Contains(prompt, "{{previous}}") {
		prompt = strings.ReplaceAll(prompt, "{{previous}}", previousOutput)
	}

	// Replace {{history}} placeholder with all previous outputs
	if strings.Contains(prompt, "{{history}}") {
		history := e.buildHistoryString(allOutputs)
		prompt = strings.ReplaceAll(prompt, "{{history}}", history)
	}

	// Replace {{structured_context}} placeholder with JSON context
	if strings.Contains(prompt, "{{structured_context}}") {
		if structuredCtx != nil {
			contextJSON, err := structuredCtx.ToJSON()
			if err != nil {
				// Log error but continue with empty context
				contextJSON = "{}"
			}
			prompt = strings.ReplaceAll(prompt, "{{structured_context}}", contextJSON)
		} else {
			// No structured context available
			prompt = strings.ReplaceAll(prompt, "{{structured_context}}", "{}")
		}
	}

	// If pass_full_history is enabled and no placeholders, append history
	if e.pattern.PassFullHistory && len(allOutputs) > 0 {
		if !strings.Contains(stage.PromptTemplate, "{{previous}}") && !strings.Contains(stage.PromptTemplate, "{{history}}") {
			history := e.buildHistoryString(allOutputs)
			prompt = fmt.Sprintf("%s\n\nPrevious stages:\n%s", prompt, history)
		}
	}

	return prompt
}

// buildHistoryString creates a formatted string of all previous outputs.
func (e *PipelineExecutor) buildHistoryString(outputs []string) string {
	if len(outputs) == 0 {
		return ""
	}

	var builder strings.Builder
	for i, output := range outputs {
		builder.WriteString(fmt.Sprintf("Stage %d output:\n%s\n\n", i+1, output))
	}
	return builder.String()
}

// executeStageWithSpan runs a single pipeline stage with comprehensive observability.
func (e *PipelineExecutor) executeStageWithSpan(ctx context.Context, workflowID string, stage *loomv1.PipelineStage, prompt string, stageNum int) (*loomv1.AgentResult, string, error) {
	startTime := time.Now()

	// Get agent from orchestrator
	ag, err := e.orchestrator.GetAgent(ctx, stage.AgentId)
	if err != nil {
		return nil, "", err
	}

	// Create trace span for agent execution
	ctx, agentSpan := e.orchestrator.tracer.StartSpan(ctx, fmt.Sprintf("pipeline.agent.%s", stage.AgentId))
	defer e.orchestrator.tracer.EndSpan(agentSpan)

	// Generate unique session ID for this pipeline stage
	sessionID := fmt.Sprintf("%s-stage%d-%s", workflowID, stageNum, stage.AgentId)

	if agentSpan != nil {
		agentSpan.SetAttribute("agent.id", stage.AgentId)
		agentSpan.SetAttribute("agent.name", ag.GetName())
		agentSpan.SetAttribute("agent.session_id", sessionID)
		agentSpan.SetAttribute("stage.number", fmt.Sprintf("%d", stageNum))
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
		AgentId: stage.AgentId,
		Output:  response.Content,
		Metadata: map[string]string{
			"stage":      fmt.Sprintf("%d", stageNum),
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

// validateStageOutput validates a stage's output using the validation prompt.
func (e *PipelineExecutor) validateStageOutput(ctx context.Context, workflowID string, stage *loomv1.PipelineStage, output string, stageNum int) (bool, error) {
	if e.orchestrator.llmProvider == nil {
		return true, fmt.Errorf("LLM provider required for validation")
	}

	// Build validation prompt
	validationPrompt := strings.ReplaceAll(stage.ValidationPrompt, "{{output}}", output)

	// Generate unique session ID for validation
	sessionID := fmt.Sprintf("%s-stage%d-%s-validation", workflowID, stageNum, stage.AgentId)

	// Create a wrapper context
	validationSession := &agent.Session{
		ID:        sessionID,
		Messages:  []agent.Message{},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	agentCtx := &mergeContext{
		Context: ctx,
		session: validationSession,
		tracer:  e.orchestrator.tracer,
	}

	// Build messages for LLM
	messages := []agent.Message{
		{
			Role:      "user",
			Content:   validationPrompt,
			Timestamp: time.Now(),
		},
	}

	// Call LLM for validation
	response, err := e.orchestrator.llmProvider.Chat(agentCtx, messages, nil)
	if err != nil {
		return false, fmt.Errorf("validation LLM call failed: %w", err)
	}

	// Simple validation: check if response contains "valid" or "yes"
	// In a real implementation, this could be more sophisticated
	content := strings.ToLower(response.Content)
	return strings.Contains(content, "valid") || strings.Contains(content, "yes") || strings.Contains(content, "true"), nil
}

// calculateCost aggregates costs from all stage results.
func (e *PipelineExecutor) calculateCost(results []*loomv1.AgentResult) *loomv1.WorkflowCost {
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

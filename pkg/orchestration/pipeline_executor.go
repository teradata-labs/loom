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

	"github.com/xeipuuv/gojsonschema"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
)

// PipelineExecutor executes a pipeline pattern.
type PipelineExecutor struct {
	orchestrator *Orchestrator
	pattern      *loomv1.PipelinePattern
	workflowID   string
}

// NewPipelineExecutor creates a new pipeline executor.
func NewPipelineExecutor(orchestrator *Orchestrator, pattern *loomv1.PipelinePattern, workflowID string) *PipelineExecutor {
	return &PipelineExecutor{
		orchestrator: orchestrator,
		pattern:      pattern,
		workflowID:   workflowID,
	}
}

// Execute runs the pipeline and returns the result.
func (e *PipelineExecutor) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
	startTime := time.Now()

	// Use the workflow ID provided at construction (stable or random)
	workflowID := e.workflowID

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
	var validationWarnings []string // tracks stages that used unvalidated output

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
		e.orchestrator.logger.Info("Pipeline stage prompt built",
			zap.Int("stage", stageNum),
			zap.Int("prompt_length", len(prompt)),
			zap.Int("previous_output_length", len(currentInput)),
			zap.String("prompt_preview", func() string { if len(prompt) > 200 { return prompt[:200] } ; return prompt }()),
		)

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

		// Emit progress with the completed stage's full result so polling
		// clients can display agent outputs incrementally.
		totalStages := int32(len(e.pattern.Stages)) // #nosec G115
		stagePct := int32(float64(stageNum) / float64(totalStages) * 100)
		nextAgentID := ""
		if stageNum < len(e.pattern.Stages) {
			nextAgentID = e.pattern.Stages[stageNum].AgentId
		}
		e.orchestrator.emitProgress(WorkflowProgressEvent{
			PatternType:    "pipeline",
			Message:        fmt.Sprintf("Stage %d of %d completed", stageNum, totalStages),
			Progress:       stagePct,
			CurrentAgentID: nextAgentID,
			PartialResults: allResults,
		})

		// Validate output — schema first (cheap), then LLM validation (expensive)
		var validationFailure string

		if stage.OutputSchema != "" {
			extractedJSON, schemaErr := e.validateStageOutputSchema(result.Output, stage.OutputSchema)
			if schemaErr != nil {
				validationFailure = fmt.Sprintf("JSON Schema validation failed: %s", schemaErr.Error())
			} else if extractedJSON != result.Output {
				// Normalize output: replace prose+JSON with just the extracted JSON
				// so downstream stages receive clean structured data.
				result.Output = extractedJSON
				allResults[len(allResults)-1].Output = extractedJSON
				stageOutputs[len(stageOutputs)-1] = extractedJSON
			}
		}

		if validationFailure == "" && stage.ValidationPrompt != "" {
			valid, err := e.validateStageOutput(ctx, workflowID, stage, result.Output, stageNum)
			if err != nil {
				e.orchestrator.logger.Warn("Stage validation error",
					zap.Int("stage", stageNum),
					zap.Error(err))
			} else if !valid {
				validationFailure = fmt.Sprintf("LLM validation failed against criteria: %s", stage.ValidationPrompt)
			}
		}

		if validationFailure != "" {
			if stage.RetryPolicy != nil && stage.RetryPolicy.MaxRetries > 0 {
				// Pass prior stage outputs (excluding current failed output) to avoid
				// leaking the failed output into {{history}} in the retry prompt.
				priorOutputs := stageOutputs[:len(stageOutputs)-1]
				retryResult, retryModel := e.retryStage(ctx, workflowID, stage, currentInput, priorOutputs, stageNum, result.Output, validationFailure)
				if retryResult != nil {
					result = retryResult
					allResults[len(allResults)-1] = result
					stageOutputs[len(stageOutputs)-1] = result.Output
					if retryModel != "" {
						modelsUsed[stage.AgentId] = retryModel
					}
				} else {
					// Graceful degradation: retries exhausted, continue with unvalidated output
					validationWarnings = append(validationWarnings,
						fmt.Sprintf("stage %d (%s): %s", stageNum, stage.AgentId, validationFailure))
				}
			} else {
				return nil, fmt.Errorf("stage %d output validation failed: %s", stageNum, validationFailure)
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

	metadata := map[string]string{
		"stage_count":       fmt.Sprintf("%d", len(e.pattern.Stages)),
		"pass_full_history": fmt.Sprintf("%t", e.pattern.PassFullHistory),
	}
	if len(validationWarnings) > 0 {
		metadata["validation_warnings"] = strings.Join(validationWarnings, "; ")
	}

	return &loomv1.WorkflowResult{
		PatternType:  "pipeline",
		AgentResults: allResults,
		MergedOutput: finalOutput,
		Metadata:     metadata,
		DurationMs:   duration.Milliseconds(),
		Cost:         cost,
		ModelsUsed:   modelsUsed,
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

	// If prompt template is empty, use the previous output (or initial prompt) directly
	if prompt == "" {
		prompt = previousOutput
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

	// Execute agent conversation. If a progress callback exists in the
	// context, use ChatWithProgress so tool calls and thinking stream
	// through to the caller (e.g., workflow UI).
	var response *agent.Response
	if progressCb := agent.ProgressCallbackFromContext(ctx); progressCb != nil {
		response, err = ag.ChatWithProgress(ctx, sessionID, prompt, progressCb)
	} else {
		response, err = ag.Chat(ctx, sessionID, prompt)
	}
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
			TotalTokens:  types.SafeInt32(response.Usage.TotalTokens),
			InputTokens:  types.SafeInt32(response.Usage.InputTokens),
			OutputTokens: types.SafeInt32(response.Usage.OutputTokens),
			CostUsd:      response.Usage.CostUSD,
		},
	}

	return result, model, nil
}

// validateStageOutput validates a stage's output using the validation prompt.
// Uses the orchestrator's merge LLM (GetMergeLLM) which resolves through the
// fallback chain: explicit LLM -> orchestrator role LLM from agents -> error.
func (e *PipelineExecutor) validateStageOutput(ctx context.Context, workflowID string, stage *loomv1.PipelineStage, output string, stageNum int) (bool, error) {
	validationLLM := e.orchestrator.GetMergeLLM()
	if validationLLM == nil {
		return true, fmt.Errorf("LLM provider required for validation (configure orchestrator LLM or agent orchestrator role LLM)")
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

	// Call LLM for validation using resolved orchestrator LLM
	response, err := validationLLM.Chat(agentCtx, messages, nil)
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

// validateStageOutputSchema validates stage output against a JSON Schema.
// Returns (extractedJSON, nil) if valid, ("", error) if invalid.
// When the output contains JSON embedded in prose, the extracted JSON is returned
// so the caller can normalize result.Output for downstream stages.
func (e *PipelineExecutor) validateStageOutputSchema(output string, schema string) (string, error) {
	// Try to extract JSON from mixed text+JSON output
	jsonStr := extractJSONFromText(output)
	if jsonStr == "" {
		return "", fmt.Errorf("no valid JSON found in output")
	}

	schemaLoader := gojsonschema.NewStringLoader(schema)
	documentLoader := gojsonschema.NewStringLoader(jsonStr)

	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return "", fmt.Errorf("schema validation error: %w", err)
	}

	if !result.Valid() {
		var violations []string
		for _, err := range result.Errors() {
			violations = append(violations, err.String())
		}
		return "", fmt.Errorf("schema violations: %s", strings.Join(violations, "; "))
	}

	return jsonStr, nil
}

// retryStage retries a pipeline stage after validation failure. Each retry uses
// a fresh session ID and includes the failure reason in the prompt. Returns nil
// if all retries are exhausted (caller should use graceful degradation).
func (e *PipelineExecutor) retryStage(
	ctx context.Context,
	workflowID string,
	stage *loomv1.PipelineStage,
	previousInput string,
	allOutputs []string,
	stageNum int,
	failedOutput string,
	validationFailure string,
) (*loomv1.AgentResult, string) {
	maxRetries := cappedRetries(stage.RetryPolicy.MaxRetries)

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, ""
		}
		// Build retry prompt with validation feedback
		basePrompt := e.buildStagePrompt(stage, previousInput, allOutputs)
		retryPrompt := e.buildStageRetryPrompt(basePrompt, failedOutput, validationFailure, stage.OutputSchema, stage.RetryPolicy.IncludeValidValues, attempt, maxRetries)

		// Fresh session ID for retry
		retryWorkflowID := fmt.Sprintf("%s-stage%d-%s-retry%d", workflowID, stageNum, stage.AgentId, attempt)

		e.orchestrator.logger.Info("Retrying pipeline stage",
			zap.Int("stage", stageNum),
			zap.Int("attempt", attempt),
			zap.Int("max_retries", maxRetries),
			zap.String("failure", truncateForLog(validationFailure, 100)))

		result, model, err := e.executeStageWithSpan(ctx, retryWorkflowID, stage, retryPrompt, stageNum)
		if err != nil {
			e.orchestrator.logger.Warn("Stage retry execution failed",
				zap.Int("stage", stageNum),
				zap.Int("attempt", attempt),
				zap.Error(err))
			continue
		}

		// Re-validate — schema first, then LLM
		var retryValidationFailure string
		if stage.OutputSchema != "" {
			extractedJSON, schemaErr := e.validateStageOutputSchema(result.Output, stage.OutputSchema)
			if schemaErr != nil {
				retryValidationFailure = fmt.Sprintf("JSON Schema validation failed: %s", schemaErr.Error())
			} else if extractedJSON != result.Output {
				result.Output = extractedJSON
			}
		}
		if retryValidationFailure == "" && stage.ValidationPrompt != "" {
			valid, valErr := e.validateStageOutput(ctx, retryWorkflowID, stage, result.Output, stageNum)
			if valErr != nil {
				e.orchestrator.logger.Warn("Stage retry validation error",
					zap.Int("stage", stageNum),
					zap.Int("attempt", attempt),
					zap.Error(valErr))
			} else if !valid {
				retryValidationFailure = fmt.Sprintf("LLM validation failed against criteria: %s", stage.ValidationPrompt)
			}
		}

		if retryValidationFailure == "" {
			e.orchestrator.logger.Info("Stage passed validation after retry",
				zap.Int("stage", stageNum),
				zap.Int("attempt", attempt))
			return result, model
		}

		// Update failure info for next retry prompt
		failedOutput = result.Output
		validationFailure = retryValidationFailure
	}

	e.orchestrator.logger.Warn("Stage failed validation after all retries, continuing with last output",
		zap.Int("stage", stageNum),
		zap.Int("retries", maxRetries))
	return nil, ""
}

// buildStageRetryPrompt constructs a retry prompt that explains what went wrong
// and shows the expected output format. When includeValidValues is true (the default),
// the JSON schema or validation criteria is included in the prompt.
func (e *PipelineExecutor) buildStageRetryPrompt(originalPrompt, failedOutput, validationFailure, schema string, includeValidValues bool, attempt, maxRetries int) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("⚠️ OUTPUT VALIDATION FAILED (retry %d of %d)\n\n", attempt, maxRetries))

	// Show failed output (truncated)
	truncated := failedOutput
	if len(truncated) > 500 {
		truncated = truncated[:500] + "... (truncated)"
	}
	sb.WriteString("YOUR PREVIOUS OUTPUT:\n---\n")
	sb.WriteString(truncated)
	sb.WriteString("\n---\n\n")

	// Explain why it failed
	sb.WriteString("WHY IT FAILED:\n")
	sb.WriteString(validationFailure)
	sb.WriteString("\n\n")

	// Include schema/criteria when includeValidValues is true (default behavior).
	// Proto3 bool default is false; callers should pass true unless explicitly disabled.
	if includeValidValues && schema != "" {
		sb.WriteString("REQUIRED JSON SCHEMA:\n")
		sb.WriteString(schema)
		sb.WriteString("\n\n")
		sb.WriteString("WHAT TO DO:\n")
		sb.WriteString("1. Your output MUST be valid JSON conforming to the schema above.\n")
		sb.WriteString("2. Output ONLY the JSON object — no markdown, no explanation, no code fences.\n")
		sb.WriteString("3. Ensure all required fields are present and have the correct types.\n\n")
	} else {
		sb.WriteString("WHAT TO DO:\n")
		sb.WriteString("1. Re-read the original task below.\n")
		sb.WriteString("2. Ensure your output satisfies the validation criteria.\n")
		sb.WriteString("3. If the validation expects a specific format (JSON, structured data, etc.),\n")
		sb.WriteString("   output ONLY that format with no surrounding explanation.\n\n")
	}

	sb.WriteString("ORIGINAL TASK:\n")
	sb.WriteString(originalPrompt)

	return sb.String()
}

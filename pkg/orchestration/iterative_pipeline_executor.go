// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"go.uber.org/zap"
)

// Stage output truncation to prevent token bloat
const (
	// MaxStageOutputBytes limits stage output size passed to next stage via {{previous}}
	// This prevents exponential token growth in multi-stage pipelines.
	// 8KB is reasonable for most stage outputs (reports, summaries, query results).
	// Too small (2KB) causes context loss and downstream stage confusion.
	// Too large risks token bloat in long pipelines.
	MaxStageOutputBytes = 8192

	// StageOutputTruncationNoticeTemplate is appended when output is truncated.
	// %s is replaced with the SharedMemory key for fetching full output.
	StageOutputTruncationNoticeTemplate = "\n\n[OUTPUT TRUNCATED - Full data stored in SharedMemory. Use shared_memory_read(namespace=\"workflow\", key=\"%s\") to fetch complete output]"
)

// IterativePipelineExecutor executes an iterative workflow pattern with restart coordination.
// Stages can trigger restarts of earlier stages via pub/sub messaging, enabling
// autonomous agent negotiation and self-correction.
type IterativePipelineExecutor struct {
	orchestrator *Orchestrator
	pattern      *loomv1.IterativeWorkflowPattern
	messageBus   *communication.MessageBus

	// Track iterations and restart state
	currentIteration int
	restartRequests  chan *loomv1.RestartRequest
	restartResponses map[string]chan *loomv1.RestartResponse

	// Cooldown tracking
	lastRestartTime map[string]time.Time

	// Goroutine tracking for clean shutdown
	wg sync.WaitGroup

	// Structured context for agent hallucination prevention
	structuredContext *StructuredContext
}

// NewIterativePipelineExecutor creates a new iterative pipeline executor.
func NewIterativePipelineExecutor(
	orchestrator *Orchestrator,
	pattern *loomv1.IterativeWorkflowPattern,
	messageBus *communication.MessageBus,
) *IterativePipelineExecutor {
	return &IterativePipelineExecutor{
		orchestrator:     orchestrator,
		pattern:          pattern,
		messageBus:       messageBus,
		currentIteration: 0,
		restartRequests:  make(chan *loomv1.RestartRequest, 10),
		restartResponses: make(map[string]chan *loomv1.RestartResponse),
		lastRestartTime:  make(map[string]time.Time),
	}
}

// truncateStageOutput limits stage output size to prevent token bloat.
// Returns truncated output and whether truncation occurred.
// If memoryKey is provided and truncation occurs, the notice will include
// instructions for fetching full output from SharedMemory.
func truncateStageOutput(output string, maxBytes int, memoryKey string) (string, bool) {
	if len(output) <= maxBytes {
		return output, false
	}

	// Try to truncate at a sensible boundary (newline)
	truncated := output[:maxBytes]
	if lastNewline := strings.LastIndex(truncated, "\n"); lastNewline > maxBytes/2 {
		truncated = truncated[:lastNewline]
	}

	// Include reference to SharedMemory key if provided
	notice := fmt.Sprintf(StageOutputTruncationNoticeTemplate, memoryKey)
	return truncated + notice, true
}

// Execute runs the iterative pipeline with restart coordination.
func (e *IterativePipelineExecutor) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
	startTime := time.Now()
	workflowID := fmt.Sprintf("iterative-pipeline-%s", uuid.New().String()[:8])

	// Start workflow-level span
	ctx, workflowSpan := e.orchestrator.tracer.StartSpan(ctx, "workflow.iterative_pipeline")
	defer e.orchestrator.tracer.EndSpan(workflowSpan)

	if workflowSpan != nil {
		workflowSpan.SetAttribute("workflow.type", "iterative_pipeline")
		workflowSpan.SetAttribute("workflow.id", workflowID)
		workflowSpan.SetAttribute("pipeline.stage_count", fmt.Sprintf("%d", len(e.pattern.Pipeline.Stages)))
		workflowSpan.SetAttribute("iterative.max_iterations", fmt.Sprintf("%d", e.pattern.MaxIterations))
	}

	// Set defaults
	maxIterations := e.pattern.MaxIterations
	if maxIterations == 0 {
		maxIterations = 3 // Default max iterations
	}

	restartTopic := e.pattern.RestartTopic
	if restartTopic == "" {
		restartTopic = "workflow.restart"
	}

	// Check if restart policy is enabled
	if e.pattern.RestartPolicy == nil || !e.pattern.RestartPolicy.Enabled {
		e.orchestrator.logger.Info("Restart policy disabled, executing as standard pipeline")
		// Fall back to standard pipeline execution
		executor := NewPipelineExecutor(e.orchestrator, e.pattern.Pipeline)
		return executor.Execute(ctx)
	}

	maxValidationRetries := int(e.pattern.RestartPolicy.MaxValidationRetries)
	if maxValidationRetries == 0 {
		maxValidationRetries = 2 // Default
	}

	e.orchestrator.logger.Info("Starting iterative pipeline execution",
		zap.Int("stages", len(e.pattern.Pipeline.Stages)),
		zap.Int32("max_iterations", maxIterations),
		zap.String("restart_topic", restartTopic),
		zap.Int("max_validation_retries", maxValidationRetries))

	// Create cancellable context for subscription
	subCtx, cancelSub := context.WithCancel(ctx)
	defer cancelSub()

	// Subscribe to restart topic
	e.subscribeToRestarts(subCtx, restartTopic)
	defer e.unsubscribeFromRestarts(restartTopic)

	// Execute pipeline with restart handling
	result, err := e.executeWithRestarts(ctx, workflowID, maxIterations)
	if err != nil {
		return nil, err
	}

	duration := time.Since(startTime)
	e.orchestrator.logger.Info("Iterative pipeline completed",
		zap.Duration("duration", duration),
		zap.Int("iterations", e.currentIteration),
		zap.Float64("total_cost_usd", result.Cost.TotalCostUsd))

	// Cancel subscription context to signal goroutine to exit
	cancelSub()

	// Wait for background goroutines to complete before returning
	// This ensures clean shutdown without logging after test completion
	e.wg.Wait()

	// Add iteration metadata
	result.Metadata["iterations"] = fmt.Sprintf("%d", e.currentIteration)
	result.DurationMs = duration.Milliseconds()

	return result, nil
}

// executeWithRestarts runs the pipeline and handles restart requests.
func (e *IterativePipelineExecutor) executeWithRestarts(ctx context.Context, workflowID string, maxIterations int32) (*loomv1.WorkflowResult, error) {
	// Track stage outputs across iterations
	stageOutputs := make(map[string]string) // stage_id -> output
	allResults := make([]*loomv1.AgentResult, 0)
	modelsUsed := make(map[string]string)

	e.currentIteration = 1

	// Initialize structured context for hallucination prevention
	ctx, contextSpan := e.orchestrator.tracer.StartSpan(ctx, "workflow.structured_context.init")
	e.structuredContext = NewStructuredContext(workflowID, "npath-discovery")
	if contextSpan != nil {
		contextSpan.SetAttribute("workflow_id", workflowID)
		contextSpan.SetAttribute("schema_version", e.structuredContext.WorkflowContext.SchemaVer)
		contextSpan.SetAttribute("workflow_type", "npath-discovery")
	}
	e.orchestrator.tracer.EndSpan(contextSpan)
	e.orchestrator.logger.Info("Initialized structured context",
		zap.String("workflow_id", workflowID),
		zap.String("schema_version", e.structuredContext.WorkflowContext.SchemaVer))

	// Create context with cancellation for restart handling
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Execute pipeline stages
	currentInput := e.pattern.Pipeline.InitialPrompt
	stageIndex := 0

	for e.currentIteration <= int(maxIterations) {
		if stageIndex >= len(e.pattern.Pipeline.Stages) {
			// Pipeline completed successfully
			break
		}

		stage := e.pattern.Pipeline.Stages[stageIndex]
		stageNum := stageIndex + 1

		e.orchestrator.logger.Info("Executing iterative pipeline stage",
			zap.Int("iteration", e.currentIteration),
			zap.Int("stage", stageNum),
			zap.Int("total_stages", len(e.pattern.Pipeline.Stages)),
			zap.String("agent_id", stage.AgentId))

		// Build prompt for this stage with structured context
		_, promptSpan := e.orchestrator.tracer.StartSpan(execCtx, "workflow.structured_context.build_prompt")
		if promptSpan != nil {
			promptSpan.SetAttribute("stage_id", stage.AgentId)
			promptSpan.SetAttribute("stage_num", fmt.Sprintf("%d", stageNum))
			promptSpan.SetAttribute("has_structured_context", fmt.Sprintf("%t", e.structuredContext != nil))
		}

		// Pass all previous stage outputs (each already truncated to MaxStageOutputBytes)
		// Per-stage truncation prevents token bloat while maintaining full context visibility
		outputsSlice := make([]string, 0, stageIndex)
		for i := 0; i < stageIndex; i++ {
			if out, ok := stageOutputs[e.pattern.Pipeline.Stages[i].AgentId]; ok {
				outputsSlice = append(outputsSlice, out)
			}
		}
		prompt := e.buildStagePromptWithStructuredContext(stage, currentInput, outputsSlice)

		if promptSpan != nil {
			// Add context size metrics
			if e.structuredContext != nil {
				contextJSON, _ := e.structuredContext.ToJSON()
				promptSpan.SetAttribute("structured_context_size", fmt.Sprintf("%d", len(contextJSON)))
				promptSpan.SetAttribute("stage_outputs_count", fmt.Sprintf("%d", len(e.structuredContext.StageOutputs)))
			}
		}
		e.orchestrator.tracer.EndSpan(promptSpan)

		// Execute stage with retry logic for validation failures
		executor := NewPipelineExecutor(e.orchestrator, e.pattern.Pipeline)

		// Retry configuration from restart policy
		// Read directly from proto to detect explicit 0 (skip validation)
		protoMaxRetries := int(e.pattern.RestartPolicy.MaxValidationRetries)
		skipValidation := (e.pattern.RestartPolicy != nil && protoMaxRetries == 0)

		maxRetries := protoMaxRetries
		if !skipValidation && maxRetries == 0 {
			maxRetries = 2 // Default if not explicitly set to 0
		}
		var result *loomv1.AgentResult
		var model string
		var validationErr error

		for retryNum := 0; retryNum <= maxRetries; retryNum++ {
			// Use retry-specific session ID for fresh context
			retryWorkflowID := workflowID
			if retryNum > 0 {
				retryWorkflowID = fmt.Sprintf("%s-retry%d", workflowID, retryNum)
			}

			// Build prompt with validation feedback on retries
			currentPrompt := prompt
			if retryNum > 0 && validationErr != nil {
				currentPrompt = e.buildRetryPrompt(prompt, validationErr, retryNum)
			}

			// Execute stage
			var err error
			result, model, err = executor.executeStageWithSpan(execCtx, retryWorkflowID, stage, currentPrompt, stageNum)
			if err != nil {
				return nil, fmt.Errorf("stage %d (iteration %d) failed: %w", stageNum, e.currentIteration, err)
			}

			// Log actual output for debugging (truncate to 500 chars)
			outputPreview := result.Output
			if len(outputPreview) > 500 {
				outputPreview = outputPreview[:500] + "... (truncated)"
			}
			e.orchestrator.logger.Debug("Stage output received",
				zap.String("stage_id", stage.AgentId),
				zap.Int("stage_num", stageNum),
				zap.Int("retry_num", retryNum),
				zap.Int("output_length", len(result.Output)),
				zap.String("output_preview", outputPreview))

			// Skip validation if max_validation_retries is explicitly set to 0
			// This allows two-agent pipelines where Stage Na outputs conversationally
			// and Stage Nb handles XML/JSON formatting
			if skipValidation {
				e.orchestrator.logger.Debug("Skipping validation (max_validation_retries: 0)",
					zap.String("stage_id", stage.AgentId),
					zap.Int("stage_num", stageNum))
				break
			}

			// Validate output structure (zero cost check)
			validationErr = ValidateOutputStructure(result.Output)
			if validationErr == nil {
				// Validation passed!
				if retryNum > 0 {
					e.orchestrator.logger.Info("Stage output validation passed after retry",
						zap.String("stage_id", stage.AgentId),
						zap.Int("stage_num", stageNum),
						zap.Int("retry_num", retryNum))
				}
				break
			}

			// Validation failed
			if retryNum < maxRetries {
				e.orchestrator.logger.Warn("Stage output validation failed, retrying with fresh context",
					zap.String("stage_id", stage.AgentId),
					zap.Int("stage_num", stageNum),
					zap.Int("retry_num", retryNum+1),
					zap.Int("max_retries", maxRetries),
					zap.Error(validationErr))
				// Continue to next retry iteration
				continue
			} else {
				// Max retries reached
				e.orchestrator.logger.Error("Stage output failed structure validation after max retries",
					zap.String("stage_id", stage.AgentId),
					zap.Int("stage_num", stageNum),
					zap.Int("retries_attempted", maxRetries),
					zap.Error(validationErr),
					zap.String("hint", "Agent must output valid JSON or XML with required structure"))
				// Continue workflow with failed validation
			}
		}

		// Track model used
		if model != "" {
			modelsUsed[stage.AgentId] = model
		}

		// Hybrid context passing: store full output in SharedMemory, pass truncated summary
		// SharedMemory key format: "stage-N-output" (e.g., "stage-1-output")
		stageMemoryKey := fmt.Sprintf("stage-%d-output", stageNum)

		// Store FULL (non-truncated) output in SharedMemory for on-demand retrieval
		if err := e.storeStageOutputInMemory(execCtx, stageMemoryKey, stage.AgentId, result.Output); err != nil {
			e.orchestrator.logger.Warn("Failed to store full output in SharedMemory (continuing with truncation only)",
				zap.String("stage_id", stage.AgentId),
				zap.Int("stage_num", stageNum),
				zap.String("memory_key", stageMemoryKey),
				zap.Error(err))
		} else {
			e.orchestrator.logger.Debug("Stored full stage output in SharedMemory",
				zap.String("stage_id", stage.AgentId),
				zap.Int("stage_num", stageNum),
				zap.String("memory_key", stageMemoryKey),
				zap.Int("output_size", len(result.Output)))
		}

		// Store truncated output for context passing (includes SharedMemory reference if truncated)
		truncatedForStorage, _ := truncateStageOutput(result.Output, MaxStageOutputBytes, stageMemoryKey)
		stageOutputs[stage.AgentId] = truncatedForStorage
		allResults = append(allResults, result)

		// Try to parse stage output as JSON and add to structured context
		if e.structuredContext != nil {
			_, parseSpan := e.orchestrator.tracer.StartSpan(execCtx, "workflow.structured_context.parse_output")
			if parseSpan != nil {
				parseSpan.SetAttribute("stage_id", stage.AgentId)
				parseSpan.SetAttribute("stage_num", fmt.Sprintf("%d", stageNum))
				if validationErr != nil {
					parseSpan.SetAttribute("validation_success", "false")
					parseSpan.SetAttribute("validation_error", validationErr.Error())
				} else {
					parseSpan.SetAttribute("validation_success", "true")
				}
			}

			stageKey := fmt.Sprintf("stage-%d", stageNum)

			// Then try to parse and add to context
			if err := e.parseAndAddStageOutput(stageKey, stage.AgentId, result.Output); err != nil {
				if parseSpan != nil {
					parseSpan.SetAttribute("parse_success", "false")
					parseSpan.SetAttribute("error", err.Error())
				}
				e.orchestrator.logger.Warn("Failed to parse stage output as structured format (continuing with raw output)",
					zap.String("stage_id", stage.AgentId),
					zap.Int("stage_num", stageNum),
					zap.Error(err))
				// Not a fatal error - continue with normal workflow
			} else {
				if parseSpan != nil {
					parseSpan.SetAttribute("parse_success", "true")
					parseSpan.SetAttribute("validation_success", "true")
					parseSpan.SetAttribute("stage_key", stageKey)
				}
				e.orchestrator.logger.Info("Added structured stage output to context",
					zap.String("stage_id", stage.AgentId),
					zap.Int("stage_num", stageNum),
					zap.String("stage_key", stageKey))
			}
			e.orchestrator.tracer.EndSpan(parseSpan)
		}

		// Extract and save HTML if present in output (handles LLM not using tool_use properly)
		// Target path matches the prompt instruction for Stage 8
		htmlPath := e.extractAndSaveHTML(result.Output, "internal/reports/npath_report.html")
		if htmlPath != "" {
			stageOutputs[stage.AgentId+"_html_report"] = htmlPath
		}

		// Update current input for next stage (use truncated output with SharedMemory reference)
		// Full output already stored in SharedMemory above - agents can fetch via shared_memory_read
		truncatedOutput, wasTruncated := truncateStageOutput(result.Output, MaxStageOutputBytes, stageMemoryKey)
		if wasTruncated {
			e.orchestrator.logger.Info("Stage output truncated (full data available in SharedMemory)",
				zap.String("stage_id", stage.AgentId),
				zap.Int("stage_num", stageNum),
				zap.Int("original_size", len(result.Output)),
				zap.Int("truncated_size", len(truncatedOutput)),
				zap.String("memory_key", stageMemoryKey))
		}
		currentInput = truncatedOutput

		// Check for restart requests (non-blocking)
		select {
		case restartReq := <-e.restartRequests:
			e.orchestrator.logger.Info("Processing restart request",
				zap.String("requester", restartReq.RequesterStageId),
				zap.String("target", restartReq.TargetStageId),
				zap.String("reason", restartReq.Reason))

			// Validate restart request
			if err := e.validateRestartRequest(restartReq, stageIndex); err != nil {
				e.orchestrator.logger.Warn("Invalid restart request",
					zap.Error(err))
				e.sendRestartResponse(restartReq.RequesterStageId, &loomv1.RestartResponse{
					TargetStageId: restartReq.TargetStageId,
					Success:       false,
					Error:         err.Error(),
					Iteration:     int32(e.currentIteration),
				})
				// Continue with normal flow
				stageIndex++
				continue
			}

			// Apply cooldown if configured
			if e.pattern.RestartPolicy.CooldownSeconds > 0 {
				if lastRestart, ok := e.lastRestartTime[restartReq.TargetStageId]; ok {
					elapsed := time.Since(lastRestart)
					cooldown := time.Duration(e.pattern.RestartPolicy.CooldownSeconds) * time.Second
					if elapsed < cooldown {
						err := fmt.Errorf("cooldown period not elapsed (%.1fs remaining)", (cooldown - elapsed).Seconds())
						e.orchestrator.logger.Warn("Restart request in cooldown",
							zap.Error(err))
						e.sendRestartResponse(restartReq.RequesterStageId, &loomv1.RestartResponse{
							TargetStageId: restartReq.TargetStageId,
							Success:       false,
							Error:         err.Error(),
							Iteration:     int32(e.currentIteration),
						})
						stageIndex++
						continue
					}
				}
			}

			// Find target stage index
			targetIndex := -1
			for i, s := range e.pattern.Pipeline.Stages {
				if s.AgentId == restartReq.TargetStageId {
					targetIndex = i
					break
				}
			}

			if targetIndex == -1 {
				err := fmt.Errorf("target stage not found: %s", restartReq.TargetStageId)
				e.sendRestartResponse(restartReq.RequesterStageId, &loomv1.RestartResponse{
					TargetStageId: restartReq.TargetStageId,
					Success:       false,
					Error:         err.Error(),
					Iteration:     int32(e.currentIteration),
				})
				stageIndex++
				continue
			}

			// Execute restart
			e.currentIteration++
			if e.currentIteration > int(maxIterations) {
				return nil, fmt.Errorf("max iterations (%d) exceeded", maxIterations)
			}

			e.orchestrator.logger.Info("Executing restart",
				zap.String("target", restartReq.TargetStageId),
				zap.Int("target_stage", targetIndex+1),
				zap.Int("new_iteration", e.currentIteration))

			// Clear outputs from target stage onward if not preserving
			if !e.pattern.RestartPolicy.PreserveOutputs {
				for i := targetIndex; i < len(e.pattern.Pipeline.Stages); i++ {
					delete(stageOutputs, e.pattern.Pipeline.Stages[i].AgentId)
				}
			}

			// Reset shared memory if configured
			if e.pattern.RestartPolicy.ResetSharedMemory && e.orchestrator.sharedMemory != nil {
				// This is a destructive operation - use with caution
				e.orchestrator.logger.Warn("Resetting shared memory on restart")

				// Implement selective namespace reset for workflow namespace only
				if err := e.resetWorkflowNamespace(ctx); err != nil {
					e.orchestrator.logger.Error("Failed to reset workflow namespace",
						zap.Error(err))
					// Continue execution despite reset failure
				}
			}

			// Update last restart time
			e.lastRestartTime[restartReq.TargetStageId] = time.Now()

			// Jump back to target stage
			stageIndex = targetIndex

			// Send success response
			e.sendRestartResponse(restartReq.RequesterStageId, &loomv1.RestartResponse{
				TargetStageId: restartReq.TargetStageId,
				Success:       true,
				Iteration:     int32(e.currentIteration),
			})

			// Continue from target stage
			continue

		default:
			// No restart request, continue normally
			stageIndex++
		}
	}

	// Calculate final output (last stage's output)
	var finalOutput string
	if len(allResults) > 0 {
		finalOutput = allResults[len(allResults)-1].Output
	}

	// Calculate total cost
	executor := NewPipelineExecutor(e.orchestrator, e.pattern.Pipeline)
	cost := executor.calculateCost(allResults)

	return &loomv1.WorkflowResult{
		PatternType:  "iterative_pipeline",
		AgentResults: allResults,
		MergedOutput: finalOutput,
		Metadata: map[string]string{
			"stage_count":      fmt.Sprintf("%d", len(e.pattern.Pipeline.Stages)),
			"max_iterations":   fmt.Sprintf("%d", maxIterations),
			"iterations_used":  fmt.Sprintf("%d", e.currentIteration),
			"restarts_enabled": fmt.Sprintf("%t", e.pattern.RestartPolicy.Enabled),
		},
		Cost:       cost,
		ModelsUsed: modelsUsed,
	}, nil
}

// buildStagePromptWithStructuredContext constructs the prompt with structured context injection.
// For hybrid context passing (v3.13+), also prepends a SharedMemory context header that lists
// available stage outputs agents can fetch via shared_memory_read tool.
func (e *IterativePipelineExecutor) buildStagePromptWithStructuredContext(stage *loomv1.PipelineStage, previousOutput string, allOutputs []string) string {
	executor := NewPipelineExecutor(e.orchestrator, e.pattern.Pipeline)
	basePrompt := executor.buildStagePromptWithContext(stage, previousOutput, allOutputs, e.structuredContext)

	// Prepend SharedMemory context header if there are previous stages
	if len(allOutputs) > 0 {
		header := e.buildSharedMemoryContextHeader(len(allOutputs))
		return header + basePrompt
	}

	return basePrompt
}

// buildSharedMemoryContextHeader creates a header listing available SharedMemory keys.
// This enables agents to fetch full context from previous stages even when prompts use {{previous}}.
func (e *IterativePipelineExecutor) buildSharedMemoryContextHeader(stageCount int) string {
	var builder strings.Builder
	builder.WriteString("## AVAILABLE CONTEXT (SharedMemory)\n")
	builder.WriteString("Full outputs from previous stages are stored in SharedMemory.\n")
	builder.WriteString("Use `shared_memory_read(namespace=\"workflow\", key=\"stage-N-output\")` to fetch any stage's complete output.\n\n")
	builder.WriteString("Available keys:\n")

	for i := 1; i <= stageCount; i++ {
		builder.WriteString(fmt.Sprintf("- `stage-%d-output`\n", i))
	}

	builder.WriteString("\n---\n\n")
	return builder.String()
}

// parseAndAddStageOutput parses agent output as JSON and adds to structured context.
// Supports both v3.8 (nested) and v3.9 (flat) JSON formats.
func (e *IterativePipelineExecutor) parseAndAddStageOutput(stageKey, agentID, output string) error {
	// Step 1: Strip thinking tags if present
	cleanedOutput := output
	if strings.Contains(output, "<thinking>") {
		thinkingStart := strings.Index(output, "<thinking>")
		thinkingEnd := strings.Index(output, "</thinking>")
		if thinkingStart != -1 && thinkingEnd != -1 && thinkingEnd > thinkingStart {
			cleanedOutput = output[:thinkingStart] + output[thinkingEnd+11:]
		}
	}

	// Step 2: Try JSON parsing first
	stageOutputData, jsonErr := e.tryParseJSON(cleanedOutput)

	// Step 3: If JSON fails, try XML parsing
	if jsonErr != nil {
		xmlData, xmlErr := e.tryParseXML(cleanedOutput)
		if xmlErr != nil {
			// Both formats failed
			return fmt.Errorf("failed to parse as JSON (%v) or XML (%v)", jsonErr, xmlErr)
		}
		stageOutputData = xmlData
	}

	// Step 4: Create StageOutput from extracted data
	stageOutput := StageOutput{
		StageID:     agentID,
		Status:      "completed",
		StartedAt:   time.Now(),
		CompletedAt: time.Now(),
	}

	// Override stage_id if present in data
	if stageID, ok := stageOutputData["stage_id"].(string); ok {
		stageOutput.StageID = stageID
	}

	// Override status if present in data
	if status, ok := stageOutputData["status"].(string); ok {
		stageOutput.Status = status
	}

	// Extract inputs (optional)
	if inputs, ok := stageOutputData["inputs"].(map[string]interface{}); ok {
		stageOutput.Inputs = inputs
	}

	// Extract outputs (required)
	if outputs, ok := stageOutputData["outputs"].(map[string]interface{}); ok {
		stageOutput.Outputs = outputs
	} else {
		return fmt.Errorf("no 'outputs' field in stage output")
	}

	// Extract evidence (optional)
	if evidence, ok := stageOutputData["evidence"].(map[string]interface{}); ok {
		stageOutput.Evidence = Evidence{}

		// Extract tool_calls
		if toolCalls, ok := evidence["tool_calls"].([]interface{}); ok {
			for _, tc := range toolCalls {
				if tcMap, ok := tc.(map[string]interface{}); ok {
					toolCall := ToolCall{}
					if name, ok := tcMap["tool_name"].(string); ok {
						toolCall.ToolName = name
					}
					if params, ok := tcMap["parameters"].(map[string]interface{}); ok {
						toolCall.Parameters = params
					}
					if summary, ok := tcMap["result_summary"].(string); ok {
						toolCall.ResultSummary = summary
					}
					stageOutput.Evidence.ToolCalls = append(stageOutput.Evidence.ToolCalls, toolCall)
				}
			}
		}

		// Extract queries_executed
		if queries, ok := evidence["queries_executed"].([]interface{}); ok {
			for _, q := range queries {
				if qStr, ok := q.(string); ok {
					stageOutput.Evidence.QueriesExecuted = append(stageOutput.Evidence.QueriesExecuted, qStr)
				}
			}
		}
	}

	// Add to structured context
	return e.structuredContext.AddStageOutput(stageKey, stageOutput)
}

// tryParseJSON attempts to extract and parse JSON from agent output
func (e *IterativePipelineExecutor) tryParseJSON(output string) (map[string]interface{}, error) {
	// Extract JSON from output (handles markdown code blocks or raw JSON)
	var jsonStr string
	if strings.Contains(output, "```json") {
		startIdx := strings.Index(output, "```json")
		if startIdx != -1 {
			startIdx += 7
			endIdx := strings.Index(output[startIdx:], "```")
			if endIdx != -1 {
				jsonStr = output[startIdx : startIdx+endIdx]
			}
		}
	}
	if jsonStr == "" {
		startIdx := strings.Index(output, "{")
		endIdx := strings.LastIndex(output, "}")
		if startIdx != -1 && endIdx != -1 && endIdx > startIdx {
			jsonStr = output[startIdx : endIdx+1]
		} else {
			return nil, fmt.Errorf("no JSON object found in output")
		}
	}

	// Parse JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	// Detect format and extract stage data
	var stageOutputData map[string]interface{}

	if stageOutputsRaw, ok := parsed["stage_outputs"]; ok {
		// OLD FORMAT (v3.8): {"stage_outputs": {"stage-N": {...}}}
		stageOutputsMap, ok := stageOutputsRaw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("'stage_outputs' is not an object")
		}

		// Find the stage output data
		for _, value := range stageOutputsMap {
			if data, ok := value.(map[string]interface{}); ok {
				stageOutputData = data
				break
			}
		}

		if stageOutputData == nil {
			return nil, fmt.Errorf("no stage output data found in stage_outputs")
		}
	} else {
		// NEW FORMAT (v3.9): {"stage_id": "...", "status": "...", "outputs": {...}}
		stageOutputData = parsed
	}

	return stageOutputData, nil
}

// tryParseXML attempts to extract and parse XML from agent output
func (e *IterativePipelineExecutor) tryParseXML(output string) (map[string]interface{}, error) {
	// Extract XML between <stage_output> tags
	startTag := "<stage_output>"
	endTag := "</stage_output>"

	startIdx := strings.Index(output, startTag)
	endIdx := strings.Index(output, endTag)

	if startIdx == -1 || endIdx == -1 || endIdx <= startIdx {
		return nil, fmt.Errorf("no <stage_output> tags found in output")
	}

	xmlStr := output[startIdx : endIdx+len(endTag)]

	// Define XML structure matching the expected format
	type XMLOutput struct {
		StageID string `xml:"stage_id"`
		Status  string `xml:"status"`
		Summary string `xml:"summary"`
		Outputs struct {
			Content []byte `xml:",innerxml"`
		} `xml:"outputs"`
		Inputs struct {
			Content []byte `xml:",innerxml"`
		} `xml:"inputs"`
	}

	var xmlData XMLOutput
	if err := xml.Unmarshal([]byte(xmlStr), &xmlData); err != nil {
		return nil, fmt.Errorf("invalid XML: %w", err)
	}

	// Convert to map[string]interface{} format expected by rest of code
	result := make(map[string]interface{})

	if xmlData.StageID != "" {
		result["stage_id"] = xmlData.StageID
	}
	if xmlData.Status != "" {
		result["status"] = xmlData.Status
	}
	if xmlData.Summary != "" {
		result["summary"] = xmlData.Summary
	}

	// Parse outputs as key-value pairs
	if len(xmlData.Outputs.Content) > 0 {
		outputs := make(map[string]interface{})
		// Simple XML parsing: extract <key>value</key> pairs
		content := string(xmlData.Outputs.Content)
		e.parseXMLKeyValues(content, outputs)
		if len(outputs) > 0 {
			result["outputs"] = outputs
		}
	} else {
		// FALLBACK: If no <outputs> tag, parse remaining XML elements as outputs
		// This handles agents that put data directly in <stage_output> without wrapping in <outputs>
		outputs := make(map[string]interface{})
		// Extract content between <stage_output> tags
		innerContent := xmlStr[len("<stage_output>") : len(xmlStr)-len("</stage_output>")]
		e.parseXMLKeyValues(innerContent, outputs)
		// Remove known fields that aren't outputs
		delete(outputs, "stage_id")
		delete(outputs, "status")
		delete(outputs, "summary")
		delete(outputs, "inputs")
		delete(outputs, "outputs")
		if len(outputs) > 0 {
			result["outputs"] = outputs
			e.orchestrator.logger.Debug("Parsed outputs from fallback (no <outputs> wrapper)",
				zap.Int("output_count", len(outputs)))
		}
	}

	// Parse inputs as key-value pairs (optional)
	if len(xmlData.Inputs.Content) > 0 {
		inputs := make(map[string]interface{})
		content := string(xmlData.Inputs.Content)
		e.parseXMLKeyValues(content, inputs)
		if len(inputs) > 0 {
			result["inputs"] = inputs
		}
	}

	return result, nil
}

// parseXMLKeyValues extracts <key>value</key> pairs from XML content
func (e *IterativePipelineExecutor) parseXMLKeyValues(content string, target map[string]interface{}) {
	// Find all <key>value</key> patterns
	for {
		startIdx := strings.Index(content, "<")
		if startIdx == -1 {
			break
		}

		endOfTag := strings.Index(content[startIdx:], ">")
		if endOfTag == -1 {
			break
		}
		endOfTag += startIdx

		tagName := content[startIdx+1 : endOfTag]
		if strings.HasPrefix(tagName, "/") || strings.Contains(tagName, " ") {
			content = content[endOfTag+1:]
			continue
		}

		closeTag := fmt.Sprintf("</%s>", tagName)
		closeIdx := strings.Index(content, closeTag)
		if closeIdx == -1 {
			content = content[endOfTag+1:]
			continue
		}

		value := strings.TrimSpace(content[endOfTag+1 : closeIdx])
		target[tagName] = value

		content = content[closeIdx+len(closeTag):]
	}
}

// subscribeToRestarts subscribes to restart messages on the message bus.
func (e *IterativePipelineExecutor) subscribeToRestarts(ctx context.Context, topic string) {
	if e.messageBus == nil {
		e.orchestrator.logger.Warn("MessageBus not available, restart coordination disabled")
		return
	}

	e.orchestrator.logger.Info("Subscribing to restart topic", zap.String("topic", topic))

	// Subscribe to restart topic
	sub, err := e.messageBus.Subscribe(ctx, "workflow-orchestrator", topic, nil, communication.DefaultMessageBufferSize)
	if err != nil {
		e.orchestrator.logger.Warn("Failed to subscribe to restart topic",
			zap.String("topic", topic),
			zap.Error(err))
		return
	}

	// Listen for restart messages in background
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		var unsubscribed bool
		defer func() {
			// Only unsubscribe if we haven't already
			if !unsubscribed {
				// Use context.Background() since original context is cancelled
				_ = e.messageBus.Unsubscribe(context.Background(), sub.ID)
			}
		}()

		for {
			select {
			case <-ctx.Done():
				// Unsubscribe while context is still valid to avoid logging after test completes
				_ = e.messageBus.Unsubscribe(ctx, sub.ID)
				unsubscribed = true
				return
			case msg := <-sub.Channel:
				// Decode restart request
				var restartReq loomv1.RestartRequest
				if err := json.Unmarshal(msg.Payload.GetValue(), &restartReq); err != nil {
					e.orchestrator.logger.Warn("Failed to decode restart request",
						zap.Error(err))
					continue
				}

				// Send to restart channel
				select {
				case e.restartRequests <- &restartReq:
				case <-ctx.Done():
					// Unsubscribe while context is still valid
					_ = e.messageBus.Unsubscribe(ctx, sub.ID)
					unsubscribed = true
					return
				default:
					e.orchestrator.logger.Warn("Restart request channel full, dropping request")
				}
			}
		}
	}()
}

// unsubscribeFromRestarts cleans up subscription.
func (e *IterativePipelineExecutor) unsubscribeFromRestarts(topic string) {
	// Subscription cleanup handled by defer in subscribeToRestarts
}

// validateRestartRequest checks if a restart request is valid.
func (e *IterativePipelineExecutor) validateRestartRequest(req *loomv1.RestartRequest, currentStageIndex int) error {
	policy := e.pattern.RestartPolicy

	// Check if restarts are enabled
	if !policy.Enabled {
		return fmt.Errorf("restart policy disabled")
	}

	// Find target stage index
	targetIndex := -1
	for i, stage := range e.pattern.Pipeline.Stages {
		if stage.AgentId == req.TargetStageId {
			targetIndex = i
			break
		}
	}

	if targetIndex == -1 {
		return fmt.Errorf("target stage not found: %s", req.TargetStageId)
	}

	// Check if target stage is earlier than current stage (no forward jumps)
	if targetIndex >= currentStageIndex {
		return fmt.Errorf("can only restart earlier stages (target: %d, current: %d)", targetIndex, currentStageIndex)
	}

	// Check if target stage is in restartable_stages list (if specified)
	if len(policy.RestartableStages) > 0 {
		allowed := false
		for _, allowedStage := range policy.RestartableStages {
			if allowedStage == req.TargetStageId {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("stage %s not in restartable_stages list", req.TargetStageId)
		}
	}

	// Check if requester is in restart_triggers list (if specified)
	if len(e.pattern.RestartTriggers) > 0 {
		allowed := false
		for _, allowedTrigger := range e.pattern.RestartTriggers {
			if allowedTrigger == req.RequesterStageId {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("stage %s not authorized to trigger restarts", req.RequesterStageId)
		}
	}

	return nil
}

// buildRetryPrompt constructs a retry prompt with validation error feedback.
func (e *IterativePipelineExecutor) buildRetryPrompt(originalPrompt string, validationErr error, retryNum int) string {
	return fmt.Sprintf(`⚠️ VALIDATION ERROR - RETRY %d

Your previous output failed JSON structure validation:

ERROR: %s

You must output valid JSON in this exact format:

`+"```json"+`
{
  "stage_outputs": {
    "stage-N": {
      "stage_id": "your-agent-id",
      "status": "completed",
      "outputs": {
        ... your actual outputs ...
      },
      "evidence": {
        "tool_calls": [
          {
            "tool_name": "tool_name",
            "parameters": {...},
            "result_summary": "..."
          }
        ],
        "queries_executed": ["..."]
      }
    }
  }
}
`+"```"+`

Now please retry the task with proper JSON output:

%s`, retryNum, validationErr.Error(), originalPrompt)
}

// sendRestartResponse sends a response back to the requesting stage.
func (e *IterativePipelineExecutor) sendRestartResponse(requesterID string, response *loomv1.RestartResponse) {
	if e.messageBus == nil {
		return
	}

	// Encode response
	payload, err := json.Marshal(response)
	if err != nil {
		e.orchestrator.logger.Warn("Failed to encode restart response",
			zap.Error(err))
		return
	}

	// Publish response on requester-specific topic
	responseTopic := fmt.Sprintf("workflow.restart.response.%s", requesterID)
	msg := &loomv1.BusMessage{
		Id:        fmt.Sprintf("restart-response-%d", time.Now().UnixNano()),
		Topic:     responseTopic,
		FromAgent: "workflow-orchestrator",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{
				Value: payload,
			},
		},
		Metadata: map[string]string{
			"requester": requesterID,
			"type":      "restart_response",
		},
	}

	ctx := context.Background()
	if _, _, err := e.messageBus.Publish(ctx, responseTopic, msg); err != nil {
		e.orchestrator.logger.Warn("Failed to publish restart response",
			zap.String("topic", responseTopic),
			zap.Error(err))
	}
}

// extractAndSaveHTML extracts HTML content from agent output and saves it to a file.
// This handles the case where the LLM outputs HTML in code blocks instead of calling file_write.
// Returns the path if HTML was extracted and saved, empty string otherwise.
func (e *IterativePipelineExecutor) extractAndSaveHTML(output, targetPath string) string {
	// Most robust approach: find complete HTML document from <!DOCTYPE to </html>
	// This works regardless of wrapper format (code block, fake function call, raw text)
	htmlDocPattern := regexp.MustCompile(`(?s)<!DOCTYPE html[^>]*>.*</html>`)
	match := htmlDocPattern.FindString(output)

	if match == "" {
		// Fallback: try lowercase doctype
		htmlDocPattern = regexp.MustCompile(`(?s)<!doctype html[^>]*>.*</html>`)
		match = htmlDocPattern.FindString(output)
	}

	if match == "" {
		return ""
	}

	htmlContent := strings.TrimSpace(match)
	if len(htmlContent) == 0 {
		return ""
	}

	// Ensure target directory exists
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		e.orchestrator.logger.Warn("Failed to create directory for HTML report",
			zap.String("path", dir),
			zap.Error(err))
		return ""
	}

	// Write the HTML file
	if err := os.WriteFile(targetPath, []byte(htmlContent), 0600); err != nil {
		e.orchestrator.logger.Warn("Failed to write HTML report",
			zap.String("path", targetPath),
			zap.Error(err))
		return ""
	}

	e.orchestrator.logger.Info("Extracted and saved HTML report from agent output",
		zap.String("path", targetPath),
		zap.Int("size", len(htmlContent)))

	return targetPath
}

// storeStageOutputInMemory stores the full (non-truncated) stage output in SharedMemory.
// This enables the hybrid context passing pattern: truncated summaries in prompts,
// full data available on-demand via shared_memory_read tool.
func (e *IterativePipelineExecutor) storeStageOutputInMemory(ctx context.Context, key, agentID, output string) error {
	if e.orchestrator.sharedMemory == nil {
		return fmt.Errorf("SharedMemory not available")
	}

	// Use WORKFLOW namespace - accessible by all agents in this workflow
	req := &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
		Key:       key,
		Value:     []byte(output),
		AgentId:   agentID,
		Metadata: map[string]string{
			"type":       "stage_output",
			"agent_id":   agentID,
			"stored_at":  time.Now().Format(time.RFC3339),
			"full_size":  fmt.Sprintf("%d", len(output)),
			"compressed": "auto", // SharedMemory auto-compresses > 1KB
		},
	}

	_, err := e.orchestrator.sharedMemory.Put(ctx, req)
	return err
}

// resetWorkflowNamespace clears all keys in the WORKFLOW namespace.
// This is used when RestartPolicy.ResetSharedMemory is enabled to provide
// a clean slate for stages after a restart.
func (e *IterativePipelineExecutor) resetWorkflowNamespace(ctx context.Context) error {
	// List all keys in the WORKFLOW namespace
	listReq := &loomv1.ListSharedMemoryKeysRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
	}

	listResp, err := e.orchestrator.sharedMemory.List(ctx, listReq)
	if err != nil {
		return fmt.Errorf("failed to list workflow namespace keys: %w", err)
	}

	// Delete each key in the namespace
	var deletionErrors []error
	for _, key := range listResp.Keys {
		deleteReq := &loomv1.DeleteSharedMemoryRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
			Key:       key,
		}

		if _, err := e.orchestrator.sharedMemory.Delete(ctx, deleteReq); err != nil {
			e.orchestrator.logger.Warn("Failed to delete key during namespace reset",
				zap.String("namespace", "workflow"),
				zap.String("key", key),
				zap.Error(err))
			deletionErrors = append(deletionErrors, err)
		}
	}

	if len(deletionErrors) > 0 {
		e.orchestrator.logger.Warn("Workflow namespace reset completed with some errors",
			zap.Int("total_keys", len(listResp.Keys)),
			zap.Int("failed", len(deletionErrors)))
		return fmt.Errorf("failed to delete %d/%d keys", len(deletionErrors), len(listResp.Keys))
	}

	e.orchestrator.logger.Info("Workflow namespace reset successfully",
		zap.Int("keys_deleted", len(listResp.Keys)))

	return nil
}

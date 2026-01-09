// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package teleprompter

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

// Variable represents an optimizable parameter in TextGrad-style optimization.
// Variables are updated based on judge feedback ("textual gradients").
//
// Example:
//
//	variable := &Variable{
//	    Name:  "system_prompt",
//	    Value: "You are a helpful SQL assistant...",
//	}
//
//	// After Backward():
//	// variable.Gradient = "[quality: 75/100] [safety: 65/100]\nSuggestions:\n- Add input validation..."
type Variable struct {
	Name     string // Parameter name (e.g., "system_prompt", "pattern_template")
	Value    string // Current value
	Gradient string // Textual gradient from judge feedback (set by Backward)
}

// JudgeGradientEngine implements TextGrad-style optimization using Loom's judge system.
//
// Traditional TextGrad uses LLM-generated "textual gradients" for optimization.
// JudgeGradientEngine improves this by using multi-dimensional judge verdicts as gradients,
// providing more structured and actionable feedback.
//
// Key features:
// - Multi-dimensional gradients (6 dimensions: quality, cost, safety, domain, perf, usability)
// - Actionable suggestions per dimension
// - Weighted aggregation of judge feedback
// - Integration with Learning Agent's improvement generation
//
// Example usage:
//
//	engine := NewJudgeGradientEngine(&JudgeGradientConfig{
//	    Orchestrator: judgeOrchestrator,
//	    JudgeIDs:     []string{"quality-judge", "safety-judge"},
//	})
//
//	variables := []*Variable{{Name: "system_prompt", Value: currentPrompt}}
//
//	// Backward pass: Extract dimension scores as gradients
//	engine.Backward(ctx, example, result, variables)
//	// variables[0].Gradient now contains: "[quality: 75/100] [safety: 65/100]\n..."
//
//	// Step: Generate and apply improvements
//	improvements, err := engine.Step(ctx, variables)
type JudgeGradientEngine struct {
	orchestrator     JudgeOrchestrator
	judgeIDs         []string
	targetDimensions []loomv1.JudgeDimension
	aggregation      loomv1.AggregationStrategy
	exportToHawk     bool
	tracer           observability.Tracer
	logger           *zap.Logger

	// Phase 10: Auto-apply fields
	autoApplyMode       loomv1.AutoApplyMode
	validationConfig    *loomv1.ValidationConfig
	learningAgentClient loomv1.LearningAgentServiceClient
	agentID             string
}

// JudgeGradientConfig configures the gradient engine
type JudgeGradientConfig struct {
	Orchestrator     JudgeOrchestrator
	JudgeIDs         []string
	TargetDimensions []loomv1.JudgeDimension
	Aggregation      loomv1.AggregationStrategy
	ExportToHawk     bool
	Tracer           observability.Tracer
	Logger           *zap.Logger

	// Phase 10: Auto-apply configuration
	AutoApplyMode    loomv1.AutoApplyMode
	ValidationConfig *loomv1.ValidationConfig

	// Phase 10: Learning agent client (for applying improvements)
	LearningAgentClient loomv1.LearningAgentServiceClient
	AgentID             string
}

// NewJudgeGradientEngine creates a new gradient engine
func NewJudgeGradientEngine(config *JudgeGradientConfig) (*JudgeGradientEngine, error) {
	if config == nil {
		return nil, fmt.Errorf("config required")
	}
	if config.Orchestrator == nil {
		return nil, fmt.Errorf("orchestrator required")
	}
	if len(config.JudgeIDs) == 0 {
		return nil, fmt.Errorf("at least one judge ID required")
	}

	// Set defaults
	aggregation := config.Aggregation
	if aggregation == loomv1.AggregationStrategy_AGGREGATION_STRATEGY_UNSPECIFIED {
		aggregation = loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE
	}

	tracer := config.Tracer
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}

	logger := config.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	// Set auto-apply defaults
	autoApplyMode := config.AutoApplyMode
	if autoApplyMode == loomv1.AutoApplyMode_AUTO_APPLY_MODE_UNSPECIFIED {
		autoApplyMode = loomv1.AutoApplyMode_AUTO_APPLY_MODE_MANUAL
	}

	return &JudgeGradientEngine{
		orchestrator:        config.Orchestrator,
		judgeIDs:            config.JudgeIDs,
		targetDimensions:    config.TargetDimensions,
		aggregation:         aggregation,
		exportToHawk:        config.ExportToHawk,
		tracer:              tracer,
		logger:              logger,
		autoApplyMode:       autoApplyMode,
		validationConfig:    config.ValidationConfig,
		learningAgentClient: config.LearningAgentClient,
		agentID:             config.AgentID,
	}, nil
}

// Backward performs the backward pass: extract dimension scores as textual gradients.
//
// This method:
// 1. Runs judge evaluation on the execution result
// 2. Extracts dimension scores from verdicts
// 3. Formats scores and suggestions as text
// 4. Stores formatted feedback in variable.Gradient
//
// The "gradient" is a human-readable summary of judge feedback that can be used
// to guide improvements. Unlike numeric gradients in traditional ML, these are
// textual explanations that can be processed by LLMs or humans.
//
// Example gradient output:
//
//	[Dimension Scores]
//	quality: 75/100
//	safety: 65/100 ⚠️
//	cost: 80/100
//
//	[Suggestions]
//	- Safety (65/100): Add input validation to prevent SQL injection
//	- Quality (75/100): Improve error handling in generated queries
//
// Parameters:
//   - ctx: Context for the operation
//   - example: The training example being evaluated
//   - result: The execution result to evaluate
//   - variables: Variables to store gradients in (modified in-place)
func (jge *JudgeGradientEngine) Backward(
	ctx context.Context,
	example *loomv1.Example,
	result *ExecutionResult,
	variables []*Variable,
) error {
	ctx, span := jge.tracer.StartSpan(ctx, observability.SpanTeleprompterBootstrap)
	defer jge.tracer.EndSpan(span)

	span.SetAttribute("engine.type", "judge_gradient")
	span.SetAttribute("judge.count", len(jge.judgeIDs))
	span.SetAttribute("variable.count", len(variables))

	jge.logger.Debug("Running backward pass",
		zap.Strings("judge_ids", jge.judgeIDs),
		zap.Int("variable_count", len(variables)),
	)

	// Construct evaluation context
	evalCtx := &loomv1.EvaluationContext{
		Prompt:   extractQuery(example),
		Response: extractResponse(result),
		Metadata: buildMetadata(example, result),
	}

	// Create evaluation request
	evalReq := &loomv1.EvaluateRequest{
		JudgeIds:       jge.judgeIDs,
		Context:        evalCtx,
		ExecutionMode:  loomv1.ExecutionMode_EXECUTION_MODE_HYBRID,
		Aggregation:    jge.aggregation,
		ExportToHawk:   jge.exportToHawk,
		TimeoutSeconds: 30,
		FailFast:       false,
	}

	// Run judges via orchestrator
	evalResp, err := jge.orchestrator.Evaluate(ctx, evalReq)
	if err != nil {
		jge.logger.Error("Judge evaluation failed",
			zap.Error(err),
			zap.Strings("judge_ids", jge.judgeIDs),
		)
		span.RecordError(err)
		return fmt.Errorf("judge evaluation failed: %w", err)
	}

	// Extract dimension scores and format as textual gradient
	gradient := jge.formatGradient(evalResp)

	// Store gradient in all variables
	// (In practice, different variables might get different gradients,
	// but for now we apply the same feedback to all)
	for _, variable := range variables {
		variable.Gradient = gradient
	}

	jge.logger.Debug("Backward pass complete",
		zap.Int("verdicts_count", len(evalResp.Verdicts)),
		zap.Float64("final_score", evalResp.FinalScore),
		zap.Bool("passed", evalResp.Passed),
	)

	span.SetAttribute("evaluation.final_score", evalResp.FinalScore)
	span.SetAttribute("evaluation.passed", evalResp.Passed)
	span.SetAttribute("evaluation.verdicts_count", len(evalResp.Verdicts))

	return nil
}

// Step performs the optimization step: generate improvements based on gradients.
//
// This method:
// 1. Analyzes the textual gradients stored in variables
// 2. Generates targeted improvement proposals per dimension
// 3. Returns improvement recommendations
//
// The improvements can then be applied to update the agent's patterns/prompts.
// This is analogous to taking an optimization step in traditional gradient descent,
// but using structured judge feedback instead of numeric gradients.
//
// Example improvements generated:
//
//	Improvement{
//	    Type: IMPROVEMENT_PARAMETER_TUNE,
//	    Description: "Safety score 65/100: Add input validation",
//	    Impact: IMPACT_CRITICAL,
//	    Details: {
//	        ExpectedSuccessRateDelta: 0.15,
//	        Rationale: "Safety score 65% below threshold...",
//	    },
//	}
//
// Parameters:
//   - ctx: Context for the operation
//   - variables: Variables with gradients from Backward()
//
// Returns:
//   - Improvement proposals based on judge feedback
func (jge *JudgeGradientEngine) Step(
	ctx context.Context,
	variables []*Variable,
) ([]*loomv1.Improvement, error) {
	ctx, span := jge.tracer.StartSpan(ctx, observability.SpanTeleprompterBootstrap)
	defer jge.tracer.EndSpan(span)

	span.SetAttribute("engine.type", "judge_gradient")
	span.SetAttribute("variable.count", len(variables))
	span.SetAttribute("auto_apply_mode", jge.autoApplyMode.String())

	jge.logger.Debug("Running optimization step",
		zap.Int("variable_count", len(variables)),
		zap.String("auto_apply_mode", jge.autoApplyMode.String()),
	)

	var improvements []*loomv1.Improvement

	// Generate improvements for each variable based on its gradient
	for _, variable := range variables {
		if variable.Gradient == "" {
			continue // No gradient, skip
		}

		// Parse dimension scores from gradient
		dimScores := jge.parseGradientScores(variable.Gradient)

		// Generate improvements for failing dimensions
		// Using the same logic as Learning Agent's generateImprovementsWithJudgeFeedback
		for dimension, score := range dimScores {
			improvement := jge.generateImprovementForDimension(
				variable,
				dimension,
				score,
			)
			if improvement != nil {
				improvements = append(improvements, improvement)
			}
		}
	}

	jge.logger.Debug("Generated improvements",
		zap.Int("improvements_count", len(improvements)),
	)

	// Phase 10: Apply based on auto-apply mode
	switch jge.autoApplyMode {
	case loomv1.AutoApplyMode_AUTO_APPLY_MODE_MANUAL:
		// Current behavior: return suggestions without applying
		jge.logger.Debug("Manual mode: returning improvements without applying")
		span.SetAttribute("improvements.applied", false)
		return improvements, nil

	case loomv1.AutoApplyMode_AUTO_APPLY_MODE_DRY_RUN:
		// Apply to temp agent, test, show results (not yet implemented)
		jge.logger.Warn("Dry-run mode not yet implemented, returning improvements")
		span.SetAttribute("improvements.applied", false)
		return improvements, nil

	case loomv1.AutoApplyMode_AUTO_APPLY_MODE_VALIDATED:
		// Apply if validation passes
		jge.logger.Info("Validated mode: applying improvements with validation")
		appliedImprovements, err := jge.applyWithValidation(ctx, improvements)
		if err != nil {
			jge.logger.Error("Failed to apply improvements with validation", zap.Error(err))
			span.RecordError(err)
			return improvements, err
		}
		span.SetAttribute("improvements.applied", true)
		span.SetAttribute("improvements.applied_count", len(appliedImprovements))
		return appliedImprovements, nil

	case loomv1.AutoApplyMode_AUTO_APPLY_MODE_AUTONOMOUS:
		// Apply immediately, monitor, rollback if needed (not yet implemented)
		jge.logger.Warn("Autonomous mode not yet implemented, returning improvements")
		span.SetAttribute("improvements.applied", false)
		return improvements, nil

	default:
		// Default to manual mode
		jge.logger.Debug("Unspecified mode: defaulting to manual")
		span.SetAttribute("improvements.applied", false)
		return improvements, nil
	}
}

// formatGradient converts judge verdicts into a textual gradient string
func (jge *JudgeGradientEngine) formatGradient(evalResp *loomv1.EvaluateResponse) string {
	var sb strings.Builder

	// Header
	sb.WriteString("[Dimension Scores]\n")

	// Collect all dimension scores across judges
	dimensionScores := make(map[string][]float64)
	for _, verdict := range evalResp.Verdicts {
		for dim, score := range verdict.DimensionScores {
			dimensionScores[dim] = append(dimensionScores[dim], score)
		}
	}

	// Calculate averages and format
	for dim, scores := range dimensionScores {
		avg := average(scores)
		status := ""
		if avg < 70.0 {
			status = " ⚠️" // Warning for low scores
		} else if avg >= 90.0 {
			status = " ✓" // Good score
		}
		sb.WriteString(fmt.Sprintf("%s: %.0f/100%s\n", dim, avg, status))
	}

	// Add overall score
	sb.WriteString(fmt.Sprintf("\nOverall Score: %.1f/100", evalResp.FinalScore))
	if evalResp.Passed {
		sb.WriteString(" ✓\n")
	} else {
		sb.WriteString(" ✗\n")
	}

	// Add suggestions section
	sb.WriteString("\n[Suggestions]\n")

	// Extract reasoning and suggestions from verdicts
	for _, verdict := range evalResp.Verdicts {
		if verdict.Reasoning != "" {
			// Format reasoning by dimension
			for dim, score := range verdict.DimensionScores {
				if score < 80.0 { // Only show suggestions for scores below 80
					sb.WriteString(fmt.Sprintf("- %s (%.0f/100): %s\n",
						dim, score, verdict.Reasoning))
				}
			}
		}
	}

	return sb.String()
}

// parseGradientScores extracts dimension scores from gradient text
func (jge *JudgeGradientEngine) parseGradientScores(gradient string) map[string]float64 {
	scores := make(map[string]float64)

	// Parse only the [Dimension Scores] section
	lines := strings.Split(gradient, "\n")
	inScoresSection := false

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Start of [Dimension Scores] section
		if line == "[Dimension Scores]" {
			inScoresSection = true
			continue
		}

		// End of section (empty line or new section starting with "[")
		if inScoresSection && (line == "" || strings.HasPrefix(line, "[")) {
			break
		}

		// Parse dimension scores (only in scores section)
		if inScoresSection && !strings.HasPrefix(line, "Overall Score:") {
			if strings.Contains(line, ":") && strings.Contains(line, "/100") {
				parts := strings.Split(line, ":")
				if len(parts) >= 2 {
					dimension := strings.TrimSpace(parts[0])
					// Skip if dimension starts with "-" (it's from suggestions section, shouldn't happen but defensive)
					if strings.HasPrefix(dimension, "-") {
						continue
					}
					// Extract score (e.g., "75/100" → 75.0)
					scoreStr := strings.TrimSpace(parts[1])
					var score float64
					_, _ = fmt.Sscanf(scoreStr, "%f/100", &score)
					scores[dimension] = score
				}
			}
		}
	}

	return scores
}

// generateImprovementForDimension creates an improvement proposal for a failing dimension
func (jge *JudgeGradientEngine) generateImprovementForDimension(
	variable *Variable,
	dimension string,
	score float64,
) *loomv1.Improvement {
	// Define thresholds (same as Learning Agent)
	thresholds := map[string]float64{
		"safety":            70.0,
		"cost":              75.0,
		"quality":           80.0,
		"correctness":       80.0,
		"domain":            75.0,
		"domain_compliance": 75.0,
		"performance":       70.0,
		"usability":         70.0,
	}

	threshold, ok := thresholds[dimension]
	if !ok {
		threshold = 75.0 // Default threshold
	}

	// Only generate improvement if below threshold
	if score >= threshold {
		return nil
	}

	severity := threshold - score

	// Create improvement based on dimension type
	var improvementType loomv1.ImprovementType
	var description string
	var impact loomv1.ImpactLevel
	var expectedDelta float64

	switch dimension {
	case "safety":
		improvementType = loomv1.ImprovementType_IMPROVEMENT_PARAMETER_TUNE
		description = fmt.Sprintf(
			"Variable '%s' failing safety evaluation (score: %.1f%%, threshold: %.1f%%). "+
				"Add guardrails or validation to prevent unsafe outputs.",
			variable.Name, score, threshold,
		)
		impact = loomv1.ImpactLevel_IMPACT_CRITICAL
		expectedDelta = min(severity*0.5, 20.0) / 100.0 // Up to 20% improvement

	case "cost":
		improvementType = loomv1.ImprovementType_IMPROVEMENT_PARAMETER_TUNE
		description = fmt.Sprintf(
			"Variable '%s' failing cost efficiency (score: %.1f%%, threshold: %.1f%%). "+
				"Reduce prompt size, optimize token usage, or use cheaper model.",
			variable.Name, score, threshold,
		)
		impact = loomv1.ImpactLevel_IMPACT_MEDIUM
		expectedDelta = min(severity*0.2, 5.0) / 100.0 // Up to 5% improvement

	case "quality", "correctness":
		improvementType = loomv1.ImprovementType_IMPROVEMENT_TEMPLATE_ADJUST
		description = fmt.Sprintf(
			"Variable '%s' failing quality/correctness (score: %.1f%%, threshold: %.1f%%). "+
				"Improve prompt template, add examples, or tune parameters.",
			variable.Name, score, threshold,
		)
		impact = loomv1.ImpactLevel_IMPACT_HIGH
		expectedDelta = min(severity*0.8, 25.0) / 100.0 // Up to 25% improvement

	case "domain", "domain_compliance":
		improvementType = loomv1.ImprovementType_IMPROVEMENT_TEMPLATE_ADJUST
		description = fmt.Sprintf(
			"Variable '%s' failing domain compliance (score: %.1f%%, threshold: %.1f%%). "+
				"Add domain-specific guidance or constraints.",
			variable.Name, score, threshold,
		)
		impact = loomv1.ImpactLevel_IMPACT_HIGH
		expectedDelta = min(severity*0.6, 20.0) / 100.0 // Up to 20% improvement

	default:
		improvementType = loomv1.ImprovementType_IMPROVEMENT_PARAMETER_TUNE
		description = fmt.Sprintf(
			"Variable '%s' - dimension '%s' below threshold (score: %.1f%%, threshold: %.1f%%).",
			variable.Name, dimension, score, threshold,
		)
		impact = loomv1.ImpactLevel_IMPACT_MEDIUM
		expectedDelta = min(severity*0.3, 10.0) / 100.0 // Up to 10% improvement
	}

	return &loomv1.Improvement{
		Id:            uuid.New().String(),
		Type:          improvementType,
		Description:   description,
		Confidence:    0.8, // High confidence for judge-based improvements
		Impact:        impact,
		TargetPattern: variable.Name,
		Status:        loomv1.ImprovementStatus_IMPROVEMENT_PENDING,
		Details: &loomv1.ImprovementDetails{
			ExpectedSuccessRateDelta: expectedDelta,
			Rationale: fmt.Sprintf(
				"Dimension '%s' scored %.1f%%, which is %.1f%% below threshold of %.1f%%. "+
					"Targeted improvements expected to address this gap.",
				dimension, score, severity, threshold,
			),
		},
	}
}

// ============================================================================
// Helper Functions
// ============================================================================

// extractQuery gets the query/question from the example inputs
func extractQuery(example *loomv1.Example) string {
	// Common input field names for queries
	for _, field := range []string{"query", "question", "input", "prompt"} {
		if val, ok := example.Inputs[field]; ok {
			return val
		}
	}

	// If no standard field found, concatenate all inputs
	query := ""
	for field, val := range example.Inputs {
		if query != "" {
			query += "\n"
		}
		query += fmt.Sprintf("%s: %s", field, val)
	}
	return query
}

// extractResponse gets the agent's response from execution result
func extractResponse(result *ExecutionResult) string {
	// Prefer "answer" or "response" outputs
	for _, field := range []string{"answer", "response", "output", "result"} {
		if val, ok := result.Outputs[field]; ok {
			return val
		}
	}

	// If no standard field found, concatenate all outputs
	response := ""
	for field, val := range result.Outputs {
		if response != "" {
			response += "\n"
		}
		response += fmt.Sprintf("%s: %s", field, val)
	}
	return response
}

// buildMetadata constructs metadata for judge evaluation
func buildMetadata(example *loomv1.Example, result *ExecutionResult) map[string]string {
	metadata := make(map[string]string)

	// Copy example metadata
	if example.Metadata != nil {
		for k, v := range example.Metadata {
			metadata[k] = v
		}
	}

	// Add execution metadata
	metadata["trace_id"] = result.TraceID
	metadata["success"] = fmt.Sprintf("%v", result.Success)

	// Add rationale if available
	if result.Rationale != "" {
		metadata["rationale"] = result.Rationale
	}

	// Add expected outputs for judges to reference
	if len(example.Outputs) > 0 {
		expectedOutputs := ""
		for field, val := range example.Outputs {
			if expectedOutputs != "" {
				expectedOutputs += "\n"
			}
			expectedOutputs += fmt.Sprintf("%s: %s", field, val)
		}
		metadata["expected_output"] = expectedOutputs
	}

	return metadata
}

// average calculates the mean of a slice of floats
func average(values []float64) float64 {
	if len(values) == 0 {
		return 0.0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// min returns the minimum of two float64 values
func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// ============================================================================
// Phase 10: Auto-Apply Implementation
// ============================================================================

// ValidationResult tracks validation performance
type ValidationResult struct {
	ScoreDelta  float64 // Improvement over baseline (positive = better)
	BaseScore   float64 // Baseline score before improvement
	NewScore    float64 // New score after improvement
	Passed      bool    // Whether improvement meets threshold
	FailedTests int     // Number of failed validation examples
}

// applyWithValidation applies improvements with validation checks
func (jge *JudgeGradientEngine) applyWithValidation(
	ctx context.Context,
	improvements []*loomv1.Improvement,
) ([]*loomv1.Improvement, error) {
	ctx, span := jge.tracer.StartSpan(ctx, observability.SpanTeleprompterBootstrap)
	defer jge.tracer.EndSpan(span)

	span.SetAttribute("validation.improvements_count", len(improvements))

	if jge.validationConfig == nil {
		return nil, fmt.Errorf("validation config required for validated mode")
	}

	if jge.learningAgentClient == nil {
		return nil, fmt.Errorf("learning agent client required for validated mode")
	}

	if jge.agentID == "" {
		return nil, fmt.Errorf("agent ID required for validated mode")
	}

	jge.logger.Info("Starting validation-based improvement application",
		zap.Int("improvements_count", len(improvements)),
		zap.Int("validation_set_size", len(jge.validationConfig.ValidationSet)),
		zap.Float64("min_score_delta", jge.validationConfig.MinScoreDelta),
	)

	// Capture baseline performance (before any improvements)
	baselineResult, err := jge.validate(ctx, jge.validationConfig.ValidationSet)
	if err != nil {
		jge.logger.Error("Baseline validation failed", zap.Error(err))
		span.RecordError(err)
		return nil, fmt.Errorf("baseline validation failed: %w", err)
	}

	jge.logger.Info("Baseline validation complete",
		zap.Float64("baseline_score", baselineResult.BaseScore),
	)

	appliedImprovements := []*loomv1.Improvement{}

	for i, imp := range improvements {
		jge.logger.Info("Testing improvement",
			zap.Int("improvement_index", i+1),
			zap.Int("total_improvements", len(improvements)),
			zap.String("improvement_id", imp.Id),
			zap.String("description", imp.Description),
		)

		// 1. Apply improvement via Learning Agent
		applyResp, err := jge.learningAgentClient.ApplyImprovement(ctx, &loomv1.ApplyImprovementRequest{
			ImprovementId: imp.Id,
			Force:         false,
		})
		if err != nil {
			jge.logger.Warn("Failed to apply improvement",
				zap.Error(err),
				zap.String("improvement_id", imp.Id),
			)
			continue
		}

		if !applyResp.Success {
			jge.logger.Warn("Improvement application rejected",
				zap.String("improvement_id", imp.Id),
				zap.String("message", applyResp.Message),
			)
			continue
		}

		jge.logger.Debug("Improvement applied, running validation",
			zap.String("improvement_id", imp.Id),
		)

		// 2. Run validation set
		validationResult, err := jge.validate(ctx, jge.validationConfig.ValidationSet)
		if err != nil {
			jge.logger.Error("Validation failed after improvement",
				zap.Error(err),
				zap.String("improvement_id", imp.Id),
			)

			// Rollback on error if configured
			if jge.validationConfig.RollbackOnFailure {
				if rollbackErr := jge.rollback(ctx, imp); rollbackErr != nil {
					jge.logger.Error("Rollback failed",
						zap.Error(rollbackErr),
						zap.String("improvement_id", imp.Id),
					)
				}
			}
			continue
		}

		// 3. Check if improvement meets threshold
		scoreDelta := validationResult.NewScore - baselineResult.BaseScore
		validationResult.ScoreDelta = scoreDelta
		validationResult.Passed = scoreDelta >= jge.validationConfig.MinScoreDelta

		jge.logger.Info("Validation complete",
			zap.String("improvement_id", imp.Id),
			zap.Float64("baseline_score", baselineResult.BaseScore),
			zap.Float64("new_score", validationResult.NewScore),
			zap.Float64("score_delta", scoreDelta),
			zap.Float64("min_threshold", jge.validationConfig.MinScoreDelta),
			zap.Bool("passed", validationResult.Passed),
		)

		if !validationResult.Passed {
			jge.logger.Info("Improvement did not meet threshold, rolling back",
				zap.String("improvement_id", imp.Id),
				zap.Float64("score_delta", scoreDelta),
				zap.Float64("min_threshold", jge.validationConfig.MinScoreDelta),
			)

			if rollbackErr := jge.rollback(ctx, imp); rollbackErr != nil {
				jge.logger.Error("Rollback failed",
					zap.Error(rollbackErr),
					zap.String("improvement_id", imp.Id),
				)
			}
			continue
		}

		// 4. Accept improvement
		jge.logger.Info("Improvement accepted",
			zap.String("improvement_id", imp.Id),
			zap.Float64("score_delta", scoreDelta),
		)
		appliedImprovements = append(appliedImprovements, imp)

		// Update baseline for next improvement
		baselineResult.BaseScore = validationResult.NewScore
	}

	jge.logger.Info("Validation-based application complete",
		zap.Int("applied_count", len(appliedImprovements)),
		zap.Int("total_count", len(improvements)),
	)

	span.SetAttribute("validation.applied_count", len(appliedImprovements))
	span.SetAttribute("validation.rejected_count", len(improvements)-len(appliedImprovements))

	return appliedImprovements, nil
}

// validate runs the agent on a validation set and computes aggregate score
func (jge *JudgeGradientEngine) validate(
	ctx context.Context,
	validationSet []*loomv1.Example,
) (*ValidationResult, error) {
	ctx, span := jge.tracer.StartSpan(ctx, observability.SpanTeleprompterBootstrap)
	defer jge.tracer.EndSpan(span)

	span.SetAttribute("validation.examples_count", len(validationSet))

	if len(validationSet) == 0 {
		return nil, fmt.Errorf("validation set is empty")
	}

	jge.logger.Debug("Running validation",
		zap.Int("validation_set_size", len(validationSet)),
	)

	// Run judge evaluation on each validation example
	scores := []float64{}
	failedTests := 0

	for i, example := range validationSet {
		// Create a mock ExecutionResult for the example
		// In a real implementation, this would execute the agent with the example inputs
		result := &ExecutionResult{
			Success:   true,
			Outputs:   example.Outputs, // For now, use expected outputs
			TraceID:   fmt.Sprintf("validation-%d", i),
			Rationale: fmt.Sprintf("Validation test %d", i),
		}

		// Construct evaluation context
		evalCtx := &loomv1.EvaluationContext{
			Prompt:   extractQuery(example),
			Response: extractResponse(result),
			Metadata: buildMetadata(example, result),
		}

		// Create evaluation request
		// NOTE: For now, we use the engine's default judge config.
		// In the future, we could use jge.validationConfig.MetricConfig to customize validation metrics.
		evalReq := &loomv1.EvaluateRequest{
			JudgeIds:       jge.judgeIDs,
			Context:        evalCtx,
			ExecutionMode:  loomv1.ExecutionMode_EXECUTION_MODE_HYBRID,
			Aggregation:    jge.aggregation,
			ExportToHawk:   false, // Don't export validation runs to Hawk
			TimeoutSeconds: 30,
			FailFast:       false,
		}

		// Run judges
		evalResp, err := jge.orchestrator.Evaluate(ctx, evalReq)
		if err != nil {
			jge.logger.Warn("Validation example evaluation failed",
				zap.Error(err),
				zap.Int("example_index", i),
			)
			failedTests++
			continue
		}

		scores = append(scores, evalResp.FinalScore)

		if !evalResp.Passed {
			failedTests++
		}
	}

	if len(scores) == 0 {
		return nil, fmt.Errorf("no validation examples succeeded")
	}

	// Compute average score
	avgScore := average(scores)

	result := &ValidationResult{
		BaseScore:   avgScore,
		NewScore:    avgScore,
		ScoreDelta:  0.0,   // Will be set by caller
		Passed:      false, // Will be set by caller
		FailedTests: failedTests,
	}

	jge.logger.Debug("Validation complete",
		zap.Float64("avg_score", avgScore),
		zap.Int("failed_tests", failedTests),
		zap.Int("total_tests", len(validationSet)),
	)

	span.SetAttribute("validation.avg_score", avgScore)
	span.SetAttribute("validation.failed_tests", failedTests)

	return result, nil
}

// rollback undoes a failed improvement
func (jge *JudgeGradientEngine) rollback(
	ctx context.Context,
	improvement *loomv1.Improvement,
) error {
	ctx, span := jge.tracer.StartSpan(ctx, observability.SpanTeleprompterBootstrap)
	defer jge.tracer.EndSpan(span)

	span.SetAttribute("rollback.improvement_id", improvement.Id)

	if jge.learningAgentClient == nil {
		return fmt.Errorf("learning agent client required for rollback")
	}

	jge.logger.Info("Rolling back improvement",
		zap.String("improvement_id", improvement.Id),
		zap.String("description", improvement.Description),
	)

	// Call Learning Agent to rollback the improvement
	rollbackResp, err := jge.learningAgentClient.RollbackImprovement(ctx, &loomv1.RollbackImprovementRequest{
		ImprovementId: improvement.Id,
		Reason:        "Validation failed: improvement did not meet threshold",
	})
	if err != nil {
		jge.logger.Error("Rollback RPC failed",
			zap.Error(err),
			zap.String("improvement_id", improvement.Id),
		)
		span.RecordError(err)
		return fmt.Errorf("rollback RPC failed: %w", err)
	}

	if !rollbackResp.Success {
		jge.logger.Error("Rollback rejected by learning agent",
			zap.String("improvement_id", improvement.Id),
			zap.String("message", rollbackResp.Message),
		)
		return fmt.Errorf("rollback rejected: %s", rollbackResp.Message)
	}

	jge.logger.Info("Rollback successful",
		zap.String("improvement_id", improvement.Id),
		zap.String("restored_version", rollbackResp.RestoredVersion),
	)

	span.SetAttribute("rollback.success", true)

	return nil
}

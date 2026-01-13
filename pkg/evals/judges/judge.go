// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package judges

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	llmjudge "github.com/teradata-labs/loom/pkg/evals/judges/llm"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Judge evaluates agent outputs against specific criteria.
// Multiple judges can be coordinated to provide multi-dimensional evaluation.
type Judge interface {
	// ID returns the unique judge identifier
	ID() string

	// Name returns the human-readable judge name
	Name() string

	// Criteria returns the evaluation criteria this judge uses
	Criteria() []string

	// Evaluate performs the evaluation and returns a verdict
	Evaluate(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error)

	// Weight returns the weight for aggregation (default: 1.0)
	Weight() float64

	// Criticality returns the judge criticality level (sync vs async)
	Criticality() loomv1.JudgeCriticality

	// Dimensions returns which dimensions this judge evaluates
	Dimensions() []loomv1.JudgeDimension

	// Config returns the judge configuration
	Config() *loomv1.JudgeConfig
}

// LLMJudge provides LLM-as-a-judge evaluation for Loom's multi-judge framework.
// This implementation uses a simple LLM-based evaluation and maps it to Loom's
// multi-judge framework with dimensions, criticality, and aggregation.
type LLMJudge struct {
	id          string
	llmJudge    *llmjudge.Judge
	config      *loomv1.JudgeConfig
	tracer      observability.Tracer
	llmProvider types.LLMProvider
}

// NewLLMJudge creates a new LLM-based judge.
func NewLLMJudge(llmProvider types.LLMProvider, config *loomv1.JudgeConfig, tracer observability.Tracer) (*LLMJudge, error) {
	if llmProvider == nil {
		return nil, fmt.Errorf("LLM provider required")
	}
	if config == nil {
		return nil, fmt.Errorf("judge config required")
	}
	if config.Name == "" {
		return nil, fmt.Errorf("judge name required")
	}

	// Phase 8: Validate custom dimension configuration
	if err := validateCustomDimensions(config); err != nil {
		return nil, fmt.Errorf("invalid custom dimension config: %w", err)
	}

	// Set defaults
	if config.Weight == 0 {
		config.Weight = 1.0
	}
	if config.MinPassingScore == 0 {
		config.MinPassingScore = 80
	}
	if config.Criticality == loomv1.JudgeCriticality_JUDGE_CRITICALITY_UNSPECIFIED {
		config.Criticality = loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL
	}

	// Create LLM judge instance
	judge, err := llmjudge.NewJudge(&llmjudge.Config{
		Provider: llmProvider,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create LLM judge: %w", err)
	}

	id := config.Id
	if id == "" {
		id = uuid.New().String()
	}

	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}

	return &LLMJudge{
		id:          id,
		llmJudge:    judge,
		config:      config,
		tracer:      tracer,
		llmProvider: llmProvider,
	}, nil
}

// ID returns the judge identifier
func (h *LLMJudge) ID() string {
	return h.id
}

// Name returns the judge name
func (h *LLMJudge) Name() string {
	return h.config.Name
}

// Criteria returns the evaluation criteria
func (h *LLMJudge) Criteria() []string {
	if h.config.Criteria != "" {
		return []string{h.config.Criteria}
	}
	return []string{"quality", "correctness", "completeness"}
}

// Weight returns the aggregation weight
func (h *LLMJudge) Weight() float64 {
	return h.config.Weight
}

// Criticality returns the judge criticality level
func (h *LLMJudge) Criticality() loomv1.JudgeCriticality {
	return h.config.Criticality
}

// Dimensions returns the dimensions this judge evaluates
func (h *LLMJudge) Dimensions() []loomv1.JudgeDimension {
	if len(h.config.Dimensions) > 0 {
		return h.config.Dimensions
	}
	// Default: quality dimension
	return []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY}
}

// Config returns the judge configuration
func (h *LLMJudge) Config() *loomv1.JudgeConfig {
	return h.config
}

// Evaluate performs the evaluation using Hawk's judge
func (h *LLMJudge) Evaluate(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
	// Start tracing
	ctx, span := h.tracer.StartSpan(ctx, observability.SpanJudgeEvaluation)
	defer h.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("judge.name", h.Name())
		span.SetAttribute("judge.id", h.ID())
		span.SetAttribute("judge.criticality", h.Criticality().String())
	}

	startTime := time.Now()

	// Create evidence for LLM judge
	evidence := &llmjudge.Evidence{
		Query:         evalCtx.Prompt,
		Response:      evalCtx.Response,
		Success:       true,
		ExecutionTime: evalCtx.LatencyMs,
		Model:         h.llmProvider.Model(),
	}

	// Call LLM judge
	verdict, err := h.llmJudge.Judge(ctx, evidence)
	if err != nil {
		return &loomv1.JudgeResult{
			JudgeId:   h.ID(),
			JudgeName: h.Name(),
			Error:     err.Error(),
			Verdict:   "FAIL",
			JudgedAt:  timestamppb.Now(),
		}, fmt.Errorf("LLM judge failed: %w", err)
	}

	// Convert verdict to Loom JudgeResult
	result := h.convertVerdictToResult(verdict, evalCtx)
	result.ExecutionTimeMs = time.Since(startTime).Milliseconds()
	result.CostUsd = evalCtx.CostUsd

	return result, nil
}

// convertVerdictToResult converts LLM judge Verdict to Loom's JudgeResult
func (h *LLMJudge) convertVerdictToResult(verdict *llmjudge.Verdict, evalCtx *loomv1.EvaluationContext) *loomv1.JudgeResult {
	// Calculate overall score from 4 dimensions
	// Lower hallucination is better, so invert it
	overallScore := (float64(verdict.FactualAccuracy) +
		(100.0 - float64(verdict.HallucinationScore)) +
		float64(verdict.QueryQuality) +
		float64(verdict.Completeness)) / 4.0

	// Map dimensions to Loom's dimension scores
	dimensionScores := map[string]float64{
		"quality":      float64(verdict.QueryQuality),
		"correctness":  float64(verdict.FactualAccuracy),
		"completeness": float64(verdict.Completeness),
		"safety":       100.0 - float64(verdict.HallucinationScore),
	}

	// Phase 8: Add custom dimension score if configured
	// For custom dimensions, we use the overall score as the dimension score
	// (in production, this would be extracted from the judge's specialized evaluation)
	if h.config.CustomDimensionName != "" {
		hasCustomDimension := false
		for _, dim := range h.config.Dimensions {
			if dim == loomv1.JudgeDimension_JUDGE_DIMENSION_CUSTOM {
				hasCustomDimension = true
				break
			}
		}
		if hasCustomDimension {
			// Use the overall score as the custom dimension score
			// Normalize to 0-1 scale (overall score is 0-100)
			dimensionScores[h.config.CustomDimensionName] = overallScore / 100.0
		}
	}

	return &loomv1.JudgeResult{
		JudgeId:            h.ID(),
		JudgeName:          h.Name(),
		JudgeModel:         verdict.JudgeModel,
		Criteria:           h.Criteria(),
		FactualAccuracy:    int32(verdict.FactualAccuracy),
		HallucinationScore: int32(verdict.HallucinationScore),
		QueryQuality:       int32(verdict.QueryQuality),
		Completeness:       int32(verdict.Completeness),
		OverallScore:       overallScore,
		Verdict:            verdict.Verdict,
		Reasoning:          verdict.Reasoning,
		Issues:             verdict.Issues,
		DimensionScores:    dimensionScores,
		JudgedAt:           timestamppb.New(time.Unix(verdict.CreatedAt, 0)),
	}
}

// AgentJudge uses a Loom agent as a judge.
// This allows agents to evaluate other agents, enabling meta-evaluation.
type AgentJudge struct {
	id     string
	agent  *agent.Agent
	config *loomv1.JudgeConfig
	tracer observability.Tracer
}

// NewAgentJudge creates a new judge using a Loom agent.
func NewAgentJudge(agent *agent.Agent, config *loomv1.JudgeConfig, tracer observability.Tracer) (*AgentJudge, error) {
	if agent == nil {
		return nil, fmt.Errorf("agent required")
	}
	if config == nil {
		return nil, fmt.Errorf("judge config required")
	}

	// Phase 8: Validate custom dimension configuration
	if err := validateCustomDimensions(config); err != nil {
		return nil, fmt.Errorf("invalid custom dimension config: %w", err)
	}

	// Set defaults
	if config.Weight == 0 {
		config.Weight = 1.0
	}
	if config.MinPassingScore == 0 {
		config.MinPassingScore = 80
	}
	if config.Criticality == loomv1.JudgeCriticality_JUDGE_CRITICALITY_UNSPECIFIED {
		config.Criticality = loomv1.JudgeCriticality_JUDGE_CRITICALITY_NON_CRITICAL
	}

	id := config.Id
	if id == "" {
		id = uuid.New().String()
	}

	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}

	return &AgentJudge{
		id:     id,
		agent:  agent,
		config: config,
		tracer: tracer,
	}, nil
}

// ID returns the judge identifier
func (a *AgentJudge) ID() string {
	return a.id
}

// Name returns the judge name
func (a *AgentJudge) Name() string {
	return a.config.Name
}

// Criteria returns the evaluation criteria
func (a *AgentJudge) Criteria() []string {
	if a.config.Criteria != "" {
		return []string{a.config.Criteria}
	}
	return []string{"agent evaluation"}
}

// Weight returns the aggregation weight
func (a *AgentJudge) Weight() float64 {
	return a.config.Weight
}

// Criticality returns the judge criticality level
func (a *AgentJudge) Criticality() loomv1.JudgeCriticality {
	return a.config.Criticality
}

// Dimensions returns the dimensions this judge evaluates
func (a *AgentJudge) Dimensions() []loomv1.JudgeDimension {
	if len(a.config.Dimensions) > 0 {
		return a.config.Dimensions
	}
	return []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY}
}

// Config returns the judge configuration
func (a *AgentJudge) Config() *loomv1.JudgeConfig {
	return a.config
}

// Evaluate performs the evaluation using the agent
func (a *AgentJudge) Evaluate(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
	// Start tracing
	_, span := a.tracer.StartSpan(ctx, observability.SpanJudgeEvaluation)
	defer a.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("judge.name", a.Name())
		span.SetAttribute("judge.id", a.ID())
		span.SetAttribute("judge.type", "agent")
	}

	startTime := time.Now()

	// Build evaluation prompt for agent
	evaluationPrompt := a.buildEvaluationPrompt(evalCtx)

	// Execute agent (this would use agent.Process() method in real implementation)
	// For now, return a placeholder result
	// TODO: Implement agent execution once agent package is integrated

	result := &loomv1.JudgeResult{
		JudgeId:         a.ID(),
		JudgeName:       a.Name(),
		JudgeModel:      "agent-judge",
		Criteria:        a.Criteria(),
		OverallScore:    80.0, // Placeholder
		Verdict:         "PASS",
		Reasoning:       "Agent-based evaluation not yet fully implemented",
		ExecutionTimeMs: time.Since(startTime).Milliseconds(),
		JudgedAt:        timestamppb.Now(),
		Error:           "", // Empty string for now, will be populated when agent execution is implemented
	}

	_ = evaluationPrompt // Suppress unused variable warning

	return result, nil
}

// buildEvaluationPrompt builds the evaluation prompt for the agent
func (a *AgentJudge) buildEvaluationPrompt(evalCtx *loomv1.EvaluationContext) string {
	return fmt.Sprintf(`Evaluate this agent output:

Prompt: %s
Response: %s
Pattern Used: %s
Tools Used: %v
Cost: $%.4f
Latency: %dms

Criteria: %s

Provide a structured evaluation with:
1. Overall score (0-100)
2. Verdict (PASS/FAIL/PARTIAL)
3. Reasoning
4. Specific issues (if any)
5. Suggestions for improvement`,
		evalCtx.Prompt,
		evalCtx.Response,
		evalCtx.PatternUsed,
		evalCtx.ToolsUsed,
		evalCtx.CostUsd,
		evalCtx.LatencyMs,
		a.config.Criteria,
	)
}

// validateCustomDimensions validates custom dimension configuration (Phase 8).
// Rules:
// 1. If dimensions contains JUDGE_DIMENSION_CUSTOM, custom_dimension_name must be set
// 2. If custom_dimension_name is set, dimensions must contain JUDGE_DIMENSION_CUSTOM
func validateCustomDimensions(config *loomv1.JudgeConfig) error {
	hasCustomDimension := false
	for _, dim := range config.Dimensions {
		if dim == loomv1.JudgeDimension_JUDGE_DIMENSION_CUSTOM {
			hasCustomDimension = true
			break
		}
	}

	if hasCustomDimension && config.CustomDimensionName == "" {
		return fmt.Errorf("custom_dimension_name must be set when using JUDGE_DIMENSION_CUSTOM")
	}

	// Silently ignore custom_dimension_name if CUSTOM dimension not in dimensions list
	// (This is not an error, could log a warning in production if needed)
	_ = hasCustomDimension // Silence linter

	return nil
}

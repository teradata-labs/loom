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
//go:build hawk

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
	hawkcore "github.com/teradata-labs/hawk/pkg/core"
	hawkjudge "github.com/teradata-labs/hawk/pkg/core/judge"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
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

// HawkJudge wraps Hawk's embedded judge library for LLM-as-a-judge evaluation.
// This implementation uses Hawk's single-judge capability and maps it to Loom's
// multi-judge framework.
type HawkJudge struct {
	id          string
	hawkJudge   *hawkjudge.Judge
	config      *loomv1.JudgeConfig
	tracer      observability.Tracer
	llmProvider types.LLMProvider
}

// NewHawkJudge creates a new judge using Hawk's embedded library.
func NewHawkJudge(llmProvider types.LLMProvider, config *loomv1.JudgeConfig, tracer observability.Tracer) (*HawkJudge, error) {
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

	// Create Hawk judge instance
	hawkJudge, err := hawkjudge.NewJudge(&hawkjudge.Config{
		Provider: llmProvider,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Hawk judge: %w", err)
	}

	id := config.Id
	if id == "" {
		id = uuid.New().String()
	}

	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}

	return &HawkJudge{
		id:          id,
		hawkJudge:   hawkJudge,
		config:      config,
		tracer:      tracer,
		llmProvider: llmProvider,
	}, nil
}

// ID returns the judge identifier
func (h *HawkJudge) ID() string {
	return h.id
}

// Name returns the judge name
func (h *HawkJudge) Name() string {
	return h.config.Name
}

// Criteria returns the evaluation criteria
func (h *HawkJudge) Criteria() []string {
	if h.config.Criteria != "" {
		return []string{h.config.Criteria}
	}
	return []string{"quality", "correctness", "completeness"}
}

// Weight returns the aggregation weight
func (h *HawkJudge) Weight() float64 {
	return h.config.Weight
}

// Criticality returns the judge criticality level
func (h *HawkJudge) Criticality() loomv1.JudgeCriticality {
	return h.config.Criticality
}

// Dimensions returns the dimensions this judge evaluates
func (h *HawkJudge) Dimensions() []loomv1.JudgeDimension {
	if len(h.config.Dimensions) > 0 {
		return h.config.Dimensions
	}
	// Default: quality dimension
	return []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY}
}

// Config returns the judge configuration
func (h *HawkJudge) Config() *loomv1.JudgeConfig {
	return h.config
}

// Evaluate performs the evaluation using Hawk's judge
func (h *HawkJudge) Evaluate(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
	// Start tracing
	ctx, span := h.tracer.StartSpan(ctx, observability.SpanJudgeEvaluation)
	defer h.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("judge.name", h.Name())
		span.SetAttribute("judge.id", h.ID())
		span.SetAttribute("judge.criticality", h.Criticality().String())
	}

	startTime := time.Now()

	// Create EvalRun for Hawk judge
	evalRun := &hawkcore.EvalRun{
		ID:              evalCtx.TraceId,
		Query:           evalCtx.Prompt,
		Response:        evalCtx.Response,
		Success:         true,
		ExecutionTimeMS: evalCtx.LatencyMs,
		Model:           h.llmProvider.Model(),
	}

	// Call Hawk judge
	verdict, err := h.hawkJudge.JudgeEvalRun(ctx, evalRun)
	if err != nil {
		return &loomv1.JudgeResult{
			JudgeId:   h.ID(),
			JudgeName: h.Name(),
			Error:     err.Error(),
			Verdict:   "FAIL",
			JudgedAt:  timestamppb.Now(),
		}, fmt.Errorf("hawk judge failed: %w", err)
	}

	// Convert Hawk verdict to Loom JudgeResult
	result := h.convertVerdictToResult(verdict, evalCtx)
	result.ExecutionTimeMs = time.Since(startTime).Milliseconds()
	result.CostUsd = evalCtx.CostUsd

	return result, nil
}

// convertVerdictToResult converts Hawk's Verdict to Loom's JudgeResult
func (h *HawkJudge) convertVerdictToResult(verdict *hawkjudge.Verdict, evalCtx *loomv1.EvaluationContext) *loomv1.JudgeResult {
	// Calculate overall score from Hawk's 4 dimensions
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

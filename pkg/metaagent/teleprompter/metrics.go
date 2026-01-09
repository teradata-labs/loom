// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package teleprompter

import (
	"context"
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

// JudgeOrchestrator defines the interface for judge orchestration.
// This interface decouples the metric from the concrete orchestrator implementation,
// making it easier to test with mocks.
type JudgeOrchestrator interface {
	Evaluate(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error)
}

// MultiJudgeMetric uses Loom's multi-judge infrastructure for evaluation.
// This metric runs multiple LLM-as-a-judge evaluations in parallel and
// aggregates the results based on dimension weights and aggregation strategy.
//
// Key features:
// - Multi-dimensional evaluation (quality, cost, safety, domain, performance, usability)
// - Parallel judge execution (sync, async, hybrid modes)
// - 6 aggregation strategies (weighted average, all-must-pass, majority, etc.)
// - Hawk integration for observability
// - Dimension filtering and weighting
//
// Example usage:
//
//	metric := NewMultiJudgeMetric(&MultiJudgeMetricConfig{
//	    Orchestrator: judgeOrchestrator,
//	    JudgeIDs:     []string{"quality-judge", "safety-judge"},
//	    Aggregation:  loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
//	    DimensionWeights: map[string]float64{
//	        "quality": 2.0,
//	        "safety":  3.0,
//	    },
//	    MinThreshold: 80.0,
//	})
//
//	score, err := metric.Evaluate(ctx, example, result)
type MultiJudgeMetric struct {
	orchestrator     JudgeOrchestrator
	judgeIDs         []string
	aggregation      loomv1.AggregationStrategy
	targetDimensions []loomv1.JudgeDimension
	dimensionWeights map[string]float64
	minThreshold     float64
	exportToHawk     bool
	tracer           observability.Tracer
	logger           *zap.Logger

	// lastResponse stores the most recent evaluation response for dimension score extraction
	lastResponse *loomv1.EvaluateResponse
}

// MultiJudgeMetricConfig configures the multi-judge metric
type MultiJudgeMetricConfig struct {
	Orchestrator     JudgeOrchestrator
	JudgeIDs         []string
	Aggregation      loomv1.AggregationStrategy
	TargetDimensions []loomv1.JudgeDimension
	DimensionWeights map[string]float64
	MinThreshold     float64
	ExportToHawk     bool
	Tracer           observability.Tracer
	Logger           *zap.Logger
}

// NewMultiJudgeMetric creates a new multi-judge metric
func NewMultiJudgeMetric(config *MultiJudgeMetricConfig) (*MultiJudgeMetric, error) {
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

	minThreshold := config.MinThreshold
	if minThreshold == 0 {
		minThreshold = 80.0 // Default: 80% passing threshold
	}

	tracer := config.Tracer
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}

	logger := config.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	return &MultiJudgeMetric{
		orchestrator:     config.Orchestrator,
		judgeIDs:         config.JudgeIDs,
		aggregation:      aggregation,
		targetDimensions: config.TargetDimensions,
		dimensionWeights: config.DimensionWeights,
		minThreshold:     minThreshold,
		exportToHawk:     config.ExportToHawk,
		tracer:           tracer,
		logger:           logger,
	}, nil
}

// NewMultiJudgeMetricFromProto creates a metric from proto config
func NewMultiJudgeMetricFromProto(
	orchestrator JudgeOrchestrator,
	config *loomv1.MultiJudgeMetricConfig,
	tracer observability.Tracer,
	logger *zap.Logger,
) (*MultiJudgeMetric, error) {
	if config == nil {
		return nil, fmt.Errorf("config required")
	}

	// Convert proto dimension weights from map<string, double> to map[string]float64
	dimensionWeights := make(map[string]float64)
	for dim, weight := range config.DimensionWeights {
		dimensionWeights[dim] = weight
	}

	return NewMultiJudgeMetric(&MultiJudgeMetricConfig{
		Orchestrator:     orchestrator,
		JudgeIDs:         config.JudgeIds,
		Aggregation:      config.Aggregation,
		TargetDimensions: config.TargetDimensions,
		DimensionWeights: dimensionWeights,
		MinThreshold:     config.MinThreshold,
		ExportToHawk:     config.ExportToHawk,
		Tracer:           tracer,
		Logger:           logger,
	})
}

// Evaluate implements the Metric interface.
// It runs multi-judge evaluation on the agent's output and returns a score [0, 1].
//
// The evaluation process:
// 1. Construct evaluation context from example and result
// 2. Run judges in parallel via orchestrator
// 3. Aggregate verdicts based on strategy
// 4. Apply dimension weights to calculate final score
// 5. Optionally export to Hawk for observability
func (m *MultiJudgeMetric) Evaluate(
	ctx context.Context,
	example *loomv1.Example,
	result *ExecutionResult,
) (float64, error) {
	// Start tracing
	ctx, span := m.tracer.StartSpan(ctx, observability.SpanTeleprompterMetric)
	defer m.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("metric.type", "multi_judge")
		span.SetAttribute("judge.count", len(m.judgeIDs))
		span.SetAttribute("judge.aggregation", m.aggregation.String())
	}

	m.logger.Debug("Evaluating with multi-judge metric",
		zap.Strings("judge_ids", m.judgeIDs),
		zap.String("aggregation", m.aggregation.String()),
		zap.Float64("min_threshold", m.minThreshold),
	)

	// Construct evaluation context
	evalCtx := &loomv1.EvaluationContext{
		Prompt:   m.extractQuery(example),
		Response: m.extractResponse(result),
		Metadata: m.buildMetadata(example, result),
	}

	// Create evaluation request
	evalReq := &loomv1.EvaluateRequest{
		JudgeIds: m.judgeIDs,
		Context:  evalCtx,
		// Use HYBRID mode for teleprompters:
		// - Critical judges (safety, quality) run sync
		// - Non-critical judges (cost, usability) run async
		ExecutionMode:  loomv1.ExecutionMode_EXECUTION_MODE_HYBRID,
		Aggregation:    m.aggregation,
		ExportToHawk:   m.exportToHawk,
		TimeoutSeconds: 30,    // 30 second timeout per judge
		FailFast:       false, // Collect all verdicts for learning
	}

	// Run judges via orchestrator
	evalResp, err := m.orchestrator.Evaluate(ctx, evalReq)
	if err != nil {
		m.logger.Error("Judge evaluation failed",
			zap.Error(err),
			zap.Strings("judge_ids", m.judgeIDs),
		)
		return 0.0, fmt.Errorf("judge evaluation failed: %w", err)
	}

	// Store response for dimension score extraction
	m.lastResponse = evalResp

	// Calculate weighted score from dimension scores
	finalScore := m.calculateWeightedScore(evalResp)

	m.logger.Debug("Multi-judge evaluation complete",
		zap.Float64("final_score", finalScore),
		zap.Float64("raw_score", evalResp.FinalScore),
		zap.Bool("passed", evalResp.Passed),
		zap.Int("verdicts_count", len(evalResp.Verdicts)),
	)

	if span != nil {
		span.SetAttribute("evaluation.final_score", finalScore)
		span.SetAttribute("evaluation.passed", evalResp.Passed)
		span.SetAttribute("evaluation.verdicts_count", len(evalResp.Verdicts))
	}

	// Return normalized score [0, 1]
	return finalScore / 100.0, nil
}

// Type returns the metric type
func (m *MultiJudgeMetric) Type() loomv1.MetricType {
	return loomv1.MetricType_METRIC_MULTI_JUDGE
}

// Name returns a human-readable name
func (m *MultiJudgeMetric) Name() string {
	return fmt.Sprintf("MultiJudge(%d judges, %s)", len(m.judgeIDs), m.aggregation.String())
}

// GetLastDimensionScores returns dimension scores from the most recent evaluation.
// Returns an aggregated map of dimension scores across all judges.
// Returns nil if no evaluation has been performed yet.
//
// Example:
//
//	score, err := metric.Evaluate(ctx, example, result)
//	dimensionScores := metric.GetLastDimensionScores()
//	// dimensionScores = {"quality": 85.0, "safety": 90.0, "cost": 75.0}
func (m *MultiJudgeMetric) GetLastDimensionScores() map[string]float64 {
	if m.lastResponse == nil || len(m.lastResponse.Verdicts) == 0 {
		return nil
	}

	// Aggregate dimension scores across all verdicts
	dimensionScores := make(map[string]float64)
	dimensionCounts := make(map[string]int)

	for _, verdict := range m.lastResponse.Verdicts {
		for dim, score := range verdict.DimensionScores {
			dimensionScores[dim] += score
			dimensionCounts[dim]++
		}
	}

	// Calculate averages
	for dim, sum := range dimensionScores {
		count := dimensionCounts[dim]
		if count > 0 {
			dimensionScores[dim] = sum / float64(count)
		}
	}

	return dimensionScores
}

// GetLastVerdicts returns the full judge verdicts from the most recent evaluation.
// Returns nil if no evaluation has been performed yet.
//
// This is useful for detailed analysis of judge reasoning and per-dimension feedback.
func (m *MultiJudgeMetric) GetLastVerdicts() []*loomv1.JudgeResult {
	if m.lastResponse == nil {
		return nil
	}
	return m.lastResponse.Verdicts
}

// calculateWeightedScore computes the final score from judge verdicts
// using dimension weights. If no dimension weights are specified,
// it falls back to the aggregated score from the orchestrator.
func (m *MultiJudgeMetric) calculateWeightedScore(resp *loomv1.EvaluateResponse) float64 {
	// If no dimension weights specified, use aggregated score
	if len(m.dimensionWeights) == 0 {
		if resp.Aggregated != nil {
			return resp.Aggregated.WeightedAverageScore
		}
		return resp.FinalScore
	}

	// Calculate weighted average across dimensions
	totalWeight := 0.0
	weightedSum := 0.0

	for _, verdict := range resp.Verdicts {
		for dimName, weight := range m.dimensionWeights {
			if dimScore, ok := verdict.DimensionScores[dimName]; ok {
				weightedSum += dimScore * weight
				totalWeight += weight
			}
		}
	}

	if totalWeight == 0 {
		// No dimension scores found, fall back to overall scores
		for _, verdict := range resp.Verdicts {
			weightedSum += verdict.OverallScore
			totalWeight += 1.0
		}
	}

	if totalWeight == 0 {
		return 0.0
	}

	return weightedSum / totalWeight
}

// extractQuery gets the query/question from the example inputs
func (m *MultiJudgeMetric) extractQuery(example *loomv1.Example) string {
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
func (m *MultiJudgeMetric) extractResponse(result *ExecutionResult) string {
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
func (m *MultiJudgeMetric) buildMetadata(example *loomv1.Example, result *ExecutionResult) map[string]string {
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

	// Add rationale if available (for chain-of-thought evaluation)
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

// ExactMatchMetric evaluates output using exact string match.
// Returns 1.0 if output exactly matches expected, 0.0 otherwise.
type ExactMatchMetric struct{}

// NewExactMatchMetric creates a new exact match metric
func NewExactMatchMetric() *ExactMatchMetric {
	return &ExactMatchMetric{}
}

// Evaluate implements the Metric interface
func (e *ExactMatchMetric) Evaluate(
	ctx context.Context,
	example *loomv1.Example,
	result *ExecutionResult,
) (float64, error) {
	// Get expected output
	expectedOutput := ""
	for _, field := range []string{"answer", "expected", "output"} {
		if val, ok := example.Outputs[field]; ok {
			expectedOutput = val
			break
		}
	}

	// Get actual output
	actualOutput := ""
	for _, field := range []string{"answer", "response", "output"} {
		if val, ok := result.Outputs[field]; ok {
			actualOutput = val
			break
		}
	}

	// Exact string comparison
	if expectedOutput == actualOutput {
		return 1.0, nil
	}
	return 0.0, nil
}

// Type returns the metric type
func (e *ExactMatchMetric) Type() loomv1.MetricType {
	return loomv1.MetricType_METRIC_EXACT_MATCH
}

// Name returns a human-readable name
func (e *ExactMatchMetric) Name() string {
	return "ExactMatch"
}

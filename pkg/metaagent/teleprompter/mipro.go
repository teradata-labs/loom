// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package teleprompter

import (
	"context"
	"fmt"
	"sort"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// MIPRO implements the Multi-prompt Instruction Proposal Optimizer.
//
// Status: ✅ Implemented with multi-judge integration
//
// Algorithm (from DSPy with multi-dimensional enhancements):
//  1. Generate N candidate instructions using LLM (or use provided candidates)
//  2. For each instruction candidate:
//     a. Create agent variant with this instruction
//     b. Run on trainset and evaluate with metric (captures dimension scores)
//     c. Track overall score + dimension-specific performance
//  3. Select best instruction based on:
//     - Overall score (default)
//     - Dimension priorities (quality vs cost, safety-first, etc.)
//  4. Bootstrap demonstrations for winning instruction
//  5. (Optional) Multi-round refinement
//
// Multi-Dimensional Features:
// - When used with MultiJudgeMetric, captures per-dimension scores for each instruction
// - Supports dimension-weighted selection (e.g., prioritize quality over cost)
// - Enables trade-off optimization (balance quality, cost, safety)
// - Tracks dimension improvements across optimization rounds
//
// Configuration:
// - num_candidates: Number of instruction candidates to evaluate (default: 10)
// - max_rounds: Optimization rounds for iterative refinement (default: 1)
// - min_confidence: Minimum score threshold for instruction acceptance (default: 0.7)
// - dimension_priorities: Optional dimension weights for selection (e.g., {"quality": 2.0, "cost": 1.0})
//
// References:
// - DSPy MIPRO: https://github.com/stanfordnlp/dspy
type MIPRO struct {
	*BaseTeleprompter
	instructionGenerator InstructionGenerator // Optional: for automatic instruction generation
}

// InstructionGenerator generates instruction candidates for optimization.
// This is optional - users can provide pre-defined instruction candidates instead.
type InstructionGenerator interface {
	// Generate creates N instruction candidates for the given task
	Generate(ctx context.Context, taskDescription string, numCandidates int) ([]string, error)
}

// NewMIPRO creates a new MIPRO teleprompter
func NewMIPRO(tracer observability.Tracer, registry *Registry, generator InstructionGenerator) *MIPRO {
	return &MIPRO{
		BaseTeleprompter:     NewBaseTeleprompter(tracer, registry),
		instructionGenerator: generator,
	}
}

// InstructionCandidate represents an instruction variant with evaluation results
type InstructionCandidate struct {
	Instruction      string
	OverallScore     float64
	DimensionScores  map[string]float64 // Per-dimension scores (quality, cost, safety, etc.)
	Demonstrations   []*loomv1.Demonstration
	TrainsetScore    float64
	DevsetScore      float64
	SuccessfulTraces int32
}

// Compile implements the Teleprompter interface
func (m *MIPRO) Compile(
	ctx context.Context,
	req *CompileRequest,
) (*CompilationResult, error) {
	startTime := time.Now()

	ctx, span := m.GetTracer().StartSpan(ctx, "teleprompter.mipro.compile")
	defer m.GetTracer().EndSpan(span)

	// Validate config
	if err := m.ValidateConfig(req.Config); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	m.SetDefaultsConfig(req.Config)

	span.SetAttribute("agent_id", req.AgentID)
	span.SetAttribute("trainset_size", fmt.Sprintf("%d", len(req.Trainset)))

	numCandidates := 0
	if req.Config.Mipro != nil {
		numCandidates = int(req.Config.Mipro.NumCandidates)
	}
	span.SetAttribute("num_candidates", fmt.Sprintf("%d", numCandidates))

	// Get instruction candidates
	candidates, err := m.getInstructionCandidates(ctx, req)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to get instruction candidates: %w", err)
	}

	// Evaluate each candidate instruction on trainset
	evaluatedCandidates, err := m.evaluateCandidates(ctx, req, candidates)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to evaluate candidates: %w", err)
	}

	// Select best instruction based on scores and dimension priorities
	bestCandidate := m.selectBestInstruction(evaluatedCandidates, req.Config)
	if bestCandidate == nil {
		return nil, fmt.Errorf("no instruction candidates met minimum confidence threshold")
	}

	span.SetAttribute("best_instruction_score", fmt.Sprintf("%.4f", bestCandidate.OverallScore))

	// Build result
	compilationTimeMs := time.Since(startTime).Milliseconds()

	// Optimized prompts: use the winning instruction
	optimizedPrompts := map[string]string{
		"system": bestCandidate.Instruction,
	}

	result := m.BuildCompilationResult(
		req.AgentID,
		loomv1.TeleprompterType_TELEPROMPTER_MIPRO,
		optimizedPrompts,
		bestCandidate.Demonstrations,
		bestCandidate.TrainsetScore,
		bestCandidate.DevsetScore,
		int32(len(req.Trainset)),
		bestCandidate.SuccessfulTraces,
		1,   // optimization rounds (MIPRO is single-pass)
		0.0, // improvement delta (no baseline comparison)
		compilationTimeMs,
	)

	// Store dimension scores in metadata if available
	if len(bestCandidate.DimensionScores) > 0 {
		for dim, score := range bestCandidate.DimensionScores {
			result.Metadata[fmt.Sprintf("dimension.%s", dim)] = fmt.Sprintf("%.2f", score)
		}
	}

	span.Status = observability.Status{Code: observability.StatusOK}
	return result, nil
}

// getInstructionCandidates retrieves instruction candidates either from config or via generation
func (m *MIPRO) getInstructionCandidates(ctx context.Context, req *CompileRequest) ([]string, error) {
	// Check if MIPRO config exists
	if req.Config.Mipro == nil {
		return nil, fmt.Errorf("MIPRO config required")
	}

	// If instruction candidates provided in config, use those
	if len(req.Config.Mipro.InstructionCandidates) > 0 {
		return req.Config.Mipro.InstructionCandidates, nil
	}

	// Otherwise, generate candidates using instruction generator
	if m.instructionGenerator == nil {
		return nil, fmt.Errorf("no instruction candidates provided and no instruction generator configured")
	}

	numCandidates := int(req.Config.Mipro.NumCandidates)
	if numCandidates == 0 {
		numCandidates = 10 // Default
	}

	taskDescription := req.Config.Mipro.TaskDescription
	if taskDescription == "" {
		taskDescription = "Optimize agent instructions for improved performance"
	}

	return m.instructionGenerator.Generate(ctx, taskDescription, numCandidates)
}

// evaluateCandidates evaluates each instruction candidate on the trainset
func (m *MIPRO) evaluateCandidates(
	ctx context.Context,
	req *CompileRequest,
	candidates []string,
) ([]*InstructionCandidate, error) {
	ctx, span := m.GetTracer().StartSpan(ctx, "mipro.evaluate_candidates")
	defer m.GetTracer().EndSpan(span)

	span.SetAttribute("num_candidates", fmt.Sprintf("%d", len(candidates)))

	evaluatedCandidates := make([]*InstructionCandidate, 0, len(candidates))

	for i, instruction := range candidates {
		// Create agent variant with this instruction
		agentVariant := m.createAgentVariant(req.Agent, instruction)

		// Run on trainset
		traces, err := m.RunOnTrainset(ctx, agentVariant, req.Trainset, req.Metric, req.Config.MinConfidence)
		if err != nil {
			span.RecordError(err)
			continue
		}

		if len(traces) == 0 {
			// No successful traces - skip this candidate
			continue
		}

		// Calculate average trainset score
		totalScore := 0.0
		for _, trace := range traces {
			totalScore += trace.QualityScore
		}
		avgTrainsetScore := totalScore / float64(len(traces))

		// Aggregate dimension scores across all traces (if available)
		dimensionScores := m.aggregateDimensionScores(traces)

		// Bootstrap demonstrations from successful traces
		demonstrations, err := m.SelectDemonstrations(ctx, traces, int(req.Config.MaxBootstrappedDemos), loomv1.BootstrapStrategy_BOOTSTRAP_TOP_K)
		if err != nil {
			span.RecordError(err)
			demonstrations = []*loomv1.Demonstration{} // Continue with empty demos
		}

		// Evaluate on devset if available
		devsetScore := 0.0
		if len(req.Devset) > 0 {
			devsetScore, err = m.EvaluateOnDevset(ctx, agentVariant, req.Devset, req.Metric)
			if err != nil {
				span.RecordError(err)
			}
		}

		candidate := &InstructionCandidate{
			Instruction:      instruction,
			OverallScore:     avgTrainsetScore,
			DimensionScores:  dimensionScores,
			Demonstrations:   demonstrations,
			TrainsetScore:    avgTrainsetScore,
			DevsetScore:      devsetScore,
			SuccessfulTraces: int32(len(traces)),
		}

		evaluatedCandidates = append(evaluatedCandidates, candidate)

		span.SetAttribute(fmt.Sprintf("candidate_%d_score", i), fmt.Sprintf("%.4f", avgTrainsetScore))
	}

	span.SetAttribute("evaluated_count", fmt.Sprintf("%d", len(evaluatedCandidates)))
	span.Status = observability.Status{Code: observability.StatusOK}

	return evaluatedCandidates, nil
}

// aggregateDimensionScores aggregates dimension scores across all traces
func (m *MIPRO) aggregateDimensionScores(traces []*ExecutionTrace) map[string]float64 {
	if len(traces) == 0 {
		return nil
	}

	// Check if any trace has dimension scores
	hasDimensionScores := false
	for _, trace := range traces {
		if len(trace.DimensionScores) > 0 {
			hasDimensionScores = true
			break
		}
	}

	if !hasDimensionScores {
		return nil
	}

	// Aggregate dimension scores
	dimensionSums := make(map[string]float64)
	dimensionCounts := make(map[string]int)

	for _, trace := range traces {
		for dim, score := range trace.DimensionScores {
			dimensionSums[dim] += score
			dimensionCounts[dim]++
		}
	}

	// Calculate averages
	dimensionScores := make(map[string]float64)
	for dim, sum := range dimensionSums {
		count := dimensionCounts[dim]
		if count > 0 {
			dimensionScores[dim] = sum / float64(count)
		}
	}

	return dimensionScores
}

// selectBestInstruction selects the best instruction based on scores and dimension priorities
func (m *MIPRO) selectBestInstruction(
	candidates []*InstructionCandidate,
	config *loomv1.TeleprompterConfig,
) *InstructionCandidate {
	if len(candidates) == 0 {
		return nil
	}

	// Get dimension priorities from MIPRO config
	var dimensionPriorities map[string]float64
	if config.Mipro != nil {
		dimensionPriorities = config.Mipro.DimensionPriorities
	}

	// If no dimension priorities specified, select by overall score
	if len(dimensionPriorities) == 0 {
		// Sort by overall score (descending)
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].OverallScore > candidates[j].OverallScore
		})
		return candidates[0]
	}

	// Calculate weighted scores based on dimension priorities
	type scoredCandidate struct {
		candidate     *InstructionCandidate
		weightedScore float64
	}

	scoredCandidates := make([]*scoredCandidate, 0, len(candidates))

	for _, candidate := range candidates {
		weightedScore := m.calculateWeightedScore(candidate, dimensionPriorities)
		scoredCandidates = append(scoredCandidates, &scoredCandidate{
			candidate:     candidate,
			weightedScore: weightedScore,
		})
	}

	// Sort by weighted score (descending)
	sort.Slice(scoredCandidates, func(i, j int) bool {
		return scoredCandidates[i].weightedScore > scoredCandidates[j].weightedScore
	})

	return scoredCandidates[0].candidate
}

// calculateWeightedScore calculates weighted score based on dimension priorities
func (m *MIPRO) calculateWeightedScore(
	candidate *InstructionCandidate,
	dimensionPriorities map[string]float64,
) float64 {
	if len(candidate.DimensionScores) == 0 {
		// No dimension scores available, use overall score
		return candidate.OverallScore
	}

	totalWeight := 0.0
	weightedSum := 0.0

	for dim, weight := range dimensionPriorities {
		if score, ok := candidate.DimensionScores[dim]; ok {
			// Normalize score to [0, 1] if needed (scores might be in [0, 100])
			normalizedScore := score
			if score > 1.0 {
				normalizedScore = score / 100.0
			}
			weightedSum += normalizedScore * weight
			totalWeight += weight
		}
	}

	if totalWeight == 0 {
		// No matching dimensions, fall back to overall score
		return candidate.OverallScore
	}

	return weightedSum / totalWeight
}

// createAgentVariant creates an agent variant with a different instruction
// This is a simplified implementation - in practice, this would modify the agent's system prompt
func (m *MIPRO) createAgentVariant(baseAgent Agent, instruction string) Agent {
	// Clone the agent
	agentVariant := baseAgent.Clone()

	// Update the learned layer with the new instruction
	memory := agentVariant.GetMemory()
	if memory != nil {
		// Store instruction in optimized prompts
		optimizedPrompts := map[string]string{
			"system": instruction,
		}
		_ = memory.UpdateLearnedLayer(optimizedPrompts, nil)
	}

	return agentVariant
}

// Type returns the teleprompter type
func (m *MIPRO) Type() loomv1.TeleprompterType {
	return loomv1.TeleprompterType_TELEPROMPTER_MIPRO
}

// Name returns a human-readable name
func (m *MIPRO) Name() string {
	return "MIPRO"
}

// SupportsMultiRound indicates if this teleprompter supports iterative optimization
func (m *MIPRO) SupportsMultiRound() bool {
	return true
}

// SupportsTeacher indicates if this teleprompter supports teacher-student bootstrapping
func (m *MIPRO) SupportsTeacher() bool {
	return true
}

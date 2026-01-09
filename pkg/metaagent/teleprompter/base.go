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
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// BaseTeleprompter provides shared functionality for all teleprompters.
// Embed this in concrete teleprompter implementations to get common utilities.
type BaseTeleprompter struct {
	tracer   observability.Tracer
	registry *Registry
}

// NewBaseTeleprompter creates a new base teleprompter
func NewBaseTeleprompter(tracer observability.Tracer, registry *Registry) *BaseTeleprompter {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	if registry == nil {
		registry = NewRegistry()
	}
	return &BaseTeleprompter{
		tracer:   tracer,
		registry: registry,
	}
}

// RunOnTrainset executes the agent on all training examples.
// Returns successful execution traces that can be used for bootstrapping.
func (bt *BaseTeleprompter) RunOnTrainset(
	ctx context.Context,
	agent Agent,
	trainset []*loomv1.Example,
	metric Metric,
	minConfidence float64,
) ([]*ExecutionTrace, error) {
	ctx, span := bt.tracer.StartSpan(ctx, "teleprompter.run_on_trainset")
	defer bt.tracer.EndSpan(span)

	span.SetAttribute("trainset_size", fmt.Sprintf("%d", len(trainset)))
	span.SetAttribute("min_confidence", fmt.Sprintf("%.2f", minConfidence))

	traces := make([]*ExecutionTrace, 0, len(trainset))
	successCount := 0

	for i, example := range trainset {
		// Run agent on example
		result, err := agent.Run(ctx, example.Inputs)
		if err != nil {
			// Log error but continue with other examples
			span.RecordError(err)
			continue
		}

		// Evaluate with metric
		score, err := metric.Evaluate(ctx, example, result)
		if err != nil {
			span.RecordError(err)
			continue
		}

		// Only keep traces above confidence threshold
		if score >= minConfidence {
			trace := &ExecutionTrace{
				TraceID:      result.TraceID,
				Example:      example,
				Result:       result,
				QualityScore: score,
				Timestamp:    time.Now().Unix(),
				Metadata: map[string]string{
					"index":    fmt.Sprintf("%d", i),
					"metric":   metric.Name(),
					"agent_id": agent.GetID(),
				},
			}

			// If using MultiJudgeMetric, populate dimension scores and verdicts
			if multiJudgeMetric, ok := metric.(*MultiJudgeMetric); ok {
				trace.DimensionScores = multiJudgeMetric.GetLastDimensionScores()
				trace.JudgeVerdicts = multiJudgeMetric.GetLastVerdicts()
			}

			traces = append(traces, trace)
			successCount++
		}
	}

	span.SetAttribute("successful_traces", fmt.Sprintf("%d", successCount))
	span.SetAttribute("success_rate", fmt.Sprintf("%.2f", float64(successCount)/float64(len(trainset))))
	span.Status = observability.Status{Code: observability.StatusOK}

	return traces, nil
}

// EvaluateOnDevset evaluates the compiled agent on a validation set.
// Returns the average score across all examples.
func (bt *BaseTeleprompter) EvaluateOnDevset(
	ctx context.Context,
	agent Agent,
	devset []*loomv1.Example,
	metric Metric,
) (float64, error) {
	ctx, span := bt.tracer.StartSpan(ctx, "teleprompter.evaluate_on_devset")
	defer bt.tracer.EndSpan(span)

	if len(devset) == 0 {
		return 0.0, nil
	}

	span.SetAttribute("devset_size", fmt.Sprintf("%d", len(devset)))

	totalScore := 0.0
	validCount := 0

	for _, example := range devset {
		result, err := agent.Run(ctx, example.Inputs)
		if err != nil {
			span.RecordError(err)
			continue
		}

		score, err := metric.Evaluate(ctx, example, result)
		if err != nil {
			span.RecordError(err)
			continue
		}

		totalScore += score
		validCount++
	}

	if validCount == 0 {
		return 0.0, fmt.Errorf("no valid devset examples evaluated")
	}

	avgScore := totalScore / float64(validCount)
	span.SetAttribute("avg_score", fmt.Sprintf("%.4f", avgScore))
	span.SetAttribute("valid_count", fmt.Sprintf("%d", validCount))
	span.Status = observability.Status{Code: observability.StatusOK}

	return avgScore, nil
}

// SelectDemonstrations chooses the best demonstrations from traces using the configured strategy.
// Falls back to TopK strategy if no selector is configured.
func (bt *BaseTeleprompter) SelectDemonstrations(
	ctx context.Context,
	traces []*ExecutionTrace,
	maxDemos int,
	strategy loomv1.BootstrapStrategy,
) ([]*loomv1.Demonstration, error) {
	ctx, span := bt.tracer.StartSpan(ctx, "teleprompter.select_demonstrations")
	defer bt.tracer.EndSpan(span)

	span.SetAttribute("traces_count", fmt.Sprintf("%d", len(traces)))
	span.SetAttribute("max_demos", fmt.Sprintf("%d", maxDemos))
	span.SetAttribute("strategy", strategy.String())

	// Get selector from registry
	selector, ok := bt.registry.GetSelector(strategy)
	if !ok {
		// Fall back to TopK strategy
		selector = NewTopKSelector()
	}

	demonstrations, err := selector.Select(ctx, traces, maxDemos)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	span.SetAttribute("selected_count", fmt.Sprintf("%d", len(demonstrations)))
	span.Status = observability.Status{Code: observability.StatusOK}

	return demonstrations, nil
}

// ApplyLearnedLayer updates the agent's memory with optimized content
func (bt *BaseTeleprompter) ApplyLearnedLayer(
	ctx context.Context,
	agent Agent,
	optimizedPrompts map[string]string,
	demonstrations []*loomv1.Demonstration,
) error {
	_, span := bt.tracer.StartSpan(ctx, "teleprompter.apply_learned_layer")
	defer bt.tracer.EndSpan(span)

	span.SetAttribute("prompts_count", fmt.Sprintf("%d", len(optimizedPrompts)))
	span.SetAttribute("demos_count", fmt.Sprintf("%d", len(demonstrations)))

	memory := agent.GetMemory()
	if memory == nil {
		return fmt.Errorf("agent has no memory interface")
	}

	if err := memory.UpdateLearnedLayer(optimizedPrompts, demonstrations); err != nil {
		span.RecordError(err)
		span.Status = observability.Status{Code: observability.StatusError, Message: err.Error()}
		return fmt.Errorf("failed to update learned layer: %w", err)
	}

	span.Status = observability.Status{Code: observability.StatusOK}
	return nil
}

// ComputeImprovement calculates the improvement delta between baseline and optimized scores
func (bt *BaseTeleprompter) ComputeImprovement(
	baselineScore float64,
	optimizedScore float64,
) float64 {
	return optimizedScore - baselineScore
}

// GenerateCompilationID creates a unique identifier for this compilation
func (bt *BaseTeleprompter) GenerateCompilationID() string {
	return uuid.New().String()
}

// GenerateCompiledVersion creates a version hash from learned content
func (bt *BaseTeleprompter) GenerateCompiledVersion(
	optimizedPrompts map[string]string,
	demonstrations []*loomv1.Demonstration,
) string {
	hasher := sha256.New()

	// Hash prompts
	for key, value := range optimizedPrompts {
		hasher.Write([]byte(key))
		hasher.Write([]byte(value))
	}

	// Hash demonstrations
	for _, demo := range demonstrations {
		hasher.Write([]byte(demo.PatternName))
		hasher.Write([]byte(demo.Input))
		hasher.Write([]byte(demo.Output))
		hasher.Write([]byte(demo.Rationale))
	}

	return fmt.Sprintf("%x", hasher.Sum(nil)[:8]) // First 8 bytes as hex
}

// BuildCompilationResult creates a CompilationResult from compilation data
func (bt *BaseTeleprompter) BuildCompilationResult(
	agentID string,
	teleprompterType loomv1.TeleprompterType,
	optimizedPrompts map[string]string,
	demonstrations []*loomv1.Demonstration,
	trainsetScore float64,
	devsetScore float64,
	examplesUsed int32,
	successfulTraces int32,
	optimizationRounds int32,
	improvementDelta float64,
	compilationTimeMs int64,
) *CompilationResult {
	return &CompilationResult{
		CompilationID:      bt.GenerateCompilationID(),
		AgentID:            agentID,
		Teleprompter:       teleprompterType,
		OptimizedPrompts:   optimizedPrompts,
		Demonstrations:     demonstrations,
		TrainsetScore:      trainsetScore,
		DevsetScore:        devsetScore,
		ExamplesUsed:       examplesUsed,
		SuccessfulTraces:   successfulTraces,
		OptimizationRounds: optimizationRounds,
		ImprovementDelta:   improvementDelta,
		CompilationTimeMs:  compilationTimeMs,
		CompiledVersion:    bt.GenerateCompiledVersion(optimizedPrompts, demonstrations),
		CompiledAt:         time.Now().Unix(),
		Metadata:           make(map[string]string),
	}
}

// ValidateConfig validates teleprompter configuration
func (bt *BaseTeleprompter) ValidateConfig(config *loomv1.TeleprompterConfig) error {
	if config == nil {
		return fmt.Errorf("config is required")
	}

	if config.MaxBootstrappedDemos < 0 {
		return fmt.Errorf("max_bootstrapped_demos must be non-negative")
	}

	if config.MinConfidence < 0 || config.MinConfidence > 1 {
		return fmt.Errorf("min_confidence must be in [0, 1]")
	}

	if config.MaxRounds < 1 {
		return fmt.Errorf("max_rounds must be at least 1")
	}

	return nil
}

// SetDefaultsConfig applies default values to config
func (bt *BaseTeleprompter) SetDefaultsConfig(config *loomv1.TeleprompterConfig) {
	if config.MaxBootstrappedDemos == 0 {
		config.MaxBootstrappedDemos = 5
	}
	if config.MaxLabeledDemos == 0 {
		config.MaxLabeledDemos = 8
	}
	if config.MinConfidence == 0 {
		config.MinConfidence = 0.7
	}
	if config.MaxRounds == 0 {
		config.MaxRounds = 1
	}
}

// GetTracer returns the tracer for subclass use
func (bt *BaseTeleprompter) GetTracer() observability.Tracer {
	return bt.tracer
}

// GetRegistry returns the registry for subclass use
func (bt *BaseTeleprompter) GetRegistry() *Registry {
	return bt.registry
}

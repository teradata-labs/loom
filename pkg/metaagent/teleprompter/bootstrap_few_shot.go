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
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// BootstrapFewShot implements DSPy's BootstrapFewShot teleprompter.
//
// Algorithm:
// 1. Run agent on training set
// 2. Collect successful execution traces (using metric to filter)
// 3. Select best demonstrations (top K by quality score)
// 4. Update agent's Learned Layer with demonstrations
// 5. (Optional) Use a "teacher" agent with better LLM to generate demos
//
// Example usage:
//
//	bootstrap := NewBootstrapFewShot(tracer, registry)
//	result, err := bootstrap.Compile(ctx, &CompileRequest{
//	    Agent:    agent,
//	    Trainset: examples,
//	    Metric:   exactMatchMetric,
//	    Config:   config,
//	})
type BootstrapFewShot struct {
	*BaseTeleprompter
}

// NewBootstrapFewShot creates a new BootstrapFewShot teleprompter
func NewBootstrapFewShot(tracer observability.Tracer, registry *Registry) *BootstrapFewShot {
	return &BootstrapFewShot{
		BaseTeleprompter: NewBaseTeleprompter(tracer, registry),
	}
}

// Compile implements the Teleprompter interface
func (bf *BootstrapFewShot) Compile(
	ctx context.Context,
	req *CompileRequest,
) (*CompilationResult, error) {
	startTime := time.Now()
	tracer := req.Tracer
	if tracer == nil {
		tracer = bf.GetTracer()
	}

	ctx, span := tracer.StartSpan(ctx, "teleprompter.bootstrap_few_shot.compile")
	defer tracer.EndSpan(span)

	span.SetAttribute("agent_id", req.AgentID)
	span.SetAttribute("trainset_size", fmt.Sprintf("%d", len(req.Trainset)))

	// Validate and set defaults
	if err := bf.ValidateConfig(req.Config); err != nil {
		span.RecordError(err)
		span.Status = observability.Status{Code: observability.StatusError, Message: err.Error()}
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	bf.SetDefaultsConfig(req.Config)

	// Determine which agent to use for bootstrapping (teacher or student)
	bootstrapAgent := req.Agent
	if req.Config.Teacher != nil && req.Config.Teacher.UseTeacher {
		// Create teacher agent (more capable LLM)
		teacherAgent, err := bf.createTeacherAgent(ctx, req.Agent, req.Config.Teacher)
		if err != nil {
			// Fall back to student agent if teacher creation fails
			span.RecordError(err)
			span.SetAttribute("teacher_fallback", "true")
		} else {
			bootstrapAgent = teacherAgent
			span.SetAttribute("using_teacher", "true")
		}
	}

	// Step 1: Run agent on trainset, collect successful traces
	span.SetAttribute("step", "run_on_trainset")
	traces, err := bf.RunOnTrainset(
		ctx,
		bootstrapAgent,
		req.Trainset,
		req.Metric,
		req.Config.MinConfidence,
	)
	if err != nil {
		span.RecordError(err)
		span.Status = observability.Status{Code: observability.StatusError, Message: err.Error()}
		return nil, fmt.Errorf("failed to run on trainset: %w", err)
	}

	span.SetAttribute("successful_traces", fmt.Sprintf("%d", len(traces)))

	if len(traces) == 0 {
		return nil, fmt.Errorf("no successful traces collected (min_confidence=%.2f)", req.Config.MinConfidence)
	}

	// Step 2: Select best demonstrations
	span.SetAttribute("step", "select_demonstrations")
	maxDemos := int(req.Config.MaxBootstrappedDemos)
	if maxDemos == 0 {
		maxDemos = 5 // Default
	}

	demonstrations, err := bf.SelectDemonstrations(
		ctx,
		traces,
		maxDemos,
		loomv1.BootstrapStrategy_BOOTSTRAP_TOP_K, // Default strategy
	)
	if err != nil {
		span.RecordError(err)
		span.Status = observability.Status{Code: observability.StatusError, Message: err.Error()}
		return nil, fmt.Errorf("failed to select demonstrations: %w", err)
	}

	span.SetAttribute("demonstrations_selected", fmt.Sprintf("%d", len(demonstrations)))

	// Step 3: Apply demonstrations to student agent's Learned Layer
	span.SetAttribute("step", "apply_learned_layer")
	if err := bf.ApplyLearnedLayer(ctx, req.Agent, nil, demonstrations); err != nil {
		span.RecordError(err)
		span.Status = observability.Status{Code: observability.StatusError, Message: err.Error()}
		return nil, fmt.Errorf("failed to apply learned layer: %w", err)
	}

	// Step 4: Evaluate on trainset (final score)
	span.SetAttribute("step", "evaluate_trainset")
	trainsetScore, err := bf.EvaluateOnDevset(ctx, req.Agent, req.Trainset, req.Metric)
	if err != nil {
		span.RecordError(err)
		trainsetScore = 0.0
	}

	// Step 5: Evaluate on devset (if provided)
	var devsetScore float64
	if len(req.Devset) > 0 {
		span.SetAttribute("step", "evaluate_devset")
		devsetScore, err = bf.EvaluateOnDevset(ctx, req.Agent, req.Devset, req.Metric)
		if err != nil {
			span.RecordError(err)
			devsetScore = 0.0
		}
	}

	// Calculate improvement (compare to baseline without demonstrations)
	// Note: In production, you'd run baseline first, but for now we estimate
	improvementDelta := bf.ComputeImprovement(0.5, trainsetScore) // Assume 0.5 baseline

	// Build compilation result
	compilationTime := time.Since(startTime).Milliseconds()
	result := bf.BuildCompilationResult(
		req.AgentID,
		loomv1.TeleprompterType_TELEPROMPTER_BOOTSTRAP_FEW_SHOT,
		nil, // No prompt optimization in basic bootstrap
		demonstrations,
		trainsetScore,
		devsetScore,
		int32(len(req.Trainset)),
		int32(len(traces)),
		1, // Single round
		improvementDelta,
		compilationTime,
	)

	span.SetAttribute("trainset_score", fmt.Sprintf("%.4f", trainsetScore))
	span.SetAttribute("devset_score", fmt.Sprintf("%.4f", devsetScore))
	span.SetAttribute("improvement_delta", fmt.Sprintf("%.4f", improvementDelta))
	span.SetAttribute("compilation_time_ms", fmt.Sprintf("%d", compilationTime))
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Compilation successful"}

	return result, nil
}

// Type returns the teleprompter type
func (bf *BootstrapFewShot) Type() loomv1.TeleprompterType {
	return loomv1.TeleprompterType_TELEPROMPTER_BOOTSTRAP_FEW_SHOT
}

// Name returns a human-readable name
func (bf *BootstrapFewShot) Name() string {
	return "BootstrapFewShot"
}

// SupportsMultiRound indicates if this teleprompter supports iterative optimization
func (bf *BootstrapFewShot) SupportsMultiRound() bool {
	return false
}

// SupportsTeacher indicates if this teleprompter supports teacher-student bootstrapping
func (bf *BootstrapFewShot) SupportsTeacher() bool {
	return true
}

// createTeacherAgent creates a teacher agent with a more capable LLM
func (bf *BootstrapFewShot) createTeacherAgent(
	ctx context.Context,
	studentAgent Agent,
	teacherConfig *loomv1.TeacherConfig,
) (Agent, error) {
	// Clone student agent
	teacherAgent := studentAgent.Clone()

	// TODO: Replace LLM provider with teacher's more capable model
	// This requires access to the agent's LLM configuration, which
	// should be exposed through the Agent interface in production.
	//
	// For now, return cloned agent (same model)
	// In production:
	// - Get agent's LLM config
	// - Replace provider/model with teacher's config
	// - Adjust temperature, max_tokens, etc.

	return teacherAgent, nil
}

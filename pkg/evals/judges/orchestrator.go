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
	"encoding/json"
	"fmt"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/orchestration"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Orchestrator coordinates multiple judges for multi-dimensional evaluation.
// It manages sync/async execution, aggregates verdicts, and integrates with
// Loom's workflow orchestration patterns (Fork-Join for parallel execution).
type Orchestrator struct {
	registry     *Registry
	aggregator   *Aggregator
	tracer       observability.Tracer
	logger       *zap.Logger
	workflowOrch *orchestration.Orchestrator      // For Fork-Join pattern
	hawkExporter *observability.HawkJudgeExporter // For exporting verdicts to Hawk
}

// Config configures the judge orchestrator.
type Config struct {
	Registry     *Registry
	Aggregator   *Aggregator
	Tracer       observability.Tracer
	Logger       *zap.Logger
	WorkflowOrch *orchestration.Orchestrator      // Optional: for Fork-Join pattern
	HawkExporter *observability.HawkJudgeExporter // Optional: for Hawk verdict export
}

// NewOrchestrator creates a new judge orchestrator.
func NewOrchestrator(config *Config) *Orchestrator {
	if config == nil {
		config = &Config{}
	}

	if config.Registry == nil {
		config.Registry = NewRegistry()
	}
	if config.Aggregator == nil {
		config.Aggregator = NewAggregator(nil)
	}
	if config.Tracer == nil {
		config.Tracer = observability.NewNoOpTracer()
	}
	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}

	// Create workflow orchestrator if not provided (for Fork-Join pattern)
	workflowOrch := config.WorkflowOrch
	if workflowOrch == nil {
		workflowOrch = orchestration.NewOrchestrator(orchestration.Config{
			Tracer: config.Tracer,
			Logger: config.Logger,
		})
	}

	return &Orchestrator{
		registry:     config.Registry,
		aggregator:   config.Aggregator,
		tracer:       config.Tracer,
		logger:       config.Logger,
		workflowOrch: workflowOrch,
		hawkExporter: config.HawkExporter,
	}
}

// EvaluateStream runs multi-judge evaluation with streaming progress updates.
// This is the streaming variant that sends progress messages to the provided channel.
// Useful for long-running evaluations (MIPRO, BootstrapFewShot) to provide real-time feedback.
//
// The stream channel will receive:
// - JudgeStarted: Before each judge evaluation
// - JudgeCompleted: After each judge completes
// - ExampleCompleted: After all judges evaluate an example (if multiple examples)
// - EvaluationCompleted: After entire evaluation finishes
//
// The caller is responsible for closing the stream channel after this method returns.
func (o *Orchestrator) EvaluateStream(
	ctx context.Context,
	req *loomv1.EvaluateRequest,
	stream chan<- *loomv1.EvaluateProgress,
) (*loomv1.EvaluateResponse, error) {
	// Use internal implementation with streaming support
	return o.evaluateInternal(ctx, req, stream)
}

// Evaluate runs multi-judge evaluation on an agent output.
// This is the main entry point for the multi-judge system.
func (o *Orchestrator) Evaluate(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
	// Use internal implementation without streaming
	return o.evaluateInternal(ctx, req, nil)
}

// evaluateInternal is the shared implementation for both streaming and non-streaming evaluation.
// If stream is nil, no progress updates are sent.
func (o *Orchestrator) evaluateInternal(
	ctx context.Context,
	req *loomv1.EvaluateRequest,
	stream chan<- *loomv1.EvaluateProgress,
) (*loomv1.EvaluateResponse, error) {
	// Start tracing
	ctx, span := o.tracer.StartSpan(ctx, observability.SpanJudgeOrchestration)
	defer o.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("judge.count", len(req.JudgeIds))
		span.SetAttribute("judge.execution_mode", req.ExecutionMode.String())
		span.SetAttribute("judge.aggregation", req.Aggregation.String())
	}

	o.logger.Info("Starting multi-judge evaluation",
		zap.Strings("judge_ids", req.JudgeIds),
		zap.String("execution_mode", req.ExecutionMode.String()),
		zap.String("aggregation", req.Aggregation.String()),
	)

	startTime := time.Now()

	// Get judges from registry
	judges, err := o.registry.GetJudges(req.JudgeIds)
	if err != nil {
		return nil, fmt.Errorf("failed to get judges: %w", err)
	}

	if len(judges) == 0 {
		return nil, fmt.Errorf("no judges found for evaluation")
	}

	// Classify judges by criticality
	criticalJudges, nonCriticalJudges := o.classifyByCriticality(judges)

	o.logger.Debug("Classified judges",
		zap.Int("critical", len(criticalJudges)),
		zap.Int("non_critical", len(nonCriticalJudges)),
	)

	var allVerdicts []*loomv1.JudgeResult
	var criticalVerdicts []*loomv1.JudgeResult

	// Apply timeout if specified
	if req.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	// Execute based on execution mode
	switch req.ExecutionMode {
	case loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS:
		// All judges run synchronously
		allVerdicts, err = o.executeSyncJudges(ctx, judges, req.Context, req.FailFast, stream)
		if err != nil {
			return nil, fmt.Errorf("sync judge execution failed: %w", err)
		}

	case loomv1.ExecutionMode_EXECUTION_MODE_ASYNCHRONOUS:
		// All judges run asynchronously (but we still wait for them in this call)
		allVerdicts, err = o.executeAsyncJudges(ctx, judges, req.Context, stream)
		if err != nil {
			return nil, fmt.Errorf("async judge execution failed: %w", err)
		}

	case loomv1.ExecutionMode_EXECUTION_MODE_HYBRID:
		// Critical judges run sync, non-critical run async (fire and forget)
		criticalVerdicts, err = o.executeSyncJudges(ctx, criticalJudges, req.Context, req.FailFast, stream)
		if err != nil {
			return nil, fmt.Errorf("critical judge execution failed: %w", err)
		}

		// Early exit if critical judges fail (all-must-pass mode)
		if req.Aggregation == loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS {
			if !o.allPassed(criticalVerdicts) {
				aggregated := o.aggregator.Aggregate(criticalVerdicts, criticalJudges, req.Aggregation)

				return &loomv1.EvaluateResponse{
					Passed:      false,
					Verdicts:    criticalVerdicts,
					FinalScore:  aggregated.WeightedAverageScore,
					Explanation: "Critical judges failed (all-must-pass mode)",
					Aggregated:  aggregated,
					Metadata: &loomv1.EvaluationMetadata{
						TotalJudges:          int32(len(criticalJudges)),
						PassedJudges:         int32(o.countPassed(criticalVerdicts)),
						FailedJudges:         int32(len(criticalJudges) - o.countPassed(criticalVerdicts)),
						ExecutionMode:        req.ExecutionMode,
						TotalCostUsd:         aggregated.TotalCostUsd,
						TotalExecutionTimeMs: time.Since(startTime).Milliseconds(),
					},
				}, nil
			}
		}

		// Execute non-critical judges in background
		if len(nonCriticalJudges) > 0 {
			go func() {
				bgCtx := context.Background()
				// No streaming for background non-critical judges
				nonCriticalVerdicts, err := o.executeAsyncJudges(bgCtx, nonCriticalJudges, req.Context, nil)
				if err != nil {
					o.logger.Warn("Non-critical judge execution failed",
						zap.Error(err),
					)
					return
				}

				o.logger.Info("Non-critical judges completed",
					zap.Int("count", len(nonCriticalVerdicts)),
					zap.Int("passed", o.countPassed(nonCriticalVerdicts)),
				)

				// Store verdicts for later analysis (would integrate with storage here)
				// For now, just log them
				for _, verdict := range nonCriticalVerdicts {
					o.logger.Debug("Non-critical judge result",
						zap.String("judge", verdict.JudgeName),
						zap.String("verdict", verdict.Verdict),
						zap.Float64("score", verdict.OverallScore),
					)
				}
			}()
		}

		// For hybrid mode, only use critical verdicts for immediate response
		allVerdicts = criticalVerdicts

	default:
		// Default to synchronous
		allVerdicts, err = o.executeSyncJudges(ctx, judges, req.Context, req.FailFast, stream)
		if err != nil {
			return nil, fmt.Errorf("judge execution failed: %w", err)
		}
	}

	// Aggregate results
	aggregated := o.aggregator.Aggregate(allVerdicts, judges, req.Aggregation)
	finalVerdict := o.aggregator.ComputeFinalVerdict(aggregated, allVerdicts)

	// Build dimension scores
	dimensionScores := aggregated.AvgDimensionScores
	if dimensionScores == nil {
		dimensionScores = make(map[string]float64)
	}

	// Generate explanation and suggestions
	explanation := o.buildExplanation(allVerdicts, aggregated, req.Aggregation)
	suggestions := o.collectSuggestions(allVerdicts)

	// Phase 9: Export judge verdicts to Hawk if requested
	exportedToHawk := false
	if req.ExportToHawk && o.hawkExporter != nil {
		for _, verdict := range allVerdicts {
			if err := o.hawkExporter.ExportJudgeResult(ctx, verdict); err != nil {
				// Log warning but don't fail evaluation
				o.logger.Warn("Failed to export judge verdict to Hawk",
					zap.String("judge_id", verdict.JudgeId),
					zap.String("judge_name", verdict.JudgeName),
					zap.Error(err),
				)
			} else {
				exportedToHawk = true
			}
		}

		if exportedToHawk {
			o.logger.Info("Exported judge verdicts to Hawk",
				zap.Int("verdict_count", len(allVerdicts)),
			)
		}
	}

	// Build response
	response := &loomv1.EvaluateResponse{
		Passed:          finalVerdict == "PASS",
		Verdicts:        allVerdicts,
		DimensionScores: dimensionScores,
		FinalScore:      aggregated.WeightedAverageScore,
		Explanation:     explanation,
		Suggestions:     suggestions,
		Aggregated:      aggregated,
		Metadata: &loomv1.EvaluationMetadata{
			TotalJudges:          int32(len(allVerdicts)),
			PassedJudges:         int32(o.countPassed(allVerdicts)),
			FailedJudges:         int32(len(allVerdicts) - o.countPassed(allVerdicts)),
			TimeoutJudges:        0, // TODO: track timeouts
			ExecutionMode:        req.ExecutionMode,
			TotalCostUsd:         aggregated.TotalCostUsd,
			TotalExecutionTimeMs: time.Since(startTime).Milliseconds(),
			ExportedToHawk:       exportedToHawk,
		},
	}

	o.logger.Info("Multi-judge evaluation completed",
		zap.String("final_verdict", finalVerdict),
		zap.Float64("final_score", aggregated.WeightedAverageScore),
		zap.Int("total_judges", len(allVerdicts)),
		zap.Int("passed", o.countPassed(allVerdicts)),
		zap.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	)

	// Send EvaluationCompleted progress if streaming
	if stream != nil {
		select {
		case stream <- &loomv1.EvaluateProgress{
			Progress: &loomv1.EvaluateProgress_EvaluationCompleted{
				EvaluationCompleted: &loomv1.EvaluationCompleted{
					FinalResult:     response,
					TotalDurationMs: time.Since(startTime).Milliseconds(),
				},
			},
		}:
		case <-ctx.Done():
			// Context cancelled, don't block
		}
	}

	return response, nil
}

// classifyByCriticality separates judges into critical and non-critical groups.
func (o *Orchestrator) classifyByCriticality(judges []Judge) (critical, nonCritical []Judge) {
	critical = make([]Judge, 0)
	nonCritical = make([]Judge, 0)

	for _, judge := range judges {
		if judge.Criticality() == loomv1.JudgeCriticality_JUDGE_CRITICALITY_SAFETY_CRITICAL ||
			judge.Criticality() == loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL {
			critical = append(critical, judge)
		} else {
			nonCritical = append(nonCritical, judge)
		}
	}

	return critical, nonCritical
}

// executeSyncJudges runs judges in parallel using Fork-Join pattern.
// Wraps judges as agents, executes them in parallel, and extracts verdicts.
// If stream is provided, sends progress updates (JudgeStarted, JudgeCompleted).
func (o *Orchestrator) executeSyncJudges(
	ctx context.Context,
	judges []Judge,
	evalCtx *loomv1.EvaluationContext,
	failFast bool,
	stream chan<- *loomv1.EvaluateProgress,
) ([]*loomv1.JudgeResult, error) {
	if len(judges) == 0 {
		return []*loomv1.JudgeResult{}, nil
	}

	// Execute judges in parallel using goroutines
	type judgeResult struct {
		verdict *loomv1.JudgeResult
		err     error
		judge   Judge
	}

	resultChan := make(chan judgeResult, len(judges))

	// Start all judge evaluations in parallel
	for _, judge := range judges {
		go func(j Judge) {
			startTime := time.Now()
			o.logger.Debug("Executing judge", zap.String("judge", j.Name()))

			// Send JudgeStarted progress if streaming
			if stream != nil {
				select {
				case stream <- &loomv1.EvaluateProgress{
					Progress: &loomv1.EvaluateProgress_JudgeStarted{
						JudgeStarted: &loomv1.JudgeStarted{
							JudgeId:       j.Config().Id,
							ExampleNumber: 0, // Single example evaluation (not batch)
							StartedAt:     timestamppb.Now(),
						},
					},
				}:
				case <-ctx.Done():
					// Context cancelled, continue anyway
				}
			}

			// Create span for this judge evaluation
			ctx, span := o.tracer.StartSpan(ctx, observability.SpanJudgeEvaluation)
			defer o.tracer.EndSpan(span)

			// Wrap judge with retry and circuit breaker logic if configured
			wrappedJudge := o.wrapJudgeWithRetry(j)

			verdict, err := wrappedJudge.Evaluate(ctx, evalCtx)
			durationMs := time.Since(startTime).Milliseconds()

			// Send JudgeCompleted progress if streaming (even on error)
			if stream != nil && verdict != nil {
				select {
				case stream <- &loomv1.EvaluateProgress{
					Progress: &loomv1.EvaluateProgress_JudgeCompleted{
						JudgeCompleted: &loomv1.JudgeCompleted{
							JudgeId:       j.Config().Id,
							ExampleNumber: 0, // Single example evaluation
							Result:        verdict,
							DurationMs:    durationMs,
						},
					},
				}:
				case <-ctx.Done():
					// Context cancelled, continue anyway
				}
			}

			resultChan <- judgeResult{verdict: verdict, err: err, judge: j}
		}(judge)
	}

	// Collect results
	verdicts := make([]*loomv1.JudgeResult, 0, len(judges))
	for i := 0; i < len(judges); i++ {
		result := <-resultChan

		if result.err != nil {
			o.logger.Warn("Judge execution failed",
				zap.String("judge", result.judge.Name()),
				zap.Error(result.err))

			if failFast {
				return nil, fmt.Errorf("judge %s failed (fail-fast enabled): %w", result.judge.Name(), result.err)
			}
			continue
		}

		verdicts = append(verdicts, result.verdict)
	}

	return verdicts, nil
}

// extractJudgeResultFromAgentResult extracts JudgeResult from AgentResult metadata.
func extractJudgeResultFromAgentResult(agentResult *loomv1.AgentResult) (*loomv1.JudgeResult, error) {
	if agentResult == nil || agentResult.Metadata == nil {
		return nil, fmt.Errorf("agent result or metadata is nil")
	}

	// The judge_result is stored as JSON string in metadata
	resultJSON, ok := agentResult.Metadata["judge_result"]
	if !ok {
		return nil, fmt.Errorf("judge_result not found in metadata")
	}

	var result loomv1.JudgeResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal judge result: %w", err)
	}

	return &result, nil
}

// executeAsyncJudges runs judges asynchronously using Fork-Join pattern.
// Same as executeSyncJudges but without fail-fast behavior.
func (o *Orchestrator) executeAsyncJudges(
	ctx context.Context,
	judges []Judge,
	evalCtx *loomv1.EvaluationContext,
	stream chan<- *loomv1.EvaluateProgress,
) ([]*loomv1.JudgeResult, error) {
	// Use same Fork-Join implementation but without fail-fast
	return o.executeSyncJudges(ctx, judges, evalCtx, false, stream)
}

// allPassed checks if all verdicts passed.
func (o *Orchestrator) allPassed(verdicts []*loomv1.JudgeResult) bool {
	for _, verdict := range verdicts {
		if verdict.Verdict != "PASS" {
			return false
		}
	}
	return true
}

// countPassed counts how many verdicts passed.
func (o *Orchestrator) countPassed(verdicts []*loomv1.JudgeResult) int {
	count := 0
	for _, verdict := range verdicts {
		if verdict.Verdict == "PASS" {
			count++
		}
	}
	return count
}

// buildExplanation generates a human-readable explanation of the evaluation.
func (o *Orchestrator) buildExplanation(
	verdicts []*loomv1.JudgeResult,
	aggregated *loomv1.AggregatedJudgeMetrics,
	strategy loomv1.AggregationStrategy,
) string {
	passCount := o.countPassed(verdicts)
	totalCount := len(verdicts)

	switch strategy {
	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE:
		return fmt.Sprintf("Weighted average score: %.1f/100 (%d/%d judges passed)",
			aggregated.WeightedAverageScore, passCount, totalCount)

	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS:
		if passCount == totalCount {
			return fmt.Sprintf("All %d judges passed", totalCount)
		}
		return fmt.Sprintf("%d/%d judges failed (all-must-pass)", totalCount-passCount, totalCount)

	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS:
		return fmt.Sprintf("Majority vote: %d/%d judges passed (%.0f%%)",
			passCount, totalCount, aggregated.PassRate*100)

	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ANY_PASS:
		if passCount > 0 {
			return fmt.Sprintf("At least one judge passed (%d/%d)", passCount, totalCount)
		}
		return "No judges passed"

	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MIN_SCORE:
		return fmt.Sprintf("Minimum score: %.1f/100 (strictest judge)", aggregated.MinScore)

	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAX_SCORE:
		return fmt.Sprintf("Maximum score: %.1f/100 (best judge)", aggregated.MaxScore)

	default:
		return fmt.Sprintf("%d/%d judges passed", passCount, totalCount)
	}
}

// collectSuggestions aggregates suggestions from all judges.
func (o *Orchestrator) collectSuggestions(verdicts []*loomv1.JudgeResult) []string {
	suggestions := make([]string, 0)
	seen := make(map[string]bool)

	for _, verdict := range verdicts {
		for _, suggestion := range verdict.Suggestions {
			if !seen[suggestion] {
				suggestions = append(suggestions, suggestion)
				seen[suggestion] = true
			}
		}
	}

	return suggestions
}

// wrapJudgeWithRetry wraps a judge with retry and circuit breaker logic if configured.
// If the judge's config has retry_config, returns a RetryableJudge wrapper.
// Otherwise, returns the original judge.
func (o *Orchestrator) wrapJudgeWithRetry(judge Judge) Judge {
	config := judge.Config()
	if config == nil || config.RetryConfig == nil {
		return judge
	}

	// Check if retry is effectively disabled
	if config.RetryConfig.MaxAttempts <= 0 {
		return judge
	}

	// Wrap with RetryableJudge
	return NewRetryableJudge(judge, config, o.logger)
}

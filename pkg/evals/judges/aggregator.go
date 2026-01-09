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
	"fmt"
	"math"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Aggregator combines verdicts from multiple judges into a final result.
type Aggregator struct {
	// Configuration for aggregation
	config *AggregatorConfig
}

// AggregatorConfig configures verdict aggregation behavior.
type AggregatorConfig struct {
	// Default min passing score if not specified per judge
	DefaultMinPassingScore int32

	// Dimension weights for multi-dimensional scoring
	DimensionWeights map[string]float64
}

// NewAggregator creates a new verdict aggregator.
func NewAggregator(config *AggregatorConfig) *Aggregator {
	if config == nil {
		config = &AggregatorConfig{
			DefaultMinPassingScore: 80,
			DimensionWeights: map[string]float64{
				"quality": 1.0,
				"cost":    1.0,
				"safety":  1.0,
				"domain":  1.0,
			},
		}
	}

	return &Aggregator{
		config: config,
	}
}

// Aggregate combines judge results using the specified strategy.
func (a *Aggregator) Aggregate(
	verdicts []*loomv1.JudgeResult,
	judges []Judge,
	strategy loomv1.AggregationStrategy,
) *loomv1.AggregatedJudgeMetrics {
	if len(verdicts) == 0 {
		return &loomv1.AggregatedJudgeMetrics{
			Strategy: strategy,
		}
	}

	switch strategy {
	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE:
		return a.weightedAverage(verdicts, judges)

	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS:
		return a.allMustPass(verdicts)

	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS:
		return a.majorityPass(verdicts)

	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ANY_PASS:
		return a.anyPass(verdicts)

	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MIN_SCORE:
		return a.minScore(verdicts, strategy)

	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAX_SCORE:
		return a.maxScore(verdicts, strategy)

	default:
		// Default to weighted average
		return a.weightedAverage(verdicts, judges)
	}
}

// weightedAverage aggregates using weighted average of scores.
func (a *Aggregator) weightedAverage(verdicts []*loomv1.JudgeResult, judges []Judge) *loomv1.AggregatedJudgeMetrics {
	totalWeight := 0.0
	weightedSum := 0.0
	passCount := 0
	totalExecutionTime := int64(0)
	totalCost := 0.0

	// Calculate min/max scores
	minScore := 100.0
	maxScore := 0.0

	// Aggregate dimension scores
	dimensionScoresSum := make(map[string]float64)
	dimensionCounts := make(map[string]int)

	// Build judge weight map
	judgeWeights := make(map[string]float64)
	for _, judge := range judges {
		judgeWeights[judge.ID()] = judge.Weight()
	}

	for _, verdict := range verdicts {
		weight := 1.0
		if w, ok := judgeWeights[verdict.JudgeId]; ok {
			weight = w
		}

		totalWeight += weight
		weightedSum += verdict.OverallScore * weight

		if verdict.Verdict == "PASS" {
			passCount++
		}

		if verdict.OverallScore < minScore {
			minScore = verdict.OverallScore
		}
		if verdict.OverallScore > maxScore {
			maxScore = verdict.OverallScore
		}

		totalExecutionTime += verdict.ExecutionTimeMs
		totalCost += verdict.CostUsd

		// Aggregate dimension scores
		for dim, score := range verdict.DimensionScores {
			dimensionScoresSum[dim] += score
			dimensionCounts[dim]++
		}
	}

	weightedAvg := weightedSum / totalWeight
	passRate := float64(passCount) / float64(len(verdicts))

	// Calculate standard deviation
	varianceSum := 0.0
	for _, verdict := range verdicts {
		diff := verdict.OverallScore - weightedAvg
		varianceSum += diff * diff
	}
	stddev := 0.0
	if len(verdicts) > 1 {
		stddev = math.Sqrt(varianceSum / float64(len(verdicts)))
	}

	// Calculate average dimension scores
	avgDimensionScores := make(map[string]float64)
	for dim, sum := range dimensionScoresSum {
		avgDimensionScores[dim] = sum / float64(dimensionCounts[dim])
	}

	return &loomv1.AggregatedJudgeMetrics{
		WeightedAverageScore: weightedAvg,
		MinScore:             minScore,
		MaxScore:             maxScore,
		ScoreStddev:          stddev,
		PassRate:             passRate,
		Strategy:             loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
		TotalExecutionTimeMs: totalExecutionTime,
		TotalCostUsd:         totalCost,
		AvgDimensionScores:   avgDimensionScores,
	}
}

// allMustPass requires all judges to pass.
func (a *Aggregator) allMustPass(verdicts []*loomv1.JudgeResult) *loomv1.AggregatedJudgeMetrics {
	passCount := 0
	avgScore := 0.0
	minScore := 100.0
	maxScore := 0.0
	totalExecutionTime := int64(0)
	totalCost := 0.0

	for _, verdict := range verdicts {
		if verdict.Verdict == "PASS" {
			passCount++
		}

		avgScore += verdict.OverallScore

		if verdict.OverallScore < minScore {
			minScore = verdict.OverallScore
		}
		if verdict.OverallScore > maxScore {
			maxScore = verdict.OverallScore
		}

		totalExecutionTime += verdict.ExecutionTimeMs
		totalCost += verdict.CostUsd
	}

	avgScore /= float64(len(verdicts))
	passRate := float64(passCount) / float64(len(verdicts))

	return &loomv1.AggregatedJudgeMetrics{
		WeightedAverageScore: avgScore,
		MinScore:             minScore,
		MaxScore:             maxScore,
		PassRate:             passRate,
		Strategy:             loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS,
		TotalExecutionTimeMs: totalExecutionTime,
		TotalCostUsd:         totalCost,
	}
}

// majorityPass requires >50% of judges to pass.
func (a *Aggregator) majorityPass(verdicts []*loomv1.JudgeResult) *loomv1.AggregatedJudgeMetrics {
	passCount := 0
	avgScore := 0.0
	minScore := 100.0
	maxScore := 0.0
	totalExecutionTime := int64(0)
	totalCost := 0.0

	for _, verdict := range verdicts {
		if verdict.Verdict == "PASS" {
			passCount++
		}

		avgScore += verdict.OverallScore

		if verdict.OverallScore < minScore {
			minScore = verdict.OverallScore
		}
		if verdict.OverallScore > maxScore {
			maxScore = verdict.OverallScore
		}

		totalExecutionTime += verdict.ExecutionTimeMs
		totalCost += verdict.CostUsd
	}

	avgScore /= float64(len(verdicts))
	passRate := float64(passCount) / float64(len(verdicts))

	return &loomv1.AggregatedJudgeMetrics{
		WeightedAverageScore: avgScore,
		MinScore:             minScore,
		MaxScore:             maxScore,
		PassRate:             passRate,
		Strategy:             loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS,
		TotalExecutionTimeMs: totalExecutionTime,
		TotalCostUsd:         totalCost,
	}
}

// anyPass requires at least one judge to pass.
func (a *Aggregator) anyPass(verdicts []*loomv1.JudgeResult) *loomv1.AggregatedJudgeMetrics {
	passCount := 0
	avgScore := 0.0
	minScore := 100.0
	maxScore := 0.0
	totalExecutionTime := int64(0)
	totalCost := 0.0

	for _, verdict := range verdicts {
		if verdict.Verdict == "PASS" {
			passCount++
		}

		avgScore += verdict.OverallScore

		if verdict.OverallScore < minScore {
			minScore = verdict.OverallScore
		}
		if verdict.OverallScore > maxScore {
			maxScore = verdict.OverallScore
		}

		totalExecutionTime += verdict.ExecutionTimeMs
		totalCost += verdict.CostUsd
	}

	avgScore /= float64(len(verdicts))
	passRate := float64(passCount) / float64(len(verdicts))

	return &loomv1.AggregatedJudgeMetrics{
		WeightedAverageScore: avgScore,
		MinScore:             minScore,
		MaxScore:             maxScore,
		PassRate:             passRate,
		Strategy:             loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ANY_PASS,
		TotalExecutionTimeMs: totalExecutionTime,
		TotalCostUsd:         totalCost,
	}
}

// minScore uses the minimum score across all judges.
func (a *Aggregator) minScore(verdicts []*loomv1.JudgeResult, strategy loomv1.AggregationStrategy) *loomv1.AggregatedJudgeMetrics {
	passCount := 0
	minScore := 100.0
	maxScore := 0.0
	totalExecutionTime := int64(0)
	totalCost := 0.0

	for _, verdict := range verdicts {
		if verdict.Verdict == "PASS" {
			passCount++
		}

		if verdict.OverallScore < minScore {
			minScore = verdict.OverallScore
		}
		if verdict.OverallScore > maxScore {
			maxScore = verdict.OverallScore
		}

		totalExecutionTime += verdict.ExecutionTimeMs
		totalCost += verdict.CostUsd
	}

	passRate := float64(passCount) / float64(len(verdicts))

	return &loomv1.AggregatedJudgeMetrics{
		WeightedAverageScore: minScore, // Use min score as final score
		MinScore:             minScore,
		MaxScore:             maxScore,
		PassRate:             passRate,
		Strategy:             strategy,
		TotalExecutionTimeMs: totalExecutionTime,
		TotalCostUsd:         totalCost,
	}
}

// maxScore uses the maximum score across all judges.
func (a *Aggregator) maxScore(verdicts []*loomv1.JudgeResult, strategy loomv1.AggregationStrategy) *loomv1.AggregatedJudgeMetrics {
	passCount := 0
	minScore := 100.0
	maxScore := 0.0
	totalExecutionTime := int64(0)
	totalCost := 0.0

	for _, verdict := range verdicts {
		if verdict.Verdict == "PASS" {
			passCount++
		}

		if verdict.OverallScore < minScore {
			minScore = verdict.OverallScore
		}
		if verdict.OverallScore > maxScore {
			maxScore = verdict.OverallScore
		}

		totalExecutionTime += verdict.ExecutionTimeMs
		totalCost += verdict.CostUsd
	}

	passRate := float64(passCount) / float64(len(verdicts))

	return &loomv1.AggregatedJudgeMetrics{
		WeightedAverageScore: maxScore, // Use max score as final score
		MinScore:             minScore,
		MaxScore:             maxScore,
		PassRate:             passRate,
		Strategy:             strategy,
		TotalExecutionTimeMs: totalExecutionTime,
		TotalCostUsd:         totalCost,
	}
}

// ComputeFinalVerdict determines the final pass/fail verdict based on aggregated metrics.
func (a *Aggregator) ComputeFinalVerdict(
	aggregated *loomv1.AggregatedJudgeMetrics,
	verdicts []*loomv1.JudgeResult,
) string {
	switch aggregated.Strategy {
	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE:
		if aggregated.WeightedAverageScore >= 80 {
			return "PASS"
		} else if aggregated.WeightedAverageScore >= 60 {
			return "PARTIAL"
		}
		return "FAIL"

	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS:
		// All judges must pass
		if aggregated.PassRate >= 1.0 {
			return "PASS"
		}
		return "FAIL"

	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS:
		// >50% must pass
		if aggregated.PassRate > 0.5 {
			return "PASS"
		} else if aggregated.PassRate >= 0.3 {
			return "PARTIAL"
		}
		return "FAIL"

	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ANY_PASS:
		// At least one judge passes
		if aggregated.PassRate > 0 {
			return "PASS"
		}
		return "FAIL"

	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MIN_SCORE:
		if aggregated.MinScore >= 80 {
			return "PASS"
		} else if aggregated.MinScore >= 60 {
			return "PARTIAL"
		}
		return "FAIL"

	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAX_SCORE:
		if aggregated.MaxScore >= 80 {
			return "PASS"
		} else if aggregated.MaxScore >= 60 {
			return "PARTIAL"
		}
		return "FAIL"

	default:
		return "PARTIAL"
	}
}

// FormatJudgeFailures formats failure reasons from judge verdicts.
func FormatJudgeFailures(verdicts []*loomv1.JudgeResult) string {
	failures := make([]string, 0)
	for _, verdict := range verdicts {
		if verdict.Verdict != "PASS" {
			failure := fmt.Sprintf("%s: %s (score: %.1f)",
				verdict.JudgeName,
				verdict.Reasoning,
				verdict.OverallScore,
			)
			failures = append(failures, failure)
		}
	}

	if len(failures) == 0 {
		return "All judges passed"
	}

	result := fmt.Sprintf("%d judge(s) failed: ", len(failures))
	for i, f := range failures {
		if i > 0 {
			result += "; "
		}
		result += f
	}

	return result
}

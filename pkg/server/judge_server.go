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

package server

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/evals/judges"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// JudgeServer implements loomv1.JudgeServiceServer.
// It stores judge configurations in memory and executes evaluations
// via judges.LLMJudge (no hawk build tag required).
type JudgeServer struct {
	loomv1.UnimplementedJudgeServiceServer

	mu              sync.RWMutex
	configs         map[string]*loomv1.JudgeConfig // judgeID → config
	providerPool    map[string]agent.LLMProvider   // name → provider
	defaultProvider agent.LLMProvider              // fallback LLM for evaluation
	tracer          observability.Tracer
	logger          *zap.Logger
}

// NewJudgeServer creates a new JudgeServer with the given tracer and logger.
func NewJudgeServer(tracer observability.Tracer, logger *zap.Logger) *JudgeServer {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &JudgeServer{
		configs: make(map[string]*loomv1.JudgeConfig),
		tracer:  tracer,
		logger:  logger,
	}
}

// SetProviderPool configures the provider pool and default fallback provider.
func (s *JudgeServer) SetProviderPool(pool map[string]agent.LLMProvider, defaultP agent.LLMProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providerPool = pool
	s.defaultProvider = defaultP
}

// GetJudgeConfig returns the judge config for the given ID.
// Used by Server to resolve req.JudgeId in ABTest.
func (s *JudgeServer) GetJudgeConfig(id string) (*loomv1.JudgeConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg, ok := s.configs[id]
	if !ok {
		return nil, fmt.Errorf("judge %q not registered", id)
	}
	return cfg, nil
}

// RegisterJudge stores a judge configuration.
func (s *JudgeServer) RegisterJudge(ctx context.Context, req *loomv1.RegisterJudgeRequest) (*loomv1.RegisterJudgeResponse, error) {
	if req.Config == nil {
		return nil, status.Error(codes.InvalidArgument, "config is required")
	}

	// Derive an ID from the config name (slug) or generate a UUID.
	id := req.Config.Id
	if id == "" {
		if req.Config.Name != "" {
			id = slugify(req.Config.Name)
		} else {
			id = uuid.New().String()
		}
		req.Config.Id = id
	}

	s.mu.Lock()
	s.configs[id] = req.Config
	s.mu.Unlock()

	s.logger.Debug("judge registered", zap.String("judge_id", id), zap.String("name", req.Config.Name))

	return &loomv1.RegisterJudgeResponse{
		JudgeId: id,
		Message: fmt.Sprintf("judge %q registered successfully", id),
	}, nil
}

// EvaluateWithJudges runs named judges against the provided context and returns aggregated results.
func (s *JudgeServer) EvaluateWithJudges(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
	if len(req.JudgeIds) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one judge_id is required")
	}

	verdicts, err := s.runJudges(ctx, req.JudgeIds, req.Context)
	if err != nil {
		return nil, err
	}

	return s.buildResponse(verdicts, req.Aggregation), nil
}

// EvaluateWithJudgesStream runs judges with streaming progress events.
func (s *JudgeServer) EvaluateWithJudgesStream(req *loomv1.EvaluateRequest, stream loomv1.JudgeService_EvaluateWithJudgesStreamServer) error {
	if len(req.JudgeIds) == 0 {
		return status.Error(codes.InvalidArgument, "at least one judge_id is required")
	}

	ctx := stream.Context()
	var verdicts []*loomv1.JudgeResult

	for _, judgeID := range req.JudgeIds {
		// Send JudgeStarted event.
		if err := stream.Send(&loomv1.EvaluateProgress{
			Progress: &loomv1.EvaluateProgress_JudgeStarted{
				JudgeStarted: &loomv1.JudgeStarted{
					JudgeId:   judgeID,
					StartedAt: timestamppb.Now(),
				},
			},
		}); err != nil {
			return err
		}

		start := time.Now()
		result, judgeErr := s.evaluateSingle(ctx, judgeID, req.Context)
		durationMs := time.Since(start).Milliseconds()
		if judgeErr != nil {
			return judgeErr
		}
		verdicts = append(verdicts, result)

		// Send JudgeCompleted event.
		if err := stream.Send(&loomv1.EvaluateProgress{
			Progress: &loomv1.EvaluateProgress_JudgeCompleted{
				JudgeCompleted: &loomv1.JudgeCompleted{
					JudgeId:    judgeID,
					Result:     result,
					DurationMs: durationMs,
				},
			},
		}); err != nil {
			return err
		}
	}

	finalResp := s.buildResponse(verdicts, req.Aggregation)

	// Send EvaluationCompleted event.
	return stream.Send(&loomv1.EvaluateProgress{
		Progress: &loomv1.EvaluateProgress_EvaluationCompleted{
			EvaluationCompleted: &loomv1.EvaluationCompleted{
				FinalResult: finalResp,
			},
		},
	})
}

// GetJudgeHistory returns empty history (no persistence in this implementation).
func (s *JudgeServer) GetJudgeHistory(_ context.Context, _ *loomv1.GetJudgeHistoryRequest) (*loomv1.GetJudgeHistoryResponse, error) {
	s.logger.Debug("GetJudgeHistory called; in-memory store has no persistence")
	return &loomv1.GetJudgeHistoryResponse{}, nil
}

// runJudges executes all named judges and returns their results.
func (s *JudgeServer) runJudges(ctx context.Context, judgeIDs []string, evalCtx *loomv1.EvaluationContext) ([]*loomv1.JudgeResult, error) {
	verdicts := make([]*loomv1.JudgeResult, 0, len(judgeIDs))
	for _, id := range judgeIDs {
		result, err := s.evaluateSingle(ctx, id, evalCtx)
		if err != nil {
			return nil, err
		}
		verdicts = append(verdicts, result)
	}
	return verdicts, nil
}

// evaluateSingle looks up a judge config and runs evaluation.
func (s *JudgeServer) evaluateSingle(ctx context.Context, judgeID string, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
	cfg, err := s.GetJudgeConfig(judgeID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "judge %q not found", judgeID)
	}

	provider := s.resolveProvider()
	if provider == nil {
		return nil, status.Error(codes.FailedPrecondition, "no LLM provider available for evaluation; call SetProviderPool first")
	}

	judge, err := judges.NewLLMJudge(provider, cfg, s.tracer)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create judge %q: %v", judgeID, err)
	}

	return judge.Evaluate(ctx, evalCtx)
}

// resolveProvider returns the best available LLM provider.
// It checks defaultProvider; pool is available for callers who need named access.
func (s *JudgeServer) resolveProvider() agent.LLMProvider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.defaultProvider
}

// buildResponse aggregates judge verdicts into an EvaluateResponse.
func (s *JudgeServer) buildResponse(verdicts []*loomv1.JudgeResult, strategy loomv1.AggregationStrategy) *loomv1.EvaluateResponse {
	if len(verdicts) == 0 {
		return &loomv1.EvaluateResponse{Passed: false}
	}

	aggregated := s.aggregate(verdicts, strategy)
	passed := s.computePassed(verdicts, aggregated, strategy)

	// Collect suggestions and explanation from verdicts.
	var suggestions []string
	var explanations []string
	for _, v := range verdicts {
		suggestions = append(suggestions, v.Suggestions...)
		if v.Reasoning != "" {
			explanations = append(explanations, fmt.Sprintf("[%s] %s", v.JudgeName, v.Reasoning))
		}
	}

	total := int32(len(verdicts)) // #nosec G115 -- judge count never approaches int32 max
	passed32 := countPassed(verdicts)
	return &loomv1.EvaluateResponse{
		Passed:      passed,
		Verdicts:    verdicts,
		FinalScore:  aggregated.WeightedAverageScore,
		Explanation: strings.Join(explanations, "; "),
		Suggestions: suggestions,
		Aggregated:  aggregated,
		Metadata: &loomv1.EvaluationMetadata{
			TotalJudges:  total,
			PassedJudges: passed32,
			FailedJudges: total - passed32,
		},
	}
}

// aggregate implements WEIGHTED_AVERAGE and MAJORITY_PASS; defaults to WEIGHTED_AVERAGE.
func (s *JudgeServer) aggregate(verdicts []*loomv1.JudgeResult, strategy loomv1.AggregationStrategy) *loomv1.AggregatedJudgeMetrics {
	n := len(verdicts)
	if n == 0 {
		return &loomv1.AggregatedJudgeMetrics{Strategy: strategy}
	}

	var totalWeight float64
	var weightedSum float64
	var minScore = math.MaxFloat64
	var maxScore = -math.MaxFloat64
	var passCount int
	var totalTimeMs int64
	var totalCostUSD float64
	dimSums := make(map[string]float64)
	dimCounts := make(map[string]int)

	for _, v := range verdicts {
		score := v.OverallScore
		weight := 1.0 // default weight

		weightedSum += score * weight
		totalWeight += weight

		if score < minScore {
			minScore = score
		}
		if score > maxScore {
			maxScore = score
		}
		if v.Verdict == "PASS" {
			passCount++
		}
		totalTimeMs += v.ExecutionTimeMs
		totalCostUSD += v.CostUsd

		for dim, val := range v.DimensionScores {
			dimSums[dim] += val
			dimCounts[dim]++
		}
	}

	weightedAvg := 0.0
	if totalWeight > 0 {
		weightedAvg = weightedSum / totalWeight
	}
	passRate := float64(passCount) / float64(n) * 100.0

	// Compute stddev
	var variance float64
	if totalWeight > 0 {
		mean := weightedAvg
		for _, v := range verdicts {
			diff := v.OverallScore - mean
			variance += diff * diff
		}
		variance /= float64(n)
	}

	avgDimScores := make(map[string]float64)
	for dim, sum := range dimSums {
		avgDimScores[dim] = sum / float64(dimCounts[dim])
	}

	return &loomv1.AggregatedJudgeMetrics{
		WeightedAverageScore: weightedAvg,
		MinScore:             minScore,
		MaxScore:             maxScore,
		ScoreStddev:          math.Sqrt(variance),
		PassRate:             passRate,
		Strategy:             strategy,
		TotalExecutionTimeMs: totalTimeMs,
		TotalCostUsd:         totalCostUSD,
		AvgDimensionScores:   avgDimScores,
	}
}

// computePassed determines overall pass/fail based on the aggregation strategy.
func (s *JudgeServer) computePassed(verdicts []*loomv1.JudgeResult, agg *loomv1.AggregatedJudgeMetrics, strategy loomv1.AggregationStrategy) bool {
	passCount := int(countPassed(verdicts))
	n := len(verdicts)

	switch strategy {
	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS:
		return passCount > n/2
	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS:
		return passCount == n
	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ANY_PASS:
		return passCount > 0
	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MIN_SCORE:
		return agg.MinScore >= 80
	case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAX_SCORE:
		return agg.MaxScore >= 80
	default: // WEIGHTED_AVERAGE and UNSPECIFIED
		return agg.WeightedAverageScore >= 80
	}
}

// countPassed returns the number of verdicts with "PASS" verdict.
func countPassed(verdicts []*loomv1.JudgeResult) int32 {
	var count int32
	for _, v := range verdicts {
		if v.Verdict == "PASS" {
			count++
		}
	}
	return count
}

// slugify converts a name to a URL-safe identifier.
var nonAlphanumericRE = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(name string) string {
	s := strings.ToLower(name)
	s = nonAlphanumericRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return uuid.New().String()
	}
	return s
}

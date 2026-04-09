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

//go:build fts5

package main

import (
	"context"
	"fmt"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const correctnessJudgeID = "longmemeval-correctness"

// Scorer uses the Loom JudgeService to evaluate answer correctness.
type Scorer struct {
	client loomv1.JudgeServiceClient
	conn   *grpc.ClientConn
	logger *zap.Logger
}

// NewScorer connects to a Loom server and registers the correctness judge.
func NewScorer(serverAddr string, logger *zap.Logger) (*Scorer, error) {
	conn, err := grpc.NewClient(serverAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", serverAddr, err)
	}

	client := loomv1.NewJudgeServiceClient(conn)

	// Register the correctness judge.
	_, err = client.RegisterJudge(context.Background(), &loomv1.RegisterJudgeRequest{
		Config: &loomv1.JudgeConfig{
			Id:   correctnessJudgeID,
			Name: "LongMemEval Correctness",
			Criteria: "Evaluate whether the hypothesis answers the question correctly " +
				"based on the ground truth answer. The hypothesis does NOT need to match " +
				"word-for-word — it just needs to contain the same factual answer. " +
				"For numeric answers (dates, counts, durations), the number must match. " +
				"For factual answers, the key fact must be present. " +
				"Score 90-100 if correct, 50-70 if partially correct (right topic but wrong detail), " +
				"0-30 if wrong or 'I don't know'.",
			Weight:          1.0,
			MinPassingScore: 80,
			Criticality:     loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL,
			Type:            loomv1.JudgeType_JUDGE_TYPE_CUSTOM,
			Dimensions:      []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY},
		},
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("register judge: %w", err)
	}

	logger.Info("correctness judge registered", zap.String("id", correctnessJudgeID))

	return &Scorer{
		client: client,
		conn:   conn,
		logger: logger,
	}, nil
}

// Close closes the gRPC connection.
func (s *Scorer) Close() error {
	return s.conn.Close()
}

// ScoredResult extends EntryResult with judge scoring.
type ScoredResult struct {
	EntryResult
	Score   float64 `json:"score"`
	Verdict string  `json:"verdict"`
	Reason  string  `json:"reason"`
}

// ScoreResults evaluates all results using the judge service.
func (s *Scorer) ScoreResults(ctx context.Context, results []EntryResult) []ScoredResult {
	scored := make([]ScoredResult, len(results))

	for i, r := range results {
		scored[i] = ScoredResult{EntryResult: r}

		if r.Error != "" {
			scored[i].Verdict = "ERROR"
			scored[i].Reason = r.Error
			continue
		}

		prompt := fmt.Sprintf(
			"Question: %s\nGround truth answer: %s",
			r.QuestionID, r.GroundTruth,
		)

		evalCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		resp, err := s.client.EvaluateWithJudges(evalCtx, &loomv1.EvaluateRequest{
			Context: &loomv1.EvaluationContext{
				Prompt:   prompt,
				Response: r.Hypothesis,
				Metadata: map[string]string{
					"question_id":   r.QuestionID,
					"question_type": r.QuestionType,
					"ground_truth":  r.GroundTruth,
				},
			},
			JudgeIds:    []string{correctnessJudgeID},
			Aggregation: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
		})
		cancel()

		if err != nil {
			s.logger.Warn("judge evaluation failed",
				zap.String("question_id", r.QuestionID),
				zap.Error(err))
			scored[i].Verdict = "JUDGE_ERROR"
			scored[i].Reason = err.Error()
			continue
		}

		scored[i].Score = resp.FinalScore
		if resp.Passed {
			scored[i].Verdict = "PASS"
		} else if resp.FinalScore >= 50 {
			scored[i].Verdict = "PARTIAL"
		} else {
			scored[i].Verdict = "FAIL"
		}
		scored[i].Reason = resp.Explanation

		s.logger.Info("scored",
			zap.String("question_id", r.QuestionID),
			zap.Float64("score", resp.FinalScore),
			zap.String("verdict", scored[i].Verdict),
		)
	}

	return scored
}

// PrintScoredSummary prints results with judge scores.
func PrintScoredSummary(scored []ScoredResult) {
	var pass, partial, fail, errors int
	var totalScore float64
	scoredCount := 0

	for _, s := range scored {
		switch s.Verdict {
		case "PASS":
			pass++
			totalScore += s.Score
			scoredCount++
		case "PARTIAL":
			partial++
			totalScore += s.Score
			scoredCount++
		case "FAIL":
			fail++
			totalScore += s.Score
			scoredCount++
		default:
			errors++
		}
	}

	n := len(scored)
	fmt.Println("Judge-Scored Results:")
	fmt.Printf("  PASS:    %d/%d (%.0f%%)\n", pass, n, float64(pass)/float64(n)*100)
	fmt.Printf("  PARTIAL: %d/%d\n", partial, n)
	fmt.Printf("  FAIL:    %d/%d\n", fail, n)
	if errors > 0 {
		fmt.Printf("  ERRORS:  %d/%d\n", errors, n)
	}
	if scoredCount > 0 {
		fmt.Printf("  Avg Score: %.1f/100\n", totalScore/float64(scoredCount))
	}
}

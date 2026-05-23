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
	"sync"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	correctnessJudgeID = "longmemeval-correctness"
	preferenceJudgeID  = "longmemeval-preference"
)

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
			Criteria: "Evaluate whether the hypothesis CONTAINS the key fact from the ground " +
				"truth answer. The hypothesis does NOT need to match word-for-word and MAY " +
				"include extra context, caveats, or elaboration — do NOT penalize verbosity. " +
				"Only the presence or absence of the key fact matters.\n\n" +
				"Step 1: Extract the key fact from the ground truth. For named entities (people, " +
				"places, things), the name is the key fact. For numeric answers (dates, counts, " +
				"durations), the number is the key fact.\n" +
				"Step 2: Check whether the hypothesis explicitly states that key fact. Case and " +
				"formatting do not matter. Additional correct detail beyond the key fact is " +
				"neutral — it does not raise or lower the score.\n" +
				"Step 3: Score.\n" +
				"  - 90-100: key fact is explicitly present in the hypothesis.\n" +
				"  - 50-70: hypothesis is on the right topic but the key fact is missing, wrong, " +
				"or only implied.\n" +
				"  - 0-30: hypothesis is wrong, contradicts the key fact, or says 'I don't know'.\n\n" +
				"Ignore whether the hypothesis adds extra information that isn't in the ground " +
				"truth — extra context never reduces the score as long as the key fact is stated " +
				"and not contradicted.",
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

	// Register the preference-compliance judge (used for single-session-preference questions,
	// where the "ground truth" is a description of the user's preferences rather than a
	// factual answer — the hypothesis should be evaluated for whether its recommendation
	// respects those preferences).
	_, err = client.RegisterJudge(context.Background(), &loomv1.RegisterJudgeRequest{
		Config: &loomv1.JudgeConfig{
			Id:   preferenceJudgeID,
			Name: "LongMemEval Preference Compliance",
			Criteria: "The 'ground truth' describes a user preference derived from an earlier " +
				"session (e.g., 'prefers Sony-compatible accessories'). The hypothesis is a " +
				"recommendation. Evaluate whether the recommendation respects the stated " +
				"preference — do NOT require textual overlap with the ground truth. " +
				"Score 90-100 if the recommendation clearly aligns with every stated preference. " +
				"Score 50-70 if partially compliant (respects some preferences, violates or ignores others). " +
				"Score 0-30 if the recommendation contradicts the preference, ignores it entirely, " +
				"or answers 'I don't know'. A generic recommendation that happens not to violate the " +
				"preference but shows no awareness of it should score 40-60.",
			Weight:          1.0,
			MinPassingScore: 80,
			Criticality:     loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL,
			Type:            loomv1.JudgeType_JUDGE_TYPE_CUSTOM,
			Dimensions:      []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY},
		},
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("register preference judge: %w", err)
	}
	logger.Info("preference judge registered", zap.String("id", preferenceJudgeID))

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

// BackfillQuestions enriches results with question text from the dataset.
// This is needed for results generated before the Question field was added.
func BackfillQuestions(results []EntryResult, datasetPath string) {
	if datasetPath == "" {
		return
	}
	entries, err := LoadDataset(datasetPath)
	if err != nil {
		return
	}
	lookup := make(map[string]string, len(entries))
	for _, e := range entries {
		lookup[e.QuestionID] = e.Question
	}
	for i := range results {
		if results[i].Question == "" {
			results[i].Question = lookup[results[i].QuestionID]
		}
	}
}

// ScoreResults evaluates all results using the judge service with concurrency control.
func (s *Scorer) ScoreResults(ctx context.Context, results []EntryResult, concurrency int) []ScoredResult {
	if concurrency < 1 {
		concurrency = 1
	}

	scored := make([]ScoredResult, len(results))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, r := range results {
		scored[i] = ScoredResult{EntryResult: r}

		if r.Error != "" {
			scored[i].Verdict = "ERROR"
			scored[i].Reason = r.Error
			continue
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, r EntryResult) {
			defer wg.Done()
			defer func() { <-sem }()

			judgeID := correctnessJudgeID
			var prompt string
			if r.QuestionType == "single-session-preference" {
				judgeID = preferenceJudgeID
				prompt = fmt.Sprintf(
					"Question the user asked: %s\n"+
						"User's preference (from an earlier session): %s",
					r.Question, r.GroundTruth,
				)
			} else {
				prompt = fmt.Sprintf(
					"Question: %s\nGround truth answer: %s",
					r.Question, r.GroundTruth,
				)
			}

			evalCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
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
				JudgeIds:    []string{judgeID},
				Aggregation: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
			})
			cancel()

			if err != nil {
				s.logger.Warn("judge evaluation failed",
					zap.String("question_id", r.QuestionID),
					zap.Error(err))
				scored[idx].Verdict = "JUDGE_ERROR"
				scored[idx].Reason = err.Error()
				return
			}

			scored[idx].Score = resp.FinalScore
			if resp.Passed {
				scored[idx].Verdict = "PASS"
			} else if resp.FinalScore >= 50 {
				scored[idx].Verdict = "PARTIAL"
			} else {
				scored[idx].Verdict = "FAIL"
			}
			scored[idx].Reason = resp.Explanation

			s.logger.Info("scored",
				zap.String("question_id", r.QuestionID),
				zap.Float64("score", resp.FinalScore),
				zap.String("verdict", scored[idx].Verdict),
			)
		}(i, r)
	}

	wg.Wait()
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

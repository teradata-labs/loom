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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// mockStreamingJudge is a test helper for streaming tests
type mockStreamingJudge struct {
	config       *loomv1.JudgeConfig
	evaluateFunc func(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error)
}

func (m *mockStreamingJudge) ID() string                           { return m.config.Id }
func (m *mockStreamingJudge) Name() string                         { return m.config.Name }
func (m *mockStreamingJudge) Weight() float64                      { return m.config.Weight }
func (m *mockStreamingJudge) Criticality() loomv1.JudgeCriticality { return m.config.Criticality }
func (m *mockStreamingJudge) Criteria() []string                   { return []string{m.config.Criteria} }
func (m *mockStreamingJudge) Dimensions() []loomv1.JudgeDimension  { return m.config.Dimensions }
func (m *mockStreamingJudge) Config() *loomv1.JudgeConfig          { return m.config }
func (m *mockStreamingJudge) Evaluate(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
	if m.evaluateFunc != nil {
		return m.evaluateFunc(ctx, evalCtx)
	}
	return &loomv1.JudgeResult{
		JudgeId:   m.config.Id,
		JudgeName: m.config.Name,
		Verdict:   "PASS",
	}, nil
}

// TestEvaluateStream_SendsJudgeStartedAndCompleted verifies streaming sends progress for each judge
func TestEvaluateStream_SendsJudgeStartedAndCompleted(t *testing.T) {
	// Create orchestrator
	registry := NewRegistry()
	orch := NewOrchestrator(&Config{
		Registry:   registry,
		Aggregator: NewAggregator(nil),
		Tracer:     observability.NewNoOpTracer(),
		Logger:     zap.NewNop(),
	})

	// Register test judges
	judgeConfig1 := &loomv1.JudgeConfig{
		Id:              "quality-judge",
		Name:            "Quality Judge",
		MinPassingScore: 80,
		Criticality:     loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL,
		Type:            loomv1.JudgeType_JUDGE_TYPE_CUSTOM,
		Dimensions:      []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY},
	}

	judgeConfig2 := &loomv1.JudgeConfig{
		Id:              "cost-judge",
		Name:            "Cost Judge",
		MinPassingScore: 70,
		Criticality:     loomv1.JudgeCriticality_JUDGE_CRITICALITY_NON_CRITICAL,
		Type:            loomv1.JudgeType_JUDGE_TYPE_CUSTOM,
		Dimensions:      []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_COST},
	}

	mockJudge1 := &mockStreamingJudge{
		config: judgeConfig1,
		evaluateFunc: func(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
			time.Sleep(10 * time.Millisecond) // Simulate work
			return &loomv1.JudgeResult{
				JudgeId:      judgeConfig1.Id,
				JudgeName:    judgeConfig1.Name,
				Verdict:      "PASS",
				OverallScore: 85.0,
				JudgedAt:     timestamppb.Now(),
			}, nil
		},
	}

	mockJudge2 := &mockStreamingJudge{
		config: judgeConfig2,
		evaluateFunc: func(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
			time.Sleep(10 * time.Millisecond) // Simulate work
			return &loomv1.JudgeResult{
				JudgeId:      judgeConfig2.Id,
				JudgeName:    judgeConfig2.Name,
				Verdict:      "PASS",
				OverallScore: 75.0,
				JudgedAt:     timestamppb.Now(),
			}, nil
		},
	}

	err := registry.Register(mockJudge1)
	require.NoError(t, err)
	err = registry.Register(mockJudge2)
	require.NoError(t, err)

	// Create evaluation request
	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:   "test-agent",
			SessionId: "test-session",
			Prompt:    "Test prompt",
			Response:  "Test response",
		},
		JudgeIds:      []string{"quality-judge", "cost-judge"},
		Aggregation:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
		ExecutionMode: loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
	}

	// Create stream channel
	stream := make(chan *loomv1.EvaluateProgress, 10)

	// Run evaluation in goroutine
	var resp *loomv1.EvaluateResponse
	var evalErr error
	done := make(chan struct{})
	go func() {
		resp, evalErr = orch.EvaluateStream(context.Background(), req, stream)
		close(done)
		close(stream)
	}()

	// Collect progress messages
	var judgeStarted []*loomv1.JudgeStarted
	var judgeCompleted []*loomv1.JudgeCompleted
	var evalCompleted *loomv1.EvaluationCompleted

	for progress := range stream {
		switch p := progress.Progress.(type) {
		case *loomv1.EvaluateProgress_JudgeStarted:
			judgeStarted = append(judgeStarted, p.JudgeStarted)
		case *loomv1.EvaluateProgress_JudgeCompleted:
			judgeCompleted = append(judgeCompleted, p.JudgeCompleted)
		case *loomv1.EvaluateProgress_EvaluationCompleted:
			evalCompleted = p.EvaluationCompleted
		}
	}

	<-done

	// Verify no error
	require.NoError(t, evalErr)
	require.NotNil(t, resp)

	// Verify we got 2 JudgeStarted messages
	assert.Len(t, judgeStarted, 2, "Should receive JudgeStarted for each judge")

	// Verify we got 2 JudgeCompleted messages
	assert.Len(t, judgeCompleted, 2, "Should receive JudgeCompleted for each judge")

	// Verify JudgeCompleted messages have results
	for _, jc := range judgeCompleted {
		assert.NotNil(t, jc.Result, "JudgeCompleted should have result")
		assert.Greater(t, jc.DurationMs, int64(0), "Duration should be positive")
	}

	// Verify we got EvaluationCompleted
	require.NotNil(t, evalCompleted, "Should receive EvaluationCompleted")
	assert.NotNil(t, evalCompleted.FinalResult, "EvaluationCompleted should have final result")
	assert.Greater(t, evalCompleted.TotalDurationMs, int64(0), "Total duration should be positive")
}

// TestEvaluateStream_NonStreamingStillWorks verifies backward compatibility
func TestEvaluateStream_NonStreamingStillWorks(t *testing.T) {
	registry := NewRegistry()
	orch := NewOrchestrator(&Config{
		Registry:   registry,
		Aggregator: NewAggregator(nil),
		Tracer:     observability.NewNoOpTracer(),
		Logger:     zap.NewNop(),
	})

	// Register test judge
	judgeConfig := &loomv1.JudgeConfig{
		Id:              "test-judge",
		Name:            "Test Judge",
		MinPassingScore: 80,
		Criticality:     loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL,
		Type:            loomv1.JudgeType_JUDGE_TYPE_CUSTOM,
	}

	mockJudge := &mockStreamingJudge{
		config: judgeConfig,
		evaluateFunc: func(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
			return &loomv1.JudgeResult{
				JudgeId:      judgeConfig.Id,
				JudgeName:    judgeConfig.Name,
				Verdict:      "PASS",
				OverallScore: 90.0,
			}, nil
		},
	}

	err := registry.Register(mockJudge)
	require.NoError(t, err)

	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:  "test-agent",
			Prompt:   "Test prompt",
			Response: "Test response",
		},
		JudgeIds:      []string{"test-judge"},
		Aggregation:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
		ExecutionMode: loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
	}

	// Call non-streaming Evaluate (backward compatible)
	resp, err := orch.Evaluate(context.Background(), req)

	// Verify it still works
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Debug: log the response
	t.Logf("Response: Passed=%v, FinalScore=%.2f, Verdicts=%d", resp.Passed, resp.FinalScore, len(resp.Verdicts))
	if len(resp.Verdicts) > 0 {
		t.Logf("First verdict: %s, Score=%.2f", resp.Verdicts[0].Verdict, resp.Verdicts[0].OverallScore)
	}

	// Verify evaluation passed
	assert.Len(t, resp.Verdicts, 1, "Should have 1 verdict")
	if len(resp.Verdicts) > 0 {
		assert.Equal(t, "PASS", resp.Verdicts[0].Verdict, "Judge should pass")
	}
}

// TestEvaluateStream_ConcurrentStreamingEvaluations verifies race-free concurrent streaming
func TestEvaluateStream_ConcurrentStreamingEvaluations(t *testing.T) {
	registry := NewRegistry()
	orch := NewOrchestrator(&Config{
		Registry:   registry,
		Aggregator: NewAggregator(nil),
		Tracer:     observability.NewNoOpTracer(),
		Logger:     zap.NewNop(),
	})

	// Register test judge
	judgeConfig := &loomv1.JudgeConfig{
		Id:              "concurrent-judge",
		Name:            "Concurrent Judge",
		MinPassingScore: 80,
		Criticality:     loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL,
		Type:            loomv1.JudgeType_JUDGE_TYPE_CUSTOM,
	}

	mockJudge := &mockStreamingJudge{
		config: judgeConfig,
		evaluateFunc: func(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
			time.Sleep(5 * time.Millisecond) // Simulate work
			return &loomv1.JudgeResult{
				JudgeId:      judgeConfig.Id,
				JudgeName:    judgeConfig.Name,
				Verdict:      "PASS",
				OverallScore: 85.0,
			}, nil
		},
	}

	err := registry.Register(mockJudge)
	require.NoError(t, err)

	// Run 10 concurrent streaming evaluations
	numEvaluations := 10
	done := make(chan struct{}, numEvaluations)

	for i := 0; i < numEvaluations; i++ {
		go func() {
			defer func() { done <- struct{}{} }()

			req := &loomv1.EvaluateRequest{
				Context: &loomv1.EvaluationContext{
					AgentId:  "test-agent",
					Prompt:   "Test prompt",
					Response: "Test response",
				},
				JudgeIds:      []string{"concurrent-judge"},
				Aggregation:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
				ExecutionMode: loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
			}

			stream := make(chan *loomv1.EvaluateProgress, 10)

			// Run evaluation
			go func() {
				_, err := orch.EvaluateStream(context.Background(), req, stream)
				assert.NoError(t, err)
				close(stream)
			}()

			// Consume stream
			for range stream {
				// Just consume messages
			}
		}()
	}

	// Wait for all evaluations
	for i := 0; i < numEvaluations; i++ {
		<-done
	}
}

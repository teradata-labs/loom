// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package teleprompter

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"google.golang.org/grpc"
)

// ============================================================================
// Mock Learning Agent Client
// ============================================================================

type MockLearningAgentClient struct {
	applyFunc    func(ctx context.Context, req *loomv1.ApplyImprovementRequest) (*loomv1.ApplyImprovementResponse, error)
	rollbackFunc func(ctx context.Context, req *loomv1.RollbackImprovementRequest) (*loomv1.RollbackImprovementResponse, error)

	appliedImprovements    []string // Track applied improvement IDs
	rolledBackImprovements []string // Track rolled back improvement IDs
	mu                     sync.Mutex
}

func (m *MockLearningAgentClient) ApplyImprovement(ctx context.Context, req *loomv1.ApplyImprovementRequest, opts ...grpc.CallOption) (*loomv1.ApplyImprovementResponse, error) {
	if m.applyFunc != nil {
		return m.applyFunc(ctx, req)
	}

	m.mu.Lock()
	m.appliedImprovements = append(m.appliedImprovements, req.ImprovementId)
	m.mu.Unlock()

	return &loomv1.ApplyImprovementResponse{
		Success: true,
		Message: "Applied successfully",
		Improvement: &loomv1.Improvement{
			Id:     req.ImprovementId,
			Status: loomv1.ImprovementStatus_IMPROVEMENT_APPLIED,
		},
	}, nil
}

func (m *MockLearningAgentClient) RollbackImprovement(ctx context.Context, req *loomv1.RollbackImprovementRequest, opts ...grpc.CallOption) (*loomv1.RollbackImprovementResponse, error) {
	if m.rollbackFunc != nil {
		return m.rollbackFunc(ctx, req)
	}

	m.mu.Lock()
	m.rolledBackImprovements = append(m.rolledBackImprovements, req.ImprovementId)
	m.mu.Unlock()

	return &loomv1.RollbackImprovementResponse{
		Success:         true,
		Message:         "Rolled back successfully",
		RestoredVersion: "v1.0.0",
	}, nil
}

// Unused methods (stubbed for interface compliance)
func (m *MockLearningAgentClient) AnalyzePatternEffectiveness(ctx context.Context, req *loomv1.AnalyzePatternEffectivenessRequest, opts ...grpc.CallOption) (*loomv1.PatternAnalysisResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockLearningAgentClient) GenerateImprovements(ctx context.Context, req *loomv1.GenerateImprovementsRequest, opts ...grpc.CallOption) (*loomv1.ImprovementsResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockLearningAgentClient) GetImprovementHistory(ctx context.Context, req *loomv1.GetImprovementHistoryRequest, opts ...grpc.CallOption) (*loomv1.ImprovementHistoryResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockLearningAgentClient) StreamPatternMetrics(ctx context.Context, req *loomv1.StreamPatternMetricsRequest, opts ...grpc.CallOption) (loomv1.LearningAgentService_StreamPatternMetricsClient, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockLearningAgentClient) TunePatterns(ctx context.Context, req *loomv1.TunePatternsRequest, opts ...grpc.CallOption) (*loomv1.TunePatternsResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

// ============================================================================
// Tests: Auto-Apply Mode Defaults
// ============================================================================

func TestNewJudgeGradientEngine_AutoApplyDefaults(t *testing.T) {
	t.Run("defaults to manual mode", func(t *testing.T) {
		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator: &MockOrchestrator{},
			JudgeIDs:     []string{"test-judge"},
		})

		require.NoError(t, err)
		assert.Equal(t, loomv1.AutoApplyMode_AUTO_APPLY_MODE_MANUAL, engine.autoApplyMode)
	})

	t.Run("respects explicit auto-apply mode", func(t *testing.T) {
		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:  &MockOrchestrator{},
			JudgeIDs:      []string{"test-judge"},
			AutoApplyMode: loomv1.AutoApplyMode_AUTO_APPLY_MODE_VALIDATED,
		})

		require.NoError(t, err)
		assert.Equal(t, loomv1.AutoApplyMode_AUTO_APPLY_MODE_VALIDATED, engine.autoApplyMode)
	})

	t.Run("stores validation config", func(t *testing.T) {
		validationConfig := &loomv1.ValidationConfig{
			ValidationSet: []*loomv1.Example{
				{Inputs: map[string]string{"query": "test"}},
			},
			MinScoreDelta: 5.0,
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:     &MockOrchestrator{},
			JudgeIDs:         []string{"test-judge"},
			ValidationConfig: validationConfig,
		})

		require.NoError(t, err)
		assert.Equal(t, validationConfig, engine.validationConfig)
	})
}

// ============================================================================
// Tests: Manual Mode (Baseline)
// ============================================================================

func TestAutoApply_ManualMode(t *testing.T) {
	t.Run("returns improvements without applying", func(t *testing.T) {
		mockClient := &MockLearningAgentClient{}
		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:        &MockOrchestrator{},
			JudgeIDs:            []string{"test-judge"},
			AutoApplyMode:       loomv1.AutoApplyMode_AUTO_APPLY_MODE_MANUAL,
			LearningAgentClient: mockClient,
			Tracer:              observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{
				Name:  "test",
				Value: "value",
				Gradient: `[Dimension Scores]
safety: 60/100 ⚠️

Overall Score: 60.0/100 ✗`,
			},
		}

		improvements, err := engine.Step(context.Background(), variables)
		require.NoError(t, err)
		assert.NotEmpty(t, improvements)

		// Should not have called learning agent
		mockClient.mu.Lock()
		assert.Empty(t, mockClient.appliedImprovements)
		mockClient.mu.Unlock()
	})
}

// ============================================================================
// Tests: Validated Mode - Success Cases
// ============================================================================

func TestAutoApply_ValidatedMode_Success(t *testing.T) {
	t.Run("applies improvement when validation passes", func(t *testing.T) {
		mockClient := &MockLearningAgentClient{}

		// Mock orchestrator that returns improving scores
		callCount := 0
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				callCount++
				score := 70.0 // Baseline
				if callCount > 1 {
					score = 85.0 // After improvement
				}
				return &loomv1.EvaluateResponse{
					Passed:     score >= 80.0,
					FinalScore: score,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "test-judge",
							OverallScore: score,
							DimensionScores: map[string]float64{
								"quality": score,
							},
						},
					},
				}, nil
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:  mockOrch,
			JudgeIDs:      []string{"test-judge"},
			AutoApplyMode: loomv1.AutoApplyMode_AUTO_APPLY_MODE_VALIDATED,
			ValidationConfig: &loomv1.ValidationConfig{
				ValidationSet: []*loomv1.Example{
					{
						Inputs:  map[string]string{"query": "test"},
						Outputs: map[string]string{"answer": "expected"},
					},
				},
				MinScoreDelta:     5.0,
				RollbackOnFailure: true,
			},
			LearningAgentClient: mockClient,
			AgentID:             "test-agent",
			Tracer:              observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{
				Name:  "test",
				Value: "value",
				Gradient: `[Dimension Scores]
quality: 70/100 ⚠️

Overall Score: 70.0/100 ✗`,
			},
		}

		improvements, err := engine.Step(context.Background(), variables)
		require.NoError(t, err)
		assert.NotEmpty(t, improvements)

		// Should have applied improvement
		mockClient.mu.Lock()
		assert.Len(t, mockClient.appliedImprovements, 1)
		assert.Empty(t, mockClient.rolledBackImprovements)
		mockClient.mu.Unlock()
	})

	t.Run("applies multiple improvements sequentially", func(t *testing.T) {
		mockClient := &MockLearningAgentClient{}

		callCount := 0
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				callCount++
				// Baseline: 70, After imp1: 80, After imp2: 90
				score := 70.0 + float64(callCount-1)*10.0
				return &loomv1.EvaluateResponse{
					Passed:     score >= 80.0,
					FinalScore: score,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "test-judge",
							OverallScore: score,
							DimensionScores: map[string]float64{
								"quality": score,
							},
						},
					},
				}, nil
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:  mockOrch,
			JudgeIDs:      []string{"test-judge"},
			AutoApplyMode: loomv1.AutoApplyMode_AUTO_APPLY_MODE_VALIDATED,
			ValidationConfig: &loomv1.ValidationConfig{
				ValidationSet: []*loomv1.Example{
					{Inputs: map[string]string{"query": "test"}, Outputs: map[string]string{"answer": "expected"}},
				},
				MinScoreDelta:     5.0,
				RollbackOnFailure: true,
			},
			LearningAgentClient: mockClient,
			AgentID:             "test-agent",
			Tracer:              observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{
				Name:  "test",
				Value: "value",
				Gradient: `[Dimension Scores]
quality: 70/100 ⚠️
safety: 65/100 ⚠️

Overall Score: 67.5/100 ✗`,
			},
		}

		improvements, err := engine.Step(context.Background(), variables)
		require.NoError(t, err)
		assert.NotEmpty(t, improvements)

		// Should have applied both improvements
		mockClient.mu.Lock()
		assert.Len(t, mockClient.appliedImprovements, 2)
		mockClient.mu.Unlock()
	})
}

// ============================================================================
// Tests: Validated Mode - Rollback Cases
// ============================================================================

func TestAutoApply_ValidatedMode_Rollback(t *testing.T) {
	t.Run("rolls back when improvement degrades score", func(t *testing.T) {
		mockClient := &MockLearningAgentClient{}

		callCount := 0
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				callCount++
				score := 80.0 // Baseline
				if callCount > 1 {
					score = 65.0 // After improvement (regression!)
				}
				return &loomv1.EvaluateResponse{
					Passed:     score >= 80.0,
					FinalScore: score,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "test-judge",
							OverallScore: score,
							DimensionScores: map[string]float64{
								"quality": score,
							},
						},
					},
				}, nil
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:  mockOrch,
			JudgeIDs:      []string{"test-judge"},
			AutoApplyMode: loomv1.AutoApplyMode_AUTO_APPLY_MODE_VALIDATED,
			ValidationConfig: &loomv1.ValidationConfig{
				ValidationSet: []*loomv1.Example{
					{Inputs: map[string]string{"query": "test"}, Outputs: map[string]string{"answer": "expected"}},
				},
				MinScoreDelta:     0.0, // No regression allowed
				RollbackOnFailure: true,
			},
			LearningAgentClient: mockClient,
			AgentID:             "test-agent",
			Tracer:              observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{
				Name:  "test",
				Value: "value",
				Gradient: `[Dimension Scores]
quality: 70/100 ⚠️

Overall Score: 70.0/100 ✗`,
			},
		}

		improvements, err := engine.Step(context.Background(), variables)
		require.NoError(t, err)
		assert.Empty(t, improvements) // No improvements accepted

		// Should have applied then rolled back
		mockClient.mu.Lock()
		assert.Len(t, mockClient.appliedImprovements, 1)
		assert.Len(t, mockClient.rolledBackImprovements, 1)
		mockClient.mu.Unlock()
	})

	t.Run("rolls back when improvement below threshold", func(t *testing.T) {
		mockClient := &MockLearningAgentClient{}

		callCount := 0
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				callCount++
				score := 70.0 // Baseline
				if callCount > 1 {
					score = 72.0 // Small improvement (below 5.0 threshold)
				}
				return &loomv1.EvaluateResponse{
					Passed:     score >= 70.0,
					FinalScore: score,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "test-judge",
							OverallScore: score,
							DimensionScores: map[string]float64{
								"quality": score,
							},
						},
					},
				}, nil
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:  mockOrch,
			JudgeIDs:      []string{"test-judge"},
			AutoApplyMode: loomv1.AutoApplyMode_AUTO_APPLY_MODE_VALIDATED,
			ValidationConfig: &loomv1.ValidationConfig{
				ValidationSet: []*loomv1.Example{
					{Inputs: map[string]string{"query": "test"}, Outputs: map[string]string{"answer": "expected"}},
				},
				MinScoreDelta:     5.0, // Require 5% improvement
				RollbackOnFailure: true,
			},
			LearningAgentClient: mockClient,
			AgentID:             "test-agent",
			Tracer:              observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{
				Name:  "test",
				Value: "value",
				Gradient: `[Dimension Scores]
quality: 70/100 ⚠️

Overall Score: 70.0/100 ✗`,
			},
		}

		improvements, err := engine.Step(context.Background(), variables)
		require.NoError(t, err)
		assert.Empty(t, improvements)

		// Should have rolled back
		mockClient.mu.Lock()
		assert.Len(t, mockClient.appliedImprovements, 1)
		assert.Len(t, mockClient.rolledBackImprovements, 1)
		mockClient.mu.Unlock()
	})
}

// ============================================================================
// Tests: Validated Mode - Error Handling
// ============================================================================

func TestAutoApply_ValidatedMode_Errors(t *testing.T) {
	t.Run("requires validation config", func(t *testing.T) {
		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:        &MockOrchestrator{},
			JudgeIDs:            []string{"test-judge"},
			AutoApplyMode:       loomv1.AutoApplyMode_AUTO_APPLY_MODE_VALIDATED,
			ValidationConfig:    nil, // Missing!
			LearningAgentClient: &MockLearningAgentClient{},
			AgentID:             "test-agent",
			Tracer:              observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{Name: "test", Value: "value", Gradient: "[Dimension Scores]\nsafety: 60/100\n"},
		}

		_, err = engine.Step(context.Background(), variables)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "validation config required")
	})

	t.Run("requires learning agent client", func(t *testing.T) {
		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:  &MockOrchestrator{},
			JudgeIDs:      []string{"test-judge"},
			AutoApplyMode: loomv1.AutoApplyMode_AUTO_APPLY_MODE_VALIDATED,
			ValidationConfig: &loomv1.ValidationConfig{
				ValidationSet: []*loomv1.Example{{Inputs: map[string]string{"query": "test"}}},
			},
			LearningAgentClient: nil, // Missing!
			AgentID:             "test-agent",
			Tracer:              observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{Name: "test", Value: "value", Gradient: "[Dimension Scores]\nsafety: 60/100\n"},
		}

		_, err = engine.Step(context.Background(), variables)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "learning agent client required")
	})

	t.Run("requires agent ID", func(t *testing.T) {
		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:  &MockOrchestrator{},
			JudgeIDs:      []string{"test-judge"},
			AutoApplyMode: loomv1.AutoApplyMode_AUTO_APPLY_MODE_VALIDATED,
			ValidationConfig: &loomv1.ValidationConfig{
				ValidationSet: []*loomv1.Example{{Inputs: map[string]string{"query": "test"}}},
			},
			LearningAgentClient: &MockLearningAgentClient{},
			AgentID:             "", // Missing!
			Tracer:              observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{Name: "test", Value: "value", Gradient: "[Dimension Scores]\nsafety: 60/100\n"},
		}

		_, err = engine.Step(context.Background(), variables)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "agent ID required")
	})

	t.Run("handles apply failure gracefully", func(t *testing.T) {
		mockClient := &MockLearningAgentClient{
			applyFunc: func(ctx context.Context, req *loomv1.ApplyImprovementRequest) (*loomv1.ApplyImprovementResponse, error) {
				return nil, fmt.Errorf("apply failed")
			},
		}

		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				return &loomv1.EvaluateResponse{
					Passed:     false,
					FinalScore: 70.0,
					Verdicts:   []*loomv1.JudgeResult{{JudgeId: "test", OverallScore: 70.0, DimensionScores: map[string]float64{"quality": 70.0}}},
				}, nil
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:  mockOrch,
			JudgeIDs:      []string{"test-judge"},
			AutoApplyMode: loomv1.AutoApplyMode_AUTO_APPLY_MODE_VALIDATED,
			ValidationConfig: &loomv1.ValidationConfig{
				ValidationSet: []*loomv1.Example{{Inputs: map[string]string{"query": "test"}}},
			},
			LearningAgentClient: mockClient,
			AgentID:             "test-agent",
			Tracer:              observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{Name: "test", Value: "value", Gradient: "[Dimension Scores]\nquality: 70/100\n"},
		}

		improvements, err := engine.Step(context.Background(), variables)
		require.NoError(t, err)
		assert.Empty(t, improvements) // Should skip failed improvement
	})

	t.Run("handles validation failure with rollback", func(t *testing.T) {
		mockClient := &MockLearningAgentClient{}

		callCount := 0
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				callCount++
				if callCount == 1 {
					// Baseline
					return &loomv1.EvaluateResponse{Passed: false, FinalScore: 70.0, Verdicts: []*loomv1.JudgeResult{{JudgeId: "test", OverallScore: 70.0, DimensionScores: map[string]float64{"quality": 70.0}}}}, nil
				}
				// Validation fails
				return nil, fmt.Errorf("validation failed")
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:  mockOrch,
			JudgeIDs:      []string{"test-judge"},
			AutoApplyMode: loomv1.AutoApplyMode_AUTO_APPLY_MODE_VALIDATED,
			ValidationConfig: &loomv1.ValidationConfig{
				ValidationSet:     []*loomv1.Example{{Inputs: map[string]string{"query": "test"}}},
				RollbackOnFailure: true,
			},
			LearningAgentClient: mockClient,
			AgentID:             "test-agent",
			Tracer:              observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{Name: "test", Value: "value", Gradient: "[Dimension Scores]\nquality: 70/100\n"},
		}

		improvements, err := engine.Step(context.Background(), variables)
		require.NoError(t, err)
		assert.Empty(t, improvements)

		// Should have rolled back after validation failure
		mockClient.mu.Lock()
		assert.Len(t, mockClient.appliedImprovements, 1)
		assert.Len(t, mockClient.rolledBackImprovements, 1)
		mockClient.mu.Unlock()
	})
}

// ============================================================================
// Tests: Context Cancellation
// ============================================================================

func TestAutoApply_ContextCancellation(t *testing.T) {
	t.Run("respects context cancellation during validation", func(t *testing.T) {
		mockClient := &MockLearningAgentClient{}

		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				// Simulate slow validation
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(100 * time.Millisecond):
					return &loomv1.EvaluateResponse{Passed: true, FinalScore: 85.0, Verdicts: []*loomv1.JudgeResult{{JudgeId: "test", OverallScore: 85.0}}}, nil
				}
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:  mockOrch,
			JudgeIDs:      []string{"test-judge"},
			AutoApplyMode: loomv1.AutoApplyMode_AUTO_APPLY_MODE_VALIDATED,
			ValidationConfig: &loomv1.ValidationConfig{
				ValidationSet: []*loomv1.Example{{Inputs: map[string]string{"query": "test"}}},
			},
			LearningAgentClient: mockClient,
			AgentID:             "test-agent",
			Tracer:              observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{Name: "test", Value: "value", Gradient: "[Dimension Scores]\nquality: 70/100\n"},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		_, err = engine.Step(ctx, variables)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "baseline validation failed")
	})
}

// ============================================================================
// Tests: Race Detector (Concurrent Operations)
// ============================================================================

func TestAutoApply_RaceDetection(t *testing.T) {
	t.Run("concurrent auto-apply operations are safe", func(t *testing.T) {
		mockClient := &MockLearningAgentClient{}

		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				return &loomv1.EvaluateResponse{
					Passed:     true,
					FinalScore: 85.0,
					Verdicts: []*loomv1.JudgeResult{
						{JudgeId: "test", OverallScore: 85.0, DimensionScores: map[string]float64{"quality": 85.0}},
					},
				}, nil
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:  mockOrch,
			JudgeIDs:      []string{"test-judge"},
			AutoApplyMode: loomv1.AutoApplyMode_AUTO_APPLY_MODE_VALIDATED,
			ValidationConfig: &loomv1.ValidationConfig{
				ValidationSet: []*loomv1.Example{{Inputs: map[string]string{"query": "test"}}},
				MinScoreDelta: 0.0,
			},
			LearningAgentClient: mockClient,
			AgentID:             "test-agent",
			Tracer:              observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		// Run multiple Step() calls concurrently
		const numGoroutines = 10
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()

				variables := []*Variable{
					{
						Name:  fmt.Sprintf("var-%d", id),
						Value: "value",
						Gradient: `[Dimension Scores]
quality: 70/100

Overall Score: 70.0/100 ✗`,
					},
				}

				_, err := engine.Step(context.Background(), variables)
				assert.NoError(t, err)
			}(i)
		}

		wg.Wait()

		// Verify no race conditions occurred
		mockClient.mu.Lock()
		appliedCount := len(mockClient.appliedImprovements)
		mockClient.mu.Unlock()

		assert.Equal(t, numGoroutines, appliedCount)
	})
}

// ============================================================================
// Tests: Integration - Backward + Step with Auto-Apply
// ============================================================================

func TestAutoApply_Integration(t *testing.T) {
	t.Run("complete backward-step-apply cycle", func(t *testing.T) {
		mockClient := &MockLearningAgentClient{}

		callCount := 0
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				callCount++

				// First call: Backward pass (low scores)
				// Second call: Baseline validation (still low)
				// Third+ calls: Incremental improvement per improvement applied
				score := 65.0
				if callCount == 3 {
					score = 80.0 // First improvement: +15
				} else if callCount >= 4 {
					score = 95.0 // Second improvement: +15 more
				}

				return &loomv1.EvaluateResponse{
					Passed:     score >= 80.0,
					FinalScore: score,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "quality-judge",
							OverallScore: score,
							DimensionScores: map[string]float64{
								"quality": score,
								"safety":  score,
							},
							Reasoning: "Test reasoning",
						},
					},
				}, nil
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:  mockOrch,
			JudgeIDs:      []string{"quality-judge"},
			AutoApplyMode: loomv1.AutoApplyMode_AUTO_APPLY_MODE_VALIDATED,
			ValidationConfig: &loomv1.ValidationConfig{
				ValidationSet: []*loomv1.Example{
					{Inputs: map[string]string{"query": "test"}, Outputs: map[string]string{"answer": "expected"}},
				},
				MinScoreDelta:     10.0, // Require 10% improvement
				RollbackOnFailure: true,
			},
			LearningAgentClient: mockClient,
			AgentID:             "test-agent",
			Tracer:              observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		// 1. Backward pass
		variables := []*Variable{{Name: "system_prompt", Value: "You are a helpful assistant"}}
		example := &loomv1.Example{
			Inputs:  map[string]string{"query": "test query"},
			Outputs: map[string]string{"answer": "expected answer"},
		}
		result := &ExecutionResult{
			Outputs: map[string]string{"answer": "actual answer"},
			TraceID: "test-trace",
			Success: true,
		}

		err = engine.Backward(context.Background(), example, result, variables)
		require.NoError(t, err)
		assert.NotEmpty(t, variables[0].Gradient)
		assert.Contains(t, variables[0].Gradient, "quality: 65/100")

		// 2. Step with auto-apply
		improvements, err := engine.Step(context.Background(), variables)
		require.NoError(t, err)

		// Should have applied both improvements (quality and safety)
		assert.Len(t, improvements, 2, "Expected both improvements to be applied")

		mockClient.mu.Lock()
		assert.Len(t, mockClient.appliedImprovements, 2, "Both improvements should be applied")
		assert.Empty(t, mockClient.rolledBackImprovements, "No rollbacks should occur")
		mockClient.mu.Unlock()
	})
}

// ============================================================================
// Tests: DryRun and Autonomous Modes (Placeholder)
// ============================================================================

func TestAutoApply_DryRunMode(t *testing.T) {
	t.Run("dry-run mode not yet implemented", func(t *testing.T) {
		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:  &MockOrchestrator{},
			JudgeIDs:      []string{"test-judge"},
			AutoApplyMode: loomv1.AutoApplyMode_AUTO_APPLY_MODE_DRY_RUN,
			Tracer:        observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{Name: "test", Value: "value", Gradient: "[Dimension Scores]\nsafety: 60/100\n"},
		}

		improvements, err := engine.Step(context.Background(), variables)
		require.NoError(t, err)
		assert.NotEmpty(t, improvements) // Returns improvements but doesn't apply
	})
}

func TestAutoApply_AutonomousMode(t *testing.T) {
	t.Run("autonomous mode not yet implemented", func(t *testing.T) {
		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:  &MockOrchestrator{},
			JudgeIDs:      []string{"test-judge"},
			AutoApplyMode: loomv1.AutoApplyMode_AUTO_APPLY_MODE_AUTONOMOUS,
			Tracer:        observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{Name: "test", Value: "value", Gradient: "[Dimension Scores]\nsafety: 60/100\n"},
		}

		improvements, err := engine.Step(context.Background(), variables)
		require.NoError(t, err)
		assert.NotEmpty(t, improvements) // Returns improvements but doesn't apply
	})
}

// ============================================================================
// Tests: Validation Logic
// ============================================================================

func TestValidate(t *testing.T) {
	t.Run("requires non-empty validation set", func(t *testing.T) {
		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator: &MockOrchestrator{},
			JudgeIDs:     []string{"test-judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		_, err = engine.validate(context.Background(), []*loomv1.Example{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "validation set is empty")
	})

	t.Run("computes average score across examples", func(t *testing.T) {
		callCount := 0
		scores := []float64{70.0, 80.0, 90.0}
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				score := scores[callCount%len(scores)]
				callCount++
				return &loomv1.EvaluateResponse{
					Passed:     score >= 75.0,
					FinalScore: score,
					Verdicts: []*loomv1.JudgeResult{
						{JudgeId: "test", OverallScore: score, DimensionScores: map[string]float64{"quality": score}},
					},
				}, nil
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator: mockOrch,
			JudgeIDs:     []string{"test-judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		engine.validationConfig = &loomv1.ValidationConfig{}

		validationSet := []*loomv1.Example{
			{Inputs: map[string]string{"query": "test1"}, Outputs: map[string]string{"answer": "ans1"}},
			{Inputs: map[string]string{"query": "test2"}, Outputs: map[string]string{"answer": "ans2"}},
			{Inputs: map[string]string{"query": "test3"}, Outputs: map[string]string{"answer": "ans3"}},
		}

		result, err := engine.validate(context.Background(), validationSet)
		require.NoError(t, err)

		// Average of 70, 80, 90 = 80
		assert.InDelta(t, 80.0, result.BaseScore, 0.1)
		assert.InDelta(t, 80.0, result.NewScore, 0.1)
	})

	t.Run("counts failed tests", func(t *testing.T) {
		callCount := 0
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				callCount++
				passed := callCount%2 == 0 // Every other test passes
				score := 50.0
				if passed {
					score = 90.0
				}
				return &loomv1.EvaluateResponse{
					Passed:     passed,
					FinalScore: score,
					Verdicts: []*loomv1.JudgeResult{
						{JudgeId: "test", OverallScore: score},
					},
				}, nil
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator: mockOrch,
			JudgeIDs:     []string{"test-judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		engine.validationConfig = &loomv1.ValidationConfig{}

		validationSet := []*loomv1.Example{
			{Inputs: map[string]string{"query": "test1"}},
			{Inputs: map[string]string{"query": "test2"}},
			{Inputs: map[string]string{"query": "test3"}},
			{Inputs: map[string]string{"query": "test4"}},
		}

		result, err := engine.validate(context.Background(), validationSet)
		require.NoError(t, err)

		assert.Equal(t, 2, result.FailedTests) // 2 out of 4 failed
	})
}

func TestRollback(t *testing.T) {
	t.Run("requires learning agent client", func(t *testing.T) {
		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator: &MockOrchestrator{},
			JudgeIDs:     []string{"test-judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		improvement := &loomv1.Improvement{Id: "test-improvement"}
		err = engine.rollback(context.Background(), improvement)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "learning agent client required")
	})

	t.Run("calls rollback RPC successfully", func(t *testing.T) {
		mockClient := &MockLearningAgentClient{}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:        &MockOrchestrator{},
			JudgeIDs:            []string{"test-judge"},
			LearningAgentClient: mockClient,
			Tracer:              observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		improvement := &loomv1.Improvement{
			Id:          "test-improvement",
			Description: "Test improvement",
		}

		err = engine.rollback(context.Background(), improvement)
		require.NoError(t, err)

		mockClient.mu.Lock()
		assert.Contains(t, mockClient.rolledBackImprovements, "test-improvement")
		mockClient.mu.Unlock()
	})

	t.Run("handles rollback RPC failure", func(t *testing.T) {
		mockClient := &MockLearningAgentClient{
			rollbackFunc: func(ctx context.Context, req *loomv1.RollbackImprovementRequest) (*loomv1.RollbackImprovementResponse, error) {
				return nil, fmt.Errorf("rollback RPC failed")
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:        &MockOrchestrator{},
			JudgeIDs:            []string{"test-judge"},
			LearningAgentClient: mockClient,
			Tracer:              observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		improvement := &loomv1.Improvement{Id: "test-improvement"}
		err = engine.rollback(context.Background(), improvement)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "rollback RPC failed")
	})

	t.Run("handles rollback rejection", func(t *testing.T) {
		mockClient := &MockLearningAgentClient{
			rollbackFunc: func(ctx context.Context, req *loomv1.RollbackImprovementRequest) (*loomv1.RollbackImprovementResponse, error) {
				return &loomv1.RollbackImprovementResponse{
					Success: false,
					Message: "Rollback rejected: no checkpoint found",
				}, nil
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator:        &MockOrchestrator{},
			JudgeIDs:            []string{"test-judge"},
			LearningAgentClient: mockClient,
			Tracer:              observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		improvement := &loomv1.Improvement{Id: "test-improvement"}
		err = engine.rollback(context.Background(), improvement)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "rollback rejected")
	})
}

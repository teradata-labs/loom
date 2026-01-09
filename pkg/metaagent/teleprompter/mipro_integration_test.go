// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package teleprompter

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// MockMemoryWithCallback is a memory implementation that supports custom update callbacks
type MockMemoryWithCallback struct {
	prompts        map[string]string
	demonstrations []*loomv1.Demonstration
	updateFunc     func(map[string]string, []*loomv1.Demonstration) error
}

func (m *MockMemoryWithCallback) UpdateLearnedLayer(
	optimizedPrompts map[string]string,
	demonstrations []*loomv1.Demonstration,
) error {
	m.prompts = optimizedPrompts
	m.demonstrations = demonstrations
	if m.updateFunc != nil {
		return m.updateFunc(optimizedPrompts, demonstrations)
	}
	return nil
}

func (m *MockMemoryWithCallback) GetLearnedLayer() (map[string]string, []*loomv1.Demonstration, error) {
	return m.prompts, m.demonstrations, nil
}

func (m *MockMemoryWithCallback) GetLearnedVersion() string {
	return "test-version"
}

// MockAgentWithMemory is a mock agent that supports custom memory
type MockAgentWithMemory struct {
	id      string
	memory  Memory
	runFunc func(ctx context.Context, inputs map[string]string) (*ExecutionResult, error)
}

func (m *MockAgentWithMemory) Run(ctx context.Context, inputs map[string]string) (*ExecutionResult, error) {
	if m.runFunc != nil {
		return m.runFunc(ctx, inputs)
	}
	return &ExecutionResult{
		Inputs:  inputs,
		Outputs: map[string]string{"answer": "default answer"},
		TraceID: "trace-123",
		Success: true,
	}, nil
}

func (m *MockAgentWithMemory) Clone() Agent {
	return &MockAgentWithMemory{
		id:      m.id,
		memory:  m.memory,
		runFunc: m.runFunc,
	}
}

func (m *MockAgentWithMemory) GetMemory() Memory {
	if m.memory == nil {
		m.memory = &MockMemoryWithCallback{}
	}
	return m.memory
}

func (m *MockAgentWithMemory) GetID() string {
	return m.id
}

// TestMIPRO_WithMultiJudgeMetric demonstrates Phase 4 integration:
// Multi-dimensional instruction optimization with MIPRO
func TestMIPRO_WithMultiJudgeMetric(t *testing.T) {
	t.Run("successful instruction optimization with dimension-aware selection", func(t *testing.T) {
		// Track which instruction is currently being evaluated
		var currentInstruction string

		// Create mock orchestrator that returns dimension scores based on instruction quality
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				// Use the current instruction being evaluated
				instruction := currentInstruction

				var qualityScore, costScore float64
				// Simulate different instruction quality levels
				if instruction == "high-quality-instruction" {
					qualityScore = 95.0
					costScore = 60.0 // Expensive
				} else if instruction == "balanced-instruction" {
					qualityScore = 80.0
					costScore = 80.0 // Balanced
				} else if instruction == "low-cost-instruction" {
					qualityScore = 70.0
					costScore = 95.0 // Cheap
				} else {
					qualityScore = 50.0
					costScore = 50.0
				}

				avgScore := (qualityScore + costScore) / 2.0

				return &loomv1.EvaluateResponse{
					Passed:     avgScore >= 70.0,
					FinalScore: avgScore,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "quality-judge",
							JudgeName:    "Quality Judge",
							Verdict:      "PASS",
							OverallScore: avgScore,
							DimensionScores: map[string]float64{
								"quality": qualityScore,
								"cost":    costScore,
							},
							Reasoning: fmt.Sprintf("Quality: %.0f%%, Cost: %.0f%%", qualityScore, costScore),
						},
					},
				}, nil
			},
		}

		// Create MultiJudgeMetric
		metric, err := NewMultiJudgeMetric(&MultiJudgeMetricConfig{
			Orchestrator: mockOrch,
			JudgeIDs:     []string{"quality-judge"},
			Aggregation:  loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		// Create mock agent with memory that tracks instruction updates
		agent := &MockAgentWithMemory{
			id: "test-agent",
			runFunc: func(ctx context.Context, inputs map[string]string) (*ExecutionResult, error) {
				return &ExecutionResult{
					Inputs:  inputs,
					Outputs: map[string]string{"answer": "test answer"},
					TraceID: "trace-" + inputs["query"],
					Success: true,
				}, nil
			},
			memory: &MockMemoryWithCallback{
				updateFunc: func(prompts map[string]string, demos []*loomv1.Demonstration) error {
					if systemPrompt, ok := prompts["system"]; ok {
						currentInstruction = systemPrompt
					}
					return nil
				},
			},
		}

		// Create trainset
		trainset := []*loomv1.Example{
			{
				Id:      "ex1",
				Inputs:  map[string]string{"query": "test query 1"},
				Outputs: map[string]string{"answer": "expected answer 1"},
			},
			{
				Id:      "ex2",
				Inputs:  map[string]string{"query": "test query 2"},
				Outputs: map[string]string{"answer": "expected answer 2"},
			},
		}

		// Create MIPRO teleprompter
		registry := NewRegistry()
		mipro := NewMIPRO(observability.NewNoOpTracer(), registry, nil)

		// Instruction candidates to evaluate
		instructionCandidates := []string{
			"high-quality-instruction",
			"balanced-instruction",
			"low-cost-instruction",
		}

		// Run compilation (default: select by overall score)
		config := &loomv1.TeleprompterConfig{
			MaxBootstrappedDemos: 2,
			MinConfidence:        0.7,
			MaxRounds:            1,
			Mipro: &loomv1.MIPROConfig{
				InstructionCandidates: instructionCandidates,
			},
		}

		result, err := mipro.Compile(context.Background(), &CompileRequest{
			AgentID:  "test-agent",
			Agent:    agent,
			Trainset: trainset,
			Metric:   metric,
			Config:   config,
		})

		// Verify compilation succeeded
		require.NoError(t, err)
		require.NotNil(t, result)

		// Should select low-cost instruction (highest overall score: 82.5%)
		// Scores: high-quality=77.5%, balanced=80.0%, low-cost=82.5%
		assert.Contains(t, result.OptimizedPrompts["system"], "low-cost-instruction")
		assert.Greater(t, result.TrainsetScore, 0.7)

		// Verify dimension scores captured in metadata
		assert.Contains(t, result.Metadata, "dimension.quality")
		assert.Contains(t, result.Metadata, "dimension.cost")

		t.Logf("Selected instruction: %s", result.OptimizedPrompts["system"])
		t.Logf("Trainset score: %.4f", result.TrainsetScore)
		t.Logf("Quality dimension: %s", result.Metadata["dimension.quality"])
		t.Logf("Cost dimension: %s", result.Metadata["dimension.cost"])
	})

	t.Run("quality-prioritized instruction selection", func(t *testing.T) {
		// Track which instruction is currently being evaluated
		var currentInstruction string

		// Demonstrate dimension-weighted instruction selection
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				instruction := currentInstruction

				var qualityScore, costScore float64
				if instruction == "high-quality-instruction" {
					qualityScore = 95.0
					costScore = 60.0 // Expensive (overall: 77.5)
				} else if instruction == "balanced-instruction" {
					qualityScore = 80.0
					costScore = 80.0 // Balanced (overall: 80.0)
				} else {
					qualityScore = 70.0
					costScore = 95.0 // Cheap (overall: 82.5)
				}

				avgScore := (qualityScore + costScore) / 2.0

				return &loomv1.EvaluateResponse{
					Passed:     avgScore >= 70.0,
					FinalScore: avgScore,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "multi-dim-judge",
							OverallScore: avgScore,
							DimensionScores: map[string]float64{
								"quality": qualityScore,
								"cost":    costScore,
							},
						},
					},
				}, nil
			},
		}

		metric, err := NewMultiJudgeMetric(&MultiJudgeMetricConfig{
			Orchestrator: mockOrch,
			JudgeIDs:     []string{"multi-dim-judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		agent := &MockAgentWithMemory{
			id: "test-agent",
			runFunc: func(ctx context.Context, inputs map[string]string) (*ExecutionResult, error) {
				return &ExecutionResult{
					Inputs:  inputs,
					Outputs: map[string]string{"answer": "test answer"},
					TraceID: "trace-" + inputs["query"],
					Success: true,
				}, nil
			},
			memory: &MockMemoryWithCallback{
				updateFunc: func(prompts map[string]string, demos []*loomv1.Demonstration) error {
					if systemPrompt, ok := prompts["system"]; ok {
						currentInstruction = systemPrompt
					}
					return nil
				},
			},
		}

		trainset := []*loomv1.Example{
			{
				Id:      "ex1",
				Inputs:  map[string]string{"query": "test query"},
				Outputs: map[string]string{"answer": "expected answer"},
			},
		}

		registry := NewRegistry()
		mipro := NewMIPRO(observability.NewNoOpTracer(), registry, nil)

		instructionCandidates := []string{
			"high-quality-instruction",
			"balanced-instruction",
			"low-cost-instruction",
		}

		// Prioritize quality over cost (quality: 2.0, cost: 1.0)
		config := &loomv1.TeleprompterConfig{
			MaxBootstrappedDemos: 2,
			MinConfidence:        0.7,
			MaxRounds:            1,
			Mipro: &loomv1.MIPROConfig{
				InstructionCandidates: instructionCandidates,
				DimensionPriorities: map[string]float64{
					"quality": 2.0, // Quality weighted 2x
					"cost":    1.0,
				},
			},
		}

		result, err := mipro.Compile(context.Background(), &CompileRequest{
			AgentID:  "test-agent",
			Agent:    agent,
			Trainset: trainset,
			Metric:   metric,
			Config:   config,
		})

		require.NoError(t, err)
		require.NotNil(t, result)

		// With quality prioritization, should select high-quality-instruction
		// Weighted score: (95*2 + 60*1) / 3 = 83.3 (highest)
		// vs balanced: (80*2 + 80*1) / 3 = 80.0
		// vs low-cost: (70*2 + 95*1) / 3 = 78.3
		assert.Contains(t, result.OptimizedPrompts["system"], "high-quality-instruction")

		t.Logf("Quality-prioritized selection: %s", result.OptimizedPrompts["system"])
		t.Logf("Trainset score: %.4f", result.TrainsetScore)
	})

	t.Run("cost-prioritized instruction selection", func(t *testing.T) {
		// Track which instruction is currently being evaluated
		var currentInstruction string

		// Demonstrate cost optimization (opposite of quality)
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				instruction := currentInstruction

				var qualityScore, costScore float64
				if instruction == "high-quality-instruction" {
					qualityScore = 95.0
					costScore = 60.0 // Expensive
				} else if instruction == "balanced-instruction" {
					qualityScore = 80.0
					costScore = 80.0 // Balanced
				} else {
					qualityScore = 70.0
					costScore = 95.0 // Cheap (best for cost)
				}

				avgScore := (qualityScore + costScore) / 2.0

				return &loomv1.EvaluateResponse{
					Passed:     avgScore >= 70.0,
					FinalScore: avgScore,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "cost-judge",
							OverallScore: avgScore,
							DimensionScores: map[string]float64{
								"quality": qualityScore,
								"cost":    costScore,
							},
						},
					},
				}, nil
			},
		}

		metric, err := NewMultiJudgeMetric(&MultiJudgeMetricConfig{
			Orchestrator: mockOrch,
			JudgeIDs:     []string{"cost-judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		agent := &MockAgentWithMemory{
			id: "test-agent",
			runFunc: func(ctx context.Context, inputs map[string]string) (*ExecutionResult, error) {
				return &ExecutionResult{
					Inputs:  inputs,
					Outputs: map[string]string{"answer": "test answer"},
					TraceID: "trace-" + inputs["query"],
					Success: true,
				}, nil
			},
			memory: &MockMemoryWithCallback{
				updateFunc: func(prompts map[string]string, demos []*loomv1.Demonstration) error {
					if systemPrompt, ok := prompts["system"]; ok {
						currentInstruction = systemPrompt
					}
					return nil
				},
			},
		}

		trainset := []*loomv1.Example{
			{
				Id:      "ex1",
				Inputs:  map[string]string{"query": "test query"},
				Outputs: map[string]string{"answer": "expected answer"},
			},
		}

		registry := NewRegistry()
		mipro := NewMIPRO(observability.NewNoOpTracer(), registry, nil)

		instructionCandidates := []string{
			"high-quality-instruction",
			"balanced-instruction",
			"low-cost-instruction",
		}

		// Prioritize cost over quality (cost: 3.0, quality: 1.0)
		config := &loomv1.TeleprompterConfig{
			MaxBootstrappedDemos: 2,
			MinConfidence:        0.7,
			MaxRounds:            1,
			Mipro: &loomv1.MIPROConfig{
				InstructionCandidates: instructionCandidates,
				DimensionPriorities: map[string]float64{
					"quality": 1.0,
					"cost":    3.0, // Cost weighted 3x
				},
			},
		}

		result, err := mipro.Compile(context.Background(), &CompileRequest{
			AgentID:  "test-agent",
			Agent:    agent,
			Trainset: trainset,
			Metric:   metric,
			Config:   config,
		})

		require.NoError(t, err)
		require.NotNil(t, result)

		// With cost prioritization, should select low-cost-instruction
		// Weighted score: (70*1 + 95*3) / 4 = 88.75 (highest)
		// vs balanced: (80*1 + 80*3) / 4 = 80.0
		// vs high-quality: (95*1 + 60*3) / 4 = 68.75
		assert.Contains(t, result.OptimizedPrompts["system"], "low-cost-instruction")

		t.Logf("Cost-prioritized selection: %s", result.OptimizedPrompts["system"])
		t.Logf("Trainset score: %.4f", result.TrainsetScore)
	})

	t.Run("safety-first instruction filtering", func(t *testing.T) {
		// Track which instruction is currently being evaluated
		var currentInstruction string

		// Demonstrate that unsafe instructions are filtered out
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				instruction := currentInstruction

				var qualityScore, safetyScore float64
				if instruction == "safe-instruction" {
					qualityScore = 80.0
					safetyScore = 90.0 // Safe (avg: 85.0)
				} else if instruction == "unsafe-instruction" {
					qualityScore = 95.0 // High quality
					safetyScore = 40.0  // But unsafe! (avg: 67.5 < 70%)
				} else {
					qualityScore = 75.0
					safetyScore = 75.0 // Acceptable (avg: 75.0)
				}

				avgScore := (qualityScore + safetyScore) / 2.0

				return &loomv1.EvaluateResponse{
					Passed:     avgScore >= 70.0,
					FinalScore: avgScore,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "safety-judge",
							OverallScore: avgScore,
							DimensionScores: map[string]float64{
								"quality": qualityScore,
								"safety":  safetyScore,
							},
						},
					},
				}, nil
			},
		}

		metric, err := NewMultiJudgeMetric(&MultiJudgeMetricConfig{
			Orchestrator: mockOrch,
			JudgeIDs:     []string{"safety-judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		agent := &MockAgentWithMemory{
			id: "test-agent",
			runFunc: func(ctx context.Context, inputs map[string]string) (*ExecutionResult, error) {
				return &ExecutionResult{
					Inputs:  inputs,
					Outputs: map[string]string{"answer": "test answer"},
					TraceID: "trace-" + inputs["query"],
					Success: true,
				}, nil
			},
			memory: &MockMemoryWithCallback{
				updateFunc: func(prompts map[string]string, demos []*loomv1.Demonstration) error {
					if systemPrompt, ok := prompts["system"]; ok {
						currentInstruction = systemPrompt
					}
					return nil
				},
			},
		}

		trainset := []*loomv1.Example{
			{
				Id:      "ex1",
				Inputs:  map[string]string{"query": "test query"},
				Outputs: map[string]string{"answer": "expected answer"},
			},
		}

		registry := NewRegistry()
		mipro := NewMIPRO(observability.NewNoOpTracer(), registry, nil)

		instructionCandidates := []string{
			"safe-instruction",
			"unsafe-instruction",
			"acceptable-instruction",
		}

		config := &loomv1.TeleprompterConfig{
			MaxBootstrappedDemos: 2,
			MinConfidence:        0.7, // 70% threshold filters unsafe instruction
			MaxRounds:            1,
			Mipro: &loomv1.MIPROConfig{
				InstructionCandidates: instructionCandidates,
			},
		}

		result, err := mipro.Compile(context.Background(), &CompileRequest{
			AgentID:  "test-agent",
			Agent:    agent,
			Trainset: trainset,
			Metric:   metric,
			Config:   config,
		})

		require.NoError(t, err)
		require.NotNil(t, result)

		// Unsafe instruction should be filtered out by min_confidence threshold
		// Should select safe-instruction (highest among safe options)
		assert.NotContains(t, result.OptimizedPrompts["system"], "unsafe-instruction")
		assert.Contains(t, result.OptimizedPrompts["system"], "safe-instruction")

		t.Logf("Safety filtering: selected %s", result.OptimizedPrompts["system"])
		t.Logf("Unsafe instruction filtered out by threshold")
	})

	t.Run("no instruction generator error", func(t *testing.T) {
		// Test error when no candidates provided and no generator configured
		registry := NewRegistry()
		mipro := NewMIPRO(observability.NewNoOpTracer(), registry, nil) // No generator

		config := &loomv1.TeleprompterConfig{
			MaxBootstrappedDemos: 2,
			MinConfidence:        0.7,
			MaxRounds:            1,
			Mipro:                &loomv1.MIPROConfig{
				// No InstructionCandidates provided
			},
		}

		_, err := mipro.Compile(context.Background(), &CompileRequest{
			AgentID:  "test-agent",
			Agent:    &MockAgent{id: "test-agent"},
			Trainset: []*loomv1.Example{{Id: "ex1"}},
			Metric:   NewExactMatchMetric(),
			Config:   config,
		})

		// Should error
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no instruction candidates provided and no instruction generator configured")
	})

	t.Run("all candidates fail threshold", func(t *testing.T) {
		// Test error when all candidates fail min_confidence threshold
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				// All instructions score below 70%
				return &loomv1.EvaluateResponse{
					Passed:     false,
					FinalScore: 50.0,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "judge",
							OverallScore: 50.0,
						},
					},
				}, nil
			},
		}

		metric, err := NewMultiJudgeMetric(&MultiJudgeMetricConfig{
			Orchestrator: mockOrch,
			JudgeIDs:     []string{"judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		agent := &MockAgent{
			id: "test-agent",
			runFunc: func(ctx context.Context, inputs map[string]string) (*ExecutionResult, error) {
				return &ExecutionResult{
					Inputs:  inputs,
					Outputs: map[string]string{"answer": "test"},
					TraceID: "trace-123",
					Success: true,
				}, nil
			},
		}

		trainset := []*loomv1.Example{
			{
				Id:      "ex1",
				Inputs:  map[string]string{"query": "test"},
				Outputs: map[string]string{"answer": "expected"},
			},
		}

		registry := NewRegistry()
		mipro := NewMIPRO(observability.NewNoOpTracer(), registry, nil)

		config := &loomv1.TeleprompterConfig{
			MaxBootstrappedDemos: 2,
			MinConfidence:        0.7, // All score 50% < 70%
			MaxRounds:            1,
			Mipro: &loomv1.MIPROConfig{
				InstructionCandidates: []string{"bad-instruction-1", "bad-instruction-2"},
			},
		}

		_, err = mipro.Compile(context.Background(), &CompileRequest{
			AgentID:  "test-agent",
			Agent:    agent,
			Trainset: trainset,
			Metric:   metric,
			Config:   config,
		})

		// Should error
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no instruction candidates met minimum confidence threshold")
	})
}

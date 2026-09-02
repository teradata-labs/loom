// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
)

// TestPipelineLevelingCostCoversTheWholeLadder pins two things a billing
// consumer depends on: the workflow cost of a leveled stage includes the
// attempts and rungs that lost, and models_used names the model that actually
// produced the output.
//
// Before this, the report's total was computed and dropped: cost aggregation
// summed the winning result's own single-call cost, so the spend leveling
// introduces — retries and losing rungs — was invisible, and models_used named
// the primary even when a rung's output was adopted.
func TestPipelineLevelingCostCoversTheWholeLadder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		primaryOutput string
		wantOutput    string
		wantPrimary   int
		wantRung      int
		wantCost      float64
		wantModel     string
	}{
		{
			// One retry (both attempts fail the schema) plus one escalation
			// that wins: 2 * 0.10 + 0.90.
			name:          "retry plus escalation reports the summed cost",
			primaryOutput: lvlInvalidJSON,
			wantOutput:    lvlValidJSON,
			wantPrimary:   2,
			wantRung:      1,
			wantCost:      2*0.10 + 0.90,
			wantModel:     lvlFrontierModel,
		},
		{
			name:          "a passing primary reports one call and its own model",
			primaryOutput: lvlValidJSON,
			wantOutput:    lvlValidJSON,
			wantPrimary:   1,
			wantRung:      0,
			wantCost:      0.10,
			wantModel:     lvlLowModel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orch := newLevelingTestOrchestrator(t)
			primary := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0.10, tt.primaryOutput)
			rung := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.90, lvlValidJSON)
			registerLevelingAgent(t, orch, "worker", primary,
				map[string]agent.LLMProvider{lvlFrontierProvider: rung})

			result, err := runLevelingPipeline(t, orch, &loomv1.PipelineStage{
				AgentId:        "worker",
				PromptTemplate: "{{previous}}",
				OutputPolicy: &loomv1.OutputPolicy{
					OutputSchema: lvlSchema,
					RetryPolicy:  &loomv1.OutputRetryPolicy{MaxRetries: 1},
				},
				LevelingPolicy: &loomv1.LevelingPolicy{
					Enabled: true,
					Ladder:  []*loomv1.LevelingRung{{Provider: lvlFrontierProvider, Model: lvlFrontierModel}},
				},
			})

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantOutput, result.MergedOutput)
			assert.Equal(t, tt.wantPrimary, primary.count())
			assert.Equal(t, tt.wantRung, rung.count())

			require.NotNil(t, result.Cost)
			assert.InDelta(t, tt.wantCost, result.Cost.TotalCostUsd, 1e-9,
				"the stage's cost is what producing its output cost, losing rungs included")
			assert.InDelta(t, tt.wantCost, result.Cost.AgentCostsUsd["worker"], 1e-9,
				"the spend attributes to the stage's own agent")
			assert.Equal(t, tt.wantModel, result.ModelsUsed["worker"],
				"models_used names the model that produced the output")
		})
	}
}

// TestIterativePipelineHonorsLeveling pins the iterative executor's leveling
// branch. The loader accepted a leveling block on an iterative stage and the
// executor ignored it, so an operator got a silently unleveled stage.
func TestIterativePipelineHonorsLeveling(t *testing.T) {
	t.Parallel()

	t.Run("escalation runs and wins", func(t *testing.T) {
		t.Parallel()

		orch := newLevelingTestOrchestrator(t)
		primary := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0.10, lvlInvalidJSON)
		rung := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.90, lvlValidJSON)
		registerLevelingAgent(t, orch, "worker", primary,
			map[string]agent.LLMProvider{lvlFrontierProvider: rung})

		result, err := runLevelingIterativePipeline(t, orch, &loomv1.PipelineStage{
			AgentId:        "worker",
			PromptTemplate: "{{previous}}",
			OutputPolicy: &loomv1.OutputPolicy{
				OutputSchema: lvlSchema,
				RetryPolicy:  &loomv1.OutputRetryPolicy{MaxRetries: 0},
			},
			LevelingPolicy: &loomv1.LevelingPolicy{
				Enabled: true,
				Ladder:  []*loomv1.LevelingRung{{Provider: lvlFrontierProvider, Model: lvlFrontierModel}},
			},
		})

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "iterative_pipeline", result.PatternType)
		assert.Equal(t, lvlValidJSON, result.MergedOutput, "the escalated output is the stage output")
		assert.Equal(t, 1, primary.count(),
			"leveling owns the retries: the restart-policy loop must not also run the stage")
		assert.Equal(t, 1, rung.count())
		assert.Equal(t, lvlFrontierModel, result.ModelsUsed["worker"])
		require.NotNil(t, result.Cost)
		assert.InDelta(t, 0.10+0.90, result.Cost.TotalCostUsd, 1e-9)
		// The failed first attempt is reported, but the stage is not flagged as
		// continuing with an unvalidated output: the escalation satisfied the
		// contract. Both readings come from the same metadata key the plain
		// pipeline uses.
		assert.Contains(t, result.Metadata["validation_warnings"], "attempt 1: schema validation")
		assert.NotContains(t, result.Metadata["validation_warnings"], "continuing with unvalidated output")
	})

	t.Run("exhaustion surfaces warnings on the workflow result", func(t *testing.T) {
		t.Parallel()

		orch := newLevelingTestOrchestrator(t)
		primary := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0.10, lvlInvalidJSON)
		registerLevelingAgent(t, orch, "worker", primary, nil)

		result, err := runLevelingIterativePipeline(t, orch, &loomv1.PipelineStage{
			AgentId:        "worker",
			PromptTemplate: "{{previous}}",
			OutputPolicy: &loomv1.OutputPolicy{
				OutputSchema: lvlSchema,
				RetryPolicy:  &loomv1.OutputRetryPolicy{MaxRetries: 0},
			},
			LevelingPolicy: &loomv1.LevelingPolicy{Enabled: true},
		})

		require.NoError(t, err, "an unsatisfied contract is graceful degradation, not a failure")
		require.NotNil(t, result)
		assert.Equal(t, lvlInvalidJSON, result.MergedOutput, "the best output obtained is returned")
		assert.Equal(t, 1, primary.count())
		require.Contains(t, result.Metadata, "validation_warnings")
		assert.Contains(t, result.Metadata["validation_warnings"], "continuing with unvalidated output")
		assert.Contains(t, result.Metadata["validation_warnings"], "stage 1 (worker)")
	})
}

// runLevelingIterativePipeline runs a single-stage iterative pipeline with the
// restart policy enabled, which is what routes execution through
// IterativePipelineExecutor.executeWithRestarts rather than the plain pipeline
// fallback.
func runLevelingIterativePipeline(t *testing.T, orch *Orchestrator, stage *loomv1.PipelineStage) (*loomv1.WorkflowResult, error) {
	t.Helper()
	return orch.ExecutePattern(context.Background(), &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Iterative{
			Iterative: &loomv1.IterativeWorkflowPattern{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "do the thing",
					Stages:        []*loomv1.PipelineStage{stage},
				},
				MaxIterations: 1,
				RestartPolicy: &loomv1.RestartPolicy{Enabled: true},
			},
		},
	})
}

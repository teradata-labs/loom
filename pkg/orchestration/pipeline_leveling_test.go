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
	"go.uber.org/zap/zaptest"
	"google.golang.org/protobuf/proto"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
)

// newLevelingTestOrchestrator returns an orchestrator with a no-op tracer and a
// test logger — no progress callback, so the pipeline takes its plain path.
func newLevelingTestOrchestrator(t *testing.T) *Orchestrator {
	t.Helper()
	return NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
	})
}

// registerLevelingAgent creates a mock agent backed by llm and registers it,
// optionally giving it a provider pool for ladder rungs to resolve against.
func registerLevelingAgent(
	t *testing.T,
	orch *Orchestrator,
	agentID string,
	llm agent.LLMProvider,
	pool map[string]agent.LLMProvider,
) *agent.Agent {
	t.Helper()
	ag := createMockAgent(t, agentID, llm)
	if pool != nil {
		require.NoError(t, ag.SetProviderPool(pool, "", nil))
	}
	orch.RegisterAgent(agentID, ag)
	return ag
}

func runLevelingPipeline(t *testing.T, orch *Orchestrator, stage *loomv1.PipelineStage) (*loomv1.WorkflowResult, error) {
	t.Helper()
	return orch.ExecutePattern(context.Background(), &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "do the thing",
				Stages:        []*loomv1.PipelineStage{stage},
			},
		},
	})
}

// TestPipelineOutputPolicyInertWithoutLeveling is the safety test for stored
// data: an OutputPolicy that has never been enforced by any executor must stay
// unenforced. The agent's output violates the policy's schema and the pipeline
// must still succeed, pass the output through untouched, and call the agent
// exactly once.
func TestPipelineOutputPolicyInertWithoutLeveling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		leveling *loomv1.LevelingPolicy
	}{
		{name: "no leveling policy at all", leveling: nil},
		{name: "leveling policy explicitly disabled", leveling: &loomv1.LevelingPolicy{
			Enabled: false,
			// Deliberately hostile config: if any of this were read, the run
			// would either escalate or fail conversion.
			MaxEscalations: proto.Int32(-1),
			MaxCostUsd:     -1,
			TierPolicies:   map[string]*loomv1.LevelingTierPolicy{"NOT-A-TIER": {}},
			Ladder:         []*loomv1.LevelingRung{{Provider: "not-in-any-pool"}},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orch := newLevelingTestOrchestrator(t)
			llm := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0.001, lvlInvalidJSON)
			escalation := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
			registerLevelingAgent(t, orch, "worker", llm,
				map[string]agent.LLMProvider{lvlFrontierProvider: escalation})

			result, err := runLevelingPipeline(t, orch, &loomv1.PipelineStage{
				AgentId:        "worker",
				PromptTemplate: "{{previous}}",
				// Stored, persisted, and never enforced before leveling existed.
				OutputPolicy: &loomv1.OutputPolicy{
					OutputSchema: lvlSchema,
					RetryPolicy:  &loomv1.OutputRetryPolicy{MaxRetries: 3},
				},
				LevelingPolicy: tt.leveling,
			})

			require.NoError(t, err, "an unenforced OutputPolicy must not fail the pipeline")
			require.NotNil(t, result)
			assert.Equal(t, lvlInvalidJSON, result.MergedOutput, "output passes through unvalidated")
			require.Len(t, result.AgentResults, 1)
			assert.Equal(t, lvlInvalidJSON, result.AgentResults[0].Output)
			assert.Equal(t, 1, llm.count(), "exactly one agent call: no validation, no retry")
			assert.Equal(t, 0, escalation.count(), "no ladder rung is reachable")
			assert.NotContains(t, result.Metadata, "validation_warnings", "nothing was validated")
		})
	}
}

// TestPipelineLevelingFrontierShortCircuits proves an enabled policy on a
// strong primary adds no LLM calls: no judge, no rung, one agent call.
func TestPipelineLevelingFrontierShortCircuits(t *testing.T) {
	t.Parallel()

	orch := newLevelingTestOrchestrator(t)
	llm := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlInvalidJSON)
	escalation := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
	registerLevelingAgent(t, orch, "worker", llm,
		map[string]agent.LLMProvider{"strong": escalation})

	result, err := runLevelingPipeline(t, orch, &loomv1.PipelineStage{
		AgentId:        "worker",
		PromptTemplate: "{{previous}}",
		OutputPolicy:   &loomv1.OutputPolicy{OutputSchema: lvlSchema},
		LevelingPolicy: &loomv1.LevelingPolicy{
			Enabled: true,
			Ladder:  []*loomv1.LevelingRung{{Provider: "strong"}},
		},
	})

	require.NoError(t, err, "leveling exhaustion is graceful degradation, not an error")
	require.NotNil(t, result)
	assert.Equal(t, lvlInvalidJSON, result.MergedOutput)
	assert.Equal(t, 1, llm.count(), "frontier primary: no retry budget, no extra calls")
	assert.Equal(t, 0, escalation.count(), "no rung runs on the short-circuit path")
	assert.Contains(t, result.Metadata["validation_warnings"], "short-circuited")
	assert.Contains(t, result.Metadata["validation_warnings"], "tier=frontier")
}

// TestPipelineLevelingEscalatesBounded covers the two independent bounds on
// escalation: the rung cap and the cost ceiling.
func TestPipelineLevelingEscalatesBounded(t *testing.T) {
	t.Parallel()

	t.Run("bounded by max_escalations", func(t *testing.T) {
		t.Parallel()

		orch := newLevelingTestOrchestrator(t)
		primary := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, lvlInvalidJSON)
		rung1 := newLvlMockLLM(lvlMidProvider, lvlMidModel, 0.05, lvlInvalidJSON)
		rung2 := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
		registerLevelingAgent(t, orch, "worker", primary, map[string]agent.LLMProvider{
			"mid":      rung1,
			"frontier": rung2,
		})

		result, err := runLevelingPipeline(t, orch, &loomv1.PipelineStage{
			AgentId:        "worker",
			PromptTemplate: "{{previous}}",
			OutputPolicy:   &loomv1.OutputPolicy{OutputSchema: lvlSchema},
			LevelingPolicy: &loomv1.LevelingPolicy{
				Enabled:        true,
				MaxEscalations: proto.Int32(1),
				Ladder: []*loomv1.LevelingRung{
					{Provider: "mid", Model: lvlMidModel},
					{Provider: "frontier", Model: lvlFrontierModel},
				},
			},
		})

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 3, primary.count(), "local tier retry budget: one attempt plus two retries")
		assert.Equal(t, 1, rung1.count(), "first rung runs once")
		assert.Equal(t, 0, rung2.count(), "second rung is beyond max_escalations")
		assert.Contains(t, result.Metadata["validation_warnings"], "escalations=1")
	})

	t.Run("bounded by max_cost_usd", func(t *testing.T) {
		t.Parallel()

		orch := newLevelingTestOrchestrator(t)
		// Primary spend alone clears the ceiling, so no paid rung may run.
		primary := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0.01, lvlInvalidJSON)
		rung1 := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
		registerLevelingAgent(t, orch, "worker", primary,
			map[string]agent.LLMProvider{"frontier": rung1})

		result, err := runLevelingPipeline(t, orch, &loomv1.PipelineStage{
			AgentId:        "worker",
			PromptTemplate: "{{previous}}",
			OutputPolicy:   &loomv1.OutputPolicy{OutputSchema: lvlSchema},
			LevelingPolicy: &loomv1.LevelingPolicy{
				Enabled:        true,
				MaxEscalations: proto.Int32(2),
				MaxCostUsd:     0.005,
				Ladder: []*loomv1.LevelingRung{
					{Provider: "frontier", Model: lvlFrontierModel},
				},
			},
		})

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 0, rung1.count(), "cost ceiling reached, no paid rung runs")
		assert.Contains(t, result.Metadata["validation_warnings"], "budget_exhausted=true")
		assert.Contains(t, result.Metadata["validation_warnings"], "escalations=0")
	})
}

// TestPipelineLevelingEscalationOutputWins proves a successful escalation's
// output becomes the stage output and carries the rung identity.
func TestPipelineLevelingEscalationOutputWins(t *testing.T) {
	t.Parallel()

	orch := newLevelingTestOrchestrator(t)
	primary := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, lvlInvalidJSON)
	rung1 := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
	registerLevelingAgent(t, orch, "worker", primary,
		map[string]agent.LLMProvider{lvlFrontierProvider: rung1})

	result, err := runLevelingPipeline(t, orch, &loomv1.PipelineStage{
		AgentId:        "worker",
		PromptTemplate: "{{previous}}",
		OutputPolicy:   &loomv1.OutputPolicy{OutputSchema: lvlSchema},
		LevelingPolicy: &loomv1.LevelingPolicy{
			Enabled: true,
			Ladder:  []*loomv1.LevelingRung{{Provider: lvlFrontierProvider, Model: lvlFrontierModel}},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, lvlValidJSON, result.MergedOutput, "the escalated output is the stage output")
	assert.Equal(t, 1, rung1.count())
	require.Len(t, result.AgentResults, 1)
	res := result.AgentResults[0]
	assert.Equal(t, "worker", res.AgentId, "escalated spend attributes to the stage's agent")
	assert.Equal(t, lvlFrontierProvider, res.Metadata[levelingRungProviderKey])
	assert.Equal(t, lvlFrontierModel, res.Metadata[levelingRungModelKey])
	assert.Equal(t, "1", res.Metadata["stage"], "executor metadata is backfilled onto rung results")
	assert.InDelta(t, 0.9, result.Cost.TotalCostUsd, 1e-9)
	assert.NotContains(t, result.Metadata["validation_warnings"], "continuing with unvalidated output")
}

// TestPipelineLevelingPassingPrimaryCostsNothingExtra proves a low-tier primary
// that satisfies the contract on its first attempt spends nothing more.
func TestPipelineLevelingPassingPrimaryCostsNothingExtra(t *testing.T) {
	t.Parallel()

	orch := newLevelingTestOrchestrator(t)
	primary := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, lvlValidJSON)
	rung1 := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
	registerLevelingAgent(t, orch, "worker", primary,
		map[string]agent.LLMProvider{"frontier": rung1})

	result, err := runLevelingPipeline(t, orch, &loomv1.PipelineStage{
		AgentId:        "worker",
		PromptTemplate: "{{previous}}",
		OutputPolicy:   &loomv1.OutputPolicy{OutputSchema: lvlSchema},
		LevelingPolicy: &loomv1.LevelingPolicy{
			Enabled: true,
			Ladder:  []*loomv1.LevelingRung{{Provider: "frontier", Model: lvlFrontierModel}},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, lvlValidJSON, result.MergedOutput)
	assert.Equal(t, 1, primary.count(), "a passing first attempt needs no retry")
	assert.Equal(t, 0, rung1.count(), "no escalation on a passing output")
	assert.NotContains(t, result.Metadata, "validation_warnings")
}

// TestPipelineLevelingHonorsLegacySchema proves enabling leveling on a stage
// written against the legacy output_schema/retry_policy fields validates against
// them rather than ignoring them.
func TestPipelineLevelingHonorsLegacySchema(t *testing.T) {
	t.Parallel()

	orch := newLevelingTestOrchestrator(t)
	primary := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, lvlInvalidJSON)
	rung1 := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
	registerLevelingAgent(t, orch, "worker", primary,
		map[string]agent.LLMProvider{"frontier": rung1})

	result, err := runLevelingPipeline(t, orch, &loomv1.PipelineStage{
		AgentId:        "worker",
		PromptTemplate: "{{previous}}",
		OutputSchema:   lvlSchema,
		RetryPolicy:    &loomv1.OutputRetryPolicy{MaxRetries: 0},
		LevelingPolicy: &loomv1.LevelingPolicy{
			Enabled: true,
			Ladder:  []*loomv1.LevelingRung{{Provider: "frontier", Model: lvlFrontierModel}},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, lvlValidJSON, result.MergedOutput)
	assert.Equal(t, 1, primary.count(), "the explicit legacy retry_policy (0) wins over the tier budget")
	assert.Equal(t, 1, rung1.count())
}

func TestPipelineLevelingConfigErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		stage   *loomv1.PipelineStage
		wantMsg []string
	}{
		{
			name: "validation_prompt cannot be combined with leveling",
			stage: &loomv1.PipelineStage{
				AgentId:          "worker",
				PromptTemplate:   "{{previous}}",
				ValidationPrompt: "is {{output}} any good?",
				OutputPolicy:     &loomv1.OutputPolicy{OutputSchema: lvlSchema},
				LevelingPolicy:   &loomv1.LevelingPolicy{Enabled: true},
			},
			wantMsg: []string{"validation_prompt", "leveling_policy"},
		},
		{
			name: "invalid leveling policy fails the stage",
			stage: &loomv1.PipelineStage{
				AgentId:        "worker",
				PromptTemplate: "{{previous}}",
				LevelingPolicy: &loomv1.LevelingPolicy{
					Enabled:      true,
					TierPolicies: map[string]*loomv1.LevelingTierPolicy{"MID": {}},
				},
			},
			wantMsg: []string{"tier name", "MID"},
		},
		{
			name: "unresolvable ladder rung fails the stage",
			stage: &loomv1.PipelineStage{
				AgentId:        "worker",
				PromptTemplate: "{{previous}}",
				LevelingPolicy: &loomv1.LevelingPolicy{
					Enabled: true,
					Ladder:  []*loomv1.LevelingRung{{Provider: "nowhere"}},
				},
			},
			wantMsg: []string{"nowhere", "provider pool"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orch := newLevelingTestOrchestrator(t)
			llm := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, lvlValidJSON)
			registerLevelingAgent(t, orch, "worker", llm, nil)

			result, err := runLevelingPipeline(t, orch, tt.stage)
			require.Error(t, err)
			assert.Nil(t, result)
			for _, want := range tt.wantMsg {
				assert.Contains(t, err.Error(), want)
			}
			assert.Equal(t, 0, llm.count(), "a config error must not reach the LLM")
		})
	}
}

// --- parallel executor ---

func runLevelingParallel(t *testing.T, orch *Orchestrator, tasks ...*loomv1.AgentTask) (*loomv1.WorkflowResult, error) {
	t.Helper()
	return orch.ExecutePattern(context.Background(), &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Parallel{
			Parallel: &loomv1.ParallelPattern{
				Tasks:         tasks,
				MergeStrategy: loomv1.MergeStrategy_CONCATENATE,
			},
		},
	})
}

// TestParallelOutputPolicyInertWithoutLeveling is the parallel-side safety test:
// a stored OutputPolicy the task's output violates stays unenforced.
func TestParallelOutputPolicyInertWithoutLeveling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		leveling *loomv1.LevelingPolicy
	}{
		{name: "no leveling policy at all", leveling: nil},
		{name: "leveling policy explicitly disabled", leveling: &loomv1.LevelingPolicy{
			Enabled:        false,
			MaxEscalations: proto.Int32(-1),
			MaxCostUsd:     -1,
			TierPolicies:   map[string]*loomv1.LevelingTierPolicy{"NOT-A-TIER": {}},
			Ladder:         []*loomv1.LevelingRung{{Provider: "not-in-any-pool"}},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orch := newLevelingTestOrchestrator(t)
			llm := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0.001, lvlInvalidJSON)
			escalation := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
			registerLevelingAgent(t, orch, "worker", llm,
				map[string]agent.LLMProvider{lvlFrontierProvider: escalation})

			result, err := runLevelingParallel(t, orch, &loomv1.AgentTask{
				AgentId: "worker",
				Prompt:  "do the thing",
				OutputPolicy: &loomv1.OutputPolicy{
					OutputSchema: lvlSchema,
					RetryPolicy:  &loomv1.OutputRetryPolicy{MaxRetries: 3},
				},
				LevelingPolicy: tt.leveling,
			})

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, result.AgentResults, 1)
			assert.Equal(t, lvlInvalidJSON, result.AgentResults[0].Output)
			assert.Equal(t, 1, llm.count(), "exactly one agent call: no validation, no retry")
			assert.Equal(t, 0, escalation.count())
		})
	}
}

// TestParallelLevelingEscalatesPerTask exercises two concurrent tasks, each with
// its own enabled policy and ladder, under the race detector.
func TestParallelLevelingEscalatesPerTask(t *testing.T) {
	t.Parallel()

	orch := newLevelingTestOrchestrator(t)

	weak := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, lvlInvalidJSON)
	weakRung := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
	registerLevelingAgent(t, orch, "weak", weak,
		map[string]agent.LLMProvider{"frontier": weakRung})

	strong := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlInvalidJSON)
	strongRung := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
	registerLevelingAgent(t, orch, "strong", strong,
		map[string]agent.LLMProvider{"frontier": strongRung})

	policy := func() *loomv1.LevelingPolicy {
		return &loomv1.LevelingPolicy{
			Enabled: true,
			Ladder:  []*loomv1.LevelingRung{{Provider: "frontier", Model: lvlFrontierModel}},
		}
	}

	result, err := runLevelingParallel(t, orch,
		&loomv1.AgentTask{
			AgentId:        "weak",
			Prompt:         "task one",
			OutputPolicy:   &loomv1.OutputPolicy{OutputSchema: lvlSchema},
			LevelingPolicy: policy(),
		},
		&loomv1.AgentTask{
			AgentId:        "strong",
			Prompt:         "task two",
			OutputPolicy:   &loomv1.OutputPolicy{OutputSchema: lvlSchema},
			LevelingPolicy: policy(),
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.AgentResults, 2)

	assert.Equal(t, 3, weak.count(), "local tier retry budget applies to the weak task")
	assert.Equal(t, 1, weakRung.count(), "the weak task escalates")
	assert.Equal(t, 1, strong.count(), "the frontier task short-circuits")
	assert.Equal(t, 0, strongRung.count(), "no rung runs for the frontier task")

	byAgent := map[string]*loomv1.AgentResult{}
	for _, r := range result.AgentResults {
		byAgent[r.AgentId] = r
	}
	require.Contains(t, byAgent, "weak")
	require.Contains(t, byAgent, "strong")
	assert.Equal(t, lvlValidJSON, byAgent["weak"].Output)
	assert.Equal(t, lvlInvalidJSON, byAgent["strong"].Output)
	assert.Equal(t, "0", byAgent["weak"].Metadata["task_index"],
		"executor metadata is backfilled onto rung results so merge labeling survives")
}

func TestParallelLevelingConfigErrorFailsTask(t *testing.T) {
	t.Parallel()

	orch := newLevelingTestOrchestrator(t)
	llm := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, lvlValidJSON)
	registerLevelingAgent(t, orch, "worker", llm, nil)

	result, err := runLevelingParallel(t, orch, &loomv1.AgentTask{
		AgentId: "worker",
		Prompt:  "do the thing",
		LevelingPolicy: &loomv1.LevelingPolicy{
			Enabled: true,
			Ladder:  []*loomv1.LevelingRung{{Provider: "nowhere"}},
		},
	})

	// A single failing task means every task failed, which the parallel
	// executor surfaces as an error.
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "nowhere")
	assert.Equal(t, 0, llm.count(), "a config error must not reach the LLM")
}

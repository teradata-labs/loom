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

// A negative retry bound is only reachable as raw proto: the gRPC
// ExecuteWorkflow path accepts an inline pattern that no YAML loader ever sees.
// Before the floor and the conversion check below, such a bound made the
// validator's attempt loop run zero times, so ValidateAndRetry returned a nil
// result with a nil error and the pipeline dereferenced it.

// TestValidateAndRetryNegativeRetryBoundRunsOneAttempt pins the validator-side
// floor: whatever bound arrives, at least one attempt runs, so a nil error
// always comes with a result.
func TestValidateAndRetryNegativeRetryBoundRunsOneAttempt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		maxRetries  int32
		wantCalls   int
		wantPassed  bool
		wantWarning bool
	}{
		{name: "minus one floors to zero retries", maxRetries: -1, wantCalls: 1, wantWarning: true},
		{name: "large negative floors to zero retries", maxRetries: -1000, wantCalls: 1, wantWarning: true},
		{name: "zero is one attempt", maxRetries: 0, wantCalls: 1, wantWarning: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rung := newMockRung(0.01, lvlInvalidJSON)
			v := NewOutputValidator(nil, nil)

			result, outcome, err := v.ValidateAndRetry(
				context.Background(), schemaPolicy(tt.maxRetries, true),
				rung.execute, nil, "do the thing", "wf-neg", nil, nil)

			require.NoError(t, err)
			require.NotNil(t, result, "a nil error must come with a result")
			assert.Equal(t, lvlInvalidJSON, result.Output)
			assert.Equal(t, tt.wantCalls, rung.count())
			assert.Equal(t, tt.wantPassed, outcome.Passed)
			if tt.wantWarning {
				require.Error(t, outcome.Err, "the failing attempt's verdict is reported")
				assert.Len(t, outcome.Warnings, 1)
			}
		})
	}
}

// TestLevelingNegativeRetryBoundReturnsResult proves the executor forwards a
// real result on both of its paths with a negative bound in the policy — the
// floor holds even when the conversion check is bypassed by constructing the
// Go policy directly.
func TestLevelingNegativeRetryBoundReturnsResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		policy *LevelingPolicy
	}{
		{name: "leveling disabled", policy: nil},
		{
			name:   "leveling enabled, frontier short-circuits",
			policy: &LevelingPolicy{Enabled: true, ShortCircuitMid: true, MaxEscalations: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rung := newMockRung(0.01, lvlInvalidJSON)
			exec := newTestLevelingExecutor(t, tt.policy)

			result, _, err := exec.Execute(
				context.Background(), schemaPolicy(-1, true),
				[]LevelingRung{{
					Provider: lvlFrontierProvider,
					Model:    lvlFrontierModel,
					Execute:  rung.execute,
				}},
				"do the thing", "wf-neg-lvl")

			require.NoError(t, err)
			require.NotNil(t, result, "a nil error must come with a result")
			assert.Equal(t, lvlInvalidJSON, result.Output)
			assert.Equal(t, 1, rung.count())
		})
	}
}

// TestPipelineLevelingRejectsNegativeRetryBound builds the panic input as raw
// proto — an inline pattern, exactly what gRPC ExecuteWorkflow accepts — and
// pins that it now fails conversion with the offending field named instead of
// panicking on a nil result.
func TestPipelineLevelingRejectsNegativeRetryBound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		outputPolicy *loomv1.OutputPolicy
		retryPolicy  *loomv1.OutputRetryPolicy
	}{
		{
			name: "unified output_policy",
			outputPolicy: &loomv1.OutputPolicy{
				OutputSchema: lvlSchema,
				RetryPolicy:  &loomv1.OutputRetryPolicy{MaxRetries: -1},
			},
		},
		{
			name:        "legacy retry_policy synthesized into a contract",
			retryPolicy: &loomv1.OutputRetryPolicy{MaxRetries: -1},
		},
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
				OutputSchema:   lvlSchema,
				OutputPolicy:   tt.outputPolicy,
				RetryPolicy:    tt.retryPolicy,
				LevelingPolicy: &loomv1.LevelingPolicy{Enabled: true},
			})

			require.Error(t, err, "a negative retry bound is a config error, not a panic")
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), "output_policy.retry_policy.max_retries must be >= 0")
			assert.Equal(t, 0, llm.count(), "the stage never runs")
			assert.Equal(t, 0, escalation.count())
		})
	}
}

// TestParallelLevelingRejectsNegativeRetryBound is the parallel-task twin of
// the pipeline case above: the same raw-proto bound, the same rejection.
func TestParallelLevelingRejectsNegativeRetryBound(t *testing.T) {
	t.Parallel()

	orch := newLevelingTestOrchestrator(t)
	llm := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0.001, lvlInvalidJSON)
	registerLevelingAgent(t, orch, "worker", llm, nil)

	result, err := orch.ExecutePattern(context.Background(), &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Parallel{
			Parallel: &loomv1.ParallelPattern{
				Tasks: []*loomv1.AgentTask{{
					AgentId: "worker",
					Prompt:  "do the thing",
					OutputPolicy: &loomv1.OutputPolicy{
						OutputSchema: lvlSchema,
						RetryPolicy:  &loomv1.OutputRetryPolicy{MaxRetries: -1},
					},
					LevelingPolicy: &loomv1.LevelingPolicy{Enabled: true},
				}},
			},
		},
	})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "output_policy.retry_policy.max_retries must be >= 0")
	assert.Equal(t, 0, llm.count(), "the task never runs")
}

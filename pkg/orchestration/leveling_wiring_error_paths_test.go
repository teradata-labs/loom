// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
)

// --- pipeline executor ---

// TestPipelineLevelingUnknownAgentFails pins that enabling leveling does not
// change what happens to a stage naming an unregistered agent: the pipeline's
// up-front agent resolution still fails the whole workflow before any LLM is
// touched, rather than the leveling path quietly resolving something else.
func TestPipelineLevelingUnknownAgentFails(t *testing.T) {
	t.Parallel()

	orch := newLevelingTestOrchestrator(t)
	other := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, lvlValidJSON)
	registerLevelingAgent(t, orch, "registered-worker", other, nil)

	result, err := runLevelingPipeline(t, orch, &loomv1.PipelineStage{
		AgentId:        "no-such-worker",
		PromptTemplate: "{{previous}}",
		OutputPolicy:   &loomv1.OutputPolicy{OutputSchema: lvlSchema},
		LevelingPolicy: &loomv1.LevelingPolicy{Enabled: true},
	})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "no-such-worker")
	assert.Equal(t, 0, other.count(), "leveling must not fall back to another agent")
}

// TestPipelineLevelingExecutionErrorFailsStage pins that a transport failure on
// the leveling path fails the stage. Leveling degrades gracefully on a bad
// output; it must not degrade gracefully on a call that never completed.
func TestPipelineLevelingExecutionErrorFailsStage(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("provider unavailable")
	orch := newLevelingTestOrchestrator(t)
	rung := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
	registerLevelingAgent(t, orch, "worker", newLvlErrLLM(lvlLowProvider, lvlLowModel, wantErr),
		map[string]agent.LLMProvider{"frontier": rung})

	result, err := runLevelingPipeline(t, orch, &loomv1.PipelineStage{
		AgentId:        "worker",
		PromptTemplate: "{{previous}}",
		OutputPolicy:   &loomv1.OutputPolicy{OutputSchema: lvlSchema},
		LevelingPolicy: &loomv1.LevelingPolicy{
			Enabled: true,
			Ladder:  []*loomv1.LevelingRung{{Provider: "frontier", Model: lvlFrontierModel}},
		},
	})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Equal(t, 0, rung.count(), "an execution error is not an escalation trigger")
}

// TestPipelineLevelingContinueModeUsesStageFeedback covers the stage's Feedback
// closure. A CONTINUE-mode retry policy makes the validator continue the stage's
// own session rather than start a fresh one, so the second attempt must arrive
// as feedback text and still be labeled as the same stage.
func TestPipelineLevelingContinueModeUsesStageFeedback(t *testing.T) {
	t.Parallel()

	orch := newLevelingTestOrchestrator(t)
	// Invalid first, then valid: the CONTINUE retry is what turns it around, so
	// no escalation rung is needed.
	primary := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0.01, lvlInvalidJSON, lvlValidJSON)
	rung := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
	registerLevelingAgent(t, orch, "worker", primary,
		map[string]agent.LLMProvider{"frontier": rung})

	result, err := runLevelingPipeline(t, orch, &loomv1.PipelineStage{
		AgentId:        "worker",
		PromptTemplate: "{{previous}}",
		OutputPolicy: &loomv1.OutputPolicy{
			OutputSchema: lvlSchema,
			RetryPolicy: &loomv1.OutputRetryPolicy{
				MaxRetries:  1,
				SessionMode: loomv1.RetrySessionMode_RETRY_SESSION_MODE_CONTINUE,
			},
		},
		LevelingPolicy: &loomv1.LevelingPolicy{
			Enabled: true,
			Ladder:  []*loomv1.LevelingRung{{Provider: "frontier", Model: lvlFrontierModel}},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, lvlValidJSON, result.MergedOutput)
	assert.Equal(t, 2, primary.count(), "one attempt plus one CONTINUE retry")
	assert.Equal(t, 0, rung.count(), "a successful retry must not pay for an escalation")
	assert.Contains(t, primary.promptAt(1), "schema validation",
		"the retry arrives as feedback carrying the validation failure")
	require.Len(t, result.AgentResults, 1)
	assert.Equal(t, "1", result.AgentResults[0].Metadata["stage"])
}

// TestPipelineValidationPromptLLMErrorIsWarnedNotFatal covers the non-leveling
// validation path when the validating LLM itself fails: the stage must continue
// rather than fail, because a broken validator is an infrastructure problem, not
// evidence that the output is bad.
func TestPipelineValidationPromptLLMErrorIsWarnedNotFatal(t *testing.T) {
	t.Parallel()

	// The orchestrator's explicit merge LLM wins over any agent role LLM, so
	// this is what validateStageOutput will call — and it always errors.
	orch := NewOrchestrator(Config{
		Logger:      zaptest.NewLogger(t),
		Tracer:      observability.NewNoOpTracer(),
		LLMProvider: newLvlErrLLM(lvlFrontierProvider, lvlFrontierModel, errors.New("validator offline")),
	})
	llm := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, "a prose answer")
	registerLevelingAgent(t, orch, "worker", llm, nil)

	result, err := runLevelingPipeline(t, orch, &loomv1.PipelineStage{
		AgentId:          "worker",
		PromptTemplate:   "{{previous}}",
		ValidationPrompt: "is {{output}} any good?",
	})

	require.NoError(t, err, "an unavailable validator must not fail the stage")
	require.NotNil(t, result)
	assert.Equal(t, "a prose answer", result.MergedOutput)
	assert.Equal(t, 1, llm.count(), "no retry: nothing said the output was bad")
	assert.NotContains(t, result.Metadata, "validation_warnings",
		"a validator that never ran produced no verdict to warn about")
}

// --- parallel executor ---

// TestParallelLevelingPolicyConversionErrorFailsTask covers the proto-to-Go
// conversion gate on the parallel path. The task index and agent are named so a
// bad policy in one task of many can be found.
func TestParallelLevelingPolicyConversionErrorFailsTask(t *testing.T) {
	t.Parallel()

	orch := newLevelingTestOrchestrator(t)
	llm := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, lvlValidJSON)
	registerLevelingAgent(t, orch, "worker", llm, nil)

	result, err := runLevelingParallel(t, orch, &loomv1.AgentTask{
		AgentId: "worker",
		Prompt:  "do the thing",
		LevelingPolicy: &loomv1.LevelingPolicy{
			Enabled:      true,
			TierPolicies: map[string]*loomv1.LevelingTierPolicy{"MID": {}},
		},
	})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "unknown tier name")
	assert.Contains(t, err.Error(), "MID")
	assert.Contains(t, err.Error(), "worker")
	assert.Equal(t, 0, llm.count(), "a config error must not reach the LLM")
}

// TestParallelLevelingUnknownAgentFailsTask is the parallel twin: the executor's
// up-front task-agent resolution still owns this failure with leveling enabled.
func TestParallelLevelingUnknownAgentFailsTask(t *testing.T) {
	t.Parallel()

	orch := newLevelingTestOrchestrator(t)
	other := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, lvlValidJSON)
	registerLevelingAgent(t, orch, "registered-worker", other, nil)

	result, err := runLevelingParallel(t, orch, &loomv1.AgentTask{
		AgentId:        "no-such-worker",
		Prompt:         "do the thing",
		OutputPolicy:   &loomv1.OutputPolicy{OutputSchema: lvlSchema},
		LevelingPolicy: &loomv1.LevelingPolicy{Enabled: true},
	})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "no-such-worker")
	assert.Equal(t, 0, other.count())
}

// TestParallelLevelingExecutionErrorFailsTask is the parallel twin of the
// pipeline execution-error test.
func TestParallelLevelingExecutionErrorFailsTask(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("provider unavailable")
	orch := newLevelingTestOrchestrator(t)
	rung := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
	registerLevelingAgent(t, orch, "worker", newLvlErrLLM(lvlLowProvider, lvlLowModel, wantErr),
		map[string]agent.LLMProvider{"frontier": rung})

	result, err := runLevelingParallel(t, orch, &loomv1.AgentTask{
		AgentId:      "worker",
		Prompt:       "do the thing",
		OutputPolicy: &loomv1.OutputPolicy{OutputSchema: lvlSchema},
		LevelingPolicy: &loomv1.LevelingPolicy{
			Enabled: true,
			Ladder:  []*loomv1.LevelingRung{{Provider: "frontier", Model: lvlFrontierModel}},
		},
	})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Equal(t, 0, rung.count())
}

// TestParallelLevelingContinueModeUsesTaskFeedback is the parallel twin of the
// stage feedback test: a CONTINUE-mode retry must continue the task's own
// session through the task's Feedback closure rather than starting a fresh chat,
// and the retried attempt must still be labeled with the task's index.
func TestParallelLevelingContinueModeUsesTaskFeedback(t *testing.T) {
	t.Parallel()

	orch := newLevelingTestOrchestrator(t)
	primary := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0.01, lvlInvalidJSON, lvlValidJSON)
	rung := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
	registerLevelingAgent(t, orch, "worker", primary,
		map[string]agent.LLMProvider{"frontier": rung})

	result, err := runLevelingParallel(t, orch, &loomv1.AgentTask{
		AgentId: "worker",
		Prompt:  "do the thing",
		OutputPolicy: &loomv1.OutputPolicy{
			OutputSchema: lvlSchema,
			RetryPolicy: &loomv1.OutputRetryPolicy{
				MaxRetries:  1,
				SessionMode: loomv1.RetrySessionMode_RETRY_SESSION_MODE_CONTINUE,
			},
		},
		LevelingPolicy: &loomv1.LevelingPolicy{
			Enabled: true,
			Ladder:  []*loomv1.LevelingRung{{Provider: "frontier", Model: lvlFrontierModel}},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.AgentResults, 1)
	assert.Equal(t, lvlValidJSON, result.AgentResults[0].Output)
	assert.Equal(t, 2, primary.count(), "one attempt plus one CONTINUE retry")
	assert.Equal(t, 0, rung.count(), "a successful retry must not pay for an escalation")
	assert.Contains(t, primary.promptAt(1), "schema validation",
		"the retry arrives as feedback carrying the validation failure")
	assert.Equal(t, "0", result.AgentResults[0].Metadata["task_index"])
}

// TestParallelLevelingBackfillsTaskMetadata pins that a task's own metadata
// survives onto a result produced by an escalation rung. An escalation rung
// builds its result from scratch, so without the backfill a leveled task would
// silently lose the labels an unleveled one carries.
func TestParallelLevelingBackfillsTaskMetadata(t *testing.T) {
	t.Parallel()

	orch := newLevelingTestOrchestrator(t)
	primary := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, lvlInvalidJSON)
	rung := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
	registerLevelingAgent(t, orch, "worker", primary,
		map[string]agent.LLMProvider{"frontier": rung})

	result, err := runLevelingParallel(t, orch, &loomv1.AgentTask{
		AgentId:  "worker",
		Prompt:   "do the thing",
		Metadata: map[string]string{"team": "billing", "priority": "high"},
		OutputPolicy: &loomv1.OutputPolicy{
			OutputSchema: lvlSchema,
			RetryPolicy:  &loomv1.OutputRetryPolicy{MaxRetries: 0},
		},
		LevelingPolicy: &loomv1.LevelingPolicy{
			Enabled: true,
			Ladder:  []*loomv1.LevelingRung{{Provider: "frontier", Model: lvlFrontierModel}},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.AgentResults, 1)
	got := result.AgentResults[0]

	assert.Equal(t, lvlValidJSON, got.Output, "precondition: the rung's output won")
	assert.Equal(t, 1, rung.count())
	assert.Equal(t, "billing", got.Metadata["team"], "task metadata is backfilled onto a rung result")
	assert.Equal(t, "high", got.Metadata["priority"])
	assert.Equal(t, "0", got.Metadata["task_index"])
	assert.Contains(t, got.Metadata, "agent_name",
		"the executor's own base keys are backfilled alongside the task metadata")
	assert.Equal(t, lvlFrontierModel, got.Metadata[levelingRungModelKey],
		"the rung's own labeling is not overwritten by the backfill")
}

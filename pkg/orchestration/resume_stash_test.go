// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap/zaptest"
)

// gatedOrchestrator builds the three-agent gated pipeline harness with the
// designer's reply under test control and an optional store.
func gatedOrchestrator(t *testing.T, designerReply string, store *communication.SharedMemoryStore) (*Orchestrator, *recordingLLMProvider, *recordingLLMProvider, *recordingLLMProvider) {
	t.Helper()
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), SharedMemory: store})
	analyst := newRecordingLLMProvider("requirements: orders table")
	designer := newRecordingLLMProvider(designerReply)
	executor := newRecordingLLMProvider("Table created successfully")
	orch.RegisterAgent("analyst", createMockAgent(t, "analyst", analyst))
	orch.RegisterAgent("designer", createMockAgent(t, "designer", designer))
	orch.RegisterAgent("executor", createMockAgent(t, "executor", executor))
	return orch, analyst, designer, executor
}

// With a store, every stage prompt carries the write rule, the first stage
// included: a key written without the workflow prefix is shared store state
// that no checkpoint carries and no reset clears, so the agent must hear the
// rule before its first write.
func TestPipeline_FirstStagePromptCarriesTheWriteHint(t *testing.T) {
	store, err := communication.NewSharedMemoryStore(observability.NewNoOpTracer(), zaptest.NewLogger(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	orch, analyst, designer, _ := gatedOrchestrator(t, "CREATE TABLE orders (...)", store)
	pattern := gatedPipelinePattern(nil)
	pattern.WorkflowId = "wf-hint"

	_, err = orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	first := analyst.recordedPrompts()
	require.Len(t, first, 1)
	assert.Contains(t, first[0], "## SHARED MEMORY", "the first stage gets the short header")
	assert.Contains(t, first[0], "should start with `wf-hint-`", "and the write rule")
	assert.NotContains(t, first[0], "Available keys", "with no prior output to list")

	second := designer.recordedPrompts()
	require.Len(t, second, 1)
	assert.Contains(t, second[0], "## AVAILABLE CONTEXT (SharedMemory)")
	assert.Contains(t, second[0], stageOutputKey("wf-hint", 1))
	assert.Contains(t, second[0], "should start with `wf-hint-`")
}

// The iterative executor renders the same header as the plain pipeline.
func TestIterativePipeline_FirstStagePromptCarriesTheWriteHint(t *testing.T) {
	store, err := communication.NewSharedMemoryStore(observability.NewNoOpTracer(), zaptest.NewLogger(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	orch, analyst, _, _ := gatedOrchestrator(t, "CREATE TABLE orders (...)", store)
	pattern := &loomv1.WorkflowPattern{WorkflowId: "wf-iter-hint", Pattern: &loomv1.WorkflowPattern_Iterative{Iterative: &loomv1.IterativeWorkflowPattern{
		Pipeline: gatedPipelinePattern(nil).GetPipeline(), MaxIterations: 1,
	}}}
	_, err = orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	first := analyst.recordedPrompts()
	require.Len(t, first, 1)
	assert.Contains(t, first[0], "should start with `wf-iter-hint-`")
	assert.NotContains(t, first[0], "Available keys")
}

// Without a store nothing is cut: a resumed iterative run hands the next
// stage the full output, not an 8 KiB copy with a notice naming a key nobody
// can fetch.
func TestIterativeGate_ResumeWithoutStore_PassesFullOutput(t *testing.T) {
	long := strings.Repeat("CREATE TABLE orders (...)\n", 400) // > MaxStageOutputBytes
	orch, _, _, executor := gatedOrchestrator(t, long, nil)
	pattern := &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Iterative{Iterative: &loomv1.IterativeWorkflowPattern{
		Pipeline: gatedPipelinePattern(&loomv1.HITLGate{}).GetPipeline(), MaxIterations: 1,
	}}}

	_, err := orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)

	_, err = orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	prompts := executor.recordedPrompts()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], long, "the full designer output reaches the executor")
	assert.NotContains(t, prompts[0], "OUTPUT TRUNCATED")
	assert.NotContains(t, prompts[0], "shared_memory_read")
}

// A resume whose re-seed fails (here: the store was closed between the
// suspension and the resume) must not point the next stage at a key that is
// not in the store; it gets the full output instead.
func TestPipelineGate_ResumeWithFailedReseed_PassesFullOutput(t *testing.T) {
	long := strings.Repeat("CREATE TABLE orders (...)\n", 400)
	store, err := communication.NewSharedMemoryStore(observability.NewNoOpTracer(), zaptest.NewLogger(t))
	require.NoError(t, err)
	orch, _, _, executor := gatedOrchestrator(t, long, store)
	pattern := gatedPipelinePattern(&loomv1.HITLGate{})

	_, err = orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	require.Equal(t, long, suspended.Checkpoint.StageSnapshots[1].FullOutput)

	require.NoError(t, store.Close()) // every Put now fails
	_, err = orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	prompts := executor.recordedPrompts()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], long, "the re-seed failed, so the executor gets the full output inline")
	assert.NotContains(t, prompts[0], "OUTPUT TRUNCATED")
}

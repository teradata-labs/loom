// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap/zaptest"
)

// gatedOrchestrator builds the three-agent gated pipeline harness with the
// designer's reply under test control and an optional store. The iterative
// executor runs a pattern itself only when its restart policy is enabled
// (iterativeGated sets that; without it the executor falls back to the plain
// pipeline and its own code goes untested); the MessageBus lets that restart
// subscription attach instead of logging that no bus is available.
func gatedOrchestrator(t *testing.T, designerReply string, store *communication.SharedMemoryStore) (*Orchestrator, *recordingLLMProvider, *recordingLLMProvider, *recordingLLMProvider) {
	t.Helper()
	tracer, logger := observability.NewNoOpTracer(), zaptest.NewLogger(t)
	bus := communication.NewMessageBus(communication.NewMemoryStore(0), nil, tracer, logger)
	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer, SharedMemory: store, MessageBus: bus})
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

// iterativeGated wraps the gated pipeline in an iterative pattern with
// restarts enabled, so the iterative executor (not its plain-pipeline
// fallback) runs it.
func iterativeGated(gate *loomv1.HITLGate, workflowID string) *loomv1.WorkflowPattern {
	return &loomv1.WorkflowPattern{WorkflowId: workflowID, Pattern: &loomv1.WorkflowPattern_Iterative{Iterative: &loomv1.IterativeWorkflowPattern{
		Pipeline:      gatedPipelinePattern(gate).GetPipeline(),
		MaxIterations: 1,
		RestartPolicy: &loomv1.RestartPolicy{Enabled: true},
	}}}
}

// The iterative executor renders the same header as the plain pipeline.
func TestIterativePipeline_FirstStagePromptCarriesTheWriteHint(t *testing.T) {
	store, err := communication.NewSharedMemoryStore(observability.NewNoOpTracer(), zaptest.NewLogger(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	orch, analyst, _, _ := gatedOrchestrator(t, "CREATE TABLE orders (...)", store)
	pattern := iterativeGated(nil, "wf-iter-hint")
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
	pattern := iterativeGated(&loomv1.HITLGate{}, "")

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

// On a REVISE resume with a store, the revise target's {{previous}} is the
// same cut copy (with the re-seeded key's notice) the original pass handed
// it, for the plain pipeline and for the iterative executor's own Resume.
func TestGate_ReviseResume_PreviousIsCutWhenReseeded(t *testing.T) {
	long := strings.Repeat("requirements: orders table\n", 400) // > MaxStageOutputBytes
	cases := []struct {
		name        string
		pattern     func() *loomv1.WorkflowPattern
		patternType string // the executor that ran, read from the checkpoint
	}{
		{"pipeline", func() *loomv1.WorkflowPattern { return gatedPipelinePattern(&loomv1.HITLGate{}) }, "pipeline"},
		{"iterative-own-resume", func() *loomv1.WorkflowPattern { return iterativeGated(&loomv1.HITLGate{}, "") }, "iterative_pipeline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := communication.NewSharedMemoryStore(observability.NewNoOpTracer(), zaptest.NewLogger(t))
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), SharedMemory: store})
			designer := newRecordingLLMProvider("CREATE TABLE orders (...)")
			orch.RegisterAgent("analyst", createMockAgent(t, "analyst", newRecordingLLMProvider(long)))
			orch.RegisterAgent("designer", createMockAgent(t, "designer", designer))
			orch.RegisterAgent("executor", createMockAgent(t, "executor", newRecordingLLMProvider("Table created successfully")))

			pattern := tc.pattern()
			_, err = orch.ExecutePattern(context.Background(), pattern)
			var suspended *WorkflowSuspended
			require.ErrorAs(t, err, &suspended)
			require.Equal(t, tc.patternType, suspended.Checkpoint.PatternType, "the intended executor ran, not a fallback")
			wfID := suspended.Checkpoint.WorkflowId
			require.NotEmpty(t, wfID)
			first := designer.recordedPrompts()
			require.Len(t, first, 1)
			assert.Contains(t, first[0], "OUTPUT TRUNCATED", "the original pass handed the designer the cut copy")

			_, err = orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
				&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "Use MULTISET"})
			var again *WorkflowSuspended
			require.ErrorAs(t, err, &again, "the revised designer output reaches the gate again")

			prompts := designer.recordedPrompts()
			require.Len(t, prompts, 2, "the designer ran again after REVISE")
			revised := prompts[1]
			assert.Contains(t, revised, "Use MULTISET", "the feedback reached the revise target")
			assert.Contains(t, revised, "OUTPUT TRUNCATED", "the re-seeded stage-1 output is handed over cut")
			assert.Contains(t, revised, stageOutputKey(wfID, 1), "the notice names the re-seeded key")
			assert.NotContains(t, revised, long, "the full analyst output must not be inline")
		})
	}
}

// An inline-handler REVISE on the plain pipeline derives the revise target's
// {{previous}} from the stash record, as the resume does: when that stage's
// stash Put failed (here: the store was closed before the run) the target
// gets the full output and no notice naming a key the store does not hold.
func TestPipelineGate_InLoopRevise_FailedStash_PassesFullOutput(t *testing.T) {
	long := strings.Repeat("requirements: orders table\n", 400) // > MaxStageOutputBytes
	store, err := communication.NewSharedMemoryStore(observability.NewNoOpTracer(), zaptest.NewLogger(t))
	require.NoError(t, err)
	require.NoError(t, store.Close()) // every Put fails: nothing is stashed
	handler := &scriptedHITLHandler{decisions: []*loomv1.GateDecision{
		{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "Use MULTISET"},
		{Action: loomv1.GateAction_GATE_ACTION_APPROVE},
	}}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), SharedMemory: store, HITLHandler: handler})
	designer := newRecordingLLMProvider("CREATE TABLE orders (...)", "CREATE MULTISET TABLE orders (...)")
	orch.RegisterAgent("analyst", createMockAgent(t, "analyst", newRecordingLLMProvider(long)))
	orch.RegisterAgent("designer", createMockAgent(t, "designer", designer))
	orch.RegisterAgent("executor", createMockAgent(t, "executor", newRecordingLLMProvider("Table created successfully")))

	pattern := gatedPipelinePattern(&loomv1.HITLGate{})
	pattern.WorkflowId = "wf-inloop-revise"
	_, err = orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	prompts := designer.recordedPrompts()
	require.Len(t, prompts, 2, "the designer ran again after the inline REVISE")
	assert.Contains(t, prompts[0], long)
	assert.NotContains(t, prompts[0], "OUTPUT TRUNCATED")
	assert.Contains(t, prompts[1], "Use MULTISET")
	assert.Contains(t, prompts[1], long, "the stash failed, so the revision gets the full output")
	assert.NotContains(t, prompts[1], "OUTPUT TRUNCATED")
	assert.NotContains(t, prompts[1], stageOutputKey("wf-inloop-revise", 1), "no notice naming a key the store does not hold")
}

// A REVISE resume on the iterative executor restores only the stages before
// the revise target: the discarded stage's old stash is not put back, so the
// revised stage never sees a stale copy of its own earlier output.
func TestIterativeGate_ReviseResume_DiscardsTheRevisedStagesStash(t *testing.T) {
	store, err := communication.NewSharedMemoryStore(observability.NewNoOpTracer(), zaptest.NewLogger(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	tracer, logger := observability.NewNoOpTracer(), zaptest.NewLogger(t)
	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer, SharedMemory: store})
	var designerCalls atomic.Int32
	var keysAtRevision []string
	orch.RegisterAgent("analyst", createMockAgent(t, "analyst", newRecordingLLMProvider("requirements: orders table")))
	orch.RegisterAgent("designer", createMockAgent(t, "designer", newFuncLLMProvider(func(string) (string, error) {
		if designerCalls.Add(1) == 2 {
			keysAtRevision = workflowMemoryKeys(t, store)
		}
		return "CREATE TABLE orders (...)", nil
	})))
	orch.RegisterAgent("executor", createMockAgent(t, "executor", newRecordingLLMProvider("Table created successfully")))

	pattern := iterativeGated(&loomv1.HITLGate{}, "wf-revise-trim")
	_, err = orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	require.Equal(t, "iterative_pipeline", suspended.Checkpoint.PatternType)

	_, err = orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "Use MULTISET"})
	var again *WorkflowSuspended
	require.ErrorAs(t, err, &again)
	require.Equal(t, int32(2), designerCalls.Load())
	assert.Contains(t, keysAtRevision, stageOutputKey("wf-revise-trim", 1), "the kept stage's stash is re-seeded")
	assert.NotContains(t, keysAtRevision, stageOutputKey("wf-revise-trim", 2), "the revised stage's old stash is not restored")
}

// A plain-pipeline REVISE resume re-seeds only the stages it keeps, as the
// iterative executor's Resume does: when the revise target re-runs, the store
// holds the kept stage's stash and not the revised stage's old one.
func TestPipelineGate_ReviseResume_DoesNotReseedTheRevisedStages(t *testing.T) {
	store, err := communication.NewSharedMemoryStore(observability.NewNoOpTracer(), zaptest.NewLogger(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), SharedMemory: store})
	var designerCalls atomic.Int32
	var keysAtRevision []string
	orch.RegisterAgent("analyst", createMockAgent(t, "analyst", newRecordingLLMProvider("requirements: orders table")))
	orch.RegisterAgent("designer", createMockAgent(t, "designer", newFuncLLMProvider(func(string) (string, error) {
		if designerCalls.Add(1) == 2 {
			keysAtRevision = workflowMemoryKeys(t, store)
		}
		return "CREATE TABLE orders (...)", nil
	})))
	orch.RegisterAgent("executor", createMockAgent(t, "executor", newRecordingLLMProvider("Table created successfully")))
	pattern := gatedPipelinePattern(&loomv1.HITLGate{})
	pattern.WorkflowId = "wf-plain-trim"

	_, err = orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	require.Equal(t, "pipeline", suspended.Checkpoint.PatternType)
	require.Len(t, suspended.Checkpoint.StageSnapshots, 2, "the analyst and designer outputs are checkpointed")

	_, err = orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "Use MULTISET"})
	var again *WorkflowSuspended
	require.ErrorAs(t, err, &again, "the revised designer output reaches the gate again")
	require.Equal(t, int32(2), designerCalls.Load())
	assert.Contains(t, keysAtRevision, stageOutputKey("wf-plain-trim", 1), "the kept stage's stash is re-seeded")
	assert.NotContains(t, keysAtRevision, stageOutputKey("wf-plain-trim", 2), "the revised stage's old stash is not restored")
}

// A REVISE resume on the iterative executor replays only the kept stages
// into the structured context: the revise target's prompt carries the
// analyst's stage-1 entry and not its own superseded stage-2 entry under
// {{structured_context}}.
func TestIterativeGate_ReviseResume_ReplaysOnlyKeptStagesIntoStructuredContext(t *testing.T) {
	for _, withStore := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-store", true: "with-store"}[withStore], func(t *testing.T) {
			tracer, logger := observability.NewNoOpTracer(), zaptest.NewLogger(t)
			bus := communication.NewMessageBus(communication.NewMemoryStore(0), nil, tracer, logger)
			cfg := Config{Logger: logger, Tracer: tracer, MessageBus: bus}
			if withStore {
				store, err := communication.NewSharedMemoryStore(tracer, logger)
				require.NoError(t, err)
				t.Cleanup(func() { _ = store.Close() })
				cfg.SharedMemory = store
			}
			orch := NewOrchestrator(cfg)
			designer := newRecordingLLMProvider(`{"outputs":{"ddl":"CREATE TABLE orders (...)"}}`)
			orch.RegisterAgent("analyst", createMockAgent(t, "analyst", newRecordingLLMProvider(`{"outputs":{"requirements":["orders table"]}}`)))
			orch.RegisterAgent("designer", createMockAgent(t, "designer", designer))
			orch.RegisterAgent("executor", createMockAgent(t, "executor", newRecordingLLMProvider("Table created successfully")))
			pattern := &loomv1.WorkflowPattern{WorkflowId: "wf-replay", Pattern: &loomv1.WorkflowPattern_Iterative{Iterative: &loomv1.IterativeWorkflowPattern{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "Create an orders table",
					Stages: []*loomv1.PipelineStage{
						{AgentId: "analyst", PromptTemplate: "Analyze: {{previous}}"},
						{AgentId: "designer", PromptTemplate: "Context: {{structured_context}}\nDesign DDL for: {{previous}}", HitlGate: &loomv1.HITLGate{}},
						{AgentId: "executor", PromptTemplate: "Execute exactly: {{previous}}"},
					},
				},
				MaxIterations: 1,
				RestartPolicy: &loomv1.RestartPolicy{Enabled: true},
			}}}

			_, err := orch.ExecutePattern(context.Background(), pattern)
			var suspended *WorkflowSuspended
			require.ErrorAs(t, err, &suspended)
			require.Equal(t, "iterative_pipeline", suspended.Checkpoint.PatternType, "the iterative executor ran, not a fallback")
			first := designer.recordedPrompts()
			require.Len(t, first, 1)
			assert.Contains(t, first[0], `"stage-1"`, "the analyst's JSON is in the structured context on the first pass")

			_, err = orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
				&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "Use MULTISET"})
			var again *WorkflowSuspended
			require.ErrorAs(t, err, &again)
			prompts := designer.recordedPrompts()
			require.Len(t, prompts, 2, "the designer ran again after REVISE")
			assert.Contains(t, prompts[1], `"stage-1"`, "the kept stage is replayed into the structured context")
			assert.NotContains(t, prompts[1], `"stage-2"`, "the revised stage's superseded output is not replayed")
		})
	}
}

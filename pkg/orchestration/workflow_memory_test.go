// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap/zaptest"
)

func TestManagedKeyWorkflow(t *testing.T) {
	assert.Equal(t, "pipeline-7f3a9c1e-stage-2-output", stageOutputKey("pipeline-7f3a9c1e", 2))
	tests := map[string]string{
		"pipeline-7f3a9c1e-stage-2-output":         "pipeline-7f3a9c1e",
		"fork-join-ab12cd34-branch-3-output":       "fork-join-ab12cd34",
		"debate-9988aabb-round-2-position-skeptic": "debate-9988aabb",
		"my-custom-id-stage-1-output":              "my-custom-id",
		"stage-1-output":                           "", // legacy unscoped key
		"analysis-notes":                           "", // agent-written
		"pipeline-7f3a9c1e-stage-x-output":         "",
	}
	for key, want := range tests {
		assert.Equal(t, want, managedKeyWorkflow(key), key)
	}
	assert.True(t, isStageOutputKey("wf-a-stage-1-output", "wf-a"))
	assert.False(t, isStageOutputKey("wf-a-stage-1-output", "wf"), "prefix must be the whole workflow id")
	assert.False(t, isStageOutputKey("wf-a-branch-1-output", "wf-a"))
}

func seedWorkflowKeys(t *testing.T, store *communication.SharedMemoryStore, keys ...string) {
	t.Helper()
	for _, k := range keys {
		_, err := store.Put(context.Background(), &loomv1.PutSharedMemoryRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
			Key:       k, Value: []byte("v:" + k), AgentId: "seed",
		})
		require.NoError(t, err)
	}
}

func TestWorkflowOwnsKey(t *testing.T) {
	tests := []struct {
		key, workflow string
		owns          bool
	}{
		{"wf-a-stage-1-output", "wf-a", true},
		{"wf-a-branch-2-output", "wf-a", true},
		{"wf-a-round-1-position-x", "wf-a", true},
		{"wf-a-notes", "wf-a", true},
		{"wf-a-b-stage-1-output", "wf-a", false}, // managed key of another run, even with a matching prefix
		{"wf-b-stage-1-output", "wf-a", false},
		{"wf-b-notes", "wf-a", false},
		{"analysis-notes", "wf-a", false}, // no prefix: shared, nobody's
		{"stage-1-output", "wf-a", false}, // legacy unscoped stash
		{"wf-a", "wf-a", false},
		{"wf-a-notes", "", false},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.owns, workflowOwnsKey(tc.key, tc.workflow), "%s owned by %q", tc.key, tc.workflow)
	}
}

func TestSnapshotWorkflowMemory_OnlyThisWorkflowsEntriesAndAgentKeys(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	seedWorkflowKeys(t, orch.sharedMemory,
		"wf-a-stage-1-output", "wf-a-branch-1-output", "wf-a-round-1-position-x", "wf-a-notes",
		"wf-b-stage-1-output", "wf-b-branch-1-output", "wf-b-round-1-position-y", "wf-b-notes",
		"stage-1-output", "analysis-notes")
	entries := snapshotWorkflowMemory(context.Background(), orch, "wf-a")
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, e.Key)
	}
	assert.ElementsMatch(t, []string{"wf-a-branch-1-output", "wf-a-round-1-position-x", "wf-a-notes"}, keys,
		"own stage keys are re-seeded from the stage snapshots; other workflows' keys, prefixed or not, are not this workflow's data")
}

func TestRestoreWorkflowMemory_ReseedsScopedStageKeys(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	ckpt := &loomv1.WorkflowCheckpoint{
		WorkflowId:     "pipeline-cafe0001",
		StageSnapshots: []*loomv1.CheckpointStageSnapshot{{AgentId: "a", FullOutput: "one"}, {AgentId: "b", FullOutput: "two"}},
		SharedMemory:   []*loomv1.CheckpointMemoryEntry{{Key: "analysis-notes", Value: []byte("n"), AgentId: "a"}},
	}
	restoreWorkflowMemory(context.Background(), orch, ckpt, ckpt.WorkflowId)
	assert.ElementsMatch(t, []string{"pipeline-cafe0001-stage-1-output", "pipeline-cafe0001-stage-2-output", "analysis-notes"},
		workflowMemoryKeys(t, orch.sharedMemory))
}

func TestCleanupWorkflowMemory_RemovesOnlyThisWorkflowsManagedKeys(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	seedWorkflowKeys(t, orch.sharedMemory,
		"wf-a-stage-1-output", "wf-a-branch-1-output", "wf-a-round-1-position-x",
		"wf-b-stage-1-output", "stage-1-output", "analysis-notes", "wf-a-notes")
	orch.cleanupWorkflowMemory(context.Background(), "wf-a")
	assert.ElementsMatch(t, []string{"wf-b-stage-1-output", "stage-1-output", "analysis-notes", "wf-a-notes"},
		workflowMemoryKeys(t, orch.sharedMemory), "agent-written keys and other workflows' keys stay")

	seedWorkflowKeys(t, orch.sharedMemory, "wf-b-branch-1-output")
	orch.cleanupWorkflowMemory(context.Background(), "wf-b")
	assert.NotContains(t, workflowMemoryKeys(t, orch.sharedMemory), "wf-b-branch-1-output")
	assert.NotContains(t, workflowMemoryKeys(t, orch.sharedMemory), "wf-b-stage-1-output")

	noStore := newJoinTestOrchestrator(t, nil, false)
	noStore.cleanupWorkflowMemory(context.Background(), "wf-a") // no store: no-op, no panic
}

func TestIterativeReset_LeavesOtherWorkflowsManagedKeys(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	seedWorkflowKeys(t, orch.sharedMemory,
		"iterative-0001-stage-1-output", "iterative-0001-stage-2-output", "iterative-0001-notes",
		"wf-other-stage-1-output", "wf-other-branch-1-output", "wf-other-notes", "debate-x-round-1-position-a",
		"analysis-notes", "stage-1-output")
	e := NewIterativePipelineExecutor(orch, &loomv1.IterativeWorkflowPattern{Pipeline: &loomv1.PipelinePattern{}}, nil, "iterative-0001")
	require.NoError(t, e.resetWorkflowNamespace(context.Background()))
	assert.ElementsMatch(t, []string{"wf-other-stage-1-output", "wf-other-branch-1-output", "wf-other-notes", "debate-x-round-1-position-a", "analysis-notes"},
		workflowMemoryKeys(t, orch.sharedMemory),
		"the reset clears this run's own keys (its stash, its agents' prefixed keys, legacy unscoped stage keys); other workflows' keys and unprefixed keys stay")
}

// Two pipelines share one store, as every workflow on a looms server does. A's
// second stage is told the key of A's first stage and fetches A's text even
// though B's first stage completed in between.
func TestConcurrentPipelinesOnOneStore_KeepTheirOwnStageOutputs(t *testing.T) {
	store, err := communication.NewSharedMemoryStore(observability.NewNoOpTracer(), zaptest.NewLogger(t))
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	newOrch := func() *Orchestrator {
		return NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), SharedMemory: store})
	}
	orchA, orchB := newOrch(), newOrch()
	keyRe := regexp.MustCompile("`([a-z0-9-]+-stage-1-output)`")

	aStage2Entered, releaseA := make(chan struct{}), make(chan struct{})
	var (
		mu                 sync.Mutex
		aKey, aFetched     string
		aStage2EnteredOnce sync.Once
	)
	orchA.RegisterAgent("a1", createMockAgent(t, "a1", newFuncLLMProvider(func(string) (string, error) { return bigText("A-ONE", 20_000), nil })))
	orchA.RegisterAgent("a2", createMockAgent(t, "a2", newFuncLLMProvider(func(p string) (string, error) {
		aStage2EnteredOnce.Do(func() { close(aStage2Entered) })
		<-releaseA
		m := keyRe.FindStringSubmatch(p)
		if m == nil {
			return "", nil
		}
		got, gerr := store.Get(context.Background(), &loomv1.GetSharedMemoryRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW, Key: m[1], AgentId: "a2"})
		if gerr != nil || !got.Found {
			return "", gerr
		}
		mu.Lock()
		aKey, aFetched = m[1], string(got.Value.Value)
		mu.Unlock()
		return "A done", nil
	})))
	orchB.RegisterAgent("b1", createMockAgent(t, "b1", newFuncLLMProvider(func(string) (string, error) { return bigText("B-ONE", 20_000), nil })))
	orchB.RegisterAgent("b2", createMockAgent(t, "b2", newFuncLLMProvider(func(string) (string, error) { return "B done", nil })))

	pipeline := func(s1, s2 string) *loomv1.WorkflowPattern {
		return &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Pipeline{Pipeline: &loomv1.PipelinePattern{
			InitialPrompt: "go",
			Stages:        []*loomv1.PipelineStage{{AgentId: s1, PromptTemplate: "Research"}, {AgentId: s2, PromptTemplate: "Compile: {{previous}}"}},
		}}}
	}

	var wg sync.WaitGroup
	var aErr error
	wg.Add(1)
	go func() { defer wg.Done(); _, aErr = orchA.ExecutePattern(context.Background(), pipeline("a1", "a2")) }()
	<-aStage2Entered
	_, bErr := orchB.ExecutePattern(context.Background(), pipeline("b1", "b2"))
	require.NoError(t, bErr)
	close(releaseA)
	wg.Wait()
	require.NoError(t, aErr)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, aKey, "A's stage-2 prompt names A's stage-1 key")
	assert.True(t, strings.HasPrefix(aKey, "pipeline-"), aKey)
	assert.True(t, strings.HasPrefix(aFetched, "A-ONE"), "A fetched its own stage 1, got %q", aFetched[:min(40, len(aFetched))])
	for _, k := range workflowMemoryKeys(t, store) {
		assert.Empty(t, managedKeyWorkflow(k), "both runs cleaned up their stash; found %s", k)
	}
}

// A gRPC server never installs a HITL handler and keeps no checkpoint, so a
// gated pipeline it runs suspends and is never resumed. The stash must not
// outlive the suspension: the checkpoint carries the stage outputs and the
// resume re-seeds the keys from it.
func TestPipelineGate_SuspensionDropsStashAndResumeReseedsIt(t *testing.T) {
	t.Parallel()

	store, err := communication.NewSharedMemoryStore(observability.NewNoOpTracer(), zaptest.NewLogger(t))
	require.NoError(t, err)
	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		SharedMemory: store,
		// No HITLHandler: every gate suspends, as on the gRPC server.
	})
	var keysSeenByExecutor []string
	orch.RegisterAgent("analyst", createMockAgent(t, "analyst", newRecordingLLMProvider("requirements: orders table")))
	orch.RegisterAgent("designer", createMockAgent(t, "designer", newRecordingLLMProvider("CREATE TABLE orders (...)")))
	orch.RegisterAgent("executor", createMockAgent(t, "executor", newFuncLLMProvider(func(string) (string, error) {
		keysSeenByExecutor = workflowMemoryKeys(t, store)
		return "Table created successfully", nil
	})))

	pattern := gatedPipelinePattern(&loomv1.HITLGate{})
	_, err = orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	wfID := suspended.Checkpoint.WorkflowId
	require.NotEmpty(t, wfID)
	require.Len(t, suspended.Checkpoint.StageSnapshots, 2)
	assert.Equal(t, "CREATE TABLE orders (...)", suspended.Checkpoint.StageSnapshots[1].FullOutput,
		"the checkpoint, not the store, carries the stage outputs across the pause")

	for _, key := range workflowMemoryKeys(t, store) {
		assert.NotEqual(t, wfID, managedKeyWorkflow(key), "managed key %q survived the suspension", key)
	}

	result, err := orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	assert.Equal(t, "Table created successfully", result.MergedOutput)
	assert.ElementsMatch(t, []string{stageOutputKey(wfID, 1), stageOutputKey(wfID, 2)}, keysSeenByExecutor,
		"the resume re-seeds the earlier stages' keys before the next stage runs")

	for _, key := range workflowMemoryKeys(t, store) {
		assert.NotEqual(t, wfID, managedKeyWorkflow(key), "managed key %q survived the resumed run", key)
	}
}

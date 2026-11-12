// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap/zaptest"
)

// topicProbeLLM records the restart topic the stage turn's context carries
// (what the publish_restart tool would read) and answers through fn.
type topicProbeLLM struct {
	*funcLLMProvider
	mu     sync.Mutex
	topics []string
}

func newTopicProbeLLM(fn func(prompt string) (string, error)) *topicProbeLLM {
	return &topicProbeLLM{funcLLMProvider: newFuncLLMProvider(fn)}
}

func (m *topicProbeLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	m.topics = append(m.topics, communication.RestartTopicFromContext(ctx))
	m.mu.Unlock()
	return m.funcLLMProvider.Chat(ctx, messages, tools)
}

func (m *topicProbeLLM) seen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.topics...)
}

func publishRestartOn(t *testing.T, bus *communication.MessageBus, topic, requester, target string) {
	t.Helper()
	payload, err := json.Marshal(&loomv1.RestartRequest{RequesterStageId: requester, TargetStageId: target, Reason: "test", TimestampMs: time.Now().UnixMilli()})
	require.NoError(t, err)
	_, _, err = bus.Publish(context.Background(), topic, &loomv1.BusMessage{
		Id: "restart-" + topic, Topic: topic, FromAgent: requester,
		Payload:  &loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: payload}},
		Metadata: map[string]string{"type": "restart_request"},
	})
	require.NoError(t, err)
}

// waitForQueuedRestart blocks until the executor's subscription has forwarded
// a request into its queue (the executor drains it only after the running
// stage returns), or gives up. Returns whether one arrived.
func waitForQueuedRestart(e *IterativePipelineExecutor, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if len(e.restartRequests) > 0 {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func iterativeRestartPattern(stages []*loomv1.PipelineStage, restartable, triggers []string, reset bool) *loomv1.IterativeWorkflowPattern {
	return &loomv1.IterativeWorkflowPattern{
		Pipeline:        &loomv1.PipelinePattern{InitialPrompt: "go", Stages: stages},
		MaxIterations:   3,
		RestartPolicy:   &loomv1.RestartPolicy{Enabled: true, RestartableStages: restartable, ResetSharedMemory: reset},
		RestartTriggers: triggers,
	}
}

type twoRunOutcome struct {
	resA, resB   *loomv1.WorkflowResult
	bStage1Calls int32
	aTopics      []string
	bQueued      bool
}

// runTwoIterativeRuns executes two iterative runs with the same stage ids
// (s1, s2) on one bus and one store, as every workflow on a looms server
// does. A's stage 2 publishes a restart (requester s2, target s1) on topic
// once B is mid-run, then waits until the request is queued at A (and gives
// B a moment to show whether it was queued there too); B's stage 2 waits for
// A's publish before returning.
func runTwoIterativeRuns(t *testing.T, patternA, patternB *loomv1.IterativeWorkflowPattern, topic string) twoRunOutcome {
	t.Helper()
	tracer, logger := observability.NewNoOpTracer(), zaptest.NewLogger(t)
	bus := communication.NewMessageBus(communication.NewMemoryStore(0), nil, tracer, logger)
	store, err := communication.NewSharedMemoryStore(tracer, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	orchA := NewOrchestrator(Config{Logger: logger, Tracer: tracer, SharedMemory: store, MessageBus: bus})
	orchB := NewOrchestrator(Config{Logger: logger, Tracer: tracer, SharedMemory: store, MessageBus: bus})
	execA := NewIterativePipelineExecutor(orchA, patternA, bus, "wf-a")
	execB := NewIterativePipelineExecutor(orchB, patternB, bus, "wf-b")

	bEntered, aPublished := make(chan struct{}), make(chan struct{})
	var bEnteredOnce, aPublishedOnce sync.Once
	var out twoRunOutcome
	var bStage1Calls atomic.Int32
	var bQueued atomic.Bool

	aStage1 := newTopicProbeLLM(func(string) (string, error) { return "A one", nil })
	orchA.RegisterAgent("s1", createMockAgent(t, "s1", aStage1))
	orchA.RegisterAgent("s2", createMockAgent(t, "s2", newFuncLLMProvider(func(string) (string, error) {
		aPublishedOnce.Do(func() {
			<-bEntered // B is mid-run and subscribed
			publishRestartOn(t, bus, topic, "s2", "s1")
			waitForQueuedRestart(execA, 5*time.Second)
			bQueued.Store(waitForQueuedRestart(execB, 200*time.Millisecond))
			close(aPublished)
		})
		return "A two", nil
	})))
	orchB.RegisterAgent("s1", createMockAgent(t, "s1", newFuncLLMProvider(func(string) (string, error) {
		bStage1Calls.Add(1)
		return "B one", nil
	})))
	orchB.RegisterAgent("s2", createMockAgent(t, "s2", newFuncLLMProvider(func(string) (string, error) {
		bEnteredOnce.Do(func() { close(bEntered) })
		<-aPublished
		return "B two", nil
	})))

	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() { defer wg.Done(); out.resA, errA = execA.Execute(context.Background()) }()
	go func() { defer wg.Done(); out.resB, errB = execB.Execute(context.Background()) }()
	wg.Wait()
	require.NoError(t, errA)
	require.NoError(t, errB)
	out.bStage1Calls = bStage1Calls.Load()
	out.aTopics = aStage1.seen()
	out.bQueued = bQueued.Load()
	return out
}

var twoStageRestartStages = []*loomv1.PipelineStage{
	{AgentId: "s1", PromptTemplate: "Research"},
	{AgentId: "s2", PromptTemplate: "Review: {{previous}}"},
}

func assertOnlyARestarted(t *testing.T, out twoRunOutcome) {
	t.Helper()
	assert.Equal(t, "2", out.resA.Metadata["iterations_used"], "A restarted once on its own request (the counter starts at 1)")
	assert.Equal(t, "1", out.resB.Metadata["iterations_used"], "B must not restart on A's request")
	assert.Equal(t, int32(1), out.bStage1Calls, "B's stage 1 ran once")
	assert.False(t, out.bQueued, "B's executor never received A's request")
	require.NotEmpty(t, out.aTopics)
	for _, topic := range out.aTopics {
		assert.Equal(t, communication.RestartTopicFor("wf-a"), topic, "A's stage turns carry A's restart topic for the publish_restart tool")
	}
}

// Two iterative runs with the same stage ids share one bus. A's stage 2
// publishes a restart on A's own topic: A restarts, B does not, and the topic
// A's agents see on their context is A's.
func TestIterativeRuns_RestartRequestsStayWithTheirRun(t *testing.T) {
	out := runTwoIterativeRuns(t,
		iterativeRestartPattern(twoStageRestartStages, nil, nil, false),
		iterativeRestartPattern(twoStageRestartStages, nil, nil, false),
		communication.RestartTopicFor("wf-a"))
	assertOnlyARestarted(t, out)
}

const iterativeYAMLWithoutRestartTopic = `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: isolated-iterative
spec:
  type: iterative
  max_iterations: 3
  restart_policy:
    enabled: true
  pipeline:
    initial_prompt: go
    stages:
      - agent_id: s1
        prompt_template: "Research"
      - agent_id: s2
        prompt_template: "Review: {{previous}}"
`

// The same isolation must hold for YAML-loaded workflows, which is what
// looms serve and looms workflow run execute: an omitted restart_topic stays
// empty at load so the executor's per-run default applies.
func TestIterativeRuns_YAMLWithoutRestartTopic_StayIsolated(t *testing.T) {
	load := func() *loomv1.IterativeWorkflowPattern {
		pattern, err := LoadWorkflowFromYAMLBytes([]byte(iterativeYAMLWithoutRestartTopic))
		require.NoError(t, err)
		require.Equal(t, "", pattern.GetIterative().RestartTopic, "the loader must not fill a shared default topic")
		return pattern.GetIterative()
	}
	out := runTwoIterativeRuns(t, load(), load(), communication.RestartTopicFor("wf-a"))
	assertOnlyARestarted(t, out)
}

// An explicit restart_topic is shared by every run that sets it, as
// documented: a request on it reaches both runs.
func TestIterativeRuns_ExplicitSharedTopicReachesEveryRun(t *testing.T) {
	a := iterativeRestartPattern(twoStageRestartStages, nil, nil, false)
	b := iterativeRestartPattern(twoStageRestartStages, nil, nil, false)
	a.RestartTopic, b.RestartTopic = "team.restart", "team.restart"
	out := runTwoIterativeRuns(t, a, b, "team.restart")
	assert.Equal(t, "2", out.resA.Metadata["iterations_used"])
	assert.True(t, out.bQueued, "B shares the explicit topic and receives the request")
	for _, topic := range out.aTopics {
		assert.Equal(t, "team.restart", topic, "the explicit topic is what the publish_restart tool sees")
	}
}

// A restart with reset_shared_memory clears the run's stash, but the stages
// the restart keeps are still referenced by the retained {{previous}} copies
// and history entries: their stashes come back right after the reset.
func TestIterativeReset_ReseedsRetainedStages(t *testing.T) {
	tracer, logger := observability.NewNoOpTracer(), zaptest.NewLogger(t)
	bus := communication.NewMessageBus(communication.NewMemoryStore(0), nil, tracer, logger)
	store, err := communication.NewSharedMemoryStore(tracer, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	seedWorkflowKeys(t, store, "wf-other-notes", "analysis-notes")

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer, SharedMemory: store, MessageBus: bus})
	stages := []*loomv1.PipelineStage{
		{AgentId: "s1", PromptTemplate: "Research"},
		{AgentId: "s2", PromptTemplate: "Review: {{previous}}"},
		{AgentId: "s3", PromptTemplate: "Judge: {{previous}}"},
	}
	exec := NewIterativePipelineExecutor(orch, iterativeRestartPattern(stages, []string{"s2"}, []string{"s3"}, true), bus, "wf-reseed")

	var s2Calls atomic.Int32
	var keysAtSecondS2 []string
	var publishOnce sync.Once
	orch.RegisterAgent("s1", createMockAgent(t, "s1", newFuncLLMProvider(func(string) (string, error) { return "one", nil })))
	orch.RegisterAgent("s2", createMockAgent(t, "s2", newFuncLLMProvider(func(string) (string, error) {
		if s2Calls.Add(1) == 2 {
			keysAtSecondS2 = workflowMemoryKeys(t, store)
		}
		return "two", nil
	})))
	orch.RegisterAgent("s3", createMockAgent(t, "s3", newFuncLLMProvider(func(string) (string, error) {
		publishOnce.Do(func() {
			publishRestartOn(t, bus, communication.RestartTopicFor("wf-reseed"), "s3", "s2")
			require.True(t, waitForQueuedRestart(exec, 5*time.Second), "the restart request reached the executor's queue")
		})
		return "three", nil
	})))

	res, err := exec.Execute(context.Background())
	require.NoError(t, err)
	require.Equal(t, "2", res.Metadata["iterations_used"], "s3 restarted s2 once")
	require.Equal(t, int32(2), s2Calls.Load())

	assert.Contains(t, keysAtSecondS2, stageOutputKey("wf-reseed", 1), "the retained stage's stash is back before the restarted stage runs")
	assert.NotContains(t, keysAtSecondS2, stageOutputKey("wf-reseed", 2), "the restarted stage's old stash was reset")
	assert.Contains(t, keysAtSecondS2, "wf-other-notes", "another workflow's prefixed key survives the reset")
	assert.Contains(t, keysAtSecondS2, "analysis-notes", "an unprefixed key survives the reset")
}

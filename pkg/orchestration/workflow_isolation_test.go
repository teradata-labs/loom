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

func iterativeRestartPattern(stages []*loomv1.PipelineStage, restartable, triggers []string, reset bool) *loomv1.IterativeWorkflowPattern {
	return &loomv1.IterativeWorkflowPattern{
		Pipeline:        &loomv1.PipelinePattern{InitialPrompt: "go", Stages: stages},
		MaxIterations:   3,
		RestartPolicy:   &loomv1.RestartPolicy{Enabled: true, RestartableStages: restartable, ResetSharedMemory: reset},
		RestartTriggers: triggers,
	}
}

// Two iterative runs with the same stage ids share one bus, as every workflow
// on a looms server does. A's stage 2 publishes a restart on A's own topic:
// A restarts, B does not, and the topic A's agents see on their context is
// A's.
func TestIterativeRuns_RestartRequestsStayWithTheirRun(t *testing.T) {
	tracer, logger := observability.NewNoOpTracer(), zaptest.NewLogger(t)
	bus := communication.NewMessageBus(communication.NewMemoryStore(0), nil, tracer, logger)
	store, err := communication.NewSharedMemoryStore(tracer, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	bEntered, aPublished := make(chan struct{}), make(chan struct{})
	var bEnteredOnce, aPublishedOnce sync.Once
	var bStage1Calls atomic.Int32

	aStage1 := newTopicProbeLLM(func(string) (string, error) { return "A one", nil })
	orchA := NewOrchestrator(Config{Logger: logger, Tracer: tracer, SharedMemory: store, MessageBus: bus})
	orchA.RegisterAgent("s1", createMockAgent(t, "s1", aStage1))
	orchA.RegisterAgent("s2", createMockAgent(t, "s2", newFuncLLMProvider(func(string) (string, error) {
		aPublishedOnce.Do(func() {
			<-bEntered // B is mid-run and subscribed
			publishRestartOn(t, bus, communication.RestartTopicFor("wf-a"), "s2", "s1")
			close(aPublished)
			time.Sleep(500 * time.Millisecond) // let the subscription forward the request before this stage returns
		})
		return "A two", nil
	})))

	orchB := NewOrchestrator(Config{Logger: logger, Tracer: tracer, SharedMemory: store, MessageBus: bus})
	orchB.RegisterAgent("s1", createMockAgent(t, "s1", newFuncLLMProvider(func(string) (string, error) {
		bStage1Calls.Add(1)
		return "B one", nil
	})))
	orchB.RegisterAgent("s2", createMockAgent(t, "s2", newFuncLLMProvider(func(string) (string, error) {
		bEnteredOnce.Do(func() { close(bEntered) })
		<-aPublished
		time.Sleep(500 * time.Millisecond)
		return "B two", nil
	})))

	stages := []*loomv1.PipelineStage{{AgentId: "s1", PromptTemplate: "Research"}, {AgentId: "s2", PromptTemplate: "Review: {{previous}}"}}
	var wg sync.WaitGroup
	var resA, resB *loomv1.WorkflowResult
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		resA, errA = NewIterativePipelineExecutor(orchA, iterativeRestartPattern(stages, nil, nil, false), bus, "wf-a").Execute(context.Background())
	}()
	go func() {
		defer wg.Done()
		resB, errB = NewIterativePipelineExecutor(orchB, iterativeRestartPattern(stages, nil, nil, false), bus, "wf-b").Execute(context.Background())
	}()
	wg.Wait()
	require.NoError(t, errA)
	require.NoError(t, errB)

	assert.Equal(t, "2", resA.Metadata["iterations_used"], "A restarted once on its own request (the counter starts at 1)")
	assert.Equal(t, "1", resB.Metadata["iterations_used"], "B must not restart on A's request")
	assert.Equal(t, int32(1), bStage1Calls.Load(), "B's stage 1 ran once")
	for _, topic := range aStage1.seen() {
		assert.Equal(t, communication.RestartTopicFor("wf-a"), topic, "A's stage turns carry A's restart topic for the publish_restart tool")
	}
	assert.NotEmpty(t, aStage1.seen())
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
			time.Sleep(500 * time.Millisecond)
		})
		return "three", nil
	})))

	stages := []*loomv1.PipelineStage{
		{AgentId: "s1", PromptTemplate: "Research"},
		{AgentId: "s2", PromptTemplate: "Review: {{previous}}"},
		{AgentId: "s3", PromptTemplate: "Judge: {{previous}}"},
	}
	res, err := NewIterativePipelineExecutor(orch, iterativeRestartPattern(stages, []string{"s2"}, []string{"s3"}, true), bus, "wf-reseed").Execute(context.Background())
	require.NoError(t, err)
	require.Equal(t, "2", res.Metadata["iterations_used"], "s3 restarted s2 once")
	require.Equal(t, int32(2), s2Calls.Load())

	assert.Contains(t, keysAtSecondS2, stageOutputKey("wf-reseed", 1), "the retained stage's stash is back before the restarted stage runs")
	assert.NotContains(t, keysAtSecondS2, stageOutputKey("wf-reseed", 2), "the restarted stage's old stash was reset")
	assert.Contains(t, keysAtSecondS2, "wf-other-notes", "another workflow's prefixed key survives the reset")
	assert.Contains(t, keysAtSecondS2, "analysis-notes", "an unprefixed key survives the reset")
}

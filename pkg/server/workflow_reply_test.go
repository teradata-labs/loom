// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// assertParticipantRejectionLogged checks that exactly one Warn entry records
// a tools-disabled rejection for the given operation and participant.
func assertParticipantRejectionLogged(t *testing.T, logs *observer.ObservedLogs, operation, workflow, participant, role string) {
	t.Helper()
	var matched []observer.LoggedEntry
	for _, entry := range logs.FilterMessage("Rejected workflow participant with tools disabled").All() {
		if entry.ContextMap()["operation"] == operation {
			matched = append(matched, entry)
		}
	}
	require.Len(t, matched, 1, "%s must log its rejection once", operation)
	entry := matched[0]
	assert.Equal(t, zap.WarnLevel, entry.Level)
	fields := entry.ContextMap()
	assert.Equal(t, workflow, fields["workflow"])
	assert.Equal(t, participant, fields["participant"])
	assert.Equal(t, role, fields["role"])
}

// replyingLLM is a worker's brain that obeys the team block: when a message
// arrives it answers by calling send_message back to the sender, because turn
// text alone is delivered nowhere. It records the system prompt it was given so
// a test can assert what the worker was actually told.
type replyingLLM struct {
	mu           sync.Mutex
	systemPrompt string
	sawMessage   string
	replied      bool
	seenReplies  int
}

func (m *replyingLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	for _, msg := range messages {
		if msg.Role == "system" && m.systemPrompt == "" {
			m.systemPrompt = msg.Content
		}
	}
	last := messages[len(messages)-1]
	incoming := strings.Contains(last.Content, "[MESSAGE FROM ")
	if incoming {
		m.sawMessage = last.Content
		m.seenReplies++
	}
	alreadyReplied := m.replied
	if incoming && !alreadyReplied {
		m.replied = true
	}
	m.mu.Unlock()

	// Obey the reply rule exactly once: answer the sender via send_message.
	if incoming && !alreadyReplied {
		head := strings.SplitN(last.Content, "\n", 2)[0]
		head = head[strings.Index(head, "[MESSAGE FROM ")+len("[MESSAGE FROM "):]
		sender := strings.TrimSuffix(head, "]:")
		return &llmtypes.LLMResponse{
			ToolCalls: []types.ToolCall{{
				ID:   "reply-1",
				Name: "send_message",
				Input: map[string]interface{}{
					"to_agent": sender,
					"message":  "worker result: 42",
				},
			}},
			Usage: llmtypes.Usage{InputTokens: 10, OutputTokens: 5},
		}, nil
	}

	return &llmtypes.LLMResponse{
		Content: "done",
		Usage:   llmtypes.Usage{InputTokens: 10, OutputTokens: 5},
	}, nil
}

func (m *replyingLLM) Name() string  { return "replying-llm" }
func (m *replyingLLM) Model() string { return "replying-model" }

func (m *replyingLLM) prompt() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.systemPrompt
}

func (m *replyingLLM) replyCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seenReplies
}

func (m *replyingLLM) message() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sawMessage
}

// wovenWorkflow stands up a coordinator plus one worker through the real weave
// path and returns the server, the two brains, and the coordinator session.
func wovenWorkflow(t *testing.T) (*MultiAgentServer, *replyingLLM, *replyingLLM, string) {
	t.Helper()

	coordinatorLLM := &replyingLLM{}
	workerLLM := &replyingLLM{}
	backend := &mockBackend{}

	coordinatorAgent := agent.NewAgent(backend, coordinatorLLM, agent.WithName("test-workflow"))
	workerAgent := agent.NewAgent(backend, workerLLM, agent.WithName("test-workflow:worker"))

	agents := map[string]*agent.Agent{
		"test-workflow":        coordinatorAgent,
		"test-workflow:worker": workerAgent,
	}

	logger := newTestLogger(t)
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir: t.TempDir(),
		DBPath:    ":memory:",
		Logger:    logger,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })

	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:     "test-workflow",
		Metadata: map[string]string{"role": "coordinator", "workflow": "test-workflow"},
	})
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:     "test-workflow:worker",
		Metadata: map[string]string{"role": "executor", "workflow": "test-workflow"},
	})

	srv := setupBroadcastTestServer(t, agents, registry)

	// The coordinator's inbound path is driven by the server-level queue
	// monitor, which production starts once at boot.
	monitorCtx, stopMonitor := context.WithCancel(context.Background())
	srv.StartMessageQueueMonitor(monitorCtx)
	t.Cleanup(stopMonitor)

	sessionID := GenerateSessionID()
	require.NoError(t, srv.spawnWorkflowSubAgents(context.Background(), coordinatorAgent, "test-workflow", sessionID))
	t.Cleanup(func() {
		srv.workflowSubAgentsMu.Lock()
		defer srv.workflowSubAgentsMu.Unlock()
		for _, wctx := range srv.workflowSubAgents {
			if wctx.cancelFunc != nil {
				wctx.cancelFunc()
			}
		}
	})

	return srv, coordinatorLLM, workerLLM, sessionID
}

// TestWorker_IsToldTheCoordinatorAddress proves C4: the coordinator is in the
// worker's reachable set. Without it the worker has no address to return its
// result to and the workflow stalls after the first delegation.
func TestWorker_IsToldTheCoordinatorAddress(t *testing.T) {
	srv, _, workerLLM, _ := wovenWorkflow(t)

	// Drive one turn so the worker builds its session prompt.
	workerAgent, _, err := srv.getAgent("test-workflow:worker")
	require.NoError(t, err)
	_, err = workerAgent.Chat(context.Background(), GenerateSessionID(), "warmup")
	require.NoError(t, err)

	prompt := workerLLM.prompt()
	require.NotEmpty(t, prompt, "the worker's system prompt was captured")
	assert.Contains(t, prompt, "test-workflow",
		"the worker must be told the coordinator's address")
	assert.Contains(t, prompt, "When a message arrives, send your answer back to its sender",
		"the worker must be told to reply, since its turn text is delivered nowhere")
}

// TestWovenWorkflow_ReplyReachesCoordinator is the acceptance gate: the
// coordinator tasks a worker, the worker answers, and the answer lands back in
// the coordinator's session. This is the whole round trip the woven path
// exists for.
func TestWovenWorkflow_ReplyReachesCoordinator(t *testing.T) {
	srv, coordinatorLLM, workerLLM, _ := wovenWorkflow(t)

	// The coordinator delegates: a message enqueued for the worker, exactly as
	// its send_message tool would produce.
	require.NoError(t, srv.messageQueue.Enqueue(context.Background(), &communication.QueueMessage{
		ID:          "task-1",
		FromAgent:   "test-workflow",
		ToAgent:     "test-workflow:worker",
		MessageType: "task",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{Value: []byte("compute the total")},
		},
	}))

	// Step 1: the worker wakes and sees the task under the promised label.
	require.Eventually(t, func() bool {
		return strings.Contains(workerLLM.message(), "compute the total")
	}, 5*time.Second, 50*time.Millisecond,
		"the worker must receive the task")
	require.Contains(t, workerLLM.message(), "[MESSAGE FROM test-workflow]:",
		"the task carries the label the team block names")

	// Step 2: obeying the reply rule, the worker answers via send_message, and
	// the coordinator's notification loop injects that answer into its session.
	require.Eventually(t, func() bool {
		return strings.Contains(coordinatorLLM.message(), "worker result: 42")
	}, 10*time.Second, 50*time.Millisecond,
		"the worker's answer must arrive in the coordinator's session")

	assert.Contains(t, coordinatorLLM.message(), "[MESSAGE FROM ",
		"the reply carries the label the team block names")
}

// TestAgentDisplayName_FallsBackToRawID proves the sender label degrades
// safely: send_message stamps a GUID, and where the registry can name it the
// peers see that name — otherwise the raw id is passed through unchanged rather
// than dropped, so a message is never attributed to an empty sender.
func TestAgentDisplayName_FallsBackToRawID(t *testing.T) {
	srv := &MultiAgentServer{}

	assert.Equal(t, "", srv.agentDisplayName(""), "empty stays empty")
	assert.Equal(t, "some-guid", srv.agentDisplayName("some-guid"),
		"with no registry the id passes through")

}

// TestWovenWorkflow_FanOutRepliesReachCoordinator proves the wiring holds with
// more than one worker: each worker is told the coordinator's address, both are
// tasked, and both answers arrive in the coordinator's session. The
// coordinator's inbound path is a single monitor-driven goroutine, so
// concurrent replies must serialise rather than collide.
func TestWovenWorkflow_FanOutRepliesReachCoordinator(t *testing.T) {
	coordinatorLLM := &replyingLLM{}
	workerLLMs := map[string]*replyingLLM{
		"test-workflow:alpha": {},
		"test-workflow:beta":  {},
	}

	backend := &mockBackend{}
	agents := map[string]*agent.Agent{
		"test-workflow": agent.NewAgent(backend, coordinatorLLM),
	}
	for name, llm := range workerLLMs {
		agents[name] = agent.NewAgent(backend, llm)
	}

	logger := newTestLogger(t)
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir: t.TempDir(),
		DBPath:    ":memory:",
		Logger:    logger,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })

	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:     "test-workflow",
		Metadata: map[string]string{"role": "coordinator", "workflow": "test-workflow"},
	})
	for name := range workerLLMs {
		registry.RegisterConfig(&loomv1.AgentConfig{
			Name:     name,
			Metadata: map[string]string{"role": "executor", "workflow": "test-workflow"},
		})
	}

	srv := setupBroadcastTestServer(t, agents, registry)
	srv.mu.Lock()
	for name, ag := range agents {
		srv.agents[name] = ag
	}
	srv.mu.Unlock()

	monitorCtx, stopMonitor := context.WithCancel(context.Background())
	srv.StartMessageQueueMonitor(monitorCtx)
	t.Cleanup(stopMonitor)

	sessionID := GenerateSessionID()
	require.NoError(t, srv.spawnWorkflowSubAgents(context.Background(), agents["test-workflow"], "test-workflow", sessionID))

	// Task both workers.
	for name := range workerLLMs {
		require.NoError(t, srv.messageQueue.Enqueue(context.Background(), &communication.QueueMessage{
			ID:          "task-" + name,
			FromAgent:   "test-workflow",
			ToAgent:     name,
			MessageType: "task",
			Payload: &loomv1.MessagePayload{
				Data: &loomv1.MessagePayload_Value{Value: []byte("compute for " + name)},
			},
		}))
	}

	// Both answer, and both answers land in the coordinator's session.
	require.Eventually(t, func() bool {
		return coordinatorLLM.replyCount() >= 2
	}, 15*time.Second, 50*time.Millisecond,
		"every worker's answer must reach the coordinator")

	for name, llm := range workerLLMs {
		assert.Contains(t, llm.message(), "compute for "+name, "%s received its own task", name)
		assert.Contains(t, llm.prompt(), "test-workflow",
			"%s must be told the coordinator's address", name)
	}
}

// TestWovenWorkflow_RejectsToolsDisabledWorker proves F9: a workflow worker
// configured with WithoutTools() cannot call send_message, so starting the
// workflow must fail explicitly before registering background workers or
// acknowledging queued messages.
func TestWovenWorkflow_RejectsToolsDisabledWorker(t *testing.T) {
	for _, tc := range []struct {
		name        string
		coordinator string
	}{
		{name: "coordinator_name", coordinator: "test-workflow"},
		{name: "coordinator_guid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			coordinatorLLM := &replyingLLM{}
			workerLLM := &replyingLLM{}
			backend := &mockBackend{}

			coordinatorAgent := agent.NewAgent(backend, coordinatorLLM, agent.WithName("test-workflow"))
			workerAgent := agent.NewAgent(backend, workerLLM, agent.WithName("test-workflow:worker"), agent.WithoutTools())

			agents := map[string]*agent.Agent{
				"test-workflow":        coordinatorAgent,
				"test-workflow:worker": workerAgent,
			}

			logger := newTestLogger(t)
			registry, err := agent.NewRegistry(agent.RegistryConfig{
				ConfigDir: t.TempDir(),
				DBPath:    ":memory:",
				Logger:    logger,
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = registry.Close() })

			registry.RegisterConfig(&loomv1.AgentConfig{
				Name:     "test-workflow",
				Metadata: map[string]string{"role": "coordinator", "workflow": "test-workflow"},
			})
			registry.RegisterConfig(&loomv1.AgentConfig{
				Name:     "test-workflow:worker",
				Metadata: map[string]string{"role": "executor", "workflow": "test-workflow"},
			})

			srv := setupBroadcastTestServer(t, agents, registry)
			core, logs := observer.New(zap.WarnLevel)
			srv.logger = zap.New(core)
			coordinatorID := tc.coordinator
			if coordinatorID == "" {
				coordinatorID = coordinatorAgent.GetID()
			}

			assert.Empty(t, workerAgent.ListTools(), "WithoutTools worker must have zero tools, including send_message")

			ctx := context.Background()
			require.NoError(t, srv.messageQueue.Enqueue(ctx, &communication.QueueMessage{
				ID:          "task-disabled-1",
				FromAgent:   "test-workflow",
				ToAgent:     "test-workflow:worker",
				MessageType: "task",
				Payload: &loomv1.MessagePayload{
					Data: &loomv1.MessagePayload_Value{Value: []byte("compute with no tools")},
				},
			}))

			sessionID := GenerateSessionID()
			err = srv.spawnWorkflowSubAgents(ctx, coordinatorAgent, coordinatorID, sessionID)
			require.Error(t, err, "workflow startup must reject a tool-disabled worker")
			require.ErrorIs(t, err, ErrWorkflowParticipantToolsDisabled)
			assert.ErrorContains(t, err, "test-workflow:worker")
			assert.ErrorContains(t, err, "tools.disabled")

			// Verify Weave surfaces the rejection to the caller rather than logging and continuing.
			weaveResp, weaveErr := srv.Weave(ctx, &loomv1.WeaveRequest{
				AgentId:   coordinatorID,
				SessionId: sessionID,
				Query:     "delegate to worker",
			})
			require.Error(t, weaveErr, "Weave must surface the incompatible workflow participant error")
			assert.Nil(t, weaveResp)
			require.ErrorIs(t, weaveErr, ErrWorkflowParticipantToolsDisabled)
			st, ok := status.FromError(weaveErr)
			require.True(t, ok)
			assert.Equal(t, codes.FailedPrecondition, st.Code())
			assert.Contains(t, st.Message(), "test-workflow:worker")

			// Verify StreamWeave also surfaces the rejection to the caller.
			stream := &mockStreamWeaveServer{ctx: ctx}
			streamErr := srv.StreamWeave(&loomv1.WeaveRequest{
				AgentId:   coordinatorID,
				SessionId: sessionID,
				Query:     "delegate to worker",
			}, stream)
			require.Error(t, streamErr, "StreamWeave must surface the incompatible workflow participant error")
			require.ErrorIs(t, streamErr, ErrWorkflowParticipantToolsDisabled)
			assert.Empty(t, stream.messages)

			// Both rejections return before the tracer span starts, so each
			// must leave one Warn line naming the workflow, participant and role.
			assertParticipantRejectionLogged(t, logs, "Weave", "test-workflow", "test-workflow:worker", "worker")
			assertParticipantRejectionLogged(t, logs, "StreamWeave", "test-workflow", "test-workflow:worker", "worker")

			srv.workflowSubAgentsMu.RLock()
			activeCount := len(srv.workflowSubAgents)
			srv.workflowSubAgentsMu.RUnlock()
			assert.Zero(t, activeCount, "rejected workflow startup must leave no registered workflow contexts")
			_, registered := srv.messageQueue.GetNotificationChannel("test-workflow:worker")
			assert.False(t, registered)
			assert.Empty(t, srv.messageBus.GetSubscriptionsByAgent("test-workflow"))
			assert.Empty(t, srv.messageBus.GetSubscriptionsByAgent("test-workflow:worker"))

			assert.Empty(t, workerLLM.message(), "disabled worker must not process queued messages")
			assert.Empty(t, coordinatorLLM.prompt(), "coordinator turn must not run when workflow validation fails")

			pending, deqErr := srv.messageQueue.Dequeue(ctx, "test-workflow:worker")
			require.NoError(t, deqErr)
			require.NotNil(t, pending, "queued message must remain unacknowledged after rejection")
			assert.Equal(t, "task-disabled-1", pending.ID)
		})
	}
}

// TestWovenWorkflow_RejectsToolsDisabledCoordinator verifies that a workflow
// coordinator configured with WithoutTools() is rejected before registering
// notifications, starting workers, or running a coordinator turn.
func TestWovenWorkflow_RejectsToolsDisabledCoordinator(t *testing.T) {
	coordinatorLLM := &replyingLLM{}
	workerLLM := &replyingLLM{}
	backend := &mockBackend{}

	coordinatorAgent := agent.NewAgent(backend, coordinatorLLM, agent.WithName("test-workflow"), agent.WithoutTools())
	workerAgent := agent.NewAgent(backend, workerLLM, agent.WithName("test-workflow:worker"))

	agents := map[string]*agent.Agent{
		"test-workflow":        coordinatorAgent,
		"test-workflow:worker": workerAgent,
	}

	logger := newTestLogger(t)
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir: t.TempDir(),
		DBPath:    ":memory:",
		Logger:    logger,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })

	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:     "test-workflow",
		Metadata: map[string]string{"role": "coordinator", "workflow": "test-workflow"},
	})
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:     "test-workflow:worker",
		Metadata: map[string]string{"role": "executor", "workflow": "test-workflow"},
	})

	srv := setupBroadcastTestServer(t, agents, registry)
	srv.mu.Lock()
	for name, ag := range agents {
		srv.agents[name] = ag
	}
	srv.mu.Unlock()

	ctx := context.Background()
	sessionID := GenerateSessionID()

	weaveResp, weaveErr := srv.Weave(ctx, &loomv1.WeaveRequest{
		AgentId:   "test-workflow",
		SessionId: sessionID,
		Query:     "coordinate workflow",
	})
	require.Error(t, weaveErr)
	assert.Nil(t, weaveResp)
	require.ErrorIs(t, weaveErr, ErrWorkflowParticipantToolsDisabled)
	assert.ErrorContains(t, weaveErr, "coordinator")
	assert.ErrorContains(t, weaveErr, "test-workflow")
	st, ok := status.FromError(weaveErr)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())

	srv.workflowSubAgentsMu.RLock()
	activeCount := len(srv.workflowSubAgents)
	srv.workflowSubAgentsMu.RUnlock()
	assert.Zero(t, activeCount, "rejected coordinator must leave no registered workflow contexts")
	assert.Empty(t, coordinatorLLM.prompt(), "disabled coordinator must not execute a turn")
}

// TestWovenWorkflow_AutoSpawnRejectsToolsDisabledWorker verifies that
// autoSpawnWorkflowSubAgent and the queue monitor refuse to start a disabled
// worker and leave queued messages unacknowledged.
func TestWovenWorkflow_AutoSpawnRejectsToolsDisabledWorker(t *testing.T) {
	workerLLM := &replyingLLM{}
	backend := &mockBackend{}

	workerAgent := agent.NewAgent(backend, workerLLM, agent.WithName("worker"), agent.WithoutTools())
	agents := map[string]*agent.Agent{
		"worker": workerAgent,
	}

	logger := newTestLogger(t)
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir: t.TempDir(),
		DBPath:    ":memory:",
		Logger:    logger,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })

	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:     "test-workflow:worker",
		Metadata: map[string]string{"role": "executor", "workflow": "test-workflow"},
		Tools:    &loomv1.ToolsConfig{Disabled: true},
	})

	srv := setupBroadcastTestServer(t, agents, registry)
	core, logs := observer.New(zap.DebugLevel)
	srv.logger = zap.New(core)

	ctx := context.Background()
	require.NoError(t, srv.messageQueue.Enqueue(ctx, &communication.QueueMessage{
		ID:          "auto-task-1",
		FromAgent:   "test-workflow",
		ToAgent:     "test-workflow:worker",
		MessageType: "task",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{Value: []byte("auto-spawn task")},
		},
	}))

	err = srv.autoSpawnWorkflowSubAgent(ctx, "test-workflow:worker")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrWorkflowParticipantToolsDisabled)
	assert.ErrorContains(t, err, "test-workflow:worker")

	rejected := make(map[string]*agent.Agent)
	for range 3 {
		srv.notifyQueuedWorkflowAgents(ctx, rejected)
	}
	assert.Equal(t, 1, logs.FilterMessage("Refusing to auto-spawn workflow sub-agent with tools disabled").Len())
	assert.Same(t, workerAgent, rejected["test-workflow:worker"])

	srv.workflowSubAgentsMu.RLock()
	activeCount := len(srv.workflowSubAgents)
	srv.workflowSubAgentsMu.RUnlock()
	assert.Zero(t, activeCount, "auto-spawn monitor must not register a disabled worker")
	assert.Empty(t, workerLLM.message(), "disabled worker must not process auto-spawned message")
	_, registered := srv.messageQueue.GetNotificationChannel("test-workflow:worker")
	assert.False(t, registered)

	pending, deqErr := srv.messageQueue.Dequeue(ctx, "test-workflow:worker")
	require.NoError(t, deqErr)
	require.NotNil(t, pending, "message must remain unacknowledged in queue")
	assert.Equal(t, "auto-task-1", pending.ID)
	require.NoError(t, srv.messageQueue.Requeue(ctx, pending.ID))

	replacement := agent.NewAgent(backend, &replyingLLM{}, agent.WithName("worker"))
	replacement.SetID(workerAgent.GetID())
	require.NoError(t, srv.UpdateAgent(workerAgent.GetID(), replacement))
	srv.notifyQueuedWorkflowAgents(ctx, rejected)
	assert.Empty(t, rejected, "a new enabled instance must clear the rejection")
	srv.workflowSubAgentsMu.RLock()
	running := srv.workflowSubAgents["auto:test-workflow:worker"]
	srv.workflowSubAgentsMu.RUnlock()
	require.NotNil(t, running)
	assert.Same(t, replacement, running.agent)
	assert.Equal(t, 1, logs.FilterMessage("Refusing to auto-spawn workflow sub-agent with tools disabled").Len())
}

func TestWovenWorkflow_AutoSpawnCachesMetadataResolutionForDisabledWorker(t *testing.T) {
	workerAgent := agent.NewAgent(&mockBackend{}, &replyingLLM{}, agent.WithName("configured-worker-helper"), agent.WithoutTools())
	logger := newTestLogger(t)
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir: t.TempDir(),
		DBPath:    ":memory:",
		Logger:    logger,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:     "configured-worker-helper",
		Metadata: map[string]string{"role": "executor", "workflow": "test-workflow"},
	})

	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{"configured-worker-helper": workerAgent}, registry)
	core, logs := observer.New(zap.DebugLevel)
	srv.logger = zap.New(core)
	ctx := context.Background()
	const agentID = "test-workflow:worker"
	require.NoError(t, srv.messageQueue.Enqueue(ctx, &communication.QueueMessage{
		ID:          "auto-metadata-task",
		FromAgent:   "test-workflow",
		ToAgent:     agentID,
		MessageType: "task",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{Value: []byte("auto-spawn task")},
		},
	}))

	rejected := make(map[string]*agent.Agent)
	for range 4 {
		srv.notifyQueuedWorkflowAgents(ctx, rejected)
	}

	assert.Equal(t, 1, logs.FilterMessage("Found matching agent in registry via workflow metadata search").Len(),
		"cached rejections must not repeat the metadata resolver's Info log")
	assert.Equal(t, 1, logs.FilterMessage("Refusing to auto-spawn workflow sub-agent with tools disabled").Len())
	assert.Same(t, workerAgent, rejected[agentID])
	srv.workflowSubAgentsMu.RLock()
	activeCount := len(srv.workflowSubAgents)
	srv.workflowSubAgentsMu.RUnlock()
	assert.Zero(t, activeCount, "a disabled worker must not be registered")
}

// TestWovenWorkflow_ReloadWithToolsDisabledRejectsSubsequentWorkflowAndAutoSpawn
// verifies that replacing or reloading an initially enabled worker with
// tools.disabled: true stops any active workflow worker and prevents subsequent
// workflow startup or auto-spawn from bypassing the policy.
func TestWovenWorkflow_ReloadWithToolsDisabledRejectsSubsequentWorkflowAndAutoSpawn(t *testing.T) {
	srv, _, workerLLM, _ := wovenWorkflow(t)
	ctx := context.Background()

	workerOrig, workerGUID, err := srv.getAgent("test-workflow:worker")
	require.NoError(t, err)
	require.False(t, workerOrig.ToolsDisabled())

	// Replace the worker via UpdateAgent with a WithoutTools() instance sharing the same GUID.
	disabledWorker := agent.NewAgent(&mockBackend{}, workerLLM, agent.WithName("test-workflow:worker"), agent.WithoutTools())
	disabledWorker.SetID(workerGUID)
	require.NoError(t, srv.UpdateAgent("test-workflow:worker", disabledWorker))

	// Active workflow sub-agent entry for the replaced worker must be stopped and removed.
	srv.workflowSubAgentsMu.RLock()
	_, workerStillActive := srv.workflowSubAgents["test-workflow:worker"]
	for key := range srv.workflowSubAgents {
		if strings.HasSuffix(key, ":test-workflow:worker") {
			workerStillActive = true
		}
	}
	srv.workflowSubAgentsMu.RUnlock()
	assert.False(t, workerStillActive, "UpdateAgent with tools disabled must stop active workflow worker contexts")

	// Enqueue a message after the reload/replacement and verify neither auto-spawn nor a new workflow accepts it.
	require.NoError(t, srv.messageQueue.Enqueue(ctx, &communication.QueueMessage{
		ID:          "task-after-reload",
		FromAgent:   "test-workflow",
		ToAgent:     "test-workflow:worker",
		MessageType: "task",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{Value: []byte("task after disabling tools")},
		},
	}))

	err = srv.autoSpawnWorkflowSubAgent(ctx, "test-workflow:worker")
	require.ErrorIs(t, err, ErrWorkflowParticipantToolsDisabled)

	newSessionID := GenerateSessionID()
	_, weaveErr := srv.Weave(ctx, &loomv1.WeaveRequest{
		AgentId:   "test-workflow",
		SessionId: newSessionID,
		Query:     "run workflow after worker reload",
	})
	require.ErrorIs(t, weaveErr, ErrWorkflowParticipantToolsDisabled)

	assert.Empty(t, workerLLM.message(), "reloaded disabled worker must not process queued messages")
	pending, deqErr := srv.messageQueue.Dequeue(ctx, "test-workflow:worker")
	require.NoError(t, deqErr)
	require.NotNil(t, pending, "queued message must remain unacknowledged after reload")
	assert.Equal(t, "task-after-reload", pending.ID)
}

// TestWovenWorkflow_StandaloneDisabledAgentAndEmptyToolOrdinaryWorker verifies
// that standalone disabled agents continue to work and that an ordinary agent
// whose initial tool list is empty is not misclassified as WithoutTools().
func TestWovenWorkflow_StandaloneDisabledAgentAndEmptyToolOrdinaryWorker(t *testing.T) {
	ctx := context.Background()
	backend := &mockBackend{}

	standaloneLLM := &replyingLLM{}
	standaloneDisabled := agent.NewAgent(backend, standaloneLLM, agent.WithName("standalone-disabled"), agent.WithoutTools())

	coordinatorLLM := &replyingLLM{}
	workerLLM := &replyingLLM{}
	coordinatorAgent := agent.NewAgent(backend, coordinatorLLM, agent.WithName("test-workflow"))
	// Construct an ordinary worker whose built-in tools are suppressed so its initial tool list is empty before server injection.
	emptyToolOrdinaryWorker := agent.NewAgent(backend, workerLLM,
		agent.WithName("test-workflow:worker"),
		agent.WithoutBuiltinTool("query_tool_result"),
		agent.WithoutBuiltinTool("recall"),
		agent.WithoutBuiltinTool("load_pattern"),
	)
	require.Zero(t, emptyToolOrdinaryWorker.ToolCount(), "precondition: ordinary worker starts with zero tools before server injection")
	require.False(t, emptyToolOrdinaryWorker.ToolsDisabled())

	agents := map[string]*agent.Agent{
		"standalone-disabled":  standaloneDisabled,
		"test-workflow":        coordinatorAgent,
		"test-workflow:worker": emptyToolOrdinaryWorker,
	}

	logger := newTestLogger(t)
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir: t.TempDir(),
		DBPath:    ":memory:",
		Logger:    logger,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })

	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:  "standalone-disabled",
		Tools: &loomv1.ToolsConfig{Disabled: true},
	})
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:     "test-workflow",
		Metadata: map[string]string{"role": "coordinator", "workflow": "test-workflow"},
	})
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:     "test-workflow:worker",
		Metadata: map[string]string{"role": "executor", "workflow": "test-workflow"},
	})

	srv := setupBroadcastTestServer(t, agents, registry)
	srv.mu.Lock()
	for name, ag := range agents {
		srv.agents[name] = ag
	}
	srv.mu.Unlock()

	// 1. Standalone disabled agent still works via Weave and exposes zero tools.
	standaloneResp, err := srv.Weave(ctx, &loomv1.WeaveRequest{
		AgentId: "standalone-disabled",
		Query:   "hello standalone",
	})
	require.NoError(t, err)
	require.NotNil(t, standaloneResp)
	assert.Equal(t, "done", standaloneResp.Text)
	assert.Empty(t, standaloneDisabled.ListTools())

	// 2. Ordinary worker that started with an empty tool list receives communication tools and succeeds in workflow.
	assert.Contains(t, emptyToolOrdinaryWorker.ListTools(), "send_message")
	require.NoError(t, srv.spawnWorkflowSubAgents(ctx, coordinatorAgent, "test-workflow", GenerateSessionID()))
}

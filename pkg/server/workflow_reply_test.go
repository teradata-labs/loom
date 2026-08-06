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
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

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

	coordinatorAgent := agent.NewAgent(backend, coordinatorLLM)
	workerAgent := agent.NewAgent(backend, workerLLM)

	agents := map[string]*agent.Agent{
		"test-workflow":        coordinatorAgent,
		"test-workflow:worker": workerAgent,
	}

	logger := zaptest.NewLogger(t)
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

	// The server re-keys its agent map by GUID; the workflow addresses agents
	// by name (registry metadata), so make both names directly resolvable.
	srv.mu.Lock()
	for name, ag := range agents {
		srv.agents[name] = ag
	}
	srv.mu.Unlock()

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

	logger := zaptest.NewLogger(t)
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

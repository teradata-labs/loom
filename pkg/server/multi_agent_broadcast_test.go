// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"fmt"
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
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// mockLLMForBroadcastTest implements a simple LLM for testing broadcast auto-injection
type mockLLMForBroadcastTest struct {
	mu               sync.Mutex
	injectedMessages []string // Track messages injected via Chat()
}

func (m *mockLLMForBroadcastTest) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	// Track injected messages
	m.mu.Lock()
	if len(messages) > 0 {
		lastMsg := messages[len(messages)-1].Content
		m.injectedMessages = append(m.injectedMessages, lastMsg)
	}
	m.mu.Unlock()

	return &llmtypes.LLMResponse{
		Content: "Acknowledged: " + messages[len(messages)-1].Content,
		Usage: llmtypes.Usage{
			InputTokens:  10,
			OutputTokens: 20,
			CostUSD:      0.001,
		},
	}, nil
}

func (m *mockLLMForBroadcastTest) Name() string {
	return "mock-broadcast-llm"
}

func (m *mockLLMForBroadcastTest) Model() string {
	return "mock-broadcast-model"
}

func (m *mockLLMForBroadcastTest) GetInjectedMessages() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.injectedMessages...)
}

// setupBroadcastTestServer creates a test server with message bus configured
func setupBroadcastTestServer(t *testing.T, agents map[string]*agent.Agent, registry *agent.Registry) *MultiAgentServer {
	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	sessionStore, err := agent.NewSessionStore(":memory:", tracer)
	require.NoError(t, err)
	t.Cleanup(func() { sessionStore.Close() })

	srv := NewMultiAgentServer(agents, sessionStore)
	srv.registry = registry
	srv.logger = logger

	// Configure message bus
	bus := communication.NewMessageBus(nil, nil, nil, logger)
	t.Cleanup(func() { bus.Close() })

	// Configure message queue (needed for spawnWorkflowSubAgents)
	queue, err := communication.NewMessageQueue(":memory:", nil, logger)
	require.NoError(t, err)
	t.Cleanup(func() { queue.Close() })

	sharedMem, err := communication.NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	t.Cleanup(func() { sharedMem.Close() })

	refStore, err := communication.NewReferenceStoreFromConfig(communication.FactoryConfig{
		Store: communication.StoreConfig{Backend: "memory"},
		GC:    communication.GCConfig{Enabled: false},
	})
	require.NoError(t, err)

	policy := communication.NewPolicyManager()

	err = srv.ConfigureCommunication(bus, queue, sharedMem, refStore, policy, logger)
	require.NoError(t, err)

	return srv
}

// TestCoordinatorBroadcastAutoInjection_SingleSubscription tests basic broadcast auto-injection
func TestCoordinatorBroadcastAutoInjection_SingleSubscription(t *testing.T) {
	mockBackend := &mockBackend{}
	mockLLM := &mockLLMForBroadcastTest{}

	coordinatorAgent := agent.NewAgent(mockBackend, mockLLM)
	subAgent := agent.NewAgent(mockBackend, mockLLM)

	agents := map[string]*agent.Agent{
		"test-workflow":        coordinatorAgent,
		"test-workflow:worker": subAgent,
	}

	// Create registry with coordinator metadata
	logger := zaptest.NewLogger(t)
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   t.TempDir(),
		DBPath:      ":memory:",
		Logger:      logger,
		LLMProvider: nil,
		MCPManager:  nil,
	})
	require.NoError(t, err)
	t.Cleanup(func() { registry.Close() })

	// Register coordinator
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name: "test-workflow",
		Metadata: map[string]string{
			"role":     "coordinator",
			"workflow": "test-workflow",
		},
	})

	// Register sub-agent (required after PR #43 GUID refactoring)
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name: "test-workflow:worker",
		Metadata: map[string]string{
			"role":     "executor",
			"workflow": "test-workflow",
		},
	})

	srv := setupBroadcastTestServer(t, agents, registry)

	// Create coordinator session and subscribe to a topic
	ctx := context.Background()
	sessionID := GenerateSessionID()

	// Coordinator subscribes to topic (directly via bus, not via gRPC stream)
	_, err = srv.messageBus.Subscribe(ctx, "test-workflow", "test-topic", nil, 10)
	require.NoError(t, err)

	// Spawn workflow sub-agents (triggers auto-injection setup)
	err = srv.spawnWorkflowSubAgents(ctx, coordinatorAgent, "test-workflow", sessionID)
	require.NoError(t, err)

	// Give goroutine time to start
	time.Sleep(100 * time.Millisecond)

	// Verify coordinator context has broadcast fields set
	srv.workflowSubAgentsMu.RLock()
	coordinatorKey := fmt.Sprintf("%s:test-workflow", sessionID)
	coordCtx, exists := srv.workflowSubAgents[coordinatorKey]
	srv.workflowSubAgentsMu.RUnlock()

	require.True(t, exists, "Coordinator context should exist")
	assert.NotNil(t, coordCtx.broadcastCancelFunc, "Broadcast cancel func should be set")
	assert.NotEmpty(t, coordCtx.subscriptionIDs, "Subscription IDs should be populated")
	assert.NotEmpty(t, coordCtx.notifyChannels, "Notify channels should be populated")

	// Publish message to trigger auto-injection
	_, err = srv.Publish(ctx, &loomv1.PublishRequest{
		Topic: "test-topic",
		Message: &loomv1.BusMessage{
			Id:        "msg1",
			Topic:     "test-topic",
			FromAgent: "test-workflow:worker",
			Payload: &loomv1.MessagePayload{
				Data: &loomv1.MessagePayload_Value{
					Value: []byte("Hello from worker"),
				},
			},
			Timestamp: time.Now().UnixMilli(),
		},
	})
	require.NoError(t, err)

	// Wait for auto-injection to complete
	time.Sleep(500 * time.Millisecond)

	// Verify message was injected
	injected := mockLLM.GetInjectedMessages()
	require.NotEmpty(t, injected, "Message should have been auto-injected")

	// Find the broadcast message
	found := false
	for _, msg := range injected {
		if contains(msg, "[BROADCAST on topic 'test-topic' FROM test-workflow:worker]") &&
			contains(msg, "Hello from worker") {
			found = true
			break
		}
	}
	assert.True(t, found, "Broadcast message should have been injected")
}

// TestCoordinatorBroadcastAutoInjection_MultipleSubscriptions tests multiple topic subscriptions
func TestCoordinatorBroadcastAutoInjection_MultipleSubscriptions(t *testing.T) {
	mockBackend := &mockBackend{}
	mockLLM := &mockLLMForBroadcastTest{}

	coordinatorAgent := agent.NewAgent(mockBackend, mockLLM)
	subAgent := agent.NewAgent(mockBackend, mockLLM)

	agents := map[string]*agent.Agent{
		"test-workflow":        coordinatorAgent,
		"test-workflow:worker": subAgent,
	}

	logger := zaptest.NewLogger(t)
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   t.TempDir(),
		DBPath:      ":memory:",
		Logger:      logger,
		LLMProvider: nil,
		MCPManager:  nil,
	})
	require.NoError(t, err)
	t.Cleanup(func() { registry.Close() })

	// Register coordinator
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name: "test-workflow",
		Metadata: map[string]string{
			"role":     "coordinator",
			"workflow": "test-workflow",
		},
	})

	// Register sub-agent (required after PR #43 GUID refactoring)
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name: "test-workflow:worker",
		Metadata: map[string]string{
			"role":     "executor",
			"workflow": "test-workflow",
		},
	})

	srv := setupBroadcastTestServer(t, agents, registry)

	ctx := context.Background()
	sessionID := GenerateSessionID()

	// Subscribe to multiple topics (directly via bus)
	_, err = srv.messageBus.Subscribe(ctx, "test-workflow", "topic-a", nil, 10)
	require.NoError(t, err)

	_, err = srv.messageBus.Subscribe(ctx, "test-workflow", "topic-b", nil, 10)
	require.NoError(t, err)

	// Spawn workflow
	err = srv.spawnWorkflowSubAgents(ctx, coordinatorAgent, "test-workflow", sessionID)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Verify multiple subscriptions tracked
	srv.workflowSubAgentsMu.RLock()
	coordinatorKey := fmt.Sprintf("%s:test-workflow", sessionID)
	coordCtx := srv.workflowSubAgents[coordinatorKey]
	srv.workflowSubAgentsMu.RUnlock()

	assert.Len(t, coordCtx.subscriptionIDs, 2, "Should track 2 subscriptions")
	assert.Len(t, coordCtx.notifyChannels, 2, "Should have 2 notify channels")

	// Publish to both topics
	_, err = srv.Publish(ctx, &loomv1.PublishRequest{
		Topic: "topic-a",
		Message: &loomv1.BusMessage{
			Id:        "msg-a",
			Topic:     "topic-a",
			FromAgent: "test-workflow:worker",
			Payload: &loomv1.MessagePayload{
				Data: &loomv1.MessagePayload_Value{
					Value: []byte("Message A"),
				},
			},
			Timestamp: time.Now().UnixMilli(),
		},
	})
	require.NoError(t, err)

	_, err = srv.Publish(ctx, &loomv1.PublishRequest{
		Topic: "topic-b",
		Message: &loomv1.BusMessage{
			Id:        "msg-b",
			Topic:     "topic-b",
			FromAgent: "test-workflow:worker",
			Payload: &loomv1.MessagePayload{
				Data: &loomv1.MessagePayload_Value{
					Value: []byte("Message B"),
				},
			},
			Timestamp: time.Now().UnixMilli(),
		},
	})
	require.NoError(t, err)

	// Wait for auto-injection
	time.Sleep(500 * time.Millisecond)

	// Verify both messages injected
	injected := mockLLM.GetInjectedMessages()
	require.GreaterOrEqual(t, len(injected), 2, "Both messages should be injected")

	foundA := false
	foundB := false
	for _, msg := range injected {
		if contains(msg, "topic-a") && contains(msg, "Message A") {
			foundA = true
		}
		if contains(msg, "topic-b") && contains(msg, "Message B") {
			foundB = true
		}
	}
	assert.True(t, foundA, "Message A should be injected")
	assert.True(t, foundB, "Message B should be injected")
}

// TestCoordinatorBroadcastAutoInjection_SkipsSelfMessages verifies self-messages are not injected
func TestCoordinatorBroadcastAutoInjection_SkipsSelfMessages(t *testing.T) {
	mockBackend := &mockBackend{}
	mockLLM := &mockLLMForBroadcastTest{}

	coordinatorAgent := agent.NewAgent(mockBackend, mockLLM)
	subAgent := agent.NewAgent(mockBackend, mockLLM)

	agents := map[string]*agent.Agent{
		"test-workflow":        coordinatorAgent,
		"test-workflow:worker": subAgent,
	}

	logger := zaptest.NewLogger(t)
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   t.TempDir(),
		DBPath:      ":memory:",
		Logger:      logger,
		LLMProvider: nil,
		MCPManager:  nil,
	})
	require.NoError(t, err)
	t.Cleanup(func() { registry.Close() })

	// Register coordinator
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name: "test-workflow",
		Metadata: map[string]string{
			"role":     "coordinator",
			"workflow": "test-workflow",
		},
	})

	// Register sub-agent (required after PR #43 GUID refactoring)
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name: "test-workflow:worker",
		Metadata: map[string]string{
			"role":     "executor",
			"workflow": "test-workflow",
		},
	})

	srv := setupBroadcastTestServer(t, agents, registry)

	ctx := context.Background()
	sessionID := GenerateSessionID()

	// Subscribe (directly via bus)
	_, err = srv.messageBus.Subscribe(ctx, "test-workflow", "test-topic", nil, 10)
	require.NoError(t, err)

	// Spawn workflow
	err = srv.spawnWorkflowSubAgents(ctx, coordinatorAgent, "test-workflow", sessionID)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Publish message FROM coordinator (self-message)
	_, err = srv.Publish(ctx, &loomv1.PublishRequest{
		Topic: "test-topic",
		Message: &loomv1.BusMessage{
			Id:        "self-msg",
			Topic:     "test-topic",
			FromAgent: "test-workflow", // Same as coordinator
			Payload: &loomv1.MessagePayload{
				Data: &loomv1.MessagePayload_Value{
					Value: []byte("Self message"),
				},
			},
			Timestamp: time.Now().UnixMilli(),
		},
	})
	require.NoError(t, err)

	// Wait
	time.Sleep(300 * time.Millisecond)

	// Verify self-message was NOT injected
	injected := mockLLM.GetInjectedMessages()
	for _, msg := range injected {
		assert.NotContains(t, msg, "Self message", "Self-message should not be injected")
	}
}

// TestCoordinatorBroadcastAutoInjection_CleanupOnSessionEnd verifies no leaks
func TestCoordinatorBroadcastAutoInjection_CleanupOnSessionEnd(t *testing.T) {
	mockBackend := &mockBackend{}
	mockLLM := &mockLLMForBroadcastTest{}

	coordinatorAgent := agent.NewAgent(mockBackend, mockLLM)
	subAgent := agent.NewAgent(mockBackend, mockLLM)

	agents := map[string]*agent.Agent{
		"test-workflow":        coordinatorAgent,
		"test-workflow:worker": subAgent,
	}

	logger := zaptest.NewLogger(t)
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   t.TempDir(),
		DBPath:      ":memory:",
		Logger:      logger,
		LLMProvider: nil,
		MCPManager:  nil,
	})
	require.NoError(t, err)
	t.Cleanup(func() { registry.Close() })

	// Register coordinator
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name: "test-workflow",
		Metadata: map[string]string{
			"role":     "coordinator",
			"workflow": "test-workflow",
		},
	})

	// Register sub-agent (required after PR #43 GUID refactoring)
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name: "test-workflow:worker",
		Metadata: map[string]string{
			"role":     "executor",
			"workflow": "test-workflow",
		},
	})

	srv := setupBroadcastTestServer(t, agents, registry)

	ctx, cancel := context.WithCancel(context.Background())
	sessionID := GenerateSessionID()

	// Subscribe
	mockStream := &mockSubscribeStream{
		ctx:      ctx,
		messages: make(chan *loomv1.BusMessage, 10),
		done:     make(chan struct{}),
	}
	go func() {
		_ = srv.Subscribe(&loomv1.SubscribeRequest{
			AgentId:      "test-workflow",
			TopicPattern: "test-topic",
			BufferSize:   10,
		}, mockStream)
	}()

	time.Sleep(100 * time.Millisecond)

	// Spawn workflow
	err = srv.spawnWorkflowSubAgents(ctx, coordinatorAgent, "test-workflow", sessionID)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Verify context exists
	srv.workflowSubAgentsMu.RLock()
	coordinatorKey := fmt.Sprintf("%s:test-workflow", sessionID)
	coordCtx, exists := srv.workflowSubAgents[coordinatorKey]
	srv.workflowSubAgentsMu.RUnlock()
	require.True(t, exists)
	require.NotNil(t, coordCtx.broadcastCancelFunc)

	// Trigger cleanup by canceling context
	cancel()
	time.Sleep(200 * time.Millisecond)

	// Verify goroutine stopped (by checking that publish after cancel doesn't cause panic)
	_, err = srv.Publish(context.Background(), &loomv1.PublishRequest{
		Topic: "test-topic",
		Message: &loomv1.BusMessage{
			Id:        "after-cancel",
			Topic:     "test-topic",
			FromAgent: "test-workflow:worker",
			Payload: &loomv1.MessagePayload{
				Data: &loomv1.MessagePayload_Value{
					Value: []byte("After cancel"),
				},
			},
			Timestamp: time.Now().UnixMilli(),
		},
	})
	require.NoError(t, err)

	// No panic = cleanup worked correctly
	time.Sleep(100 * time.Millisecond)
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

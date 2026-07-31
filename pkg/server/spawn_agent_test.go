// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"go.uber.org/zap"
)

// newSpawnTestServer builds a MultiAgentServer with a real agent registry
// (temp SQLite), a spawnable agent named "checker", and a live message bus.
func newSpawnTestServer(t *testing.T) (*MultiAgentServer, *communication.MessageBus) {
	t.Helper()

	llm := &mockLLMForMultiAgent{}
	parent := agent.NewAgent(&mockBackend{}, llm)
	sessionStore, err := agent.NewSessionStore(":memory:", observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sessionStore.Close() })
	server := NewMultiAgentServer(map[string]*agent.Agent{"parent": parent}, sessionStore)

	tmpDir := t.TempDir()
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   tmpDir,
		DBPath:      filepath.Join(tmpDir, "spawn_test_registry.db"),
		LLMProvider: llm,
		Logger:      zap.NewNop(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })

	ctx := context.Background()
	require.NoError(t, agent.SaveAgentConfig(
		&loomv1.AgentConfig{Name: "checker", SystemPrompt: "Verify artifacts against criteria."},
		filepath.Join(tmpDir, "agents", "checker.yaml")))
	require.NoError(t, registry.LoadAgents(ctx))
	_, err = registry.CreateAgent(ctx, "checker")
	require.NoError(t, err)
	server.SetAgentRegistry(registry)

	// Sub-agent sessions reference their parent session (FK) — create it.
	require.NoError(t, sessionStore.SaveSession(ctx, &agent.Session{
		ID:      "parent-session",
		AgentID: parent.GetID(),
	}))

	bus := communication.NewMessageBus(nil, nil, observability.NewNoOpTracer(), zap.NewNop())
	server.mu.Lock()
	server.messageBus = bus
	server.mu.Unlock()

	return server, bus
}

func TestSpawnSubAgent_InitialMessageRequiresAutoSubscribe(t *testing.T) {
	t.Parallel()

	llm := &mockLLMForMultiAgent{}
	parent := agent.NewAgent(&mockBackend{}, llm)
	server := NewMultiAgentServer(map[string]*agent.Agent{"parent": parent}, nil)

	_, err := server.SpawnSubAgent(context.Background(), &builtin.SpawnSubAgentRequest{
		ParentSessionID: "parent-session",
		AgentID:         "checker",
		InitialMessage:  "verify this artifact",
		// AutoSubscribe deliberately empty
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initial_message requires auto_subscribe")
}

func TestSpawnSubAgent_InitialMessageRequiresBus(t *testing.T) {
	t.Parallel()

	llm := &mockLLMForMultiAgent{}
	parent := agent.NewAgent(&mockBackend{}, llm)
	server := NewMultiAgentServer(map[string]*agent.Agent{"parent": parent}, nil)

	tmpDir := t.TempDir()
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   tmpDir,
		DBPath:      filepath.Join(tmpDir, "reg.db"),
		LLMProvider: llm,
		Logger:      zap.NewNop(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })
	server.SetAgentRegistry(registry)
	// No message bus configured.

	_, err = server.SpawnSubAgent(context.Background(), &builtin.SpawnSubAgentRequest{
		ParentSessionID: "parent-session",
		AgentID:         "checker",
		InitialMessage:  "verify this artifact",
		AutoSubscribe:   []string{"verify.topic"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a message bus")
}

func TestSpawnSubAgent_InitialMessageDelivered(t *testing.T) {
	t.Parallel()

	server, bus := newSpawnTestServer(t)
	ctx := context.Background()
	const topic = "verify.artifact.topic"

	// Observe the topic as the parent would.
	observerSub, err := bus.Subscribe(ctx, "observer", topic, nil, 10)
	require.NoError(t, err)

	resp, err := server.SpawnSubAgent(ctx, &builtin.SpawnSubAgentRequest{
		ParentSessionID: "parent-session",
		ParentAgentID:   "parent-agent-id",
		AgentID:         "checker",
		InitialMessage:  "verify this artifact",
		AutoSubscribe:   []string{topic},
	})
	require.NoError(t, err)
	assert.Equal(t, "spawned", resp.Status)
	assert.Equal(t, []string{topic}, resp.SubscribedTopics)

	// The initial message must be on the topic, parent-origin, and flagged.
	select {
	case msg := <-observerSub.Channel:
		assert.Equal(t, "parent-agent-id", msg.FromAgent)
		assert.Equal(t, "verify this artifact", string(msg.Payload.GetValue()))
		assert.Equal(t, "true", msg.Metadata["initial_message"])
	case <-time.After(2 * time.Second):
		t.Fatal("initial_message was not published to the auto-subscribed topic")
	}

	// The spawned agent must process it (parent-origin passes the self-echo
	// filter) and publish its response back to the same topic.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case msg := <-observerSub.Channel:
			if msg.FromAgent == resp.SubAgentID {
				assert.Contains(t, string(msg.Payload.GetValue()), "Mock response from",
					"spawned agent's Chat output is published back to the topic")
				return
			}
			// Skip unrelated messages (e.g., our own echo of the initial message).
		case <-deadline:
			t.Fatal("spawned agent never published a response to the initial message")
		}
	}
}

func TestSpawnSubAgent_NoInitialMessageStillSpawns(t *testing.T) {
	t.Parallel()

	server, _ := newSpawnTestServer(t)

	resp, err := server.SpawnSubAgent(context.Background(), &builtin.SpawnSubAgentRequest{
		ParentSessionID: "parent-session",
		AgentID:         "checker",
		AutoSubscribe:   []string{"idle.topic"},
	})
	require.NoError(t, err)
	assert.Equal(t, "spawned", resp.Status)
}

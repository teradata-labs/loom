// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

const yamlStartupDefinition = `apiVersion: loom/v1
kind: Workflow
metadata:
  name: yaml-startup
spec:
  type: custom
  communication:
    topic: yaml-startup-events
`

type workflowStartupTurn struct {
	systemPrompt string
	prompt       string
}

type workflowStartupLLM struct {
	turns chan workflowStartupTurn
}

func (l *workflowStartupLLM) Chat(ctx context.Context, messages []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	turn := workflowStartupTurn{prompt: messages[len(messages)-1].Content}
	for _, message := range messages {
		if message.Role == "system" {
			turn.systemPrompt += message.Content
		}
	}
	select {
	case l.turns <- turn:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &llmtypes.LLMResponse{Content: "acknowledged"}, nil
}

func (l *workflowStartupLLM) Name() string  { return "workflow-startup-test" }
func (l *workflowStartupLLM) Model() string { return "workflow-startup-test" }

func (l *workflowStartupLLM) nextTurn(t *testing.T) workflowStartupTurn {
	t.Helper()
	select {
	case turn := <-l.turns:
		return turn
	case <-time.After(5 * time.Second):
		t.Fatal("workflow notification did not reach the coordinator")
		return workflowStartupTurn{}
	}
}

func newYAMLWorkflowStartupServer(t *testing.T, workflowYAML string) (*MultiAgentServer, *agent.Agent, *workflowStartupLLM) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if workflowYAML != "" {
		workflowDir := filepath.Join(home, ".loom", "workflows")
		require.NoError(t, os.MkdirAll(workflowDir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "yaml-startup.yaml"), []byte(workflowYAML), 0600))
	}

	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir: t.TempDir(),
		DBPath:    ":memory:",
		Logger:    newTestLogger(t),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, registry.Close()) })
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:     "yaml-startup",
		Metadata: map[string]string{"role": "coordinator", "workflow": "yaml-startup"},
	})
	registry.RegisterConfig(&loomv1.AgentConfig{Name: "worker"})

	llm := &workflowStartupLLM{turns: make(chan workflowStartupTurn, 4)}
	coordinator := agent.NewAgent(&mockBackend{}, llm, agent.WithName("yaml-startup"))
	worker := agent.NewAgent(&mockBackend{}, &mockLLMForMultiAgent{}, agent.WithName("worker"))
	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{
		"yaml-startup": coordinator,
		"worker":       worker,
	}, registry)
	return srv, coordinator, llm
}

func TestSpawnWorkflowSubAgents_YAMLOnly(t *testing.T) {
	srv, coordinator, llm := newYAMLWorkflowStartupServer(t, yamlStartupDefinition+`  agents:
    - name: coordinator
      agent: yaml-startup
    - name: worker
      agent: worker
`)
	require.Empty(t, srv.registry.GetConfig("worker").Metadata["workflow"],
		"only the workflow YAML associates this worker with the workflow")

	ctx := context.Background()
	sessionID := GenerateSessionID()
	require.NoError(t, srv.spawnWorkflowSubAgents(ctx, coordinator, "yaml-startup", sessionID))

	srv.workflowSubAgentsMu.RLock()
	coordinatorCtx := srv.workflowSubAgents[sessionID+":yaml-startup"]
	workerCtx := srv.workflowSubAgents["auto:yaml-startup:worker"]
	contextCount := len(srv.workflowSubAgents)
	srv.workflowSubAgentsMu.RUnlock()
	require.NotNil(t, coordinatorCtx, "YAML-only workflows need the coordinator notification handler")
	require.NotNil(t, workerCtx)
	assert.Equal(t, 2, contextCount)
	assert.Same(t, coordinator, coordinatorCtx.agent)
	assert.Equal(t, sessionID, coordinatorCtx.sessionID)
	assert.Equal(t, "yaml-startup", coordinatorCtx.workflowID)
	assert.NotNil(t, coordinatorCtx.cancelFunc)
	assert.NotNil(t, coordinatorCtx.broadcastCancelFunc)
	assert.Equal(t, "worker", workerCtx.agent.GetName())

	workerNotify, registered := srv.messageQueue.GetNotificationChannel("yaml-startup:worker")
	require.True(t, registered)
	assert.Equal(t, workerCtx.notifyChan, workerNotify)
	_, registered = srv.messageQueue.GetNotificationChannel("yaml-startup")
	assert.False(t, registered, "coordinator queue notifications remain monitor-driven")

	subscriptions := srv.messageBus.GetSubscriptionsByAgent("yaml-startup")
	require.Len(t, subscriptions, 1)
	assert.Equal(t, "yaml-startup-events", subscriptions[0].Topic)
	_, registered = srv.messageBus.GetNotificationChannel(subscriptions[0].ID)
	require.True(t, registered)

	require.NoError(t, srv.messageQueue.Enqueue(ctx, &communication.QueueMessage{
		ID:          "yaml-worker-result",
		FromAgent:   "yaml-startup:worker",
		ToAgent:     "yaml-startup",
		MessageType: "response",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{Value: []byte("worker result: 42")},
		},
	}))
	// Deliver the monitor's signal directly, without waiting for its ticker.
	coordinatorCtx.notifyChan <- struct{}{}
	turn := llm.nextTurn(t)
	assert.Contains(t, turn.prompt, "[MESSAGE FROM yaml-startup:worker]:")
	assert.Contains(t, turn.prompt, "worker result: 42")
	assert.Contains(t, turn.systemPrompt, "Subscribed topics: yaml-startup-events",
		"the coordinator's workflow communication context must be attached before its first turn")

	_, err := srv.Publish(ctx, &loomv1.PublishRequest{
		Topic: "yaml-startup-events",
		Message: &loomv1.BusMessage{
			Id:        "yaml-worker-broadcast",
			FromAgent: "yaml-startup:worker",
			Payload: &loomv1.MessagePayload{
				Data: &loomv1.MessagePayload_Value{Value: []byte("worker broadcast")},
			},
		},
	})
	require.NoError(t, err)
	turn = llm.nextTurn(t)
	assert.Contains(t, turn.prompt, "[BROADCAST on topic 'yaml-startup-events' FROM yaml-startup:worker]")
	assert.Contains(t, turn.prompt, "worker broadcast")
}

func TestSpawnWorkflowSubAgents_NoWorkers(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{name: "no workflow file"},
		{name: "empty list", yaml: yamlStartupDefinition + "  agents: []\n"},
		{name: "coordinator only", yaml: yamlStartupDefinition + `  agents:
    - name: coordinator
      agent: yaml-startup
`},
		{name: "entry without agent", yaml: yamlStartupDefinition + `  agents:
    - name: worker
`},
		{name: "coordinator under another role", yaml: yamlStartupDefinition + `  agents:
    - name: worker
      agent: yaml-startup
`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, coordinator, llm := newYAMLWorkflowStartupServer(t, tt.yaml)
			ctx := context.Background()
			sessionID := GenerateSessionID()
			err := srv.spawnWorkflowSubAgents(ctx, coordinator, "yaml-startup", sessionID)
			require.EqualError(t, err, "no sub-agents found for workflow 'yaml-startup'")

			srv.workflowSubAgentsMu.RLock()
			contextCount := len(srv.workflowSubAgents)
			srv.workflowSubAgentsMu.RUnlock()
			assert.Zero(t, contextCount, "rejection must not leave workflow contexts or workers")
			assert.Empty(t, srv.messageBus.GetSubscriptionsByAgent("yaml-startup"),
				"rejection must not leave a coordinator subscription")
			assert.Empty(t, srv.messageBus.GetSubscriptionsByAgent("yaml-startup:worker"))
			for _, id := range []string{"yaml-startup", "yaml-startup:worker", "worker"} {
				_, registered := srv.messageQueue.GetNotificationChannel(id)
				assert.False(t, registered, "rejection registered notifications for %s", id)
			}
			assert.Empty(t, llm.turns, "rejection must not start a coordinator turn")

			_, err = coordinator.Chat(ctx, sessionID, "standalone turn")
			require.NoError(t, err)
			assert.NotContains(t, llm.nextTurn(t).systemPrompt, "WORKFLOW COMMUNICATION",
				"rejection must not attach workflow communication instructions")
		})
	}
}

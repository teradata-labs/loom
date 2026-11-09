// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestWovenWorkflow_EnabledReloadKeepsSpawnedWorkerAndTeamBlock(t *testing.T) {
	srv, coordinatorLLM, oldLLM, sessionID := wovenWorkflow(t)
	old, _, err := srv.getAgent("test-workflow:worker")
	require.NoError(t, err)

	newLLM := &replyingLLM{}
	replacement := agent.NewAgent(&mockBackend{}, newLLM, agent.WithName("test-workflow:worker"))
	replacement.SetID(old.GetID())
	require.NoError(t, srv.UpdateAgent(old.GetID(), replacement))

	require.NoError(t, srv.messageQueue.Enqueue(context.Background(), &communication.QueueMessage{
		ID:          "enabled-reload-task",
		FromAgent:   "test-workflow",
		ToAgent:     "test-workflow:worker",
		MessageType: "task",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{Value: []byte("compute after reload")},
		},
	}))
	require.Eventually(t, func() bool {
		return strings.Contains(oldLLM.message(), "compute after reload") ||
			strings.Contains(newLLM.message(), "compute after reload")
	}, 5*time.Second, 10*time.Millisecond)

	prompt := oldLLM.prompt()
	if newLLM.message() != "" {
		prompt = newLLM.prompt()
	}
	assert.Contains(t, prompt, "When a message arrives, send your answer back to its sender")
	assert.Empty(t, newLLM.message(), "running workers must keep their spawned instance")
	assert.Contains(t, oldLLM.message(), "compute after reload")

	srv.workflowSubAgentsMu.RLock()
	running := srv.workflowSubAgents[sessionID+":test-workflow:worker"]
	srv.workflowSubAgentsMu.RUnlock()
	require.NotNil(t, running)
	assert.Same(t, old, running.agent)
	require.Eventually(t, func() bool {
		return strings.Contains(coordinatorLLM.message(), "worker result: 42")
	}, 5*time.Second, 10*time.Millisecond)
}

func TestWovenWorkflow_DisabledReloadOnlyStopsOutgoingInstance(t *testing.T) {
	first := agent.NewAgent(&mockBackend{}, &replyingLLM{}, agent.WithName("worker"))
	other := agent.NewAgent(&mockBackend{}, &replyingLLM{}, agent.WithName("other-workflow:worker"))
	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{
		"worker":                first,
		"other-workflow:worker": other,
	}, nil)
	ctx := context.Background()
	require.NoError(t, srv.autoSpawnWorkflowSubAgent(ctx, "first-workflow:worker", "worker"))
	require.NoError(t, srv.autoSpawnWorkflowSubAgent(ctx, "other-workflow:worker"))

	replacement := agent.NewAgent(nil, nil, agent.WithName("worker"), agent.WithoutTools())
	replacement.SetID(first.GetID())
	require.NoError(t, srv.UpdateAgent(first.GetID(), replacement))

	srv.workflowSubAgentsMu.RLock()
	_, firstRunning := srv.workflowSubAgents["auto:first-workflow:worker"]
	otherContext := srv.workflowSubAgents["auto:other-workflow:worker"]
	activeCount := len(srv.workflowSubAgents)
	srv.workflowSubAgentsMu.RUnlock()
	assert.False(t, firstRunning)
	require.NotNil(t, otherContext, "a shared role name must not cancel another agent")
	assert.Same(t, other, otherContext.agent)
	assert.Equal(t, 1, activeCount)
	_, registered := srv.messageQueue.GetNotificationChannel("other-workflow:worker")
	assert.True(t, registered, "the other workflow must keep its notification registration")
	_, registered = srv.messageQueue.GetNotificationChannel("first-workflow:worker")
	assert.False(t, registered)
}

func TestWovenWorkflow_AutoSpawnIgnoresUnrelatedDisabledRoleConfig(t *testing.T) {
	actual := agent.NewAgent(&mockBackend{}, &replyingLLM{}, agent.WithName("configured-helper"))
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir: t.TempDir(),
		DBPath:    ":memory:",
		Logger:    newTestLogger(t),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })
	registry.RegisterConfig(&loomv1.AgentConfig{Name: "configured-helper"})
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:  "worker",
		Tools: &loomv1.ToolsConfig{Disabled: true},
	})
	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{"configured-helper": actual}, registry)

	require.NoError(t, srv.autoSpawnWorkflowSubAgent(context.Background(), "test-workflow:worker", "configured-helper"))
	srv.workflowSubAgentsMu.RLock()
	running := srv.workflowSubAgents["auto:test-workflow:worker"]
	srv.workflowSubAgentsMu.RUnlock()
	require.NotNil(t, running)
	assert.Same(t, actual, running.agent)
}

func TestWovenWorkflow_StartupKeepsValidatedWorkerInstances(t *testing.T) {
	coordinator := agent.NewAgent(&mockBackend{}, &replyingLLM{}, agent.WithName("test-workflow"))
	first := agent.NewAgent(&mockBackend{}, &replyingLLM{}, agent.WithName("test-workflow:first"))
	second := agent.NewAgent(&mockBackend{}, &replyingLLM{}, agent.WithName("test-workflow:second"))
	agents := map[string]*agent.Agent{
		"test-workflow":        coordinator,
		"test-workflow:first":  first,
		"test-workflow:second": second,
	}
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir: t.TempDir(),
		DBPath:    ":memory:",
		Logger:    newTestLogger(t),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })
	for name := range agents {
		role := "executor"
		if name == "test-workflow" {
			role = "coordinator"
		}
		registry.RegisterConfig(&loomv1.AgentConfig{
			Name:     name,
			Metadata: map[string]string{"role": role, "workflow": "test-workflow"},
		})
	}
	srv := setupBroadcastTestServer(t, agents, registry)
	replacement := agent.NewAgent(nil, nil, agent.WithName("test-workflow:second"), agent.WithoutTools())
	replacement.SetID(second.GetID())

	var swapped bool
	var once sync.Once
	srv.logger = srv.logger.WithOptions(zap.Hooks(func(entry zapcore.Entry) error {
		if entry.Message == "Registered coordinator for event-driven message notifications (monitor-based)" {
			once.Do(func() {
				require.NoError(t, srv.UpdateAgent(second.GetID(), replacement))
				swapped = true
			})
		}
		return nil
	}))

	sessionID := GenerateSessionID()
	require.NoError(t, srv.spawnWorkflowSubAgents(context.Background(), coordinator, "test-workflow", sessionID))
	require.True(t, swapped, "the replacement must land between validation and worker startup")
	srv.workflowSubAgentsMu.RLock()
	activeCount := len(srv.workflowSubAgents)
	secondContext := srv.workflowSubAgents[sessionID+":test-workflow:second"]
	srv.workflowSubAgentsMu.RUnlock()
	assert.Equal(t, 3, activeCount, "successful startup must own all its worker contexts")
	require.NotNil(t, secondContext)
	assert.Same(t, second, secondContext.agent, "startup must use the enabled instance it validated")
	require.ErrorIs(t, srv.spawnWorkflowSubAgents(context.Background(), coordinator, "test-workflow", GenerateSessionID()),
		ErrWorkflowParticipantToolsDisabled, "later startups must reject the disabled replacement")
}

func TestWovenWorkflow_RegistryWorkersKeepExactLookup(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "enabled_base", true: "disabled_base"}[disabled], func(t *testing.T) {
			coordinator := agent.NewAgent(nil, nil, agent.WithName("lookup-policy"))
			options := []agent.Option{agent.WithName("worker")}
			if disabled {
				options = append(options, agent.WithoutTools())
			}
			base := agent.NewAgent(nil, nil, options...)
			registry, err := agent.NewRegistry(agent.RegistryConfig{
				ConfigDir: t.TempDir(), DBPath: ":memory:", Logger: newTestLogger(t),
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = registry.Close() })
			registry.RegisterConfig(&loomv1.AgentConfig{
				Name:     "lookup-policy",
				Metadata: map[string]string{"role": "coordinator", "workflow": "lookup-policy"},
			})
			registry.RegisterConfig(&loomv1.AgentConfig{
				Name:     "lookup-policy:worker",
				Metadata: map[string]string{"role": "executor", "workflow": "lookup-policy"},
			})
			srv := setupBroadcastTestServer(t, map[string]*agent.Agent{
				"lookup-policy": coordinator, "worker": base,
			}, registry)

			require.NoError(t, srv.spawnWorkflowSubAgents(context.Background(), coordinator, "lookup-policy", GenerateSessionID()))
			srv.workflowSubAgentsMu.RLock()
			activeCount := len(srv.workflowSubAgents)
			for _, wctx := range srv.workflowSubAgents {
				assert.NotSame(t, base, wctx.agent, "a missing registry worker must not fall back to a base-name agent")
			}
			srv.workflowSubAgentsMu.RUnlock()
			assert.Equal(t, 1, activeCount, "only the coordinator should start, as with exact lookup on main")
			_, registered := srv.messageQueue.GetNotificationChannel("lookup-policy:worker")
			assert.False(t, registered)
		})
	}
}

func TestWovenWorkflow_YAMLOnlyStillReturnsNoSubAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".loom", "workflows")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "yaml-only-policy.yaml"), []byte(`apiVersion: loom/v1
kind: Workflow
metadata:
  name: yaml-only-policy
spec:
  communication:
    topic: yaml-policy-events
  agents:
    - name: worker
      agent: helper
`), 0o600))

	coordinator := agent.NewAgent(nil, nil, agent.WithName("yaml-only-policy"))
	helper := agent.NewAgent(nil, nil, agent.WithName("helper"))
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir: t.TempDir(), DBPath: ":memory:", Logger: newTestLogger(t),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:     "yaml-only-policy",
		Metadata: map[string]string{"role": "coordinator", "workflow": "yaml-only-policy"},
	})
	registry.RegisterConfig(&loomv1.AgentConfig{Name: "helper"})
	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{
		"yaml-only-policy": coordinator, "helper": helper,
	}, registry)

	// Same behavior as main: the coordinator subscribes and the YAML worker
	// starts, and the call still reports that no registry sub-agents exist.
	err = srv.spawnWorkflowSubAgents(context.Background(), coordinator, "yaml-only-policy", GenerateSessionID())
	require.ErrorContains(t, err, "no sub-agents found")
	srv.workflowSubAgentsMu.RLock()
	activeCount := len(srv.workflowSubAgents)
	srv.workflowSubAgentsMu.RUnlock()
	assert.Equal(t, 1, activeCount, "the YAML worker must start, as on main")
	assert.Len(t, srv.messageBus.GetSubscriptionsByAgent("yaml-only-policy"), 1,
		"the coordinator must subscribe to its workflow topic, as on main")
	_, registered := srv.messageQueue.GetNotificationChannel("yaml-only-policy:worker")
	assert.True(t, registered)
}

func TestSpawn_DisabledToolsRequiresNoSubscriptions(t *testing.T) {
	for _, subscribed := range []bool{false, true} {
		t.Run(map[bool]string{false: "one_shot", true: "auto_subscribe"}[subscribed], func(t *testing.T) {
			srv := spawnTestServer(t, &replyingLLM{})
			srv.registry.RegisterConfig(&loomv1.AgentConfig{
				Name: "helper", Tools: &loomv1.ToolsConfig{Disabled: true},
			})
			parentID := parentSession(t, srv)
			before, err := srv.sessionStore.ListSessions(context.Background())
			require.NoError(t, err)
			req := &builtin.SpawnSubAgentRequest{
				ParentSessionID: parentID,
				ParentAgentID:   "parent",
				WorkflowID:      "spawn-policy",
				AgentID:         "helper",
				InitialMessage:  "return the answer directly",
			}
			if subscribed {
				req.AutoSubscribe = []string{"policy-events"}
			}
			resp, err := srv.SpawnSubAgent(context.Background(), req)
			if subscribed {
				require.ErrorIs(t, err, ErrWorkflowParticipantToolsDisabled)
				assert.Nil(t, resp)
				after, listErr := srv.sessionStore.ListSessions(context.Background())
				require.NoError(t, listErr)
				assert.ElementsMatch(t, before, after, "rejection must happen before saving a child session")
				assert.Empty(t, srv.messageBus.GetSubscriptionsByAgent("spawn-policy:helper"))
			} else {
				require.NoError(t, err)
				require.NotNil(t, resp)
				assert.Equal(t, "completed", resp.Status)
				assert.Equal(t, "done", resp.Output)
			}
			assert.Zero(t, srv.countSpawnedAgentsByParent(parentID))
			actual, getErr := srv.registry.GetAgent(context.Background(), "helper")
			require.NoError(t, getErr)
			assert.Empty(t, actual.ListTools())
		})
	}
}

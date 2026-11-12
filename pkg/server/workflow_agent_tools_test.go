// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"
)

// The agents ExecuteWorkflow loads come from the registry, whose builtin
// table has no shared_memory_read; the server injects its communication
// tools only into agents added through AddAgent. A workflow agent must still
// be able to fetch the hand-off outputs the orchestrator stashes, so
// getOrLoadAgent gives registry-loaded agents the same tools, once.
func TestGetOrLoadAgent_RegistryAgentGetsCommunicationTools(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "presenter.yaml"), []byte(`apiVersion: loom/v1
kind: Agent
metadata:
  name: presenter
spec:
  system_prompt: Build the deck.
`), 0o600))
	reg, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   dir,
		DBPath:      filepath.Join(dir, "registry.db"),
		Logger:      zaptest.NewLogger(t),
		LLMProvider: &mockLLMForMultiAgent{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })
	require.NoError(t, reg.LoadAgents(context.Background()))

	store, err := communication.NewSharedMemoryStore(observability.NewNoOpTracer(), zaptest.NewLogger(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	s := &MultiAgentServer{logger: zaptest.NewLogger(t), agents: map[string]*agent.Agent{}}
	s.SetAgentRegistry(reg)
	s.sharedMemoryComm = store

	ag, err := s.getOrLoadAgent(context.Background(), "presenter", reg)
	require.NoError(t, err)
	tools := ag.ListTools()
	assert.Contains(t, tools, "shared_memory_read")
	assert.Contains(t, tools, "shared_memory_write")
	count := ag.ToolCount()

	again, err := s.getOrLoadAgent(context.Background(), "presenter", reg)
	require.NoError(t, err)
	assert.Same(t, ag, again, "the registry caches the agent")
	assert.Equal(t, count, again.ToolCount(), "a second load does not register the tools twice")

	// Without any communication component configured, nothing is injected.
	bare := &MultiAgentServer{logger: zaptest.NewLogger(t), agents: map[string]*agent.Agent{}}
	reg2, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   dir,
		DBPath:      filepath.Join(dir, "registry2.db"),
		Logger:      zaptest.NewLogger(t),
		LLMProvider: &mockLLMForMultiAgent{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg2.Close() })
	require.NoError(t, reg2.LoadAgents(context.Background()))
	bare.SetAgentRegistry(reg2)
	plain, err := bare.getOrLoadAgent(context.Background(), "presenter", reg2)
	require.NoError(t, err)
	assert.NotContains(t, plain.ListTools(), "shared_memory_read")
}

// The server installs no HITL handler and keeps no checkpoint, so a gated
// pipeline it runs suspends and is never resumed. Its stash must not outlive
// the suspension in the server's shared store.
func TestExecuteWorkflow_SuspendedPipelineLeavesNoStash(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	for _, name := range []string{"s1", "s2"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", name+".yaml"), []byte("apiVersion: loom/v1\nkind: Agent\nmetadata:\n  name: "+name+"\nspec:\n  system_prompt: Do the stage.\n"), 0o600))
	}
	reg, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   dir,
		DBPath:      filepath.Join(dir, "registry.db"),
		Logger:      zaptest.NewLogger(t),
		LLMProvider: &mockLLMForMultiAgent{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })
	require.NoError(t, reg.LoadAgents(context.Background()))

	store, err := communication.NewSharedMemoryStore(observability.NewNoOpTracer(), zaptest.NewLogger(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	s := &MultiAgentServer{logger: zaptest.NewLogger(t), agents: map[string]*agent.Agent{}, workflowStore: NewWorkflowStore()}
	s.SetAgentRegistry(reg)
	s.sharedMemoryComm = store

	keys := func() []string {
		resp, lerr := store.List(context.Background(), &loomv1.ListSharedMemoryKeysRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW, AgentId: "test",
		})
		require.NoError(t, lerr)
		return resp.Keys
	}
	pipeline := func(id string, gate *loomv1.HITLGate) *loomv1.ExecuteWorkflowRequest {
		return &loomv1.ExecuteWorkflowRequest{Pattern: &loomv1.WorkflowPattern{WorkflowId: id,
			Pattern: &loomv1.WorkflowPattern_Pipeline{Pipeline: &loomv1.PipelinePattern{InitialPrompt: "go",
				Stages: []*loomv1.PipelineStage{
					{AgentId: "s1", PromptTemplate: "Research"},
					{AgentId: "s2", PromptTemplate: "Review: {{previous}}", HitlGate: gate},
				}}}}}
	}

	_, err = s.ExecuteWorkflow(context.Background(), pipeline("gated-run", &loomv1.HITLGate{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "suspended")
	for _, key := range keys() {
		assert.NotContains(t, key, "gated-run-", "the suspended run's stash stayed in the server store: %s", key)
	}

	_, err = s.ExecuteWorkflow(context.Background(), pipeline("plain-run", nil))
	require.NoError(t, err)
	for _, key := range keys() {
		assert.NotContains(t, key, "plain-run-", "the completed run's stash stayed in the server store: %s", key)
	}
}

// With a bus but no store the tool set has no shared_memory_read; the
// once-only guard must still hold, so a second load registers nothing.
func TestEnsureCommunicationTools_OncePerAgentWithBusOnly(t *testing.T) {
	bus := communication.NewMessageBus(communication.NewMemoryStore(0), nil, observability.NewNoOpTracer(), zaptest.NewLogger(t))
	s := &MultiAgentServer{logger: zaptest.NewLogger(t), agents: map[string]*agent.Agent{}, messageBus: bus}
	ag := agent.NewAgent(nil, &mockLLMForMultiAgent{}, agent.WithName("presenter"))

	s.ensureCommunicationTools(ag, "presenter")
	tools := ag.ListTools()
	assert.Contains(t, tools, "publish")
	assert.Contains(t, tools, "publish_restart_request", "a bus means a workflow agent can request a restart of its own run")
	assert.NotContains(t, tools, "shared_memory_read")
	count := ag.ToolCount()

	s.ensureCommunicationTools(ag, "presenter")
	assert.Equal(t, count, ag.ToolCount(), "nothing registered twice")
}

type fakeAgentRegistry struct{}

func (fakeAgentRegistry) GetConfig(string) *loomv1.AgentConfig { return nil }

// The registry wiring is one helper for every injection site: it finds the
// send_message tool when a queue produced one and reports when there is
// none to wire (bus or store only).
func TestWireSendMessageRegistry(t *testing.T) {
	bus := communication.NewMessageBus(communication.NewMemoryStore(0), nil, observability.NewNoOpTracer(), zaptest.NewLogger(t))
	busOnly := builtin.CommunicationTools(nil, bus, nil, "a")
	assert.False(t, wireSendMessageRegistry(busOnly, fakeAgentRegistry{}, zaptest.NewLogger(t)), "no send_message without a queue")

	queue, err := communication.NewMessageQueue(filepath.Join(t.TempDir(), "q.db"), observability.NewNoOpTracer(), zaptest.NewLogger(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = queue.Close() })
	withQueue := builtin.CommunicationTools(queue, bus, nil, "a")
	assert.True(t, wireSendMessageRegistry(withQueue, fakeAgentRegistry{}, zaptest.NewLogger(t)))
}

// An agent whose config suppresses one of the injected names (RegisterTool
// skips it) must still be wired once: the guard treats a suppressed name as
// satisfied, so later workflow runs neither re-register nor log again, and
// nothing about the agent is retained by the server.
func TestEnsureCommunicationTools_SuppressedNameIsInjectedOnce(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	bus := communication.NewMessageBus(communication.NewMemoryStore(0), nil, observability.NewNoOpTracer(), zaptest.NewLogger(t))
	s := &MultiAgentServer{logger: zap.New(core), agents: map[string]*agent.Agent{}, messageBus: bus}
	ag := agent.NewAgent(nil, &mockLLMForMultiAgent{}, agent.WithName("presenter"), agent.WithoutBuiltinTool("publish"))

	s.ensureCommunicationTools(ag, "presenter")
	assert.NotContains(t, ag.ListTools(), "publish", "the suppressed name stays out")
	assert.Contains(t, ag.ListTools(), "publish_restart_request")
	count := ag.ToolCount()
	s.ensureCommunicationTools(ag, "presenter")
	s.ensureCommunicationTools(ag, "presenter")
	assert.Equal(t, count, ag.ToolCount())
	assert.Equal(t, 1, logs.FilterMessage("Communication tools injected into workflow agent").Len(),
		"a suppressed builtin name must not cause a re-registration and a log line on every run")
}

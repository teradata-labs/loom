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
	"go.uber.org/zap/zaptest"
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
	assert.NotContains(t, tools, "shared_memory_read")
	count := ag.ToolCount()

	s.ensureCommunicationTools(ag, "presenter")
	assert.Equal(t, count, ag.ToolCount(), "nothing registered twice")
}

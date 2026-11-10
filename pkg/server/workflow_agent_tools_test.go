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

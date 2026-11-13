// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build fts5

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"go.uber.org/zap/zaptest"
)

// toolsNoneStubLLM is the provider the registry needs to build an agent; no
// test here chats with it.
type toolsNoneStubLLM struct{}

func (toolsNoneStubLLM) Name() string  { return "stub" }
func (toolsNoneStubLLM) Model() string { return "stub-model" }
func (toolsNoneStubLLM) Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return &llmtypes.LLMResponse{Content: "ok", StopReason: "stop"}, nil
}

// workflowInjectedTools builds the tools MultiAgentServer.ensureCommunicationTools
// injects on a workflow agent when every communication component is wired, from
// the factories themselves, so the tools.none list is checked against the real
// set and not against a copy of it.
func workflowInjectedTools(t *testing.T) []shuttle.Tool {
	t.Helper()
	tracer, logger := observability.NewNoOpTracer(), zaptest.NewLogger(t)
	queue, err := communication.NewMessageQueue(filepath.Join(t.TempDir(), "queue.db"), tracer, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = queue.Close() })
	bus := communication.NewMessageBus(communication.NewMemoryStore(0), nil, tracer, logger)
	store, err := communication.NewSharedMemoryStore(tracer, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return append(builtin.CommunicationTools(queue, bus, store, "reviewer"), builtin.NewPublishRestartTool(bus, "reviewer"))
}

// tools.none promises an agent sees only what its YAML declares, so every
// tool the server injects on workflow agents must be on the list, the
// restart tool included. The expected names come from the factories, so a
// tool added to the injected set without a list entry fails here.
func TestBuiltinToolsToSuppressNoneModeCoversWorkflowInjectedTools(t *testing.T) {
	withViperMinimalMode(t, false, true)
	suppressed := builtinToolsToSuppress()
	injected := workflowInjectedTools(t)
	require.NotEmpty(t, injected)
	for _, tool := range injected {
		assert.Contains(t, suppressed, tool.Name(), "tools.none must suppress the workflow-injected tool %s", tool.Name())
	}
}

// The list reaches a registry-built agent the way looms serve wires it
// (wireRegistrySubsystems → Registry.SetSuppressedBuiltinTools → buildAgent),
// and the agent then refuses every injected tool at RegisterTool: under
// tools.none a workflow agent on looms serve keeps exactly its YAML tools.
func TestToolsNone_RegistryBuiltAgentRefusesTheWorkflowInjectedTools(t *testing.T) {
	withViperMinimalMode(t, false, true)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "reviewer.yaml"), []byte(`apiVersion: loom/v1
kind: Agent
metadata:
  name: reviewer
spec:
  system_prompt: Review the design.
`), 0o600))
	reg, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   dir,
		DBPath:      filepath.Join(dir, "registry.db"),
		Logger:      zaptest.NewLogger(t),
		LLMProvider: toolsNoneStubLLM{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })
	require.NoError(t, reg.LoadAgents(context.Background()))
	reg.SetSuppressedBuiltinTools(builtinToolsToSuppress())

	ag, err := reg.GetAgent(context.Background(), "reviewer")
	require.NoError(t, err)
	before := ag.ListTools()
	injected := workflowInjectedTools(t)
	for _, tool := range injected {
		assert.True(t, ag.IsBuiltinToolSuppressed(tool.Name()), "%s must be suppressed on a registry-built agent", tool.Name())
	}

	ag.RegisterTools(injected...)

	assert.ElementsMatch(t, before, ag.ListTools(), "the agent keeps exactly the tools it had")
	for _, tool := range injected {
		assert.NotContains(t, ag.ListTools(), tool.Name())
	}
}

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type deploymentPromptLLM struct {
	mu      sync.Mutex
	systems []string
}

func (m *deploymentPromptLLM) Chat(_ context.Context, messages []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, message := range messages {
		if message.Role == "system" {
			m.systems = append(m.systems, message.Content)
		}
	}
	return &llmtypes.LLMResponse{Content: "ok", StopReason: "end_turn"}, nil
}

func (m *deploymentPromptLLM) Name() string  { return "deployment-prompt-recorder" }
func (m *deploymentPromptLLM) Model() string { return "deployment-prompt-model" }

func (m *deploymentPromptLLM) lastSystem(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotEmpty(t, m.systems)
	return m.systems[len(m.systems)-1]
}

func newDeploymentRegistry(t *testing.T) (*agent.Registry, *deploymentPromptLLM, string) {
	t.Helper()
	dir := t.TempDir()
	llm := &deploymentPromptLLM{}
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   dir,
		DBPath:      filepath.Join(dir, "registry.db"),
		LLMProvider: llm,
		Logger:      zaptest.NewLogger(t),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, registry.Close()) })
	return registry, llm, dir
}

func TestDeploymentRegistryPromptDelivered(t *testing.T) {
	registry, llm, _ := newDeploymentRegistry(t)
	srv := newTestMultiAgentServer(t)
	srv.SetAgentRegistry(registry)
	const prompt = "Score each Mastermind guess against the hidden code."
	created, err := srv.CreateAgentFromConfig(context.Background(), &loomv1.CreateAgentRequest{
		Config: &loomv1.AgentConfig{
			Name:         "deployed-registry-agent",
			Description:  "Pilot prompt verification",
			SystemPrompt: prompt,
			Rom:          "none",
			Behavior:     &loomv1.BehaviorConfig{MaxIterations: 2},
		},
	})
	require.NoError(t, err)
	runtime, _, err := srv.getAgent(created.Id)
	require.NoError(t, err)
	assert.Equal(t, 2, runtime.GetConfig().MaxIterations)
	assert.Equal(t, "Pilot prompt verification", runtime.GetDescription())
	_, err = runtime.Chat(context.Background(), "deployment-prompt-session", "Reply ok.")
	require.NoError(t, err)
	assert.True(t, strings.Contains(llm.lastSystem(t), prompt))
}

func TestDeploymentFileReloadReplacesRegistryRuntime(t *testing.T) {
	registry, llm, dir := newDeploymentRegistry(t)
	srv := newTestMultiAgentServer(t)
	srv.SetAgentRegistry(registry)
	ctx := context.Background()
	created, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
		Config: &loomv1.AgentConfig{Name: "deployed-reload", SystemPrompt: "Old rules."},
	})
	require.NoError(t, err)
	before, _, err := srv.getAgent(created.Id)
	require.NoError(t, err)

	updated := &loomv1.AgentConfig{
		Name:         "deployed-reload",
		Description:  "Updated",
		SystemPrompt: "New rules.",
		Rom:          "none",
		Behavior:     &loomv1.BehaviorConfig{MaxIterations: 2},
	}
	require.NoError(t, agent.SaveAgentConfig(updated, filepath.Join(dir, "agents", updated.Name+".yaml")))
	response, err := srv.ReloadAgent(ctx, &loomv1.ReloadAgentRequest{AgentId: created.Id, ReloadFromFile: true})
	require.NoError(t, err)
	after, _, err := srv.getAgent(created.Id)
	require.NoError(t, err)
	assert.NotSame(t, before, after)
	assert.Equal(t, created.Id, after.GetID())
	assert.Equal(t, updated.SystemPrompt, response.Config.SystemPrompt)
	assert.Equal(t, updated.Rom, after.GetConfig().Rom)
	assert.Equal(t, 2, after.GetConfig().MaxIterations)
	_, err = after.Chat(ctx, "deployment-reloaded-session", "Reply ok.")
	require.NoError(t, err)
	assert.Contains(t, llm.lastSystem(t), updated.SystemPrompt)
}

func TestDeploymentFileReloadUsesStaticServerCallback(t *testing.T) {
	registry, llm, dir := newDeploymentRegistry(t)
	ctx := context.Background()
	config := &loomv1.AgentConfig{Name: "deployed-static", SystemPrompt: "Before."}
	path := filepath.Join(dir, "agents", config.Name+".yaml")
	require.NoError(t, agent.SaveAgentConfig(config, path))
	require.NoError(t, registry.LoadAgents(ctx))
	info, err := registry.GetAgentInfo(config.Name)
	require.NoError(t, err)

	srv := newTestMultiAgentServer(t)
	srv.SetAgentRegistry(registry)
	before := agent.NewAgent(&mockBackend{}, llm, agent.WithName(config.Name), agent.WithSystemPrompt(config.SystemPrompt))
	before.SetID(info.ID)
	srv.AddAgent(info.ID, before)
	var callbackAgent *agent.Agent
	registry.SetReloadCallback(func(name, guid string, updated *loomv1.AgentConfig) error {
		callbackAgent = agent.NewAgent(&mockBackend{}, llm, agent.WithName(name), agent.WithSystemPrompt(updated.SystemPrompt))
		callbackAgent.SetID(guid)
		return srv.UpdateAgent(guid, callbackAgent)
	})

	config.SystemPrompt = "After."
	require.NoError(t, agent.SaveAgentConfig(config, path))
	_, err = srv.ReloadAgent(ctx, &loomv1.ReloadAgentRequest{AgentId: info.ID, ReloadFromFile: true})
	require.NoError(t, err)
	after, _, err := srv.getAgent(info.ID)
	require.NoError(t, err)
	assert.Same(t, callbackAgent, after)
	assert.NotSame(t, before, after)
	assert.Equal(t, info.ID, after.GetID())
	_, err = after.Chat(ctx, "deployment-static-session", "Reply ok.")
	require.NoError(t, err)
	assert.Contains(t, llm.lastSystem(t), config.SystemPrompt)

	registry.SetReloadCallback(func(string, string, *loomv1.AgentConfig) error { return nil })
	_, err = srv.ReloadAgent(ctx, &loomv1.ReloadAgentRequest{AgentId: info.ID, ReloadFromFile: true})
	require.Error(t, err)
	assert.Equal(t, codes.Internal, status.Code(err))
}

func TestDeploymentFileReloadReconcilesCallbackGUIDMismatch(t *testing.T) {
	registry, llm, dir := newDeploymentRegistry(t)
	ctx := context.Background()
	config := &loomv1.AgentConfig{Name: "deployed-mismatch", SystemPrompt: "Before."}
	path := filepath.Join(dir, "agents", config.Name+".yaml")
	require.NoError(t, agent.SaveAgentConfig(config, path))
	require.NoError(t, registry.LoadAgents(ctx))
	info, err := registry.GetAgentInfo(config.Name)
	require.NoError(t, err)

	srv := NewMultiAgentServer(map[string]*agent.Agent{}, nil)
	srv.SetAgentRegistry(registry)
	logger := zaptest.NewLogger(t)
	queue, err := communication.NewMessageQueue(":memory:", observability.NewNoOpTracer(), logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = queue.Close() })
	require.NoError(t, srv.ConfigureCommunication(nil, queue, nil, nil, nil, logger))

	// Register the server agent under a different GUID than the registry's info.ID
	const serverID = "server-custom-guid"
	require.NotEqual(t, serverID, info.ID)
	before := agent.NewAgent(&mockBackend{}, llm, agent.WithName(config.Name), agent.WithSystemPrompt(config.SystemPrompt))
	before.SetID(serverID)
	before.AdoptApprovedSet(shuttle.NewApprovedSet())
	sctx := session.WithSessionID(ctx, "sess-mismatch")
	approvedCall := shuttle.Canonicalize("execute_query", map[string]interface{}{"sql": "SELECT 42"}, "sql")
	require.NoError(t, before.ApprovedSet().Record(sctx, "approved_sql", []shuttle.CallIdentity{approvedCall}))
	srv.AddAgent(serverID, before)

	var callbackAgent *agent.Agent
	registry.SetReloadCallback(func(name, guid string, updated *loomv1.AgentConfig) error {
		callbackAgent = agent.NewAgent(&mockBackend{}, llm, agent.WithName(name), agent.WithSystemPrompt(updated.SystemPrompt))
		callbackAgent.SetID(guid)
		srv.AddAgent(guid, callbackAgent)
		return nil
	})

	config.SystemPrompt = "After mismatch reconciliation."
	require.NoError(t, agent.SaveAgentConfig(config, path))

	_, err = srv.ReloadAgent(ctx, &loomv1.ReloadAgentRequest{AgentId: serverID, ReloadFromFile: true})
	require.NoError(t, err)

	after, _, err := srv.getAgent(serverID)
	require.NoError(t, err)
	assert.Same(t, callbackAgent, after)
	assert.Equal(t, serverID, after.GetID())
	ok, err := after.ApprovedSet().Contains(sctx, "approved_sql", approvedCall)
	require.NoError(t, err)
	assert.True(t, ok, "existing session must retain its approved set across GUID-reconciled reload")

	// Verify communication tools on the replacement use the canonical serverID, not info.ID
	var sendTool shuttle.Tool
	for _, tool := range after.RegisteredTools() {
		if tool.Name() == "send_message" {
			sendTool = tool
			break
		}
	}
	require.NotNil(t, sendTool, "send_message tool must be injected on the replacement agent")
	res, err := sendTool.Execute(ctx, map[string]interface{}{
		"to_agent": "peer-agent",
		"message":  "ping after reload",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.True(t, res.Success, "send_message failed: %+v", res.Error)
	msg, err := queue.Dequeue(ctx, "peer-agent")
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, serverID, msg.FromAgent, "communication tools must use the original server GUID")

	// Verify the duplicate registry GUID key was cleaned up and exactly one entry survives
	assert.Equal(t, []string{serverID}, srv.GetAgentIDs(), "exactly one agent entry must survive")
}

func TestDeploymentFileReloadRegistryOwnedConflictReturnsAborted(t *testing.T) {
	dir := t.TempDir()
	llm := &deploymentPromptLLM{}
	var registry *agent.Registry
	reloading := false
	logger := zaptest.NewLogger(t, zaptest.WrapOptions(zap.Hooks(func(entry zapcore.Entry) error {
		if reloading && entry.Message == "Using default LLM provider" && registry != nil {
			registry.RegisterConfig(&loomv1.AgentConfig{
				Name:         "deployed-reg-conflict",
				SystemPrompt: "Concurrent edit during rebuild.",
			})
		}
		return nil
	})))
	var err error
	registry, err = agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   dir,
		DBPath:      filepath.Join(dir, "agents.db"),
		LLMProvider: llm,
		Logger:      logger,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })

	srv := newTestMultiAgentServer(t)
	srv.SetAgentRegistry(registry)
	ctx := context.Background()

	initial := &loomv1.AgentConfig{
		Name:         "deployed-reg-conflict",
		SystemPrompt: "Initial prompt.",
	}
	created, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{Config: initial})
	require.NoError(t, err)

	path := filepath.Join(dir, "agents", initial.Name+".yaml")
	updated := &loomv1.AgentConfig{
		Name:         initial.Name,
		SystemPrompt: "Updated prompt.",
	}
	require.NoError(t, agent.SaveAgentConfig(updated, path))

	reloading = true
	_, err = srv.ReloadAgent(ctx, &loomv1.ReloadAgentRequest{AgentId: created.Id, ReloadFromFile: true})
	require.Error(t, err)
	assert.Equal(t, codes.Aborted, status.Code(err))
}

func TestDeploymentFileReloadConcurrentChangeReturnsAborted(t *testing.T) {
	registry, llm, dir := newDeploymentRegistry(t)
	ctx := context.Background()
	config := &loomv1.AgentConfig{Name: "deployed-conflict", SystemPrompt: "Initial."}
	path := filepath.Join(dir, "agents", config.Name+".yaml")
	require.NoError(t, agent.SaveAgentConfig(config, path))
	require.NoError(t, registry.LoadAgents(ctx))
	info, err := registry.GetAgentInfo(config.Name)
	require.NoError(t, err)

	srv := newTestMultiAgentServer(t)
	srv.SetAgentRegistry(registry)
	before := agent.NewAgent(&mockBackend{}, llm, agent.WithName(config.Name), agent.WithSystemPrompt(config.SystemPrompt))
	before.SetID(info.ID)
	srv.AddAgent(info.ID, before)

	// Callback simulates a concurrent reload conflict (ErrAgentChanged)
	registry.SetReloadCallback(func(string, string, *loomv1.AgentConfig) error {
		return agent.ErrAgentChanged
	})

	config.SystemPrompt = "Reloaded."
	require.NoError(t, agent.SaveAgentConfig(config, path))

	_, err = srv.ReloadAgent(ctx, &loomv1.ReloadAgentRequest{AgentId: info.ID, ReloadFromFile: true})
	require.Error(t, err)
	assert.Equal(t, codes.Aborted, status.Code(err))
}

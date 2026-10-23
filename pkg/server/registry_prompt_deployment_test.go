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
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
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

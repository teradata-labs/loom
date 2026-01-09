// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"go.uber.org/zap"
)

// TestHotReloadIntegration tests the full hot-reload flow
// This test bypasses fsnotify (which is unreliable on macOS) and directly triggers callbacks
func TestHotReloadIntegration(t *testing.T) {
	// Skip if ANTHROPIC_API_KEY not set (integration test requires real LLM provider)
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set - skipping integration test")
	}

	tmpDir := t.TempDir()
	logger := zap.NewNop()

	// Create registry with agent
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	// Create initial agent config
	config := &loomv1.AgentConfig{
		Name:        "hotreload-test",
		Description: "Initial description",
		Llm: &loomv1.LLMConfig{
			Provider:    "anthropic",
			Model:       "claude-3",
			Temperature: 0.7,
			MaxTokens:   4096,
		},
		SystemPrompt: "You are a test assistant",
		Memory: &loomv1.MemoryConfig{
			Type:       "memory",
			MaxHistory: 50,
		},
		Behavior: &loomv1.BehaviorConfig{
			MaxIterations:  10,
			TimeoutSeconds: 300,
		},
	}

	configPath := filepath.Join(agentsDir, "hotreload-test.yaml")
	err = agent.SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	// Create registry
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   tmpDir,
		DBPath:      filepath.Join(tmpDir, "test.db"),
		MCPManager:  nil,
		LLMProvider: &mockLLMForMultiAgent{},
		Logger:      logger,
	})
	require.NoError(t, err)
	defer registry.Close()

	// Load agents
	ctx := context.Background()
	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	// Create agent
	ag, err := registry.CreateAgent(ctx, "hotreload-test")
	require.NoError(t, err)
	require.NotNil(t, ag)
	assert.Equal(t, "Initial description", ag.GetDescription())

	// Create multi-agent server
	agents := map[string]*agent.Agent{
		"hotreload-test": ag,
	}
	loomService := NewMultiAgentServer(agents, nil)

	// Track callback invocations
	callbackInvoked := false
	var callbackName string
	var callbackConfig *loomv1.AgentConfig

	// Set reload callback that updates the server
	registry.SetReloadCallback(func(name string, agentConfig *loomv1.AgentConfig) error {
		callbackInvoked = true
		callbackName = name
		callbackConfig = agentConfig

		// Create new agent with updated config
		backend := &mockBackend{}
		llm := &mockLLMForMultiAgent{}

		cfg := &agent.Config{
			Name:         agentConfig.Name,
			Description:  agentConfig.Description,
			SystemPrompt: agentConfig.SystemPrompt,
		}

		newAgent := agent.NewAgent(backend, llm, agent.WithConfig(cfg))

		// Update in multi-agent server
		return loomService.UpdateAgent(name, newAgent)
	})

	// Modify agent config
	config.Description = "Updated description after reload"
	err = agent.SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	// Force reload (this simulates what WatchConfigs does)
	err = registry.ForceReload(ctx, "hotreload-test")
	require.NoError(t, err)

	// Verify callback was invoked
	assert.True(t, callbackInvoked)
	assert.Equal(t, "hotreload-test", callbackName)
	assert.NotNil(t, callbackConfig)
	assert.Equal(t, "Updated description after reload", callbackConfig.Description)

	// Verify agent in server was updated
	updatedAgent, _, err := loomService.getAgent("hotreload-test")
	require.NoError(t, err)
	assert.Equal(t, "Updated description after reload", updatedAgent.GetDescription())
}

// TestHotReloadIntegration_MultipleAgents tests hot-reload with multiple agents
func TestHotReloadIntegration_MultipleAgents(t *testing.T) {
	// Skip if ANTHROPIC_API_KEY not set (integration test requires real LLM provider)
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set - skipping integration test")
	}

	tmpDir := t.TempDir()
	logger := zap.NewNop()

	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	// Create registry
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   tmpDir,
		DBPath:      filepath.Join(tmpDir, "test.db"),
		MCPManager:  nil,
		LLMProvider: &mockLLMForMultiAgent{},
		Logger:      logger,
	})
	require.NoError(t, err)
	defer registry.Close()

	// Create multiple agent configs
	agentNames := []string{"agent1", "agent2", "agent3"}
	for _, name := range agentNames {
		config := &loomv1.AgentConfig{
			Name:        name,
			Description: "Initial " + name,
			Llm: &loomv1.LLMConfig{
				Provider:    "anthropic",
				Model:       "claude-3",
				Temperature: 0.7,
				MaxTokens:   4096,
			},
			SystemPrompt: "You are " + name,
			Memory: &loomv1.MemoryConfig{
				Type:       "memory",
				MaxHistory: 50,
			},
			Behavior: &loomv1.BehaviorConfig{
				MaxIterations:  10,
				TimeoutSeconds: 300,
			},
		}

		configPath := filepath.Join(agentsDir, name+".yaml")
		err = agent.SaveAgentConfig(config, configPath)
		require.NoError(t, err)
	}

	// Load and create agents
	ctx := context.Background()
	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	agents := make(map[string]*agent.Agent)
	for _, name := range agentNames {
		ag, err := registry.CreateAgent(ctx, name)
		require.NoError(t, err)
		agents[name] = ag
	}

	// Create multi-agent server
	loomService := NewMultiAgentServer(agents, nil)

	// Set reload callback
	reloadedAgents := make(map[string]bool)
	registry.SetReloadCallback(func(name string, agentConfig *loomv1.AgentConfig) error {
		reloadedAgents[name] = true

		backend := &mockBackend{}
		llm := &mockLLMForMultiAgent{}

		cfg := &agent.Config{
			Name:         agentConfig.Name,
			Description:  agentConfig.Description,
			SystemPrompt: agentConfig.SystemPrompt,
		}

		newAgent := agent.NewAgent(backend, llm, agent.WithConfig(cfg))
		return loomService.UpdateAgent(name, newAgent)
	})

	// Update each agent config and trigger reload
	for _, name := range agentNames {
		configPath := filepath.Join(agentsDir, name+".yaml")
		config, err := agent.LoadAgentConfig(configPath)
		require.NoError(t, err)

		config.Description = "Updated " + name
		err = agent.SaveAgentConfig(config, configPath)
		require.NoError(t, err)

		// Force reload
		err = registry.ForceReload(ctx, name)
		require.NoError(t, err)

		// Small delay to avoid race
		time.Sleep(10 * time.Millisecond)
	}

	// Verify all agents were reloaded
	assert.Len(t, reloadedAgents, 3)
	for _, name := range agentNames {
		assert.True(t, reloadedAgents[name], "Agent %s should have been reloaded", name)

		updatedAgent, _, err := loomService.getAgent(name)
		require.NoError(t, err)
		assert.Equal(t, "Updated "+name, updatedAgent.GetDescription())
	}
}

// TestHotReloadIntegration_CallbackError tests error handling during hot-reload
func TestHotReloadIntegration_CallbackError(t *testing.T) {
	// Skip if ANTHROPIC_API_KEY not set (integration test requires real LLM provider)
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set - skipping integration test")
	}

	tmpDir := t.TempDir()
	logger := zap.NewNop()

	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	// Create registry
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   tmpDir,
		DBPath:      filepath.Join(tmpDir, "test.db"),
		MCPManager:  nil,
		LLMProvider: &mockLLMForMultiAgent{},
		Logger:      logger,
	})
	require.NoError(t, err)
	defer registry.Close()

	// Create agent config
	config := &loomv1.AgentConfig{
		Name:        "error-test",
		Description: "Initial",
		Llm: &loomv1.LLMConfig{
			Provider:    "anthropic",
			Model:       "claude-3",
			Temperature: 0.7,
			MaxTokens:   4096,
		},
		SystemPrompt: "You are a test",
		Memory: &loomv1.MemoryConfig{
			Type:       "memory",
			MaxHistory: 50,
		},
		Behavior: &loomv1.BehaviorConfig{
			MaxIterations:  10,
			TimeoutSeconds: 300,
		},
	}

	configPath := filepath.Join(agentsDir, "error-test.yaml")
	err = agent.SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	ctx := context.Background()
	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	ag, err := registry.CreateAgent(ctx, "error-test")
	require.NoError(t, err)

	// Create multi-agent server
	agents := map[string]*agent.Agent{
		"error-test": ag,
	}
	loomService := NewMultiAgentServer(agents, nil)

	// Set callback that returns error
	callbackInvoked := false
	registry.SetReloadCallback(func(name string, agentConfig *loomv1.AgentConfig) error {
		callbackInvoked = true
		return assert.AnError // Return error
	})

	// Modify config and force reload (should fail)
	config.Description = "Updated"
	err = agent.SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	err = registry.ForceReload(ctx, "error-test")
	require.Error(t, err)
	assert.True(t, callbackInvoked)

	// Verify agent was NOT updated in server (callback failed)
	originalAgent, _, err := loomService.getAgent("error-test")
	require.NoError(t, err)
	assert.Equal(t, "Initial", originalAgent.GetDescription(), "Agent should not be updated when callback fails")
}

// TestWatchConfigs_ReloadTriggerFile tests that WatchConfigs detects .reload trigger file
// Note: This test uses manual callback invocation since fsnotify on macOS is unreliable
func TestWatchConfigs_ReloadTriggerFile(t *testing.T) {
	// Skip if ANTHROPIC_API_KEY not set (integration test requires real LLM provider)
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set - skipping integration test")
	}

	tmpDir := t.TempDir()
	logger := zap.NewNop()

	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	// Create multiple agent configs
	agentNames := []string{"trigger1", "trigger2", "trigger3"}
	for _, name := range agentNames {
		config := &loomv1.AgentConfig{
			Name:        name,
			Description: "Initial " + name,
			Llm: &loomv1.LLMConfig{
				Provider:    "anthropic",
				Model:       "claude-3",
				Temperature: 0.7,
				MaxTokens:   4096,
			},
			SystemPrompt: "You are " + name,
			Memory: &loomv1.MemoryConfig{
				Type:       "memory",
				MaxHistory: 50,
			},
			Behavior: &loomv1.BehaviorConfig{
				MaxIterations:  10,
				TimeoutSeconds: 300,
			},
		}

		configPath := filepath.Join(agentsDir, name+".yaml")
		err = agent.SaveAgentConfig(config, configPath)
		require.NoError(t, err)
	}

	// Create registry
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   tmpDir,
		DBPath:      filepath.Join(tmpDir, "test.db"),
		MCPManager:  nil,
		LLMProvider: &mockLLMForMultiAgent{},
		Logger:      logger,
	})
	require.NoError(t, err)
	defer registry.Close()

	// Load agents
	ctx := context.Background()
	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	// Create agents
	agents := make(map[string]*agent.Agent)
	for _, name := range agentNames {
		ag, err := registry.CreateAgent(ctx, name)
		require.NoError(t, err)
		agents[name] = ag
	}

	// Create multi-agent server
	loomService := NewMultiAgentServer(agents, nil)

	// Track callback invocations
	reloadedAgents := make(map[string]bool)
	var mu sync.Mutex

	registry.SetReloadCallback(func(name string, agentConfig *loomv1.AgentConfig) error {
		mu.Lock()
		reloadedAgents[name] = true
		mu.Unlock()

		backend := &mockBackend{}
		llm := &mockLLMForMultiAgent{}

		cfg := &agent.Config{
			Name:         agentConfig.Name,
			Description:  agentConfig.Description,
			SystemPrompt: agentConfig.SystemPrompt,
		}

		newAgent := agent.NewAgent(backend, llm, agent.WithConfig(cfg))
		return loomService.UpdateAgent(name, newAgent)
	})

	// Simulate .reload trigger by:
	// 1. Writing .reload file
	// 2. Force reload each agent (simulating what WatchConfigs does)

	reloadPath := filepath.Join(agentsDir, ".reload")
	err = os.WriteFile(reloadPath, []byte(fmt.Sprintf("%d\n", time.Now().Unix())), 0644)
	require.NoError(t, err)

	// Force reload each agent (simulating WatchConfigs behavior)
	for _, name := range agentNames {
		err := registry.ForceReload(ctx, name)
		// Log error but don't fail (same as WatchConfigs)
		if err != nil {
			t.Logf("ForceReload error for %s: %v", name, err)
		}
	}

	// Verify all agents were processed
	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, reloadedAgents, 3, "All agents should be reloaded")
	for _, name := range agentNames {
		assert.True(t, reloadedAgents[name], "Agent %s should be reloaded", name)
	}
}

// TestWatchConfigs_ReloadTrigger_NewAgent tests that new agents created by metaagent are detected
func TestWatchConfigs_ReloadTrigger_NewAgent(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	// Create registry with no initial agents
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   tmpDir,
		DBPath:      filepath.Join(tmpDir, "test.db"),
		MCPManager:  nil,
		LLMProvider: &mockLLMForMultiAgent{},
		Logger:      logger,
	})
	require.NoError(t, err)
	defer registry.Close()

	// Load agents (none yet)
	ctx := context.Background()
	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	// Create multi-agent server with empty agents map
	agents := make(map[string]*agent.Agent)
	loomService := NewMultiAgentServer(agents, nil)

	// Track new agents added
	newAgents := make(map[string]bool)
	var mu sync.Mutex

	registry.SetReloadCallback(func(name string, agentConfig *loomv1.AgentConfig) error {
		mu.Lock()
		defer mu.Unlock()

		// Check if agent already exists
		existingAgents := loomService.GetAgentIDs()
		agentExists := false
		for _, id := range existingAgents {
			if id == name {
				agentExists = true
				break
			}
		}

		backend := &mockBackend{}
		llm := &mockLLMForMultiAgent{}

		cfg := &agent.Config{
			Name:         agentConfig.Name,
			Description:  agentConfig.Description,
			SystemPrompt: agentConfig.SystemPrompt,
		}

		newAgent := agent.NewAgent(backend, llm, agent.WithConfig(cfg))

		if agentExists {
			// Hot-reload existing agent
			return loomService.UpdateAgent(name, newAgent)
		} else {
			// New agent from metaagent
			loomService.AddAgent(name, newAgent)
			newAgents[name] = true
			return nil
		}
	})

	// Simulate metaagent creating new agent:
	// 1. Write new agent config
	// 2. Write .reload trigger file
	// 3. Simulate WatchConfigs detection

	newAgentConfig := &loomv1.AgentConfig{
		Name:        "metaagent-spawned",
		Description: "Agent created by metaagent",
		Llm: &loomv1.LLMConfig{
			Provider:    "anthropic",
			Model:       "claude-3",
			Temperature: 0.7,
			MaxTokens:   4096,
		},
		SystemPrompt: "You are a metaagent-spawned assistant",
		Memory: &loomv1.MemoryConfig{
			Type:       "memory",
			MaxHistory: 50,
		},
		Behavior: &loomv1.BehaviorConfig{
			MaxIterations:  10,
			TimeoutSeconds: 300,
		},
	}

	configPath := filepath.Join(agentsDir, "metaagent-spawned.yaml")
	err = agent.SaveAgentConfig(newAgentConfig, configPath)
	require.NoError(t, err)

	// Write .reload trigger
	reloadPath := filepath.Join(agentsDir, ".reload")
	err = os.WriteFile(reloadPath, []byte(fmt.Sprintf("%d\n", time.Now().Unix())), 0644)
	require.NoError(t, err)

	// Simulate WatchConfigs behavior: rescan directory and force reload
	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	// Force reload the new agent (simulating WatchConfigs callback invocation)
	err = registry.ForceReload(ctx, "metaagent-spawned")
	if err != nil {
		t.Logf("ForceReload error: %v", err)
	}

	// Verify new agent was added
	mu.Lock()
	defer mu.Unlock()
	assert.True(t, newAgents["metaagent-spawned"], "New agent should be added")

	// Verify agent is available in server
	agentIDs := loomService.GetAgentIDs()
	assert.Contains(t, agentIDs, "metaagent-spawned")

	// Verify agent can be retrieved
	spawnedAgent, _, err := loomService.getAgent("metaagent-spawned")
	require.NoError(t, err)
	assert.Equal(t, "Agent created by metaagent", spawnedAgent.GetDescription())
}

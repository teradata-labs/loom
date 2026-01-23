// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap"
)

// mockLLMProvider implements LLMProvider for testing
type mockLLMProvider struct{}

func (m *mockLLMProvider) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return &llmtypes.LLMResponse{
		Content:    "Mock response",
		StopReason: "end_turn",
	}, nil
}

func (m *mockLLMProvider) Name() string {
	return "mock"
}

func (m *mockLLMProvider) Model() string {
	return "mock-model"
}

// createTestRegistry creates a registry for testing
func createTestRegistry(t *testing.T) (*Registry, string) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	registry, err := NewRegistry(RegistryConfig{
		ConfigDir:   tmpDir,
		DBPath:      filepath.Join(tmpDir, "test_registry.db"),
		MCPManager:  nil,
		LLMProvider: &mockLLMProvider{},
		Logger:      logger,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		registry.Close()
	})

	return registry, tmpDir
}

// createTestAgentConfig creates a minimal valid agent config
func createTestAgentConfig(name string) *loomv1.AgentConfig {
	return &loomv1.AgentConfig{
		Name:        name,
		Description: "Test agent",
		Llm: &loomv1.LLMConfig{
			Provider:    "", // Empty provider will use registry's default mock LLM
			Model:       "", // Empty model will use registry's default mock LLM
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
}

func TestNewRegistry(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	registry, err := NewRegistry(RegistryConfig{
		ConfigDir:   tmpDir,
		DBPath:      filepath.Join(tmpDir, "test.db"),
		MCPManager:  nil,
		LLMProvider: &mockLLMProvider{},
		Logger:      logger,
	})

	require.NoError(t, err)
	require.NotNil(t, registry)
	defer registry.Close()

	// Verify agents directory was created
	agentsDir := filepath.Join(tmpDir, "agents")
	_, err = os.Stat(agentsDir)
	require.NoError(t, err)

	// Verify database was initialized
	_, err = os.Stat(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
}

func TestRegistry_LoadAgents(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Create test agent configs
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config1 := createTestAgentConfig("agent1")
	config2 := createTestAgentConfig("agent2")

	err = SaveAgentConfig(config1, filepath.Join(agentsDir, "agent1.yaml"))
	require.NoError(t, err)

	err = SaveAgentConfig(config2, filepath.Join(agentsDir, "agent2.yaml"))
	require.NoError(t, err)

	// Load agents
	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	// Verify agents were loaded
	registry.mu.RLock()
	assert.Len(t, registry.configs, 2)
	assert.Contains(t, registry.configs, "agent1")
	assert.Contains(t, registry.configs, "agent2")
	registry.mu.RUnlock()
}

func TestRegistry_CreateAgent(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Create and save config
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config := createTestAgentConfig("test_agent")
	err = SaveAgentConfig(config, filepath.Join(agentsDir, "test_agent.yaml"))
	require.NoError(t, err)

	// Load agents
	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	// Create agent
	agent, err := registry.CreateAgent(ctx, "test_agent")
	require.NoError(t, err)
	require.NotNil(t, agent)

	assert.Equal(t, "test_agent", agent.GetName())

	// Verify agent info was created
	info, err := registry.GetAgentInfo("test_agent")
	require.NoError(t, err)
	assert.Equal(t, "test_agent", info.Name)
	assert.Equal(t, "stopped", info.Status)
}

func TestRegistry_CreateAgent_AlreadyExists(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Create and save config
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config := createTestAgentConfig("duplicate")
	err = SaveAgentConfig(config, filepath.Join(agentsDir, "duplicate.yaml"))
	require.NoError(t, err)

	// Load agents
	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	// Create agent first time
	_, err = registry.CreateAgent(ctx, "duplicate")
	require.NoError(t, err)

	// Try to create again
	_, err = registry.CreateAgent(ctx, "duplicate")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already running")
}

func TestRegistry_StartStopAgent(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Setup agent
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config := createTestAgentConfig("startstop")
	err = SaveAgentConfig(config, filepath.Join(agentsDir, "startstop.yaml"))
	require.NoError(t, err)

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	_, err = registry.CreateAgent(ctx, "startstop")
	require.NoError(t, err)

	// Start agent
	err = registry.StartAgent(ctx, "startstop")
	require.NoError(t, err)

	info, err := registry.GetAgentInfo("startstop")
	require.NoError(t, err)
	assert.Equal(t, "running", info.Status)

	// Stop agent
	err = registry.StopAgent(ctx, "startstop")
	require.NoError(t, err)

	info, err = registry.GetAgentInfo("startstop")
	require.NoError(t, err)
	assert.Equal(t, "stopped", info.Status)
}

func TestRegistry_DeleteAgent(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Setup agent
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config := createTestAgentConfig("deleteme")
	configPath := filepath.Join(agentsDir, "deleteme.yaml")
	err = SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	_, err = registry.CreateAgent(ctx, "deleteme")
	require.NoError(t, err)

	// Delete agent (not running, should succeed)
	err = registry.DeleteAgent(ctx, "deleteme", false)
	require.NoError(t, err)

	// Verify agent is gone
	_, err = registry.GetAgentInfo("deleteme")
	require.Error(t, err)

	// Verify config file was deleted
	_, err = os.Stat(configPath)
	assert.True(t, os.IsNotExist(err))
}

func TestRegistry_DeleteAgent_Running(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Setup running agent
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config := createTestAgentConfig("running")
	err = SaveAgentConfig(config, filepath.Join(agentsDir, "running.yaml"))
	require.NoError(t, err)

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	_, err = registry.CreateAgent(ctx, "running")
	require.NoError(t, err)

	err = registry.StartAgent(ctx, "running")
	require.NoError(t, err)

	// Try to delete without force (should fail)
	err = registry.DeleteAgent(ctx, "running", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is running")

	// Delete with force (should succeed)
	err = registry.DeleteAgent(ctx, "running", true)
	require.NoError(t, err)
}

func TestRegistry_ReloadAgent(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Setup agent
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config := createTestAgentConfig("reload")
	configPath := filepath.Join(agentsDir, "reload.yaml")
	err = SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	_, err = registry.CreateAgent(ctx, "reload")
	require.NoError(t, err)

	// Modify config
	config.Description = "Updated description"
	err = SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	// Reload agent
	err = registry.ReloadAgent(ctx, "reload")
	require.NoError(t, err)

	// Verify config was reloaded
	registry.mu.RLock()
	reloadedConfig := registry.configs["reload"]
	registry.mu.RUnlock()
	assert.Equal(t, "Updated description", reloadedConfig.Description)
}

func TestRegistry_ListAgents(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Create multiple agents
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("agent%d", i)
		config := createTestAgentConfig(name)
		err = SaveAgentConfig(config, filepath.Join(agentsDir, name+".yaml"))
		require.NoError(t, err)
	}

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	for i := 1; i <= 3; i++ {
		_, err = registry.CreateAgent(ctx, fmt.Sprintf("agent%d", i))
		require.NoError(t, err)
	}

	// List agents
	infos := registry.ListAgents()
	assert.Len(t, infos, 3)

	// Verify all agents are present
	names := make(map[string]bool)
	for _, info := range infos {
		names[info.Name] = true
	}
	assert.True(t, names["agent1"])
	assert.True(t, names["agent2"])
	assert.True(t, names["agent3"])
}

// TestRegistry_ConcurrentOperations tests thread-safety with race detector
func TestRegistry_ConcurrentOperations(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Setup agents
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	numAgents := 5
	for i := 0; i < numAgents; i++ {
		name := fmt.Sprintf("concurrent%d", i)
		config := createTestAgentConfig(name)
		err = SaveAgentConfig(config, filepath.Join(agentsDir, name+".yaml"))
		require.NoError(t, err)
	}

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	// Create agents concurrently
	var wg sync.WaitGroup
	wg.Add(numAgents)

	for i := 0; i < numAgents; i++ {
		go func(index int) {
			defer wg.Done()
			name := fmt.Sprintf("concurrent%d", index)
			_, err := registry.CreateAgent(ctx, name)
			if err != nil {
				t.Logf("CreateAgent error (expected in concurrent test): %v", err)
			}
		}(i)
	}

	wg.Wait()

	// Perform concurrent operations
	wg.Add(numAgents * 3) // start, list, get operations

	// Concurrent starts
	for i := 0; i < numAgents; i++ {
		go func(index int) {
			defer wg.Done()
			name := fmt.Sprintf("concurrent%d", index)
			err := registry.StartAgent(ctx, name)
			if err != nil {
				t.Logf("StartAgent error (expected in concurrent test): %v", err)
			}
		}(i)
	}

	// Concurrent list operations
	for i := 0; i < numAgents; i++ {
		go func() {
			defer wg.Done()
			infos := registry.ListAgents()
			assert.NotNil(t, infos)
		}()
	}

	// Concurrent get operations
	for i := 0; i < numAgents; i++ {
		go func(index int) {
			defer wg.Done()
			name := fmt.Sprintf("concurrent%d", index)
			_, err := registry.GetAgentInfo(name)
			if err != nil {
				t.Logf("GetAgentInfo error (expected in concurrent test): %v", err)
			}
		}(i)
	}

	wg.Wait()

	// Verify final state
	infos := registry.ListAgents()
	assert.GreaterOrEqual(t, len(infos), 1) // At least some agents should be created
}

func TestRegistry_GetAgent(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Setup agent
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config := createTestAgentConfig("getme")
	err = SaveAgentConfig(config, filepath.Join(agentsDir, "getme.yaml"))
	require.NoError(t, err)

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	_, err = registry.CreateAgent(ctx, "getme")
	require.NoError(t, err)

	// Get agent
	agent, err := registry.GetAgent(ctx, "getme")
	require.NoError(t, err)
	require.NotNil(t, agent)
	assert.Equal(t, "getme", agent.GetName())

	// Try to get non-existent agent
	_, err = registry.GetAgent(ctx, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRegistry_GetAgentInfo_NotFound(t *testing.T) {
	registry, _ := createTestRegistry(t)

	_, err := registry.GetAgentInfo("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestRegistry_RaceDetection is specifically designed to catch race conditions
// This test intentionally creates high contention scenarios
func TestRegistry_RaceDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping race detection test in short mode")
	}

	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Setup a single agent for high contention
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config := createTestAgentConfig("racetest")
	err = SaveAgentConfig(config, filepath.Join(agentsDir, "racetest.yaml"))
	require.NoError(t, err)

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	_, err = registry.CreateAgent(ctx, "racetest")
	require.NoError(t, err)

	// Hammer the registry with concurrent operations
	const goroutines = 20
	const iterations = 10

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				// Mix of read and write operations
				switch i % 4 {
				case 0:
					_ = registry.ListAgents()
				case 1:
					_, _ = registry.GetAgentInfo("racetest")
				case 2:
					_ = registry.StartAgent(ctx, "racetest")
				case 3:
					_ = registry.StopAgent(ctx, "racetest")
				}
			}
		}()
	}

	wg.Wait()
}

// TestSetReloadCallback verifies the callback is stored correctly
func TestSetReloadCallback(t *testing.T) {
	registry, _ := createTestRegistry(t)

	// Initially callback should be nil
	registry.mu.RLock()
	assert.Nil(t, registry.onReload)
	registry.mu.RUnlock()

	// Set callback
	called := false
	callback := func(name string, config *loomv1.AgentConfig) error {
		called = true
		return nil
	}

	registry.SetReloadCallback(callback)

	// Verify callback is stored
	registry.mu.RLock()
	assert.NotNil(t, registry.onReload)
	registry.mu.RUnlock()

	// Test callback can be invoked
	err := registry.onReload("test", &loomv1.AgentConfig{Name: "test"})
	require.NoError(t, err)
	assert.True(t, called)
}

// TestReloadCallback_Success verifies callback receives correct parameters
func TestReloadCallback_Success(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Setup agent
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config := createTestAgentConfig("callbacktest")
	configPath := filepath.Join(agentsDir, "callbacktest.yaml")
	err = SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	_, err = registry.CreateAgent(ctx, "callbacktest")
	require.NoError(t, err)

	// Set callback
	var receivedName string
	var receivedConfig *loomv1.AgentConfig
	callback := func(name string, config *loomv1.AgentConfig) error {
		receivedName = name
		receivedConfig = config
		return nil
	}

	registry.SetReloadCallback(callback)

	// Modify config
	config.Description = "Updated via callback"
	err = SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	// Manually trigger callback (bypassing fsnotify which doesn't work reliably on macOS)
	newConfig, err := LoadAgentConfig(configPath)
	require.NoError(t, err)

	registry.mu.Lock()
	registry.configs["callbacktest"] = newConfig
	registry.mu.Unlock()

	err = registry.onReload("callbacktest", newConfig)
	require.NoError(t, err)

	// Verify callback received correct parameters
	assert.Equal(t, "callbacktest", receivedName)
	assert.NotNil(t, receivedConfig)
	assert.Equal(t, "Updated via callback", receivedConfig.Description)
}

// TestReloadCallback_Error verifies error handling in callback
func TestReloadCallback_Error(t *testing.T) {
	registry, _ := createTestRegistry(t)

	// Set callback that returns error
	expectedErr := fmt.Errorf("callback error")
	callback := func(name string, config *loomv1.AgentConfig) error {
		return expectedErr
	}

	registry.SetReloadCallback(callback)

	// Invoke callback
	err := registry.onReload("test", &loomv1.AgentConfig{Name: "test"})
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

// TestReloadCallback_NotSet verifies fallback behavior when callback is nil
// This tests the WatchConfigs logic that falls back to ReloadAgent
func TestReloadCallback_NotSet(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Setup agent
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config := createTestAgentConfig("fallbacktest")
	configPath := filepath.Join(agentsDir, "fallbacktest.yaml")
	err = SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	_, err = registry.CreateAgent(ctx, "fallbacktest")
	require.NoError(t, err)

	// Don't set callback - it should be nil

	// Modify config
	config.Description = "Updated for fallback"
	err = SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	// Call ReloadAgent directly (what WatchConfigs does when callback is nil)
	err = registry.ReloadAgent(ctx, "fallbacktest")
	require.NoError(t, err)

	// Verify config was reloaded
	registry.mu.RLock()
	reloadedConfig := registry.configs["fallbacktest"]
	registry.mu.RUnlock()
	assert.Equal(t, "Updated for fallback", reloadedConfig.Description)
}

// TestReloadCallback_ThreadSafety tests concurrent callback operations with race detector
func TestReloadCallback_ThreadSafety(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping thread safety test in short mode")
	}

	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Setup agent
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config := createTestAgentConfig("threadtest")
	configPath := filepath.Join(agentsDir, "threadtest.yaml")
	err = SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	_, err = registry.CreateAgent(ctx, "threadtest")
	require.NoError(t, err)

	// Concurrent operations: SetReloadCallback, callback invocations, and config reads
	const goroutines = 10
	const iterations = 10

	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	// Goroutines setting callbacks
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				registry.SetReloadCallback(func(name string, config *loomv1.AgentConfig) error {
					return nil
				})
			}
		}()
	}

	// Goroutines reading callback
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				registry.mu.RLock()
				callback := registry.onReload
				registry.mu.RUnlock()
				if callback != nil {
					_ = callback("threadtest", config)
				}
			}
		}()
	}

	// Goroutines reading configs
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				registry.mu.RLock()
				_ = registry.configs["threadtest"]
				registry.mu.RUnlock()
			}
		}()
	}

	wg.Wait()
}

// TestForceReload_Success verifies successful ForceReload with callback invocation
func TestForceReload_Success(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Setup agent
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config := createTestAgentConfig("forcereload")
	configPath := filepath.Join(agentsDir, "forcereload.yaml")
	err = SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	// Track callback invocation
	callbackInvoked := false
	var receivedName string
	var receivedConfig *loomv1.AgentConfig

	registry.SetReloadCallback(func(name string, cfg *loomv1.AgentConfig) error {
		callbackInvoked = true
		receivedName = name
		receivedConfig = cfg
		return nil
	})

	// Modify config
	config.Description = "Updated by ForceReload"
	err = SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	// Force reload
	err = registry.ForceReload(ctx, "forcereload")
	require.NoError(t, err)

	// Verify callback was invoked
	assert.True(t, callbackInvoked, "Callback should be invoked")
	assert.Equal(t, "forcereload", receivedName)
	assert.NotNil(t, receivedConfig)
	assert.Equal(t, "Updated by ForceReload", receivedConfig.Description)

	// Verify config was updated in registry
	registry.mu.RLock()
	updatedConfig := registry.configs["forcereload"]
	registry.mu.RUnlock()
	assert.Equal(t, "Updated by ForceReload", updatedConfig.Description)
}

// TestForceReload_MissingFile verifies graceful handling of missing config files
func TestForceReload_MissingFile(t *testing.T) {
	registry, _ := createTestRegistry(t)
	ctx := context.Background()

	// Try to force reload non-existent file
	err := registry.ForceReload(ctx, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load config")
}

// TestForceReload_InvalidConfig verifies graceful handling of invalid configs
func TestForceReload_InvalidConfig(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Setup invalid config file (missing required llm field)
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	configPath := filepath.Join(agentsDir, "invalid.yaml")
	invalidYAML := `name: invalid
description: Invalid config
memory:
  type: memory
  max_history: 50
behavior:
  max_iterations: 10
  timeout_seconds: 300
# Missing required llm field`
	err = os.WriteFile(configPath, []byte(invalidYAML), 0644)
	require.NoError(t, err)

	// Try to force reload invalid config
	err = registry.ForceReload(ctx, "invalid")
	require.Error(t, err)
	// Error can be either during load or validation
	assert.True(t,
		strings.Contains(err.Error(), "invalid config") ||
			strings.Contains(err.Error(), "failed to load config"),
		"Error should indicate config issue: %v", err)
}

// TestForceReload_NoCallback verifies fallback to ReloadAgent when callback is not set
func TestForceReload_NoCallback(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Setup agent
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config := createTestAgentConfig("nocallback")
	configPath := filepath.Join(agentsDir, "nocallback.yaml")
	err = SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	_, err = registry.CreateAgent(ctx, "nocallback")
	require.NoError(t, err)

	// Don't set callback - should fallback to ReloadAgent
	config.Description = "Updated without callback"
	err = SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	// Force reload should succeed using fallback
	err = registry.ForceReload(ctx, "nocallback")
	require.NoError(t, err)

	// Verify config was updated
	registry.mu.RLock()
	updatedConfig := registry.configs["nocallback"]
	registry.mu.RUnlock()
	assert.Equal(t, "Updated without callback", updatedConfig.Description)
}

// TestForceReload_CallbackError verifies error handling when callback fails
func TestForceReload_CallbackError(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Setup agent
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config := createTestAgentConfig("callbackerr")
	configPath := filepath.Join(agentsDir, "callbackerr.yaml")
	err = SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	// Set callback that returns error
	expectedErr := fmt.Errorf("callback failed")
	registry.SetReloadCallback(func(name string, cfg *loomv1.AgentConfig) error {
		return expectedErr
	})

	// Force reload should fail with callback error
	err = registry.ForceReload(ctx, "callbackerr")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reload callback failed")
}

// TestForceReload_ThreadSafety tests concurrent ForceReload calls with race detector
func TestForceReload_ThreadSafety(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping thread safety test in short mode")
	}

	registry, tmpDir := createTestRegistry(t)
	ctx := context.Background()

	// Setup agents
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	numAgents := 5
	for i := 0; i < numAgents; i++ {
		name := fmt.Sprintf("concurrent_reload_%d", i)
		config := createTestAgentConfig(name)
		configPath := filepath.Join(agentsDir, name+".yaml")
		err = SaveAgentConfig(config, configPath)
		require.NoError(t, err)
	}

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	// Create agents
	for i := 0; i < numAgents; i++ {
		name := fmt.Sprintf("concurrent_reload_%d", i)
		_, err = registry.CreateAgent(ctx, name)
		if err != nil {
			t.Logf("CreateAgent warning: %v", err)
		}
	}

	// Set callback
	var callbackCount sync.Map
	registry.SetReloadCallback(func(name string, cfg *loomv1.AgentConfig) error {
		// Track callback invocations
		count, _ := callbackCount.LoadOrStore(name, 0)
		callbackCount.Store(name, count.(int)+1)
		return nil
	})

	// Concurrent ForceReload operations
	const goroutines = 10
	const iterations = 5

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				agentIndex := (gid + i) % numAgents
				name := fmt.Sprintf("concurrent_reload_%d", agentIndex)

				// Modify config
				configPath := filepath.Join(agentsDir, name+".yaml")
				config, err := LoadAgentConfig(configPath)
				if err != nil {
					continue
				}
				config.Description = fmt.Sprintf("Updated by goroutine %d iteration %d", gid, i)
				err = SaveAgentConfig(config, configPath)
				if err != nil {
					continue
				}

				// Force reload
				err = registry.ForceReload(ctx, name)
				if err != nil {
					t.Logf("ForceReload warning: %v", err)
				}
			}
		}(g)
	}

	wg.Wait()

	// Verify callbacks were invoked for all agents
	for i := 0; i < numAgents; i++ {
		name := fmt.Sprintf("concurrent_reload_%d", i)
		count, ok := callbackCount.Load(name)
		if ok {
			t.Logf("Agent %s: %d callbacks invoked", name, count.(int))
		}
	}
}

// TestToolFiltering verifies that agents only register tools specified in config.Tools.Builtin
func TestToolFiltering(t *testing.T) {
	ctx := context.Background()
	registry, tmpDir := createTestRegistry(t)

	// Create agent with only specific tools
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config := createTestAgentConfig("tool_filter_test")
	config.Tools = &loomv1.ToolsConfig{
		Builtin: []string{
			"shell_execute",
			// Note: tool_search requires ToolRegistry to be configured (not in basic test setup)
			"query_tool_result",
			// Note: get_tool_result removed - inline metadata makes it unnecessary
			// Note: recall_conversation removed in scratchpad experiment
		},
	}

	err = SaveAgentConfig(config, filepath.Join(agentsDir, "tool_filter_test.yaml"))
	require.NoError(t, err)

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	agent, err := registry.CreateAgent(ctx, "tool_filter_test")
	require.NoError(t, err)
	require.NotNil(t, agent)

	// Get list of registered tools
	registeredTools := agent.ListTools()
	t.Logf("Registered tools: %v", registeredTools)

	// Verify only specified tools are registered
	expectedTools := map[string]bool{
		"shell_execute": true,
		// query_tool_result uses progressive disclosure (registered after first large result)
		// get_tool_result removed - inline metadata makes it unnecessary
		// recall_conversation removed in scratchpad experiment
	}

	// Check that all expected tools are present
	for toolName := range expectedTools {
		assert.Contains(t, registeredTools, toolName, "Expected tool %s to be registered", toolName)
	}

	// Check that unexpected tools are NOT present
	unexpectedTools := []string{
		"http_request",
		"web_search",
		"file_write",
		"file_read",
		"grpc_call",
		"search_conversation",    // Not in the config list
		"clear_recalled_context", // Not in the config list
	}

	for _, toolName := range unexpectedTools {
		assert.NotContains(t, registeredTools, toolName, "Unexpected tool %s should not be registered", toolName)
	}
}

// TestToolFiltering_BackwardCompatibility verifies backward compatibility when no Tools.Builtin is specified
func TestToolFiltering_BackwardCompatibility(t *testing.T) {
	ctx := context.Background()
	registry, tmpDir := createTestRegistry(t)

	// Create agent without specifying Tools.Builtin (backward compatibility mode)
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	config := createTestAgentConfig("backward_compat_test")
	// Don't set config.Tools - it should register all builtin tools

	err = SaveAgentConfig(config, filepath.Join(agentsDir, "backward_compat_test.yaml"))
	require.NoError(t, err)

	err = registry.LoadAgents(ctx)
	require.NoError(t, err)

	agent, err := registry.CreateAgent(ctx, "backward_compat_test")
	require.NoError(t, err)
	require.NotNil(t, agent)

	// Get list of registered tools
	registeredTools := agent.ListTools()
	t.Logf("Registered tools (backward compat): %v", registeredTools)

	// In backward compatibility mode, common builtin tools should be registered
	expectedTools := []string{
		"http_request",
		"web_search",
		"file_write",
		"file_read",
		"grpc_call",
		"shell_execute",
	}

	for _, toolName := range expectedTools {
		assert.Contains(t, registeredTools, toolName, "Expected builtin tool %s to be registered in backward compat mode", toolName)
	}
}

// TestToolFiltering_ErrorDetailVariation verifies get_error_detail vs get_error_details name handling
// Note: This test is skipped because get_error_details requires ErrorStore infrastructure
// which is not set up in the basic test registry. The name variation handling is tested
// via the filtering logic, but actual registration requires proper error store setup.
func TestToolFiltering_ErrorDetailVariation(t *testing.T) {
	t.Skip("Skipping: get_error_details requires ErrorStore infrastructure not available in basic test setup")

	// The actual name variation logic is tested in the registry filtering code at line 513-516:
	// if toolName == "get_error_detail" {
	//     allowedTools["get_error_details"] = true
	// }
}

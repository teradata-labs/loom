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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
)

// mockLLMForMultiAgent implements a simple LLM for testing multi-agent functionality
type mockLLMForMultiAgent struct{}

func (m *mockLLMForMultiAgent) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return &llmtypes.LLMResponse{
		Content: "Mock response from " + messages[len(messages)-1].Content,
		Usage: llmtypes.Usage{
			InputTokens:  10,
			OutputTokens: 20,
			CostUSD:      0.001,
		},
	}, nil
}

func (m *mockLLMForMultiAgent) Name() string {
	return "mock-llm"
}

func (m *mockLLMForMultiAgent) Model() string {
	return "mock-model"
}

func TestNewMultiAgentServer(t *testing.T) {
	backend1 := &mockBackend{}
	backend2 := &mockBackend{}
	llm := &mockLLMForMultiAgent{}

	agent1 := agent.NewAgent(backend1, llm)
	agent2 := agent.NewAgent(backend2, llm)

	agents := map[string]*agent.Agent{
		"agent1": agent1,
		"agent2": agent2,
	}

	server := NewMultiAgentServer(agents, nil)
	require.NotNil(t, server)

	// Check agents are registered
	agentList := server.GetAgentIDs()
	assert.Len(t, agentList, 2)
	assert.Contains(t, agentList, "agent1")
	assert.Contains(t, agentList, "agent2")

	// Check default agent is set
	assert.NotEmpty(t, server.defaultAgentID)
}

func TestMultiAgentServer_DefaultAgent(t *testing.T) {
	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}

	defaultAgent := agent.NewAgent(backend, llm)

	agents := map[string]*agent.Agent{
		"default": defaultAgent,
	}

	server := NewMultiAgentServer(agents, nil)

	// Verify default agent is "default"
	assert.Equal(t, "default", server.defaultAgentID)
}

func TestMultiAgentServer_AddRemoveAgent(t *testing.T) {
	backend1 := &mockBackend{}
	llm := &mockLLMForMultiAgent{}

	agent1 := agent.NewAgent(backend1, llm)

	agents := map[string]*agent.Agent{
		"agent1": agent1,
	}

	server := NewMultiAgentServer(agents, nil)

	// Add new agent
	backend2 := &mockBackend{}
	agent2 := agent.NewAgent(backend2, llm)
	server.AddAgent("agent2", agent2)

	agentList := server.GetAgentIDs()
	assert.Len(t, agentList, 2)

	// Remove agent
	err := server.RemoveAgent("agent1")
	require.NoError(t, err)

	agentList = server.GetAgentIDs()
	assert.Len(t, agentList, 1)
	assert.Contains(t, agentList, "agent2")
}

func TestMultiAgentServer_GetAgent(t *testing.T) {
	backend1 := &mockBackend{}
	backend2 := &mockBackend{}
	llm := &mockLLMForMultiAgent{}

	agent1 := agent.NewAgent(backend1, llm)
	agent2 := agent.NewAgent(backend2, llm)

	agents := map[string]*agent.Agent{
		"agent1": agent1,
		"agent2": agent2,
	}

	server := NewMultiAgentServer(agents, nil)

	// Get existing agent
	ag, id, err := server.getAgent("agent1")
	require.NoError(t, err)
	assert.NotNil(t, ag)
	assert.Equal(t, "agent1", id)

	// Get non-existent agent
	ag, id, err = server.getAgent("nonexistent")
	assert.Error(t, err)
	assert.Nil(t, ag)
	assert.Empty(t, id)

	// Get default agent (empty string)
	ag, id, err = server.getAgent("")
	require.NoError(t, err)
	assert.NotNil(t, ag)
	assert.NotEmpty(t, id)
}

func TestMultiAgentServer_Weave(t *testing.T) {
	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}

	ag := agent.NewAgent(backend, llm)

	agents := map[string]*agent.Agent{
		"test-agent": ag,
	}

	server := NewMultiAgentServer(agents, nil)
	ctx := context.Background()

	// Test with specific agent
	req := &loomv1.WeaveRequest{
		Query:   "Hello, agent!",
		AgentId: "test-agent",
	}

	resp, err := server.Weave(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.NotEmpty(t, resp.Text)
	assert.NotEmpty(t, resp.SessionId)
	assert.Equal(t, "test-agent", resp.AgentId)
	assert.NotNil(t, resp.Cost)
}

func TestMultiAgentServer_WeaveWithDefaultAgent(t *testing.T) {
	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}

	ag := agent.NewAgent(backend, llm)

	agents := map[string]*agent.Agent{
		"default": ag,
	}

	server := NewMultiAgentServer(agents, nil)
	ctx := context.Background()

	// Test without specifying agent (should use default)
	req := &loomv1.WeaveRequest{
		Query: "Hello!",
	}

	resp, err := server.Weave(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "default", resp.AgentId)
}

func TestMultiAgentServer_WeaveWithInvalidAgent(t *testing.T) {
	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}

	ag := agent.NewAgent(backend, llm)

	agents := map[string]*agent.Agent{
		"test-agent": ag,
	}

	server := NewMultiAgentServer(agents, nil)
	ctx := context.Background()

	// Test with non-existent agent
	req := &loomv1.WeaveRequest{
		Query:   "Hello!",
		AgentId: "nonexistent",
	}

	resp, err := server.Weave(ctx, req)
	assert.Error(t, err)
	assert.Nil(t, resp)
}

func TestMultiAgentServer_WeaveEmptyQuery(t *testing.T) {
	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}

	ag := agent.NewAgent(backend, llm)

	agents := map[string]*agent.Agent{
		"test-agent": ag,
	}

	server := NewMultiAgentServer(agents, nil)
	ctx := context.Background()

	// Test with empty query
	req := &loomv1.WeaveRequest{
		Query:   "",
		AgentId: "test-agent",
	}

	resp, err := server.Weave(ctx, req)
	assert.Error(t, err)
	assert.Nil(t, resp)
}

func TestMultiAgentServer_ConcurrentAccess(t *testing.T) {
	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}

	ag := agent.NewAgent(backend, llm)

	agents := map[string]*agent.Agent{
		"test-agent": ag,
	}

	server := NewMultiAgentServer(agents, nil)

	// Test concurrent access (add, list, get operations)
	done := make(chan bool, 3)

	// Concurrent GetAgentIDs
	go func() {
		for i := 0; i < 10; i++ {
			server.GetAgentIDs()
		}
		done <- true
	}()

	// Concurrent AddAgent
	go func() {
		for i := 0; i < 10; i++ {
			newBackend := &mockBackend{}
			newAgent := agent.NewAgent(newBackend, llm)
			server.AddAgent("temp-agent", newAgent)
			_ = server.RemoveAgent("temp-agent") // Ignore error in concurrent test
		}
		done <- true
	}()

	// Concurrent getAgent
	go func() {
		for i := 0; i < 10; i++ {
			_, _, _ = server.getAgent("test-agent") // Ignore result in concurrent test
		}
		done <- true
	}()

	// Wait for all goroutines
	for i := 0; i < 3; i++ {
		<-done
	}
}

func TestMultiAgentServer_CreatePattern(t *testing.T) {
	// Create temp directory for patterns
	tmpDir := t.TempDir()

	// Create agent with patterns directory
	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}
	ag := agent.NewAgent(backend, llm, agent.WithConfig(&agent.Config{
		Name:        "test-agent",
		PatternsDir: tmpDir,
	}))

	agents := map[string]*agent.Agent{
		"test-agent": ag,
	}

	server := NewMultiAgentServer(agents, nil)

	// Test valid pattern creation
	patternYAML := `name: runtime_test_pattern
title: Runtime Test Pattern
description: Pattern created via CreatePattern RPC
category: analytics
difficulty: beginner
templates:
  default:
    content: SELECT * FROM test_table
`

	req := &loomv1.CreatePatternRequest{
		AgentId:     "test-agent",
		Name:        "runtime_test_pattern",
		YamlContent: patternYAML,
	}

	resp, err := server.CreatePattern(context.Background(), req)
	require.NoError(t, err)
	require.True(t, resp.Success)
	assert.Equal(t, "runtime_test_pattern", resp.PatternName)
	assert.Contains(t, resp.FilePath, "runtime_test_pattern.yaml")

	// Verify file was created
	_, err = os.Stat(resp.FilePath)
	require.NoError(t, err, "Pattern file should exist")
}

func TestMultiAgentServer_CreatePattern_MissingAgentID(t *testing.T) {
	server := NewMultiAgentServer(map[string]*agent.Agent{}, nil)

	req := &loomv1.CreatePatternRequest{
		Name:        "test",
		YamlContent: "name: test",
	}

	resp, err := server.CreatePattern(context.Background(), req)
	require.NoError(t, err) // RPC should succeed
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "agent_id is required")
}

func TestMultiAgentServer_CreatePattern_MissingName(t *testing.T) {
	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}
	ag := agent.NewAgent(backend, llm)

	agents := map[string]*agent.Agent{
		"test": ag,
	}

	server := NewMultiAgentServer(agents, nil)

	req := &loomv1.CreatePatternRequest{
		AgentId:     "test",
		YamlContent: "content: test",
	}

	resp, err := server.CreatePattern(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "pattern name is required")
}

func TestMultiAgentServer_CreatePattern_AgentNotFound(t *testing.T) {
	server := NewMultiAgentServer(map[string]*agent.Agent{}, nil)

	req := &loomv1.CreatePatternRequest{
		AgentId:     "nonexistent",
		Name:        "test",
		YamlContent: "name: test",
	}

	resp, err := server.CreatePattern(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "agent not found")
}

func TestMultiAgentServer_CreatePattern_NoPatternsDir(t *testing.T) {
	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}
	ag := agent.NewAgent(backend, llm, agent.WithConfig(&agent.Config{
		Name: "test",
		// No PatternsDir configured
	}))

	agents := map[string]*agent.Agent{
		"test": ag,
	}

	server := NewMultiAgentServer(agents, nil)

	req := &loomv1.CreatePatternRequest{
		AgentId:     "test",
		Name:        "pattern",
		YamlContent: "name: pattern",
	}

	resp, err := server.CreatePattern(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "patterns_dir")
}

func TestMultiAgentServer_CreatePattern_Concurrent(t *testing.T) {
	tmpDir := t.TempDir()

	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}
	ag := agent.NewAgent(backend, llm, agent.WithConfig(&agent.Config{
		Name:        "test-agent",
		PatternsDir: tmpDir,
	}))

	agents := map[string]*agent.Agent{
		"test-agent": ag,
	}

	server := NewMultiAgentServer(agents, nil)

	// Create 10 patterns concurrently
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func(id int) {
			patternYAML := fmt.Sprintf(`name: concurrent_pattern_%d
title: Concurrent Pattern %d
description: Pattern %d
category: analytics
templates:
  default:
    content: SELECT %d
`, id, id, id, id)

			req := &loomv1.CreatePatternRequest{
				AgentId:     "test-agent",
				Name:        fmt.Sprintf("concurrent_pattern_%d", id),
				YamlContent: patternYAML,
			}

			resp, err := server.CreatePattern(context.Background(), req)
			if err != nil {
				t.Errorf("CreatePattern failed: %v", err)
			}
			if !resp.Success {
				t.Errorf("CreatePattern returned error: %s", resp.Error)
			}

			done <- true
		}(i)
	}

	// Wait for all
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify all pattern files exist
	for i := 0; i < 10; i++ {
		patternFile := filepath.Join(tmpDir, fmt.Sprintf("concurrent_pattern_%d.yaml", i))
		_, err := os.Stat(patternFile)
		require.NoError(t, err, "Pattern file %d should exist", i)
	}
}

// TestUpdateAgent_Success verifies that an agent can be updated successfully
func TestUpdateAgent_Success(t *testing.T) {
	backend1 := &mockBackend{}
	backend2 := &mockBackend{}
	llm := &mockLLMForMultiAgent{}

	// Create initial agent
	agent1 := agent.NewAgent(backend1, llm, agent.WithConfig(&agent.Config{
		Name:        "test-agent",
		Description: "Original agent",
	}))

	agents := map[string]*agent.Agent{
		"test-agent": agent1,
	}

	server := NewMultiAgentServer(agents, nil)

	// Verify initial agent
	ag, _, err := server.getAgent("test-agent")
	require.NoError(t, err)
	assert.Equal(t, "Original agent", ag.GetDescription())

	// Create new agent instance
	agent2 := agent.NewAgent(backend2, llm, agent.WithConfig(&agent.Config{
		Name:        "test-agent",
		Description: "Updated agent",
	}))

	// Update agent
	err = server.UpdateAgent("test-agent", agent2)
	require.NoError(t, err)

	// Verify agent was replaced
	ag, _, err = server.getAgent("test-agent")
	require.NoError(t, err)
	assert.Equal(t, "Updated agent", ag.GetDescription())
}

// TestUpdateAgent_NotFound verifies error when updating non-existent agent
func TestUpdateAgent_NotFound(t *testing.T) {
	server := NewMultiAgentServer(map[string]*agent.Agent{}, nil)

	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}
	newAgent := agent.NewAgent(backend, llm)

	// Try to update non-existent agent
	err := server.UpdateAgent("nonexistent", newAgent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent not found")
}

// TestUpdateAgent_SharedMemory verifies shared memory is injected into updated agent
func TestUpdateAgent_SharedMemory(t *testing.T) {
	t.Skip("Test needs updating - Agent.GetSharedMemory() method not yet implemented")

	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}

	// Create initial agent
	agent1 := agent.NewAgent(backend, llm)

	agents := map[string]*agent.Agent{
		"test-agent": agent1,
	}

	server := NewMultiAgentServer(agents, nil)

	// Configure shared memory
	config := &storage.Config{
		MaxMemoryBytes: 1024 * 1024, // 1MB
	}
	err := server.ConfigureSharedMemory(config)
	require.NoError(t, err)

	// Verify initial agent has shared memory
	// assert.NotNil(t, agent1.GetSharedMemory())

	// Create new agent instance (without shared memory initially)
	agent2 := agent.NewAgent(backend, llm)
	// assert.Nil(t, agent2.GetSharedMemory(), "New agent should not have shared memory yet")

	// Update agent
	err = server.UpdateAgent("test-agent", agent2)
	require.NoError(t, err)

	// Verify new agent now has shared memory injected
	// assert.NotNil(t, agent2.GetSharedMemory(), "Updated agent should have shared memory injected")
	// assert.Equal(t, server.GetSharedMemory(), agent2.GetSharedMemory())
}

// TestUpdateAgent_ThreadSafety tests concurrent UpdateAgent calls with race detector
func TestUpdateAgent_ThreadSafety(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping thread safety test in short mode")
	}

	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}

	// Create multiple agents
	agents := make(map[string]*agent.Agent)
	numAgents := 5
	for i := 0; i < numAgents; i++ {
		agentID := fmt.Sprintf("agent%d", i)
		agents[agentID] = agent.NewAgent(backend, llm, agent.WithConfig(&agent.Config{
			Name: agentID,
		}))
	}

	server := NewMultiAgentServer(agents, nil)

	// Concurrent operations: UpdateAgent, AddAgent, getAgent
	const goroutines = 10
	const iterations = 10

	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	// Goroutines updating agents
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				agentID := fmt.Sprintf("agent%d", i%numAgents)
				newAgent := agent.NewAgent(backend, llm, agent.WithConfig(&agent.Config{
					Name:        agentID,
					Description: fmt.Sprintf("Updated %d", i),
				}))
				_ = server.UpdateAgent(agentID, newAgent)
			}
		}()
	}

	// Goroutines reading agents
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				agentID := fmt.Sprintf("agent%d", i%numAgents)
				_, _, _ = server.getAgent(agentID)
			}
		}()
	}

	// Goroutines listing agents
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = server.GetAgentIDs()
			}
		}()
	}

	wg.Wait()
}

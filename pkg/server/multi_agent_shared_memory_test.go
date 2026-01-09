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
package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
)

// Helper function to create test agents (reuses mockBackend and mockLLMProvider from integration_test.go)
func createTestAgentForSharedMemory() *agent.Agent {
	mockLLM := &mockLLMProvider{
		responses: []string{"Test response"},
	}
	mockBackend := &mockBackend{}
	return agent.NewAgent(mockBackend, mockLLM)
}

func TestMultiAgentServer_ConfigureSharedMemory(t *testing.T) {
	// Create test agents
	agent1 := createTestAgentForSharedMemory()
	agent2 := createTestAgentForSharedMemory()
	agent3 := createTestAgentForSharedMemory()

	agents := map[string]*agent.Agent{
		"agent1": agent1,
		"agent2": agent2,
		"agent3": agent3,
	}

	store, err := agent.NewSessionStore(":memory:", observability.NewNoOpTracer())
	require.NoError(t, err)
	server := NewMultiAgentServer(agents, store)

	// Verify shared memory is not configured initially
	assert.Nil(t, server.SharedMemoryStore())

	// Configure shared memory
	config := &storage.Config{
		MaxMemoryBytes:       1024 * 1024, // 1MB
		CompressionThreshold: 512 * 1024,  // 512KB
		TTLSeconds:           3600,
	}

	err = server.ConfigureSharedMemory(config)
	require.NoError(t, err)

	// Verify shared memory is now configured
	sharedMemory := server.SharedMemoryStore()
	assert.NotNil(t, sharedMemory)

	// Verify shared memory works by storing and retrieving data
	testData := []byte("test data for all agents")
	ref, err := sharedMemory.Store("test-shared", testData, "text/plain", nil)
	require.NoError(t, err)
	assert.NotNil(t, ref)

	// Verify data can be retrieved
	retrieved, err := sharedMemory.Get(ref)
	require.NoError(t, err)
	assert.Equal(t, testData, retrieved)

	// Verify all agents can trigger sessions which will have shared memory
	// (This will happen automatically when agents process queries)
}

func TestMultiAgentServer_AddAgent_WithSharedMemory(t *testing.T) {
	// Create initial server with one agent
	agent1 := createTestAgentForSharedMemory()
	agents := map[string]*agent.Agent{
		"agent1": agent1,
	}

	store, err := agent.NewSessionStore(":memory:", observability.NewNoOpTracer())
	require.NoError(t, err)
	server := NewMultiAgentServer(agents, store)

	// Configure shared memory
	config := &storage.Config{
		MaxMemoryBytes: 1024 * 1024,
	}
	err = server.ConfigureSharedMemory(config)
	require.NoError(t, err)

	sharedMemory := server.SharedMemoryStore()
	require.NotNil(t, sharedMemory)

	// Add a new agent at runtime
	agent2 := createTestAgentForSharedMemory()
	server.AddAgent("agent2", agent2)

	// Store data and verify it's accessible (agent2 should have access to shared memory)
	testData := []byte("test data for agent2")
	ref, err := sharedMemory.Store("test-agent2", testData, "text/plain", nil)
	require.NoError(t, err)

	retrieved, err := sharedMemory.Get(ref)
	require.NoError(t, err)
	assert.Equal(t, testData, retrieved)
}

func TestMultiAgentServer_SharedMemory_CrossAgentDataAccess(t *testing.T) {
	// Create multiple agents
	agent1 := createTestAgentForSharedMemory()
	agent2 := createTestAgentForSharedMemory()

	agents := map[string]*agent.Agent{
		"agent1": agent1,
		"agent2": agent2,
	}

	store, err := agent.NewSessionStore(":memory:", observability.NewNoOpTracer())
	require.NoError(t, err)
	server := NewMultiAgentServer(agents, store)

	// Configure shared memory
	config := &storage.Config{
		MaxMemoryBytes: 1024 * 1024,
	}
	err = server.ConfigureSharedMemory(config)
	require.NoError(t, err)

	sharedMemory := server.SharedMemoryStore()
	require.NotNil(t, sharedMemory)

	// Agent1 stores large data
	largeData := make([]byte, 200*1024) // 200KB
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	ref, err := sharedMemory.Store("cross-agent-data", largeData, "application/octet-stream", map[string]string{
		"source": "agent1",
		"type":   "large-result",
	})
	require.NoError(t, err)
	assert.NotNil(t, ref)

	// Agent2 retrieves the data
	retrieved, err := sharedMemory.Get(ref)
	require.NoError(t, err)
	assert.Equal(t, largeData, retrieved)

	// Verify metadata
	assert.Equal(t, "agent1", ref.Metadata["source"])
	assert.Equal(t, "large-result", ref.Metadata["type"])
}

func TestMultiAgentServer_SharedMemory_LargeToolResults(t *testing.T) {
	// Create agent with a tool that returns large results
	ag := createTestAgentForSharedMemory()

	// Register a tool that returns large data
	tool := &mockLargeTool{}
	ag.RegisterTool(tool)

	agents := map[string]*agent.Agent{
		"test-agent": ag,
	}

	store, err := agent.NewSessionStore(":memory:", observability.NewNoOpTracer())
	require.NoError(t, err)
	server := NewMultiAgentServer(agents, store)

	// Configure shared memory with 100KB threshold
	config := &storage.Config{
		MaxMemoryBytes: 10 * 1024 * 1024, // 10MB
	}
	err = server.ConfigureSharedMemory(config)
	require.NoError(t, err)

	sharedMemory := server.SharedMemoryStore()
	require.NotNil(t, sharedMemory)

	// The executor should have shared memory configured via Agent.SetSharedMemory
	// Create a large result directly to simulate tool output
	largeResult := make(map[string]interface{})
	largeResult["data"] = string(make([]byte, 150*1024)) // 150KB
	largeResult["rows"] = 1000

	resultJSON, err := json.Marshal(largeResult)
	require.NoError(t, err)
	assert.Greater(t, len(resultJSON), 100*1024) // Verify it's > 100KB

	// Store it in shared memory (simulating what the executor would do)
	ref, err := sharedMemory.Store("tool-result-1", resultJSON, "application/json", map[string]string{
		"tool": "mock-large-tool",
	})
	require.NoError(t, err)
	assert.NotNil(t, ref)

	// Verify it can be retrieved
	retrieved, err := sharedMemory.Get(ref)
	require.NoError(t, err)
	assert.Equal(t, resultJSON, retrieved)
}

func TestMultiAgentServer_SharedMemory_ConcurrentAccess(t *testing.T) {
	// Create multiple agents
	agents := make(map[string]*agent.Agent)
	for i := 0; i < 5; i++ {
		agentID := "agent" + string(rune('0'+i))
		agents[agentID] = createTestAgentForSharedMemory()
	}

	store, err := agent.NewSessionStore(":memory:", observability.NewNoOpTracer())
	require.NoError(t, err)
	server := NewMultiAgentServer(agents, store)

	// Configure shared memory
	config := &storage.Config{
		MaxMemoryBytes: 10 * 1024 * 1024,
	}
	err = server.ConfigureSharedMemory(config)
	require.NoError(t, err)

	sharedMemory := server.SharedMemoryStore()
	require.NotNil(t, sharedMemory)

	// Run concurrent operations from multiple agents
	done := make(chan bool, len(agents))

	for agentID := range agents {
		go func(id string) {
			// Each agent stores and retrieves data
			for i := 0; i < 10; i++ {
				data := []byte(id + "-data-" + string(rune('0'+i)))
				ref, err := sharedMemory.Store(id+"-"+string(rune('0'+i)), data, "text/plain", nil)
				assert.NoError(t, err)
				assert.NotNil(t, ref)

				retrieved, err := sharedMemory.Get(ref)
				assert.NoError(t, err)
				assert.Equal(t, data, retrieved)

				// Release reference
				sharedMemory.Release(ref.Id)
			}
			done <- true
		}(agentID)
	}

	// Wait for all agents to complete
	for i := 0; i < len(agents); i++ {
		<-done
	}

	// Verify shared memory stats
	stats := sharedMemory.Stats()
	assert.Greater(t, stats.Hits, int64(0))
	assert.Equal(t, int64(0), stats.Misses) // Should have no misses
}

// mockLargeTool is a test tool that returns large results
type mockLargeTool struct{}

func (m *mockLargeTool) Name() string {
	return "mock_large_tool"
}

func (m *mockLargeTool) Description() string {
	return "A test tool that returns large results"
}

func (m *mockLargeTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema("Empty input", nil, nil)
}

func (m *mockLargeTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	// Return 200KB of data
	largeData := make([]byte, 200*1024)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	return &shuttle.Result{
		Success: true,
		Data:    largeData,
	}, nil
}

func (m *mockLargeTool) Backend() string {
	return ""
}

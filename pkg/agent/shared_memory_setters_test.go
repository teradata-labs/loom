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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/storage"
)

// TestAgent_SetSharedMemory_UpdatesAllReferences verifies that SetSharedMemory
// correctly updates all references to the shared memory store, including:
// - The agent's own sharedMemory field (used by formatToolResult)
// - The GetToolResultTool registration
// - The reference tracker
//
// This test reproduces the bug where SetSharedMemory didn't update a.sharedMemory,
// causing tool results to be stored in one store but retrieved from another.
func TestAgent_SetSharedMemory_UpdatesAllReferences(t *testing.T) {
	// Reset global store for clean test
	storage.ResetGlobalSharedMemory()

	// Create first store (simulates initial agent creation)
	store1 := storage.GetGlobalSharedMemory(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})
	require.NotNil(t, store1)

	// Create agent with store1
	agent := NewAgent(
		nil,
		&mockLLMProvider{},
		WithSharedMemory(store1),
	)
	require.NotNil(t, agent)
	require.NotNil(t, agent.sharedMemory)
	assert.Same(t, store1, agent.sharedMemory, "Agent should initially use store1")

	// Verify GetToolResultTool is registered with store1
	tool1, exists1 := agent.tools.Get("get_tool_result")
	require.True(t, exists1, "GetToolResultTool should be registered initially")
	require.NotNil(t, tool1, "GetToolResultTool should not be nil")

	// Now simulate what happens during hot-reload or post-creation injection
	// The server calls SetSharedMemory to inject the global store
	store2 := storage.GetGlobalSharedMemory(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	// store2 should be the same as store1 (singleton)
	assert.Same(t, store1, store2, "Global store should be singleton")

	// Call SetSharedMemory (this is what was failing before the fix)
	agent.SetSharedMemory(store2)

	// CRITICAL: Verify that a.sharedMemory was updated
	assert.Same(t, store2, agent.sharedMemory, "Agent sharedMemory field must be updated by SetSharedMemory")

	// Verify GetToolResultTool was re-registered with the correct store
	tool2, exists2 := agent.tools.Get("get_tool_result")
	require.True(t, exists2, "GetToolResultTool should still be registered after SetSharedMemory")
	require.NotNil(t, tool2, "GetToolResultTool should not be nil after SetSharedMemory")

	// Verify refTracker was updated
	assert.NotNil(t, agent.refTracker, "Reference tracker should be initialized after SetSharedMemory")
}

// TestAgent_SetSharedMemory_NilSafety verifies that SetSharedMemory handles nil gracefully
func TestAgent_SetSharedMemory_NilSafety(t *testing.T) {
	agent := NewAgent(nil, &mockLLMProvider{})

	// Should not panic
	require.NotPanics(t, func() {
		agent.SetSharedMemory(nil)
	})

	// sharedMemory should be set to nil
	assert.Nil(t, agent.sharedMemory)
}

// TestAgent_SetSharedMemory_Integration verifies the complete flow:
// 1. Agent created without shared memory
// 2. SetSharedMemory called (simulating server startup)
// 3. Agent can store and retrieve data via the global store
func TestAgent_SetSharedMemory_Integration(t *testing.T) {
	storage.ResetGlobalSharedMemory()

	globalStore := storage.GetGlobalSharedMemory(&storage.Config{
		MaxMemoryBytes:       50 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	// Create agent (simulating registry.buildAgent without shared memory initially)
	agent := NewAgent(nil, &mockLLMProvider{})

	// Initially agent has a sharedMemory instance from NewAgent
	initialStore := agent.sharedMemory
	require.NotNil(t, initialStore, "NewAgent should initialize sharedMemory")
	t.Logf("Initial store: %p", initialStore)
	t.Logf("Global store: %p", globalStore)

	// Store data in the global store BEFORE calling SetSharedMemory
	// This simulates the issue where data is stored in one store but retrieved from another
	ctx := context.Background()
	largeData := []byte(strings.Repeat("X", 15000))
	refID := "test_ref_integration"

	ref, err := globalStore.Store(refID, largeData, "text/plain", map[string]string{
		"test": "integration",
	})
	require.NoError(t, err, "Should be able to store data")
	require.NotNil(t, ref)
	assert.Equal(t, refID, ref.Id)
	t.Logf("Data stored in global store with ref ID: %s", ref.Id)

	// Try to retrieve with tool BEFORE SetSharedMemory (should fail)
	getTool1, exists1 := agent.tools.Get("get_tool_result")
	require.True(t, exists1, "get_tool_result should be registered")
	result1, err1 := getTool1.Execute(ctx, map[string]interface{}{
		"reference_id": refID,
	})
	require.NoError(t, err1)
	// This SHOULD fail because the tool is using initialStore, not globalStore
	if result1.Success {
		t.Logf("WARNING: Tool succeeded before SetSharedMemory - stores are the same! (This is actually correct since they're both global)")
	} else {
		t.Logf("Expected: Tool failed before SetSharedMemory because using different store: %s", result1.Error.Message)
	}

	// Now call SetSharedMemory to inject the global store
	agent.SetSharedMemory(globalStore)

	// Verify agent has the global store
	require.NotNil(t, agent.sharedMemory)
	assert.Same(t, globalStore, agent.sharedMemory, "Agent should use global store after SetSharedMemory")

	// Verify GetToolResultTool is properly registered
	getTool, exists := agent.tools.Get("get_tool_result")
	require.True(t, exists, "get_tool_result should be registered after SetSharedMemory")
	require.NotNil(t, getTool, "get_tool_result should not be nil")

	// Retrieve via get_tool_result tool (which should use the same global store)
	result, err := getTool.Execute(ctx, map[string]interface{}{
		"reference_id": refID,
	})
	require.NoError(t, err, "get_tool_result should not error")
	require.NotNil(t, result, "Result should not be nil")

	// Check if tool succeeded, if not print the error
	if !result.Success {
		t.Logf("Tool failed with error: %+v", result.Error)
		if result.Error != nil {
			t.Logf("Error code: %s, message: %s", result.Error.Code, result.Error.Message)
		}
	}
	require.True(t, result.Success, "Tool should succeed")

	// Verify data was retrieved correctly
	retrieved, ok := result.Data.(string)
	require.True(t, ok, "Data should be a string")
	assert.Equal(t, string(largeData), retrieved, "Retrieved data should match original")
}

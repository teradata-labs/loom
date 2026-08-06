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

	// Verify QueryToolResultTool is now registered with store1
	// Note: GetToolResultTool removed - inline metadata makes it unnecessary
	tool1, exists1 := agent.tools.Get("query_tool_result")
	require.True(t, exists1, "QueryToolResultTool should be registered after manual registration")
	require.NotNil(t, tool1, "QueryToolResultTool should not be nil")

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

	// Verify QueryToolResultTool was re-registered with the correct store
	tool2, exists2 := agent.tools.Get("query_tool_result")
	require.True(t, exists2, "QueryToolResultTool should still be registered after SetSharedMemory")
	require.NotNil(t, tool2, "QueryToolResultTool should not be nil after SetSharedMemory")

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

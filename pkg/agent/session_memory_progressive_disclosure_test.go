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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionMemoryTool_ProgressiveDisclosure tests that session_memory tool
// is registered after 3+ sessions accumulate.
func TestSessionMemoryTool_ProgressiveDisclosure(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	backend := &mockBackend{}
	llmProvider := &simpleLLM{}
	agent := NewAgent(backend, llmProvider)

	// Set agent config name
	agent.config = &Config{Name: "test-agent"}

	// Set memory store
	agent.memory = NewMemoryWithStore(store)

	ctx := context.Background()

	// Initially, session_memory should NOT be registered
	assert.False(t, agent.tools.IsRegistered("session_memory"), "session_memory should not be registered initially")

	// Create 2 sessions - should NOT trigger registration
	for i := 0; i < 2; i++ {
		sessionID := fmt.Sprintf("session-%d", i)
		session := agent.memory.GetOrCreateSession(ctx, sessionID)
		session.AgentID = "test-agent"
		require.NoError(t, store.SaveSession(ctx, session))
	}

	// Check progressive disclosure (should not register yet)
	agent.checkAndRegisterSessionMemoryTool(ctx)
	assert.False(t, agent.tools.IsRegistered("session_memory"), "session_memory should not be registered with only 2 sessions")

	// Create 3rd session - should trigger registration
	session3ID := "session-3"
	session3 := agent.memory.GetOrCreateSession(ctx, session3ID)
	session3.AgentID = "test-agent"
	require.NoError(t, store.SaveSession(ctx, session3))

	// Check progressive disclosure (should register now)
	agent.checkAndRegisterSessionMemoryTool(ctx)
	assert.True(t, agent.tools.IsRegistered("session_memory"), "session_memory should be registered after 3+ sessions")

	// Verify tool is functional
	tool, exists := agent.tools.Get("session_memory")
	require.True(t, exists, "session_memory tool should exist")
	assert.Equal(t, "session_memory", tool.Name())
}

// TestSessionMemoryTool_NoStoreNoRegistration tests that session_memory
// is not registered if session store is unavailable.
func TestSessionMemoryTool_NoStoreNoRegistration(t *testing.T) {
	backend := &mockBackend{}
	llmProvider := &simpleLLM{}
	agent := NewAgent(backend, llmProvider)

	// Set agent config name
	agent.config = &Config{Name: "test-agent"}

	// No store - should not register
	agent.checkAndRegisterSessionMemoryTool(context.Background())
	assert.False(t, agent.tools.IsRegistered("session_memory"), "session_memory should not be registered without store")
}

// TestSessionMemoryTool_IdempotentRegistration tests that progressive disclosure
// doesn't double-register the tool.
func TestSessionMemoryTool_IdempotentRegistration(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	backend := &mockBackend{}
	llmProvider := &simpleLLM{}
	agent := NewAgent(backend, llmProvider)

	agent.config = &Config{Name: "test-agent"}
	agent.memory = NewMemoryWithStore(store)

	ctx := context.Background()

	// Create 3 sessions
	for i := 0; i < 3; i++ {
		sessionID := fmt.Sprintf("session-%d", i)
		session := agent.memory.GetOrCreateSession(ctx, sessionID)
		session.AgentID = "test-agent"
		require.NoError(t, store.SaveSession(ctx, session))
	}

	// First registration
	agent.checkAndRegisterSessionMemoryTool(ctx)
	assert.True(t, agent.tools.IsRegistered("session_memory"))

	// Call again - should not panic or duplicate
	agent.checkAndRegisterSessionMemoryTool(ctx)
	assert.True(t, agent.tools.IsRegistered("session_memory"))

	// Verify still functional
	tool, exists := agent.tools.Get("session_memory")
	require.True(t, exists, "session_memory tool should exist")
	assert.Equal(t, "session_memory", tool.Name())
}

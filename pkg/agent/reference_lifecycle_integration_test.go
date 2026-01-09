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
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
	"github.com/teradata-labs/loom/pkg/types"
)

// TestReferenceLifecycle_Integration tests the complete reference lifecycle:
// Session creation → Reference pinning → Session deletion → Reference cleanup
func TestReferenceLifecycle_Integration(t *testing.T) {
	// Create temporary database for session storage
	tmpfile, err := os.CreateTemp("", "reference_lifecycle_test_*.db")
	require.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	// Create SessionStore with observability
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpfile.Name(), tracer)
	require.NoError(t, err)
	defer store.Close()

	// Create SharedMemoryStore (global singleton pattern)
	sharedMem := storage.GetGlobalSharedMemory(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024, // 10MB for test
		CompressionThreshold: 1024,             // Compress >1KB
		TTLSeconds:           60,
	})

	// Create simple LLM provider (returns text response)
	llmProvider := &simpleLLM{response: "Test response"}

	// Create backend (mock)
	backend := &mockBackend{}

	// Create agent with SessionStore and SharedMemory
	memory := NewMemoryWithStore(store)
	agent := NewAgent(
		backend,
		llmProvider,
		WithMemory(memory),
		WithTracer(tracer),
	)

	// Verify refTracker was initialized
	require.NotNil(t, agent.refTracker, "refTracker should be initialized when sharedMemory is configured")

	// Verify cleanup hook was registered
	require.NotNil(t, memory.GetStore(), "SessionStore should be accessible from memory")

	// Create a test session
	sessionID := "test_session_ref_lifecycle"
	session := agent.CreateSession(sessionID)
	require.NotNil(t, session)

	// Manually create and store a reference (simulating what formatToolResult does)
	ctx := context.Background()
	refID := "ref_test_tool_12345"
	largeData := make([]byte, 15*1024) // 15KB
	for i := range largeData {
		largeData[i] = byte('A' + (i % 26))
	}

	// Store reference in shared memory
	storedRef, err := sharedMem.Store(refID, largeData, "text/plain", map[string]string{
		"tool_name":  "test_tool",
		"session_id": sessionID,
	})
	require.NoError(t, err)
	assert.Equal(t, refID, storedRef.Id)

	// Pin the reference for the session (this is what formatToolResult does)
	agent.refTracker.PinForSession(sessionID, refID)

	// Verify it was pinned
	stats := agent.refTracker.Stats()
	assert.Equal(t, 1, stats.SessionCount, "Should have 1 session with pinned references")
	assert.Equal(t, 1, stats.TotalRefs, "Should have 1 pinned reference")

	// Verify the reference is tracked for this session
	refs := agent.refTracker.GetSessionReferences(sessionID)
	require.Len(t, refs, 1, "Session should have exactly 1 pinned reference")
	assert.Equal(t, refID, refs[0])

	// Now delete the session (this should trigger cleanup hook)
	err = store.DeleteSession(ctx, sessionID)
	require.NoError(t, err, "Session deletion should succeed")

	// Verify the reference was unpinned
	statsAfterDelete := agent.refTracker.Stats()
	assert.Equal(t, 0, statsAfterDelete.SessionCount, "Should have 0 sessions after deletion")
	assert.Equal(t, 0, statsAfterDelete.TotalRefs, "Should have 0 pinned references after deletion")

	// Verify the reference is no longer tracked for the deleted session
	refsAfterDelete := agent.refTracker.GetSessionReferences(sessionID)
	assert.Empty(t, refsAfterDelete, "Deleted session should have no pinned references")
}

// TestReferenceLifecycle_MultipleReferences tests cleanup with multiple references per session
func TestReferenceLifecycle_MultipleReferences(t *testing.T) {
	// Create temporary database
	tmpfile, err := os.CreateTemp("", "reference_multi_test_*.db")
	require.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	// Setup agent with store and shared memory
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpfile.Name(), tracer)
	require.NoError(t, err)
	defer store.Close()

	sharedMem := storage.GetGlobalSharedMemory(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024,
		TTLSeconds:           60,
	})

	llmProvider := &simpleLLM{response: "Test response"}
	backend := &mockBackend{}

	memory := NewMemoryWithStore(store)
	agent := NewAgent(backend, llmProvider, WithMemory(memory), WithTracer(tracer))

	// Create session
	sessionID := "test_session_multi_refs"
	agent.CreateSession(sessionID)

	// Create and pin multiple references
	ctx := context.Background()
	const numRefs = 3
	for i := 0; i < numRefs; i++ {
		refID := "ref_test_" + string(rune('A'+i))
		largeData := make([]byte, 15*1024)
		for j := range largeData {
			largeData[j] = byte('A' + (j % 26))
		}

		_, err := sharedMem.Store(refID, largeData, "text/plain", map[string]string{
			"tool_name":  "test_tool",
			"session_id": sessionID,
		})
		require.NoError(t, err)

		agent.refTracker.PinForSession(sessionID, refID)
	}

	// Verify all references were pinned
	stats := agent.refTracker.Stats()
	assert.Equal(t, 1, stats.SessionCount, "Should have 1 session")
	assert.Equal(t, numRefs, stats.TotalRefs, "Should have %d pinned references", numRefs)

	// Delete session
	err = store.DeleteSession(ctx, sessionID)
	require.NoError(t, err)

	// Verify all references were unpinned
	statsAfter := agent.refTracker.Stats()
	assert.Equal(t, 0, statsAfter.SessionCount, "Should have 0 sessions after deletion")
	assert.Equal(t, 0, statsAfter.TotalRefs, "Should have 0 pinned references after deletion")
}

// TestReferenceLifecycle_NoStore tests that refTracker works without SessionStore
func TestReferenceLifecycle_NoStore(t *testing.T) {
	// Create agent WITHOUT SessionStore (in-memory only)
	llmProvider := &simpleLLM{response: "Test response"}
	backend := &mockBackend{}

	agent := NewAgent(backend, llmProvider)

	// refTracker should still be initialized (SharedMemory exists by default)
	require.NotNil(t, agent.refTracker, "refTracker should be initialized")

	// No cleanup hook should be registered (no SessionStore)
	// This is fine - references can still be manually unpinned

	// Create a session and verify basic functionality
	sessionID := "test_session_no_store"
	session := agent.CreateSession(sessionID)
	require.NotNil(t, session)

	// Pin a reference manually
	sharedMem := storage.GetGlobalSharedMemory(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024,
		TTLSeconds:           60,
	})

	refID := "ref_no_store_test"
	largeData := make([]byte, 15*1024)
	for i := range largeData {
		largeData[i] = byte('C' + (i % 26))
	}

	_, err := sharedMem.Store(refID, largeData, "text/plain", map[string]string{
		"tool_name":  "test_tool",
		"session_id": sessionID,
	})
	require.NoError(t, err)

	agent.refTracker.PinForSession(sessionID, refID)

	// Verify reference was pinned
	stats := agent.refTracker.Stats()
	assert.Equal(t, 1, stats.SessionCount, "Should have 1 session with pinned references")
	assert.Equal(t, 1, stats.TotalRefs, "Should have 1 pinned reference")

	// Manual cleanup (simulating in-memory session deletion without store)
	agent.refTracker.UnpinSession(sessionID)

	// Verify cleanup worked
	statsAfter := agent.refTracker.Stats()
	assert.Equal(t, 0, statsAfter.SessionCount, "Should have 0 sessions after manual cleanup")
	assert.Equal(t, 0, statsAfter.TotalRefs, "Should have 0 pinned references after manual cleanup")
}

// simpleLLM is a minimal LLM implementation for testing
type simpleLLM struct {
	response string
}

func (l *simpleLLM) Chat(ctx context.Context, messages []types.Message, tools []shuttle.Tool) (*types.LLMResponse, error) {
	return &types.LLMResponse{
		Content: l.response,
		Usage: types.Usage{
			InputTokens:  10,
			OutputTokens: 10,
		},
	}, nil
}

func (l *simpleLLM) Name() string {
	return "test-llm"
}

func (l *simpleLLM) Model() string {
	return "test-model"
}

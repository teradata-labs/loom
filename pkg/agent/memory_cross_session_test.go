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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// TestMemory_GetOrCreateSessionWithAgent tests creating sessions with agent metadata.
func TestMemory_GetOrCreateSessionWithAgent(t *testing.T) {
	memory := NewMemory()

	// Test: Create new session with agent metadata
	session := memory.GetOrCreateSessionWithAgent("session-1", "agent-1", "parent-session")
	assert.NotNil(t, session)
	assert.Equal(t, "session-1", session.ID)
	assert.Equal(t, "agent-1", session.AgentID)
	assert.Equal(t, "parent-session", session.ParentSessionID)

	// Test: Retrieve same session (should return existing)
	session2 := memory.GetOrCreateSessionWithAgent("session-1", "", "")
	assert.Equal(t, session, session2, "Should return same session instance")
	assert.Equal(t, "agent-1", session2.AgentID, "Should preserve existing agent ID")
	assert.Equal(t, "parent-session", session2.ParentSessionID, "Should preserve existing parent")

	// Test: Update agent metadata on existing session
	session3 := memory.GetOrCreateSessionWithAgent("session-2", "", "")
	assert.Equal(t, "", session3.AgentID)
	assert.Equal(t, "", session3.ParentSessionID)

	session4 := memory.GetOrCreateSessionWithAgent("session-2", "agent-2", "parent-2")
	assert.Equal(t, session3, session4, "Should return same session instance")
	assert.Equal(t, "agent-2", session4.AgentID, "Should update agent ID")
	assert.Equal(t, "parent-2", session4.ParentSessionID, "Should update parent")

	// Test: Don't overwrite existing metadata
	session5 := memory.GetOrCreateSessionWithAgent("session-2", "agent-3", "parent-3")
	assert.Equal(t, "agent-2", session5.AgentID, "Should NOT overwrite existing agent ID")
	assert.Equal(t, "parent-2", session5.ParentSessionID, "Should NOT overwrite existing parent")
}

// TestMemory_GetOrCreateSessionWithAgent_WithStore tests persistence.
func TestMemory_GetOrCreateSessionWithAgent_WithStore(t *testing.T) {
	// Create temporary database
	tmpFile, err := os.CreateTemp("", "loom-test-*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSessionStore(tmpFile.Name(), observability.NewNoOpTracer())
	require.NoError(t, err)
	defer store.Close()

	memory := NewMemoryWithStore(store)

	// Test: Create new session and verify it's persisted
	session1 := memory.GetOrCreateSessionWithAgent("session-1", "agent-1", "parent-1")
	assert.Equal(t, "agent-1", session1.AgentID)
	assert.Equal(t, "parent-1", session1.ParentSessionID)

	// Verify persisted to database
	loadedSession, err := store.LoadSession(context.Background(), "session-1")
	require.NoError(t, err)
	assert.Equal(t, "agent-1", loadedSession.AgentID)
	assert.Equal(t, "parent-1", loadedSession.ParentSessionID)

	// Test: Update metadata on existing session and verify persistence
	session2 := memory.GetOrCreateSessionWithAgent("session-2", "", "")
	assert.Equal(t, "", session2.AgentID)

	session3 := memory.GetOrCreateSessionWithAgent("session-2", "agent-2", "parent-2")
	assert.Equal(t, "agent-2", session3.AgentID)

	// Verify updated metadata is persisted
	loadedSession2, err := store.LoadSession(context.Background(), "session-2")
	require.NoError(t, err)
	assert.Equal(t, "agent-2", loadedSession2.AgentID)
	assert.Equal(t, "parent-2", loadedSession2.ParentSessionID)
}

// TestMemory_Observers tests the observer pattern for real-time updates.
func TestMemory_Observers(t *testing.T) {
	memory := NewMemory()

	// Create a session with agent ID
	_ = memory.GetOrCreateSessionWithAgent("session-1", "agent-1", "")

	// Create an observer to track notifications
	var receivedAgentIDs []string
	var receivedSessionIDs []string
	var receivedMessages []Message
	var mu sync.Mutex

	observer := MemoryObserverFunc(func(agentID, sessionID string, msg Message) {
		mu.Lock()
		defer mu.Unlock()
		receivedAgentIDs = append(receivedAgentIDs, agentID)
		receivedSessionIDs = append(receivedSessionIDs, sessionID)
		receivedMessages = append(receivedMessages, msg)
	})

	// Register observer
	memory.RegisterObserver("agent-1", observer)

	// Add message via Memory.AddMessage (should trigger observer)
	testMsg := Message{
		Role:      "user",
		Content:   "Test message",
		Timestamp: time.Now(),
	}
	memory.AddMessage("session-1", testMsg)

	// Give async notification time to complete
	time.Sleep(50 * time.Millisecond)

	// Verify observer was notified
	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, receivedMessages, 1)
	assert.Equal(t, "agent-1", receivedAgentIDs[0])
	assert.Equal(t, "session-1", receivedSessionIDs[0])
	assert.Equal(t, "Test message", receivedMessages[0].Content)
}

// TestMemory_Observers_MultipleObservers tests multiple observers for same agent.
func TestMemory_Observers_MultipleObservers(t *testing.T) {
	memory := NewMemory()
	memory.GetOrCreateSessionWithAgent("session-1", "agent-1", "")

	// Create multiple observers
	var count1, count2 atomic.Int32

	observer1 := MemoryObserverFunc(func(agentID, sessionID string, msg Message) {
		count1.Add(1)
	})

	observer2 := MemoryObserverFunc(func(agentID, sessionID string, msg Message) {
		count2.Add(1)
	})

	// Register both observers
	memory.RegisterObserver("agent-1", observer1)
	memory.RegisterObserver("agent-1", observer2)

	// Add message
	memory.AddMessage("session-1", Message{Role: "user", Content: "test", Timestamp: time.Now()})

	// Give async notifications time to complete
	time.Sleep(50 * time.Millisecond)

	// Both observers should be notified
	assert.Equal(t, int32(1), count1.Load())
	assert.Equal(t, int32(1), count2.Load())
}

// TestMemory_Observers_NoAgentID tests that messages without agent ID don't trigger observers.
func TestMemory_Observers_NoAgentID(t *testing.T) {
	memory := NewMemory()

	// Create session WITHOUT agent ID
	memory.GetOrCreateSession("session-1")

	var count atomic.Int32
	observer := MemoryObserverFunc(func(agentID, sessionID string, msg Message) {
		count.Add(1)
	})

	// Register observer (for any agent)
	memory.RegisterObserver("some-agent", observer)

	// Add message to session without agent ID
	memory.AddMessage("session-1", Message{Role: "user", Content: "test", Timestamp: time.Now()})
	time.Sleep(50 * time.Millisecond)

	// Should NOT notify (no agent ID on session)
	assert.Equal(t, int32(0), count.Load())
}

// TestMemory_Observers_DifferentAgents tests agent isolation.
func TestMemory_Observers_DifferentAgents(t *testing.T) {
	memory := NewMemory()
	memory.GetOrCreateSessionWithAgent("session-1", "agent-1", "")
	memory.GetOrCreateSessionWithAgent("session-2", "agent-2", "")

	var agent1Count, agent2Count atomic.Int32

	observer1 := MemoryObserverFunc(func(agentID, sessionID string, msg Message) {
		agent1Count.Add(1)
	})

	observer2 := MemoryObserverFunc(func(agentID, sessionID string, msg Message) {
		agent2Count.Add(1)
	})

	// Register observers for different agents
	memory.RegisterObserver("agent-1", observer1)
	memory.RegisterObserver("agent-2", observer2)

	// Add messages to both sessions
	memory.AddMessage("session-1", Message{Role: "user", Content: "test1", Timestamp: time.Now()})
	memory.AddMessage("session-2", Message{Role: "user", Content: "test2", Timestamp: time.Now()})
	time.Sleep(50 * time.Millisecond)

	// Each observer should only receive messages for their agent
	assert.Equal(t, int32(1), agent1Count.Load())
	assert.Equal(t, int32(1), agent2Count.Load())
}

// TestMemory_AddMessage_SessionContextPersistence tests session_context is persisted.
func TestMemory_AddMessage_SessionContextPersistence(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "loom-test-*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSessionStore(tmpFile.Name(), observability.NewNoOpTracer())
	require.NoError(t, err)
	defer store.Close()

	memory := NewMemoryWithStore(store)
	memory.GetOrCreateSessionWithAgent("session-1", "agent-1", "")

	// Add message with coordinator context
	msg := Message{
		Role:           "user",
		Content:        "Test message",
		SessionContext: types.SessionContextCoordinator,
		Timestamp:      time.Now(),
	}
	memory.AddMessage("session-1", msg)

	// Load messages from store and verify context is preserved
	messages, err := store.LoadMessages(context.Background(), "session-1")
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, types.SessionContextCoordinator, messages[0].SessionContext)
}

// TestMemory_Observers_ConcurrentAccess tests race conditions.
func TestMemory_Observers_ConcurrentAccess(t *testing.T) {
	memory := NewMemory()
	memory.GetOrCreateSessionWithAgent("session-1", "agent-1", "")

	var count atomic.Int32
	observer := MemoryObserverFunc(func(agentID, sessionID string, msg Message) {
		count.Add(1)
	})

	memory.RegisterObserver("agent-1", observer)

	// Concurrently add messages and register/unregister observers
	var wg sync.WaitGroup
	numGoroutines := 10

	// Add messages concurrently
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(i int) {
			defer wg.Done()
			memory.AddMessage("session-1", Message{
				Role:      "user",
				Content:   "test",
				Timestamp: time.Now(),
			})
		}(i)
	}

	// Register observers concurrently (no unregister - function comparison not supported)
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			tempObserver := MemoryObserverFunc(func(agentID, sessionID string, msg Message) {})
			memory.RegisterObserver("agent-1", tempObserver)
		}()
	}

	wg.Wait()
	time.Sleep(100 * time.Millisecond) // Let async notifications complete

	// Verify at least the original observer received notifications
	assert.Greater(t, count.Load(), int32(0), "Observer should receive notifications")
}

// TestMemory_SegmentedMemoryReattachment tests that SegmentedMemory is reattached when loading sessions from DB.
// This is a critical test for the compression fix - sessions loaded from DB must have their SegmentedMemory
// reinitialized since it's not persisted (interface{} doesn't serialize).
func TestMemory_SegmentedMemoryReattachment(t *testing.T) {
	// Create temporary database
	tmpFile, err := os.CreateTemp("", "loom-test-compression-*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSessionStore(tmpFile.Name(), observability.NewNoOpTracer())
	require.NoError(t, err)
	defer store.Close()

	// Create memory with compression profile (conversational: max_l1=12)
	conversationalProfile := CompressionProfile{
		Name:                     "conversational",
		MaxL1Messages:            12,
		MinL1Messages:            6,
		WarningThresholdPercent:  70,
		CriticalThresholdPercent: 85,
		NormalBatchSize:          4,
		WarningBatchSize:         6,
		CriticalBatchSize:        8,
	}

	memory := NewMemoryWithStore(store)
	memory.SetCompressionProfile(&conversationalProfile)
	memory.SetSystemPromptFunc(func() string {
		return "Test system prompt"
	})

	// Create a new session and verify SegmentedMemory exists
	session1 := memory.GetOrCreateSessionWithAgent("test-session-1", "test-agent-1", "")
	require.NotNil(t, session1)
	require.NotNil(t, session1.SegmentedMem, "New session should have SegmentedMemory")

	// Add a message to verify it works
	session1.AddMessage(Message{
		Role:      "user",
		Content:   "Test message 1",
		Timestamp: time.Now(),
	})

	// Verify message was added to SegmentedMemory
	segMem1, ok := session1.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok, "SegmentedMem should be *SegmentedMemory")
	messages1 := segMem1.GetMessages()
	assert.Greater(t, len(messages1), 0, "SegmentedMemory should have messages")

	// Simulate server restart: clear in-memory cache
	memory.mu.Lock()
	memory.sessions = make(map[string]*types.Session)
	memory.mu.Unlock()

	// Load session from DB (this is where the bug was - SegmentedMem would be nil)
	session2 := memory.GetOrCreateSessionWithAgent("test-session-1", "", "")
	require.NotNil(t, session2)

	// CRITICAL: Verify SegmentedMemory was reattached
	require.NotNil(t, session2.SegmentedMem, "Session loaded from DB MUST have SegmentedMemory reattached")

	// Verify SegmentedMemory is functional and has correct type
	segMem2, ok := session2.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok, "Reattached SegmentedMem should be *SegmentedMemory")

	// Verify it has the correct compression profile
	assert.Equal(t, 12, segMem2.maxL1Messages, "Should have conversational maxL1Messages")
	assert.Equal(t, "conversational", segMem2.compressionProfile.Name, "Should have conversational profile")

	// Add another message and verify it goes to SegmentedMemory
	session2.AddMessage(Message{
		Role:      "assistant",
		Content:   "Test message 2 after reload",
		Timestamp: time.Now(),
	})

	messages2 := segMem2.GetMessages()
	assert.Greater(t, len(messages2), 0, "SegmentedMemory should accept new messages after reattachment")

	// Verify FailureTracker was also reattached
	require.NotNil(t, session2.FailureTracker, "Session loaded from DB MUST have FailureTracker reattached")
}

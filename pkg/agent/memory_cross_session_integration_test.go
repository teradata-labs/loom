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

// TestCrossSessionMemory_CoordinatorToSubAgent_Integration tests the full flow of:
// 1. Coordinator creates sessions for itself and sub-agents
// 2. Coordinator adds messages to its session
// 3. Sub-agent accesses parent (coordinator) messages
// 4. Sub-agent adds its own messages
// 5. All data persists to SessionStore
// 6. Messages are filtered by SessionContext
func TestCrossSessionMemory_CoordinatorToSubAgent_Integration(t *testing.T) {
	// Create temporary database
	tmpFile, err := os.CreateTemp("", "loom-test-*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSessionStore(tmpFile.Name(), observability.NewNoOpTracer())
	require.NoError(t, err)
	defer store.Close()

	memory := NewMemoryWithStore(store)
	ctx := context.Background()

	// Step 1: Coordinator creates its session
	coordSession := memory.GetOrCreateSessionWithAgent("coord-session-1", "coordinator", "")
	assert.Equal(t, "coordinator", coordSession.AgentID)
	assert.Equal(t, "", coordSession.ParentSessionID)

	// Step 2: Coordinator adds messages (mix of coordinator and shared context)
	coordMsg1 := Message{
		Role:           "user",
		Content:        "Analyze this dataset and provide insights",
		SessionContext: types.SessionContextCoordinator,
		Timestamp:      time.Now().Add(-5 * time.Second),
	}
	coordMsg2 := Message{
		Role:           "assistant",
		Content:        "Internal coordinator thought: I should delegate to analyzer sub-agent",
		SessionContext: types.SessionContextDirect,
		Timestamp:      time.Now().Add(-4 * time.Second),
	}
	coordMsg3 := Message{
		Role:           "assistant",
		Content:        "Delegating analysis to sub-agent...",
		SessionContext: types.SessionContextShared,
		Timestamp:      time.Now().Add(-3 * time.Second),
	}

	memory.AddMessage("coord-session-1", coordMsg1)
	memory.AddMessage("coord-session-1", coordMsg2)
	memory.AddMessage("coord-session-1", coordMsg3)

	// Step 3: Sub-agent is created with link to coordinator session
	subAgentSession := memory.GetOrCreateSessionWithAgent("sub-agent-session-1", "analyzer", "coord-session-1")
	assert.Equal(t, "analyzer", subAgentSession.AgentID)
	assert.Equal(t, "coord-session-1", subAgentSession.ParentSessionID)

	// Step 4: Sub-agent accesses parent messages (should only see coordinator + shared)
	parentMessages, err := store.LoadMessagesFromParentSession(ctx, "sub-agent-session-1")
	require.NoError(t, err)
	assert.Len(t, parentMessages, 2, "Should only see coordinator and shared messages")
	assert.Equal(t, "Analyze this dataset and provide insights", parentMessages[0].Content)
	assert.Equal(t, "Delegating analysis to sub-agent...", parentMessages[1].Content)

	// Step 5: Sub-agent adds its own messages
	subMsg1 := Message{
		Role:           "user",
		Content:        "Executing analysis on dataset",
		SessionContext: types.SessionContextDirect,
		Timestamp:      time.Now().Add(-2 * time.Second),
	}
	subMsg2 := Message{
		Role:           "assistant",
		Content:        "Analysis complete: Found 3 outliers",
		SessionContext: types.SessionContextShared,
		Timestamp:      time.Now().Add(-1 * time.Second),
	}

	memory.AddMessage("sub-agent-session-1", subMsg1)
	memory.AddMessage("sub-agent-session-1", subMsg2)

	// Step 6: Load all messages for analyzer agent (should include parent + own)
	allAnalyzerMessages, err := store.LoadMessagesForAgent(ctx, "analyzer")
	require.NoError(t, err)
	assert.Len(t, allAnalyzerMessages, 4, "Should have 2 parent messages + 2 own messages")

	// Verify messages are ordered by timestamp
	assert.Equal(t, "Analyze this dataset and provide insights", allAnalyzerMessages[0].Content)
	assert.Equal(t, "Delegating analysis to sub-agent...", allAnalyzerMessages[1].Content)
	assert.Equal(t, "Executing analysis on dataset", allAnalyzerMessages[2].Content)
	assert.Equal(t, "Analysis complete: Found 3 outliers", allAnalyzerMessages[3].Content)

	// Step 7: Verify persistence - reload from database
	loadedCoordSession, err := store.LoadSession(ctx, "coord-session-1")
	require.NoError(t, err)
	assert.Equal(t, "coordinator", loadedCoordSession.AgentID)

	loadedSubSession, err := store.LoadSession(ctx, "sub-agent-session-1")
	require.NoError(t, err)
	assert.Equal(t, "analyzer", loadedSubSession.AgentID)
	assert.Equal(t, "coord-session-1", loadedSubSession.ParentSessionID)

	// Step 8: Verify LoadAgentSessions returns all sessions for analyzer
	analyzerSessions, err := store.LoadAgentSessions(ctx, "analyzer")
	require.NoError(t, err)
	assert.Len(t, analyzerSessions, 1)
	assert.Equal(t, "sub-agent-session-1", analyzerSessions[0])
}

// TestCrossSessionMemory_MultipleSubAgents_Integration tests multiple sub-agents
// sharing the same coordinator session with proper isolation.
func TestCrossSessionMemory_MultipleSubAgents_Integration(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "loom-test-*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSessionStore(tmpFile.Name(), observability.NewNoOpTracer())
	require.NoError(t, err)
	defer store.Close()

	memory := NewMemoryWithStore(store)
	ctx := context.Background()

	// Create coordinator session
	memory.GetOrCreateSessionWithAgent("coord-session-1", "coordinator", "")

	// Coordinator adds shared message
	coordMsg := Message{
		Role:           "user",
		Content:        "Analyze data quality and validate results",
		SessionContext: types.SessionContextShared,
		Timestamp:      time.Now().Add(-10 * time.Second),
	}
	memory.AddMessage("coord-session-1", coordMsg)

	// Create two sub-agents
	memory.GetOrCreateSessionWithAgent("analyzer-session", "analyzer", "coord-session-1")
	memory.GetOrCreateSessionWithAgent("validator-session", "validator", "coord-session-1")

	// Analyzer adds its messages
	analyzerMsg := Message{
		Role:           "assistant",
		Content:        "Data quality analysis: 95% clean",
		SessionContext: types.SessionContextDirect,
		Timestamp:      time.Now().Add(-8 * time.Second),
	}
	memory.AddMessage("analyzer-session", analyzerMsg)

	// Validator adds its messages
	validatorMsg := Message{
		Role:           "assistant",
		Content:        "Validation passed: Results accurate",
		SessionContext: types.SessionContextDirect,
		Timestamp:      time.Now().Add(-6 * time.Second),
	}
	memory.AddMessage("validator-session", validatorMsg)

	// Verify each sub-agent sees parent message but NOT other sub-agent's direct messages
	analyzerParentMsgs, err := store.LoadMessagesFromParentSession(ctx, "analyzer-session")
	require.NoError(t, err)
	assert.Len(t, analyzerParentMsgs, 1)
	assert.Equal(t, "Analyze data quality and validate results", analyzerParentMsgs[0].Content)

	validatorParentMsgs, err := store.LoadMessagesFromParentSession(ctx, "validator-session")
	require.NoError(t, err)
	assert.Len(t, validatorParentMsgs, 1)
	assert.Equal(t, "Analyze data quality and validate results", validatorParentMsgs[0].Content)

	// Verify LoadMessagesForAgent returns only parent + own messages (not other sub-agents)
	analyzerAllMsgs, err := store.LoadMessagesForAgent(ctx, "analyzer")
	require.NoError(t, err)
	assert.Len(t, analyzerAllMsgs, 2) // 1 parent + 1 own
	assert.Equal(t, "Analyze data quality and validate results", analyzerAllMsgs[0].Content)
	assert.Equal(t, "Data quality analysis: 95% clean", analyzerAllMsgs[1].Content)

	validatorAllMsgs, err := store.LoadMessagesForAgent(ctx, "validator")
	require.NoError(t, err)
	assert.Len(t, validatorAllMsgs, 2) // 1 parent + 1 own
	assert.Equal(t, "Analyze data quality and validate results", validatorAllMsgs[0].Content)
	assert.Equal(t, "Validation passed: Results accurate", validatorAllMsgs[1].Content)
}

// TestCrossSessionMemory_RealtimeObservers_Integration tests real-time observer
// notifications across sessions with persistence.
func TestCrossSessionMemory_RealtimeObservers_Integration(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "loom-test-*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSessionStore(tmpFile.Name(), observability.NewNoOpTracer())
	require.NoError(t, err)
	defer store.Close()

	memory := NewMemoryWithStore(store)

	// Create coordinator and sub-agent sessions
	memory.GetOrCreateSessionWithAgent("coord-session-1", "coordinator", "")
	memory.GetOrCreateSessionWithAgent("sub-agent-session-1", "analyzer", "coord-session-1")

	// Set up observers for both agents
	var coordMessages []Message
	var coordMu sync.Mutex
	coordObserver := MemoryObserverFunc(func(agentID, sessionID string, msg Message) {
		coordMu.Lock()
		defer coordMu.Unlock()
		coordMessages = append(coordMessages, msg)
	})
	memory.RegisterObserver("coordinator", coordObserver)

	var analyzerMessages []Message
	var analyzerMu sync.Mutex
	analyzerObserver := MemoryObserverFunc(func(agentID, sessionID string, msg Message) {
		analyzerMu.Lock()
		defer analyzerMu.Unlock()
		analyzerMessages = append(analyzerMessages, msg)
	})
	memory.RegisterObserver("analyzer", analyzerObserver)

	// Coordinator adds message
	coordMsg := Message{
		Role:           "user",
		Content:        "Start analysis",
		SessionContext: types.SessionContextCoordinator,
		Timestamp:      time.Now(),
	}
	memory.AddMessage("coord-session-1", coordMsg)

	// Sub-agent adds message
	subMsg := Message{
		Role:           "assistant",
		Content:        "Analysis complete",
		SessionContext: types.SessionContextShared,
		Timestamp:      time.Now(),
	}
	memory.AddMessage("sub-agent-session-1", subMsg)

	// Wait for async notifications
	time.Sleep(100 * time.Millisecond)

	// Verify coordinator observer received only coordinator messages
	coordMu.Lock()
	assert.Len(t, coordMessages, 1)
	assert.Equal(t, "Start analysis", coordMessages[0].Content)
	coordMu.Unlock()

	// Verify analyzer observer received only analyzer messages
	analyzerMu.Lock()
	assert.Len(t, analyzerMessages, 1)
	assert.Equal(t, "Analysis complete", analyzerMessages[0].Content)
	analyzerMu.Unlock()

	// Verify both messages persisted to database
	coordStoredMsgs, err := store.LoadMessages(context.Background(), "coord-session-1")
	require.NoError(t, err)
	assert.Len(t, coordStoredMsgs, 1)

	subStoredMsgs, err := store.LoadMessages(context.Background(), "sub-agent-session-1")
	require.NoError(t, err)
	assert.Len(t, subStoredMsgs, 1)
}

// TestCrossSessionMemory_ConcurrentMultiAgent_Integration tests concurrent access
// from multiple agents with full persistence and race detection.
func TestCrossSessionMemory_ConcurrentMultiAgent_Integration(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "loom-test-*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSessionStore(tmpFile.Name(), observability.NewNoOpTracer())
	require.NoError(t, err)
	defer store.Close()

	memory := NewMemoryWithStore(store)

	// Create coordinator
	memory.GetOrCreateSessionWithAgent("coord-session-1", "coordinator", "")

	// Create multiple sub-agents concurrently
	numSubAgents := 5
	var wg sync.WaitGroup

	// Concurrently create sub-agent sessions
	wg.Add(numSubAgents)
	for i := 0; i < numSubAgents; i++ {
		go func(idx int) {
			defer wg.Done()
			sessionID := "sub-agent-session-" + string(rune('A'+idx))
			agentID := "analyzer-" + string(rune('A'+idx))
			memory.GetOrCreateSessionWithAgent(sessionID, agentID, "coord-session-1")
		}(i)
	}
	wg.Wait()

	// Concurrently add messages from all agents
	var messageCount atomic.Int32
	wg.Add(numSubAgents)
	for i := 0; i < numSubAgents; i++ {
		go func(idx int) {
			defer wg.Done()
			sessionID := "sub-agent-session-" + string(rune('A'+idx))
			msg := Message{
				Role:           "assistant",
				Content:        "Message from agent " + string(rune('A'+idx)),
				SessionContext: types.SessionContextDirect,
				Timestamp:      time.Now(),
			}
			memory.AddMessage(sessionID, msg)
			messageCount.Add(1)
		}(i)
	}
	wg.Wait()

	// Verify all messages were added
	assert.Equal(t, int32(numSubAgents), messageCount.Load())

	// Verify all sessions were persisted
	for i := 0; i < numSubAgents; i++ {
		sessionID := "sub-agent-session-" + string(rune('A'+i))
		session, err := store.LoadSession(context.Background(), sessionID)
		require.NoError(t, err)
		assert.Equal(t, "coord-session-1", session.ParentSessionID)
	}

	// Verify LoadAgentSessions works for each sub-agent
	for i := 0; i < numSubAgents; i++ {
		agentID := "analyzer-" + string(rune('A'+i))
		sessions, err := store.LoadAgentSessions(context.Background(), agentID)
		require.NoError(t, err)
		assert.Len(t, sessions, 1, "Each sub-agent should have exactly 1 session")
	}
}

// TestCrossSessionMemory_SessionContextFiltering_Integration tests that
// SessionContext filtering works correctly across the full flow.
func TestCrossSessionMemory_SessionContextFiltering_Integration(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "loom-test-*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSessionStore(tmpFile.Name(), observability.NewNoOpTracer())
	require.NoError(t, err)
	defer store.Close()

	memory := NewMemoryWithStore(store)
	ctx := context.Background()

	// Create coordinator and sub-agent
	memory.GetOrCreateSessionWithAgent("coord-session-1", "coordinator", "")
	memory.GetOrCreateSessionWithAgent("sub-agent-session-1", "analyzer", "coord-session-1")

	// Coordinator adds messages with ALL three contexts
	msg1 := Message{
		Role:           "user",
		Content:        "Coordinator context message",
		SessionContext: types.SessionContextCoordinator,
		Timestamp:      time.Now().Add(-5 * time.Second),
	}
	msg2 := Message{
		Role:           "assistant",
		Content:        "Direct context message (internal)",
		SessionContext: types.SessionContextDirect,
		Timestamp:      time.Now().Add(-4 * time.Second),
	}
	msg3 := Message{
		Role:           "assistant",
		Content:        "Shared context message",
		SessionContext: types.SessionContextShared,
		Timestamp:      time.Now().Add(-3 * time.Second),
	}

	memory.AddMessage("coord-session-1", msg1)
	memory.AddMessage("coord-session-1", msg2)
	memory.AddMessage("coord-session-1", msg3)

	// Sub-agent should only see coordinator + shared (NOT direct)
	parentMessages, err := store.LoadMessagesFromParentSession(ctx, "sub-agent-session-1")
	require.NoError(t, err)
	assert.Len(t, parentMessages, 2, "Should only see coordinator and shared, not direct")

	// Verify the contexts
	assert.Equal(t, types.SessionContextCoordinator, parentMessages[0].SessionContext)
	assert.Equal(t, types.SessionContextShared, parentMessages[1].SessionContext)

	// Verify the content
	assert.Equal(t, "Coordinator context message", parentMessages[0].Content)
	assert.Equal(t, "Shared context message", parentMessages[1].Content)

	// Verify direct context is NOT included
	for _, msg := range parentMessages {
		assert.NotEqual(t, "Direct context message (internal)", msg.Content)
	}
}

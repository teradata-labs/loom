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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// TestSessionStore_CrossSession_AgentSessions tests loading all sessions for an agent.
func TestSessionStore_CrossSession_AgentSessions(t *testing.T) {
	// Create temporary database
	tmpFile, err := os.CreateTemp("", "loom-test-*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSessionStore(tmpFile.Name(), observability.NewNoOpTracer())
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Create sessions with different agents
	coordinatorSession := &Session{
		ID:        "coord-session-1",
		AgentID:   "coordinator",
		Messages:  []Message{},
		Context:   make(map[string]interface{}),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	subAgent1Session := &Session{
		ID:              "sub1-session-1",
		AgentID:         "analyzer-sub-agent",
		ParentSessionID: "coord-session-1",
		Messages:        []Message{},
		Context:         make(map[string]interface{}),
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	subAgent2Session := &Session{
		ID:              "sub1-session-2",
		AgentID:         "analyzer-sub-agent",
		ParentSessionID: "",
		Messages:        []Message{},
		Context:         make(map[string]interface{}),
		CreatedAt:       time.Now().Add(-1 * time.Hour), // Older
		UpdatedAt:       time.Now().Add(-1 * time.Hour),
	}

	otherAgentSession := &Session{
		ID:        "other-session-1",
		AgentID:   "validator-sub-agent",
		Messages:  []Message{},
		Context:   make(map[string]interface{}),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Save all sessions
	require.NoError(t, store.SaveSession(ctx, coordinatorSession))
	require.NoError(t, store.SaveSession(ctx, subAgent1Session))
	require.NoError(t, store.SaveSession(ctx, subAgent2Session))
	require.NoError(t, store.SaveSession(ctx, otherAgentSession))

	// Test: Load sessions for analyzer-sub-agent
	sessionIDs, err := store.LoadAgentSessions(ctx, "analyzer-sub-agent")
	require.NoError(t, err)
	assert.Len(t, sessionIDs, 2)
	// Should be ordered by updated_at DESC
	assert.Equal(t, "sub1-session-1", sessionIDs[0], "Most recent session should be first")
	assert.Equal(t, "sub1-session-2", sessionIDs[1])

	// Test: Load sessions for coordinator
	coordSessions, err := store.LoadAgentSessions(ctx, "coordinator")
	require.NoError(t, err)
	assert.Len(t, coordSessions, 1)
	assert.Equal(t, "coord-session-1", coordSessions[0])

	// Test: Load sessions for non-existent agent
	emptySessions, err := store.LoadAgentSessions(ctx, "non-existent-agent")
	require.NoError(t, err)
	assert.Empty(t, emptySessions)
}

// TestSessionStore_CrossSession_LoadMessagesForAgent tests loading messages across agent sessions.
func TestSessionStore_CrossSession_LoadMessagesForAgent(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "loom-test-*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSessionStore(tmpFile.Name(), observability.NewNoOpTracer())
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Create coordinator session
	coordinatorSession := &Session{
		ID:        "coord-session-1",
		AgentID:   "coordinator",
		Messages:  []Message{},
		Context:   make(map[string]interface{}),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, store.SaveSession(ctx, coordinatorSession))

	// Create sub-agent session linked to coordinator
	subAgentSession := &Session{
		ID:              "sub-agent-session-1",
		AgentID:         "analyzer",
		ParentSessionID: "coord-session-1",
		Messages:        []Message{},
		Context:         make(map[string]interface{}),
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	require.NoError(t, store.SaveSession(ctx, subAgentSession))

	// Add messages to coordinator session (should be visible to sub-agent)
	coordMsg1 := Message{
		Role:           "user",
		Content:        "Analyze this data",
		SessionContext: types.SessionContextCoordinator,
		Timestamp:      time.Now().Add(-3 * time.Second),
	}
	coordMsg2 := Message{
		Role:           "assistant",
		Content:        "Delegating to analyzer",
		SessionContext: types.SessionContextShared,
		Timestamp:      time.Now().Add(-2 * time.Second),
	}
	require.NoError(t, store.SaveMessage(ctx, "coord-session-1", coordMsg1))
	require.NoError(t, store.SaveMessage(ctx, "coord-session-1", coordMsg2))

	// Add messages to sub-agent session
	subMsg1 := Message{
		Role:           "user",
		Content:        "Executing analysis",
		SessionContext: types.SessionContextDirect,
		Timestamp:      time.Now().Add(-1 * time.Second),
	}
	subMsg2 := Message{
		Role:           "assistant",
		Content:        "Analysis complete",
		SessionContext: types.SessionContextShared,
		Timestamp:      time.Now(),
	}
	require.NoError(t, store.SaveMessage(ctx, "sub-agent-session-1", subMsg1))
	require.NoError(t, store.SaveMessage(ctx, "sub-agent-session-1", subMsg2))

	// Test: Load all messages for analyzer (should include coordinator + own messages)
	messages, err := store.LoadMessagesForAgent(ctx, "analyzer")
	require.NoError(t, err)
	assert.Len(t, messages, 4, "Should have 2 coordinator messages + 2 sub-agent messages")

	// Verify messages are ordered by timestamp
	assert.Equal(t, "Analyze this data", messages[0].Content)
	assert.Equal(t, "Delegating to analyzer", messages[1].Content)
	assert.Equal(t, "Executing analysis", messages[2].Content)
	assert.Equal(t, "Analysis complete", messages[3].Content)

	// Verify session contexts are preserved
	assert.Equal(t, types.SessionContextCoordinator, messages[0].SessionContext)
	assert.Equal(t, types.SessionContextShared, messages[1].SessionContext)
	assert.Equal(t, types.SessionContextDirect, messages[2].SessionContext)
	assert.Equal(t, types.SessionContextShared, messages[3].SessionContext)
}

// TestSessionStore_CrossSession_LoadMessagesFromParent tests loading parent session messages.
func TestSessionStore_CrossSession_LoadMessagesFromParent(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "loom-test-*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSessionStore(tmpFile.Name(), observability.NewNoOpTracer())
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Create parent (coordinator) session
	parentSession := &Session{
		ID:        "parent-session",
		AgentID:   "coordinator",
		Messages:  []Message{},
		Context:   make(map[string]interface{}),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, store.SaveSession(ctx, parentSession))

	// Create child (sub-agent) session
	childSession := &Session{
		ID:              "child-session",
		AgentID:         "sub-agent",
		ParentSessionID: "parent-session",
		Messages:        []Message{},
		Context:         make(map[string]interface{}),
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	require.NoError(t, store.SaveSession(ctx, childSession))

	// Add messages to parent session
	parentMsg1 := Message{
		Role:           "user",
		Content:        "Please analyze this",
		SessionContext: types.SessionContextCoordinator,
		Timestamp:      time.Now().Add(-2 * time.Second),
	}
	parentMsg2 := Message{
		Role:           "assistant",
		Content:        "Internal coordinator thought",
		SessionContext: types.SessionContextDirect, // Should NOT be visible to child
		Timestamp:      time.Now().Add(-1 * time.Second),
	}
	parentMsg3 := Message{
		Role:           "assistant",
		Content:        "Delegating to sub-agent",
		SessionContext: types.SessionContextShared,
		Timestamp:      time.Now(),
	}
	require.NoError(t, store.SaveMessage(ctx, "parent-session", parentMsg1))
	require.NoError(t, store.SaveMessage(ctx, "parent-session", parentMsg2))
	require.NoError(t, store.SaveMessage(ctx, "parent-session", parentMsg3))

	// Test: Load parent messages from child session
	parentMessages, err := store.LoadMessagesFromParentSession(ctx, "child-session")
	require.NoError(t, err)
	assert.Len(t, parentMessages, 2, "Should only see coordinator and shared context messages")

	// Verify only coordinator and shared messages are visible
	assert.Equal(t, "Please analyze this", parentMessages[0].Content)
	assert.Equal(t, "Delegating to sub-agent", parentMessages[1].Content)
	assert.Equal(t, types.SessionContextCoordinator, parentMessages[0].SessionContext)
	assert.Equal(t, types.SessionContextShared, parentMessages[1].SessionContext)

	// Test: Session with no parent should return empty
	orphanSession := &Session{
		ID:        "orphan-session",
		AgentID:   "orphan-agent",
		Messages:  []Message{},
		Context:   make(map[string]interface{}),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, store.SaveSession(ctx, orphanSession))

	emptyMessages, err := store.LoadMessagesFromParentSession(ctx, "orphan-session")
	require.NoError(t, err)
	assert.Empty(t, emptyMessages, "Orphan session should have no parent messages")

	// Test: Non-existent session should return empty
	noMessages, err := store.LoadMessagesFromParentSession(ctx, "non-existent")
	require.NoError(t, err)
	assert.Empty(t, noMessages)
}

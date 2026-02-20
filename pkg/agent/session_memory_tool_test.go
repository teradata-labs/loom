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
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// TestSessionMemoryTool_Name verifies tool name.
func TestSessionMemoryTool_Name(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	memory := NewMemoryWithStore(store)
	tool := NewSessionMemoryTool(store, memory)

	assert.Equal(t, "session_memory", tool.Name())
}

// TestSessionMemoryTool_Backend verifies tool is backend-agnostic.
func TestSessionMemoryTool_Backend(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	memory := NewMemoryWithStore(store)
	tool := NewSessionMemoryTool(store, memory)

	assert.Equal(t, "", tool.Backend())
}

// TestSessionMemoryTool_InputSchema verifies schema structure.
func TestSessionMemoryTool_InputSchema(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	memory := NewMemoryWithStore(store)
	tool := NewSessionMemoryTool(store, memory)

	schema := tool.InputSchema()
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)

	// Verify required fields
	require.Contains(t, schema.Required, "action")

	// Verify properties
	require.Contains(t, schema.Properties, "action")
	require.Contains(t, schema.Properties, "agent_id")
	require.Contains(t, schema.Properties, "limit")
	require.Contains(t, schema.Properties, "session_id")
	require.Contains(t, schema.Properties, "snapshot_type")
	require.Contains(t, schema.Properties, "force")
}

// TestSessionMemoryTool_InvalidAction tests invalid action parameter.
func TestSessionMemoryTool_InvalidAction(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	memory := NewMemoryWithStore(store)
	tool := NewSessionMemoryTool(store, memory)

	ctx := context.Background()
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx = context.WithValue(ctx, "session_id", "test-session")
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx = context.WithValue(ctx, "agent_id", "test-agent")

	tests := []struct {
		name   string
		input  map[string]interface{}
		errMsg string
	}{
		{
			name:   "missing action",
			input:  map[string]interface{}{},
			errMsg: "action must be one of: list, summary, compact",
		},
		{
			name: "invalid action",
			input: map[string]interface{}{
				"action": "invalid",
			},
			errMsg: "Unknown action 'invalid'",
		},
		{
			name: "numeric action",
			input: map[string]interface{}{
				"action": 123,
			},
			errMsg: "action must be one of: list, summary, compact",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Execute(ctx, tt.input)
			require.NoError(t, err)
			assert.False(t, result.Success)
			require.NotNil(t, result.Error)
			assert.Contains(t, result.Error.Message, tt.errMsg)
		})
	}
}

// TestSessionMemoryTool_List tests the list action.
func TestSessionMemoryTool_List(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	memory := NewMemoryWithStore(store)
	tool := NewSessionMemoryTool(store, memory)

	// Create test sessions
	agentID := "test-agent"
	ctx := context.Background()
	session1 := memory.GetOrCreateSession(ctx, "session-1")
	session1.AgentID = agentID
	session2 := memory.GetOrCreateSession(ctx, "session-2")
	session2.AgentID = agentID
	session3 := memory.GetOrCreateSession(ctx, "session-3")
	session3.AgentID = "other-agent"

	// Save sessions
	require.NoError(t, store.SaveSession(ctx, session1))
	require.NoError(t, store.SaveSession(ctx, session2))
	require.NoError(t, store.SaveSession(ctx, session3))

	// Add messages to sessions
	require.NoError(t, store.SaveMessage(ctx, "session-1", Message{
		Role:      "user",
		Content:   "Message in session 1",
		Timestamp: time.Now(),
	}))
	require.NoError(t, store.SaveMessage(ctx, "session-2", Message{
		Role:      "user",
		Content:   "Message in session 2",
		Timestamp: time.Now(),
	}))

	// List sessions for test-agent
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx = context.WithValue(ctx, "agent_id", agentID)
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "list",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	// Parse response
	var response map[string]interface{}
	dataStr, ok := result.Data.(string)
	require.True(t, ok, "result.Data should be string")
	err = json.Unmarshal([]byte(dataStr), &response)
	require.NoError(t, err)

	assert.Equal(t, "list", response["action"])
	assert.Equal(t, agentID, response["agent_id"])
	assert.Equal(t, float64(2), response["count"]) // Only 2 sessions for test-agent

	sessions, ok := response["sessions"].([]interface{})
	require.True(t, ok)
	assert.Len(t, sessions, 2)

	// Verify session metadata
	session1Data := sessions[0].(map[string]interface{})
	assert.NotEmpty(t, session1Data["session_id"])
	assert.Equal(t, agentID, session1Data["agent_id"])
	assert.NotEmpty(t, session1Data["created_at"])
	assert.NotEmpty(t, session1Data["updated_at"])
	assert.GreaterOrEqual(t, session1Data["message_count"], float64(0))
}

// TestSessionMemoryTool_List_WithLimit tests list action with limit parameter.
func TestSessionMemoryTool_List_WithLimit(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	memory := NewMemoryWithStore(store)
	tool := NewSessionMemoryTool(store, memory)

	agentID := "test-agent"
	ctx := context.Background()

	// Create 5 sessions
	for i := 0; i < 5; i++ {
		sessionID := fmt.Sprintf("session-%d", i)
		session := memory.GetOrCreateSession(ctx, sessionID)
		session.AgentID = agentID
		require.NoError(t, store.SaveSession(ctx, session))
	}

	// List with limit=2
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx = context.WithValue(ctx, "agent_id", agentID)
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "list",
		"limit":  float64(2),
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	var response map[string]interface{}
	dataStr, ok := result.Data.(string)
	require.True(t, ok, "result.Data should be string")
	err = json.Unmarshal([]byte(dataStr), &response)
	require.NoError(t, err)

	assert.Equal(t, float64(2), response["count"])
	sessions := response["sessions"].([]interface{})
	assert.Len(t, sessions, 2)
}

// TestSessionMemoryTool_Summary tests the summary action.
func TestSessionMemoryTool_Summary(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	memory := NewMemoryWithStore(store)
	tool := NewSessionMemoryTool(store, memory)

	sessionID := "test-session"
	ctx := context.Background()

	// Create session first (required for FOREIGN KEY constraint)
	session := memory.GetOrCreateSession(context.Background(), sessionID)
	session.AgentID = "test-agent"
	require.NoError(t, store.SaveSession(ctx, session))

	// Save L2 memory snapshots
	err := store.SaveMemorySnapshot(ctx, sessionID, "l2_summary", "First summary of conversation", 500)
	require.NoError(t, err)

	time.Sleep(10 * time.Millisecond) // Ensure different timestamps

	err = store.SaveMemorySnapshot(ctx, sessionID, "l2_summary", "Second summary with more context", 750)
	require.NoError(t, err)

	// Get summary
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":     "summary",
		"session_id": sessionID,
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	// Parse response
	var response map[string]interface{}
	dataStr, ok := result.Data.(string)
	require.True(t, ok, "result.Data should be string")
	err = json.Unmarshal([]byte(dataStr), &response)
	require.NoError(t, err)

	assert.Equal(t, "summary", response["action"])
	assert.Equal(t, sessionID, response["session_id"])
	assert.Equal(t, "l2_summary", response["snapshot_type"])
	assert.Equal(t, float64(2), response["count"])

	// Verify latest snapshot
	latest := response["latest"].(map[string]interface{})
	assert.Equal(t, "Second summary with more context", latest["summary"])
	assert.Equal(t, float64(750), latest["token_count"])

	// Verify history
	history, ok := response["history"].([]interface{})
	require.True(t, ok)
	assert.Len(t, history, 1) // Should have 1 historical snapshot
}

// TestSessionMemoryTool_Summary_NoSnapshots tests summary when no snapshots exist.
func TestSessionMemoryTool_Summary_NoSnapshots(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	memory := NewMemoryWithStore(store)
	tool := NewSessionMemoryTool(store, memory)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":     "summary",
		"session_id": "nonexistent-session",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	require.NotNil(t, result.Error)
	assert.Contains(t, result.Error.Message, "No memory snapshots found")
}

// TestSessionMemoryTool_Compact tests the compact action.
func TestSessionMemoryTool_Compact(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	memory := NewMemoryWithStore(store)
	tool := NewSessionMemoryTool(store, memory)

	sessionID := "test-session"
	ctx := context.Background()
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx = context.WithValue(ctx, "session_id", sessionID)

	// Create session with segmented memory
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]
	session := memory.GetOrCreateSession(context.Background(), sessionID)
	segMem := NewSegmentedMemoryWithCompression("Test ROM", 20000, 2000, profile)
	session.SegmentedMem = segMem

	// Add messages to L1
	for i := 0; i < 10; i++ {
		segMem.AddMessage(context.Background(), Message{
			Role:      "user",
			Content:   fmt.Sprintf("Test message %d with enough content to make it substantial", i),
			Timestamp: time.Now(),
		})
	}

	// Verify L1 has messages
	assert.Greater(t, segMem.GetL1MessageCount(), 0)

	// Trigger compaction with force=true
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "compact",
		"force":  true,
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	// Parse response
	var response map[string]interface{}
	dataStr, ok := result.Data.(string)
	require.True(t, ok, "result.Data should be string")
	err = json.Unmarshal([]byte(dataStr), &response)
	require.NoError(t, err)

	assert.Equal(t, "compact", response["action"])
	assert.Equal(t, sessionID, response["session_id"])
	assert.GreaterOrEqual(t, response["messages_compressed"].(float64), float64(0))
	assert.GreaterOrEqual(t, response["tokens_saved"].(float64), float64(0))
}

// TestSessionMemoryTool_Compact_NotNeeded tests compact when not needed.
func TestSessionMemoryTool_Compact_NotNeeded(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	memory := NewMemoryWithStore(store)
	tool := NewSessionMemoryTool(store, memory)

	sessionID := "test-session"
	ctx := context.Background()
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx = context.WithValue(ctx, "session_id", sessionID)

	// Create session with minimal messages
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]
	session := memory.GetOrCreateSession(context.Background(), sessionID)
	segMem := NewSegmentedMemoryWithCompression("Test ROM", 20000, 2000, profile)
	session.SegmentedMem = segMem

	// Add only 2 messages (below minL1Messages=4)
	segMem.AddMessage(context.Background(), Message{
		Role:      "user",
		Content:   "Message 1",
		Timestamp: time.Now(),
	})
	segMem.AddMessage(context.Background(), Message{
		Role:      "assistant",
		Content:   "Response 1",
		Timestamp: time.Now(),
	})

	// Try to compact without force
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "compact",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	require.NotNil(t, result.Error)
	assert.Contains(t, result.Error.Message, "Compaction not needed")
}

// TestSessionMemoryTool_Compact_Force tests forced compaction.
func TestSessionMemoryTool_Compact_Force(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	memory := NewMemoryWithStore(store)
	tool := NewSessionMemoryTool(store, memory)

	sessionID := "test-session"
	ctx := context.Background()
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx = context.WithValue(ctx, "session_id", sessionID)

	// Create session with minimal messages
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]
	session := memory.GetOrCreateSession(context.Background(), sessionID)
	segMem := NewSegmentedMemoryWithCompression("Test ROM", 20000, 2000, profile)
	session.SegmentedMem = segMem

	// Add only 2 messages
	segMem.AddMessage(context.Background(), Message{
		Role:      "user",
		Content:   "Message 1",
		Timestamp: time.Now(),
	})
	segMem.AddMessage(context.Background(), Message{
		Role:      "assistant",
		Content:   "Response 1",
		Timestamp: time.Now(),
	})

	// Force compaction
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "compact",
		"force":  true,
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	var response map[string]interface{}
	dataStr, ok := result.Data.(string)
	require.True(t, ok, "result.Data should be string")
	err = json.Unmarshal([]byte(dataStr), &response)
	require.NoError(t, err)

	assert.Equal(t, "compact", response["action"])
	// Should have compacted the 2 messages
	assert.Equal(t, float64(2), response["messages_compressed"])
}

// TestSessionMemoryTool_MissingContext tests behavior when context values are missing.
func TestSessionMemoryTool_MissingContext(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	memory := NewMemoryWithStore(store)
	tool := NewSessionMemoryTool(store, memory)

	tests := []struct {
		name    string
		ctx     context.Context
		input   map[string]interface{}
		errCode string
	}{
		{
			name: "list without agent_id",
			ctx:  context.Background(),
			input: map[string]interface{}{
				"action": "list",
			},
			errCode: "MISSING_AGENT_ID",
		},
		{
			name: "compact without session_id",
			ctx:  context.Background(),
			input: map[string]interface{}{
				"action": "compact",
			},
			errCode: "MISSING_SESSION_ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Execute(tt.ctx, tt.input)
			require.NoError(t, err)
			assert.False(t, result.Success)
			require.NotNil(t, result.Error)
			assert.Equal(t, tt.errCode, result.Error.Code)
		})
	}
}

// setupTestStore creates a temporary test database.
func setupTestStore(t *testing.T) (*SessionStore, func()) {
	tmpDir := t.TempDir()
	dbPath := fmt.Sprintf("%s/test.db", tmpDir)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(dbPath, tracer)
	require.NoError(t, err)

	cleanup := func() {
		store.Close()
		os.RemoveAll(tmpDir)
	}

	return store, cleanup
}

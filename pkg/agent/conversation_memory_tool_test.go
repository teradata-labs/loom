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
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Note: Using string literals for context keys to match tool implementation.
// The SA1029 linter warning is suppressed as these keys are part of the
// tool's API contract and must match what the tool expects.

// TestConversationMemoryTool_Name verifies tool name.
func TestConversationMemoryTool_Name(t *testing.T) {
	memory := NewMemory()
	tool := NewConversationMemoryTool(memory)

	assert.Equal(t, "conversation_memory", tool.Name())
}

// TestConversationMemoryTool_Backend verifies tool is backend-agnostic.
func TestConversationMemoryTool_Backend(t *testing.T) {
	memory := NewMemory()
	tool := NewConversationMemoryTool(memory)

	assert.Equal(t, "", tool.Backend())
}

// TestConversationMemoryTool_InputSchema verifies schema structure.
func TestConversationMemoryTool_InputSchema(t *testing.T) {
	memory := NewMemory()
	tool := NewConversationMemoryTool(memory)

	schema := tool.InputSchema()
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)

	// Verify required fields
	require.Contains(t, schema.Required, "action")

	// Verify properties
	require.Contains(t, schema.Properties, "action")
	require.Contains(t, schema.Properties, "offset")
	require.Contains(t, schema.Properties, "limit")
	require.Contains(t, schema.Properties, "query")
	require.Contains(t, schema.Properties, "session_scope")
}

// TestConversationMemoryTool_InvalidAction tests invalid action parameter.
func TestConversationMemoryTool_InvalidAction(t *testing.T) {
	memory := NewMemory()
	tool := NewConversationMemoryTool(memory)

	ctx := context.Background()
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx = context.WithValue(ctx, "session_id", "test-session")

	tests := []struct {
		name   string
		input  map[string]interface{}
		errMsg string
	}{
		{
			name:   "missing action",
			input:  map[string]interface{}{},
			errMsg: "action must be one of: recall, search, clear",
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
			errMsg: "action must be one of: recall, search, clear",
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

// TestConversationMemoryTool_RecallAction tests recall action.
func TestConversationMemoryTool_RecallAction(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer func() { _ = os.Remove(tmpDB) }()

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	// Create memory with store
	memory := NewMemory()
	memory.store = store
	memory.SetTracer(tracer)

	// Create session with swap enabled
	sessionID := "recall-test-session"
	session := memory.GetOrCreateSession(context.Background(), sessionID)

	// Get segmented memory
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	require.True(t, segMem.IsSwapEnabled())

	// Add messages and persist to swap
	for i := 0; i < 20; i++ {
		msg := Message{
			Role:      "user",
			Content:   fmt.Sprintf("Message %d for recall test", i),
			Timestamp: time.Now(),
		}
		segMem.AddMessage(context.Background(), msg)
		err := store.SaveMessage(context.Background(), sessionID, msg)
		require.NoError(t, err)
	}

	// Force compression to trigger swap
	segMem.SetMaxL2Tokens(100)
	_, _ = segMem.CompactMemory(context.Background())

	// Verify swap occurred
	evictions, _ := segMem.GetSwapStats()
	require.Greater(t, evictions, 0, "Swap should have occurred")

	// Create tool and test recall
	tool := NewConversationMemoryTool(memory)

	ctx := context.Background()
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx = context.WithValue(ctx, "session_id", sessionID)

	// Test recall with valid parameters
	input := map[string]interface{}{
		"action": "recall",
		"offset": float64(0),
		"limit":  float64(5),
	}

	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	assert.True(t, result.Success, "Recall should succeed")

	// Parse result
	var resultData map[string]interface{}
	dataStr, ok := result.Data.(string)
	require.True(t, ok, "result.Data should be string")
	err = json.Unmarshal([]byte(dataStr), &resultData)
	require.NoError(t, err)

	assert.Equal(t, "recall", resultData["action"])
	assert.True(t, resultData["success"].(bool))
	assert.Equal(t, float64(5), resultData["count"].(float64))

	// Verify messages are in result
	messages, ok := resultData["messages"].([]interface{})
	require.True(t, ok)
	assert.Equal(t, 5, len(messages))
}

// TestConversationMemoryTool_RecallErrors tests recall error cases.
func TestConversationMemoryTool_RecallErrors(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer func() { _ = os.Remove(tmpDB) }()

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	memory := NewMemory()
	memory.store = store
	memory.SetTracer(tracer)

	// Create a test session for parameter validation tests
	sessionID := "test-session"
	memory.GetOrCreateSession(context.Background(), sessionID)

	tool := NewConversationMemoryTool(memory)

	tests := []struct {
		name        string
		sessionID   string
		input       map[string]interface{}
		errCode     string
		errContains string
	}{
		{
			name:      "missing session_id",
			sessionID: "",
			input: map[string]interface{}{
				"action": "recall",
				"offset": float64(0),
			},
			errCode:     "MISSING_SESSION_ID",
			errContains: "Session ID not found",
		},
		{
			name:      "missing offset",
			sessionID: sessionID,
			input: map[string]interface{}{
				"action": "recall",
			},
			errCode:     "INVALID_PARAMETER",
			errContains: "offset must be an integer",
		},
		{
			name:      "invalid offset type",
			sessionID: sessionID,
			input: map[string]interface{}{
				"action": "recall",
				"offset": "not-a-number",
			},
			errCode:     "INVALID_PARAMETER",
			errContains: "offset must be an integer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.sessionID != "" {
				//nolint:staticcheck // SA1029: using string key to match tool API contract
				ctx = context.WithValue(ctx, "session_id", tt.sessionID)
			}

			result, err := tool.Execute(ctx, tt.input)
			require.NoError(t, err)
			assert.False(t, result.Success)
			require.NotNil(t, result.Error)
			assert.Equal(t, tt.errCode, result.Error.Code)
			assert.Contains(t, result.Error.Message, tt.errContains)
		})
	}
}

// TestConversationMemoryTool_RecallLimits tests limit enforcement.
func TestConversationMemoryTool_RecallLimits(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer func() { _ = os.Remove(tmpDB) }()

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	// Create memory with store
	memory := NewMemory()
	memory.store = store
	memory.SetTracer(tracer)

	// Create session
	sessionID := "limit-test-session"
	session := memory.GetOrCreateSession(context.Background(), sessionID)

	// Add many messages
	segMem, _ := session.SegmentedMem.(*SegmentedMemory)
	for i := 0; i < 100; i++ {
		msg := Message{
			Role:      "user",
			Content:   fmt.Sprintf("Message %d", i),
			Timestamp: time.Now(),
		}
		segMem.AddMessage(context.Background(), msg)
		err := store.SaveMessage(context.Background(), sessionID, msg)
		require.NoError(t, err)
	}

	// Force swap
	segMem.SetMaxL2Tokens(100)
	segMem.CompactMemory(context.Background())

	tool := NewConversationMemoryTool(memory)
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx := context.WithValue(context.Background(), "session_id", sessionID)

	// Test with limit > 50 (should be capped at 50)
	input := map[string]interface{}{
		"action": "recall",
		"offset": float64(0),
		"limit":  float64(100),
	}

	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	assert.True(t, result.Success)

	var resultData map[string]interface{}
	dataStr, _ := result.Data.(string)
	err = json.Unmarshal([]byte(dataStr), &resultData)
	require.NoError(t, err)

	// Should be capped at 50
	assert.LessOrEqual(t, int(resultData["count"].(float64)), 50)
}

// TestConversationMemoryTool_SearchAction tests search action with current scope.
func TestConversationMemoryTool_SearchAction(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer func() { _ = os.Remove(tmpDB) }()

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	// Create memory with store
	memory := NewMemory()
	memory.store = store
	memory.SetTracer(tracer)

	// Create session
	sessionID := "search-test-session"
	session := memory.GetOrCreateSession(context.Background(), sessionID)

	// Add messages with searchable content
	segMem, _ := session.SegmentedMem.(*SegmentedMemory)
	messages := []string{
		"Let's discuss SQL performance optimization",
		"The database query is running slow",
		"We need to add indexes to improve performance",
		"Can you help with Python code review?",
		"The API endpoint needs authentication",
	}

	for _, content := range messages {
		msg := Message{
			Role:      "user",
			Content:   content,
			Timestamp: time.Now(),
		}
		segMem.AddMessage(context.Background(), msg)
		err := store.SaveMessage(context.Background(), sessionID, msg)
		require.NoError(t, err)
	}

	// Force swap
	segMem.SetMaxL2Tokens(50)
	segMem.CompactMemory(context.Background())

	tool := NewConversationMemoryTool(memory)
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx := context.WithValue(context.Background(), "session_id", sessionID)

	// Search for SQL-related messages
	input := map[string]interface{}{
		"action":        "search",
		"query":         "SQL database performance",
		"session_scope": "current",
		"limit":         float64(5),
	}

	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	assert.True(t, result.Success)

	var resultData map[string]interface{}
	dataStr, _ := result.Data.(string)
	err = json.Unmarshal([]byte(dataStr), &resultData)
	require.NoError(t, err)

	assert.Equal(t, "search", resultData["action"])
	assert.Equal(t, "SQL database performance", resultData["query"])
	assert.Equal(t, "current", resultData["session_scope"])
	assert.Greater(t, int(resultData["count"].(float64)), 0, "Should find relevant messages")
}

// TestConversationMemoryTool_SearchErrors tests search error cases.
func TestConversationMemoryTool_SearchErrors(t *testing.T) {
	memory := NewMemory()
	tool := NewConversationMemoryTool(memory)

	tests := []struct {
		name        string
		sessionID   string
		input       map[string]interface{}
		errCode     string
		errContains string
	}{
		{
			name:      "missing query",
			sessionID: "test-session",
			input: map[string]interface{}{
				"action": "search",
			},
			errCode:     "INVALID_PARAMETER",
			errContains: "query must be a non-empty string",
		},
		{
			name:      "empty query",
			sessionID: "test-session",
			input: map[string]interface{}{
				"action": "search",
				"query":  "",
			},
			errCode:     "INVALID_PARAMETER",
			errContains: "query must be a non-empty string",
		},
		{
			name:      "invalid session scope",
			sessionID: "test-session",
			input: map[string]interface{}{
				"action":        "search",
				"query":         "test query",
				"session_scope": "invalid",
			},
			errCode:     "INVALID_PARAMETER",
			errContains: "session_scope 'invalid' invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.sessionID != "" {
				//nolint:staticcheck // SA1029: using string key to match tool API contract
				ctx = context.WithValue(ctx, "session_id", tt.sessionID)
			}

			result, err := tool.Execute(ctx, tt.input)
			require.NoError(t, err)
			assert.False(t, result.Success)
			require.NotNil(t, result.Error)
			assert.Equal(t, tt.errCode, result.Error.Code)
			assert.Contains(t, result.Error.Message, tt.errContains)
		})
	}
}

// TestConversationMemoryTool_SearchAgentScope tests agent-scoped search.
func TestConversationMemoryTool_SearchAgentScope(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer func() { _ = os.Remove(tmpDB) }()

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	// Create memory with store
	memory := NewMemory()
	memory.store = store
	memory.SetTracer(tracer)

	agentID := "test-agent"

	// Create multiple sessions for the same agent
	for sessionNum := 0; sessionNum < 3; sessionNum++ {
		sessionID := fmt.Sprintf("agent-session-%d", sessionNum)
		session := memory.GetOrCreateSessionWithAgent(context.Background(), sessionID, agentID, "")

		// Add messages to each session
		segMem, _ := session.SegmentedMem.(*SegmentedMemory)
		for i := 0; i < 5; i++ {
			msg := Message{
				Role:      "user",
				Content:   fmt.Sprintf("Session %d: SQL optimization message %d", sessionNum, i),
				Timestamp: time.Now(),
			}
			segMem.AddMessage(context.Background(), msg)
			err := store.SaveMessage(context.Background(), sessionID, msg)
			require.NoError(t, err)
		}
	}

	tool := NewConversationMemoryTool(memory)
	ctx := context.Background()
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx = context.WithValue(ctx, "session_id", "agent-session-0")
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx = context.WithValue(ctx, "agent_id", agentID)

	// Search across all agent sessions
	input := map[string]interface{}{
		"action":        "search",
		"query":         "SQL optimization",
		"session_scope": "agent",
		"limit":         float64(10),
	}

	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	assert.True(t, result.Success)

	var resultData map[string]interface{}
	dataStr, _ := result.Data.(string)
	err = json.Unmarshal([]byte(dataStr), &resultData)
	require.NoError(t, err)

	assert.Equal(t, "agent", resultData["session_scope"])
	// Should find messages from multiple sessions
	assert.Greater(t, int(resultData["count"].(float64)), 0)
}

// TestConversationMemoryTool_SearchAllScope tests all-sessions search.
func TestConversationMemoryTool_SearchAllScope(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer func() { _ = os.Remove(tmpDB) }()

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	// Create memory with store
	memory := NewMemory()
	memory.store = store
	memory.SetTracer(tracer)

	// Create sessions for different agents
	agents := []string{"agent-1", "agent-2", "agent-3"}
	for _, agentID := range agents {
		sessionID := fmt.Sprintf("session-%s", agentID)
		session := memory.GetOrCreateSessionWithAgent(context.Background(), sessionID, agentID, "")

		segMem, _ := session.SegmentedMem.(*SegmentedMemory)
		msg := Message{
			Role:      "user",
			Content:   fmt.Sprintf("Performance optimization topic from %s", agentID),
			Timestamp: time.Now(),
		}
		segMem.AddMessage(context.Background(), msg)
		err := store.SaveMessage(context.Background(), sessionID, msg)
		require.NoError(t, err)
	}

	tool := NewConversationMemoryTool(memory)
	ctx := context.Background()
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx = context.WithValue(ctx, "session_id", "session-agent-1")

	// Search across ALL sessions
	input := map[string]interface{}{
		"action":        "search",
		"query":         "performance optimization",
		"session_scope": "all",
		"limit":         float64(10),
	}

	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	assert.True(t, result.Success)

	var resultData map[string]interface{}
	dataStr, _ := result.Data.(string)
	err = json.Unmarshal([]byte(dataStr), &resultData)
	require.NoError(t, err)

	assert.Equal(t, "all", resultData["session_scope"])
	// Should find messages from all agents
	assert.Greater(t, int(resultData["count"].(float64)), 0)
}

// TestConversationMemoryTool_ClearAction tests clear action.
func TestConversationMemoryTool_ClearAction(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer func() { _ = os.Remove(tmpDB) }()

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	// Create memory with store
	memory := NewMemory()
	memory.store = store
	memory.SetTracer(tracer)

	// Create session
	sessionID := "clear-test-session"
	session := memory.GetOrCreateSession(context.Background(), sessionID)

	// Add and recall messages
	segMem, _ := session.SegmentedMem.(*SegmentedMemory)
	for i := 0; i < 10; i++ {
		msg := Message{
			Role:      "user",
			Content:   fmt.Sprintf("Message %d", i),
			Timestamp: time.Now(),
		}
		segMem.AddMessage(context.Background(), msg)
		err := store.SaveMessage(context.Background(), sessionID, msg)
		require.NoError(t, err)
	}

	// Force swap
	segMem.SetMaxL2Tokens(50)
	segMem.CompactMemory(context.Background())

	// Recall messages to promote them
	recalled, err := segMem.RetrieveMessagesFromSwap(context.Background(), 0, 5)
	require.NoError(t, err)
	err = segMem.PromoteMessagesToContext(recalled)
	require.NoError(t, err)

	// Verify promoted messages exist
	promoted := segMem.GetPromotedContext()
	assert.Greater(t, len(promoted), 0, "Should have promoted messages")

	// Create tool and clear
	tool := NewConversationMemoryTool(memory)
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx := context.WithValue(context.Background(), "session_id", sessionID)

	input := map[string]interface{}{
		"action": "clear",
	}

	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	assert.True(t, result.Success)

	var resultData map[string]interface{}
	dataStr, _ := result.Data.(string)
	err = json.Unmarshal([]byte(dataStr), &resultData)
	require.NoError(t, err)

	assert.Equal(t, "clear", resultData["action"])
	assert.True(t, resultData["success"].(bool))
	assert.Greater(t, int(resultData["cleared_count"].(float64)), 0)

	// Verify promoted messages are cleared
	promotedAfter := segMem.GetPromotedContext()
	assert.Equal(t, 0, len(promotedAfter), "Promoted context should be empty")
}

// TestConversationMemoryTool_ClearErrors tests clear error cases.
func TestConversationMemoryTool_ClearErrors(t *testing.T) {
	memory := NewMemory()
	tool := NewConversationMemoryTool(memory)

	tests := []struct {
		name        string
		sessionID   string
		errCode     string
		errContains string
	}{
		{
			name:        "missing session_id",
			sessionID:   "",
			errCode:     "MISSING_SESSION_ID",
			errContains: "Session ID not found",
		},
		{
			name:        "non-existent session",
			sessionID:   "non-existent-session",
			errCode:     "SESSION_NOT_FOUND",
			errContains: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.sessionID != "" {
				//nolint:staticcheck // SA1029: using string key to match tool API contract
				ctx = context.WithValue(ctx, "session_id", tt.sessionID)
			}

			input := map[string]interface{}{
				"action": "clear",
			}

			result, err := tool.Execute(ctx, input)
			require.NoError(t, err)
			assert.False(t, result.Success)
			require.NotNil(t, result.Error)
			assert.Equal(t, tt.errCode, result.Error.Code)
			assert.Contains(t, result.Error.Message, tt.errContains)
		})
	}
}

// TestConversationMemoryTool_SwapNotEnabled tests behavior when swap is not enabled.
func TestConversationMemoryTool_SwapNotEnabled(t *testing.T) {
	// Create memory WITHOUT session store (swap disabled)
	memory := NewMemory()

	sessionID := "no-swap-session"
	session := memory.GetOrCreateSession(context.Background(), sessionID)

	// Verify swap is disabled
	segMem, _ := session.SegmentedMem.(*SegmentedMemory)
	assert.False(t, segMem.IsSwapEnabled(), "Swap should be disabled")

	tool := NewConversationMemoryTool(memory)
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx := context.WithValue(context.Background(), "session_id", sessionID)

	// Try recall - should fail gracefully
	input := map[string]interface{}{
		"action": "recall",
		"offset": float64(0),
		"limit":  float64(10),
	}

	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "SWAP_NOT_ENABLED", result.Error.Code)
	assert.Contains(t, result.Error.Message, "Long-term storage is not enabled")
}

// TestConversationMemoryTool_SessionNotFound tests non-existent session.
func TestConversationMemoryTool_SessionNotFound(t *testing.T) {
	memory := NewMemory()
	tool := NewConversationMemoryTool(memory)

	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx := context.WithValue(context.Background(), "session_id", "non-existent")

	input := map[string]interface{}{
		"action": "recall",
		"offset": float64(0),
		"limit":  float64(10),
	}

	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "SESSION_NOT_FOUND", result.Error.Code)
}

// TestConversationMemoryTool_Integration tests end-to-end workflow.
func TestConversationMemoryTool_Integration(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer func() { _ = os.Remove(tmpDB) }()

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	// Create memory with store
	memory := NewMemory()
	memory.store = store
	memory.SetTracer(tracer)

	// Create session
	sessionID := "integration-session"
	session := memory.GetOrCreateSession(context.Background(), sessionID)

	// Add messages
	segMem, _ := session.SegmentedMem.(*SegmentedMemory)
	for i := 0; i < 30; i++ {
		msg := Message{
			Role:      "user",
			Content:   fmt.Sprintf("Integration test message %d", i),
			Timestamp: time.Now(),
		}
		segMem.AddMessage(context.Background(), msg)
		err := store.SaveMessage(context.Background(), sessionID, msg)
		require.NoError(t, err)
	}

	// Force swap
	segMem.SetMaxL2Tokens(100)
	segMem.CompactMemory(context.Background())

	tool := NewConversationMemoryTool(memory)
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx := context.WithValue(context.Background(), "session_id", sessionID)

	// 1. Recall old messages
	recallInput := map[string]interface{}{
		"action": "recall",
		"offset": float64(0),
		"limit":  float64(5),
	}

	result, err := tool.Execute(ctx, recallInput)
	require.NoError(t, err)
	assert.True(t, result.Success)

	// 2. Search for messages
	searchInput := map[string]interface{}{
		"action":        "search",
		"query":         "integration test",
		"session_scope": "current",
		"limit":         float64(5),
	}

	result, err = tool.Execute(ctx, searchInput)
	require.NoError(t, err)
	assert.True(t, result.Success)

	// 3. Clear recalled context
	clearInput := map[string]interface{}{
		"action": "clear",
	}

	result, err = tool.Execute(ctx, clearInput)
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Verify promoted context is empty
	promoted := segMem.GetPromotedContext()
	assert.Equal(t, 0, len(promoted))
}

// TestConversationMemoryTool_AsShuttleTool tests integration with shuttle.Tool wrapper.
func TestConversationMemoryTool_AsShuttleTool(t *testing.T) {
	memory := NewMemory()
	conversationMemoryTool := NewConversationMemoryTool(memory)

	// Wrap with shuttle.Tool
	shuttleTool := shuttle.Tool(conversationMemoryTool)

	assert.Equal(t, "conversation_memory", shuttleTool.Name())
	assert.NotNil(t, shuttleTool.InputSchema())
}

// TestConversationMemoryTool_SearchLimitEnforcement tests search limit is capped at 20.
func TestConversationMemoryTool_SearchLimitEnforcement(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer func() { _ = os.Remove(tmpDB) }()

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	memory := NewMemory()
	memory.store = store
	memory.SetTracer(tracer)

	sessionID := "search-limit-session"
	session := memory.GetOrCreateSession(context.Background(), sessionID)

	// Add many messages
	segMem, _ := session.SegmentedMem.(*SegmentedMemory)
	for i := 0; i < 50; i++ {
		msg := Message{
			Role:      "user",
			Content:   fmt.Sprintf("Searchable message %d", i),
			Timestamp: time.Now(),
		}
		segMem.AddMessage(context.Background(), msg)
		err := store.SaveMessage(context.Background(), sessionID, msg)
		require.NoError(t, err)
	}

	// Force swap
	segMem.SetMaxL2Tokens(50)
	segMem.CompactMemory(context.Background())

	tool := NewConversationMemoryTool(memory)
	//nolint:staticcheck // SA1029: using string key to match tool API contract
	ctx := context.WithValue(context.Background(), "session_id", sessionID)

	// Request more than 20 results
	input := map[string]interface{}{
		"action":        "search",
		"query":         "searchable",
		"session_scope": "current",
		"limit":         float64(100), // Should be capped at 20
	}

	result, err := tool.Execute(ctx, input)
	require.NoError(t, err)
	assert.True(t, result.Success)

	var resultData map[string]interface{}
	dataStr, _ := result.Data.(string)
	err = json.Unmarshal([]byte(dataStr), &resultData)
	require.NoError(t, err)

	// Count should not exceed 20
	assert.LessOrEqual(t, int(resultData["count"].(float64)), 20)
}

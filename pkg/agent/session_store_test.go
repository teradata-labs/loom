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
	"testing"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

func TestNewSessionStore(t *testing.T) {
	tmpfile := t.TempDir() + "/test.db"
	defer os.Remove(tmpfile)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpfile, tracer)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	defer store.Close()

	if store == nil {
		t.Fatal("Expected non-nil store")
	}

	if store.db == nil {
		t.Error("Expected database to be initialized")
	}
}

func TestSessionStore_SaveAndLoad(t *testing.T) {
	tmpfile := t.TempDir() + "/test.db"
	defer os.Remove(tmpfile)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpfile, tracer)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create and save session
	session := &Session{
		ID:           "test-session",
		Messages:     []Message{{Role: "user", Content: "hello"}},
		Context:      map[string]interface{}{"key": "value"},
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		TotalCostUSD: 0.05,
		TotalTokens:  100,
	}

	err = store.SaveSession(ctx, session)
	if err != nil {
		t.Fatalf("Expected no error saving, got %v", err)
	}

	// Load session
	loaded, err := store.LoadSession(ctx, "test-session")
	if err != nil {
		t.Fatalf("Expected no error loading, got %v", err)
	}

	if loaded.ID != "test-session" {
		t.Errorf("Expected ID 'test-session', got %s", loaded.ID)
	}

	if loaded.TotalCostUSD != 0.05 {
		t.Errorf("Expected cost 0.05, got %f", loaded.TotalCostUSD)
	}

	if loaded.TotalTokens != 100 {
		t.Errorf("Expected 100 tokens, got %d", loaded.TotalTokens)
	}

	if val, ok := loaded.Context["key"].(string); !ok || val != "value" {
		t.Error("Expected context to be preserved")
	}
}

func TestSessionStore_SaveMessage(t *testing.T) {
	tmpfile := t.TempDir() + "/test.db"
	defer os.Remove(tmpfile)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpfile, tracer)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create session first
	session := &Session{
		ID:        "test-session",
		Messages:  []Message{},
		Context:   make(map[string]interface{}),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_ = store.SaveSession(ctx, session)

	// Save message
	msg := Message{
		Role:       "user",
		Content:    "test message",
		Timestamp:  time.Now(),
		TokenCount: 10,
		CostUSD:    0.001,
	}

	err = store.SaveMessage(ctx, "test-session", msg)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	// Load messages
	messages, err := store.LoadMessages(ctx, "test-session")
	if err != nil {
		t.Fatalf("Expected no error loading messages, got %v", err)
	}

	if len(messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(messages))
	}

	if messages[0].Content != "test message" {
		t.Errorf("Expected content 'test message', got %s", messages[0].Content)
	}

	if messages[0].TokenCount != 10 {
		t.Errorf("Expected 10 tokens, got %d", messages[0].TokenCount)
	}
}

func TestSessionStore_SaveToolExecution(t *testing.T) {
	tmpfile := t.TempDir() + "/test.db"
	defer os.Remove(tmpfile)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpfile, tracer)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create session first
	session := &Session{
		ID:        "test-session",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Context:   make(map[string]interface{}),
	}
	_ = store.SaveSession(ctx, session)

	// Save tool execution
	exec := ToolExecution{
		ToolName: "execute_sql",
		Input:    map[string]interface{}{"query": "SELECT 1"},
		Result:   nil,
		Error:    nil,
	}

	err = store.SaveToolExecution(ctx, "test-session", exec)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
}

func TestSessionStore_SaveToolExecution_MCPFailures(t *testing.T) {
	tmpfile := t.TempDir() + "/test.db"
	defer os.Remove(tmpfile)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpfile, tracer)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create session first
	session := &Session{
		ID:        "test-session",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Context:   make(map[string]interface{}),
	}
	_ = store.SaveSession(ctx, session)

	tests := []struct {
		name        string
		exec        ToolExecution
		expectError bool
		errorMsg    string
	}{
		{
			name: "Go error should be recorded",
			exec: ToolExecution{
				ToolName: "test_tool",
				Input:    map[string]interface{}{"query": "SELECT 1"},
				Result:   nil,
				Error:    &testError{msg: "connection failed"},
			},
			expectError: true,
			errorMsg:    "connection failed",
		},
		{
			name: "MCP tool failure should be recorded",
			exec: ToolExecution{
				ToolName: "teradata_sample_table",
				Input:    map[string]interface{}{"limit": "10"},
				Result: &shuttle.Result{
					Success: false,
					Error: &shuttle.Error{
						Code:    "MCP_CALL_FAILED",
						Message: "invalid arguments: [limit: Invalid type. Expected: integer, given: string]",
					},
				},
				Error: nil, // No Go error, but Result.Success = false
			},
			expectError: true,
			errorMsg:    "MCP_CALL_FAILED: invalid arguments",
		},
		{
			name: "Successful tool should have no error",
			exec: ToolExecution{
				ToolName: "teradata_list_databases",
				Input:    map[string]interface{}{},
				Result: &shuttle.Result{
					Success: true,
					Data:    "databases retrieved",
				},
				Error: nil,
			},
			expectError: false,
			errorMsg:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.SaveToolExecution(ctx, "test-session", tt.exec)
			if err != nil {
				t.Fatalf("Expected no error saving, got %v", err)
			}

			// Query database to verify error column
			query := `SELECT error FROM tool_executions WHERE tool_name = ? ORDER BY timestamp DESC LIMIT 1`
			row := store.db.QueryRowContext(ctx, query, tt.exec.ToolName)

			var errorMsg *string
			if err := row.Scan(&errorMsg); err != nil {
				t.Fatalf("Failed to query tool execution: %v", err)
			}

			if tt.expectError {
				if errorMsg == nil {
					t.Errorf("Expected error to be recorded, but error column is NULL")
				} else if tt.errorMsg != "" && len(*errorMsg) > 0 {
					// Check error message contains expected substring
					if !contains(*errorMsg, tt.errorMsg) {
						t.Errorf("Expected error to contain '%s', got '%s'", tt.errorMsg, *errorMsg)
					}
				}
			} else {
				if errorMsg != nil {
					t.Errorf("Expected no error, but got: %s", *errorMsg)
				}
			}
		})
	}
}

// Helper types for testing
type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestSessionStore_DeleteSession(t *testing.T) {
	tmpfile := t.TempDir() + "/test.db"
	defer os.Remove(tmpfile)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpfile, tracer)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create session
	session := &Session{
		ID:        "test-session",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Context:   make(map[string]interface{}),
	}
	_ = store.SaveSession(ctx, session)

	// Verify it exists
	_, err = store.LoadSession(ctx, "test-session")
	if err != nil {
		t.Fatal("Expected session to exist")
	}

	// Delete it
	err = store.DeleteSession(ctx, "test-session")
	if err != nil {
		t.Fatalf("Expected no error deleting, got %v", err)
	}

	// Verify it's gone
	_, err = store.LoadSession(ctx, "test-session")
	if err == nil {
		t.Error("Expected error loading deleted session")
	}
}

func TestSessionStore_ListSessions(t *testing.T) {
	tmpfile := t.TempDir() + "/test.db"
	defer os.Remove(tmpfile)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpfile, tracer)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create multiple sessions
	for i := 0; i < 3; i++ {
		session := &Session{
			ID:        "session-" + string(rune('A'+i)),
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Context:   make(map[string]interface{}),
		}
		_ = store.SaveSession(ctx, session)
	}

	sessions, err := store.ListSessions(ctx)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if len(sessions) != 3 {
		t.Errorf("Expected 3 sessions, got %d", len(sessions))
	}
}

func TestSessionStore_GetStats(t *testing.T) {
	tmpfile := t.TempDir() + "/test.db"
	defer os.Remove(tmpfile)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpfile, tracer)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create session with messages and tool executions
	session := &Session{
		ID:           "test-session",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		Context:      make(map[string]interface{}),
		TotalCostUSD: 0.10,
		TotalTokens:  200,
	}
	_ = store.SaveSession(ctx, session)

	msg := Message{
		Role:      "user",
		Content:   "test",
		Timestamp: time.Now(),
	}
	_ = store.SaveMessage(ctx, "test-session", msg)

	exec := ToolExecution{
		ToolName: "test_tool",
		Input:    map[string]interface{}{},
	}
	_ = store.SaveToolExecution(ctx, "test-session", exec)

	// Get stats
	stats, err := store.GetStats(ctx)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if stats.SessionCount != 1 {
		t.Errorf("Expected 1 session, got %d", stats.SessionCount)
	}

	if stats.MessageCount != 1 {
		t.Errorf("Expected 1 message, got %d", stats.MessageCount)
	}

	if stats.ToolExecutionCount != 1 {
		t.Errorf("Expected 1 tool execution, got %d", stats.ToolExecutionCount)
	}

	if stats.TotalCostUSD != 0.10 {
		t.Errorf("Expected cost 0.10, got %f", stats.TotalCostUSD)
	}

	if stats.TotalTokens != 200 {
		t.Errorf("Expected 200 tokens, got %d", stats.TotalTokens)
	}
}

func TestSessionStore_ConcurrentWrites(t *testing.T) {
	tmpfile := t.TempDir() + "/test.db"
	defer os.Remove(tmpfile)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpfile, tracer)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create initial session
	session := &Session{
		ID:        "concurrent-session",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Context:   make(map[string]interface{}),
	}
	_ = store.SaveSession(ctx, session)

	// Write messages concurrently
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			msg := Message{
				Role:      "user",
				Content:   "concurrent message",
				Timestamp: time.Now(),
			}
			_ = store.SaveMessage(ctx, "concurrent-session", msg)
		}(i)
	}

	wg.Wait()

	// Verify all messages were saved
	messages, err := store.LoadMessages(ctx, "concurrent-session")
	if err != nil {
		t.Fatalf("Expected no error loading messages, got %v", err)
	}

	if len(messages) != 50 {
		t.Errorf("Expected 50 messages, got %d", len(messages))
	}
}

func TestMemory_WithStore_Integration(t *testing.T) {
	tmpfile := t.TempDir() + "/test.db"
	defer os.Remove(tmpfile)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpfile, tracer)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	defer store.Close()

	mem := NewMemoryWithStore(store)

	// Create session
	session := mem.GetOrCreateSession("persistent-session")
	session.AddMessage(Message{Role: "user", Content: "test"})

	// Persist message
	ctx := context.Background()
	err = mem.PersistMessage(ctx, "persistent-session", session.Messages[0])
	if err != nil {
		t.Fatalf("Expected no error persisting message, got %v", err)
	}

	// Clear memory cache
	mem.ClearAll()

	// Session should be reloaded from store
	reloaded := mem.GetOrCreateSession("persistent-session")
	if len(reloaded.Messages) != 1 {
		t.Errorf("Expected 1 message to be reloaded, got %d", len(reloaded.Messages))
	}

	if reloaded.Messages[0].Content != "test" {
		t.Error("Expected message content to be preserved")
	}
}

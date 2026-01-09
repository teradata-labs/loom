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
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
)

// TestSwapIntegration_OptOut tests that swap is automatically enabled when SessionStore is configured.
func TestSwapIntegration_OptOut(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	// Create memory with store
	memory := NewMemory()
	memory.store = store
	memory.SetTracer(tracer)

	// Create session - swap should be automatically enabled
	session := memory.GetOrCreateSession("test-session")

	// Verify swap is enabled
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok, "SegmentedMem should be *SegmentedMemory")
	assert.True(t, segMem.IsSwapEnabled(), "Swap should be automatically enabled when store is configured")
}

// TestSwapIntegration_EndToEnd tests complete swap workflow with agent.
func TestSwapIntegration_EndToEnd(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	// Create memory with store
	memory := NewMemory()
	memory.store = store
	memory.SetTracer(tracer)

	// Create session
	sessionID := "end-to-end-session"
	session := memory.GetOrCreateSession(sessionID)

	// Get segmented memory
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	require.True(t, segMem.IsSwapEnabled())

	// Configure small L2 limit to trigger eviction
	segMem.SetMaxL2Tokens(100)

	// Add many messages to trigger L2 eviction to swap
	for i := 0; i < 30; i++ {
		msg := Message{
			Role:      "user",
			Content:   "This is a test message that will eventually be compressed and evicted to swap storage for long-term persistence.",
			Timestamp: time.Now(),
		}
		segMem.AddMessage(msg)

		// Persist message to database
		err := store.SaveMessage(context.Background(), sessionID, msg)
		require.NoError(t, err)
	}

	// Verify L2 eviction occurred
	evictions, _ := segMem.GetSwapStats()
	assert.Greater(t, evictions, 0, "L2 should have been evicted to swap")

	// Verify L2 is bounded
	l2Tokens := segMem.tokenCounter.CountTokens(segMem.GetL2Summary())
	assert.LessOrEqual(t, l2Tokens, segMem.maxL2Tokens, "L2 should not exceed maxL2Tokens after eviction")

	// Retrieve old messages from swap
	ctx := context.Background()
	oldMessages, err := segMem.RetrieveMessagesFromSwap(ctx, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, 10, len(oldMessages), "Should retrieve 10 old messages")

	// Verify messages are in chronological order
	for i := 1; i < len(oldMessages); i++ {
		assert.True(t, oldMessages[i].Timestamp.After(oldMessages[i-1].Timestamp) ||
			oldMessages[i].Timestamp.Equal(oldMessages[i-1].Timestamp),
			"Messages should be in chronological order")
	}

	// Promote messages to context
	tokensBefore := segMem.GetTokenCount()
	err = segMem.PromoteMessagesToContext(oldMessages)
	require.NoError(t, err)

	tokensAfter := segMem.GetTokenCount()
	assert.Greater(t, tokensAfter, tokensBefore, "Token count should increase after promotion")

	// Verify promoted messages appear in LLM context
	llmMessages := segMem.GetMessagesForLLM()
	foundPromoted := false
	for _, msg := range llmMessages {
		if msg.Role == "user" && msg.Content == oldMessages[0].Content {
			foundPromoted = true
			break
		}
	}
	assert.True(t, foundPromoted, "Promoted messages should appear in LLM context")

	// Clear promoted context
	segMem.ClearPromotedContext()
	tokensCleared := segMem.GetTokenCount()
	assert.Equal(t, tokensBefore, tokensCleared, "Token count should return to original after clearing")

	// Verify promoted messages are removed
	promoted := segMem.GetPromotedContext()
	assert.Equal(t, 0, len(promoted), "Promoted context should be empty after clearing")
}

// TestSwapIntegration_RecallTool tests the recall_conversation tool.
func TestSwapIntegration_RecallTool(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	// Create memory with store
	memory := NewMemory()
	memory.store = store

	// Create session and add messages
	sessionID := "recall-tool-session"
	session := memory.GetOrCreateSession(sessionID)

	// Add some messages to database
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		msg := Message{
			Role:      "user",
			Content:   "Message number " + string(rune(i)),
			Timestamp: time.Now(),
		}
		err := store.SaveMessage(ctx, sessionID, msg)
		require.NoError(t, err)
	}

	// Create recall tool
	recallTool := NewRecallConversationTool(memory)

	// Test tool execution
	ctxWithSession := context.WithValue(ctx, "session_id", sessionID) //nolint:staticcheck // Test uses string key intentionally
	result, err := recallTool.Execute(ctxWithSession, map[string]interface{}{
		"offset": float64(0),
		"limit":  float64(10),
	})

	require.NoError(t, err)
	assert.True(t, result.Success, "Tool execution should succeed")
	assert.Contains(t, result.Data, "Retrieved and loaded 10 messages", "Data should mention retrieved messages")

	// Verify messages were promoted
	segMem, _ := session.SegmentedMem.(*SegmentedMemory)
	promoted := segMem.GetPromotedContext()
	assert.Equal(t, 10, len(promoted), "10 messages should be promoted to context")
}

// TestSwapIntegration_ClearTool tests the clear_recalled_context tool.
func TestSwapIntegration_ClearTool(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	// Create memory with store
	memory := NewMemory()
	memory.store = store

	// Create session
	sessionID := "clear-tool-session"
	session := memory.GetOrCreateSession(sessionID)

	// Add some messages to database
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		msg := Message{
			Role:      "user",
			Content:   "Test message",
			Timestamp: time.Now(),
		}
		err := store.SaveMessage(ctx, sessionID, msg)
		require.NoError(t, err)
	}

	// Manually promote some messages
	segMem, _ := session.SegmentedMem.(*SegmentedMemory)
	messages, _ := segMem.RetrieveMessagesFromSwap(ctx, 0, 5)
	_ = segMem.PromoteMessagesToContext(messages)

	// Verify messages are promoted
	promoted := segMem.GetPromotedContext()
	assert.Equal(t, 5, len(promoted), "5 messages should be promoted")

	// Create clear tool and execute
	clearTool := NewClearRecalledContextTool(memory)
	ctxWithSession := context.WithValue(ctx, "session_id", sessionID) //nolint:staticcheck // Test uses string key intentionally
	result, err := clearTool.Execute(ctxWithSession, map[string]interface{}{})

	require.NoError(t, err)
	assert.True(t, result.Success, "Tool execution should succeed")
	assert.Contains(t, result.Data, "Cleared 5 recalled messages", "Data should mention cleared count")

	// Verify messages are cleared
	promotedAfter := segMem.GetPromotedContext()
	assert.Equal(t, 0, len(promotedAfter), "Promoted context should be empty after clearing")
}

// TestSwapIntegration_TokenBudgetEnforcement tests that swap respects token budget.
func TestSwapIntegration_TokenBudgetEnforcement(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	// Create memory with store and small token budget
	memory := NewMemory()
	memory.store = store
	memory.maxContextTokens = 1000
	memory.reservedOutputTokens = 200

	// Create session
	sessionID := "budget-session"
	session := memory.GetOrCreateSession(sessionID)

	// Get segmented memory
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)

	// Fill context to near capacity
	for i := 0; i < 10; i++ {
		msg := Message{
			Role:      "user",
			Content:   "This is a message to fill up the token budget.",
			Timestamp: time.Now(),
		}
		segMem.AddMessage(msg)
	}

	// Save some messages to swap
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		msg := Message{
			Role:      "user",
			Content:   "Very long message " + string(make([]byte, 1000)),
			Timestamp: time.Now(),
		}
		err := store.SaveMessage(ctx, sessionID, msg)
		require.NoError(t, err)
	}

	// Try to retrieve and promote - should fail due to budget
	largeMessages, err := segMem.RetrieveMessagesFromSwap(ctx, 0, 5)
	require.NoError(t, err)

	err = segMem.PromoteMessagesToContext(largeMessages)
	assert.Error(t, err, "Should fail to promote due to token budget exceeded")
	assert.Contains(t, err.Error(), "token budget exceeded")
}

// TestSwapIntegration_ConcurrentAccess tests concurrent swap operations.
func TestSwapIntegration_ConcurrentAccess(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	// Create memory with store
	memory := NewMemory()
	memory.store = store

	// Create session
	sessionID := "concurrent-session"
	session := memory.GetOrCreateSession(sessionID)
	segMem, _ := session.SegmentedMem.(*SegmentedMemory)
	segMem.SetMaxL2Tokens(50) // Small limit to trigger evictions

	ctx := context.Background()

	// Concurrent message additions (triggers evictions)
	done := make(chan bool)
	for i := 0; i < 3; i++ {
		go func(id int) {
			for j := 0; j < 10; j++ {
				msg := Message{
					Role:      "user",
					Content:   "Concurrent message",
					Timestamp: time.Now(),
				}
				segMem.AddMessage(msg)
				_ = store.SaveMessage(ctx, sessionID, msg)
			}
			done <- true
		}(i)
	}

	// Concurrent retrievals and promotions
	for i := 0; i < 2; i++ {
		go func() {
			for j := 0; j < 5; j++ {
				messages, _ := segMem.RetrieveMessagesFromSwap(ctx, 0, 5)
				if len(messages) > 0 {
					_ = segMem.PromoteMessagesToContext(messages)
					time.Sleep(10 * time.Millisecond)
					segMem.ClearPromotedContext()
				}
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 5; i++ {
		<-done
	}

	// Verify no panics or data corruption
	evictions, retrievals := segMem.GetSwapStats()
	t.Logf("Evictions: %d, Retrievals: %d", evictions, retrievals)
	assert.True(t, evictions >= 0 && retrievals >= 0, "Stats should be non-negative")

	// Verify memory is still consistent
	llmMessages := segMem.GetMessagesForLLM()
	assert.Greater(t, len(llmMessages), 0, "Should have some messages in context")
}

// TestSwapIntegration_TracerIntegration tests that tracer is properly integrated.
func TestSwapIntegration_TracerIntegration(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	// Create memory with store and tracer
	memory := NewMemory()
	memory.store = store
	memory.SetTracer(tracer)

	// Create session
	session := memory.GetOrCreateSession("tracer-session")

	// Verify tracer is set on segmented memory
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	assert.NotNil(t, segMem.tracer, "Tracer should be set on segmented memory")

	// Trigger eviction error by corrupting store (close it early)
	store.Close()

	// Try to trigger eviction - should use tracer to log error
	segMem.SetMaxL2Tokens(10)
	for i := 0; i < 20; i++ {
		segMem.AddMessage(Message{
			Role:      "user",
			Content:   "Trigger eviction with closed store",
			Timestamp: time.Now(),
		})
	}

	// Should not panic, error should be traced
	// (NoOpTracer discards errors, but real tracer would record them)
}

// TestSwapIntegration_NoSwapGracefulDegradation tests behavior when swap is disabled.
func TestSwapIntegration_NoSwapGracefulDegradation(t *testing.T) {
	// Create memory WITHOUT store
	memory := NewMemory()

	// Create session
	sessionID := "no-swap-session"
	session := memory.GetOrCreateSession(sessionID)

	// Verify swap is NOT enabled
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	assert.False(t, segMem.IsSwapEnabled(), "Swap should not be enabled without store")

	// Try to use recall tool - should fail gracefully
	recallTool := NewRecallConversationTool(memory)
	ctx := context.WithValue(context.Background(), "session_id", sessionID) //nolint:staticcheck // Test uses string key intentionally

	result, err := recallTool.Execute(ctx, map[string]interface{}{
		"offset": float64(0),
		"limit":  float64(10),
	})

	require.NoError(t, err, "Tool should not error, but return failure result")
	assert.False(t, result.Success, "Tool should indicate failure")
	assert.NotNil(t, result.Error)
	assert.Equal(t, "SWAP_NOT_ENABLED", result.Error.Code)
}

// TestSwapIntegration_SemanticSearch tests the search_conversation feature with semantic search.
func TestSwapIntegration_SemanticSearch(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	// Create memory with store
	memory := NewMemory()
	memory.store = store

	// Create session
	sessionID := "semantic-search-session"
	session := memory.GetOrCreateSession(sessionID)

	// Add messages with different topics
	ctx := context.Background()
	messages := []Message{
		{Role: "user", Content: "How do I optimize SQL queries for better performance?", Timestamp: time.Now()},
		{Role: "assistant", Content: "Use indexes and analyze execution plans for SQL optimization.", Timestamp: time.Now()},
		{Role: "user", Content: "Tell me about API authentication methods.", Timestamp: time.Now()},
		{Role: "assistant", Content: "APIs support JWT, OAuth, and API key authentication.", Timestamp: time.Now()},
		{Role: "user", Content: "What are database normalization techniques?", Timestamp: time.Now()},
	}

	for _, msg := range messages {
		err := store.SaveMessage(ctx, sessionID, msg)
		require.NoError(t, err)
	}

	// Get segmented memory
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	require.True(t, segMem.IsSwapEnabled())

	// Search for SQL-related content (should find messages[0], [1], [4])
	// FTS5 uses OR logic for multi-word queries, so any message with SQL, database, or optimization will match
	results, err := segMem.SearchMessages(ctx, "SQL database optimization", 3)
	require.NoError(t, err)

	assert.Greater(t, len(results), 0, "Should find SQL-related messages")

	// Verify at least one result is about SQL
	foundSQL := false
	for _, msg := range results {
		if msg.Content == messages[0].Content || msg.Content == messages[1].Content {
			foundSQL = true
			break
		}
	}
	assert.True(t, foundSQL, "Should find messages about SQL")
}

// TestSwapIntegration_SemanticSearchTool tests the search_conversation tool.
func TestSwapIntegration_SemanticSearchTool(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	// Create memory with store
	memory := NewMemory()
	memory.store = store

	// Create session and add messages
	sessionID := "search-tool-session"
	session := memory.GetOrCreateSession(sessionID)

	ctx := context.Background()
	for i := 0; i < 15; i++ {
		msg := Message{
			Role:      "user",
			Content:   fmt.Sprintf("Message %d about database optimization techniques", i),
			Timestamp: time.Now(),
		}
		err := store.SaveMessage(ctx, sessionID, msg)
		require.NoError(t, err)
	}

	// Create search tool
	searchTool := NewSearchConversationTool(memory)

	// Test tool execution with promote=true
	ctxWithSession := context.WithValue(ctx, "session_id", sessionID) //nolint:staticcheck // Test uses string key intentionally
	result, err := searchTool.Execute(ctxWithSession, map[string]interface{}{
		"query":   "database optimization",
		"limit":   float64(5),
		"promote": true,
	})

	require.NoError(t, err)
	assert.True(t, result.Success, "Tool execution should succeed")
	assert.Contains(t, result.Data, "Found", "Data should mention found messages")
	assert.Contains(t, result.Data, "promoted to context", "Should indicate promotion")

	// Verify messages were promoted
	segMem, _ := session.SegmentedMem.(*SegmentedMemory)
	promoted := segMem.GetPromotedContext()
	assert.Equal(t, 5, len(promoted), "Should promote 5 messages to context")

	// Test tool execution with promote=false (preview mode)
	segMem.ClearPromotedContext()

	result, err = searchTool.Execute(ctxWithSession, map[string]interface{}{
		"query":   "database",
		"limit":   float64(3),
		"promote": false,
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Data, "preview only", "Should indicate preview mode")

	// Verify messages were NOT promoted
	promoted = segMem.GetPromotedContext()
	assert.Equal(t, 0, len(promoted), "Preview mode should not promote messages")
}

// TestSwapIntegration_SemanticSearchNoSwap tests semantic search fails gracefully without swap.
func TestSwapIntegration_SemanticSearchNoSwap(t *testing.T) {
	// Create memory WITHOUT store
	memory := NewMemory()

	// Create session
	sessionID := "no-swap-search-session"
	session := memory.GetOrCreateSession(sessionID)

	// Verify swap is NOT enabled
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	assert.False(t, segMem.IsSwapEnabled(), "Swap should not be enabled without store")

	// Try to use search tool - should fail gracefully
	searchTool := NewSearchConversationTool(memory)
	ctx := context.WithValue(context.Background(), "session_id", sessionID) //nolint:staticcheck // Test uses string key intentionally

	result, err := searchTool.Execute(ctx, map[string]interface{}{
		"query": "test search",
		"limit": float64(10),
	})

	require.NoError(t, err, "Tool should not error, but return failure result")
	assert.False(t, result.Success, "Tool should indicate failure")
	assert.NotNil(t, result.Error)
	assert.Equal(t, "SWAP_NOT_ENABLED", result.Error.Code)
}

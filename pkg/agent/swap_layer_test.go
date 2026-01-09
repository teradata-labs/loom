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
)

// TestSwapLayerEviction tests that L2 is evicted to swap when exceeding maxL2Tokens.
func TestSwapLayerEviction(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	// Create session
	sessionID := "test-session-eviction"

	// Create segmented memory with small L2 limit
	sm := NewSegmentedMemory("System prompt", 200000, 20000)
	sm.SetSessionStore(store, sessionID)
	sm.SetMaxL2Tokens(100) // Very small limit to trigger eviction

	// Verify swap is enabled
	assert.True(t, sm.IsSwapEnabled())

	// Add messages to fill L1
	for i := 0; i < 15; i++ {
		sm.AddMessage(Message{
			Role:      "user",
			Content:   "This is a test message that will eventually be compressed to L2.",
			Timestamp: time.Now(),
		})
	}

	// Check that L2 was evicted to swap
	evictions, _ := sm.GetSwapStats()
	assert.Greater(t, evictions, 0, "Expected L2 to be evicted to swap")

	// Verify L2 is now empty or small after eviction
	l2Summary := sm.GetL2Summary()
	tokenCount := sm.tokenCounter.CountTokens(l2Summary)
	assert.LessOrEqual(t, tokenCount, sm.maxL2Tokens, "L2 should not exceed max tokens after eviction")

	// Verify snapshot was saved to database
	ctx := context.Background()
	snapshots, err := store.LoadMemorySnapshots(ctx, sessionID, "l2_summary", 0)
	require.NoError(t, err)
	assert.Greater(t, len(snapshots), 0, "Expected snapshots in database")
}

// TestSwapLayerRetrieval tests retrieving messages from swap.
func TestSwapLayerRetrieval(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	sessionID := "test-session-retrieval"

	// Save some messages directly to database
	ctx := context.Background()
	messages := []Message{
		{Role: "user", Content: "Message 1", Timestamp: time.Now()},
		{Role: "assistant", Content: "Response 1", Timestamp: time.Now()},
		{Role: "user", Content: "Message 2", Timestamp: time.Now()},
		{Role: "assistant", Content: "Response 2", Timestamp: time.Now()},
	}

	for _, msg := range messages {
		err := store.SaveMessage(ctx, sessionID, msg)
		require.NoError(t, err)
	}

	// Create segmented memory with swap
	sm := NewSegmentedMemory("System prompt", 200000, 20000)
	sm.SetSessionStore(store, sessionID)

	// Retrieve messages from swap
	retrieved, err := sm.RetrieveMessagesFromSwap(ctx, 0, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, len(retrieved), "Expected 2 messages")
	assert.Equal(t, "Message 1", retrieved[0].Content)
	assert.Equal(t, "Response 1", retrieved[1].Content)

	// Test pagination
	retrieved, err = sm.RetrieveMessagesFromSwap(ctx, 2, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, len(retrieved))
	assert.Equal(t, "Message 2", retrieved[0].Content)

	// Verify retrieval stats
	_, retrievals := sm.GetSwapStats()
	assert.Equal(t, 2, retrievals, "Expected 2 retrievals")
}

// TestSwapLayerPromotion tests promoting retrieved messages to context.
func TestSwapLayerPromotion(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	sessionID := "test-session-promotion"

	// Create segmented memory with swap
	sm := NewSegmentedMemory("System prompt", 200000, 20000)
	sm.SetSessionStore(store, sessionID)

	// Save and retrieve messages
	ctx := context.Background()
	messages := []Message{
		{Role: "user", Content: "Old message 1", Timestamp: time.Now()},
		{Role: "assistant", Content: "Old response 1", Timestamp: time.Now()},
	}

	for _, msg := range messages {
		err := store.SaveMessage(ctx, sessionID, msg)
		require.NoError(t, err)
	}

	retrieved, err := sm.RetrieveMessagesFromSwap(ctx, 0, 2)
	require.NoError(t, err)

	// Promote to context
	err = sm.PromoteMessagesToContext(retrieved)
	require.NoError(t, err)

	// Verify promoted context
	promoted := sm.GetPromotedContext()
	assert.Equal(t, 2, len(promoted))
	assert.Equal(t, "Old message 1", promoted[0].Content)

	// Verify promoted messages appear in GetMessagesForLLM
	llmMessages := sm.GetMessagesForLLM()
	hasPromoted := false
	for _, msg := range llmMessages {
		if msg.Role == "user" && msg.Content == "Old message 1" {
			hasPromoted = true
			break
		}
	}
	assert.True(t, hasPromoted, "Promoted messages should appear in LLM context")

	// Clear promoted context
	sm.ClearPromotedContext()
	promoted = sm.GetPromotedContext()
	assert.Equal(t, 0, len(promoted), "Promoted context should be cleared")
}

// TestSwapLayerTokenBudget tests that promotion respects token budget.
func TestSwapLayerTokenBudget(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	sessionID := "test-session-budget"

	// Create segmented memory with very small token budget
	sm := NewSegmentedMemory("System prompt", 1000, 200) // Only 800 tokens available
	sm.SetSessionStore(store, sessionID)

	// Fill context to near capacity
	for i := 0; i < 10; i++ {
		sm.AddMessage(Message{
			Role:      "user",
			Content:   "This is a relatively long message to consume token budget.",
			Timestamp: time.Now(),
		})
	}

	// Try to promote a large message that would exceed budget
	largeMessages := []Message{
		{
			Role:      "user",
			Content:   "This is an extremely long message " + string(make([]byte, 10000)) + " that should exceed token budget.",
			Timestamp: time.Now(),
		},
	}

	// Should fail due to budget
	err = sm.PromoteMessagesToContext(largeMessages)
	assert.Error(t, err, "Expected error when exceeding token budget")
	assert.Contains(t, err.Error(), "token budget exceeded")
}

// TestSwapLayerDisabled tests behavior when swap is not enabled.
func TestSwapLayerDisabled(t *testing.T) {
	// Create segmented memory WITHOUT setting session store
	sm := NewSegmentedMemory("System prompt", 200000, 20000)

	// Verify swap is disabled
	assert.False(t, sm.IsSwapEnabled())

	// Retrieval should fail
	ctx := context.Background()
	_, err := sm.RetrieveMessagesFromSwap(ctx, 0, 10)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not enabled")

	// Promotion should fail
	err = sm.PromoteMessagesToContext([]Message{{Role: "user", Content: "Test"}})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not enabled")

	// L2 should grow unbounded (existing behavior)
	sm.SetMaxL2Tokens(100)
	for i := 0; i < 50; i++ {
		sm.AddMessage(Message{
			Role:      "user",
			Content:   "Message to fill L2",
			Timestamp: time.Now(),
		})
	}

	// L2 may exceed limit without swap (degraded mode)
	l2Summary := sm.GetL2Summary()
	// Just verify it doesn't crash - L2 can grow unbounded without swap
	assert.NotEmpty(t, l2Summary)
}

// TestSwapLayerConcurrency tests concurrent access to swap layer with race detector.
func TestSwapLayerConcurrency(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	sessionID := "test-session-concurrent"

	// Create segmented memory with swap
	sm := NewSegmentedMemory("System prompt", 200000, 20000)
	sm.SetSessionStore(store, sessionID)
	sm.SetMaxL2Tokens(50) // Small limit to trigger frequent evictions

	ctx := context.Background()

	// Concurrent message additions (should trigger evictions)
	done := make(chan bool)
	for i := 0; i < 3; i++ {
		go func(id int) {
			for j := 0; j < 10; j++ {
				sm.AddMessage(Message{
					Role:      "user",
					Content:   "Concurrent message",
					Timestamp: time.Now(),
				})
			}
			done <- true
		}(i)
	}

	// Concurrent retrievals
	for i := 0; i < 2; i++ {
		go func() {
			for j := 0; j < 5; j++ {
				_, _ = sm.RetrieveMessagesFromSwap(ctx, 0, 5)
				time.Sleep(10 * time.Millisecond)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 5; i++ {
		<-done
	}

	// Verify no panics occurred and stats are reasonable
	evictions, retrievals := sm.GetSwapStats()
	t.Logf("Evictions: %d, Retrievals: %d", evictions, retrievals)
	assert.True(t, evictions >= 0 && retrievals >= 0, "Stats should be non-negative")
}

// TestSwapLayerL2Snapshots tests retrieving L2 snapshot history.
func TestSwapLayerL2Snapshots(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	sessionID := "test-session-snapshots"

	// Create segmented memory with small L2 limit to trigger multiple evictions
	sm := NewSegmentedMemory("System prompt", 200000, 20000)
	sm.SetSessionStore(store, sessionID)
	sm.SetMaxL2Tokens(50) // Very small to force multiple evictions

	// Add many messages to trigger multiple L2 evictions
	for i := 0; i < 30; i++ {
		sm.AddMessage(Message{
			Role:      "user",
			Content:   "This message will eventually be compressed and evicted to create L2 snapshots.",
			Timestamp: time.Now(),
		})
	}

	// Retrieve L2 snapshots
	ctx := context.Background()
	snapshots, err := sm.RetrieveL2Snapshots(ctx, 0) // All snapshots
	require.NoError(t, err)

	// Should have at least one snapshot
	assert.Greater(t, len(snapshots), 0, "Expected at least one L2 snapshot")

	// Verify snapshots are non-empty strings
	for i, snapshot := range snapshots {
		assert.NotEmpty(t, snapshot, "Snapshot %d should not be empty", i)
	}
}

// TestSwapLayerMemoryStats tests that swap stats are included in memory statistics.
func TestSwapLayerMemoryStats(t *testing.T) {
	// Create temporary database
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	// Create session store
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	sessionID := "test-session-stats"

	// Create segmented memory with swap
	sm := NewSegmentedMemory("System prompt", 200000, 20000)
	sm.SetSessionStore(store, sessionID)
	sm.SetMaxL2Tokens(100)

	// Add messages to trigger eviction
	for i := 0; i < 20; i++ {
		sm.AddMessage(Message{
			Role:      "user",
			Content:   "Message for statistics test",
			Timestamp: time.Now(),
		})
	}

	// Get memory stats
	stats := sm.GetMemoryStats()

	// Verify expected keys exist
	assert.Contains(t, stats, "total_tokens")
	assert.Contains(t, stats, "l1_message_count")
	assert.Contains(t, stats, "l2_summary_length")
	assert.Contains(t, stats, "budget_usage_pct")

	// Verify values are reasonable
	totalTokens := stats["total_tokens"].(int)
	assert.Greater(t, totalTokens, 0, "Total tokens should be > 0")

	budgetPct := stats["budget_usage_pct"].(float64)
	assert.GreaterOrEqual(t, budgetPct, 0.0)
	assert.LessOrEqual(t, budgetPct, 100.0)
}

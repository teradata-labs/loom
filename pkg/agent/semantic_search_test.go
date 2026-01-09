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
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// TestSearchFTS5_BasicQuery tests that FTS5 returns relevant results.
func TestSearchFTS5_BasicQuery(t *testing.T) {
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	sessionID := "basic-query-session"

	// Add messages with different content
	messages := []Message{
		{Role: "user", Content: "How do I optimize database queries?", Timestamp: time.Now()},
		{Role: "assistant", Content: "To optimize SQL queries, use indexes and analyze execution plans.", Timestamp: time.Now()},
		{Role: "user", Content: "Tell me about API authentication.", Timestamp: time.Now()},
		{Role: "assistant", Content: "APIs can use JWT tokens, OAuth, or API keys for authentication.", Timestamp: time.Now()},
	}

	for _, msg := range messages {
		err := store.SaveMessage(ctx, sessionID, msg)
		require.NoError(t, err)
	}

	// Search for database-related content
	results, err := store.SearchFTS5(ctx, sessionID, "database optimization", 10)
	require.NoError(t, err)

	assert.Greater(t, len(results), 0, "Should find database-related messages")

	// Verify results contain relevant content
	found := false
	for _, msg := range results {
		if msg.Content == messages[0].Content || msg.Content == messages[1].Content {
			found = true
			break
		}
	}
	assert.True(t, found, "Should find messages about database optimization")
}

// TestSearchFTS5_SessionFiltering tests that only messages from the session are returned.
func TestSearchFTS5_SessionFiltering(t *testing.T) {
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Add messages to different sessions
	session1 := "session-1"
	session2 := "session-2"

	msg1 := Message{Role: "user", Content: "Database query optimization techniques", Timestamp: time.Now()}
	msg2 := Message{Role: "user", Content: "Database performance tuning guide", Timestamp: time.Now()}

	err = store.SaveMessage(ctx, session1, msg1)
	require.NoError(t, err)

	err = store.SaveMessage(ctx, session2, msg2)
	require.NoError(t, err)

	// Search in session1 only
	results, err := store.SearchFTS5(ctx, session1, "database", 10)
	require.NoError(t, err)

	assert.Equal(t, 1, len(results), "Should only find messages from session1")
	assert.Equal(t, msg1.Content, results[0].Content)
}

// TestSearchFTS5_BM25Ranking tests that results are ordered by relevance.
func TestSearchFTS5_BM25Ranking(t *testing.T) {
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	sessionID := "ranking-session"

	// Add messages with varying relevance to "SQL performance"
	messages := []Message{
		{Role: "user", Content: "SQL performance optimization is critical for applications.", Timestamp: time.Now()},
		{Role: "user", Content: "Tell me about Python programming.", Timestamp: time.Now()},
		{Role: "user", Content: "How to improve SQL query performance and speed?", Timestamp: time.Now()},
	}

	for _, msg := range messages {
		err := store.SaveMessage(ctx, sessionID, msg)
		require.NoError(t, err)
	}

	// Search for SQL performance
	results, err := store.SearchFTS5(ctx, sessionID, "SQL performance", 10)
	require.NoError(t, err)

	assert.Greater(t, len(results), 0, "Should find SQL-related messages")

	// Most relevant messages should be at the top (BM25 ranks by relevance)
	// Both messages[0] and messages[2] contain "SQL performance", they should rank higher than messages[1]
	topContent := results[0].Content
	assert.Contains(t, topContent, "SQL", "Top result should contain SQL")
	assert.Contains(t, topContent, "performance", "Top result should contain performance")
}

// TestSearchFTS5_EmptyQuery tests handling of empty queries.
func TestSearchFTS5_EmptyQuery(t *testing.T) {
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	sessionID := "empty-query-session"

	// Search with empty query
	results, err := store.SearchFTS5(ctx, sessionID, "", 10)
	require.NoError(t, err)
	assert.Equal(t, 0, len(results), "Empty query should return no results")

	// Search with whitespace-only query
	results, err = store.SearchFTS5(ctx, sessionID, "   ", 10)
	require.NoError(t, err)
	assert.Equal(t, 0, len(results), "Whitespace query should return no results")
}

// TestSearchFTS5_NoResults tests behavior when no matches are found.
func TestSearchFTS5_NoResults(t *testing.T) {
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	sessionID := "no-results-session"

	// Add a message
	msg := Message{Role: "user", Content: "Database optimization techniques", Timestamp: time.Now()}
	err = store.SaveMessage(ctx, sessionID, msg)
	require.NoError(t, err)

	// Search for unrelated content
	results, err := store.SearchFTS5(ctx, sessionID, "kubernetes deployment", 10)
	require.NoError(t, err)
	assert.Equal(t, 0, len(results), "Should return empty array when no matches")
}

// mockRerankingLLM is a mock LLM provider for testing reranking.
type mockRerankingLLM struct {
	responseJSON string
	shouldError  bool
}

func (m *mockRerankingLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	if m.shouldError {
		return nil, fmt.Errorf("mock LLM error")
	}
	return &llmtypes.LLMResponse{
		Content:    m.responseJSON,
		StopReason: "end_turn",
	}, nil
}

func (m *mockRerankingLLM) Name() string {
	return "mock-reranking"
}

func (m *mockRerankingLLM) Model() string {
	return "mock-reranking-model"
}

// TestRerankByRelevance_ImprovesOrdering tests that LLM reranking can improve BM25 ordering.
func TestRerankByRelevance_ImprovesOrdering(t *testing.T) {
	tracer := observability.NewNoOpTracer()
	segMem := NewSegmentedMemory("test", 100000, 10000)
	segMem.SetTracer(tracer)

	// Create mock LLM that returns reranking scores
	// Score index 2 highest, then 0, then 1
	mockLLM := &mockRerankingLLM{
		responseJSON: `[{"index": 2, "score": 10}, {"index": 0, "score": 7}, {"index": 1, "score": 3}]`,
	}
	segMem.SetLLMProvider(mockLLM)

	candidates := []Message{
		{Role: "user", Content: "Message about databases", Timestamp: time.Now()},
		{Role: "user", Content: "Unrelated message", Timestamp: time.Now()},
		{Role: "user", Content: "Database performance optimization", Timestamp: time.Now()},
	}

	ctx := context.Background()
	ranked, err := segMem.rerankByRelevance(ctx, "database optimization", candidates, 3)
	require.NoError(t, err)

	// Verify reordering based on LLM scores
	assert.Equal(t, 3, len(ranked), "Should return all candidates")
	assert.Equal(t, candidates[2].Content, ranked[0].Content, "Highest scored message should be first")
	assert.Equal(t, candidates[0].Content, ranked[1].Content, "Second scored message should be second")
	assert.Equal(t, candidates[1].Content, ranked[2].Content, "Lowest scored message should be last")
}

// TestRerankByRelevance_FallbackOnError tests that reranking falls back to BM25 on LLM error.
func TestRerankByRelevance_FallbackOnError(t *testing.T) {
	tracer := observability.NewNoOpTracer()
	segMem := NewSegmentedMemory("test", 100000, 10000)
	segMem.SetTracer(tracer)

	// Mock LLM that returns an error
	mockLLM := &mockRerankingLLM{
		shouldError: true,
	}
	segMem.SetLLMProvider(mockLLM)

	candidates := []Message{
		{Role: "user", Content: "First message", Timestamp: time.Now()},
		{Role: "user", Content: "Second message", Timestamp: time.Now()},
		{Role: "user", Content: "Third message", Timestamp: time.Now()},
	}

	ctx := context.Background()
	ranked, err := segMem.rerankByRelevance(ctx, "test query", candidates, 2)
	require.NoError(t, err, "Should not error on LLM failure")

	// Should fallback to BM25 ordering (first 2 candidates)
	assert.Equal(t, 2, len(ranked), "Should return requested limit")
	assert.Equal(t, candidates[0].Content, ranked[0].Content)
	assert.Equal(t, candidates[1].Content, ranked[1].Content)
}

// TestRerankByRelevance_NoLLM tests behavior when no LLM is configured.
func TestRerankByRelevance_NoLLM(t *testing.T) {
	tracer := observability.NewNoOpTracer()
	segMem := NewSegmentedMemory("test", 100000, 10000)
	segMem.SetTracer(tracer)
	// No LLM provider set

	candidates := []Message{
		{Role: "user", Content: "First message", Timestamp: time.Now()},
		{Role: "user", Content: "Second message", Timestamp: time.Now()},
	}

	ctx := context.Background()
	ranked, err := segMem.rerankByRelevance(ctx, "test query", candidates, 2)
	require.NoError(t, err)

	// Should return BM25 results unchanged
	assert.Equal(t, candidates, ranked)
}

// TestRerankByRelevance_MalformedJSON tests handling of malformed LLM output.
func TestRerankByRelevance_MalformedJSON(t *testing.T) {
	tracer := observability.NewNoOpTracer()
	segMem := NewSegmentedMemory("test", 100000, 10000)
	segMem.SetTracer(tracer)

	// Mock LLM that returns invalid JSON
	mockLLM := &mockRerankingLLM{
		responseJSON: `this is not valid json`,
	}
	segMem.SetLLMProvider(mockLLM)

	candidates := []Message{
		{Role: "user", Content: "First message", Timestamp: time.Now()},
		{Role: "user", Content: "Second message", Timestamp: time.Now()},
	}

	ctx := context.Background()
	ranked, err := segMem.rerankByRelevance(ctx, "test query", candidates, 2)
	require.NoError(t, err, "Should not error on malformed JSON")

	// Should fallback to BM25 ordering
	assert.Equal(t, candidates, ranked)
}

// TestSearchMessages_Integration tests full pipeline (BM25 + rerank).
func TestSearchMessages_Integration(t *testing.T) {
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	// Create memory with store
	memory := NewMemory()
	memory.store = store
	memory.SetTracer(tracer)

	sessionID := "integration-session"
	session := memory.GetOrCreateSession(sessionID)

	// Get segmented memory
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)

	// Set up mock LLM for reranking
	mockLLM := &mockRerankingLLM{
		responseJSON: `[{"index": 0, "score": 10}, {"index": 1, "score": 5}]`,
	}
	segMem.SetLLMProvider(mockLLM)

	ctx := context.Background()

	// Add messages to database
	messages := []Message{
		{Role: "user", Content: "How to optimize SQL database queries for better performance?", Timestamp: time.Now()},
		{Role: "assistant", Content: "SQL performance can be improved using indexes and query optimization techniques.", Timestamp: time.Now()},
		{Role: "user", Content: "Tell me about Python programming.", Timestamp: time.Now()},
	}

	for _, msg := range messages {
		err := store.SaveMessage(ctx, sessionID, msg)
		require.NoError(t, err)
	}

	// Execute semantic search
	results, err := segMem.SearchMessages(ctx, "SQL performance", 2)
	require.NoError(t, err)

	assert.Equal(t, 2, len(results), "Should return top 2 results")
	assert.Contains(t, results[0].Content, "SQL", "First result should be about SQL")
}

// TestSearchMessages_TokenBudget tests that promotion respects token budget.
func TestSearchMessages_TokenBudget(t *testing.T) {
	tmpDB := t.TempDir() + "/test.db"
	defer os.Remove(tmpDB)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	// Create memory with small token budget
	memory := NewMemory()
	memory.store = store
	memory.maxContextTokens = 500
	memory.reservedOutputTokens = 100

	sessionID := "budget-session"
	session := memory.GetOrCreateSession(sessionID)

	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)

	ctx := context.Background()

	// Fill context near capacity
	for i := 0; i < 10; i++ {
		msg := Message{
			Role:      "user",
			Content:   "This is a message to fill up the token budget with some content.",
			Timestamp: time.Now(),
		}
		segMem.AddMessage(msg)
	}

	// Save large messages to database
	largeMsg := Message{
		Role:      "user",
		Content:   "This is a very long message that will exceed the token budget when promoted." + string(make([]byte, 2000)),
		Timestamp: time.Now(),
	}
	err = store.SaveMessage(ctx, sessionID, largeMsg)
	require.NoError(t, err)

	// Search and attempt to promote
	results, err := segMem.SearchMessages(ctx, "long message", 1)
	require.NoError(t, err)
	assert.Greater(t, len(results), 0, "Should find the message")

	// Try to promote - should fail due to budget
	err = segMem.PromoteMessagesToContext(results)
	assert.Error(t, err, "Should fail to promote due to token budget exceeded")
	assert.Contains(t, err.Error(), "token budget exceeded")
}

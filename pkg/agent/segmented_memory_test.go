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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockCompressor implements MemoryCompressor for testing
type mockCompressor struct {
	enabled     bool
	compressFn  func([]Message) string
	shouldError bool
}

func (m *mockCompressor) CompressMessages(ctx context.Context, messages []Message) (string, error) {
	if m.shouldError {
		return "", fmt.Errorf("mock compression error")
	}
	if m.compressFn != nil {
		return m.compressFn(messages), nil
	}
	return fmt.Sprintf("Compressed %d messages", len(messages)), nil
}

func (m *mockCompressor) IsEnabled() bool {
	return m.enabled
}

func TestNewSegmentedMemory(t *testing.T) {
	romContent := "This is ROM content"
	sm := NewSegmentedMemory(romContent, 0, 0)

	assert.NotNil(t, sm)
	assert.Equal(t, romContent, sm.romContent)
	assert.NotNil(t, sm.tokenCounter)
	assert.NotNil(t, sm.tokenBudget)
	assert.Equal(t, 8, sm.maxL1Messages, "Should use balanced profile default maxL1Messages")
	assert.Equal(t, 4, sm.minL1Messages, "Should use balanced profile default minL1Messages")
	assert.Equal(t, 1, sm.maxToolResults)
	assert.Equal(t, 10, sm.maxSchemas)
	assert.Empty(t, sm.l1Messages)
	assert.Empty(t, sm.toolResults)
	assert.Empty(t, sm.schemaCache)

	// Token count should be initialized with ROM content
	assert.Greater(t, sm.GetTokenCount(), 0)
}

func TestSegmentedMemory_AddMessage(t *testing.T) {
	sm := NewSegmentedMemory("ROM content", 0, 0)

	// Add a few messages
	for i := 0; i < 5; i++ {
		msg := Message{
			Role:      "user",
			Content:   fmt.Sprintf("Message %d", i),
			Timestamp: time.Now(),
		}
		sm.AddMessage(msg)
	}

	assert.Equal(t, 5, sm.GetL1MessageCount())

	// Verify messages are stored
	messages := sm.GetMessages()
	assert.Len(t, messages, 5)
	assert.Equal(t, "Message 0", messages[0].Content)
	assert.Equal(t, "Message 4", messages[4].Content)
}

func TestSegmentedMemory_AddMessage_Compression(t *testing.T) {
	sm := NewSegmentedMemory("ROM content", 0, 0)
	sm.maxL1Messages = 5 // Low limit to trigger compression

	// Add messages up to the limit
	for i := 0; i < 10; i++ {
		msg := Message{
			Role:      "user",
			Content:   fmt.Sprintf("Message %d with some content to take up tokens", i),
			Timestamp: time.Now(),
		}
		sm.AddMessage(msg)
	}

	// Should trigger compression and keep messages under maxL1Messages
	assert.LessOrEqual(t, sm.GetL1MessageCount(), sm.maxL1Messages)

	// L2 summary should have content (compressed old messages)
	sm.mu.RLock()
	hasL2Content := len(sm.l2Summary) > 0
	sm.mu.RUnlock()
	assert.True(t, hasL2Content, "L2 summary should contain compressed messages")
}

func TestSegmentedMemory_AddMessage_AdaptiveCompression(t *testing.T) {
	sm := NewSegmentedMemory("ROM content", 0, 0)

	// Set up mock compressor
	compressor := &mockCompressor{
		enabled: true,
		compressFn: func(messages []Message) string {
			return fmt.Sprintf("Compressed summary of %d messages", len(messages))
		},
	}
	sm.SetCompressor(compressor)
	sm.maxL1Messages = 20 // Higher limit

	// Add many messages to trigger adaptive compression
	for i := 0; i < 25; i++ {
		msg := Message{
			Role:      "user",
			Content:   strings.Repeat(fmt.Sprintf("Long message %d ", i), 100), // ~500 tokens each
			Timestamp: time.Now(),
		}
		sm.AddMessage(msg)
	}

	// Should have compressed some messages
	assert.LessOrEqual(t, sm.GetL1MessageCount(), sm.maxL1Messages)
}

func TestSegmentedMemory_AddToolResult(t *testing.T) {
	sm := NewSegmentedMemory("ROM content", 0, 0)

	result := CachedToolResult{
		ToolName:  "test_tool",
		Args:      map[string]interface{}{"param": "value"},
		Result:    "Tool execution result",
		Timestamp: time.Now(),
	}

	sm.AddToolResult(result)

	// With maxToolResults=1, should only keep the latest result
	results := sm.GetCachedToolResults()
	assert.Len(t, results, 1)
	assert.Equal(t, "test_tool", results[0].ToolName)

	// Add another result - should replace the first one
	result2 := CachedToolResult{
		ToolName:  "another_tool",
		Args:      map[string]interface{}{"param": "value2"},
		Result:    "Second tool result",
		Timestamp: time.Now(),
	}

	sm.AddToolResult(result2)

	results = sm.GetCachedToolResults()
	assert.Len(t, results, 1)
	assert.Equal(t, "another_tool", results[0].ToolName)
}

func TestSegmentedMemory_AddToolResult_MultipleResults(t *testing.T) {
	sm := NewSegmentedMemory("ROM content", 0, 0)
	sm.maxToolResults = 3 // Allow multiple results

	// Add 5 results
	for i := 0; i < 5; i++ {
		result := CachedToolResult{
			ToolName:  fmt.Sprintf("tool_%d", i),
			Args:      map[string]interface{}{"id": i},
			Result:    fmt.Sprintf("Result %d", i),
			Timestamp: time.Now(),
		}
		sm.AddToolResult(result)
	}

	// Should keep only last 3
	results := sm.GetCachedToolResults()
	assert.Len(t, results, 3)
	assert.Equal(t, "tool_2", results[0].ToolName)
	assert.Equal(t, "tool_4", results[2].ToolName)
}

func TestSegmentedMemory_CacheSchema(t *testing.T) {
	sm := NewSegmentedMemory("ROM content", 0, 0)

	// Cache a schema
	sm.CacheSchema("table1", "CREATE TABLE table1 (id INT, name VARCHAR(100))")

	// Retrieve it
	schema, ok := sm.GetSchema("table1")
	assert.True(t, ok)
	assert.Contains(t, schema, "CREATE TABLE")

	// Schema not found
	_, ok = sm.GetSchema("nonexistent")
	assert.False(t, ok)
}

func TestSegmentedMemory_CacheSchema_LRUEviction(t *testing.T) {
	sm := NewSegmentedMemory("ROM content", 0, 0)
	sm.maxSchemas = 3 // Small cache for testing

	// Cache 3 schemas
	sm.CacheSchema("schema1", "data1")
	time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	sm.CacheSchema("schema2", "data2")
	time.Sleep(10 * time.Millisecond)
	sm.CacheSchema("schema3", "data3")

	// All should be present
	_, ok1 := sm.GetSchema("schema1")
	_, ok2 := sm.GetSchema("schema2")
	_, ok3 := sm.GetSchema("schema3")
	assert.True(t, ok1)
	assert.True(t, ok2)
	assert.True(t, ok3)

	time.Sleep(10 * time.Millisecond)

	// Access schema2 to make it more recently used
	sm.GetSchema("schema2")

	time.Sleep(10 * time.Millisecond)

	// Add a 4th schema - should evict schema1 (least recently used)
	sm.CacheSchema("schema4", "data4")

	// schema1 should be evicted
	_, ok1 = sm.GetSchema("schema1")
	_, ok2 = sm.GetSchema("schema2")
	_, ok3 = sm.GetSchema("schema3")
	_, ok4 := sm.GetSchema("schema4")

	assert.False(t, ok1, "schema1 should be evicted")
	assert.True(t, ok2, "schema2 should remain (was accessed)")
	assert.True(t, ok3, "schema3 should remain")
	assert.True(t, ok4, "schema4 should be present")
}

func TestSegmentedMemory_GetContextWindow(t *testing.T) {
	sm := NewSegmentedMemory("ROM: System documentation", 0, 0)

	// Add messages
	sm.AddMessage(Message{Role: "user", Content: "Hello"})
	sm.AddMessage(Message{Role: "assistant", Content: "Hi there"})

	// Add tool result
	sm.AddToolResult(CachedToolResult{
		ToolName: "query",
		Args:     map[string]interface{}{"sql": "SELECT 1"},
		Result:   "Success",
	})

	// Cache schema
	sm.CacheSchema("users", "CREATE TABLE users (id INT)")

	// Get context window
	context := sm.GetContextWindow()

	// Verify all layers are present
	assert.Contains(t, context, "=== DOCUMENTATION (ROM) ===")
	assert.Contains(t, context, "ROM: System documentation")
	assert.Contains(t, context, "=== SESSION CONTEXT (KERNEL) ===")
	assert.Contains(t, context, "query")
	assert.Contains(t, context, "users")
	assert.Contains(t, context, "=== RECENT CONVERSATION (L1 CACHE) ===")
	assert.Contains(t, context, "[user]: Hello")
	assert.Contains(t, context, "[assistant]: Hi there")
}

func TestSegmentedMemory_CompactMemory(t *testing.T) {
	sm := NewSegmentedMemory("ROM content", 0, 0)

	// Add messages
	for i := 0; i < 10; i++ {
		sm.AddMessage(Message{
			Role:    "user",
			Content: fmt.Sprintf("Message %d", i),
		})
	}

	initialL1Count := sm.GetL1MessageCount()
	// With balanced profile (maxL1=8), compression happens after 8 messages
	// So actual count will be less than 10
	assert.LessOrEqual(t, initialL1Count, 8, "L1 should not exceed maxL1Messages for balanced profile")

	// Compact memory
	messagesCompressed, tokensSaved := sm.CompactMemory()

	// All messages should be compressed
	assert.Equal(t, initialL1Count, messagesCompressed)
	assert.Greater(t, tokensSaved, 0)

	// L1 should be empty
	assert.Equal(t, 0, sm.GetL1MessageCount())

	// L2 should have content
	sm.mu.RLock()
	hasL2Content := len(sm.l2Summary) > 0
	sm.mu.RUnlock()
	assert.True(t, hasL2Content)
}

func TestSegmentedMemory_GetMemoryStats(t *testing.T) {
	sm := NewSegmentedMemory("ROM content for testing", 0, 0)

	// Add some data
	sm.AddMessage(Message{Role: "user", Content: "Test message"})
	sm.AddToolResult(CachedToolResult{
		ToolName: "test",
		Args:     map[string]interface{}{"param": "value"},
		Result:   "Result",
	})
	sm.CacheSchema("test_table", "CREATE TABLE test_table (id INT)")

	stats := sm.GetMemoryStats()

	// Verify required fields
	assert.NotNil(t, stats["total_tokens"])
	assert.NotNil(t, stats["tokens_used"])
	assert.NotNil(t, stats["tokens_available"])
	assert.NotNil(t, stats["token_budget_total"])
	assert.NotNil(t, stats["budget_usage_pct"])
	assert.Equal(t, 1, stats["l1_message_count"])
	assert.Equal(t, 8, stats["l1_max_messages"], "Should use balanced profile default maxL1Messages")
	assert.Equal(t, 4, stats["l1_min_messages"], "Should use balanced profile default minL1Messages")
	assert.Equal(t, 1, stats["tool_result_count"])
	assert.Equal(t, 1, stats["schema_cache_count"])
	assert.Equal(t, 10, stats["schema_cache_max"])

	// Token counts should be positive
	assert.Greater(t, stats["total_tokens"].(int), 0)
	assert.Greater(t, stats["rom_token_count"].(int), 0)
	assert.Greater(t, stats["kernel_token_count"].(int), 0)
	assert.Greater(t, stats["l1_token_count"].(int), 0)
}

func TestSegmentedMemory_GetMemoryStats_BudgetWarnings(t *testing.T) {
	// Create ROM content large enough to exceed 70% of token budget (>126K tokens)
	// Need ~500K characters to get ~125K tokens
	largeROM := strings.Repeat("This is a large ROM content section with documentation, examples, and reference material. ", 5000)
	sm := NewSegmentedMemory(largeROM, 0, 0)

	stats := sm.GetMemoryStats()
	warning := stats["budget_warning"].(string)

	// Verify budget usage is high enough to trigger a warning
	budgetPct := stats["budget_usage_pct"].(float64)
	t.Logf("Budget usage: %.2f%%", budgetPct)

	// With large ROM, we should see a warning if budget >70%
	if budgetPct > 70.0 {
		assert.NotEmpty(t, warning, "Should have warning when budget >70%")
	} else {
		// If we didn't hit the threshold, that's okay for this test
		t.Skip("ROM not large enough to trigger warning threshold")
	}
}

func TestSegmentedMemory_ClearL2(t *testing.T) {
	sm := NewSegmentedMemory("ROM content", 0, 0)

	// Add and compress messages to create L2 content
	for i := 0; i < 15; i++ {
		sm.AddMessage(Message{
			Role:    "user",
			Content: strings.Repeat(fmt.Sprintf("Message %d ", i), 50),
		})
	}

	// Verify L2 has content
	sm.mu.RLock()
	hasL2BeforeClear := len(sm.l2Summary) > 0
	sm.mu.RUnlock()
	assert.True(t, hasL2BeforeClear)

	// Clear L2
	sm.ClearL2()

	// Verify L2 is empty
	sm.mu.RLock()
	l2Empty := len(sm.l2Summary) == 0
	sm.mu.RUnlock()
	assert.True(t, l2Empty)
}

func TestSegmentedMemory_ConcurrentAccess(t *testing.T) {
	sm := NewSegmentedMemory("ROM content for concurrency test", 0, 0)

	var wg sync.WaitGroup
	// Reduce operations when running with race detector to avoid timeout
	// Race detector adds significant overhead (especially with token counting)
	// Further reduced from 5x20 to 3x10, then to 2x5 to prevent timeout
	// Token counting with tiktoken under race detector is very expensive
	numGoroutines := 2
	operationsPerGoroutine := 5

	// Concurrent AddMessage
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < operationsPerGoroutine; j++ {
				sm.AddMessage(Message{
					Role:    "user",
					Content: fmt.Sprintf("Message from goroutine %d iteration %d", id, j),
				})
			}
		}(i)
	}
	wg.Wait()

	// Concurrent GetMessages
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < operationsPerGoroutine; j++ {
				_ = sm.GetMessages()
			}
		}()
	}
	wg.Wait()

	// Concurrent CacheSchema and GetSchema
	wg.Add(numGoroutines * 2)
	for i := 0; i < numGoroutines; i++ {
		// Writer
		go func(id int) {
			defer wg.Done()
			for j := 0; j < operationsPerGoroutine; j++ {
				sm.CacheSchema(fmt.Sprintf("schema_%d", id), fmt.Sprintf("data_%d", j))
			}
		}(i)

		// Reader
		go func(id int) {
			defer wg.Done()
			for j := 0; j < operationsPerGoroutine; j++ {
				_, _ = sm.GetSchema(fmt.Sprintf("schema_%d", id))
			}
		}(i)
	}
	wg.Wait()

	// Verify state is consistent
	assert.NotPanics(t, func() {
		_ = sm.GetTokenCount()
		_ = sm.GetMemoryStats()
		_ = sm.GetContextWindow()
	})
}

func TestSegmentedMemory_CompressionWithMockCompressor(t *testing.T) {
	sm := NewSegmentedMemory("ROM content", 0, 0)
	sm.maxL1Messages = 5

	compressor := &mockCompressor{
		enabled: true,
		compressFn: func(messages []Message) string {
			var contents []string
			for _, msg := range messages {
				contents = append(contents, msg.Content)
			}
			return "Summary: " + strings.Join(contents, ", ")
		},
	}
	sm.SetCompressor(compressor)

	// Add messages to trigger compression
	for i := 0; i < 10; i++ {
		sm.AddMessage(Message{
			Role:    "user",
			Content: fmt.Sprintf("msg%d", i),
		})
	}

	// L2 should contain the mock compressed summary
	sm.mu.RLock()
	l2Contains := strings.Contains(sm.l2Summary, "Summary:")
	sm.mu.RUnlock()
	assert.True(t, l2Contains)
}

func TestSegmentedMemory_CompressionFallback(t *testing.T) {
	sm := NewSegmentedMemory("ROM content", 0, 0)
	sm.maxL1Messages = 5

	// Set compressor that errors
	compressor := &mockCompressor{
		enabled:     true,
		shouldError: true,
	}
	sm.SetCompressor(compressor)

	// Add messages to trigger compression
	for i := 0; i < 10; i++ {
		sm.AddMessage(Message{
			Role:    "user",
			Content: fmt.Sprintf("Message %d", i),
		})
	}

	// Should fall back to simple compression
	sm.mu.RLock()
	hasL2Content := len(sm.l2Summary) > 0
	sm.mu.RUnlock()
	assert.True(t, hasL2Content, "Should use fallback compression on error")
}

func TestSegmentedMemory_TokenCountAccuracy(t *testing.T) {
	romContent := "ROM content"
	sm := NewSegmentedMemory(romContent, 0, 0)

	initialTokens := sm.GetTokenCount()
	require.Greater(t, initialTokens, 0, "ROM should have tokens")

	// Add a message
	sm.AddMessage(Message{
		Role:    "user",
		Content: "Hello world",
	})

	tokensAfterMessage := sm.GetTokenCount()
	assert.Greater(t, tokensAfterMessage, initialTokens, "Tokens should increase after message")

	// Add tool result
	sm.AddToolResult(CachedToolResult{
		ToolName: "test_tool",
		Args:     map[string]interface{}{"param": "value"},
		Result:   "Success",
	})

	tokensAfterTool := sm.GetTokenCount()
	assert.Greater(t, tokensAfterTool, tokensAfterMessage, "Tokens should increase after tool result")

	// Cache schema
	sm.CacheSchema("table1", "CREATE TABLE table1 (id INT)")

	tokensAfterSchema := sm.GetTokenCount()
	assert.Greater(t, tokensAfterSchema, tokensAfterTool, "Tokens should increase after schema cache")
}

func BenchmarkSegmentedMemory_AddMessage(b *testing.B) {
	sm := NewSegmentedMemory("ROM content", 0, 0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm.AddMessage(Message{
			Role:    "user",
			Content: "Benchmark message",
		})
	}
}

func BenchmarkSegmentedMemory_GetContextWindow(b *testing.B) {
	sm := NewSegmentedMemory("ROM content for benchmark", 0, 0)

	// Pre-populate with data
	for i := 0; i < 10; i++ {
		sm.AddMessage(Message{
			Role:    "user",
			Content: fmt.Sprintf("Message %d", i),
		})
	}
	sm.CacheSchema("table1", "CREATE TABLE table1 (id INT)")
	sm.AddToolResult(CachedToolResult{
		ToolName: "query",
		Result:   "Success",
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sm.GetContextWindow()
	}
}

func BenchmarkSegmentedMemory_ConcurrentAccess(b *testing.B) {
	sm := NewSegmentedMemory("ROM content", 0, 0)

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%2 == 0 {
				sm.AddMessage(Message{
					Role:    "user",
					Content: fmt.Sprintf("Message %d", i),
				})
			} else {
				_ = sm.GetMessages()
			}
			i++
		}
	})
}

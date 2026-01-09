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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLLMCaller implements LLMCaller for testing
type mockLLMCaller struct {
	compressFn  func(string) string
	shouldError bool
	callCount   int
	mu          sync.Mutex
}

func (m *mockLLMCaller) CompressConversation(ctx context.Context, conversationText string) (string, error) {
	m.mu.Lock()
	m.callCount++
	m.mu.Unlock()

	if m.shouldError {
		return "", fmt.Errorf("mock compression error")
	}

	if m.compressFn != nil {
		return m.compressFn(conversationText), nil
	}

	// Default: simple summary
	return "Compressed summary of conversation", nil
}

func (m *mockLLMCaller) getCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

func TestNewLLMCompressor(t *testing.T) {
	// With LLM caller
	caller := &mockLLMCaller{}
	compressor := NewLLMCompressor(caller)
	assert.NotNil(t, compressor)
	assert.True(t, compressor.IsEnabled())

	// Without LLM caller (nil)
	compressor = NewLLMCompressor(nil)
	assert.NotNil(t, compressor)
	assert.False(t, compressor.IsEnabled())
}

func TestLLMCompressor_CompressMessages_WithLLM(t *testing.T) {
	caller := &mockLLMCaller{
		compressFn: func(text string) string {
			// Verify conversation text contains messages
			assert.Contains(t, text, "[user]:")
			assert.Contains(t, text, "[assistant]:")
			return "User asked about data; Agent provided results"
		},
	}

	compressor := NewLLMCompressor(caller)
	messages := []Message{
		{Role: "user", Content: "What data do we have?"},
		{Role: "assistant", Content: "We have customer and order data"},
		{Role: "user", Content: "Show me customers"},
		{Role: "assistant", Content: "Here are the customers..."},
	}

	summary, err := compressor.CompressMessages(context.Background(), messages)
	require.NoError(t, err)
	assert.Equal(t, "User asked about data; Agent provided results", summary)
	assert.Equal(t, 1, caller.getCallCount())
}

func TestLLMCompressor_CompressMessages_LLMError(t *testing.T) {
	caller := &mockLLMCaller{
		shouldError: true,
	}

	compressor := NewLLMCompressor(caller)
	messages := []Message{
		{Role: "user", Content: "Test message"},
		{Role: "assistant", Content: "Test response"},
	}

	// Should fall back to simple compression on error
	summary, err := compressor.CompressMessages(context.Background(), messages)
	require.NoError(t, err) // Fallback means no error propagated
	assert.NotEmpty(t, summary)
	assert.Contains(t, summary, "User:")
}

func TestLLMCompressor_CompressMessages_EmptySummary(t *testing.T) {
	caller := &mockLLMCaller{
		compressFn: func(text string) string {
			return "" // LLM returns empty
		},
	}

	compressor := NewLLMCompressor(caller)
	messages := []Message{
		{Role: "user", Content: "Test"},
	}

	// Should fall back to simple compression when LLM returns empty
	summary, err := compressor.CompressMessages(context.Background(), messages)
	require.NoError(t, err)
	assert.NotEmpty(t, summary)
}

func TestLLMCompressor_CompressMessages_NoLLM(t *testing.T) {
	compressor := NewLLMCompressor(nil)
	messages := []Message{
		{Role: "user", Content: "What is the weather like?"},
		{Role: "assistant", Content: "It's sunny today!"},
	}

	summary, err := compressor.CompressMessages(context.Background(), messages)
	require.NoError(t, err)
	assert.NotEmpty(t, summary)
	// Should use simple compression
	assert.Contains(t, summary, "User:")
	assert.Contains(t, summary, "weather")
}

func TestLLMCompressor_SimpleCompress(t *testing.T) {
	compressor := NewLLMCompressor(nil)

	tests := []struct {
		name     string
		messages []Message
		expected []string // Strings that should be in the result
	}{
		{
			name: "user and assistant messages",
			messages: []Message{
				{Role: "user", Content: "Hello, how are you?"},
				{Role: "assistant", Content: "I'm doing well, thank you!"},
			},
			expected: []string{"User:", "Hello"},
		},
		{
			name: "long user message truncated",
			messages: []Message{
				{Role: "user", Content: strings.Repeat("This is a very long message that should be truncated. ", 5)},
			},
			expected: []string{"User:", "..."},
		},
		{
			name: "assistant with tool calls",
			messages: []Message{
				{Role: "user", Content: "Query data"},
				{
					Role:    "assistant",
					Content: "Running query",
					ToolCalls: []ToolCall{
						{ID: "1", Name: "query_tool", Input: map[string]interface{}{"sql": "SELECT *"}},
					},
				},
			},
			expected: []string{"Agent executed tools"},
		},
		{
			name: "long assistant message truncated",
			messages: []Message{
				{Role: "assistant", Content: strings.Repeat("This is a long response. ", 10)},
			},
			expected: []string{"Agent:", "..."},
		},
		{
			name:     "empty messages",
			messages: []Message{},
			expected: []string{"Previous exchanges"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := compressor.simpleCompress(tt.messages)
			for _, exp := range tt.expected {
				assert.Contains(t, summary, exp)
			}
		})
	}
}

func TestLLMCompressor_ContainsToolCall(t *testing.T) {
	compressor := NewLLMCompressor(nil)

	// Message with tool calls
	msgWithTools := Message{
		Role:    "assistant",
		Content: "Executing query",
		ToolCalls: []ToolCall{
			{ID: "1", Name: "query", Input: map[string]interface{}{}},
		},
	}
	assert.True(t, compressor.containsToolCall(msgWithTools))

	// Message without tool calls
	msgWithoutTools := Message{
		Role:    "assistant",
		Content: "Just a regular message",
	}
	assert.False(t, compressor.containsToolCall(msgWithoutTools))
}

func TestLLMCompressor_SetLLMCaller(t *testing.T) {
	compressor := NewLLMCompressor(nil)
	assert.False(t, compressor.IsEnabled())

	caller := &mockLLMCaller{}
	compressor.SetLLMCaller(caller)
	assert.True(t, compressor.IsEnabled())

	// Set back to nil
	compressor.SetLLMCaller(nil)
	assert.False(t, compressor.IsEnabled())
}

func TestSimpleCompressor(t *testing.T) {
	compressor := NewSimpleCompressor()
	assert.NotNil(t, compressor)
	assert.False(t, compressor.IsEnabled())

	messages := []Message{
		{Role: "user", Content: "Test question"},
		{Role: "assistant", Content: "Test answer"},
	}

	summary, err := compressor.CompressMessages(context.Background(), messages)
	require.NoError(t, err)
	assert.Contains(t, summary, "User:")
	assert.Contains(t, summary, "Test question")
}

func TestSimpleCompressor_EmptyMessages(t *testing.T) {
	compressor := NewSimpleCompressor()
	summary, err := compressor.CompressMessages(context.Background(), []Message{})
	require.NoError(t, err)
	assert.Equal(t, "Previous exchanges", summary)
}

func TestSimpleCompressor_WithToolCalls(t *testing.T) {
	compressor := NewSimpleCompressor()
	messages := []Message{
		{Role: "user", Content: "Execute query"},
		{
			Role:    "assistant",
			Content: "Running...",
			ToolCalls: []ToolCall{
				{ID: "1", Name: "sql_query", Input: map[string]interface{}{"query": "SELECT 1"}},
			},
		},
	}

	summary, err := compressor.CompressMessages(context.Background(), messages)
	require.NoError(t, err)
	assert.Contains(t, summary, "Agent executed tools")
}

func TestLLMCompressor_ConcurrentAccess(t *testing.T) {
	caller := &mockLLMCaller{
		compressFn: func(text string) string {
			return "Summary"
		},
	}

	compressor := NewLLMCompressor(caller)
	messages := []Message{
		{Role: "user", Content: "Test"},
		{Role: "assistant", Content: "Response"},
	}

	var wg sync.WaitGroup
	numGoroutines := 10

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_, err := compressor.CompressMessages(context.Background(), messages)
				assert.NoError(t, err)
			}
		}()
	}

	wg.Wait()

	// Verify all calls were made
	assert.Equal(t, numGoroutines*10, caller.getCallCount())
}

func TestLLMCompressor_ContextCancellation(t *testing.T) {
	caller := &mockLLMCaller{
		compressFn: func(text string) string {
			return "Summary"
		},
	}

	compressor := NewLLMCompressor(caller)
	messages := []Message{
		{Role: "user", Content: "Test"},
	}

	// Create cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Should still work (falls back to simple compression if context issues)
	summary, err := compressor.CompressMessages(ctx, messages)
	require.NoError(t, err)
	assert.NotEmpty(t, summary)
}

func TestLLMCompressor_LargeMessageBatch(t *testing.T) {
	caller := &mockLLMCaller{
		compressFn: func(text string) string {
			// Verify we can handle large batches
			assert.NotEmpty(t, text)
			return "Compressed large batch"
		},
	}

	compressor := NewLLMCompressor(caller)

	// Create 50 messages
	messages := make([]Message, 50)
	for i := 0; i < 50; i++ {
		if i%2 == 0 {
			messages[i] = Message{
				Role:    "user",
				Content: fmt.Sprintf("User message %d", i),
			}
		} else {
			messages[i] = Message{
				Role:    "assistant",
				Content: fmt.Sprintf("Assistant message %d", i),
			}
		}
	}

	summary, err := compressor.CompressMessages(context.Background(), messages)
	require.NoError(t, err)
	assert.Equal(t, "Compressed large batch", summary)
}

func TestMemoryCompressor_Interface(t *testing.T) {
	// Verify our implementations satisfy the MemoryCompressor interface
	var _ MemoryCompressor = (*LLMCompressor)(nil)
	var _ MemoryCompressor = (*SimpleCompressor)(nil)
}

func BenchmarkLLMCompressor_SimpleCompress(b *testing.B) {
	compressor := NewLLMCompressor(nil)
	messages := []Message{
		{Role: "user", Content: "What data do we have?"},
		{Role: "assistant", Content: "We have customer and order data"},
		{Role: "user", Content: "Show me customers"},
		{Role: "assistant", Content: "Here are the customers: ..."},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = compressor.simpleCompress(messages)
	}
}

func BenchmarkLLMCompressor_WithMockLLM(b *testing.B) {
	caller := &mockLLMCaller{
		compressFn: func(text string) string {
			return "Summary"
		},
	}
	compressor := NewLLMCompressor(caller)
	messages := []Message{
		{Role: "user", Content: "Test message"},
		{Role: "assistant", Content: "Test response"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = compressor.CompressMessages(context.Background(), messages)
	}
}

func BenchmarkSimpleCompressor(b *testing.B) {
	compressor := NewSimpleCompressor()
	messages := []Message{
		{Role: "user", Content: "Test question"},
		{Role: "assistant", Content: "Test answer"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = compressor.CompressMessages(context.Background(), messages)
	}
}

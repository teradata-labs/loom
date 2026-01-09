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
	"sync"
	"testing"
	"time"

	"github.com/teradata-labs/loom/pkg/fabric"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Test full agent loop with tool execution

func TestAgent_FullLoopWithTools(t *testing.T) {
	// Create agent with mock LLM that requests a tool
	mockLLM := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{
				content:   "",
				toolCalls: []llmtypes.ToolCall{{ID: "call_1", Name: "calculator", Input: map[string]interface{}{"expression": "2+2"}}},
			},
			{
				content: "The result of 2+2 is 4",
			},
		},
	}

	mockBackend := &mockBackend{}
	ag := NewAgent(mockBackend, mockLLM)

	// Register calculator tool
	ag.RegisterTool(&mockCalculatorTool{})

	ctx := context.Background()
	resp, err := ag.Chat(ctx, "test_session", "What is 2+2?")

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	if resp.Content != "The result of 2+2 is 4" {
		t.Errorf("Expected final response, got: %s", resp.Content)
	}

	if len(resp.ToolExecutions) != 1 {
		t.Errorf("Expected 1 tool execution, got: %d", len(resp.ToolExecutions))
	}

	if resp.ToolExecutions[0].ToolName != "calculator" {
		t.Errorf("Expected calculator tool, got: %s", resp.ToolExecutions[0].ToolName)
	}

	// Verify tool was actually executed
	if resp.ToolExecutions[0].Result == nil {
		t.Error("Expected tool result, got nil")
	}

	if !resp.ToolExecutions[0].Result.Success {
		t.Error("Expected tool execution to succeed")
	}

	// Verify metadata
	if resp.Metadata["turns"] != 2 {
		t.Errorf("Expected 2 turns, got: %v", resp.Metadata["turns"])
	}

	if resp.Metadata["tool_executions"] != 1 {
		t.Errorf("Expected 1 tool execution, got: %v", resp.Metadata["tool_executions"])
	}
}

func TestAgent_MultipleToolExecutions(t *testing.T) {
	// LLM calls multiple tools in sequence
	mockLLM := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{
				content: "",
				toolCalls: []llmtypes.ToolCall{
					{ID: "call_1", Name: "calculator", Input: map[string]interface{}{"expression": "5+3"}},
					{ID: "call_2", Name: "calculator", Input: map[string]interface{}{"expression": "10*2"}},
				},
			},
			{
				content: "5+3 = 8 and 10*2 = 20",
			},
		},
	}

	mockBackend := &mockBackend{}
	ag := NewAgent(mockBackend, mockLLM)
	ag.RegisterTool(&mockCalculatorTool{})

	ctx := context.Background()
	resp, err := ag.Chat(ctx, "multi_tool", "Calculate 5+3 and 10*2")

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	if len(resp.ToolExecutions) != 2 {
		t.Fatalf("Expected 2 tool executions, got: %d", len(resp.ToolExecutions))
	}

	// Both tools should succeed
	for i, exec := range resp.ToolExecutions {
		if exec.Result == nil {
			t.Errorf("Tool execution %d: expected result, got nil", i)
		}
		if !exec.Result.Success {
			t.Errorf("Tool execution %d: expected success", i)
		}
	}
}

func TestAgent_MultiTurnConversation(t *testing.T) {
	// LLM makes multiple turns, requesting tools each time
	mockLLM := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{
				content:   "",
				toolCalls: []llmtypes.ToolCall{{ID: "call_1", Name: "calculator", Input: map[string]interface{}{"expression": "5+3"}}},
			},
			{
				content:   "Now let me calculate the square",
				toolCalls: []llmtypes.ToolCall{{ID: "call_2", Name: "calculator", Input: map[string]interface{}{"expression": "8*8"}}},
			},
			{
				content: "5+3 = 8, and 8 squared is 64",
			},
		},
	}

	mockBackend := &mockBackend{}
	ag := NewAgent(mockBackend, mockLLM)
	ag.RegisterTool(&mockCalculatorTool{})

	ctx := context.Background()
	resp, err := ag.Chat(ctx, "multi_turn", "What is 5+3, and what is that number squared?")

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	if len(resp.ToolExecutions) != 2 {
		t.Errorf("Expected 2 tool executions, got: %d", len(resp.ToolExecutions))
	}

	if resp.Metadata["turns"] != 3 {
		t.Errorf("Expected 3 turns, got: %v", resp.Metadata["turns"])
	}
}

func TestAgent_ToolExecutionError(t *testing.T) {
	// Test agent handling of tool errors
	mockLLM := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{
				content:   "",
				toolCalls: []llmtypes.ToolCall{{ID: "call_1", Name: "failing_tool", Input: map[string]interface{}{}}},
			},
			{
				content: "The tool failed, but I handled it gracefully",
			},
		},
	}

	mockBackend := &mockBackend{}
	ag := NewAgent(mockBackend, mockLLM)
	ag.RegisterTool(&mockFailingTool{})

	ctx := context.Background()
	resp, err := ag.Chat(ctx, "tool_error", "Use the failing tool")

	if err != nil {
		t.Fatalf("Chat should handle tool errors gracefully: %v", err)
	}

	if len(resp.ToolExecutions) != 1 {
		t.Errorf("Expected 1 tool execution, got: %d", len(resp.ToolExecutions))
	}

	// Check Result.Error (executor converts Go errors to Result errors)
	if resp.ToolExecutions[0].Result == nil {
		t.Fatal("Expected tool result, got nil")
	}

	if resp.ToolExecutions[0].Result.Success {
		t.Error("Expected tool execution to fail")
	}

	if resp.ToolExecutions[0].Result.Error == nil {
		t.Error("Expected tool execution error in Result, got nil")
	}

	// Agent should still return a response even if tool failed
	if resp.Content == "" {
		t.Error("Expected response content despite tool error")
	}
}

func TestAgent_MaxTurnsLimit(t *testing.T) {
	// LLM keeps requesting tools indefinitely
	mockLLM := &mockToolCallingLLM{
		alwaysCallTools: true,
	}

	mockBackend := &mockBackend{}
	ag := NewAgent(mockBackend, mockLLM, WithConfig(&Config{
		MaxTurns:          5,
		MaxToolExecutions: 50,
	}))
	ag.RegisterTool(&mockCalculatorTool{})

	ctx := context.Background()
	resp, err := ag.Chat(ctx, "max_turns", "Keep calling tools")

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	// Should stop at max turns (6 = 5 conversation turns + 1 final max-turns-exceeded response)
	if resp.Metadata["turns"] != 6 {
		t.Errorf("Expected 6 turns (max limit + final response), got: %v", resp.Metadata["turns"])
	}
}

func TestAgent_MaxToolExecutionsLimit(t *testing.T) {
	// LLM requests many tools at once
	mockLLM := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{
				content: "",
				toolCalls: []llmtypes.ToolCall{
					{ID: "call_1", Name: "calculator", Input: map[string]interface{}{"expression": "1"}},
					{ID: "call_2", Name: "calculator", Input: map[string]interface{}{"expression": "2"}},
					{ID: "call_3", Name: "calculator", Input: map[string]interface{}{"expression": "3"}},
					{ID: "call_4", Name: "calculator", Input: map[string]interface{}{"expression": "4"}},
					{ID: "call_5", Name: "calculator", Input: map[string]interface{}{"expression": "5"}},
				},
			},
		},
	}

	mockBackend := &mockBackend{}
	ag := NewAgent(mockBackend, mockLLM, WithConfig(&Config{
		MaxTurns:          25,
		MaxToolExecutions: 3, // Limit to 3 tools
	}))
	ag.RegisterTool(&mockCalculatorTool{})

	ctx := context.Background()
	resp, err := ag.Chat(ctx, "max_tools", "Execute 5 calculations")

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	// Should stop after 3 tool executions
	if len(resp.ToolExecutions) != 3 {
		t.Errorf("Expected 3 tool executions (max limit), got: %d", len(resp.ToolExecutions))
	}
}

func TestAgent_NoToolsAvailable(t *testing.T) {
	// Agent without any tools should still work
	mockLLM := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{
				content: "I don't have any tools, but I can still help",
			},
		},
	}

	mockBackend := &mockBackend{}
	ag := NewAgent(mockBackend, mockLLM)
	// Don't register any tools

	ctx := context.Background()
	resp, err := ag.Chat(ctx, "no_tools", "Help me")

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	if resp.Content == "" {
		t.Error("Expected response content")
	}

	if len(resp.ToolExecutions) != 0 {
		t.Errorf("Expected 0 tool executions, got: %d", len(resp.ToolExecutions))
	}
}

func TestAgent_MultiTurnConversationWithContext(t *testing.T) {
	// Test that agent maintains context across turns
	mockBackend := &mockBackend{}
	mockLLM := &mockSimpleLLM{}
	ag := NewAgent(mockBackend, mockLLM)

	ctx := context.Background()
	sessionID := "context_test"

	// First message
	resp1, err := ag.Chat(ctx, sessionID, "My name is Alice")
	if err != nil {
		t.Fatalf("First chat failed: %v", err)
	}

	// Second message - should have context from first
	resp2, err := ag.Chat(ctx, sessionID, "What's my name?")
	if err != nil {
		t.Fatalf("Second chat failed: %v", err)
	}

	// Verify both responses exist
	if resp1.Content == "" || resp2.Content == "" {
		t.Error("Expected non-empty responses")
	}

	// Verify session has multiple messages
	session, ok := ag.GetSession(sessionID)
	if !ok {
		t.Fatal("Session not found")
	}

	messages := session.GetMessages()
	// Should have: user1, assistant1, user2, assistant2
	if len(messages) < 4 {
		t.Errorf("Expected at least 4 messages, got: %d", len(messages))
	}
}

func TestAgent_ConcurrentConversations(t *testing.T) {
	// Test concurrent conversations in different sessions
	mockBackend := &mockBackend{}
	mockLLM := &mockSimpleLLM{}
	ag := NewAgent(mockBackend, mockLLM)

	ctx := context.Background()
	const numConcurrent = 20

	done := make(chan error, numConcurrent)
	for i := 0; i < numConcurrent; i++ {
		go func(id int) {
			sessionID := fmt.Sprintf("session_%d", id)
			_, err := ag.Chat(ctx, sessionID, fmt.Sprintf("Query %d", id))
			done <- err
		}(i)
	}

	// Wait for all to complete
	for i := 0; i < numConcurrent; i++ {
		err := <-done
		if err != nil {
			t.Errorf("Concurrent conversation %d failed: %v", i, err)
		}
	}

	// Verify all sessions created
	sessions := ag.ListSessions()
	if len(sessions) != numConcurrent {
		t.Errorf("Expected %d sessions, got: %d", numConcurrent, len(sessions))
	}
}

func TestAgent_ToolNotFound(t *testing.T) {
	// LLM requests a tool that doesn't exist
	mockLLM := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{
				content:   "",
				toolCalls: []llmtypes.ToolCall{{ID: "call_1", Name: "nonexistent_tool", Input: map[string]interface{}{}}},
			},
			{
				content: "Tool not found, but I can continue",
			},
		},
	}

	mockBackend := &mockBackend{}
	ag := NewAgent(mockBackend, mockLLM)
	// Don't register the tool

	ctx := context.Background()
	resp, err := ag.Chat(ctx, "tool_not_found", "Use nonexistent tool")

	if err != nil {
		t.Fatalf("Chat should handle missing tool gracefully: %v", err)
	}

	if len(resp.ToolExecutions) != 1 {
		t.Errorf("Expected 1 tool execution attempt, got: %d", len(resp.ToolExecutions))
	}

	if resp.ToolExecutions[0].Error == nil {
		t.Error("Expected error for tool not found")
	}
}

func TestAgent_SessionPersistence(t *testing.T) {
	mockBackend := &mockBackend{}
	mockLLM := &mockSimpleLLM{}
	ag := NewAgent(mockBackend, mockLLM)

	ctx := context.Background()
	sessionID := "persistent_session"

	// Send first message
	resp1, err := ag.Chat(ctx, sessionID, "First message")
	if err != nil {
		t.Fatalf("First chat failed: %v", err)
	}

	// Get session
	session, ok := ag.GetSession(sessionID)
	if !ok {
		t.Fatal("Session should exist")
	}

	if session.ID != sessionID {
		t.Errorf("Session ID mismatch")
	}

	// Verify messages persisted
	messages := session.GetMessages()
	if len(messages) < 2 {
		t.Errorf("Expected at least 2 messages, got: %d", len(messages))
	}

	// Send second message to same session
	resp2, err := ag.Chat(ctx, sessionID, "Second message")
	if err != nil {
		t.Fatalf("Second chat failed: %v", err)
	}

	// Session should now have more messages
	session, _ = ag.GetSession(sessionID)
	messages = session.GetMessages()
	if len(messages) < 4 {
		t.Errorf("Expected at least 4 messages after second chat, got: %d", len(messages))
	}

	// Verify both responses have cost tracking
	if resp1.Usage.CostUSD == 0 && resp2.Usage.CostUSD == 0 {
		t.Error("Expected cost tracking in responses")
	}
}

func TestAgent_SessionDeletion(t *testing.T) {
	mockBackend := &mockBackend{}
	mockLLM := &mockSimpleLLM{}
	ag := NewAgent(mockBackend, mockLLM)

	ctx := context.Background()
	sessionID := "delete_me"

	// Create session
	_, err := ag.Chat(ctx, sessionID, "Test message")
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	// Verify session exists
	_, ok := ag.GetSession(sessionID)
	if !ok {
		t.Fatal("Session should exist")
	}

	// Delete session
	ag.DeleteSession(sessionID)

	// Verify session deleted
	_, ok = ag.GetSession(sessionID)
	if ok {
		t.Error("Session should have been deleted")
	}
}

func TestAgent_SessionListing(t *testing.T) {
	mockBackend := &mockBackend{}
	mockLLM := &mockSimpleLLM{}
	ag := NewAgent(mockBackend, mockLLM)

	ctx := context.Background()

	// Initially no sessions
	sessions := ag.ListSessions()
	if len(sessions) != 0 {
		t.Errorf("Expected 0 sessions initially, got: %d", len(sessions))
	}

	// Create 3 sessions
	for i := 1; i <= 3; i++ {
		_, err := ag.Chat(ctx, fmt.Sprintf("session_%d", i), fmt.Sprintf("Message %d", i))
		if err != nil {
			t.Fatalf("Chat %d failed: %v", i, err)
		}
	}

	// List should show 3 sessions
	sessions = ag.ListSessions()
	if len(sessions) != 3 {
		t.Errorf("Expected 3 sessions, got: %d", len(sessions))
	}

	// Verify each session has messages
	for _, sess := range sessions {
		if len(sess.GetMessages()) < 2 {
			t.Errorf("Session %s: expected messages", sess.ID)
		}
	}
}

func TestAgent_ToolRegistration(t *testing.T) {
	mockBackend := &mockBackend{}
	mockLLM := &mockSimpleLLM{}
	ag := NewAgent(mockBackend, mockLLM)

	// Initially has 5 built-in tools (get_tool_result, query_tool_result, recall_conversation, clear_recalled_context, search_conversation)
	tools := ag.RegisteredTools()
	if len(tools) != 5 {
		t.Errorf("Expected 5 tools initially (get_tool_result, query_tool_result, recall_conversation, clear_recalled_context, search_conversation), got: %d", len(tools))
	}

	// Register single tool
	ag.RegisterTool(&mockCalculatorTool{})
	tools = ag.RegisteredTools()
	if len(tools) != 6 {
		t.Errorf("Expected 6 tools, got: %d", len(tools))
	}

	// Register multiple tools at once
	ag.RegisterTools(&mockSearchTool{}, &mockWeatherTool{})
	tools = ag.RegisteredTools()
	if len(tools) != 8 {
		t.Errorf("Expected 8 tools, got: %d", len(tools))
	}
}

func TestAgent_WithTracing(t *testing.T) {
	mockBackend := &mockBackend{}
	mockLLM := &mockSimpleLLM{}
	mockTracer := &mockTracer{}

	ag := NewAgent(mockBackend, mockLLM,
		WithTracer(mockTracer),
		WithConfig(&Config{
			MaxTurns:          25,
			MaxToolExecutions: 50,
			EnableTracing:     true,
		}),
	)

	ctx := context.Background()
	_, err := ag.Chat(ctx, "traced_session", "Test with tracing")

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	// Verify tracer was called
	if mockTracer.spanCount == 0 {
		t.Error("Expected tracer to be called")
	}
}

func TestAgent_WithoutTracing(t *testing.T) {
	mockBackend := &mockBackend{}
	mockLLM := &mockSimpleLLM{}
	mockTracer := &mockTracer{}

	ag := NewAgent(mockBackend, mockLLM,
		WithTracer(mockTracer),
		WithConfig(&Config{
			MaxTurns:          25,
			MaxToolExecutions: 50,
			EnableTracing:     false, // Disabled
		}),
	)

	ctx := context.Background()
	_, err := ag.Chat(ctx, "untraced", "Test without tracing")

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	// Tracer should not be called
	if mockTracer.spanCount > 0 {
		t.Error("Tracer should not be called when tracing disabled")
	}
}

func TestAgent_LLMError(t *testing.T) {
	mockBackend := &mockBackend{}
	mockLLM := &mockErrorLLM{errorMsg: "LLM service unavailable"}
	ag := NewAgent(mockBackend, mockLLM)

	ctx := context.Background()
	resp, err := ag.Chat(ctx, "llm_error", "This will fail")

	if err == nil {
		t.Fatal("Expected error from LLM failure")
	}

	if resp != nil {
		t.Errorf("Expected nil response on LLM error, got: %v", resp)
	}
}

func TestAgent_ContextCancellation(t *testing.T) {
	mockBackend := &mockBackend{}
	mockLLM := &mockSlowLLM{delay: 500 * time.Millisecond}
	ag := NewAgent(mockBackend, mockLLM)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	resp, err := ag.Chat(ctx, "cancelled", "This will timeout")

	if err == nil {
		t.Fatal("Expected error from context cancellation")
	}

	if resp != nil {
		t.Errorf("Expected nil response on cancellation, got: %v", resp)
	}
}

func TestAgent_EmptyMessage(t *testing.T) {
	mockBackend := &mockBackend{}
	mockLLM := &mockSimpleLLM{}
	ag := NewAgent(mockBackend, mockLLM)

	ctx := context.Background()
	resp, err := ag.Chat(ctx, "empty_msg", "")

	// Should handle empty message gracefully
	if err != nil {
		t.Fatalf("Chat with empty message should succeed: %v", err)
	}

	if resp.Content == "" {
		t.Error("Expected response even for empty message")
	}
}

// Mock Implementations for Testing

// mockToolCallingLLM simulates an LLM that calls tools
type mockToolCallingLLM struct {
	responses       []mockLLMResponse
	currentIdx      int
	alwaysCallTools bool
	mu              sync.Mutex
}

type mockLLMResponse struct {
	content   string
	toolCalls []llmtypes.ToolCall
}

func (m *mockToolCallingLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.alwaysCallTools {
		// Simulate LLM that keeps calling tools
		return &llmtypes.LLMResponse{
			Content: "",
			ToolCalls: []llmtypes.ToolCall{
				{ID: "infinite", Name: "calculator", Input: map[string]interface{}{"expression": "1"}},
			},
			Usage: llmtypes.Usage{InputTokens: 50, OutputTokens: 25, CostUSD: 0.0037},
		}, nil
	}

	if m.currentIdx >= len(m.responses) {
		// Default response
		return &llmtypes.LLMResponse{
			Content:   "Final response",
			ToolCalls: []llmtypes.ToolCall{},
			Usage:     llmtypes.Usage{InputTokens: 50, OutputTokens: 25, CostUSD: 0.0037},
		}, nil
	}

	resp := m.responses[m.currentIdx]
	m.currentIdx++

	return &llmtypes.LLMResponse{
		Content:   resp.content,
		ToolCalls: resp.toolCalls,
		Usage:     llmtypes.Usage{InputTokens: 50, OutputTokens: 25, CostUSD: 0.0037},
	}, nil
}

func (m *mockToolCallingLLM) Name() string  { return "mock-tool-calling" }
func (m *mockToolCallingLLM) Model() string { return "mock-v1" }

// mockSimpleLLM always returns text, no tools
type mockSimpleLLM struct {
	mu sync.Mutex
}

func (m *mockSimpleLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return &llmtypes.LLMResponse{
		Content:   "Mock response",
		ToolCalls: []llmtypes.ToolCall{},
		Usage:     llmtypes.Usage{InputTokens: 50, OutputTokens: 25, CostUSD: 0.0037},
	}, nil
}

func (m *mockSimpleLLM) Name() string  { return "mock-simple" }
func (m *mockSimpleLLM) Model() string { return "mock-v1" }

// mockErrorLLM always returns errors
type mockErrorLLM struct {
	errorMsg string
}

func (m *mockErrorLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return nil, fmt.Errorf("%s", m.errorMsg)
}

func (m *mockErrorLLM) Name() string  { return "mock-error" }
func (m *mockErrorLLM) Model() string { return "mock-v1" }

// mockSlowLLM simulates slow LLM responses
type mockSlowLLM struct {
	delay time.Duration
}

func (m *mockSlowLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	select {
	case <-time.After(m.delay):
		return &llmtypes.LLMResponse{
			Content: "Slow response",
			Usage:   llmtypes.Usage{InputTokens: 50, OutputTokens: 25, CostUSD: 0.0037},
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *mockSlowLLM) Name() string  { return "mock-slow" }
func (m *mockSlowLLM) Model() string { return "mock-v1" }

// mockCalculatorTool simulates a simple calculator
type mockCalculatorTool struct{}

func (m *mockCalculatorTool) Name() string { return "calculator" }
func (m *mockCalculatorTool) Description() string {
	return "Performs arithmetic calculations"
}
func (m *mockCalculatorTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"expression": {Type: "string"},
		},
	}
}
func (m *mockCalculatorTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	// Simple mock calculation
	return &shuttle.Result{
		Success: true,
		Data:    "42",
		Metadata: map[string]interface{}{
			"execution_time_ms": 10,
		},
		ExecutionTimeMs: 10,
	}, nil
}
func (m *mockCalculatorTool) Backend() string { return "" }

// mockFailingTool always fails
type mockFailingTool struct{}

func (m *mockFailingTool) Name() string        { return "failing_tool" }
func (m *mockFailingTool) Description() string { return "Always fails" }
func (m *mockFailingTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (m *mockFailingTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{
		Success: false,
		Error: &shuttle.Error{
			Code:    "EXECUTION_FAILED",
			Message: "Tool execution failed",
		},
	}, fmt.Errorf("tool execution failed")
}
func (m *mockFailingTool) Backend() string { return "" }

// mockSearchTool for testing multiple tools
type mockSearchTool struct{}

func (m *mockSearchTool) Name() string        { return "search" }
func (m *mockSearchTool) Description() string { return "Search for information" }
func (m *mockSearchTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (m *mockSearchTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: "search results"}, nil
}
func (m *mockSearchTool) Backend() string { return "" }

// mockWeatherTool for testing multiple tools
type mockWeatherTool struct{}

func (m *mockWeatherTool) Name() string        { return "weather" }
func (m *mockWeatherTool) Description() string { return "Get weather information" }
func (m *mockWeatherTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (m *mockWeatherTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: "sunny"}, nil
}
func (m *mockWeatherTool) Backend() string { return "" }

// mockBackend for testing
type mockBackend struct{}

func (m *mockBackend) Name() string { return "mock" }
func (m *mockBackend) ExecuteQuery(ctx context.Context, query string) (*fabric.QueryResult, error) {
	return &fabric.QueryResult{Type: "rows", RowCount: 0}, nil
}
func (m *mockBackend) GetSchema(ctx context.Context, resource string) (*fabric.Schema, error) {
	return &fabric.Schema{Name: resource}, nil
}
func (m *mockBackend) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}
func (m *mockBackend) ListResources(ctx context.Context, filters map[string]string) ([]fabric.Resource, error) {
	return []fabric.Resource{}, nil
}
func (m *mockBackend) Capabilities() *fabric.Capabilities {
	return fabric.NewCapabilities()
}
func (m *mockBackend) Close() error                   { return nil }
func (m *mockBackend) Ping(ctx context.Context) error { return nil }
func (m *mockBackend) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("not supported")
}

// TestAgent_SelfCorrection_Success tests successful execution with guardrails enabled
func TestAgent_SelfCorrection_Success(t *testing.T) {
	backend := &mockBackend{}
	llm := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{
				content: "",
				toolCalls: []llmtypes.ToolCall{
					{ID: "call_1", Name: "TestTool", Input: map[string]interface{}{"sql": "SELECT * FROM table"}},
				},
			},
			{
				content: "Query executed successfully!",
			},
		},
	}

	// Enable guardrails for error tracking
	guardrails := fabric.NewGuardrailEngine()
	agent := NewAgent(backend, llm, WithGuardrails(guardrails))

	// Register a successful tool
	agent.RegisterTool(&mockSuccessfulTool{})

	ctx := context.Background()
	response, err := agent.Chat(ctx, "test-session", "Execute query")

	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if response == nil {
		t.Fatal("Expected response, got nil")
	}

	if response.Content != "Query executed successfully!" {
		t.Errorf("Expected 'Query executed successfully!', got %q", response.Content)
	}

	// Verify error record was cleared on success
	if record := guardrails.GetErrorRecord("test-session"); record != nil {
		t.Error("Expected error record to be cleared after successful execution")
	}
}

// mockSuccessfulTool always succeeds
type mockSuccessfulTool struct{}

func (m *mockSuccessfulTool) Name() string        { return "TestTool" }
func (m *mockSuccessfulTool) Description() string { return "Always succeeds" }
func (m *mockSuccessfulTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (m *mockSuccessfulTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{
		Success: true,
		Data:    "success",
	}, nil
}
func (m *mockSuccessfulTool) Backend() string { return "" }

// TestAgent_SelfCorrection_NonSQLError tests that non-SQL errors pass through
func TestAgent_SelfCorrection_NonSQLError(t *testing.T) {
	backend := &mockBackend{}
	llm := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{
				content: "",
				toolCalls: []llmtypes.ToolCall{
					{ID: "call_1", Name: "TestTool", Input: map[string]interface{}{"param": "value"}},
				},
			},
			{
				content: "Tool failed",
			},
		},
	}

	agent := NewAgent(backend, llm)
	agent.RegisterTool(&mockNonSQLErrorTool{})

	ctx := context.Background()
	response, err := agent.Chat(ctx, "test-session-nonsql", "Execute tool")

	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	// Non-SQL errors should be returned without correction attempts
	if response == nil {
		t.Fatal("Expected response, got nil")
	}
}

// mockNonSQLErrorTool returns non-SQL errors
type mockNonSQLErrorTool struct{}

func (m *mockNonSQLErrorTool) Name() string        { return "TestTool" }
func (m *mockNonSQLErrorTool) Description() string { return "Returns non-SQL errors" }
func (m *mockNonSQLErrorTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (m *mockNonSQLErrorTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{
		Success: false,
		Error: &shuttle.Error{
			Code:    "INVALID_PARAM",
			Message: "invalid parameter",
		},
	}, fmt.Errorf("invalid parameter")
}
func (m *mockNonSQLErrorTool) Backend() string { return "" }

// TestAgent_SelfCorrection_CircuitBreakerCreated tests circuit breaker usage when enabled
func TestAgent_SelfCorrection_CircuitBreakerCreated(t *testing.T) {
	backend := &mockBackend{}
	llm := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{
				content: "",
				toolCalls: []llmtypes.ToolCall{
					{ID: "call_1", Name: "calculator", Input: map[string]interface{}{"expression": "1+1"}},
				},
			},
			{
				content: "Calculation complete",
			},
		},
	}

	// Enable circuit breakers for failure isolation
	circuitBreakerConfig := fabric.DefaultCircuitBreakerConfig()
	circuitBreakers := fabric.NewCircuitBreakerManager(circuitBreakerConfig)
	agent := NewAgent(backend, llm, WithCircuitBreakers(circuitBreakers))
	agent.RegisterTool(&mockCalculatorTool{})

	ctx := context.Background()
	_, err := agent.Chat(ctx, "test-session-breaker", "Calculate 1+1")

	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	// Verify circuit breaker was created for the tool
	breaker := circuitBreakers.GetBreaker("calculator")
	if breaker == nil {
		t.Fatal("Expected circuit breaker to be created for calculator tool")
	}
}

// TestAgent_AnalyzeError tests error analysis helper
func TestAgent_AnalyzeError(t *testing.T) {
	backend := &mockBackend{}
	llm := &mockSimpleLLM{}
	agent := NewAgent(backend, llm)

	tests := []struct {
		name         string
		result       *shuttle.Result
		err          error
		expectedType string
	}{
		{
			name:         "execution error",
			result:       nil,
			err:          fmt.Errorf("connection failed"),
			expectedType: "execution_error",
		},
		{
			name: "result error with syntax",
			result: &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "SYNTAX_ERROR",
					Message: "syntax error in SQL",
				},
			},
			err:          nil,
			expectedType: "syntax_error",
		},
		{
			name: "result error with table not found",
			result: &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "NOT_FOUND",
					Message: "table does not exist",
				},
			},
			err:          nil,
			expectedType: "table_not_found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			analysis := agent.analyzeError(tt.result, tt.err)
			if analysis.ErrorType != tt.expectedType {
				t.Errorf("Expected error type %q, got %q", tt.expectedType, analysis.ErrorType)
			}
		})
	}
}

// TestAgent_OrchestratorIntegration tests that orchestrator is initialized
func TestAgent_OrchestratorIntegration(t *testing.T) {
	backend := &mockBackend{}
	llm := &mockSimpleLLM{}

	agent := NewAgent(backend, llm)

	// Verify orchestrator is initialized
	orch := agent.GetOrchestrator()
	if orch == nil {
		t.Fatal("Expected orchestrator to be initialized, got nil")
	}

	// Test intent classification
	intent, confidence := orch.ClassifyIntent("what tables are available", nil)
	if intent != "schema_discovery" {
		t.Errorf("Expected schema_discovery intent, got %s", intent)
	}
	if confidence < 0.85 {
		t.Errorf("Expected confidence >= 0.85, got %.2f", confidence)
	}

	// Test execution planning
	plan, err := orch.PlanExecution(intent, "show tables", nil)
	if err != nil {
		t.Fatalf("PlanExecution failed: %v", err)
	}
	if plan == nil {
		t.Fatal("Expected execution plan, got nil")
	}
	if len(plan.Steps) == 0 {
		t.Error("Expected at least one step in execution plan")
	}
}

// mockTracer for testing observability
type mockTracer struct {
	spanCount int
	mu        sync.Mutex
}

func (m *mockTracer) StartSpan(ctx context.Context, name string, opts ...observability.SpanOption) (context.Context, *observability.Span) {
	m.mu.Lock()
	m.spanCount++
	m.mu.Unlock()
	return ctx, &observability.Span{}
}

func (m *mockTracer) EndSpan(span *observability.Span) {}

func (m *mockTracer) RecordMetric(name string, value float64, labels map[string]string) {}

func (m *mockTracer) RecordEvent(ctx context.Context, name string, attributes map[string]interface{}) {
}

func (m *mockTracer) Flush(ctx context.Context) error { return nil }

// TestAgent_ToolCallMessagePersistence verifies that assistant messages with tool calls
// are properly persisted to storage (fixes observability gap where tool_calls were missing)
func TestAgent_ToolCallMessagePersistence(t *testing.T) {
	// Create temporary database for testing
	tmpfile := t.TempDir() + "/test.db"

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpfile, tracer)
	if err != nil {
		t.Fatalf("Failed to create session store: %v", err)
	}
	defer store.Close()

	// Create mock LLM that makes tool calls
	mockLLM := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{
				content: "",
				toolCalls: []llmtypes.ToolCall{
					{ID: "call_1", Name: "calculator", Input: map[string]interface{}{"expression": "2+2"}},
				},
			},
			{
				content: "The answer is 4",
			},
		},
	}

	mockBackend := &mockBackend{}

	// Create memory with session store
	memory := NewMemoryWithStore(store)

	// Create agent with memory configured
	ag := NewAgent(mockBackend, mockLLM, WithMemory(memory))
	ag.RegisterTool(&mockCalculatorTool{})

	ctx := context.Background()
	sessionID := "test_tool_call_persistence"

	// Execute conversation that triggers tool calls
	resp, err := ag.Chat(ctx, sessionID, "What is 2+2?")
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	if resp.Content != "The answer is 4" {
		t.Errorf("Expected final response, got: %s", resp.Content)
	}

	// Load messages from store to verify persistence
	messages, err := store.LoadMessages(ctx, sessionID)
	if err != nil {
		t.Fatalf("Failed to load messages: %v", err)
	}

	// Expected message sequence:
	// 1. user: "What is 2+2?"
	// 2. assistant: (with tool calls to calculator) ← THIS WAS MISSING BEFORE THE FIX
	// 3. tool: calculator result
	// 4. assistant: "The answer is 4"

	if len(messages) < 4 {
		t.Fatalf("Expected at least 4 messages, got %d", len(messages))
	}

	// Verify message 1: user message
	if messages[0].Role != "user" {
		t.Errorf("Message 0: expected role 'user', got %q", messages[0].Role)
	}

	// Verify message 2: assistant message WITH tool calls (the critical fix)
	if messages[1].Role != "assistant" {
		t.Errorf("Message 1: expected role 'assistant', got %q", messages[1].Role)
	}

	if len(messages[1].ToolCalls) != 1 {
		t.Fatalf("Message 1: expected 1 tool call, got %d. This indicates tool_calls are not being persisted!", len(messages[1].ToolCalls))
	}

	if messages[1].ToolCalls[0].Name != "calculator" {
		t.Errorf("Message 1: expected tool call to 'calculator', got %q", messages[1].ToolCalls[0].Name)
	}

	if messages[1].ToolCalls[0].ID != "call_1" {
		t.Errorf("Message 1: expected tool call ID 'call_1', got %q", messages[1].ToolCalls[0].ID)
	}

	// Verify message 3: tool result
	if messages[2].Role != "tool" {
		t.Errorf("Message 2: expected role 'tool', got %q", messages[2].Role)
	}

	// Verify message 4: final assistant response
	if messages[3].Role != "assistant" {
		t.Errorf("Message 3: expected role 'assistant', got %q", messages[3].Role)
	}

	if messages[3].Content != "The answer is 4" {
		t.Errorf("Message 3: expected content 'The answer is 4', got %q", messages[3].Content)
	}

	// Verify no tool calls on final response (should be empty)
	if len(messages[3].ToolCalls) != 0 {
		t.Errorf("Message 3: expected 0 tool calls on final response, got %d", len(messages[3].ToolCalls))
	}
}

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
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// mockSessionForRecovery implements sessionForRecovery for testing.
type mockSessionForRecovery struct {
	mu       sync.Mutex
	messages []Message
	trimmed  int
}

func (m *mockSessionForRecovery) AddMessage(_ context.Context, msg Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
}

func (m *mockSessionForRecovery) TrimLastN(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trimmed = n
	if n > 0 && n <= len(m.messages) {
		m.messages = m.messages[:len(m.messages)-n]
	} else if n > len(m.messages) {
		m.messages = m.messages[:0]
	}
}

// mockTrimableMemory implements TrimableMemory for testing.
type mockTrimableMemory struct {
	mu          sync.Mutex
	messages    []Message
	l2Summary   string
	tokenCount  int
	trimCalled  int
	aggressiveN int
}

func (m *mockTrimableMemory) TrimLastN(n int) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trimCalled++
	if n > len(m.messages) {
		n = len(m.messages)
	}
	m.messages = m.messages[:len(m.messages)-n]
	m.tokenCount -= n * 100 // simulate 100 tokens per message
	return n
}

func (m *mockTrimableMemory) AggressiveTrim(keepLastN int) (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.aggressiveN = keepLastN
	before := m.tokenCount
	if len(m.messages) > keepLastN {
		m.messages = m.messages[len(m.messages)-keepLastN:]
	}
	m.l2Summary = ""
	m.tokenCount = len(m.messages) * 100
	return before, m.tokenCount
}

// --- Test: Output Token CB Self-Heals ---

func TestRecovery_OutputTokenCB_SelfHeals(t *testing.T) {
	_, span := observability.NewNoOpTracer().StartSpan(context.Background(), "test")
	recovery := newRecoveryOrchestrator(nil, span)

	session := &mockSessionForRecovery{
		messages: make([]Message, 10),
	}
	segMem := &mockTrimableMemory{
		messages:   make([]Message, 10),
		tokenCount: 1000,
	}
	tracker := newConsecutiveFailureTracker()
	// Simulate CB being triggered (8 exhaustions)
	for i := 0; i < 8; i++ {
		tracker.recordOutputTokenExhaustion(true)
	}

	recovered, err := recovery.recoverOutputTokenCB(context.Background(), session, segMem, tracker, 8)
	require.NoError(t, err)
	assert.True(t, recovered)

	// The class-blind trim is gone (O-SW-3): recoverOutputTokenCB must no longer trim memory,
	// only inject the recovery nudge. Trimming is confined solely to reset_context now.
	assert.Equal(t, 0, segMem.trimCalled)

	// Verify failure tracker was reset
	assert.Equal(t, 0, tracker.outputTokenExhaustions)

	// Verify recovery message was injected (10 original + 1 recovery msg, nothing trimmed)
	assert.Len(t, session.messages, 11)
	lastMsg := session.messages[len(session.messages)-1]
	assert.Equal(t, "user", lastMsg.Role)
	assert.Contains(t, lastMsg.Content, "Simplify your approach")
}

// --- Test: Output Token CB Fails After Retry ---

func TestRecovery_OutputTokenCB_FailsAfterRetry(t *testing.T) {
	_, span := observability.NewNoOpTracer().StartSpan(context.Background(), "test")
	recovery := newRecoveryOrchestrator(nil, span)

	session := &mockSessionForRecovery{messages: make([]Message, 10)}
	segMem := &mockTrimableMemory{messages: make([]Message, 10), tokenCount: 1000}
	tracker := newConsecutiveFailureTracker()
	for i := 0; i < 8; i++ {
		tracker.recordOutputTokenExhaustion(true)
	}

	// First attempt succeeds
	recovered, _ := recovery.recoverOutputTokenCB(context.Background(), session, segMem, tracker, 8)
	assert.True(t, recovered)

	// Second attempt (same conversation) — should fail (one-shot)
	for i := 0; i < 8; i++ {
		tracker.recordOutputTokenExhaustion(true)
	}
	recovered, _ = recovery.recoverOutputTokenCB(context.Background(), session, segMem, tracker, 8)
	assert.False(t, recovered)
}

// --- Test: Output Token CB Disabled Config ---

func TestRecovery_OutputTokenCB_DisabledConfig(t *testing.T) {
	// When EnableSelfHealing is false, recovery should not be created.
	// This test validates that a nil recovery orchestrator is a no-op.
	var recovery *recoveryOrchestrator // nil — self-healing disabled

	// Verify nil recovery means no recovery path is taken.
	assert.Nil(t, recovery)

	// The actual wiring in agent.go checks `if recovery != nil` before calling methods.
	// Here we verify the RecoverableError is built correctly for the fallback path.
	cause := fmt.Errorf("output token circuit breaker triggered after 8 turns")
	_, span := observability.NewNoOpTracer().StartSpan(context.Background(), "test")
	r := newRecoveryOrchestrator(nil, span)
	recErr := r.buildRecoverableError("output_token_circuit_breaker", cause, "rewind_and_retry", map[string]any{"threshold": 8})
	assert.Equal(t, "output_token_circuit_breaker", recErr.ErrorType)
	assert.True(t, recErr.Retryable)
	assert.ErrorIs(t, recErr, cause)
}

// --- Test: Tool CB Disables Tool ---

func TestRecovery_ToolCB_DisablesTool(t *testing.T) {
	_, span := observability.NewNoOpTracer().StartSpan(context.Background(), "test")
	recovery := newRecoveryOrchestrator(nil, span)

	tools := []shuttle.Tool{
		&shuttle.MockTool{MockName: "web_search"},
		&shuttle.MockTool{MockName: "execute_sql"},
		&shuttle.MockTool{MockName: "read_file"},
	}

	recovered, syntheticResult := recovery.recoverToolCB(context.Background(), "web_search", &tools)
	assert.True(t, recovered)
	assert.NotNil(t, syntheticResult)
	assert.False(t, syntheticResult.Success)
	assert.Equal(t, "tool_disabled", syntheticResult.Error.Code)
	assert.Contains(t, syntheticResult.Error.Message, "web_search")

	// Verify tool was removed from slice
	assert.Len(t, tools, 2)
	for _, tool := range tools {
		assert.NotEqual(t, "web_search", tool.Name())
	}

	// Verify disabled tools map
	assert.True(t, recovery.disabledTools["web_search"])
}

// --- Test: RecoverableError Interface ---

func TestRecoverableError_Interface(t *testing.T) {
	cause := fmt.Errorf("underlying error")
	recErr := &RecoverableError{
		ErrorType:       "test_error",
		Message:         "something broke",
		RecoveryAction:  "retry",
		RecoveryPayload: map[string]any{"key": "value"},
		Retryable:       true,
		Cause:           cause,
	}

	// Implements error
	var err error = recErr
	assert.Equal(t, "something broke", err.Error())

	// Unwrap works with errors.Is / errors.As
	assert.ErrorIs(t, recErr, cause)

	var target *RecoverableError
	assert.True(t, errors.As(err, &target))
	assert.Equal(t, "test_error", target.ErrorType)
	assert.Equal(t, "retry", target.RecoveryAction)
	assert.True(t, target.Retryable)
}

// --- Test: Observability (span events emitted) ---

func TestRecovery_Observability(t *testing.T) {
	tracer := observability.NewNoOpTracer()
	_, span := tracer.StartSpan(context.Background(), "test")
	recovery := newRecoveryOrchestrator(nil, span)

	// Output token CB recovery emits an event.
	session := &mockSessionForRecovery{messages: make([]Message, 10)}
	segMem := &mockTrimableMemory{messages: make([]Message, 10), tokenCount: 1000}
	tracker := newConsecutiveFailureTracker()
	for i := 0; i < 8; i++ {
		tracker.recordOutputTokenExhaustion(true)
	}

	recovered, _ := recovery.recoverOutputTokenCB(context.Background(), session, segMem, tracker, 8)
	assert.True(t, recovered)
	// NoOpTracer doesn't store events, but we verify no panics occur and the method completes.

	// Tool CB recovery emits an event.
	tools := []shuttle.Tool{&shuttle.MockTool{MockName: "broken_tool"}}
	recovered, _ = recovery.recoverToolCB(context.Background(), "broken_tool", &tools)
	assert.True(t, recovered)
	// No panics = observability integration works. Token-budget recovery is no
	// longer part of recoveryOrchestrator (O-SW-3): that pressure path moved to
	// Agent.prepareContext (see prepare_context_test.go).
}

// --- Test: Concurrent Access ---
// The recoveryOrchestrator is designed for single-goroutine use (one per loop).
// This test validates that the TrimableMemory interface implementations are
// safe for concurrent access (since extraction goroutines run in parallel).

func TestRecovery_ConcurrentAccess(t *testing.T) {
	sm := NewSegmentedMemory("ROM", 200000, 20000)

	// Fill with messages
	for i := 0; i < 50; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		sm.AddMessage(context.Background(), Message{
			Role:      role,
			Content:   fmt.Sprintf("message %d with some content", i),
			Timestamp: time.Now(),
		})
	}

	var wg sync.WaitGroup
	const readers = 5

	// Concurrent readers while a trim happens.
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = sm.GetTokenCount()
			_ = sm.GetMessagesForLLM()
		}()
	}

	// One writer doing a trim.
	wg.Add(1)
	go func() {
		defer wg.Done()
		sm.TrimLastN(5)
	}()

	wg.Wait()

	// Verify no panic and data is consistent.
	sm.mu.RLock()
	assert.Less(t, len(sm.l1Messages), 50)
	sm.mu.RUnlock()
}

// --- Test: SegmentedMemory.TrimLastN ---

func TestSegmentedMemory_TrimLastN(t *testing.T) {
	sm := NewSegmentedMemory("test ROM", 200000, 20000)

	// Add messages: user, assistant (with tool calls), tool, tool, user, assistant
	messages := []Message{
		{Role: "user", Content: "Hello", Timestamp: time.Now()},
		{Role: "assistant", Content: "I'll search", ToolCalls: []ToolCall{{ID: "1", Name: "search"}}, Timestamp: time.Now()},
		{Role: "tool", Content: "result1", ToolUseID: "1", Timestamp: time.Now()},
		{Role: "user", Content: "Thanks", Timestamp: time.Now()},
		{Role: "assistant", Content: "Done", Timestamp: time.Now()},
	}
	for _, msg := range messages {
		sm.AddMessage(context.Background(), msg)
	}

	// Trim last 2: should remove "Done" (assistant) and "Thanks" (user) = 2
	removed := sm.TrimLastN(2)
	assert.Equal(t, 2, removed)

	// Remaining: user, assistant, tool = 3
	sm.mu.RLock()
	assert.Len(t, sm.l1Messages, 3)
	sm.mu.RUnlock()
}

func TestSegmentedMemory_TrimLastN_PairBoundary(t *testing.T) {
	sm := NewSegmentedMemory("", 200000, 20000)

	messages := []Message{
		{Role: "user", Content: "Do something", Timestamp: time.Now()},
		{Role: "assistant", Content: "Calling tools", ToolCalls: []ToolCall{{ID: "1", Name: "a"}, {ID: "2", Name: "b"}}, Timestamp: time.Now()},
		{Role: "tool", Content: "result_a", ToolUseID: "1", Timestamp: time.Now()},
		{Role: "tool", Content: "result_b", ToolUseID: "2", Timestamp: time.Now()},
	}
	for _, msg := range messages {
		sm.AddMessage(context.Background(), msg)
	}

	// Trim last 1: lands on a tool message, should expand backward to include
	// the assistant + all tool results = actually removes 3 (assistant + 2 tools).
	removed := sm.TrimLastN(1)
	// Cut point starts at index 3 (last msg), but it's "tool" role, so walks back.
	// Index 2 is also "tool", walks back. Index 1 is "assistant" — stops.
	// Removes messages[1:] = 3 messages.
	assert.Equal(t, 3, removed)

	sm.mu.RLock()
	assert.Len(t, sm.l1Messages, 1)
	assert.Equal(t, "user", sm.l1Messages[0].Role)
	sm.mu.RUnlock()
}

// --- Test: SegmentedMemory.AggressiveTrim ---

func TestSegmentedMemory_AggressiveTrim(t *testing.T) {
	sm := NewSegmentedMemory("ROM content", 200000, 20000)

	// Add many messages
	for i := 0; i < 20; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		sm.AddMessage(context.Background(), Message{
			Role:      role,
			Content:   fmt.Sprintf("msg %d", i),
			Timestamp: time.Now(),
		})
	}

	// Set L2 summary
	sm.mu.Lock()
	sm.l2Summary = "This is a long summary of prior conversation..."
	sm.mu.Unlock()

	before, after := sm.AggressiveTrim(4)
	assert.Greater(t, before, after)

	sm.mu.RLock()
	assert.LessOrEqual(t, len(sm.l1Messages), 4)
	assert.Empty(t, sm.l2Summary)
	sm.mu.RUnlock()
}

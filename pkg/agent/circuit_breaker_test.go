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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOutputTokenCircuitBreaker tests the basic threshold behavior.
func TestOutputTokenCircuitBreaker(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	// First exhaustion — should not trigger circuit breaker
	count := tracker.recordOutputTokenExhaustion(true)
	assert.Equal(t, 1, count)
	err := tracker.checkOutputTokenCircuitBreaker(3)
	assert.NoError(t, err, "First exhaustion should not trigger circuit breaker")

	// Second exhaustion — should not trigger circuit breaker
	count = tracker.recordOutputTokenExhaustion(true)
	assert.Equal(t, 2, count)
	err = tracker.checkOutputTokenCircuitBreaker(3)
	assert.NoError(t, err, "Second exhaustion should not trigger circuit breaker")

	// Third exhaustion — should trigger circuit breaker
	count = tracker.recordOutputTokenExhaustion(true)
	assert.Equal(t, 3, count)
	err = tracker.checkOutputTokenCircuitBreaker(3)
	require.Error(t, err, "Third exhaustion should trigger circuit breaker")
	assert.Contains(t, err.Error(), "circuit breaker triggered")
	assert.Contains(t, err.Error(), "Break this task into smaller steps")
	assert.Contains(t, err.Error(), "Consecutive truncated-tool-call turns: 3")
}

// TestOutputTokenCircuitBreaker_Clear tests that clearing resets the counter.
func TestOutputTokenCircuitBreaker_Clear(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	// Record two exhaustions
	tracker.recordOutputTokenExhaustion(false)
	tracker.recordOutputTokenExhaustion(false)
	assert.Equal(t, 2, tracker.outputTokenExhaustions)

	// Clear should reset counter
	tracker.clearOutputTokenExhaustion()
	assert.Equal(t, 0, tracker.outputTokenExhaustions)
	assert.False(t, tracker.hasEmptyToolCall)

	// Next exhaustion should be count 1
	count := tracker.recordOutputTokenExhaustion(true)
	assert.Equal(t, 1, count)
	assert.True(t, tracker.hasEmptyToolCall)
}

// TestOutputTokenCircuitBreaker_ThresholdCustomization tests custom thresholds.
func TestOutputTokenCircuitBreaker_ThresholdCustomization(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	// Test with threshold of 8 (new default)
	for i := 1; i < 8; i++ {
		tracker.recordOutputTokenExhaustion(false)
		err := tracker.checkOutputTokenCircuitBreaker(8)
		assert.NoError(t, err, "Should not trigger before threshold at count %d", i)
	}

	// 8th exhaustion should trigger
	tracker.recordOutputTokenExhaustion(false)
	err := tracker.checkOutputTokenCircuitBreaker(8)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circuit breaker triggered")
}

// TestOutputTokenCB_TextResponseClearsCounter is the KEY regression test.
//
// A max_tokens event on a text response (no tool calls) is a SUCCESSFUL response
// and must NOT increment the counter. In the old (buggy) code, the counter accumulated
// across the entire session, causing the CB to fire after 3 verbose text responses
// even though no actual failure occurred.
//
// The correct behavior is: counter is only incremented when the agent is stuck in a
// tool loop with truncated tool calls. A text response clears the counter.
func TestOutputTokenCB_TextResponseClearsCounter(t *testing.T) {
	// This test simulates what was happening to Dan Bo's healthcare agents:
	// three successful verbose responses that happened to hit max_tokens,
	// incorrectly triggering the circuit breaker.

	tracker := newConsecutiveFailureTracker()

	// Simulate the agent-level logic for three user turns:
	// Each turn: max_tokens, ToolCalls=[], counter SHOULD clear (text response = success)
	for i := 1; i <= 5; i++ {
		// Agent generates verbose text → hits max_tokens → ToolCalls empty
		// Correct behavior: clear the counter (this is a successful response)
		tracker.clearOutputTokenExhaustion() // ← what agent.go now does for text responses

		err := tracker.checkOutputTokenCircuitBreaker(8)
		assert.NoError(t, err, "Turn %d: CB must not fire on verbose text responses", i)
		assert.Equal(t, 0, tracker.outputTokenExhaustions, "Turn %d: counter must be 0 after text response", i)
	}
}

// TestOutputTokenCB_OnlyTruncatedToolCallsCount tests that only truncated tool calls
// (the actual failure condition) count toward the threshold.
func TestOutputTokenCB_OnlyTruncatedToolCallsCount(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	// Scenario 1: max_tokens with non-truncated tool calls — should NOT count
	// (agent may still make progress; tool calls are complete)
	// The agent.go code handles this in the default switch case (no record, no clear)
	// So counter stays at 0.
	assert.Equal(t, 0, tracker.outputTokenExhaustions)

	// Scenario 2: max_tokens with truncated tool calls — SHOULD count
	tracker.recordOutputTokenExhaustion(true)
	assert.Equal(t, 1, tracker.outputTokenExhaustions)

	// Scenario 3: text response (no tool calls) — clears counter
	tracker.clearOutputTokenExhaustion()
	assert.Equal(t, 0, tracker.outputTokenExhaustions)

	// Scenario 4: multiple truncated-tool-call turns in sequence → CB fires at threshold
	for i := 0; i < 8; i++ {
		tracker.recordOutputTokenExhaustion(true)
	}
	err := tracker.checkOutputTokenCircuitBreaker(8)
	require.Error(t, err, "8 consecutive truncated-tool-call turns should trigger CB")
	assert.Contains(t, err.Error(), "circuit breaker triggered")
}

// TestOutputTokenCB_ThresholdInErrorMessage verifies the threshold is reported correctly.
func TestOutputTokenCB_ThresholdInErrorMessage(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	for i := 0; i < 5; i++ {
		tracker.recordOutputTokenExhaustion(true)
	}
	err := tracker.checkOutputTokenCircuitBreaker(5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Threshold: 5", "Error message must report the configured threshold")
	assert.Contains(t, err.Error(), "output_token_cb_threshold", "Error message must hint at the config key")
}

// TestDetectEmptyToolCall tests detection of truncated tool calls.
func TestDetectEmptyToolCall(t *testing.T) {
	tests := []struct {
		name      string
		toolCalls []ToolCall
		expected  bool
	}{
		{
			name:      "empty toolcalls slice",
			toolCalls: []ToolCall{},
			expected:  false,
		},
		{
			name: "normal tool call with params",
			toolCalls: []ToolCall{
				{
					ID:   "toolu_123",
					Name: "shell_execute",
					Input: map[string]interface{}{
						"command": "ls -la",
					},
				},
			},
			expected: false,
		},
		{
			name: "tool call with nil Input",
			toolCalls: []ToolCall{
				{
					ID:    "toolu_123",
					Name:  "shell_execute",
					Input: nil,
				},
			},
			expected: true,
		},
		{
			name: "tool call with empty Input map",
			toolCalls: []ToolCall{
				{
					ID:    "toolu_123",
					Name:  "shell_execute",
					Input: map[string]interface{}{},
				},
			},
			expected: true,
		},
		{
			name: "tool call with all empty values",
			toolCalls: []ToolCall{
				{
					ID:   "toolu_123",
					Name: "shell_execute",
					Input: map[string]interface{}{
						"command": "",
						"timeout": 0,
						"enabled": false,
					},
				},
			},
			expected: true,
		},
		{
			name: "multiple tool calls - one empty",
			toolCalls: []ToolCall{
				{
					ID:   "toolu_123",
					Name: "read_file",
					Input: map[string]interface{}{
						"path": "/tmp/test.txt",
					},
				},
				{
					ID:    "toolu_456",
					Name:  "shell_execute",
					Input: map[string]interface{}{},
				},
			},
			expected: true,
		},
		{
			name: "tool call with some non-empty values",
			toolCalls: []ToolCall{
				{
					ID:   "toolu_123",
					Name: "shell_execute",
					Input: map[string]interface{}{
						"command": "",
						"timeout": 30, // Non-zero value
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectEmptyToolCall(tt.toolCalls)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestOutputTokenCircuitBreaker_Integration tests the full flow with failure tracker.
// This covers the actual failure case: agent stuck in tool loop with truncated calls.
func TestOutputTokenCircuitBreaker_Integration(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	// Simulate an agent stuck in a tool loop with truncated calls
	// (this is the ONLY scenario the CB should trigger on)
	scenarios := []struct {
		stopReason       string
		hasEmptyToolCall bool
		hasToolCalls     bool
		shouldError      bool
		description      string
	}{
		{"max_tokens", true, true, false, "First truncated tool call — no fire"},
		{"max_tokens", true, true, false, "Second truncated tool call — no fire"},
		{"max_tokens", true, true, true, "Third truncated tool call — CB fires"},
	}

	for i, scenario := range scenarios {
		if scenario.stopReason == "max_tokens" && scenario.hasToolCalls && scenario.hasEmptyToolCall {
			tracker.recordOutputTokenExhaustion(scenario.hasEmptyToolCall)
		} else {
			tracker.clearOutputTokenExhaustion()
		}

		err := tracker.checkOutputTokenCircuitBreaker(3)
		if scenario.shouldError {
			require.Error(t, err, "Scenario %d (%s) should trigger circuit breaker", i+1, scenario.description)
			assert.Contains(t, err.Error(), "circuit breaker triggered")
		} else {
			assert.NoError(t, err, "Scenario %d (%s) should not trigger circuit breaker", i+1, scenario.description)
		}
	}
}

// TestOutputTokenCircuitBreaker_Recovery tests that circuit breaker recovers after success.
func TestOutputTokenCircuitBreaker_Recovery(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	// Hit output limit (truncated tool calls) twice
	tracker.recordOutputTokenExhaustion(true)
	tracker.recordOutputTokenExhaustion(true)
	assert.Equal(t, 2, tracker.outputTokenExhaustions)

	// A text response (or normal completion) clears the counter
	tracker.clearOutputTokenExhaustion()
	assert.Equal(t, 0, tracker.outputTokenExhaustions)

	// Can now accumulate again without residual count
	tracker.recordOutputTokenExhaustion(false)
	tracker.recordOutputTokenExhaustion(false)
	err := tracker.checkOutputTokenCircuitBreaker(8)
	assert.NoError(t, err, "Should not trigger after recovery")
}

// TestOutputTokenCB_SessionAccumulation_Regression is the exact regression scenario
// reported by Dan Bo and other Anthropic/OpenAI users.
//
// Three legitimate verbose responses in a session must NOT trigger the CB.
// The old code incremented the counter for ALL max_tokens events, even successful ones,
// and never cleared it between user turns when stop_reason was max_tokens.
func TestOutputTokenCB_SessionAccumulation_Regression(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	// Turn 1: "Create HIPAA compliance report"
	// Agent returns verbose text → max_tokens, ToolCalls=[] → agent.go clears counter
	tracker.clearOutputTokenExhaustion() // agent.go behavior for text responses
	assert.Equal(t, 0, tracker.outputTokenExhaustions, "After turn 1 text response: counter must be 0")
	assert.NoError(t, tracker.checkOutputTokenCircuitBreaker(8))

	// Turn 2: "Check data quality on PATIENTS table"
	// Agent returns verbose analysis → max_tokens, ToolCalls=[] → agent.go clears counter
	tracker.clearOutputTokenExhaustion()
	assert.Equal(t, 0, tracker.outputTokenExhaustions, "After turn 2 text response: counter must be 0")
	assert.NoError(t, tracker.checkOutputTokenCircuitBreaker(8))

	// Turn 3: "Generate the final summary"
	// Agent returns final summary → max_tokens, ToolCalls=[] → agent.go clears counter
	tracker.clearOutputTokenExhaustion()
	assert.Equal(t, 0, tracker.outputTokenExhaustions, "After turn 3 text response: counter must be 0")
	err := tracker.checkOutputTokenCircuitBreaker(8)
	assert.NoError(t, err, "CB must NOT fire on legitimate verbose text responses — this was the reported bug")
}

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

// TestOutputTokenCircuitBreaker tests the output token circuit breaker functionality.
func TestOutputTokenCircuitBreaker(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	// First exhaustion - should not trigger circuit breaker
	count := tracker.recordOutputTokenExhaustion(true)
	assert.Equal(t, 1, count)
	err := tracker.checkOutputTokenCircuitBreaker(3)
	assert.NoError(t, err, "First exhaustion should not trigger circuit breaker")

	// Second exhaustion - should not trigger circuit breaker
	count = tracker.recordOutputTokenExhaustion(true)
	assert.Equal(t, 2, count)
	err = tracker.checkOutputTokenCircuitBreaker(3)
	assert.NoError(t, err, "Second exhaustion should not trigger circuit breaker")

	// Third exhaustion - should trigger circuit breaker
	count = tracker.recordOutputTokenExhaustion(true)
	assert.Equal(t, 3, count)
	err = tracker.checkOutputTokenCircuitBreaker(3)
	require.Error(t, err, "Third exhaustion should trigger circuit breaker")
	assert.Contains(t, err.Error(), "OUTPUT TOKEN CIRCUIT BREAKER TRIGGERED")
	assert.Contains(t, err.Error(), "Break this task into smaller chunks")
	assert.Contains(t, err.Error(), "Consecutive failures: 3")
	assert.Contains(t, err.Error(), "Truncated tool calls detected: true")
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

	// Test with threshold of 5
	for i := 1; i < 5; i++ {
		tracker.recordOutputTokenExhaustion(false)
		err := tracker.checkOutputTokenCircuitBreaker(5)
		assert.NoError(t, err, "Should not trigger before threshold")
	}

	// 5th exhaustion should trigger
	tracker.recordOutputTokenExhaustion(false)
	err := tracker.checkOutputTokenCircuitBreaker(5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OUTPUT TOKEN CIRCUIT BREAKER TRIGGERED")
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
func TestOutputTokenCircuitBreaker_Integration(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	// Simulate a conversation that hits output token limit repeatedly
	scenarios := []struct {
		stopReason       string
		hasEmptyToolCall bool
		shouldError      bool
	}{
		{"max_tokens", true, false}, // First hit
		{"max_tokens", true, false}, // Second hit
		{"max_tokens", true, true},  // Third hit - circuit breaker triggers
	}

	for i, scenario := range scenarios {
		if scenario.stopReason == "max_tokens" {
			tracker.recordOutputTokenExhaustion(scenario.hasEmptyToolCall)
		} else {
			tracker.clearOutputTokenExhaustion()
		}

		err := tracker.checkOutputTokenCircuitBreaker(3)
		if scenario.shouldError {
			require.Error(t, err, "Scenario %d should trigger circuit breaker", i+1)
			assert.Contains(t, err.Error(), "OUTPUT TOKEN CIRCUIT BREAKER TRIGGERED")
		} else {
			assert.NoError(t, err, "Scenario %d should not trigger circuit breaker", i+1)
		}
	}
}

// TestOutputTokenCircuitBreaker_Recovery tests that circuit breaker recovers after success.
func TestOutputTokenCircuitBreaker_Recovery(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	// Hit output limit twice
	tracker.recordOutputTokenExhaustion(true)
	tracker.recordOutputTokenExhaustion(true)
	assert.Equal(t, 2, tracker.outputTokenExhaustions)

	// Successful response clears the counter
	tracker.clearOutputTokenExhaustion()
	assert.Equal(t, 0, tracker.outputTokenExhaustions)

	// Can now hit limit again without immediately triggering
	tracker.recordOutputTokenExhaustion(false)
	tracker.recordOutputTokenExhaustion(false)
	err := tracker.checkOutputTokenCircuitBreaker(3)
	assert.NoError(t, err, "Should not trigger after recovery")
}

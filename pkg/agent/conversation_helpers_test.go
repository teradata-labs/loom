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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConsecutiveFailureTracker(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	params := map[string]interface{}{
		"sql": "SELECT * FROM test",
	}

	// Record first failure
	count1 := tracker.record("query_tool", params, "syntax_error")
	assert.Equal(t, 1, count1)

	// Record second failure (same signature)
	count2 := tracker.record("query_tool", params, "syntax_error")
	assert.Equal(t, 2, count2)

	// Clear on success
	tracker.clear("query_tool", params)

	// Should restart counting
	count3 := tracker.record("query_tool", params, "syntax_error")
	assert.Equal(t, 1, count3)
}

func TestConsecutiveFailureTracker_DifferentSignatures(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	params1 := map[string]interface{}{"sql": "SELECT 1"}
	params2 := map[string]interface{}{"sql": "SELECT 2"}

	// Different params = different signatures
	count1 := tracker.record("query_tool", params1, "error")
	count2 := tracker.record("query_tool", params2, "error")

	assert.Equal(t, 1, count1)
	assert.Equal(t, 1, count2) // Separate counter
}

func TestGetEscalationMessage(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	// Below threshold
	msg1 := tracker.getEscalationMessage(1, 2)
	assert.Empty(t, msg1)

	// At threshold
	msg2 := tracker.getEscalationMessage(2, 2)
	assert.NotEmpty(t, msg2)
	assert.Contains(t, msg2, "ESCALATION")
	assert.Contains(t, msg2, "2 times in a row")
}

func TestCheckTokenBudget(t *testing.T) {
	sm := NewSegmentedMemory("Small ROM content", 0, 0)

	info := checkTokenBudget(sm)

	// Get actual budget from segmented memory
	used, available, _ := sm.GetTokenBudgetUsage()

	assert.Greater(t, info.currentTokens, 0)
	assert.Equal(t, available, info.availableTokens)
	assert.Equal(t, used, info.currentTokens)
	assert.Greater(t, info.budgetPct, 0.0)
	// MaxOutputTokens should be 50% of available budget (no longer capped at 8192)
	assert.Equal(t, available/2, info.maxOutputTokens)
	assert.GreaterOrEqual(t, info.maxOutputTokens, 2048)
}

func TestCheckTokenBudget_LowBudget(t *testing.T) {
	// Create ROM large enough to consume most of budget
	largeROM := strings.Repeat("This is test content for token budget testing. ", 10000)
	sm := NewSegmentedMemory(largeROM, 0, 0)

	info := checkTokenBudget(sm)

	// Should cap at minimum
	assert.GreaterOrEqual(t, info.maxOutputTokens, 2048)
}

func TestCheckTokenBudget_HighBudget(t *testing.T) {
	sm := NewSegmentedMemory("Small ROM", 0, 0)

	info := checkTokenBudget(sm)

	// With high budget, maxOutputTokens = available / 2
	// For Claude Sonnet 4.5 default (200K context, 20K reserved = 180K available)
	// After small ROM usage, should have ~179K available, so max output ~89K
	_, available, _ := sm.GetTokenBudgetUsage()
	assert.Equal(t, available/2, info.maxOutputTokens)
	assert.Greater(t, info.maxOutputTokens, 8192) // Should exceed old fixed cap
}

func TestEnforceTokenBudget_NormalUsage(t *testing.T) {
	sm := NewSegmentedMemory("Normal ROM content", 0, 0)

	info := checkTokenBudget(sm)
	compressed, err := enforceTokenBudget(context.Background(), sm, info)

	require.NoError(t, err)
	assert.False(t, compressed) // Shouldn't compress at low usage
}

func TestEnforceTokenBudget_CriticalUsage(t *testing.T) {
	// Create large ROM to push budget over 85%
	largeROM := strings.Repeat("Large ROM content for testing compression. ", 15000)
	sm := NewSegmentedMemory(largeROM, 0, 0)

	// Add many messages to L1
	for i := 0; i < 10; i++ {
		sm.AddMessage(Message{
			Role:    "user",
			Content: strings.Repeat("Test message ", 50),
		})
	}

	info := checkTokenBudget(sm)
	if info.budgetPct > 85 {
		compressed, err := enforceTokenBudget(context.Background(), sm, info)
		require.NoError(t, err)
		assert.True(t, compressed) // Should compress at critical usage
	} else {
		t.Skip("ROM not large enough to trigger critical threshold")
	}
}

func TestBuildSoftReminderLegacy(t *testing.T) {
	// Test with default config (MaxToolExecutions=50, threshold at 37)
	maxTools := 50

	// Below threshold (75% of 50 = 37)
	msg1 := buildSoftReminder(5, maxTools)
	assert.Empty(t, msg1)

	// At threshold
	msg2 := buildSoftReminder(38, maxTools)
	assert.NotEmpty(t, msg2)
	assert.Contains(t, msg2, "38")
	assert.Contains(t, msg2, "IMPORTANT")

	// Above threshold but within window
	msg3 := buildSoftReminder(40, maxTools)
	assert.NotEmpty(t, msg3)

	// At 90% (stop reminding)
	msg4 := buildSoftReminder(45, maxTools)
	assert.Empty(t, msg4)
}

func TestKeealiveProgress(t *testing.T) {
	callCount := 0
	callback := func(p ProgressEvent) {
		callCount++
		assert.Equal(t, StageToolExecution, p.Stage)
		assert.Contains(t, p.Message, "Still executing")
		assert.Contains(t, p.ToolName, "test_tool")
	}

	ctx := context.Background()
	done := make(chan struct{})
	startTime := time.Now()

	// Start keepalive in goroutine
	go keepaliveProgress(ctx, done, "test_tool", startTime, callback)

	// Let it run briefly
	time.Sleep(100 * time.Millisecond)

	// Stop keepalive
	close(done)

	// Give it time to stop
	time.Sleep(50 * time.Millisecond)

	// Should not have called callback (interval is 10 seconds)
	assert.Equal(t, 0, callCount)
}

func TestKeealiveProgress_NilCallback(t *testing.T) {
	ctx := context.Background()
	done := make(chan struct{})

	// Should not panic with nil callback
	go keepaliveProgress(ctx, done, "test_tool", time.Now(), nil)

	time.Sleep(50 * time.Millisecond)
	close(done)
}

func TestFormatToolResultWithEscalation(t *testing.T) {
	// No escalation
	result1 := formatToolResultWithEscalation("Success", nil, "")
	assert.Equal(t, "Success", result1)

	// With error
	result2 := formatToolResultWithEscalation(nil, assert.AnError, "")
	assert.Contains(t, result2, "Error:")

	// With escalation
	result3 := formatToolResultWithEscalation("Failed", nil, "\n\nESCALATION: Stop!")
	assert.Contains(t, result3, "Failed")
	assert.Contains(t, result3, "ESCALATION")
}

func TestBuildSoftReminder(t *testing.T) {
	tests := []struct {
		name               string
		toolExecutionCount int
		maxToolExecutions  int
		expectReminder     bool
		expectedThreshold  int
	}{
		{
			name:               "Below 75% threshold - no reminder",
			toolExecutionCount: 5,
			maxToolExecutions:  50,
			expectReminder:     false,
		},
		{
			name:               "At 75% threshold - show reminder",
			toolExecutionCount: 38, // 75% of 50 = 37.5 -> 37
			maxToolExecutions:  50,
			expectReminder:     true,
			expectedThreshold:  37,
		},
		{
			name:               "At 80% threshold - show reminder",
			toolExecutionCount: 40,
			maxToolExecutions:  50,
			expectReminder:     true,
		},
		{
			name:               "At 90% threshold - no reminder (upper bound)",
			toolExecutionCount: 45, // 90% of 50 = 45
			maxToolExecutions:  50,
			expectReminder:     false,
		},
		{
			name:               "Above 90% - no reminder",
			toolExecutionCount: 48,
			maxToolExecutions:  50,
			expectReminder:     false,
		},
		{
			name:               "Low max with minimum threshold - uses minimum of 10",
			toolExecutionCount: 8,
			maxToolExecutions:  10, // 75% = 7.5, but minimum is 10
			expectReminder:     false,
		},
		{
			name:               "High max for weaver - 75% is 750",
			toolExecutionCount: 750,
			maxToolExecutions:  1000,
			expectReminder:     true,
		},
		{
			name:               "Weaver below threshold - no reminder",
			toolExecutionCount: 500,
			maxToolExecutions:  1000,
			expectReminder:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reminder := buildSoftReminder(tt.toolExecutionCount, tt.maxToolExecutions)
			if tt.expectReminder {
				assert.NotEmpty(t, reminder, "Expected reminder but got empty string")
				assert.Contains(t, reminder, fmt.Sprintf("%d of %d max", tt.toolExecutionCount, tt.maxToolExecutions))
			} else {
				assert.Empty(t, reminder, "Expected no reminder but got: %s", reminder)
			}
		})
	}
}

func TestBuildTurnReminder(t *testing.T) {
	tests := []struct {
		name           string
		turnCount      int
		maxTurns       int
		expectReminder bool
	}{
		{
			name:           "Below 75% threshold - no reminder",
			turnCount:      10,
			maxTurns:       25,
			expectReminder: false,
		},
		{
			name:           "At 75% threshold - show reminder",
			turnCount:      19, // 75% of 25 = 18.75 -> 18
			maxTurns:       25,
			expectReminder: true,
		},
		{
			name:           "At 90% threshold - no reminder (upper bound)",
			turnCount:      23, // 90% of 25 = 22.5 -> 22
			maxTurns:       25,
			expectReminder: false,
		},
		{
			name:           "High turn limit for weaver - 75% is 750",
			turnCount:      750,
			maxTurns:       1000,
			expectReminder: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reminder := buildTurnReminder(tt.turnCount, tt.maxTurns)
			if tt.expectReminder {
				assert.NotEmpty(t, reminder, "Expected reminder but got empty string")
				assert.Contains(t, reminder, fmt.Sprintf("%d of %d max", tt.turnCount, tt.maxTurns))
			} else {
				assert.Empty(t, reminder, "Expected no reminder but got: %s", reminder)
			}
		})
	}
}

func TestExtractErrorType(t *testing.T) {
	// Nil result
	errType1 := extractErrorType(nil)
	assert.Empty(t, errType1)

	// Map with error_type
	result2 := map[string]interface{}{
		"error_type": "syntax_error",
	}
	errType2 := extractErrorType(result2)
	assert.Equal(t, "syntax_error", errType2)

	// Map without error_type
	result3 := map[string]interface{}{
		"other_field": "value",
	}
	errType3 := extractErrorType(result3)
	assert.Empty(t, errType3)

	// Non-map result
	errType4 := extractErrorType("string result")
	assert.Empty(t, errType4)
}

func TestDefaultTokenBudgetConfig(t *testing.T) {
	config := DefaultTokenBudgetConfig()

	assert.Equal(t, 200000, config.MaxContextTokens)
	assert.Equal(t, 20000, config.ReservedOutputTokens)
	assert.Equal(t, 70.0, config.WarningThresholdPct)
	assert.Equal(t, 85.0, config.CriticalThresholdPct)
	assert.Equal(t, 8192, config.MaxOutputTokens)
	assert.Equal(t, 2048, config.MinOutputTokens)
	assert.Equal(t, 0.5, config.OutputBudgetFraction)
}

func TestDefaultFailureEscalationConfig(t *testing.T) {
	config := DefaultFailureEscalationConfig()

	assert.Equal(t, 2, config.MaxConsecutiveFailures)
	assert.True(t, config.TrackFailureSignature)
}

func TestDefaultSoftReminderConfig(t *testing.T) {
	config := DefaultSoftReminderConfig()

	assert.Equal(t, 10, config.ToolExecutionThreshold)
	assert.Equal(t, 20, config.StopThreshold)
	assert.True(t, config.Enabled)
}

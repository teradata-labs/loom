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
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// failureSignature uniquely identifies a tool call failure for tracking consecutive failures.
type failureSignature struct {
	toolName  string
	params    string // JSON serialization of params
	errorType string
}

// consecutiveFailureTracker tracks identical failures to enable early escalation.
// Thread-safe through session-level locking.
type consecutiveFailureTracker struct {
	failures               map[failureSignature]int
	outputTokenExhaustions int  // Track consecutive max_tokens stop reasons
	hasEmptyToolCall       bool // Track if last response had empty tool call (truncation indicator)
}

// newConsecutiveFailureTracker creates a new failure tracker.
func newConsecutiveFailureTracker() *consecutiveFailureTracker {
	return &consecutiveFailureTracker{
		failures: make(map[failureSignature]int),
	}
}

// record increments the failure count for a given signature.
// Returns the new count.
func (t *consecutiveFailureTracker) record(toolName string, params map[string]interface{}, errorType string) int {
	paramsJSON, _ := json.Marshal(params)
	sig := failureSignature{
		toolName:  toolName,
		params:    string(paramsJSON),
		errorType: errorType,
	}
	t.failures[sig]++
	return t.failures[sig]
}

// clear removes all failure records for a given tool/params combination (called on success).
func (t *consecutiveFailureTracker) clear(toolName string, params map[string]interface{}) {
	paramsJSON, _ := json.Marshal(params)
	paramsStr := string(paramsJSON)

	// Remove all signatures matching this tool and params (any error type)
	for sig := range t.failures {
		if sig.toolName == toolName && sig.params == paramsStr {
			delete(t.failures, sig)
		}
	}
}

// getEscalationMessage returns an escalation message if threshold is exceeded.
// Returns empty string if below threshold.
func (t *consecutiveFailureTracker) getEscalationMessage(count int, threshold int) string {
	if count < threshold {
		return ""
	}

	return fmt.Sprintf("\n\n⛔ ESCALATION: This exact tool call has failed %d times in a row.\n"+
		"This approach is not working. Please try:\n"+
		"1. A different tool or strategy\n"+
		"2. Simplifying your query\n"+
		"3. Checking if you have the correct parameters\n\n"+
		"Do NOT retry this same tool call again.",
		count)
}

// tokenBudgetInfo holds token budget status for decision making.
type tokenBudgetInfo struct {
	currentTokens   int
	availableTokens int
	budgetPct       float64
	maxOutputTokens int
}

// getModelOutputCapacity determines the optimal output token cap based on model name.
// This ensures smaller models aren't overwhelmed with output requirements while
// allowing larger models to use their full capabilities.
//
//nolint:unused // Reserved for future dynamic output capacity adjustment
func getModelOutputCapacity(modelName string) int {
	modelLower := strings.ToLower(modelName)

	// Small models (7B-8B parameters) - more focused outputs
	// These models perform better with shorter, more concise responses
	if strings.Contains(modelLower, "7b") || strings.Contains(modelLower, "8b") ||
		strings.Contains(modelLower, "gemma") || // Gemma models are typically smaller
		strings.Contains(modelLower, "phi") { // Phi models are compact
		return 4096 // 4K tokens for small models
	}

	// Medium models (13B-32B parameters) - balanced outputs
	if strings.Contains(modelLower, "13b") || strings.Contains(modelLower, "14b") ||
		strings.Contains(modelLower, "20b") || strings.Contains(modelLower, "32b") {
		return 6144 // 6K tokens for medium models
	}

	// Large models (70B+ parameters, Claude, GPT-4, etc.) - full capacity
	// These models can handle longer, more detailed responses
	return 8192 // 8K tokens for large models
}

// checkTokenBudget calculates current token budget status.
// Returns budget info for logging and decision making.
func checkTokenBudget(segmentedMem *SegmentedMemory) tokenBudgetInfo {
	currentTokens := segmentedMem.GetTokenCount()

	// Get actual token budget from segmented memory
	used, available, total := segmentedMem.GetTokenBudgetUsage()

	// Calculate budget percentage
	budgetPct := 0.0
	if total > 0 {
		budgetPct = float64(used) / float64(total) * 100
	}

	// Calculate adaptive maxTokens based on remaining budget
	// Use at most 50% of remaining budget for output
	maxOutputTokens := available / 2
	if maxOutputTokens < 2048 {
		maxOutputTokens = 2048 // Minimum viable output size
	}

	return tokenBudgetInfo{
		currentTokens:   currentTokens,
		availableTokens: available,
		budgetPct:       budgetPct,
		maxOutputTokens: maxOutputTokens,
	}
}

// enforceTokenBudget checks token usage and triggers compression if needed.
// Returns true if compression was performed.
func enforceTokenBudget(ctx context.Context, segmentedMem *SegmentedMemory, budgetInfo tokenBudgetInfo) (bool, error) {
	// Force aggressive compression if budget is critical (>70% - lowered from 85%)
	// This prevents context overflow in data-intensive workloads with many tool executions
	if budgetInfo.budgetPct > 70 {
		messagesCompressed, tokensSaved := segmentedMem.CompactMemory()
		if messagesCompressed > 0 {
			// Note: In production, use structured logging here
			_ = fmt.Sprintf("pre_llm_compression_forced: messages=%d tokens_saved=%d new_count=%d",
				messagesCompressed, tokensSaved, segmentedMem.GetTokenCount())
			return true, nil
		}
	} else if budgetInfo.budgetPct > 60 {
		// Warning level - log but don't force compression yet
		_ = fmt.Sprintf("token_budget_warning: tokens=%d pct=%.2f",
			budgetInfo.currentTokens, budgetInfo.budgetPct)
	}

	return false, nil
}

// buildSoftReminder creates a soft reminder message after many tool executions.
// Returns empty string if threshold not reached.
// Threshold is 75% of maxToolExecutions to allow agents room to work before nudging completion.
func buildSoftReminder(toolExecutionCount int, maxToolExecutions int) string {
	// Calculate 75% threshold (but minimum of 10 to avoid spamming on low limits)
	threshold := int(float64(maxToolExecutions) * 0.75)
	if threshold < 10 {
		threshold = 10
	}

	// Reminder window: 75% to 90% of max
	upperBound := int(float64(maxToolExecutions) * 0.90)

	if toolExecutionCount >= threshold && toolExecutionCount < upperBound {
		return fmt.Sprintf("\n\n🔔 IMPORTANT: You have executed many tool calls (%d of %d max). "+
			"If you have enough information to answer the user's question, please provide your final response now. "+
			"Only call more tools if absolutely necessary.",
			toolExecutionCount, maxToolExecutions)
	}
	return ""
}

// buildTurnReminder creates a soft reminder message after many conversation turns.
// Returns empty string if threshold not reached.
// Threshold is 75% of maxTurns to allow agents room to work before nudging completion.
func buildTurnReminder(turnCount int, maxTurns int) string {
	// Calculate 75% threshold (but minimum of 8 to avoid spamming on low limits)
	threshold := int(float64(maxTurns) * 0.75)
	if threshold < 8 {
		threshold = 8
	}

	// Reminder window: 75% to 90% of max
	upperBound := int(float64(maxTurns) * 0.90)

	if turnCount >= threshold && turnCount < upperBound {
		return fmt.Sprintf("\n\n🔔 NOTICE: This conversation has progressed through many turns (%d of %d max). "+
			"If you have sufficient information, please provide your complete response. "+
			"The conversation will be automatically concluded if the turn limit is reached.",
			turnCount, maxTurns)
	}
	return ""
}

// keepaliveProgress sends periodic progress updates during long-running operations.
// Stops when done channel is closed or context is cancelled.
func keepaliveProgress(ctx context.Context, done chan struct{}, toolName string, startTime time.Time, progressCallback ProgressCallback) {
	if progressCallback == nil {
		return
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			elapsed := time.Since(startTime).Round(time.Second)
			progressCallback(ProgressEvent{
				Stage:     StageToolExecution,
				Progress:  -1, // Indeterminate
				Message:   fmt.Sprintf("Still executing %s... (%s elapsed)", toolName, elapsed),
				ToolName:  toolName,
				Timestamp: time.Now(),
			})
		}
	}
}

// formatToolResultWithEscalation formats a tool result with optional escalation message.
func formatToolResultWithEscalation(result interface{}, err error, escalationMsg string) string {
	var baseResult string

	if err != nil {
		baseResult = fmt.Sprintf("Error: %v", err)
	} else {
		baseResult = fmt.Sprintf("%v", result)
	}

	if escalationMsg != "" {
		return baseResult + escalationMsg
	}

	return baseResult
}

// extractErrorType extracts error type from result data for failure tracking.
// Returns empty string if error type not available.
func extractErrorType(result interface{}) string {
	if result == nil {
		return ""
	}

	// Try to extract from map structure
	if resultMap, ok := result.(map[string]interface{}); ok {
		if errType, ok := resultMap["error_type"].(string); ok {
			return errType
		}
	}

	return ""
}

// recordOutputTokenExhaustion increments the output token exhaustion counter.
// Call this when LLM response has StopReason == "max_tokens".
// Returns the new count.
func (t *consecutiveFailureTracker) recordOutputTokenExhaustion(hasEmptyToolCall bool) int {
	t.outputTokenExhaustions++
	t.hasEmptyToolCall = hasEmptyToolCall
	return t.outputTokenExhaustions
}

// clearOutputTokenExhaustion resets the output token exhaustion counter.
// Call this when LLM response completes successfully without hitting max_tokens.
func (t *consecutiveFailureTracker) clearOutputTokenExhaustion() {
	t.outputTokenExhaustions = 0
	t.hasEmptyToolCall = false
}

// checkOutputTokenCircuitBreaker checks if output token circuit breaker should trigger.
// Returns error if threshold exceeded, nil otherwise.
// threshold: number of consecutive max_tokens failures before circuit breaker triggers (default: 3)
func (t *consecutiveFailureTracker) checkOutputTokenCircuitBreaker(threshold int) error {
	if t.outputTokenExhaustions < threshold {
		return nil
	}

	// Build detailed error message with actionable suggestions
	const msgTemplate = "\n\n🔴 OUTPUT TOKEN CIRCUIT BREAKER TRIGGERED\n\n" +
		"The model has hit the output token limit %d times in a row.\n" +
		"This usually happens when generating very large outputs (e.g., large JSON files, long code blocks).\n\n" +
		"IMMEDIATE ACTIONS:\n" +
		"1. Break this task into smaller chunks (e.g., create file sections separately)\n" +
		"2. Use file write operations instead of heredocs for large content\n" +
		"3. Simplify the output format or reduce the amount of data generated\n" +
		"4. If creating JSON artifacts, write them incrementally rather than all at once\n\n" +
		"TECHNICAL DETAILS:\n" +
		"- Model output limit: 8,192 tokens (approximately 6,000 words)\n" +
		"- Consecutive failures: %d\n" +
		"- Truncated tool calls detected: %t\n\n" +
		"The conversation will now stop to prevent an infinite error loop.\n" +
		"Please reformulate your approach with smaller, incremental steps."

	msg := fmt.Sprintf(msgTemplate,
		t.outputTokenExhaustions,
		t.outputTokenExhaustions,
		t.hasEmptyToolCall,
	)

	return fmt.Errorf("%s", msg)
}

// detectEmptyToolCall checks if any tool call has an empty Input object.
// This is a strong indicator that the LLM output was truncated mid-generation.
// Returns true if empty tool call detected.
func detectEmptyToolCall(toolCalls []ToolCall) bool {
	for _, tc := range toolCalls {
		// Check if Input is empty or only has zero values
		if len(tc.Input) == 0 {
			return true
		}

		// Check if all values are empty/zero
		allEmpty := true
		for _, v := range tc.Input {
			if v != nil && v != "" && v != 0 && v != false {
				allEmpty = false
				break
			}
		}

		if allEmpty {
			return true
		}
	}

	return false
}

// TokenBudgetConfig holds configuration for token budget management.
type TokenBudgetConfig struct {
	MaxContextTokens     int     // Total context window (default: 200000)
	ReservedOutputTokens int     // Reserved for output (default: 20000)
	WarningThresholdPct  float64 // Warning threshold (default: 70.0)
	CriticalThresholdPct float64 // Critical threshold (default: 85.0)
	MaxOutputTokens      int     // Maximum output tokens (default: 8192)
	MinOutputTokens      int     // Minimum output tokens (default: 2048)
	OutputBudgetFraction float64 // Fraction of available for output (default: 0.5)
}

// DefaultTokenBudgetConfig returns default token budget configuration for Claude Sonnet 4.5.
func DefaultTokenBudgetConfig() TokenBudgetConfig {
	return TokenBudgetConfig{
		MaxContextTokens:     200000,
		ReservedOutputTokens: 20000,
		WarningThresholdPct:  70.0,
		CriticalThresholdPct: 85.0,
		MaxOutputTokens:      8192,
		MinOutputTokens:      2048,
		OutputBudgetFraction: 0.5,
	}
}

// FailureEscalationConfig holds configuration for failure escalation.
type FailureEscalationConfig struct {
	MaxConsecutiveFailures int  // Threshold for escalation (default: 2)
	TrackFailureSignature  bool // Whether to track failure signatures (default: true)
}

// DefaultFailureEscalationConfig returns default failure escalation configuration.
func DefaultFailureEscalationConfig() FailureEscalationConfig {
	return FailureEscalationConfig{
		MaxConsecutiveFailures: 2,
		TrackFailureSignature:  true,
	}
}

// SoftReminderConfig holds configuration for soft reminders.
type SoftReminderConfig struct {
	ToolExecutionThreshold int  // Threshold to start reminders (default: 10)
	StopThreshold          int  // Threshold to stop reminders (default: 20)
	Enabled                bool // Whether soft reminders are enabled (default: true)
}

// DefaultSoftReminderConfig returns default soft reminder configuration.
func DefaultSoftReminderConfig() SoftReminderConfig {
	return SoftReminderConfig{
		ToolExecutionThreshold: 10,
		StopThreshold:          20,
		Enabled:                true,
	}
}

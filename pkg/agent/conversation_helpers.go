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
	"time"

	"github.com/teradata-labs/loom/pkg/shuttle"
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
// threshold: number of consecutive turns with truncated tool calls before the CB fires (default: 8).
// This only fires when the agent is stuck in an agentic tool loop — NOT on verbose text responses.
func (t *consecutiveFailureTracker) checkOutputTokenCircuitBreaker(threshold int) error {
	if t.outputTokenExhaustions < threshold {
		return nil
	}

	// The title line is extracted by the TUI as the status bar summary; keep it concise.
	// Subsequent lines become the expandable details panel rendered as markdown.
	msgTemplate := "Output token circuit breaker triggered after %d consecutive turns with truncated tool calls.\n\n" +
		"The agent is stuck: each turn hits the model output limit before tool calls complete, " +
		"so no forward progress can be made.\n\n" +
		"## Immediate Actions\n\n" +
		"1. Break this task into smaller steps and re-submit\n" +
		"2. Ask the agent to generate large content incrementally (e.g., section by section)\n" +
		"3. Use file write tools instead of generating large blocks inline\n" +
		"4. Simplify the task scope\n\n" +
		"## Technical Details\n\n" +
		"- Consecutive truncated-tool-call turns: %d\n" +
		"- Threshold: %d (configurable via `output_token_cb_threshold` in agent YAML)\n\n" +
		"The conversation has stopped to prevent an infinite loop. " +
		"Reformulate your request with smaller, incremental steps."

	msg := fmt.Sprintf(msgTemplate,
		t.outputTokenExhaustions,
		t.outputTokenExhaustions,
		threshold,
	)

	return fmt.Errorf("%s", msg)
}

// advertisedSchema returns the input schema the named tool was advertised with.
// ok is false when the tool is not in the advertised set at all, which is a
// different state from a tool that advertises no schema.
func advertisedSchema(name string, tools []shuttle.Tool) (schema *shuttle.JSONSchema, ok bool) {
	for _, t := range tools {
		if t == nil || t.Name() != name {
			continue
		}
		return t.InputSchema(), true
	}
	return nil, false
}

// advertisedTool returns the tool the call names, so the caller can reuse the
// executor's own parameter normalization against it.
func advertisedTool(name string, tools []shuttle.Tool) shuttle.Tool {
	for _, t := range tools {
		if t != nil && t.Name() == name {
			return t
		}
	}
	return nil
}

// maxSchemaDepth bounds schema recursion. Tool schemas arrive from MCP servers
// and other third parties, so nesting is not under our control. Exhausting the
// bound yields toolCallStateUnknown, never complete: past it nothing has been
// established, and saying otherwise would clear a run on an unread schema.
const maxSchemaDepth = 16

// schemaDemandsProperty reports whether schema requires at least one property at
// its own level, counting composite branches: a schema with an empty root
// "required" can still demand a property through oneOf/anyOf/allOf, which MCP
// tools do use.
//
// This is deliberately a question about THIS level only — it decides whether an
// empty input is acceptable for the call. Nested requirements bite only when the
// value carrying them is actually present, which checkAgainstSchema handles.
func schemaDemandsProperty(schema *shuttle.JSONSchema, depth int) bool {
	if schema == nil || depth > maxSchemaDepth {
		return false
	}
	if len(schema.Required) > 0 {
		return true
	}
	for _, branches := range [][]*shuttle.JSONSchema{schema.OneOf, schema.AnyOf, schema.AllOf} {
		for _, b := range branches {
			if schemaDemandsProperty(b, depth+1) {
				return true
			}
		}
	}
	return false
}

// checkAgainstSchema decides how much can be established about value under
// schema, descending through present properties and array items.
//
// Descending matters: a root-only check positively reports "complete" for input
// whose nested objects are missing what they demand, and the accounting above
// treats complete as forward progress. The visualization tool is the concrete
// case — its root requires "datasets", but each dataset item requires "name" and
// "data", so {"datasets":[{}], ...} passes a root-only check and is rejected by
// the tool itself.
//
// Only structural presence is checked. Semantic constraints (enum, pattern,
// range) are NOT evidence that the model stopped emitting mid-call, and treating
// a violated one as truncation would fire the breaker on a complete answer.
func checkAgainstSchema(schema *shuttle.JSONSchema, value interface{}, depth int) toolCallState {
	if schema == nil {
		return toolCallStateComplete
	}
	if depth > maxSchemaDepth {
		return toolCallStateUnknown
	}

	switch v := value.(type) {
	case map[string]interface{}:
		return checkObject(schema, v, depth)
	case []interface{}:
		if schema.Items == nil {
			return toolCallStateComplete
		}
		state := toolCallStateComplete
		for _, item := range v {
			switch checkAgainstSchema(schema.Items, item, depth+1) {
			case toolCallStateIncomplete:
				return toolCallStateIncomplete
			case toolCallStateUnknown:
				state = toolCallStateUnknown
			case toolCallStateComplete:
			}
		}
		return state
	default:
		// A scalar, or a shape this walker does not model. If the schema demands
		// properties of it, the value cannot satisfy them and nothing can be
		// established; otherwise there is nothing to check.
		if schemaDemandsProperty(schema, depth) {
			return toolCallStateUnknown
		}
		return toolCallStateComplete
	}
}

// checkObject applies one schema node to one object value: its own required
// keys, its composite branches, then each present property in turn.
func checkObject(schema *shuttle.JSONSchema, obj map[string]interface{}, depth int) toolCallState {
	for _, key := range schema.Required {
		if _, present := obj[key]; !present {
			return toolCallStateIncomplete
		}
	}

	state := toolCallStateComplete
	demote := func(s toolCallState) bool {
		switch s {
		case toolCallStateIncomplete:
			return true
		case toolCallStateUnknown:
			state = toolCallStateUnknown
		case toolCallStateComplete:
		}
		return false
	}

	for _, b := range schema.AllOf {
		if demote(checkAgainstSchema(b, obj, depth+1)) {
			return toolCallStateIncomplete
		}
	}
	for _, branches := range [][]*shuttle.JSONSchema{schema.OneOf, schema.AnyOf} {
		if len(branches) == 0 {
			continue
		}
		// At least one branch must hold. oneOf is checked as "at least one"
		// rather than JSON Schema's "exactly one": the question is only whether
		// the model finished emitting arguments, and the permissive direction
		// keeps a well-formed call from reading as truncated.
		best := toolCallStateIncomplete
		for _, b := range branches {
			switch checkAgainstSchema(b, obj, depth+1) {
			case toolCallStateComplete:
				best = toolCallStateComplete
			case toolCallStateUnknown:
				if best != toolCallStateComplete {
					best = toolCallStateUnknown
				}
			case toolCallStateIncomplete:
			}
		}
		if best == toolCallStateIncomplete {
			return toolCallStateIncomplete
		}
		if best == toolCallStateUnknown {
			state = toolCallStateUnknown
		}
	}

	// Descend into the properties that are actually present. An absent optional
	// property carries no requirement; an absent required one was caught above.
	for key, sub := range schema.Properties {
		val, present := obj[key]
		if !present {
			continue
		}
		if demote(checkAgainstSchema(sub, val, depth+1)) {
			return toolCallStateIncomplete
		}
	}

	return state
}

// providerRawArgsKey is the marker the OpenAI and Azure OpenAI clients store
// when a tool call's arguments JSON does not parse: pkg/llm/openai/client.go and
// pkg/llm/azureopenai/client.go both fall back to Input{"_raw": <partial>} on
// both their non-streaming and streaming paths.
//
// That is precisely what a call truncated at max_tokens looks like on those
// providers — a partial arguments string that never closed — so the marker is a
// positive signal of an INCOMPLETE call, even though the resulting map is
// non-empty and would otherwise read as fully populated.
//
// The name is not reserved by JSON Schema or by the Tool contract, so it is only
// read as a marker when the advertised schema does not itself define a property
// called "_raw"; a tool that legitimately takes one keeps its own meaning.
const providerRawArgsKey = "_raw"

// schemaDefinesRawKey reports whether the schema declares "_raw" as a real
// property of its own, at the root or in a composite branch.
func schemaDefinesRawKey(schema *shuttle.JSONSchema, depth int) bool {
	if schema == nil || depth > maxSchemaDepth {
		return false
	}
	if _, ok := schema.Properties[providerRawArgsKey]; ok {
		return true
	}
	for _, key := range schema.Required {
		if key == providerRawArgsKey {
			return true
		}
	}
	for _, branches := range [][]*shuttle.JSONSchema{schema.OneOf, schema.AnyOf, schema.AllOf} {
		for _, b := range branches {
			if schemaDefinesRawKey(b, depth+1) {
				return true
			}
		}
	}
	return false
}

// toolCallState is how much can be established about the tool calls on a
// max_tokens turn. The three states exist because "not visibly empty" is not
// the same as "known to be complete", and the circuit breaker must only treat
// the latter as forward progress.
type toolCallState int

const (
	// toolCallStateUnknown: completeness could not be established — the tool was
	// not in the advertised set, advertised no schema, or carries a shape this
	// walker cannot read.
	toolCallStateUnknown toolCallState = iota
	// toolCallStateComplete: every call carries everything its schema demands.
	toolCallStateComplete
	// toolCallStateIncomplete: at least one call is truncated or malformed.
	toolCallStateIncomplete
)

// classifyToolCalls aggregates the per-call verdicts for a turn: any incomplete
// call makes the turn incomplete; otherwise any unestablished call makes it
// unknown; only an all-complete turn is complete.
func classifyToolCalls(toolCalls []ToolCall, tools []shuttle.Tool) toolCallState {
	state := toolCallStateComplete
	for _, tc := range toolCalls {
		switch classifyToolCall(tc, tools) {
		case toolCallStateIncomplete:
			return toolCallStateIncomplete
		case toolCallStateUnknown:
			state = toolCallStateUnknown
		case toolCallStateComplete:
			// leave state as-is
		}
	}
	return state
}

// classifyToolCall establishes what can be said about a single tool call.
func classifyToolCall(tc ToolCall, tools []shuttle.Tool) toolCallState {
	tool := advertisedTool(tc.Name, tools)
	var schema *shuttle.JSONSchema
	if tool != nil {
		schema = tool.InputSchema()
	}

	// The provider could not parse the arguments at all. Checked before anything
	// else, and regardless of what the tool demands: an argument-less tool never
	// produces a parse failure, so the marker still means a broken call. Skipped
	// only when the tool genuinely declares a "_raw" property of its own.
	if _, raw := tc.Input[providerRawArgsKey]; raw && !schemaDefinesRawKey(schema, 0) {
		return toolCallStateIncomplete
	}

	if tool == nil || schema == nil {
		// Nothing to check against. Keep the historical emptiness heuristic so
		// detection does not weaken, but never report completeness we cannot
		// establish — an unknown call must not clear a truncation run.
		if inputLooksEmpty(tc.Input) {
			return toolCallStateIncomplete
		}
		return toolCallStateUnknown
	}

	// Judge the same parameter map the executor will run. Execute normalizes
	// root keys to the schema's spelling before dispatch, so a call sending
	// snake_case for a camelCase property executes fine; checking raw keys here
	// would count that executable call as incomplete and fire the breaker on
	// exactly the productive turns this accounting exists to protect.
	input := shuttle.NormalizeParametersToSchema(tool, tc.Input)

	if !schemaDemandsProperty(schema, 0) {
		// An argument-less tool is expected to be called with an empty input, so
		// emptiness says nothing about truncation for it. Its nested content, if
		// any, is still checked below.
		if len(input) == 0 {
			return toolCallStateComplete
		}
	} else if inputLooksEmpty(input) {
		return toolCallStateIncomplete
	}

	return checkAgainstSchema(schema, map[string]interface{}(input), 0)
}

// inputLooksEmpty is the historical truncation heuristic: an absent input, or
// one whose values are all zero-valued.
func inputLooksEmpty(input map[string]interface{}) bool {
	if len(input) == 0 {
		return true
	}
	for _, v := range input {
		if v != nil && v != "" && v != 0 && v != false {
			return false
		}
	}
	return true
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

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

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// RecoverableError is returned when self-healing fails but the error carries
// enough context for an upper layer (cloud, TUI) to offer recovery to the user.
type RecoverableError struct {
	ErrorType       string
	Message         string
	RecoveryAction  string
	RecoveryPayload map[string]any
	Retryable       bool
	Cause           error
}

func (e *RecoverableError) Error() string { return e.Message }
func (e *RecoverableError) Unwrap() error { return e.Cause }

// repeatHintThresholds are the run lengths at which the repeat-call hint fires
// (the DeepSeek Harness defaults). Only agents that opt in use them.
var repeatHintThresholds = []int{3, 5, 8}

// RecoveryConfig holds tunables for the self-healing orchestrator. The
// destructive trim knobs are gone with the second relief ladder (blueprint
// A5): no code path outside releasePressure can remove or shrink a message.
type RecoveryConfig struct {
	// RepeatHintThresholds are the lengths of a run of identical consecutive
	// executed tool calls (same name and canonical input; a call rejected
	// before execution neither counts nor resets the run) at which noteToolCall
	// reports a hint. Nil or empty disables hints, which is the default: the
	// agent opts in with behavior.repeat_call_hint (see registry.go). Hints
	// exist only when the agent runs with EnableSelfHealing (the orchestrator
	// is nil otherwise). A park/resume (park.go) builds a fresh orchestrator, so
	// a run of identical calls restarts its count after a human decision.
	RepeatHintThresholds []int
}

// DefaultRecoveryConfig returns the defaults: no repeat hints.
func DefaultRecoveryConfig() *RecoveryConfig {
	return &RecoveryConfig{}
}

// recoveryOrchestrator coordinates Tier 1 self-healing attempts within
// a single conversation loop execution. One instance per loop invocation.
type recoveryOrchestrator struct {
	config        *RecoveryConfig
	disabledTools map[string]bool
	span          *observability.Span

	// Run of identical consecutive tool calls. repeatMu guards lastCallKey,
	// repeatCount and pendingHint; repeatThresholds is read-only after
	// construction.
	repeatMu         sync.Mutex
	lastCallKey      string
	repeatCount      int
	repeatThresholds map[int]bool
	// pendingHint is the note for the next provider call. It is never stored in
	// a tool row: a note in the row would break JSON and tabular results, and
	// would persist into later turns.
	pendingHint string
}

func newRecoveryOrchestrator(config *RecoveryConfig, span *observability.Span) *recoveryOrchestrator {
	if config == nil {
		config = DefaultRecoveryConfig()
	}
	thresholds := make(map[int]bool, len(config.RepeatHintThresholds))
	for _, n := range config.RepeatHintThresholds {
		thresholds[n] = true
	}
	return &recoveryOrchestrator{
		config:           config,
		disabledTools:    make(map[string]bool),
		span:             span,
		repeatThresholds: thresholds,
	}
}

// noteToolCall records one executed tool call keyed by callKey (name + canonical
// input) and reports the length of the current run of identical consecutive
// calls and whether that length is a configured hint threshold.
// A nil receiver returns (0, false).
func (r *recoveryOrchestrator) noteToolCall(callKey string) (count int, hint bool) {
	if r == nil {
		return 0, false
	}
	r.repeatMu.Lock()
	defer r.repeatMu.Unlock()
	if callKey == r.lastCallKey {
		r.repeatCount++
	} else {
		r.lastCallKey = callKey
		r.repeatCount = 1
	}
	return r.repeatCount, r.repeatThresholds[r.repeatCount]
}

// repeatHintRecoveryConfig returns the recovery tunables an agent gets from its
// metadata: the repeat-call thresholds when the agent opted in with
// behavior.repeat_call_hint (config_loader.go), otherwise nil (no hints).
func repeatHintRecoveryConfig(metadata map[string]string) *RecoveryConfig {
	if metadata["_repeat_call_hint"] != "true" {
		return nil
	}
	return &RecoveryConfig{RepeatHintThresholds: repeatHintThresholds}
}

// setRepeatHint stores the note to add to the next provider call.
func (r *recoveryOrchestrator) setRepeatHint(note string) {
	if r == nil {
		return
	}
	r.repeatMu.Lock()
	defer r.repeatMu.Unlock()
	r.pendingHint = note
}

// takeRepeatHint returns the pending note and clears it, so each note reaches
// exactly one provider call. A nil receiver returns "".
func (r *recoveryOrchestrator) takeRepeatHint() string {
	if r == nil {
		return ""
	}
	r.repeatMu.Lock()
	defer r.repeatMu.Unlock()
	note := r.pendingHint
	r.pendingHint = ""
	return note
}

// recoverToolCB handles a tool whose circuit breaker has opened.
// Removes the tool from the local tools slice and returns a synthetic result
// for the caller to inject into the conversation.
func (r *recoveryOrchestrator) recoverToolCB(
	_ context.Context,
	toolName string,
	tools *[]shuttle.Tool,
) (recovered bool, syntheticResult *shuttle.Result) {
	r.disabledTools[toolName] = true

	// Filter tool from local slice (does NOT mutate the agent-level registry).
	filtered := make([]shuttle.Tool, 0, len(*tools))
	for _, t := range *tools {
		if t.Name() != toolName {
			filtered = append(filtered, t)
		}
	}
	*tools = filtered

	if r.span != nil {
		r.span.AddEvent("recovery.tool_cb.disabled", map[string]any{
			"tool_name":       toolName,
			"remaining_tools": len(filtered),
		})
	}

	return true, &shuttle.Result{
		Success: false,
		Error: &shuttle.Error{
			Code:    "tool_disabled",
			Message: fmt.Sprintf("Tool %s unavailable due to repeated failures. Use alternatives.", toolName),
		},
	}
}

// activeTools returns in with circuit-breaker-disabled tools removed. It keeps a
// tool disabled across turns even though the advertised set is re-derived each
// provider call. A nil receiver (self-healing disabled) returns in unchanged.
func (r *recoveryOrchestrator) activeTools(in []shuttle.Tool) []shuttle.Tool {
	if r == nil || len(r.disabledTools) == 0 {
		return in
	}
	out := make([]shuttle.Tool, 0, len(in))
	for _, t := range in {
		if !r.disabledTools[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}

// buildRecoverableError constructs a RecoverableError for Tier 3.
func (r *recoveryOrchestrator) buildRecoverableError(
	errorType string,
	cause error,
	action string,
	payload map[string]any,
) *RecoverableError {
	return &RecoverableError{
		ErrorType:       errorType,
		Message:         cause.Error(),
		RecoveryAction:  action,
		RecoveryPayload: payload,
		Retryable:       action != "",
		Cause:           cause,
	}
}

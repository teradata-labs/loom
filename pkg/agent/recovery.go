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

// RecoveryConfig holds tunables for the self-healing orchestrator. The
// destructive trim knobs are gone with the second relief ladder (blueprint
// A5): no code path outside releasePressure can remove or shrink a message.
type RecoveryConfig struct{}

// DefaultRecoveryConfig returns sensible defaults.
func DefaultRecoveryConfig() *RecoveryConfig {
	return &RecoveryConfig{}
}

// recoveryOrchestrator coordinates Tier 1 self-healing attempts within
// a single conversation loop execution. One instance per loop invocation.
type recoveryOrchestrator struct {
	config        *RecoveryConfig
	disabledTools map[string]bool
	span          *observability.Span
}

func newRecoveryOrchestrator(config *RecoveryConfig, span *observability.Span) *recoveryOrchestrator {
	if config == nil {
		config = DefaultRecoveryConfig()
	}
	return &recoveryOrchestrator{
		config:        config,
		disabledTools: make(map[string]bool),
		span:          span,
	}
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

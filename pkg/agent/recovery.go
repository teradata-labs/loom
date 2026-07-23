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
	"time"

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

// TrimableMemory is a runtime-asserted interface for memory managers that
// support destructive trimming. Kept separate from SegmentedMemoryInterface
// to avoid a breaking change to that contract.
type TrimableMemory interface {
	TrimLastN(n int) int
	AggressiveTrim(keepLastN int) (beforeTokens, afterTokens int)
}

// RecoveryConfig holds tunables for the self-healing orchestrator.
type RecoveryConfig struct {
	AggressiveTrimKeepLastN int // Messages to retain during aggressive trim (default: 4)
}

// DefaultRecoveryConfig returns sensible defaults.
func DefaultRecoveryConfig() *RecoveryConfig {
	return &RecoveryConfig{
		AggressiveTrimKeepLastN: 4,
	}
}

// recoveryOrchestrator coordinates Tier 1 self-healing attempts within
// a single conversation loop execution. One instance per loop invocation.
type recoveryOrchestrator struct {
	config                         *RecoveryConfig
	outputTokenCBRecoveryAttempted bool
	disabledTools                  map[string]bool
	span                           *observability.Span
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

// recoverOutputTokenCB attempts to self-heal after the output token circuit
// breaker fires. One attempt per conversation (prevents infinite retry loops).
//
// Strategy: trim the broken turns from memory and inject a recovery nudge
// asking the LLM to simplify its approach.
func (r *recoveryOrchestrator) recoverOutputTokenCB(
	ctx context.Context,
	session sessionForRecovery,
	segMem TrimableMemory,
	failureTracker *consecutiveFailureTracker,
	threshold int,
) (bool, error) {
	if r.outputTokenCBRecoveryAttempted {
		return false, nil
	}
	r.outputTokenCBRecoveryAttempted = true

	failureTracker.clearOutputTokenExhaustion()

	recoveryMsg := Message{
		Role:      "user",
		Content:   "Your previous responses exceeded the output limit and were truncated. Simplify your approach — break the task into smaller steps, call one tool at a time, and keep responses concise.",
		Timestamp: time.Now(),
	}
	session.AddMessage(ctx, recoveryMsg)

	if r.span != nil {
		r.span.AddEvent("recovery.output_token_cb.attempted", map[string]any{
			"threshold": threshold,
		})
	}

	return true, nil
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

// sessionForRecovery is the subset of Session that recovery needs.
// Decouples recovery from the full types.Session for testability.
type sessionForRecovery interface {
	AddMessage(ctx context.Context, msg Message)
	TrimLastN(n int)
}

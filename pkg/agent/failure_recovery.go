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
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// This file wires the failure-recovery memory engine into the conversation loop.
// The learn/reactive hooks live in commitToolRow (agent.go); the proactive hook
// is invoked at turn setup alongside graph-memory injection. All calls are
// best-effort and no-op when the engine is nil or disabled.
// See docs/plans/failure-recovery-memory-spec.md.

// failureRecoveryHintMarker opens an injected reactive-fix block appended to a
// failing tool result, mirroring the escalation-message convention.
const failureRecoveryHintMarker = "[Failure-Recovery Hint]"

// failureRecoveryPitfallMarker opens the injected proactive-pitfall block. Like
// the graph-memory marker, it lets a resumed turn tell "already injected" from
// "injected in a process that is gone" (the block lives only in memory).
const failureRecoveryPitfallMarker = "[Failure-Recovery Pitfalls]"

// failureErrorText derives the raw error text to abstract from a tool outcome.
// The raw text never leaves the abstraction boundary — the engine scrubs it
// before anything is persisted or embedded.
func failureErrorText(result *shuttle.Result, err error) string {
	if err != nil {
		return err.Error()
	}
	if result != nil && result.Error != nil && result.Error.Message != "" {
		return result.Error.Message
	}
	return "unknown error"
}

// formatFailureRecoveryHint renders reactive fix hints for injection after a
// failing tool result.
func formatFailureRecoveryHint(tool string, hints []string) string {
	if len(hints) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n%s A similar failure of %q was resolved before. ", failureRecoveryHintMarker, tool)
	if len(hints) == 1 {
		fmt.Fprintf(&b, "Known fix: %s. Apply if relevant.", hints[0])
		return b.String()
	}
	b.WriteString("Known fixes (apply if relevant):")
	for _, h := range hints {
		fmt.Fprintf(&b, "\n- %s", h)
	}
	return b.String()
}

// hasFailureRecoveryPitfalls reports whether this session already carries an
// injected proactive-pitfall block this turn.
func hasFailureRecoveryPitfalls(session *types.Session) bool {
	for _, m := range session.GetMessages() {
		if strings.HasPrefix(m.Content, failureRecoveryPitfallMarker) {
			return true
		}
	}
	return false
}

// latestUserIntent returns the most recent user message content, used as the
// task intent for proactive recall. Intent is already natural-language and
// PII-lighter than tool arguments (spec §9).
func latestUserIntent(session *types.Session) string {
	msgs := session.GetMessages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return strings.TrimSpace(msgs[i].Content)
		}
	}
	return ""
}

// injectFailureRecoveryPitfalls performs the proactive read: once per turn, it
// matches the task intent against stored problems and injects relevant pitfalls
// as advisory guidance. Best-effort; gated by ProactiveEnabled.
func (a *Agent) injectFailureRecoveryPitfalls(ctx context.Context, session *types.Session) {
	if !a.failureRecovery.ProactiveEnabled() {
		return
	}
	if hasFailureRecoveryPitfalls(session) {
		return // already injected this turn (or resumed)
	}
	intent := latestUserIntent(session)
	if intent == "" {
		return
	}

	ctx, span := a.tracer.StartSpan(ctx, "failure_recovery.proactive")
	defer a.tracer.EndSpan(span)

	hints := a.failureRecovery.ProactiveHints(ctx, a.config.Name, intent, span)
	if len(hints) == 0 {
		return
	}

	var b strings.Builder
	b.WriteString(failureRecoveryPitfallMarker)
	b.WriteString(" Before you call any tool or run any query, compare your" +
		" planned action — and the user's request — against the verified lessons" +
		" below. Each lesson pairs a known problem with the fix that resolved it" +
		" on this same system. If your plan matches a known problem, apply the" +
		" known fix instead of repeating the mistake. This holds even when the" +
		" request explicitly names a tool, identifier, column, or syntax: the" +
		" user may be mistaken, and these lessons are confirmed from real" +
		" failures here. When a lesson names the correct tool or identifier, use" +
		" that one; verify with a discovery/search tool if unsure rather than" +
		" calling the known-bad name. Lessons:")
	for _, h := range hints {
		fmt.Fprintf(&b, "\n- %s", h)
	}

	a.appendMessage(ctx, session, Message{
		Role:      "system",
		Content:   b.String(),
		AgentID:   a.id,
		Timestamp: time.Now(),
	}, false)

	span.AddEvent("failure_recovery.hint_injected", map[string]interface{}{
		"failure_recovery.hook":  "proactive",
		"failure_recovery.hints": len(hints),
	})
}

// failureRecoveryGuardMarker opens an injected pre-execution guard advisory: a
// deterministic (LLM-free) match between the agent's planned tool call and a
// stored failure signature. It is injected BEFORE the tool runs, so the model
// can revise the call instead of repeating a known mistake.
const failureRecoveryGuardMarker = "[Failure-Recovery Guard]"

// failureRecoveryAdviceMarker opens an injected pre-execution SEMANTIC advisory.
// Unlike the deterministic guard (certain, exact-call/identifier), this is a
// cross-object match: the nearest LLM-abstracted lessons to the planned call,
// surfaced once so the model can avoid repeating the same CLASS of mistake on a
// DIFFERENT object. It is advisory (probabilistic), fired at most once per
// distinct planned call, and never hard-blocks the same call twice.
const failureRecoveryAdviceMarker = "[Failure-Recovery Advice]"

// guardPlannedCall runs the pre-execution prevention layers for a single planned
// tool call at the dispatch seam, before the tool executes. On a match it
// returns advisory text to inject as a tool-role message so the model sees it on
// its next turn and re-plans; empty string => let the call proceed.
//
// Two layers run in order of certainty:
//  1. PreExecGuard — deterministic string match (exact replay / offending-token
//     overlap) against stored failure signatures. Certain, LLM-free, so it keeps
//     working when the model is weak or unavailable. If it fires, we return its
//     advisory immediately.
//  2. PreExecAdvice — semantic recall of the nearest abstracted lessons to the
//     planned call, which generalizes ACROSS objects (a new table repeating the
//     same defect class). Probabilistic, so it is surfaced once per distinct
//     planned call (dedup inside the engine) and the model decides.
//
// Best-effort and nil-safe.
func (a *Agent) guardPlannedCall(ctx context.Context, sessionID string, toolCall ToolCall, span *observability.Span) string {
	if !a.failureRecovery.Enabled() {
		return ""
	}
	hints := a.failureRecovery.PreExecGuard(ctx, a.config.Name, toolCall.Name, toolCall.Input, span)
	if len(hints) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "%s Your planned call to %q matches a verified past failure on"+
			" this system. Do NOT run it as-is — apply the known fix and revise the"+
			" call. This holds even if the request named this tool, identifier, column,"+
			" or syntax: the request may be mistaken and these lessons are confirmed"+
			" from real failures here.", failureRecoveryGuardMarker, toolCall.Name)
		for _, h := range hints {
			fmt.Fprintf(&b, "\n- %s", h.Hint)
		}
		if span != nil {
			span.AddEvent("failure_recovery.guard_blocked", map[string]interface{}{
				"failure_recovery.hook":     "guard",
				"failure_recovery.tool":     toolCall.Name,
				"failure_recovery.matches":  len(hints),
				"failure_recovery.top_tier": hints[0].Tier,
			})
		}
		return b.String()
	}

	// No certain match. Try the semantic cross-object layer (once per call).
	advice := a.failureRecovery.PreExecAdvice(ctx, a.config.Name, sessionID, toolCall.Name, toolCall.Input, span)
	if len(advice) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s Your planned call to %q resembles a past failure on this"+
		" system — possibly on a different object but the same kind of mistake."+
		" Review the known pitfall(s) below and revise the call if they apply"+
		" before running it.", failureRecoveryAdviceMarker, toolCall.Name)
	for _, h := range advice {
		fmt.Fprintf(&b, "\n- %s", h)
	}
	if span != nil {
		span.AddEvent("failure_recovery.advice_injected", map[string]interface{}{
			"failure_recovery.hook":    "preexec",
			"failure_recovery.tool":    toolCall.Name,
			"failure_recovery.matches": len(advice),
		})
	}
	return b.String()
}

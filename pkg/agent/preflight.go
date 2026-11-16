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
	"go.uber.org/zap"
)

// Pre-flight guarantee: no request whose calibrated estimate exceeds the usable
// window (window minus reserved output) is sent. Normal relief aims at the
// water marks; this is the hard line behind it.
const (
	// preflightReliefPasses bounds the deeper relief passes tried before the
	// fallback view. Each pass lowers both marks a further
	// pressureRecoveryPenalty points.
	preflightReliefPasses = 2
)

// preflightFallbackTurns are the newest-turn counts the fallback view tries,
// largest first. The current turn is always included, so 1 means "this turn
// alone".
var preflightFallbackTurns = []int64{4, 2, 1}

// FitResult is the outcome of EnsureFits.
type FitResult struct {
	// Fits is false only when even the smallest fallback view is over usable.
	Fits bool
	// Estimate is the calibrated estimate of what will be sent (the fallback
	// view's, when one is used).
	Estimate int
	// Usable is the window minus the output reservation.
	Usable int
	// Shed is true when a deeper relief pass changed the stored context, so the
	// caller must recompile messages and the advertised tools.
	Shed bool
	// Fallback is true when the send view was narrowed to ROM, the summary and
	// the newest TurnsKept turns. Nothing is evicted or folded in the store.
	Fallback bool
	// TurnsKept is the number of newest turns in the fallback view.
	TurnsKept int
	// ThinkingPinned is true when the current turn already sent thinking blocks,
	// so the view in front of them could not be changed (no deeper relief, no
	// new or narrower fallback).
	ThinkingPinned bool
	// PendingReminderBytes is the size of the not-yet-pinned reminders the
	// estimate counted (SetTransientTail).
	PendingReminderBytes int
}

// EnsureFits makes the next request fit the usable window or reports that it
// cannot. In order: deeper relief passes (evict/fold with lower marks), then a
// provider-send view of ROM, the summary, active skills' bodies and the newest
// N turns for N in preflightFallbackTurns. The fallback does not touch the
// store, so the next turn's relief can still fold the dropped turns into the
// summary.
//
// A view chosen in a turn holds for the rest of that turn and only ever
// narrows: widening it again would change what precedes the turn's own
// thinking blocks, which the provider rejects. Once the turn has sent thinking
// blocks the view is not changed at all: a request over the window is refused
// (Fits false, ThinkingPinned) instead of reshaped into one the provider would
// reject.
func (sm *SegmentedMemory) EnsureFits(ctx context.Context) FitResult {
	var res FitResult
	sm.mu.Lock()
	t := sm.currentTurnLocked()
	var held int64
	if sm.fallbackTurns > 0 && sm.fallbackTurn == t {
		held = sm.fallbackTurns
	} else {
		sm.fallbackTurns, sm.fallbackTurn = 0, 0
	}
	res.ThinkingPinned = sm.turnSentThinkingLocked(t)
	res.PendingReminderBytes = len(sm.transientTail)
	sm.mu.Unlock()

	for pass := 1; ; pass++ {
		sm.mu.Lock()
		est, usable := sm.viewEstimateLocked(held), sm.usableLocked()
		sm.mu.Unlock()
		res.Estimate, res.Usable = est, usable
		// usable <= 0 means no window is configured: nothing to guard against.
		if est <= usable || usable <= 0 {
			res.Fits = true
			res.Fallback, res.TurnsKept = held > 0, int(held)
			return res
		}
		if res.ThinkingPinned || pass > preflightReliefPasses {
			break
		}
		shed, _, _ := sm.ReleasePressure(ctx, pass*pressureRecoveryPenalty)
		if !shed {
			break
		}
		res.Shed = true
	}
	if res.ThinkingPinned {
		res.Fallback, res.TurnsKept = held > 0, int(held)
		return res
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	for _, n := range preflightFallbackTurns {
		if held > 0 && n >= held {
			continue
		}
		if est := sm.viewEstimateLocked(n); est <= res.Usable {
			sm.fallbackTurns, sm.fallbackTurn = n, sm.currentTurnLocked()
			res.Fits, res.Fallback, res.Estimate, res.TurnsKept = true, true, est, int(n)
			return res
		}
	}
	// Report the smallest view's size: that is what could not be made to fit.
	res.Estimate = sm.viewEstimateLocked(1)
	return res
}

// viewEstimateLocked is the estimate of the view keeping the newest n turns,
// or of the whole context when n is 0. Must hold lock.
func (sm *SegmentedMemory) viewEstimateLocked(n int64) int {
	if n <= 0 {
		return sm.estimateLocked()
	}
	return sm.estimateCompiledLocked(sm.compileViewLocked(sm.currentTurnLocked() - n + 1))
}

// turnSentThinkingLocked reports whether turn t holds an assistant message
// with thinking blocks, which the provider checks against what precedes it.
// Must hold lock.
func (sm *SegmentedMemory) turnSentThinkingLocked(t int64) bool {
	for i := len(sm.contextMessages) - 1; i >= 0; i-- {
		m := &sm.contextMessages[i]
		if m.Turn > 0 && m.Turn < t {
			break
		}
		if m.Turn == t && m.Role == "assistant" && len(m.ThinkingBlocks) > 0 {
			return true
		}
	}
	return false
}

// preflightFit is the hard guard before a provider call (behavior.preflight_fit).
// It returns the messages and tools to send, or a context_exhausted error when
// the request cannot be made to fit — raised here so the user sees an
// actionable message instead of a provider refusal that would also be billed.
func (a *Agent) preflightFit(ctx Context, span *observability.Span, session *Session, segMem *SegmentedMemory,
	recovery *recoveryOrchestrator, messages []Message, tools []shuttle.Tool) ([]Message, []shuttle.Tool, error) {
	if a.config == nil || !a.config.PreflightFit {
		return messages, tools, nil
	}
	fit := segMem.EnsureFits(ctx)

	if fit.Shed || fit.Fallback {
		messages = segMem.GetMessagesForSend()
	}
	if fit.Shed {
		tools = recovery.activeTools(a.advertisedTools(session))
		a.setAdvertisedTools(segMem, tools)
	}
	if !fit.Fits {
		zap.L().Error("preflight: request cannot fit the usable window; not sending",
			zap.String("session_id", session.ID),
			zap.Int("estimate_tokens", fit.Estimate),
			zap.Int("usable_tokens", fit.Usable))
		if span != nil {
			span.AddEvent("context.preflight_refused", map[string]interface{}{
				"estimate_tokens": fit.Estimate,
				"usable_tokens":   fit.Usable,
				"shed":            fit.Shed,
				"thinking_pinned": fit.ThinkingPinned,
			})
		}
		why := "even with only the current turn kept"
		if fit.ThinkingPinned {
			why = "and this turn's reasoning cannot be resent after trimming the conversation before it"
		}
		cause := fmt.Errorf("conversation too long: the request is about %d tokens against a usable window of %d "+
			"%s; compact the conversation or start a new session",
			fit.Estimate, fit.Usable, why)
		return nil, nil, recovery.buildRecoverableError("context_exhausted", cause, "",
			map[string]any{"estimate": fit.Estimate, "usable": fit.Usable, "preflight": true, "thinking_pinned": fit.ThinkingPinned})
	}
	if fit.Fallback {
		zap.L().Warn("preflight: sending fallback view (system prompt, summary, newest turns)",
			zap.String("session_id", session.ID),
			zap.Int("estimate_tokens", fit.Estimate),
			zap.Int("usable_tokens", fit.Usable),
			zap.Int("turns_kept", fit.TurnsKept))
		if span != nil {
			span.AddEvent("context.preflight_fallback", map[string]interface{}{
				"estimate_tokens": fit.Estimate,
				"usable_tokens":   fit.Usable,
				"turns_kept":      fit.TurnsKept,
				"shed":            fit.Shed,
			})
		}
	} else if fit.Shed {
		zap.L().Info("preflight: deeper relief brought the request under the usable window",
			zap.String("session_id", session.ID),
			zap.Int("estimate_tokens", fit.Estimate),
			zap.Int("usable_tokens", fit.Usable))
	} else {
		zap.L().Debug("preflight: request fits",
			zap.String("session_id", session.ID),
			zap.Int("estimate_tokens", fit.Estimate),
			zap.Int("usable_tokens", fit.Usable),
			zap.Int("pending_reminder_bytes", fit.PendingReminderBytes))
	}
	return messages, tools, nil
}

// synthesisTooLong is the error for a synthesis request the guard would not
// send; the caller answers with the guidance message instead.
func synthesisTooLong(span *observability.Span, sessionID string, fit FitResult) error {
	if span != nil {
		span.AddEvent("context.preflight_refused", map[string]interface{}{
			"estimate_tokens": fit.Estimate,
			"usable_tokens":   fit.Usable,
			"synthesis":       true,
		})
	}
	zap.L().Error("preflight: synthesis request cannot fit the usable window; not sending",
		zap.String("session_id", sessionID),
		zap.Int("estimate_tokens", fit.Estimate),
		zap.Int("usable_tokens", fit.Usable))
	return fmt.Errorf("conversation too long: synthesis request is about %d tokens against a usable window of %d",
		fit.Estimate, fit.Usable)
}

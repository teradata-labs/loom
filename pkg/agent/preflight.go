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
}

// EnsureFits makes the next request fit the usable window or reports that it
// cannot. In order: deeper relief passes (evict/fold with lower marks), then a
// transient send-side view of ROM + summary + the newest N turns for N in
// preflightFallbackTurns. The fallback does not touch the store, so the next
// call's relief can still fold the dropped turns into the summary.
func (sm *SegmentedMemory) EnsureFits(ctx context.Context) FitResult {
	sm.mu.Lock()
	sm.fallbackTurns = 0
	sm.mu.Unlock()

	var res FitResult
	for pass := 1; ; pass++ {
		sm.mu.Lock()
		est, usable := sm.estimateLocked(), sm.usableLocked()
		sm.mu.Unlock()
		res.Estimate, res.Usable = est, usable
		// usable <= 0 means no window is configured: nothing to guard against.
		if est <= usable || usable <= 0 {
			res.Fits = true
			return res
		}
		if pass > preflightReliefPasses {
			break
		}
		shed, _, _ := sm.ReleasePressure(ctx, pass*pressureRecoveryPenalty)
		if !shed {
			break
		}
		res.Shed = true
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	compiled := sm.compileLocked()
	t := sm.currentTurnLocked()
	for _, n := range preflightFallbackTurns {
		kept := keepNewestTurns(compiled, t, n)
		if est := sm.estimateCompiledLocked(kept); est <= res.Usable {
			sm.fallbackTurns = n
			res.Fits, res.Fallback, res.Estimate, res.TurnsKept = true, true, est, int(n)
			return res
		}
	}
	// Report the smallest view's size: that is what could not be made to fit.
	res.Estimate = sm.estimateCompiledLocked(keepNewestTurns(compiled, t, 1))
	return res
}

// keepNewestTurns keeps every system message (ROM, summary) and the messages of
// the newest n turns up to t, in order. Turns are kept whole, so a tool call
// and its results stay together; a synthetic failed result carries no turn and
// follows the turn of the message before it.
func keepNewestTurns(compiled []Message, t, n int64) []Message {
	floor := t - n // keep turns strictly above floor
	kept := make([]Message, 0, len(compiled))
	var turn int64
	for i := range compiled {
		m := compiled[i]
		if m.Role == "system" {
			kept = append(kept, m)
			continue
		}
		if m.Turn > 0 {
			turn = m.Turn
		}
		if turn > floor {
			kept = append(kept, m)
		}
	}
	return kept
}

// preflightFit is the hard guard before a provider call. It returns the
// messages and tools to send, or a context_exhausted error when the request
// cannot be made to fit — raised here so the user sees an actionable message
// instead of a provider refusal that would also be billed.
func (a *Agent) preflightFit(ctx Context, session *Session, segMem *SegmentedMemory, recovery *recoveryOrchestrator,
	messages []Message, tools []shuttle.Tool) ([]Message, []shuttle.Tool, error) {
	fit := segMem.EnsureFits(ctx)

	if fit.Shed || fit.Fallback {
		messages = session.GetMessages()
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
		cause := fmt.Errorf("conversation too long: the request is about %d tokens against a usable window of %d "+
			"even with only the current turn kept; compact the conversation or start a new session",
			fit.Estimate, fit.Usable)
		return nil, nil, recovery.buildRecoverableError("context_exhausted", cause, "",
			map[string]any{"estimate": fit.Estimate, "usable": fit.Usable, "preflight": true})
	}
	if fit.Fallback {
		zap.L().Warn("preflight: sending fallback view (system prompt, summary, newest turns)",
			zap.String("session_id", session.ID),
			zap.Int("estimate_tokens", fit.Estimate),
			zap.Int("usable_tokens", fit.Usable),
			zap.Int("turns_kept", fit.TurnsKept))

	} else if fit.Shed {
		zap.L().Info("preflight: deeper relief brought the request under the usable window",
			zap.String("session_id", session.ID),
			zap.Int("estimate_tokens", fit.Estimate),
			zap.Int("usable_tokens", fit.Usable))
	}
	return messages, tools, nil
}

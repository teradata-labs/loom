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
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

const (
	// autoCompactMinTurnGap is the fewest turns between two auto-compaction
	// attempts, so a context the kept turns hold above the threshold does not
	// pay a summary call every turn.
	autoCompactMinTurnGap = 3

	// autoCompactTimeout bounds the whole end-of-turn pass; it runs while the
	// turn's caller still waits, so a stuck compressor must not hold it.
	autoCompactTimeout = 90 * time.Second

	// autoCompactStubOnlyPercent: when stubbing older tool results alone brings
	// the context under this share of the threshold, the fold, an LLM call, is
	// skipped.
	autoCompactStubOnlyPercent = 80
)

// AutoCompactLedger remembers, per session, the turn of the last automatic
// compaction attempt, so a session that stays above the threshold (or whose
// summarizer is failing) does not pay for an attempt every turn even when the
// embedder builds fresh memory each turn. Each Agent owns one and drops a
// session's entry in DeleteSession; an embedder that builds a new Agent per
// turn shares one across them (WithAutoCompactLedger) and calls Forget when
// it deletes a session. Safe for concurrent use.
type AutoCompactLedger struct {
	mu    sync.Mutex
	turns map[string]int64
}

// NewAutoCompactLedger returns an empty ledger.
func NewAutoCompactLedger() *AutoCompactLedger {
	return &AutoCompactLedger{turns: map[string]int64{}}
}

func (l *AutoCompactLedger) last(sessionID string) int64 {
	if l == nil || sessionID == "" {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.turns[sessionID]
}

func (l *AutoCompactLedger) note(sessionID string, turn int64) {
	if l == nil || sessionID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.turns == nil {
		l.turns = map[string]int64{}
	}
	l.turns[sessionID] = turn
}

// Forget drops the session's entry.
func (l *AutoCompactLedger) Forget(sessionID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.turns, sessionID)
}

// autoCompactCoolingDownLocked reports whether the last attempt on this
// memory was fewer than autoCompactMinTurnGap turns ago. A turn number below
// the recorded one means the history was reset and numbering restarted, so the
// old record no longer applies. Must hold lock.
func (sm *SegmentedMemory) autoCompactCoolingDownLocked(turn int64) bool {
	last := sm.lastAutoCompactTurn
	since := turn - last
	return last > 0 && since >= 0 && since < autoCompactMinTurnGap
}

// autoCompactAttempt is the turn of the latest AutoCompact attempt on this
// memory (0: none).
func (sm *SegmentedMemory) autoCompactAttempt() int64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.lastAutoCompactTurn
}

// seedAutoCompactAttempt carries an attempt recorded outside this memory
// instance (AutoCompactLedger) into it.
func (sm *SegmentedMemory) seedAutoCompactAttempt(turn int64) {
	if turn <= 0 {
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.lastAutoCompactTurn = turn
}

// foldableTurnsLocked counts the distinct turns up to boundary still in
// context. Folding batches at least autoCompactMinTurnGap of them per summary
// call, a limit read from the stored rows, so it holds across restarts too.
// Must hold lock.
func (sm *SegmentedMemory) foldableTurnsLocked(boundary int64) int {
	seen := map[int64]struct{}{}
	for i := range sm.contextMessages {
		if t := sm.contextMessages[i].Turn; t <= boundary {
			seen[t] = struct{}{}
		}
	}
	return len(seen)
}

// AutoCompact is the proactive compaction: once the context reaches percent of
// the usable window it stubs older tool results (lossless, re-fetchable) and,
// only if that is not enough, folds older turns into the running summary — at
// that point there is still room for a good summary, unlike at the relief
// start mark. The newest DefaultCompactKeepTurns turns stay verbatim.
//
// It never drops history for want of a summary: with no LLM compressor the
// fold is skipped, and a fold whose compressor fails aborts. Attempts are at
// least autoCompactMinTurnGap turns apart. Call it between turns, under the
// session's lease.
func (sm *SegmentedMemory) AutoCompact(ctx context.Context, percent int) (CompactResult, error) {
	var res CompactResult
	if percent <= 0 || percent >= 100 {
		return res, nil
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	usable := sm.usableLocked()
	turn := sm.currentTurnLocked()
	boundary := turn - DefaultCompactKeepTurns
	if sm.reliefInFlight || usable <= 0 || boundary < 1 || sm.autoCompactCoolingDownLocked(turn) {
		return res, nil
	}
	threshold := percent * usable / 100
	res.TokensBefore = sm.estimateLocked()
	res.TokensAfter = res.TokensBefore
	res.SummaryVersion = sm.summary.n
	if res.TokensBefore < threshold {
		return res, nil
	}

	sm.reliefInFlight = true
	defer func() { sm.reliefInFlight = false }()
	sm.compressorFailedThisPass = false
	sm.lastAutoCompactTurn = turn

	res.ToolResultsStubbed = sm.evictLocked(ctx, boundary)
	if sm.estimateLocked() > threshold*autoCompactStubOnlyPercent/100 &&
		sm.foldableTurnsLocked(boundary) >= autoCompactMinTurnGap &&
		sm.compressor != nil && sm.compressor.IsEnabled() {
		sm.foldRequireSummary = true
		res.TurnsFolded = sm.foldLocked(ctx, boundary)
		sm.foldRequireSummary = false
	}
	res.Compacted = res.ToolResultsStubbed || res.TurnsFolded
	res.TokensAfter = sm.estimateLocked()
	res.SummaryVersion = sm.summary.n

	zap.L().Info("compaction: auto compact complete",
		zap.String("session_id", sm.sessionID),
		zap.Int64("turn", turn),
		zap.Int("threshold_tokens", threshold),
		zap.Bool("tool_results_stubbed", res.ToolResultsStubbed),
		zap.Bool("turns_folded", res.TurnsFolded),
		zap.Int("tokens_before", res.TokensBefore),
		zap.Int("tokens_after", res.TokensAfter),
		zap.Int("tokens_saved", res.TokensSaved()))
	return res, nil
}

// maybeAutoCompact runs the proactive compaction after a finished turn (when
// Config.AutoCompactPercent is set) and records it on the response. It runs
// while the turn's caller still holds the session, so the next turn cannot
// start under it. A failure never fails the turn that just succeeded.
func (a *Agent) maybeAutoCompact(ctx context.Context, sess *Session, span *observability.Span, response *Response) {
	if a.config == nil || a.config.AutoCompactPercent <= 0 {
		return
	}
	segMem, ok := sess.SegmentedMem.(*SegmentedMemory)
	if !ok || segMem == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, autoCompactTimeout)
	defer cancel()

	segMem.seedAutoCompactAttempt(a.autoCompactLedger.last(sess.ID))
	before := segMem.autoCompactAttempt()
	res, err := segMem.AutoCompact(ctx, a.config.AutoCompactPercent)
	if after := segMem.autoCompactAttempt(); after != before {
		a.autoCompactLedger.note(sess.ID, after)
	}
	if err != nil {
		zap.L().Warn("auto compaction failed", zap.String("session_id", sess.ID), zap.Error(err))
		return
	}
	if !res.Compacted {
		return
	}
	if response.Metadata == nil {
		response.Metadata = map[string]interface{}{}
	}
	response.Metadata["auto_compaction"] = map[string]interface{}{
		"tokens_before":        res.TokensBefore,
		"tokens_after":         res.TokensAfter,
		"tokens_saved":         res.TokensSaved(),
		"tool_results_stubbed": res.ToolResultsStubbed,
		"turns_folded":         res.TurnsFolded,
	}
	span.AddEvent("context.auto_compacted", map[string]interface{}{
		"tokens_before": res.TokensBefore,
		"tokens_after":  res.TokensAfter,
		"turns_folded":  res.TurnsFolded,
	})
}

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
	"errors"
	"fmt"
	"strings"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/session"
	"go.uber.org/zap"
)

// DefaultCompactKeepTurns is how many of the newest turns a manual compaction
// leaves verbatim.
const DefaultCompactKeepTurns = 2

// MaxCompactFocusBytes bounds the user's focus text so it cannot itself grow
// the compressor input without limit.
const MaxCompactFocusBytes = 2000

// ErrCompactionBusy is returned when a relief pass or another compaction is
// already running on the session's memory.
var ErrCompactionBusy = errors.New("compaction already in progress for this session")

// ErrSummaryUnavailable is returned when older turns needed folding but no
// summary could be produced (no LLM compressor, or it failed). History is
// untouched; trying again later may work.
var ErrSummaryUnavailable = errors.New("could not summarize the older conversation")

// ErrCompactionNotSaved is returned when the fold failed to write to the
// session store. The summary call may already have been paid for; history is
// untouched.
var ErrCompactionNotSaved = errors.New("could not save the compaction")

// CompactOptions steers a manual compaction.
type CompactOptions struct {
	// Focus is optional user guidance on what the summary must keep.
	Focus string
	// KeepTurns is the number of newest turns left verbatim; <= 0 means
	// DefaultCompactKeepTurns.
	KeepTurns int
}

// CompactResult reports what a manual compaction did.
type CompactResult struct {
	// Compacted is false when the session had nothing older than KeepTurns.
	Compacted bool
	// TokensBefore and TokensAfter are the calibrated tokenizer counts of the
	// compiled context around the compaction (compiledTokensLocked).
	TokensBefore int
	TokensAfter  int
	// TurnsFolded is true when older turns were folded into the running summary.
	TurnsFolded bool
	// SummaryVersion is the session summary's version after the compaction.
	SummaryVersion int
	// TurnsKept is the number of newest turns left verbatim.
	TurnsKept int
}

// TokensSaved is the estimated reduction.
func (r CompactResult) TokensSaved() int { return max(r.TokensBefore-r.TokensAfter, 0) }

// Compact is the user-requested compaction: the turns older than the newest
// KeepTurns fold into the running summary (foldLocked); ROM, the summary and
// the newest KeepTurns turns stay verbatim. It folds only. No tool result is
// stubbed, because each result was already bounded when it arrived. The fold
// never drops a region without a real summary (foldRequireSummary): when no
// summary is produced, history is unchanged and Compact returns
// ErrSummaryUnavailable. Agent.CompactSession calls it.
//
// The caller must hold the session between turns: Compact assumes no turn is
// running on the memory.
func (sm *SegmentedMemory) Compact(ctx context.Context, opts CompactOptions) (CompactResult, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	var res CompactResult
	if sm.reliefInFlight {
		return res, ErrCompactionBusy
	}
	sm.reliefInFlight = true
	defer func() { sm.reliefInFlight = false }()
	sm.compressorFailedThisPass = false

	focus := strings.TrimSpace(opts.Focus)
	if len(focus) > MaxCompactFocusBytes {
		focus = strings.ToValidUTF8(focus[:MaxCompactFocusBytes], "")
	}
	sm.compactFocus = focus
	defer func() { sm.compactFocus = "" }()

	keep := int64(opts.KeepTurns)
	if keep <= 0 {
		keep = DefaultCompactKeepTurns
	}
	res.TurnsKept = int(min(keep, sm.currentTurnLocked()))

	res.TokensBefore = sm.compiledTokensLocked()
	res.TokensAfter = res.TokensBefore
	res.SummaryVersion = sm.summary.n

	// Every turn up to and including boundary is compacted; the newest keep stay.
	boundary := sm.currentTurnLocked() - keep
	if boundary < 1 {
		return res, nil
	}

	sm.foldAbortedNoSummary, sm.compactPersistErr = false, nil
	// A requested compaction must not report success after dropping turns with
	// only a placeholder line: without a real summary the fold is skipped.
	sm.foldRequireSummary = true
	defer func() { sm.foldRequireSummary = false }()
	res.TurnsFolded = sm.foldBoundedLocked(ctx, boundary)
	summaryMissing, persistErr := sm.foldAbortedNoSummary, sm.compactPersistErr
	sm.foldAbortedNoSummary, sm.compactPersistErr = false, nil
	res.Compacted = res.TurnsFolded
	res.TokensAfter = sm.compiledTokensLocked()
	res.SummaryVersion = sm.summary.n
	if res.Compacted && (summaryMissing || persistErr != nil) {
		// A later chunk failed after an earlier one folded (foldBoundedLocked):
		// the earlier chunks stay folded, and more turns than asked stay
		// verbatim. Report the turns really kept.
		if len(sm.contextMessages) > 0 {
			res.TurnsKept = int(sm.currentTurnLocked() - sm.contextMessages[0].Turn + 1)
		}
		zap.L().Warn("compaction: folded only part of the older turns; a later chunk could not be summarized or saved",
			zap.String("session_id", sm.sessionID),
			zap.Int("turns_kept", res.TurnsKept),
			zap.Bool("summary_missing", summaryMissing),
			zap.Error(persistErr))
	}
	if !res.Compacted {
		// Nothing changed: say why rather than report an ordinary no-op.
		if persistErr != nil {
			return res, fmt.Errorf("%w: %w", ErrCompactionNotSaved, persistErr)
		}
		if summaryMissing {
			return res, ErrSummaryUnavailable
		}
	}

	zap.L().Info("compaction: manual compact complete",
		zap.String("session_id", sm.sessionID),
		zap.Int64("boundary_turn", boundary),
		zap.Bool("turns_folded", res.TurnsFolded),
		zap.Int("tokens_before", res.TokensBefore),
		zap.Int("tokens_after", res.TokensAfter),
		zap.Int("tokens_saved", res.TokensSaved()),
		zap.Bool("focus", focus != ""))
	return res, nil
}

// compiledTokensLocked is the tokenizer's count of the compiled context,
// scaled by the provider calibration. Compact reports both of its figures
// from it: the estimate answers with a byte bound under the start mark and
// the tokenizer over it, and the two are not comparable. Must hold lock.
func (sm *SegmentedMemory) compiledTokensLocked() int {
	return int(float64(sm.accurateTokensLocked(sm.compileLocked())) * sm.calibrationLocked())
}

// CompactSession compacts one session's context on user request. It takes the
// same session lease and parked-tail guard as a new turn, so it never runs
// against a live turn or buries a parked batch. It runs under its own span:
// the fold may make an LLM call.
func (a *Agent) CompactSession(ctx context.Context, sessionID string, opts CompactOptions) (res CompactResult, err error) {
	ctx = session.WithSessionID(ctx, sessionID)
	if a.tracer != nil {
		var span *observability.Span
		ctx, span = a.tracer.StartSpan(ctx, "agent.compact_session")
		defer a.tracer.EndSpan(span)
		span.SetAttribute(observability.AttrSessionID, sessionID)
		span.SetAttribute("compaction.focus", strings.TrimSpace(opts.Focus) != "")
		defer func() {
			if err != nil {
				span.RecordError(err)
				return
			}
			span.SetAttribute("compaction.compacted", res.Compacted)
			span.SetAttribute("compaction.turns_folded", res.TurnsFolded)
			span.SetAttribute("compaction.turns_kept", res.TurnsKept)
			span.SetAttribute("compaction.tokens_before", res.TokensBefore)
			span.SetAttribute("compaction.tokens_after", res.TokensAfter)
		}()
	}
	sess := a.memory.GetOrCreateSessionWithAgent(ctx, sessionID, a.config.Name, "")

	ctx, release, err := a.holdSessionLease(ctx, sessionID)
	if err != nil {
		return CompactResult{}, err
	}
	defer release()

	if err := a.guardParkedTail(ctx, sessionID, sess); err != nil {
		return CompactResult{}, err
	}
	segMem, ok := sess.SegmentedMem.(*SegmentedMemory)
	if !ok || segMem == nil {
		return CompactResult{}, fmt.Errorf("session %s has no segmented memory to compact", sessionID)
	}
	return segMem.Compact(ctx, opts)
}

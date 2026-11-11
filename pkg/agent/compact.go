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

	"github.com/teradata-labs/loom/pkg/session"
	"go.uber.org/zap"
)

// DefaultCompactKeepTurns is how many of the newest turns a manual compaction
// leaves verbatim.
const DefaultCompactKeepTurns = 2

// maxCompactFocusBytes bounds the user's focus text so it cannot itself grow
// the compressor input without limit.
const maxCompactFocusBytes = 2000

// ErrCompactionBusy is returned when a relief pass or another compaction is
// already running on the session's memory.
var ErrCompactionBusy = errors.New("compaction already in progress for this session")

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
	// TokensBefore and TokensAfter are calibrated estimates of the compiled
	// context around the compaction.
	TokensBefore int
	TokensAfter  int
	// ToolResultsStubbed is true when older tool results became re-fetchable stubs.
	ToolResultsStubbed bool
	// TurnsFolded is true when older turns were folded into the running summary.
	TurnsFolded bool
	// SummaryVersion is the session summary's version after the compaction.
	SummaryVersion int
}

// TokensSaved is the estimated reduction.
func (r CompactResult) TokensSaved() int { return max(r.TokensBefore-r.TokensAfter, 0) }

// Compact is the user-requested compaction: older tool results become stubs
// (re-fetchable) and older turns fold into the running summary; ROM, the
// summary and the newest KeepTurns turns stay verbatim. It works on a session
// already over its window — eviction runs first, so the compressor's input is
// the shrunk, stubbed rendering rather than the oversized one.
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
	sm.fallbackTurns = 0

	focus := strings.TrimSpace(opts.Focus)
	if len(focus) > maxCompactFocusBytes {
		focus = strings.ToValidUTF8(focus[:maxCompactFocusBytes], "")
	}
	sm.compactFocus = focus
	defer func() { sm.compactFocus = "" }()

	keep := int64(opts.KeepTurns)
	if keep <= 0 {
		keep = DefaultCompactKeepTurns
	}

	res.TokensBefore = sm.estimateLocked()
	res.TokensAfter = res.TokensBefore
	res.SummaryVersion = sm.summary.n

	// Every turn up to and including boundary is compacted; the newest keep stay.
	boundary := sm.currentTurnLocked() - keep
	if boundary < 1 {
		return res, nil
	}

	res.ToolResultsStubbed = sm.evictLocked(ctx, boundary)
	res.TurnsFolded = sm.foldLocked(ctx, boundary)
	res.Compacted = res.ToolResultsStubbed || res.TurnsFolded
	res.TokensAfter = sm.estimateLocked()
	res.SummaryVersion = sm.summary.n

	zap.L().Info("compaction: manual compact complete",
		zap.String("session_id", sm.sessionID),
		zap.Int64("boundary_turn", boundary),
		zap.Bool("tool_results_stubbed", res.ToolResultsStubbed),
		zap.Bool("turns_folded", res.TurnsFolded),
		zap.Int("tokens_before", res.TokensBefore),
		zap.Int("tokens_after", res.TokensAfter),
		zap.Int("tokens_saved", res.TokensSaved()),
		zap.Bool("focus", focus != ""))
	return res, nil
}

// CompactSession compacts one session's context on user request. It takes the
// same session lease and parked-tail guard as a new turn, so it never runs
// against a live turn or buries a parked batch.
func (a *Agent) CompactSession(ctx context.Context, sessionID string, opts CompactOptions) (CompactResult, error) {
	ctx = session.WithSessionID(ctx, sessionID)
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

	// The previous turn has ended: drop its in-memory payloads, as a new turn would.
	segMem.DropTurnPayloads()
	a.dropInTurnSQLite(sessionID)

	return segMem.Compact(ctx, opts)
}

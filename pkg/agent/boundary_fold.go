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

// Task-span fold: the task list is the session's liveness map. While the list
// is open, the groundwork at the front of the context is the working set for
// the remaining items and is never touched. When an item completes, the
// transcript span it occupied — bracketed by its own in_progress and done
// transitions — is dead by the list's one-item-in-progress discipline, and is
// replaced by the item's completion summary.
//
// The model knows nothing about any of this. It fills a completion summary on
// a finished deliverable — work documentation it writes willingly — and the
// harness does the rest: the transitions mark the span, the summary is the
// replacement text, and an economics gate decides whether the excision is
// worth a prefix rebuild.
//
// The fold never runs inside a turn. Transitions are recorded mid-turn
// (DeclareClosedSpan); the excision happens at the compile seam
// (ReleaseDeclared), the same safe point as pressure relief. Edge turns of
// every span are kept: the transition turns can carry neighbouring items'
// material (batched done+in_progress calls), so only strictly interior turns
// fold. User-role rows never fold, whatever span they fall in.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// declaredFoldFactor is the economics gate: fold only when the droppable
// spans total at least this many times the kept remainder. Below it, deleting
// costs more in cache re-write than it saves in reads.
const declaredFoldFactor = 2

// boardSummaryHeader marks a summary as board-rendered; its absence in the
// current summary text means another regime wrote it (compressor fold or a
// pre-reload session) and renderBoardLocked must carry it forward.
const boardSummaryHeader = "Completed work, per the agent's own task summaries:"

// closedSpan is one finished item's transcript span, pending excision.
type closedSpan struct {
	start, end int64  // transition turns; interior (start, end) folds
	line       string // board line: title + completion summary
	ruledOut   bool   // cancelled item — files under closed doors
}

// boardState accumulates completed-item summaries — the running board the
// span fold installs — plus the spans still awaiting excision.
type boardState struct {
	done     []string
	ruledOut []string
	spans    []closedSpan
	pending  bool
}

// DeclareClosedSpan records a finished item: its span and its board line.
// Called from the task-list tool on done/cancelled transitions. Mutates no
// context; the excision happens at the compile seam.
func (sm *SegmentedMemory) DeclareClosedSpan(start, end int64, line string, ruledOut bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if ruledOut {
		sm.board.ruledOut = append(sm.board.ruledOut, line)
	} else {
		sm.board.done = append(sm.board.done, line)
	}
	if end > start {
		sm.board.spans = append(sm.board.spans, closedSpan{start: start, end: end, line: line, ruledOut: ruledOut})
	}
	sm.board.pending = true
}

// renderBoardLocked renders the accumulated summaries as the fold's summary
// text. Must hold lock.
func (sm *SegmentedMemory) renderBoardLocked(loSeq, hiSeq int64) string {
	var b strings.Builder
	if hiSeq > 0 {
		fmt.Fprintf(&b, "covers msg:%d-%d\n", loSeq, hiSeq)
	}
	b.WriteString(boardSummaryHeader + "\n")
	for _, s := range sm.board.done {
		b.WriteString("- " + s + "\n")
	}
	if len(sm.board.ruledOut) > 0 {
		b.WriteString("\nCancelled:\n")
		for _, s := range sm.board.ruledOut {
			b.WriteString("- " + s + "\n")
		}
	}
	if prior := strings.TrimSpace(sm.summary.text); prior != "" && !strings.Contains(prior, boardSummaryHeader) {
		b.WriteString("\nEarlier summary (pre-boundary regime):\n" + prior + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// ReleaseDeclared excises the closed spans if the economics gate passes.
// Called at the compile seam, before ReleasePressure. Returns whether context
// changed.
func (sm *SegmentedMemory) ReleaseDeclared(ctx context.Context) (shed bool, estimate int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.reliefInFlight || !sm.board.pending || len(sm.board.spans) == 0 {
		return false, 0
	}

	// Interior-turn predicate over every pending span. Edge turns are kept
	// (transition turns can carry neighbouring items' material); the current
	// turn is never eligible by construction, since every span closed at an
	// earlier done transition.
	interior := func(turn int64) bool {
		for _, sp := range sm.board.spans {
			if turn > sp.start && turn < sp.end {
				return true
			}
		}
		return false
	}

	region := make([]Message, 0)
	kept := make([]Message, 0, len(sm.contextMessages))
	for i := range sm.contextMessages {
		m := sm.contextMessages[i]
		if m.Role != "user" && interior(m.Turn) {
			region = append(region, m)
			continue
		}
		kept = append(kept, m)
	}
	if len(region) == 0 {
		sm.board.pending = false // spans were empty of foldable rows
		return false, 0
	}

	// Economics gate on rendered tokens: the excision must outweigh the
	// prefix rebuild it forces. Below the gate the spans stay pending — the
	// next done adds mass and re-offers.
	regionTokens := 0
	for i := range region {
		regionTokens += sm.msgTokensLocked(sm.tokenCounter, &region[i])
	}
	total := sm.estimateLocked()
	keptTokens := total - regionTokens
	if keptTokens < 1 {
		keptTokens = 1
	}
	if regionTokens < declaredFoldFactor*keptTokens {
		return false, 0
	}

	var loSeq, hiSeq int64
	var seqs []int64
	for i := range region {
		if seq, err := strconv.ParseInt(region[i].ID, 10, 64); err == nil {
			if loSeq == 0 || seq < loSeq {
				loSeq = seq
			}
			if seq > hiSeq {
				hiSeq = seq
			}
			seqs = append(seqs, seq)
		}
	}
	newText := sm.renderBoardLocked(loSeq, hiSeq)

	newlyFolded := foldedSkillLoads(region, sm.currentTurnLocked())
	if len(newlyFolded) > 0 && sm.foldedSkills == nil {
		sm.foldedSkills = make(map[string]bool)
	}
	for _, name := range newlyFolded {
		sm.foldedSkills[name] = true
	}
	if len(sm.foldedSkills) > 0 {
		names := make([]string, 0, len(sm.foldedSkills))
		for n := range sm.foldedSkills {
			names = append(names, n)
		}
		sort.Strings(names)
		newText += fmt.Sprintf("\n\n[Folded active skill(s): %s — reload with manage_skills if still in use.]",
			strings.Join(names, ", "))
	}

	n1 := sm.summary.n + 1
	if sm.sessionStore != nil && sm.sessionID != "" {
		if err := sm.sessionStore.FoldMessages(ctx, sm.sessionID, seqs, n1, newText); err != nil {
			zap.L().Error("spanFold: persist failed",
				zap.String("session_id", sm.sessionID), zap.Error(err))
			return false, 0
		}
	}
	if sm.skillDeactivation != nil {
		for _, name := range newlyFolded {
			sm.skillDeactivation(sm.sessionID, name)
		}
	}

	sm.summary = summaryState{n: n1, text: newText}
	sm.contextMessages = kept
	sm.l1Dirty = true
	sm.updateTokenCount()
	sm.tokenCountDirty = false
	sm.board.spans = nil
	sm.board.pending = false

	estimate = sm.estimateLocked()
	zap.L().Info("spanFold: closed task spans excised",
		zap.String("session_id", sm.sessionID),
		zap.Int("rows_folded", len(region)),
		zap.Int("region_tokens", regionTokens),
		zap.Int("estimate_tokens", estimate),
		zap.Int("version", n1))
	return true, estimate
}

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

// In-loop full-collapse fold. When the context estimate crosses the start
// mark, the conversation loop appends ONE transient user message — the fold
// instruction — to the next call. The model answers with a JSON summary; the
// harness performs the collapse mechanically. The model's only contribution
// is the summary text: the fold range is the harness's (full collapse), the
// excision is the harness's, and the fold reply itself is never stored,
// streamed onward, or returned — from outside, the session just got shorter.
//
// The summariser is the agent itself, mid-state, on its own warm cache —
// testimony, not archaeology: no separate compressor call re-reading the
// region at full input rate, no lock released across a network call, no
// second prompt path. The compressor subsystem remains only as the failsafe
// (ReleasePressure), which runs when the in-loop fold has failed.
//
// What survives a collapse: prompt/ROM, the summary (L2), the operator's
// user rows verbatim, the assistant TEXT of the newest K turns (call/result
// pairs stripped whole — both sides always), and the pending pair. All tool
// calls and results outside the pending pair are excised: the summary has
// just harvested every conclusion, and what remains of old tool traffic is
// snapshot — re-fetchable, stale, and heavy.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// foldPromptKey names the fold ask in the prompt registry (prompts/memory/
// compaction.yaml), where every prompt loom sends lives, so an embedder can
// change it without a rebuild.
const foldPromptKey = "memory.fold"

// foldInstruction is the fold ask as shipped: the transient message appended
// to the fold call, used when no registry is wired. It is never stored; the
// model's reply to it is consumed by the harness.
const foldInstruction = `<system-reminder>
Context maintenance: summarise the work so far. Reply with ONLY this JSON —
no other text, no tool calls:

{"summary": "<the state of the work>"}

Write for one reader: yourself, continuing this work with nothing but this
summary and your most recent turns. Everything you write should serve the
remaining work; everything that serves nothing ahead is dropped.

A session summary is already in context. It is the complete record of
everything before your recent turns, and what you write now REPLACES it.
Carry forward everything in it that still serves the work; whatever you omit
is gone for good. Fold the recent turns into it — do not summarise them alone.

Write the current state in the present tense — never the story of how it got
here. Keep, in this order: the operator's instructions and rulings, verbatim
where the wording carries force; decisions with their rationale; approaches
ruled out, each with its reason; constraints and facts discovered about this
environment (schemas, conventions, paths, gotchas); where the work stands —
done, in hand, next. Identifiers and paths as pointers — never file contents
or query results; anything re-fetchable is one cheap call away, and a stale
copy is worse than none.

Drop without trace: every superseded value (only the current version of any
fact may appear); corrected errors (the correction survives, the error does
not); raw results already reduced to a conclusion; exploration that led
nowhere unless it closed a door — then it is a ruled-out line with its
reason. Do not invent facts not present in the conversation.
</system-reminder>`

// foldAsk is the fold ask this agent sends: the registry's, or the one
// shipped when the registry has none.
func (a *Agent) foldAsk(ctx context.Context) string {
	if a.prompts != nil {
		if text, err := a.prompts.Get(ctx, foldPromptKey, nil); err == nil && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return foldInstruction
}

// NeedsFold reports whether the past has crossed the start mark. The summary
// is left out of the comparison: it is the floor the next fold stands on, not
// part of what a fold can shed, and counting it made every fold bring the next
// one closer as the summary grew. The pressure path keeps the full estimate —
// that is the provider-window guard, where the whole prompt is the right
// number.
func (sm *SegmentedMemory) NeedsFold() bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.reliefInFlight {
		return false
	}
	return sm.estimateOptLocked(true) >= sm.startMarkLocked(0)
}

// parseFoldReply extracts the summary from the model's fold reply. Lenient:
// it takes the outermost JSON object in the text and reads its summary field,
// so a model that wraps the JSON in prose still lands.
func parseFoldReply(text string) (string, bool) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return "", false
	}
	var v struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &v); err != nil {
		return "", false
	}
	v.Summary = strings.TrimSpace(v.Summary)
	if v.Summary == "" {
		return "", false
	}
	return v.Summary, true
}

// ApplyFoldReply performs the full collapse with the model's summary. The
// range is the harness's: everything settled. Returns whether the collapse
// committed and the post-collapse estimate.
func (sm *SegmentedMemory) ApplyFoldReply(ctx context.Context, summary string) (bool, int) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return false, 0 // a fold without a summary is amnesia
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.reliefInFlight {
		return false, 0
	}

	kept, excised := sm.foldSplitLocked()
	if len(excised) == 0 {
		return false, 0
	}

	var seqs []int64
	for i := range excised {
		if seq, err := strconv.ParseInt(excised[i].ID, 10, 64); err == nil {
			seqs = append(seqs, seq)
		}
	}

	newlyFolded, newText := sm.noteFoldedSkillsLocked(excised, kept, summary)

	n1 := sm.summary.n + 1
	if sm.sessionStore != nil && sm.sessionID != "" {
		if err := sm.sessionStore.FoldMessages(ctx, sm.sessionID, seqs, n1, newText); err != nil {
			zap.L().Error("inloopFold: persist failed",
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

	estimate := sm.estimateLocked()
	zap.L().Info("inloopFold: full collapse committed",
		zap.String("session_id", sm.sessionID),
		zap.Int("rows_excised", len(excised)),
		zap.Int("rows_kept", len(kept)),
		zap.Int("estimate_tokens", estimate),
		zap.Int("summary_bytes", len(newText)),
		zap.Int("version", n1))
	return true, estimate
}

// foldSplitLocked is the in-loop fold's cut of the context: what a full fold
// keeps and what it excises. Kept: the pending pair — the last assistant row and
// everything after it, untouchable — and the last k turns before it, as text:
// a turn is a user message or an assistant row with words in it, and tool
// traffic is never one. Everything else is excised, the user's older messages
// with it: the summary stands in for all of it, carrying the operator's
// instructions verbatim where their wording carries force.
//
// k counts turns of either role, never user turns alone. Within one user turn
// there are many steps; counted in user turns, a long one-shot turn kept every
// step it had taken, and the floor a fold left rose with every step. Kept
// whatever their age, the user's messages — a pasted file, a log — could never
// be folded at all. Must hold lock.
func (sm *SegmentedMemory) foldSplitLocked() (kept, excised []Message) {
	k := sm.protectedRecentTurns
	if k <= 0 {
		k = defaultProtectedRecentTurns
	}

	// The pending pair: the last assistant row and everything after it are
	// untouchable — the in-flight region.
	pendingFrom := -1
	for i := range sm.contextMessages {
		if sm.contextMessages[i].Role == "assistant" {
			pendingFrom = i
		}
	}
	// Before the first assistant row the pending turn is the user's request
	// itself: it starts at the last user message, and no fold removes it.
	if pendingFrom < 0 {
		for i := len(sm.contextMessages) - 1; i >= 0; i-- {
			if sm.contextMessages[i].Role == "user" {
				pendingFrom = i
				break
			}
		}
	}
	// The last k turns before the pending one, counted back from it: a user
	// message, or an assistant row with words in it. A row that only called
	// tools is tool traffic, and goes with it.
	recent := map[int]bool{}
	for i, n := pendingFrom-1, 0; i >= 0 && n < k; i-- {
		m := &sm.contextMessages[i]
		if m.Role == "user" || (m.Role == "assistant" && strings.TrimSpace(m.Content) != "") {
			recent[i] = true
			n++
		}
	}

	kept = make([]Message, 0, len(sm.contextMessages))
	excised = make([]Message, 0, len(sm.contextMessages))
	for i := range sm.contextMessages {
		m := sm.contextMessages[i]
		switch {
		case pendingFrom >= 0 && i >= pendingFrom:
			kept = append(kept, m) // pending pair and after
		case recent[i] && m.Role == "user":
			kept = append(kept, m)
		case recent[i]:
			// Recent steps: the assistant THREAD survives, its tool traffic
			// does not. Pairs are stripped whole — the calls leave here, the
			// results leave via the excised branch below.
			// The row itself stays unflagged (its text must survive reload);
			// its RESULTS are excised and flagged below, and on reload the
			// compile's pair-atomicity fills the orphaned calls with
			// synthetic failed results — the re-run door.
			mm := m
			mm.ToolCalls = nil
			kept = append(kept, mm)
		default:
			excised = append(excised, m)
		}
	}
	return kept, excised
}

// InFlightResults counts the results of the step in flight: the tool rows
// after the last assistant row.
func (sm *SegmentedMemory) InFlightResults() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	last := -1
	for i := range sm.contextMessages {
		if sm.contextMessages[i].Role == "assistant" {
			last = i
		}
	}
	if last < 0 {
		return 0
	}
	n := 0
	for i := last + 1; i < len(sm.contextMessages); i++ {
		if sm.contextMessages[i].Role == "tool" {
			n++
		}
	}
	return n
}

// ReplaceInFlightResults replaces the results of the step in flight — every
// tool row after the last assistant row — with note, keeping each row's tool
// call id so every call still has its result. It is the answer to a step whose
// results together are too large for the context: the model is told what
// happened and re-plans, instead of the turn ending. The stored rows are
// rewritten too, so a reload sends what the model was sent; a store that
// cannot rewrite leaves its rows as they were, and says so once. Returns how
// many results were replaced.
func (sm *SegmentedMemory) ReplaceInFlightResults(ctx context.Context, note string) int {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	lastAssistant := -1
	for i := range sm.contextMessages {
		if sm.contextMessages[i].Role == "assistant" {
			lastAssistant = i
		}
	}
	if lastAssistant < 0 {
		return 0
	}
	var seqs []int64
	n := 0
	for i := lastAssistant + 1; i < len(sm.contextMessages); i++ {
		m := &sm.contextMessages[i]
		if m.Role != "tool" {
			continue
		}
		m.Content = note
		m.ToolResult = nil
		n++
		if seq, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
			seqs = append(seqs, seq)
		}
	}
	if n == 0 {
		return 0
	}
	if sm.sessionStore != nil && sm.sessionID != "" && len(seqs) > 0 {
		if rw, ok := sm.sessionStore.(ResultRewriter); ok {
			if err := rw.RewriteToolResults(ctx, sm.sessionID, seqs, note); err != nil {
				zap.L().Warn("step results replaced in memory; the store could not rewrite them",
					zap.String("session_id", sm.sessionID), zap.Error(err))
			}
		} else {
			warnOnce("ResultRewriter", "the store cannot rewrite tool rows; a step replaced for being too large reloads as it was stored")
		}
	}
	sm.l1Dirty = true
	sm.updateTokenCount()
	sm.tokenCountDirty = false
	return n
}

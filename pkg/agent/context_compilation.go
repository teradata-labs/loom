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

// ContextCompilation (HLD §5): data structures hold no logic — ALL logic lives
// in the one algorithm here, run at every LLM call. Compile renders the context
// (offload is a pure render condition); releasePressure is the only mechanism
// that mutates context state (write-once flags + summary versions), entered
// only from the provider's context-too-long refusal.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go.uber.org/zap"
)

// summaryState is L2 — the session summary: ONE cumulative text, persisted as
// version rows; the newest version is the summary (HLD §2, §5.4).
type summaryState struct {
	n    int
	text string
}

// syntheticFailedResult is the normative text of HLD §5.2 step 7: a call with
// no matching result row gets a synthetic failed result emitted in its place —
// synthetic, never strip, because the signature is the re-run door.
const syntheticFailedResult = "[no result recorded — the call did not complete. Re-run it if needed.]"

// offloadStubFormat is the normative offload stub of HLD §5.5 — a turn=T result
// strictly over the threshold; the payload is in memory and queryable.
const offloadStubFormat = "[%s result, ~%d tokens, held in memory this turn — query_tool_result(message_id=%d, offset=0, limit=100) to read it; sql=\"SELECT ... FROM results\" for tabular data.%s\n preview: %s]"

// evictedStubFormat is the normative evicted stub of HLD §5.5 — evicted=true or
// legacy oversize; the only door is re-run.
const evictedStubFormat = "[%s result, ~%d tokens, evicted from context — re-run the call above if this data is needed again.%s\n preview: %s]"

// compileLocked is ContextCompilation §5.2 steps 1–7: KERNEL rides the provider
// tools parameter (its bytes are counted via kernelBytes); ROM; the summary's
// newest version as one system message; then L1 in seq order under the five
// render cases, with pair atomicity restored by synthetic failed results.
// Must hold lock.
func (sm *SegmentedMemory) compileLocked() []Message {
	out := make([]Message, 0, len(sm.contextMessages)+2)

	// Step 2: ROM. Its own cache breakpoint — the most stable prefix; survives a
	// fold, which only invalidates from the summary downward.
	if sm.romContent != "" {
		out = append(out, Message{Role: "system", Content: sm.romContent, CacheBreakpoint: true})
	}

	// Step 3: the summary's newest version, one system message. Its own cache
	// breakpoint — stable until the next fold rewrites it.
	if sm.summary.text != "" {
		out = append(out, Message{Role: "system", Content: sm.summary.text, CacheBreakpoint: true})
	}

	// Step 5: T — the session's current turn number.
	t := sm.currentTurnLocked()

	// Tool names by call id, for the stub headers.
	callName := make(map[string]string)
	for i := range sm.contextMessages {
		for _, c := range sm.contextMessages[i].ToolCalls {
			if c.ID != "" {
				callName[c.ID] = c.Name
			}
		}
	}

	// Step 6 render cases + step 7 pair atomicity. Track the message cache
	// breakpoint: the last message before any CURRENT-TURN ephemeral content.
	// Two kinds re-render differently once the turn settles, so caching either
	// would mismatch the next call's prefix: (a) offload stubs, which re-render
	// to evicted stubs next turn; (b) query_tool_result call/result pairs, which
	// never persist (§4.3) and are pruned once the turn settles. Everything
	// before the first of these is turn-stable and safe to cache.
	msgs := sm.contextMessages
	lastStable, frozen := len(out)-1, false
	emit := func(m *Message) {
		out = append(out, sm.renderLocked(m, t, callName))
		if m.Turn == t && ((m.Role == "tool" && !m.Evicted && len(m.Content) > sm.threshold) ||
			(m.Role == "assistant" && hasQueryToolResultCall(m))) {
			frozen = true
		}
		if !frozen {
			lastStable = len(out) - 1
		}
	}
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		emit(&m)

		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			// Consume this call batch's contiguous tool results, then emit a
			// synthetic failed result for every call left without one — never
			// strip the call: the signature is the re-run door (§5.2 step 7).
			missing := make(map[string]bool, len(m.ToolCalls))
			for _, c := range m.ToolCalls {
				if c.ID != "" {
					missing[c.ID] = true
				}
			}
			j := i + 1
			for j < len(msgs) && msgs[j].Role == "tool" {
				r := msgs[j]
				delete(missing, r.ToolUseID)
				emit(&r)
				j++
			}
			for _, c := range m.ToolCalls {
				if c.ID != "" && missing[c.ID] {
					out = append(out, Message{
						Role:      "tool",
						ToolUseID: c.ID,
						Content:   syntheticFailedResult,
					})
					if !frozen {
						lastStable = len(out) - 1
					}
				}
			}
			i = j - 1
		}
	}

	// The message breakpoint (ROM/summary already carry their own).
	if lastStable >= 0 && !out[lastStable].CacheBreakpoint {
		out[lastStable].CacheBreakpoint = true
	}

	return out
}

// hasQueryToolResultCall reports whether an assistant message issues a
// query_tool_result call. Its call/result pair is ephemeral (§4.3) and pruned
// when the turn settles, so it must sit behind the cache breakpoint.
func hasQueryToolResultCall(m *Message) bool {
	for _, c := range m.ToolCalls {
		if c.Name == "query_tool_result" {
			return true
		}
	}
	return false
}

// renderLocked applies §5.2 step 6's five render cases — first matching case
// wins. Conversation is never stubbed; offload is a pure render condition of
// the current turn; evicted and legacy-oversize rows render the evicted stub.
func (sm *SegmentedMemory) renderLocked(m *Message, t int64, callName map[string]string) Message {
	if m.Role != "tool" {
		return *m
	}
	switch {
	case m.Evicted:
		r := *m
		r.Content = sm.evictedStub(m, callName)
		return r
	case len(m.Content) > sm.threshold && m.Turn == t:
		r := *m
		r.Content = sm.offloadStub(m, callName)
		return r
	case len(m.Content) > sm.threshold && m.Turn < t:
		r := *m
		r.Content = sm.evictedStub(m, callName)
		return r
	default:
		return *m
	}
}

// offloadStub renders the §5.5 offload stub for a current-turn result strictly
// over the threshold. A row with no durable ID (storeless session) has no
// query_tool_result door — printing message_id=0 would advertise a door that
// errors — so it renders the evicted stub, whose only door is re-run.
func (sm *SegmentedMemory) offloadStub(m *Message, callName map[string]string) string {
	id, _ := strconv.ParseInt(m.ID, 10, 64)
	if id <= 0 {
		return sm.evictedStub(m, callName)
	}
	return fmt.Sprintf(offloadStubFormat,
		stubToolName(m, callName),
		tokenFigure(len(m.Content)),
		id,
		harvestTails(m.Content),
		previewOf(m.Content))
}

// evictedStub renders the §5.5 evicted stub.
func (sm *SegmentedMemory) evictedStub(m *Message, callName map[string]string) string {
	return fmt.Sprintf(evictedStubFormat,
		stubToolName(m, callName),
		tokenFigure(len(m.Content)),
		harvestTails(m.Content),
		previewOf(m.Content))
}

// stubToolName resolves the producing call's tool name via the paired
// assistant row's signature.
func stubToolName(m *Message, callName map[string]string) string {
	if name := callName[m.ToolUseID]; name != "" {
		return name
	}
	return "tool"
}

// harvestTails extracts the row's trailing [harvested: …] / [harvest failed …]
// line(s), prefixed by one space — the tails sit at the end of stored content,
// outside the preview, so the stub must carry them itself (HLD §5.5, §4.4).
func harvestTails(content string) string {
	var tails []string
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "[harvested: ") || strings.HasPrefix(line, "[harvest failed for ") {
			tails = append([]string{line}, tails...)
			continue
		}
		break
	}
	if len(tails) == 0 {
		return ""
	}
	return " " + strings.Join(tails, " ")
}

// previewOf renders the §5.5 preview: the first 160 bytes of the content with
// every whitespace run collapsed to a single space, cut backward to a rune
// boundary (whole runes are appended, so no rune is ever split).
func previewOf(content string) string {
	var b strings.Builder
	lastSpace := false
	for _, r := range content {
		var s string
		if unicode.IsSpace(r) {
			if lastSpace {
				continue
			}
			lastSpace = true
			s = " "
		} else {
			lastSpace = false
			s = string(r)
		}
		if b.Len()+len(s) > 160 {
			break
		}
		b.WriteString(s)
	}
	return b.String()
}

// --- estimate & target (HLD §5.1) -------------------------------------------

// msgFramingTokens is the per-message role/serialization overhead the provider
// adds around each message — a small fixed add.
const msgFramingTokens = 4

// cheapBytesPerToken is the divisor for estimateLocked's cheap tier. It is
// deliberately smaller than the real bytes-per-token of this system's content
// (~3.3), so the byte bound OVER-counts tokens: the cheap tier may tokenize a
// little early, but it never reports "under" when we are actually near the limit.
const cheapBytesPerToken = 2.7

// estimateLocked returns the token count of the compiled context (KERNEL + ROM +
// summary + L1 view). Two tiers, so the tokenizer runs only near the limit:
//   - cheap: a byte bound, no tokenization — if it is under the start mark we are
//     safely under and it is returned as is;
//   - accurate: near the limit only, the real tiktoken count (cached per rendered
//     message).
//
// Must hold lock.
func (sm *SegmentedMemory) estimateLocked() int {
	compiled := sm.compileLocked()

	// Cheap tier — byte bound, no tokenization.
	bytes := sm.kernelBytes
	for i := range compiled {
		bytes += len(compiled[i].Content)
		if len(compiled[i].ToolCalls) > 0 {
			if b, err := json.Marshal(compiled[i].ToolCalls); err == nil {
				bytes += len(b)
			}
		}
	}
	cheap := int(float64(bytes) / cheapBytesPerToken)
	if limit := sm.startMarkLocked(0); limit <= 0 || cheap < limit {
		return cheap
	}

	// Accurate tier — near the limit, tokenize (cached per rendered message).
	tc := sm.tokenCounter
	if tc == nil {
		tc = GetTokenCounter()
	}
	tokens := tokenFigure(sm.kernelBytes)
	for i := range compiled {
		tokens += sm.msgTokensLocked(tc, &compiled[i])
	}
	return tokens
}

// msgTokensLocked returns one compiled message's token count, memoising the
// (large) content tokenization by its rendered string. Tool-call bytes are small
// and counted each time. Must hold lock.
func (sm *SegmentedMemory) msgTokensLocked(tc *TokenCounter, m *Message) int {
	n, ok := sm.msgTokenCache[m.Content]
	if !ok {
		n = tc.CountTokens(m.Content)
		if sm.msgTokenCache == nil {
			sm.msgTokenCache = make(map[string]int)
		}
		if len(sm.msgTokenCache) > 8192 { // bound: drop wholesale rather than grow unbounded
			sm.msgTokenCache = make(map[string]int)
		}
		sm.msgTokenCache[m.Content] = n
	}
	if len(m.ToolCalls) > 0 {
		if b, err := json.Marshal(m.ToolCalls); err == nil {
			n += tc.CountTokens(string(b))
		}
	}
	return n + msgFramingTokens
}

// Pressure water marks, both a percentage of usable context — the window minus
// the output reservation, the space the prompt may actually occupy on the
// wire — one base so start and release are directly comparable (HLD §5.1):
//   - START (HWM): begin relief when the estimate reaches this. Comes from the
//     profile's CriticalThresholdPercent (default 90).
//   - RELEASE (LWM): shed down to this. Comes from the profile's
//     WarningThresholdPercent (default 60). The HWM−LWM gap is the hysteresis
//     band — 30 points at the defaults — so relief fires about half as often
//     as a 15-point band would, each pass shedding more. The 10% below HWM is
//     headroom for the estimate's error.
//
// The defaults below stand in when a profile carries no valid pair.
const (
	defaultPressureStartPercent   = 90 // HWM: trigger relief
	defaultPressureReleasePercent = 60 // LWM: shed target
)

// pressureRecoveryPenalty lowers both water marks (percentage points) for the
// one recovery pass after a provider refusal. A refusal means loom's estimate
// under-counted — the normal marks did not shed enough — so retry with the bar
// 20 points lower (start 70% / release 40% at the defaults) to force the
// deeper shed the estimate would not otherwise trigger.
const pressureRecoveryPenalty = 20

// usableLocked is the prompt's real ceiling: the window minus the output
// reservation. The provider refuses any request whose prompt exceeds it, so
// the water marks are percentages of this, never of the full window — a mark
// above usable would sit beyond the refusal line and never fire. Must hold
// lock.
func (sm *SegmentedMemory) usableLocked() int {
	return sm.tokenBudget.MaxTokens - sm.tokenBudget.ReservedTokens
}

// applyPenalty lowers a mark percentage for the recovery pass, floored at 1.
// Without the floor a configured profile with a release mark under the penalty
// (validation allows warning=10) yields a NEGATIVE target on the recovery pass:
// the pass's exit test is `estimate <= target`, which no estimate can satisfy,
// so it runs every rung of the ladder — folding the whole of L1 into the
// summary after a single provider refusal.
func applyPenalty(pct, penalty int) int {
	if p := pct - penalty; p >= 1 {
		return p
	}
	return 1
}

// marksLocked resolves the water marks from the compression profile:
// HWM ← CriticalThresholdPercent, LWM ← WarningThresholdPercent. A missing or
// inverted pair falls back to the 90/60 defaults, so a hand-built profile can
// never zero the marks. Must hold lock.
func (sm *SegmentedMemory) marksLocked() (start, release int) {
	start = sm.compressionProfile.CriticalThresholdPercent
	release = sm.compressionProfile.WarningThresholdPercent
	if release <= 0 || release >= start || start > 100 {
		return defaultPressureStartPercent, defaultPressureReleasePercent
	}
	return start, release
}

// startMarkLocked is the HWM: begin relief when the estimate reaches it. penalty
// (percentage points) lowers it for the recovery pass. Must hold lock.
func (sm *SegmentedMemory) startMarkLocked(penalty int) int {
	start, _ := sm.marksLocked()
	return applyPenalty(start, penalty) * sm.usableLocked() / 100
}

// releaseMarkLocked is the LWM: shed down to it. penalty lowers it in step with
// startMarkLocked so the recovery pass sheds deeper. Must hold lock.
func (sm *SegmentedMemory) releaseMarkLocked(penalty int) int {
	_, release := sm.marksLocked()
	return applyPenalty(release, penalty) * sm.usableLocked() / 100
}

// --- releasePressure (HLD §5.2) ----------------------------------------------

// ReleasePressure is the relief pass, called at compile before every send. It
// SELF-GATES on loom's own accounting: if the estimate is under the start mark
// (HWM, §5.1) there is no pressure and it is a no-op. Otherwise it sheds to the
// release mark (LWM) via the escalating evict-then-fold ladder below —
// recompiling and re-estimating after each op, returning the instant the
// estimate reaches the mark. Returns whether it shed and the post-pass
// {estimate, target}.
//
// penalty (percentage points) lowers both marks. It is 0 for the normal compile
// pass; on the one recovery pass after a provider refusal it is
// pressureRecoveryPenalty, so a shed the normal gate would skip is forced — the
// only place the provider's refusal touches relief.
func (sm *SegmentedMemory) ReleasePressure(ctx context.Context, penalty int) (shed bool, estimate, target int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// One pass at a time: fold releases the lock across its compressor call,
	// so without this guard a second caller could enter mid-pass and run an
	// interleaved ladder. The conversation loop is sequential today; the flag
	// makes that a guarantee instead of a call-graph accident.
	if sm.reliefInFlight {
		return false, sm.estimateLocked(), sm.releaseMarkLocked(penalty)
	}

	if sm.estimateLocked() < sm.startMarkLocked(penalty) {
		return false, 0, 0 // under the start mark — no pressure to release
	}

	sm.reliefInFlight = true
	defer func() { sm.reliefInFlight = false }()

	t := sm.currentTurnLocked()
	k := int64(sm.protectedRecentTurns)
	if k <= 0 {
		k = defaultProtectedRecentTurns
	}
	target = sm.releaseMarkLocked(penalty)
	estimate = -1

	// Escalation is a halving ladder (§5.2): keep the newest K turns, then K/2,
	// K/4 … down to 1 — shed the minimum needed and preserve as much recent
	// context as possible, instead of an all-or-nothing K-then-1 cliff. K is
	// config (protectedRecentTurns, default 16); halving is the mechanism. All
	// eviction (re-runnable) is tried before any fold (lossy); each op recompiles
	// and the pass returns the instant the estimate reaches target.
	type reliefOp struct {
		name     string
		boundary int64
		run      func(context.Context, int64) bool
	}
	var rungs []int64
	seen := map[int64]bool{}
	for step := k; step >= 1; step /= 2 {
		if b := t - step; !seen[b] {
			seen[b] = true
			rungs = append(rungs, b)
		}
	}
	ops := make([]reliefOp, 0, 2*len(rungs))
	for _, b := range rungs {
		ops = append(ops, reliefOp{"evict", b, sm.evictLocked})
	}
	for _, b := range rungs {
		ops = append(ops, reliefOp{"fold", b, sm.foldLocked})
	}
	for _, op := range ops {
		if !op.run(ctx, op.boundary) {
			// An operation that changed nothing (its rows were already flagged
			// by an earlier pressure event) passes to the next.
			continue
		}
		estimate = sm.estimateLocked()
		zap.L().Info("releasePressure: operation complete",
			zap.String("session_id", sm.sessionID),
			zap.String("operation", op.name),
			zap.Int64("boundary_turn", op.boundary),
			zap.Int("estimate_tokens", estimate),
			zap.Int("target_tokens", target))
		if estimate <= target {
			return true, estimate, target
		}
	}

	if estimate < 0 {
		// The pass ran but shed nothing (every region was already flagged) —
		// compute once for the caller and say so honestly: a false return
		// spares the caller a pointless recompile and resend.
		return false, sm.estimateLocked(), target
	}
	return true, estimate, target
}

// evictLocked marks evicted=true on every tool row with turn ≤ b whose stored
// content ≥ 2× its stub (the eviction floor, §5.1) — one transaction, in-memory
// copies updated in the same step. Returns whether anything changed. Must hold
// lock.
func (sm *SegmentedMemory) evictLocked(ctx context.Context, b int64) bool {
	callName := make(map[string]string)
	for i := range sm.contextMessages {
		for _, c := range sm.contextMessages[i].ToolCalls {
			if c.ID != "" {
				callName[c.ID] = c.Name
			}
		}
	}

	var marked []int
	var seqs []int64
	for i := range sm.contextMessages {
		m := &sm.contextMessages[i]
		if m.Role != "tool" || m.Evicted || m.Turn > b {
			continue
		}
		stub := sm.evictedStub(m, callName)
		if len(m.Content) < 2*len(stub) {
			// Below the floor, stubbing saves nothing or goes negative.
			continue
		}
		marked = append(marked, i)
		if seq, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
			seqs = append(seqs, seq)
		}
	}
	if len(marked) == 0 {
		return false
	}

	if sm.sessionStore != nil && sm.sessionID != "" && len(seqs) > 0 {
		if err := sm.sessionStore.MarkEvicted(ctx, sm.sessionID, seqs); err != nil {
			zap.L().Error("releasePressure: evict flag persist failed",
				zap.String("session_id", sm.sessionID),
				zap.Int64("boundary_turn", b),
				zap.Error(err))
			return false
		}
	}

	// Flag mirror: update the in-memory L1 copies in the same step (§5.2).
	for _, i := range marked {
		sm.contextMessages[i].Evicted = true
	}
	sm.l1Dirty = true
	sm.updateTokenCount()
	sm.tokenCountDirty = false

	zap.L().Info("releasePressure: evict",
		zap.String("session_id", sm.sessionID),
		zap.Int64("boundary_turn", b),
		zap.Int("rows_marked", len(marked)),
		zap.Int64s("seqs", seqs))
	return true
}

// foldLocked folds every folded=false row with turn ≤ b into summary version
// n+1 (HLD §5.4): pairs fold whole; compressor input = current summary + the
// region AS RENDERED (stubs as stubs); the version insert and the region's
// folded flags commit in one transaction; the new version and flags are
// mirrored into memory in the same step; skills whose manage_skills load pair
// lies inside the region are deactivated. Returns whether anything changed.
//
// Lock contract: held at entry and exit, RELEASED across the compressor's LLM
// call (prepare → compress → validate+commit). The snapshot version check
// after re-locking keeps the commit correct if a concurrent mutator ever
// appears.
func (sm *SegmentedMemory) foldLocked(ctx context.Context, b int64) bool {
	// Region: the seq-ordered prefix with turn ≤ b, pair-adjusted so a call
	// and its result are never split across the boundary.
	count := 0
	for count < len(sm.contextMessages) && sm.contextMessages[count].Turn <= b {
		count++
	}
	count = sm.adjustCompressionBoundary(count)
	if count <= 0 {
		return false
	}
	region := sm.contextMessages[:count]

	// The covered span, from the region's persisted seqs.
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

	// Compressor input: the current summary text + the region as rendered —
	// stubs as stubs, so it summarizes conversation, never payloads — with
	// msg: addresses visible for citation (§5.4.2/4). Built under the lock,
	// as value copies: nothing the unlocked compress phase touches aliases
	// shared state.
	t := sm.currentTurnLocked()
	callName := make(map[string]string)
	for i := range sm.contextMessages {
		for _, c := range sm.contextMessages[i].ToolCalls {
			if c.ID != "" {
				callName[c.ID] = c.Name
			}
		}
	}
	input := make([]Message, 0, len(region)+1)
	if sm.summary.text != "" {
		input = append(input, Message{Role: "system", Content: "Current summary:\n" + sm.summary.text})
	}
	for i := range region {
		r := sm.renderLocked(&region[i], t, callName)
		if r.ID != "" {
			r.Content = "msg:" + r.ID + " " + r.Content
		}
		input = append(input, r)
	}

	// The prepare snapshot: the summary version this fold builds on and the
	// region's identity, checked after the unlocked compress phase.
	n0 := sm.summary.n
	lastRegionID := sm.contextMessages[count-1].ID

	// Compressor = the same model as the agent, or better; its output is
	// version n+1 whole (superseded, not merged). Fold is rare and its output
	// is the session's whole memory, so the call gets 120s, not the old 5s.
	// The LOCK IS RELEASED across the call: the compressor is a pure function
	// of the snapshot built above, and holding the write lock through a
	// network call would serialize every reader behind it for the duration.
	newText := ""
	fallback := false
	if sm.compressor != nil && sm.compressor.IsEnabled() {
		sm.mu.Unlock()
		compressCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		// The re-lock is DEFERRED, not sequential. ReleasePressure holds a
		// `defer sm.mu.Unlock()` for the whole pass, so if the compressor
		// panicked while the lock was released, that deferred unlock would run
		// against an unlocked mutex — a fatal runtime throw that recover()
		// cannot catch, turning a recoverable provider fault into a process
		// crash. Re-taking the lock on the way out keeps the pass's invariant
		// true however the call exits.
		compressed, err := func() (string, error) {
			defer sm.mu.Lock()
			defer cancel()
			return sm.compressor.CompressMessages(compressCtx, input)
		}()

		// Validate the snapshot before committing on top of it. Every L1
		// appender runs on the conversation-loop thread, which is blocked
		// inside this pass — so a mismatch is not an expected path but the
		// guard that keeps this true by enforcement, not by call graph: if a
		// concurrent mutator ever appears, the fold degrades to one discarded
		// compressor call instead of a lost update.
		if sm.summary.n != n0 || count > len(sm.contextMessages) ||
			sm.contextMessages[count-1].ID != lastRegionID {
			zap.L().Warn("releasePressure: fold snapshot invalidated during compress — discarding",
				zap.String("session_id", sm.sessionID),
				zap.Int64("boundary_turn", b),
				zap.Int("summary_version_at_prepare", n0),
				zap.Int("summary_version_now", sm.summary.n))
			return false
		}

		if err == nil && compressed != "" {
			newText = strings.TrimSpace(compressed)
			// The first line states the covered span (§5.4.4) — enforced here
			// when the compressor omitted it OR echoed a stale line: a span
			// whose upper bound is below hiSeq under-claims this fold's
			// coverage, and coverage is never silently lost.
			if !coversThrough(newText, hiSeq) {
				newText = fmt.Sprintf("covers msg:%d-%d\n", loSeq, hiSeq) + newText
			}
		}
	}
	if newText == "" {
		// Compressor failure (§5.4.5): version n+1 = the previous text
		// unchanged plus one line — coverage is never silently claimed and
		// never lost; the next fold's compressor pass absorbs it.
		fallback = true
		line := fmt.Sprintf("also covers msg:%d-%d (unsummarized): %s", loSeq, hiSeq, firstUserLine(region))
		if sm.summary.text == "" {
			newText = line
		} else {
			newText = sm.summary.text + "\n" + line
		}
	}

	// Skills whose load pair folds are deactivated (§4.5). Accumulate them on the
	// memory and re-pin the FULL set as a note at the END of the summary, so the
	// model sees which capability went out with the folded conversation and can
	// reload it if still in use. Tracking it as state (not just this fold's text)
	// keeps the note alive when a later fold's compressor paraphrases the summary.
	newlyFolded := foldedSkillLoads(region)
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
		newText = strings.TrimRight(newText, "\n") +
			fmt.Sprintf("\n\n[Folded active skill(s): %s — reload with manage_skills if still in use.]",
				strings.Join(names, ", "))
	}

	n1 := sm.summary.n + 1

	// One transaction: the version insert and the region's folded flags
	// (§5.4.6). A failed insert fails the step — never a fold that evaporates
	// on reload.
	if sm.sessionStore != nil && sm.sessionID != "" {
		if err := sm.sessionStore.FoldMessages(ctx, sm.sessionID, seqs, n1, newText); err != nil {
			zap.L().Error("releasePressure: fold persist failed",
				zap.String("session_id", sm.sessionID),
				zap.Int64("boundary_turn", b),
				zap.Int("version", n1),
				zap.Error(err))
			return false
		}
	}

	// Deactivate every skill whose manage_skills load pair lies inside the
	// region (HLD §4.5) — its tools leave KERNEL at the next compile;
	// re-loading the skill is the door back.
	if sm.skillDeactivation != nil {
		for _, name := range newlyFolded {
			sm.skillDeactivation(sm.sessionID, name)
		}
	}

	// Mirror into memory in the same step: install the new version and drop
	// the folded region from L1 — the recompile that follows must see
	// post-stage state without a re-read (folded rows are filtered at the
	// database read on reload).
	sm.summary = summaryState{n: n1, text: newText}
	remaining := make([]Message, len(sm.contextMessages)-count)
	copy(remaining, sm.contextMessages[count:])
	sm.contextMessages = remaining
	sm.l1Dirty = true
	sm.updateTokenCount()
	sm.tokenCountDirty = false

	zap.L().Info("releasePressure: fold",
		zap.String("session_id", sm.sessionID),
		zap.Int64("boundary_turn", b),
		zap.Int("rows_folded", count),
		zap.Int64("seq_lo", loSeq),
		zap.Int64("seq_hi", hiSeq),
		zap.Int("version", n1),
		zap.Int("output_bytes", len(newText)),
		zap.Bool("fallback", fallback))
	return true
}

// firstUserLine returns the first line of the region's first user message —
// the §5.4.5 fallback citation.
func firstUserLine(region []Message) string {
	for i := range region {
		if region[i].Role == "user" && region[i].Content != "" {
			line, _, _ := strings.Cut(region[i].Content, "\n")
			return line
		}
	}
	return ""
}

// foldedSkillLoads returns the names of skills whose manage_skills load pair —
// the load call paired with a "Skill loaded: " confirmation — lies inside the
// region.
// coversThrough reports whether text opens with a "covers msg:A-B" line whose
// upper bound reaches hiSeq — i.e. the span line genuinely claims this fold's
// coverage, not a stale echo of a previous version's line.
func coversThrough(text string, hiSeq int64) bool {
	first := text
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	rest, ok := strings.CutPrefix(first, "covers msg:")
	if !ok {
		return false
	}
	parts := strings.SplitN(strings.TrimSpace(rest), "-", 2)
	if len(parts) != 2 {
		return false
	}
	fields := strings.Fields(parts[1])
	if len(fields) == 0 {
		return false
	}
	hi, err := strconv.ParseInt(fields[0], 10, 64)
	return err == nil && hi >= hiSeq
}

func foldedSkillLoads(region []Message) []string {
	loadCalls := make(map[string]string) // tool_use_id → skill name
	for i := range region {
		for _, c := range region[i].ToolCalls {
			if c.Name != "manage_skills" || c.ID == "" {
				continue
			}
			if action, _ := c.Input["action"].(string); action != "load" {
				continue
			}
			if name, _ := c.Input["name"].(string); name != "" {
				loadCalls[c.ID] = name
			}
		}
	}
	var names []string
	seen := make(map[string]bool)
	for i := range region {
		m := &region[i]
		if m.Role != "tool" || m.ToolUseID == "" {
			continue
		}
		name := loadCalls[m.ToolUseID]
		if name == "" || seen[name] {
			continue
		}
		if strings.HasPrefix(m.Content, "Skill loaded: ") {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// defaultProtectedRecentTurns is K (HLD §5.1): the top rung of the halving
// escalation ladder — the newest user turns relief tries hardest to keep. The
// ladder folds/evicts at K, K/2, K/4 … 1 (§5.2).
const defaultProtectedRecentTurns = 16

// SetProtectedRecentTurns configures K (config ProtectedRecentTurns, §9).
func (sm *SegmentedMemory) SetProtectedRecentTurns(k int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if k > 0 {
		sm.protectedRecentTurns = k
	}
}

// SetAdvertisedToolsBytes records the serialized bytes of the advertised tool
// schemas — the provider tools parameter as built (HLD §2, blueprint A6). The
// schemas' bytes are part of the compiled artifact and therefore of the
// estimate; nothing acts on the derived token count.
func (sm *SegmentedMemory) SetAdvertisedToolsBytes(bytes int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if bytes < 0 {
		bytes = 0
	}
	sm.kernelBytes = bytes
	sm.recountKernel()
	sm.kernelDirty = false
	sm.updateTokenCount()
	sm.tokenCountDirty = false
}

// SetSkillDeactivationHook wires the skills orchestrator's deactivation path,
// used when fold flags a region containing a manage_skills load pair.
func (sm *SegmentedMemory) SetSkillDeactivationHook(fn func(sessionID, skillName string)) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.skillDeactivation = fn
}

// setSummary installs a restored summary version (reload, HLD §8).
func (sm *SegmentedMemory) setSummary(n int, text string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.summary = summaryState{n: n, text: text}
	sm.cachedL2Tokens = sm.tokenCounter.CountTokens(text)
	sm.updateTokenCount()
	sm.tokenCountDirty = false
}

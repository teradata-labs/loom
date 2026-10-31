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

// summaryRowPrefix opens the compiled summary row; the trigger's estimate
// recognises the row by it.
const summaryRowPrefix = "<system-reminder>\nSession summary — the work so far:\n\n"

// syntheticFailedResult is the normative text of HLD §5.2 step 7: a call with
// no matching result row gets a synthetic failed result emitted in its place —
// synthetic, never strip, because the signature is the re-run door.
const syntheticFailedResult = "[no result recorded — the call did not complete. Re-run it if needed.]"

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

	// Step 3: the summary's newest version — CONVERSATION content, not system.
	// The summary is the folded past, the model's own testimony; it renders as
	// a user-role reminder at the head of the conversation region (loom's
	// established harness-message pattern). Deliberately NO cache breakpoint
	// and no system role: a summary block in the system region consumed a
	// marker slot, and the client cap (4 minus the tool list) then dropped
	// till-NOW — the only marker that caches the session tail in a one-turn
	// (one-shot CLI) session, so every post-fold call re-billed its whole
	// tail as raw input. As a conversation message it is covered by the
	// lastStable/till-NOW markers like any other row, and a fold invalidates
	// from HERE down — ROM and the tool schemas stay cached across folds.
	if sm.summary.text != "" {
		out = append(out, Message{Role: "user",
			Content: summaryRowPrefix + sm.summary.text +
				"\n\nExact values quoted in this summary (expected outputs, literals, paths, interfaces) are unverified — the summary may restate them imprecisely. Before relying on one, read it from its source.\n</system-reminder>"})
	}

	// Step 5: T — the session's current turn number.
	t := sm.currentTurnLocked()

	// Render cases + pair atomicity. Track the message cache breakpoint: the
	// last message before any CURRENT-TURN content that can re-render at
	// settle. An offload-exempt tool's current-turn result renders whole and
	// re-renders truncated once the turn settles, so caching it would
	// mismatch the next call's prefix. Non-exempt over-threshold rows render
	// their truncated §4.1 bytes from the first compile and never change;
	// they sit behind the same freeze conservatively.
	msgs := sm.contextMessages
	lastStable, frozen := len(out)-1, false
	emit := func(m *Message) {
		out = append(out, sm.renderLocked(m, t))
		// Advance only onto messages that can carry a wire cache marker: a
		// text-empty message (an assistant tool-call shell) gets no
		// cache_control from the provider clients, so parking the breakpoint
		// there silently uncaches the entire prefix for the rest of the turn.
		if !frozen && out[len(out)-1].Content != "" {
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

	// The till-NOW breakpoint — the fourth marker, on the last markable
	// message. Within a turn the region past lastStable is byte-stable:
	// truncated renders are deterministic and arrivals append rather than
	// rewrite, so this marker is read back by every following call of the
	// same turn. At turn settle the region re-renders and the request falls
	// back to the lastStable marker — cross-turn behavior unchanged. Clients
	// that spend a marker on the tool list cap message markers at three,
	// which skips exactly this one.
	for i := len(out) - 1; i >= 0 && i != lastStable; i-- {
		if out[i].Content != "" {
			out[i].CacheBreakpoint = true
			break
		}
	}

	return out
}

// renderLocked applies the render cases — first matching case wins.
// Conversation is never stubbed, and compile applies no size bound of its own:
// a tool row renders with the bytes the row holds. Those bytes were bounded at
// arrival (appendMessage, §4.1): an over-threshold result entered as core +
// tail, and the stored row is the same bytes, so a session reloaded from the
// store renders exactly what the producing session did.
//
// What compile guarantees is byte-stability: no row is ever rewritten in
// place, so a compiled prefix stays valid and the fold is the only thing that
// invalidates it.
func (sm *SegmentedMemory) renderLocked(m *Message, t int64) Message {
	if m.Role == "assistant" && len(m.ThinkingBlocks) > 0 && m.Turn < t {
		// Settled turns render without thinking blocks: the provider ignores
		// them there, and the strip is a deterministic render (same one-time
		// prefix rewrite as the stub re-render at the settle boundary).
		r := *m
		r.ThinkingBlocks = nil
		return r
	}
	// User turns carry their arrival time into the compiled view ONLY, so the
	// model keeps per-turn temporal grounding ("today", "this month") while the
	// stored, client-visible Content stays verbatim. Timestamp is write-once and
	// persisted as Unix seconds, so every compile — including after restart —
	// renders identical bytes: byte-stability and all cache breakpoints hold.
	// Rendered in UTC so a server timezone change across restart cannot alter
	// the bytes. Legacy rows without a Timestamp render unchanged.
	//
	// Multimodal turns need the stamp inside ContentBlocks too: providers build
	// the request exclusively from ContentBlocks when present (agent.go), so a
	// stamp on Content alone would never reach the model for an image turn. Stamp
	// the leading text block (deep-copying so the stored row is untouched); if a
	// turn somehow carries no text block, fall back to a stamp-only Content so
	// grounding is never silently dropped.
	if (m.Role == "user" || IsSyntheticWireUserRole(m.Role)) && !m.Timestamp.IsZero() {
		stamp := "[" + m.Timestamp.UTC().Format("Mon 2006-01-02 15:04 MST") + "] "
		r := *m
		r.Content = stamp + m.Content
		if len(m.ContentBlocks) > 0 {
			r.ContentBlocks = stampLeadingTextBlock(m.ContentBlocks, stamp)
		}
		return r
	}
	// Tool rows render as stored. Eviction (stub-in-place) was removed — see
	// releasePressure — so nothing rewrites a row at render time and history
	// behind the cache breakpoint is stable for the life of the session.
	return *m
}

// stampLeadingTextBlock returns a copy of blocks with stamp prepended to the
// first text block, so the arrival stamp reaches multimodal providers (which
// build the request from ContentBlocks). The input slice and its blocks are
// never mutated — only the compiled view is stamped. If no text block exists,
// blocks are returned unchanged; renderLocked's stamped Content is the fallback.
func stampLeadingTextBlock(blocks []ContentBlock, stamp string) []ContentBlock {
	for i := range blocks {
		if blocks[i].Type == "text" {
			out := make([]ContentBlock, len(blocks))
			copy(out, blocks)
			out[i].Text = stamp + out[i].Text
			return out
		}
	}
	return blocks
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
	return collapseTo(content, 160)
}

// collapseTo is previewOf's engine at an arbitrary byte cap: whitespace runs
// collapse to one space, whole runes only.
func collapseTo(content string, max int) string {
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
		if b.Len()+len(s) > max {
			break
		}
		b.WriteString(s)
	}
	return b.String()
}

// jsonSketch renders a one-level shape sketch of a parsed JSON value — sorted
// keys with value kinds, array lengths, nested sizes; never values. Sorted
// iteration keeps the sketch byte-stable (cache safety).
func jsonSketch(v interface{}) string {
	switch t := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for i, k := range keys {
			if i == 8 {
				parts = append(parts, "…")
				break
			}
			parts = append(parts, k+": "+jsonKind(t[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case []interface{}:
		if len(t) == 0 {
			return "[0 items]"
		}
		return fmt.Sprintf("[%d items of %s]", len(t), jsonKind(t[0]))
	default:
		return jsonKind(v)
	}
}

// jsonKind names a JSON value's kind for the sketch.
func jsonKind(v interface{}) string {
	switch t := v.(type) {
	case string:
		return "str"
	case float64:
		return "num"
	case bool:
		return "bool"
	case nil:
		return "null"
	case []interface{}:
		return fmt.Sprintf("[%d items]", len(t))
	case map[string]interface{}:
		return fmt.Sprintf("{%d keys}", len(t))
	default:
		return "value"
	}
}

// --- estimate & target (HLD §5.1) -------------------------------------------

// msgFramingTokens is the per-message role/serialization overhead the provider
// adds around each message — a small fixed add.
const msgFramingTokens = 4

// cheapBytesPerToken is the divisor for estimateLocked's cheap tier. It is
// deliberately smaller than the real bytes-per-token of this system's content
// (~3.3), so the byte bound OVER-counts tokens: the cheap tier may tokenize a
// little early, but it never reports "under" when we are actually near the limit.
const cheapBytesPerToken = 2.0

// estimateLocked returns the token count of the compiled context (KERNEL + ROM +
// summary + L1 view). Two tiers, so the tokenizer runs only near the limit:
//   - cheap: a byte bound, no tokenization — if it is under the start mark we are
//     safely under and it is returned as is;
//   - accurate: near the limit only, the real tiktoken count (cached per rendered
//     message).
//
// Must hold lock.
func (sm *SegmentedMemory) estimateLocked() int { return sm.estimateOptLocked(false) }

// estimateOptLocked is estimateLocked with one option: skipSummary leaves the
// summary row out of the count. The fold trigger uses it — the summary is the
// floor the next fold stands on, not something a fold can shed. The pressure
// path never sets it: against the provider window the whole prompt counts.
// Both tiers are scaled by the provider calibration factor, so the cheap
// tier's early exit compares like with like. Must hold lock.
func (sm *SegmentedMemory) estimateOptLocked(skipSummary bool) int {
	compiled := sm.compileLocked()
	f := sm.calibrationLocked()
	isSummary := func(i int) bool {
		return skipSummary && compiled[i].Role == "user" && strings.HasPrefix(compiled[i].Content, summaryRowPrefix)
	}

	// The in-flight batch: tool results after the last assistant row. The
	// model has not consumed them and no fold may excise them, so no trigger
	// may count them — an estimate inflated by unshedable mass fires folds
	// that cannot commit (the collapse finds nothing removable), and the
	// pressure failsafe then churns the prefix on every call. The batch is
	// consumed by the model's next response, ages into excisable past, and
	// counts from then on. The provider refusal backstop still covers real
	// overflow.
	lastAssistant := -1
	for i := range compiled {
		if compiled[i].Role == "assistant" {
			lastAssistant = i
		}
	}
	inFlight := func(i int) bool {
		return compiled[i].Role == "tool" && i > lastAssistant
	}

	// Cheap tier — byte bound, no tokenization.
	bytes := sm.kernelBytes
	for i := range compiled {
		if inFlight(i) || isSummary(i) {
			continue
		}
		bytes += len(compiled[i].Content)
		if len(compiled[i].ToolCalls) > 0 {
			if b, err := json.Marshal(compiled[i].ToolCalls); err == nil {
				bytes += len(b)
			}
		}
		for _, tb := range compiled[i].ThinkingBlocks {
			bytes += len(tb.Thinking) + len(tb.Signature)
		}
	}
	cheap := int(float64(bytes) / cheapBytesPerToken * f)
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
		if inFlight(i) || isSummary(i) {
			continue
		}
		tokens += sm.msgTokensLocked(tc, &compiled[i])
	}
	return int(float64(tokens) * f)
}

// accurateTokensLocked is the uncalibrated tiktoken count of KERNEL plus every
// compiled message — the whole prompt as sent, in-flight batch and summary
// included. Must hold lock.
func (sm *SegmentedMemory) accurateTokensLocked(compiled []Message) int {
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

// Provider calibration. The estimate tokenizes with tiktoken, which counts
// Claude prompts ~25% low (production: 1.17M estimated, 1.46M billed), so
// relief fired late and its recovery pass under-shed. After each successful
// call the provider's own prompt count is compared with loom's count of the
// same compiled context, and the running ratio scales every estimate.
const (
	// calibrationMin: never scale below tiktoken — a provider that counts
	// fewer tokens must not make relief fire later than it did uncalibrated.
	calibrationMin = 1.0
	// calibrationMax bounds a skewed sample (e.g. image blocks the estimate
	// does not count) to relief firing earlier, never to a runaway factor.
	calibrationMax = 2.0
	// calibrationAlpha is the EWMA weight of each new sample.
	calibrationAlpha = 0.5
	// calibrationMinSampleTokens skips small prompts, where fixed provider
	// overhead (tool-use system text, framing) dominates the ratio.
	calibrationMinSampleTokens = 2000
)

// calibrationLocked returns the current estimate scale factor. Must hold lock.
func (sm *SegmentedMemory) calibrationLocked() float64 {
	if sm.estimateCalibration < calibrationMin {
		return calibrationMin
	}
	return sm.estimateCalibration
}

// EstimateCalibration returns the factor the estimate is currently scaled by.
func (sm *SegmentedMemory) EstimateCalibration() float64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.calibrationLocked()
}

// ObservePromptTokens feeds one provider-reported prompt size (input + cache
// read + cache write — the whole prompt, see types.Usage) into the calibration.
// Call it right after a successful send, before anything is appended, so the
// compiled context is the one that was sent.
func (sm *SegmentedMemory) ObservePromptTokens(actual int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if actual <= 0 {
		return
	}
	counted := sm.accurateTokensLocked(sm.compileLocked())
	if counted < calibrationMinSampleTokens {
		return
	}
	ratio := min(max(float64(actual)/float64(counted), calibrationMin), calibrationMax)
	f := sm.calibrationLocked()
	sm.estimateCalibration = min(max(f+calibrationAlpha*(ratio-f), calibrationMin), calibrationMax)
	zap.L().Debug("relief: estimate calibration",
		zap.String("session_id", sm.sessionID),
		zap.Int("provider_prompt_tokens", actual),
		zap.Int("counted_tokens", counted),
		zap.Float64("sample_ratio", ratio),
		zap.Float64("factor", sm.estimateCalibration))
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
	for _, tb := range m.ThinkingBlocks {
		// Thinking rides the wire in-turn: text tokenized, signature ~4B/token.
		n += tc.CountTokens(tb.Thinking) + len(tb.Signature)/4
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

// startMarkLocked is the HWM: begin relief when the estimate reaches it. The
// configured MaxTokens IS G — the growth budget (fold every ~G of sheddable
// conversation), an absolute the same for every model, NOT a fraction of a
// provider window. The provider's real ceiling is enforced only by the
// refusal backstop. penalty (percentage points) lowers the mark for the
// recovery pass. Must hold lock.
func (sm *SegmentedMemory) startMarkLocked(penalty int) int {
	return applyPenalty(100, penalty) * sm.tokenBudget.MaxTokens / 100
}

// releaseMarkLocked is the LWM: shed down to it — the profile's release
// percentage applied to G. penalty lowers it in step with startMarkLocked so
// the recovery pass sheds deeper. Must hold lock.
func (sm *SegmentedMemory) releaseMarkLocked(penalty int) int {
	_, release := sm.marksLocked()
	return applyPenalty(release, penalty) * sm.tokenBudget.MaxTokens / 100
}

// --- releasePressure (HLD §5.2) ----------------------------------------------

// ReleasePressure is the relief pass, called at compile before every send. It
// SELF-GATES on loom's own accounting: if the estimate is under the start mark
// (HWM, §5.1) there is no pressure and it is a no-op. Otherwise it sheds to the
// release mark (LWM) via the escalating fold ladder below — recompiling and
// re-estimating after each op, returning the instant the estimate reaches the
// mark. The estimate is scaled by the provider calibration
// (ObservePromptTokens). Returns whether it shed and the post-pass
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

	sm.compressorFailedThisPass = false

	startEstimate := sm.estimateLocked()
	if startEstimate < sm.startMarkLocked(penalty) {
		return false, 0, 0 // under the start mark — no pressure to release
	}
	// The per-op lines log only post-op estimates; without the starting point
	// two consecutive passes read as one op inflating the estimate.
	zap.L().Info("releasePressure: start",
		zap.String("session_id", sm.sessionID),
		zap.Int64("turn", sm.currentTurnLocked()),
		zap.Int("estimate_tokens", startEstimate),
		zap.Int("start_tokens", sm.startMarkLocked(penalty)),
		zap.Int("target_tokens", sm.releaseMarkLocked(penalty)),
		zap.Int("penalty", penalty))

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
	// config (protectedRecentTurns, default 16); halving is the mechanism. Each
	// op recompiles and the pass returns the instant the estimate reaches target.
	//
	// The fold is the only shedding op. Eviction — stubbing an already-cached
	// tool result in place — was removed: rewriting history behind the cache
	// breakpoint discards the whole cached conversation region, so a stub worth
	// a few KB of reads cost a full-context rewrite at the write price, which is
	// 12.5x the read price it saved. Measured on the 30K window sweep: 9 of 12
	// prefix invalidations in one run were evictions, ~180K tokens of rewrite,
	// and every windowed arm cost more than not shedding at all.
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
	ops := make([]reliefOp, 0, len(rungs)+1)
	for _, b := range rungs {
		ops = append(ops, reliefOp{"fold", b, sm.foldLocked})
	}
	ops = append(ops, reliefOp{"fold", t, sm.foldLocked})
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
	if b >= sm.currentTurnLocked() {
		// Rung 0: the region may reach into the current turn, but never the
		// pending pair — cap it before the last assistant message (exclusive:
		// the call signature must survive with its results). For b < t the cap
		// is formally a no-op: the prefix ends before any turn-t row.
		lastAssistant := -1
		for i := range sm.contextMessages {
			if sm.contextMessages[i].Role == "assistant" {
				lastAssistant = i
			}
		}
		if lastAssistant < 0 {
			count = 0
		} else if count > lastAssistant {
			count = lastAssistant
		}
	}
	count = sm.adjustCompressionBoundary(count)
	if count <= 0 {
		return false
	}

	// Partition the prefix: the current turn's user-role rows — the ticket
	// and any skill-body sidecar — are the run's INPUT and never fold. They
	// cannot be re-derived from anything else in the session; a summary is a
	// paraphrase of the axioms, not the axioms. They stay in L1 verbatim and
	// re-enter the compile untouched. For b < t the predicate never fires:
	// settled turns fold whole, user rows included.
	tNow := sm.currentTurnLocked()
	foldRegion := make([]Message, 0, count)
	protected := make([]Message, 0, 2)
	for i := 0; i < count; i++ {
		if sm.contextMessages[i].Turn == tNow &&
			(sm.contextMessages[i].Role == "user" || IsSyntheticWireUserRole(sm.contextMessages[i].Role)) {
			protected = append(protected, sm.contextMessages[i])
			continue
		}
		foldRegion = append(foldRegion, sm.contextMessages[i])
	}
	if len(foldRegion) == 0 {
		return false
	}
	region := foldRegion

	// The covered span, from the folded rows' persisted seqs.
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
	input := make([]Message, 0, len(region)+1)
	if sm.summary.text != "" {
		input = append(input, Message{Role: "system", Content: "Current summary:\n" + sm.summary.text})
	}
	for i := range region {
		r := sm.renderLocked(&region[i], t)
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
	// The compressor is REQUIRED: a fold without a real summary is task
	// amnesia when a summary was possible. A compressor that is absent or
	// still failing after retries drops the region and says so in one line,
	// which sheds the space without claiming a summary it does not have: the
	// summary gains one line naming the span and its first user ask. Lossy and
	// honest, and the fold is the only rung there is, so a session with no
	// compressor would otherwise meet the window with no rung left.
	fallback := false
	askCompressor := sm.compressor != nil && sm.compressor.IsEnabled()
	if askCompressor && sm.compressorFailedThisPass {
		// The compressor already failed this pass; another region is another
		// three attempts against the same fault. Drop with the marker instead.
		askCompressor = false
		zap.L().Warn("releasePressure: fold dropping the region unsummarised — the compressor already failed in this pass",
			zap.String("session_id", sm.sessionID),
			zap.Int64("boundary_turn", b))
	}
	newText := ""
	const compressAttempts = 3
	for attempt := 1; askCompressor && attempt <= compressAttempts; attempt++ {
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

		if err == nil && strings.TrimSpace(compressed) != "" {
			newText = strings.TrimSpace(compressed)
			// The first line states the covered span (§5.4.4) — enforced here
			// when the compressor omitted it OR echoed a stale line: a span
			// whose upper bound is below hiSeq under-claims this fold's
			// coverage, and coverage is never silently lost.
			if !coversThrough(newText, hiSeq) {
				newText = fmt.Sprintf("covers msg:%d-%d\n", loSeq, hiSeq) + newText
			}
			break
		}
		zap.L().Warn("releasePressure: compressor attempt failed",
			zap.String("session_id", sm.sessionID),
			zap.Int64("boundary_turn", b),
			zap.Int("attempt", attempt),
			zap.Error(err))
	}
	if newText == "" {
		if askCompressor {
			// Remember it for the rest of the pass: the next region would pay
			// the same attempts against the same fault.
			sm.compressorFailedThisPass = true
			zap.L().Error("releasePressure: compressor failed after retries; the region is dropped unsummarised",
				zap.String("session_id", sm.sessionID),
				zap.Int64("boundary_turn", b))
		}
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
	//
	// A skill whose load pair ALSO appears after the region was reloaded and its
	// newer load is still in live context: that activation is current, so it is
	// neither deactivated nor noted, and a note left by an earlier fold is
	// dropped. Deactivation is by name, so without this a fold of an old load
	// pair kills a reload from the very turn that asked for it.
	stillLoaded := make(map[string]bool)
	// Pass 0, not the current turn: this scan asks which skills are loaded in
	// live context, and a reload made THIS turn is the most live activation
	// there is — skipping it would leave an earlier fold's note standing over
	// a skill that is active again.
	for _, name := range foldedSkillLoads(sm.contextMessages[count:], 0) {
		stillLoaded[name] = true
		delete(sm.foldedSkills, name)
	}
	var newlyFolded []string
	for _, name := range foldedSkillLoads(region, sm.currentTurnLocked()) {
		if !stillLoaded[name] {
			newlyFolded = append(newlyFolded, name)
		}
	}
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
	remaining := make([]Message, 0, len(protected)+len(sm.contextMessages)-count)
	remaining = append(remaining, protected...)
	remaining = append(remaining, sm.contextMessages[count:]...)
	sm.contextMessages = remaining
	sm.l1Dirty = true
	sm.updateTokenCount()
	sm.tokenCountDirty = false

	zap.L().Info("releasePressure: fold",
		zap.String("session_id", sm.sessionID),
		zap.Int64("boundary_turn", b),
		zap.Int("rows_folded", len(region)),
		zap.Int("rows_protected", len(protected)),
		zap.Int64("seq_lo", loSeq),
		zap.Int64("seq_hi", hiSeq),
		zap.Int("version", n1),
		zap.Int("output_bytes", len(newText)),
		zap.Bool("unsummarised", fallback))
	return true
}

// firstUserLine returns the first line of the region's first user message, so
// a dropped region still names what it was about.
func firstUserLine(region []Message) string {
	for i := range region {
		if region[i].Role != "user" || region[i].Content == "" {
			continue
		}
		line := region[i].Content
		if j := strings.IndexByte(line, '\n'); j >= 0 {
			line = line[:j]
		}
		line = strings.TrimSpace(line)
		return collapseTo(line, 120)
	}
	return "(no user message in the region)"
}

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

// foldedSkillLoads returns the names of skills whose manage_skills load pair —
// the load call paired with a "Skill loaded: " confirmation — lies inside the
// region AND belongs to a settled turn.
//
// A load pair from the CURRENT turn is skipped. Rung 0 folds the current turn,
// so without this a turn that loaded a skill and then hit the window would
// have that skill deactivated mid-turn: its tools leave the kernel while the
// protected skill-body row — the instructions telling the model to use them —
// stays in context. The model is then told how to drive tools it no longer
// has. The load pair's text still folds away; the activation it created
// outlives it, and the ordinary settled-turn fold deactivates it later.
func foldedSkillLoads(region []Message, currentTurn int64) []string {
	loadCalls := make(map[string]string) // tool_use_id → skill name
	for i := range region {
		if region[i].Turn == currentTurn {
			continue
		}
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
		if m.Role != "tool" || m.ToolUseID == "" || m.Turn == currentTurn {
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
// ladder folds at K, K/2, K/4 … 1 (§5.2).
const defaultProtectedRecentTurns = 4

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

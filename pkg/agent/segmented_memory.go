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
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage"
	"github.com/teradata-labs/loom/pkg/types"
)

// MemoryLayer represents different tiers of context memory
type MemoryLayer string

const (
	LayerROM    MemoryLayer = "rom"    // Read-only: Documentation, system prompt (never changes)
	LayerKernel MemoryLayer = "kernel" // Advertised tool schemas (ride the provider tools parameter)
	LayerL1     MemoryLayer = "l1"     // The message list, in order
	LayerL2     MemoryLayer = "l2"     // The session summary: one cumulative text
)

// SegmentedMemory holds the context segments (HLD §2 — data, no logic):
//
//   - KERNEL — the advertised tool schemas ride the provider tools parameter;
//     only their serialized byte count is tracked here (blueprint A6).
//   - ROM — the system prompt. Byte-stable for the life of the session.
//   - L2 — the session summary: ONE cumulative text, empty until the first
//     fold; persisted as version rows, the newest version is the summary.
//   - L1 — the message list, in order. In memory during a turn (full natural
//     forms); rebuilt from rows on the next turn or on reload.
//
// All logic lives in ContextCompilation (context_compilation.go): compile
// renders, releasePressure is the only mutator of context state. Arrival
// appends; nothing here examines, sizes, flags, or transforms at arrival.
type SegmentedMemory struct {
	// ROM Layer (never changes during session)
	romContent string // Static documentation content

	// Kernel Layer: advertised tool names (stats only) and the serialized
	// bytes of the advertised tool schemas as built for the provider call.
	tools       []string
	kernelBytes int

	// L1 — the message list (renamed from l1Messages: rename-enforcement, D7).
	contextMessages []Message

	// L2 — the session summary (renamed from l2Summary: one cumulative text,
	// versioned; HLD §2).
	summary summaryState

	// foldedSkills accumulates skills deactivated by fold (§4.5) and not yet
	// reloaded. Re-pinned as a note at the end of every summary version so the
	// note survives a later fold whose compressor would otherwise paraphrase it
	// away — the note is STATE, not compressor output. Keyed by skill name.
	foldedSkills map[string]bool

	// Store binding for relief transactions and reload (optional).
	sessionStore SessionStorage
	sessionID    string

	// Token management (internal caches; nothing acts on them — the only
	// actionable measure is releasePressure's estimate).
	tokenCounter    *TokenCounter
	tokenBudget     *TokenBudget
	tokenCount      int
	tokenCountDirty bool

	// Per-layer token count caches (reporting only)
	cachedROMTokens    int
	cachedKernelTokens int
	cachedL1Tokens     int
	cachedL2Tokens     int
	kernelDirty        bool
	l1Dirty            bool

	// msgTokenCache memoises the tiktoken count of a rendered message content,
	// so the compile-time estimate (§5.1) does not re-tokenize unchanged
	// messages on every provider call. Keyed by the rendered string, so a stub,
	// a whole row, and a folded form each cache separately.
	msgTokenCache map[string]int

	// Memory compression (the fold compressor)
	compressor MemoryCompressor

	// Shared memory for large data (backs explicit shared_memory tools only)
	sharedMemory *storage.SharedMemoryStore

	// threshold is the one threshold value (HLD §5.1): compile-time offload
	// bound, persist-time row bound, retrieval page bound. Bytes.
	threshold int

	// protectedRecentTurns is K (HLD §5.1): the K newest turns are never
	// touched by relief.
	protectedRecentTurns int

	// reliefInFlight guards ReleasePressure: fold releases the lock across
	// its compressor call, and this keeps a second pass from interleaving.
	reliefInFlight bool

	// skillDeactivation is the skills orchestrator's deactivation path, called
	// when fold flags a region containing a manage_skills load pair.
	skillDeactivation func(sessionID, skillName string)

	// Observability
	tracer observability.Tracer

	// Per-mutation debug carrier (optional).
	ctxDebug *contextDebug

	// Semantic search
	llmProvider LLMProvider // For reranking search results (optional)

	// Active pattern tracking (optional)
	patternName string // Name of the active pattern, surfaced via GetActivePattern

	// Configuration (the profile carries the relief water marks, §5.1)
	maxL1Tokens        int
	minL1Messages      int
	compressionProfile CompressionProfile

	mu sync.RWMutex
}

// MemoryCompressor defines the interface for LLM-powered memory compression.
// Implementations should compress message history into brief summaries.
type MemoryCompressor interface {
	CompressMessages(ctx context.Context, messages []Message) (string, error)
	IsEnabled() bool
}

// NewSegmentedMemory creates a new segmented memory instance with ROM content.
// If maxContextTokens or reservedOutputTokens are 0, defaults to Claude Sonnet 4.5 values (200K/20K)
func NewSegmentedMemory(romContent string, maxContextTokens, reservedOutputTokens int) *SegmentedMemory {
	// Use balanced profile as default (backwards compatibility)
	balancedProfile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]
	return NewSegmentedMemoryWithCompression(romContent, maxContextTokens, reservedOutputTokens, balancedProfile)
}

// NewSegmentedMemoryWithCompression creates a new segmented memory instance
// with a custom profile. The profile's CriticalThresholdPercent /
// WarningThresholdPercent are the relief water marks (HWM/LWM, HLD §5.1),
// percentages of usable context; a missing or inverted pair falls back to
// 90/60.
func NewSegmentedMemoryWithCompression(romContent string, maxContextTokens, reservedOutputTokens int, profile CompressionProfile) *SegmentedMemory {
	// Use defaults if not specified (backwards compatibility)
	if maxContextTokens == 0 {
		maxContextTokens = 200000 // Claude Sonnet 4.5 default
	}
	if reservedOutputTokens == 0 {
		reservedOutputTokens = maxContextTokens / 10 // 10% of the window, not a flat 20K
	}
	// Reserve must leave a positive working budget. Cap the reserve at half
	// the window on small/misconfigured limits.
	if reservedOutputTokens >= maxContextTokens {
		reservedOutputTokens = maxContextTokens / 2
	}

	// Initialize token counter and budget
	tokenCounter := GetTokenCounter()
	tokenBudget := NewTokenBudget(maxContextTokens, reservedOutputTokens)

	sm := &SegmentedMemory{
		romContent:           romContent,
		tools:                make([]string, 0),
		contextMessages:      make([]Message, 0),
		sessionStore:         nil, // Set via SetSessionStore
		sessionID:            "",  // Set via SetSessionStore
		tokenCounter:         tokenCounter,
		tokenBudget:          tokenBudget,
		compressor:           nil,                           // Set via SetCompressor after initialization
		tracer:               observability.NewNoOpTracer(), // Set via SetTracer
		maxL1Tokens:          profile.MaxL1Tokens,
		minL1Messages:        profile.MinL1Messages,
		compressionProfile:   profile,
		threshold:            int(storage.DefaultSharedMemoryThreshold),
		protectedRecentTurns: defaultProtectedRecentTurns,
	}

	// Initialize all per-layer token caches
	sm.fullRecount()
	sm.tokenCountDirty = false
	return sm
}

// SetCompressor sets the memory compressor for fold (HLD §5.4).
// Should be called after agent initialization to avoid dependency cycles.
func (sm *SegmentedMemory) SetCompressor(compressor MemoryCompressor) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.compressor = compressor
}

// SetSharedMemory sets the shared memory store (backs explicit shared_memory tools).
func (sm *SegmentedMemory) SetSharedMemory(sharedMemory *storage.SharedMemoryStore) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.sharedMemory = sharedMemory
}

// SetTracer sets the observability tracer for error logging and metrics.
func (sm *SegmentedMemory) SetTracer(tracer observability.Tracer) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if tracer != nil {
		sm.tracer = tracer
	}
}

// SetContextDebug injects the per-mutation debug carrier.
func (sm *SegmentedMemory) SetContextDebug(cd *contextDebug) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.ctxDebug = cd
}

// SetLLMProvider injects an LLM provider for semantic search reranking.
// If not set, semantic search will fall back to BM25-only ranking.
func (sm *SegmentedMemory) SetLLMProvider(llm LLMProvider) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if llm != nil {
		sm.llmProvider = llm
	}
}

// SetSessionStore binds the durable store: relief transactions (flags, summary
// versions) and reload read through it.
func (sm *SegmentedMemory) SetSessionStore(store SessionStorage, sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.sessionStore = store
	sm.sessionID = sessionID
}

// SetThreshold sets the one threshold value in bytes (HLD §5.1: one value,
// three roles — compile-time offload bound, persist-time row bound, retrieval
// page bound). Non-positive values keep the current threshold; positive values
// below minThreshold clamp to it — the truncation tail alone runs ~150 bytes,
// so a tinier bound could not hold stored = core + tail ≤ threshold.
func (sm *SegmentedMemory) SetThreshold(bytes int64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if bytes > 0 {
		if bytes < minThreshold {
			bytes = minThreshold
		}
		sm.threshold = int(bytes)
	}
}

// minThreshold is the floor for the §5.1 threshold: enough room for the §4.1
// truncation tail plus a meaningful core.
const minThreshold = 256

// Threshold returns the configured threshold in bytes.
func (sm *SegmentedMemory) Threshold() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.threshold
}

// CurrentTurn returns T — the session's current turn number: max(Turn) over L1
// (HLD §5.1). Derived, never counted.
func (sm *SegmentedMemory) CurrentTurn() int64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.currentTurnLocked()
}

// currentTurnLocked computes max(Turn) over L1. Must hold lock.
func (sm *SegmentedMemory) currentTurnLocked() int64 {
	var t int64
	for i := range sm.contextMessages {
		if sm.contextMessages[i].Turn > t {
			t = sm.contextMessages[i].Turn
		}
	}
	return t
}

// DropTurnPayloads is the TURN END drop (HLD §1, §7.3; blueprint A1), run when
// a new turn starts: every prior-turn tool message's in-memory content is
// replaced by its persisted-row form (truncated core + tail), so a long-lived
// loom agent does not grow by one payload set per turn. Cloud gets this for
// free by being stateless.
func (sm *SegmentedMemory) DropTurnPayloads() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.threshold <= 0 {
		return
	}
	changed := false
	for i := range sm.contextMessages {
		m := &sm.contextMessages[i]
		if m.Role == "tool" && len(m.Content) > sm.threshold {
			m.Content = truncateToolRowContent(m.Content, sm.threshold)
			changed = true
		}
	}
	if changed {
		sm.l1Dirty = true
		sm.updateTokenCount()
		sm.tokenCountDirty = false
	}
}

// AddMessage appends a message to L1 (HLD §1: arrival appends — nothing is
// examined, sized, flagged, or transformed at arrival). The incremental token
// caches are updated for reporting only; no mechanism here acts on them.
func (sm *SegmentedMemory) AddMessage(ctx context.Context, msg Message) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.contextMessages = append(sm.contextMessages, msg)

	// Incremental L1 token update (reporting only).
	msgTokens := 10 + sm.tokenCounter.CountTokens(msg.Content) // 10 = message overhead
	if len(msg.ToolCalls) > 0 {
		msgTokens += sm.tokenCounter.CountTokens(fmt.Sprintf("%v", msg.ToolCalls))
	}
	sm.cachedL1Tokens += msgTokens

	if sm.tokenCountDirty {
		sm.fullRecount()
		sm.tokenCountDirty = false
	} else {
		sm.updateTokenCount()
	}
}

// adjustCompressionBoundary ensures tool_use/tool_result pairs stay together
// (pair atomicity — HLD §11): a fold region never splits a call from its
// result. Returns the adjusted boundary count. Must hold lock.
func (sm *SegmentedMemory) adjustCompressionBoundary(toCompressCount int) int {
	if toCompressCount >= len(sm.contextMessages) {
		return toCompressCount
	}

	// Build a set of tool_call IDs for each assistant message, and a reverse
	// map from tool_use_id → assistant index, so we can reason about complete
	// groups rather than individual messages.
	type toolGroup struct {
		assistantIdx int
		toolCallIDs  map[string]struct{}
	}

	// Collect all tool groups in the full message list.
	var groups []toolGroup
	assistantForToolID := make(map[string]int) // tool_use_id → index into groups

	for i, msg := range sm.contextMessages {
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			g := toolGroup{
				assistantIdx: i,
				toolCallIDs:  make(map[string]struct{}, len(msg.ToolCalls)),
			}
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" {
					g.toolCallIDs[tc.ID] = struct{}{}
					assistantForToolID[tc.ID] = len(groups)
				}
			}
			groups = append(groups, g)
		}
	}

	// For each group, find the index of its last tool_result in the message list.
	// A group is "complete" only when all its tool_results are present.
	groupLastToolIdx := make(map[int]int, len(groups))
	groupFoundCount := make(map[int]int, len(groups))
	for i, msg := range sm.contextMessages {
		if msg.Role == "tool" && msg.ToolUseID != "" {
			if gIdx, ok := assistantForToolID[msg.ToolUseID]; ok {
				groupFoundCount[gIdx]++
				if i > groupLastToolIdx[gIdx] {
					groupLastToolIdx[gIdx] = i
				}
			}
		}
	}

	// Walk backward from the proposed boundary to find a safe cut point.
	changed := true
	for changed {
		changed = false

		for gIdx, g := range groups {
			lastToolIdx, hasResults := groupLastToolIdx[gIdx]
			isComplete := hasResults && groupFoundCount[gIdx] == len(g.toolCallIDs)
			// A PRIOR-turn group missing results will never gain them — the
			// crash window closed and compile synthesizes the missing result
			// (§5.2 step 7) — so it must not block the boundary forever.
			staleIncomplete := !isComplete &&
				sm.contextMessages[g.assistantIdx].Turn < sm.currentTurnLocked()

			if g.assistantIdx < toCompressCount {
				// Assistant would fold.
				if !isComplete && !staleIncomplete {
					// Incomplete current-turn group: results may still arrive —
					// pull boundary back to exclude the assistant.
					toCompressCount = g.assistantIdx
					changed = true
				} else if hasResults && lastToolIdx >= toCompressCount {
					// Assistant folds but some tool_results stay in L1 — pull back.
					toCompressCount = g.assistantIdx
					changed = true
				}
				// If assistant AND all its results fold, that's fine.
			} else {
				// Assistant stays in L1.
				if hasResults {
					// Some tool_results might be on the folded side of the boundary.
					for i := g.assistantIdx + 1; i < len(sm.contextMessages) && i <= lastToolIdx; i++ {
						if sm.contextMessages[i].Role == "tool" && sm.contextMessages[i].ToolUseID != "" {
							if _, belongs := g.toolCallIDs[sm.contextMessages[i].ToolUseID]; belongs {
								if i < toCompressCount {
									// This tool_result would fold but its assistant stays.
									toCompressCount = g.assistantIdx
									changed = true
									break
								}
							}
						}
					}
				}
			}
		}
	}

	return toCompressCount
}

// ReplayMessages bulk-loads rows into L1 (reload, HLD §8): a pure append of
// rows already filtered folded=false by the store read. T = max(Turn) over the
// read rows — nothing else to restore; live and restored are the same read of
// the same rows.
func (sm *SegmentedMemory) ReplayMessages(ctx context.Context, messages []Message) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if len(messages) == 0 {
		return
	}

	sm.contextMessages = append(sm.contextMessages, messages...)
	sm.l1Dirty = true
	sm.updateTokenCount()
	sm.tokenCountDirty = false
}

// GetMessages returns all L1 messages for building conversation context.
func (sm *SegmentedMemory) GetMessages() []Message {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	messages := make([]Message, len(sm.contextMessages))
	copy(messages, sm.contextMessages)
	return messages
}

// GetRecentConversationTurns retrieves the last N messages from L1 cache,
// including all roles (user, assistant, tool). Used by graph memory extraction
// to get richer context than tool-only results.
func (sm *SegmentedMemory) GetRecentConversationTurns(n int) []types.Message {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if n <= 0 {
		return nil
	}

	if len(sm.contextMessages) <= n {
		messages := make([]types.Message, len(sm.contextMessages))
		copy(messages, sm.contextMessages)
		return messages
	}

	start := len(sm.contextMessages) - n
	messages := make([]types.Message, n)
	copy(messages, sm.contextMessages[start:])
	return messages
}

// GetL2Summary returns the session summary's newest version text.
// Returns empty string if no fold has occurred yet.
func (sm *SegmentedMemory) GetL2Summary() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.summary.text
}

// HasL2Content returns true if the session summary has content (a fold occurred).
func (sm *SegmentedMemory) HasL2Content() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.summary.text) > 0
}

// maxSummaryUserQueryChars caps user message content preserved in compressor
// fallback summaries (memory_compressor.go).
const maxSummaryUserQueryChars = 500

// truncateForSummary caps text at max bytes without splitting a UTF-8 rune,
// appending "..." when truncated.
func truncateForSummary(text string, max int) string {
	if len(text) <= max {
		return text
	}
	cut := text[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "..."
}

// updateTokenCount refreshes the reporting caches (must hold lock). Nothing
// acts on these values; the only actionable measure is releasePressure's
// estimate.
func (sm *SegmentedMemory) updateTokenCount() {
	if sm.kernelDirty {
		sm.recountKernel()
		sm.kernelDirty = false
	}

	if sm.l1Dirty {
		sm.cachedL1Tokens = sm.tokenCounter.EstimateMessagesTokens(sm.contextMessages)
		sm.l1Dirty = false
	}

	count := sm.cachedROMTokens +
		sm.cachedKernelTokens +
		sm.cachedL1Tokens +
		sm.cachedL2Tokens

	sm.tokenCount = count
	sm.tokenBudget.Set(count)
}

// fullRecount forces a complete recalculation of all layer caches (must hold lock).
func (sm *SegmentedMemory) fullRecount() {
	sm.cachedROMTokens = sm.tokenCounter.CountTokens(sm.romContent)
	sm.recountKernel()
	sm.cachedL1Tokens = sm.tokenCounter.EstimateMessagesTokens(sm.contextMessages)
	sm.cachedL2Tokens = sm.tokenCounter.CountTokens(sm.summary.text)
	sm.kernelDirty = false
	sm.l1Dirty = false

	sm.tokenCount = sm.cachedROMTokens +
		sm.cachedKernelTokens +
		sm.cachedL1Tokens +
		sm.cachedL2Tokens

	sm.tokenBudget.Set(sm.tokenCount)
}

// recountKernel derives the kernel token figure from the serialized bytes of
// the advertised tool schemas (blueprint A6). Feeds the estimate only.
func (sm *SegmentedMemory) recountKernel() {
	sm.cachedKernelTokens = tokenFigure(sm.kernelBytes)
}

// GetMessagesForLLM builds the compiled context for the LLM call
// (ContextCompilation, HLD §5.2 steps 1–7).
func (sm *SegmentedMemory) GetMessagesForLLM() []Message {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.compileLocked()
}

// GetContextWindow renders the compiled context as a formatted string, via the
// same compile.
func (sm *SegmentedMemory) GetContextWindow() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var parts []string

	if sm.romContent != "" {
		parts = append(parts, "=== DOCUMENTATION (ROM) ===")
		parts = append(parts, sm.romContent)
		parts = append(parts, "")
	}

	if sm.summary.text != "" {
		parts = append(parts, "=== CONVERSATION SUMMARY (L2) ===")
		parts = append(parts, sm.summary.text)
		parts = append(parts, "")
	}

	compiled := sm.compileLocked()
	if len(compiled) > 0 {
		parts = append(parts, "=== CONVERSATION (L1, as compiled) ===")
		for _, msg := range compiled {
			if msg.Role == "system" {
				continue // ROM and summary already rendered above
			}
			parts = append(parts, fmt.Sprintf("[%s]: %s", msg.Role, msg.Content))
		}
		parts = append(parts, "")
	}

	return strings.Join(parts, "\n")
}

// GetTokenCount returns current token count across all memory layers (reporting).
func (sm *SegmentedMemory) GetTokenCount() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.tokenCountDirty {
		sm.fullRecount()
		sm.tokenCountDirty = false
	}

	return sm.tokenCount
}

// GetActivePattern returns the name of the active pattern (empty if none).
func (sm *SegmentedMemory) GetActivePattern() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.patternName
}

// GetTokenBudgetMax returns the total token budget (context window size).
func (sm *SegmentedMemory) GetTokenBudgetMax() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	_, _, total := sm.tokenBudget.GetUsage()
	return total
}

// ResetContext clears the conversational state: L1, the summary, and the
// active pattern name. ROM and kernel are preserved — they are structural.
func (sm *SegmentedMemory) ResetContext() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.contextMessages = sm.contextMessages[:0]
	sm.summary = summaryState{}
	sm.patternName = ""

	sm.fullRecount()
	sm.tokenCountDirty = false

	if sm.tracer != nil {
		sm.tracer.RecordMetric("memory.context_reset", 1.0, nil)
	}
}

// GetL1MessageCount returns number of messages in L1 cache.
func (sm *SegmentedMemory) GetL1MessageCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.contextMessages)
}

// GetTokenBudgetUsage returns current token budget usage information.
// Returns: (used, available, total)
func (sm *SegmentedMemory) GetTokenBudgetUsage() (int, int, int) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.tokenBudget.GetUsage()
}

// GetMemoryStats returns comprehensive memory statistics (reporting only —
// no UsagePercentage read gates any action anywhere).
func (sm *SegmentedMemory) GetMemoryStats() map[string]interface{} {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	used, available, total := sm.tokenBudget.GetUsage()
	budgetPct := sm.tokenBudget.UsagePercentage()

	return map[string]interface{}{
		"total_tokens":       sm.tokenCount,
		"tokens_used":        used,
		"tokens_available":   available,
		"token_budget_total": total,
		"budget_usage_pct":   budgetPct,
		"l1_message_count":   len(sm.contextMessages),
		"l1_max_tokens":      sm.maxL1Tokens,
		"l1_min_messages":    sm.minL1Messages,
		"l2_summary_length":  len(sm.summary.text),
		"l2_summary_version": sm.summary.n,
		"rom_token_count":    sm.cachedROMTokens,
		"kernel_token_count": sm.cachedKernelTokens,
		"l1_token_count":     sm.cachedL1Tokens,
		"l2_token_count":     sm.cachedL2Tokens,
		"budget_warning":     sm.getBudgetWarning(),
	}
}

// getBudgetWarning returns a warning message if budget usage is high (must
// hold lock). A read for reporting — it acts on nothing.
func (sm *SegmentedMemory) getBudgetWarning() string {
	usage := sm.tokenBudget.UsagePercentage()
	warningThreshold := float64(sm.compressionProfile.WarningThresholdPercent)
	if usage > warningThreshold {
		return fmt.Sprintf("INFO: Token budget >%.0f%% - monitoring", warningThreshold)
	}
	return ""
}

// SearchMessages performs semantic search over conversation history using BM25 + LLM reranking.
//
// Algorithm:
// 1. BM25 full-text search via FTS5 (top-50 candidates)
// 2. LLM-based reranking for semantic relevance (top-N results)
//
// Returns top-N most relevant messages ordered by relevance.
func (sm *SegmentedMemory) SearchMessages(
	ctx context.Context,
	query string,
	limit int,
) ([]Message, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.sessionStore == nil || sm.sessionID == "" {
		return nil, fmt.Errorf("semantic search requires a session store")
	}

	ctx, span := sm.tracer.StartSpan(ctx, "memory.search_messages")
	defer sm.tracer.EndSpan(span)

	span.SetAttribute("query", query)
	span.SetAttribute("limit", fmt.Sprintf("%d", limit))

	// Phase 1: BM25 retrieval (top-50 for reranking)
	candidateLimit := 50
	candidates, err := sm.sessionStore.SearchMessages(ctx, sm.sessionID, query, candidateLimit)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("BM25 search failed: %w", err)
	}

	if len(candidates) == 0 {
		return []Message{}, nil
	}

	span.SetAttribute("bm25_results", fmt.Sprintf("%d", len(candidates)))

	// Phase 2: LLM reranking (top-N most relevant)
	ranked, err := sm.rerankByRelevance(ctx, query, candidates, limit)
	if err != nil {
		// Log error but return BM25 results as fallback
		span.RecordError(err)
		if limit > len(candidates) {
			return candidates, nil
		}
		return candidates[:limit], nil
	}

	span.SetAttribute("reranked_results", fmt.Sprintf("%d", len(ranked)))
	return ranked, nil
}

// rerankByRelevance uses LLM to rerank search results by semantic relevance.
//
// Falls back to BM25 ordering if LLM is not configured or reranking fails.
func (sm *SegmentedMemory) rerankByRelevance(
	ctx context.Context,
	query string,
	candidates []Message,
	topN int,
) ([]Message, error) {
	if sm.llmProvider == nil {
		// No LLM configured, return BM25 results
		if topN > len(candidates) {
			return candidates, nil
		}
		return candidates[:topN], nil
	}

	ctx, span := sm.tracer.StartSpan(ctx, "memory.rerank_search_results")
	defer sm.tracer.EndSpan(span)

	span.SetAttribute("query", query)
	span.SetAttribute("candidates_count", fmt.Sprintf("%d", len(candidates)))
	span.SetAttribute("top_n", fmt.Sprintf("%d", topN))

	// Build reranking prompt
	prompt := fmt.Sprintf(`Given the search query: "%s"

Rank the following conversation messages by relevance (0-10, where 10 is most relevant).
Consider semantic similarity, not just keyword matching.

Messages:
%s

Respond with JSON array: [{"index": 0, "score": 8}, {"index": 1, "score": 3}, ...]
Order by score descending (most relevant first).`, query, sm.formatCandidatesForReranking(candidates))

	messages := []types.Message{
		{Role: "user", Content: prompt},
	}

	// Call LLM (no tools needed for reranking)
	response, err := sm.llmProvider.Chat(ctx, messages, nil)
	if err != nil {
		span.RecordError(err)
		// Fallback: return BM25 results
		if topN > len(candidates) {
			return candidates, nil
		}
		return candidates[:topN], nil
	}

	// Parse JSON response
	type RankScore struct {
		Index int     `json:"index"`
		Score float64 `json:"score"`
	}
	var scores []RankScore
	if err := json.Unmarshal([]byte(response.Content), &scores); err != nil {
		span.RecordError(fmt.Errorf("failed to parse reranking scores: %w", err))
		// Fallback to BM25 ordering
		if topN > len(candidates) {
			return candidates, nil
		}
		return candidates[:topN], nil
	}

	// Reorder candidates by LLM scores
	ranked := make([]Message, 0, topN)
	for _, score := range scores {
		if score.Index >= 0 && score.Index < len(candidates) {
			ranked = append(ranked, candidates[score.Index])
			if len(ranked) >= topN {
				break
			}
		}
	}

	span.SetAttribute("ranked_count", fmt.Sprintf("%d", len(ranked)))
	return ranked, nil
}

// formatCandidatesForReranking formats messages for the reranking prompt.
// Includes message index, role, and content preview (200 chars).
func (sm *SegmentedMemory) formatCandidatesForReranking(candidates []Message) string {
	var sb strings.Builder
	for i, msg := range candidates {
		preview := msg.Content
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		fmt.Fprintf(&sb, "[%d] %s: %s\n", i, msg.Role, preview)
	}
	return sb.String()
}

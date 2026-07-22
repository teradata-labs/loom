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
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
)

// MemoryLayer represents different tiers of context memory
type MemoryLayer string

const (
	LayerROM    MemoryLayer = "rom"    // Read-only: Documentation, system prompt (never changes)
	LayerKernel MemoryLayer = "kernel" // Tool definitions, recent tool results (per conversation)
	LayerL1     MemoryLayer = "l1"     // Hot: Recent messages (last 5-10 exchanges)
	LayerL2     MemoryLayer = "l2"     // Warm: Summarized history (compressed older messages)
	LayerSwap   MemoryLayer = "swap"   // Cold: Long-term storage (database-backed)
)

// SegmentedMemory manages context using a tiered memory hierarchy.
//
// Architecture:
// - ROM Layer: Static documentation/prompts (never changes during session)
// - Kernel Layer: Tool definitions, recent results, schema cache (per conversation)
// - L1 Cache: Hot - Recent messages (last 10 messages / 5 exchanges)
// - L2 Cache: Warm - Compressed history summaries
// - Swap: Cold - Database-backed long-term storage
//
// Features:
// - Adaptive compression: Triggers at 70% token budget usage
// - LRU schema caching: Max 10 schemas with least-recently-used eviction
// - Database-backed tool results: Keeps only immediate previous result in memory
// - Token budget enforcement: 200K context, 20K output reserve = 180K available
type SegmentedMemory struct {
	// ROM Layer (never changes during session)
	romContent string // Static documentation content

	// Kernel Layer (changes per conversation)
	tools           []string             // Available tool names
	toolResults     []CachedToolResult   // Recent tool execution results (max 1 for database-backed optimization)
	schemaCache     map[string]string    // Cached schema discoveries
	schemaAccessLog map[string]time.Time // LRU tracking for schema cache
	maxSchemas      int                  // Maximum schemas to cache (default: 10)

	// L1 Cache (hot - recent messages)
	l1Messages []Message // Last N messages (configurable, default: 10)

	// L2 Cache (warm - summarized history)
	l2Summary string // Compressed summary of older conversation

	// Swap Layer (cold - database-backed long-term storage)
	sessionStore       SessionStorage // Database for persistent storage (optional)
	sessionID          string         // Session identifier for swap operations
	swapEnabled        bool           // Whether swap layer is configured
	maxL2Tokens        int            // Maximum tokens in L2 before eviction to swap (default: 5000)
	swapEvictionCount  int            // Number of L2 evictions to swap (statistics)
	swapRetrievalCount int            // Number of retrievals from swap (statistics)

	// Token management
	tokenCounter    *TokenCounter // Accurate token counting
	tokenBudget     *TokenBudget  // Token budget enforcement
	tokenCount      int           // Actual token count of current context
	tokenCountDirty bool          // Whether token count needs recalculation

	// Per-layer token count caches (avoids full recalculation on every AddMessage)
	cachedROMTokens        int    // Tokens in ROM layer (changes only on init)
	cachedL1Tokens         int    // Tokens in L1 messages
	cachedL2Tokens         int    // Tokens in L2 summary
	cachedToolSchemaTokens int    // Measured tool-schema cost (Σ CountTokens(json(tool.InputSchema()))), recomputed only when the registered/effective tool set changes
	toolSchemaFingerprint  string // Order-independent fingerprint of the tool set last used to compute cachedToolSchemaTokens
	l1Dirty                bool   // Whether L1 needs full recount (e.g., after compression)

	// Memory compression
	compressor MemoryCompressor // LLM-powered compression (optional)

	// Shared memory for large data
	sharedMemory *storage.SharedMemoryStore // Shared memory store for large tool results (optional)

	// Observability
	tracer observability.Tracer // Tracer for error logging and metrics (optional)

	// Semantic search
	llmProvider LLMProvider // For reranking search results (optional)

	// Configuration
	minL1Messages      int                // Minimum messages to keep in L1 (for recency)
	maxToolResults     int                // Max tool results to keep in kernel (default: 1 for database-backed)
	compressionProfile CompressionProfile // Compression behavior profile (thresholds, batch sizes)

	// Single-writer pressure pipeline zone thresholds (percentages, D-1).
	// Defaults 70/85, overridable via agent config (MemoryCompressionConfig).
	// Used by ZoneThresholds(); when the token window/basis is unknown these
	// are bypassed in favor of the legacy compressionProfile Warning/Critical
	// thresholds (see ZoneThresholds).
	yellowPct float64
	redPct    float64

	// Valve (yellow-zone eviction) configuration (D-4, C-E). Defaults
	// maxContextTokens/10 (10% of budget) and 3, overridable via
	// SetMinValvePayoffTokens/SetKeepRecentBallast. The min-payoff scales
	// with budget because both sides of the cache-cost math scale with it:
	// a stub-in-place edit invalidates the prompt cache from that position
	// to the tail (bounded by budget), so the reclaim that justifies it
	// must be a fixed fraction of budget, not a fixed absolute (a Claude-
	// derived 20000 that made sense at 200k becomes nickel-and-diming on
	// a 1M window and never fires on a 50k one).
	minValvePayoffTokens int  // Minimum aggregate reclaim (tokens) required for ValveEvict to fire
	keepRecentBallast    int  // Newest N ballast tool results ValveEvict never touches
	valveDisabledLogged  bool // Set once the "no durable store" warning has logged (C-022)

	// Fold breaker + flat-history bookkeeping (D-5, O-BRK-1). foldTurnHistory
	// records the authoritative ledger-user-turn count (countLedgerUsers over
	// sm.l1Messages) at each successful fold; breakerTrips derives the
	// consecutive-close-fold streak from it. flatMessageCount mirrors
	// len(session.Messages) — the durable flat history length — without a
	// session handle, incremented by AddMessage/ReplayMessages and set
	// directly by RestoreFold; it is the flatLen source CompactMemory passes
	// to Fold, since CompactMemory's exported signature carries no session
	// parameter.
	foldTurnHistory  []int
	flatMessageCount int

	mu sync.RWMutex
}

// MemoryCompressor defines the interface for LLM-powered memory compression.
// Implementations should compress message history into brief summaries.
type MemoryCompressor interface {
	CompressMessages(ctx context.Context, messages []Message) (string, error)
	IsEnabled() bool
}

// StrictMemoryCompressor is an optional capability a MemoryCompressor may
// implement to expose compression failures instead of silently substituting
// a heuristic fallback. CompressMessages' contract deliberately never
// returns a "degraded" signal (it always yields a usable string with a nil
// error); Fold needs to tell a genuine LLM summary apart from a degraded one
// to satisfy its logged degraded-fallback requirement (O-FLD-4, C-028), so
// it uses CompressMessagesStrict when the configured compressor implements
// this interface, falling back to CompressMessages otherwise.
type StrictMemoryCompressor interface {
	MemoryCompressor
	CompressMessagesStrict(ctx context.Context, messages []Message) (string, error)
}

// NewSegmentedMemory creates a new segmented memory instance with ROM content.
// The ROM content is static documentation/prompts that never change during the session.
//
// Configuration:
// - Token Budget: Configurable context window and output reserve
// - L1 Cache: Last 10 messages (5 exchanges) for focused context
// - Kernel: Max 1 tool result (database-backed optimization)
// - Schema Cache: Max 10 schemas with LRU eviction
//
// If maxContextTokens or reservedOutputTokens are 0, defaults to Claude Sonnet 4.5 values (200K/20K)
func NewSegmentedMemory(romContent string, maxContextTokens, reservedOutputTokens int) *SegmentedMemory {
	// Use balanced profile as default (backwards compatibility)
	balancedProfile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]
	return NewSegmentedMemoryWithCompression(romContent, maxContextTokens, reservedOutputTokens, balancedProfile)
}

// NewSegmentedMemoryWithCompression creates a new segmented memory instance with custom compression profile.
// This allows fine-grained control over compression behavior for different workload types.
//
// Configuration:
// - Token Budget: Configurable context window and output reserve
// - L1 Cache: Configurable based on profile (data_intensive=5, balanced=8, conversational=12)
// - Kernel: Max 1 tool result (database-backed optimization)
// - Schema Cache: Max 10 schemas with LRU eviction
//
// If maxContextTokens or reservedOutputTokens are 0, defaults to Claude Sonnet 4.5 values (200K/20K)
func NewSegmentedMemoryWithCompression(romContent string, maxContextTokens, reservedOutputTokens int, profile CompressionProfile) *SegmentedMemory {
	// Use defaults if not specified (backwards compatibility)
	if maxContextTokens == 0 {
		maxContextTokens = 200000 // Claude Sonnet 4.5 default
	}
	if reservedOutputTokens == 0 {
		reservedOutputTokens = 20000 // 10% of 200K
	}

	// Initialize token counter and budget
	tokenCounter := GetTokenCounter()
	tokenBudget := NewTokenBudget(maxContextTokens, reservedOutputTokens)

	sm := &SegmentedMemory{
		romContent:         romContent,
		tools:              make([]string, 0),
		toolResults:        make([]CachedToolResult, 0),
		schemaCache:        make(map[string]string),
		schemaAccessLog:    make(map[string]time.Time),
		maxSchemas:         10, // Max 10 schemas cached
		l1Messages:         make([]Message, 0),
		sessionStore:       nil,   // Set via SetSessionStore
		sessionID:          "",    // Set via SetSessionStore
		swapEnabled:        false, // Disabled until SetSessionStore is called
		maxL2Tokens:        5000,  // Default L2 eviction threshold
		swapEvictionCount:  0,
		swapRetrievalCount: 0,
		tokenCounter:       tokenCounter,
		tokenBudget:        tokenBudget,
		compressor:         nil,                           // Set via SetCompressor after initialization
		tracer:             observability.NewNoOpTracer(), // Set via SetTracer
		minL1Messages:      profile.MinL1Messages,         // Use profile value (minimum for recency)
		maxToolResults:     5,                             // Keep last 5 tool results in kernel for richer context
		compressionProfile: profile,                       // Store profile for adaptive compression
		yellowPct:          effectiveZonePct(profile.YellowThresholdPercent, 70),
		redPct:             effectiveZonePct(profile.RedThresholdPercent, 85),

		minValvePayoffTokens: maxContextTokens / 10,
		keepRecentBallast:    3,
	}

	// Initialize all per-layer token caches
	sm.fullRecount()
	sm.tokenCountDirty = false
	return sm
}

// SetCompressor sets the memory compressor for intelligent history compression.
// Should be called after agent initialization to avoid dependency cycles.
func (sm *SegmentedMemory) SetCompressor(compressor MemoryCompressor) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.compressor = compressor
}

// SetSharedMemory sets the shared memory store for large data handling.
// When set, large tool results can be stored in shared memory to save context tokens.
func (sm *SegmentedMemory) SetSharedMemory(sharedMemory *storage.SharedMemoryStore) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.sharedMemory = sharedMemory
}

// SetTracer sets the observability tracer for error logging and metrics.
// Should be called after agent initialization to enable proper error reporting.
func (sm *SegmentedMemory) SetTracer(tracer observability.Tracer) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if tracer != nil {
		sm.tracer = tracer

		// Log compression profile configuration for observability.
		// context.Background() is intentional here: SetTracer is a configuration setter called
		// during agent initialization, before any user request context exists.
		sm.tracer.RecordEvent(context.Background(), "memory.profile_configured", map[string]interface{}{
			"profile":                    sm.compressionProfile.Name,
			"max_l1_tokens":              sm.compressionProfile.MaxL1Tokens,
			"min_l1_messages":            sm.compressionProfile.MinL1Messages,
			"warning_threshold_percent":  sm.compressionProfile.WarningThresholdPercent,
			"critical_threshold_percent": sm.compressionProfile.CriticalThresholdPercent,
			"normal_batch_size":          sm.compressionProfile.NormalBatchSize,
			"warning_batch_size":         sm.compressionProfile.WarningBatchSize,
			"critical_batch_size":        sm.compressionProfile.CriticalBatchSize,
		})
	}
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

// SetSessionStore enables the swap layer with database-backed long-term storage.
// When set, L2 summaries will be automatically evicted to swap when exceeding maxL2Tokens.
// This enables "forever conversations" by preventing unbounded L2 growth.
func (sm *SegmentedMemory) SetSessionStore(store SessionStorage, sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.sessionStore = store
	sm.sessionID = sessionID
	sm.swapEnabled = (store != nil && sessionID != "")
}

// SetMaxL2Tokens configures the maximum token count for L2 before eviction to swap.
// Default is 5000 tokens. When L2 exceeds this limit, the entire L2 summary is
// moved to swap storage and L2 is cleared to start fresh.
func (sm *SegmentedMemory) SetMaxL2Tokens(maxTokens int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if maxTokens > 0 {
		sm.maxL2Tokens = maxTokens
	}
}

// IsSwapEnabled returns true if the swap layer is configured and operational.
func (sm *SegmentedMemory) IsSwapEnabled() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.swapEnabled
}

// GetSwapStats returns swap layer statistics.
func (sm *SegmentedMemory) GetSwapStats() (evictions, retrievals int) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.swapEvictionCount, sm.swapRetrievalCount
}

// AddMessage adds a message to the L1 cache. This is pure admission: it never
// compresses or evicts. Pressure relief is handled solely by prepareContext,
// the single-writer pressure pipeline's only mutation entry point, which runs
// before each LLM-bound call.
//
// ctx is accepted for interface symmetry with the rest of the memory API but
// is not otherwise used here.
func (sm *SegmentedMemory) AddMessage(ctx context.Context, msg Message) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.l1Messages = append(sm.l1Messages, msg)
	sm.flatMessageCount++

	// Incremental L1 token update: count only the new message's tokens
	// instead of recounting ALL messages. This avoids O(n) tiktoken calls
	// on every AddMessage.
	msgTokens := 10 + sm.tokenCounter.CountTokens(msg.Content) // 10 = message overhead
	if len(msg.ToolCalls) > 0 {
		msgTokens += sm.tokenCounter.CountTokens(fmt.Sprintf("%v", msg.ToolCalls))
	}
	if msg.ToolResult != nil {
		msgTokens += sm.tokenCounter.CountTokens(fmt.Sprintf("%v", *msg.ToolResult))
	}
	sm.cachedL1Tokens += msgTokens

	// If other layers were dirtied (e.g., schema operations between messages),
	// do a full recount to pick up those changes too.
	if sm.tokenCountDirty {
		sm.fullRecount()
		sm.tokenCountDirty = false
	} else {
		// Fast path: just update the total from cached layer values
		sm.updateTokenCount()
	}

	// ADMIT beat: record the class an item lands in on entry — the usual
	// root-cause site (e.g. content misclassified as evictable ballast). Debug
	// only, zero-cost at info via the Check gate.
	if ce := zap.L().Named(contextLoggerName).Check(zap.DebugLevel, "context.admit"); ce != nil {
		ce.Write(
			zap.String("beat", "admit"),
			zap.String("session", sm.sessionID),
			zap.String("class", messageClass(msg)),
			zap.String("role", msg.Role),
			zap.Int("tokens", msgTokens),
			zap.Int("l1_count_after", len(sm.l1Messages)),
			zap.String("preview", contextPreview(msg.Content)),
		)
	}
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ReplayMessages bulk-loads messages into L1 without per-message compression.
// This MUST be used instead of calling AddMessage in a loop when restoring a
// session from the database. Restore is a pure bulk-load: it never compresses
// or evicts. Pressure relief (if the restored session lands at or above the
// yellow zone) is handled by prepareContext on the next beat, the sole
// mutation entry point after admission.
func (sm *SegmentedMemory) ReplayMessages(ctx context.Context, messages []Message) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if len(messages) == 0 {
		return
	}

	// Bulk-load all messages into L1. No compression runs here — restore is pure.
	sm.l1Messages = append(sm.l1Messages, messages...)
	sm.flatMessageCount += len(messages)
	// fullRecount (not updateTokenCount) — updateTokenCount only refreshes
	// the L1 token cache when l1Dirty is set, which a freshly constructed
	// SegmentedMemory never is, so the bulk-loaded L1 content would silently
	// never be counted. fullRecount unconditionally recomputes it, matching
	// RestoreFold's equivalent bulk-load path.
	sm.fullRecount()
	sm.tokenCountDirty = false
}

// AddToolResult adds a tool execution result to kernel layer.
// Database-backed optimization: Keeps ONLY immediate previous result in memory.
// All historical results should be persisted to database and retrievable via tools.
// Tool results are not part of the reported token budget (O-TOK-1): the LLM
// never receives them directly, so they are tracked here purely for recall
// tools (GetCachedToolResults) and never feed token accounting.
func (sm *SegmentedMemory) AddToolResult(result CachedToolResult) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Database-backed context optimization: Keep only immediate previous result
	// Historical results are available via recall tools
	if sm.maxToolResults == 1 {
		// Simple replace strategy for single-result context
		sm.toolResults = []CachedToolResult{result}
	} else {
		// Legacy sliding window strategy (for backward compatibility)
		sm.toolResults = append(sm.toolResults, result)
		if len(sm.toolResults) > sm.maxToolResults {
			sm.toolResults = sm.toolResults[len(sm.toolResults)-sm.maxToolResults:]
		}
	}
}

// GetCachedToolResults returns a copy of all cached tool results.
func (sm *SegmentedMemory) GetCachedToolResults() []CachedToolResult {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	results := make([]CachedToolResult, len(sm.toolResults))
	copy(results, sm.toolResults)
	return results
}

// CacheSchema stores a discovered schema in kernel layer with LRU eviction.
// When cache exceeds maxSchemas (default: 10), the least recently used schema is evicted.
func (sm *SegmentedMemory) CacheSchema(key, schema string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Add or update schema
	sm.schemaCache[key] = schema
	sm.schemaAccessLog[key] = time.Now()

	// Check if we need to evict (LRU eviction)
	if len(sm.schemaCache) > sm.maxSchemas {
		// Find least recently used schema
		var lruKey string
		var lruTime time.Time
		first := true

		for k, accessTime := range sm.schemaAccessLog {
			if first || accessTime.Before(lruTime) {
				lruKey = k
				lruTime = accessTime
				first = false
			}
		}

		// Evict LRU schema
		if lruKey != "" {
			delete(sm.schemaCache, lruKey)
			delete(sm.schemaAccessLog, lruKey)
			// Note: In production, log eviction event here
		}
	}
}

// GetSchema retrieves a cached schema and updates access time for LRU tracking.
func (sm *SegmentedMemory) GetSchema(key string) (string, bool) {
	// Use write lock from the start to avoid lock upgrade deadlock
	// Lock upgrade pattern (RLock -> unlock -> Lock) is unsafe under concurrent access
	sm.mu.Lock()
	defer sm.mu.Unlock()

	schema, ok := sm.schemaCache[key]
	if !ok {
		return "", false
	}

	// Update access time for LRU tracking
	sm.schemaAccessLog[key] = time.Now()

	return schema, ok
}

// GetMessages returns a copy of L1 (the conversation).
//
// This is the L1 accessor Session.assembleForLLM calls when composing the
// Contract 1 message list for an LLM call. Firing the context.compile beat
// here — rather than from the older GetMessagesForLLM path — keeps the
// observability wiring intact after the assembler moved into Session
// (Session owns ROM composition now, SegmentedMemory owns L1 state).
// The beat is DEBUG-gated in logContextSnapshot; zero cost at info.
func (sm *SegmentedMemory) GetMessages() []Message {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	messages := make([]Message, len(sm.l1Messages))
	copy(messages, sm.l1Messages)
	sm.logContextSnapshot("compile")
	return messages
}

// GetL2Summary returns the L2 summary content for inspection.
// Returns empty string if no compression has occurred yet.
func (sm *SegmentedMemory) GetL2Summary() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.l2Summary
}

// RomBase returns the static base ROM string (identity, guidance,
// protocols) captured at construction. The full ROM emitted to the LLM
// is composed by Session.GetMessages, which concatenates this base with
// the per-session skill catalog (filtered against active skills). The
// split lets SegmentedMemory keep its ROM byte-stable across turns
// without knowing about session-level catalog state.
func (sm *SegmentedMemory) RomBase() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.romContent
}

// RemoveSkillLoadMessage removes the most recent manage_skills(load, name)
// tool_result from L1 for the given skill name. Called by executeUnload
// to keep the L1 view aligned with the orchestrator's active-set: without
// this, orchestrator says "not active" but walk-L1 (ActiveSkillNames)
// still sees the load metadata and hides the skill from the ROM catalog
// — a stale state that breaks the reload path.
//
// Also removes the paired assistant tool_call message that invoked the
// load, so the API-valid tool_use/tool_result invariant holds after
// removal. Returns true when a load message was found and removed.
//
// Safe under concurrent AddMessage: takes the write lock.
func (sm *SegmentedMemory) RemoveSkillLoadMessage(skillName string) bool {
	if skillName == "" {
		return false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Walk newest-first to find the most recent load for this skill.
	for i := len(sm.l1Messages) - 1; i >= 0; i-- {
		msg := sm.l1Messages[i]
		if msg.Role != "tool" || msg.ToolResult == nil {
			continue
		}
		md := msg.ToolResult.Metadata
		if md == nil {
			continue
		}
		action, _ := md["action"].(string)
		if action != "load" {
			continue
		}
		name, _ := md["skill"].(string)
		if name != skillName {
			continue
		}

		// Found the load tool_result at i. Also find its paired assistant
		// message (immediately preceding, or somewhere earlier) that
		// carries the ToolCall with matching ID — remove both to preserve
		// tool_use/tool_result pairing.
		toolUseID := msg.ToolUseID
		pairIdx := -1
		if toolUseID != "" {
			for j := i - 1; j >= 0; j-- {
				am := sm.l1Messages[j]
				if am.Role != "assistant" {
					continue
				}
				for _, tc := range am.ToolCalls {
					if tc.ID == toolUseID {
						pairIdx = j
						break
					}
				}
				if pairIdx >= 0 {
					break
				}
			}
		}

		// Remove tool_result at i.
		sm.l1Messages = append(sm.l1Messages[:i], sm.l1Messages[i+1:]...)
		// Remove paired assistant if it ONLY held this tool_call (would
		// otherwise leave a stranded assistant with an orphaned tool_call).
		// If the assistant had multiple tool_calls, removing the whole
		// assistant would orphan the others; in that case leave it alone
		// and let the API reject-or-strip logic handle downstream.
		if pairIdx >= 0 && pairIdx < len(sm.l1Messages) {
			am := sm.l1Messages[pairIdx]
			if len(am.ToolCalls) == 1 && am.ToolCalls[0].ID == toolUseID {
				sm.l1Messages = append(sm.l1Messages[:pairIdx], sm.l1Messages[pairIdx+1:]...)
			}
		}
		sm.l1Dirty = true
		sm.updateTokenCount()
		return true
	}
	return false
}

// ActiveSkillNames returns the set of skill names whose manage_skills
// load-body is currently resident in L1. Walked structurally — read
// ToolResult.Metadata["action"]=="load" and ["skill"] — not by content
// parsing, so the answer stays correct across:
//
//   - Explicit unload (removes the load message from L1 → not seen here → skill returns to ROM catalog next turn)
//   - Fold at red (narrative-classed load bodies compressed into residue → not in L1 → skill returns to catalog)
//   - Valve at yellow (ballast-only path, never touches loads)
//
// The result feeds Session.assembleForLLM's catalog filter: entries whose
// skill is in this set are hidden from the ROM catalog, so the LLM never
// sees "call load" prompts for skills it has already loaded.
func (sm *SegmentedMemory) ActiveSkillNames() map[string]bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	active := make(map[string]bool)
	for _, msg := range sm.l1Messages {
		if msg.Role != "tool" || msg.ToolResult == nil {
			continue
		}
		md := msg.ToolResult.Metadata
		if md == nil {
			continue
		}
		action, _ := md["action"].(string)
		if action != "load" {
			continue
		}
		name, _ := md["skill"].(string)
		if name != "" {
			active[name] = true
		}
	}
	return active
}

// HasL2Content returns true if L2 summary has content (compression occurred).
func (sm *SegmentedMemory) HasL2Content() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.l2Summary) > 0
}

// compressToL2 summarizes old messages into L2 cache (must hold lock).
// Uses LLM-powered compression if available, falls back to simple extraction.
// If L2 exceeds maxL2Tokens and swap is enabled, evicts L2 to swap storage.
// ctx carries caller context (including RLS user_id) for storage operations.
func (sm *SegmentedMemory) compressToL2(ctx context.Context, messages []Message) {
	if len(messages) == 0 {
		return
	}

	// Try LLM-powered compression if available (with timeout derived from caller ctx)
	var summary string
	if sm.compressor != nil && sm.compressor.IsEnabled() {
		compressCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		compressed, err := sm.compressor.CompressMessages(compressCtx, messages)
		if err == nil && compressed != "" {
			summary = compressed
		} else {
			// Fall back to simple compression on error/timeout
			summary = sm.summarizeMessages(messages)
		}
	} else {
		// No LLM available - use simple compression
		summary = sm.summarizeMessages(messages)
	}

	// Append to L2 summary
	if sm.l2Summary == "" {
		sm.l2Summary = summary
	} else {
		sm.l2Summary += "\n" + summary
	}

	// Check if L2 exceeds token limit and swap is enabled
	if sm.swapEnabled {
		l2Tokens := sm.tokenCounter.CountTokens(sm.l2Summary)
		if l2Tokens > sm.maxL2Tokens {
			// Evict L2 to swap storage
			if err := sm.evictL2ToSwap(ctx); err != nil {
				// Log error but don't crash - continue in degraded mode
				// Use tracer to record error for observability
				if sm.tracer != nil {
					_, span := sm.tracer.StartSpan(ctx, "memory.swap_eviction_failed")
					span.RecordError(err)
					span.SetAttribute("l2_tokens", fmt.Sprintf("%d", l2Tokens))
					span.SetAttribute("max_l2_tokens", fmt.Sprintf("%d", sm.maxL2Tokens))
					sm.tracer.EndSpan(span)
				}
			}
		}
	}
}

// summarizeMessages creates a brief summary of messages (must hold lock).
// Simple heuristic-based compression as fallback.
func (sm *SegmentedMemory) summarizeMessages(messages []Message) string {
	var parts []string

	for _, msg := range messages {
		// Extract key information. User messages must not reach this function
		// under the v5 classification invariant (Part A #1): genuine user turns
		// are tagged ClassLedger at construction and carried whole through fold;
		// only narrative reaches the compressor's degraded fallback. The
		// pre-419 `case "user"` branch truncated the user's active objective
		// to 50 chars (issue #262) — leaving it in place as an unreachable
		// safety net silently masked the classification bug that produced it.
		// Fail loud instead: log a warning if a user-role ever lands here,
		// then treat it as narrative so the residue still holds together.
		switch msg.Role {
		case "user":
			// Invariant violation — a user turn reached the degraded
			// fallback. Something upstream failed to classify a genuine
			// user turn as ledger, or classified a synthetic user-role
			// message as ledger and let it fall through here. Preserve the
			// content (no truncation) so #262 cannot recur silently.
			parts = append(parts, fmt.Sprintf("User: %s", msg.Content))
		case "assistant":
			// Assistant actions
			if sm.containsToolCall(msg) {
				parts = append(parts, "Agent executed tools and provided results")
			} else {
				parts = append(parts, "Agent provided analysis")
			}
		case "tool":
			// Tool results - include summary to preserve tool execution context
			parts = append(parts, "Tool result received")
		case "system":
			// System messages (rare in L1, typically in ROM, but handle defensively)
			parts = append(parts, "System instruction provided")
		}
	}

	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, "; ")
}

// containsToolCall checks if message contains tool execution.
// Adapted for loom's Message type which uses ToolCalls field instead of Metadata.
func (sm *SegmentedMemory) containsToolCall(msg Message) bool {
	return len(msg.ToolCalls) > 0
}

// updateTokenCount calculates actual token usage across all memory layers (must hold lock).
// Uses per-layer caching to avoid expensive tiktoken calls when layers haven't changed.
// ROM tokens are computed once at construction. cachedToolSchemaTokens is recomputed only
// by RecomputeToolSchema, when the registered/effective tool set changes. L1 is recounted
// only when l1Dirty is set (e.g., after compression removes messages).
func (sm *SegmentedMemory) updateTokenCount() {
	// ROM layer — cached at construction, never changes
	// (cachedROMTokens is set in constructor and after full recount)

	// L1 layer — recount only when dirty (compression removed messages)
	if sm.l1Dirty {
		sm.cachedL1Tokens = sm.tokenCounter.EstimateMessagesTokens(sm.l1Messages)
		sm.l1Dirty = false
	}

	// Sum all cached layer values: compiled output (ROM + L2 residue + L1) plus
	// the measured tool-schema cost. No kernel-cache tokens are counted.
	count := sm.cachedROMTokens +
		sm.cachedL2Tokens +
		sm.cachedL1Tokens +
		sm.cachedToolSchemaTokens

	sm.tokenCount = count

	// Update token budget usage
	sm.tokenBudget.Reset()
	sm.tokenBudget.Use(count)
}

// fullRecount forces a complete recalculation of all layer caches (must hold lock).
// Used during initialization and when tokenCountDirty is set by operations that
// change layers without updating their specific cache (backwards compatibility).
// cachedToolSchemaTokens is left untouched here — it is conversation-independent
// and only ever changes via RecomputeToolSchema.
func (sm *SegmentedMemory) fullRecount() {
	sm.cachedROMTokens = sm.tokenCounter.CountTokens(sm.romContent)
	sm.cachedL1Tokens = sm.tokenCounter.EstimateMessagesTokens(sm.l1Messages)
	sm.cachedL2Tokens = sm.tokenCounter.CountTokens(sm.l2Summary)
	sm.l1Dirty = false

	sm.tokenCount = sm.cachedROMTokens +
		sm.cachedL2Tokens +
		sm.cachedL1Tokens +
		sm.cachedToolSchemaTokens

	sm.tokenBudget.Reset()
	sm.tokenBudget.Use(sm.tokenCount)
}

// RecomputeToolSchema measures the tool-schema token cost — Σ
// CountTokens(json(tool.InputSchema())) — for the given live tool set and
// caches it. Call this only when the registered/effective tool set changes
// (register, unregister, skill-exclusion, lazy-tool promotion); a
// fingerprint guard makes a call with an unchanged tool set a cheap no-op,
// so callers may invoke it defensively without paying the marshal+count
// cost when nothing changed. Adding messages never triggers this — it is
// the only thing that recomputes cachedToolSchemaTokens.
func (sm *SegmentedMemory) RecomputeToolSchema(tools []shuttle.Tool) {
	fingerprint := toolSetFingerprint(tools)

	sm.mu.Lock()
	defer sm.mu.Unlock()

	if fingerprint == sm.toolSchemaFingerprint {
		return
	}
	sm.toolSchemaFingerprint = fingerprint

	total := 0
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		schemaJSON, err := json.Marshal(tool.InputSchema())
		if err != nil {
			continue
		}
		total += sm.tokenCounter.CountTokens(string(schemaJSON))
	}

	sm.cachedToolSchemaTokens = total
	sm.updateTokenCount()
}

// toolSetFingerprint builds an order-independent fingerprint of a tool set's
// names, used by RecomputeToolSchema to detect whether the effective tool
// set actually changed since the last measurement.
func toolSetFingerprint(tools []shuttle.Tool) string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		names = append(names, tool.Name())
	}
	sort.Strings(names)
	return strings.Join(names, "\x00")
}

// GetMessagesForLLM builds the message list for the LLM call: ROM (system,
// the only system-role message), then fold residue (a user-role message
// carrying the L2 summary, present only after a fold), then L1 messages —
// in that order, with no other message. This is the single assembler: the
// system prefix is exactly ROM and stays byte-stable across every beat,
// including folds; everything per-iteration-dynamic (residue included) is
// a message, never system. Total function; never mutates; never errors.
func (sm *SegmentedMemory) GetMessagesForLLM() []Message {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	out := []Message{}
	if sm.romContent != "" {
		out = append(out, Message{Role: "system", Content: sm.romContent})
	}
	msgs := sm.l1Messages
	if sm.l2Summary != "" {
		msgs = append([]Message{{Role: "user", Content: "[Prior conversation summary]\n" + sm.l2Summary}}, msgs...)
	}
	sm.logContextSnapshot("compile")
	return append(out, msgs...)
}

// --- Beat-by-beat context observability -------------------------------------
//
// These helpers emit the per-turn context state — what the model receives and
// how the pressure pipeline mutates it — as structured logs on the
// "memory.context" named logger at DEBUG level. They exist for LOCAL testing:
// run with LOG_LEVEL=debug to see every beat in the log file. At the default
// info level the Check gate short-circuits before any work is done, so this is
// zero-cost in integration/production, where Hawk metrics remain the observability
// path. The dump contains raw conversation/skill content, which is another reason
// it must never be enabled outside local testing.

const contextLoggerName = "memory.context"

// maxLogFieldBytes caps individual ROM/L2/L1-item bytes emitted in the
// context.compile beat. The beat runs at DEBUG only, but a shared env with
// LOG_LEVEL=debug would otherwise flood centralized logs (and any PII carried
// in user turns / tool results) at every threshold-crossing event.
const maxLogFieldBytes = 4096

// truncateForLog returns s bounded to maxLogFieldBytes with a byte-count
// suffix, so a truncated field is unmistakable in the log line. Truncates
// on a UTF-8 rune boundary so the log emitter never sees a partial rune
// (which zap's JSON encoder replaces with U+FFFD, silently corrupting a
// downstream analyzer's re-parsing of the log line).
func truncateForLog(s string) string {
	if len(s) <= maxLogFieldBytes {
		return s
	}
	cut := runeSafeCut(s, maxLogFieldBytes)
	return cut + fmt.Sprintf("…[+%d bytes]", len(s)-len(cut))
}

// contextPreview returns a single-line, length-bounded preview of message
// content suitable for a log field. Truncates on a UTF-8 rune boundary
// (see truncateForLog for the rationale).
func contextPreview(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= 140 {
		return s
	}
	return runeSafeCut(s, 140) + "…"
}

// runeSafeCut returns s truncated at or before the maxBytes-th byte, aligned
// to a UTF-8 rune boundary — the returned string never ends mid-rune. If
// maxBytes falls in the middle of a multibyte rune, the entire partial rune
// is dropped (bytes are lost from the tail, never from the head).
func runeSafeCut(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Start at maxBytes and walk back until we land on a rune-start byte.
	// That byte begins the rune that would have been split; we truncate
	// AT that position (excluding the partial rune entirely).
	i := maxBytes
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}

// maxSummaryUserQueryChars caps user message content preserved in compression
// summaries. Generous on purpose: user questions are small, and losing their
// tail loses the objective (issue #262 — a question truncated at 50 chars cut
// `demo_user.telecocustomer` down to a table named "t"). Assistant/tool
// content stays aggressively summarized — it is derivable from tool traces,
// unlike the user's intent.
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

// pressureZone maps a budget-usage percentage (0–100) to the pipeline zone using
// the configured yellow/red thresholds.
func pressureZone(pct float64, yellow, red int) string {
	switch {
	case pct >= float64(red):
		return "RED"
	case pct >= float64(yellow):
		return "YELLOW"
	default:
		return "GREEN"
	}
}

// messageClass returns the retention class of a message, defaulting the zero
// value to "narrative" (see ContextClass).
func messageClass(m Message) string {
	if m.ContextClass == "" {
		return "narrative"
	}
	return m.ContextClass
}

// logContextSnapshot dumps the full L1 context state — every resident item by
// class with a content preview, plus budget/zone — at the given beat.
//
// Precondition: the caller MUST already hold sm.mu (read or write). This reads
// sm.l1Messages/romContent/l2Summary directly and does not lock. TokenBudget and
// TokenCounter use their own independent locks, so calling them here is
// deadlock-safe for both RLock and Lock holders.
func (sm *SegmentedMemory) logContextSnapshot(beat string) {
	ce := zap.L().Named(contextLoggerName).Check(zap.DebugLevel, "context."+beat)
	if ce == nil {
		return // info level or higher: no work performed
	}
	used, available, total := sm.tokenBudget.GetUsage()
	// Compute pressure on the SAME basis the pipeline dispatches valve/fold on:
	// used/total (BudgetPct), NOT UsagePercentage() which divides by
	// (total-reserved) and would read systematically hotter than the value that
	// actually triggered the action — making the logged zone disagree with what
	// valve/fold did. GetUsage returns total = MaxTokens, so used/total == BudgetPct.
	pct := 0.0
	if total > 0 {
		pct = float64(used) / float64(total) * 100
	}
	// Match the pipeline's zone thresholds, including its fallback for a profile
	// that leaves them unset (0 → the 70/85 defaults), so the logged zone agrees
	// with what valve/fold actually act on.
	yellow := sm.compressionProfile.YellowThresholdPercent
	if yellow == 0 {
		yellow = 70
	}
	red := sm.compressionProfile.RedThresholdPercent
	if red == 0 {
		red = 85
	}
	zone := pressureZone(pct, yellow, red)

	tokensByClass := map[string]int{}
	items := make([]string, 0, len(sm.l1Messages))
	for i, m := range sm.l1Messages {
		cls := messageClass(m)
		tok := sm.tokenCounter.CountTokens(m.Content)
		tokensByClass[cls] += tok
		items = append(items, fmt.Sprintf("[%d] class=%s role=%s tok=%d %s",
			i, cls, m.Role, tok, truncateForLog(m.Content)))
	}
	ce.Write(
		zap.String("beat", beat),
		zap.String("session", sm.sessionID),
		zap.Bool("rom_present", sm.romContent != ""),
		zap.Int("rom_tokens", sm.tokenCounter.CountTokens(sm.romContent)),
		zap.String("rom", truncateForLog(sm.romContent)),
		zap.Bool("l2_summary_present", sm.l2Summary != ""),
		zap.String("l2_summary", truncateForLog(sm.l2Summary)),
		zap.Int("l1_count", len(sm.l1Messages)),
		zap.Int("tokens_used", used),
		zap.Int("tokens_available", available),
		zap.Int("tokens_total", total),
		zap.Float64("budget_pct", pct),
		zap.String("zone", zone),
		zap.Any("tokens_by_class", tokensByClass),
		zap.Strings("l1_items", items),
	)
}

// GetTokenCount returns current token count across all memory layers.
func (sm *SegmentedMemory) GetTokenCount() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Recalculate if dirty (lazy evaluation for performance).
	// tokenCountDirty means a layer changed via an operation that didn't update
	// its specific cache — do a full recount for correctness.
	if sm.tokenCountDirty {
		sm.fullRecount()
		sm.tokenCountDirty = false
	}

	return sm.tokenCount
}

// GetTokenBudgetMax returns the total token budget (context window size).
func (sm *SegmentedMemory) GetTokenBudgetMax() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	_, _, total := sm.tokenBudget.GetUsage()
	return total
}

// ResetContext clears the entire context window: L1 messages, L2 summary, schema
// cache, and tool results. ROM, the registered tool set, and the measured
// tool-schema token cost are preserved since they are structural, not
// conversational.
func (sm *SegmentedMemory) ResetContext() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Clear conversational layers
	sm.l1Messages = sm.l1Messages[:0]
	sm.l2Summary = ""

	// Clear caches (they're per-conversation artifacts)
	sm.schemaCache = make(map[string]string)
	sm.schemaAccessLog = make(map[string]time.Time)
	sm.toolResults = sm.toolResults[:0]

	// Reset swap counters
	sm.swapEvictionCount = 0
	sm.swapRetrievalCount = 0

	// Reset fold breaker + flat-history bookkeeping: a full context reset
	// also truncates the durable flat history (ResetSessionContext syncs
	// session.Messages to L1's new, empty length via TrimLastN(0)), and the
	// breaker's close-fold streak should not survive into the fresh context.
	sm.foldTurnHistory = nil
	sm.flatMessageCount = 0

	// Full recount after reset
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
	return len(sm.l1Messages)
}

// ClearL2 clears the L2 summary cache.
func (sm *SegmentedMemory) ClearL2() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.l2Summary = ""
	sm.cachedL2Tokens = 0
	sm.updateTokenCount()
	sm.tokenCountDirty = false
}

// CompactMemory forces compression of all L1 to L2 via Fold (O-SW-4, C-012,
// FR-010) — a thin wrapper, kept at the same exported signature so its
// caller (session_memory_tool.go's "compact" action) keeps working. The
// session-memory-tool route thereby goes through the same red-zone breaker
// as prepareContext's automatic fold: both share sm.foldTurnHistory, so 3
// close folds in a row trip the breaker regardless of which route produced
// them (SC-005).
//
// ledgerUserTurns and flatLen — Fold's cross-check and fold-index inputs —
// are derived from SegmentedMemory's own state (countLedgerUsers over
// sm.l1Messages, sm.flatMessageCount) since CompactMemory has no session
// handle to read len(session.Messages) from directly.
//
// Returns number of messages compressed and tokens saved; (0, 0) if L1 was
// empty or the breaker tripped (logged, never returned as an error — this
// route's caller expects (int, int), not an error).
// ctx carries caller context (including RLS user_id) for storage operations.
func (sm *SegmentedMemory) CompactMemory(ctx context.Context) (int, int) {
	sm.mu.RLock()
	if len(sm.l1Messages) == 0 {
		sm.mu.RUnlock()
		return 0, 0
	}
	ledgerUserTurns := countLedgerUsers(sm.l1Messages)
	flatLen := sm.flatMessageCount
	messagesBefore := len(sm.l1Messages)
	tokensBefore := sm.tokenCount
	sm.mu.RUnlock()

	if err := sm.Fold(ctx, ledgerUserTurns, flatLen); err != nil {
		if sm.tracer != nil {
			sm.tracer.RecordEvent(ctx, "memory.compact_memory.fold_failed", map[string]interface{}{
				"error": err.Error(),
			})
		}
		return 0, 0
	}

	sm.mu.RLock()
	messagesAfter := len(sm.l1Messages)
	tokensAfter := sm.tokenCount
	sm.mu.RUnlock()

	return messagesBefore - messagesAfter, tokensBefore - tokensAfter
}

// logCompressionEvent logs a memory compression event.
// Records metrics to Hawk for observability.
func (sm *SegmentedMemory) logCompressionEvent(messagesCompressed, tokensSaved int) {
	used, available, _ := sm.tokenBudget.GetUsage()
	budgetPct := sm.tokenBudget.UsagePercentage()

	// Determine which batch size was used based on budget percentage
	var batchSizeUsed string
	criticalThreshold := float64(sm.compressionProfile.CriticalThresholdPercent)
	warningThreshold := float64(sm.compressionProfile.WarningThresholdPercent)

	if budgetPct > criticalThreshold {
		batchSizeUsed = "critical"
	} else if budgetPct > warningThreshold {
		batchSizeUsed = "warning"
	} else {
		batchSizeUsed = "normal"
	}

	// Note: In production, replace with structured logging (zap, etc.)
	_ = fmt.Sprintf("memory_compressed: messages=%d tokens_saved=%d l1=%d l2_size=%d tokens=%d used=%d available=%d budget_pct=%.2f profile=%s batch_size=%s",
		messagesCompressed, tokensSaved, len(sm.l1Messages), len(sm.l2Summary),
		sm.tokenCount, used, available, budgetPct, sm.compressionProfile.Name, batchSizeUsed)

	// Record metrics to Hawk for observability
	if sm.tracer != nil {
		labels := map[string]string{
			"profile":           sm.compressionProfile.Name,
			"batch_size":        batchSizeUsed,
			"trigger_threshold": fmt.Sprintf("%.0f%%", budgetPct),
		}

		// Record compression event counter
		sm.tracer.RecordMetric("memory.compression.events", 1, labels)

		// Record messages compressed
		sm.tracer.RecordMetric("memory.compression.messages", float64(messagesCompressed), labels)

		// Record tokens saved
		sm.tracer.RecordMetric("memory.compression.tokens_saved", float64(tokensSaved), labels)

		// Record budget usage at compression time
		sm.tracer.RecordMetric("memory.compression.budget_pct", budgetPct, labels)

		// Record L1 size after compression
		sm.tracer.RecordMetric("memory.l1.size", float64(len(sm.l1Messages)), labels)
	}
}

// GetTokenBudgetUsage returns current token budget usage information.
// Returns: (used, available, total)
func (sm *SegmentedMemory) GetTokenBudgetUsage() (int, int, int) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.tokenBudget.GetUsage()
}

// BudgetPct returns the current token budget usage as a percentage
// (used/total*100) — the single basis the pressure pipeline dispatches on.
func (sm *SegmentedMemory) BudgetPct() float64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	used, _, total := sm.tokenBudget.GetUsage()
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

// ZoneThresholds returns the effective yellow/red budget-pressure thresholds
// as percentages. When the token window/basis is unknown (total tokens == 0),
// it falls back to the legacy compression-profile Warning/Critical
// thresholds — the only surviving use of the profile-threshold path.
func (sm *SegmentedMemory) ZoneThresholds() (yellowPct, redPct float64) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	_, _, total := sm.tokenBudget.GetUsage()
	if total == 0 {
		return float64(sm.compressionProfile.WarningThresholdPercent), float64(sm.compressionProfile.CriticalThresholdPercent)
	}
	return sm.yellowPct, sm.redPct
}

// evictedStubPrefix marks a valve-evicted message's replacement content.
// isStub matches on this prefix to keep an already-evicted candidate out of
// a later valve pass, and to distinguish a stub from genuine ballast content
// during candidate selection.
const evictedStubPrefix = "[evicted: "

// isStub reports whether msg's Content is a valve eviction stub (see
// ValveEvict), rather than the tool result's real content.
func isStub(msg Message) bool {
	return strings.HasPrefix(msg.Content, evictedStubPrefix)
}

// SetMinValvePayoffTokens configures the minimum aggregate token reclaim a
// candidate batch must reach for ValveEvict to fire (default: 20000,
// O-VLV-1/C-021 — eviction is only worth the later recall_context round-trip
// above this bar). Values <= 0 are ignored (default retained).
func (sm *SegmentedMemory) SetMinValvePayoffTokens(tokens int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if tokens > 0 {
		sm.minValvePayoffTokens = tokens
	}
}

// SetKeepRecentBallast configures how many of the newest ballast tool
// results ValveEvict must never evict (default: 3). Negative values are
// ignored (default retained).
func (sm *SegmentedMemory) SetKeepRecentBallast(n int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if n >= 0 {
		sm.keepRecentBallast = n
	}
}

// ValveEvict relieves yellow-zone pressure by evicting low-value context:
// the oldest eligible ballast tool results, replaced in the in-memory
// l1Messages projection with a stub that names recall_context and carries
// the evicted result's ToolUseID as its ref (C-E, O-VLV-1/2).
//
// Candidates are ballast tool results (Role=="tool", ContextClass==
// ClassBallast, ToolUseID != "") that are not already stubs, walked
// oldest→newest and excluding the newest keepRecentBallast ballast items —
// charter, ledger, and narrative messages are never candidates. The valve
// fires only when the aggregate reclaim across the full candidate set (each
// tokened via tokenCounter.CountTokens, not the unpopulated msg.TokenCount)
// reaches minValvePayoffTokens; below that bar it evicts nothing.
//
// Only l1Messages is rewritten — ToolUseID, Role, and ContextClass are left
// intact so the stub remains a valid tool_result and tool_use/tool_result
// pairing holds — the durable messages row is never touched, so the
// original content is always recoverable via recall_context. With no
// durable session store wired, a stub would be unrecoverable, so the valve
// disables itself and logs once instead of evicting (C-022).
func (sm *SegmentedMemory) ValveEvict(ctx context.Context) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// VALVE beat: show what the valve sheds. before runs now; after runs at
	// return (defer LIFO — before Unlock, lock still held).
	sm.logContextSnapshot("valve.before")
	defer sm.logContextSnapshot("valve.after")

	if sm.sessionStore == nil {
		if !sm.valveDisabledLogged {
			sm.valveDisabledLogged = true
			if sm.tracer != nil {
				sm.tracer.RecordEvent(ctx, "memory.valve_disabled_no_store", map[string]interface{}{
					"reason": "no durable session store wired — valve evicts nothing",
				})
			}
		}
		return
	}

	// Collect ballast candidates oldest→newest, excluding existing stubs.
	ballastIdx := make([]int, 0, len(sm.l1Messages))
	for i, msg := range sm.l1Messages {
		if msg.Role == "tool" && msg.ContextClass == ClassBallast && msg.ToolUseID != "" && !isStub(msg) {
			ballastIdx = append(ballastIdx, i)
		}
	}

	// Exclude the newest keepRecentBallast ballast items.
	if len(ballastIdx) <= sm.keepRecentBallast {
		return
	}
	candidateIdx := ballastIdx[:len(ballastIdx)-sm.keepRecentBallast]

	// Payoff bar: token each candidate and accumulate oldest-first. Only
	// evict the batch if the aggregate reclaim reaches minValvePayoffTokens.
	tokensByIdx := make(map[int]int, len(candidateIdx))
	totalReclaim := 0
	for _, i := range candidateIdx {
		tok := sm.tokenCounter.CountTokens(sm.l1Messages[i].Content)
		tokensByIdx[i] = tok
		totalReclaim += tok
	}
	if totalReclaim < sm.minValvePayoffTokens {
		return
	}

	// Evict: replace each candidate's Content with a stub, in-memory only.
	for _, i := range candidateIdx {
		msg := &sm.l1Messages[i]
		toolName := precedingToolCallName(sm.l1Messages, i)
		msg.Content = fmt.Sprintf("%s%s result, %d tok → recall_context('%s')]", evictedStubPrefix, toolName, tokensByIdx[i], msg.ToolUseID)
	}

	sm.l1Dirty = true
	sm.updateTokenCount()
}

// breakerLedgerTurnWindow is the ledger-user-turn proximity threshold the
// fold breaker trips on (O-BRK-1, C-024): a fold attempted within this many
// ledger-user turns of the previous fold counts as "close" to it.
const breakerLedgerTurnWindow = 3

// breakerTripThreshold is the number of consecutive close-together folds
// that trips the breaker — the third close-in-a-row fold is refused (not
// folded), matching the spec's "3 folds within 3 ledger-user turns."
const breakerTripThreshold = 3

// foldRecord is the persisted {residue, foldIndex} envelope Fold writes to
// the l2_summary snapshot row (O-FLD-5, C-029) and restore parses back
// (Seam 3) to reproduce Fold's L1 projection without ever persisting the
// carry set itself.
type foldRecord struct {
	Residue   string `json:"residue"`
	FoldIndex int    `json:"foldIndex"`
}

// Fold folds the conversation ledger to relieve red-zone pressure
// (O-FLD-1..5). Runs entirely inside prepareContext at red, holding
// sm.mu.Lock() for its duration.
//
// ledgerUserTurns is the caller's ledger-user-turn count (userLedgerCount) —
// a cross-check against the count Fold derives authoritatively from
// sm.l1Messages (logged on mismatch, never used for breaker logic, since
// sm.l1Messages always retains 100% of ledger-class messages verbatim across
// every prior fold, making it a monotonic count for the whole session).
// flatLen (= len(session.Messages) at the beat) is the fold-index source:
// the pre-fold transcript is flat[:flatLen] — the durable messages rows,
// recoverable via recall_context('fold:<flatLen>') — no separate copy is
// written here.
//
// Order: breaker check first (O-BRK-1) — do not fold if it trips; partition
// sm.l1Messages into the carry set (all charter + all ledger + each ledger
// user message's immediately-preceding assistant + tool-pair closure) and
// the remainder (O-FLD-2); drop remaining ballast unconditionally — valve
// eviction with no payoff bar (O-FLD-3); compress remaining narrative into
// the residue in one LLM pass, heuristic fallback on compressor error or
// when disabled, logged degraded (O-FLD-4); swap L1 to the carry set
// (O-FLD-2); persist {residue, foldIndex} — the carry set itself is never
// persisted, it is recomputed from flat[:foldIndex] on restore (O-FLD-5).
//
// Returns a *RecoverableError (action reset_context) when the breaker trips.
func (sm *SegmentedMemory) Fold(ctx context.Context, ledgerUserTurns, flatLen int) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// FOLD beat: the one lossy event — show before→after (kept verbatim vs
	// summarized). before runs now; after runs at return (defer LIFO — before
	// Unlock, lock still held).
	sm.logContextSnapshot("fold.before")
	defer sm.logContextSnapshot("fold.after")

	// --- 1. Breaker FIRST (O-BRK-1, C-024) ---
	currentLedgerTurns := countLedgerUsers(sm.l1Messages)
	if ledgerUserTurns != currentLedgerTurns && sm.tracer != nil {
		sm.tracer.RecordEvent(ctx, "memory.fold.ledger_turn_mismatch", map[string]interface{}{
			"passed_ledger_user_turns":        ledgerUserTurns,
			"authoritative_ledger_user_turns": currentLedgerTurns,
		})
	}
	if sm.breakerTrips(currentLedgerTurns) {
		if sm.tracer != nil {
			sm.tracer.RecordEvent(ctx, "memory.fold.breaker_tripped", map[string]interface{}{
				"ledger_user_turns": currentLedgerTurns,
			})
		}
		return sm.breakerError()
	}
	sm.foldTurnHistory = append(sm.foldTurnHistory, currentLedgerTurns)

	// --- 2. Pre-fold transcript = flat[:foldIndex] (O-FLD-1, C-025) ---
	// No separate copy is written: the pre-fold transcript IS the durable
	// messages rows [:foldIndex], recoverable via recall_context.
	foldIndex := flatLen

	// --- 3. Partition with carry closure (O-FLD-2, C-026/027) ---
	include := computeCarryInclude(sm.l1Messages)
	carry := make([]Message, 0, len(sm.l1Messages))
	var narrativeMsgs []Message
	for i, msg := range sm.l1Messages {
		if include[i] {
			carry = append(carry, msg)
			continue
		}

		// --- 4. Remaining ballast -> valve path, no payoff bar (O-FLD-3, C-026) ---
		// Unlike ValveEvict's yellow-zone policy (recency exception,
		// payoff-gated, stub-in-place), a fold drops every remaining
		// ballast item unconditionally — it is not carried into the new L1
		// either way, and the whole pre-fold transcript stays recoverable
		// via the single fold-scope recall pointer, so no per-item stub is
		// needed.
		if msg.ContextClass == ClassBallast {
			continue
		}

		narrativeMsgs = append(narrativeMsgs, msg)
	}

	// --- 5. Residue: one LLM pass (O-FLD-4, C-028) ---
	var residue string
	if len(narrativeMsgs) > 0 {
		degraded := false
		if sm.compressor != nil && sm.compressor.IsEnabled() {
			compressCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			var compressed string
			var err error
			if strict, ok := sm.compressor.(StrictMemoryCompressor); ok {
				compressed, err = strict.CompressMessagesStrict(compressCtx, narrativeMsgs)
			} else {
				compressed, err = sm.compressor.CompressMessages(compressCtx, narrativeMsgs)
			}
			cancel()
			if err == nil && compressed != "" {
				residue = compressed
			} else {
				residue = sm.summarizeMessages(narrativeMsgs)
				degraded = true
			}
		} else {
			residue = sm.summarizeMessages(narrativeMsgs)
			degraded = true
		}
		if degraded && sm.tracer != nil {
			sm.tracer.RecordEvent(ctx, "memory.fold.residue_degraded_fallback", map[string]interface{}{
				"narrative_message_count": len(narrativeMsgs),
			})
		}
	}

	foldPointer := fmt.Sprintf("Full pre-fold transcript: recall_context('fold:%d')", foldIndex)
	if residue == "" {
		residue = foldPointer
	} else {
		residue = residue + "\n" + foldPointer
	}

	// --- 6. Swap L1 (O-FLD-2) ---
	tokensBefore := sm.tokenCount
	messagesCompressed := len(sm.l1Messages) - len(carry)
	sm.l1Messages = carry
	sm.l2Summary = residue
	sm.fullRecount()
	sm.tokenCountDirty = false
	sm.logCompressionEvent(messagesCompressed, tokensBefore-sm.tokenCount)

	// --- 7. Persist {residue, foldIndex} (O-FLD-5, C-029) — carry is NOT persisted ---
	// This is Fold's swap eviction: the residue is durably written to swap
	// storage as part of every fold (unlike the legacy continuous-L2 writer
	// evictL2ToSwap, which is threshold-gated and writes plain text — Fold
	// reuses neither its gate nor its wire format, since a plain-text row
	// would collide with the JSON {residue, foldIndex} envelope restore
	// parses). swapEvictionCount is incremented here so GetSwapStats()
	// reflects fold-driven evictions the same way it reflects
	// evictL2ToSwap's.
	if sm.sessionStore != nil {
		content, err := json.Marshal(foldRecord{Residue: residue, FoldIndex: foldIndex})
		if err != nil {
			if sm.tracer != nil {
				sm.tracer.RecordEvent(ctx, "memory.fold.persist_marshal_failed", map[string]interface{}{"error": err.Error()})
			}
		} else {
			tokenCount := sm.tokenCounter.CountTokens(residue)
			persistCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			saveErr := sm.sessionStore.SaveMemorySnapshot(persistCtx, sm.sessionID, "l2_summary", string(content), tokenCount)
			cancel()
			if saveErr != nil {
				if sm.tracer != nil {
					sm.tracer.RecordEvent(ctx, "memory.fold.persist_failed", map[string]interface{}{"error": saveErr.Error()})
				}
			} else {
				sm.swapEvictionCount++
			}
		}
	}

	return nil
}

// breakerTrips reports whether attempting a fold at currentLedgerTurns would
// be the third consecutive fold within breakerLedgerTurnWindow ledger-user
// turns of its own predecessor (O-BRK-1, C-024). Must be called with sm.mu
// held.
func (sm *SegmentedMemory) breakerTrips(currentLedgerTurns int) bool {
	n := len(sm.foldTurnHistory)
	if n == 0 {
		return false
	}
	if currentLedgerTurns-sm.foldTurnHistory[n-1] >= breakerLedgerTurnWindow {
		return false
	}

	// This attempt is close to the previous fold. Count how many prior
	// folds, walking backward, were each also close to their own
	// predecessor — a consecutive streak of close-together folds.
	streak := 1
	for i := n - 1; i > 0; i-- {
		if sm.foldTurnHistory[i]-sm.foldTurnHistory[i-1] < breakerLedgerTurnWindow {
			streak++
		} else {
			break
		}
	}
	return streak >= breakerTripThreshold
}

// breakerError builds the RecoverableError the breaker returns when tripped
// (O-BRK-1): action reset_context, reusing the RecoverableError shape
// recovery.go declares for Tier 3 self-healing.
func (sm *SegmentedMemory) breakerError() *RecoverableError {
	return &RecoverableError{
		ErrorType:      "token_budget_exceeded",
		Message:        "fold breaker tripped: a third fold within 3 ledger-user turns of the previous fold",
		RecoveryAction: "reset_context",
		RecoveryPayload: map[string]any{
			"fold_turn_history": append([]int(nil), sm.foldTurnHistory...),
		},
		Retryable: true,
	}
}

// countLedgerUsers counts ledger-class user messages in messages — the
// shared basis for userLedgerCount (over the flat session history) and
// Fold's breaker (over sm.l1Messages).
func countLedgerUsers(messages []Message) int {
	count := 0
	for _, m := range messages {
		if m.Role == "user" && m.ContextClass == ClassLedger {
			count++
		}
	}
	return count
}

// computeCarryInclude marks, for each message in flat (original order),
// whether it belongs to the fold carry set: all charter + all ledger + each
// ledger user message's immediately-preceding assistant message (adjacency)
// + tool-pair closure (any carried tool_result pulls its paired assistant
// and that assistant's sibling tool_results; any carried assistant with tool
// calls pulls all its tool_results). Closure runs to a fixed point since it
// can pull in assistants whose own tool_results must then be included too.
//
// Shared by Fold (partitioning sm.l1Messages) and restore's recompute_carry
// (partitioning flat[:foldIndex]) — deterministic over persisted
// class+structure, so both reproduce the identical carry set.
func computeCarryInclude(flat []Message) []bool {
	include := make([]bool, len(flat))
	if len(flat) == 0 {
		return include
	}

	// Pass 1: all charter + all ledger + ledger-user adjacency (the
	// immediately preceding assistant message, if any).
	for i, msg := range flat {
		switch msg.ContextClass {
		case ClassCharter, ClassLedger:
			include[i] = true
		}
		if msg.Role == "user" && msg.ContextClass == ClassLedger && i > 0 && flat[i-1].Role == "assistant" {
			include[i-1] = true
		}
	}

	// Map each tool_result to the assistant that issued it (by ToolUseID
	// matched against that assistant's ToolCalls) and each assistant to all
	// of its tool_results.
	assistantOf := make(map[string]int, len(flat))
	resultsOf := make(map[int][]int, len(flat))
	for i, msg := range flat {
		if msg.Role != "tool" || msg.ToolUseID == "" {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			if flat[j].Role != "assistant" {
				continue
			}
			for _, tc := range flat[j].ToolCalls {
				if tc.ID == msg.ToolUseID {
					assistantOf[msg.ToolUseID] = j
					resultsOf[j] = append(resultsOf[j], i)
				}
			}
			break
		}
	}

	// Pass 2: tool-pair closure to a fixed point (C-027 — guarantees no
	// orphaned tool_result and no assistant left without its tool_results,
	// which is the exact property API validity needs).
	for changed := true; changed; {
		changed = false
		for i, msg := range flat {
			if !include[i] {
				continue
			}
			if msg.Role == "tool" && msg.ToolUseID != "" {
				if aIdx, ok := assistantOf[msg.ToolUseID]; ok {
					if !include[aIdx] {
						include[aIdx] = true
						changed = true
					}
					for _, sibling := range resultsOf[aIdx] {
						if !include[sibling] {
							include[sibling] = true
							changed = true
						}
					}
				}
			}
			if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
				for _, r := range resultsOf[i] {
					if !include[r] {
						include[r] = true
						changed = true
					}
				}
			}
		}
	}

	// Safety net: structural classification alone (charter/ledger/adjacency/
	// closure) can yield nothing to carry — e.g. flat contains no
	// charter- or ledger-classed message at all. An empty carry would leave
	// downstream consumers (GetMessagesForLLM, the LLM API call) with zero
	// user-role messages, violating "every fold produces an API-valid
	// carried sequence" (SC-004). Fall back to retaining the last user-role
	// message and everything after it — the same floor the pre-fold
	// CompactMemory guaranteed.
	hasCarry := false
	for _, v := range include {
		if v {
			hasCarry = true
			break
		}
	}
	if !hasCarry {
		lastUserIdx := -1
		for i := len(flat) - 1; i >= 0; i-- {
			if flat[i].Role == "user" {
				lastUserIdx = i
				break
			}
		}
		if lastUserIdx >= 0 {
			for i := lastUserIdx; i < len(flat); i++ {
				include[i] = true
			}
		}
	}

	return include
}

// computeCarry returns the fold carry set for flat, in original order — the
// same partition Fold uses to build post-fold L1 (recompute_carry, Seam 3),
// reused by restore to reproduce the fold-time carry deterministically from
// the persisted foldIndex without persisting the carry set itself.
func computeCarry(flat []Message) []Message {
	include := computeCarryInclude(flat)
	carry := make([]Message, 0, len(flat))
	for i, msg := range flat {
		if include[i] {
			carry = append(carry, msg)
		}
	}
	return carry
}

// RestoreFold bulk-loads a fold-aware restore (O-RST-1/2): l2Summary is set
// to the persisted fold residue and l1Messages to the caller-supplied carry
// set plus post-fold tail (recompute_carry(flat[:foldIndex]) +
// flat[foldIndex:]) — the exact L1 projection Fold left in place before the
// restart. flatLen is the true durable message count (len(session.Messages)
// at restore), recorded so a later CompactMemory->Fold call (which has no
// session handle) has an accurate flatLen source.
//
// Pure bulk-load: never compresses, never evicts — no pressure at restore
// (O-RST-1); the first beat's prepareContext evaluates zones normally.
func (sm *SegmentedMemory) RestoreFold(ctx context.Context, residue string, l1 []Message, flatLen int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.l2Summary = residue
	if len(l1) > 0 {
		sm.l1Messages = append(sm.l1Messages, l1...)
	}
	sm.flatMessageCount = flatLen

	sm.fullRecount()
	sm.tokenCountDirty = false
}

// effectiveZonePct returns pct if it's a valid non-zero percentage, else def.
// Guards SegmentedMemory zone-threshold fields against unset (zero-value)
// CompressionProfile inputs, preserving the documented 70/85 defaults.
func effectiveZonePct(pct int, def float64) float64 {
	if pct <= 0 {
		return def
	}
	return float64(pct)
}

// GetMemoryStats returns comprehensive memory statistics.
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
		"l1_message_count":   len(sm.l1Messages),
		"l1_max_tokens":      sm.compressionProfile.MaxL1Tokens,
		"l1_min_messages":    sm.minL1Messages,
		"l2_summary_length":  len(sm.l2Summary),
		"tool_result_count":  len(sm.toolResults),
		"tool_result_max":    sm.maxToolResults,
		"schema_cache_count": len(sm.schemaCache),
		"schema_cache_max":   sm.maxSchemas,
		"rom_token_count":    sm.cachedROMTokens,
		"kernel_token_count": sm.cachedToolSchemaTokens,
		"l1_token_count":     sm.cachedL1Tokens,
		"l2_token_count":     sm.cachedL2Tokens,
		"budget_warning":     sm.getBudgetWarning(),
	}
}

// getBudgetWarning returns a warning message if budget usage is high (must hold lock).
// Uses compression profile thresholds to determine warning levels.
func (sm *SegmentedMemory) getBudgetWarning() string {
	usage := sm.tokenBudget.UsagePercentage()
	criticalThreshold := float64(sm.compressionProfile.CriticalThresholdPercent)
	warningThreshold := float64(sm.compressionProfile.WarningThresholdPercent)

	// Critical: 10% above critical threshold
	if usage > criticalThreshold+10.0 {
		return fmt.Sprintf("CRITICAL: Token budget >%.0f%% - aggressive compression active", criticalThreshold+10.0)
	} else if usage > criticalThreshold {
		return fmt.Sprintf("WARNING: Token budget >%.0f%% - compression active", criticalThreshold)
	} else if usage > warningThreshold {
		return fmt.Sprintf("INFO: Token budget >%.0f%% - monitoring", warningThreshold)
	}
	return ""
}

// evictL2ToSwap moves the entire L2 summary to swap storage (must hold lock).
// Called automatically when L2 exceeds maxL2Tokens.
// The L2 summary is saved as a snapshot in the database, then L2 is cleared.
// ctx carries caller context (including RLS user_id) for storage operations.
func (sm *SegmentedMemory) evictL2ToSwap(ctx context.Context) error {
	if !sm.swapEnabled || sm.l2Summary == "" {
		return nil
	}

	// Calculate token count
	tokenCount := sm.tokenCounter.CountTokens(sm.l2Summary)

	// Save L2 snapshot to database with timeout derived from caller ctx
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	err := sm.sessionStore.SaveMemorySnapshot(ctx, sm.sessionID, "l2_summary", sm.l2Summary, tokenCount)
	if err != nil {
		return fmt.Errorf("failed to evict L2 to swap: %w", err)
	}

	// Clear L2 to start fresh
	sm.l2Summary = ""
	sm.swapEvictionCount++

	return nil
}

// RetrieveMessagesFromSwap retrieves old messages from database.
// Returns messages in chronological order (oldest first).
// Offset and limit control pagination (offset=0, limit=10 gets first 10 messages).
func (sm *SegmentedMemory) RetrieveMessagesFromSwap(ctx context.Context, offset, limit int) ([]Message, error) {
	sm.mu.RLock()
	if !sm.swapEnabled {
		sm.mu.RUnlock()
		return nil, fmt.Errorf("swap layer not enabled")
	}
	sessionID := sm.sessionID
	store := sm.sessionStore
	sm.mu.RUnlock()

	// Load full message history from database
	// SessionStore.LoadMessages returns messages in chronological order
	allMessages, err := store.LoadMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve messages from swap: %w", err)
	}

	// Apply offset and limit
	if offset >= len(allMessages) {
		return []Message{}, nil
	}

	end := offset + limit
	if end > len(allMessages) {
		end = len(allMessages)
	}

	messages := allMessages[offset:end]

	// Update statistics
	sm.mu.Lock()
	sm.swapRetrievalCount++
	sm.mu.Unlock()

	return messages, nil
}

// RetrieveL2Snapshots retrieves old L2 summary snapshots from swap.
// Returns snapshots in chronological order (oldest first).
// Limit controls maximum snapshots to return (0 = all).
func (sm *SegmentedMemory) RetrieveL2Snapshots(ctx context.Context, limit int) ([]string, error) {
	sm.mu.RLock()
	if !sm.swapEnabled {
		sm.mu.RUnlock()
		return nil, fmt.Errorf("swap layer not enabled")
	}
	sessionID := sm.sessionID
	store := sm.sessionStore
	sm.mu.RUnlock()

	// Load snapshots from database
	snapshots, err := store.LoadMemorySnapshots(ctx, sessionID, "l2_summary", limit)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve L2 snapshots: %w", err)
	}

	// Extract content strings
	// Extract summary text from each snapshot. Fold persists snapshots as a
	// {residue, foldIndex} JSON envelope; consumers want the residue string
	// (the actual summary), not the wire format. Legacy plain-text snapshots
	// written before the envelope existed fall through unchanged.
	contents := make([]string, len(snapshots))
	for i, snapshot := range snapshots {
		var rec foldRecord
		if err := json.Unmarshal([]byte(snapshot.Content), &rec); err == nil && rec.Residue != "" {
			contents[i] = rec.Residue
		} else {
			contents[i] = snapshot.Content
		}
	}

	// Update statistics
	sm.mu.Lock()
	sm.swapRetrievalCount++
	sm.mu.Unlock()

	return contents, nil
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

	if !sm.swapEnabled {
		return nil, fmt.Errorf("semantic search requires swap layer to be enabled")
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
// Algorithm:
// 1. Format candidates with message previews (200 chars each)
// 2. Ask LLM to score each message 0-10 for relevance to query
// 3. Parse JSON response and reorder by score
// 4. Return top-N most relevant
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

// TrimLastN removes the last N messages from L1, respecting tool_use/tool_result
// pair boundaries. If removing a message would orphan a tool result (or leave an
// assistant message without its subsequent tool results), the boundary is expanded
// to include the full pair. Returns the actual number of messages removed.
func (sm *SegmentedMemory) TrimLastN(n int) int {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if n <= 0 || len(sm.l1Messages) == 0 {
		return 0
	}

	// Clamp to available messages.
	if n > len(sm.l1Messages) {
		n = len(sm.l1Messages)
	}

	// Walk backward to find the cut point, expanding to respect pair boundaries.
	cutIdx := len(sm.l1Messages) - n

	// If the cut lands on a "tool" message, walk backward to include its
	// preceding assistant message (which holds the tool_use blocks).
	for cutIdx > 0 && sm.l1Messages[cutIdx].Role == "tool" {
		cutIdx--
	}

	removed := len(sm.l1Messages) - cutIdx
	sm.l1Messages = sm.l1Messages[:cutIdx]

	sm.fullRecount()
	sm.tokenCountDirty = false

	return removed
}

// AggressiveTrim keeps only the last keepLastN messages in L1 and clears L2
// summaries entirely. Used as a last-resort recovery when token budget is
// critically exceeded even after normal compression. Returns before and after
// token counts for observability.
func (sm *SegmentedMemory) AggressiveTrim(keepLastN int) (beforeTokens, afterTokens int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	beforeTokens = sm.tokenCount

	if keepLastN < 0 {
		keepLastN = 0
	}

	// Trim L1 to last keepLastN messages, respecting pair boundaries.
	if len(sm.l1Messages) > keepLastN {
		cutIdx := len(sm.l1Messages) - keepLastN
		// Expand forward if we'd cut into a tool-result block.
		for cutIdx < len(sm.l1Messages) && sm.l1Messages[cutIdx].Role == "tool" {
			cutIdx++
		}
		if cutIdx >= len(sm.l1Messages) {
			// Edge case: all remaining messages are tool results; keep them all.
			cutIdx = 0
		}
		remaining := make([]Message, len(sm.l1Messages[cutIdx:]))
		copy(remaining, sm.l1Messages[cutIdx:])
		sm.l1Messages = remaining
	}

	// Clear L2 entirely.
	sm.l2Summary = ""

	sm.fullRecount()
	sm.tokenCountDirty = false
	afterTokens = sm.tokenCount

	return beforeTokens, afterTokens
}

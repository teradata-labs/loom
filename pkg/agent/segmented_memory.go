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
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage"
	"github.com/teradata-labs/loom/pkg/types"
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

// Finding represents a structured piece of information discovered during analysis.
// Findings are stored in the Kernel layer to provide working memory for agents,
// preventing hallucination by maintaining verified facts from tool executions.
type Finding struct {
	Path      string      `json:"path"`      // Hierarchical key: "table.statistics.row_count"
	Value     interface{} `json:"value"`     // The actual data: numbers, strings, arrays, objects
	Category  string      `json:"category"`  // Type: "statistic", "schema", "observation", "distribution"
	Note      string      `json:"note"`      // Optional explanation/context
	Timestamp time.Time   `json:"timestamp"` // When recorded
	Source    string      `json:"source"`    // Which tool_call_id produced this (optional)
}

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
	findingsCache   map[string]Finding   // Verified findings from tool executions (working memory)
	maxFindings     int                  // Maximum findings to cache (default: 100)

	// L1 Cache (hot - recent messages)
	l1Messages []Message // Last N messages (configurable, default: 10)

	// L2 Cache (warm - summarized history)
	l2Summary string // Compressed summary of older conversation

	// Swap Layer (cold - database-backed long-term storage)
	sessionStore       *SessionStore // Database for persistent storage (optional)
	sessionID          string        // Session identifier for swap operations
	swapEnabled        bool          // Whether swap layer is configured
	maxL2Tokens        int           // Maximum tokens in L2 before eviction to swap (default: 5000)
	swapEvictionCount  int           // Number of L2 evictions to swap (statistics)
	swapRetrievalCount int           // Number of retrievals from swap (statistics)
	promotedContext    []Message     // Messages retrieved from swap and promoted to context

	// Token management
	tokenCounter    *TokenCounter // Accurate token counting
	tokenBudget     *TokenBudget  // Token budget enforcement
	tokenCount      int           // Actual token count of current context
	tokenCountDirty bool          // Whether token count needs recalculation

	// Memory compression
	compressor MemoryCompressor // LLM-powered compression (optional)

	// Shared memory for large data
	sharedMemory *storage.SharedMemoryStore // Shared memory store for large tool results (optional)

	// Observability
	tracer observability.Tracer // Tracer for error logging and metrics (optional)

	// Semantic search
	llmProvider LLMProvider // For reranking search results (optional)

	// Pattern injection (optional)
	patternContent string // Formatted pattern content for LLM context
	patternName    string // Pattern name for tracking

	// Configuration
	maxL1Messages      int                // Max messages in L1 before compression (adaptive)
	minL1Messages      int                // Minimum messages to keep in L1
	maxToolResults     int                // Max tool results to keep in kernel (default: 1 for database-backed)
	compressionProfile CompressionProfile // Compression behavior profile (thresholds, batch sizes)

	mu sync.RWMutex
}

// MemoryCompressor defines the interface for LLM-powered memory compression.
// Implementations should compress message history into brief summaries.
type MemoryCompressor interface {
	CompressMessages(ctx context.Context, messages []Message) (string, error)
	IsEnabled() bool
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
		maxSchemas:         10,                       // Max 10 schemas cached
		findingsCache:      make(map[string]Finding), // Working memory for verified findings
		maxFindings:        50,                       // Max 50 findings cached (LRU eviction)
		l1Messages:         make([]Message, 0),
		promotedContext:    make([]Message, 0),
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
		maxL1Messages:      profile.MaxL1Messages,         // Use profile value
		minL1Messages:      profile.MinL1Messages,         // Use profile value
		maxToolResults:     1,                             // Database-backed: keep only immediate previous result
		compressionProfile: profile,                       // Store profile for adaptive compression
	}

	// Initialize token count with ROM layer
	sm.updateTokenCount()
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

		// Log compression profile configuration for observability
		sm.tracer.RecordEvent(context.Background(), "memory.profile_configured", map[string]interface{}{
			"profile":                    sm.compressionProfile.Name,
			"max_l1_messages":            sm.compressionProfile.MaxL1Messages,
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
func (sm *SegmentedMemory) SetSessionStore(store *SessionStore, sessionID string) {
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

// AddMessage adds a message to L1 cache with adaptive compression.
// Compression triggers based on two criteria:
// 1. L1 at max capacity (hard limit)
// 2. Token budget exceeds profile's warning threshold (soft limit)
//
// Compression strategy is profile-dependent:
// - data_intensive: warning=50%, critical=70%, batches=2/4/6
// - balanced: warning=60%, critical=75%, batches=3/5/7
// - conversational: warning=70%, critical=85%, batches=4/6/8
func (sm *SegmentedMemory) AddMessage(msg Message) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.l1Messages = append(sm.l1Messages, msg)

	// Recalculate token count if dirty (from schema operations) or after adding message
	// This ensures accurate budget tracking for compression decisions
	sm.updateTokenCount()
	sm.tokenCountDirty = false

	// Check if we need to compress based on two criteria:
	// 1. L1 is at max capacity (hard limit)
	// 2. Token budget exceeds warning threshold (profile-dependent)
	budgetUsage := sm.tokenBudget.UsagePercentage()
	warningThreshold := float64(sm.compressionProfile.WarningThresholdPercent)
	shouldCompress := len(sm.l1Messages) > sm.maxL1Messages || budgetUsage > warningThreshold

	if shouldCompress && len(sm.l1Messages) > sm.minL1Messages {
		// Adaptive compression: compress more aggressively if budget exceeds critical threshold
		var toCompressCount int
		criticalThreshold := float64(sm.compressionProfile.CriticalThresholdPercent)
		if budgetUsage > criticalThreshold {
			// Critical: use critical batch size (aggressive compression)
			toCompressCount = min(sm.compressionProfile.CriticalBatchSize, len(sm.l1Messages)-sm.minL1Messages)
		} else if budgetUsage > warningThreshold {
			// Warning: use warning batch size
			toCompressCount = min(sm.compressionProfile.WarningBatchSize, len(sm.l1Messages)-sm.minL1Messages)
		} else {
			// Normal: use normal batch size
			toCompressCount = min(sm.compressionProfile.NormalBatchSize, len(sm.l1Messages)-sm.minL1Messages)
		}

		if toCompressCount > 0 {
			// CRITICAL: Ensure tool_use/tool_result pairs stay together
			// If compression boundary splits a tool pair, adjust to keep them together
			toCompressCount = sm.adjustCompressionBoundary(toCompressCount)

			// Track token count before compression
			tokensBefore := sm.tokenCount

			// Compress oldest messages to L2
			toCompress := sm.l1Messages[:toCompressCount]
			sm.l1Messages = sm.l1Messages[toCompressCount:]

			sm.compressToL2(toCompress)
			sm.updateTokenCount()
			sm.tokenCountDirty = false

			// Log compression event with token savings
			tokensSaved := tokensBefore - sm.tokenCount
			sm.logCompressionEvent(len(toCompress), tokensSaved)
		}
	}
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// adjustCompressionBoundary ensures tool_use/tool_result pairs stay together.
// Returns adjusted compression count that doesn't split tool pairs.
// Must hold lock when calling this method.
//
// CRITICAL: Tool pairs must NEVER be compressed because compression converts them to text summaries,
// losing the tool_use/tool_result block structure that Bedrock/Anthropic API requires.
// Instead, we EXCLUDE tool pairs from compression entirely.
func (sm *SegmentedMemory) adjustCompressionBoundary(toCompressCount int) int {
	if toCompressCount >= len(sm.l1Messages) {
		return toCompressCount
	}

	// Check if we're splitting a tool_use/tool_result pair
	// The compression boundary should be after a complete exchange:
	// 1. User message
	// 2. Assistant message (possibly with tool_use blocks)
	// 3. Tool result messages (role=tool with tool_use_id)
	//
	// If the message at boundary-1 is an assistant with tool_calls,
	// and the message at boundary is a tool result (role=tool),
	// we need to EXCLUDE the assistant from compression to keep the pair in L1.

	boundaryIdx := toCompressCount

	// Check if previous message is assistant with tool calls
	if boundaryIdx > 0 && boundaryIdx < len(sm.l1Messages) {
		prevMsg := sm.l1Messages[boundaryIdx-1]
		currMsg := sm.l1Messages[boundaryIdx]

		// If prev is assistant with tool_use and curr is tool result,
		// we're splitting a pair - EXCLUDE the assistant from compression
		if prevMsg.Role == "assistant" && len(prevMsg.ToolCalls) > 0 && currMsg.Role == "tool" {
			// Move boundary back to exclude the assistant message
			toCompressCount--

			// Also check if there are more assistant messages with tool_calls before this
			// and exclude them as well to preserve complete tool interaction sequences
			for toCompressCount > 0 {
				checkMsg := sm.l1Messages[toCompressCount-1]
				if checkMsg.Role == "assistant" && len(checkMsg.ToolCalls) > 0 {
					// Check if there are tool results after this assistant
					hasToolResults := false
					for i := toCompressCount; i < len(sm.l1Messages); i++ {
						if sm.l1Messages[i].Role == "tool" {
							hasToolResults = true
							break
						}
						// Stop checking if we hit a user message (start of new exchange)
						if sm.l1Messages[i].Role == "user" {
							break
						}
					}
					if hasToolResults {
						toCompressCount--
					} else {
						break
					}
				} else {
					break
				}
			}
		}
	}

	// Final safety check: scan backwards from boundary to ensure we're not
	// leaving orphaned tool results in L1 after compression
	for i := toCompressCount; i < len(sm.l1Messages); i++ {
		if sm.l1Messages[i].Role == "tool" {
			// Found a tool message that would remain in L1
			// Check if its corresponding assistant is being compressed
			for j := i - 1; j >= 0; j-- {
				if sm.l1Messages[j].Role == "assistant" && len(sm.l1Messages[j].ToolCalls) > 0 {
					if j < toCompressCount {
						// The assistant is being compressed but tool result would stay
						// Move boundary to exclude this assistant
						toCompressCount = j
					}
					break
				}
			}
		}
	}

	return toCompressCount
}

// AddToolResult adds a tool execution result to kernel layer.
// Database-backed optimization: Keeps ONLY immediate previous result in memory.
// All historical results should be persisted to database and retrievable via tools.
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

	sm.updateTokenCount()
	sm.tokenCountDirty = false
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

	// Mark token count as dirty for lazy recalculation
	// Token counting is expensive and not critical for schema caching
	sm.tokenCountDirty = true
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

	// Token count not updated here for performance - will be lazily recalculated when needed
	sm.tokenCountDirty = true

	return schema, ok
}

// RecordFinding stores a verified finding in the kernel layer for working memory.
// This prevents hallucination by maintaining structured facts discovered during analysis.
// If maxFindings is exceeded, LRU eviction removes the oldest finding.
func (sm *SegmentedMemory) RecordFinding(path string, value interface{}, category, note, source string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// If cache is full and this is a new path, evict oldest finding (LRU)
	if _, exists := sm.findingsCache[path]; !exists && len(sm.findingsCache) >= sm.maxFindings {
		// Find the oldest finding
		var oldestPath string
		var oldestTime time.Time
		first := true
		for p, f := range sm.findingsCache {
			if first || f.Timestamp.Before(oldestTime) {
				oldestPath = p
				oldestTime = f.Timestamp
				first = false
			}
		}
		// Remove the oldest finding
		if oldestPath != "" {
			delete(sm.findingsCache, oldestPath)
		}
	}

	// Add or update the finding
	finding := Finding{
		Path:      path,
		Value:     value,
		Category:  category,
		Note:      note,
		Timestamp: time.Now(),
		Source:    source,
	}

	sm.findingsCache[path] = finding
	sm.tokenCountDirty = true // Findings summary will affect token count
}

// GetFinding retrieves a specific finding by path.
func (sm *SegmentedMemory) GetFinding(path string) (Finding, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	finding, ok := sm.findingsCache[path]
	return finding, ok
}

// GetAllFindings returns all recorded findings.
func (sm *SegmentedMemory) GetAllFindings() map[string]Finding {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// Return a copy to avoid external mutation
	findings := make(map[string]Finding, len(sm.findingsCache))
	for k, v := range sm.findingsCache {
		findings[k] = v
	}
	return findings
}

// GetFindingsSummary generates a formatted markdown summary of all findings (thread-safe).
// This summary is injected into the LLM context to provide verified working memory.
func (sm *SegmentedMemory) GetFindingsSummary() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return sm.getFindingsSummaryUnlocked()
}

// getFindingsSummaryUnlocked is the internal implementation (must be called with lock held).
func (sm *SegmentedMemory) getFindingsSummaryUnlocked() string {
	if len(sm.findingsCache) == 0 {
		return ""
	}

	// Group findings by category
	byCategory := make(map[string][]Finding)
	for _, finding := range sm.findingsCache {
		byCategory[finding.Category] = append(byCategory[finding.Category], finding)
	}

	var sb strings.Builder
	sb.WriteString("## Verified Findings (Working Memory)\n\n")

	// Statistics
	if stats := byCategory["statistic"]; len(stats) > 0 {
		sb.WriteString("### Statistics:\n")
		for _, s := range stats {
			sb.WriteString(fmt.Sprintf("- **%s**: %v", s.Path, s.Value))
			if s.Note != "" {
				sb.WriteString(fmt.Sprintf(" (%s)", s.Note))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// Schema
	if schemas := byCategory["schema"]; len(schemas) > 0 {
		sb.WriteString("### Schema Discovered:\n")
		for _, s := range schemas {
			// Format arrays nicely
			if arr, ok := s.Value.([]interface{}); ok {
				formatted := make([]string, len(arr))
				for i, v := range arr {
					formatted[i] = fmt.Sprintf("%v", v)
				}
				sb.WriteString(fmt.Sprintf("- **%s**: [%s]\n", s.Path, strings.Join(formatted, ", ")))
			} else {
				sb.WriteString(fmt.Sprintf("- **%s**: %v\n", s.Path, s.Value))
			}
		}
		sb.WriteString("\n")
	}

	// Distribution
	if dists := byCategory["distribution"]; len(dists) > 0 {
		sb.WriteString("### Data Distribution:\n")
		for _, d := range dists {
			sb.WriteString(fmt.Sprintf("- **%s**: %v\n", d.Path, d.Value))
		}
		sb.WriteString("\n")
	}

	// Observations
	if obs := byCategory["observation"]; len(obs) > 0 {
		sb.WriteString("### Key Observations:\n")
		for _, o := range obs {
			sb.WriteString(fmt.Sprintf("- %v", o.Value))
			if o.Note != "" {
				sb.WriteString(fmt.Sprintf(" (%s)", o.Note))
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// ClearFindings removes all findings from working memory.
// Useful for starting fresh analysis or cleaning up between tasks.
func (sm *SegmentedMemory) ClearFindings() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.findingsCache = make(map[string]Finding)
	sm.tokenCountDirty = true
}

// GetMessages returns all L1 messages for building conversation context.
func (sm *SegmentedMemory) GetMessages() []Message {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	messages := make([]Message, len(sm.l1Messages))
	copy(messages, sm.l1Messages)
	return messages
}

// GetL2Summary returns the L2 summary content for inspection.
// Returns empty string if no compression has occurred yet.
func (sm *SegmentedMemory) GetL2Summary() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.l2Summary
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
func (sm *SegmentedMemory) compressToL2(messages []Message) {
	if len(messages) == 0 {
		return
	}

	// Try LLM-powered compression if available (with timeout)
	var summary string
	if sm.compressor != nil && sm.compressor.IsEnabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		compressed, err := sm.compressor.CompressMessages(ctx, messages)
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
			if err := sm.evictL2ToSwap(); err != nil {
				// Log error but don't crash - continue in degraded mode
				// Use tracer to record error for observability
				if sm.tracer != nil {
					ctx := context.Background()
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
		// Extract key information
		if msg.Role == "user" {
			// User queries
			parts = append(parts, fmt.Sprintf("User asked about: %s", sm.extractKeywords(msg.Content)))
		} else if msg.Role == "assistant" {
			// Assistant actions
			if sm.containsToolCall(msg) {
				parts = append(parts, "Agent executed tools and provided results")
			} else {
				parts = append(parts, "Agent provided analysis")
			}
		} else if msg.Role == "tool" {
			// Tool results - include summary to preserve tool execution context
			parts = append(parts, "Tool result received")
		} else if msg.Role == "system" {
			// System messages (rare in L1, typically in ROM, but handle defensively)
			parts = append(parts, "System instruction provided")
		}
	}

	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, "; ")
}

// extractKeywords pulls out key terms from text (simple heuristic)
func (sm *SegmentedMemory) extractKeywords(text string) string {
	// Simple keyword extraction - just take first 50 chars
	if len(text) > 50 {
		return text[:50] + "..."
	}
	return text
}

// containsToolCall checks if message contains tool execution.
// Adapted for loom's Message type which uses ToolCalls field instead of Metadata.
func (sm *SegmentedMemory) containsToolCall(msg Message) bool {
	return len(msg.ToolCalls) > 0
}

// updateTokenCount calculates actual token usage across all memory layers (must hold lock).
func (sm *SegmentedMemory) updateTokenCount() {
	count := 0

	// ROM layer
	count += sm.tokenCounter.CountTokens(sm.romContent)

	// Kernel layer
	if len(sm.tools) > 0 {
		count += sm.tokenCounter.CountTokens(fmt.Sprintf("Available tools: %s", strings.Join(sm.tools, ", ")))
	}
	count += sm.tokenCounter.EstimateToolResultTokens(sm.toolResults)
	for key, schema := range sm.schemaCache {
		count += sm.tokenCounter.CountTokens(fmt.Sprintf("%s: %s", key, schema))
	}

	// L1 layer (messages)
	count += sm.tokenCounter.EstimateMessagesTokens(sm.l1Messages)

	// L2 layer (summary)
	count += sm.tokenCounter.CountTokens(sm.l2Summary)

	// Promoted context (from swap layer)
	if len(sm.promotedContext) > 0 {
		count += sm.tokenCounter.EstimateMessagesTokens(sm.promotedContext)
	}

	sm.tokenCount = count

	// Update token budget usage
	sm.tokenBudget.Reset()
	sm.tokenBudget.Use(count)
}

// InjectPattern injects a formatted pattern into the message stream.
// Pattern is added as system message after L2 summary, before promoted context.
// This placement ensures pattern knowledge is available but doesn't override ROM or conversation history.
func (sm *SegmentedMemory) InjectPattern(patternContent string, patternName string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.patternContent = patternContent
	sm.patternName = patternName
	sm.tokenCountDirty = true

	if sm.tracer != nil {
		sm.tracer.RecordMetric("patterns.injected", 1.0, map[string]string{
			"pattern": patternName,
		})
	}
}

// GetMessagesForLLM builds the full message list for the LLM call.
// Returns: ROM message + L2 summary message (if exists) + pattern (if injected) + promoted context (if exists) + L1 messages.
// This is what gets sent to the LLM in Message format.
func (sm *SegmentedMemory) GetMessagesForLLM() []Message {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	messages := []Message{}

	// Add ROM as system message
	if sm.romContent != "" {
		messages = append(messages, Message{
			Role:    "system",
			Content: sm.romContent,
		})
	}

	// Add L2 summary as system message (if exists)
	if sm.l2Summary != "" {
		messages = append(messages, Message{
			Role:    "system",
			Content: "Previous conversation summary: " + sm.l2Summary,
		})
	}

	// Add pattern as system message (if injected)
	if sm.patternContent != "" {
		messages = append(messages, Message{
			Role:    "system",
			Content: fmt.Sprintf("# Relevant Pattern Guidance\n\n%s\n\nUse this pattern as guidance for tool selection and parameter construction.", sm.patternContent),
		})
	}

	// Add findings summary as system message (if any findings exist)
	// This provides verified working memory to prevent hallucination
	// Note: getFindingsSummaryUnlocked() must be called with lock already held
	if findingsSummary := sm.getFindingsSummaryUnlocked(); findingsSummary != "" {
		messages = append(messages, Message{
			Role:    "system",
			Content: findingsSummary,
		})
	}

	// Add promoted context from swap (if exists)
	// This is old conversation context retrieved from database
	if len(sm.promotedContext) > 0 {
		// Add as system message to separate from recent conversation
		messages = append(messages, Message{
			Role:    "system",
			Content: fmt.Sprintf("Retrieved conversation history (%d messages):", len(sm.promotedContext)),
		})
		messages = append(messages, sm.promotedContext...)
	}

	// Add L1 messages (recent conversation)
	messages = append(messages, sm.l1Messages...)

	return messages
}

// GetContextWindow builds the full context for LLM with proper layering.
// Returns formatted context string with ROM, Kernel, L2, and L1 layers.
func (sm *SegmentedMemory) GetContextWindow() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var parts []string

	// ROM Layer (always included)
	if sm.romContent != "" {
		parts = append(parts, "=== DOCUMENTATION (ROM) ===")
		parts = append(parts, sm.romContent)
		parts = append(parts, "")
	}

	// Kernel Layer (tool info + recent results)
	if len(sm.tools) > 0 || len(sm.toolResults) > 0 || len(sm.schemaCache) > 0 {
		parts = append(parts, "=== SESSION CONTEXT (KERNEL) ===")

		if len(sm.tools) > 0 {
			parts = append(parts, fmt.Sprintf("Available tools: %s", strings.Join(sm.tools, ", ")))
		}

		if len(sm.toolResults) > 0 {
			parts = append(parts, "\nRecent tool results:")
			for _, result := range sm.toolResults {
				argsJSON, _ := json.Marshal(result.Args)
				parts = append(parts, fmt.Sprintf("- %s(%s): %s", result.ToolName, string(argsJSON), result.Result))
			}
		}

		if len(sm.schemaCache) > 0 {
			parts = append(parts, "\nCached schemas:")
			for key, schema := range sm.schemaCache {
				parts = append(parts, fmt.Sprintf("- %s: %s", key, schema))
			}
		}

		parts = append(parts, "")
	}

	// L2 Cache (compressed history)
	if sm.l2Summary != "" {
		parts = append(parts, "=== CONVERSATION SUMMARY (L2 CACHE) ===")
		parts = append(parts, sm.l2Summary)
		parts = append(parts, "")
	}

	// L1 Cache (recent messages)
	if len(sm.l1Messages) > 0 {
		parts = append(parts, "=== RECENT CONVERSATION (L1 CACHE) ===")
		for _, msg := range sm.l1Messages {
			parts = append(parts, fmt.Sprintf("[%s]: %s", msg.Role, msg.Content))
		}
		parts = append(parts, "")
	}

	return strings.Join(parts, "\n")
}

// GetTokenCount returns current token count across all memory layers.
func (sm *SegmentedMemory) GetTokenCount() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Recalculate if dirty (lazy evaluation for performance)
	if sm.tokenCountDirty {
		sm.updateTokenCount()
		sm.tokenCountDirty = false
	}

	return sm.tokenCount
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
	sm.updateTokenCount()
	sm.tokenCountDirty = false
}

// CompactMemory forces compression of all L1 to L2.
// Returns number of messages compressed and tokens saved.
func (sm *SegmentedMemory) CompactMemory() (int, int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if len(sm.l1Messages) > 0 {
		// Track metrics before compression
		messageCount := len(sm.l1Messages)
		tokensBefore := sm.tokenCount

		sm.compressToL2(sm.l1Messages)
		sm.l1Messages = sm.l1Messages[:0] // Clear L1
		sm.updateTokenCount()
		sm.tokenCountDirty = false

		// Calculate token savings
		tokensSaved := tokensBefore - sm.tokenCount

		// Log compression event
		sm.logCompressionEvent(messageCount, tokensSaved)

		return messageCount, tokensSaved
	}

	return 0, 0
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
		"l1_max_messages":    sm.maxL1Messages,
		"l1_min_messages":    sm.minL1Messages,
		"l2_summary_length":  len(sm.l2Summary),
		"tool_result_count":  len(sm.toolResults),
		"tool_result_max":    sm.maxToolResults,
		"schema_cache_count": len(sm.schemaCache),
		"schema_cache_max":   sm.maxSchemas,
		"rom_token_count":    sm.tokenCounter.CountTokens(sm.romContent),
		"kernel_token_count": sm.getKernelTokens(),
		"l1_token_count":     sm.tokenCounter.EstimateMessagesTokens(sm.l1Messages),
		"l2_token_count":     sm.tokenCounter.CountTokens(sm.l2Summary),
		"budget_warning":     sm.getBudgetWarning(),
	}
}

// getKernelTokens calculates tokens used by kernel layer (must hold lock).
func (sm *SegmentedMemory) getKernelTokens() int {
	count := 0
	if len(sm.tools) > 0 {
		count += sm.tokenCounter.CountTokens(fmt.Sprintf("Available tools: %s", strings.Join(sm.tools, ", ")))
	}
	count += sm.tokenCounter.EstimateToolResultTokens(sm.toolResults)
	for key, schema := range sm.schemaCache {
		count += sm.tokenCounter.CountTokens(fmt.Sprintf("%s: %s", key, schema))
	}
	return count
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
func (sm *SegmentedMemory) evictL2ToSwap() error {
	if !sm.swapEnabled || sm.l2Summary == "" {
		return nil
	}

	// Calculate token count
	tokenCount := sm.tokenCounter.CountTokens(sm.l2Summary)

	// Save L2 snapshot to database with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	contents := make([]string, len(snapshots))
	for i, snapshot := range snapshots {
		contents[i] = snapshot.Content
	}

	// Update statistics
	sm.mu.Lock()
	sm.swapRetrievalCount++
	sm.mu.Unlock()

	return contents, nil
}

// PromoteMessagesToContext adds retrieved messages from swap to active context.
// The messages are added as "promoted context" separate from L1 (which is for recent conversation).
// This allows old context to be available to the LLM without polluting L1.
// Checks token budget before promotion - returns error if budget would be exceeded.
func (sm *SegmentedMemory) PromoteMessagesToContext(messages []Message) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.swapEnabled {
		return fmt.Errorf("swap layer not enabled")
	}

	if len(messages) == 0 {
		return nil
	}

	// Calculate token cost of promoted messages
	promotedTokens := sm.tokenCounter.EstimateMessagesTokens(messages)

	// Check token budget
	used, available, _ := sm.tokenBudget.GetUsage()
	if used+promotedTokens > available {
		return fmt.Errorf("token budget exceeded: would need %d tokens but only %d available",
			promotedTokens, available-used)
	}

	// Add to promoted context
	sm.promotedContext = append(sm.promotedContext, messages...)

	// Update token count
	sm.updateTokenCount()
	sm.tokenCountDirty = false

	return nil
}

// ClearPromotedContext removes all promoted messages from context.
// This allows reclaiming token budget used by retrieved old messages.
func (sm *SegmentedMemory) ClearPromotedContext() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.promotedContext = sm.promotedContext[:0]
	sm.updateTokenCount()
	sm.tokenCountDirty = false
}

// GetPromotedContext returns a copy of promoted messages.
func (sm *SegmentedMemory) GetPromotedContext() []Message {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	promoted := make([]Message, len(sm.promotedContext))
	copy(promoted, sm.promotedContext)
	return promoted
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
	candidates, err := sm.sessionStore.SearchFTS5(ctx, sm.sessionID, query, candidateLimit)
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

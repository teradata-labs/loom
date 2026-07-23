// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package types contains shared types used across the loom framework.
// This package breaks import cycles by providing common types that both
// pkg/agent and pkg/llm packages depend on.
package types

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// segMemAssertOnce guards the one-shot warning emitted when a
// Session.SegmentedMem value fails the SegmentedMemoryInterface type
// assertion — an out-of-repo implementer of an older, narrower interface
// would otherwise silently degrade (AddMessage becomes a no-op,
// GetMessages returns the raw flat list). One log per process is enough
// to surface the issue without spamming the operator on every call.
var segMemAssertOnce sync.Once

func warnSegMemAssertion(actual any) {
	segMemAssertOnce.Do(func() {
		log.Printf("loom/types: Session.SegmentedMem value (%T) does not implement SegmentedMemoryInterface; "+
			"AddMessage/GetMessages will fall back to the flat message list. "+
			"Check that your custom SegmentedMemory implementation exposes RomBase/GetL2Summary/GetMessages/ActiveSkillNames.",
			actual)
	})
}

// ============================================================================
// LLM Types (originally from pkg/llm/types)
// ============================================================================

// ToolCall represents a tool invocation by the LLM.
type ToolCall struct {
	// ID is a unique identifier for this tool call
	ID string

	// Name is the tool name
	Name string

	// Input contains the tool parameters as JSON
	Input map[string]interface{}

	// ThoughtSignature is an opaque token from the provider that must be
	// echoed back verbatim in conversation history. Used by Gemini 3+ models
	// to preserve reasoning context across multi-turn function calling.
	ThoughtSignature string
}

// ContentBlock represents a piece of content in a multi-modal message.
// Can be text or image content.
type ContentBlock struct {
	// Type is the content type ("text" or "image")
	Type string

	// Text contains text content (when Type is "text")
	Text string

	// Image contains image content (when Type is "image")
	Image *ImageContent
}

// ImageContent represents an image in a message.
type ImageContent struct {
	// Type is always "image"
	Type string

	// Source contains the image data
	Source ImageSource
}

// ImageSource contains the actual image data.
type ImageSource struct {
	// Type is the source type ("base64" or "url")
	Type string

	// MediaType is the MIME type ("image/jpeg", "image/png", "image/gif", "image/webp")
	MediaType string

	// Data contains base64-encoded image data (when Type is "base64")
	Data string

	// URL contains the image URL (when Type is "url")
	URL string
}

// SessionContext identifies the context in which a message was created.
// Used for cross-session memory filtering.
type SessionContext string

const (
	// SessionContextDirect indicates message is in direct session (user <-> agent)
	SessionContextDirect SessionContext = "direct"

	// SessionContextCoordinator indicates message is from coordinator to sub-agent
	SessionContextCoordinator SessionContext = "coordinator"

	// SessionContextShared indicates message is visible across sessions
	// (e.g., sub-agent response visible in both coordinator and direct sessions)
	SessionContextShared SessionContext = "shared"
)

// Message represents a single message in the conversation.
type Message struct {
	// ID is the unique message identifier (from database)
	ID string

	// Role is the message sender (user, assistant, tool)
	Role string

	// Content is the message text (for text-only messages, backward compatible)
	Content string

	// ContentBlocks contains multi-modal content (text and/or images)
	// If present, this takes precedence over Content field
	ContentBlocks []ContentBlock

	// ToolCalls contains tool invocations (if role is assistant)
	ToolCalls []ToolCall

	// ToolUseID is the ID of the tool_use block this result corresponds to (if role is tool)
	// This is used by LLM providers like Bedrock/Anthropic to match tool results to tool requests
	ToolUseID string

	// ToolResult contains tool execution result (if role is tool)
	ToolResult *shuttle.Result

	// SessionContext identifies the context (direct, coordinator, shared)
	// Used for cross-session memory filtering
	SessionContext SessionContext

	// AgentID identifies which agent created this message
	// Optional - may be empty for messages created before this field was added
	AgentID string

	// ContextClass is the structural retention class (narrative/charter/ledger/ballast).
	// Optional - empty (narrative) for messages created before this field was added.
	ContextClass string

	// UserID identifies which user owns this message (for RLS multi-tenancy).
	// Set from context via interceptor for PostgreSQL backends.
	// Defaults to "default-user" for SQLite backends.
	UserID string

	// Timestamp when the message was created
	Timestamp time.Time

	// TokenCount for cost tracking
	TokenCount int

	// CostUSD for cost tracking
	CostUSD float64
}

// Usage tracks LLM token usage and costs.
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	CostUSD      float64
	// CacheReadInputTokens: tokens served from prompt cache.
	// For Anthropic, these do NOT count against ITPM rate limits.
	CacheReadInputTokens int
	// CacheCreationInputTokens: tokens written to prompt cache (billed at 1.25x for Anthropic).
	CacheCreationInputTokens int
}

// LLMResponse represents a response from the LLM.
type LLMResponse struct {
	// Content is the text response (if no tool calls)
	Content string

	// ToolCalls contains requested tool executions
	ToolCalls []ToolCall

	// StopReason indicates why the LLM stopped
	StopReason string

	// Usage tracks token usage
	Usage Usage

	// Metadata contains provider-specific metadata
	Metadata map[string]interface{}

	// Thinking contains the agent's internal reasoning process
	// (for models that support extended thinking like Claude with thinking blocks)
	Thinking string
}

// LLMProvider defines the interface for LLM providers.
// This allows pluggable LLM backends (Anthropic, Bedrock, Ollama, Azure, etc.).
//
// Note: The Chat method accepts context.Context (not agent.Context) to avoid
// import cycles. Agent-specific context (session, tracer, progress) should be
// handled at the agent layer, not in LLM providers.
type LLMProvider interface {
	// Chat sends a conversation to the LLM and returns the response
	Chat(ctx context.Context, messages []Message, tools []shuttle.Tool) (*LLMResponse, error)

	// Name returns the provider name
	Name() string

	// Model returns the model identifier
	Model() string
}

// HealthChecker is an optional interface providers can implement to supply a
// lightweight reachability check. ValidateProviders uses this instead of a
// full Chat "ping" when available, which avoids side-effects like model
// loading on providers such as Ollama.
type HealthChecker interface {
	HealthCheck(ctx context.Context) error
}

// TokenCallback is called for each token/chunk during streaming.
// Implementations should be lightweight and non-blocking.
type TokenCallback func(token string)

// StreamingLLMProvider extends LLMProvider with token streaming support.
// Providers implement this interface if they support real-time token streaming.
// Use the SupportsStreaming helper to check if a provider implements this interface.
type StreamingLLMProvider interface {
	LLMProvider

	// ChatStream streams tokens as they're generated from the LLM.
	// Returns the complete LLMResponse after the stream finishes.
	// Calls tokenCallback for each token/chunk received from the LLM.
	// The callback is called synchronously and should not block.
	ChatStream(ctx context.Context, messages []Message, tools []shuttle.Tool,
		tokenCallback TokenCallback) (*LLMResponse, error)
}

// SupportsStreaming checks if a provider supports token streaming.
// Returns true if the provider implements StreamingLLMProvider.
func SupportsStreaming(provider LLMProvider) bool {
	_, ok := provider.(StreamingLLMProvider)
	return ok
}

// ============================================================================
// Agent Types (originally from pkg/agent)
// ============================================================================

// Session represents a conversation session with history and context.
// Thread-safe: All methods can be called concurrently.
type Session struct {
	mu sync.RWMutex

	// ID is the unique session identifier
	ID string

	// Name is a human-readable session name (optional, set via CreateSession RPC)
	Name string

	// AgentID identifies which agent owns this session (for cross-session memory)
	// Example: "coordinator-agent", "analyzer-sub-agent"
	// This enables agent-scoped memory where a sub-agent can access messages
	// from multiple sessions (coordinator parent + direct user sessions)
	AgentID string

	// ParentSessionID links sub-agent sessions to their coordinator session
	// NULL for top-level sessions (direct user interaction)
	// Set for sub-agent sessions created by workflow coordinators
	ParentSessionID string

	// UserID identifies which user owns this session (for RLS multi-tenancy).
	// Set from context via interceptor for PostgreSQL backends.
	// Defaults to "default-user" for SQLite backends.
	UserID string

	// Messages is the conversation history (flat, for backward compatibility)
	Messages []Message

	// SegmentedMem is the tiered memory manager (ROM/Kernel/L1/L2)
	// If nil, falls back to flat Messages list
	// Note: SegmentedMemory type will remain in pkg/agent
	SegmentedMem interface{} // Keep as interface{} to avoid circular dependency

	// RomCatalog is the per-session, append-only skill catalog rendered
	// into the ROM slot. The router appends entries as it discovers
	// relevant skills; entries whose skill is currently loaded are filtered
	// out at ROM assembly time (by walking L1 for load-metadata). Not
	// persisted — rebuilt on restore by the router as the session runs.
	RomCatalog []SkillCatalogEntry

	// FailureTracker tracks consecutive tool failures for escalation
	// Note: consecutiveFailureTracker type will remain in pkg/agent
	FailureTracker interface{} // Keep as interface{} to avoid circular dependency

	// Context holds session-specific context (database, table, etc.)
	Context map[string]interface{}

	// CreatedAt is when the session was created
	CreatedAt time.Time

	// UpdatedAt is when the session was last updated
	UpdatedAt time.Time

	// TotalCostUSD is the accumulated cost for this session
	TotalCostUSD float64

	// TotalTokens is the accumulated token usage
	TotalTokens int
}

// SegmentedMemoryInterface defines the interface for segmented memory.
// This allows Session to work with segmented memory without importing pkg/agent.
type SegmentedMemoryInterface interface {
	AddMessage(ctx context.Context, msg Message)
	GetMessagesForLLM() []Message
	GetL1MessageCount() int

	// RomBase returns the static base ROM string (identity, guidance, protocols).
	// The full ROM the LLM sees is composed by Session.GetMessages: RomBase
	// concatenated with a rendered skill catalog whose entries are filtered
	// against the currently-loaded skills. Splitting them here keeps
	// SegmentedMemory's ROM byte-stable across turns and lets Session own the
	// per-session catalog state without SegmentedMemory needing a Session
	// handle.
	RomBase() string

	// GetL2Summary returns the fold residue text if a fold has run this session.
	GetL2Summary() string

	// GetMessages returns a copy of L1 (the conversation).
	GetMessages() []Message

	// ActiveSkillNames returns the set of skill names whose load-body is
	// currently present in L1. A skill is active iff its manage_skills load
	// tool_result is still resident — walked structurally, not textually, by
	// reading msg.ToolResult.Metadata["action"]=="load" + ["skill"]. Empty on
	// no memory (fresh session).
	ActiveSkillNames() map[string]bool
}

// SkillCatalogEntry is one line in the session's per-session skill catalog
// rendered into the ROM slot. The catalog is append-only within a session:
// the router adds entries as it discovers skills relevant to the ongoing
// conversation, and each entry stays until the session ends. At ROM
// assembly time, entries whose skill is already active (body present in L1)
// are filtered out — the LLM only ever sees candidates it might load, never
// candidates it has already loaded.
type SkillCatalogEntry struct {
	Name        string
	Description string
}

// AddMessage adds a message to the session history.
// If SegmentedMem is configured, uses tiered memory management.
// Otherwise falls back to flat message list.
// ctx is threaded through to enable RLS-aware storage operations during compression.
func (s *Session) AddMessage(ctx context.Context, msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Add to flat list (backward compatibility)
	s.Messages = append(s.Messages, msg)

	// Add to segmented memory if configured
	if s.SegmentedMem != nil {
		if segMem, ok := s.SegmentedMem.(SegmentedMemoryInterface); ok {
			segMem.AddMessage(ctx, msg)
		} else {
			warnSegMemAssertion(s.SegmentedMem)
		}
	}

	s.UpdatedAt = time.Now()
	s.TotalCostUSD += msg.CostUSD
	s.TotalTokens += msg.TokenCount
}

// GetMessages returns the compiled message list for the LLM call.
//
// When SegmentedMem is configured this is the sole Contract 1 assembler:
// ROM (system, exactly one) + fold residue (user, present only after a
// fold) + L1 conversation. No other channel exists.
//
// The ROM's base is byte-stable per session; the trailing [Available
// Skills] block mutates on skill activity (see composeROM for the
// cache-economics rationale).
//
// The ROM composed here = SegmentedMem.RomBase() + a rendered skill
// catalog filtered against the currently-loaded skills. The catalog
// itself lives on Session.RomCatalog (append-only, router-fed); the
// loaded set is walked structurally out of L1 via
// SegmentedMem.ActiveSkillNames — "skill is loaded iff its
// manage_skills(load) tool_result is still in L1", so fold / valve
// eviction of the load body self-heals (skill returns to the catalog
// next turn). There is no explicit unload — a skill load is an event
// in the append-only context, retired only by pressure reclaim.
//
// Fallback (no SegmentedMem) returns a copy of the flat message list.
func (s *Session) GetMessages() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.SegmentedMem != nil {
		if segMem, ok := s.SegmentedMem.(SegmentedMemoryInterface); ok {
			return s.assembleForLLM(segMem)
		}
		warnSegMemAssertion(s.SegmentedMem)
	}

	// Fallback to flat list
	messages := make([]Message, len(s.Messages))
	copy(messages, s.Messages)
	return messages
}

// assembleForLLM is Contract 1: ROM + fold residue + L1. Called under
// s.mu.RLock (via GetMessages). segMem's own methods take their own
// RLock as needed — the two locks are not held simultaneously in a way
// that could deadlock (s.mu is the outer lock, sm.mu the inner one, no
// call from sm.* takes s.mu back).
func (s *Session) assembleForLLM(segMem SegmentedMemoryInterface) []Message {
	out := []Message{}

	rom := composeROM(segMem.RomBase(), s.RomCatalog, segMem.ActiveSkillNames())
	if rom != "" {
		out = append(out, Message{Role: "system", Content: rom})
	}

	msgs := segMem.GetMessages()
	if l2 := segMem.GetL2Summary(); l2 != "" {
		out = append(out, Message{Role: "user", Content: "[Prior conversation summary]\n" + l2})
	}
	out = append(out, msgs...)
	return out
}

// composeROM renders the ROM slot: base ROM concatenated with an
// [Available Skills] section that lists every catalog entry whose skill
// is NOT currently active (body not in L1). Order-stable: catalog
// insertion order is preserved so the cache stays warm for descriptions
// added earlier in the session. A catalog with no non-active entries
// renders no section at all — no empty header ever leaks into ROM.
func composeROM(base string, catalog []SkillCatalogEntry, active map[string]bool) string {
	if len(catalog) == 0 {
		return base
	}
	var lines []string
	for _, e := range catalog {
		if e.Name == "" {
			continue
		}
		if active[e.Name] {
			continue
		}
		if e.Description == "" {
			lines = append(lines, "- "+e.Name)
		} else {
			lines = append(lines, "- "+e.Name+": "+e.Description)
		}
	}
	if len(lines) == 0 {
		return base
	}
	if base == "" {
		return "[Available Skills]\n" + joinLines(lines)
	}
	return base + "\n\n[Available Skills]\n" + joinLines(lines)
}

func joinLines(lines []string) string {
	var b strings.Builder
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l)
	}
	return b.String()
}

// AppendToRomCatalog adds a skill entry to the session's ROM catalog
// if not already present (dedup by Name). Order-stable: first-seen wins,
// later duplicates are ignored so cache remains warm.
//
// Called by the discovery/router path once per turn with candidates the
// router surfaced from the user's message. Not persisted — the catalog
// rebuilds naturally on restore as the router runs over subsequent turns.
func (s *Session) AppendToRomCatalog(entries ...SkillCatalogEntry) {
	if len(entries) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	seen := make(map[string]bool, len(s.RomCatalog))
	for _, e := range s.RomCatalog {
		seen[e.Name] = true
	}
	for _, e := range entries {
		if e.Name == "" || seen[e.Name] {
			continue
		}
		s.RomCatalog = append(s.RomCatalog, e)
		seen[e.Name] = true
	}
}

// TrimLastN removes the last n messages from the flat Messages list.
// If n is 0, it syncs the flat list to match the segmented memory length
// (used after AggressiveTrim where segmented memory was already trimmed).
// Respects tool_use/tool_result pair boundaries.
func (s *Session) TrimLastN(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.Messages) == 0 {
		return
	}

	if n == 0 {
		// Sync mode: trim flat list to match segmented memory's retained
		// conversation message count (L1). GetMessagesForLLM() is not usable
		// here — it includes synthetic, non-conversational entries (ROM, L2
		// summary, pattern/skill injections) that are never part of the flat
		// list, which would leave stale messages behind after a full reset.
		if s.SegmentedMem != nil {
			if segMem, ok := s.SegmentedMem.(SegmentedMemoryInterface); ok {
				targetLen := segMem.GetL1MessageCount()
				if targetLen < len(s.Messages) {
					s.Messages = s.Messages[:targetLen]
				}
			} else {
				warnSegMemAssertion(s.SegmentedMem)
			}
		}
		s.UpdatedAt = time.Now()
		return
	}

	if n >= len(s.Messages) {
		s.Messages = s.Messages[:0]
		s.UpdatedAt = time.Now()
		return
	}

	cutIdx := len(s.Messages) - n
	// Expand backward to not orphan tool results.
	for cutIdx > 0 && s.Messages[cutIdx].Role == "tool" {
		cutIdx--
	}

	s.Messages = s.Messages[:cutIdx]
	s.UpdatedAt = time.Now()
}

// MessageCount returns the total number of messages in the session.
// Thread-safe via RLock.
// Returns int32 capped at MaxInt32 to prevent overflow when used in proto messages.
func (s *Session) MessageCount() int32 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := len(s.Messages)
	if count > 2147483647 { // math.MaxInt32
		return 2147483647
	}
	return int32(count)
}

// SnapshotMessages returns a copy of the flat message history under RLock.
// Callers that need to read session.Messages from outside the type — e.g.,
// prepareContext's zone-check inputs — must go through this accessor rather
// than reading the field directly. Direct-field reads would race against
// AddMessage's writes; the race is not tripped today (SendMessage
// serializes per session) but the Session's thread-safety contract
// requires all reads honor the same lock its writes take.
func (s *Session) SnapshotMessages() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Message, len(s.Messages))
	copy(out, s.Messages)
	return out
}

// ExecutionStage represents the current stage of agent execution.
type ExecutionStage string

const (
	StagePatternSelection ExecutionStage = "pattern_selection"
	StageSchemaDiscovery  ExecutionStage = "schema_discovery"
	StageLLMGeneration    ExecutionStage = "llm_generation"
	StageToolExecution    ExecutionStage = "tool_execution"
	StageSynthesis        ExecutionStage = "synthesis"         // Synthesizing tool execution results
	StageHumanInTheLoop   ExecutionStage = "human_in_the_loop" // Waiting for human response
	StageGuardrailCheck   ExecutionStage = "guardrail_check"
	StageSelfCorrection   ExecutionStage = "self_correction"
	StageCompleted        ExecutionStage = "completed"
	StageFailed           ExecutionStage = "failed"
)

// HITLRequestInfo carries information about a HITL request during streaming.
type HITLRequestInfo struct {
	// RequestID is the unique HITL request identifier
	RequestID string

	// Question is the question being asked to the human
	Question string

	// RequestType is the type of request (approval, decision, input, review)
	RequestType string

	// Priority is the request priority (low, normal, high, critical)
	Priority string

	// Timeout is how long to wait for human response
	Timeout time.Duration

	// Context provides additional context for the request
	Context map[string]interface{}
}

// ProgressEvent represents a progress update during agent execution.
type ProgressEvent struct {
	// Stage is the current execution stage
	Stage ExecutionStage

	// Progress is the completion percentage (0-100)
	Progress int32

	// Message is a human-readable description of current activity
	Message string

	// ToolName is the tool being executed (if applicable)
	ToolName string

	// Timestamp when this event occurred
	Timestamp time.Time

	// HITLRequest contains HITL request details (only when Stage == StageHumanInTheLoop)
	HITLRequest *HITLRequestInfo

	// Token streaming fields (for real-time LLM response rendering)

	// PartialContent is the accumulated content so far during streaming
	PartialContent string

	// IsTokenStream indicates if this event is a token streaming update
	IsTokenStream bool

	// TokenCount is the running count of tokens received
	TokenCount int32

	// TTFT is the time to first token in milliseconds (0 if not applicable)
	TTFT int64

	// Tool execution lifecycle fields (for real-time tool event rendering)

	// IsToolStarted indicates this event is a tool-execution-started update
	IsToolStarted bool

	// IsToolCompleted indicates this event is a tool-execution-completed update
	IsToolCompleted bool

	// ToolInput contains the tool's input parameters (populated when IsToolStarted)
	ToolInput map[string]interface{}

	// ToolResult contains the tool's output data (populated when IsToolCompleted)
	ToolResult interface{}

	// ToolError contains the error message if tool execution failed (when IsToolCompleted)
	ToolError string

	// ToolSuccess indicates whether the tool execution succeeded (when IsToolCompleted)
	ToolSuccess bool

	// ToolDurationMs is the tool execution duration in milliseconds (when IsToolCompleted)
	ToolDurationMs int64

	// ToolCallID is a unique identifier correlating started/completed events for the same tool call
	ToolCallID string
}

// ProgressCallback is called when agent execution progress occurs.
// This allows streaming progress updates to clients.
type ProgressCallback func(event ProgressEvent)

// Context is an enhanced context.Context with agent-specific methods.
// This wraps the standard context to provide agent-specific functionality.
type Context interface {
	// Embed standard context
	context.Context

	// Session returns the current session
	Session() *Session

	// Tracer returns the observability tracer
	Tracer() observability.Tracer

	// ProgressCallback returns the progress callback (may be nil)
	ProgressCallback() ProgressCallback
}

// ============================================================================
// Utility Functions
// ============================================================================

// SafeInt32 converts an int to int32, capping at MaxInt32/MinInt32 to prevent overflow.
// This prevents gosec G115 integer overflow warnings when converting to proto int32 fields.
// Use this when converting Go int (which may be int64) to proto int32 fields
// (for example ListSessionsResponse.total_count when the filtered session count exceeds MaxInt32).
func SafeInt32(n int) int32 {
	const maxInt32 = 2147483647  // math.MaxInt32
	const minInt32 = -2147483648 // math.MinInt32
	if n > maxInt32 {
		return maxInt32
	}
	if n < minInt32 {
		return minInt32
	}
	return int32(n)
}

// SafeInt32FromInt64 converts an int64 to int32, capping at MaxInt32/MinInt32 to prevent overflow.
// This prevents gosec G115 integer overflow warnings.
func SafeInt32FromInt64(n int64) int32 {
	const maxInt32 = 2147483647  // math.MaxInt32
	const minInt32 = -2147483648 // math.MinInt32
	if n > maxInt32 {
		return maxInt32
	}
	if n < minInt32 {
		return minInt32
	}
	return int32(n)
}

// SafeInt32FromFloat64 converts a float64 to int32, capping at MaxInt32/MinInt32 to prevent overflow.
// This prevents gosec G115 integer overflow warnings.
func SafeInt32FromFloat64(f float64) int32 {
	const maxInt32 = 2147483647  // math.MaxInt32
	const minInt32 = -2147483648 // math.MinInt32
	if f > maxInt32 {
		return maxInt32
	}
	if f < minInt32 {
		return minInt32
	}
	return int32(f)
}

// SafeUint converts an int to uint, returning 0 for negative values.
// This prevents gosec G115 integer overflow warnings.
func SafeUint(n int) uint {
	if n < 0 {
		return 0
	}
	return uint(n)
}

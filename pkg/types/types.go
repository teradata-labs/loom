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
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

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

	// AgentID identifies which agent owns this session (for cross-session memory)
	// Example: "coordinator-agent", "analyzer-sub-agent"
	// This enables agent-scoped memory where a sub-agent can access messages
	// from multiple sessions (coordinator parent + direct user sessions)
	AgentID string

	// ParentSessionID links sub-agent sessions to their coordinator session
	// NULL for top-level sessions (direct user interaction)
	// Set for sub-agent sessions created by workflow coordinators
	ParentSessionID string

	// Messages is the conversation history (flat, for backward compatibility)
	Messages []Message

	// SegmentedMem is the tiered memory manager (ROM/Kernel/L1/L2)
	// If nil, falls back to flat Messages list
	// Note: SegmentedMemory type will remain in pkg/agent
	SegmentedMem interface{} // Keep as interface{} to avoid circular dependency

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
	AddMessage(msg Message)
	GetMessagesForLLM() []Message
}

// AddMessage adds a message to the session history.
// If SegmentedMem is configured, uses tiered memory management.
// Otherwise falls back to flat message list.
func (s *Session) AddMessage(msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Add to flat list (backward compatibility)
	s.Messages = append(s.Messages, msg)

	// Add to segmented memory if configured
	if s.SegmentedMem != nil {
		if segMem, ok := s.SegmentedMem.(SegmentedMemoryInterface); ok {
			segMem.AddMessage(msg)
		}
	}

	s.UpdatedAt = time.Now()
	s.TotalCostUSD += msg.CostUSD
	s.TotalTokens += msg.TokenCount
}

// GetMessages returns a copy of the conversation history.
// If SegmentedMem is configured, returns the optimized context window (ROM + Kernel + L1 + L2).
// Otherwise returns the flat message list.
func (s *Session) GetMessages() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Use segmented memory if configured (optimized message list for LLM)
	if s.SegmentedMem != nil {
		if segMem, ok := s.SegmentedMem.(SegmentedMemoryInterface); ok {
			return segMem.GetMessagesForLLM()
		}
	}

	// Fallback to flat list
	messages := make([]Message, len(s.Messages))
	copy(messages, s.Messages)
	return messages
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
// Use this when converting Go int (which may be int64) to proto int32 fields.
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

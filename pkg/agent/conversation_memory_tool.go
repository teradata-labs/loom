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

	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// ConversationMemoryTool provides unified access to conversation history across sessions.
// Consolidates recall, search, and clear operations into a single tool with action parameter.
// Supports session scopes: current, agent, all.
type ConversationMemoryTool struct {
	memory *Memory
}

// NewConversationMemoryTool creates a new conversation memory tool.
func NewConversationMemoryTool(memory *Memory) *ConversationMemoryTool {
	return &ConversationMemoryTool{
		memory: memory,
	}
}

// Name returns the tool name.
func (t *ConversationMemoryTool) Name() string {
	return "conversation_memory"
}

// Description returns the tool description.
func (t *ConversationMemoryTool) Description() string {
	return `Access conversation history from long-term storage.

Three actions available:
1. recall - Retrieve specific messages by offset/limit from current session
2. search - Search for relevant messages using natural language queries (supports session scopes)
3. clear - Remove recalled messages from context to free token budget

Use this when:
- User references earlier discussions no longer in recent context
- Need to find specific topics across conversation history
- Token budget is tight and old context needs clearing`
}

// Backend returns the backend type this tool requires (empty = backend-agnostic).
func (t *ConversationMemoryTool) Backend() string {
	return "" // Backend-agnostic
}

// InputSchema returns the JSON schema for tool parameters.
func (t *ConversationMemoryTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"action": {
				Type:        "string",
				Description: "Action to perform: 'recall', 'search', or 'clear'",
			},
			"offset": {
				Type:        "integer",
				Description: "(recall only) Number of messages to skip from beginning (0 = oldest messages)",
			},
			"limit": {
				Type:        "integer",
				Description: "(recall/search) Max messages to retrieve (default: 10, max: 50 for recall, 20 for search)",
			},
			"query": {
				Type:        "string",
				Description: "(search only) Natural language search query (e.g., 'What did we discuss about SQL performance?')",
			},
			"session_scope": {
				Type:        "string",
				Description: "(search only) Scope of search: 'current' (this session), 'agent' (all sessions for this agent), 'all' (all sessions). Default: 'current'",
			},
		},
		Required: []string{"action"},
	}
}

// Execute performs the requested memory operation.
func (t *ConversationMemoryTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Extract action
	action, ok := input["action"].(string)
	if !ok || action == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMETER",
				Message: "action must be one of: recall, search, clear",
			},
		}, nil
	}

	// Route to appropriate handler
	switch action {
	case "recall":
		return t.executeRecall(ctx, input)
	case "search":
		return t.executeSearch(ctx, input)
	case "clear":
		return t.executeClear(ctx, input)
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_ACTION",
				Message: fmt.Sprintf("Unknown action '%s'. Must be: recall, search, or clear", action),
			},
		}, nil
	}
}

// executeRecall retrieves messages from swap storage by offset/limit.
func (t *ConversationMemoryTool) executeRecall(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Extract session ID from context
	sessionID := session.SessionIDFromContext(ctx)
	if sessionID == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "MISSING_SESSION_ID",
				Message: "Session ID not found in context",
			},
		}, nil
	}

	// Get session
	session, exists := t.memory.GetSession(sessionID)
	if !exists {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "SESSION_NOT_FOUND",
				Message:    fmt.Sprintf("Session %s not found. Use session_memory(action='list') to see available sessions.", sessionID),
				Suggestion: "Use session_memory tool to explore session history",
			},
		}, nil
	}

	// Get segmented memory
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	if !ok {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "SWAP_NOT_SUPPORTED",
				Message: "This session does not support long-term storage",
			},
		}, nil
	}

	// Check if swap is enabled
	if !segMem.IsSwapEnabled() {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "SWAP_NOT_ENABLED",
				Message: "Long-term storage is not enabled for this session. All conversation history is already in context.",
			},
		}, nil
	}

	// Parse parameters
	offset, ok := input["offset"].(float64)
	if !ok {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMETER",
				Message:    "offset must be an integer (number of messages to skip from beginning)",
				Suggestion: "Example: conversation_memory(action='recall', offset=0, limit=10)",
			},
		}, nil
	}

	limit := 10.0 // default
	if limitVal, ok := input["limit"].(float64); ok {
		limit = limitVal
	}

	// Enforce maximum limit
	if limit > 50 {
		limit = 50
	}

	// Retrieve messages from swap
	messages, err := segMem.RetrieveMessagesFromSwap(ctx, int(offset), int(limit))
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "RETRIEVAL_FAILED",
				Message: fmt.Sprintf("Failed to retrieve messages: %v", err),
			},
		}, nil
	}

	// Promote messages to context
	if err := segMem.PromoteMessagesToContext(messages); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "PROMOTION_FAILED",
				Message:    fmt.Sprintf("Failed to add messages to context (token budget may be full): %v", err),
				Suggestion: "Try clearing recalled context first: conversation_memory(action='clear')",
			},
		}, nil
	}

	// Format response
	responseData := map[string]interface{}{
		"action":  "recall",
		"success": true,
		"count":   len(messages),
	}

	if len(messages) > 0 {
		// Include message previews
		previews := make([]map[string]string, 0, len(messages))
		for i, msg := range messages {
			if i >= 5 { // Limit previews to first 5
				break
			}
			preview := msg.Content
			if len(preview) > 100 {
				preview = preview[:100] + "..."
			}
			previews = append(previews, map[string]string{
				"role":      msg.Role,
				"preview":   preview,
				"timestamp": msg.Timestamp.Format("2006-01-02 15:04:05"),
			})
		}
		responseData["messages"] = previews
		responseData["oldest_message"] = messages[0].Timestamp.Format("2006-01-02 15:04:05")
	}

	responseJSON, _ := json.MarshalIndent(responseData, "", "  ")

	return &shuttle.Result{
		Success: true,
		Data:    string(responseJSON),
	}, nil
}

// executeSearch searches conversation history using natural language queries.
func (t *ConversationMemoryTool) executeSearch(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Parse query
	query, ok := input["query"].(string)
	if !ok || query == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMETER",
				Message:    "query must be a non-empty string",
				Suggestion: "Example: conversation_memory(action='search', query='SQL performance optimization')",
			},
		}, nil
	}

	// Parse session scope
	sessionScope := "current" // default
	if scopeVal, ok := input["session_scope"].(string); ok {
		sessionScope = scopeVal
	}

	// Validate scope
	if sessionScope != "current" && sessionScope != "agent" && sessionScope != "all" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMETER",
				Message: fmt.Sprintf("session_scope '%s' invalid. Must be: current, agent, or all", sessionScope),
			},
		}, nil
	}

	limit := 10.0 // default
	if limitVal, ok := input["limit"].(float64); ok {
		limit = limitVal
	}
	if limit > 20 {
		limit = 20 // enforce maximum for search
	}

	// Route based on session scope
	switch sessionScope {
	case "current":
		return t.searchCurrentSession(ctx, query, int(limit))
	case "agent":
		return t.searchAgentSessions(ctx, query, int(limit))
	case "all":
		return t.searchAllSessions(ctx, query, int(limit))
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_SCOPE",
				Message: fmt.Sprintf("Unknown session_scope: %s", sessionScope),
			},
		}, nil
	}
}

// searchCurrentSession searches only the current session using BM25 + LLM reranking.
func (t *ConversationMemoryTool) searchCurrentSession(ctx context.Context, query string, limit int) (*shuttle.Result, error) {
	// Extract session ID
	sessionID := session.SessionIDFromContext(ctx)
	if sessionID == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "MISSING_SESSION_ID",
				Message: "Session ID not found in context",
			},
		}, nil
	}

	// Get session
	session, exists := t.memory.GetSession(sessionID)
	if !exists {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "SESSION_NOT_FOUND",
				Message: fmt.Sprintf("Session %s not found", sessionID),
			},
		}, nil
	}

	// Get segmented memory
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	if !ok {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "SWAP_NOT_SUPPORTED",
				Message: "This session does not support semantic search",
			},
		}, nil
	}

	// Check if swap is enabled
	if !segMem.IsSwapEnabled() {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "SWAP_NOT_ENABLED",
				Message: "Semantic search requires long-term storage. All messages are already in context.",
			},
		}, nil
	}

	// Execute semantic search (BM25 + LLM reranking)
	messages, err := segMem.SearchMessages(ctx, query, limit)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "SEARCH_FAILED",
				Message: fmt.Sprintf("Search failed: %v", err),
			},
		}, nil
	}

	// Promote to context
	if len(messages) > 0 {
		if err := segMem.PromoteMessagesToContext(messages); err != nil {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:       "PROMOTION_FAILED",
					Message:    fmt.Sprintf("Found results but failed to add to context: %v", err),
					Suggestion: "Try clearing recalled context first: conversation_memory(action='clear')",
				},
			}, nil
		}
	}

	return t.formatSearchResults(messages, query, "current")
}

// searchAgentSessions searches all sessions belonging to the current agent.
func (t *ConversationMemoryTool) searchAgentSessions(ctx context.Context, query string, limit int) (*shuttle.Result, error) {
	// Extract agent ID from context (typed key)
	agentID := session.AgentIDFromContext(ctx)
	if agentID == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "MISSING_AGENT_ID",
				Message: "Agent ID not found in context",
			},
		}, nil
	}

	// Get session store
	sessionStore := t.memory.store
	if sessionStore == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "NO_SESSION_STORE",
				Message: "Session store not available for cross-session search",
			},
		}, nil
	}

	// Use FTS5 search with agent scope (BM25 ranking)
	messages, err := sessionStore.SearchMessagesByAgent(ctx, agentID, query, limit)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "SEARCH_FAILED",
				Message: fmt.Sprintf("Agent-scoped search failed: %v", err),
			},
		}, nil
	}

	return t.formatSearchResults(messages, query, "agent")
}

// searchAllSessions searches across all sessions using FTS5.
func (t *ConversationMemoryTool) searchAllSessions(ctx context.Context, query string, limit int) (*shuttle.Result, error) {
	// Get session store
	sessionStore := t.memory.store
	if sessionStore == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "NO_SESSION_STORE",
				Message: "Session store not available for cross-session search",
			},
		}, nil
	}

	// Use FTS5 search across all sessions
	// Pass empty sessionID to search all
	messages, err := sessionStore.SearchMessages(ctx, "", query, limit)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "SEARCH_FAILED",
				Message: fmt.Sprintf("Cross-session search failed: %v", err),
			},
		}, nil
	}

	return t.formatSearchResults(messages, query, "all")
}

// formatSearchResults formats search results for agent consumption.
func (t *ConversationMemoryTool) formatSearchResults(messages []Message, query string, scope string) (*shuttle.Result, error) {
	responseData := map[string]interface{}{
		"action":        "search",
		"success":       true,
		"query":         query,
		"session_scope": scope,
		"count":         len(messages),
	}

	if len(messages) > 0 {
		// Include message previews
		previews := make([]map[string]interface{}, 0, len(messages))
		for i, msg := range messages {
			if i >= 5 { // Limit previews to first 5
				break
			}
			preview := msg.Content
			if len(preview) > 150 {
				preview = preview[:150] + "..."
			}
			msgPreview := map[string]interface{}{
				"role":      msg.Role,
				"preview":   preview,
				"timestamp": msg.Timestamp.Format("2006-01-02 15:04:05"),
			}
			// Note: Message struct doesn't include session_id field
			// For cross-session searches, session_id tracking would need to be added separately
			previews = append(previews, msgPreview)
		}
		responseData["messages"] = previews
	}

	responseJSON, _ := json.MarshalIndent(responseData, "", "  ")

	return &shuttle.Result{
		Success: true,
		Data:    string(responseJSON),
	}, nil
}

// executeClear removes promoted messages from context.
func (t *ConversationMemoryTool) executeClear(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Extract session ID
	sessionID := session.SessionIDFromContext(ctx)
	if sessionID == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "MISSING_SESSION_ID",
				Message: "Session ID not found in context",
			},
		}, nil
	}

	// Get session
	session, exists := t.memory.GetSession(sessionID)
	if !exists {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "SESSION_NOT_FOUND",
				Message: fmt.Sprintf("Session %s not found", sessionID),
			},
		}, nil
	}

	// Get segmented memory
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	if !ok {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "SWAP_NOT_SUPPORTED",
				Message: "This session does not support recalled context",
			},
		}, nil
	}

	// Get count before clearing
	promoted := segMem.GetPromotedContext()
	count := len(promoted)

	// Clear promoted context
	segMem.ClearPromotedContext()

	responseData := map[string]interface{}{
		"action":        "clear",
		"success":       true,
		"cleared_count": count,
	}

	responseJSON, _ := json.MarshalIndent(responseData, "", "  ")

	return &shuttle.Result{
		Success: true,
		Data:    string(responseJSON),
	}, nil
}

// Note: Agent-scoped and all-scoped searches now use FTS5 with BM25 ranking
// via SessionStore.SearchMessagesByAgent() and SessionStore.SearchMessages()
// No need for manual keyword filtering.

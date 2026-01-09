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

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// RecallConversationTool retrieves old messages from swap storage.
// Allows agents to access conversation history beyond L1/L2 capacity.
type RecallConversationTool struct {
	memory *Memory // Reference to memory manager for accessing sessions
}

// NewRecallConversationTool creates a new recall conversation tool.
func NewRecallConversationTool(memory *Memory) *RecallConversationTool {
	return &RecallConversationTool{
		memory: memory,
	}
}

// Name returns the tool name.
func (t *RecallConversationTool) Name() string {
	return "recall_conversation"
}

// Description returns the tool description.
func (t *RecallConversationTool) Description() string {
	return "Retrieve older conversation history from long-term storage when you need context from earlier in this conversation. Use this when the user references something from a previous discussion that is no longer in recent context."
}

// Backend returns the backend type this tool requires (empty = backend-agnostic).
func (t *RecallConversationTool) Backend() string {
	return "" // Backend-agnostic
}

// InputSchema returns the JSON schema for tool parameters.
func (t *RecallConversationTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"offset": {
				Type:        "integer",
				Description: "Number of messages to skip from the beginning (0 = oldest messages)",
			},
			"limit": {
				Type:        "integer",
				Description: "Maximum number of messages to retrieve (default: 10, max: 50)",
			},
		},
		Required: []string{"offset"},
	}
}

// Execute retrieves messages from swap and promotes them to context.
func (t *RecallConversationTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Extract session ID from context
	sessionID, ok := ctx.Value("session_id").(string)
	if !ok || sessionID == "" {
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
				Message: "This session does not support swap layer",
			},
		}, nil
	}

	// Check if swap is enabled
	if !segMem.IsSwapEnabled() {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "SWAP_NOT_ENABLED",
				Message: "Swap layer is not enabled for this session. Long-term storage is not available.",
			},
		}, nil
	}

	// Parse parameters
	offset, ok := input["offset"].(float64)
	if !ok {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMETER",
				Message: "offset must be an integer",
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
				Code:    "PROMOTION_FAILED",
				Message: fmt.Sprintf("Failed to add messages to context: %v", err),
			},
		}, nil
	}

	// Format summary for agent
	summary := fmt.Sprintf("Retrieved and loaded %d messages from conversation history (offset %d).", len(messages), int(offset))
	if len(messages) > 0 {
		summary += fmt.Sprintf(" The oldest message is from %s.", messages[0].Timestamp.Format("2006-01-02 15:04:05"))
	}

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

	previewsJSON, _ := json.Marshal(previews)

	return &shuttle.Result{
		Success: true,
		Data:    fmt.Sprintf("%s\n\nMessage previews:\n%s", summary, string(previewsJSON)),
	}, nil
}

// ClearRecalledContextTool removes promoted messages from context.
// Allows agents to reclaim token budget after using recalled context.
type ClearRecalledContextTool struct {
	memory *Memory
}

// NewClearRecalledContextTool creates a new clear recalled context tool.
func NewClearRecalledContextTool(memory *Memory) *ClearRecalledContextTool {
	return &ClearRecalledContextTool{
		memory: memory,
	}
}

// Name returns the tool name.
func (t *ClearRecalledContextTool) Name() string {
	return "clear_recalled_context"
}

// Description returns the tool description.
func (t *ClearRecalledContextTool) Description() string {
	return "Remove previously recalled conversation history from context to free up token budget. Use this when you no longer need the older context that was loaded with recall_conversation."
}

// Backend returns the backend type this tool requires (empty = backend-agnostic).
func (t *ClearRecalledContextTool) Backend() string {
	return "" // Backend-agnostic
}

// InputSchema returns the JSON schema for tool parameters.
func (t *ClearRecalledContextTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type:       "object",
		Properties: map[string]*shuttle.JSONSchema{},
	}
}

// Execute clears promoted context.
func (t *ClearRecalledContextTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Extract session ID from context
	sessionID, ok := ctx.Value("session_id").(string)
	if !ok || sessionID == "" {
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
				Message: "This session does not support swap layer",
			},
		}, nil
	}

	// Get count before clearing
	promoted := segMem.GetPromotedContext()
	count := len(promoted)

	// Clear promoted context
	segMem.ClearPromotedContext()

	return &shuttle.Result{
		Success: true,
		Data:    fmt.Sprintf("Cleared %d recalled messages from context. Token budget has been freed up.", count),
	}, nil
}

// SearchConversationTool searches conversation history using semantic search.
// Uses BM25 + LLM reranking to find relevant messages based on natural language queries.
type SearchConversationTool struct {
	memory *Memory
}

// NewSearchConversationTool creates a new semantic search tool.
func NewSearchConversationTool(memory *Memory) *SearchConversationTool {
	return &SearchConversationTool{
		memory: memory,
	}
}

// Name returns the tool name.
func (t *SearchConversationTool) Name() string {
	return "search_conversation"
}

// Description returns the tool description.
func (t *SearchConversationTool) Description() string {
	return `Search conversation history using natural language queries.

Use this when you need to find relevant context from earlier in the conversation, but don't know the exact offset.

Examples:
- "What did we discuss about database optimization?"
- "Find mentions of API errors"
- "Search for decisions about authentication"

The tool uses semantic search to find messages that are conceptually relevant, even if they use different terminology.`
}

// Backend returns the backend type (empty = backend-agnostic).
func (t *SearchConversationTool) Backend() string {
	return ""
}

// InputSchema returns the JSON schema for tool parameters.
func (t *SearchConversationTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"query": {
				Type:        "string",
				Description: "Natural language search query (e.g., 'What did we discuss about SQL performance?')",
			},
			"limit": {
				Type:        "integer",
				Description: "Maximum number of results to return (default: 10, max: 20)",
			},
			"promote": {
				Type:        "boolean",
				Description: "Automatically promote results to context (default: true). Set false to preview without adding to context.",
			},
		},
		Required: []string{"query"},
	}
}

// Execute performs semantic search and optionally promotes results.
func (t *SearchConversationTool) Execute(
	ctx context.Context,
	input map[string]interface{},
) (*shuttle.Result, error) {
	// Extract session ID
	sessionID, ok := ctx.Value("session_id").(string)
	if !ok || sessionID == "" {
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
				Message: "Semantic search requires swap layer to be enabled",
			},
		}, nil
	}

	// Parse parameters
	query, ok := input["query"].(string)
	if !ok || query == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMETER",
				Message: "query must be a non-empty string",
			},
		}, nil
	}

	limit := 10.0 // default
	if limitVal, ok := input["limit"].(float64); ok {
		limit = limitVal
	}
	if limit > 20 {
		limit = 20 // enforce maximum
	}

	promote := true // default
	if promoteVal, ok := input["promote"].(bool); ok {
		promote = promoteVal
	}

	// Execute semantic search
	messages, err := segMem.SearchMessages(ctx, query, int(limit))
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "SEARCH_FAILED",
				Message: fmt.Sprintf("Semantic search failed: %v", err),
			},
		}, nil
	}

	// Optionally promote to context
	if promote && len(messages) > 0 {
		if err := segMem.PromoteMessagesToContext(messages); err != nil {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "PROMOTION_FAILED",
					Message: fmt.Sprintf("Failed to add messages to context: %v", err),
				},
			}, nil
		}
	}

	// Format response
	summary := fmt.Sprintf("Found %d relevant messages for query: '%s'", len(messages), query)
	if promote {
		summary += " (promoted to context)"
	} else {
		summary += " (preview only, not added to context)"
	}

	if len(messages) > 0 {
		summary += fmt.Sprintf("\n\nOldest result: %s", messages[0].Timestamp.Format("2006-01-02 15:04:05"))
	}

	// Include message previews
	previews := make([]map[string]string, 0, len(messages))
	for i, msg := range messages {
		if i >= 5 { // Limit previews to first 5
			break
		}
		preview := msg.Content
		if len(preview) > 150 {
			preview = preview[:150] + "..."
		}
		previews = append(previews, map[string]string{
			"role":      msg.Role,
			"preview":   preview,
			"timestamp": msg.Timestamp.Format("2006-01-02 15:04:05"),
		})
	}

	previewsJSON, _ := json.Marshal(previews)

	return &shuttle.Result{
		Success: true,
		Data:    fmt.Sprintf("%s\n\nTop results:\n%s", summary, string(previewsJSON)),
	}, nil
}

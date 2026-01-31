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

// SessionMemoryTool provides unified access to session lifecycle and cross-session memory.
// Consolidates list, summary, and compact operations into a single tool with action parameter.
type SessionMemoryTool struct {
	store  *SessionStore
	memory *Memory
}

// NewSessionMemoryTool creates a new session memory tool.
func NewSessionMemoryTool(store *SessionStore, memory *Memory) *SessionMemoryTool {
	return &SessionMemoryTool{
		store:  store,
		memory: memory,
	}
}

// Name returns the tool name.
func (t *SessionMemoryTool) Name() string {
	return "session_memory"
}

// Description returns the tool description.
func (t *SessionMemoryTool) Description() string {
	return `Manage sessions and access cross-session memory.

Three actions available:
1. list - List all sessions for the current agent with metadata
2. summary - Retrieve L2 memory summaries (compacted conversation history) for a session
3. compact - Manually trigger memory compaction for current session

Use this when:
- User asks about past sessions or conversations
- Need to view summarized conversation history
- Want to free up token budget by compacting current session
- Exploring conversation history across multiple sessions`
}

// Backend returns the backend type this tool requires (empty = backend-agnostic).
func (t *SessionMemoryTool) Backend() string {
	return "" // Backend-agnostic
}

// InputSchema returns the JSON schema for tool parameters.
func (t *SessionMemoryTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"action": {
				Type:        "string",
				Description: "Action to perform: 'list', 'summary', or 'compact'",
			},
			"agent_id": {
				Type:        "string",
				Description: "(list only) Agent ID to list sessions for (default: current agent)",
			},
			"limit": {
				Type:        "integer",
				Description: "(list only) Max sessions to retrieve (default: 20, max: 100)",
			},
			"session_id": {
				Type:        "string",
				Description: "(summary/compact) Session ID to operate on (default for compact: current session)",
			},
			"snapshot_type": {
				Type:        "string",
				Description: "(summary only) Snapshot type to retrieve (default: 'l2_summary')",
			},
			"force": {
				Type:        "boolean",
				Description: "(compact only) Force compaction even if not needed (default: false)",
			},
		},
		Required: []string{"action"},
	}
}

// Execute performs the requested session memory operation.
func (t *SessionMemoryTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Extract action
	action, ok := input["action"].(string)
	if !ok || action == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMETER",
				Message: "action must be one of: list, summary, compact",
			},
		}, nil
	}

	// Route to appropriate handler
	switch action {
	case "list":
		return t.executeList(ctx, input)
	case "summary":
		return t.executeSummary(ctx, input)
	case "compact":
		return t.executeCompact(ctx, input)
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_ACTION",
				Message: fmt.Sprintf("Unknown action '%s'. Must be: list, summary, or compact", action),
			},
		}, nil
	}
}

// executeList lists all sessions for the specified agent.
func (t *SessionMemoryTool) executeList(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Get agent ID from input or context
	agentID, ok := input["agent_id"].(string)
	if !ok || agentID == "" {
		// Try to get from context
		agentID, ok = ctx.Value("agent_id").(string)
		if !ok || agentID == "" {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "MISSING_AGENT_ID",
					Message: "Agent ID not found in context",
				},
			}, nil
		}
	}

	// Parse limit
	limit := 20 // default
	if limitVal, ok := input["limit"].(float64); ok {
		limit = int(limitVal)
	}
	if limit > 100 {
		limit = 100 // enforce maximum
	}

	// Get session IDs for agent
	sessionIDs, err := t.store.LoadAgentSessions(ctx, agentID)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "RETRIEVAL_FAILED",
				Message: fmt.Sprintf("Failed to load sessions: %v", err),
			},
		}, nil
	}

	// Apply limit
	if len(sessionIDs) > limit {
		sessionIDs = sessionIDs[:limit]
	}

	// Query session metadata for each session
	sessions := make([]map[string]interface{}, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		metadata, err := t.getSessionMetadata(ctx, sessionID)
		if err != nil {
			// Log error but continue with other sessions
			continue
		}
		sessions = append(sessions, metadata)
	}

	// Format response
	responseData := map[string]interface{}{
		"action":   "list",
		"success":  true,
		"agent_id": agentID,
		"count":    len(sessions),
		"sessions": sessions,
	}

	responseJSON, _ := json.MarshalIndent(responseData, "", "  ")

	return &shuttle.Result{
		Success: true,
		Data:    string(responseJSON),
	}, nil
}

// getSessionMetadata retrieves metadata for a single session.
func (t *SessionMemoryTool) getSessionMetadata(ctx context.Context, sessionID string) (map[string]interface{}, error) {
	// Query session metadata
	query := `
		SELECT s.id, s.agent_id, s.created_at, s.updated_at, s.total_tokens,
		       COUNT(m.id) as message_count
		FROM sessions s
		LEFT JOIN messages m ON m.session_id = s.id
		WHERE s.id = ?
		GROUP BY s.id
	`

	var id, agentID string
	var createdAt, updatedAt int64
	var totalTokens, messageCount int

	err := t.store.db.QueryRowContext(ctx, query, sessionID).Scan(
		&id, &agentID, &createdAt, &updatedAt, &totalTokens, &messageCount,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query session metadata: %w", err)
	}

	// Get last message preview
	lastMessageQuery := `
		SELECT content
		FROM messages
		WHERE session_id = ?
		ORDER BY timestamp DESC
		LIMIT 1
	`

	var lastMessagePreview string
	err = t.store.db.QueryRowContext(ctx, lastMessageQuery, sessionID).Scan(&lastMessagePreview)
	if err != nil {
		// No messages yet, that's ok
		lastMessagePreview = ""
	} else if len(lastMessagePreview) > 100 {
		lastMessagePreview = lastMessagePreview[:100] + "..."
	}

	return map[string]interface{}{
		"session_id":            id,
		"agent_id":              agentID,
		"created_at":            fmt.Sprintf("%d", createdAt),
		"updated_at":            fmt.Sprintf("%d", updatedAt),
		"message_count":         messageCount,
		"total_tokens":          totalTokens,
		"last_message_preview":  lastMessagePreview,
	}, nil
}

// executeSummary retrieves L2 memory snapshots for a session.
func (t *SessionMemoryTool) executeSummary(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Parse session ID
	sessionID, ok := input["session_id"].(string)
	if !ok || sessionID == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMETER",
				Message:    "session_id is required for summary action",
				Suggestion: "Use session_memory(action='list') to see available sessions",
			},
		}, nil
	}

	// Parse snapshot type
	snapshotType := "l2_summary" // default
	if typeVal, ok := input["snapshot_type"].(string); ok && typeVal != "" {
		snapshotType = typeVal
	}

	// Load memory snapshots (retrieve all, we'll format the most recent prominently)
	snapshots, err := t.store.LoadMemorySnapshots(ctx, sessionID, snapshotType, 0)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "RETRIEVAL_FAILED",
				Message: fmt.Sprintf("Failed to load memory snapshots: %v", err),
			},
		}, nil
	}

	if len(snapshots) == 0 {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "NO_SNAPSHOTS",
				Message: fmt.Sprintf("No memory snapshots found for session %s", sessionID),
			},
		}, nil
	}

	// Get most recent snapshot
	latest := snapshots[len(snapshots)-1]

	// Format response
	responseData := map[string]interface{}{
		"action":        "summary",
		"success":       true,
		"session_id":    sessionID,
		"snapshot_type": snapshotType,
		"count":         len(snapshots),
		"latest": map[string]interface{}{
			"id":          latest.ID,
			"summary":     latest.Content,
			"token_count": latest.TokenCount,
			"created_at":  latest.CreatedAt.Format("2006-01-02 15:04:05"),
		},
	}

	// Include all snapshot IDs and timestamps for reference
	if len(snapshots) > 1 {
		history := make([]map[string]interface{}, 0, len(snapshots)-1)
		for i := 0; i < len(snapshots)-1; i++ {
			snap := snapshots[i]
			history = append(history, map[string]interface{}{
				"id":          snap.ID,
				"token_count": snap.TokenCount,
				"created_at":  snap.CreatedAt.Format("2006-01-02 15:04:05"),
			})
		}
		responseData["history"] = history
	}

	responseJSON, _ := json.MarshalIndent(responseData, "", "  ")

	return &shuttle.Result{
		Success: true,
		Data:    string(responseJSON),
	}, nil
}

// executeCompact triggers manual memory compaction for the current session.
func (t *SessionMemoryTool) executeCompact(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Parse session ID (default to current session)
	sessionID, ok := input["session_id"].(string)
	if !ok || sessionID == "" {
		// Try to get from context
		sessionID, ok = ctx.Value("session_id").(string)
		if !ok || sessionID == "" {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "MISSING_SESSION_ID",
					Message: "Session ID not found in context",
				},
			}, nil
		}
	}

	// Parse force flag
	force := false
	if forceVal, ok := input["force"].(bool); ok {
		force = forceVal
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
				Code:    "COMPACTION_NOT_SUPPORTED",
				Message: "This session does not support memory compaction",
			},
		}, nil
	}

	// Check if compaction is needed (unless forced)
	if !force {
		l1Count := segMem.GetL1MessageCount()
		if l1Count < segMem.minL1Messages {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "COMPACTION_NOT_NEEDED",
					Message: fmt.Sprintf("Compaction not needed (L1 has only %d messages, minimum is %d). Use force=true to compact anyway.", l1Count, segMem.minL1Messages),
				},
			}, nil
		}
	}

	// Trigger compaction
	messagesCompressed, tokensSaved := segMem.CompactMemory()

	// Format response
	responseData := map[string]interface{}{
		"action":              "compact",
		"success":             true,
		"session_id":          sessionID,
		"messages_compressed": messagesCompressed,
		"tokens_saved":        tokensSaved,
	}

	// If L2 was saved to database, include snapshot ID
	// (Note: CompactMemory doesn't automatically save to database,
	// that happens during normal L2 eviction. This is a manual compaction.)
	responseData["note"] = "Memory compacted to L2. Use session_memory(action='summary') to view L2 summaries."

	responseJSON, _ := json.MarshalIndent(responseData, "", "  ")

	return &shuttle.Result{
		Success: true,
		Data:    string(responseJSON),
	}, nil
}

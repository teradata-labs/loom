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
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// SessionStore implements agent.SessionStorage using PostgreSQL with pgx.
type SessionStore struct {
	pool         *pgxpool.Pool
	tracer       observability.Tracer
	cleanupHooks []agent.SessionCleanupHook
}

// NewSessionStore creates a new PostgreSQL-backed session store.
func NewSessionStore(pool *pgxpool.Pool, tracer observability.Tracer) *SessionStore {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &SessionStore{
		pool:   pool,
		tracer: tracer,
	}
}

// SaveSession persists a session to PostgreSQL using an upsert.
func (s *SessionStore) SaveSession(ctx context.Context, session *agent.Session) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg_session_store.save_session")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", session.ID)

	contextJSON, err := json.Marshal(session.Context)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to marshal session context: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO sessions (id, agent_id, parent_session_id, context_json, created_at, updated_at, total_cost_usd, total_tokens)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO UPDATE SET
			agent_id = EXCLUDED.agent_id,
			parent_session_id = EXCLUDED.parent_session_id,
			context_json = EXCLUDED.context_json,
			updated_at = EXCLUDED.updated_at,
			total_cost_usd = EXCLUDED.total_cost_usd,
			total_tokens = EXCLUDED.total_tokens`,
		session.ID,
		nullableString(session.AgentID),
		nullableString(session.ParentSessionID),
		contextJSON,
		session.CreatedAt,
		session.UpdatedAt,
		session.TotalCostUSD,
		session.TotalTokens,
	)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}

// LoadSession retrieves a session and its messages from PostgreSQL.
func (s *SessionStore) LoadSession(ctx context.Context, sessionID string) (*agent.Session, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_session_store.load_session")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)

	var (
		agentID         *string
		parentSessionID *string
		contextJSON     []byte
		createdAt       time.Time
		updatedAt       time.Time
		totalCost       float64
		totalTokens     int
	)

	err := s.pool.QueryRow(ctx, `
		SELECT agent_id, parent_session_id, context_json, created_at, updated_at, total_cost_usd, total_tokens
		FROM sessions WHERE id = $1`,
		sessionID,
	).Scan(&agentID, &parentSessionID, &contextJSON, &createdAt, &updatedAt, &totalCost, &totalTokens)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("session not found: %s", sessionID)
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to load session: %w", err)
	}

	session := &agent.Session{
		ID:           sessionID,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
		TotalCostUSD: totalCost,
		TotalTokens:  totalTokens,
	}
	if agentID != nil {
		session.AgentID = *agentID
	}
	if parentSessionID != nil {
		session.ParentSessionID = *parentSessionID
	}
	if len(contextJSON) > 0 {
		var contextMap map[string]interface{}
		if err := json.Unmarshal(contextJSON, &contextMap); err == nil {
			session.Context = contextMap
		}
	}

	// Load messages
	messages, err := s.LoadMessages(ctx, sessionID)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to load messages: %w", err)
	}
	session.Messages = messages

	return session, nil
}

// ListSessions returns all session IDs ordered by most recently updated.
func (s *SessionStore) ListSessions(ctx context.Context) ([]string, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_session_store.list_sessions")
	defer s.tracer.EndSpan(span)

	rows, err := s.pool.Query(ctx, "SELECT id FROM sessions ORDER BY updated_at DESC")
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to scan session ID: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteSession removes a session and cascades to related records.
func (s *SessionStore) DeleteSession(ctx context.Context, sessionID string) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg_session_store.delete_session")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)

	// Run cleanup hooks before deletion
	for _, hook := range s.cleanupHooks {
		hook(ctx, sessionID)
	}

	_, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE id = $1", sessionID)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to delete session: %w", err)
	}
	return nil
}

// LoadAgentSessions returns session IDs for a specific agent.
func (s *SessionStore) LoadAgentSessions(ctx context.Context, agentID string) ([]string, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_session_store.load_agent_sessions")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("agent_id", agentID)

	rows, err := s.pool.Query(ctx,
		"SELECT id FROM sessions WHERE agent_id = $1 ORDER BY updated_at DESC",
		agentID,
	)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to load agent sessions: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to scan session ID: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SaveMessage persists a message to the messages table.
func (s *SessionStore) SaveMessage(ctx context.Context, sessionID string, msg agent.Message) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg_session_store.save_message")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)
	span.SetAttribute("role", msg.Role)

	// Serialize tool calls
	var toolCallsJSON []byte
	if len(msg.ToolCalls) > 0 {
		var err error
		toolCallsJSON, err = json.Marshal(msg.ToolCalls)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to marshal tool calls: %w", err)
		}
	}

	// Serialize tool result
	var toolResultJSON []byte
	if msg.ToolResult != nil {
		var err error
		toolResultJSON, err = json.Marshal(msg.ToolResult)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to marshal tool result: %w", err)
		}
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO messages (session_id, role, content, tool_calls_json, tool_use_id, tool_result_json, session_context, agent_id, timestamp, token_count, cost_usd)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		sessionID,
		msg.Role,
		msg.Content,
		nullableBytes(toolCallsJSON),
		nullableString(msg.ToolUseID),
		nullableBytes(toolResultJSON),
		string(msg.SessionContext),
		nullableString(msg.AgentID),
		msg.Timestamp,
		msg.TokenCount,
		msg.CostUSD,
	)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to save message: %w", err)
	}

	// Update session's updated_at timestamp
	_, _ = s.pool.Exec(ctx, "UPDATE sessions SET updated_at = $1 WHERE id = $2", time.Now().UTC(), sessionID)

	return nil
}

// LoadMessages retrieves all messages for a session ordered by timestamp.
func (s *SessionStore) LoadMessages(ctx context.Context, sessionID string) ([]agent.Message, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_session_store.load_messages")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)

	rows, err := s.pool.Query(ctx, `
		SELECT id, role, content, tool_calls_json, tool_use_id, tool_result_json, session_context, agent_id, timestamp, token_count, cost_usd
		FROM messages
		WHERE session_id = $1
		ORDER BY timestamp ASC`,
		sessionID,
	)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to load messages: %w", err)
	}
	defer rows.Close()

	return scanMessages(rows)
}

// LoadMessagesForAgent retrieves all messages created by a specific agent.
func (s *SessionStore) LoadMessagesForAgent(ctx context.Context, agentID string) ([]agent.Message, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_session_store.load_messages_for_agent")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("agent_id", agentID)

	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.role, m.content, m.tool_calls_json, m.tool_use_id, m.tool_result_json, m.session_context, m.agent_id, m.timestamp, m.token_count, m.cost_usd
		FROM messages m
		JOIN sessions s ON m.session_id = s.id
		WHERE s.agent_id = $1
		ORDER BY m.timestamp ASC`,
		agentID,
	)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to load messages for agent: %w", err)
	}
	defer rows.Close()

	return scanMessages(rows)
}

// LoadMessagesFromParentSession loads messages from the parent session of the given session.
func (s *SessionStore) LoadMessagesFromParentSession(ctx context.Context, sessionID string) ([]agent.Message, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_session_store.load_messages_from_parent")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)

	// First find the parent session
	var parentID *string
	err := s.pool.QueryRow(ctx,
		"SELECT parent_session_id FROM sessions WHERE id = $1",
		sessionID,
	).Scan(&parentID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to find parent session: %w", err)
	}
	if parentID == nil || *parentID == "" {
		return nil, nil
	}

	return s.LoadMessages(ctx, *parentID)
}

// SearchMessages performs full-text search on messages within a session using tsvector.
func (s *SessionStore) SearchMessages(ctx context.Context, sessionID, query string, limit int) ([]agent.Message, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_session_store.search_messages")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)
	span.SetAttribute("query", query)

	if limit <= 0 {
		limit = 20
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, role, content, tool_calls_json, tool_use_id, tool_result_json, session_context, agent_id, timestamp, token_count, cost_usd
		FROM messages
		WHERE session_id = $1 AND content_search @@ websearch_to_tsquery('english', $2)
		ORDER BY ts_rank_cd(content_search, websearch_to_tsquery('english', $2)) DESC
		LIMIT $3`,
		sessionID, query, limit,
	)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to search messages: %w", err)
	}
	defer rows.Close()

	return scanMessages(rows)
}

// SearchMessagesByAgent performs full-text search on messages for an agent.
func (s *SessionStore) SearchMessagesByAgent(ctx context.Context, agentID, query string, limit int) ([]agent.Message, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_session_store.search_messages_by_agent")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("agent_id", agentID)
	span.SetAttribute("query", query)

	if limit <= 0 {
		limit = 20
	}

	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.role, m.content, m.tool_calls_json, m.tool_use_id, m.tool_result_json, m.session_context, m.agent_id, m.timestamp, m.token_count, m.cost_usd
		FROM messages m
		JOIN sessions s ON m.session_id = s.id
		WHERE s.agent_id = $1 AND m.content_search @@ websearch_to_tsquery('english', $2)
		ORDER BY ts_rank_cd(m.content_search, websearch_to_tsquery('english', $2)) DESC
		LIMIT $3`,
		agentID, query, limit,
	)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to search messages by agent: %w", err)
	}
	defer rows.Close()

	return scanMessages(rows)
}

// SaveToolExecution persists a tool execution record.
func (s *SessionStore) SaveToolExecution(ctx context.Context, sessionID string, exec agent.ToolExecution) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg_session_store.save_tool_execution")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)
	span.SetAttribute("tool_name", exec.ToolName)

	inputJSON, err := json.Marshal(exec.Input)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to marshal tool input: %w", err)
	}

	var resultJSON []byte
	if exec.Result != nil {
		resultJSON, err = json.Marshal(exec.Result)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to marshal tool result: %w", err)
		}
	}

	var errMsg *string
	if exec.Error != nil {
		msg := exec.Error.Error()
		errMsg = &msg
	} else if exec.Result != nil && exec.Result.Error != nil {
		errMsg = &exec.Result.Error.Message
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO tool_executions (session_id, tool_name, input_json, result_json, error, execution_time_ms, timestamp)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		sessionID,
		exec.ToolName,
		inputJSON,
		nullableBytes(resultJSON),
		errMsg,
		0, // execution_time_ms not tracked in ToolExecution struct
		time.Now().UTC(),
	)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to save tool execution: %w", err)
	}
	return nil
}

// SaveMemorySnapshot persists a memory snapshot.
func (s *SessionStore) SaveMemorySnapshot(ctx context.Context, sessionID, snapshotType, content string, tokenCount int) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg_session_store.save_memory_snapshot")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)
	span.SetAttribute("snapshot_type", snapshotType)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO memory_snapshots (session_id, snapshot_type, content, token_count, created_at)
		VALUES ($1, $2, $3, $4, $5)`,
		sessionID, snapshotType, content, tokenCount, time.Now().UTC(),
	)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to save memory snapshot: %w", err)
	}
	return nil
}

// LoadMemorySnapshots retrieves memory snapshots for a session.
func (s *SessionStore) LoadMemorySnapshots(ctx context.Context, sessionID string, snapshotType string, limit int) ([]agent.MemorySnapshot, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_session_store.load_memory_snapshots")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)
	span.SetAttribute("snapshot_type", snapshotType)

	query := `
		SELECT id, session_id, snapshot_type, content, token_count, created_at
		FROM memory_snapshots
		WHERE session_id = $1 AND snapshot_type = $2
		ORDER BY created_at ASC`

	args := []interface{}{sessionID, snapshotType}
	if limit > 0 {
		query += " LIMIT $3"
		args = append(args, limit)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to load memory snapshots: %w", err)
	}
	defer rows.Close()

	var snapshots []agent.MemorySnapshot
	for rows.Next() {
		var snap agent.MemorySnapshot
		if err := rows.Scan(&snap.ID, &snap.SessionID, &snap.SnapshotType, &snap.Content, &snap.TokenCount, &snap.CreatedAt); err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to scan memory snapshot: %w", err)
		}
		snapshots = append(snapshots, snap)
	}
	return snapshots, rows.Err()
}

// RegisterCleanupHook adds a function to be called before session deletion.
func (s *SessionStore) RegisterCleanupHook(hook agent.SessionCleanupHook) {
	s.cleanupHooks = append(s.cleanupHooks, hook)
}

// GetStats returns aggregate statistics about stored sessions.
func (s *SessionStore) GetStats(ctx context.Context) (*agent.Stats, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_session_store.get_stats")
	defer s.tracer.EndSpan(span)

	stats := &agent.Stats{}

	err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM sessions").Scan(&stats.SessionCount)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to count sessions: %w", err)
	}

	err = s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM messages").Scan(&stats.MessageCount)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to count messages: %w", err)
	}

	err = s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM tool_executions").Scan(&stats.ToolExecutionCount)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to count tool executions: %w", err)
	}

	err = s.pool.QueryRow(ctx, "SELECT COALESCE(SUM(total_cost_usd), 0), COALESCE(SUM(total_tokens), 0) FROM sessions").
		Scan(&stats.TotalCostUSD, &stats.TotalTokens)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to sum costs/tokens: %w", err)
	}

	return stats, nil
}

// Close is a no-op for the session store; the pool is managed by the backend.
func (s *SessionStore) Close() error {
	return nil
}

// scanMessages extracts Message objects from pgx rows.
func scanMessages(rows pgx.Rows) ([]agent.Message, error) {
	var messages []agent.Message
	for rows.Next() {
		var (
			id             int64
			role           string
			content        *string
			toolCallsJSON  []byte
			toolUseID      *string
			toolResultJSON []byte
			sessionCtx     *string
			msgAgentID     *string
			timestamp      time.Time
			tokenCount     int
			costUSD        float64
		)

		if err := rows.Scan(&id, &role, &content, &toolCallsJSON, &toolUseID, &toolResultJSON, &sessionCtx, &msgAgentID, &timestamp, &tokenCount, &costUSD); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}

		msg := agent.Message{
			ID:         fmt.Sprintf("%d", id),
			Role:       role,
			Timestamp:  timestamp,
			TokenCount: tokenCount,
			CostUSD:    costUSD,
		}

		if content != nil {
			msg.Content = *content
		}
		if toolUseID != nil {
			msg.ToolUseID = *toolUseID
		}
		if sessionCtx != nil {
			msg.SessionContext = types.SessionContext(*sessionCtx)
		}
		if msgAgentID != nil {
			msg.AgentID = *msgAgentID
		}

		// Deserialize tool calls
		if len(toolCallsJSON) > 0 {
			if err := json.Unmarshal(toolCallsJSON, &msg.ToolCalls); err != nil {
				return nil, fmt.Errorf("failed to unmarshal tool calls: %w", err)
			}
		}

		// Deserialize tool result
		if len(toolResultJSON) > 0 {
			var result shuttle.Result
			if err := json.Unmarshal(toolResultJSON, &result); err != nil {
				return nil, fmt.Errorf("failed to unmarshal tool result: %w", err)
			}
			msg.ToolResult = &result
		}

		messages = append(messages, msg)
	}
	return messages, rows.Err()
}

// nullableString returns nil for empty strings, otherwise a pointer to the string.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nullableBytes returns nil for empty/nil byte slices.
func nullableBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

// Compile-time check: SessionStore implements agent.SessionStorage.
var _ agent.SessionStorage = (*SessionStore)(nil)

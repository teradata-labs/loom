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
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// SessionCleanupHook is called when a session is deleted.
// Used for cleanup tasks like releasing shared memory references.
// The hook receives the session ID being deleted.
type SessionCleanupHook func(ctx context.Context, sessionID string)

// SessionStore provides persistent storage for sessions, messages, and tool executions.
// All database operations are traced to hawk for observability.
type SessionStore struct {
	db           *sql.DB
	mu           sync.RWMutex
	tracer       observability.Tracer
	cleanupHooks []SessionCleanupHook
}

// NewSessionStore creates a new SessionStore with SQLite persistence.
// For backward compatibility, encryption is disabled by default.
// Use NewSessionStoreWithConfig for encryption support.
func NewSessionStore(dbPath string, tracer observability.Tracer) (*SessionStore, error) {
	return NewSessionStoreWithConfig(DBConfig{Path: dbPath}, tracer)
}

// NewSessionStoreWithConfig creates a new SessionStore with optional encryption.
func NewSessionStoreWithConfig(config DBConfig, tracer observability.Tracer) (*SessionStore, error) {
	// Open database with optional encryption
	db, err := OpenDB(config)
	if err != nil {
		return nil, err
	}

	// Enable WAL mode for better concurrency
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	store := &SessionStore{
		db:     db,
		tracer: tracer,
	}

	// Initialize schema
	if err := store.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return store, nil
}

// initSchema creates the database schema if it doesn't exist.
func (s *SessionStore) initSchema() error {
	ctx := context.Background()
	var span *observability.Span
	if s.tracer != nil {
		ctx, span = s.tracer.StartSpan(ctx, "session_store.init_schema")
		defer s.tracer.EndSpan(span)
	}

	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		agent_id TEXT,
		parent_session_id TEXT,
		context_json TEXT,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		total_cost_usd REAL DEFAULT 0,
		total_tokens INTEGER DEFAULT 0,
		FOREIGN KEY (parent_session_id) REFERENCES sessions(id) ON DELETE SET NULL
	);

	CREATE TABLE IF NOT EXISTS messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT,
		tool_calls_json TEXT,
		tool_use_id TEXT,
		tool_result_json TEXT,
		session_context TEXT DEFAULT 'direct',
		timestamp INTEGER NOT NULL,
		token_count INTEGER DEFAULT 0,
		cost_usd REAL DEFAULT 0,
		FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS tool_executions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		tool_name TEXT NOT NULL,
		input_json TEXT,
		result_json TEXT,
		error TEXT,
		execution_time_ms INTEGER,
		timestamp INTEGER NOT NULL,
		FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS memory_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		snapshot_type TEXT NOT NULL,
		content TEXT NOT NULL,
		token_count INTEGER DEFAULT 0,
		created_at INTEGER NOT NULL,
		FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
	);

	-- FTS5 virtual table for semantic search (BM25 ranking)
	CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts5 USING fts5(
		message_id UNINDEXED,
		session_id UNINDEXED,
		role UNINDEXED,
		content,
		timestamp UNINDEXED,
		tokenize='porter unicode61'
	);

	-- Sync triggers: Keep FTS5 in sync with messages table
	CREATE TRIGGER IF NOT EXISTS messages_fts5_insert AFTER INSERT ON messages
	BEGIN
		INSERT INTO messages_fts5(message_id, session_id, role, content, timestamp)
		VALUES (NEW.id, NEW.session_id, NEW.role, NEW.content, NEW.timestamp);
	END;

	CREATE TRIGGER IF NOT EXISTS messages_fts5_update AFTER UPDATE ON messages
	BEGIN
		DELETE FROM messages_fts5 WHERE message_id = OLD.id;
		INSERT INTO messages_fts5(message_id, session_id, role, content, timestamp)
		VALUES (NEW.id, NEW.session_id, NEW.role, NEW.content, NEW.timestamp);
	END;

	CREATE TRIGGER IF NOT EXISTS messages_fts5_delete AFTER DELETE ON messages
	BEGIN
		DELETE FROM messages_fts5 WHERE message_id = OLD.id;
	END;

	CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id);
	CREATE INDEX IF NOT EXISTS idx_messages_context ON messages(session_context);
	CREATE INDEX IF NOT EXISTS idx_tool_executions_session ON tool_executions(session_id);
	CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at);
	CREATE INDEX IF NOT EXISTS idx_sessions_agent ON sessions(agent_id);
	CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_session_id);
	CREATE INDEX IF NOT EXISTS idx_snapshots_session ON memory_snapshots(session_id, created_at);

	-- Artifacts table for user-provided and agent-generated files
	CREATE TABLE IF NOT EXISTS artifacts (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		path TEXT NOT NULL,
		source TEXT NOT NULL,
		source_agent_id TEXT,
		purpose TEXT,
		content_type TEXT NOT NULL,
		size_bytes INTEGER NOT NULL,
		checksum TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		last_accessed_at INTEGER,
		access_count INTEGER DEFAULT 0,
		tags TEXT,
		metadata_json TEXT,
		deleted_at INTEGER
	);

	-- Artifacts indexes
	CREATE INDEX IF NOT EXISTS idx_artifacts_name ON artifacts(name);
	CREATE INDEX IF NOT EXISTS idx_artifacts_source ON artifacts(source);
	CREATE INDEX IF NOT EXISTS idx_artifacts_content_type ON artifacts(content_type);
	CREATE INDEX IF NOT EXISTS idx_artifacts_created ON artifacts(created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_artifacts_deleted ON artifacts(deleted_at);

	-- FTS5 virtual table for artifact search
	CREATE VIRTUAL TABLE IF NOT EXISTS artifacts_fts5 USING fts5(
		artifact_id UNINDEXED,
		name,
		purpose,
		tags,
		tokenize='porter unicode61'
	);

	-- Sync triggers for artifacts FTS5
	CREATE TRIGGER IF NOT EXISTS artifacts_fts5_insert AFTER INSERT ON artifacts
	BEGIN
		INSERT INTO artifacts_fts5(artifact_id, name, purpose, tags)
		VALUES (NEW.id, NEW.name, NEW.purpose, NEW.tags);
	END;

	CREATE TRIGGER IF NOT EXISTS artifacts_fts5_update AFTER UPDATE ON artifacts
	BEGIN
		DELETE FROM artifacts_fts5 WHERE artifact_id = OLD.id;
		INSERT INTO artifacts_fts5(artifact_id, name, purpose, tags)
		VALUES (NEW.id, NEW.name, NEW.purpose, NEW.tags);
	END;

	CREATE TRIGGER IF NOT EXISTS artifacts_fts5_delete AFTER DELETE ON artifacts
	BEGIN
		DELETE FROM artifacts_fts5 WHERE artifact_id = OLD.id;
	END;
	`

	_, err := s.db.ExecContext(ctx, schema)
	if err != nil {
		if span != nil {
			span.RecordError(err)
		}
		return err
	}

	// Migration: Add tool_use_id column to existing tables (safe for new databases too)
	// SQLite doesn't error if the column already exists with "ADD COLUMN IF NOT EXISTS" syntax
	// but that syntax isn't supported in older SQLite versions, so we check first
	var columnCount int
	checkQuery := `SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name='tool_use_id'`
	if err := s.db.QueryRowContext(ctx, checkQuery).Scan(&columnCount); err == nil && columnCount == 0 {
		migration := `ALTER TABLE messages ADD COLUMN tool_use_id TEXT`
		if _, err := s.db.ExecContext(ctx, migration); err != nil {
			// Log but don't fail - column might already exist from another process
			if span != nil {
				span.RecordError(fmt.Errorf("migration warning (non-fatal): %w", err))
			}
		} else {
			if span != nil {
				span.SetAttribute("migration_applied", "tool_use_id_column")
			}
		}
	}

	// Migration: Add agent_id, parent_session_id, session_context columns for cross-session memory
	agentMemoryMigrations := map[string]string{
		"agent_id":          "ALTER TABLE sessions ADD COLUMN agent_id TEXT",
		"parent_session_id": "ALTER TABLE sessions ADD COLUMN parent_session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL",
		"session_context":   "ALTER TABLE messages ADD COLUMN session_context TEXT DEFAULT 'direct'",
	}

	for columnName, migration := range agentMemoryMigrations {
		// Check if column exists
		var table string
		if columnName == "session_context" {
			table = "messages"
		} else {
			table = "sessions"
		}

		checkQuery := fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name='%s'", table, columnName)
		var count int
		if err := s.db.QueryRowContext(ctx, checkQuery).Scan(&count); err == nil && count == 0 {
			if _, err := s.db.ExecContext(ctx, migration); err != nil {
				// Log but don't fail - column might already exist from another process
				if span != nil {
					span.RecordError(fmt.Errorf("migration warning (non-fatal) for %s: %w", columnName, err))
				}
			} else {
				if span != nil {
					span.SetAttribute(fmt.Sprintf("migration_applied_%s", columnName), "true")
				}
			}
		}
	}

	// Backfill FTS5 index for existing messages (one-time operation)
	// This is safe to run on every startup - it only fills if FTS5 is empty
	if err := s.backfillFTS5(ctx); err != nil {
		// Log warning but don't fail - FTS5 will populate on new messages
		if span != nil {
			span.RecordError(fmt.Errorf("fts5_backfill warning (non-fatal): %w", err))
		}
		// Also log to stderr for immediate visibility
		fmt.Fprintf(os.Stderr, "WARNING: FTS5 backfill failed (non-fatal): %v\n", err)
		fmt.Fprintf(os.Stderr, "         Semantic search may not find messages created before this session.\n")
		fmt.Fprintf(os.Stderr, "         New messages will be indexed automatically.\n")
	} else {
		// Log successful backfill
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages_fts5").Scan(&count); err == nil {
			if span != nil {
				span.SetAttribute("fts5_index_size", fmt.Sprintf("%d", count))
			}
			if count > 0 {
				fmt.Fprintf(os.Stderr, "FTS5 semantic search index ready (%d messages indexed)\n", count)
			}
		}
	}

	if span != nil {
		span.SetAttribute("tables_created", "3")
	}
	return nil
}

// SaveSession persists a session to the database.
func (s *SessionStore) SaveSession(ctx context.Context, session *Session) error {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.save_session")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", session.ID)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Serialize context
	contextJSON, err := json.Marshal(session.Context)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to marshal context: %w", err)
	}

	query := `
		INSERT INTO sessions (id, agent_id, parent_session_id, context_json, created_at, updated_at, total_cost_usd, total_tokens)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			agent_id = excluded.agent_id,
			parent_session_id = excluded.parent_session_id,
			context_json = excluded.context_json,
			updated_at = excluded.updated_at,
			total_cost_usd = excluded.total_cost_usd,
			total_tokens = excluded.total_tokens
	`

	// Handle NULL for empty agent fields (SQLite compatibility)
	var agentID, parentSessionID interface{}
	if session.AgentID != "" {
		agentID = session.AgentID
	}
	if session.ParentSessionID != "" {
		parentSessionID = session.ParentSessionID
	}

	_, err = s.db.ExecContext(ctx, query,
		session.ID,
		agentID,
		parentSessionID,
		string(contextJSON),
		session.CreatedAt.Unix(),
		session.UpdatedAt.Unix(),
		session.TotalCostUSD,
		session.TotalTokens,
	)

	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to save session: %w", err)
	}

	span.SetAttribute("cost_usd", fmt.Sprintf("%.4f", session.TotalCostUSD))
	span.SetAttribute("total_tokens", fmt.Sprintf("%d", session.TotalTokens))
	return nil
}

// LoadSession loads a session from the database.
func (s *SessionStore) LoadSession(ctx context.Context, sessionID string) (*Session, error) {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.load_session")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)

	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, agent_id, parent_session_id, context_json, created_at, updated_at, total_cost_usd, total_tokens
		FROM sessions
		WHERE id = ?
	`

	row := s.db.QueryRowContext(ctx, query, sessionID)

	var session Session
	var contextJSON string
	var createdAt, updatedAt int64
	var agentID, parentSessionID sql.NullString

	err := row.Scan(
		&session.ID,
		&agentID,
		&parentSessionID,
		&contextJSON,
		&createdAt,
		&updatedAt,
		&session.TotalCostUSD,
		&session.TotalTokens,
	)

	// Populate agent fields from nullable database values
	if agentID.Valid {
		session.AgentID = agentID.String
	}
	if parentSessionID.Valid {
		session.ParentSessionID = parentSessionID.String
	}

	if err == sql.ErrNoRows {
		span.SetAttribute("found", "false")
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to load session: %w", err)
	}

	// Deserialize context
	if err := json.Unmarshal([]byte(contextJSON), &session.Context); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to unmarshal context: %w", err)
	}

	session.CreatedAt = time.Unix(createdAt, 0)
	session.UpdatedAt = time.Unix(updatedAt, 0)

	// Load messages
	messages, err := s.LoadMessages(ctx, sessionID)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to load messages: %w", err)
	}
	session.Messages = messages

	span.SetAttribute("found", "true")
	span.SetAttribute("message_count", fmt.Sprintf("%d", len(messages)))
	return &session, nil
}

// SaveMessage persists a message to the database.
func (s *SessionStore) SaveMessage(ctx context.Context, sessionID string, msg Message) error {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.save_message")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)
	span.SetAttribute("role", msg.Role)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Serialize tool calls if present
	var toolCallsJSON *string
	if len(msg.ToolCalls) > 0 {
		data, err := json.Marshal(msg.ToolCalls)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to marshal tool calls: %w", err)
		}
		jsonStr := string(data)
		toolCallsJSON = &jsonStr
	}

	// Handle tool_use_id (for tool role messages)
	var toolUseID *string
	if msg.ToolUseID != "" {
		toolUseID = &msg.ToolUseID
	}

	// Serialize tool result if present
	var toolResultJSON *string
	if msg.ToolResult != nil {
		data, err := json.Marshal(msg.ToolResult)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to marshal tool result: %w", err)
		}
		jsonStr := string(data)
		toolResultJSON = &jsonStr
	}

	// Default to 'direct' if session_context is not set
	sessionContext := msg.SessionContext
	if sessionContext == "" {
		sessionContext = types.SessionContextDirect
	}

	query := `
		INSERT INTO messages (session_id, role, content, tool_calls_json, tool_use_id, tool_result_json, session_context, timestamp, token_count, cost_usd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.ExecContext(ctx, query,
		sessionID,
		msg.Role,
		msg.Content,
		toolCallsJSON,
		toolUseID,
		toolResultJSON,
		string(sessionContext),
		msg.Timestamp.Unix(),
		msg.TokenCount,
		msg.CostUSD,
	)

	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to save message: %w", err)
	}

	span.SetAttribute("tokens", fmt.Sprintf("%d", msg.TokenCount))
	span.SetAttribute("cost_usd", fmt.Sprintf("%.4f", msg.CostUSD))
	return nil
}

// LoadMessages loads all messages for a session.
func (s *SessionStore) LoadMessages(ctx context.Context, sessionID string) ([]Message, error) {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.load_messages")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)

	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, role, content, tool_calls_json, tool_use_id, tool_result_json, session_context, timestamp, token_count, cost_usd
		FROM messages
		WHERE session_id = ?
		ORDER BY timestamp ASC
	`

	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		var msgID int64
		var toolCallsJSON, toolUseID, toolResultJSON *string
		var sessionContext sql.NullString
		var timestamp int64

		err := rows.Scan(
			&msgID,
			&msg.Role,
			&msg.Content,
			&toolCallsJSON,
			&toolUseID,
			&toolResultJSON,
			&sessionContext,
			&timestamp,
			&msg.TokenCount,
			&msg.CostUSD,
		)

		// Populate session_context from nullable database value
		if sessionContext.Valid {
			msg.SessionContext = types.SessionContext(sessionContext.String)
		} else {
			msg.SessionContext = types.SessionContextDirect // Default
		}
		if err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}

		// Convert integer ID to string
		msg.ID = fmt.Sprintf("%d", msgID)
		msg.Timestamp = time.Unix(timestamp, 0)

		// Deserialize tool calls
		if toolCallsJSON != nil {
			if err := json.Unmarshal([]byte(*toolCallsJSON), &msg.ToolCalls); err != nil {
				span.RecordError(err)
				return nil, fmt.Errorf("failed to unmarshal tool calls: %w", err)
			}
		}

		// Load tool_use_id
		if toolUseID != nil {
			msg.ToolUseID = *toolUseID
		}

		// Deserialize tool result
		if toolResultJSON != nil {
			if err := json.Unmarshal([]byte(*toolResultJSON), &msg.ToolResult); err != nil {
				span.RecordError(err)
				return nil, fmt.Errorf("failed to unmarshal tool result: %w", err)
			}
		}

		messages = append(messages, msg)
	}

	if err := rows.Err(); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("error iterating messages: %w", err)
	}

	span.SetAttribute("message_count", fmt.Sprintf("%d", len(messages)))
	return messages, nil
}

// LoadAgentSessions loads all sessions for a given agent.
// Returns sessions where agent_id matches the provided agentID.
func (s *SessionStore) LoadAgentSessions(ctx context.Context, agentID string) ([]string, error) {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.load_agent_sessions")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("agent_id", agentID)

	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id
		FROM sessions
		WHERE agent_id = ?
		ORDER BY updated_at DESC
	`

	rows, err := s.db.QueryContext(ctx, query, agentID)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to query agent sessions: %w", err)
	}
	defer rows.Close()

	var sessionIDs []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to scan session ID: %w", err)
		}
		sessionIDs = append(sessionIDs, sessionID)
	}

	if err := rows.Err(); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("error iterating sessions: %w", err)
	}

	span.SetAttribute("session_count", fmt.Sprintf("%d", len(sessionIDs)))
	return sessionIDs, nil
}

// LoadMessagesForAgent loads all messages for an agent across all its sessions.
// This includes messages from:
// - All sessions owned by this agent (agent_id = agentID)
// - Parent sessions (if agent has coordinator parent)
// Filters by session_context to include only relevant messages (coordinator, shared).
func (s *SessionStore) LoadMessagesForAgent(ctx context.Context, agentID string) ([]Message, error) {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.load_messages_for_agent")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("agent_id", agentID)

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Query gets messages from:
	// 1. All sessions owned by this agent (all contexts)
	// 2. Parent sessions of sessions owned by this agent (coordinator, shared only)
	query := `
		SELECT DISTINCT m.id, m.role, m.content, m.tool_calls_json, m.tool_use_id,
		       m.tool_result_json, m.session_context, m.timestamp, m.token_count, m.cost_usd,
		       m.session_id
		FROM messages m
		WHERE (
			-- Messages from sessions owned by this agent (all contexts)
			m.session_id IN (SELECT id FROM sessions WHERE agent_id = ?)
		) OR (
			-- Messages from parent sessions (coordinator and shared only)
			m.session_id IN (
				SELECT parent_session_id FROM sessions
				WHERE agent_id = ? AND parent_session_id IS NOT NULL
			)
			AND m.session_context IN ('coordinator', 'shared')
		)
		ORDER BY m.timestamp ASC
	`

	rows, err := s.db.QueryContext(ctx, query, agentID, agentID)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to query agent messages: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		var msgID int64
		var sessionID string
		var toolCallsJSON, toolUseID, toolResultJSON *string
		var sessionContext sql.NullString
		var timestamp int64

		err := rows.Scan(
			&msgID,
			&msg.Role,
			&msg.Content,
			&toolCallsJSON,
			&toolUseID,
			&toolResultJSON,
			&sessionContext,
			&timestamp,
			&msg.TokenCount,
			&msg.CostUSD,
			&sessionID,
		)
		if err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}

		// Populate fields
		msg.ID = fmt.Sprintf("%d", msgID)
		msg.Timestamp = time.Unix(timestamp, 0)

		if sessionContext.Valid {
			msg.SessionContext = types.SessionContext(sessionContext.String)
		} else {
			msg.SessionContext = types.SessionContextDirect
		}

		// Deserialize tool calls
		if toolCallsJSON != nil {
			if err := json.Unmarshal([]byte(*toolCallsJSON), &msg.ToolCalls); err != nil {
				span.RecordError(err)
				return nil, fmt.Errorf("failed to unmarshal tool calls: %w", err)
			}
		}

		// Load tool_use_id
		if toolUseID != nil {
			msg.ToolUseID = *toolUseID
		}

		// Deserialize tool result
		if toolResultJSON != nil {
			if err := json.Unmarshal([]byte(*toolResultJSON), &msg.ToolResult); err != nil {
				span.RecordError(err)
				return nil, fmt.Errorf("failed to unmarshal tool result: %w", err)
			}
		}

		messages = append(messages, msg)
	}

	if err := rows.Err(); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("error iterating messages: %w", err)
	}

	span.SetAttribute("message_count", fmt.Sprintf("%d", len(messages)))
	return messages, nil
}

// LoadMessagesFromParentSession loads messages from the parent session of a given session.
// This is used by sub-agents to see coordinator instructions.
// Returns empty slice if session has no parent.
func (s *SessionStore) LoadMessagesFromParentSession(ctx context.Context, sessionID string) ([]Message, error) {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.load_messages_from_parent")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)

	s.mu.RLock()
	defer s.mu.RUnlock()

	// First, get the parent_session_id
	var parentSessionID sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT parent_session_id FROM sessions WHERE id = ?", sessionID).
		Scan(&parentSessionID)
	if err != nil {
		if err == sql.ErrNoRows {
			span.SetAttribute("has_parent", "false")
			return []Message{}, nil // No parent session
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to query parent session: %w", err)
	}

	if !parentSessionID.Valid {
		span.SetAttribute("has_parent", "false")
		return []Message{}, nil // No parent session
	}

	span.SetAttribute("parent_session_id", parentSessionID.String)
	span.SetAttribute("has_parent", "true")

	// Load messages from parent session that are relevant to sub-agents
	query := `
		SELECT id, role, content, tool_calls_json, tool_use_id, tool_result_json,
		       session_context, timestamp, token_count, cost_usd
		FROM messages
		WHERE session_id = ?
		AND session_context IN ('coordinator', 'shared')
		ORDER BY timestamp ASC
	`

	rows, err := s.db.QueryContext(ctx, query, parentSessionID.String)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to query parent messages: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		var msgID int64
		var toolCallsJSON, toolUseID, toolResultJSON *string
		var sessionContext sql.NullString
		var timestamp int64

		err := rows.Scan(
			&msgID,
			&msg.Role,
			&msg.Content,
			&toolCallsJSON,
			&toolUseID,
			&toolResultJSON,
			&sessionContext,
			&timestamp,
			&msg.TokenCount,
			&msg.CostUSD,
		)
		if err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to scan parent message: %w", err)
		}

		// Populate fields
		msg.ID = fmt.Sprintf("%d", msgID)
		msg.Timestamp = time.Unix(timestamp, 0)

		if sessionContext.Valid {
			msg.SessionContext = types.SessionContext(sessionContext.String)
		} else {
			msg.SessionContext = types.SessionContextDirect
		}

		// Deserialize tool calls
		if toolCallsJSON != nil {
			if err := json.Unmarshal([]byte(*toolCallsJSON), &msg.ToolCalls); err != nil {
				span.RecordError(err)
				return nil, fmt.Errorf("failed to unmarshal tool calls: %w", err)
			}
		}

		// Load tool_use_id
		if toolUseID != nil {
			msg.ToolUseID = *toolUseID
		}

		// Deserialize tool result
		if toolResultJSON != nil {
			if err := json.Unmarshal([]byte(*toolResultJSON), &msg.ToolResult); err != nil {
				span.RecordError(err)
				return nil, fmt.Errorf("failed to unmarshal tool result: %w", err)
			}
		}

		messages = append(messages, msg)
	}

	if err := rows.Err(); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("error iterating parent messages: %w", err)
	}

	span.SetAttribute("message_count", fmt.Sprintf("%d", len(messages)))
	return messages, nil
}

// SaveToolExecution persists a tool execution to the database.
func (s *SessionStore) SaveToolExecution(ctx context.Context, sessionID string, exec ToolExecution) error {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.save_tool_execution")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)
	span.SetAttribute("tool_name", exec.ToolName)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Serialize input
	inputJSON, err := json.Marshal(exec.Input)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to marshal input: %w", err)
	}

	// Serialize result
	var resultJSON *string
	if exec.Result != nil {
		data, err := json.Marshal(exec.Result)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to marshal result: %w", err)
		}
		jsonStr := string(data)
		resultJSON = &jsonStr
	}

	// Error message - check both Go error and result.Error
	var errMsg *string
	if exec.Error != nil {
		msg := exec.Error.Error()
		errMsg = &msg
	} else if exec.Result != nil && !exec.Result.Success && exec.Result.Error != nil {
		// MCP tools can fail without Go error - extract from result.Error
		msg := fmt.Sprintf("%s: %s", exec.Result.Error.Code, exec.Result.Error.Message)
		errMsg = &msg
	}

	// Execution time
	var execTimeMs *int64
	if exec.Result != nil {
		execTimeMs = &exec.Result.ExecutionTimeMs
	}

	query := `
		INSERT INTO tool_executions (session_id, tool_name, input_json, result_json, error, execution_time_ms, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`

	_, err = s.db.ExecContext(ctx, query,
		sessionID,
		exec.ToolName,
		string(inputJSON),
		resultJSON,
		errMsg,
		execTimeMs,
		time.Now().Unix(),
	)

	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to save tool execution: %w", err)
	}

	if execTimeMs != nil {
		span.SetAttribute("execution_time_ms", fmt.Sprintf("%d", *execTimeMs))
	}
	// Success requires both no Go error AND result.Success == true
	success := exec.Error == nil && (exec.Result == nil || exec.Result.Success)
	span.SetAttribute("success", fmt.Sprintf("%t", success))
	return nil
}

// DeleteSession removes a session and all its associated data.
func (s *SessionStore) DeleteSession(ctx context.Context, sessionID string) error {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.delete_session")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)

	s.mu.Lock()
	// CASCADE delete will remove messages and tool executions
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", sessionID)
	if err != nil {
		s.mu.Unlock()
		span.RecordError(err)
		return fmt.Errorf("failed to delete session: %w", err)
	}

	// Copy hooks to avoid holding lock during callback execution
	hooks := make([]SessionCleanupHook, len(s.cleanupHooks))
	copy(hooks, s.cleanupHooks)
	s.mu.Unlock()

	// Execute cleanup hooks after successful deletion
	// These run outside the lock to prevent deadlocks and improve concurrency
	for _, hook := range hooks {
		hook(ctx, sessionID)
	}

	return nil
}

// ListSessions returns all session IDs.
func (s *SessionStore) ListSessions(ctx context.Context) ([]string, error) {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.list_sessions")
	defer s.tracer.EndSpan(span)

	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, "SELECT id FROM sessions ORDER BY updated_at DESC")
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to query sessions: %w", err)
	}
	defer rows.Close()

	var sessionIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to scan session ID: %w", err)
		}
		sessionIDs = append(sessionIDs, id)
	}

	if err := rows.Err(); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("error iterating sessions: %w", err)
	}

	span.SetAttribute("session_count", fmt.Sprintf("%d", len(sessionIDs)))
	return sessionIDs, nil
}

// SaveMemorySnapshot persists a memory snapshot (L2 summary) to the database.
// This is used by the swap layer to archive L2 summaries when they exceed the token limit.
func (s *SessionStore) SaveMemorySnapshot(ctx context.Context, sessionID, snapshotType, content string, tokenCount int) error {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.save_memory_snapshot")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)
	span.SetAttribute("snapshot_type", snapshotType)
	span.SetAttribute("token_count", fmt.Sprintf("%d", tokenCount))

	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
		INSERT INTO memory_snapshots (session_id, snapshot_type, content, token_count, created_at)
		VALUES (?, ?, ?, ?, ?)
	`

	_, err := s.db.ExecContext(ctx, query,
		sessionID,
		snapshotType,
		content,
		tokenCount,
		time.Now().Unix(),
	)

	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to save memory snapshot: %w", err)
	}

	span.SetAttribute("content_length", fmt.Sprintf("%d", len(content)))
	return nil
}

// MemorySnapshot represents a saved memory snapshot (e.g., L2 summary).
type MemorySnapshot struct {
	ID           int
	SessionID    string
	SnapshotType string
	Content      string
	TokenCount   int
	CreatedAt    time.Time
}

// LoadMemorySnapshots retrieves memory snapshots for a session.
// Returns snapshots in chronological order (oldest first).
// Limit controls the maximum number of snapshots to return (0 = all).
func (s *SessionStore) LoadMemorySnapshots(ctx context.Context, sessionID string, snapshotType string, limit int) ([]MemorySnapshot, error) {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.load_memory_snapshots")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)
	span.SetAttribute("snapshot_type", snapshotType)

	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, session_id, snapshot_type, content, token_count, created_at
		FROM memory_snapshots
		WHERE session_id = ? AND snapshot_type = ?
		ORDER BY created_at ASC
	`

	// Add LIMIT clause if specified
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.db.QueryContext(ctx, query, sessionID, snapshotType)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to query memory snapshots: %w", err)
	}
	defer rows.Close()

	var snapshots []MemorySnapshot
	for rows.Next() {
		var snapshot MemorySnapshot
		var createdAt int64

		err := rows.Scan(
			&snapshot.ID,
			&snapshot.SessionID,
			&snapshot.SnapshotType,
			&snapshot.Content,
			&snapshot.TokenCount,
			&createdAt,
		)
		if err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to scan memory snapshot: %w", err)
		}

		snapshot.CreatedAt = time.Unix(createdAt, 0)
		snapshots = append(snapshots, snapshot)
	}

	if err := rows.Err(); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("error iterating memory snapshots: %w", err)
	}

	span.SetAttribute("snapshot_count", fmt.Sprintf("%d", len(snapshots)))
	return snapshots, nil
}

// Close closes the database connection.
func (s *SessionStore) Close() error {
	return s.db.Close()
}

// RegisterCleanupHook registers a callback to be invoked when sessions are deleted.
// This enables decoupled cleanup operations (e.g., releasing shared memory references)
// without tight coupling between SessionStore and other components.
// Thread-safe: Can be called from multiple goroutines.
func (s *SessionStore) RegisterCleanupHook(hook SessionCleanupHook) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupHooks = append(s.cleanupHooks, hook)
}

// GetStats returns database statistics for monitoring.
func (s *SessionStore) GetStats(ctx context.Context) (*Stats, error) {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.get_stats")
	defer s.tracer.EndSpan(span)

	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := &Stats{}

	// Count sessions
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions").Scan(&stats.SessionCount)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	// Count messages
	err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&stats.MessageCount)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	// Count tool executions
	err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tool_executions").Scan(&stats.ToolExecutionCount)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	// Sum costs
	err = s.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(total_cost_usd), 0) FROM sessions").Scan(&stats.TotalCostUSD)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	// Sum tokens
	err = s.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(total_tokens), 0) FROM sessions").Scan(&stats.TotalTokens)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	span.SetAttribute("sessions", fmt.Sprintf("%d", stats.SessionCount))
	span.SetAttribute("messages", fmt.Sprintf("%d", stats.MessageCount))
	span.SetAttribute("tool_executions", fmt.Sprintf("%d", stats.ToolExecutionCount))

	return stats, nil
}

// Stats holds database statistics.
type Stats struct {
	SessionCount       int
	MessageCount       int
	ToolExecutionCount int
	TotalCostUSD       float64
	TotalTokens        int
}

// backfillFTS5 populates the FTS5 index with existing messages.
// This is a one-time migration operation for databases created before FTS5 was added.
// Safe to run multiple times - only inserts if FTS5 is empty.
func (s *SessionStore) backfillFTS5(ctx context.Context) error {
	// Check if FTS5 already has data
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages_fts5").Scan(&count); err != nil {
		return fmt.Errorf("failed to check FTS5 count: %w", err)
	}

	if count > 0 {
		// Already backfilled
		return nil
	}

	// Backfill from messages table
	query := `
		INSERT INTO messages_fts5(message_id, session_id, role, content, timestamp)
		SELECT id, session_id, role, content, timestamp
		FROM messages
		WHERE content IS NOT NULL AND content != ''
	`

	_, err := s.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to backfill FTS5: %w", err)
	}

	return nil
}

// SearchFTS5 searches message content using FTS5 full-text search with BM25 ranking.
// Returns messages sorted by relevance (highest BM25 score first).
//
// Parameters:
//   - sessionID: Filter results to specific session
//   - query: Natural language search query (FTS5 MATCH syntax)
//   - limit: Maximum number of results to return
//
// Returns messages ordered by BM25 relevance score.
func (s *SessionStore) SearchFTS5(ctx context.Context, sessionID, query string, limit int) ([]Message, error) {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.search_fts5")
	defer s.tracer.EndSpan(span)

	span.SetAttribute("session_id", sessionID)
	span.SetAttribute("query", query)
	span.SetAttribute("limit", fmt.Sprintf("%d", limit))

	// Validate query - return empty results for invalid/empty queries
	if strings.TrimSpace(query) == "" {
		span.SetAttribute("query_validation", "empty_query")
		return []Message{}, nil
	}

	// Convert multi-word query to FTS5 OR query for semantic search
	// "SQL database optimization" -> "SQL OR database OR optimization"
	// This matches any document containing any of the search terms
	fts5Query := convertToFTS5Query(query)
	span.SetAttribute("fts5_query", fts5Query)

	s.mu.RLock()
	defer s.mu.RUnlock()

	// FTS5 MATCH query with BM25 ranking
	// bm25(messages_fts5) is FTS5's built-in BM25 ranking function (lower score = more relevant)
	// Note: FTS5 requires the actual table name in bm25(), not an alias
	sqlQuery := `
		SELECT m.role, m.content, m.tool_calls_json,
		       m.tool_use_id, m.tool_result_json, m.timestamp, m.token_count, m.cost_usd
		FROM messages_fts5
		JOIN messages m ON messages_fts5.message_id = m.id
		WHERE messages_fts5.session_id = ? AND messages_fts5.content MATCH ?
		ORDER BY bm25(messages_fts5)
		LIMIT ?
	`

	rows, err := s.db.QueryContext(ctx, sqlQuery, sessionID, fts5Query, limit)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("FTS5 search failed: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		var toolCallsJSON, toolUseID, toolResultJSON *string
		var timestamp int64

		err := rows.Scan(
			&msg.Role,
			&msg.Content,
			&toolCallsJSON,
			&toolUseID,
			&toolResultJSON,
			&timestamp,
			&msg.TokenCount,
			&msg.CostUSD,
		)
		if err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}

		msg.Timestamp = time.Unix(timestamp, 0)

		// Deserialize tool calls
		if toolCallsJSON != nil {
			if err := json.Unmarshal([]byte(*toolCallsJSON), &msg.ToolCalls); err != nil {
				span.RecordError(fmt.Errorf("failed to unmarshal tool_calls: %w", err))
			}
		}

		// Load tool_use_id
		if toolUseID != nil {
			msg.ToolUseID = *toolUseID
		}

		// Deserialize tool result
		if toolResultJSON != nil {
			if err := json.Unmarshal([]byte(*toolResultJSON), &msg.ToolResult); err != nil {
				span.RecordError(fmt.Errorf("failed to unmarshal tool_result: %w", err))
			}
		}

		messages = append(messages, msg)
	}

	if err := rows.Err(); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("rows iteration error: %w", err)
	}

	span.SetAttribute("results_count", fmt.Sprintf("%d", len(messages)))
	return messages, nil
}

// convertToFTS5Query converts a natural language query into FTS5 MATCH syntax.
// Multi-word queries are converted to OR queries for semantic search flexibility.
// Example: "SQL database optimization" -> "SQL OR database OR optimization"
//
// This allows finding documents that contain ANY of the search terms,
// which is more suitable for semantic search than requiring ALL terms.
func convertToFTS5Query(query string) string {
	words := strings.Fields(strings.TrimSpace(query))
	if len(words) <= 1 {
		// Single word or empty - return as-is
		return query
	}

	// Join words with OR for FTS5 boolean query
	// FTS5 OR operator requires uppercase
	return strings.Join(words, " OR ")
}

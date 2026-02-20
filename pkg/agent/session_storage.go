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

import "context"

// SessionStorage defines the backend-agnostic interface for session persistence.
// Implementations include SQLite (SessionStore) and PostgreSQL (postgres.SessionStore).
// All operations must be safe for concurrent use.
type SessionStorage interface {
	// Sessions
	SaveSession(ctx context.Context, session *Session) error
	LoadSession(ctx context.Context, sessionID string) (*Session, error)
	ListSessions(ctx context.Context) ([]string, error)
	DeleteSession(ctx context.Context, sessionID string) error
	LoadAgentSessions(ctx context.Context, agentID string) ([]string, error)

	// Messages
	SaveMessage(ctx context.Context, sessionID string, msg Message) error
	LoadMessages(ctx context.Context, sessionID string) ([]Message, error)
	LoadMessagesForAgent(ctx context.Context, agentID string) ([]Message, error)
	LoadMessagesFromParentSession(ctx context.Context, sessionID string) ([]Message, error)

	// Search (backend-agnostic: FTS5 for SQLite, tsvector for PostgreSQL)
	SearchMessages(ctx context.Context, sessionID, query string, limit int) ([]Message, error)
	SearchMessagesByAgent(ctx context.Context, agentID, query string, limit int) ([]Message, error)

	// Tool executions
	SaveToolExecution(ctx context.Context, sessionID string, exec ToolExecution) error

	// Memory snapshots
	SaveMemorySnapshot(ctx context.Context, sessionID, snapshotType, content string, tokenCount int) error
	LoadMemorySnapshots(ctx context.Context, sessionID string, snapshotType string, limit int) ([]MemorySnapshot, error)

	// Lifecycle
	RegisterCleanupHook(hook SessionCleanupHook)
	GetStats(ctx context.Context) (*Stats, error)
	Close() error
}

// SoftDeleteStorage defines optional soft-delete operations.
// Not all backends support soft-delete (SQLite does hard delete).
// Use type assertion to check if a SessionStorage supports soft-delete:
//
//	if sds, ok := store.(SoftDeleteStorage); ok {
//	    sds.RestoreSession(ctx, sessionID)
//	}
type SoftDeleteStorage interface {
	// SoftDeleteSession marks a session as deleted without removing data.
	// The session can be restored with RestoreSession.
	SoftDeleteSession(ctx context.Context, sessionID string) error

	// RestoreSession restores a soft-deleted session.
	RestoreSession(ctx context.Context, sessionID string) error

	// PurgeDeleted permanently removes all soft-deleted data older than the grace period.
	PurgeDeleted(ctx context.Context, graceInterval string) error
}

// Compile-time check: SessionStore (SQLite) implements SessionStorage.
var _ SessionStorage = (*SessionStore)(nil)

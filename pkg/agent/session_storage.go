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
	"errors"
	"sync"

	"go.uber.org/zap"
)

// SessionStorage defines the backend-agnostic interface for session persistence.
// Implementations include SQLite (SessionStore) and PostgreSQL (postgres.SessionStore).
// All operations must be safe for concurrent use.
type SessionStorage interface {
	// Sessions
	SaveSession(ctx context.Context, session *Session) error
	// LoadSession returns the caller's session. A session that is not there,
	// or not the caller's, is (nil, nil) or an error that wraps
	// ErrSessionNotFound; the agent's reload (Memory.GetOrCreateSession)
	// then starts the session empty. Any other error is logged at error
	// level, and the session also starts empty in memory.
	LoadSession(ctx context.Context, sessionID string) (*Session, error)
	ListSessions(ctx context.Context) ([]string, error)
	DeleteSession(ctx context.Context, sessionID string) error
	LoadAgentSessions(ctx context.Context, agentID string) ([]string, error)

	// SessionExists reports whether any session row has this id, regardless
	// of owner. It is an authorization primitive, not a data read: resume
	// gates use it to distinguish exists-but-foreign (must deny) from absent
	// (safe to create) — an owner-scoped read cannot tell those apart, and
	// treating foreign as absent would let a caller squat another user's
	// session id with a fresh session under their own identity.
	SessionExists(ctx context.Context, sessionID string) (bool, error)

	// Messages
	//
	// SaveMessage derives the row's turn at insert (HLD §4.5): the turnStart
	// site — the Chat()-entry user message, the only turn-incrementing event —
	// computes turn = COALESCE(MAX(turn),0)+1 for the session; every other site
	// uses the same subquery without the +1. The store stamps the derived seq
	// and turn back onto msg.
	SaveMessage(ctx context.Context, sessionID string, msg *Message, turnStart bool) error

	// LoadMessages and LoadSession return the session's unfolded rows, seq
	// ascending, each assistant row with every call its tool_calls_json
	// holds. A store does not repair the rows after a fold: when a fold
	// keeps an assistant row for its words, the row's stored calls stay and
	// only their results are folded. The agent's reload
	// (Memory.GetOrCreateSession, dropFoldedCalls) reads the folded results
	// through ListMessagesBySeqRange and removes those calls itself, so every
	// SessionStorage gets the same context back.
	LoadMessages(ctx context.Context, sessionID string) ([]Message, error)

	// ListMessagesBySeqRange is the by-seq span read backing recall (HLD §6):
	// rows lo..hi inclusive, session-filtered, seq ascending, folded included
	// (a summary-cited span is exactly what recall retrieves).
	ListMessagesBySeqRange(ctx context.Context, sessionID string, lo, hi int64) ([]Message, error)
	LoadMessagesForAgent(ctx context.Context, agentID string) ([]Message, error)
	LoadMessagesFromParentSession(ctx context.Context, sessionID string) ([]Message, error)

	// Search (backend-agnostic: FTS5 for SQLite, tsvector for PostgreSQL)
	SearchMessages(ctx context.Context, sessionID, query string, limit int) ([]Message, error)
	SearchMessagesByAgent(ctx context.Context, agentID, query string, limit int) ([]Message, error)

	// Tool executions
	SaveToolExecution(ctx context.Context, sessionID string, exec ToolExecution) error

	// Relief transaction (HLD §5.2 — write-once flags, one transaction per
	// operation):
	//
	// FoldMessages writes summary version n (snapshot_type='fold', content =
	// JSON {"n","text"}) and sets the region's folded flags in ONE transaction
	// (HLD §5.4.6) — a fold never stands in memory without its row.
	FoldMessages(ctx context.Context, sessionID string, seqs []int64, n int, text string) error

	// Memory snapshots
	SaveMemorySnapshot(ctx context.Context, sessionID, snapshotType, content string, tokenCount int) error
	LoadMemorySnapshots(ctx context.Context, sessionID string, snapshotType string, limit int) ([]MemorySnapshot, error)

	// Lifecycle
	RegisterCleanupHook(hook SessionCleanupHook)
	GetStats(ctx context.Context) (*Stats, error)
	Close() error
}

// CostStorage is the optional operation a store offers when it can sum what a
// session cost where the rows are kept, so a host shows a cost without loading
// the messages. loom's SQLite and Postgres stores implement it; use a type
// assertion:
//
//	if cs, ok := store.(CostStorage); ok {
//	    usd, err := cs.SessionCost(ctx, sessionID)
//	}
type CostStorage interface {
	// SessionCost is the sum of cost_usd over every message of the session,
	// the rows relief folded away included: a fold hides a row from the
	// model, not from the ledger. A session the caller does not own reads as
	// zero, as it reads as missing everywhere else.
	SessionCost(ctx context.Context, sessionID string) (float64, error)
}

// ResultRewriter is the optional operation a store offers to replace the
// content of tool result rows. It exists for one case: a step whose results
// together are too large for the context is answered by replacing them with a
// note the model can act on, and the stored rows must say what the model was
// sent. loom's SQLite and Postgres stores implement it; a row the caller does
// not own is left unchanged. A call that names rows but changes none returns
// an error wrapping ErrNoToolResultsRewritten, so the caller
// (SegmentedMemory.ReplaceInFlightResults) warns that memory and store now
// differ instead of taking the rewrite as done.
type ResultRewriter interface {
	RewriteToolResults(ctx context.Context, sessionID string, seqs []int64, content string) error
}

// ErrSessionNotFound is the error a SessionStorage wraps when LoadSession
// finds no session with the id for the caller. Memory.GetOrCreateSession
// takes it as a new session and logs nothing; any other load error is
// logged.
var ErrSessionNotFound = errors.New("session not found")

// ErrNoToolResultsRewritten is the error a ResultRewriter wraps when it was
// given rows to rewrite and changed none of them: the caller does not own
// the session (for example a context without the owner's user id), or no
// given row is a tool result of the session. A reload then sends the stored
// results, not the note the model was sent.
var ErrNoToolResultsRewritten = errors.New("no tool result row was rewritten")

// RowUsageAdder is the optional operation a store offers to add what LLM
// calls cost to a row it already holds. It exists for one case: a turn that
// ends on an error after calls that the provider billed but no row holds yet
// (a fold call, which writes no row; a reply the loop asked again for). The
// agent then adds their cost to the turn's last stored row, so the session's
// rows still sum to what the provider billed and no new row is written. It
// adds the figures a carried row gets (carryFoldCost): cost_usd, token_count
// (TotalTokens), output_tokens and thinking_tokens; the input figures stay the
// row's own. loom's SQLite and Postgres stores implement it; a row the caller
// does not own is left unchanged and is an error. A store without it loses
// that amount from its rows, and the agent logs that once.
type RowUsageAdder interface {
	AddRowUsage(ctx context.Context, sessionID string, seq int64, u Usage) error
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

// warnOnce logs msg the first time key is seen in this process.
func warnOnce(key, msg string, fields ...zap.Field) {
	if _, done := warnedOnce.LoadOrStore(key, true); done {
		return
	}
	zap.L().Warn(msg, fields...)
}

var warnedOnce sync.Map

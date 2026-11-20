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
	"errors"
	"fmt"

	"go.uber.org/zap"
)

// A session can be reached by more than one agent object — a loop's step and
// the conversation a person is connected to — and by more than one process.
// The session lease (session_lease.go) lets one turn at a time run in it. An
// agent object holds each session it has loaded in memory; the turn count says
// how many turns the session has had, so an object whose copy is from fewer
// turns ago — another object or process took one since — drops it and loads
// fresh. A store without the counter runs with copies trusted as they are.

// TurnCounter is a session store that counts a session's turns.
type TurnCounter interface {
	// Turns is how many turns the session has had; 0 for a session with no
	// row yet.
	Turns(ctx context.Context, sessionID string) (int64, error)
	// CountTurn counts one more turn for the session. A session with no row
	// is not counted: its first turn's persist gives it the row.
	CountTurn(ctx context.Context, sessionID string) error
}

// beginTurnCount reads the session's turn count as a turn begins and drops
// this object's copy of the session when it is from another count. It
// returns the count the turn began at.
func (a *Agent) beginTurnCount(ctx context.Context, sessionID string) int64 {
	store := a.turnCounter()
	if store == nil {
		return 0
	}
	turns, err := store.Turns(ctx, sessionID)
	if err != nil {
		zap.L().Warn("could not read the session's turn count; keeping this copy",
			zap.String("session_id", sessionID), zap.Error(err))
		return 0
	}
	if turns == 0 {
		// No turn has run in the session, so no copy of it can be behind:
		// a session this object just created is current as it stands.
		a.memory.sawTurns(sessionID, 0)
		return 0
	}
	if a.memory.staleCopy(sessionID, turns) {
		a.memory.DeleteSession(sessionID)
	}
	return turns
}

// endTurnCount counts the turn and records that this object's copy of the
// session is current as of the new count.
func (a *Agent) endTurnCount(ctx context.Context, sessionID string, began int64) {
	store := a.turnCounter()
	if store == nil {
		return
	}
	if err := store.CountTurn(ctx, sessionID); err != nil {
		zap.L().Warn("could not count the session's turn",
			zap.String("session_id", sessionID), zap.Error(err))
		return
	}
	a.memory.sawTurns(sessionID, began+1)
}

// turnCounter is the session store's counter, or nil for an agent without a
// memory or with a store that does not count.
func (a *Agent) turnCounter() TurnCounter {
	if a.memory == nil {
		return nil
	}
	store, ok := a.memory.GetStore().(TurnCounter)
	if !ok {
		return nil
	}
	return store
}

func (s *SessionStore) Turns(ctx context.Context, sessionID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var turns int64
	err := s.db.QueryRowContext(ctx,
		`SELECT turns FROM sessions WHERE id = ? AND user_id = ?`,
		sessionID, storeUserID(ctx)).Scan(&turns)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read the turn count of session %s: %w", sessionID, err)
	}
	return turns, nil
}

func (s *SessionStore) CountTurn(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET turns = turns + 1 WHERE id = ? AND user_id = ?`,
		sessionID, storeUserID(ctx))
	return err
}

var _ TurnCounter = (*SessionStore)(nil)

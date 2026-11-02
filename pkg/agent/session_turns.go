// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// TurnLock is a session store that lets one turn at a time run in a session,
// whatever agent object or process runs it. A store without it runs turns
// unlocked.
type TurnLock interface {
	// TakeTurn claims a session for a turn by holder. The claim is taken when
	// nobody holds it, when replacing names the holder now on it (one known
	// to be dead), or when the claim was last refreshed before staleBefore.
	TakeTurn(ctx context.Context, sessionID, holder, replacing string, staleBefore time.Time) (TurnClaim, error)
	// KeepTurn refreshes a held claim, so a long turn is not taken as stale.
	KeepTurn(ctx context.Context, sessionID, holder string) error
	// EndTurn releases a held claim and counts the turn.
	EndTurn(ctx context.Context, sessionID, holder string) error
}

// TurnClaim is what taking a turn found.
type TurnClaim struct {
	// Exists is false for a session with no row yet: its first turn, which
	// nothing else can be in.
	Exists bool
	// Taken is whether this holder has the session now; Holder is who does.
	Taken  bool
	Holder string
	// Turns is how many turns the session has had: an agent holding a copy
	// from fewer turns ago holds a stale one.
	Turns int64
}

func (s *SessionStore) TakeTurn(ctx context.Context, sessionID, holder, replacing string, staleBefore time.Time) (TurnClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user := storeUserID(ctx)
	if _, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET turn_holder = ?, turn_since = ?
		 WHERE id = ? AND user_id = ? AND (turn_holder = '' OR turn_holder = ? OR turn_since < ?)`,
		holder, time.Now().UnixMilli(), sessionID, user, replacing, staleBefore.UnixMilli()); err != nil {
		return TurnClaim{}, fmt.Errorf("take a turn in session %s: %w", sessionID, err)
	}
	claim := TurnClaim{}
	err := s.db.QueryRowContext(ctx,
		`SELECT turn_holder, turns FROM sessions WHERE id = ? AND user_id = ?`,
		sessionID, user).Scan(&claim.Holder, &claim.Turns)
	if errors.Is(err, sql.ErrNoRows) {
		return TurnClaim{}, nil
	}
	if err != nil {
		return TurnClaim{}, fmt.Errorf("read the turn of session %s: %w", sessionID, err)
	}
	claim.Exists, claim.Taken = true, claim.Holder == holder
	return claim, nil
}

func (s *SessionStore) KeepTurn(ctx context.Context, sessionID, holder string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET turn_since = ? WHERE id = ? AND user_id = ? AND turn_holder = ?`,
		time.Now().UnixMilli(), sessionID, storeUserID(ctx), holder)
	return err
}

func (s *SessionStore) EndTurn(ctx context.Context, sessionID, holder string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET turn_holder = '', turn_since = 0, turns = turns + 1
		 WHERE id = ? AND user_id = ? AND turn_holder = ?`,
		sessionID, storeUserID(ctx), holder)
	return err
}

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/teradata-labs/loom/pkg/types"
)

// ThreadMessage is a message together with the session it was said in.
//
// A message on its own does not say which session it came from, and a thread is
// several sessions interleaved — so without this the caller could order the
// conversation but not tell who was speaking where.
type ThreadMessage struct {
	Message
	SessionID string
}

// LoadThreadMessages reads one conversation that several agents spoke in.
//
// A conversation is a root session plus a session per agent that was handed it,
// so what a person saw on screen is spread across rows that no single
// session-scoped read returns. This gathers a named set and puts it back in the
// order it happened.
//
// The set is passed in rather than derived from parent_session_id, because being
// a child of the root is not the same as being part of the conversation: work
// given to an agent that was never handed the conversation writes into a child
// session too, and that belongs in its own view, not here.
//
// Ordering is by message id, which is one autoincrement across every session and
// so is monotonic across the whole set. Timestamps are stored to the second and
// several messages routinely share one, which makes them unusable for this.
//
// Only `direct` messages are returned. A `coordinator` row is machinery — a
// report handed back on a disconnect — written so the next agent can read it,
// never so a person can.
func LoadThreadMessages(ctx context.Context, store SessionStorage, sessionIDs []string) ([]ThreadMessage, error) {
	seen := make(map[string]bool, len(sessionIDs))
	var out []ThreadMessage
	for _, id := range sessionIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true

		msgs, err := store.LoadMessages(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, m := range msgs {
			// A thread is what was said to and by the people in it: the person's
			// words, the agents' replies, and what a loop sent in — its context
			// tag is its raise key.
			if !inThread(m.SessionContext) {
				continue
			}
			out = append(out, ThreadMessage{Message: m, SessionID: id})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return messageSeq(out[i].ID) < messageSeq(out[j].ID)
	})
	return out, nil
}

// inThread is which messages belong to a thread: everything said to and by
// the people in it, and the traffic between the parts that the host folds
// away — a loop's raise, a hand-up, a tick, a report handed back, what the
// host itself reports — and the turn in which the agent then speaks to the
// person about it. Only what is shared across sessions for another agent's
// memory is left out.
func inThread(c types.SessionContext) bool {
	switch {
	case c == "", c == types.SessionContextDirect, c == types.SessionContextCoordinator, c == "tick",
		c == "tera-code", c == "speak":
		return true
	case strings.HasPrefix(string(c), "loop:"), strings.HasPrefix(string(c), "handup:"):
		return true
	}
	return false
}

// messageSeq reads the store's autoincrement out of a message id. An id that is
// not a number sorts first and keeps its relative position, which is what a
// stable sort gives it — better than dropping a message because its id was
// minted somewhere unexpected.
func messageSeq(id string) int64 {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// SetHolding records which run has a conversation. An empty runID hands it back.
func (s *SessionStore) SetHolding(ctx context.Context, sessionID, runID string) error {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.set_holding")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)
	span.SetAttribute("holding", runID)

	if owned, err := s.ownsSession(ctx, sessionID); err != nil {
		return err
	} else if !owned {
		return fmt.Errorf("session %s not found", sessionID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET holding = ? WHERE id = ? AND user_id = ?`,
		runID, sessionID, storeUserID(ctx))
	return err
}

// Holding returns the run that has this conversation, or "" for nobody — which
// means the conversation's own agent answers.
func (s *SessionStore) Holding(ctx context.Context, sessionID string) (string, error) {
	if owned, err := s.ownsSession(ctx, sessionID); err != nil {
		return "", err
	} else if !owned {
		return "", nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	var holding string
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(holding, '') FROM sessions WHERE id = ? AND user_id = ?`,
		sessionID, storeUserID(ctx)).Scan(&holding)
	if err != nil {
		return "", nil // absent reads as nobody holding it
	}
	return holding, nil
}

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"sync"
	"time"
)

// sessionTurnWaitReportThreshold is the shortest wait for the session turn
// slot that is recorded on the turn span; an uncontended acquire takes
// microseconds and is not worth an attribute.
const sessionTurnWaitReportThreshold = 10 * time.Millisecond

// sessionTurnLock is the per-session turn slot. A buffered channel of one
// rather than a sync.Mutex so a waiter can give up when its context ends.
type sessionTurnLock struct {
	slot chan struct{}
	// refs counts holders plus waiters; guarded by Agent.sessionLocksMu. The
	// entry is removed when it reaches zero, so the map holds only sessions
	// with a turn running or queued.
	refs int
}

// lockSession runs one turn of a session at a time within this Agent: every
// new turn (chat) and every resume of a parked turn (ResumeChat) holds it for
// the whole turn. Without it, a user turn and a workflow injection on the same
// session interleave their assistant and tool rows, and the next compiled
// request can carry a tool_result twice, which providers reject ("each
// tool_use must have a single result").
//
// It blocks until the session is free or ctx ends; on ctx end it returns
// ctx.Err() and holds nothing. The returned release is idempotent.
//
// Scope: one Agent instance in one process. Agents never share it, so a host
// that builds a fresh Agent per call, or spreads a session's turns across
// processes, must serialize them itself.
func (a *Agent) lockSession(ctx context.Context, sessionID string) (func(), error) {
	a.sessionLocksMu.Lock()
	if a.sessionLocks == nil {
		a.sessionLocks = make(map[string]*sessionTurnLock)
	}
	l := a.sessionLocks[sessionID]
	if l == nil {
		l = &sessionTurnLock{slot: make(chan struct{}, 1)}
		a.sessionLocks[sessionID] = l
	}
	l.refs++
	a.sessionLocksMu.Unlock()

	// Take a free slot even if ctx is already done: select picks randomly
	// among ready cases, and a turn must not fail spuriously when the session
	// is idle.
	select {
	case l.slot <- struct{}{}:
	default:
		select {
		case l.slot <- struct{}{}:
		case <-ctx.Done():
			a.dropSessionLockRef(sessionID, l)
			return nil, ctx.Err()
		}
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			<-l.slot
			a.dropSessionLockRef(sessionID, l)
		})
	}, nil
}

// dropSessionLockRef releases one reference and removes the entry when no
// turn holds or awaits it.
func (a *Agent) dropSessionLockRef(sessionID string, l *sessionTurnLock) {
	a.sessionLocksMu.Lock()
	defer a.sessionLocksMu.Unlock()
	l.refs--
	if l.refs == 0 && a.sessionLocks[sessionID] == l {
		delete(a.sessionLocks, sessionID)
	}
}

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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// One turn at a time per session.
//
// A session can be reached by more than one agent object — a loop's step and
// the conversation Tera connects a person to — and by more than one process.
// Two turns writing one session at once interleave its history. So a turn
// claims its session before it touches it and releases it when it ends, on
// every path; a turn that finds the session claimed is refused with
// ErrSessionBusy, and whoever sent it decides whether to wait. The claim is on
// the session's row, so it holds across processes; a claim left by a process
// that died is taken over at once on the same host, or when it goes stale.
//
// An agent object holds each session it has loaded in memory. The claim says
// how many turns the session has had; an object whose copy is from fewer turns
// ago — another object or process took one since — drops it and loads fresh.

// ErrSessionBusy is a turn refused because another turn is running in the
// session.
var ErrSessionBusy = errors.New("session busy")

// errNoSessionRow is a claim that does not create the session finding it has
// no row: there is no history to act on.
var errNoSessionRow = errors.New("session has no row")

// SessionBusyError names the session and who is taking a turn in it.
type SessionBusyError struct {
	SessionID, Holder string
}

func (e *SessionBusyError) Error() string {
	return fmt.Sprintf("session %s is busy: a turn is running in it (%s)", e.SessionID, e.Holder)
}

func (e *SessionBusyError) Is(target error) bool { return target == ErrSessionBusy }

// How a claim stays fresh: refreshed this often while its turn runs, and taken
// over by anyone when not refreshed for this long.
var (
	turnKeepEvery  = 30 * time.Second
	turnStaleAfter = 5 * time.Minute
)

// thisProcess is this process as a claim names it: pid@host:pidns/nonce.
// The pid namespace tells apart processes that share a hostname but not a pid
// space (containers on host networking), so one is never judged dead by
// another; the nonce tells apart two processes that reuse a pid.
var thisProcess = func() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%d@%s:%s/%s", os.Getpid(), host, pidNamespace(), processNonce())
}()

// pidNamespace names this process's pid namespace where the platform has
// them (Linux), and is empty where it does not.
func pidNamespace() string {
	ns, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return ""
	}
	return ns
}

func processNonce() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// turnSerial tells this process's claims apart, so a claim one of its turns
// left behind is not mistaken for another's.
var turnSerial atomic.Int64

// takeTurn claims a session for a turn and returns how to release it. It
// refuses with ErrSessionBusy when another turn holds the session, and drops
// this agent's copy of the session when another has taken a turn in it since.
//
// create says whether a session with no row yet gets one, which only a turn
// that writes a message should do. Without it, a session that has no row is
// reported with errNoSessionRow and nothing is created.
func (a *Agent) takeTurn(ctx context.Context, sessionID string, create bool) (func(), error) {
	store, ok := a.memory.GetStore().(TurnLock)
	if !ok || sessionID == "" {
		return func() {}, nil
	}
	holder := fmt.Sprintf("%s#%d", thisProcess, turnSerial.Add(1))
	claim, err := store.TakeTurn(ctx, sessionID, holder, "", time.Now().Add(-turnStaleAfter))
	if err != nil {
		return nil, err
	}
	if claim.Exists && !claim.Taken && deadHere(claim.Holder) {
		claim, err = store.TakeTurn(ctx, sessionID, holder, claim.Holder, time.Now().Add(-turnStaleAfter))
		if err != nil {
			return nil, err
		}
	}
	if !claim.Exists && !create {
		return nil, errNoSessionRow
	}
	if !claim.Exists {
		// A first turn: give the session its row, then claim it like any
		// other. A second first turn racing this one finds the row and is
		// refused, and this turn's count is recorded, so its own copy of the
		// session is current at the next turn.
		session := a.memory.GetOrCreateSessionWithAgent(ctx, sessionID, a.config.Name, "")
		if err := a.memory.PersistSession(ctx, session); err != nil {
			return nil, fmt.Errorf("create session %s for its first turn: %w", sessionID, err)
		}
		claim, err = store.TakeTurn(ctx, sessionID, holder, "", time.Now().Add(-turnStaleAfter))
		if err != nil {
			return nil, err
		}
		if !claim.Exists {
			// The store did not keep the row — an owner it will not write for.
			return nil, fmt.Errorf("session %s could not be created for its first turn", sessionID)
		}
	}
	if !claim.Taken {
		return nil, &SessionBusyError{SessionID: sessionID, Holder: claim.Holder}
	}
	if a.memory.staleCopy(sessionID, claim.Turns) {
		a.memory.DeleteSession(sessionID)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(turnKeepEvery)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				_ = store.KeepTurn(context.WithoutCancel(ctx), sessionID, holder)
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		// Released even when the turn's context was cancelled: a claim left
		// behind would refuse every turn after it until it went stale.
		held, err := store.EndTurn(context.WithoutCancel(ctx), sessionID, holder)
		if err == nil && held {
			a.memory.sawTurns(sessionID, claim.Turns+1)
			return
		}
		// The claim was taken over while this turn ran (it went stale), or
		// the release could not be confirmed: another turn may have written
		// the session since, so this copy is not known to be current.
		a.memory.DeleteSession(sessionID)
	}, nil
}

// deadHere is a claim held by a process on this host that no longer exists.
// A holder from another host, or from another pid namespace on this host, is
// never judged dead here: its pid means nothing in this process's pid space.
func deadHere(holder string) bool {
	who, _, _ := strings.Cut(holder, "#")
	who, _, _ = strings.Cut(who, "/")
	pidText, where, ok := strings.Cut(who, "@")
	if !ok {
		return false
	}
	// A holder written before claims named their pid namespace has none:
	// it matches only where this process has none either (no namespaces).
	host, ns, _ := strings.Cut(where, ":")
	if here, _ := os.Hostname(); host != here || ns != pidNamespace() {
		return false
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	err = proc.Signal(syscall.Signal(0))
	return errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH)
}

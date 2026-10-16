// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
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

// thisProcess is this process as a claim names it: pid@host.
var thisProcess = func() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%d@%s", os.Getpid(), host)
}()

// turnSerial tells this process's claims apart, so a claim one of its turns
// left behind is not mistaken for another's.
var turnSerial atomic.Int64

// takeTurn claims a session for a turn and returns how to release it. It
// refuses with ErrSessionBusy when another turn holds the session, and drops
// this agent's copy of the session when another has taken a turn in it since.
func (a *Agent) takeTurn(ctx context.Context, sessionID string) (func(), error) {
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
	if !claim.Exists {
		// A first turn: the session has no row for anything else to be in.
		return func() {}, nil
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
		if store.EndTurn(context.WithoutCancel(ctx), sessionID, holder) == nil {
			a.memory.sawTurns(sessionID, claim.Turns+1)
		}
	}, nil
}

// deadHere is a claim held by a process on this host that no longer exists.
func deadHere(holder string) bool {
	pidHost, _, _ := strings.Cut(holder, "#")
	pidText, host, ok := strings.Cut(pidHost, "@")
	if !ok {
		return false
	}
	if here, _ := os.Hostname(); host != here {
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

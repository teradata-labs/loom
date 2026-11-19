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
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/eventlog"
)

// ErrSessionBusy is returned by Chat, ResumeChat, ResumeSession and
// RewindSession on an agent with a session lease store when another holder,
// in this process or another, has the session's lease: a live turn, a resume
// or a rewind is already driving that session's conversation loop.
var ErrSessionBusy = errors.New("session is being driven by another process")

// ErrSessionLeaseLost is the cancellation cause of a turn's context when its
// session lease was taken over (it expired and another process acquired it)
// or could not be renewed before it would expire. The turn stops dispatching
// calls, because the new holder may dispatch the same ones.
var ErrSessionLeaseLost = errors.New("session lease lost")

// sessionLeaseLost returns the cancellation cause when ctx was cancelled
// because its session lease was lost, or nil otherwise. Ordinary cancellation
// (a caller cancelling the turn) returns nil, so its handling is unchanged.
func sessionLeaseLost(ctx context.Context) error {
	if cause := context.Cause(ctx); errors.Is(cause, ErrSessionLeaseLost) {
		return cause
	}
	return nil
}

// sessionLeaseTTL returns the lease TTL this agent uses; tests shorten it.
func (a *Agent) sessionLeaseTTL() time.Duration {
	if a.leaseTTL > 0 {
		return a.leaseTTL
	}
	return eventlog.DefaultSessionLeaseTTL
}

// leaseLostError returns err with ErrSessionLeaseLost in its chain when ctx was
// cancelled because the lease was lost. A blocked LLM call or tool returns
// ctx.Err() (context.Canceled) when that happens, which would otherwise read
// as the caller cancelling and map to CANCELED instead of ABORTED. err's own
// text is kept, but not its chain: context.Canceled must not outrank the cause.
//
// The same rule drops the chain of a structured error (an *ExecutionGap or
// *TurnParkedError, say) when the lease was lost, so errors.As no longer finds
// it. That is intended: the agent's own readers of those errors run before
// this wrap, and the server maps the ResumeSession and ResolveExecutionGap
// RPCs' errors through resumeSessionResponse and resumeStatus, which check for
// a lease error first. The one structured error kept is TurnFailed, so what
// the turn spent before the lease was lost is still reported; its chain is
// the same flat one. A caller that does need another structured error from
// a lease-lost turn should change this deliberately, keeping context.Canceled
// out of the chain, not wrap err with a second %w.
func leaseLostError(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, ErrSessionLeaseLost) {
		return err
	}
	if cause := sessionLeaseLost(ctx); cause != nil {
		flat := fmt.Errorf("%w: %s", cause, err.Error())
		// What the turn spent before the lease was lost is kept: the same
		// flat chain under the cause, inside the TurnFailed that carried it.
		var failed *TurnFailed
		if errors.As(err, &failed) {
			return &TurnFailed{Err: flat, Spent: failed.Spent}
		}
		return flat
	}
	return err
}

// holdSessionLease takes sessionID's lease for one entry into the
// conversation loop (Chat, ResumeChat or ResumeSession), so that only one
// process at a time records events and dispatches calls for the session.
//
// It returns a context that is cancelled with cause ErrSessionLeaseLost if the
// lease is lost, and a release function the caller must defer. The lease is
// renewed every TTL/3 while held. Without a lease store it returns ctx
// unchanged and a release that only counts the turn.
//
// The holder names this process, pid@host, so a lease left by a process that
// died on this host is released and taken within one renewal interval rather
// than after its TTL; a holder elsewhere is waited out. Taking the lease reads the session's turn
// count (TurnCounter) and releasing it counts the turn: an agent object whose
// copy of the session is from fewer turns than the session has had drops it
// and loads fresh.
func (a *Agent) holdSessionLease(ctx context.Context, sessionID string) (context.Context, func(), error) {
	leases := a.leaseStore()
	if leases == nil {
		// No lock to hold; the turn is still counted, so this object's copy
		// of the session is dropped when another object or process has taken
		// a turn in it since.
		turns := a.beginTurnCount(ctx, sessionID)
		return ctx, func() { a.endTurnCount(context.WithoutCancel(ctx), sessionID, turns) }, nil
	}
	ttl := a.sessionLeaseTTL()
	holder := thisProcess + "/" + uuid.NewString()

	// The store stamps the expiry somewhere inside the acquire call, so the
	// lease is vouched for from before the call, not after it returns.
	acquireBegan := time.Now()
	won, err := leases.AcquireSessionLease(ctx, sessionID, holder, ttl)
	if err != nil {
		return nil, nil, fmt.Errorf("taking the session lease: %w", err)
	}
	if !won {
		// A lease held by a process that died on this host would block the
		// session until its TTL ran out. The holder names its process; when
		// that process is gone from this host AND the lease has less than
		// ttl-ttl/3 left, it is dead: release it and take the lease now. A
		// live holder renews every ttl/3, so its lease never has less than
		// ttl-ttl/3 left, and one whose renewals have failed that long stops
		// itself at its next tick (the renewal loop below cancels with
		// ErrSessionLeaseLost once time.Since(lastHeld) >= margin). So the
		// takeover never evicts a holder that would otherwise go on: the
		// second condition is what makes a same-named host — another machine
		// reporting this hostname, where a live holder's pid is absent — safe.
		// A lease that expired between the acquire and the read has no
		// holder: taken on a second acquire.
		current, remaining, err := leases.SessionLeaseHolder(ctx, sessionID)
		if err != nil {
			return nil, nil, fmt.Errorf("reading the session lease: %w", err)
		}
		if current != "" && deadHere(current) && remaining < ttl-ttl/3 {
			if err := leases.ReleaseSessionLease(ctx, sessionID, current); err != nil {
				return nil, nil, fmt.Errorf("releasing a dead process's session lease: %w", err)
			}
			zap.L().Info("session lease: taken from a holder that died on this host",
				zap.String("session_id", sessionID),
				zap.String("previous_holder", current),
				zap.Duration("remaining", remaining),
				zap.Duration("ttl", ttl))
			current = ""
		}
		if current == "" {
			acquireBegan = time.Now()
			if won, err = leases.AcquireSessionLease(ctx, sessionID, holder, ttl); err != nil {
				return nil, nil, fmt.Errorf("taking the session lease: %w", err)
			}
		}
		if !won {
			return nil, nil, fmt.Errorf("session %s: %w", sessionID, ErrSessionBusy)
		}
	}
	turns := a.beginTurnCount(ctx, sessionID)

	leaseCtx, cancel := context.WithCancelCause(ctx)
	// Renewal and release must outlive a cancelled turn, but keep its values
	// (the caller identity the store scopes by).
	storeCtx := context.WithoutCancel(ctx)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(ttl / 3)
		defer ticker.Stop()
		lastHeld := acquireBegan
		// The turn must stop before the lease can expire.
		margin := ttl - ttl/3
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			// A renewal that never returns must not outlive the lease: it would
			// keep the turn dispatching after another process could have taken
			// over, and block release. Each call is bounded to one renewal
			// interval and also to the time left before the safety margin, so
			// a hang after an earlier fast failure cannot run past it. The
			// lease is vouched for from when the call began, not when it
			// returned, since the store stamps the renewal somewhere inside
			// the call.
			began := time.Now()
			budget := min(ttl/3, margin-began.Sub(lastHeld))
			if budget <= 0 {
				cancel(fmt.Errorf("%w: renewal failing", ErrSessionLeaseLost))
				return
			}
			renewCtx, cancelRenew := context.WithTimeout(storeCtx, budget)
			held, err := leases.RenewSessionLease(renewCtx, sessionID, holder, ttl)
			cancelRenew()
			switch {
			case err == nil && held:
				lastHeld = began
			case err == nil:
				cancel(ErrSessionLeaseLost)
				return
			case time.Since(lastHeld) >= margin:
				// Renewal has failed long enough that the lease may expire
				// before the next attempt; stop rather than risk running
				// alongside a new holder.
				cancel(fmt.Errorf("%w: renewal failing: %v", ErrSessionLeaseLost, err))
				return
			default:
				zap.L().Warn("session lease renewal failed; retrying",
					zap.String("session_id", sessionID), zap.Error(err))
			}
		}
	}()

	release := func() {
		close(stop)
		wg.Wait()
		cancel(nil)
		// Bounded so a hung store cannot keep the RPC from returning; the
		// lease expires on its own if the release does not land.
		releaseCtx, cancelRelease := context.WithTimeout(storeCtx, ttl/3)
		defer cancelRelease()
		// The turn is counted while the lease is still held, so the count
		// and the history it stands for change under one lock.
		a.endTurnCount(releaseCtx, sessionID, turns)
		if err := leases.ReleaseSessionLease(releaseCtx, sessionID, holder); err != nil {
			zap.L().Warn("failed to release session lease; it expires on its own",
				zap.String("session_id", sessionID), zap.Duration("ttl", ttl), zap.Error(err))
		}
	}
	return leaseCtx, release, nil
}

// thisProcess is this process as a lease holder names it: pid@host.
var thisProcess = func() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%d@%s", os.Getpid(), host)
}()

// deadHere is a lease holder that names a process on this host which no
// longer exists. A holder from another host, or one this code did not name
// (no pid@host prefix), is never dead here.
func deadHere(holder string) bool {
	pidHost, _, _ := strings.Cut(holder, "/")
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

// leaseStore is where this agent holds session leases: the store attached for
// that (WithSessionLeases), else the event log, which carries the lease too.
func (a *Agent) leaseStore() eventlog.SessionLeaseStore {
	if a.sessionLeases != nil {
		return a.sessionLeases
	}
	if a.eventLog != nil {
		return a.eventLog
	}
	return nil
}

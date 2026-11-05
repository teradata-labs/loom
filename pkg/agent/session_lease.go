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
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/eventlog"
)

// ErrSessionBusy is returned by Chat, ResumeChat and ResumeSession on an agent
// with an event log when another process holds the session's lease: a live
// turn or a resume is already driving that session's conversation loop.
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
// this wrap, and the server's (resumeSessionResponse, resumeStatus) check for
// a lease error first. A caller that does need the structured error from a
// lease-lost turn should change this deliberately, keeping context.Canceled
// out of the chain, not wrap err with a second %w.
func leaseLostError(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, ErrSessionLeaseLost) {
		return err
	}
	if cause := sessionLeaseLost(ctx); cause != nil {
		return fmt.Errorf("%w: %s", cause, err.Error())
	}
	return err
}

// holdSessionLease takes sessionID's lease for one entry into the
// conversation loop (Chat, ResumeChat or ResumeSession), so that only one
// process at a time records events and dispatches calls for the session.
//
// It returns a context that is cancelled with cause ErrSessionLeaseLost if the
// lease is lost, and a release function the caller must defer. The lease is
// renewed every TTL/3 while held. Without an event log it returns ctx
// unchanged and a no-op release.
func (a *Agent) holdSessionLease(ctx context.Context, sessionID string) (context.Context, func(), error) {
	if a.eventLog == nil {
		return ctx, func() {}, nil
	}
	ttl := a.sessionLeaseTTL()
	holder := uuid.NewString()

	// The store stamps the expiry somewhere inside the acquire call, so the
	// lease is vouched for from before the call, not after it returns.
	acquireBegan := time.Now()
	won, err := a.eventLog.AcquireSessionLease(ctx, sessionID, holder, ttl)
	if err != nil {
		return nil, nil, fmt.Errorf("taking the session lease: %w", err)
	}
	if !won {
		return nil, nil, fmt.Errorf("session %s: %w", sessionID, ErrSessionBusy)
	}

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
			held, err := a.eventLog.RenewSessionLease(renewCtx, sessionID, holder, ttl)
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
		if err := a.eventLog.ReleaseSessionLease(releaseCtx, sessionID, holder); err != nil {
			zap.L().Warn("failed to release session lease; it expires on its own",
				zap.String("session_id", sessionID), zap.Duration("ttl", ttl), zap.Error(err))
		}
	}
	return leaseCtx, release, nil
}

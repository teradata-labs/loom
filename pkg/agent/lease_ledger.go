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
	"sync"

	"github.com/teradata-labs/loom/pkg/llm/scheduler"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// The generic RESOURCE_HOLDER binding: backends declare scarce leases through
// lease events on tool results (pkg/shuttle/lease.go); the agent folds them
// into a per-session ledger and mirrors the outcome onto the LLM slot
// scheduler. loom never knows what the leased resource is — Kind and ID are
// backend-defined opaque strings, tracked purely by identity.

// leaseKey identifies one outstanding lease: the backend-defined (Kind, ID)
// pair, matched exactly.
type leaseKey struct {
	kind string
	id   string
}

// leaseLedger tracks, per session, the scarce backend resources the session's
// conversation currently holds. Leases outlive turns (a Teradata session
// handle survives the multi-minute think time between turns), so the ledger
// lives on the agent, keyed by session ID, and is retired with the session
// (DeleteSession / ClearAllSessions) — a leaked entry on a deleted session
// would pin RESOURCE_HOLDER priority forever.
//
// Self-guarded (own mutex, zero value ready): tool executions within one
// conversation are sequential, but distinct sessions apply events
// concurrently on a multi-session agent.
type leaseLedger struct {
	mu   sync.Mutex
	held map[string]map[leaseKey]struct{}
}

// apply folds tool-result lease events into the ledger, in emit order, and
// reports whether the session still holds any lease afterwards. Re-acquire
// of a held lease is idempotent; double-release and release-of-unknown are
// tolerated no-ops — tool results are data, so the ledger never errors and
// a session's count never goes negative.
func (l *leaseLedger) apply(sessionID string, events []shuttle.LeaseEvent) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, ev := range events {
		key := leaseKey{kind: ev.Kind, id: ev.ID}
		switch ev.Action {
		case shuttle.LeaseAcquired:
			if l.held == nil {
				l.held = make(map[string]map[leaseKey]struct{})
			}
			if l.held[sessionID] == nil {
				l.held[sessionID] = make(map[leaseKey]struct{})
			}
			l.held[sessionID][key] = struct{}{}
		case shuttle.LeaseReleased:
			if set := l.held[sessionID]; set != nil {
				delete(set, key)
				if len(set) == 0 {
					delete(l.held, sessionID)
				}
			}
		}
	}
	return len(l.held[sessionID]) > 0
}

// holds reports whether the session has any outstanding lease.
func (l *leaseLedger) holds(sessionID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.held[sessionID]) > 0
}

// forget retires a session's leases. Called from every session-retirement
// path, mirroring sessionToolLedger's lifecycle.
func (l *leaseLedger) forget(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.held, sessionID)
}

// reset drops every session's leases (ClearAllSessions).
func (l *leaseLedger) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held = nil
}

// applyLeaseEvents folds one tool result's backend-declared lease events into
// the session's ledger and mirrors the outcome onto the turn's SlotInfo:
// while the session holds any lease, the conversation's remaining LLM calls
// ride the RESOURCE_HOLDER class; the last release drops them back. Results
// without lease events never touch scheduler state. Events are honored on
// failed results too — the backend's declaration is authoritative (a failing
// tool can still have released its handle).
//
// The ledger read and the mark are not one atomic step, which is safe
// because a conversation's tool calls execute sequentially in the loop; the
// ledger's own mutex covers cross-session concurrency, and sessions never
// share a SlotInfo.
func (a *Agent) applyLeaseEvents(ctx context.Context, sessionID string, res *shuttle.Result) {
	events := shuttle.LeaseEventsFrom(res)
	if len(events) == 0 {
		return
	}
	if a.leases.apply(sessionID, events) {
		scheduler.MarkResourceHolder(ctx)
	} else {
		scheduler.UnmarkResourceHolder(ctx)
	}
}

// seedLeaseHolding starts a turn in the RESOURCE_HOLDER class when the
// session already holds leases from a previous turn: leases outlive turns,
// but the turn's SlotInfo does not (the server installs a fresh one per
// turn), so each turn re-marks from the ledger. No-op without SlotInfo.
func (a *Agent) seedLeaseHolding(ctx context.Context, sessionID string) {
	if a.leases.holds(sessionID) {
		scheduler.MarkResourceHolder(ctx)
	}
}

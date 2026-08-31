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
package shuttle

import (
	"context"
	"time"
)

// Heartbeater is an OPTIONAL capability a Notifier may implement. A notifier
// that implements it is poked periodically for as long as a hold is still
// waiting on a human, so a transport that would otherwise go byte-silent for
// the whole hold can keep its stream alive.
//
// A hold blocks the turn goroutine inside its poll loop and emits nothing
// between the pending notification and the human's decision. On a streaming
// transport that is silence for the whole hold window — up to 300s for an ask
// hold, up to the configured ceiling for contact_human — which is long enough
// for an intermediary to trip its inactivity timeout and tear down the very
// stream the decision has to travel back on.
//
// It is deliberately a separate optional interface rather than a method on
// Notifier: a Notifier that does not implement it is never heartbeaten and
// behaves exactly as before.
type Heartbeater interface {
	// Heartbeat signals that a hold is still pending. It carries no request
	// payload — its only job is to produce traffic. Implementations must be
	// cheap, best-effort, and non-blocking: a heartbeat that blocks stalls the
	// turn goroutine mid-hold and stops the poll that would see the human's
	// decision. The returned error never changes a hold's outcome.
	Heartbeat(ctx context.Context) error
}

// holdHeartbeatInterval is how often a still-pending hold pokes a Heartbeater
// notifier. Shared by both hold origins — the ask resolver and contact_human —
// so the two cannot drift. 30s sits comfortably inside common proxy read
// timeouts while costing at most ~10 events over a default-length hold.
const holdHeartbeatInterval = 30 * time.Second

// holdBeater is the per-hold heartbeat bookkeeping shared by both waiters. It
// is owned by the single goroutine running the hold — the turn's — and carries
// no locking, which is deliberate: the beat is emitted on that goroutine so a
// downstream consumer still receives progress events serially (a background
// ticker would have broken that invariant and forced a mutex into every
// consumer, e.g. grpc.ServerStream.Send is not concurrency-safe).
type holdBeater struct {
	hb       Heartbeater
	interval time.Duration
	last     time.Time
}

// newHoldBeater derives the beater for a hold. Heartbeating is opt-in BY
// CAPABILITY: a notifier that does not implement Heartbeater — or no notifier
// at all — yields a beater that never beats, so the hold stays exactly as
// silent as it was before. Asserting a nil interface yields ok=false, so the
// nil-notifier case needs no separate guard. A non-positive interval disables
// beating.
func newHoldBeater(n Notifier, interval time.Duration) *holdBeater {
	hb, _ := n.(Heartbeater)
	return &holdBeater{hb: hb, interval: interval, last: time.Now()}
}

// beat pokes the notifier when the interval has elapsed since the last poke,
// and is a no-op otherwise. Best-effort throughout: callers invoke it only
// AFTER the resolution poll — so it can never delay a decision the poll already
// saw — and a Heartbeat error never changes a hold's outcome.
func (b *holdBeater) beat(ctx context.Context) {
	if b == nil || b.hb == nil || b.interval <= 0 {
		return
	}
	if time.Since(b.last) < b.interval {
		return
	}
	b.last = time.Now()
	_ = b.hb.Heartbeat(ctx)
}

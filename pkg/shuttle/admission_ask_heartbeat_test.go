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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// plainNotifier implements only Notifier — the pre-heartbeat shape. Used to
// prove a notifier without the capability is never heartbeaten and keeps
// behaving exactly as before.
type plainNotifier struct {
	mu       sync.Mutex
	notifies int
}

func (n *plainNotifier) Notify(context.Context, *HumanRequest) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notifies++
	return nil
}

func (n *plainNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.notifies
}

// beatingNotifier implements Notifier AND Heartbeater, recording both.
type beatingNotifier struct {
	mu         sync.Mutex
	notifies   int
	heartbeats int
	err        error // returned by Heartbeat, to prove errors are ignored
}

func (n *beatingNotifier) Notify(context.Context, *HumanRequest) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notifies++
	return nil
}

func (n *beatingNotifier) Heartbeat(context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.heartbeats++
	return n.err
}

func (n *beatingNotifier) beats() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.heartbeats
}

func (n *beatingNotifier) notifyCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.notifies
}

// askReq builds a minimal Ask request for the resolver under test.
func askReq(ctx context.Context) AdmissionRequest {
	return AdmissionRequest{
		Ctx:       ctx,
		ToolName:  "write_table",
		Params:    map[string]interface{}{"table": "sales"},
		UserID:    "user-1",
		SessionID: "sess-1",
	}
}

// resolveAsync runs Resolve on its own goroutine (it blocks) and returns a
// channel carrying the decision.
func resolveAsync(r *hitlAskResolver, ctx context.Context) <-chan Decision {
	out := make(chan Decision, 1)
	go func() { out <- r.Resolve(askReq(ctx), Decision{Kind: Ask, Reason: "approval required"}) }()
	return out
}

// TestAskResolver_HeartbeatsWhilePending is the core guarantee: a hold that is
// waiting on a human keeps poking a Heartbeater, so the caller's stream never
// goes silent for the whole window.
func TestAskResolver_HeartbeatsWhilePending(t *testing.T) {
	store := NewInMemoryHumanRequestStore()
	notifier := &beatingNotifier{}
	r := &hitlAskResolver{
		store:     store,
		timeout:   5 * time.Second,
		poll:      2 * time.Millisecond,
		notifier:  notifier,
		heartbeat: 15 * time.Millisecond,
	}

	decision := resolveAsync(r, context.Background())

	// Let the hold sit pending across several heartbeat intervals.
	require.Eventually(t, func() bool { return notifier.beats() >= 3 }, 2*time.Second, 5*time.Millisecond,
		"a pending hold must keep heartbeating")

	// The pending card itself is emitted exactly once, no matter how many
	// heartbeats follow — heartbeats must never re-raise the card.
	require.Equal(t, 1, notifier.notifyCount())

	respondOncePending(store, "approved")
	select {
	case d := <-decision:
		require.Equal(t, Allow, d.Kind)
	case <-time.After(3 * time.Second):
		t.Fatal("resolver did not return after approval")
	}
}

// TestAskResolver_HeartbeatStopsWhenHoldResolves proves the beat is bounded by
// the hold: once the decision lands, nothing further is emitted.
func TestAskResolver_HeartbeatStopsWhenHoldResolves(t *testing.T) {
	store := NewInMemoryHumanRequestStore()
	notifier := &beatingNotifier{}
	r := &hitlAskResolver{
		store:     store,
		timeout:   5 * time.Second,
		poll:      2 * time.Millisecond,
		notifier:  notifier,
		heartbeat: 10 * time.Millisecond,
	}

	decision := resolveAsync(r, context.Background())
	require.Eventually(t, func() bool { return notifier.beats() >= 1 }, 2*time.Second, 5*time.Millisecond)

	respondOncePending(store, "approved")
	select {
	case d := <-decision:
		require.Equal(t, Allow, d.Kind)
	case <-time.After(3 * time.Second):
		t.Fatal("resolver did not return after approval")
	}

	settled := notifier.beats()
	time.Sleep(50 * time.Millisecond) // several heartbeat intervals
	require.Equal(t, settled, notifier.beats(), "no heartbeat may fire after the hold resolved")
}

// TestAskResolver_HeartbeatOptInAndErrorsIgnored is table-driven over the
// notifier shapes: only a Heartbeater is beaten, a zero interval disables the
// beat entirely, and a Heartbeat that errors never changes the outcome.
func TestAskResolver_HeartbeatOptInAndErrorsIgnored(t *testing.T) {
	tests := []struct {
		name        string
		heartbeat   time.Duration
		errOnBeat   error
		wantBeats   bool
		plainNotify bool
	}{
		{name: "heartbeater is beaten", heartbeat: 10 * time.Millisecond, wantBeats: true},
		{name: "beat errors are ignored", heartbeat: 10 * time.Millisecond, errOnBeat: context.DeadlineExceeded, wantBeats: true},
		{name: "zero interval disables beating", heartbeat: 0, wantBeats: false},
		{name: "plain notifier is never beaten", heartbeat: 10 * time.Millisecond, plainNotify: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewInMemoryHumanRequestStore()
			beating := &beatingNotifier{err: tt.errOnBeat}
			plain := &plainNotifier{}

			var n Notifier = beating
			if tt.plainNotify {
				n = plain
			}
			r := &hitlAskResolver{
				store:     store,
				timeout:   5 * time.Second,
				poll:      2 * time.Millisecond,
				notifier:  n,
				heartbeat: tt.heartbeat,
			}

			decision := resolveAsync(r, context.Background())
			waitForPending(t, store)
			time.Sleep(60 * time.Millisecond) // several intervals

			// Every assertion is made against the notifier the resolver
			// ACTUALLY holds — counting beats on an uninstalled notifier
			// would pass no matter what the resolver did.
			if tt.plainNotify {
				_, isBeater := n.(Heartbeater)
				require.False(t, isBeater, "a plain notifier must not advertise the capability")
				require.Equal(t, 1, plain.count(), "the pending card is still emitted once")
			} else if tt.wantBeats {
				require.GreaterOrEqual(t, beating.beats(), 1, "expected the hold to heartbeat")
			} else {
				require.Zero(t, beating.beats(), "expected no heartbeat")
			}

			// The hold still resolves normally in every shape.
			respondOncePending(store, "approved")
			select {
			case d := <-decision:
				require.Equal(t, Allow, d.Kind)
			case <-time.After(3 * time.Second):
				t.Fatal("resolver did not return after approval")
			}
		})
	}
}

// TestAskResolver_NilNotifierWithHeartbeatDoesNotPanic covers the nil-interface
// assertion path: a nil notifier must stay a silent, working hold.
func TestAskResolver_NilNotifierWithHeartbeatDoesNotPanic(t *testing.T) {
	store := NewInMemoryHumanRequestStore()
	r := &hitlAskResolver{
		store:     store,
		timeout:   5 * time.Second,
		poll:      2 * time.Millisecond,
		notifier:  nil,
		heartbeat: 5 * time.Millisecond,
	}

	decision := resolveAsync(r, context.Background())
	waitForPending(t, store)
	time.Sleep(30 * time.Millisecond)
	respondOncePending(store, "approved")

	select {
	case d := <-decision:
		require.Equal(t, Allow, d.Kind)
	case <-time.After(3 * time.Second):
		t.Fatal("resolver did not return after approval")
	}
}

// TestNewHITLAskResolver_DefaultsHeartbeatInterval pins the constructor default
// so the production path is beaten without any caller opt-in.
func TestNewHITLAskResolver_DefaultsHeartbeatInterval(t *testing.T) {
	r, ok := NewHITLAskResolver(NewInMemoryHumanRequestStore(), 0, 0, nil).(*hitlAskResolver)
	require.True(t, ok)
	require.Equal(t, holdHeartbeatInterval, r.heartbeat)
	require.Equal(t, 300*time.Second, r.timeout)
	require.Equal(t, time.Second, r.poll)
}

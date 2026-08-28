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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// contactHumanUnderTest builds a contact_human tool over a real in-memory store
// with test-scale poll and heartbeat intervals.
func contactHumanUnderTest(n Notifier, heartbeat time.Duration) (*ContactHumanTool, *InMemoryHumanRequestStore) {
	store := NewInMemoryHumanRequestStore()
	tool := NewContactHumanTool(ContactHumanConfig{
		Store:        store,
		Notifier:     n,
		Timeout:      5 * time.Second,
		PollInterval: 2 * time.Millisecond,
	})
	tool.heartbeat = heartbeat
	return tool, store
}

// askQuestionAsync runs a contact_human call on its own goroutine (it blocks)
// and returns a channel carrying the result.
func askQuestionAsync(t *ContactHumanTool, ctx context.Context) <-chan *Result {
	out := make(chan *Result, 1)
	go func() {
		res, _ := t.Execute(ctx, map[string]interface{}{
			"question":        "Proceed?",
			"timeout_seconds": float64(5),
		})
		out <- res
	}()
	return out
}

// A question hold is byte-silent for its whole window exactly like an approval
// hold — up to the configured ceiling, and the model may ask for minutes — so
// it must heartbeat on the same terms.
func TestContactHuman_HeartbeatsWhilePending(t *testing.T) {
	notifier := &beatingNotifier{}
	tool, store := contactHumanUnderTest(notifier, 15*time.Millisecond)

	result := askQuestionAsync(tool, context.Background())

	require.Eventually(t, func() bool { return notifier.beats() >= 3 }, 2*time.Second, 5*time.Millisecond,
		"a pending question must keep heartbeating")

	// The card itself is emitted exactly once no matter how many beats follow.
	require.Equal(t, 1, notifier.notifyCount())

	respondOncePending(store, "approved")
	select {
	case res := <-result:
		require.True(t, res.Success)
	case <-time.After(3 * time.Second):
		t.Fatal("contact_human did not return after the human responded")
	}
}

// The beat is bounded by the hold: once the human answers, nothing further is
// emitted.
func TestContactHuman_HeartbeatStopsWhenAnswered(t *testing.T) {
	notifier := &beatingNotifier{}
	tool, store := contactHumanUnderTest(notifier, 10*time.Millisecond)

	result := askQuestionAsync(tool, context.Background())
	require.Eventually(t, func() bool { return notifier.beats() >= 1 }, 2*time.Second, 5*time.Millisecond)

	respondOncePending(store, "approved")
	select {
	case res := <-result:
		require.True(t, res.Success)
	case <-time.After(3 * time.Second):
		t.Fatal("contact_human did not return after the human responded")
	}

	settled := notifier.beats()
	time.Sleep(50 * time.Millisecond) // several heartbeat intervals
	require.Equal(t, settled, notifier.beats(), "no heartbeat may fire after the question was answered")
}

// Opt-in by capability, same as the ask hold: a plain Notifier is never beaten
// and keeps behaving exactly as before, and the default notifier (NoOpNotifier,
// installed when a caller supplies none) is safe to beat against.
func TestContactHuman_HeartbeatIsOptInByCapability(t *testing.T) {
	t.Run("plain notifier is never beaten", func(t *testing.T) {
		plain := &plainNotifier{}
		tool, store := contactHumanUnderTest(plain, 5*time.Millisecond)

		result := askQuestionAsync(tool, context.Background())
		waitForPending(t, store)
		time.Sleep(40 * time.Millisecond) // several intervals

		_, isBeater := Notifier(plain).(Heartbeater)
		require.False(t, isBeater, "a plain notifier must not advertise the capability")
		require.Equal(t, 1, plain.count(), "the card is still emitted once")

		respondOncePending(store, "approved")
		select {
		case res := <-result:
			require.True(t, res.Success)
		case <-time.After(3 * time.Second):
			t.Fatal("contact_human did not return after the human responded")
		}
	})

	t.Run("default no-op notifier is safe", func(t *testing.T) {
		tool, store := contactHumanUnderTest(nil, 5*time.Millisecond)

		result := askQuestionAsync(tool, context.Background())
		waitForPending(t, store)
		time.Sleep(30 * time.Millisecond)
		respondOncePending(store, "approved")

		select {
		case res := <-result:
			require.True(t, res.Success)
		case <-time.After(3 * time.Second):
			t.Fatal("contact_human did not return after the human responded")
		}
	})
}

// The constructor defaults the interval, so the production path beats without
// any caller opt-in — and shares ONE constant with the ask hold so the two
// origins cannot drift.
func TestNewContactHumanTool_DefaultsHeartbeatInterval(t *testing.T) {
	tool := NewContactHumanTool(ContactHumanConfig{})
	require.Equal(t, holdHeartbeatInterval, tool.heartbeat)
}

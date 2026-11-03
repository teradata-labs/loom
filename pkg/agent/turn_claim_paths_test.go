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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// A rewind by one agent object is seen by another holding the same session:
// its next turn does not show the model the undone exchange.
func TestARewindIsSeenByEveryObject(t *testing.T) {
	store := turnStore(t)
	a, llmA, b, _ := twoObjects(t, store)
	ctx := context.Background()
	_, err := a.Chat(ctx, "s1", "one")
	require.NoError(t, err)
	_, err = a.Chat(ctx, "s1", "two-UNDONE")
	require.NoError(t, err)
	n, err := b.RewindSession(ctx, "s1", 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	_, err = a.Chat(ctx, "s1", "three")
	require.NoError(t, err)
	require.NotContains(t, llmA.lastCall(), "two-UNDONE", "the other object's copy still held the undone turn")
}

// A rewind is refused while a turn runs in the session, and the turn's
// history is left whole.
func TestARewindWaitsForTheRunningTurn(t *testing.T) {
	store := turnStore(t)
	a, llmA, b, _ := twoObjects(t, store)
	ctx := context.Background()
	_, err := a.Chat(ctx, "s1", "one")
	require.NoError(t, err)
	entered, release := llmA.holdNext()
	done := make(chan error, 1)
	go func() { _, err := a.Chat(ctx, "s1", "two"); done <- err }()
	began(t, entered, done)
	_, err = b.RewindSession(ctx, "s1", 1)
	release()
	require.NoError(t, <-done)
	require.ErrorIs(t, err, ErrSessionBusy, "the rewind ran inside a live turn")
}

// A turn whose claim was taken over while it ran (it stalled past the stale
// time) does not keep its copy as current: its next turn sees the turn that
// took over.
func TestATurnThatLostItsClaimReloads(t *testing.T) {
	store := turnStore(t)
	a, llmA, b, _ := twoObjects(t, store)
	ctx := context.Background()
	_, err := a.Chat(ctx, "s1", "one")
	require.NoError(t, err)
	keep, stale := turnKeepEvery, turnStaleAfter
	turnKeepEvery, turnStaleAfter = time.Hour, 20*time.Millisecond
	t.Cleanup(func() { turnKeepEvery, turnStaleAfter = keep, stale })
	entered, release := llmA.holdNext()
	done := make(chan error, 1)
	go func() { _, err := a.Chat(ctx, "s1", "A-slow"); done <- err }()
	began(t, entered, done)
	time.Sleep(50 * time.Millisecond)
	_, err = b.Chat(ctx, "s1", "B-took-over")
	require.NoError(t, err)
	release()
	require.NoError(t, <-done)
	turnStaleAfter = time.Hour
	_, err = a.Chat(ctx, "s1", "A-next")
	require.NoError(t, err)
	require.Contains(t, llmA.lastCall(), "B-took-over", "the object kept a copy missing the turn that took over")
}

// Resuming with a request for a session that does not exist creates no
// session row.
func TestAResumeCreatesNoSession(t *testing.T) {
	store := turnStore(t)
	a := NewAgent(nil, &gatedLLM{}, WithName("manager"), WithMemory(NewMemoryWithStore(store)),
		WithHITLPark(shuttle.NewInMemoryHumanRequestStore(), 0, nil))
	ctx := context.Background()
	_, err := a.ResumeChat(ctx, "never-existed", ParkDecision{RequestID: "bogus", Approved: true}, nil)
	require.Error(t, err)
	ok, err := store.SessionExists(ctx, "never-existed")
	require.NoError(t, err)
	require.False(t, ok, "a resume created a session row")
}

// A claim held by another pid namespace on this host is never judged dead
// here, and neither is one from another host.
func TestOnlyALocalDeadProcessIsDead(t *testing.T) {
	host := thisProcess
	_, rest, _ := cutAt(host, "@")
	h, _, _ := cutAt(rest, ":")
	require.False(t, deadHere("999999@"+h+":pid:[1]/abcd#1"), "a holder from another pid namespace was judged dead")
	require.False(t, deadHere("999999@other-host:"+pidNamespace()+"/abcd#1"), "a holder from another host was judged dead")
	require.True(t, deadHere("999999@"+h+":"+pidNamespace()+"/abcd#1"), "a dead local process was not judged dead")
}

func cutAt(s, sep string) (string, string, bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}

// firstTurnBetween is a session store that, the first time a claim finds no
// row, lets a first turn happen before answering: the race a rewind of a
// brand-new session can meet.
type firstTurnBetween struct {
	*SessionStore
	between func()
	done    bool
}

func (s *firstTurnBetween) TakeTurn(ctx context.Context, sessionID, holder, replacing string, staleBefore time.Time) (TurnClaim, error) {
	claim, err := s.SessionStore.TakeTurn(ctx, sessionID, holder, replacing, staleBefore)
	if err == nil && !claim.Exists && !s.done {
		s.done = true
		s.between()
	}
	return claim, err
}

// A rewind of a session with no row does nothing, even when a first turn
// creates the session at that moment: the first turn's history is left whole.
func TestARewindOfASessionWithNoRowLeavesAFirstTurnAlone(t *testing.T) {
	store := turnStore(t)
	writer := NewAgent(nil, &gatedLLM{}, WithName("manager"), WithMemory(NewMemoryWithStore(store)))
	racing := &firstTurnBetween{SessionStore: store}
	rewinder := NewAgent(nil, &gatedLLM{}, WithName("manager"), WithMemory(NewMemoryWithStore(racing)))
	ctx := context.Background()
	racing.between = func() {
		_, err := writer.Chat(ctx, "new", "first words")
		require.NoError(t, err)
	}
	n, err := rewinder.RewindSession(ctx, "new", 1)
	require.NoError(t, err)
	require.Zero(t, n, "a rewind of a session with no row undid something")
	msgs, err := store.LoadMessages(ctx, "new")
	require.NoError(t, err)
	require.NotEmpty(t, msgs)
	require.Equal(t, "first words", msgs[0].Content, "the first turn lost its first message to the rewind")
}

// A claim written before claims named their pid namespace is judged by host
// and pid alone where this process has no namespace, and never where it has
// one.
func TestAnOldFormatClaimIsJudgedSafely(t *testing.T) {
	_, rest, _ := cutAt(thisProcess, "@")
	h, _, _ := cutAt(rest, ":")
	dead := "999999@" + h + "#7"
	require.Equal(t, pidNamespace() == "", deadHere(dead),
		"an old-format claim of a dead local process: judged dead only without pid namespaces")
}

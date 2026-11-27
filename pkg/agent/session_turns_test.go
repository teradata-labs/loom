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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// gatedLLM answers every call with a reply naming what it was asked, and can
// be made to wait, or to fail, on its next call.
type gatedLLM struct {
	mu      sync.Mutex
	seen    [][]string // the contents of every call's messages
	hold    chan struct{}
	entered chan struct{}
	down    int // calls still to fail: more than the agent retries is a failed turn
}

func (m *gatedLLM) Name() string  { return "gated" }
func (m *gatedLLM) Model() string { return "gated" }
func (m *gatedLLM) Chat(ctx context.Context, msgs []types.Message, _ []shuttle.Tool) (*types.LLMResponse, error) {
	m.mu.Lock()
	var contents []string
	for _, msg := range msgs {
		contents = append(contents, msg.Content)
	}
	m.seen = append(m.seen, contents)
	hold, entered, fail := m.hold, m.entered, m.down > 0
	m.hold, m.entered = nil, nil
	if fail {
		m.down--
	}
	m.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if fail {
		return nil, errors.New("the model could not be reached")
	}
	// What was asked, without the arrival time the agent puts in front.
	last := contents[len(contents)-1]
	if strings.HasPrefix(last, "[") {
		if _, rest, ok := strings.Cut(last, "] "); ok {
			last = rest
		}
	}
	return &types.LLMResponse{Content: "reply to " + last, StopReason: "end_turn",
		Usage: types.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
}

// lastCall is what the model was shown on its latest call.
func (m *gatedLLM) lastCall() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.Join(m.seen[len(m.seen)-1], "\n")
}

// holdNext makes the next call wait until released, and says when it began.
func (m *gatedLLM) holdNext() (entered <-chan struct{}, release func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hold, m.entered = make(chan struct{}), make(chan struct{})
	hold := m.hold
	return m.entered, func() { close(hold) }
}

// failNext makes the next turn fail: more failures than the agent retries.
func (m *gatedLLM) failNext() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.down = 6
}

// began waits for a held turn to reach the model, failing when the turn ended
// first — refused, or failed — rather than waiting for ever.
func began(t *testing.T, entered <-chan struct{}, done <-chan error) {
	t.Helper()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("the turn ended before it reached the model: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never reached the model")
	}
}

func turnStore(t *testing.T) *SessionStore {
	t.Helper()
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "turns.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// twoObjects is one agent, built twice over one store and one lease store —
// as a loop's step and the conversation a person is connected to each build
// it. Neither records events: the lock holds on its own.
func twoObjects(t *testing.T, store *SessionStore, leases *recordingEventLog) (*Agent, *gatedLLM, *Agent, *gatedLLM) {
	t.Helper()
	llmA, llmB := &gatedLLM{}, &gatedLLM{}
	a := NewAgent(nil, llmA, WithName("manager"), WithMemory(NewMemoryWithStore(store)), WithSessionLeases(leases))
	b := NewAgent(nil, llmB, WithName("manager"), WithMemory(NewMemoryWithStore(store)), WithSessionLeases(leases))
	require.Nil(t, a.eventLog, "the lock must hold without recording")
	require.Nil(t, b.eventLog, "the lock must hold without recording")
	return a, llmA, b, llmB
}

// One turn at a time in a session, whichever agent object takes it, recording
// on or off: a second turn while one runs is refused as busy. Afterwards the
// other object sees the session as it now is — the turn it did not take
// included — not the copy it held before.
func TestOneTurnAtATimeInASession(t *testing.T) {
	store := turnStore(t)
	a, llmA, b, llmB := twoObjects(t, store, &recordingEventLog{})
	ctx := context.Background()

	_, err := a.Chat(ctx, "s1", "one")
	require.NoError(t, err, "a first turn of a new session")
	_, err = b.Chat(ctx, "s1", "two")
	require.NoError(t, err)
	require.Contains(t, llmB.lastCall(), "reply to one", "the other object did not see the first turn")

	entered, release := llmA.holdNext()
	done := make(chan error, 1)
	go func() { _, err := a.Chat(ctx, "s1", "three"); done <- err }()
	began(t, entered, done)
	_, err = b.Chat(ctx, "s1", "four")
	require.ErrorIs(t, err, ErrSessionBusy, "a second turn ran while one was running")
	release()
	require.NoError(t, <-done)

	_, err = b.Chat(ctx, "s1", "five")
	require.NoError(t, err, "the session was left leased after its turn")
	seen := llmB.lastCall()
	require.Contains(t, seen, "reply to three", "the other object used its old copy of the session")
	require.NotContains(t, seen, "four", "the refused turn wrote into the session")
}

// A turn that ends in an error, or is cancelled, releases the session like
// one that ends well.
func TestEveryWayOutOfATurnReleasesTheSession(t *testing.T) {
	store := turnStore(t)
	a, llmA, b, _ := twoObjects(t, store, &recordingEventLog{})
	ctx := context.Background()
	_, err := a.Chat(ctx, "s1", "one")
	require.NoError(t, err)

	llmA.failNext()
	_, err = a.Chat(ctx, "s1", "fails")
	require.Error(t, err)
	_, err = b.Chat(ctx, "s1", "after a failure")
	require.NoError(t, err, "a failed turn left the session leased")

	cancelled, cancel := context.WithCancel(ctx)
	entered, _ := llmA.holdNext()
	done := make(chan error, 1)
	go func() { _, err := a.Chat(cancelled, "s1", "cancelled"); done <- err }()
	began(t, entered, done)
	cancel()
	<-done
	_, err = b.Chat(ctx, "s1", "after a cancel")
	require.NoError(t, err, "a cancelled turn left the session leased")
}

// orderedLeases checks, as a lease is released, what the release expects to
// have happened already.
type orderedLeases struct {
	recordingEventLog
	onRelease func(sessionID string)
}

func (o *orderedLeases) ReleaseSessionLease(ctx context.Context, sessionID, holder string) error {
	if o.onRelease != nil {
		o.onRelease(sessionID)
	}
	return o.recordingEventLog.ReleaseSessionLease(ctx, sessionID, holder)
}

// The turn is counted while the lease is still held, so the count and the
// history it stands for change under one lock.
func TestTheTurnIsCountedBeforeTheLeaseIsReleased(t *testing.T) {
	store := turnStore(t)
	ctx := context.Background()
	leases := &orderedLeases{}
	var countedAtRelease []int64
	leases.onRelease = func(sessionID string) {
		n, err := store.Turns(ctx, sessionID)
		require.NoError(t, err)
		countedAtRelease = append(countedAtRelease, n)
	}
	a := NewAgent(nil, &gatedLLM{}, WithName("solo"), WithMemory(NewMemoryWithStore(store)), WithSessionLeases(leases))
	_, err := a.Chat(ctx, "s1", "one")
	require.NoError(t, err)
	_, err = a.Chat(ctx, "s1", "two")
	require.NoError(t, err)
	require.Equal(t, []int64{2, 4}, countedAtRelease, "the lease was released before the turn was counted")
}

// A holder that dies mid-turn never releases its lease, but its turn was
// counted when it took the lease: once the lease is gone, another object
// loads the rows the dead holder saved instead of running on its old copy.
func TestATurnWhoseHolderDiedIsSeenByTheNextObject(t *testing.T) {
	store := turnStore(t)
	leases := &recordingEventLog{}
	a, _, b, llmB := twoObjects(t, store, leases)
	ctx := context.Background()

	_, err := a.Chat(ctx, "s1", "one")
	require.NoError(t, err)
	_, err = b.Chat(ctx, "s1", "two")
	require.NoError(t, err)

	// a takes the lease, saves a row and dies: release never runs.
	_, release, err := a.holdSessionLease(ctx, "s1")
	require.NoError(t, err)
	t.Cleanup(release) // stops the renewal goroutine once the test is over
	require.NoError(t, store.SaveMessage(ctx, "s1", &Message{
		Role: "user", Content: "three, from the holder that died", Timestamp: time.Now(),
	}, true))
	// Its lease expires.
	leases.mu.Lock()
	delete(leases.claims, "s1")
	delete(leases.expiry, "s1")
	leases.mu.Unlock()

	_, err = b.Chat(ctx, "s1", "four")
	require.NoError(t, err)
	require.Contains(t, llmB.lastCall(), "three, from the holder that died",
		"the next object ran on its old copy, without the dead holder's rows")
}

// A holder whose lease ran out while it was still alive saves its rows late,
// after another object has taken a turn: the count moves when it lets go, so
// the other object loads those rows too.
func TestAHolderThatOutlivedItsLeaseIsSeenByTheNextObject(t *testing.T) {
	store := turnStore(t)
	leases := &recordingEventLog{}
	a, _, b, llmB := twoObjects(t, store, leases)
	ctx := context.Background()

	_, err := a.Chat(ctx, "s1", "one")
	require.NoError(t, err)

	_, release, err := a.holdSessionLease(ctx, "s1")
	require.NoError(t, err)
	leases.mu.Lock()
	delete(leases.claims, "s1")
	delete(leases.expiry, "s1")
	leases.mu.Unlock()
	_, err = b.Chat(ctx, "s1", "two")
	require.NoError(t, err)

	require.NoError(t, store.SaveMessage(ctx, "s1", &Message{
		Role: "user", Content: "three, saved late", Timestamp: time.Now(),
	}, true))
	release()

	_, err = b.Chat(ctx, "s1", "four")
	require.NoError(t, err)
	require.Contains(t, llmB.lastCall(), "three, saved late",
		"the next object ran on its old copy, without the late rows")
}

// turn it did not take makes it reload.
func TestAnAgentsOwnCopySurvivesIntoItsNextTurn(t *testing.T) {
	store := turnStore(t)
	a := NewAgent(nil, &gatedLLM{}, WithName("solo"), WithMemory(NewMemoryWithStore(store)), WithSessionLeases(&recordingEventLog{}))
	ctx := context.Background()

	_, err := a.Chat(ctx, "kept", "one")
	require.NoError(t, err)
	first, ok := a.memory.GetSession("kept")
	require.True(t, ok)

	_, err = a.Chat(ctx, "kept", "two")
	require.NoError(t, err)
	second, ok := a.memory.GetSession("kept")
	require.True(t, ok)
	require.Same(t, first, second, "the session was reloaded at turn two")

	turns, err := store.Turns(ctx, "kept")
	require.NoError(t, err)
	require.EqualValues(t, 4, turns, "two turns, each counted as it began and as it ended")
}

// A requested compaction works on the session, not on a copy another object's
// turns have made stale: taking the lease drops that copy, and the compaction
// reads the session after it.
func TestCompactSession_CompactsTheSessionNotAStaleCopy(t *testing.T) {
	store := turnStore(t)
	a, _, b, _ := twoObjects(t, store, &recordingEventLog{})
	a.memory.SetCompressor(&recordingCompressor{})
	ctx := context.Background()

	_, err := a.Chat(ctx, "s1", "one")
	require.NoError(t, err)
	for _, q := range []string{"two", "three", "four", "five"} {
		_, err = b.Chat(ctx, "s1", q)
		require.NoError(t, err)
	}

	// a's copy holds one turn; the session has five.
	res, err := a.CompactSession(ctx, "s1", CompactOptions{KeepTurns: 2})
	require.NoError(t, err)
	require.True(t, res.Compacted, "the compaction ran on a copy with nothing to fold")
	require.Equal(t, 2, res.TurnsKept)
}

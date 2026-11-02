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

// twoObjects is one agent, built twice over one store — as a loop's step and
// the conversation a person is connected to each build it.
func twoObjects(t *testing.T, store *SessionStore) (*Agent, *gatedLLM, *Agent, *gatedLLM) {
	t.Helper()
	llmA, llmB := &gatedLLM{}, &gatedLLM{}
	a := NewAgent(nil, llmA, WithName("manager"), WithMemory(NewMemoryWithStore(store)))
	b := NewAgent(nil, llmB, WithName("manager"), WithMemory(NewMemoryWithStore(store)))
	return a, llmA, b, llmB
}

// One turn at a time in a session, whichever agent object takes it: a second
// turn while one runs is refused as busy. Afterwards the other object sees the
// session as it now is — the turn it did not take included — not the copy it
// held before.
func TestOneTurnAtATimeInASession(t *testing.T) {
	store := turnStore(t)
	a, llmA, b, llmB := twoObjects(t, store)
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
	require.NoError(t, err, "the session was left claimed after its turn")
	seen := llmB.lastCall()
	require.Contains(t, seen, "reply to three", "the other object used its old copy of the session")
	require.NotContains(t, seen, "four", "the refused turn wrote into the session")
}

// A turn that ends in an error, or is cancelled, releases the session like
// one that ends well.
func TestEveryWayOutOfATurnReleasesTheSession(t *testing.T) {
	store := turnStore(t)
	a, llmA, b, _ := twoObjects(t, store)
	ctx := context.Background()
	_, err := a.Chat(ctx, "s1", "one")
	require.NoError(t, err)

	llmA.failNext()
	_, err = a.Chat(ctx, "s1", "fails")
	require.Error(t, err)
	_, err = b.Chat(ctx, "s1", "after a failure")
	require.NoError(t, err, "a failed turn left the session claimed")

	cancelled, cancel := context.WithCancel(ctx)
	entered, _ := llmA.holdNext()
	done := make(chan error, 1)
	go func() { _, err := a.Chat(cancelled, "s1", "cancelled"); done <- err }()
	began(t, entered, done)
	cancel()
	<-done
	_, err = b.Chat(ctx, "s1", "after a cancel")
	require.NoError(t, err, "a cancelled turn left the session claimed")
}

// A claim left by a process that no longer exists on this host is taken over
// at once; one left by another host is taken over when it goes stale; a live
// one elsewhere is respected.
func TestAClaimLeftBehindIsTakenOver(t *testing.T) {
	store := turnStore(t)
	a, _, _, _ := twoObjects(t, store)
	ctx := context.Background()
	_, err := a.Chat(ctx, "s1", "one")
	require.NoError(t, err)
	host, _ := os.Hostname()

	claimed, err := store.TakeTurn(ctx, "s1", fmt.Sprintf("999999@%s#1", host), "", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, claimed.Taken)
	_, err = a.Chat(ctx, "s1", "after a dead process")
	require.NoError(t, err, "a dead process's claim was not taken over")

	claimed, err = store.TakeTurn(ctx, "s1", "1@elsewhere#1", "", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, claimed.Taken)
	_, err = a.Chat(ctx, "s1", "while another host works")
	require.ErrorIs(t, err, ErrSessionBusy, "a live claim on another host was taken over")

	was := turnStaleAfter
	turnStaleAfter = time.Millisecond
	t.Cleanup(func() { turnStaleAfter = was })
	time.Sleep(5 * time.Millisecond)
	_, err = a.Chat(ctx, "s1", "after it went stale")
	require.NoError(t, err, "a stale claim was not taken over")
}

// A turn longer than the stale time keeps its claim while it runs.
func TestALongTurnKeepsItsClaim(t *testing.T) {
	store := turnStore(t)
	a, llmA, b, _ := twoObjects(t, store)
	ctx := context.Background()
	_, err := a.Chat(ctx, "s1", "one")
	require.NoError(t, err)

	keep, stale := turnKeepEvery, turnStaleAfter
	turnKeepEvery, turnStaleAfter = 10*time.Millisecond, 60*time.Millisecond
	t.Cleanup(func() { turnKeepEvery, turnStaleAfter = keep, stale })

	entered, release := llmA.holdNext()
	done := make(chan error, 1)
	go func() { _, err := a.Chat(ctx, "s1", "long"); done <- err }()
	began(t, entered, done)
	time.Sleep(200 * time.Millisecond)
	_, err = b.Chat(ctx, "s1", "meanwhile")
	require.ErrorIs(t, err, ErrSessionBusy, "a long turn's claim went stale while it ran")
	release()
	require.NoError(t, <-done)
}

// Taking a turn up after a park is a turn too: refused while another runs.
func TestAResumedTurnWaitsItsTurn(t *testing.T) {
	store := turnStore(t)
	a, _, _, _ := twoObjects(t, store)
	ctx := context.Background()
	_, err := a.Chat(ctx, "s1", "one")
	require.NoError(t, err)
	_, err = store.TakeTurn(ctx, "s1", "1@elsewhere#1", "", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	_, err = a.ResumeChat(ctx, "s1", ParkDecision{RequestID: "r1", Approved: true}, nil)
	require.ErrorIs(t, err, ErrSessionBusy)
}

// A brand-new session is held by its first turn like any other: a second
// first turn, on another agent object, is refused rather than writing beside
// it.
func TestTheFirstTurnOfANewSessionHoldsIt(t *testing.T) {
	store := turnStore(t)
	a, llmA, b, _ := twoObjects(t, store)
	ctx := context.Background()

	entered, release := llmA.holdNext()
	done := make(chan error, 1)
	go func() { _, err := a.Chat(ctx, "fresh", "one"); done <- err }()
	began(t, entered, done)
	_, err := b.Chat(ctx, "fresh", "two")
	require.ErrorIs(t, err, ErrSessionBusy, "a second first turn ran beside the first")
	release()
	require.NoError(t, <-done)
}

// What an agent keeps on its own copy of a session, outside the messages,
// carries into its next turn: nothing else took a turn, so its copy is current
// and is not thrown away and reloaded.
func TestAnAgentsOwnCopySurvivesIntoItsNextTurn(t *testing.T) {
	store := turnStore(t)
	a := NewAgent(nil, &gatedLLM{}, WithName("solo"), WithMemory(NewMemoryWithStore(store)))
	ctx := context.Background()

	_, err := a.Chat(ctx, "kept", "one")
	require.NoError(t, err)
	first, ok := a.memory.GetSession("kept")
	require.True(t, ok)
	first.Tasks = types.NewTaskList()

	_, err = a.Chat(ctx, "kept", "two")
	require.NoError(t, err)
	second, ok := a.memory.GetSession("kept")
	require.True(t, ok)
	require.Same(t, first, second, "the session was reloaded at turn two, losing what it held")
	require.NotNil(t, second.Tasks, "the task list did not survive into turn two")
}

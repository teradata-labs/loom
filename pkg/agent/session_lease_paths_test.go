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

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// A rewind by one agent object is seen by another holding the same session:
// its next turn does not show the model the undone exchange.
func TestARewindIsSeenByEveryObject(t *testing.T) {
	store := turnStore(t)
	a, llmA, b, _ := twoObjects(t, store, &recordingEventLog{})
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
func TestARewindIsRefusedWhileATurnRuns(t *testing.T) {
	store := turnStore(t)
	a, llmA, b, _ := twoObjects(t, store, &recordingEventLog{})
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

// Resuming with a request for a session that does not exist creates no
// session row.
func TestAResumeCreatesNoSession(t *testing.T) {
	store := turnStore(t)
	a := NewAgent(nil, &gatedLLM{}, WithName("manager"), WithMemory(NewMemoryWithStore(store)),
		WithSessionLeases(&recordingEventLog{}),
		WithHITLPark(shuttle.NewInMemoryHumanRequestStore(), 0, nil))
	ctx := context.Background()
	_, err := a.ResumeChat(ctx, "never-existed", ParkDecision{RequestID: "bogus", Approved: true}, nil)
	require.Error(t, err)
	ok, err := store.SessionExists(ctx, "never-existed")
	require.NoError(t, err)
	require.False(t, ok, "a resume created a session row")
}

// A rewind of a session with no row does nothing and creates nothing.
func TestARewindOfASessionWithNoRowDoesNothing(t *testing.T) {
	store := turnStore(t)
	a, _, _, _ := twoObjects(t, store, &recordingEventLog{})
	ctx := context.Background()
	n, err := a.RewindSession(ctx, "never-existed", 1)
	require.NoError(t, err)
	require.Equal(t, 0, n)
	ok, err := store.SessionExists(ctx, "never-existed")
	require.NoError(t, err)
	require.False(t, ok, "a rewind created a session row")
}

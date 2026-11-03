//go:build integration

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

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/agent"
)

// One turn at a time per session, on Postgres as on SQLite: a claim is held
// by one holder, refused to another, released with the turn counted, taken
// over from a holder named as replaced or gone stale — and a session with no
// row claims nothing.
func TestTurnLock_OneTurnAtATimePerSession(t *testing.T) {
	pool := testPool(t)
	store := testSessionStore(t, pool)
	user := uniqueID("turns-user")
	ctx := ContextWithUserID(context.Background(), user)
	id := uniqueID("turns-session")
	require.NoError(t, store.SaveSession(ctx, &agent.Session{ID: id, CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	long := time.Now().Add(-time.Hour)

	a, err := store.TakeTurn(ctx, id, "a", "", long)
	require.NoError(t, err)
	require.True(t, a.Exists && a.Taken)
	require.EqualValues(t, 0, a.Turns)

	b, err := store.TakeTurn(ctx, id, "b", "", long)
	require.NoError(t, err)
	require.False(t, b.Taken, "a second holder took a held session")
	require.Equal(t, "a", b.Holder)

	require.NoError(t, store.KeepTurn(ctx, id, "a"))
	held, err := store.EndTurn(ctx, id, "b")
	require.NoError(t, err)
	require.False(t, held, "a holder that does not hold it released it")
	b, err = store.TakeTurn(ctx, id, "b", "", long)
	require.NoError(t, err)
	require.False(t, b.Taken)

	held, err = store.EndTurn(ctx, id, "a")
	require.NoError(t, err)
	require.True(t, held, "the holder's release was not reported as held")
	b, err = store.TakeTurn(ctx, id, "b", "", long)
	require.NoError(t, err)
	require.True(t, b.Taken, "a released session was not free")
	require.EqualValues(t, 1, b.Turns, "the released turn was not counted")

	c, err := store.TakeTurn(ctx, id, "c", "b", long)
	require.NoError(t, err)
	require.True(t, c.Taken, "a holder named as replaced was not taken over")

	d, err := store.TakeTurn(ctx, id, "d", "", time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, d.Taken, "a stale claim was not taken over")

	none, err := store.TakeTurn(ctx, uniqueID("no-such-session"), "e", "", long)
	require.NoError(t, err)
	require.False(t, none.Exists, "a session with no row claimed something")

	other := ContextWithUserID(context.Background(), uniqueID("someone-else"))
	theirs, err := store.TakeTurn(other, id, "f", "", time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.False(t, theirs.Exists, "another user's session was claimed")
}

// A session id that was soft-deleted and saved again can take a turn, as it
// could before turns were claimed.
func TestTurnLock_ASoftDeletedSessionIdCanTakeATurn(t *testing.T) {
	pool := testPool(t)
	store := testSessionStore(t, pool)
	ctx := ContextWithUserID(context.Background(), uniqueID("u"))
	id := uniqueID("s")
	require.NoError(t, store.SaveSession(ctx, &agent.Session{ID: id, CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	require.NoError(t, store.DeleteSession(ctx, id))
	require.NoError(t, store.SaveSession(ctx, &agent.Session{ID: id, CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	c, err := store.TakeTurn(ctx, id, "h", "", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, c.Exists && c.Taken, "the re-created session id could not be claimed")
}

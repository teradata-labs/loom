// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/agent"
)

// A session's turns are counted on its row, for its owner: a session with no
// row has had none, and counting a turn it does not have is not an error.
func TestSessionTurns_CountedOnTheRow(t *testing.T) {
	pool := testPool(t)
	store := testSessionStore(t, pool)
	var _ agent.TurnCounter = store

	owner := uniqueID("user-turns")
	sessionID := createTestSession(t, store, owner, uniqueID("sess-turns"), "turns-agent")
	ctx := ContextWithUserID(context.Background(), owner)

	turns, err := store.Turns(ctx, sessionID)
	require.NoError(t, err)
	require.EqualValues(t, 0, turns)
	require.NoError(t, store.CountTurn(ctx, sessionID))
	require.NoError(t, store.CountTurn(ctx, sessionID))
	turns, err = store.Turns(ctx, sessionID)
	require.NoError(t, err)
	require.EqualValues(t, 2, turns)

	turns, err = store.Turns(ctx, uniqueID("no-row"))
	require.NoError(t, err)
	require.EqualValues(t, 0, turns, "a session with no row has turns")
	require.NoError(t, store.CountTurn(ctx, uniqueID("no-row")))

	other := ContextWithUserID(context.Background(), uniqueID("someone-else"))
	turns, err = store.Turns(other, sessionID)
	require.NoError(t, err)
	require.EqualValues(t, 0, turns, "another owner read the session's turns")
}

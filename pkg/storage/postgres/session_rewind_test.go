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
	"github.com/teradata-labs/loom/pkg/observability"
)

// Truncating from a turn removes that turn whole (the user message, the tool
// call it caused and the reply) and every later one, leaving earlier turns as
// they were; the next turn reuses the freed number. Another user cannot
// truncate the session.
func TestPGTruncateFromRemovesWholeTurns(t *testing.T) {
	_, pool := artifactScopeStore(t)
	store := NewSessionStore(pool, observability.NewNoOpTracer(), nil)
	user, sessionID := asUniqueID("rewind-user"), asUniqueID("rewind-conv")
	ctx := ContextWithUserID(context.Background(), user)
	require.NoError(t, store.SaveSession(ctx, &agent.Session{
		ID: sessionID, AgentID: "agent-1", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM messages WHERE session_id = $1", sessionID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM sessions WHERE id = $1", sessionID)
	})

	exchange := func(n int) {
		for i, role := range []string{"user", "tool", "assistant"} {
			require.NoError(t, store.SaveMessage(ctx, sessionID, &agent.Message{
				Role: role, Content: role, Timestamp: time.Now().UTC(),
			}, i == 0))
		}
	}
	exchange(1)
	exchange(2)
	exchange(3)

	maxTurn, err := store.MaxTurn(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, 3, maxTurn)

	other := ContextWithUserID(context.Background(), asUniqueID("intruder"))
	n, err := store.TruncateFrom(other, sessionID, 1)
	require.NoError(t, err)
	require.Zero(t, n, "another user truncated the session")

	n, err = store.TruncateFrom(ctx, sessionID, 3)
	require.NoError(t, err)
	require.Equal(t, 3, n, "turn 3 is three rows")

	msgs, err := store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, msgs, 6)
	for _, m := range msgs {
		require.LessOrEqual(t, m.Turn, int64(2))
	}

	exchange(3)
	maxTurn, err = store.MaxTurn(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, 3, maxTurn, "the next turn reuses the freed number")

	_, err = store.TruncateFrom(ctx, sessionID, 0)
	require.Error(t, err, "turn 0 would delete the whole history")
}

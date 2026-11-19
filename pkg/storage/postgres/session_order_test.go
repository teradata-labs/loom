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
	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
)

// Two rows saved in the same tick have the same timestamp. The reads that
// sort by timestamp (LoadMessagesForAgent, LoadMessagesFromParentSession)
// must still return them in the order they were saved: the row id breaks the
// tie. The first row is updated, which moves its tuple to the end of the heap,
// so a sort on the timestamp alone returns it last. Runs in the ordinary gate:
// CI sets TEST_POSTGRES_URL, a local run without one skips.
func TestMessages_SameTimestampKeepsSaveOrder_Postgres(t *testing.T) {
	ctx := context.Background()
	pool := migratedAdminPool(t)
	// The store runs as a role that row-level security applies to; the admin
	// pool only moves a tuple and cleans up.
	store := NewSessionStore(rlsSubjectPool(t, pool), observability.NewNoOpTracer(), zap.NewNop())
	userID := hrUniqueID("order-user")
	agentID := hrUniqueID("order-agent")
	parentID := hrUniqueID("order-parent")
	childID := hrUniqueID("order-child")
	userCtx := ContextWithUserID(ctx, userID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM messages WHERE user_id = $1", userID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM sessions WHERE user_id = $1", userID)
	})
	now := time.Now().UTC()
	require.NoError(t, store.SaveSession(userCtx, &agent.Session{ID: parentID, AgentID: agentID, CreatedAt: now, UpdatedAt: now}))
	require.NoError(t, store.SaveSession(userCtx, &agent.Session{ID: childID, AgentID: hrUniqueID("order-sub"), ParentSessionID: parentID, CreatedAt: now, UpdatedAt: now}))

	tick := now.Truncate(time.Second)
	want := []string{"first", "second", "third", "fourth"}
	for i, content := range want {
		require.NoError(t, store.SaveMessage(userCtx, parentID,
			&agent.Message{Role: "user", Content: content, Timestamp: tick, SessionContext: "shared"}, i == 0))
	}
	// Move the first row's tuple to the end of the heap.
	_, err := pool.Exec(ctx, "UPDATE messages SET content = content WHERE session_id = $1 AND content = 'first'", parentID)
	require.NoError(t, err)

	contents := func(msgs []agent.Message) []string {
		out := make([]string, 0, len(msgs))
		for _, m := range msgs {
			out = append(out, m.Content)
		}
		return out
	}
	byAgent, err := store.LoadMessagesForAgent(userCtx, agentID)
	require.NoError(t, err)
	require.Equal(t, want, contents(byAgent), "LoadMessagesForAgent returned rows of one tick out of save order")

	fromParent, err := store.LoadMessagesFromParentSession(userCtx, childID)
	require.NoError(t, err)
	require.Equal(t, want, contents(fromParent), "LoadMessagesFromParentSession returned rows of one tick out of save order")
}

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

package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// The SQLite store keeps timestamps in whole seconds, so rows saved in one
// second tie. The reads that sort by timestamp (LoadMessagesForAgent,
// LoadMessagesFromParentSession) must still return them in save order: the
// row id breaks the tie. The rows alternate session contexts, so a read that
// walks the session_context index meets them out of save order.
func TestMessages_SameSecondKeepsSaveOrder(t *testing.T) {
	ctx := context.Background()
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "order.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	require.NoError(t, store.SaveSession(ctx, &Session{ID: "parent", AgentID: "coordinator", CreatedAt: now, UpdatedAt: now}))
	require.NoError(t, store.SaveSession(ctx, &Session{ID: "child", AgentID: "sub", ParentSessionID: "parent", CreatedAt: now, UpdatedAt: now}))

	tick := now.Truncate(time.Second)
	contexts := []types.SessionContext{types.SessionContextShared, types.SessionContextCoordinator}
	var want []string
	for i := 0; i < 8; i++ {
		content := string(rune('a' + i))
		want = append(want, content)
		require.NoError(t, store.SaveMessage(ctx, "parent",
			&Message{Role: "user", Content: content, Timestamp: tick, SessionContext: contexts[i%2]}, i == 0))
	}

	contents := func(msgs []Message) []string {
		out := make([]string, 0, len(msgs))
		for _, m := range msgs {
			out = append(out, m.Content)
		}
		return out
	}
	byAgent, err := store.LoadMessagesForAgent(ctx, "sub")
	require.NoError(t, err)
	require.Equal(t, want, contents(byAgent), "LoadMessagesForAgent returned rows of one second out of save order")

	fromParent, err := store.LoadMessagesFromParentSession(ctx, "child")
	require.NoError(t, err)
	require.Equal(t, want, contents(fromParent), "LoadMessagesFromParentSession returned rows of one second out of save order")
}

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
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// Every SQLite read returns what a billed row recorded: the agent-wide and
// parent-session loads read the billing columns back, as LoadMessages does and
// as Postgres does on all three.
func TestEverySQLiteReadReturnsTheBilling(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	now := time.Now()
	require.NoError(t, store.SaveSession(ctx, &Session{ID: "parent", AgentID: "lead", Context: map[string]interface{}{}, CreatedAt: now, UpdatedAt: now}))
	require.NoError(t, store.SaveSession(ctx, &Session{ID: "child", AgentID: "helper", ParentSessionID: "parent", Context: map[string]interface{}{}, CreatedAt: now, UpdatedAt: now}))

	billed := func() Message {
		return Message{
			Role: "assistant", Content: "answer", SessionContext: types.SessionContextCoordinator, Timestamp: now,
			InputTokens: 11, OutputTokens: 22, CacheReadTokens: 33, CacheCreateTokens: 44,
			CacheCreate1hTokens: 40, CacheCreate5mTokens: 4, ThinkingTokens: 5,
			Model: "claude-sonnet-5", Effort: "high",
		}
	}
	want := billed()
	inParent, inChild := billed(), billed()
	require.NoError(t, store.SaveMessage(ctx, "parent", &inParent, true))
	require.NoError(t, store.SaveMessage(ctx, "child", &inChild, true))

	check := func(path string, got []Message) {
		t.Helper()
		require.NotEmpty(t, got, "%s returned nothing", path)
		for _, m := range got {
			require.Equal(t, []interface{}{want.InputTokens, want.OutputTokens, want.CacheReadTokens, want.CacheCreateTokens,
				want.CacheCreate1hTokens, want.CacheCreate5mTokens, want.ThinkingTokens, want.Model, want.Effort},
				[]interface{}{m.InputTokens, m.OutputTokens, m.CacheReadTokens, m.CacheCreateTokens,
					m.CacheCreate1hTokens, m.CacheCreate5mTokens, m.ThinkingTokens, m.Model, m.Effort},
				"%s lost the row's billing", path)
		}
	}
	fromParent, err := store.LoadMessagesFromParentSession(ctx, "child")
	require.NoError(t, err)
	check("LoadMessagesFromParentSession", fromParent)
	forAgent, err := store.LoadMessagesForAgent(ctx, "helper")
	require.NoError(t, err)
	check("LoadMessagesForAgent", forAgent)
	found, err := store.SearchMessages(ctx, "parent", "answer", 10)
	require.NoError(t, err)
	check("SearchMessages", found)
	foundAll, err := store.SearchMessages(ctx, "", "answer", 10)
	require.NoError(t, err)
	check("SearchMessages(all sessions)", foundAll)
	byAgent, err := store.SearchMessagesByAgent(ctx, "lead", "answer", 10)
	require.NoError(t, err)
	check("SearchMessagesByAgent", byAgent)
}

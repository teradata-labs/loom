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
	"math"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/agent"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
)

// ResumeSession counts a folded assistant message as saved by reading the
// whole session with ListMessagesBySeqRange(1, MaxInt64). That read must
// return folded rows with their tool calls and Folded set, while the ordinary
// reload (LoadMessages) keeps dropping them. Runs in the ordinary gate: CI
// sets TEST_POSTGRES_URL, a local run without one skips.
func TestListMessagesBySeqRange_ReturnsFoldedToolCallMessages_Postgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_URL not set; skipping PostgreSQL store test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "failed to connect to PostgreSQL")
	t.Cleanup(pool.Close)
	migrator, err := NewMigrator(pool, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateUp(ctx), "failed to run migrations")

	store := NewSessionStore(pool, observability.NewNoOpTracer(), zap.NewNop())
	userID := hrUniqueID("fold-user")
	sessionID := hrUniqueID("fold-sess")
	_, err = pool.Exec(ctx, "INSERT INTO sessions (id, agent_id, user_id) VALUES ($1, 'agent-1', $2)", sessionID, userID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM memory_snapshots WHERE session_id = $1", sessionID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM messages WHERE user_id = $1", userID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM sessions WHERE id = $1", sessionID)
	})
	userCtx := ContextWithUserID(ctx, userID)

	call := llmtypes.ToolCall{ID: "toolu_kept", Name: "crashy", Input: map[string]interface{}{"n": float64(1)}}
	require.NoError(t, store.SaveMessage(userCtx, sessionID, &agent.Message{Role: "user", Content: "go"}, true))
	require.NoError(t, store.SaveMessage(userCtx, sessionID, &agent.Message{Role: "assistant", ToolCalls: []llmtypes.ToolCall{call}}, false))
	require.NoError(t, store.SaveMessage(userCtx, sessionID, &agent.Message{Role: "user", Content: "next"}, true))

	var assistantSeq int64
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT id FROM messages WHERE session_id = $1 AND role = 'assistant'", sessionID).Scan(&assistantSeq))
	require.NoError(t, store.FoldMessages(userCtx, sessionID, []int64{assistantSeq}, 1, "folded summary", nil))

	live, err := store.LoadMessages(userCtx, sessionID)
	require.NoError(t, err)
	for _, m := range live {
		require.NotEqual(t, "assistant", m.Role, "reload drops the folded assistant row")
	}

	all, err := store.ListMessagesBySeqRange(userCtx, sessionID, 1, math.MaxInt64)
	require.NoError(t, err)
	var folded []agent.Message
	for _, m := range all {
		if m.Folded && m.Role == "assistant" && len(m.ToolCalls) > 0 {
			folded = append(folded, m)
		}
	}
	require.Len(t, folded, 1, "the folded assistant row comes back from the full-range read")
	require.Equal(t, call.ID, folded[0].ToolCalls[0].ID)
	require.Equal(t, call.Name, folded[0].ToolCalls[0].Name)
}

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
	"strings"
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
	require.NoError(t, store.FoldMessages(userCtx, sessionID, []int64{assistantSeq}, 1, "folded summary"))

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

// The in-loop fold keeps a turn's words and folds its results; the stored
// assistant row keeps its calls. A reload from Postgres must leave out the
// calls whose results are stored folded, as the live session does, and keep
// a call with no stored result, which the compile answers with the synthetic
// "did not complete" result. Runs in the ordinary gate: CI sets
// TEST_POSTGRES_URL, a local run without one skips.
func TestReloadDropsCallsWhoseResultsAreFolded_Postgres(t *testing.T) {
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
	userID := hrUniqueID("reload-user")
	sessionID := hrUniqueID("reload-sess")
	_, err = pool.Exec(ctx, "INSERT INTO sessions (id, agent_id, user_id) VALUES ($1, 'agent-1', $2)", sessionID, userID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM memory_snapshots WHERE session_id = $1", sessionID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM messages WHERE user_id = $1", userID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM sessions WHERE id = $1", sessionID)
	})
	userCtx := ContextWithUserID(ctx, userID)

	save := func(m agent.Message, turnStart bool) {
		t.Helper()
		require.NoError(t, store.SaveMessage(userCtx, sessionID, &m, turnStart))
	}
	in := map[string]interface{}{}
	save(agent.Message{Role: "user", Content: "do the job"}, true)
	save(agent.Message{Role: "assistant", Content: "reading both", ToolCalls: []llmtypes.ToolCall{
		{ID: "toolu_folded_a", Name: "step", Input: in}, {ID: "toolu_folded_b", Name: "step", Input: in}}}, false)
	save(agent.Message{Role: "tool", ToolUseID: "toolu_folded_a", Content: "ok a"}, false)
	save(agent.Message{Role: "tool", ToolUseID: "toolu_folded_b", Content: "ok b"}, false)
	save(agent.Message{Role: "assistant", Content: "one more", ToolCalls: []llmtypes.ToolCall{
		{ID: "toolu_lost", Name: "step", Input: in}}}, false)

	// Fold the first step's results, as the in-loop fold does for a kept turn.
	rows, err := pool.Query(ctx, "SELECT id FROM messages WHERE session_id = $1 AND role = 'tool'", sessionID)
	require.NoError(t, err)
	var seqs []int64
	for rows.Next() {
		var seq int64
		require.NoError(t, rows.Scan(&seq))
		seqs = append(seqs, seq)
	}
	rows.Close()
	require.NoError(t, rows.Err())
	require.Len(t, seqs, 2)
	require.NoError(t, store.FoldMessages(userCtx, sessionID, seqs, 1, "folded summary"))

	session := agent.NewMemoryWithStore(store).GetOrCreateSession(userCtx, sessionID)
	segMem, ok := session.SegmentedMem.(*agent.SegmentedMemory)
	require.True(t, ok, "the reloaded session has no segmented memory")
	var calls, synthetic []string
	for _, m := range segMem.GetMessagesForLLM() {
		for _, c := range m.ToolCalls {
			calls = append(calls, c.ID)
		}
		if m.Role == "tool" && strings.HasPrefix(m.Content, "[no result recorded") {
			synthetic = append(synthetic, m.ToolUseID)
		}
	}
	require.Equal(t, []string{"toolu_lost"}, calls, "a reloaded row still sends a call whose result is folded")
	require.Equal(t, []string{"toolu_lost"}, synthetic, "the reload reports a call that ran as not completed, or lost the re-run door")
}

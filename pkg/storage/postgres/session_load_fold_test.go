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
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/agent"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
)

// The agent restores a session through LoadSession
// (Memory.GetOrCreateSessionWithAgent), not LoadMessages. After an in-loop
// fold folds a tool result and keeps the assistant row for its words, both
// reads must drop the call; otherwise the restored model is told a call that
// completed "did not complete" and may run it again. The store runs as a role
// subject to row-level security, as the server does.
func TestLoadSession_DropsCallsWhoseResultsAreFolded_Postgres(t *testing.T) {
	admin := migratedAdminPool(t)
	ctx := context.Background()
	store := NewSessionStore(rlsSubjectPool(t, admin), observability.NewNoOpTracer(), zap.NewNop())

	userID := hrUniqueID("fold-load-user")
	sessionID := hrUniqueID("fold-load-sess")
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = admin.Exec(bg, "DELETE FROM memory_snapshots WHERE session_id = $1", sessionID)
		_, _ = admin.Exec(bg, "DELETE FROM messages WHERE session_id = $1", sessionID)
		_, _ = admin.Exec(bg, "DELETE FROM sessions WHERE id = $1", sessionID)
	})
	userCtx := ContextWithUserID(ctx, userID)
	require.NoError(t, store.SaveSession(userCtx, &agent.Session{ID: sessionID, AgentID: "agent-1", UserID: userID}))

	folded := llmtypes.ToolCall{ID: "toolu_folded", Name: "rows", Input: map[string]interface{}{}}
	open := llmtypes.ToolCall{ID: "toolu_open", Name: "rows", Input: map[string]interface{}{}}
	for _, m := range []*agent.Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", Content: "let me check", ToolCalls: []llmtypes.ToolCall{folded}},
		{Role: "tool", ToolUseID: folded.ID, Content: "rows"},
		{Role: "user", Content: "next"},
		// A call that never got a result stays, so compile can answer it
		// with the synthetic failed result.
		{Role: "assistant", Content: "again", ToolCalls: []llmtypes.ToolCall{open}},
	} {
		require.NoError(t, store.SaveMessage(userCtx, sessionID, m, m.Role == "user"))
	}

	before, err := store.LoadMessages(userCtx, sessionID)
	require.NoError(t, err)
	var resultSeq int64
	for _, m := range before {
		if m.Role == "tool" {
			resultSeq, err = strconv.ParseInt(m.ID, 10, 64)
			require.NoError(t, err)
		}
	}
	require.NotZero(t, resultSeq, "the tool result row was saved")
	require.NoError(t, store.FoldMessages(userCtx, sessionID, []int64{resultSeq}, 1, "folded summary"))

	callIDs := func(ms []agent.Message) []string {
		var ids []string
		for _, m := range ms {
			for _, c := range m.ToolCalls {
				ids = append(ids, c.ID)
			}
		}
		return ids
	}

	viaMessages, err := store.LoadMessages(userCtx, sessionID)
	require.NoError(t, err)
	assert.Equal(t, []string{open.ID}, callIDs(viaMessages), "LoadMessages drops only the folded call")

	sess, err := store.LoadSession(userCtx, sessionID)
	require.NoError(t, err)
	assert.Equal(t, []string{open.ID}, callIDs(sess.Messages), "LoadSession drops only the folded call")
	for _, m := range sess.Messages {
		assert.Equal(t, userID, m.UserID, "LoadSession sets the owner on each message")
	}

	// What the model is sent after a restore.
	restored := agent.NewMemoryWithStore(store).GetOrCreateSessionWithAgent(userCtx, sessionID, "agent-1", "")
	sm, ok := restored.SegmentedMem.(*agent.SegmentedMemory)
	require.True(t, ok)
	var sent []string
	for _, m := range sm.GetMessagesForLLM() {
		sent = append(sent, m.Role+"|"+m.ToolUseID+"|"+m.Content)
	}
	joined := strings.Join(sent, "\n")
	assert.NotContains(t, joined, "|"+folded.ID+"|", "the folded call is not sent after a restore")
	assert.Contains(t, joined, "|"+open.ID+"|", "the unanswered call is still answered for the model")
}

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

// D-4 (Admission + valve eviction + recall, Part B) — Seam 3 (recall_context builtin)
// component acceptance tests.
//
// Drive RecallContextTool.Execute directly — the real external interface a tool-calling LLM
// (or the executor on its behalf) uses to invoke the builtin — backed by a real in-process
// SQLite SessionStore (temp file, no server), following the same pattern as
// session_store_cross_session_test.go. They assert:
//
//   - recall-context-returns-capped-ballast-tail-cross-turn: a ref evicted by a real
//     ValveEvict pass in one turn resolves, in a later turn of the same live session, to the
//     original durable content, capped at the 4096-byte admission threshold; an optional
//     query excerpts a window around the first match instead of a naive head-truncation; the
//     tool's own result classifies ballast (re-evictable, per ContextClassHint).
//   - recall-context-fail-closed-cross-session-sql-ref-delegates: a ref that resolves inside
//     its owning session fails closed (a generic "ref not available" error, never another
//     session's bytes) when looked up from a different session; a SQL/data reference (a
//     32-character hex id, storage.GenerateID()'s shape) is never resolved here — it errors
//     and points the caller at query_tool_result instead.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/session"
)

// TestRecallContext_CrossTurnReturnsCappedBallastContent evicts a real ballast tool result via
// ValveEvict (turn N), then resolves the resulting stub's ref via RecallContextTool.Execute in
// a later turn of the same live session (turn N+1) — proving the two seams agree on the ref
// (ToolUseID, not Message.ID, per the LLD's grounded correction) and that the recovered
// content is the original durable row, capped at the 4096-byte admission threshold.
func TestRecallContext_CrossTurnReturnsCappedBallastContent(t *testing.T) {
	store := newContextClassSQLiteStore(t)
	sessionID := "recall-cross-turn"
	bgCtx := context.Background()
	require.NoError(t, store.SaveSession(bgCtx, &Session{ID: sessionID, Context: make(map[string]interface{}), CreatedAt: time.Now(), UpdatedAt: time.Now()}))

	// Turn N: a large ballast result (marker embedded well past the 4096-byte cap), plus
	// enough small trailing ballast items that the big one clears the newest-3 window and
	// the payoff bar (~20001 tokens) on its own.
	const marker = "MARKER_FIND_ME_PAST_THE_CAP"
	original := sentenceRepeat(2000) + marker + sentenceRepeat(2000) // ~180KB, marker centered

	sm := NewSegmentedMemory("rom", 0, 0)
	sm.SetSessionStore(store, sessionID)
	addBallastTurn(t, bgCtx, sm, store, sessionID, "toolu_recall_me", "get_customer_orders", original, ClassBallast)
	addBallastTurn(t, bgCtx, sm, store, sessionID, "toolu_keep1", "get_customer_orders", sentenceRepeat(5), ClassBallast)
	addBallastTurn(t, bgCtx, sm, store, sessionID, "toolu_keep2", "get_customer_orders", sentenceRepeat(5), ClassBallast)
	addBallastTurn(t, bgCtx, sm, store, sessionID, "toolu_keep3", "get_customer_orders", sentenceRepeat(5), ClassBallast)

	sm.ValveEvict(bgCtx)
	stub := messageByToolUseID(t, sm.GetMessages(), "toolu_recall_me")
	require.True(t, strings.HasPrefix(stub.Content, evictedStubPrefix), "precondition: the result must actually have been evicted")
	require.Contains(t, stub.Content, "recall_context('toolu_recall_me')", "the stub must name this exact ToolUseID as its ref")

	// Turn N+1: recall_context resolves the ref, scoped to this session via ctx.
	memory := NewMemoryWithStore(store)
	tool := NewRecallContextTool(memory)
	turnCtx := session.WithSessionID(context.Background(), sessionID)

	t.Run("full recall capped at admission threshold", func(t *testing.T) {
		result, err := tool.Execute(turnCtx, map[string]interface{}{"ref": "toolu_recall_me"})
		require.NoError(t, err)
		require.True(t, result.Success, "a valve ref belonging to this session must resolve successfully")

		content, ok := result.Data.(string)
		require.True(t, ok, "recall_context must return its recovered content as a string")
		assert.LessOrEqual(t, len(content), recallCapBytes, "recovered content must be capped at the 4096-byte admission threshold")
		assert.Equal(t, capBytes(original, recallCapBytes), content, "uncapped recall must return exactly the durable row's original content, head-capped")
	})

	t.Run("query excerpts a window around the match beyond the cap", func(t *testing.T) {
		result, err := tool.Execute(turnCtx, map[string]interface{}{"ref": "toolu_recall_me", "query": marker})
		require.NoError(t, err)
		require.True(t, result.Success)

		content, ok := result.Data.(string)
		require.True(t, ok)
		assert.Contains(t, content, marker, "a query matching content past the naive head-cap must still surface it via excerpting")
		assert.LessOrEqual(t, len(content), recallCapBytes)
	})

	t.Run("recovered result classifies ballast (re-evictable, tail-appendable)", func(t *testing.T) {
		assert.Equal(t, ClassBallast, tool.ContextClassHint(), "recall_context's own result must classify ballast so it is re-evictable")
	})
}

// TestRecallContext_FailClosedCrossSessionRefAndSQLRefDelegates covers
// recall-context-fail-closed-cross-session-sql-ref-delegates.
func TestRecallContext_FailClosedCrossSessionRefAndSQLRefDelegates(t *testing.T) {
	store := newContextClassSQLiteStore(t)
	ctx := context.Background()

	ownerSession := "owner-session"
	otherSession := "other-session"
	require.NoError(t, store.SaveSession(ctx, &Session{ID: ownerSession, Context: make(map[string]interface{}), CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	require.NoError(t, store.SaveSession(ctx, &Session{ID: otherSession, Context: make(map[string]interface{}), CreatedAt: time.Now(), UpdatedAt: time.Now()}))

	require.NoError(t, store.SaveMessage(ctx, ownerSession, Message{
		Role:         "tool",
		Content:      "the owner session's private ballast content",
		ToolUseID:    "toolu_owned",
		ContextClass: ClassBallast,
		Timestamp:    time.Now(),
	}))

	memory := NewMemoryWithStore(store)
	tool := NewRecallContextTool(memory)

	t.Run("resolves inside its owning session", func(t *testing.T) {
		ownerCtx := session.WithSessionID(context.Background(), ownerSession)
		result, err := tool.Execute(ownerCtx, map[string]interface{}{"ref": "toolu_owned"})
		require.NoError(t, err)
		require.True(t, result.Success, "sanity check: the ref must be genuinely resolvable in its own session")
		assert.Equal(t, "the owner session's private ballast content", result.Data)
	})

	t.Run("fails closed from a different session, never leaking the owner's bytes", func(t *testing.T) {
		otherCtx := session.WithSessionID(context.Background(), otherSession)
		result, err := tool.Execute(otherCtx, map[string]interface{}{"ref": "toolu_owned"})
		require.NoError(t, err, "a cross-session ref is a tool error, not a Go error")
		require.False(t, result.Success)
		require.NotNil(t, result.Error)
		assert.Equal(t, "REF_NOT_AVAILABLE", result.Error.Code)
		assert.NotContains(t, result.Error.Message, "owner session's private ballast content", "the error must never surface the other session's bytes")
	})

	t.Run("a SQL/data reference delegates to query_tool_result instead of resolving here", func(t *testing.T) {
		sqlRef := "0123456789abcdef0123456789abcdef" // 32-char lowercase hex: storage.GenerateID()'s shape
		anyCtx := session.WithSessionID(context.Background(), ownerSession)
		result, err := tool.Execute(anyCtx, map[string]interface{}{"ref": sqlRef})
		require.NoError(t, err)
		require.False(t, result.Success)
		require.NotNil(t, result.Error)
		assert.Equal(t, "SQL_REF_NOT_SUPPORTED", result.Error.Code)
		assert.Contains(t, result.Error.Suggestion, "query_tool_result", "the error must point the caller at query_tool_result for SQL/data refs")
	})
}

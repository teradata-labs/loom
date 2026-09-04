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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/session"
)

// TestRecallTool_IncludesSyntheticRoles proves that skill_body and
// hygiene_injection rows — recoverable text content exactly like user and
// assistant rows — are not silently dropped by the recall tool's allowlist.
// Before the role split these rows were persisted under role="user" and so
// passed the old `m.Role != "user" && m.Role != "assistant"` check; after
// the split they carry their own roles and must pass via
// IsSyntheticWireUserRole instead. Only role="tool" rows are legitimately
// excluded (HLD §6: their only door is re-running the call).
func TestRecallTool_IncludesSyntheticRoles(t *testing.T) {
	tmpfile := t.TempDir() + "/recall-test.db"
	defer func() { _ = os.Remove(tmpfile) }()

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpfile, tracer)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	const sessionID = "sess-recall-synthetic"

	require.NoError(t, store.SaveSession(ctx, &Session{
		ID:        sessionID,
		Context:   map[string]interface{}{},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}))

	const skillBodySentinel = "alpha-skill-body-sentinel-content"
	const hygieneSentinel = "hygiene-retry-nudge-sentinel-content"

	userMsg := &Message{Role: "user", Content: "load alpha", Timestamp: time.Now()}
	require.NoError(t, store.SaveMessage(ctx, sessionID, userMsg, true))

	skillBodyMsg := &Message{Role: "skill_body", Content: skillBodySentinel, Timestamp: time.Now()}
	require.NoError(t, store.SaveMessage(ctx, sessionID, skillBodyMsg, false))

	hygieneMsg := &Message{Role: "hygiene_injection", Content: hygieneSentinel, Timestamp: time.Now()}
	require.NoError(t, store.SaveMessage(ctx, sessionID, hygieneMsg, false))

	assistantMsg := &Message{Role: "assistant", Content: "done", Timestamp: time.Now()}
	require.NoError(t, store.SaveMessage(ctx, sessionID, assistantMsg, false))

	toolMsg := &Message{Role: "tool", Content: "tool-result-must-stay-excluded", ToolUseID: "c1", Timestamp: time.Now()}
	require.NoError(t, store.SaveMessage(ctx, sessionID, toolMsg, false))

	ag := &Agent{memory: NewMemoryWithStore(store)}
	tool := NewRecallTool(ag)

	callCtx := session.WithSessionID(ctx, sessionID)
	result, err := tool.Execute(callCtx, map[string]interface{}{
		"range": "msg:" + userMsg.ID + "-" + toolMsg.ID,
	})
	require.NoError(t, err)
	require.True(t, result.Success)

	out, ok := result.Data.(string)
	require.True(t, ok, "recall result data must be a string")

	require.Contains(t, out, skillBodySentinel, "skill_body rows must be recoverable via recall, not silently dropped")
	require.Contains(t, out, "skill_body:", "skill_body rows must be rendered under their own role")

	require.Contains(t, out, hygieneSentinel, "hygiene_injection rows must be recoverable via recall, not silently dropped")
	require.Contains(t, out, "hygiene_injection:", "hygiene_injection rows must be rendered under their own role")

	require.NotContains(t, out, "tool-result-must-stay-excluded", "role=tool rows stay excluded — their only door is re-running the call")

	require.True(t, strings.Contains(out, "load alpha") && strings.Contains(out, "done"),
		"user/assistant rows must remain recallable alongside the synthetic roles")
}

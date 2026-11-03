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
	"github.com/teradata-labs/loom/pkg/types"
)

// Every row of a turn is for whoever the turn is for: a row that does not say
// takes the session_context of its turn's first row, and the in-memory message
// is stamped with what was stored. A row that says keeps it, and a turn that
// says nothing is direct.
func TestPGARowIsForWhoeverItsTurnIsFor(t *testing.T) {
	_, pool := artifactScopeStore(t)
	store := NewSessionStore(pool, observability.NewNoOpTracer(), nil)
	user, sessionID := asUniqueID("kind-user"), asUniqueID("kind-conv")
	ctx := ContextWithUserID(context.Background(), user)
	require.NoError(t, store.SaveSession(ctx, &agent.Session{
		ID: sessionID, AgentID: "agent-1", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM messages WHERE session_id = $1", sessionID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM sessions WHERE id = $1", sessionID)
	})

	write := func(role string, kind types.SessionContext, start bool) *agent.Message {
		m := &agent.Message{Role: role, Content: role + " words", SessionContext: kind, Timestamp: time.Now().UTC()}
		require.NoError(t, store.SaveMessage(ctx, sessionID, m, start))
		return m
	}
	write("user", "", true)
	write("assistant", "", false)
	write("user", "loop:run-1@7", true)
	thinking := write("assistant", "", false)
	write("assistant", "coordinator", false)
	write("user", "", true)
	write("assistant", "", false)

	want := []types.SessionContext{"direct", "direct", "loop:run-1@7", "loop:run-1@7", "coordinator", "direct", "direct"}
	got, err := store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, got, len(want))
	for i, m := range got {
		require.Equal(t, want[i], m.SessionContext, "row %d (%s)", i+1, m.Role)
	}
	require.Equal(t, types.SessionContext("loop:run-1@7"), thinking.SessionContext, "the in-memory message was not stamped")
}

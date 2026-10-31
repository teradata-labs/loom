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

//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/agent"
)

// What a session's tools did, and how many turns it has had, are the owner's
// to read. Another user who names the session reads nothing, as if it were not
// there.
func TestSessionReads_AnotherUsersSessionReadsAsMissing(t *testing.T) {
	pool := testPool(t)
	store := testSessionStore(t, pool)
	owner := ContextWithUserID(context.Background(), uniqueID("owner"))
	other := ContextWithUserID(context.Background(), uniqueID("other"))
	id := uniqueID("owned-session")

	require.NoError(t, store.SaveSession(owner, &agent.Session{ID: id, CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	require.NoError(t, store.SaveMessage(owner, id, &agent.Message{Role: "user", Content: "hi", Timestamp: time.Now()}, true))
	require.NoError(t, store.SaveToolExecution(owner, id, agent.ToolExecution{
		ToolName: "shell_execute", Input: map[string]interface{}{"command": "cat ~/.ssh/id_ed25519"},
	}))

	mine, err := store.ToolExecutions(owner, id)
	require.NoError(t, err)
	require.Len(t, mine, 1, "the owner reads their own tool calls")
	turns, err := store.MaxTurn(owner, id)
	require.NoError(t, err)
	require.Equal(t, 1, turns)

	theirs, err := store.ToolExecutions(other, id)
	require.NoError(t, err)
	require.Empty(t, theirs, "another user read the owner's tool calls")
	turns, err = store.MaxTurn(other, id)
	require.NoError(t, err)
	require.Zero(t, turns, "another user read the owner's turn count")
}

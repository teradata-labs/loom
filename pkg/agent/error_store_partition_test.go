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
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/session"
)

func newTestErrorStore(t *testing.T) *SQLiteErrorStore {
	t.Helper()

	store, err := NewSQLiteErrorStore(filepath.Join(t.TempDir(), "errors.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// storeOwnedError persists an error owned by ownerSession and returns its id.
func storeOwnedError(t *testing.T, store *SQLiteErrorStore, ownerSession string) string {
	t.Helper()

	id, err := store.Store(context.Background(), &StoredError{
		SessionID:    ownerSession,
		ToolName:     "sql_query",
		RawError:     json.RawMessage(`{"message":"owner-only detail"}`),
		ShortSummary: "owner-only detail",
	})
	require.NoError(t, err)
	return id
}

// AC3: a get_error_details (ErrorStore.Get) for an error id owned by another
// session resolves to not-found for the caller.
func TestErrorStore_ForeignSessionGetIsNotFound(t *testing.T) {
	store := newTestErrorStore(t)
	id := storeOwnedError(t, store, "session-owner")

	intruderCtx := session.WithSessionID(context.Background(), "session-intruder")
	stored, err := store.Get(intruderCtx, id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.Nil(t, stored, "foreign caller must receive no error details")
}

// AC4 (error store): the owning session resolves its own error id.
func TestErrorStore_OwningSessionResolvesError(t *testing.T) {
	store := newTestErrorStore(t)
	id := storeOwnedError(t, store, "session-owner")

	ownerCtx := session.WithSessionID(context.Background(), "session-owner")
	stored, err := store.Get(ownerCtx, id)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, id, stored.ID)
	assert.Equal(t, "session-owner", stored.SessionID)
}

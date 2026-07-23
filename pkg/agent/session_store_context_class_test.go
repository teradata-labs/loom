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

// D-3 (Context classification & persistence, Part A) — AC-3 SQLite-half component tests.
//
// context_class is a nullable column added to the SQLite `messages` table (additive
// migration, mirrors agent_id). These tests drive the real SessionStore.SaveMessage /
// LoadMessages / LoadSession surface (in-process SQLite, no server) and assert the
// column round-trips every ContextClass value, including the nullable/legacy empty case.
// The Postgres half of AC-3 (the six scanMessages SELECT feeders) is covered by the
// integration suite against a real Postgres backend.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
)

func newContextClassSQLiteStore(t *testing.T) *SessionStore {
	t.Helper()
	tmpfile := t.TempDir() + "/test.db"
	t.Cleanup(func() { _ = os.Remove(tmpfile) })

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpfile, tracer)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestSessionStore_ContextClass_RoundTrip_AllClasses saves one message per ContextClass
// value (including narrative's empty string) and asserts LoadMessages returns each
// unchanged, in order.
func TestSessionStore_ContextClass_RoundTrip_AllClasses(t *testing.T) {
	store := newContextClassSQLiteStore(t)
	ctx := context.Background()

	session := &Session{
		ID:        "context-class-roundtrip",
		Context:   make(map[string]interface{}),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, store.SaveSession(ctx, session))

	base := time.Now()
	fixtures := []struct {
		name  string
		class ContextClass
	}{
		{"narrative", ClassNarrative},
		{"charter", ClassCharter},
		{"ledger", ClassLedger},
		{"ballast", ClassBallast},
	}

	for i, f := range fixtures {
		msg := Message{
			Role:         "user",
			Content:      "msg-" + f.name,
			ContextClass: string(f.class),
			Timestamp:    base.Add(time.Duration(i) * time.Second),
		}
		require.NoError(t, store.SaveMessage(ctx, session.ID, msg))
	}

	loaded, err := store.LoadMessages(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, loaded, len(fixtures))

	for i, f := range fixtures {
		assert.Equal(t, "msg-"+f.name, loaded[i].Content, "message order must be preserved")
		assert.Equal(t, string(f.class), loaded[i].ContextClass, "ContextClass must round-trip unchanged for %s", f.name)
	}
}

// TestSessionStore_LoadSession_PreservesContextClass verifies the primary restore path
// (LoadSession, which populates session.Messages) carries context_class through, not just
// the standalone LoadMessages call.
func TestSessionStore_LoadSession_PreservesContextClass(t *testing.T) {
	store := newContextClassSQLiteStore(t)
	ctx := context.Background()

	session := &Session{
		ID:        "context-class-load-session",
		Context:   make(map[string]interface{}),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, store.SaveSession(ctx, session))

	require.NoError(t, store.SaveMessage(ctx, session.ID, Message{
		Role:         "assistant",
		Content:      "charter message",
		ContextClass: string(ClassCharter),
		Timestamp:    time.Now(),
	}))
	require.NoError(t, store.SaveMessage(ctx, session.ID, Message{
		Role:      "user",
		Content:   "legacy narrative message",
		Timestamp: time.Now().Add(time.Second),
		// ContextClass intentionally left empty, simulating a pre-migration row.
	}))

	loaded, err := store.LoadSession(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, loaded.Messages, 2)

	assert.Equal(t, string(ClassCharter), loaded.Messages[0].ContextClass, "LoadSession must carry a persisted non-empty class through")
	assert.Equal(t, "", loaded.Messages[1].ContextClass, "LoadSession must preserve a NULL/empty context_class as the empty string, not error or default it")
}

// TestSessionStore_ContextClass_EmptyIsNullNotError verifies that saving a message with an
// empty ContextClass (the common case pre-D-3 and for narrative messages) never trips the
// nullable-column plumbing — it must persist as NULL and load back as "".
func TestSessionStore_ContextClass_EmptyIsNullNotError(t *testing.T) {
	store := newContextClassSQLiteStore(t)
	ctx := context.Background()

	session := &Session{
		ID:        "context-class-null",
		Context:   make(map[string]interface{}),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, store.SaveSession(ctx, session))

	require.NoError(t, store.SaveMessage(ctx, session.ID, Message{
		Role:      "assistant",
		Content:   "no class set",
		Timestamp: time.Now(),
	}))

	loaded, err := store.LoadMessages(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, "", loaded[0].ContextClass)
}

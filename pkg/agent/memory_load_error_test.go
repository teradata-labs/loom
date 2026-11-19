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
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/teradata-labs/loom/pkg/observability"
)

// loadFailingStore is the SQLite store with a LoadSession that fails with
// err, as a store whose database is unreachable does.
type loadFailingStore struct {
	*SessionStore
	err      error
	existsOK bool
}

func (s loadFailingStore) LoadSession(context.Context, string) (*Session, error) {
	return nil, s.err
}

// SessionExists fails too unless existsOK: an unreachable store answers
// nothing. With existsOK the row is there and only its load fails (a decode
// error, a timeout on the message read).
func (s loadFailingStore) SessionExists(ctx context.Context, id string) (bool, error) {
	if s.existsOK {
		return true, nil
	}
	return false, s.err
}

// A store that fails to load a session is not a new session: the session
// starts with no history in memory, and an error-level line with the session
// id says why. A session the store does not have (ErrSessionNotFound, which
// loom's SQLite store returns for every new session) logs nothing.
func TestGetOrCreateSession_LogsAStoreThatCannotLoadTheSession(t *testing.T) {
	tests := []struct {
		name     string
		loadErr  error // nil: the plain SQLite store, with no such session
		existsOK bool
		wantLog  bool
	}{
		{name: "the store is unreachable", loadErr: errors.New("dial tcp: connection refused"), wantLog: true},
		{name: "the row exists but its load fails", loadErr: errors.New("dial tcp: connection refused"), existsOK: true, wantLog: true},
		{name: "the store has no such session", loadErr: nil, wantLog: false},
		{name: "an embedder store wraps ErrSessionNotFound", loadErr: errors.Join(errors.New("no row"), ErrSessionNotFound), wantLog: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.ErrorLevel)
			restore := zap.ReplaceGlobals(zap.New(core))
			defer restore()

			sqlite, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
			require.NoError(t, err, "the store must open")
			t.Cleanup(func() { _ = sqlite.Close() })
			var store SessionStorage = sqlite
			if tt.loadErr != nil {
				store = loadFailingStore{SessionStore: sqlite, err: tt.loadErr, existsOK: tt.existsOK}
			}

			sess := NewMemoryWithStore(store).GetOrCreateSession(context.Background(), "s-load")
			require.NotNil(t, sess, "a session is always returned")
			assert.Empty(t, sess.Messages, "the session starts with no history")

			found := logs.FilterMessageSnippet("could not load the session").All()
			if !tt.wantLog {
				assert.Empty(t, found, "a session the store does not have was logged as a failed load")
				return
			}
			require.Len(t, found, 1, "the failed load was not logged once")
			fields := found[0].ContextMap()
			assert.Equal(t, "s-load", fields["session_id"], "the line does not name the session")
			assert.Contains(t, fields["error"], "connection refused", "the line does not carry the store's error")
		})
	}
}

// flakyLoadStore fails LoadSession while down is set and counts the loads
// and the session saves it sees.
type flakyLoadStore struct {
	*SessionStore
	down  atomic.Bool
	loads atomic.Int32
	saves atomic.Int32
}

func (s *flakyLoadStore) LoadSession(ctx context.Context, id string) (*Session, error) {
	s.loads.Add(1)
	if s.down.Load() {
		return nil, errors.New("dial tcp: connection refused")
	}
	return s.SessionStore.LoadSession(ctx, id)
}

func (s *flakyLoadStore) SaveSession(ctx context.Context, sess *Session) error {
	s.saves.Add(1)
	return s.SessionStore.SaveSession(ctx, sess)
}

// A session whose load failed is served empty for that turn only: it is
// cached (in-turn lookups find it), it does not overwrite the stored row, the
// next GetOrCreateSession retries the load, and a load that succeeds brings
// the history back and ends the retries.
func TestGetOrCreateSession_RetriesAFailedLoadOnTheNextCall(t *testing.T) {
	ctx := context.Background()
	sqlite, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err, "the store must open")
	t.Cleanup(func() { _ = sqlite.Close() })
	require.NoError(t, sqlite.SaveSession(ctx, &Session{ID: "s-flaky", AgentID: "a", Context: map[string]interface{}{}}))
	require.NoError(t, sqlite.SaveMessage(ctx, "s-flaky", &Message{Role: "user", Content: "earlier question"}, true))

	store := &flakyLoadStore{SessionStore: sqlite}
	store.down.Store(true)
	mem := NewMemoryWithStore(store)

	first := mem.GetOrCreateSession(ctx, "s-flaky")
	assert.Empty(t, first.Messages, "a failed load starts the turn with no history")
	inTurn, ok := mem.GetSession("s-flaky")
	require.True(t, ok, "the turn's session is cached for in-turn lookups")
	assert.Same(t, first, inTurn)
	assert.Zero(t, store.saves.Load(), "the empty placeholder overwrote the stored session row")

	again := mem.GetOrCreateSession(ctx, "s-flaky")
	assert.Same(t, first, again, "a retry that fails again keeps serving the same session")
	assert.Equal(t, int32(2), store.loads.Load(), "the second call did not retry the load")

	store.down.Store(false)
	recovered := mem.GetOrCreateSession(ctx, "s-flaky")
	assert.NotSame(t, first, recovered, "the recovered load did not replace the empty session")
	require.Len(t, recovered.Messages, 1, "the stored history is back")
	assert.Equal(t, "earlier question", recovered.Messages[0].Content)

	loads := store.loads.Load()
	assert.Same(t, recovered, mem.GetOrCreateSession(ctx, "s-flaky"))
	assert.Equal(t, loads, store.loads.Load(), "a loaded session is reloaded again")
	assert.Zero(t, store.saves.Load(), "no placeholder save at any point")
}

// A turn served from the empty placeholder of a failed load must not save it
// over the stored session row: the end-of-turn save (chat), the failed-turn
// save and CreateSession's name save all go through PersistSession, which
// leaves the row as stored. The turn's own messages are still saved.
func TestPersistSession_LeavesTheStoredRowAloneAfterAFailedLoad(t *testing.T) {
	ctx := context.Background()
	sqlite, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlite.Close() })
	require.NoError(t, sqlite.SaveSession(ctx, &Session{ID: "s-keep", Name: "keep-me", AgentID: "a",
		Context: map[string]interface{}{"k": "v"}, TotalCostUSD: 5.0, TotalTokens: 1000}))
	require.NoError(t, sqlite.SaveMessage(ctx, "s-keep", &Message{Role: "user", Content: "earlier"}, true))

	store := &flakyLoadStore{SessionStore: sqlite}
	store.down.Store(true)
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &simpleLLM{response: "hi"}, WithConfig(cfg), WithMemory(NewMemoryWithStore(store)))

	_, err = ag.Chat(ctx, "s-keep", "new question")
	require.NoError(t, err)
	ag.CreateSession(ctx, "s-keep", "renamed by a placeholder")

	got, err := sqlite.LoadSession(ctx, "s-keep")
	require.NoError(t, err)
	assert.Equal(t, "keep-me", got.Name, "the placeholder overwrote the stored name")
	assert.GreaterOrEqual(t, got.TotalCostUSD, 5.0, "the placeholder overwrote the stored cost")
	assert.Equal(t, "v", got.Context["k"], "the placeholder overwrote the stored context")
	assert.Zero(t, store.saves.Load(), "a session row was saved from the placeholder")
	var contents []string
	for _, m := range got.Messages {
		contents = append(contents, m.Content)
	}
	assert.Contains(t, contents, "new question", "the turn's own message was not saved")

	// Once the store answers, the next call loads the real session and later
	// saves go through again.
	store.down.Store(false)
	sess := ag.memory.GetOrCreateSession(ctx, "s-keep")
	assert.Equal(t, "keep-me", sess.Name)
	require.NoError(t, ag.memory.PersistSession(ctx, sess))
	assert.Equal(t, int32(1), store.saves.Load(), "a loaded session's save was skipped")
}

// hostNotFoundStore is a host store whose LoadSession reports a missing
// session with its own error, not wrapping ErrSessionNotFound (tera-backend
// wraps its database.ErrNotFound).
type hostNotFoundStore struct {
	*SessionStore
	saves atomic.Int32
}

var errHostNotFound = errors.New("database: not found")

func (s *hostNotFoundStore) LoadSession(ctx context.Context, id string) (*Session, error) {
	if _, err := s.SessionStore.LoadSession(ctx, id); err != nil {
		return nil, fmt.Errorf("failed to load session %s: %w", id, errHostNotFound)
	}
	return s.SessionStore.LoadSession(ctx, id)
}

func (s *hostNotFoundStore) SaveSession(ctx context.Context, sess *Session) error {
	s.saves.Add(1)
	return s.SessionStore.SaveSession(ctx, sess)
}

// A host store's own not-found error is not an outage: SessionExists says the
// row is not there, so the session is created and its row saved (messages
// need the row), with no error line and no reload retry.
func TestGetOrCreateSession_AHostStoresNotFoundCreatesTheSession(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()
	ctx := context.Background()
	sqlite, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlite.Close() })
	store := &hostNotFoundStore{SessionStore: sqlite}
	mem := NewMemoryWithStore(store)

	sess := mem.GetOrCreateSession(ctx, "s-new")
	require.NotNil(t, sess)
	assert.Equal(t, int32(1), store.saves.Load(), "the new session's row was not saved")
	assert.Empty(t, logs.FilterMessageSnippet("could not load the session").All(), "a missing session was logged as a failed load")
	assert.False(t, mem.isLoadPlaceholder(sess), "a new session was marked for reload")
	exists, err := sqlite.SessionExists(ctx, "s-new")
	require.NoError(t, err)
	assert.True(t, exists, "the row messages need was not created")
	require.NoError(t, mem.PersistSession(ctx, sess))
	assert.Equal(t, int32(2), store.saves.Load(), "a later save of the new session was skipped")
}

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
	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

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

	again := mem.ReloadPendingSession(ctx, "s-flaky", "")
	assert.Same(t, first, again, "a retry that fails again keeps serving the same session")
	assert.Equal(t, int32(2), store.loads.Load(), "the turn holder did not retry the load (once, outside the lock)")

	// The store is back. A caller that does not hold the turn (any
	// GetOrCreateSession*) is still served the stand-in: swapping it now
	// would take the session from under a running turn.
	store.down.Store(false)
	assert.Same(t, first, mem.GetOrCreateSession(ctx, "s-flaky"), "a non-holder swapped the session")
	assert.Equal(t, int32(2), store.loads.Load(), "a non-holder retried the load")

	recovered := mem.ReloadPendingSession(ctx, "s-flaky", "")
	assert.NotSame(t, first, recovered, "the recovered load did not replace the empty session")
	require.Len(t, recovered.Messages, 1, "the stored history is back")
	assert.Equal(t, "earlier question", recovered.Messages[0].Content)

	loads := store.loads.Load()
	assert.Same(t, recovered, mem.ReloadPendingSession(ctx, "s-flaky", ""))
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
	_, err = ag.CompactSession(ctx, "s-keep", CompactOptions{})
	require.ErrorIs(t, err, ErrSessionNotLoaded, "a stand-in session was compacted")

	// Once the store answers, the next turn loads the real session (with
	// both turns' messages) and its saves go through again.
	store.down.Store(false)
	_, err = ag.Chat(ctx, "s-keep", "next question")
	require.NoError(t, err)
	sess := ag.memory.GetOrCreateSession(ctx, "s-keep")
	assert.Equal(t, "keep-me", sess.Name, "the next turn did not load the stored session")
	var seen []string
	for _, m := range sess.Messages {
		seen = append(seen, m.Content)
	}
	assert.Contains(t, seen, "new question", "the failed-load turn's message is missing after the reload")
	assert.Contains(t, seen, "earlier", "the stored history is missing after the reload")
	assert.Positive(t, store.saves.Load(), "a loaded session's save was skipped")
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

// Without an execution log there is no session lease, so a turn can still hold
// the stand-in after another caller reloaded the recovered session into the
// cache. A save of that old stand-in must still be skipped: the mark is on
// the object, not on the cache entry.
func TestPersistSession_SkipsAStandInThatWasReplacedInTheCache(t *testing.T) {
	ctx := context.Background()
	sqlite, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlite.Close() })
	require.NoError(t, sqlite.SaveSession(ctx, &Session{ID: "s-stale", Name: "kept-name", AgentID: "a", Context: map[string]interface{}{}}))

	store := &flakyLoadStore{SessionStore: sqlite}
	store.down.Store(true)
	mem := NewMemoryWithStore(store)
	standIn := mem.GetOrCreateSession(ctx, "s-stale")

	store.down.Store(false)
	recovered := mem.ReloadPendingSession(ctx, "s-stale", "")
	require.NotSame(t, standIn, recovered, "the recovered session did not replace the stand-in")

	require.NoError(t, mem.PersistSession(ctx, standIn), "a stand-in save is skipped, not an error")
	got, err := sqlite.LoadSession(ctx, "s-stale")
	require.NoError(t, err)
	assert.Equal(t, "kept-name", got.Name, "the replaced stand-in was saved over the recovered row")

	require.NoError(t, mem.PersistSession(ctx, recovered))
	assert.Equal(t, int32(1), store.saves.Load(), "the recovered session's save was skipped")
}

// Resuming needs the stored history: a session the store still cannot load is
// refused with ErrSessionNotLoaded instead of resuming the empty stand-in.
func TestResume_RefusesASessionTheStoreCannotLoad(t *testing.T) {
	ctx := context.Background()
	sqlite, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlite.Close() })
	require.NoError(t, sqlite.SaveSession(ctx, &Session{ID: "s-down", AgentID: "a", Context: map[string]interface{}{}}))
	store := &flakyLoadStore{SessionStore: sqlite}
	store.down.Store(true)
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg),
		WithEventLog(&recordingEventLog{}), WithMemory(NewMemoryWithStore(store)))

	_, err = ag.ResumeSession(ctx, "s-down")
	assert.ErrorIs(t, err, ErrSessionNotLoaded, "ResumeSession")
	_, err = ag.ResolveExecutionGap(ctx, "s-down", GapSucceeded, []byte(`{}`))
	assert.ErrorIs(t, err, ErrSessionNotLoaded, "ResolveExecutionGap")
	assert.Zero(t, store.saves.Load(), "a refused resume saved the stand-in")
}

// blockingLoadStore blocks LoadSession of one id until released, failing it
// then, as a store timing out during an outage does.
type blockingLoadStore struct {
	*SessionStore
	slowID  string
	entered chan struct{}
	release chan struct{}
	down    atomic.Bool
}

func (s *blockingLoadStore) LoadSession(ctx context.Context, id string) (*Session, error) {
	if id == s.slowID && s.down.Load() {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		<-s.release
		return nil, errors.New("dial tcp: i/o timeout")
	}
	return s.SessionStore.LoadSession(ctx, id)
}

func (s *blockingLoadStore) SessionExists(ctx context.Context, id string) (bool, error) {
	if id == s.slowID && s.down.Load() {
		return false, errors.New("dial tcp: i/o timeout")
	}
	return s.SessionStore.SessionExists(ctx, id)
}

// A retry that waits on a slow store must not hold Memory's lock: another
// session is served meanwhile.
func TestReloadPendingSession_ASlowRetryDoesNotStallOtherSessions(t *testing.T) {
	ctx := context.Background()
	sqlite, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlite.Close() })
	require.NoError(t, sqlite.SaveSession(ctx, &Session{ID: "s-slow", AgentID: "a", Context: map[string]interface{}{}}))
	store := &blockingLoadStore{SessionStore: sqlite, slowID: "s-slow", entered: make(chan struct{}, 1), release: make(chan struct{})}
	mem := NewMemoryWithStore(store)

	// The first load fails at once, so the session becomes a stand-in.
	store.down.Store(true)
	go func() { <-store.entered; store.release <- struct{}{} }()
	standIn := mem.GetOrCreateSession(ctx, "s-slow")

	done := make(chan *Session)
	go func() { done <- mem.ReloadPendingSession(ctx, "s-slow", "") }()
	<-store.entered // the retry is waiting on the store

	other := make(chan *Session)
	go func() { other <- mem.GetOrCreateSession(ctx, "s-other") }()
	select {
	case s := <-other:
		assert.Equal(t, "s-other", s.ID)
	case <-time.After(5 * time.Second):
		t.Fatal("another session stalled behind a retry waiting on the store")
	}
	close(store.release)
	assert.Same(t, standIn, <-done, "a retry that fails keeps the stand-in")
}

// outageStore fails both LoadSession and SessionExists while down, as a store
// that cannot be reached does; otherwise it is the store it wraps.
type outageStore struct {
	SessionStorage
	down atomic.Bool
}

func (s *outageStore) LoadSession(ctx context.Context, id string) (*Session, error) {
	if s.down.Load() {
		return nil, errors.New("dial tcp: connection refused")
	}
	return s.SessionStorage.LoadSession(ctx, id)
}

func (s *outageStore) SessionExists(ctx context.Context, id string) (bool, error) {
	if s.down.Load() {
		return false, errors.New("dial tcp: connection refused")
	}
	return s.SessionStorage.SessionExists(ctx, id)
}

// A brand-new session first seen during an outage is a stand-in; when the
// store comes back and reports it missing, the stand-in becomes the session:
// its mark is cleared, its row is created, and saves and compaction work.
func TestReloadPendingSession_ANewSessionSeenInAnOutageBecomesTheSession(t *testing.T) {
	ctx := context.Background()
	sqlite, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlite.Close() })
	store := &outageStore{SessionStorage: sqlite}
	store.down.Store(true)
	mem := NewMemoryWithStore(store)

	standIn := mem.GetOrCreateSession(ctx, "s-new")
	require.True(t, mem.isLoadPlaceholder(standIn), "a session seen in an outage is a stand-in")

	store.down.Store(false)
	sess := mem.ReloadPendingSession(ctx, "s-new", "a")
	assert.Same(t, standIn, sess, "the stand-in is the session once the store reports it missing")
	assert.False(t, mem.isLoadPlaceholder(sess), "the stand-in mark was not cleared")
	exists, err := sqlite.SessionExists(ctx, "s-new")
	require.NoError(t, err)
	assert.True(t, exists, "the new session's row was not created")
	require.NoError(t, sqlite.SaveMessage(ctx, "s-new", &Message{Role: "user", Content: "hello"}, true), "a message cannot be saved")
	sess.Name = "named later"
	require.NoError(t, mem.PersistSession(ctx, sess))
	got, err := sqlite.LoadSession(ctx, "s-new")
	require.NoError(t, err)
	assert.Equal(t, "named later", got.Name, "a later save of the session was skipped")
}

// sliceProvider is a value-type LLMProvider with a slice field: comparing two
// of them with == panics ("comparing uncomparable type").
type sliceProvider struct{ tags []string }

func (sliceProvider) Chat(context.Context, []Message, []shuttle.Tool) (*LLMResponse, error) {
	return &LLMResponse{Content: "ok"}, nil
}
func (sliceProvider) Name() string  { return "test" }
func (sliceProvider) Model() string { return "test-model" }

// Building an agent around a value-type provider must not compare providers:
// the compressor's served window is chosen without ==.
func TestNewAgent_AcceptsAnUncomparableValueProvider(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	// The compressor (and the window choice) is wired only with a prompt
	// registry.
	reg := prompts.NewFileRegistry("../../prompts")
	require.NoError(t, reg.Reload(context.Background()))
	var ag *Agent
	require.NotPanics(t, func() {
		ag = NewAgent(&mockBackend{}, sliceProvider{tags: []string{"a"}}, WithConfig(cfg), WithPrompts(reg))
	})
	assert.NotNil(t, ag.memory.compressor, "the test did not reach the compressor wiring")
}

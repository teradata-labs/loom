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
	"path/filepath"
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
	err error
}

func (s loadFailingStore) LoadSession(context.Context, string) (*Session, error) {
	return nil, s.err
}

// A store that fails to load a session is not a new session: the session
// starts with no history in memory, and an error-level line with the session
// id says why. A session the store does not have (ErrSessionNotFound, which
// loom's SQLite store returns for every new session) logs nothing.
func TestGetOrCreateSession_LogsAStoreThatCannotLoadTheSession(t *testing.T) {
	tests := []struct {
		name    string
		loadErr error // nil: the plain SQLite store, with no such session
		wantLog bool
	}{
		{name: "the store is unreachable", loadErr: errors.New("dial tcp: connection refused"), wantLog: true},
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
				store = loadFailingStore{SessionStore: sqlite, err: tt.loadErr}
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

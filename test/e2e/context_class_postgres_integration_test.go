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

//go:build integration

package e2e

// D-3 (Context classification & persistence, Part A) — AC-3 Postgres-half integration tests.
//
// context_class is a nullable column added to the PostgreSQL `messages` table (additive
// migration, mirrors agent_id). These tests drive the real pkg/storage/postgres.SessionStore
// surface against a live PostgreSQL instance (test/e2e/docker-compose.yml :5433, the same
// backend the pack's looms server runs against) — no mocks. The SQLite half of AC-3 is
// covered by the component suite (pkg/agent/session_store_context_class_test.go).
//
// Postgres carries the extra risk the SQLite backend does not: context_class must be added
// to the SELECT column list of ALL SIX scanMessages feeders (LoadSession, LoadMessages,
// LoadMessagesForAgent, LoadMessagesFromParentSession, SearchMessages,
// SearchMessagesByAgent) — a single missed SELECT breaks Scan arity for that feeder alone,
// which per-feeder round-trip tests below are designed to catch.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage/postgres"
)

// contextClassPostgresDSN returns the DSN for the live PostgreSQL instance this suite
// verifies against. TEST_POSTGRES_URL overrides it (matching the pkg/storage/postgres
// integration-test convention); the default is the pack's docker-compose instance, the same
// one looms-postgres.yaml points the running looms server at.
func contextClassPostgresDSN() string {
	if dsn := os.Getenv("TEST_POSTGRES_URL"); dsn != "" {
		return dsn
	}
	return "postgres://loom_test:loom_test_pass@localhost:5433/loom_test?sslmode=disable"
}

// contextClassPostgresPool connects to the real PostgreSQL instance and runs all migrations,
// so the test is self-contained regardless of whether the shared looms server has already
// auto-migrated. The pool is closed via t.Cleanup.
func contextClassPostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx := context.Background()
	dsn := contextClassPostgresDSN()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "failed to connect to PostgreSQL at %s", dsn)
	t.Cleanup(func() { pool.Close() })

	migrator, err := postgres.NewMigrator(pool, observability.NewNoOpTracer())
	require.NoError(t, err, "failed to create migrator")
	require.NoError(t, migrator.MigrateUp(ctx), "failed to run migrations")

	return pool
}

// newContextClassPostgresStore returns a real postgres.SessionStore backed by a live,
// migrated PostgreSQL instance.
func newContextClassPostgresStore(t *testing.T) *postgres.SessionStore {
	t.Helper()
	pool := contextClassPostgresPool(t)
	return postgres.NewSessionStore(pool, observability.NewNoOpTracer(), nil)
}

// contextClassSaveTestSession creates and saves a session for userID (optionally scoped to
// agentID and/or linked to parentSessionID), registers best-effort cleanup, and returns the
// RLS-scoped context subsequent calls for this session must use.
func contextClassSaveTestSession(t *testing.T, store *postgres.SessionStore, userID, sessionID, agentID, parentSessionID string) context.Context {
	t.Helper()

	ctx := postgres.ContextWithUserID(context.Background(), userID)
	now := time.Now().UTC()
	sess := &agent.Session{
		ID:              sessionID,
		AgentID:         agentID,
		ParentSessionID: parentSessionID,
		UserID:          userID,
		Context:         make(map[string]interface{}),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	require.NoError(t, store.SaveSession(ctx, sess), "failed to create session %s", sessionID)

	t.Cleanup(func() {
		_ = store.DeleteSession(postgres.ContextWithUserID(context.Background(), userID), sessionID)
	})

	return ctx
}

// TestE2E_ContextClassPostgres_RoundTrip_AllClasses saves one message per ContextClass value
// (including narrative's empty string) against a live PostgreSQL backend and asserts
// LoadMessages returns each unchanged, in order.
func TestE2E_ContextClassPostgres_RoundTrip_AllClasses(t *testing.T) {
	store := newContextClassPostgresStore(t)
	userID := uniqueTestID("cc-user-roundtrip")
	sessionID := uniqueTestID("cc-session-roundtrip")
	ctx := contextClassSaveTestSession(t, store, userID, sessionID, "", "")

	base := time.Now().UTC()
	fixtures := []struct {
		name  string
		class agent.ContextClass
	}{
		{"narrative", agent.ClassNarrative},
		{"charter", agent.ClassCharter},
		{"ledger", agent.ClassLedger},
		{"ballast", agent.ClassBallast},
	}

	for i, f := range fixtures {
		msg := agent.Message{
			Role:         "user",
			Content:      "msg-" + f.name,
			ContextClass: string(f.class),
			Timestamp:    base.Add(time.Duration(i) * time.Second),
		}
		require.NoError(t, store.SaveMessage(ctx, sessionID, msg), "SaveMessage(%s) should succeed", f.name)
	}

	loaded, err := store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, loaded, len(fixtures))

	for i, f := range fixtures {
		assert.Equal(t, "msg-"+f.name, loaded[i].Content, "message order must be preserved")
		assert.Equal(t, string(f.class), loaded[i].ContextClass,
			"ContextClass must round-trip unchanged for %s via the real Postgres backend", f.name)
	}
}

// TestE2E_ContextClassPostgres_LoadSession_PreservesContextClass verifies the primary
// restore path (LoadSession, which has its own message SELECT distinct from LoadMessages)
// carries context_class through on PostgreSQL, including preserving a legacy NULL as "".
func TestE2E_ContextClassPostgres_LoadSession_PreservesContextClass(t *testing.T) {
	store := newContextClassPostgresStore(t)
	userID := uniqueTestID("cc-user-loadsession")
	sessionID := uniqueTestID("cc-session-loadsession")
	ctx := contextClassSaveTestSession(t, store, userID, sessionID, "", "")

	require.NoError(t, store.SaveMessage(ctx, sessionID, agent.Message{
		Role:         "assistant",
		Content:      "charter message",
		ContextClass: string(agent.ClassCharter),
		Timestamp:    time.Now().UTC(),
	}))
	require.NoError(t, store.SaveMessage(ctx, sessionID, agent.Message{
		Role:      "user",
		Content:   "legacy narrative message",
		Timestamp: time.Now().UTC().Add(time.Second),
		// ContextClass intentionally left empty, simulating a pre-D-3 row.
	}))

	loaded, err := store.LoadSession(ctx, sessionID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Len(t, loaded.Messages, 2)

	assert.Equal(t, string(agent.ClassCharter), loaded.Messages[0].ContextClass,
		"LoadSession's own message SELECT must carry a persisted non-empty class through")
	assert.Equal(t, "", loaded.Messages[1].ContextClass,
		"LoadSession must preserve a NULL/empty context_class as the empty string, not error or default it")
}

// TestE2E_ContextClassPostgres_EmptyIsNullNotError verifies that saving a message with an
// empty ContextClass (the common case pre-D-3 and for narrative messages) never trips the
// nullable-column plumbing on PostgreSQL — it must persist as NULL and load back as "".
func TestE2E_ContextClassPostgres_EmptyIsNullNotError(t *testing.T) {
	store := newContextClassPostgresStore(t)
	userID := uniqueTestID("cc-user-null")
	sessionID := uniqueTestID("cc-session-null")
	ctx := contextClassSaveTestSession(t, store, userID, sessionID, "", "")

	require.NoError(t, store.SaveMessage(ctx, sessionID, agent.Message{
		Role:      "assistant",
		Content:   "no class set",
		Timestamp: time.Now().UTC(),
	}))

	loaded, err := store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, "", loaded[0].ContextClass)
}

// TestE2E_ContextClassPostgres_AllSixReadersPreserveClass is the Postgres-specific half of
// AC-3: it exercises every one of the six real scanMessages feeders against one shared
// fixture (a parent session with one message per ContextClass value, plus a child session
// that only links to the parent) and asserts each feeder returns the persisted class
// unchanged. A feeder whose SELECT column list omits context_class either errors on Scan
// arity or silently returns the wrong value — both would fail here.
func TestE2E_ContextClassPostgres_AllSixReadersPreserveClass(t *testing.T) {
	store := newContextClassPostgresStore(t)
	userID := uniqueTestID("cc-user-sixreaders")
	agentID := uniqueTestID("cc-agent-sixreaders")
	parentSessionID := uniqueTestID("cc-session-parent")
	childSessionID := uniqueTestID("cc-session-child")

	parentCtx := contextClassSaveTestSession(t, store, userID, parentSessionID, agentID, "")
	// Child session carries no messages of its own; it exists only to give
	// LoadMessagesFromParentSession a real parent link to walk.
	contextClassSaveTestSession(t, store, userID, childSessionID, "", parentSessionID)

	base := time.Now().UTC()
	fixtures := []struct {
		term  string
		class agent.ContextClass
	}{
		{"quokka", agent.ClassNarrative},
		{"zephyr", agent.ClassCharter},
		{"glimmer", agent.ClassLedger},
		{"wobble", agent.ClassBallast},
	}
	classByTerm := make(map[string]string, len(fixtures))
	for i, f := range fixtures {
		require.NoError(t, store.SaveMessage(parentCtx, parentSessionID, agent.Message{
			Role:         "user",
			Content:      "search marker " + f.term,
			ContextClass: string(f.class),
			Timestamp:    base.Add(time.Duration(i) * time.Second),
		}))
		classByTerm[f.term] = string(f.class)
	}

	// assertClasses matches each returned message back to its fixture by the unique term in
	// its content, then asserts the persisted ContextClass round-tripped through this feeder.
	assertClasses := func(t *testing.T, label string, msgs []agent.Message) {
		t.Helper()
		require.Len(t, msgs, len(fixtures), "%s should return all %d fixture messages", label, len(fixtures))
		seen := make(map[string]bool, len(fixtures))
		for _, m := range msgs {
			var matched string
			for term := range classByTerm {
				if strings.Contains(m.Content, term) {
					matched = term
					break
				}
			}
			require.NotEmpty(t, matched, "%s: message %q did not match any fixture term", label, m.Content)
			seen[matched] = true
			assert.Equal(t, classByTerm[matched], m.ContextClass,
				"%s: ContextClass for the %q fixture must round-trip unchanged", label, matched)
		}
		assert.Len(t, seen, len(fixtures), "%s should cover every fixture message exactly once", label)
	}

	t.Run("LoadMessages", func(t *testing.T) {
		msgs, err := store.LoadMessages(parentCtx, parentSessionID)
		require.NoError(t, err)
		assertClasses(t, "LoadMessages", msgs)
	})

	t.Run("LoadSession", func(t *testing.T) {
		sess, err := store.LoadSession(parentCtx, parentSessionID)
		require.NoError(t, err)
		require.NotNil(t, sess)
		assertClasses(t, "LoadSession", sess.Messages)
	})

	t.Run("LoadMessagesForAgent", func(t *testing.T) {
		msgs, err := store.LoadMessagesForAgent(parentCtx, agentID)
		require.NoError(t, err)
		assertClasses(t, "LoadMessagesForAgent", msgs)
	})

	t.Run("LoadMessagesFromParentSession", func(t *testing.T) {
		childCtx := postgres.ContextWithUserID(context.Background(), userID)
		msgs, err := store.LoadMessagesFromParentSession(childCtx, childSessionID)
		require.NoError(t, err)
		assertClasses(t, "LoadMessagesFromParentSession", msgs)
	})

	t.Run("SearchMessages", func(t *testing.T) {
		msgs, err := store.SearchMessages(parentCtx, parentSessionID, "zephyr", 10)
		require.NoError(t, err)
		require.Len(t, msgs, 1, "SearchMessages should match exactly the zephyr fixture")
		assert.Equal(t, string(agent.ClassCharter), msgs[0].ContextClass,
			"SearchMessages must carry ContextClass through its own scanMessages call")
	})

	t.Run("SearchMessagesByAgent", func(t *testing.T) {
		msgs, err := store.SearchMessagesByAgent(parentCtx, agentID, "wobble", 10)
		require.NoError(t, err)
		require.Len(t, msgs, 1, "SearchMessagesByAgent should match exactly the wobble fixture")
		assert.Equal(t, string(agent.ClassBallast), msgs[0].ContextClass,
			"SearchMessagesByAgent must carry ContextClass through its own scanMessages call")
	})
}

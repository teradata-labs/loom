// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package workspace

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/teradata-labs/loom/pkg/observability"
	pg "github.com/teradata-labs/loom/pkg/storage/postgres"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/agent"
)

// The store is the one piece with two implementations, so its behaviour is
// stated once here and run against each. SQLite is what the CLI uses; Postgres
// is what the cloud uses, and a divergence between them would show up as a
// workspace that behaves differently in the two places.
//
// newSession creates a session row owned by the given user, which the binding
// and adoption rules need.
func storeConformance(t *testing.T, store Store, newSession func(t *testing.T, id, owner string)) {
	ctx := context.Background()

	t.Run("names are unique per owner, not globally", func(t *testing.T) {
		require.NoError(t, store.Create(ctx, &Workspace{ID: "c1", OwnerUserID: "ann", Name: "tera", Kind: KindSoftwareDelivery, Status: StatusActive}))
		err := store.Create(ctx, &Workspace{ID: "c2", OwnerUserID: "ann", Name: "tera", Kind: KindScratch, Status: StatusActive})
		assert.ErrorIs(t, err, ErrExists)
		assert.NoError(t, store.Create(ctx, &Workspace{ID: "c3", OwnerUserID: "bob", Name: "tera", Kind: KindScratch, Status: StatusActive}))
	})

	t.Run("one personal workspace per owner", func(t *testing.T) {
		require.NoError(t, store.Create(ctx, &Workspace{ID: "p1", OwnerUserID: "ann", Name: "default", Kind: KindScratch, IsPersonal: true, Status: StatusActive}))
		err := store.Create(ctx, &Workspace{ID: "p2", OwnerUserID: "ann", Name: "other", Kind: KindScratch, IsPersonal: true, Status: StatusActive})
		assert.Error(t, err, "a second personal workspace is refused")

		got, err := store.Personal(ctx, "ann")
		require.NoError(t, err)
		assert.Equal(t, "p1", got.ID)
		_, err = store.Personal(ctx, "nobody")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("get by id and by name, and missing is not found", func(t *testing.T) {
		got, err := store.Get(ctx, "c1")
		require.NoError(t, err)
		assert.Equal(t, "tera", got.Name)
		assert.Equal(t, KindSoftwareDelivery, got.Kind)

		got, err = store.GetByName(ctx, "ann", "tera")
		require.NoError(t, err)
		assert.Equal(t, "c1", got.ID)

		_, err = store.Get(ctx, "nope")
		assert.ErrorIs(t, err, ErrNotFound)
		_, err = store.GetByName(ctx, "ann", "nope")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("list is per owner", func(t *testing.T) {
		list, err := store.List(ctx, "ann")
		require.NoError(t, err)
		ids := map[string]bool{}
		for _, ws := range list {
			ids[ws.ID] = true
			assert.Equal(t, "ann", ws.OwnerUserID)
		}
		assert.True(t, ids["c1"] && ids["p1"], "ann's workspaces: %v", ids)
		assert.False(t, ids["c3"], "bob's workspace is not in ann's list")
	})

	t.Run("config round-trips as json", func(t *testing.T) {
		require.NoError(t, store.UpdateConfig(ctx, "c1", map[string]any{
			"budget": map[string]any{"max_cost_usd": 12.5},
			"envs":   []any{"dit", "sit"},
		}))
		got, err := store.Get(ctx, "c1")
		require.NoError(t, err)
		budget, ok := got.Config["budget"].(map[string]any)
		require.True(t, ok, "config: %#v", got.Config)
		assert.EqualValues(t, 12.5, budget["max_cost_usd"])
		assert.Len(t, got.Config["envs"], 2)
	})

	t.Run("status changes and is readable", func(t *testing.T) {
		require.NoError(t, store.SetStatus(ctx, "c3", StatusArchived))
		got, err := store.Get(ctx, "c3")
		require.NoError(t, err)
		assert.Equal(t, StatusArchived, got.Status)
		require.NoError(t, store.SetStatus(ctx, "c3", StatusActive))
	})

	t.Run("repos are keyed by name within a workspace", func(t *testing.T) {
		require.NoError(t, store.AddRepo(ctx, &Repo{WorkspaceID: "c1", Name: "loom", URL: "git@x:o/loom.git", Ref: "main", WorktreePath: "/w/c1/repos/loom"}))
		require.NoError(t, store.AddRepo(ctx, &Repo{WorkspaceID: "c1", Name: "up", URL: "git@x:o/up.git", Ref: "dev", WorktreePath: "/w/c1/repos/up"}))
		// The same repo name in another workspace is a different row.
		require.NoError(t, store.AddRepo(ctx, &Repo{WorkspaceID: "c3", Name: "loom", URL: "git@x:o/loom.git", Ref: "main", WorktreePath: "/w/c3/repos/loom"}))

		repos, err := store.Repos(ctx, "c1")
		require.NoError(t, err)
		assert.Len(t, repos, 2)

		require.NoError(t, store.RemoveRepo(ctx, "c1", "up"))
		repos, err = store.Repos(ctx, "c1")
		require.NoError(t, err)
		require.Len(t, repos, 1)
		assert.Equal(t, "loom", repos[0].Name)
		assert.Equal(t, "main", repos[0].Ref)

		other, err := store.Repos(ctx, "c3")
		require.NoError(t, err)
		assert.Len(t, other, 1, "removing c1's repo left c3's alone")
	})

	t.Run("a session binds once, for life", func(t *testing.T) {
		newSession(t, "s-bind", "ann")
		require.NoError(t, store.BindSession(ctx, "s-bind", "c1"))
		assert.ErrorIs(t, store.BindSession(ctx, "s-bind", "p1"), ErrAlreadyBound)
		assert.ErrorIs(t, store.BindSession(ctx, "s-bind", "c1"), ErrAlreadyBound, "even to the same one")
		assert.ErrorIs(t, store.BindSession(ctx, "s-missing", "c1"), ErrSessionNotFound)

		got, err := store.SessionWorkspace(ctx, "s-bind")
		require.NoError(t, err)
		assert.Equal(t, "c1", got)

		ids, err := store.Sessions(ctx, "c1")
		require.NoError(t, err)
		assert.Equal(t, []string{"s-bind"}, ids)
	})

	t.Run("adoption takes only this owner's unbound sessions", func(t *testing.T) {
		newSession(t, "s-old-1", "ann")
		newSession(t, "s-old-2", "ann")
		newSession(t, "s-other", "bob")

		n, err := store.AdoptUnboundSessions(ctx, "ann", "p1")
		require.NoError(t, err)
		assert.EqualValues(t, 2, n)

		for _, id := range []string{"s-old-1", "s-old-2"} {
			got, err := store.SessionWorkspace(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, "p1", got)
		}
		got, _ := store.SessionWorkspace(ctx, "s-other")
		assert.Empty(t, got, "another owner's session stays unbound")

		again, err := store.AdoptUnboundSessions(ctx, "ann", "p1")
		require.NoError(t, err)
		assert.EqualValues(t, 0, again, "adoption is idempotent")
	})
}

// SQLite is the CLI's store.
func TestStoreConformance_SQLite(t *testing.T) {
	sess, store := openStores(t)
	storeConformance(t, store, func(t *testing.T, id, owner string) {
		t.Helper()
		require.NoError(t, sess.SaveSession(context.Background(), &agent.Session{ID: id, UserID: owner}))
	})
}

// Postgres is the cloud's store. Set TEST_POSTGRES_URL to run it; the schema is
// created by the real migration chain, not by hand.
func TestStoreConformance_Postgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_URL not set; skipping the PostgreSQL store conformance test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	m, err := pg.NewMigrator(pool, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, m.MigrateUp(ctx))

	// A clean slate: this suite asserts on counts.
	_, err = pool.Exec(ctx, `DELETE FROM workspace_repos; DELETE FROM sessions; DELETE FROM workspaces`)
	require.NoError(t, err)

	storeConformance(t, NewPostgresStore(pool), func(t *testing.T, id, owner string) {
		t.Helper()
		_, err := pool.Exec(ctx, `INSERT INTO sessions (id, user_id) VALUES ($1, $2)`, id, owner)
		require.NoError(t, err)
	})
}

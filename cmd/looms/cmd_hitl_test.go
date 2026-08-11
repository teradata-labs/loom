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
package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	loomconfig "github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// openHITLStore is the CLI's half of the one contract that makes a hold
// answerable: it must open the SAME store `looms serve` writes. These tests
// pin the branch selection and the SQLite path agreement; the postgres
// store's behavior is pinned in pkg/storage/postgres.

func TestOpenHITLStore_SQLiteBranch(t *testing.T) {
	origConfig, origPath := config, hitlCLIDBPath
	t.Cleanup(func() { config, hitlCLIDBPath = origConfig, origPath })

	config = nil // no config file: the default backend is SQLite
	hitlCLIDBPath = filepath.Join(t.TempDir(), "hitl.db")

	store, closeStore, ctx, err := openHITLStore(context.Background(), observability.NewNoOpTracer())
	require.NoError(t, err)
	defer closeStore()

	// The store is real and writable: a round-trip proves the CLI opened a
	// working SQLite store at the flag path.
	req := &shuttle.HumanRequest{
		ID: "r1", AgentID: "a", SessionID: "s", Question: "q",
		RequestType: "approval", Priority: "normal", Status: "pending",
		Kind: "approval", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute),
	}
	require.NoError(t, store.Store(ctx, req))
	got, err := store.Get(ctx, "r1")
	require.NoError(t, err)
	require.Equal(t, "approval", got.Kind)
}

// The serve process derives its SQLite path as GetLoomDataDir()/hitl.db; the
// CLI's --db default must name the SAME file, or the operator reads an empty
// database while the server's holds sit unanswered in another.
func TestHITLSQLitePath_CLIAndServeAgree(t *testing.T) {
	servePath := filepath.Join(loomconfig.GetLoomDataDir(), "hitl.db")
	cliDefault := hitlListCmd.Flags().Lookup("db")
	require.NotNil(t, cliDefault)
	require.Equal(t, servePath, cliDefault.DefValue,
		"the CLI's default --db and serve's hitl.db derivation must name one file")
}

func TestOpenHITLStore_PostgresRequiresUser(t *testing.T) {
	origConfig, origUser := config, hitlUser
	t.Cleanup(func() { config, hitlUser = origConfig, origUser })

	config = &Config{}
	config.Storage.Backend = "postgres"
	hitlUser = ""

	_, _, _, err := openHITLStore(context.Background(), observability.NewNoOpTracer())
	require.Error(t, err,
		"postgres operations are tenant-scoped: the CLI must refuse to open the store with no --user rather than silently seeing nothing")
	require.Contains(t, err.Error(), "--user")
}

// The expire command is the operator's retirement route: it closes a stranded
// pending row terminally and never overwrites a decision. Driven through the
// real command body over a real SQLite store.
func TestRunHitlExpire_RetiresStrandedRow(t *testing.T) {
	origConfig, origPath := config, hitlCLIDBPath
	t.Cleanup(func() { config, hitlCLIDBPath = origConfig, origPath })
	config = nil
	hitlCLIDBPath = filepath.Join(t.TempDir(), "hitl.db")

	// Seed a stranded row: pending, expired, waiter long gone.
	store, err := shuttle.NewSQLiteHumanRequestStore(shuttle.SQLiteConfig{Path: hitlCLIDBPath})
	require.NoError(t, err)
	require.NoError(t, store.Store(context.Background(), &shuttle.HumanRequest{
		ID: "stranded-1", AgentID: "a", SessionID: "s", Question: "q",
		RequestType: "approval", Priority: "normal", Status: "pending",
		Kind: "approval", CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(-30 * time.Minute),
	}))
	require.NoError(t, store.Close())

	runHitlExpire(hitlExpireCmd, []string{"stranded-1"})

	after, err := shuttle.NewSQLiteHumanRequestStore(shuttle.SQLiteConfig{Path: hitlCLIDBPath})
	require.NoError(t, err)
	defer func() { _ = after.Close() }()
	got, err := after.Get(context.Background(), "stranded-1")
	require.NoError(t, err)
	require.Equal(t, "timeout", got.Status, "the stranded row is terminally retired")
	require.Contains(t, got.RespondedBy, "operator:", "the closing actor is the operator")
}

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

package backend

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage/postgres"
)

// A server asks for the lease store once at startup, before any user is known
// (looms serve, looms workflow run). On a migrated Postgres database it gets
// one: the Postgres store answers every other query for a user, and this one
// has none. Runs in the ordinary gate: CI sets TEST_POSTGRES_URL, a local run
// without one skips.
func TestSessionLeases_AMigratedPostgresDatabaseHasTheLease(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_URL not set; skipping PostgreSQL backend test")
	}
	ctx := context.Background()
	sb, err := postgres.NewBackend(ctx, &loomv1.PostgresStorageConfig{Dsn: dsn}, observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sb.Close() })
	require.NoError(t, sb.Migrate(ctx))

	leases, err := SessionLeases(ctx, sb)
	require.NoError(t, err, "a migrated Postgres database is reported as having no lease table")
	require.NotNil(t, leases)
}

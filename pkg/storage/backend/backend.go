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

// Package backend defines the StorageBackend composite interface and factory.
// This package sits above pkg/agent, pkg/artifacts, pkg/shuttle, and pkg/storage
// to avoid import cycles while composing their individual store interfaces.
package backend

import (
	"context"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
)

// MigrationInspector is an optional interface that StorageBackend implementations
// may satisfy to provide introspection into pending database migrations.
// This is used by the RunMigration RPC in dry-run mode to report which
// migrations would be applied without actually running them.
type MigrationInspector interface {
	// PendingMigrations returns the list of migrations that have not yet been
	// applied to the database. Each entry contains the version, description,
	// and SQL that would be executed.
	PendingMigrations(ctx context.Context) ([]*PendingMigration, error)
}

// PendingMigration describes a single migration that has not yet been applied.
type PendingMigration struct {
	// Version is the migration version number.
	Version int32
	// Description is a human-readable summary of the migration.
	Description string
	// SQL is the SQL that would be executed (may be empty for non-SQL migrations).
	SQL string
}

// AdminStorageProvider is an optional interface that StorageBackend implementations
// may satisfy to expose cross-tenant administrative queries.
// Only PostgreSQL backends implement this; SQLite backends do not need multi-tenant admin.
type AdminStorageProvider interface {
	// AdminStorage returns the admin storage implementation, or nil if unavailable.
	AdminStorage() agent.AdminStorage

	// ValidateAdminPermissions checks that the admin connection has appropriate
	// database privileges (e.g., BYPASSRLS for PostgreSQL). Logs a warning if
	// the admin role lacks expected privileges but does not fail -- the admin
	// connection may still work if RLS policies allow the role.
	ValidateAdminPermissions(ctx context.Context) error
}

// StorageDetailProvider is an optional interface that StorageBackend implementations
// may satisfy to provide detailed health information (migration version, pool stats)
// for the GetStorageStatus RPC. Backends that don't implement this return zeros/nil.
type StorageDetailProvider interface {
	// StorageDetails returns the current migration version and connection pool
	// statistics. poolStats may be nil for non-pooled backends (e.g., SQLite).
	StorageDetails(ctx context.Context) (migrationVersion int32, poolStats *loomv1.PoolStats, err error)
}

// StorageBackend is the top-level composed interface for all storage operations.
// One StorageBackend per server; all agents share the same backend.
// Implementations include SQLiteBackend and PostgresBackend.
type StorageBackend interface {
	// SessionStorage returns the session storage implementation.
	SessionStorage() agent.SessionStorage

	// ErrorStore returns the error store implementation.
	ErrorStore() agent.ErrorStore

	// ArtifactStore returns the artifact store implementation.
	ArtifactStore() artifacts.ArtifactStore

	// ResultStore returns the SQL result store implementation.
	ResultStore() storage.ResultStore

	// HumanRequestStore returns the human request store implementation.
	HumanRequestStore() shuttle.HumanRequestStore

	// Migrate runs database migrations to the latest version.
	Migrate(ctx context.Context) error

	// Ping verifies the storage backend is reachable and healthy.
	Ping(ctx context.Context) error

	// Close closes all underlying connections.
	Close() error
}

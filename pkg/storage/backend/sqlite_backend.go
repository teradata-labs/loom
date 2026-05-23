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
	"database/sql"
	"errors"
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
	"github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/task"
)

// SQLiteBackend implements StorageBackend by wrapping existing SQLite stores.
// All stores share the same loom.db file via separate connections with WAL mode.
type SQLiteBackend struct {
	sessionStore      agent.SessionStorage
	errorStore        agent.ErrorStore
	artifactStore     artifacts.ArtifactStore
	resultStore       storage.ResultStore
	humanRequestStore shuttle.HumanRequestStore
	graphMemoryStore  memory.GraphMemoryStore
	graphMemDB        *sql.DB // owned connection for graph memory; closed in Close()
	taskStore         task.TaskStore
	taskDB            *sql.DB // owned connection for task store; closed in Close()
	migrator          *sqlite.Migrator
	dbPath            string
	tracer            observability.Tracer
}

// NewSQLiteBackend creates a new SQLite-backed storage backend.
// If cfg is nil, uses default paths ($LOOM_DATA_DIR/loom.db).
func NewSQLiteBackend(cfg *loomv1.SQLiteStorageConfig, tracer observability.Tracer) (*SQLiteBackend, error) {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}

	// Determine database path
	dbPath := storage.GetDefaultLoomDBPath()
	if cfg != nil && cfg.Path != "" {
		dbPath = cfg.Path
	}

	// Build encryption config
	dbConfig := agent.DBConfig{
		Path: dbPath,
	}
	if cfg != nil && cfg.Encrypt {
		dbConfig.EncryptDatabase = true
		dbConfig.EncryptionKey = cfg.EncryptionKey
	}

	// Create session store (main store, creates schema)
	sessionStore, err := agent.NewSessionStoreWithConfig(dbConfig, tracer)
	if err != nil {
		return nil, fmt.Errorf("failed to create session store: %w", err)
	}

	// Create error store (reuses same DB path with separate connection)
	errorStore, err := agent.NewSQLiteErrorStore(dbPath, tracer)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("failed to create error store: %w", err),
			sessionStore.Close(),
		)
	}

	// Create artifact store (reuses same DB path)
	artifactStore, err := artifacts.NewSQLiteStore(dbPath, tracer)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("failed to create artifact store: %w", err),
			sessionStore.Close(),
			errorStore.Close(),
		)
	}

	// Create result store
	resultStore, err := storage.NewSQLResultStore(&storage.SQLResultStoreConfig{
		DBPath: dbPath,
	})
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("failed to create result store: %w", err),
			sessionStore.Close(),
			errorStore.Close(),
			artifactStore.Close(),
		)
	}

	// Create human request store
	humanStore, err := shuttle.NewSQLiteHumanRequestStore(shuttle.SQLiteConfig{
		Path:   dbPath,
		Tracer: tracer,
	})
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("failed to create human request store: %w", err),
			sessionStore.Close(),
			errorStore.Close(),
			artifactStore.Close(),
			resultStore.Close(),
		)
	}

	// Create migrator for versioned schema management
	migratorDB, err := sql.Open("sqlite3", dbPath+"?_fk=1&_journal_mode=WAL")
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("failed to open DB for migrator: %w", err),
			sessionStore.Close(),
			errorStore.Close(),
			artifactStore.Close(),
			resultStore.Close(),
			humanStore.Close(),
		)
	}
	migrator, err := sqlite.NewMigrator(migratorDB, tracer)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("failed to create migrator: %w", err),
			migratorDB.Close(),
			sessionStore.Close(),
			errorStore.Close(),
			artifactStore.Close(),
			resultStore.Close(),
			humanStore.Close(),
		)
	}

	// Create graph memory store (uses same DB path, separate connection).
	graphMemDB, err := sql.Open("sqlite3", dbPath+"?_fk=1&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("failed to open DB for graph memory: %w", err),
			migratorDB.Close(),
			sessionStore.Close(),
			errorStore.Close(),
			artifactStore.Close(),
			resultStore.Close(),
			humanStore.Close(),
		)
	}
	tc := agent.GetTokenCounter()
	graphMemoryStore := sqlite.NewGraphMemoryStore(graphMemDB, tc, tracer)

	// Create task store (uses same DB path, separate connection).
	taskDB, err := sql.Open("sqlite3", dbPath+"?_fk=1&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("failed to open DB for task store: %w", err),
			graphMemDB.Close(),
			migratorDB.Close(),
			sessionStore.Close(),
			errorStore.Close(),
			artifactStore.Close(),
			resultStore.Close(),
			humanStore.Close(),
		)
	}
	taskStore := sqlite.NewTaskStore(taskDB, tracer)

	return &SQLiteBackend{
		sessionStore:      sessionStore,
		errorStore:        errorStore,
		artifactStore:     artifactStore,
		resultStore:       resultStore,
		humanRequestStore: humanStore,
		graphMemoryStore:  graphMemoryStore,
		graphMemDB:        graphMemDB,
		taskStore:         taskStore,
		taskDB:            taskDB,
		migrator:          migrator,
		dbPath:            dbPath,
		tracer:            tracer,
	}, nil
}

// SessionStorage returns the session storage implementation.
func (b *SQLiteBackend) SessionStorage() agent.SessionStorage {
	return b.sessionStore
}

// ErrorStore returns the error store implementation.
func (b *SQLiteBackend) ErrorStore() agent.ErrorStore {
	return b.errorStore
}

// ArtifactStore returns the artifact store implementation.
func (b *SQLiteBackend) ArtifactStore() artifacts.ArtifactStore {
	return b.artifactStore
}

// ResultStore returns the SQL result store implementation.
func (b *SQLiteBackend) ResultStore() storage.ResultStore {
	return b.resultStore
}

// HumanRequestStore returns the human request store implementation.
func (b *SQLiteBackend) HumanRequestStore() shuttle.HumanRequestStore {
	return b.humanRequestStore
}

// Migrate applies all pending versioned schema migrations to the SQLite database.
// Individual stores still create their schemas via initSchema() during construction
// (using CREATE TABLE IF NOT EXISTS), so Migrate acts as an additional layer for
// tracking schema versions and applying future incremental migrations.
func (b *SQLiteBackend) Migrate(ctx context.Context) error {
	return b.migrator.MigrateUp(ctx)
}

// PendingMigrations implements MigrationInspector by delegating to the SQLite migrator.
func (b *SQLiteBackend) PendingMigrations(ctx context.Context) ([]*PendingMigration, error) {
	raw, err := b.migrator.PendingMigrations(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*PendingMigration, len(raw))
	for i, m := range raw {
		result[i] = &PendingMigration{
			Version:     safeInt32(m.Version),
			Description: m.Description,
			SQL:         m.UpSQL,
		}
	}
	return result, nil
}

// StorageDetails implements StorageDetailProvider by querying the migrator for
// the current schema version. Pool stats are nil for SQLite (not connection-pooled).
func (b *SQLiteBackend) StorageDetails(ctx context.Context) (int32, *loomv1.PoolStats, error) {
	version, err := b.migrator.CurrentVersion(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to get migration version: %w", err)
	}
	return safeInt32(version), nil, nil
}

// Migrator returns the underlying SQLite migrator for direct access.
func (b *SQLiteBackend) Migrator() *sqlite.Migrator {
	return b.migrator
}

// Ping verifies the SQLite database is accessible.
func (b *SQLiteBackend) Ping(ctx context.Context) error {
	db, err := sql.Open("sqlite3", b.dbPath)
	if err != nil {
		return fmt.Errorf("SQLite ping failed: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("SQLite ping failed: %w", err)
	}
	return nil
}

// Close closes all underlying store connections.
func (b *SQLiteBackend) Close() error {
	var firstErr error

	if err := b.sessionStore.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("session store close: %w", err)
	}
	if err := b.errorStore.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("error store close: %w", err)
	}
	if err := b.artifactStore.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("artifact store close: %w", err)
	}
	if err := b.resultStore.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("result store close: %w", err)
	}
	if err := b.humanRequestStore.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("human request store close: %w", err)
	}
	if b.graphMemDB != nil {
		if err := b.graphMemDB.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("graph memory db close: %w", err)
		}
	}
	if b.taskDB != nil {
		if err := b.taskDB.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("task db close: %w", err)
		}
	}

	return firstErr
}

// GraphMemoryStore implements GraphMemoryProvider.
func (b *SQLiteBackend) GraphMemoryStore() memory.GraphMemoryStore {
	return b.graphMemoryStore
}

// TaskStore implements TaskStoreProvider.
func (b *SQLiteBackend) TaskStore() task.TaskStore {
	return b.taskStore
}

// Compile-time checks
var _ StorageBackend = (*SQLiteBackend)(nil)
var _ MigrationInspector = (*SQLiteBackend)(nil)
var _ StorageDetailProvider = (*SQLiteBackend)(nil)
var _ GraphMemoryProvider = (*SQLiteBackend)(nil)
var _ TaskStoreProvider = (*SQLiteBackend)(nil)

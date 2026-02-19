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
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
)

// SQLiteBackend implements StorageBackend by wrapping existing SQLite stores.
// All stores share the same loom.db file via separate connections with WAL mode.
type SQLiteBackend struct {
	sessionStore      agent.SessionStorage
	errorStore        agent.ErrorStore
	artifactStore     artifacts.ArtifactStore
	resultStore       storage.ResultStore
	humanRequestStore shuttle.HumanRequestStore
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
		sessionStore.Close()
		return nil, fmt.Errorf("failed to create error store: %w", err)
	}

	// Create artifact store (reuses same DB path)
	artifactStore, err := artifacts.NewSQLiteStore(dbPath, tracer)
	if err != nil {
		sessionStore.Close()
		return nil, fmt.Errorf("failed to create artifact store: %w", err)
	}

	// Create result store
	resultStore, err := storage.NewSQLResultStore(&storage.SQLResultStoreConfig{
		DBPath: dbPath,
	})
	if err != nil {
		sessionStore.Close()
		artifactStore.Close()
		return nil, fmt.Errorf("failed to create result store: %w", err)
	}

	// Create human request store
	humanStore, err := shuttle.NewSQLiteHumanRequestStore(shuttle.SQLiteConfig{
		Path:   dbPath,
		Tracer: tracer,
	})
	if err != nil {
		sessionStore.Close()
		artifactStore.Close()
		resultStore.Close()
		return nil, fmt.Errorf("failed to create human request store: %w", err)
	}

	return &SQLiteBackend{
		sessionStore:      sessionStore,
		errorStore:        errorStore,
		artifactStore:     artifactStore,
		resultStore:       resultStore,
		humanRequestStore: humanStore,
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

// Migrate runs SQLite schema migrations.
// For SQLite, schema is created inline by each store's initSchema() method,
// so this is a no-op. Future versions may use versioned migrations.
func (b *SQLiteBackend) Migrate(_ context.Context) error {
	// SQLite stores handle schema creation internally via initSchema()
	return nil
}

// Ping verifies the SQLite database is accessible.
func (b *SQLiteBackend) Ping(ctx context.Context) error {
	// Use GetStats as a simple health check - it queries the sessions table
	_, err := b.sessionStore.GetStats(ctx)
	if err != nil {
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
	// ErrorStore uses a separate DB connection
	if closer, ok := b.errorStore.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("error store close: %w", err)
		}
	}
	if err := b.artifactStore.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("artifact store close: %w", err)
	}
	if err := b.resultStore.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("result store close: %w", err)
	}
	if closer, ok := b.humanRequestStore.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("human request store close: %w", err)
		}
	}

	return firstErr
}

// Compile-time check: SQLiteBackend implements StorageBackend.
var _ StorageBackend = (*SQLiteBackend)(nil)

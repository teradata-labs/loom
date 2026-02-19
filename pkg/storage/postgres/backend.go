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
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/internal/pgxdriver"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
)

// Backend implements backend.StorageBackend using PostgreSQL with pgx.
type Backend struct {
	pool              *pgxpool.Pool
	sessionStore      *SessionStore
	errorStore        *ErrorStore
	artifactStore     *ArtifactStore
	resultStore       *ResultStore
	humanRequestStore *HumanRequestStore
	migrator          *Migrator
	tracer            observability.Tracer
}

// NewBackend creates a new PostgreSQL storage backend from proto configuration.
func NewBackend(ctx context.Context, cfg *loomv1.PostgresStorageConfig, tracer observability.Tracer) (*Backend, error) {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}

	ctx, span := tracer.StartSpan(ctx, "postgres_backend.new")
	defer tracer.EndSpan(span)

	// Create connection pool
	pool, err := pgxdriver.NewPool(ctx, cfg, tracer)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to create postgres pool: %w", err)
	}

	// Create migrator
	migrator, err := NewMigrator(pool, tracer)
	if err != nil {
		pool.Close()
		span.RecordError(err)
		return nil, fmt.Errorf("failed to create migrator: %w", err)
	}

	return &Backend{
		pool:              pool,
		sessionStore:      NewSessionStore(pool, tracer),
		errorStore:        NewErrorStore(pool, tracer),
		artifactStore:     NewArtifactStore(pool, tracer),
		resultStore:       NewResultStore(pool, tracer),
		humanRequestStore: NewHumanRequestStore(pool, tracer),
		migrator:          migrator,
		tracer:            tracer,
	}, nil
}

// SessionStorage returns the PostgreSQL session storage implementation.
func (b *Backend) SessionStorage() agent.SessionStorage {
	return b.sessionStore
}

// ErrorStore returns the PostgreSQL error store implementation.
func (b *Backend) ErrorStore() agent.ErrorStore {
	return b.errorStore
}

// ArtifactStore returns the PostgreSQL artifact store implementation.
func (b *Backend) ArtifactStore() artifacts.ArtifactStore {
	return b.artifactStore
}

// ResultStore returns the PostgreSQL result store implementation.
func (b *Backend) ResultStore() storage.ResultStore {
	return b.resultStore
}

// HumanRequestStore returns the PostgreSQL human request store implementation.
func (b *Backend) HumanRequestStore() shuttle.HumanRequestStore {
	return b.humanRequestStore
}

// Migrate runs all pending PostgreSQL migrations.
func (b *Backend) Migrate(ctx context.Context) error {
	return b.migrator.MigrateUp(ctx)
}

// Ping verifies the PostgreSQL connection is healthy.
func (b *Backend) Ping(ctx context.Context) error {
	return b.pool.Ping(ctx)
}

// Close closes the PostgreSQL connection pool.
func (b *Backend) Close() error {
	b.pool.Close()
	return nil
}

// Pool returns the underlying pgxpool.Pool for advanced operations.
func (b *Backend) Pool() *pgxpool.Pool {
	return b.pool
}

// Migrator returns the migration manager for manual migration operations.
func (b *Backend) Migrator() *Migrator {
	return b.migrator
}

// NOTE: Compile-time interface check is in pkg/storage/backend/factory.go
// to avoid import cycle between backend and postgres packages.

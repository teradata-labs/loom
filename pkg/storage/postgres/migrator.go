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
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teradata-labs/loom/pkg/observability"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration represents a single database migration step.
type Migration struct {
	Version     int
	Description string
	UpSQL       string
	DownSQL     string
}

// Migrator manages PostgreSQL schema migrations using embedded SQL files.
type Migrator struct {
	pool       *pgxpool.Pool
	tracer     observability.Tracer
	migrations []Migration
}

// NewMigrator creates a new migrator with embedded SQL migrations.
func NewMigrator(pool *pgxpool.Pool, tracer observability.Tracer) (*Migrator, error) {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}

	migrations, err := loadMigrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load migrations: %w", err)
	}

	return &Migrator{
		pool:       pool,
		tracer:     tracer,
		migrations: migrations,
	}, nil
}

// migrationAdvisoryLockID is a fixed advisory lock ID used to prevent
// concurrent migration execution across multiple server instances.
const migrationAdvisoryLockID = 839021573 // arbitrary constant

// MigrateUp applies all pending migrations up to the latest version.
// Uses a PostgreSQL advisory lock to prevent concurrent migration execution.
func (m *Migrator) MigrateUp(ctx context.Context) error {
	ctx, span := m.tracer.StartSpan(ctx, "migrator.migrate_up")
	defer m.tracer.EndSpan(span)

	// Acquire advisory lock to prevent concurrent migrations
	if _, err := m.pool.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationAdvisoryLockID); err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to acquire migration lock: %w", err)
	}
	defer func() {
		// Release advisory lock (best-effort, released on disconnect anyway)
		_, _ = m.pool.Exec(ctx, "SELECT pg_advisory_unlock($1)", migrationAdvisoryLockID)
	}()

	// Ensure schema_migrations table exists
	if err := m.ensureMigrationsTable(ctx); err != nil {
		span.RecordError(err)
		return err
	}

	currentVersion, err := m.CurrentVersion(ctx)
	if err != nil {
		span.RecordError(err)
		return err
	}

	span.SetAttribute("current_version", currentVersion)

	applied := 0
	for _, migration := range m.migrations {
		if migration.Version <= currentVersion {
			continue
		}

		if err := m.applyMigration(ctx, migration); err != nil {
			span.RecordError(err)
			return fmt.Errorf("migration %d failed: %w", migration.Version, err)
		}
		applied++
	}

	span.SetAttribute("migrations_applied", applied)
	return nil
}

// MigrateDown rolls back the specified number of migrations.
func (m *Migrator) MigrateDown(ctx context.Context, steps int) error {
	ctx, span := m.tracer.StartSpan(ctx, "migrator.migrate_down")
	defer m.tracer.EndSpan(span)

	currentVersion, err := m.CurrentVersion(ctx)
	if err != nil {
		span.RecordError(err)
		return err
	}

	span.SetAttribute("current_version", currentVersion)
	span.SetAttribute("steps", steps)

	// Apply down migrations in reverse order
	rolled := 0
	for i := len(m.migrations) - 1; i >= 0 && rolled < steps; i-- {
		migration := m.migrations[i]
		if migration.Version > currentVersion {
			continue
		}

		if err := m.rollbackMigration(ctx, migration); err != nil {
			span.RecordError(err)
			return fmt.Errorf("rollback of migration %d failed: %w", migration.Version, err)
		}
		rolled++
	}

	span.SetAttribute("migrations_rolled_back", rolled)
	return nil
}

// CurrentVersion returns the highest applied migration version.
func (m *Migrator) CurrentVersion(ctx context.Context) (int, error) {
	var version int
	err := m.pool.QueryRow(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("failed to get current migration version: %w", err)
	}
	return version, nil
}

// ensureMigrationsTable creates the schema_migrations table if it doesn't exist.
func (m *Migrator) ensureMigrationsTable(ctx context.Context) error {
	_, err := m.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			description TEXT
		)
	`)
	return err
}

// applyMigration runs a single up migration within a transaction.
// It executes the migration SQL and records the version in schema_migrations.
func (m *Migrator) applyMigration(ctx context.Context, migration Migration) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, migration.UpSQL); err != nil {
		return fmt.Errorf("failed to execute migration SQL: %w", err)
	}

	// Record the migration version (idempotent via ON CONFLICT)
	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_migrations (version, description) VALUES ($1, $2) ON CONFLICT (version) DO NOTHING",
		migration.Version, migration.Description,
	); err != nil {
		return fmt.Errorf("failed to record migration version: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit migration: %w", err)
	}

	return nil
}

// rollbackMigration runs a single down migration within a transaction.
// It executes the rollback SQL and removes the version from schema_migrations.
func (m *Migrator) rollbackMigration(ctx context.Context, migration Migration) error {
	if migration.DownSQL == "" {
		return fmt.Errorf("no down migration for version %d", migration.Version)
	}

	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, migration.DownSQL); err != nil {
		return fmt.Errorf("failed to execute rollback SQL: %w", err)
	}

	// Remove the migration version record
	if _, err := tx.Exec(ctx,
		"DELETE FROM schema_migrations WHERE version = $1",
		migration.Version,
	); err != nil {
		return fmt.Errorf("failed to remove migration version: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit rollback: %w", err)
	}

	return nil
}

// PendingMigrations returns the list of migrations that have not yet been applied.
func (m *Migrator) PendingMigrations(ctx context.Context) ([]Migration, error) {
	currentVersion, err := m.CurrentVersion(ctx)
	if err != nil {
		return nil, err
	}

	var pending []Migration
	for _, migration := range m.migrations {
		if migration.Version > currentVersion {
			pending = append(pending, migration)
		}
	}
	return pending, nil
}

// loadMigrations reads all embedded SQL migration files and pairs up/down files.
func loadMigrations() ([]Migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations directory: %w", err)
	}

	// Parse migration files
	upFiles := make(map[int]string)
	downFiles := make(map[int]string)
	descriptions := make(map[int]string)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}

		// Parse filename: 000001_description.up.sql or 000001_description.down.sql
		parts := strings.SplitN(name, "_", 2)
		if len(parts) < 2 {
			continue
		}

		version, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}

		content, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, fmt.Errorf("failed to read migration file %s: %w", name, err)
		}

		// Extract description from filename
		remainder := parts[1]
		if strings.HasSuffix(remainder, ".up.sql") {
			desc := strings.TrimSuffix(remainder, ".up.sql")
			descriptions[version] = desc
			upFiles[version] = string(content)
		} else if strings.HasSuffix(remainder, ".down.sql") {
			downFiles[version] = string(content)
		}
	}

	// Build sorted migration list
	var versions []int
	for v := range upFiles {
		versions = append(versions, v)
	}
	sort.Ints(versions)

	migrations := make([]Migration, 0, len(versions))
	for _, v := range versions {
		migrations = append(migrations, Migration{
			Version:     v,
			Description: descriptions[v],
			UpSQL:       upFiles[v],
			DownSQL:     downFiles[v],
		})
	}

	return migrations, nil
}

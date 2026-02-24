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

package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	_ "github.com/teradata-labs/loom/internal/sqlitedriver" // registers "sqlite3" driver

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

// Migrator manages SQLite schema migrations using embedded SQL files.
// Unlike the PostgreSQL migrator which uses advisory locks, this uses a
// sync.Mutex to prevent concurrent migration execution within the process.
type Migrator struct {
	db         *sql.DB
	tracer     observability.Tracer
	migrations []Migration
	mu         sync.Mutex
}

// NewMigrator creates a new migrator with embedded SQL migrations.
// It sets PRAGMA busy_timeout = 5000 on the database to handle lock contention.
func NewMigrator(db *sql.DB, tracer observability.Tracer) (*Migrator, error) {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}

	// Set busy_timeout so concurrent readers/writers wait instead of failing immediately
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		return nil, fmt.Errorf("failed to set busy_timeout: %w", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load migrations: %w", err)
	}

	return &Migrator{
		db:         db,
		tracer:     tracer,
		migrations: migrations,
	}, nil
}

// MigrateUp applies all pending migrations up to the latest version.
// Uses a sync.Mutex to prevent concurrent migration execution.
func (m *Migrator) MigrateUp(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ctx, span := m.tracer.StartSpan(ctx, "migrator.migrate_up")
	defer m.tracer.EndSpan(span)

	// Bootstrap pre-migration databases if needed
	if err := m.bootstrapIfNeeded(ctx); err != nil {
		span.RecordError(err)
		return err
	}

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
	m.mu.Lock()
	defer m.mu.Unlock()

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
// Returns 0 if the schema_migrations table does not exist yet.
func (m *Migrator) CurrentVersion(ctx context.Context) (int, error) {
	// Check if schema_migrations table exists
	var tableCount int
	if err := m.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'",
	).Scan(&tableCount); err != nil {
		return 0, fmt.Errorf("failed to check for schema_migrations table: %w", err)
	}
	if tableCount == 0 {
		return 0, nil
	}

	var version int
	err := m.db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("failed to get current migration version: %w", err)
	}
	return version, nil
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

// ensureMigrationsTable creates the schema_migrations table if it doesn't exist.
// Uses INTEGER for applied_at (unix timestamp) since SQLite lacks TIMESTAMPTZ.
func (m *Migrator) ensureMigrationsTable(ctx context.Context) error {
	_, err := m.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
			description TEXT
		)
	`)
	return err
}

// applyMigration runs a single up migration within a transaction.
// It executes the migration SQL and records the version in schema_migrations.
func (m *Migrator) applyMigration(ctx context.Context, migration Migration) error {
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, migration.UpSQL); err != nil {
		return fmt.Errorf("failed to execute migration SQL: %w", err)
	}

	// Record the migration version (idempotent via ON CONFLICT)
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations (version, description) VALUES (?, ?) ON CONFLICT (version) DO NOTHING",
		migration.Version, migration.Description,
	); err != nil {
		return fmt.Errorf("failed to record migration version: %w", err)
	}

	if err := tx.Commit(); err != nil {
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

	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, migration.DownSQL); err != nil {
		return fmt.Errorf("failed to execute rollback SQL: %w", err)
	}

	// Remove the migration version record
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?",
		migration.Version,
	); err != nil {
		return fmt.Errorf("failed to remove migration version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit rollback: %w", err)
	}

	return nil
}

// bootstrapIfNeeded handles pre-migration databases gracefully.
// If the sessions table exists but schema_migrations does not, this seeds
// version 1 into schema_migrations without re-running migration 1.
// This allows existing databases to adopt the migration system without data loss.
func (m *Migrator) bootstrapIfNeeded(ctx context.Context) error {
	// Check if sessions table exists
	var sessionsCount int
	if err := m.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='sessions'",
	).Scan(&sessionsCount); err != nil {
		return fmt.Errorf("failed to check for sessions table: %w", err)
	}

	// Check if schema_migrations table exists
	var migrationsCount int
	if err := m.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'",
	).Scan(&migrationsCount); err != nil {
		return fmt.Errorf("failed to check for schema_migrations table: %w", err)
	}

	// If sessions exists but schema_migrations does not, this is a pre-migration database
	if sessionsCount > 0 && migrationsCount == 0 {
		// Create schema_migrations table
		if err := m.ensureMigrationsTable(ctx); err != nil {
			return fmt.Errorf("failed to create schema_migrations during bootstrap: %w", err)
		}

		// Seed version 1 since the initial schema already exists
		if _, err := m.db.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, description) VALUES (1, 'initial_schema (bootstrapped)')",
		); err != nil {
			return fmt.Errorf("failed to seed bootstrap version: %w", err)
		}
	}

	return nil
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
		if desc, ok := strings.CutSuffix(remainder, ".up.sql"); ok {
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

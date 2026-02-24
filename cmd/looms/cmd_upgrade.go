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
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage/backend"
	"github.com/teradata-labs/loom/pkg/storage/sqlite"
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade database schema to latest version",
	Long: `Upgrade the Loom database schema to the latest version.

By default, creates a backup before applying migrations (SQLite only).
Supports both SQLite and PostgreSQL backends.

Examples:
  looms upgrade                 # Upgrade with backup (default)
  looms upgrade --dry-run       # Show pending migrations without applying
  looms upgrade --backup-only   # Only create a backup, don't migrate
  looms upgrade --no-backup     # Skip backup (not recommended)
  looms upgrade --yes           # Skip confirmation prompt`,
	RunE: runUpgrade,
}

var (
	upgradeDryRun     bool
	upgradeBackupOnly bool
	upgradeNoBackup   bool
	upgradeYes        bool
)

func init() {
	upgradeCmd.Flags().BoolVar(&upgradeDryRun, "dry-run", false, "Show pending migrations without applying")
	upgradeCmd.Flags().BoolVar(&upgradeBackupOnly, "backup-only", false, "Only create a backup, don't migrate")
	upgradeCmd.Flags().BoolVar(&upgradeNoBackup, "no-backup", false, "Skip backup (not recommended)")
	upgradeCmd.Flags().BoolVarP(&upgradeYes, "yes", "y", false, "Skip confirmation prompt")

	rootCmd.AddCommand(upgradeCmd)
}

func runUpgrade(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()
	tracer := observability.NewNoOpTracer()

	backendType := config.Storage.Backend
	if backendType == "" {
		backendType = "sqlite"
	}

	w := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(w, "Loom Database Upgrade\n")
	_, _ = fmt.Fprintf(w, "=====================\n")
	_, _ = fmt.Fprintf(w, "Backend: %s\n\n", backendType)

	// Handle SQLite backup-only mode before creating the full backend
	if upgradeBackupOnly {
		if backendType != "sqlite" && backendType != "" {
			return fmt.Errorf("--backup-only is only supported for SQLite backends")
		}
		return runSQLiteBackupOnly(cmd)
	}

	// Create storage backend
	storageCfg := config.BuildProtoStorageConfig()
	storageBackend, err := backend.NewStorageBackend(ctx, storageCfg, tracer)
	if err != nil {
		return fmt.Errorf("failed to create storage backend: %w", err)
	}
	defer func() { _ = storageBackend.Close() }()

	// Check pending migrations
	inspector, hasMigrations := storageBackend.(backend.MigrationInspector)
	if !hasMigrations {
		_, _ = fmt.Fprintf(w, "This backend does not support migration introspection.\n")
		_, _ = fmt.Fprintf(w, "Running migrations directly...\n")
		if !upgradeDryRun {
			if err := storageBackend.Migrate(ctx); err != nil {
				return fmt.Errorf("migration failed: %w", err)
			}
			_, _ = fmt.Fprintf(w, "\nUpgrade complete.\n")
		}
		return nil
	}

	pending, err := inspector.PendingMigrations(ctx)
	if err != nil {
		return fmt.Errorf("failed to check pending migrations: %w", err)
	}

	// Show current state
	detailProvider, hasDetails := storageBackend.(backend.StorageDetailProvider)
	if hasDetails {
		version, _, detailErr := detailProvider.StorageDetails(ctx)
		if detailErr == nil {
			_, _ = fmt.Fprintf(w, "Current schema version: %d\n", version)
		}
	}

	if len(pending) == 0 {
		_, _ = fmt.Fprintf(w, "Database is up to date. No pending migrations.\n")
		return nil
	}

	_, _ = fmt.Fprintf(w, "Pending migrations: %d\n\n", len(pending))
	for _, m := range pending {
		_, _ = fmt.Fprintf(w, "  %03d: %s\n", m.Version, m.Description)
	}
	_, _ = fmt.Fprintln(w)

	// Dry-run mode: print and exit
	if upgradeDryRun {
		_, _ = fmt.Fprintf(w, "[dry-run] No changes applied.\n")
		return nil
	}

	// Confirmation prompt
	if !upgradeYes {
		_, _ = fmt.Fprintf(w, "Apply %d migration(s)? [y/N] ", len(pending))
		var answer string
		if _, err := fmt.Fscanln(os.Stdin, &answer); err != nil || (answer != "y" && answer != "Y" && answer != "yes") {
			_, _ = fmt.Fprintf(w, "Aborted.\n")
			return nil
		}
	}

	// SQLite backup before migration
	if backendType == "sqlite" || backendType == "" {
		if !upgradeNoBackup {
			dbPath := config.resolveStoragePath()
			if dbPath != "" {
				_, _ = fmt.Fprintf(w, "Creating backup...\n")
				backupPath, backupErr := sqlite.Backup(dbPath)
				if backupErr != nil {
					return fmt.Errorf("backup failed (use --no-backup to skip): %w", backupErr)
				}
				_, _ = fmt.Fprintf(w, "Backup created: %s\n\n", backupPath)
			}
		}
	}

	// Apply migrations
	_, _ = fmt.Fprintf(w, "Applying migrations...\n")
	if err := storageBackend.Migrate(ctx); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	// Verify
	if err := storageBackend.Ping(ctx); err != nil {
		return fmt.Errorf("post-migration health check failed: %w", err)
	}

	// Show new version
	if hasDetails {
		version, _, detailErr := detailProvider.StorageDetails(ctx)
		if detailErr == nil {
			_, _ = fmt.Fprintf(w, "New schema version: %d\n", version)
		}
	}

	_, _ = fmt.Fprintf(w, "\nUpgrade complete.\n")
	return nil
}

// runSQLiteBackupOnly creates a backup without migrating.
func runSQLiteBackupOnly(cmd *cobra.Command) error {
	dbPath := config.resolveStoragePath()
	if dbPath == "" {
		return fmt.Errorf("could not determine SQLite database path from config")
	}

	w := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(w, "Creating backup of %s...\n", dbPath)
	backupPath, err := sqlite.Backup(dbPath)
	if err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}

	_, _ = fmt.Fprintf(w, "Backup created: %s\n", backupPath)
	return nil
}

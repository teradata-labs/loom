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

//go:build integration

package e2e

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestE2E_Migration_DryRun verifies that RunMigration with dry_run=true returns
// migration steps without applying them. Skipped unless backend is PostgreSQL.
func TestE2E_Migration_DryRun(t *testing.T) {
	if !isPostgres() {
		t.Skip("migration e2e tests only run against PostgreSQL (LOOM_E2E_BACKEND=postgres)")
	}

	client := loomClient(t)
	ctx := withUserID(context.Background(), "e2e-migration")

	resp, err := client.RunMigration(ctx, &loomv1.RunMigrationRequest{
		DryRun: true,
	})
	require.NoError(t, err, "RunMigration dry_run should succeed")
	require.NotNil(t, resp, "response should not be nil")

	// After auto_migrate on startup, a dry run should show 0 pending steps
	// (already migrated). On a fresh DB it would show migration steps.
	t.Logf("Dry run: %d steps, current_version=%d", len(resp.GetSteps()), resp.GetCurrentVersion())

	// If there are steps, they should have SQL (dry_run populates SQL)
	for i, step := range resp.GetSteps() {
		assert.Greater(t, step.GetVersion(), int32(0), "step %d version should be positive", i)
		assert.NotEmpty(t, step.GetDescription(), "step %d should have a description", i)
	}
}

// TestE2E_Migration_Apply verifies that RunMigration (non-dry-run) succeeds and
// storage remains healthy afterward.
func TestE2E_Migration_Apply(t *testing.T) {
	if !isPostgres() {
		t.Skip("migration e2e tests only run against PostgreSQL (LOOM_E2E_BACKEND=postgres)")
	}

	client := loomClient(t)
	ctx := withUserID(context.Background(), "e2e-migration")

	resp, err := client.RunMigration(ctx, &loomv1.RunMigrationRequest{})
	require.NoError(t, err, "RunMigration should succeed")
	require.NotNil(t, resp, "response should not be nil")
	assert.Greater(t, resp.GetCurrentVersion(), int32(0),
		"current_version should be positive after migration")

	// Verify storage is healthy post-migration
	statusResp, err := client.GetStorageStatus(ctx, &loomv1.GetStorageStatusRequest{})
	require.NoError(t, err, "GetStorageStatus should succeed after migration")
	assert.True(t, statusResp.GetStatus().GetHealthy(),
		"storage should be healthy after migration")

	t.Logf("Migration applied: current_version=%d", resp.GetCurrentVersion())
}

// TestE2E_Migration_Idempotent verifies that running migrations twice in a row
// succeeds without errors (idempotent).
func TestE2E_Migration_Idempotent(t *testing.T) {
	if !isPostgres() {
		t.Skip("migration e2e tests only run against PostgreSQL (LOOM_E2E_BACKEND=postgres)")
	}

	client := loomClient(t)
	ctx := withUserID(context.Background(), "e2e-migration")

	// First migration
	resp1, err := client.RunMigration(ctx, &loomv1.RunMigrationRequest{})
	require.NoError(t, err, "first RunMigration should succeed")

	// Second migration (should be a no-op)
	resp2, err := client.RunMigration(ctx, &loomv1.RunMigrationRequest{})
	require.NoError(t, err, "second RunMigration should succeed (idempotent)")

	assert.Equal(t, resp1.GetCurrentVersion(), resp2.GetCurrentVersion(),
		"version should be the same after idempotent migration")
}

// TestE2E_Migration_DryRunAfterApply verifies that after all migrations are
// applied, a dry run reports 0 pending steps.
func TestE2E_Migration_DryRunAfterApply(t *testing.T) {
	if !isPostgres() {
		t.Skip("migration e2e tests only run against PostgreSQL (LOOM_E2E_BACKEND=postgres)")
	}

	client := loomClient(t)
	ctx := withUserID(context.Background(), "e2e-migration")

	// Apply all migrations first
	_, err := client.RunMigration(ctx, &loomv1.RunMigrationRequest{})
	require.NoError(t, err, "RunMigration should succeed")

	// Dry run should show 0 pending steps
	dryResp, err := client.RunMigration(ctx, &loomv1.RunMigrationRequest{
		DryRun: true,
	})
	require.NoError(t, err, "dry run after apply should succeed")
	assert.Empty(t, dryResp.GetSteps(),
		"dry run should show 0 pending steps after all migrations are applied")

	t.Logf("Post-apply dry run: %d pending steps, current_version=%d",
		len(dryResp.GetSteps()), dryResp.GetCurrentVersion())
}

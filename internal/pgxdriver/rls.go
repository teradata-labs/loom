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
package pgxdriver

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SetRLSContext sets the current tenant ID for row-level security policies.
// Must be called within a transaction; uses SET LOCAL so the setting is
// automatically cleared when the transaction ends.
func SetRLSContext(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if tenantID == "" {
		return nil
	}
	// Use SET LOCAL so it's scoped to the current transaction
	_, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant_id', $1, true)", tenantID)
	if err != nil {
		return fmt.Errorf("failed to set RLS context: %w", err)
	}
	return nil
}

// WithTenant acquires a connection, begins a transaction with RLS context set,
// executes fn, and commits. If fn returns an error, the transaction is rolled back.
func WithTenant(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is no-op

	if err := SetRLSContext(ctx, tx, tenantID); err != nil {
		return err
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// userIDKey is the context key for user ID used in RLS isolation.
type userIDKey struct{}

// ContextWithUserID returns a new context with the given user ID attached.
// When a user ID is present, execInTx will SET LOCAL app.current_user_id
// within each transaction to activate row-level security policies.
func ContextWithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey{}, userID)
}

// UserIDFromContext extracts the user ID from the context, if present.
// Returns empty string if no user ID is set.
func UserIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(userIDKey{}).(string); ok {
		return v
	}
	return ""
}

// execInTx executes fn within a database transaction. If a user ID is
// present in the context, it sets the PostgreSQL session variable
// app.current_user_id via SET LOCAL (scoped to the transaction) to
// activate row-level security policies.
//
// Returns an error if no user ID is set in the context (required for
// all tenant-scoped operations).
//
// The transaction is committed if fn returns nil, rolled back otherwise.
// The context passed to fn carries the transaction for use by callers.
func execInTx(ctx context.Context, pool *pgxpool.Pool, fn func(ctx context.Context, tx pgx.Tx) error) error {
	userID := UserIDFromContext(ctx)
	if userID == "" {
		return fmt.Errorf("user ID is required in context for database operations")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Use set_config() instead of SET LOCAL because the SET command does not
	// support bind parameters ($1). set_config(name, value, is_local=true) is
	// equivalent to SET LOCAL and scopes the value to this transaction only.
	if _, err := tx.Exec(ctx, "SELECT pg_catalog.set_config('app.current_user_id', $1, true)", userID); err != nil {
		return fmt.Errorf("failed to set user ID: %w", err)
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// execInTxNoRLS executes fn within a transaction without setting RLS context.
// Use this for operations that should bypass RLS (e.g., schema migrations).
func execInTxNoRLS(ctx context.Context, pool *pgxpool.Pool, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := fn(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package supabase

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/teradata-labs/loom/pkg/fabric"
	"go.uber.org/zap"
)

// contextKey is an unexported type for context keys in this package.
type contextKey string

const jwtClaimsKey contextKey = "supabase_jwt_claims"

// WithJWT attaches JWT claims to a context for RLS-scoped query execution.
// When a query is executed with this context on an RLS-enabled backend,
// the claims are injected via set_config('request.jwt.claims', ...) before
// the query runs, allowing Supabase RLS policies to evaluate them.
func WithJWT(ctx context.Context, claims map[string]interface{}) context.Context {
	return context.WithValue(ctx, jwtClaimsKey, claims)
}

// extractJWT retrieves JWT claims from a context.
func extractJWT(ctx context.Context) (map[string]interface{}, bool) {
	claims, ok := ctx.Value(jwtClaimsKey).(map[string]interface{})
	return claims, ok
}

// executeQueryWithRLS wraps a query in a transaction with Supabase RLS
// context injection. It:
// 1. Begins a transaction
// 2. Sets request.jwt.claims via set_config (local to the transaction)
// 3. Sets the role to 'authenticated'
// 4. Executes the query
// 5. Commits (or rolls back on error)
func (b *Backend) executeQueryWithRLS(ctx context.Context, query string, start time.Time) (*fabric.QueryResult, error) {
	claims, ok := extractJWT(ctx)
	if !ok {
		return nil, fmt.Errorf("RLS query requires JWT claims in context (use WithJWT)")
	}

	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JWT claims: %w", err)
	}

	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		// Use background context for rollback so it succeeds even if
		// the original context was cancelled (e.g., client disconnect).
		// Rollback is a no-op if tx was already committed.
		_ = tx.Rollback(context.Background())
	}()

	// Inject RLS context: set JWT claims and role
	if _, err := tx.Exec(ctx, "SELECT set_config('request.jwt.claims', $1, true)", string(claimsJSON)); err != nil {
		return nil, fmt.Errorf("failed to set JWT claims: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('role', 'authenticated', true)"); err != nil {
		return nil, fmt.Errorf("failed to set role: %w", err)
	}

	// Execute the actual query
	isSelect := isSelectQuery(query)

	if isSelect {
		rows, err := tx.Query(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("RLS query failed: %w", err)
		}
		defer rows.Close()

		fieldDescs := rows.FieldDescriptions()
		cols := make([]fabric.Column, len(fieldDescs))
		colNames := make([]string, len(fieldDescs))
		for i, fd := range fieldDescs {
			colNames[i] = fd.Name
			cols[i] = fabric.Column{
				Name: fd.Name,
				Type: fmt.Sprintf("oid:%d", fd.DataTypeOID),
			}
		}

		var resultRows []map[string]interface{}
		for rows.Next() {
			if len(resultRows) >= maxResultRows {
				b.logger.Warn("RLS query result truncated at row limit",
					zap.Int("limit", maxResultRows),
				)
				break
			}
			values, err := rows.Values()
			if err != nil {
				return nil, fmt.Errorf("failed to read row values: %w", err)
			}
			row := make(map[string]interface{}, len(colNames))
			for i, col := range colNames {
				row[col] = values[i]
			}
			resultRows = append(resultRows, row)
		}

		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("row iteration error: %w", err)
		}

		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("failed to commit RLS transaction: %w", err)
		}

		return &fabric.QueryResult{
			Type:     "rows",
			Rows:     resultRows,
			Columns:  cols,
			RowCount: len(resultRows),
			Metadata: map[string]interface{}{
				"rls_enabled": true,
			},
			ExecutionStats: fabric.ExecutionStats{
				DurationMs: time.Since(start).Milliseconds(),
			},
		}, nil
	}

	// Modify query
	tag, err := tx.Exec(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("RLS query failed: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit RLS transaction: %w", err)
	}

	return &fabric.QueryResult{
		Type: "modify",
		Data: fmt.Sprintf("Query executed successfully. Rows affected: %d", tag.RowsAffected()),
		Metadata: map[string]interface{}{
			"rls_enabled": true,
		},
		ExecutionStats: fabric.ExecutionStats{
			DurationMs:   time.Since(start).Milliseconds(),
			RowsAffected: tag.RowsAffected(),
		},
	}, nil
}

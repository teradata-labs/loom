// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build integration

package supabase

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/fabric"
	"go.uber.org/zap"
)

// skipIfNoSupabase skips the test if Supabase environment variables are not set.
func skipIfNoSupabase(t *testing.T) {
	t.Helper()
	if os.Getenv("SUPABASE_PROJECT_REF") == "" {
		t.Skip("SUPABASE_PROJECT_REF not set, skipping integration test")
	}
	if os.Getenv("SUPABASE_DB_PASSWORD") == "" {
		t.Skip("SUPABASE_DB_PASSWORD not set, skipping integration test")
	}
	if os.Getenv("SUPABASE_POOLER_HOST") == "" {
		t.Skip("SUPABASE_POOLER_HOST not set, skipping integration test")
	}
}

// newTestBackend creates a Backend connected to a real Supabase project.
// It registers cleanup to close the backend when the test finishes.
func newTestBackend(t *testing.T, mode loomv1.PoolerMode) *Backend {
	t.Helper()
	skipIfNoSupabase(t)

	config := &loomv1.SupabaseConnection{
		ProjectRef:       os.Getenv("SUPABASE_PROJECT_REF"),
		DatabasePassword: os.Getenv("SUPABASE_DB_PASSWORD"),
		PoolerHost:       os.Getenv("SUPABASE_POOLER_HOST"),
		Region:           "us-east-1",
		PoolerMode:       mode,
		Database:         "postgres",
	}

	logger, _ := zap.NewDevelopment()
	ctx := context.Background()

	b, err := NewBackend(ctx, "integration-test", config, logger)
	require.NoError(t, err, "failed to create backend — check SUPABASE_* env vars")

	t.Cleanup(func() {
		_ = b.Close()
	})

	return b
}

// tableName returns a unique test table name based on the test name.
func tableName(t *testing.T) string {
	t.Helper()
	// Replace slashes and other problematic chars with underscores
	name := strings.ReplaceAll(t.Name(), "/", "_")
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ToLower(name)
	// Postgres identifiers max 63 chars; prefix with "loom_test_"
	name = "loom_test_" + name
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

func TestIntegration_SessionMode_Connect(t *testing.T) {
	b := newTestBackend(t, loomv1.PoolerMode_POOLER_MODE_SESSION)

	ctx := context.Background()
	require.NoError(t, b.Ping(ctx))

	// Verify we can query the server version
	result, err := b.ExecuteQuery(ctx, "SELECT version()")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "rows", result.Type)
	assert.GreaterOrEqual(t, result.RowCount, 1)

	// Version string should contain "PostgreSQL"
	row := result.Rows[0]
	version, ok := row["version"].(string)
	require.True(t, ok, "version column should be a string")
	assert.Contains(t, version, "PostgreSQL")
	t.Logf("Connected via session mode: %s", version)
}

func TestIntegration_TransactionMode_Connect(t *testing.T) {
	b := newTestBackend(t, loomv1.PoolerMode_POOLER_MODE_TRANSACTION)

	ctx := context.Background()
	require.NoError(t, b.Ping(ctx))

	result, err := b.ExecuteQuery(ctx, "SELECT version()")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "rows", result.Type)
	assert.GreaterOrEqual(t, result.RowCount, 1)

	row := result.Rows[0]
	version, ok := row["version"].(string)
	require.True(t, ok)
	assert.Contains(t, version, "PostgreSQL")
	t.Logf("Connected via transaction mode: %s", version)
}

func TestIntegration_CreateAndQueryTable(t *testing.T) {
	b := newTestBackend(t, loomv1.PoolerMode_POOLER_MODE_SESSION)
	ctx := context.Background()
	table := tableName(t)

	// Cleanup: drop table when test finishes
	t.Cleanup(func() {
		_, _ = b.ExecuteQuery(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
	})

	// CREATE TABLE
	createSQL := fmt.Sprintf(`CREATE TABLE %s (
		id SERIAL PRIMARY KEY,
		name TEXT NOT NULL,
		value INT
	)`, table)
	result, err := b.ExecuteQuery(ctx, createSQL)
	require.NoError(t, err)
	assert.Equal(t, "modify", result.Type)

	// INSERT rows
	insertSQL := fmt.Sprintf(`INSERT INTO %s (name, value) VALUES
		('alpha', 10),
		('beta', 20),
		('gamma', 30)`, table)
	result, err = b.ExecuteQuery(ctx, insertSQL)
	require.NoError(t, err)
	assert.Equal(t, "modify", result.Type)
	assert.Equal(t, int64(3), result.ExecutionStats.RowsAffected)

	// SELECT and verify
	selectSQL := fmt.Sprintf("SELECT name, value FROM %s ORDER BY value", table)
	result, err = b.ExecuteQuery(ctx, selectSQL)
	require.NoError(t, err)
	assert.Equal(t, "rows", result.Type)
	assert.Equal(t, 3, result.RowCount)

	// Verify row data
	assert.Equal(t, "alpha", result.Rows[0]["name"])
	assert.Equal(t, int32(10), result.Rows[0]["value"])
	assert.Equal(t, "beta", result.Rows[1]["name"])
	assert.Equal(t, "gamma", result.Rows[2]["name"])
}

func TestIntegration_GetSchema(t *testing.T) {
	b := newTestBackend(t, loomv1.PoolerMode_POOLER_MODE_SESSION)
	ctx := context.Background()
	table := tableName(t)

	t.Cleanup(func() {
		_, _ = b.ExecuteQuery(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
	})

	// Create a table with various column types
	createSQL := fmt.Sprintf(`CREATE TABLE %s (
		id SERIAL PRIMARY KEY,
		name TEXT NOT NULL,
		email VARCHAR(255),
		score NUMERIC(10,2) DEFAULT 0.0,
		active BOOLEAN DEFAULT true,
		created_at TIMESTAMPTZ DEFAULT now()
	)`, table)
	_, err := b.ExecuteQuery(ctx, createSQL)
	require.NoError(t, err)

	// Get schema
	schema, err := b.GetSchema(ctx, table)
	require.NoError(t, err)
	require.NotNil(t, schema)

	assert.Equal(t, table, schema.Name)
	assert.Equal(t, "table", schema.Type)
	assert.GreaterOrEqual(t, len(schema.Fields), 6)

	// Verify specific columns exist with correct types
	fieldMap := make(map[string]string)
	for _, f := range schema.Fields {
		fieldMap[f.Name] = f.Type
	}

	assert.Equal(t, "integer", fieldMap["id"])
	assert.Equal(t, "text", fieldMap["name"])
	assert.Contains(t, fieldMap["email"], "character varying")
	assert.Equal(t, "numeric", fieldMap["score"])
	assert.Equal(t, "boolean", fieldMap["active"])
	assert.Contains(t, fieldMap["created_at"], "timestamp")
}

func TestIntegration_ListResources(t *testing.T) {
	b := newTestBackend(t, loomv1.PoolerMode_POOLER_MODE_SESSION)
	ctx := context.Background()
	table := tableName(t)

	t.Cleanup(func() {
		_, _ = b.ExecuteQuery(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
	})

	// Create a table
	createSQL := fmt.Sprintf("CREATE TABLE %s (id INT)", table)
	_, err := b.ExecuteQuery(ctx, createSQL)
	require.NoError(t, err)

	// List resources
	resources, err := b.ListResources(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, resources)

	// Our test table should appear in the list
	var found bool
	for _, r := range resources {
		if r.Name == table {
			found = true
			assert.Equal(t, "BASE TABLE", r.Type)
			assert.Equal(t, "public", r.Metadata["schema"])
			break
		}
	}
	assert.True(t, found, "test table %s not found in ListResources", table)

	// Internal schemas should NOT appear
	for _, r := range resources {
		schema, _ := r.Metadata["schema"].(string)
		for _, internal := range InternalSchemasForTest() {
			assert.NotEqual(t, internal, schema,
				"internal schema %q should be filtered from ListResources (found table %s)", internal, r.Name)
		}
	}
}

func TestIntegration_ExecuteModify(t *testing.T) {
	b := newTestBackend(t, loomv1.PoolerMode_POOLER_MODE_SESSION)
	ctx := context.Background()
	table := tableName(t)

	t.Cleanup(func() {
		_, _ = b.ExecuteQuery(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
	})

	// Create table
	createSQL := fmt.Sprintf("CREATE TABLE %s (id INT, name TEXT)", table)
	_, err := b.ExecuteQuery(ctx, createSQL)
	require.NoError(t, err)

	// INSERT
	insertSQL := fmt.Sprintf("INSERT INTO %s (id, name) VALUES (1, 'one'), (2, 'two'), (3, 'three')", table)
	result, err := b.ExecuteQuery(ctx, insertSQL)
	require.NoError(t, err)
	assert.Equal(t, int64(3), result.ExecutionStats.RowsAffected)

	// UPDATE
	updateSQL := fmt.Sprintf("UPDATE %s SET name = 'updated' WHERE id <= 2", table)
	result, err = b.ExecuteQuery(ctx, updateSQL)
	require.NoError(t, err)
	assert.Equal(t, int64(2), result.ExecutionStats.RowsAffected)

	// DELETE
	deleteSQL := fmt.Sprintf("DELETE FROM %s WHERE id = 3", table)
	result, err = b.ExecuteQuery(ctx, deleteSQL)
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.ExecutionStats.RowsAffected)

	// Verify remaining rows
	selectSQL := fmt.Sprintf("SELECT * FROM %s ORDER BY id", table)
	result, err = b.ExecuteQuery(ctx, selectSQL)
	require.NoError(t, err)
	assert.Equal(t, 2, result.RowCount)
	assert.Equal(t, "updated", result.Rows[0]["name"])
	assert.Equal(t, "updated", result.Rows[1]["name"])
}

func TestIntegration_GetMetadata(t *testing.T) {
	b := newTestBackend(t, loomv1.PoolerMode_POOLER_MODE_SESSION)
	ctx := context.Background()
	table := tableName(t)

	t.Cleanup(func() {
		_, _ = b.ExecuteQuery(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
	})

	createSQL := fmt.Sprintf("CREATE TABLE %s (id INT, name TEXT, active BOOLEAN)", table)
	_, err := b.ExecuteQuery(ctx, createSQL)
	require.NoError(t, err)

	metadata, err := b.GetMetadata(ctx, table)
	require.NoError(t, err)
	require.NotNil(t, metadata)

	assert.Equal(t, table, metadata["name"])
	assert.Equal(t, "table", metadata["type"])
	assert.Equal(t, 3, metadata["field_count"])
	assert.Equal(t, "supabase", metadata["backend"])
	assert.Equal(t, os.Getenv("SUPABASE_PROJECT_REF"), metadata["project_ref"])
}

func TestIntegration_Capabilities(t *testing.T) {
	b := newTestBackend(t, loomv1.PoolerMode_POOLER_MODE_SESSION)

	caps := b.Capabilities()
	require.NotNil(t, caps)

	assert.True(t, caps.SupportsTransactions)
	assert.True(t, caps.SupportsConcurrency)
	assert.False(t, caps.SupportsStreaming)
	assert.True(t, caps.Features["sql"])
	assert.True(t, caps.Features["schemas"])
	assert.True(t, caps.Features["supabase"])
	assert.Contains(t, caps.SupportedOperations, "query")
	assert.Contains(t, caps.SupportedOperations, "schema")
	assert.Contains(t, caps.SupportedOperations, "list")
	assert.Contains(t, caps.SupportedOperations, "rls_query")
}

func TestIntegration_RLS_CustomOperation(t *testing.T) {
	b := newTestBackend(t, loomv1.PoolerMode_POOLER_MODE_SESSION)
	if !b.rlsEnabled {
		// RLS must be enabled for this test; enable_rls not set by default in newTestBackend.
		// Re-create with RLS enabled.
		_ = b.Close()
		skipIfNoSupabase(t)

		config := &loomv1.SupabaseConnection{
			ProjectRef:       os.Getenv("SUPABASE_PROJECT_REF"),
			DatabasePassword: os.Getenv("SUPABASE_DB_PASSWORD"),
			PoolerHost:       os.Getenv("SUPABASE_POOLER_HOST"),
			Region:           "us-east-1",
			PoolerMode:       loomv1.PoolerMode_POOLER_MODE_SESSION,
			Database:         "postgres",
			EnableRls:        true,
		}
		logger, _ := zap.NewDevelopment()
		ctx := context.Background()
		var err error
		b, err = NewBackend(ctx, "integration-test-rls", config, logger)
		require.NoError(t, err)
		t.Cleanup(func() { _ = b.Close() })
	}

	ctx := context.Background()
	table := tableName(t)

	t.Cleanup(func() {
		// Drop policy and table; ignore errors on cleanup
		_, _ = b.ExecuteQuery(ctx, fmt.Sprintf("DROP POLICY IF EXISTS user_owns ON %s", table))
		_, _ = b.ExecuteQuery(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
	})

	// Create table with user_id column
	createSQL := fmt.Sprintf(`CREATE TABLE %s (
		id SERIAL PRIMARY KEY,
		user_id TEXT NOT NULL,
		data TEXT
	)`, table)
	_, err := b.ExecuteQuery(ctx, createSQL)
	require.NoError(t, err)

	// Insert rows belonging to different users
	insertSQL := fmt.Sprintf(`INSERT INTO %s (user_id, data) VALUES
		('user-a', 'alpha-data'),
		('user-b', 'beta-data'),
		('user-a', 'alpha-data-2')`, table)
	_, err = b.ExecuteQuery(ctx, insertSQL)
	require.NoError(t, err)

	// Enable RLS on the table
	_, err = b.ExecuteQuery(ctx, fmt.Sprintf("ALTER TABLE %s ENABLE ROW LEVEL SECURITY", table))
	require.NoError(t, err)

	// Create policy: authenticated users can only see their own rows
	policySQL := fmt.Sprintf(`CREATE POLICY user_owns ON %s
		FOR SELECT TO authenticated
		USING (user_id = (current_setting('request.jwt.claims', true)::json->>'sub'))`, table)
	_, err = b.ExecuteQuery(ctx, policySQL)
	require.NoError(t, err)

	// Grant SELECT to authenticated role
	_, err = b.ExecuteQuery(ctx, fmt.Sprintf("GRANT SELECT ON %s TO authenticated", table))
	require.NoError(t, err)

	// Query as user-a via custom operation
	selectSQL := fmt.Sprintf("SELECT * FROM %s ORDER BY id", table)
	result, err := b.ExecuteCustomOperation(ctx, "rls_query", map[string]interface{}{
		"query": selectSQL,
		"claims": map[string]interface{}{
			"sub":  "user-a",
			"role": "authenticated",
		},
	})
	require.NoError(t, err)

	queryResult, ok := result.(*fabric.QueryResult)
	require.True(t, ok, "expected *fabric.QueryResult, got %T", result)
	assert.Equal(t, "rows", queryResult.Type)

	// user-a should only see their 2 rows (not user-b's row)
	assert.Equal(t, 2, queryResult.RowCount, "RLS should filter to user-a rows only")
	for _, row := range queryResult.Rows {
		assert.Equal(t, "user-a", row["user_id"], "all rows should belong to user-a")
	}
	t.Logf("RLS query returned %d rows for user-a (expected 2)", queryResult.RowCount)
}

func TestIntegration_RowLimit(t *testing.T) {
	// This test verifies the maxResultRows constant exists and is reasonable.
	// Inserting 10000+ rows in a real Supabase would be slow, so we just
	// verify the constant and do a small insert/select.
	assert.Equal(t, 10000, maxResultRows, "maxResultRows should be 10000")

	b := newTestBackend(t, loomv1.PoolerMode_POOLER_MODE_SESSION)
	ctx := context.Background()
	table := tableName(t)

	t.Cleanup(func() {
		_, _ = b.ExecuteQuery(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
	})

	// Create table and insert a small number of rows
	createSQL := fmt.Sprintf("CREATE TABLE %s (id INT)", table)
	_, err := b.ExecuteQuery(ctx, createSQL)
	require.NoError(t, err)

	insertSQL := fmt.Sprintf("INSERT INTO %s SELECT generate_series(1, 100)", table)
	_, err = b.ExecuteQuery(ctx, insertSQL)
	require.NoError(t, err)

	result, err := b.ExecuteQuery(ctx, fmt.Sprintf("SELECT * FROM %s", table))
	require.NoError(t, err)
	assert.Equal(t, 100, result.RowCount)
}

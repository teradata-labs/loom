// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package supabase

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/fabric"
)

// TestBackendImplementsInterface verifies compile-time interface compliance.
func TestBackendImplementsInterface(t *testing.T) {
	var _ fabric.ExecutionBackend = (*Backend)(nil)
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name        string
		config      *loomv1.SupabaseConnection
		expectErr   bool
		errContains string
	}{
		{
			name:        "nil config",
			config:      nil,
			expectErr:   true,
			errContains: "config is required",
		},
		{
			name: "missing project_ref",
			config: &loomv1.SupabaseConnection{
				DatabasePassword: "secret",
				Region:           "us-east-1",
			},
			expectErr:   true,
			errContains: "project_ref is required",
		},
		{
			name: "missing database_password",
			config: &loomv1.SupabaseConnection{
				ProjectRef: "abcdefghijklmnop",
				Region:     "us-east-1",
			},
			expectErr:   true,
			errContains: "database_password is required",
		},
		{
			name: "missing region",
			config: &loomv1.SupabaseConnection{
				ProjectRef:       "abcdefghijklmnop",
				DatabasePassword: "secret",
			},
			expectErr:   true,
			errContains: "region is required",
		},
		{
			name: "valid minimal config",
			config: &loomv1.SupabaseConnection{
				ProjectRef:       "abcdefghijklmnop",
				DatabasePassword: "secret",
				Region:           "us-east-1",
			},
			expectErr: false,
		},
		{
			name: "valid full config",
			config: &loomv1.SupabaseConnection{
				ProjectRef:       "abcdefghijklmnop",
				ApiKey:           "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
				DatabasePassword: "super-secret-pass!@#",
				PoolerMode:       loomv1.PoolerMode_POOLER_MODE_TRANSACTION,
				EnableRls:        true,
				Database:         "mydb",
				MaxPoolSize:      20,
				Region:           "eu-west-1",
			},
			expectErr: false,
		},
		{
			name: "invalid project_ref with special chars",
			config: &loomv1.SupabaseConnection{
				ProjectRef:       "proj/../etc/passwd",
				DatabasePassword: "secret",
				Region:           "us-east-1",
			},
			expectErr:   true,
			errContains: "project_ref contains invalid characters",
		},
		{
			name: "invalid region with special chars",
			config: &loomv1.SupabaseConnection{
				ProjectRef:       "abcdefghijklmnop",
				DatabasePassword: "secret",
				Region:           "us-east-1; DROP TABLE",
			},
			expectErr:   true,
			errContains: "region contains invalid characters",
		},
		{
			name: "invalid database name",
			config: &loomv1.SupabaseConnection{
				ProjectRef:       "abcdefghijklmnop",
				DatabasePassword: "secret",
				Region:           "us-east-1",
				Database:         "db; DROP TABLE users",
			},
			expectErr:   true,
			errContains: "database contains invalid characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(tt.config)
			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBuildConnectionString(t *testing.T) {
	tests := []struct {
		name     string
		config   *loomv1.SupabaseConnection
		expected string
	}{
		{
			name: "session mode defaults",
			config: &loomv1.SupabaseConnection{
				ProjectRef:       "abcdefghijklmnop",
				DatabasePassword: "secret",
				Region:           "us-east-1",
			},
			expected: "postgresql://postgres.abcdefghijklmnop:secret@aws-0-us-east-1.pooler.supabase.com:5432/postgres?sslmode=require",
		},
		{
			name: "explicit session mode",
			config: &loomv1.SupabaseConnection{
				ProjectRef:       "abcdefghijklmnop",
				DatabasePassword: "secret",
				PoolerMode:       loomv1.PoolerMode_POOLER_MODE_SESSION,
				Region:           "us-east-1",
			},
			expected: "postgresql://postgres.abcdefghijklmnop:secret@aws-0-us-east-1.pooler.supabase.com:5432/postgres?sslmode=require",
		},
		{
			name: "transaction mode uses port 6543",
			config: &loomv1.SupabaseConnection{
				ProjectRef:       "abcdefghijklmnop",
				DatabasePassword: "secret",
				PoolerMode:       loomv1.PoolerMode_POOLER_MODE_TRANSACTION,
				Region:           "us-east-1",
			},
			expected: "postgresql://postgres.abcdefghijklmnop:secret@aws-0-us-east-1.pooler.supabase.com:6543/postgres?sslmode=require",
		},
		{
			name: "custom database name",
			config: &loomv1.SupabaseConnection{
				ProjectRef:       "myproject",
				DatabasePassword: "pass",
				Database:         "mydb",
				Region:           "ap-southeast-1",
			},
			expected: "postgresql://postgres.myproject:pass@aws-0-ap-southeast-1.pooler.supabase.com:5432/mydb?sslmode=require",
		},
		{
			name: "special characters in password are URL-encoded",
			config: &loomv1.SupabaseConnection{
				ProjectRef:       "proj",
				DatabasePassword: "p@ss w0rd!#$",
				Region:           "us-west-2",
			},
			expected: "postgresql://postgres.proj:p%40ss+w0rd%21%23%24@aws-0-us-west-2.pooler.supabase.com:5432/postgres?sslmode=require",
		},
		{
			name: "different region",
			config: &loomv1.SupabaseConnection{
				ProjectRef:       "proj",
				DatabasePassword: "pass",
				Region:           "eu-central-1",
			},
			expected: "postgresql://postgres.proj:pass@aws-0-eu-central-1.pooler.supabase.com:5432/postgres?sslmode=require",
		},
		{
			name: "custom pooler host overrides auto-construction",
			config: &loomv1.SupabaseConnection{
				ProjectRef:       "proj",
				DatabasePassword: "pass",
				Region:           "us-east-1",
				PoolerHost:       "aws-1-us-east-1.pooler.supabase.com",
			},
			expected: "postgresql://postgres.proj:pass@aws-1-us-east-1.pooler.supabase.com:5432/postgres?sslmode=require",
		},
		{
			name: "custom pooler host with transaction mode",
			config: &loomv1.SupabaseConnection{
				ProjectRef:       "myproj",
				DatabasePassword: "secret",
				Region:           "us-east-1",
				PoolerMode:       loomv1.PoolerMode_POOLER_MODE_TRANSACTION,
				PoolerHost:       "aws-1-us-east-1.pooler.supabase.com",
			},
			expected: "postgresql://postgres.myproj:secret@aws-1-us-east-1.pooler.supabase.com:6543/postgres?sslmode=require",
		},
		{
			name: "empty pooler host falls back to auto-construction",
			config: &loomv1.SupabaseConnection{
				ProjectRef:       "proj",
				DatabasePassword: "pass",
				Region:           "ap-southeast-1",
				PoolerHost:       "",
			},
			expected: "postgresql://postgres.proj:pass@aws-0-ap-southeast-1.pooler.supabase.com:5432/postgres?sslmode=require",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildConnectionStringForTest(tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestJWTContextRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		claims map[string]interface{}
	}{
		{
			name: "basic claims",
			claims: map[string]interface{}{
				"sub":  "user-123",
				"role": "authenticated",
			},
		},
		{
			name: "claims with nested data",
			claims: map[string]interface{}{
				"sub":  "user-456",
				"role": "authenticated",
				"app_metadata": map[string]interface{}{
					"tenant_id": "org-789",
				},
			},
		},
		{
			name:   "empty claims",
			claims: map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Should not have claims initially
			_, ok := extractJWT(ctx)
			assert.False(t, ok, "should not have claims before WithJWT")

			// Attach claims
			ctx = WithJWT(ctx, tt.claims)

			// Should now have claims
			extracted, ok := extractJWT(ctx)
			require.True(t, ok, "should have claims after WithJWT")
			assert.Equal(t, tt.claims, extracted)
		})
	}
}

func TestExtractJWT_NoClaims(t *testing.T) {
	ctx := context.Background()
	claims, ok := extractJWT(ctx)
	assert.False(t, ok)
	assert.Nil(t, claims)
}

func TestCapabilities_WithRLS(t *testing.T) {
	b := &Backend{
		name:        "test",
		rlsEnabled:  true,
		maxPoolSize: 15,
		config: &loomv1.SupabaseConnection{
			MaxPoolSize: 15,
		},
	}

	caps := b.Capabilities()
	assert.True(t, caps.SupportsTransactions)
	assert.True(t, caps.SupportsConcurrency)
	assert.False(t, caps.SupportsStreaming)
	assert.Equal(t, 15, caps.MaxConcurrentOps)
	assert.True(t, caps.Features["sql"])
	assert.True(t, caps.Features["schemas"])
	assert.True(t, caps.Features["supabase"])
	assert.True(t, caps.Features["rls"])
	assert.Contains(t, caps.SupportedOperations, "query")
	assert.Contains(t, caps.SupportedOperations, "rls_query")
}

func TestCapabilities_WithoutRLS(t *testing.T) {
	b := &Backend{
		name:        "test",
		rlsEnabled:  false,
		maxPoolSize: 10,
		config: &loomv1.SupabaseConnection{
			MaxPoolSize: 10,
		},
	}

	caps := b.Capabilities()
	assert.True(t, caps.Features["supabase"])
	assert.False(t, caps.Features["rls"])
	assert.Equal(t, 10, caps.MaxConcurrentOps)
}

// TestCapabilities_DefaultPoolSize verifies that when MaxPoolSize is 0 (unset),
// the effective default (10) is reported.
func TestCapabilities_DefaultPoolSize(t *testing.T) {
	b := &Backend{
		name:        "test",
		maxPoolSize: 10, // effective default from NewBackend
		config: &loomv1.SupabaseConnection{
			MaxPoolSize: 0,
		},
	}

	caps := b.Capabilities()
	assert.Equal(t, 10, caps.MaxConcurrentOps)
}

func TestInternalSchemas(t *testing.T) {
	schemas := InternalSchemasForTest()

	// Verify key schemas are in the exclusion list
	expectedSchemas := []string{
		"auth",
		"storage",
		"realtime",
		"supabase_functions",
		"supabase_migrations",
		"extensions",
		"graphql",
		"graphql_public",
		"pgbouncer",
		"pgsodium",
		"pgsodium_masks",
		"vault",
		"_realtime",
		"_analytics",
		"net",
		"cron",
		"_supavisor",
		"dbdev",
	}

	for _, expected := range expectedSchemas {
		assert.Contains(t, schemas, expected, "internal schemas should include %s", expected)
	}

	// Verify it's a copy (mutating returned slice shouldn't affect original)
	schemas[0] = "modified"
	original := InternalSchemasForTest()
	assert.NotEqual(t, "modified", original[0], "InternalSchemas should return a copy")
}

func TestTruncateQuery(t *testing.T) {
	// Short query returned as-is
	assert.Equal(t, "short", truncateQuery("short", 100))
	// Exact length returned as-is
	assert.Equal(t, "exactly", truncateQuery("exactly", 7))
	// Longer than limit gets truncated with "..."
	assert.Equal(t, "exactl...", truncateQuery("exactly", 6))
	// Long query
	long := "SELECT * FROM very_long_table_name WHERE id = 1 AND name = 'something' AND another_column = 'value' AND more"
	result := truncateQuery(long, 50)
	assert.Len(t, result, 53) // 50 chars + "..."
	assert.True(t, strings.HasSuffix(result, "..."))
}

func TestIsSelectQuery(t *testing.T) {
	tests := []struct {
		query    string
		expected bool
	}{
		{"SELECT * FROM users", true},
		{"  SELECT * FROM users", true},
		{"select count(*) from orders", true},
		{"SHOW tables", true},
		{"EXPLAIN SELECT * FROM users", true},
		{"INSERT INTO users (name) VALUES ('test')", false},
		{"UPDATE users SET name = 'test'", false},
		{"DELETE FROM users", false},
		{"CREATE TABLE test (id int)", false},
		{"DROP TABLE test", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			assert.Equal(t, tt.expected, isSelectQuery(tt.query))
		})
	}
}

func TestBackendName(t *testing.T) {
	b := &Backend{name: "my-supabase-db"}
	assert.Equal(t, "my-supabase-db", b.Name())
}

func TestNewBackend_InvalidConfig(t *testing.T) {
	tests := []struct {
		name        string
		config      *loomv1.SupabaseConnection
		errContains string
	}{
		{
			name:        "nil config",
			config:      nil,
			errContains: "config is required",
		},
		{
			name: "missing project_ref",
			config: &loomv1.SupabaseConnection{
				DatabasePassword: "pass",
				Region:           "us-east-1",
			},
			errContains: "project_ref is required",
		},
		{
			name: "missing password",
			config: &loomv1.SupabaseConnection{
				ProjectRef: "proj",
				Region:     "us-east-1",
			},
			errContains: "database_password is required",
		},
		{
			name: "missing region",
			config: &loomv1.SupabaseConnection{
				ProjectRef:       "proj",
				DatabasePassword: "pass",
			},
			errContains: "region is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			backend, err := NewBackend(ctx, "test", tt.config, nil)
			require.Error(t, err)
			assert.Nil(t, backend)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

// TestNewBackend_ConnectionFailure verifies that NewBackend returns a
// connection error when the Supabase project doesn't exist (expected in
// unit tests without a real Supabase instance).
func TestNewBackend_ConnectionFailure(t *testing.T) {
	config := &loomv1.SupabaseConnection{
		ProjectRef:       "nonexistent-project",
		DatabasePassword: "test-password",
		Region:           "us-east-1",
		PoolerMode:       loomv1.PoolerMode_POOLER_MODE_SESSION,
	}

	ctx := context.Background()
	backend, err := NewBackend(ctx, "test", config, nil)
	// We expect either a DNS resolution failure or connection refused
	require.Error(t, err)
	assert.Nil(t, backend)
}

func TestGetMetadata_Fields(t *testing.T) {
	// Test the metadata structure without a real connection
	b := &Backend{
		name:       "test-sb",
		rlsEnabled: true,
		config: &loomv1.SupabaseConnection{
			ProjectRef: "test-proj",
		},
	}

	// We can't call GetMetadata without a pool, but we can verify
	// Capabilities and Name work correctly
	assert.Equal(t, "test-sb", b.Name())
	caps := b.Capabilities()
	assert.True(t, caps.Features["rls"])
	assert.True(t, caps.Features["supabase"])
}

func TestExecuteCustomOperation_NilParams(t *testing.T) {
	b := &Backend{
		name: "test",
		config: &loomv1.SupabaseConnection{
			MaxPoolSize: 10,
		},
	}

	ctx := context.Background()
	_, err := b.ExecuteCustomOperation(ctx, "rls_query", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "params is required")
}

func TestExecuteCustomOperation_UnsupportedOp(t *testing.T) {
	b := &Backend{
		name: "test",
		config: &loomv1.SupabaseConnection{
			MaxPoolSize: 10,
		},
	}

	ctx := context.Background()
	_, err := b.ExecuteCustomOperation(ctx, "unsupported_op", map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported custom operation")
}

func TestExecuteCustomOperation_RLSQuery_MissingParams(t *testing.T) {
	b := &Backend{
		name: "test",
		config: &loomv1.SupabaseConnection{
			MaxPoolSize: 10,
		},
		rlsEnabled: true,
	}

	ctx := context.Background()

	// Missing query param
	_, err := b.ExecuteCustomOperation(ctx, "rls_query", map[string]interface{}{
		"claims": map[string]interface{}{"sub": "user-1"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires 'query' string parameter")

	// Missing claims param
	_, err = b.ExecuteCustomOperation(ctx, "rls_query", map[string]interface{}{
		"query": "SELECT 1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires 'claims' map parameter")
}

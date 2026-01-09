// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package fabric

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadBackend_Postgres(t *testing.T) {
	tmpDir := t.TempDir()

	yamlContent := `apiVersion: loom/v1
kind: Backend
name: analytics-db
description: Analytics PostgreSQL database
type: postgres
database:
  dsn: postgres://user:pass@localhost:5432/analytics
  max_connections: 20
  max_idle_connections: 5
  connection_timeout_seconds: 30
  enable_ssl: true
  ssl_cert_path: ./certs/postgres.crt
schema_discovery:
  enabled: true
  cache_ttl_seconds: 3600
  include_tables:
    - users
    - orders
  exclude_tables:
    - temp_*
tool_generation:
  enable_all: true
health_check:
  enabled: true
  interval_seconds: 60
  timeout_seconds: 5
  query: "SELECT 1"
`

	// Create SSL cert file
	certsDir := filepath.Join(tmpDir, "certs")
	require.NoError(t, os.MkdirAll(certsDir, 0755))
	certPath := filepath.Join(certsDir, "postgres.crt")
	require.NoError(t, os.WriteFile(certPath, []byte("fake cert"), 0644))

	backendPath := filepath.Join(tmpDir, "postgres.yaml")
	require.NoError(t, os.WriteFile(backendPath, []byte(yamlContent), 0644))

	// Load backend
	backend, err := LoadBackend(backendPath)
	require.NoError(t, err)
	require.NotNil(t, backend)

	// Verify basic fields
	assert.Equal(t, "analytics-db", backend.Name)
	assert.Equal(t, "Analytics PostgreSQL database", backend.Description)
	assert.Equal(t, "postgres", backend.Type)

	// Verify database connection
	db := backend.GetDatabase()
	require.NotNil(t, db)
	assert.Equal(t, "postgres://user:pass@localhost:5432/analytics", db.Dsn)
	assert.Equal(t, int32(20), db.MaxConnections)
	assert.Equal(t, int32(5), db.MaxIdleConnections)
	assert.Equal(t, int32(30), db.ConnectionTimeoutSeconds)
	assert.True(t, db.EnableSsl)
	assert.True(t, filepath.IsAbs(db.SslCertPath))
	assert.Contains(t, db.SslCertPath, "postgres.crt")

	// Verify schema discovery
	assert.True(t, backend.SchemaDiscovery.Enabled)
	assert.Equal(t, int32(3600), backend.SchemaDiscovery.CacheTtlSeconds)
	assert.Equal(t, []string{"users", "orders"}, backend.SchemaDiscovery.IncludeTables)
	assert.Equal(t, []string{"temp_*"}, backend.SchemaDiscovery.ExcludeTables)

	// Verify tool generation
	assert.True(t, backend.ToolGeneration.EnableAll)

	// Verify health check
	assert.True(t, backend.HealthCheck.Enabled)
	assert.Equal(t, int32(60), backend.HealthCheck.IntervalSeconds)
	assert.Equal(t, int32(5), backend.HealthCheck.TimeoutSeconds)
	assert.Equal(t, "SELECT 1", backend.HealthCheck.Query)
}

func TestLoadBackend_RestAPI(t *testing.T) {
	tmpDir := t.TempDir()

	yamlContent := `apiVersion: loom/v1
kind: Backend
name: github-api
description: GitHub REST API
type: rest
rest:
  base_url: https://api.github.com
  auth:
    type: bearer
    token: ${GITHUB_TOKEN}
  headers:
    Accept: application/vnd.github.v3+json
    User-Agent: loom/1.0
  timeout_seconds: 30
  max_retries: 3
tool_generation:
  tools:
    - list_repos
    - create_issue
    - get_pr
health_check:
  enabled: true
  interval_seconds: 300
  timeout_seconds: 10
`

	// Set env var for test
	os.Setenv("GITHUB_TOKEN", "ghp_test123")
	defer os.Unsetenv("GITHUB_TOKEN")

	backendPath := filepath.Join(tmpDir, "github.yaml")
	require.NoError(t, os.WriteFile(backendPath, []byte(yamlContent), 0644))

	// Load backend
	backend, err := LoadBackend(backendPath)
	require.NoError(t, err)
	require.NotNil(t, backend)

	// Verify basic fields
	assert.Equal(t, "github-api", backend.Name)
	assert.Equal(t, "rest", backend.Type)

	// Verify REST connection
	rest := backend.GetRest()
	require.NotNil(t, rest)
	assert.Equal(t, "https://api.github.com", rest.BaseUrl)
	assert.Equal(t, int32(30), rest.TimeoutSeconds)
	assert.Equal(t, int32(3), rest.MaxRetries)

	// Verify auth (env var expanded)
	require.NotNil(t, rest.Auth)
	assert.Equal(t, "bearer", rest.Auth.Type)
	assert.Equal(t, "ghp_test123", rest.Auth.Token)

	// Verify headers
	assert.Equal(t, "application/vnd.github.v3+json", rest.Headers["Accept"])
	assert.Equal(t, "loom/1.0", rest.Headers["User-Agent"])

	// Verify tool generation
	assert.Equal(t, []string{"list_repos", "create_issue", "get_pr"}, backend.ToolGeneration.Tools)
}

func TestLoadBackend_GraphQL(t *testing.T) {
	tmpDir := t.TempDir()

	yamlContent := `apiVersion: loom/v1
kind: Backend
name: shopify-graphql
description: Shopify GraphQL API
type: graphql
graphql:
  endpoint: https://my-store.myshopify.com/admin/api/2023-10/graphql.json
  auth:
    type: apikey
    token: ${SHOPIFY_API_KEY}
    header_name: X-Shopify-Access-Token
  timeout_seconds: 60
`

	os.Setenv("SHOPIFY_API_KEY", "shpat_test456")
	defer os.Unsetenv("SHOPIFY_API_KEY")

	backendPath := filepath.Join(tmpDir, "shopify.yaml")
	require.NoError(t, os.WriteFile(backendPath, []byte(yamlContent), 0644))

	// Load backend
	backend, err := LoadBackend(backendPath)
	require.NoError(t, err)
	require.NotNil(t, backend)

	// Verify GraphQL connection
	gql := backend.GetGraphql()
	require.NotNil(t, gql)
	assert.Contains(t, gql.Endpoint, "graphql.json")
	assert.Equal(t, int32(60), gql.TimeoutSeconds)

	// Verify auth
	require.NotNil(t, gql.Auth)
	assert.Equal(t, "apikey", gql.Auth.Type)
	assert.Equal(t, "shpat_test456", gql.Auth.Token)
	assert.Equal(t, "X-Shopify-Access-Token", gql.Auth.HeaderName)
}

func TestLoadBackend_GRPC(t *testing.T) {
	tmpDir := t.TempDir()

	yamlContent := `apiVersion: loom/v1
kind: Backend
name: payment-service
description: Payment gRPC service
type: grpc
grpc:
  address: payment.example.com:50051
  use_tls: true
  cert_path: ./certs/payment.pem
  metadata:
    api-version: v1
    client-id: loom
  timeout_seconds: 30
`

	// Create cert file
	certsDir := filepath.Join(tmpDir, "certs")
	require.NoError(t, os.MkdirAll(certsDir, 0755))
	certPath := filepath.Join(certsDir, "payment.pem")
	require.NoError(t, os.WriteFile(certPath, []byte("fake cert"), 0644))

	backendPath := filepath.Join(tmpDir, "payment.yaml")
	require.NoError(t, os.WriteFile(backendPath, []byte(yamlContent), 0644))

	// Load backend
	backend, err := LoadBackend(backendPath)
	require.NoError(t, err)
	require.NotNil(t, backend)

	// Verify gRPC connection
	grpc := backend.GetGrpc()
	require.NotNil(t, grpc)
	assert.Equal(t, "payment.example.com:50051", grpc.Address)
	assert.True(t, grpc.UseTls)
	assert.True(t, filepath.IsAbs(grpc.CertPath))
	assert.Contains(t, grpc.CertPath, "payment.pem")
	assert.Equal(t, "v1", grpc.Metadata["api-version"])
	assert.Equal(t, "loom", grpc.Metadata["client-id"])
}

func TestLoadBackend_ValidationErrors(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		expectedErr string
	}{
		{
			name: "missing apiVersion",
			yaml: `kind: Backend
name: test`,
			expectedErr: "apiVersion is required",
		},
		{
			name: "wrong apiVersion",
			yaml: `apiVersion: loom/v2
kind: Backend
name: test`,
			expectedErr: "unsupported apiVersion",
		},
		{
			name: "wrong kind",
			yaml: `apiVersion: loom/v1
kind: NotBackend
name: test`,
			expectedErr: "kind must be 'Backend'",
		},
		{
			name: "missing name",
			yaml: `apiVersion: loom/v1
kind: Backend
type: postgres`,
			expectedErr: "name is required",
		},
		{
			name: "missing type",
			yaml: `apiVersion: loom/v1
kind: Backend
name: test`,
			expectedErr: "type is required",
		},
		{
			name: "invalid type",
			yaml: `apiVersion: loom/v1
kind: Backend
name: test
type: invalid`,
			expectedErr: "invalid backend type",
		},
		{
			name: "postgres without database config",
			yaml: `apiVersion: loom/v1
kind: Backend
name: test
type: postgres`,
			expectedErr: "database connection config is required",
		},
		{
			name: "database without DSN",
			yaml: `apiVersion: loom/v1
kind: Backend
name: test
type: postgres
database:
  max_connections: 10`,
			expectedErr: "database.dsn is required",
		},
		{
			name: "rest without config",
			yaml: `apiVersion: loom/v1
kind: Backend
name: test
type: rest`,
			expectedErr: "rest connection config is required",
		},
		{
			name: "rest without base_url",
			yaml: `apiVersion: loom/v1
kind: Backend
name: test
type: rest
rest:
  timeout_seconds: 30`,
			expectedErr: "rest.base_url is required",
		},
		{
			name: "invalid auth type",
			yaml: `apiVersion: loom/v1
kind: Backend
name: test
type: rest
rest:
  base_url: https://api.example.com
  auth:
    type: invalid`,
			expectedErr: "invalid auth type",
		},
		{
			name: "bearer without token",
			yaml: `apiVersion: loom/v1
kind: Backend
name: test
type: rest
rest:
  base_url: https://api.example.com
  auth:
    type: bearer`,
			expectedErr: "token is required",
		},
		{
			name: "basic without credentials",
			yaml: `apiVersion: loom/v1
kind: Backend
name: test
type: rest
rest:
  base_url: https://api.example.com
  auth:
    type: basic
    username: user`,
			expectedErr: "username and password are required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			backendPath := filepath.Join(tmpDir, "backend.yaml")
			require.NoError(t, os.WriteFile(backendPath, []byte(tt.yaml), 0644))

			_, err := LoadBackend(backendPath)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func TestLoadBackend_Defaults(t *testing.T) {
	tmpDir := t.TempDir()

	// Minimal config to test defaults
	yamlContent := `apiVersion: loom/v1
kind: Backend
name: minimal
type: postgres
database:
  dsn: postgres://localhost/test
`

	backendPath := filepath.Join(tmpDir, "minimal.yaml")
	require.NoError(t, os.WriteFile(backendPath, []byte(yamlContent), 0644))

	backend, err := LoadBackend(backendPath)
	require.NoError(t, err)

	// Verify defaults were set
	db := backend.GetDatabase()
	assert.Equal(t, int32(10), db.MaxConnections, "default max_connections should be 10")
	assert.Equal(t, int32(30), db.ConnectionTimeoutSeconds, "default timeout should be 30")
}

func TestLoadBackend_EnvVarExpansion(t *testing.T) {
	tmpDir := t.TempDir()

	yamlContent := `apiVersion: loom/v1
kind: Backend
name: ${DB_NAME}
description: ${DB_DESC}
type: postgres
database:
  dsn: ${DATABASE_URL}
  max_connections: ${MAX_CONN}
`

	// Set env vars
	os.Setenv("DB_NAME", "test-db")
	os.Setenv("DB_DESC", "Test database")
	os.Setenv("DATABASE_URL", "postgres://localhost/testdb")
	os.Setenv("MAX_CONN", "25")
	defer func() {
		os.Unsetenv("DB_NAME")
		os.Unsetenv("DB_DESC")
		os.Unsetenv("DATABASE_URL")
		os.Unsetenv("MAX_CONN")
	}()

	backendPath := filepath.Join(tmpDir, "backend.yaml")
	require.NoError(t, os.WriteFile(backendPath, []byte(yamlContent), 0644))

	backend, err := LoadBackend(backendPath)
	require.NoError(t, err)

	// Verify env vars were expanded
	assert.Equal(t, "test-db", backend.Name)
	assert.Equal(t, "Test database", backend.Description)
	assert.Equal(t, "postgres://localhost/testdb", backend.GetDatabase().Dsn)
	assert.Equal(t, int32(25), backend.GetDatabase().MaxConnections)
}

func TestLoadBackend_FileNotFound(t *testing.T) {
	_, err := LoadBackend("/nonexistent/backend.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read backend file")
}

func TestLoadBackend_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()

	// Use truly invalid YAML (unmatched brackets, invalid structure)
	invalidYAML := `apiVersion: loom/v1
kind: Backend
name: [test
type: postgres]`

	backendPath := filepath.Join(tmpDir, "invalid.yaml")
	require.NoError(t, os.WriteFile(backendPath, []byte(invalidYAML), 0644))

	_, err := LoadBackend(backendPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse backend YAML")
}

func TestLoadBackend_BasicAuth(t *testing.T) {
	tmpDir := t.TempDir()

	yamlContent := `apiVersion: loom/v1
kind: Backend
name: api-with-basic
type: rest
rest:
  base_url: https://api.example.com
  auth:
    type: basic
    username: admin
    password: secret123
`

	backendPath := filepath.Join(tmpDir, "basic-auth.yaml")
	require.NoError(t, os.WriteFile(backendPath, []byte(yamlContent), 0644))

	backend, err := LoadBackend(backendPath)
	require.NoError(t, err)

	rest := backend.GetRest()
	require.NotNil(t, rest)
	require.NotNil(t, rest.Auth)
	assert.Equal(t, "basic", rest.Auth.Type)
	assert.Equal(t, "admin", rest.Auth.Username)
	assert.Equal(t, "secret123", rest.Auth.Password)
}

func TestLoadBackend_MySQLAndSQLite(t *testing.T) {
	tests := []struct {
		name   string
		dbType string
		dsn    string
	}{
		{
			name:   "mysql",
			dbType: "mysql",
			dsn:    "user:pass@tcp(localhost:3306)/mydb",
		},
		{
			name:   "sqlite",
			dbType: "sqlite",
			dsn:    "file:test.db?cache=shared",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			yamlContent := `apiVersion: loom/v1
kind: Backend
name: ` + tt.name + `-db
type: ` + tt.dbType + `
database:
  dsn: ` + tt.dsn + `
`

			backendPath := filepath.Join(tmpDir, "backend.yaml")
			require.NoError(t, os.WriteFile(backendPath, []byte(yamlContent), 0644))

			backend, err := LoadBackend(backendPath)
			require.NoError(t, err)
			assert.Equal(t, tt.dbType, backend.Type)
			assert.Equal(t, tt.dsn, backend.GetDatabase().Dsn)
		})
	}
}

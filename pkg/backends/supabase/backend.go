// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package supabase provides an ExecutionBackend implementation for Supabase
// (Postgres-compatible) with native pgxpool, pooler-mode awareness, RLS
// context injection, and internal schema filtering.
package supabase

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/fabric"
	"go.uber.org/zap"
)

// internalSchemas lists Supabase internal schemas that should be excluded
// from resource listing to avoid exposing infrastructure details.
var internalSchemas = []string{
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

// Validation patterns for config fields to prevent injection in connection strings.
var (
	projectRefPattern = regexp.MustCompile(`^[a-z0-9-]+$`)
	regionPattern     = regexp.MustCompile(`^[a-z0-9-]+$`)
	databasePattern   = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

// maxResultRows is the safety limit for SELECT query results to prevent OOM.
const maxResultRows = 10000

// Compile-time interface check
var _ fabric.ExecutionBackend = (*Backend)(nil)

// Backend implements fabric.ExecutionBackend for Supabase projects.
type Backend struct {
	pool        *pgxpool.Pool
	name        string
	config      *loomv1.SupabaseConnection
	logger      *zap.Logger
	rlsEnabled  bool
	maxPoolSize int32
}

// NewBackend creates a new Supabase backend from configuration.
func NewBackend(ctx context.Context, name string, config *loomv1.SupabaseConnection, logger *zap.Logger) (*Backend, error) {
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("invalid supabase config: %w", err)
	}

	if logger == nil {
		logger = zap.NewNop()
	}

	connStr := buildConnectionString(config)

	poolConfig, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		// Don't wrap the original error as it may contain the connection string with credentials
		return nil, fmt.Errorf("failed to parse connection config for project %s: connection string invalid", config.ProjectRef)
	}

	// Configure pool size
	maxPoolSize := int32(10)
	if config.MaxPoolSize > 0 {
		maxPoolSize = config.MaxPoolSize
	}
	poolConfig.MaxConns = maxPoolSize

	// Health check and lifetime settings for all modes
	poolConfig.HealthCheckPeriod = 30 * time.Second
	poolConfig.MaxConnLifetime = 30 * time.Minute

	// Transaction-mode poolers require specific settings:
	// - No prepared statements (they don't survive across pooled connections)
	// - Shorter idle timeouts
	if config.PoolerMode == loomv1.PoolerMode_POOLER_MODE_TRANSACTION {
		poolConfig.MaxConnIdleTime = 30 * time.Second
		// pgx defaults to QueryExecModeSimpleProtocol for stdlib, but for
		// pgxpool we configure via ConnConfig.DefaultQueryExecMode
		poolConfig.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool for project %s: %w", config.ProjectRef, err)
	}

	// Verify connectivity
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to connect to supabase project %s in region %s: %w", config.ProjectRef, config.Region, err)
	}

	logger.Info("supabase backend connected",
		zap.String("name", name),
		zap.String("project_ref", config.ProjectRef),
		zap.String("region", config.Region),
		zap.String("pooler_mode", config.PoolerMode.String()),
		zap.Bool("rls_enabled", config.EnableRls),
	)

	return &Backend{
		pool:        pool,
		name:        name,
		config:      config,
		logger:      logger,
		rlsEnabled:  config.EnableRls,
		maxPoolSize: maxPoolSize,
	}, nil
}

// validateConfig checks that required fields are present and properly formatted.
func validateConfig(config *loomv1.SupabaseConnection) error {
	if config == nil {
		return fmt.Errorf("config is required")
	}
	if config.ProjectRef == "" {
		return fmt.Errorf("project_ref is required")
	}
	if !projectRefPattern.MatchString(config.ProjectRef) {
		return fmt.Errorf("project_ref contains invalid characters (allowed: lowercase alphanumeric and hyphens)")
	}
	if config.DatabasePassword == "" {
		return fmt.Errorf("database_password is required")
	}
	if config.Region == "" {
		return fmt.Errorf("region is required")
	}
	if !regionPattern.MatchString(config.Region) {
		return fmt.Errorf("region contains invalid characters (allowed: lowercase alphanumeric and hyphens)")
	}
	if config.Database != "" && !databasePattern.MatchString(config.Database) {
		return fmt.Errorf("database contains invalid characters (allowed: alphanumeric, underscores, and hyphens)")
	}
	return nil
}

// buildConnectionString constructs the Supabase pooler connection URL.
// Session mode uses port 5432, transaction mode uses port 6543.
func buildConnectionString(config *loomv1.SupabaseConnection) string {
	port := "5432"
	if config.PoolerMode == loomv1.PoolerMode_POOLER_MODE_TRANSACTION {
		port = "6543"
	}

	database := config.Database
	if database == "" {
		database = "postgres"
	}

	// URL-encode the password to handle special characters
	encodedPassword := url.QueryEscape(config.DatabasePassword)

	// Use explicit pooler_host if provided, otherwise auto-construct.
	// The aws-N prefix varies per Supabase project/infrastructure generation
	// (e.g., aws-0, aws-1) and cannot be predicted, so users should provide
	// the exact pooler host from their Supabase dashboard when the default
	// doesn't match.
	host := config.PoolerHost
	if host == "" {
		host = fmt.Sprintf("aws-0-%s.pooler.supabase.com", config.Region)
	}

	// Supabase pooler connection format:
	// postgresql://postgres.[project_ref]:[password]@[pooler_host]:[port]/[db]
	return fmt.Sprintf(
		"postgresql://postgres.%s:%s@%s:%s/%s?sslmode=require",
		config.ProjectRef,
		encodedPassword,
		host,
		port,
		database,
	)
}

// Name returns the backend identifier.
func (b *Backend) Name() string {
	return b.name
}

// ExecuteQuery executes a SQL query against the Supabase database.
// If RLS is enabled and JWT claims are present in the context,
// the query is wrapped in an RLS-scoped transaction.
func (b *Backend) ExecuteQuery(ctx context.Context, query string) (*fabric.QueryResult, error) {
	start := time.Now()
	query = strings.TrimSpace(query)

	// If RLS is enabled and JWT claims are in context, use RLS wrapper
	if b.rlsEnabled {
		if _, ok := extractJWT(ctx); ok {
			return b.executeQueryWithRLS(ctx, query, start)
		}
	}

	// Standard query execution
	isSelect := isSelectQuery(query)
	if isSelect {
		return b.executeSelect(ctx, query, start)
	}
	return b.executeModify(ctx, query, start)
}

func (b *Backend) executeSelect(ctx context.Context, query string, start time.Time) (*fabric.QueryResult, error) {
	rows, err := b.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	// Get column descriptions
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

	// Read rows with safety limit to prevent OOM
	var truncated bool
	var resultRows []map[string]interface{}
	for rows.Next() {
		if len(resultRows) >= maxResultRows {
			b.logger.Warn("query result truncated at row limit",
				zap.Int("limit", maxResultRows),
				zap.String("query_prefix", truncateQuery(query, 100)),
			)
			truncated = true
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

	_ = truncated // available for result metadata if needed

	return &fabric.QueryResult{
		Type:     "rows",
		Rows:     resultRows,
		Columns:  cols,
		RowCount: len(resultRows),
		ExecutionStats: fabric.ExecutionStats{
			DurationMs: time.Since(start).Milliseconds(),
		},
	}, nil
}

func (b *Backend) executeModify(ctx context.Context, query string, start time.Time) (*fabric.QueryResult, error) {
	tag, err := b.pool.Exec(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	return &fabric.QueryResult{
		Type: "modify",
		Data: fmt.Sprintf("Query executed successfully. Rows affected: %d", tag.RowsAffected()),
		ExecutionStats: fabric.ExecutionStats{
			DurationMs:   time.Since(start).Milliseconds(),
			RowsAffected: tag.RowsAffected(),
		},
	}, nil
}

// GetSchema retrieves column information for a table.
func (b *Backend) GetSchema(ctx context.Context, resource string) (*fabric.Schema, error) {
	query := `
		SELECT column_name, data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_name = $1
		  AND table_schema = 'public'
		ORDER BY ordinal_position`

	rows, err := b.pool.Query(ctx, query, resource)
	if err != nil {
		return nil, fmt.Errorf("failed to get schema: %w", err)
	}
	defer rows.Close()

	var fields []fabric.Field
	for rows.Next() {
		var name, dataType, isNullable string
		var columnDefault sql.NullString

		if err := rows.Scan(&name, &dataType, &isNullable, &columnDefault); err != nil {
			return nil, fmt.Errorf("failed to scan column: %w", err)
		}

		field := fabric.Field{
			Name:     name,
			Type:     dataType,
			Nullable: strings.EqualFold(isNullable, "YES"),
		}
		if columnDefault.Valid {
			field.Default = columnDefault.String
		}
		fields = append(fields, field)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return &fabric.Schema{
		Name:   resource,
		Type:   "table",
		Fields: fields,
	}, nil
}

// ListResources lists user-visible tables, excluding Supabase internal schemas.
func (b *Backend) ListResources(ctx context.Context, filters map[string]string) ([]fabric.Resource, error) {
	exclusionPlaceholders := make([]string, len(internalSchemas))
	args := make([]interface{}, len(internalSchemas))
	for i, schema := range internalSchemas {
		exclusionPlaceholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = schema
	}

	query := fmt.Sprintf(`
		SELECT table_schema, table_name, table_type
		FROM information_schema.tables
		WHERE table_schema NOT IN (%s)
		  AND table_schema NOT LIKE 'pg_%%'
		  AND table_schema != 'information_schema'
		ORDER BY table_schema, table_name`,
		strings.Join(exclusionPlaceholders, ", "))

	rows, err := b.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list resources: %w", err)
	}
	defer rows.Close()

	var resources []fabric.Resource
	for rows.Next() {
		var schema, name, typ string
		if err := rows.Scan(&schema, &name, &typ); err != nil {
			return nil, fmt.Errorf("failed to scan resource: %w", err)
		}

		resources = append(resources, fabric.Resource{
			Name: name,
			Type: typ,
			Metadata: map[string]interface{}{
				"schema": schema,
			},
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return resources, nil
}

// GetMetadata retrieves metadata for a resource.
func (b *Backend) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	schema, err := b.GetSchema(ctx, resource)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"name":        schema.Name,
		"type":        schema.Type,
		"field_count": len(schema.Fields),
		"fields":      schema.Fields,
		"backend":     "supabase",
		"project_ref": b.config.ProjectRef,
		"rls_enabled": b.rlsEnabled,
	}, nil
}

// Ping checks connectivity to the Supabase database.
func (b *Backend) Ping(ctx context.Context) error {
	return b.pool.Ping(ctx)
}

// Capabilities returns the backend's capabilities.
func (b *Backend) Capabilities() *fabric.Capabilities {
	features := map[string]bool{
		"sql":      true,
		"schemas":  true,
		"supabase": true,
	}
	if b.rlsEnabled {
		features["rls"] = true
	}

	return &fabric.Capabilities{
		SupportsTransactions: true,
		SupportsConcurrency:  true,
		SupportsStreaming:    false,
		MaxConcurrentOps:     int(b.maxPoolSize),
		SupportedOperations:  []string{"query", "schema", "list", "rls_query"},
		Features:             features,
	}
}

// ExecuteCustomOperation handles Supabase-specific operations.
func (b *Backend) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	if params == nil {
		return nil, fmt.Errorf("params is required for custom operation %q", op)
	}

	switch op {
	case "rls_query":
		query, ok := params["query"].(string)
		if !ok {
			return nil, fmt.Errorf("rls_query requires 'query' string parameter")
		}
		claims, ok := params["claims"].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("rls_query requires 'claims' map parameter")
		}
		rlsCtx := WithJWT(ctx, claims)
		return b.ExecuteQuery(rlsCtx, query)

	default:
		return nil, fmt.Errorf("unsupported custom operation: %s", op)
	}
}

// Close releases the connection pool.
func (b *Backend) Close() error {
	b.pool.Close()
	b.logger.Info("supabase backend closed", zap.String("name", b.name))
	return nil
}

// Pool returns the underlying pgxpool.Pool for advanced usage.
func (b *Backend) Pool() *pgxpool.Pool {
	return b.pool
}

// isSelectQuery returns true if the query is a read-only statement.
func isSelectQuery(query string) bool {
	upper := strings.ToUpper(strings.TrimSpace(query))
	return strings.HasPrefix(upper, "SELECT") ||
		strings.HasPrefix(upper, "SHOW") ||
		strings.HasPrefix(upper, "EXPLAIN")
}

// truncateQuery returns at most maxLen characters of the query for logging.
func truncateQuery(query string, maxLen int) string {
	if len(query) <= maxLen {
		return query
	}
	return query[:maxLen] + "..."
}

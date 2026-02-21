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
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// NewPool creates a pgxpool.Pool from proto configuration.
// If cfg.Dsn is set, it takes precedence over individual connection fields.
func NewPool(ctx context.Context, cfg *loomv1.PostgresStorageConfig, tracer observability.Tracer) (*pgxpool.Pool, error) {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}

	ctx, span := tracer.StartSpan(ctx, "pgxdriver.new_pool")
	defer tracer.EndSpan(span)

	dsn := buildDSN(cfg)
	if dsn == "" {
		return nil, fmt.Errorf("postgres configuration requires either dsn or host+database")
	}

	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to parse postgres DSN: %w", err)
	}

	// Apply pool settings from proto config
	applyPoolConfig(poolCfg, cfg.GetPool())

	// Set schema search path via AfterConnect hook
	schema := cfg.GetSchema()
	if schema == "" {
		schema = "public"
	}

	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, fmt.Sprintf("SET search_path TO %s", pgx.Identifier{schema}.Sanitize()))
		return err
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to create postgres connection pool: %w", err)
	}

	// Verify connectivity
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		span.RecordError(err)
		return nil, fmt.Errorf("failed to ping postgres: %w", err)
	}

	span.SetAttribute("pool.max_conns", poolCfg.MaxConns)
	span.SetAttribute("pool.min_conns", poolCfg.MinConns)
	span.SetAttribute("pool.schema", schema)

	return pool, nil
}

// buildDSN constructs a PostgreSQL connection string from proto config.
// Values are single-quoted per libpq keyword/value format to handle special
// characters (spaces, @, =, etc.) safely. See:
// https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING
func buildDSN(cfg *loomv1.PostgresStorageConfig) string {
	if cfg.GetDsn() != "" {
		return cfg.GetDsn()
	}

	host := cfg.GetHost()
	if host == "" {
		return ""
	}

	port := cfg.GetPort()
	if port == 0 {
		port = 5432
	}

	database := cfg.GetDatabase()
	if database == "" {
		return ""
	}

	sslMode := cfg.GetSslMode()
	if sslMode == "" {
		sslMode = "require"
	}

	dsn := fmt.Sprintf("host=%s port=%d dbname=%s sslmode=%s",
		dsnQuoteValue(host), port, dsnQuoteValue(database), dsnQuoteValue(sslMode))

	if user := cfg.GetUser(); user != "" {
		dsn += fmt.Sprintf(" user=%s", dsnQuoteValue(user))
	}
	if password := cfg.GetPassword(); password != "" {
		dsn += fmt.Sprintf(" password=%s", dsnQuoteValue(password))
	}

	return dsn
}

// dsnQuoteValue quotes a value for use in a libpq keyword/value connection string.
// Per the PostgreSQL documentation, values containing spaces, special characters,
// or that are empty must be enclosed in single quotes. Within quoted values,
// single quotes and backslashes must be escaped with a backslash.
// For simplicity and safety, we always quote all values.
func dsnQuoteValue(val string) string {
	// Escape backslashes and single quotes within the value.
	escaped := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(val)
	return "'" + escaped + "'"
}

// applyPoolConfig maps proto pool settings to pgxpool.Config.
func applyPoolConfig(poolCfg *pgxpool.Config, protoCfg *loomv1.PostgresPoolConfig) {
	if protoCfg == nil {
		// Apply defaults
		poolCfg.MaxConns = 25
		poolCfg.MinConns = 5
		poolCfg.MaxConnIdleTime = 5 * time.Minute
		poolCfg.MaxConnLifetime = 1 * time.Hour
		poolCfg.HealthCheckPeriod = 30 * time.Second
		return
	}

	if protoCfg.MaxConnections > 0 {
		poolCfg.MaxConns = protoCfg.MaxConnections
	} else {
		poolCfg.MaxConns = 25
	}

	if protoCfg.MinConnections > 0 {
		poolCfg.MinConns = protoCfg.MinConnections
	} else {
		poolCfg.MinConns = 5
	}

	if protoCfg.MaxIdleTimeSeconds > 0 {
		poolCfg.MaxConnIdleTime = time.Duration(protoCfg.MaxIdleTimeSeconds) * time.Second
	} else {
		poolCfg.MaxConnIdleTime = 5 * time.Minute
	}

	if protoCfg.MaxLifetimeSeconds > 0 {
		poolCfg.MaxConnLifetime = time.Duration(protoCfg.MaxLifetimeSeconds) * time.Second
	} else {
		poolCfg.MaxConnLifetime = 1 * time.Hour
	}

	if protoCfg.HealthCheckIntervalSeconds > 0 {
		poolCfg.HealthCheckPeriod = time.Duration(protoCfg.HealthCheckIntervalSeconds) * time.Second
	} else {
		poolCfg.HealthCheckPeriod = 30 * time.Second
	}
}

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
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestBuildDSN_WithFullDSN(t *testing.T) {
	cfg := &loomv1.PostgresStorageConfig{
		Dsn: "postgres://user:pass@localhost:5432/mydb?sslmode=disable",
	}
	dsn := buildDSN(cfg)
	assert.Equal(t, "postgres://user:pass@localhost:5432/mydb?sslmode=disable", dsn)
}

func TestBuildDSN_WithIndividualFields(t *testing.T) {
	cfg := &loomv1.PostgresStorageConfig{
		Host:     "db.example.com",
		Port:     5433,
		Database: "loomdb",
		User:     "loom",
		Password: "secret",
		SslMode:  "verify-full",
	}
	dsn := buildDSN(cfg)
	assert.Contains(t, dsn, "host=db.example.com")
	assert.Contains(t, dsn, "port=5433")
	assert.Contains(t, dsn, "dbname=loomdb")
	assert.Contains(t, dsn, "user=loom")
	assert.Contains(t, dsn, "password=secret")
	assert.Contains(t, dsn, "sslmode=verify-full")
}

func TestBuildDSN_DefaultPort(t *testing.T) {
	cfg := &loomv1.PostgresStorageConfig{
		Host:     "localhost",
		Database: "testdb",
	}
	dsn := buildDSN(cfg)
	assert.Contains(t, dsn, "port=5432")
}

func TestBuildDSN_DefaultSSLMode(t *testing.T) {
	cfg := &loomv1.PostgresStorageConfig{
		Host:     "localhost",
		Database: "testdb",
	}
	dsn := buildDSN(cfg)
	assert.Contains(t, dsn, "sslmode=require")
}

func TestBuildDSN_EmptyConfig(t *testing.T) {
	cfg := &loomv1.PostgresStorageConfig{}
	dsn := buildDSN(cfg)
	assert.Empty(t, dsn, "empty config should return empty DSN")
}

func TestBuildDSN_MissingDatabase(t *testing.T) {
	cfg := &loomv1.PostgresStorageConfig{
		Host: "localhost",
	}
	dsn := buildDSN(cfg)
	assert.Empty(t, dsn, "missing database should return empty DSN")
}

func TestBuildDSN_DSNTakesPrecedence(t *testing.T) {
	cfg := &loomv1.PostgresStorageConfig{
		Dsn:      "postgres://override@host/db",
		Host:     "ignored",
		Database: "ignored",
	}
	dsn := buildDSN(cfg)
	assert.Equal(t, "postgres://override@host/db", dsn, "DSN should take precedence")
}

func TestApplyPoolConfig_Defaults(t *testing.T) {
	poolCfg := &pgxpool.Config{}
	applyPoolConfig(poolCfg, nil)

	assert.Equal(t, int32(25), poolCfg.MaxConns)
	assert.Equal(t, int32(5), poolCfg.MinConns)
	assert.Equal(t, 5*time.Minute, poolCfg.MaxConnIdleTime)
	assert.Equal(t, 1*time.Hour, poolCfg.MaxConnLifetime)
	assert.Equal(t, 30*time.Second, poolCfg.HealthCheckPeriod)
}

func TestApplyPoolConfig_CustomValues(t *testing.T) {
	poolCfg := &pgxpool.Config{}
	protoCfg := &loomv1.PostgresPoolConfig{
		MaxConnections:             50,
		MinConnections:             10,
		MaxIdleTimeSeconds:         600,
		MaxLifetimeSeconds:         7200,
		HealthCheckIntervalSeconds: 60,
	}
	applyPoolConfig(poolCfg, protoCfg)

	assert.Equal(t, int32(50), poolCfg.MaxConns)
	assert.Equal(t, int32(10), poolCfg.MinConns)
	assert.Equal(t, 600*time.Second, poolCfg.MaxConnIdleTime)
	assert.Equal(t, 7200*time.Second, poolCfg.MaxConnLifetime)
	assert.Equal(t, 60*time.Second, poolCfg.HealthCheckPeriod)
}

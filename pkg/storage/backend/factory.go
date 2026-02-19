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
package backend

import (
	"context"
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage/postgres"
)

// NewStorageBackend creates a StorageBackend from proto configuration.
// If cfg is nil or backend is unspecified, defaults to SQLite with default paths.
func NewStorageBackend(cfg *loomv1.StorageConfig, tracer observability.Tracer) (StorageBackend, error) {
	if cfg == nil {
		cfg = &loomv1.StorageConfig{
			Backend: loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_SQLITE,
		}
	}

	switch cfg.Backend {
	case loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_UNSPECIFIED,
		loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_SQLITE:
		return NewSQLiteBackend(cfg.GetSqlite(), tracer)

	case loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_POSTGRES:
		pgCfg := cfg.GetPostgres()
		if pgCfg == nil {
			return nil, fmt.Errorf("postgres backend requires postgres configuration")
		}
		return postgres.NewBackend(context.Background(), pgCfg, tracer)

	default:
		return nil, fmt.Errorf("unsupported storage backend: %v", cfg.Backend)
	}
}

// Compile-time check: postgres.Backend implements StorageBackend.
var _ StorageBackend = (*postgres.Backend)(nil)

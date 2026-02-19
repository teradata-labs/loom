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

// Package backend defines the StorageBackend composite interface and factory.
// This package sits above pkg/agent, pkg/artifacts, pkg/shuttle, and pkg/storage
// to avoid import cycles while composing their individual store interfaces.
package backend

import (
	"context"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
)

// StorageBackend is the top-level composed interface for all storage operations.
// One StorageBackend per server; all agents share the same backend.
// Implementations include SQLiteBackend and PostgresBackend.
type StorageBackend interface {
	// SessionStorage returns the session storage implementation.
	SessionStorage() agent.SessionStorage

	// ErrorStore returns the error store implementation.
	ErrorStore() agent.ErrorStore

	// ArtifactStore returns the artifact store implementation.
	ArtifactStore() artifacts.ArtifactStore

	// ResultStore returns the SQL result store implementation.
	ResultStore() storage.ResultStore

	// HumanRequestStore returns the human request store implementation.
	HumanRequestStore() shuttle.HumanRequestStore

	// Migrate runs database migrations to the latest version.
	Migrate(ctx context.Context) error

	// Ping verifies the storage backend is reachable and healthy.
	Ping(ctx context.Context) error

	// Close closes all underlying connections.
	Close() error
}

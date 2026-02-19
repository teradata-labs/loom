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
package storage

import loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"

// ResultStore defines the backend-agnostic interface for SQL result storage.
// Implementations include SQLite (SQLResultStore) and PostgreSQL (postgres.ResultStore).
// All operations must be safe for concurrent use.
type ResultStore interface {
	// Store saves SQL result data and returns a DataReference.
	Store(id string, data interface{}) (*loomv1.DataReference, error)

	// Query executes a SQL query against a stored result.
	Query(id, query string) (interface{}, error)

	// GetMetadata returns metadata about a stored result.
	GetMetadata(id string) (*SQLResultMetadata, error)

	// Delete removes a stored result.
	Delete(id string) error

	// Close closes the store.
	Close() error
}

// Compile-time check: SQLResultStore (SQLite) implements ResultStore.
var _ ResultStore = (*SQLResultStore)(nil)

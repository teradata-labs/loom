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
package fabric

import (
	"context"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// ExecutionBackend defines the interface for pluggable execution backends.
// Implementations can be SQL databases (Teradata, Postgres, Snowflake),
// REST APIs, document stores, or any other domain-specific execution engine.
//
// This interface is intentionally minimal to support diverse backends while
// maintaining a common contract for the agent framework.
type ExecutionBackend interface {
	// Name returns the backend identifier (e.g., "teradata", "postgres", "rest-api")
	Name() string

	// ExecuteQuery executes a domain-specific query or operation.
	// For SQL backends: query is SQL, result contains rows/columns
	// For API backends: query is HTTP request spec, result contains response
	// For document backends: query is search criteria, result contains documents
	ExecuteQuery(ctx context.Context, query string) (*QueryResult, error)

	// GetSchema retrieves schema information for a resource.
	// For SQL: table/view schema with columns and types
	// For API: endpoint specifications
	// For documents: index schema
	GetSchema(ctx context.Context, resource string) (*Schema, error)

	// ListResources lists available resources with optional filtering.
	// For SQL: tables, views, procedures
	// For API: available endpoints
	// For documents: available indexes/collections
	ListResources(ctx context.Context, filters map[string]string) ([]Resource, error)

	// GetMetadata retrieves backend-specific metadata for a resource.
	// Examples: table statistics, index info, API rate limits, etc.
	GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error)

	// Ping checks backend connectivity and health.
	Ping(ctx context.Context) error

	// Capabilities returns the backend's capabilities for feature discovery.
	Capabilities() *Capabilities

	// ExecuteCustomOperation allows backend-specific operations not covered by
	// the standard interface. This provides an extension point for specialized
	// functionality while maintaining interface compatibility.
	ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error)

	// Close releases backend resources.
	Close() error
}

// QueryResult represents the result of executing a query or operation.
type QueryResult struct {
	// Type indicates the result type (e.g., "rows", "document", "response")
	Type string

	// Data contains the actual result data (format varies by backend)
	// For small results (<100KB), data is stored here directly
	// For large results, use DataReference instead
	Data interface{}

	// Rows for tabular results (SQL)
	Rows []map[string]interface{}

	// Columns for tabular results (SQL)
	Columns []Column

	// RowCount for tabular results
	RowCount int

	// Metadata contains backend-specific result metadata
	Metadata map[string]interface{}

	// ExecutionStats tracks execution metrics
	ExecutionStats ExecutionStats

	// DataReference points to large result data stored in shared memory
	// If set, Data/Rows fields should be empty and actual data retrieved via reference
	DataReference *loomv1.DataReference
}

// Column represents a column in tabular results.
type Column struct {
	Name     string
	Type     string
	Nullable bool
}

// ExecutionStats tracks execution metrics.
type ExecutionStats struct {
	// Duration in milliseconds
	DurationMs int64

	// BytesScanned for data operations
	BytesScanned int64

	// RowsAffected for write operations
	RowsAffected int64

	// Cost estimate (backend-specific units)
	EstimatedCost float64
}

// Schema represents the schema of a resource.
type Schema struct {
	// Resource name (table, endpoint, collection, etc.)
	Name string

	// Type of resource (table, view, api_endpoint, etc.)
	Type string

	// Fields/columns/properties
	Fields []Field

	// Metadata contains additional schema information
	Metadata map[string]interface{}
}

// Field represents a field/column in a schema.
type Field struct {
	Name        string
	Type        string
	Description string
	Nullable    bool
	PrimaryKey  bool
	ForeignKey  *ForeignKey
	Constraints []string
	Default     interface{}
}

// ForeignKey represents a foreign key relationship.
type ForeignKey struct {
	ReferencedTable  string
	ReferencedColumn string
}

// Resource represents an available resource in the backend.
type Resource struct {
	Name        string
	Type        string
	Description string
	Metadata    map[string]interface{}
}

// Capabilities describes what a backend supports.
type Capabilities struct {
	// SupportsTransactions indicates if the backend supports transactions
	SupportsTransactions bool

	// SupportsConcurrency indicates if the backend supports concurrent operations
	SupportsConcurrency bool

	// SupportsStreaming indicates if the backend supports streaming results
	SupportsStreaming bool

	// MaxConcurrentOps is the maximum number of concurrent operations
	MaxConcurrentOps int

	// SupportedOperations lists supported custom operations
	SupportedOperations []string

	// Features lists backend-specific features
	Features map[string]bool

	// Limits contains backend-specific limits
	Limits map[string]int64
}

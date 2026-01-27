// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package storage

import (
	"context"
	"fmt"
)

// Storage provides in-process trace storage for embedded observability.
// Implementations must be thread-safe for concurrent access.
type Storage interface {
	// Eval operations
	CreateEval(ctx context.Context, eval *Eval) error
	UpdateEvalStatus(ctx context.Context, evalID string, status string) error

	// EvalRun operations (trace/span storage)
	CreateEvalRun(ctx context.Context, run *EvalRun) error

	// Metrics operations
	CalculateEvalMetrics(ctx context.Context, evalID string) (*EvalMetrics, error)
	UpsertEvalMetrics(ctx context.Context, metrics *EvalMetrics) error

	// Lifecycle
	Close() error
}

// StorageConfig configures storage backend creation
type StorageConfig struct {
	// Type: "memory" or "sqlite"
	Type string

	// Path: SQLite database file path (required if Type = "sqlite")
	Path string

	// MaxMemoryTraces: Maximum traces to keep in memory storage (default: 10,000)
	// Only used for memory storage
	MaxMemoryTraces int
}

// DefaultStorageConfig returns sensible defaults
func DefaultStorageConfig() *StorageConfig {
	return &StorageConfig{
		Type:            "memory",
		MaxMemoryTraces: 10000,
	}
}

// NewStorage creates a new storage backend based on configuration
func NewStorage(config *StorageConfig) (Storage, error) {
	if config == nil {
		config = DefaultStorageConfig()
	}

	switch config.Type {
	case "memory":
		maxTraces := config.MaxMemoryTraces
		if maxTraces <= 0 {
			maxTraces = 10000
		}
		return NewMemoryStorage(maxTraces), nil

	case "sqlite":
		if config.Path == "" {
			return nil, fmt.Errorf("sqlite storage requires Path to be set")
		}
		return NewSQLiteStorage(config.Path)

	default:
		return nil, fmt.Errorf("unknown storage type: %s (supported: memory, sqlite)", config.Type)
	}
}

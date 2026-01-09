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
	"encoding/json"
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/storage"
	"go.uber.org/zap"
)

const (
	// DefaultSharedMemoryThreshold is 100KB
	DefaultSharedMemoryThreshold = 100 * 1024
)

// SharedBackendWrapper wraps an ExecutionBackend to automatically store
// large results in shared memory.
type SharedBackendWrapper struct {
	backend      ExecutionBackend
	sharedMemory *storage.SharedMemoryStore
	threshold    int64
	autoStore    bool
	logger       *zap.Logger
}

// SharedBackendConfig configures the shared backend wrapper.
type SharedBackendConfig struct {
	// Backend is the underlying execution backend to wrap
	Backend ExecutionBackend

	// SharedMemory is the shared memory store
	SharedMemory *storage.SharedMemoryStore

	// Threshold is the size threshold for using shared memory (bytes)
	// Results larger than this will be stored in shared memory
	Threshold int64

	// AutoStore automatically stores large results (default: true)
	AutoStore bool

	// Logger for structured logging (default: NoOp logger)
	Logger *zap.Logger
}

// NewSharedBackendWrapper creates a new shared backend wrapper.
func NewSharedBackendWrapper(config *SharedBackendConfig) (*SharedBackendWrapper, error) {
	if config.Backend == nil {
		return nil, fmt.Errorf("backend is required")
	}
	if config.SharedMemory == nil {
		return nil, fmt.Errorf("shared memory store is required")
	}

	threshold := config.Threshold
	if threshold <= 0 {
		threshold = DefaultSharedMemoryThreshold
	}

	autoStore := config.AutoStore
	if !autoStore {
		autoStore = true // Default to true
	}

	logger := config.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	return &SharedBackendWrapper{
		backend:      config.Backend,
		sharedMemory: config.SharedMemory,
		threshold:    threshold,
		autoStore:    autoStore,
		logger:       logger,
	}, nil
}

// Name returns the wrapped backend's name.
func (w *SharedBackendWrapper) Name() string {
	return w.backend.Name()
}

// ExecuteQuery executes a query and automatically stores large results in shared memory.
func (w *SharedBackendWrapper) ExecuteQuery(ctx context.Context, query string) (*QueryResult, error) {
	// Execute query on wrapped backend
	result, err := w.backend.ExecuteQuery(ctx, query)
	if err != nil {
		return nil, err
	}

	if !w.autoStore {
		return result, nil
	}

	// Check if result should be stored in shared memory
	resultSize := w.estimateResultSize(result)
	if resultSize <= w.threshold {
		return result, nil
	}

	// Store in shared memory
	refResult, err := w.storeInSharedMemory(result)
	if err != nil {
		// Log error but return original result
		w.logger.Warn("Failed to store query result in shared memory, returning original result",
			zap.Int64("result_size", resultSize),
			zap.Int64("threshold", w.threshold),
			zap.String("backend", w.backend.Name()),
			zap.Error(err),
		)
		return result, nil
	}

	return refResult, nil
}

// GetSchema delegates to wrapped backend.
func (w *SharedBackendWrapper) GetSchema(ctx context.Context, resource string) (*Schema, error) {
	return w.backend.GetSchema(ctx, resource)
}

// ListResources delegates to wrapped backend.
func (w *SharedBackendWrapper) ListResources(ctx context.Context, filters map[string]string) ([]Resource, error) {
	return w.backend.ListResources(ctx, filters)
}

// GetMetadata delegates to wrapped backend.
func (w *SharedBackendWrapper) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	return w.backend.GetMetadata(ctx, resource)
}

// Ping delegates to wrapped backend.
func (w *SharedBackendWrapper) Ping(ctx context.Context) error {
	return w.backend.Ping(ctx)
}

// Capabilities delegates to wrapped backend.
func (w *SharedBackendWrapper) Capabilities() *Capabilities {
	return w.backend.Capabilities()
}

// ExecuteCustomOperation delegates to wrapped backend.
func (w *SharedBackendWrapper) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	return w.backend.ExecuteCustomOperation(ctx, op, params)
}

// Close closes both the wrapper and wrapped backend.
func (w *SharedBackendWrapper) Close() error {
	return w.backend.Close()
}

// estimateResultSize estimates the size of a QueryResult in bytes.
func (w *SharedBackendWrapper) estimateResultSize(result *QueryResult) int64 {
	if result == nil {
		return 0
	}

	var size int64

	// Estimate based on row count and columns
	if result.RowCount > 0 && len(result.Columns) > 0 {
		// Rough estimate: rows * columns * average value size (100 bytes)
		size = int64(result.RowCount * len(result.Columns) * 100)
	}

	// If Rows are populated, calculate actual size
	if len(result.Rows) > 0 {
		// Serialize to JSON to get accurate size
		data, err := json.Marshal(result.Rows)
		if err == nil {
			size = int64(len(data))
		}
	}

	// If Data is populated, try to estimate size
	if result.Data != nil {
		data, err := json.Marshal(result.Data)
		if err == nil {
			size += int64(len(data))
		}
	}

	return size
}

// storeInSharedMemory stores a QueryResult in shared memory and returns a reference-based result.
func (w *SharedBackendWrapper) storeInSharedMemory(result *QueryResult) (*QueryResult, error) {
	// Serialize result data
	var dataToStore []byte
	var err error

	if len(result.Rows) > 0 {
		dataToStore, err = json.Marshal(result.Rows)
	} else if result.Data != nil {
		dataToStore, err = json.Marshal(result.Data)
	} else {
		// Nothing substantial to store
		return result, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to serialize result: %w", err)
	}

	// Generate ID
	id := storage.GenerateID()

	// Store in shared memory
	metadata := map[string]string{
		"type":      result.Type,
		"row_count": fmt.Sprintf("%d", result.RowCount),
	}
	ref, err := w.sharedMemory.Store(id, dataToStore, "application/json", metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to store in shared memory: %w", err)
	}

	// Create new result with reference
	refResult := &QueryResult{
		Type:           result.Type,
		Columns:        result.Columns,
		RowCount:       result.RowCount,
		Metadata:       result.Metadata,
		ExecutionStats: result.ExecutionStats,
		DataReference:  ref,
	}

	return refResult, nil
}

// RetrieveFromSharedMemory retrieves data from shared memory using a reference.
func (w *SharedBackendWrapper) RetrieveFromSharedMemory(ref *loomv1.DataReference) ([]byte, error) {
	return w.sharedMemory.Get(ref)
}

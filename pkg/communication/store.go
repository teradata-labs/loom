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
package communication

import (
	"context"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// ReferenceStore manages reference lifecycle and data storage.
// Implementations must be safe for concurrent use by multiple goroutines.
type ReferenceStore interface {
	// Store data and return reference
	Store(ctx context.Context, data []byte, opts StoreOptions) (*loomv1.Reference, error)

	// Resolve reference to actual data
	Resolve(ctx context.Context, ref *loomv1.Reference) ([]byte, error)

	// Retain increments reference count (retain ownership)
	Retain(ctx context.Context, refID string) error

	// Release decrements reference count (release ownership, may trigger GC)
	Release(ctx context.Context, refID string) error

	// List all references (debugging)
	List(ctx context.Context) ([]*loomv1.Reference, error)

	// Stats returns store statistics
	Stats(ctx context.Context) (*StoreStats, error)

	// Close cleans up store resources
	Close() error
}

// StoreOptions configures reference storage behavior
type StoreOptions struct {
	// Type categorizes the kind of data being stored
	Type loomv1.ReferenceType

	// ContentType specifies MIME type (e.g., "application/json", "text/plain")
	ContentType string

	// TTL specifies time-to-live in seconds (0 = never expires)
	TTL int64

	// Compression algorithm: "none", "gzip", "zstd"
	Compression string

	// Encoding applied: "none", "base64"
	Encoding string

	// ComputeChecksum enables integrity verification
	ComputeChecksum bool
}

// StoreStats provides statistics about reference storage
type StoreStats struct {
	// TotalRefs is the total references ever created
	TotalRefs int64

	// TotalBytes is the total bytes ever stored
	TotalBytes int64

	// ActiveRefs is the currently active references
	ActiveRefs int64

	// GCRuns is the number of garbage collection runs
	GCRuns int64

	// EvictionCount is the references evicted by GC
	EvictionCount int64

	// CurrentBytes is the current memory usage in bytes
	CurrentBytes int64
}

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

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// GenerateID generates a unique ID for a data reference.
func GenerateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// IsLargeData checks if data exceeds the threshold for shared memory.
func IsLargeData(data []byte, threshold int64) bool {
	return int64(len(data)) > threshold
}

// ShouldUseSharedMemory determines if data should go to shared memory.
func ShouldUseSharedMemory(dataSize int64, threshold int64) bool {
	return dataSize > threshold
}

// RefToString converts a DataReference to a human-readable string.
func RefToString(ref *loomv1.DataReference) string {
	if ref == nil {
		return "<nil>"
	}
	location := "UNKNOWN"
	switch ref.Location {
	case loomv1.StorageLocation_STORAGE_LOCATION_MEMORY:
		location = "MEMORY"
	case loomv1.StorageLocation_STORAGE_LOCATION_DISK:
		location = "DISK"
	}
	compressed := ""
	if ref.Compressed {
		compressed = " (compressed)"
	}
	// CRITICAL: Show FULL ID (not truncated) so LLM can extract complete reference_id for get_tool_result calls
	return fmt.Sprintf("DataRef[%s, %s, %d bytes%s]", ref.Id, location, ref.SizeBytes, compressed)
}

// ValidateReference validates a data reference.
func ValidateReference(ref *loomv1.DataReference) error {
	if ref == nil {
		return fmt.Errorf("reference is nil")
	}
	if ref.Id == "" {
		return fmt.Errorf("reference ID is empty")
	}
	if ref.SizeBytes <= 0 {
		return fmt.Errorf("reference size must be positive")
	}
	if ref.Checksum == "" {
		return fmt.Errorf("reference checksum is empty")
	}
	if ref.Location == loomv1.StorageLocation_STORAGE_LOCATION_UNSPECIFIED {
		return fmt.Errorf("reference location is unspecified")
	}
	return nil
}

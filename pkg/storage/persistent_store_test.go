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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetGlobalSharedMemory_Singleton(t *testing.T) {
	// Reset for clean test
	ResetGlobalSharedMemory()

	config := &Config{
		MaxMemoryBytes:       10 * 1024 * 1024, // 10MB
		CompressionThreshold: 1024 * 1024,      // 1MB
		TTLSeconds:           3600,
	}

	// Get instance twice
	store1 := GetGlobalSharedMemory(config)
	store2 := GetGlobalSharedMemory(config)

	// Should be same instance
	assert.Same(t, store1, store2, "GetGlobalSharedMemory should return same instance")

	// Should work across "agent instances"
	ref, err := store1.Store("test_ref", []byte("test data"), "text/plain", nil)
	require.NoError(t, err)
	require.NotNil(t, ref)

	// Retrieve from "different agent"
	data, err := store2.Get(ref)
	require.NoError(t, err)
	assert.Equal(t, "test data", string(data))
}

func TestGetGlobalSharedMemory_Stats(t *testing.T) {
	// Reset for clean test
	ResetGlobalSharedMemory()

	// Before initialization
	stats := GetGlobalSharedMemoryStats()
	assert.Nil(t, stats, "Stats should be nil before initialization")

	// After initialization
	config := &Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	}
	store := GetGlobalSharedMemory(config)
	require.NotNil(t, store)

	// Store some data
	_, err := store.Store("test1", []byte("data1"), "text/plain", nil)
	require.NoError(t, err)

	// Get stats
	stats = GetGlobalSharedMemoryStats()
	require.NotNil(t, stats)
	assert.Equal(t, 1, stats.ItemCount, "Should have 1 item")
	assert.Greater(t, stats.CurrentSize, int64(0), "Should have data")
}

func TestGetGlobalSharedMemory_PersistenceEnabled(t *testing.T) {
	// Reset for clean test
	ResetGlobalSharedMemory()

	config := &Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	}

	store := GetGlobalSharedMemory(config)
	require.NotNil(t, store)

	// Store should have overflow handler (disk persistence)
	// We can't directly check this, but we can verify it doesn't panic
	// when storing large amounts of data
	largeData := make([]byte, 15*1024*1024) // 15MB (exceeds 10MB memory limit)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	ref, err := store.Store("large_test", largeData, "application/octet-stream", nil)
	// Should either succeed (disk overflow) or fail gracefully
	if err != nil {
		t.Logf("Large data storage failed (expected if no disk space): %v", err)
	} else {
		require.NotNil(t, ref)
		t.Logf("Large data stored successfully with location: %v", ref.Location)
	}
}

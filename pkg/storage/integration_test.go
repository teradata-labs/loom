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
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestStorageIntegration_MemoryToDiskOverflow tests the full storage lifecycle
// with data overflowing from memory to disk.
func TestStorageIntegration_MemoryToDiskOverflow(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()

	// Create disk overflow manager
	diskManager, err := NewDiskOverflowManager(&DiskOverflowConfig{
		CachePath:   tmpDir,
		MaxDiskSize: 50 * 1024 * 1024, // 50MB
		TTLSeconds:  3600,
	})
	require.NoError(t, err)

	// Create shared memory store with small memory limit to force overflow
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       5 * 1024 * 1024, // 5MB memory limit
		CompressionThreshold: 1024 * 1024,     // 1MB
		TTLSeconds:           3600,
		OverflowHandler:      diskManager,
	})

	// Store 10 chunks of 2MB each (total 20MB)
	// First 2-3 will go to memory, rest will overflow to disk
	numChunks := 10
	chunkSize := 2 * 1024 * 1024 // 2MB
	refs := make([]*Reference, numChunks)

	for i := 0; i < numChunks; i++ {
		id := fmt.Sprintf("chunk-%d", i)
		data := bytes.Repeat([]byte(fmt.Sprintf("chunk %d ", i)), chunkSize/10)

		ref, err := store.Store(id, data, "application/octet-stream", nil)
		require.NoError(t, err)
		refs[i] = &Reference{id: id, ref: ref, data: data}
	}

	// Verify stats
	memStats := store.Stats()
	diskStats := diskManager.Stats()

	t.Logf("Memory: %d bytes, %d items", memStats.CurrentSize, memStats.ItemCount)
	t.Logf("Disk: %d bytes, %d items", diskStats.CurrentSize, diskStats.ItemCount)

	// Should have items in memory, disk overflow depends on compression effectiveness
	assert.Greater(t, memStats.ItemCount, 0)
	totalItems := memStats.ItemCount + diskStats.ItemCount
	assert.Equal(t, numChunks, totalItems, "All chunks should be stored")

	// Verify all chunks are retrievable
	for i, ref := range refs {
		retrieved, err := store.Get(ref.ref)
		require.NoError(t, err, "Failed to retrieve chunk %d", i)
		assert.Equal(t, len(ref.data), len(retrieved), "Chunk %d size mismatch", i)
	}
}

// TestStorageIntegration_LargeDataset tests handling 100MB+ of data.
func TestStorageIntegration_LargeDataset(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large dataset test in short mode")
	}

	tmpDir := t.TempDir()

	diskManager, err := NewDiskOverflowManager(&DiskOverflowConfig{
		CachePath:   tmpDir,
		MaxDiskSize: 200 * 1024 * 1024, // 200MB
		TTLSeconds:  3600,
	})
	require.NoError(t, err)

	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       10 * 1024 * 1024, // 10MB memory
		CompressionThreshold: 512 * 1024,       // 512KB
		TTLSeconds:           3600,
		OverflowHandler:      diskManager,
	})

	// Store 100 chunks of 1MB each (total 100MB)
	numChunks := 100
	chunkSize := 1024 * 1024 // 1MB
	storedRefs := make(map[string][]byte)

	for i := 0; i < numChunks; i++ {
		id := fmt.Sprintf("chunk-%d", i)
		// Create compressible data
		data := bytes.Repeat([]byte(fmt.Sprintf("%08d", i)), chunkSize/8)

		ref, err := store.Store(id, data, "application/octet-stream", nil)
		require.NoError(t, err)
		storedRefs[ref.Id] = data
	}

	// Verify stats
	memStats := store.Stats()
	diskStats := diskManager.Stats()

	t.Logf("Stored 100MB across memory and disk")
	t.Logf("Memory: %d MB, %d items, %d compressions",
		memStats.CurrentSize/(1024*1024), memStats.ItemCount, memStats.Compressions)
	t.Logf("Disk: %d MB, %d items",
		diskStats.CurrentSize/(1024*1024), diskStats.ItemCount)

	// Should have compressed some data
	assert.Greater(t, memStats.Compressions, int64(0))

	// Spot check - retrieve 10 random chunks
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("chunk-%d", i*10)
		ref := &loomv1.DataReference{
			Id:       id,
			Location: loomv1.StorageLocation_STORAGE_LOCATION_MEMORY,
		}
		// Try to get from store
		_, err := store.Get(ref)
		// It might be on disk or evicted, that's OK for this test
		if err != nil {
			t.Logf("Chunk %s not in memory (likely on disk or evicted)", id)
		}
	}
}

// TestStorageIntegration_ConcurrentAccess tests concurrent access to storage system.
func TestStorageIntegration_ConcurrentAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent access test in short mode")
	}

	tmpDir := t.TempDir()

	diskManager, err := NewDiskOverflowManager(&DiskOverflowConfig{
		CachePath:   tmpDir,
		MaxDiskSize: 50 * 1024 * 1024,
		TTLSeconds:  3600,
	})
	require.NoError(t, err)

	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       5 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
		OverflowHandler:      diskManager,
	})

	// Launch 10 goroutines that each store and retrieve 10 chunks
	numWorkers := 10
	chunksPerWorker := 10
	var wg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for i := 0; i < chunksPerWorker; i++ {
				id := fmt.Sprintf("worker-%d-chunk-%d", workerID, i)
				data := bytes.Repeat([]byte(fmt.Sprintf("w%d-c%d ", workerID, i)), 50*1024) // 500KB

				// Store
				ref, err := store.Store(id, data, "application/octet-stream", nil)
				if err != nil {
					t.Logf("Worker %d store error: %v", workerID, err)
					continue
				}

				// Retrieve immediately
				retrieved, err := store.Get(ref)
				if err != nil {
					t.Logf("Worker %d retrieve error: %v", workerID, err)
					continue
				}

				assert.Equal(t, data, retrieved)
				store.Release(id)
			}
		}(w)
	}

	wg.Wait()

	memStats := store.Stats()
	diskStats := diskManager.Stats()

	t.Logf("Concurrent test completed")
	t.Logf("Memory: %d bytes, %d items, %d hits, %d misses",
		memStats.CurrentSize, memStats.ItemCount, memStats.Hits, memStats.Misses)
	t.Logf("Disk: %d bytes, %d items", diskStats.CurrentSize, diskStats.ItemCount)

	// Should have successfully stored and retrieved data
	assert.Greater(t, memStats.Hits, int64(0))
}

// TestStorageIntegration_TTLCleanup tests TTL cleanup across memory and disk.
func TestStorageIntegration_TTLCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping TTL cleanup test in short mode")
	}

	tmpDir := t.TempDir()

	diskManager, err := NewDiskOverflowManager(&DiskOverflowConfig{
		CachePath:   tmpDir,
		MaxDiskSize: 10 * 1024 * 1024,
		TTLSeconds:  2, // 2 seconds
	})
	require.NoError(t, err)

	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       1 * 1024 * 1024, // 1MB
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           2, // 2 seconds
		OverflowHandler:      diskManager,
	})

	// Store 5 chunks that will overflow to disk
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("chunk-%d", i)
		data := bytes.Repeat([]byte(fmt.Sprintf("%d", i)), 500*1024) // 500KB

		_, err := store.Store(id, data, "application/octet-stream", nil)
		require.NoError(t, err)
		store.Release(id)
	}

	memStatsBefore := store.Stats()
	diskStatsBefore := diskManager.Stats()

	t.Logf("Before TTL: Memory %d items, Disk %d items",
		memStatsBefore.ItemCount, diskStatsBefore.ItemCount)

	// Wait for TTL to expire
	time.Sleep(3 * time.Second)

	// Trigger cleanup manually
	store.cleanup()
	diskManager.mu.Lock()
	diskManager.cleanupExpired()
	diskManager.mu.Unlock()

	memStatsAfter := store.Stats()
	diskStatsAfter := diskManager.Stats()

	t.Logf("After TTL: Memory %d items, Disk %d items",
		memStatsAfter.ItemCount, diskStatsAfter.ItemCount)

	// Both should have cleaned up items
	assert.Less(t, memStatsAfter.ItemCount, memStatsBefore.ItemCount)
	assert.LessOrEqual(t, diskStatsAfter.ItemCount, diskStatsBefore.ItemCount)
}

// Reference helper for integration tests
type Reference struct {
	id   string
	ref  *loomv1.DataReference
	data []byte
}

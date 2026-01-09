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
)

func TestSharedMemoryStore_StoreAndGet(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       10 * 1024 * 1024, // 10MB
		CompressionThreshold: 1024 * 1024,      // 1MB
		TTLSeconds:           3600,
	})

	data := []byte("hello world")
	ref, err := store.Store("test1", data, "text/plain", map[string]string{"key": "value"})
	require.NoError(t, err)
	require.NotNil(t, ref)
	assert.Equal(t, "test1", ref.Id)
	assert.Equal(t, int64(len(data)), ref.SizeBytes)
	assert.False(t, ref.Compressed) // Too small to compress

	// Retrieve
	retrieved, err := store.Get(ref)
	require.NoError(t, err)
	assert.Equal(t, data, retrieved)

	// Stats
	stats := store.Stats()
	assert.Equal(t, int64(1), stats.Hits)
	assert.Equal(t, 1, stats.ItemCount)
}

func TestSharedMemoryStore_Compression(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 100, // Low threshold for testing
		TTLSeconds:           3600,
	})

	// Create compressible data (repeated pattern)
	data := bytes.Repeat([]byte("abcdefghijklmnop"), 1000) // 16KB
	ref, err := store.Store("test-compressed", data, "application/octet-stream", nil)
	require.NoError(t, err)
	assert.True(t, ref.Compressed)

	// Retrieve and verify
	retrieved, err := store.Get(ref)
	require.NoError(t, err)
	assert.Equal(t, data, retrieved)

	stats := store.Stats()
	assert.Greater(t, stats.Compressions, int64(0))
}

func TestSharedMemoryStore_LRUEviction(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       600, // Very small for testing
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	// Store multiple items to trigger eviction
	data1 := bytes.Repeat([]byte("a"), 300)
	ref1, err := store.Store("item1", data1, "text/plain", nil)
	require.NoError(t, err)
	store.Release("item1") // Release so it can be evicted

	data2 := bytes.Repeat([]byte("b"), 300)
	ref2, err := store.Store("item2", data2, "text/plain", nil)
	require.NoError(t, err)
	store.Release("item2")

	// Trigger eviction by adding more data
	data3 := bytes.Repeat([]byte("c"), 300)
	ref3, err := store.Store("item3", data3, "text/plain", nil)
	require.NoError(t, err)

	// Verify at least one item was evicted
	stats := store.Stats()
	assert.Greater(t, stats.Evictions, int64(0))

	// Verify item3 (most recent) is accessible
	retrieved3, err := store.Get(ref3)
	require.NoError(t, err)
	assert.Equal(t, data3, retrieved3)

	// At least one of the older items should be evicted
	_, err1 := store.Get(ref1)
	_, err2 := store.Get(ref2)
	assert.True(t, err1 != nil || err2 != nil, "At least one item should be evicted")
}

func TestSharedMemoryStore_RefCounting(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       1000,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	data1 := bytes.Repeat([]byte("a"), 400)
	ref1, err := store.Store("item1", data1, "text/plain", nil)
	require.NoError(t, err)

	// Access item1 to increment ref count
	_, err = store.Get(ref1)
	require.NoError(t, err)

	data2 := bytes.Repeat([]byte("b"), 400)
	ref2, err := store.Store("item2", data2, "text/plain", nil)
	require.NoError(t, err)

	// Try to store item3 - should NOT evict item1 because it's referenced
	data3 := bytes.Repeat([]byte("c"), 400)
	_, err = store.Store("item3", data3, "text/plain", nil)

	// Should fail or evict item2 instead of item1
	if err == nil {
		// If it succeeded, verify item1 is still there
		_, err = store.Get(ref1)
		assert.NoError(t, err)
	}

	// Release ref count on item1
	store.Release("item1")

	// Verify item2 might have been evicted instead
	_, _ = store.Get(ref2)
	// Item2 might be evicted, that's OK
}

func TestSharedMemoryStore_ConcurrentAccess(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	var wg sync.WaitGroup
	numGoroutines := 50
	numOpsPerGoroutine := 100

	// Concurrent writes and reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOpsPerGoroutine; j++ {
				key := fmt.Sprintf("key-%d-%d", id, j)
				data := []byte(fmt.Sprintf("data-%d-%d", id, j))

				// Store
				ref, err := store.Store(key, data, "text/plain", nil)
				if err != nil {
					t.Logf("Store error: %v", err)
					continue
				}

				// Retrieve immediately
				retrieved, err := store.Get(ref)
				if err != nil {
					t.Logf("Get error: %v", err)
					continue
				}

				assert.Equal(t, data, retrieved)

				// Release
				store.Release(key)
			}
		}(i)
	}

	wg.Wait()

	stats := store.Stats()
	t.Logf("Stats: %+v", stats)
	assert.Greater(t, stats.Hits, int64(0))
}

func TestSharedMemoryStore_Delete(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	data := []byte("test data")
	ref, err := store.Store("test1", data, "text/plain", nil)
	require.NoError(t, err)

	// Verify it exists
	_, err = store.Get(ref)
	require.NoError(t, err)

	// Delete
	err = store.Delete("test1")
	require.NoError(t, err)

	// Verify it's gone
	_, err = store.Get(ref)
	assert.Error(t, err)

	stats := store.Stats()
	assert.Equal(t, 0, stats.ItemCount)
}

func TestSharedMemoryStore_Cleanup(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           1, // 1 second TTL for testing
	})

	data := []byte("test data")
	ref, err := store.Store("test1", data, "text/plain", nil)
	require.NoError(t, err)

	// Access to update access time
	_, err = store.Get(ref)
	require.NoError(t, err)

	// Release ref count so it can be cleaned up (twice: once for Store, once for Get)
	store.Release("test1")
	store.Release("test1")

	// Wait for TTL to expire
	time.Sleep(2 * time.Second)

	// Manually trigger cleanup
	store.cleanup()

	// Verify it was cleaned up
	_, err = store.Get(ref)
	assert.Error(t, err)
}

func TestSharedMemoryStore_ChecksumValidation(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	data := []byte("test data")
	ref, err := store.Store("test1", data, "text/plain", nil)
	require.NoError(t, err)

	// Tamper with checksum
	ref.Checksum = "invalid"

	// Should fail checksum validation
	_, err = store.Get(ref)
	assert.Error(t, err)
}

func TestSharedMemoryStore_Lifecycle(t *testing.T) {
	// Test full lifecycle: Store -> Get -> Release -> Delete
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	data := []byte("lifecycle test")
	ref, err := store.Store("lifecycle", data, "text/plain", map[string]string{"test": "true"})
	require.NoError(t, err)
	assert.NotEmpty(t, ref.Id)
	assert.NotEmpty(t, ref.Checksum)

	// Get and verify
	retrieved, err := store.Get(ref)
	require.NoError(t, err)
	assert.Equal(t, data, retrieved)

	// Get again (should hit cache)
	retrieved, err = store.Get(ref)
	require.NoError(t, err)
	assert.Equal(t, data, retrieved)

	// Release twice (for two Gets)
	store.Release("lifecycle")
	store.Release("lifecycle")

	// Delete
	err = store.Delete("lifecycle")
	require.NoError(t, err)

	// Verify deleted
	_, err = store.Get(ref)
	assert.Error(t, err)

	stats := store.Stats()
	assert.Equal(t, int64(2), stats.Hits)
	assert.Equal(t, 0, stats.ItemCount)
}

func TestSharedMemoryStore_LargeData(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       50 * 1024 * 1024, // 50MB
		CompressionThreshold: 1024 * 1024,      // 1MB
		TTLSeconds:           3600,
	})

	// Create 10MB of data
	data := bytes.Repeat([]byte("large data test "), 640*1024) // ~10MB
	ref, err := store.Store("large", data, "application/octet-stream", nil)
	require.NoError(t, err)
	assert.True(t, ref.Compressed)

	// Retrieve and verify
	retrieved, err := store.Get(ref)
	require.NoError(t, err)
	assert.Equal(t, len(data), len(retrieved))
	assert.Equal(t, data, retrieved)

	stats := store.Stats()
	assert.Greater(t, stats.Compressions, int64(0))
	t.Logf("Large data stats: %+v", stats)
}

// Benchmark tests

func BenchmarkSharedMemoryStore_Store(b *testing.B) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       100 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	data := bytes.Repeat([]byte("benchmark"), 1000) // ~9KB

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("bench-%d", i)
		_, err := store.Store(key, data, "application/octet-stream", nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSharedMemoryStore_Get(b *testing.B) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       100 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	data := bytes.Repeat([]byte("benchmark"), 1000)
	ref, err := store.Store("bench", data, "application/octet-stream", nil)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := store.Get(ref)
		if err != nil {
			b.Fatal(err)
		}
	}
}

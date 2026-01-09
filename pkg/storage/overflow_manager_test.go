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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestDiskOverflowManager_StoreAndRetrieve(t *testing.T) {
	tmpDir := t.TempDir()
	manager, err := NewDiskOverflowManager(&DiskOverflowConfig{
		CachePath:   tmpDir,
		MaxDiskSize: 10 * 1024 * 1024, // 10MB
		TTLSeconds:  3600,
	})
	require.NoError(t, err)

	data := []byte("test data for disk overflow")
	ref := &loomv1.DataReference{
		Id:          "test1",
		Checksum:    "abc123",
		ContentType: "text/plain",
		Compressed:  false,
	}

	// Store
	err = manager.Store("test1", data, ref)
	require.NoError(t, err)

	// Verify file exists
	filePath := filepath.Join(tmpDir, "test1.dat")
	_, err = os.Stat(filePath)
	require.NoError(t, err)

	// Retrieve
	retrieved, err := manager.Retrieve("test1")
	require.NoError(t, err)
	assert.Equal(t, data, retrieved)

	// Stats
	stats := manager.Stats()
	assert.Equal(t, 1, stats.ItemCount)
	assert.Equal(t, int64(len(data)), stats.CurrentSize)
}

func TestDiskOverflowManager_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	manager, err := NewDiskOverflowManager(&DiskOverflowConfig{
		CachePath:   tmpDir,
		MaxDiskSize: 10 * 1024 * 1024,
		TTLSeconds:  3600,
	})
	require.NoError(t, err)

	data := []byte("test data")
	ref := &loomv1.DataReference{
		Id:       "test1",
		Checksum: "abc123",
	}

	// Store
	err = manager.Store("test1", data, ref)
	require.NoError(t, err)

	// Delete
	err = manager.Delete("test1")
	require.NoError(t, err)

	// Verify file removed
	filePath := filepath.Join(tmpDir, "test1.dat")
	_, err = os.Stat(filePath)
	assert.True(t, os.IsNotExist(err))

	// Verify metadata removed
	_, err = manager.Retrieve("test1")
	assert.Error(t, err)

	// Stats
	stats := manager.Stats()
	assert.Equal(t, 0, stats.ItemCount)
	assert.Equal(t, int64(0), stats.CurrentSize)
}

func TestDiskOverflowManager_DiskFull(t *testing.T) {
	tmpDir := t.TempDir()
	manager, err := NewDiskOverflowManager(&DiskOverflowConfig{
		CachePath:   tmpDir,
		MaxDiskSize: 1000, // Very small
		TTLSeconds:  3600,
	})
	require.NoError(t, err)

	// Fill up disk
	data1 := bytes.Repeat([]byte("a"), 500)
	ref1 := &loomv1.DataReference{Id: "test1", Checksum: "abc"}
	err = manager.Store("test1", data1, ref1)
	require.NoError(t, err)

	data2 := bytes.Repeat([]byte("b"), 500)
	ref2 := &loomv1.DataReference{Id: "test2", Checksum: "def"}
	err = manager.Store("test2", data2, ref2)
	require.NoError(t, err)

	// This should fail - disk full
	data3 := bytes.Repeat([]byte("c"), 500)
	ref3 := &loomv1.DataReference{Id: "test3", Checksum: "ghi"}
	err = manager.Store("test3", data3, ref3)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "disk cache full")
}

func TestDiskOverflowManager_TTLCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	manager, err := NewDiskOverflowManager(&DiskOverflowConfig{
		CachePath:   tmpDir,
		MaxDiskSize: 10 * 1024 * 1024,
		TTLSeconds:  1, // 1 second for testing
	})
	require.NoError(t, err)

	data := []byte("test data")
	ref := &loomv1.DataReference{
		Id:       "test1",
		Checksum: "abc123",
	}

	// Store
	err = manager.Store("test1", data, ref)
	require.NoError(t, err)

	// Wait for TTL to expire
	time.Sleep(2 * time.Second)

	// Trigger cleanup manually
	manager.mu.Lock()
	manager.cleanupExpired()
	manager.mu.Unlock()

	// Verify cleaned up
	_, err = manager.Retrieve("test1")
	assert.Error(t, err)

	stats := manager.Stats()
	assert.Equal(t, 0, stats.ItemCount)
}

func TestDiskOverflowManager_Promote(t *testing.T) {
	tmpDir := t.TempDir()
	manager, err := NewDiskOverflowManager(&DiskOverflowConfig{
		CachePath:   tmpDir,
		MaxDiskSize: 10 * 1024 * 1024,
		TTLSeconds:  3600,
	})
	require.NoError(t, err)

	data := []byte("promote test")
	ref := &loomv1.DataReference{
		Id:       "test1",
		Checksum: "abc123",
	}

	// Store
	err = manager.Store("test1", data, ref)
	require.NoError(t, err)

	// Promote (retrieve and delete)
	promoted, err := manager.Promote("test1")
	require.NoError(t, err)
	assert.Equal(t, data, promoted)

	// Verify deleted from disk
	_, err = manager.Retrieve("test1")
	assert.Error(t, err)

	stats := manager.Stats()
	assert.Equal(t, 0, stats.ItemCount)
}

func TestDiskOverflowManager_MultipleItems(t *testing.T) {
	tmpDir := t.TempDir()
	manager, err := NewDiskOverflowManager(&DiskOverflowConfig{
		CachePath:   tmpDir,
		MaxDiskSize: 10 * 1024 * 1024,
		TTLSeconds:  3600,
	})
	require.NoError(t, err)

	// Store multiple items
	numItems := 10
	for i := 0; i < numItems; i++ {
		data := []byte(string(rune('a' + i)))
		ref := &loomv1.DataReference{
			Id:       string(rune('a' + i)),
			Checksum: "checksum",
		}
		err = manager.Store(string(rune('a'+i)), data, ref)
		require.NoError(t, err)
	}

	stats := manager.Stats()
	assert.Equal(t, numItems, stats.ItemCount)

	// Retrieve all
	for i := 0; i < numItems; i++ {
		id := string(rune('a' + i))
		retrieved, err := manager.Retrieve(id)
		require.NoError(t, err)
		assert.Equal(t, []byte(id), retrieved)
	}

	// Delete all
	for i := 0; i < numItems; i++ {
		err = manager.Delete(string(rune('a' + i)))
		require.NoError(t, err)
	}

	stats = manager.Stats()
	assert.Equal(t, 0, stats.ItemCount)
}

func TestDiskOverflowManager_LargeData(t *testing.T) {
	tmpDir := t.TempDir()
	manager, err := NewDiskOverflowManager(&DiskOverflowConfig{
		CachePath:   tmpDir,
		MaxDiskSize: 50 * 1024 * 1024, // 50MB
		TTLSeconds:  3600,
	})
	require.NoError(t, err)

	// Store 10MB of data
	data := bytes.Repeat([]byte("large data"), 1024*1024) // ~10MB
	ref := &loomv1.DataReference{
		Id:       "large",
		Checksum: "checksum",
	}

	err = manager.Store("large", data, ref)
	require.NoError(t, err)

	// Retrieve and verify
	retrieved, err := manager.Retrieve("large")
	require.NoError(t, err)
	assert.Equal(t, len(data), len(retrieved))
	assert.Equal(t, data, retrieved)

	stats := manager.Stats()
	assert.Greater(t, stats.CurrentSize, int64(1024*1024))
}

func TestDiskOverflowManager_AccessTimeUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	manager, err := NewDiskOverflowManager(&DiskOverflowConfig{
		CachePath:   tmpDir,
		MaxDiskSize: 10 * 1024 * 1024,
		TTLSeconds:  2, // 2 seconds
	})
	require.NoError(t, err)

	data := []byte("access time test")
	ref := &loomv1.DataReference{
		Id:       "test1",
		Checksum: "abc123",
	}

	// Store
	err = manager.Store("test1", data, ref)
	require.NoError(t, err)

	// Wait 1 second
	time.Sleep(1 * time.Second)

	// Access to update access time
	_, err = manager.Retrieve("test1")
	require.NoError(t, err)

	// Wait another 1.5 seconds (total 2.5 from store, but only 1.5 from last access)
	time.Sleep(1500 * time.Millisecond)

	// Trigger cleanup - should NOT remove because access time was updated
	manager.mu.Lock()
	manager.cleanupExpired()
	manager.mu.Unlock()

	// Verify still exists
	_, err = manager.Retrieve("test1")
	assert.NoError(t, err)
}

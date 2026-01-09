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
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestSharedMemoryPutGet(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Put value
	putResp, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "test.key",
		Value:     []byte("test value"),
		AgentId:   "agent1",
		Metadata: map[string]string{
			"content_type": "text/plain",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), putResp.Version)
	assert.True(t, putResp.Created)
	assert.NotEmpty(t, putResp.Checksum, "checksum should not be empty")

	// Get value
	getResp, err := store.Get(ctx, &loomv1.GetSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "test.key",
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.True(t, getResp.Found)
	assert.Equal(t, []byte("test value"), getResp.Value.Value)
	assert.Equal(t, int64(1), getResp.Value.Version)
	assert.Equal(t, "agent1", getResp.Value.CreatedBy)
	assert.Equal(t, "text/plain", getResp.Value.Metadata["content_type"])
}

func TestSharedMemoryVersioning(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Initial put
	putResp1, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "versioned.key",
		Value:     []byte("value 1"),
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), putResp1.Version)

	// Update with correct expected version
	putResp2, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace:       loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:             "versioned.key",
		Value:           []byte("value 2"),
		ExpectedVersion: 1,
		AgentId:         "agent2",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), putResp2.Version)
	assert.False(t, putResp2.Created) // Updated, not created

	// Update with incorrect expected version (should fail)
	_, err = store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace:       loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:             "versioned.key",
		Value:           []byte("value 3"),
		ExpectedVersion: 1, // Wrong version, current is 2
		AgentId:         "agent3",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "version conflict")

	// Verify version is still 2
	getResp, err := store.Get(ctx, &loomv1.GetSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "versioned.key",
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), getResp.Value.Version)
	assert.Equal(t, []byte("value 2"), getResp.Value.Value)
	assert.Equal(t, "agent2", getResp.Value.UpdatedBy)
}

func TestSharedMemoryCompression(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Create large value (>1KB to trigger auto-compression)
	largeValue := []byte(strings.Repeat("test data ", 200)) // ~2KB

	// Put large value (should auto-compress)
	putResp, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "large.key",
		Value:     largeValue,
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.Less(t, putResp.SizeBytes, int64(len(largeValue)), "compressed size should be smaller")

	// Get value (should be decompressed automatically)
	getResp, err := store.Get(ctx, &loomv1.GetSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "large.key",
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.True(t, getResp.Found)
	assert.Equal(t, largeValue, getResp.Value.Value, "decompressed value should match original")
	assert.False(t, getResp.Value.Compressed, "returned value should show as not compressed")

	// Small value should not be compressed
	smallValue := []byte("small")
	putResp2, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "small.key",
		Value:     smallValue,
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(len(smallValue)), putResp2.SizeBytes)
}

func TestSharedMemoryDelete(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Put value
	putResp, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "delete.key",
		Value:     []byte("to be deleted"),
		AgentId:   "agent1",
	})
	require.NoError(t, err)

	// Delete with correct version
	delResp, err := store.Delete(ctx, &loomv1.DeleteSharedMemoryRequest{
		Namespace:       loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:             "delete.key",
		ExpectedVersion: putResp.Version,
		AgentId:         "agent1",
	})
	require.NoError(t, err)
	assert.True(t, delResp.Deleted)
	assert.Equal(t, putResp.Version, delResp.DeletedVersion)

	// Get should return not found
	getResp, err := store.Get(ctx, &loomv1.GetSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "delete.key",
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.False(t, getResp.Found)

	// Delete non-existent key
	delResp2, err := store.Delete(ctx, &loomv1.DeleteSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "nonexistent",
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.False(t, delResp2.Deleted)
}

func TestSharedMemoryDeleteVersionConflict(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Put value
	putResp, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "conflict.key",
		Value:     []byte("value"),
		AgentId:   "agent1",
	})
	require.NoError(t, err)

	// Update to version 2
	_, err = store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace:       loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:             "conflict.key",
		Value:           []byte("value 2"),
		ExpectedVersion: 1,
		AgentId:         "agent1",
	})
	require.NoError(t, err)

	// Delete with wrong version (should fail)
	_, err = store.Delete(ctx, &loomv1.DeleteSharedMemoryRequest{
		Namespace:       loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:             "conflict.key",
		ExpectedVersion: putResp.Version, // Version 1, but current is 2
		AgentId:         "agent1",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "version conflict")

	// Verify key still exists
	getResp, err := store.Get(ctx, &loomv1.GetSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "conflict.key",
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.True(t, getResp.Found)
	assert.Equal(t, int64(2), getResp.Value.Version)
}

func TestSharedMemoryNamespaceIsolation(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Put same key in different namespaces
	namespaces := []loomv1.SharedMemoryNamespace{
		loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
		loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_SWARM,
	}

	for _, ns := range namespaces {
		_, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
			Namespace: ns,
			Key:       "shared.key",
			Value:     []byte(fmt.Sprintf("value in %s", ns.String())),
			AgentId:   "agent1",
		})
		require.NoError(t, err)
	}

	// Verify each namespace has its own value
	for _, ns := range namespaces {
		getResp, err := store.Get(ctx, &loomv1.GetSharedMemoryRequest{
			Namespace: ns,
			Key:       "shared.key",
			AgentId:   "agent1",
		})
		require.NoError(t, err)
		assert.True(t, getResp.Found)
		assert.Equal(t, fmt.Sprintf("value in %s", ns.String()), string(getResp.Value.Value))
	}
}

func TestSharedMemoryList(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Put multiple keys
	keys := []string{"config.db", "config.api", "config.cache", "session.user1", "session.user2"}
	for _, key := range keys {
		_, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
			Key:       key,
			Value:     []byte("value"),
			AgentId:   "agent1",
		})
		require.NoError(t, err)
	}

	// List all keys
	listResp, err := store.List(ctx, &loomv1.ListSharedMemoryKeysRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.Equal(t, int32(5), listResp.TotalCount)
	assert.Len(t, listResp.Keys, 5)

	// List with pattern
	listResp2, err := store.List(ctx, &loomv1.ListSharedMemoryKeysRequest{
		Namespace:  loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		KeyPattern: "config.*",
		AgentId:    "agent1",
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), listResp2.TotalCount)
	for _, key := range listResp2.Keys {
		assert.True(t, strings.HasPrefix(key, "config."))
	}

	// List with pattern
	listResp3, err := store.List(ctx, &loomv1.ListSharedMemoryKeysRequest{
		Namespace:  loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		KeyPattern: "session.*",
		AgentId:    "agent1",
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), listResp3.TotalCount)
}

func TestSharedMemoryWatch(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Create watcher
	watchChan, err := store.Watch(ctx, &loomv1.WatchSharedMemoryRequest{
		Namespace:  loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		KeyPattern: "watch.*",
		AgentId:    "agent1",
	})
	require.NoError(t, err)

	// Put value (should trigger watcher)
	_, err = store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "watch.key1",
		Value:     []byte("watched value"),
		AgentId:   "agent1",
	})
	require.NoError(t, err)

	// Receive notification
	select {
	case value := <-watchChan:
		assert.Equal(t, "watch.key1", value.Key)
		assert.Equal(t, []byte("watched value"), value.Value)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for watch notification")
	}

	// Put non-matching key (should not trigger)
	_, err = store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "other.key",
		Value:     []byte("other value"),
		AgentId:   "agent1",
	})
	require.NoError(t, err)

	// Should NOT receive notification
	select {
	case <-watchChan:
		t.Fatal("unexpected notification for non-matching key")
	case <-time.After(100 * time.Millisecond):
		// Expected: no notification
	}
}

func TestSharedMemoryWatchIncludeInitial(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Put initial values
	for i := 0; i < 3; i++ {
		_, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
			Key:       fmt.Sprintf("initial.key%d", i),
			Value:     []byte(fmt.Sprintf("initial value %d", i)),
			AgentId:   "agent1",
		})
		require.NoError(t, err)
	}

	// Create watcher with include_initial
	watchChan, err := store.Watch(ctx, &loomv1.WatchSharedMemoryRequest{
		Namespace:      loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		KeyPattern:     "initial.*",
		AgentId:        "agent1",
		IncludeInitial: true,
	})
	require.NoError(t, err)

	// Receive initial values
	received := 0
	timeout := time.After(time.Second)
	for received < 3 {
		select {
		case value := <-watchChan:
			assert.True(t, strings.HasPrefix(value.Key, "initial.key"))
			received++
		case <-timeout:
			t.Fatalf("timeout after receiving %d/3 initial values", received)
		}
	}
}

func TestSharedMemoryStats(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Put 5 values
	for i := 0; i < 5; i++ {
		_, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
			Key:       fmt.Sprintf("stats.key%d", i),
			Value:     []byte(fmt.Sprintf("value %d", i)),
			AgentId:   "agent1",
		})
		require.NoError(t, err)
	}

	// Get stats
	stats, err := store.GetStats(ctx, loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL)
	require.NoError(t, err)
	assert.Equal(t, int64(5), stats.KeyCount)
	assert.Equal(t, int64(5), stats.WriteCount)
	assert.Greater(t, stats.TotalBytes, int64(0))
	assert.Greater(t, stats.LastAccessAt, int64(0))

	// Read values
	for i := 0; i < 5; i++ {
		_, err := store.Get(ctx, &loomv1.GetSharedMemoryRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
			Key:       fmt.Sprintf("stats.key%d", i),
			AgentId:   "agent1",
		})
		require.NoError(t, err)
	}

	// Get updated stats
	stats2, err := store.GetStats(ctx, loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL)
	require.NoError(t, err)
	assert.Equal(t, int64(5), stats2.ReadCount)

	// Delete 2 keys
	for i := 0; i < 2; i++ {
		_, err := store.Delete(ctx, &loomv1.DeleteSharedMemoryRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
			Key:       fmt.Sprintf("stats.key%d", i),
			AgentId:   "agent1",
		})
		require.NoError(t, err)
	}

	// Get updated stats
	stats3, err := store.GetStats(ctx, loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL)
	require.NoError(t, err)
	assert.Equal(t, int64(3), stats3.KeyCount)
}

func TestSharedMemoryConcurrentPut(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Put 100 keys concurrently from 10 goroutines
	const numGoroutines = 10
	const keysPerGoroutine = 10
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines*keysPerGoroutine)

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < keysPerGoroutine; i++ {
				_, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
					Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
					Key:       fmt.Sprintf("concurrent.g%d.key%d", goroutineID, i),
					Value:     []byte(fmt.Sprintf("value-%d-%d", goroutineID, i)),
					AgentId:   fmt.Sprintf("agent%d", goroutineID),
				})
				if err != nil {
					errors <- err
				}
			}
		}(g)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Put error: %v", err)
	}

	// Verify all keys were written
	stats, err := store.GetStats(ctx, loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL)
	require.NoError(t, err)
	assert.Equal(t, int64(numGoroutines*keysPerGoroutine), stats.KeyCount)
}

func TestSharedMemoryConcurrentVersionConflicts(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Create initial value
	putResp, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "contested.key",
		Value:     []byte("initial value"),
		AgentId:   "agent0",
	})
	require.NoError(t, err)

	// 10 goroutines try to update with expected_version=1 (only one should succeed)
	const numGoroutines = 10
	var wg sync.WaitGroup
	successCount := make(chan bool, numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			_, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
				Namespace:       loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
				Key:             "contested.key",
				Value:           []byte(fmt.Sprintf("value from agent%d", goroutineID)),
				ExpectedVersion: putResp.Version,
				AgentId:         fmt.Sprintf("agent%d", goroutineID),
			})
			successCount <- (err == nil)
		}(g)
	}

	wg.Wait()
	close(successCount)

	// Count successes
	successes := 0
	for success := range successCount {
		if success {
			successes++
		}
	}

	// Only one should have succeeded
	assert.Equal(t, 1, successes, "exactly one concurrent update should succeed")

	// Final version should be 2
	getResp, err := store.Get(ctx, &loomv1.GetSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "contested.key",
		AgentId:   "agent0",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), getResp.Value.Version)
}

func TestSharedMemoryClose(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)

	ctx := context.Background()

	// Create watcher
	watchChan, err := store.Watch(ctx, &loomv1.WatchSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		AgentId:   "agent1",
	})
	require.NoError(t, err)

	// Close store
	err = store.Close()
	require.NoError(t, err)

	// Watch channel should be closed
	select {
	case _, ok := <-watchChan:
		assert.False(t, ok, "watch channel should be closed")
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for channel close")
	}

	// Operations after close should error
	_, err = store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "test",
		Value:     []byte("test"),
		AgentId:   "agent1",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestSharedMemoryValidation(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Unspecified namespace
	_, err = store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_UNSPECIFIED,
		Key:       "test",
		Value:     []byte("test"),
		AgentId:   "agent1",
	})
	assert.Error(t, err)

	// Empty key
	_, err = store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "",
		Value:     []byte("test"),
		AgentId:   "agent1",
	})
	assert.Error(t, err)

	// Empty agent ID
	_, err = store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "test",
		Value:     []byte("test"),
		AgentId:   "",
	})
	assert.Error(t, err)
}

// TestAgentNamespaceIsolation tests that agents cannot access each other's private data
func TestAgentNamespaceIsolation(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Agent 1 writes to AGENT namespace
	putResp1, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		Key:       "character_sheet",
		Value:     []byte("Agent 1: Eldrin the Elf"),
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), putResp1.Version)
	assert.True(t, putResp1.Created)

	// Agent 2 writes to AGENT namespace with same key
	putResp2, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		Key:       "character_sheet",
		Value:     []byte("Agent 2: Luna the Human"),
		AgentId:   "agent2",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), putResp2.Version) // Version 1 because it's a different scoped key
	assert.True(t, putResp2.Created)

	// Agent 1 reads its own data
	getResp1, err := store.Get(ctx, &loomv1.GetSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		Key:       "character_sheet",
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.True(t, getResp1.Found)
	assert.Equal(t, []byte("Agent 1: Eldrin the Elf"), getResp1.Value.Value)

	// Agent 2 reads its own data
	getResp2, err := store.Get(ctx, &loomv1.GetSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		Key:       "character_sheet",
		AgentId:   "agent2",
	})
	require.NoError(t, err)
	assert.True(t, getResp2.Found)
	assert.Equal(t, []byte("Agent 2: Luna the Human"), getResp2.Value.Value)

	// Agent 3 tries to read the same key - should not find anything
	getResp3, err := store.Get(ctx, &loomv1.GetSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		Key:       "character_sheet",
		AgentId:   "agent3",
	})
	require.NoError(t, err)
	assert.False(t, getResp3.Found, "Agent 3 should not find data from Agent 1 or 2")
}

// TestAgentNamespaceList tests that List only returns keys for the requesting agent
func TestAgentNamespaceList(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Agent 1 writes multiple keys
	for i := 1; i <= 3; i++ {
		_, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
			Key:       fmt.Sprintf("key%d", i),
			Value:     []byte(fmt.Sprintf("agent1-value%d", i)),
			AgentId:   "agent1",
		})
		require.NoError(t, err)
	}

	// Agent 2 writes multiple keys (same key names)
	for i := 1; i <= 3; i++ {
		_, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
			Key:       fmt.Sprintf("key%d", i),
			Value:     []byte(fmt.Sprintf("agent2-value%d", i)),
			AgentId:   "agent2",
		})
		require.NoError(t, err)
	}

	// Agent 1 lists keys - should only see its own
	listResp1, err := store.List(ctx, &loomv1.ListSharedMemoryKeysRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), listResp1.TotalCount)
	assert.ElementsMatch(t, []string{"key1", "key2", "key3"}, listResp1.Keys)

	// Agent 2 lists keys - should only see its own
	listResp2, err := store.List(ctx, &loomv1.ListSharedMemoryKeysRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		AgentId:   "agent2",
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), listResp2.TotalCount)
	assert.ElementsMatch(t, []string{"key1", "key2", "key3"}, listResp2.Keys)

	// Agent 3 lists keys - should see nothing
	listResp3, err := store.List(ctx, &loomv1.ListSharedMemoryKeysRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		AgentId:   "agent3",
	})
	require.NoError(t, err)
	assert.Equal(t, int32(0), listResp3.TotalCount)
	assert.Empty(t, listResp3.Keys)
}

// TestAgentNamespaceDelete tests that Delete only affects the requesting agent's data
func TestAgentNamespaceDelete(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Agent 1 and Agent 2 both write to same key
	_, err = store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		Key:       "shared_key_name",
		Value:     []byte("Agent 1 data"),
		AgentId:   "agent1",
	})
	require.NoError(t, err)

	_, err = store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		Key:       "shared_key_name",
		Value:     []byte("Agent 2 data"),
		AgentId:   "agent2",
	})
	require.NoError(t, err)

	// Agent 1 deletes its key
	delResp1, err := store.Delete(ctx, &loomv1.DeleteSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		Key:       "shared_key_name",
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.True(t, delResp1.Deleted)

	// Agent 1 can no longer read its key
	getResp1, err := store.Get(ctx, &loomv1.GetSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		Key:       "shared_key_name",
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.False(t, getResp1.Found)

	// Agent 2 can still read its own key (not deleted)
	getResp2, err := store.Get(ctx, &loomv1.GetSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		Key:       "shared_key_name",
		AgentId:   "agent2",
	})
	require.NoError(t, err)
	assert.True(t, getResp2.Found)
	assert.Equal(t, []byte("Agent 2 data"), getResp2.Value.Value)
}

// TestAgentNamespaceWatch tests that Watch only notifies for the requesting agent's updates
func TestAgentNamespaceWatch(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Agent 1 starts watching
	watchChan1, err := store.Watch(ctx, &loomv1.WatchSharedMemoryRequest{
		Namespace:      loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		KeyPattern:     "",
		AgentId:        "agent1",
		IncludeInitial: false,
	})
	require.NoError(t, err)

	// Agent 2 starts watching
	watchChan2, err := store.Watch(ctx, &loomv1.WatchSharedMemoryRequest{
		Namespace:      loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		KeyPattern:     "",
		AgentId:        "agent2",
		IncludeInitial: false,
	})
	require.NoError(t, err)

	// Agent 1 writes data
	_, err = store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		Key:       "watched_key",
		Value:     []byte("Agent 1 update"),
		AgentId:   "agent1",
	})
	require.NoError(t, err)

	// Agent 2 writes data (same key name, different scope)
	_, err = store.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
		Key:       "watched_key",
		Value:     []byte("Agent 2 update"),
		AgentId:   "agent2",
	})
	require.NoError(t, err)

	// Agent 1 should receive only its own update
	select {
	case value := <-watchChan1:
		assert.Equal(t, []byte("Agent 1 update"), value.Value)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Agent 1 did not receive watch notification")
	}

	// Agent 2 should receive only its own update
	select {
	case value := <-watchChan2:
		assert.Equal(t, []byte("Agent 2 update"), value.Value)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Agent 2 did not receive watch notification")
	}

	// Neither agent should receive additional notifications
	select {
	case <-watchChan1:
		t.Fatal("Agent 1 received unexpected notification")
	case <-time.After(50 * time.Millisecond):
		// Expected - no more notifications
	}

	select {
	case <-watchChan2:
		t.Fatal("Agent 2 received unexpected notification")
	case <-time.After(50 * time.Millisecond):
		// Expected - no more notifications
	}
}

// TestAgentNamespaceConcurrentAccess tests that concurrent access from different agents is safe
func TestAgentNamespaceConcurrentAccess(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	numAgents := 10
	numOperations := 100

	var wg sync.WaitGroup
	wg.Add(numAgents)

	for agentNum := 0; agentNum < numAgents; agentNum++ {
		go func(agentID string) {
			defer wg.Done()

			for i := 0; i < numOperations; i++ {
				// Put
				_, err := store.Put(ctx, &loomv1.PutSharedMemoryRequest{
					Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
					Key:       fmt.Sprintf("key%d", i%10), // Reuse keys
					Value:     []byte(fmt.Sprintf("%s-value%d", agentID, i)),
					AgentId:   agentID,
				})
				require.NoError(t, err)

				// Get
				getResp, err := store.Get(ctx, &loomv1.GetSharedMemoryRequest{
					Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
					Key:       fmt.Sprintf("key%d", i%10),
					AgentId:   agentID,
				})
				require.NoError(t, err)
				assert.True(t, getResp.Found)
				// Verify we got our own data (not another agent's)
				assert.Contains(t, string(getResp.Value.Value), agentID)
			}
		}(fmt.Sprintf("agent%d", agentNum))
	}

	wg.Wait()

	// Verify each agent has its own isolated data
	for agentNum := 0; agentNum < numAgents; agentNum++ {
		agentID := fmt.Sprintf("agent%d", agentNum)
		listResp, err := store.List(ctx, &loomv1.ListSharedMemoryKeysRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
			AgentId:   agentID,
		})
		require.NoError(t, err)
		assert.Equal(t, int32(10), listResp.TotalCount, "Agent %s should have 10 keys", agentID)
	}
}

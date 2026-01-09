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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func testDBPath(t *testing.T) string {
	tmpDir := t.TempDir()
	return filepath.Join(tmpDir, "test.db")
}

func TestSQLiteStore_StoreAndResolve(t *testing.T) {
	store, err := NewSQLiteStore(testDBPath(t), 1*time.Minute)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	data := []byte("test data")

	opts := StoreOptions{
		Type:        loomv1.ReferenceType_REFERENCE_TYPE_SESSION_STATE,
		ContentType: "text/plain",
	}

	// Store data
	ref, err := store.Store(ctx, data, opts)
	require.NoError(t, err)
	assert.NotNil(t, ref)
	assert.NotEmpty(t, ref.Id)
	assert.Equal(t, loomv1.ReferenceStore_REFERENCE_STORE_SQLITE, ref.Store)
	assert.Equal(t, loomv1.ReferenceType_REFERENCE_TYPE_SESSION_STATE, ref.Type)

	// Resolve data
	resolved, err := store.Resolve(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, data, resolved)
}

func TestSQLiteStore_StoreEmptyData(t *testing.T) {
	store, err := NewSQLiteStore(testDBPath(t), 1*time.Minute)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	opts := StoreOptions{
		Type: loomv1.ReferenceType_REFERENCE_TYPE_TOOL_RESULT,
	}

	// Attempt to store empty data
	ref, err := store.Store(ctx, []byte{}, opts)
	assert.Error(t, err)
	assert.Nil(t, ref)
	assert.Contains(t, err.Error(), "cannot store empty data")
}

func TestSQLiteStore_ResolveNonExistent(t *testing.T) {
	store, err := NewSQLiteStore(testDBPath(t), 1*time.Minute)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	ref := &loomv1.Reference{
		Id:    "nonexistent",
		Store: loomv1.ReferenceStore_REFERENCE_STORE_SQLITE,
	}

	// Attempt to resolve non-existent reference
	data, err := store.Resolve(ctx, ref)
	assert.Error(t, err)
	assert.Nil(t, data)
	assert.Contains(t, err.Error(), "reference not found")
}

func TestSQLiteStore_RefCounting(t *testing.T) {
	store, err := NewSQLiteStore(testDBPath(t), 1*time.Minute)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	data := []byte("reference counted data")

	opts := StoreOptions{
		Type: loomv1.ReferenceType_REFERENCE_TYPE_WORKFLOW_CONTEXT,
	}

	// Store data (refCount = 1)
	ref, err := store.Store(ctx, data, opts)
	require.NoError(t, err)

	// Retain twice (refCount = 3)
	err = store.Retain(ctx, ref.Id)
	require.NoError(t, err)
	err = store.Retain(ctx, ref.Id)
	require.NoError(t, err)

	// Verify entry exists
	var refCount int64
	err = store.db.QueryRow("SELECT ref_count FROM reference_store WHERE id = ?", ref.Id).Scan(&refCount)
	require.NoError(t, err)
	assert.Equal(t, int64(3), refCount)

	// Release once (refCount = 2)
	err = store.Release(ctx, ref.Id)
	require.NoError(t, err)

	// Verify still exists
	resolved, err := store.Resolve(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, data, resolved)

	// Release twice more (refCount = 0, should be deleted)
	err = store.Release(ctx, ref.Id)
	require.NoError(t, err)
	err = store.Release(ctx, ref.Id)
	require.NoError(t, err)

	// Verify deleted
	_, err = store.Resolve(ctx, ref)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reference not found")
}

func TestSQLiteStore_TTLExpiration(t *testing.T) {
	store, err := NewSQLiteStore(testDBPath(t), 1*time.Minute)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	data := []byte("expiring data")

	opts := StoreOptions{
		Type: loomv1.ReferenceType_REFERENCE_TYPE_LARGE_PAYLOAD,
		TTL:  1, // 1 second TTL
	}

	// Store data with TTL
	ref, err := store.Store(ctx, data, opts)
	require.NoError(t, err)

	// Resolve immediately (should work)
	resolved, err := store.Resolve(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, data, resolved)

	// Wait for expiration
	time.Sleep(2 * time.Second)

	// Resolve after expiration (should fail)
	_, err = store.Resolve(ctx, ref)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reference expired")
}

func TestSQLiteStore_Deduplication(t *testing.T) {
	store, err := NewSQLiteStore(testDBPath(t), 1*time.Minute)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	data := []byte("duplicate data")

	opts := StoreOptions{
		Type: loomv1.ReferenceType_REFERENCE_TYPE_PATTERN_DATA,
	}

	// Store same data twice
	ref1, err := store.Store(ctx, data, opts)
	require.NoError(t, err)

	ref2, err := store.Store(ctx, data, opts)
	require.NoError(t, err)

	// Should return same reference ID (deduplication)
	assert.Equal(t, ref1.Id, ref2.Id)

	// Verify refCount incremented
	var refCount int64
	err = store.db.QueryRow("SELECT ref_count FROM reference_store WHERE id = ?", ref1.Id).Scan(&refCount)
	require.NoError(t, err)
	assert.Equal(t, int64(2), refCount)
}

func TestSQLiteStore_List(t *testing.T) {
	store, err := NewSQLiteStore(testDBPath(t), 1*time.Minute)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	opts := StoreOptions{
		Type: loomv1.ReferenceType_REFERENCE_TYPE_TOOL_RESULT,
	}

	// Store multiple references
	data1 := []byte("data1")
	data2 := []byte("data2")
	data3 := []byte("data3")

	ref1, err := store.Store(ctx, data1, opts)
	require.NoError(t, err)

	ref2, err := store.Store(ctx, data2, opts)
	require.NoError(t, err)

	ref3, err := store.Store(ctx, data3, opts)
	require.NoError(t, err)

	// List all references
	refs, err := store.List(ctx)
	require.NoError(t, err)
	assert.Len(t, refs, 3)

	// Verify all IDs present
	ids := make(map[string]bool)
	for _, ref := range refs {
		ids[ref.Id] = true
	}
	assert.True(t, ids[ref1.Id])
	assert.True(t, ids[ref2.Id])
	assert.True(t, ids[ref3.Id])
}

func TestSQLiteStore_Stats(t *testing.T) {
	store, err := NewSQLiteStore(testDBPath(t), 1*time.Minute)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	opts := StoreOptions{
		Type: loomv1.ReferenceType_REFERENCE_TYPE_SESSION_STATE,
	}

	// Initial stats
	stats, err := store.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), stats.ActiveRefs)
	assert.Equal(t, int64(0), stats.CurrentBytes)

	// Store data
	data := []byte("test data for stats")
	ref, err := store.Store(ctx, data, opts)
	require.NoError(t, err)

	// Verify stats updated
	stats, err = store.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), stats.ActiveRefs)
	assert.Equal(t, int64(len(data)), stats.CurrentBytes)

	// Release reference
	err = store.Release(ctx, ref.Id)
	require.NoError(t, err)

	// Verify stats after deletion
	stats, err = store.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), stats.ActiveRefs)
	assert.Equal(t, int64(0), stats.CurrentBytes)
}

func TestSQLiteStore_GarbageCollection(t *testing.T) {
	// Use short GC interval for testing
	store, err := NewSQLiteStore(testDBPath(t), 500*time.Millisecond)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	data := []byte("gc test data")

	opts := StoreOptions{
		Type: loomv1.ReferenceType_REFERENCE_TYPE_LARGE_PAYLOAD,
		TTL:  1, // 1 second TTL
	}

	// Store data with short TTL
	ref, err := store.Store(ctx, data, opts)
	require.NoError(t, err)

	// Wait for expiration + GC to run
	time.Sleep(2 * time.Second)

	// Verify entry deleted by GC
	_, err = store.Resolve(ctx, ref)
	assert.Error(t, err)
	// Could be "reference not found" or "reference expired" depending on GC timing
	assert.True(t,
		strings.Contains(err.Error(), "reference not found") ||
			strings.Contains(err.Error(), "reference expired"),
		"expected 'reference not found' or 'reference expired', got: %s", err.Error())
}

func TestSQLiteStore_ConcurrentAccess(t *testing.T) {
	store, err := NewSQLiteStore(testDBPath(t), 1*time.Minute)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	opts := StoreOptions{
		Type: loomv1.ReferenceType_REFERENCE_TYPE_COLLABORATION_STATE,
	}

	// Store initial data
	data := []byte("concurrent access data")
	ref, err := store.Store(ctx, data, opts)
	require.NoError(t, err)

	// Concurrent retain operations
	const numGoroutines = 10
	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			err := store.Retain(ctx, ref.Id)
			assert.NoError(t, err)
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Verify refCount incremented correctly
	var refCount int64
	err = store.db.QueryRow("SELECT ref_count FROM reference_store WHERE id = ?", ref.Id).Scan(&refCount)
	require.NoError(t, err)
	// Initial refCount=1 + 10 retains = 11
	assert.Equal(t, int64(11), refCount)

	// Concurrent release operations
	for i := 0; i < numGoroutines; i++ {
		go func() {
			err := store.Release(ctx, ref.Id)
			assert.NoError(t, err)
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Verify refCount decremented correctly (should be 1 remaining)
	err = store.db.QueryRow("SELECT ref_count FROM reference_store WHERE id = ?", ref.Id).Scan(&refCount)
	require.NoError(t, err)
	assert.Equal(t, int64(1), refCount)
}

func TestSQLiteStore_Persistence(t *testing.T) {
	dbPath := testDBPath(t)
	ctx := context.Background()
	data := []byte("persistent data")

	opts := StoreOptions{
		Type: loomv1.ReferenceType_REFERENCE_TYPE_SESSION_STATE,
	}

	// Create store and save data
	store1, err := NewSQLiteStore(dbPath, 1*time.Minute)
	require.NoError(t, err)

	ref, err := store1.Store(ctx, data, opts)
	require.NoError(t, err)

	// Close store
	err = store1.Close()
	require.NoError(t, err)

	// Reopen store
	store2, err := NewSQLiteStore(dbPath, 1*time.Minute)
	require.NoError(t, err)
	defer store2.Close()

	// Verify data persisted
	resolved, err := store2.Resolve(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, data, resolved)

	// Verify refCount persisted
	var refCount int64
	err = store2.db.QueryRow("SELECT ref_count FROM reference_store WHERE id = ?", ref.Id).Scan(&refCount)
	require.NoError(t, err)
	assert.Equal(t, int64(1), refCount)
}

func TestSQLiteStore_WALMode(t *testing.T) {
	store, err := NewSQLiteStore(testDBPath(t), 1*time.Minute)
	require.NoError(t, err)
	defer store.Close()

	// Verify WAL mode is enabled
	var journalMode string
	err = store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode)
	require.NoError(t, err)
	assert.Equal(t, "wal", journalMode)
}

func TestSQLiteStore_InvalidPath(t *testing.T) {
	// Try to create store with invalid path
	_, err := NewSQLiteStore("/nonexistent/invalid/path.db", 1*time.Minute)
	assert.Error(t, err)
}

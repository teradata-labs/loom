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

// AC1: a Get of a shared-memory handle owned by another session resolves to
// not-found for the caller, and leaks none of the owner's data (fail-closed).
func TestSharedMemoryStore_ForeignSessionGetIsNotFound(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	data := []byte("owner-only payload")
	ref, err := store.Store("handle-1", data, "text/plain", nil, "session-owner")
	require.NoError(t, err)

	// A caller from a different session cannot resolve the owner's handle.
	retrieved, err := store.Get(ref, "session-intruder")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.Nil(t, retrieved, "foreign caller must receive no data")
}

// The describe path obeys the same partition rule as the data path: metadata
// carries schema, a sample item and a content preview, so a foreign caller must
// get not-found there too — and the owner must still be able to describe.
func TestSharedMemoryStore_ForeignSessionGetMetadataIsNotFound(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	ref, err := store.Store("handle-1", []byte(`[{"secret":"owner-only payload"}]`),
		"application/json", nil, "session-owner")
	require.NoError(t, err)

	meta, err := store.GetMetadata(ref, "session-intruder")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.Nil(t, meta, "foreign caller must receive no schema, sample or preview")

	meta, err = store.GetMetadata(ref, "session-owner")
	require.NoError(t, err)
	require.NotNil(t, meta, "the owning session still describes its own handle")
}

// AC4 (shared memory): the owning session resolves its own handle.
func TestSharedMemoryStore_OwningSessionResolvesHandle(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	data := []byte("owner-only payload")
	ref, err := store.Store("handle-1", data, "text/plain", nil, "session-owner")
	require.NoError(t, err)

	retrieved, err := store.Get(ref, "session-owner")
	require.NoError(t, err)
	assert.Equal(t, data, retrieved)
}

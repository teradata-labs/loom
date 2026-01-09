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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// MemoryStore implements ReferenceStore with in-memory storage and reference counting GC
type MemoryStore struct {
	mu sync.RWMutex

	// data stores the actual reference data
	data map[string]*memoryEntry

	// stats tracks store metrics
	stats StoreStats

	// gcInterval controls how often GC runs
	gcInterval time.Duration

	// stopGC signals GC goroutine to stop
	stopGC chan struct{}

	// gcDone signals when GC goroutine has exited
	gcDone chan struct{}
}

// memoryEntry represents a stored reference with metadata
type memoryEntry struct {
	// ref is the proto reference
	ref *loomv1.Reference

	// data is the actual stored bytes
	data []byte

	// refCount tracks how many agents hold this reference
	refCount int64

	// createdAt is the timestamp when entry was created
	createdAt time.Time

	// expiresAt is the timestamp when entry expires (zero = never)
	expiresAt time.Time
}

// NewMemoryStore creates a new in-memory reference store with GC
func NewMemoryStore(gcInterval time.Duration) *MemoryStore {
	if gcInterval == 0 {
		gcInterval = 5 * time.Minute // Default 5 minute GC interval
	}

	store := &MemoryStore{
		data:       make(map[string]*memoryEntry),
		gcInterval: gcInterval,
		stopGC:     make(chan struct{}),
		gcDone:     make(chan struct{}),
	}

	// Start garbage collection goroutine
	go store.gcLoop()

	return store
}

// Store implements ReferenceStore.Store
func (m *MemoryStore) Store(ctx context.Context, data []byte, opts StoreOptions) (*loomv1.Reference, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("cannot store empty data")
	}

	// Generate reference ID from data hash
	hash := sha256.Sum256(data)
	refID := hex.EncodeToString(hash[:])

	now := time.Now()
	expiresAt := time.Time{}
	if opts.TTL > 0 {
		expiresAt = now.Add(time.Duration(opts.TTL) * time.Second)
	}

	// Create reference
	ref := &loomv1.Reference{
		Id:        refID,
		Type:      opts.Type,
		Store:     loomv1.ReferenceStore_REFERENCE_STORE_MEMORY,
		CreatedAt: now.Unix(),
		ExpiresAt: expiresAt.Unix(),
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if reference already exists
	if entry, exists := m.data[refID]; exists {
		// Increment existing reference count
		entry.refCount++
		return entry.ref, nil
	}

	// Store new entry with refCount=1
	m.data[refID] = &memoryEntry{
		ref:       ref,
		data:      data,
		refCount:  1,
		createdAt: now,
		expiresAt: expiresAt,
	}

	// Update stats
	m.stats.TotalRefs++
	m.stats.TotalBytes += int64(len(data))
	m.stats.ActiveRefs++
	m.stats.CurrentBytes += int64(len(data))

	return ref, nil
}

// Resolve implements ReferenceStore.Resolve
func (m *MemoryStore) Resolve(ctx context.Context, ref *loomv1.Reference) ([]byte, error) {
	if ref == nil {
		return nil, fmt.Errorf("nil reference")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, exists := m.data[ref.Id]
	if !exists {
		return nil, fmt.Errorf("reference not found: %s", ref.Id)
	}

	// Check expiration
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		return nil, fmt.Errorf("reference expired: %s", ref.Id)
	}

	// Return copy of data to prevent mutation
	dataCopy := make([]byte, len(entry.data))
	copy(dataCopy, entry.data)

	return dataCopy, nil
}

// Retain implements ReferenceStore.Retain
func (m *MemoryStore) Retain(ctx context.Context, refID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, exists := m.data[refID]
	if !exists {
		return fmt.Errorf("reference not found: %s", refID)
	}

	entry.refCount++
	return nil
}

// Release implements ReferenceStore.Release
func (m *MemoryStore) Release(ctx context.Context, refID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, exists := m.data[refID]
	if !exists {
		return fmt.Errorf("reference not found: %s", refID)
	}

	entry.refCount--

	// Trigger immediate cleanup if refCount reaches 0
	if entry.refCount <= 0 {
		m.evictEntry(refID, entry)
	}

	return nil
}

// List implements ReferenceStore.List
func (m *MemoryStore) List(ctx context.Context) ([]*loomv1.Reference, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	refs := make([]*loomv1.Reference, 0, len(m.data))
	for _, entry := range m.data {
		refs = append(refs, entry.ref)
	}

	return refs, nil
}

// Stats implements ReferenceStore.Stats
func (m *MemoryStore) Stats(ctx context.Context) (*StoreStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Return copy of stats
	statsCopy := m.stats
	return &statsCopy, nil
}

// Close implements ReferenceStore.Close
func (m *MemoryStore) Close() error {
	// Signal GC goroutine to stop
	close(m.stopGC)

	// Wait for GC goroutine to exit
	<-m.gcDone

	m.mu.Lock()
	defer m.mu.Unlock()

	// Clear all data
	m.data = make(map[string]*memoryEntry)
	m.stats.ActiveRefs = 0
	m.stats.CurrentBytes = 0

	return nil
}

// gcLoop runs garbage collection periodically
func (m *MemoryStore) gcLoop() {
	defer close(m.gcDone)

	ticker := time.NewTicker(m.gcInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.runGC()
		case <-m.stopGC:
			return
		}
	}
}

// runGC performs garbage collection on expired and zero-refcount entries
func (m *MemoryStore) runGC() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	toEvict := make([]string, 0)

	// Find entries to evict
	for refID, entry := range m.data {
		// Evict if expired
		if !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
			toEvict = append(toEvict, refID)
			continue
		}

		// Evict if refCount is 0 (already released by all agents)
		if entry.refCount <= 0 {
			toEvict = append(toEvict, refID)
		}
	}

	// Perform eviction
	for _, refID := range toEvict {
		if entry, exists := m.data[refID]; exists {
			m.evictEntry(refID, entry)
		}
	}

	m.stats.GCRuns++
}

// evictEntry removes an entry from storage (caller must hold write lock)
func (m *MemoryStore) evictEntry(refID string, entry *memoryEntry) {
	delete(m.data, refID)
	m.stats.ActiveRefs--
	m.stats.CurrentBytes -= int64(len(entry.data))
	m.stats.EvictionCount++
}

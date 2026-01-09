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
	"sync"
)

// SessionReferenceTracker tracks which references belong to which sessions
// for automatic cleanup when sessions end. Thread-safe for concurrent access.
//
// Design principles:
// - Lightweight: In-memory tracking only (ephemeral tool results don't need persistence)
// - Idempotent: Safe to call Pin/Unpin multiple times
// - Defensive: Nil/empty inputs are no-ops (prevents crashes)
// - Observable: Provides stats for monitoring
//
// Usage:
//
//	tracker := NewSessionReferenceTracker(sharedMemory)
//	// When storing tool result:
//	tracker.PinForSession(sessionID, refID)
//	// When session ends:
//	tracker.UnpinSession(sessionID)  // Releases all refs for session
type SessionReferenceTracker struct {
	mu           sync.RWMutex
	sessionRefs  map[string][]string // sessionID → []refID
	sharedMemory *SharedMemoryStore
}

// NewSessionReferenceTracker creates a new reference tracker.
// The sharedMemory store is used to call Release() on references during cleanup.
func NewSessionReferenceTracker(sharedMemory *SharedMemoryStore) *SessionReferenceTracker {
	return &SessionReferenceTracker{
		sessionRefs:  make(map[string][]string),
		sharedMemory: sharedMemory,
	}
}

// PinForSession tracks a reference for a session (idempotent).
// If the reference is already pinned for this session, this is a no-op.
// Empty sessionID or refID are silently ignored (defensive).
//
// Thread-safe: Can be called concurrently from multiple goroutines.
func (t *SessionReferenceTracker) PinForSession(sessionID, refID string) {
	// Defensive: ignore empty inputs
	if sessionID == "" || refID == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Check if already pinned (prevent duplicates)
	refs := t.sessionRefs[sessionID]
	for _, existing := range refs {
		if existing == refID {
			return // Already tracked, nothing to do
		}
	}

	// Add to session's reference list
	t.sessionRefs[sessionID] = append(refs, refID)
}

// UnpinSession releases all references for a session (idempotent).
// Returns the number of references released.
// If session has no references or doesn't exist, returns 0.
//
// This method calls Release() on the SharedMemoryStore for each reference,
// decrementing the RefCount. When RefCount reaches 0, the reference becomes
// eligible for LRU eviction or TTL-based cleanup.
//
// Thread-safe: Can be called concurrently from multiple goroutines.
func (t *SessionReferenceTracker) UnpinSession(sessionID string) int {
	// Defensive: ignore empty input
	if sessionID == "" {
		return 0
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	refs, exists := t.sessionRefs[sessionID]
	if !exists || len(refs) == 0 {
		return 0
	}

	// Release all references atomically
	// Note: SharedMemoryStore.Release() is idempotent and handles nil/missing refs
	for _, refID := range refs {
		t.sharedMemory.Release(refID)
	}

	releasedCount := len(refs)
	delete(t.sessionRefs, sessionID)
	return releasedCount
}

// GetSessionReferences returns a copy of all references for a session.
// Returns empty slice if session has no references or doesn't exist.
// Used primarily for testing and debugging.
//
// Thread-safe: Can be called concurrently from multiple goroutines.
func (t *SessionReferenceTracker) GetSessionReferences(sessionID string) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	refs, exists := t.sessionRefs[sessionID]
	if !exists {
		return []string{}
	}

	// Return copy to prevent external modification
	result := make([]string, len(refs))
	copy(result, refs)
	return result
}

// Stats returns tracker statistics for monitoring and observability.
// Provides insight into reference lifecycle and helps detect leaks.
//
// Thread-safe: Can be called concurrently from multiple goroutines.
func (t *SessionReferenceTracker) Stats() TrackerStats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	totalRefs := 0
	for _, refs := range t.sessionRefs {
		totalRefs += len(refs)
	}

	return TrackerStats{
		SessionCount: len(t.sessionRefs),
		TotalRefs:    totalRefs,
	}
}

// TrackerStats holds reference tracker statistics.
type TrackerStats struct {
	// SessionCount is the number of sessions with pinned references
	SessionCount int

	// TotalRefs is the total number of references across all sessions
	TotalRefs int
}

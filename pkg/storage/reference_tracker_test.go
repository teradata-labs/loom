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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionReferenceTracker_PinAndUnpin tests basic pin/unpin flow
func TestSessionReferenceTracker_PinAndUnpin(t *testing.T) {
	sharedMem := setupTestSharedMemory(t)
	tracker := NewSessionReferenceTracker(sharedMem)

	sessionID := "sess_test_001"
	refID := "ref_tool_result_123"

	// Initially no references
	assert.Empty(t, tracker.GetSessionReferences(sessionID))

	// Pin reference
	tracker.PinForSession(sessionID, refID)

	// Verify pinned
	refs := tracker.GetSessionReferences(sessionID)
	assert.Len(t, refs, 1)
	assert.Equal(t, refID, refs[0])

	// Check stats
	stats := tracker.Stats()
	assert.Equal(t, 1, stats.SessionCount)
	assert.Equal(t, 1, stats.TotalRefs)

	// Unpin session
	count := tracker.UnpinSession(sessionID)
	assert.Equal(t, 1, count)

	// Verify unpinned
	assert.Empty(t, tracker.GetSessionReferences(sessionID))

	// Stats should be zero
	stats = tracker.Stats()
	assert.Equal(t, 0, stats.SessionCount)
	assert.Equal(t, 0, stats.TotalRefs)
}

// TestSessionReferenceTracker_MultipleReferences tests multiple refs per session
func TestSessionReferenceTracker_MultipleReferences(t *testing.T) {
	sharedMem := setupTestSharedMemory(t)
	tracker := NewSessionReferenceTracker(sharedMem)

	sessionID := "sess_test_002"
	refIDs := []string{
		"ref_result_1",
		"ref_result_2",
		"ref_result_3",
	}

	// Pin multiple references
	for _, refID := range refIDs {
		tracker.PinForSession(sessionID, refID)
	}

	// Verify all pinned
	refs := tracker.GetSessionReferences(sessionID)
	assert.Len(t, refs, 3)
	assert.ElementsMatch(t, refIDs, refs)

	// Stats
	stats := tracker.Stats()
	assert.Equal(t, 1, stats.SessionCount)
	assert.Equal(t, 3, stats.TotalRefs)

	// Unpin all at once
	count := tracker.UnpinSession(sessionID)
	assert.Equal(t, 3, count)

	// All unpinned
	assert.Empty(t, tracker.GetSessionReferences(sessionID))
}

// TestSessionReferenceTracker_MultipleSessions tests session isolation
func TestSessionReferenceTracker_MultipleSessions(t *testing.T) {
	sharedMem := setupTestSharedMemory(t)
	tracker := NewSessionReferenceTracker(sharedMem)

	sess1 := "sess_001"
	sess2 := "sess_002"
	sess3 := "sess_003"

	// Each session pins different refs
	tracker.PinForSession(sess1, "ref_1a")
	tracker.PinForSession(sess1, "ref_1b")

	tracker.PinForSession(sess2, "ref_2a")

	tracker.PinForSession(sess3, "ref_3a")
	tracker.PinForSession(sess3, "ref_3b")
	tracker.PinForSession(sess3, "ref_3c")

	// Verify session isolation
	assert.Len(t, tracker.GetSessionReferences(sess1), 2)
	assert.Len(t, tracker.GetSessionReferences(sess2), 1)
	assert.Len(t, tracker.GetSessionReferences(sess3), 3)

	// Stats
	stats := tracker.Stats()
	assert.Equal(t, 3, stats.SessionCount)
	assert.Equal(t, 6, stats.TotalRefs)

	// Unpin sess2 - others unaffected
	count := tracker.UnpinSession(sess2)
	assert.Equal(t, 1, count)

	assert.Len(t, tracker.GetSessionReferences(sess1), 2) // Still 2
	assert.Empty(t, tracker.GetSessionReferences(sess2))  // Cleared
	assert.Len(t, tracker.GetSessionReferences(sess3), 3) // Still 3

	// Stats updated
	stats = tracker.Stats()
	assert.Equal(t, 2, stats.SessionCount) // sess2 removed
	assert.Equal(t, 5, stats.TotalRefs)
}

// TestSessionReferenceTracker_Idempotency tests that operations are idempotent
func TestSessionReferenceTracker_Idempotency(t *testing.T) {
	sharedMem := setupTestSharedMemory(t)
	tracker := NewSessionReferenceTracker(sharedMem)

	sessionID := "sess_test_idem"
	refID := "ref_idem_123"

	// Pin same ref multiple times
	tracker.PinForSession(sessionID, refID)
	tracker.PinForSession(sessionID, refID)
	tracker.PinForSession(sessionID, refID)

	// Should only be tracked once
	refs := tracker.GetSessionReferences(sessionID)
	assert.Len(t, refs, 1)

	// Unpin multiple times
	count1 := tracker.UnpinSession(sessionID)
	assert.Equal(t, 1, count1)

	count2 := tracker.UnpinSession(sessionID)
	assert.Equal(t, 0, count2) // No refs to unpin

	count3 := tracker.UnpinSession(sessionID)
	assert.Equal(t, 0, count3)
}

// TestSessionReferenceTracker_EmptyInputs tests defensive nil/empty handling
func TestSessionReferenceTracker_EmptyInputs(t *testing.T) {
	sharedMem := setupTestSharedMemory(t)
	tracker := NewSessionReferenceTracker(sharedMem)

	// Empty sessionID
	tracker.PinForSession("", "ref_123")
	stats := tracker.Stats()
	assert.Equal(t, 0, stats.TotalRefs)

	// Empty refID
	tracker.PinForSession("sess_001", "")
	stats = tracker.Stats()
	assert.Equal(t, 0, stats.TotalRefs)

	// Both empty
	tracker.PinForSession("", "")
	stats = tracker.Stats()
	assert.Equal(t, 0, stats.TotalRefs)

	// Unpin empty sessionID
	count := tracker.UnpinSession("")
	assert.Equal(t, 0, count)

	// Get refs for non-existent session
	refs := tracker.GetSessionReferences("nonexistent")
	assert.Empty(t, refs)
}

// TestSessionReferenceTracker_ConcurrentAccess tests thread-safety with race detector
// Run with: go test -race -tags fts5 ./pkg/storage
func TestSessionReferenceTracker_ConcurrentAccess(t *testing.T) {
	sharedMem := setupTestSharedMemory(t)
	tracker := NewSessionReferenceTracker(sharedMem)

	const numGoroutines = 10
	const opsPerGoroutine = 100

	var wg sync.WaitGroup

	// Concurrent pin operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			sessionID := "sess_concurrent"
			for j := 0; j < opsPerGoroutine; j++ {
				refID := string(rune('a' + (workerID*opsPerGoroutine+j)%26))
				tracker.PinForSession(sessionID, refID)
			}
		}(i)
	}

	wg.Wait()

	// Verify no panic, refs were tracked
	refs := tracker.GetSessionReferences("sess_concurrent")
	assert.NotEmpty(t, refs)

	// Concurrent unpin
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tracker.UnpinSession("sess_concurrent")
		}()
	}

	wg.Wait()

	// Should be unpinned now
	refs = tracker.GetSessionReferences("sess_concurrent")
	assert.Empty(t, refs)
}

// TestSessionReferenceTracker_ConcurrentMultipleSessions tests concurrent ops across sessions
func TestSessionReferenceTracker_ConcurrentMultipleSessions(t *testing.T) {
	sharedMem := setupTestSharedMemory(t)
	tracker := NewSessionReferenceTracker(sharedMem)

	const numSessions = 20
	const refsPerSession = 10

	var wg sync.WaitGroup

	// Each goroutine manages one session
	for i := 0; i < numSessions; i++ {
		wg.Add(1)
		go func(sessionNum int) {
			defer wg.Done()
			sessionID := string(rune('A' + sessionNum%26))

			// Pin refs
			for j := 0; j < refsPerSession; j++ {
				refID := string(rune('a' + j))
				tracker.PinForSession(sessionID, refID)
			}

			// Get refs (concurrent read)
			refs := tracker.GetSessionReferences(sessionID)
			assert.NotEmpty(t, refs)

			// Unpin
			count := tracker.UnpinSession(sessionID)
			assert.Greater(t, count, 0)
		}(i)
	}

	wg.Wait()

	// All sessions should be unpinned
	stats := tracker.Stats()
	assert.Equal(t, 0, stats.SessionCount)
	assert.Equal(t, 0, stats.TotalRefs)
}

// TestSessionReferenceTracker_Stats tests statistics tracking
func TestSessionReferenceTracker_Stats(t *testing.T) {
	sharedMem := setupTestSharedMemory(t)
	tracker := NewSessionReferenceTracker(sharedMem)

	// Initial stats
	stats := tracker.Stats()
	assert.Equal(t, 0, stats.SessionCount)
	assert.Equal(t, 0, stats.TotalRefs)

	// Add sessions and refs
	tracker.PinForSession("sess1", "ref1a")
	tracker.PinForSession("sess1", "ref1b")
	tracker.PinForSession("sess2", "ref2a")

	stats = tracker.Stats()
	assert.Equal(t, 2, stats.SessionCount)
	assert.Equal(t, 3, stats.TotalRefs)

	// Remove one session
	tracker.UnpinSession("sess1")

	stats = tracker.Stats()
	assert.Equal(t, 1, stats.SessionCount)
	assert.Equal(t, 1, stats.TotalRefs)

	// Remove last session
	tracker.UnpinSession("sess2")

	stats = tracker.Stats()
	assert.Equal(t, 0, stats.SessionCount)
	assert.Equal(t, 0, stats.TotalRefs)
}

// TestSessionReferenceTracker_GetSessionReferences_ReturnsCopy tests immutability
func TestSessionReferenceTracker_GetSessionReferences_ReturnsCopy(t *testing.T) {
	sharedMem := setupTestSharedMemory(t)
	tracker := NewSessionReferenceTracker(sharedMem)

	sessionID := "sess_copy_test"
	tracker.PinForSession(sessionID, "ref1")
	tracker.PinForSession(sessionID, "ref2")

	// Get references
	refs1 := tracker.GetSessionReferences(sessionID)
	require.Len(t, refs1, 2)

	// Modify returned slice
	refs1[0] = "modified"
	_ = append(refs1, "extra")

	// Get references again - should be unchanged
	refs2 := tracker.GetSessionReferences(sessionID)
	assert.Len(t, refs2, 2)
	assert.NotContains(t, refs2, "modified")
	assert.NotContains(t, refs2, "extra")
}

// setupTestSharedMemory creates a test SharedMemoryStore
func setupTestSharedMemory(t *testing.T) *SharedMemoryStore {
	config := &Config{
		MaxMemoryBytes:       1 * 1024 * 1024, // 1MB for tests
		CompressionThreshold: 10 * 1024,       // 10KB
		TTLSeconds:           60,
	}
	return NewSharedMemoryStore(config)
}

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
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// mockPurger implements Purger for testing.
type mockPurger struct {
	mu        sync.Mutex
	callCount atomic.Int32
	lastGrace string
	err       error
}

func (m *mockPurger) PurgeDeleted(_ context.Context, graceInterval string) error {
	m.callCount.Add(1)
	m.mu.Lock()
	m.lastGrace = graceInterval
	m.mu.Unlock()
	return m.err
}

// Compile-time check: mockPurger implements Purger.
var _ Purger = (*mockPurger)(nil)

func TestStartSoftDeleteCleanup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		gracePeriodSeconds int32
		intervalSeconds    int32
		storeErr           error
		wantGrace          string
		wantMinCalls       int32
	}{
		{
			name:               "calls PurgeDeleted with correct grace interval",
			gracePeriodSeconds: 2592000,
			intervalSeconds:    1,
			storeErr:           nil,
			wantGrace:          "2592000 seconds",
			wantMinCalls:       1,
		},
		{
			name:               "continues running after PurgeDeleted error",
			gracePeriodSeconds: 86400,
			intervalSeconds:    1,
			storeErr:           fmt.Errorf("connection refused"),
			wantGrace:          "86400 seconds",
			wantMinCalls:       1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := &mockPurger{err: tc.storeErr}
			logger := zaptest.NewLogger(t)

			cleaner := StartSoftDeleteCleanup(store, tc.gracePeriodSeconds, tc.intervalSeconds, logger)

			// Wait for at least one tick
			time.Sleep(1500 * time.Millisecond)

			// Stop waits for the goroutine to exit, preventing the race with t's logger
			cleaner.Stop()

			// Verify at least the expected number of calls
			assert.GreaterOrEqual(t, store.callCount.Load(), tc.wantMinCalls)

			store.mu.Lock()
			assert.Equal(t, tc.wantGrace, store.lastGrace)
			store.mu.Unlock()
		})
	}
}

func TestStartSoftDeleteCleanup_CancelStopsGoroutine(t *testing.T) {
	t.Parallel()

	store := &mockPurger{}
	logger := zaptest.NewLogger(t)

	cleaner := StartSoftDeleteCleanup(store, 30, 1, logger)

	// Stop immediately and wait for clean exit
	cleaner.Stop()

	countAfterStop := store.callCount.Load()

	// Wait to verify no more calls happen after stop
	time.Sleep(1500 * time.Millisecond)

	// Count should not have increased after Stop returned
	assert.Equal(t, countAfterStop, store.callCount.Load())
}

func TestStartSoftDeleteCleanup_GraceIntervalFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		gracePeriodSeconds int32
		wantInterval       string
	}{
		{
			name:               "30 days",
			gracePeriodSeconds: 2592000,
			wantInterval:       "2592000 seconds",
		},
		{
			name:               "7 days",
			gracePeriodSeconds: 604800,
			wantInterval:       "604800 seconds",
		},
		{
			name:               "1 day",
			gracePeriodSeconds: 86400,
			wantInterval:       "86400 seconds",
		},
		{
			name:               "zero",
			gracePeriodSeconds: 0,
			wantInterval:       "0 seconds",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := &mockPurger{}
			logger := zaptest.NewLogger(t)

			cleaner := StartSoftDeleteCleanup(store, tc.gracePeriodSeconds, 1, logger)

			// Wait for one tick
			time.Sleep(1500 * time.Millisecond)

			// Stop waits for the goroutine to exit
			cleaner.Stop()

			require.GreaterOrEqual(t, store.callCount.Load(), int32(1))

			store.mu.Lock()
			assert.Equal(t, tc.wantInterval, store.lastGrace)
			store.mu.Unlock()
		})
	}
}

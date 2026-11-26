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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/teradata-labs/loom/pkg/memory"
)

// mockEvictor implements MemoryEvictor for testing.
type mockEvictor struct {
	mu          sync.Mutex
	decayCalls  atomic.Int32
	evictCalls  atomic.Int32
	lastDecay   float64
	lastOpts    memory.EvictionOptions
	evictReturn int
	decayErr    error
	evictErr    error
}

func (m *mockEvictor) DecayAll(_ context.Context, _ string, decayFactor float64) error {
	m.decayCalls.Add(1)
	m.mu.Lock()
	m.lastDecay = decayFactor
	m.mu.Unlock()
	return m.decayErr
}

func (m *mockEvictor) EvictStaleMemories(_ context.Context, opts memory.EvictionOptions) (int, error) {
	m.evictCalls.Add(1)
	m.mu.Lock()
	m.lastOpts = opts
	m.mu.Unlock()
	return m.evictReturn, m.evictErr
}

var _ MemoryEvictor = (*mockEvictor)(nil)

func TestStartMemoryEvictionSweep_RunsPasses(t *testing.T) {
	t.Parallel()

	store := &mockEvictor{evictReturn: 3}
	logger := zaptest.NewLogger(t)

	sweeper := StartMemoryEvictionSweep(store, MemoryEvictionConfig{
		Interval:       10 * time.Millisecond,
		DecayFactor:    0.98,
		MinSalience:    0.1,
		MaxIdle:        time.Hour,
		MinAge:         time.Minute,
		MaxAccessCount: 0,
		MaxRows:        100,
	}, logger)
	defer sweeper.Stop()

	require.Eventually(t, func() bool {
		return store.evictCalls.Load() >= 1 && store.decayCalls.Load() >= 1
	}, 2*time.Second, 5*time.Millisecond, "expected at least one decay and evict pass")

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, 0.98, store.lastDecay)
	assert.Equal(t, "", store.lastOpts.AgentID, "sweep should target all agents")
	assert.Equal(t, 0.1, store.lastOpts.MinSalience)
	assert.Equal(t, 100, store.lastOpts.MaxRows)
}

func TestStartMemoryEvictionSweep_StopHalts(t *testing.T) {
	t.Parallel()

	store := &mockEvictor{}
	logger := zaptest.NewLogger(t)

	sweeper := StartMemoryEvictionSweep(store, MemoryEvictionConfig{
		Interval:    5 * time.Millisecond,
		DecayFactor: 0.98,
		MinSalience: 0.1,
	}, logger)

	require.Eventually(t, func() bool {
		return store.evictCalls.Load() >= 1
	}, 2*time.Second, 5*time.Millisecond)

	sweeper.Stop()
	after := store.evictCalls.Load()
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, after, store.evictCalls.Load(), "no passes should run after Stop")
}

func TestRunMemoryEvictionPass_SkipsDecayWhenFactorOutOfRange(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)

	for _, factor := range []float64{0, 1, 1.5, -0.2} {
		store := &mockEvictor{}
		runMemoryEvictionPass(context.Background(), store, MemoryEvictionConfig{
			DecayFactor: factor,
			MinSalience: 0.1,
		}, logger)
		assert.Equal(t, int32(0), store.decayCalls.Load(), "factor %v should skip decay", factor)
		assert.Equal(t, int32(1), store.evictCalls.Load(), "evict should still run for factor %v", factor)
	}
}

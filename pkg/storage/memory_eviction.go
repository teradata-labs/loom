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
	"time"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/memory"
)

// MemoryEvictor is the subset of the graph-memory store used by the background
// eviction sweep. Both the SQLite and PostgreSQL stores satisfy it structurally,
// so the sweep is wired via a type assertion (like Purger) rather than widening
// the memory.GraphMemoryStore interface.
type MemoryEvictor interface {
	DecayAll(ctx context.Context, agentID string, decayFactor float64) error
	EvictStaleMemories(ctx context.Context, opts memory.EvictionOptions) (int, error)
}

// MemoryEvictionConfig configures the background stale-memory sweep that bounds
// graph-memory growth.
type MemoryEvictionConfig struct {
	// Interval between sweeps. Non-positive falls back to the default.
	Interval time.Duration
	// DecayFactor is applied to every agent's salience at the start of each pass
	// so the MinSalience floor stays meaningful over time. A value in (0,1)
	// decays; values <= 0 or >= 1 skip the decay step.
	DecayFactor float64
	// MinSalience is the salience floor for predicate eviction.
	MinSalience float64
	// MaxIdle is the minimum time since last access for eligibility.
	MaxIdle time.Duration
	// MinAge protects freshly written lessons from eviction.
	MinAge time.Duration
	// MaxAccessCount is the reuse ceiling for eligibility.
	MaxAccessCount int
	// MaxRows caps live memories per agent (0 disables the cap).
	MaxRows int
}

// MemoryEvictionSweeper manages the background goroutine that periodically
// decays and evicts stale graph memories. Call Stop during shutdown.
type MemoryEvictionSweeper struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Stop cancels the sweep goroutine and blocks until it has exited.
func (s *MemoryEvictionSweeper) Stop() {
	s.cancel()
	<-s.done
}

// StartMemoryEvictionSweep starts a background goroutine that, on each tick,
// decays salience across all agents and then soft-evicts stale, rarely reused
// memories (plus an optional per-agent row cap). It is the growth-management
// counterpart to StartSoftDeleteCleanup.
//
// Call Stop on the returned sweeper during shutdown to cancel the goroutine and
// wait for it to exit cleanly.
func StartMemoryEvictionSweep(store MemoryEvictor, cfg MemoryEvictionConfig, logger *zap.Logger) *MemoryEvictionSweeper {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	const defaultInterval = time.Hour
	if cfg.Interval <= 0 {
		logger.Warn("memory eviction interval is non-positive; using default",
			zap.Duration("default", defaultInterval),
		)
		cfg.Interval = defaultInterval
	}

	logger.Info("Starting graph-memory eviction sweep",
		zap.Duration("interval", cfg.Interval),
		zap.Float64("decay_factor", cfg.DecayFactor),
		zap.Float64("min_salience", cfg.MinSalience),
		zap.Duration("max_idle", cfg.MaxIdle),
		zap.Duration("min_age", cfg.MinAge),
		zap.Int("max_access_count", cfg.MaxAccessCount),
		zap.Int("max_rows_per_agent", cfg.MaxRows),
	)

	go func() {
		defer close(done)

		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				logger.Info("Graph-memory eviction sweep stopped")
				return
			case <-ticker.C:
				runMemoryEvictionPass(ctx, store, cfg, logger)
			}
		}
	}()

	return &MemoryEvictionSweeper{cancel: cancel, done: done}
}

// runMemoryEvictionPass performs a single decay-then-evict pass. Exported logic
// is kept here (not inlined in the goroutine) so tests can drive it directly.
func runMemoryEvictionPass(ctx context.Context, store MemoryEvictor, cfg MemoryEvictionConfig, logger *zap.Logger) {
	// Phase 1: persist salience decay so the floor predicate is meaningful.
	if cfg.DecayFactor > 0 && cfg.DecayFactor < 1 {
		if err := store.DecayAll(ctx, "", cfg.DecayFactor); err != nil {
			logger.Error("memory eviction: decay failed", zap.Error(err))
		}
	}

	// Phase 2 + 3: predicate eviction and per-agent row cap.
	n, err := store.EvictStaleMemories(ctx, memory.EvictionOptions{
		AgentID:        "",
		MinSalience:    cfg.MinSalience,
		MaxIdle:        cfg.MaxIdle,
		MinAge:         cfg.MinAge,
		MaxAccessCount: cfg.MaxAccessCount,
		MaxRows:        cfg.MaxRows,
	})
	if err != nil {
		logger.Error("memory eviction: evict failed", zap.Error(err))
		return
	}
	if n > 0 {
		logger.Info("Graph-memory eviction sweep evicted stale memories", zap.Int("count", n))
	} else {
		logger.Debug("Graph-memory eviction sweep found nothing to evict")
	}
}

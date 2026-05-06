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

package index

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// RebuildOptions controls a hot-reload-triggered index rebuild.
type RebuildOptions struct {
	// Builder is the index builder; required.
	Builder *Builder
	// Store persists the rebuilt index. Optional; nil disables persistence.
	Store Store
	// Source supplies the post-hot-reload skill catalog. The function is
	// called fresh on each rebuild so it always sees the current library.
	Source func() Source
	// Router (optional) is updated with the new tree on each rebuild via
	// SetTree. Wiring the router here lets one hot-reload event refresh
	// the live router without callers having to plumb the result.
	Router *Router
	// Cache (optional) is invalidated after rebuild to drop stale routing
	// decisions whose underlying nodes have changed.
	Cache *Cache
	// DebounceWindow coalesces rapid consecutive YAML changes into one
	// rebuild. Default 1s. fsnotify can fire multiple events per save.
	DebounceWindow time.Duration
	// Logger receives rebuild outcomes.
	Logger *zap.Logger
}

// HotReloadHandler wires the index machinery to skill hot-reload events.
// It returns a callback compatible with skills.HotReloadConfig.OnUpdate
// that, when invoked, schedules a debounced rebuild on a worker goroutine.
//
// The callback signature matches skills.SkillUpdateCallback exactly so
// callers can pass the result directly:
//
//	hr := skills.NewHotReloader(lib, skills.HotReloadConfig{
//	    Enabled:  true,
//	    OnUpdate: index.HotReloadHandler(opts),
//	})
//
// Concurrency: rebuilds are serialized through an internal mutex; only
// one rebuild runs at a time. Concurrent change notifications coalesce
// via DebounceWindow rather than producing parallel rebuilds.
func HotReloadHandler(opts RebuildOptions) func(eventType, skillName string, err error) {
	if opts.Builder == nil || opts.Source == nil {
		// Misconfigured callers get a no-op rather than a nil panic, so a
		// half-wired registry doesn't crash the hot-reload loop.
		return func(string, string, error) {}
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.DebounceWindow <= 0 {
		opts.DebounceWindow = time.Second
	}

	d := &debouncer{window: opts.DebounceWindow}

	return func(eventType, skillName string, err error) {
		// Validation failures don't invalidate the index; the affected
		// skill is left as-is until a follow-up edit fixes the YAML.
		if eventType == "validation_failed" {
			opts.Logger.Debug("hot-reload validation failure; skipping index rebuild",
				zap.String("skill", skillName),
				zap.NamedError("validation_error", err))
			return
		}

		d.fire(func() {
			rebuildOnce(context.Background(), &opts)
		})
	}
}

// rebuildOnce runs one full rebuild cycle: build -> persist -> swap into
// the live router -> invalidate the decision cache. Any individual step
// can fail without aborting the others; failures are logged at warn so
// operators can see them but the agent keeps serving with stale data.
func rebuildOnce(ctx context.Context, opts *RebuildOptions) {
	src := opts.Source()
	if src == nil {
		opts.Logger.Debug("hot-reload: source returned nil; skipping rebuild")
		return
	}

	idx, err := opts.Builder.Build(ctx, src)
	if err != nil {
		opts.Logger.Warn("hot-reload: index build failed",
			zap.Error(err))
		return
	}

	if opts.Store != nil {
		if err := opts.Store.SaveIndex(ctx, idx); err != nil {
			opts.Logger.Warn("hot-reload: index persist failed",
				zap.String("index_id", idx.ID),
				zap.Error(err))
			// Persistence failures don't block the in-memory swap; the
			// router still serves the new tree this process lifetime.
		}
	}

	if opts.Router != nil {
		opts.Router.SetTree(NewTree(idx))
	}
	if opts.Cache != nil {
		// Drop EVERYTHING — routing decisions over the prior tree are
		// stale by definition. Per-session granularity isn't useful here
		// since the change is library-wide.
		opts.Cache.Clear()
	}

	opts.Logger.Info("hot-reload: index rebuilt",
		zap.String("index_id", idx.ID),
		zap.Int("nodes", len(idx.Nodes)))
}

// debouncer coalesces rapid invocations into a single trailing call.
// Goroutine-safe; the timer field is replaced on each new call so the
// most-recently-set deadline always wins.
type debouncer struct {
	window time.Duration
	mu     sync.Mutex
	timer  *time.Timer
}

func (d *debouncer) fire(fn func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(d.window, fn)
}

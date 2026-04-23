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

package catalog

import (
	"context"
	"sync"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"golang.org/x/sync/singleflight"
)

// CachedSource wraps another Source with a TTL-based read-through cache.
// Both positive (found) and negative (nil) results are cached to avoid
// retrying misses on every chat request.
//
// Safe for concurrent use. List calls bypass the cache (the cost of caching
// a whole enumerated catalog rarely pays off and introduces staleness risk
// on admin writes).
//
// Concurrent misses on the same key collapse into a single inner.Lookup via
// singleflight, so a thundering-herd of chat requests for a newly-seen model
// only hits the backing source once.
type CachedSource struct {
	inner Source
	ttl   time.Duration

	mu      sync.RWMutex
	entries map[string]cachedEntry
	sf      singleflight.Group
}

type cachedEntry struct {
	info      *loomv1.ModelInfo // nil = negative cache
	expiresAt time.Time
}

// NewCachedSource wraps inner with a TTL cache. A ttl of zero disables
// expiration (entries live until Invalidate). A negative ttl behaves like
// zero. Passing a nil inner panics — the caller's mistake, not runtime data.
func NewCachedSource(inner Source, ttl time.Duration) *CachedSource {
	if inner == nil {
		panic("catalog: NewCachedSource requires a non-nil inner Source")
	}
	if ttl < 0 {
		ttl = 0
	}
	return &CachedSource{
		inner:   inner,
		ttl:     ttl,
		entries: map[string]cachedEntry{},
	}
}

// Lookup returns the cached ModelInfo when fresh, otherwise consults the
// inner source and caches the result (including nil). Concurrent misses on
// the same key are coalesced via singleflight so the inner source sees a
// single Lookup per key even under heavy contention.
func (c *CachedSource) Lookup(ctx context.Context, provider, modelID string) *loomv1.ModelInfo {
	key := cacheKey(provider, modelID)

	c.mu.RLock()
	entry, hit := c.entries[key]
	c.mu.RUnlock()
	if hit && (c.ttl == 0 || time.Now().Before(entry.expiresAt)) {
		return entry.info
	}

	v, _, _ := c.sf.Do(key, func() (interface{}, error) {
		// Re-check the cache under the singleflight leader: a concurrent
		// Invalidate-then-Lookup could have filled the entry while we were
		// waiting to become the leader.
		c.mu.RLock()
		entry, hit := c.entries[key]
		c.mu.RUnlock()
		if hit && (c.ttl == 0 || time.Now().Before(entry.expiresAt)) {
			return entry.info, nil
		}

		info := c.inner.Lookup(ctx, provider, modelID)

		c.mu.Lock()
		c.entries[key] = cachedEntry{
			info:      info,
			expiresAt: time.Now().Add(c.ttl),
		}
		c.mu.Unlock()
		return info, nil
	})

	info, _ := v.(*loomv1.ModelInfo)
	return info
}

// List forwards directly to the inner source without caching.
func (c *CachedSource) List(ctx context.Context) map[string][]*loomv1.ModelInfo {
	return c.inner.List(ctx)
}

// Invalidate drops every cached entry. Call this from admin write paths that
// mutate the underlying source so the next Lookup reflects the change.
func (c *CachedSource) Invalidate() {
	c.mu.Lock()
	c.entries = map[string]cachedEntry{}
	c.mu.Unlock()
}

// InvalidateModel drops the cached entry for a single (provider, modelID).
// Useful when the admin write path knows exactly which model changed.
func (c *CachedSource) InvalidateModel(provider, modelID string) {
	key := cacheKey(provider, modelID)
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

// cacheKey builds a collision-safe key from provider + modelID. Using a null
// byte separator avoids ambiguity between e.g. ("ab", "c") and ("a", "bc").
func cacheKey(provider, modelID string) string {
	return NormalizeProvider(provider) + "\x00" + modelID
}

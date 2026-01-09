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
package prompts

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// CachedRegistry wraps a PromptRegistry with an in-memory TTL cache.
//
// This reduces load on the underlying registry (file I/O, HTTP requests, etc.)
// and improves performance for frequently accessed prompts.
//
// Example:
//
//	fileRegistry := prompts.NewFileRegistry("./prompts")
//	cachedRegistry := prompts.NewCachedRegistry(fileRegistry, 5*time.Minute)
//
//	// First call: cache miss, loads from file
//	prompt1, _ := cachedRegistry.Get(ctx, "agent.system", vars)
//
//	// Second call: cache hit, instant
//	prompt2, _ := cachedRegistry.Get(ctx, "agent.system", vars)
type CachedRegistry struct {
	underlying PromptRegistry
	ttl        time.Duration

	mu       sync.RWMutex
	content  map[string]*cacheEntry         // key:variant -> content
	metadata map[string]*metadataCacheEntry // key -> metadata

	// Metrics
	hits   uint64
	misses uint64
}

// cacheEntry represents a cached prompt content.
type cacheEntry struct {
	content   string
	expiresAt time.Time
}

// metadataCacheEntry represents cached metadata.
type metadataCacheEntry struct {
	metadata  *PromptMetadata
	expiresAt time.Time
}

// NewCachedRegistry creates a new cached registry with the given TTL.
//
// A typical TTL is 5-10 minutes for production use, or 1 minute for development
// with frequent prompt changes.
func NewCachedRegistry(underlying PromptRegistry, ttl time.Duration) *CachedRegistry {
	return &CachedRegistry{
		underlying: underlying,
		ttl:        ttl,
		content:    make(map[string]*cacheEntry),
		metadata:   make(map[string]*metadataCacheEntry),
	}
}

// Get retrieves a prompt by key with variable interpolation.
// Uses cached content if available and not expired.
func (c *CachedRegistry) Get(ctx context.Context, key string, vars map[string]interface{}) (string, error) {
	return c.GetWithVariant(ctx, key, "default", vars)
}

// GetWithVariant retrieves a specific variant for A/B testing.
// Uses cached content if available and not expired.
func (c *CachedRegistry) GetWithVariant(ctx context.Context, key string, variant string, vars map[string]interface{}) (string, error) {
	cacheKey := makeCacheKey(key, variant)

	// Check cache first (read lock)
	c.mu.RLock()
	entry, found := c.content[cacheKey]
	c.mu.RUnlock()

	if found && time.Now().Before(entry.expiresAt) {
		// Cache hit
		c.mu.Lock()
		c.hits++
		c.mu.Unlock()

		// Interpolate variables (not cached, as vars change per request)
		return Interpolate(entry.content, vars), nil
	}

	// Cache miss or expired
	c.mu.Lock()
	c.misses++
	c.mu.Unlock()

	// Load from underlying registry
	// Note: We get the raw content without interpolation so we can cache it
	rawContent, err := c.getRawContent(ctx, key, variant)
	if err != nil {
		return "", err
	}

	// Store in cache
	c.mu.Lock()
	c.content[cacheKey] = &cacheEntry{
		content:   rawContent,
		expiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()

	// Interpolate and return
	return Interpolate(rawContent, vars), nil
}

// getRawContent gets the raw content from the underlying registry without interpolation.
func (c *CachedRegistry) getRawContent(ctx context.Context, key string, variant string) (string, error) {
	// Get with nil vars to avoid interpolation (if underlying registry supports it)
	// Note: This assumes the underlying registry doesn't modify content when vars is nil
	return c.underlying.GetWithVariant(ctx, key, variant, nil)
}

// GetMetadata retrieves prompt metadata without the content.
// Uses cached metadata if available and not expired.
func (c *CachedRegistry) GetMetadata(ctx context.Context, key string) (*PromptMetadata, error) {
	// Check cache first (read lock)
	c.mu.RLock()
	entry, found := c.metadata[key]
	c.mu.RUnlock()

	if found && time.Now().Before(entry.expiresAt) {
		// Cache hit
		c.mu.Lock()
		c.hits++
		c.mu.Unlock()

		// Return a copy to prevent mutation
		metadata := *entry.metadata
		return &metadata, nil
	}

	// Cache miss or expired
	c.mu.Lock()
	c.misses++
	c.mu.Unlock()

	// Load from underlying registry
	metadata, err := c.underlying.GetMetadata(ctx, key)
	if err != nil {
		return nil, err
	}

	// Store in cache (store a copy)
	metadataCopy := *metadata
	c.mu.Lock()
	c.metadata[key] = &metadataCacheEntry{
		metadata:  &metadataCopy,
		expiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()

	return metadata, nil
}

// List lists all available prompt keys, optionally filtered.
// This is NOT cached as it's typically used less frequently.
func (c *CachedRegistry) List(ctx context.Context, filters map[string]string) ([]string, error) {
	return c.underlying.List(ctx, filters)
}

// Reload reloads prompts from the underlying registry and clears the cache.
func (c *CachedRegistry) Reload(ctx context.Context) error {
	// Reload underlying registry
	if err := c.underlying.Reload(ctx); err != nil {
		return err
	}

	// Clear cache
	c.Invalidate()

	return nil
}

// Watch returns a channel that receives updates when prompts change.
// Updates automatically invalidate the cache.
func (c *CachedRegistry) Watch(ctx context.Context) (<-chan PromptUpdate, error) {
	updatesCh, err := c.underlying.Watch(ctx)
	if err != nil {
		return nil, err
	}

	// Create a new channel to forward updates
	forwardCh := make(chan PromptUpdate)

	go func() {
		defer close(forwardCh)

		for {
			select {
			case <-ctx.Done():
				return
			case update, ok := <-updatesCh:
				if !ok {
					return
				}

				// Invalidate cache for this key
				c.InvalidateKey(update.Key)

				// Forward the update
				select {
				case forwardCh <- update:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return forwardCh, nil
}

// Invalidate clears the entire cache.
func (c *CachedRegistry) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.content = make(map[string]*cacheEntry)
	c.metadata = make(map[string]*metadataCacheEntry)
}

// InvalidateKey clears cache entries for a specific prompt key (all variants).
func (c *CachedRegistry) InvalidateKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Remove metadata
	delete(c.metadata, key)

	// Remove all variants of this key
	// (we need to iterate because cache keys are "key:variant")
	for cacheKey := range c.content {
		if keyFromCacheKey(cacheKey) == key {
			delete(c.content, cacheKey)
		}
	}
}

// Stats returns cache hit/miss statistics.
//
// Example:
//
//	hits, misses := cachedRegistry.Stats()
//	hitRate := float64(hits) / float64(hits+misses)
//	fmt.Printf("Cache hit rate: %.2f%%\n", hitRate*100)
func (c *CachedRegistry) Stats() (hits, misses uint64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hits, c.misses
}

// ResetStats resets hit/miss counters to zero.
func (c *CachedRegistry) ResetStats() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hits = 0
	c.misses = 0
}

// makeCacheKey creates a cache key from prompt key and variant.
func makeCacheKey(key, variant string) string {
	return fmt.Sprintf("%s:%s", key, variant)
}

// keyFromCacheKey extracts the prompt key from a cache key.
func keyFromCacheKey(cacheKey string) string {
	// Split on first colon
	for i, c := range cacheKey {
		if c == ':' {
			return cacheKey[:i]
		}
	}
	return cacheKey
}

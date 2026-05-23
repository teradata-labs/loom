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
	"sync/atomic"
	"testing"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSource is a minimal in-memory Source for tests. It records the number
// of Lookup calls so CachedSource tests can assert cache behavior.
type fakeSource struct {
	models    map[string]map[string]*loomv1.ModelInfo // provider -> modelID -> info
	lookupCnt atomic.Int64
}

func newFakeSource(entries ...*loomv1.ModelInfo) *fakeSource {
	f := &fakeSource{models: map[string]map[string]*loomv1.ModelInfo{}}
	for _, e := range entries {
		if _, ok := f.models[e.Provider]; !ok {
			f.models[e.Provider] = map[string]*loomv1.ModelInfo{}
		}
		f.models[e.Provider][e.Id] = e
	}
	return f
}

func (f *fakeSource) Lookup(_ context.Context, provider, modelID string) *loomv1.ModelInfo {
	f.lookupCnt.Add(1)
	provider = NormalizeProvider(provider)
	if byID, ok := f.models[provider]; ok {
		return byID[modelID]
	}
	return nil
}

func (f *fakeSource) List(_ context.Context) map[string][]*loomv1.ModelInfo {
	out := map[string][]*loomv1.ModelInfo{}
	for provider, byID := range f.models {
		for _, info := range byID {
			out[provider] = append(out[provider], info)
		}
	}
	return out
}

// ─────────────────────────── StaticSource ───────────────────────────

func TestStaticSource_LookupHitsBuiltinCatalog(t *testing.T) {
	s := StaticSource()
	ctx := context.Background()

	info := s.Lookup(ctx, "anthropic", "claude-opus-4-6")
	require.NotNil(t, info)
	assert.Equal(t, "claude-opus-4-6", info.Id)
	assert.EqualValues(t, 1_000_000, info.ContextWindow)
	assert.EqualValues(t, 128_000, info.MaxOutputTokens)
}

func TestStaticSource_LookupReturnsNilForUnknown(t *testing.T) {
	s := StaticSource()
	assert.Nil(t, s.Lookup(context.Background(), "anthropic", "does-not-exist"))
	assert.Nil(t, s.Lookup(context.Background(), "nonexistent-provider", "claude-opus-4-6"))
}

func TestStaticSource_ListReturnsAllProviders(t *testing.T) {
	s := StaticSource()
	catalog := s.List(context.Background())
	assert.NotEmpty(t, catalog["anthropic"])
	assert.NotEmpty(t, catalog["openai"])
}

// ─────────────────────────── Register / DefaultSource ───────────────────────────

func TestRegister_SwapsTheDefaultSource(t *testing.T) {
	// Save and restore so other tests aren't affected.
	original := DefaultSource()
	t.Cleanup(func() { Register(original) })

	fake := newFakeSource(&loomv1.ModelInfo{
		Id: "custom-model", Provider: "anthropic", ContextWindow: 42_000,
	})
	Register(fake)

	info := Lookup("anthropic", "custom-model")
	require.NotNil(t, info)
	assert.EqualValues(t, 42_000, info.ContextWindow)

	// Built-in entry is no longer visible through the default source.
	assert.Nil(t, Lookup("anthropic", "claude-opus-4-6"))
}

func TestRegister_NilRestoresStatic(t *testing.T) {
	original := DefaultSource()
	t.Cleanup(func() { Register(original) })

	Register(newFakeSource())
	Register(nil) // caller clears

	// Package Lookup now goes back through the built-in catalog.
	info := Lookup("anthropic", "claude-opus-4-6")
	require.NotNil(t, info)
	assert.Equal(t, "claude-opus-4-6", info.Id)
}

// ─────────────────────────── MultiSource ───────────────────────────

func TestMultiSource_LookupFirstHitWins(t *testing.T) {
	a := newFakeSource(&loomv1.ModelInfo{
		Id: "shared", Provider: "anthropic", ContextWindow: 100,
	})
	b := newFakeSource(&loomv1.ModelInfo{
		Id: "shared", Provider: "anthropic", ContextWindow: 200,
	})

	chain := MultiSource{a, b}
	info := chain.Lookup(context.Background(), "anthropic", "shared")
	require.NotNil(t, info)
	assert.EqualValues(t, 100, info.ContextWindow, "earlier source should win")
}

func TestMultiSource_LookupFallsThroughOnMiss(t *testing.T) {
	empty := newFakeSource()
	found := newFakeSource(&loomv1.ModelInfo{
		Id: "only-here", Provider: "openai", ContextWindow: 8_000,
	})

	chain := MultiSource{empty, found}
	info := chain.Lookup(context.Background(), "openai", "only-here")
	require.NotNil(t, info)
	assert.EqualValues(t, 8_000, info.ContextWindow)
	assert.EqualValues(t, 1, empty.lookupCnt.Load(), "empty source consulted before fallthrough")
	assert.EqualValues(t, 1, found.lookupCnt.Load())
}

func TestMultiSource_NilEntriesIgnored(t *testing.T) {
	found := newFakeSource(&loomv1.ModelInfo{
		Id: "m", Provider: "anthropic",
	})
	chain := MultiSource{nil, found, nil}
	assert.NotNil(t, chain.Lookup(context.Background(), "anthropic", "m"))
}

func TestMultiSource_ListMergesAndDedupes(t *testing.T) {
	a := newFakeSource(
		&loomv1.ModelInfo{Id: "shared", Provider: "anthropic", ContextWindow: 100},
		&loomv1.ModelInfo{Id: "a-only", Provider: "anthropic", ContextWindow: 111},
	)
	b := newFakeSource(
		&loomv1.ModelInfo{Id: "shared", Provider: "anthropic", ContextWindow: 200},
		&loomv1.ModelInfo{Id: "b-only", Provider: "openai", ContextWindow: 222},
	)
	chain := MultiSource{a, b}

	catalog := chain.List(context.Background())

	var shared, aOnly *loomv1.ModelInfo
	for _, info := range catalog["anthropic"] {
		switch info.Id {
		case "shared":
			shared = info
		case "a-only":
			aOnly = info
		}
	}
	require.NotNil(t, shared)
	require.NotNil(t, aOnly)
	assert.EqualValues(t, 100, shared.ContextWindow, "earlier source wins on collision")

	require.Len(t, catalog["openai"], 1)
	assert.Equal(t, "b-only", catalog["openai"][0].Id)
}

func TestMultiSource_ListNormalizesProviders(t *testing.T) {
	a := newFakeSource(&loomv1.ModelInfo{
		Id: "gpt-5", Provider: "azure-openai",
	})
	// b registers under the alias; List should merge into the canonical key.
	b := &fakeSource{
		models: map[string]map[string]*loomv1.ModelInfo{
			"azureopenai": {"gpt-6": {Id: "gpt-6", Provider: "azureopenai"}},
		},
	}
	chain := MultiSource{a, b}
	catalog := chain.List(context.Background())
	assert.Len(t, catalog["azure-openai"], 2)
	assert.Empty(t, catalog["azureopenai"])
}

// ─────────────────────────── CachedSource ───────────────────────────

func TestCachedSource_HitsInnerOnce(t *testing.T) {
	fake := newFakeSource(&loomv1.ModelInfo{Id: "m", Provider: "anthropic"})
	cached := NewCachedSource(fake, time.Minute)

	for i := 0; i < 5; i++ {
		require.NotNil(t, cached.Lookup(context.Background(), "anthropic", "m"))
	}
	assert.EqualValues(t, 1, fake.lookupCnt.Load(), "inner consulted once; rest served from cache")
}

func TestCachedSource_NegativeCaching(t *testing.T) {
	fake := newFakeSource() // empty
	cached := NewCachedSource(fake, time.Minute)

	for i := 0; i < 3; i++ {
		assert.Nil(t, cached.Lookup(context.Background(), "anthropic", "missing"))
	}
	assert.EqualValues(t, 1, fake.lookupCnt.Load(), "negative result cached")
}

func TestCachedSource_TTLExpiry(t *testing.T) {
	fake := newFakeSource(&loomv1.ModelInfo{Id: "m", Provider: "anthropic"})
	cached := NewCachedSource(fake, 10*time.Millisecond)

	cached.Lookup(context.Background(), "anthropic", "m")
	cached.Lookup(context.Background(), "anthropic", "m")
	assert.EqualValues(t, 1, fake.lookupCnt.Load())

	time.Sleep(20 * time.Millisecond)
	cached.Lookup(context.Background(), "anthropic", "m")
	assert.EqualValues(t, 2, fake.lookupCnt.Load(), "entry should expire after TTL")
}

func TestCachedSource_TTLZeroNeverExpires(t *testing.T) {
	fake := newFakeSource(&loomv1.ModelInfo{Id: "m", Provider: "anthropic"})
	cached := NewCachedSource(fake, 0)

	cached.Lookup(context.Background(), "anthropic", "m")
	time.Sleep(5 * time.Millisecond)
	cached.Lookup(context.Background(), "anthropic", "m")
	assert.EqualValues(t, 1, fake.lookupCnt.Load(), "ttl=0 means no expiry")
}

func TestCachedSource_Invalidate(t *testing.T) {
	fake := newFakeSource(&loomv1.ModelInfo{Id: "m", Provider: "anthropic"})
	cached := NewCachedSource(fake, time.Minute)

	cached.Lookup(context.Background(), "anthropic", "m")
	assert.EqualValues(t, 1, fake.lookupCnt.Load())

	cached.Invalidate()
	cached.Lookup(context.Background(), "anthropic", "m")
	assert.EqualValues(t, 2, fake.lookupCnt.Load(), "Invalidate forces re-read")
}

func TestCachedSource_InvalidateModelScopedToOneKey(t *testing.T) {
	fake := newFakeSource(
		&loomv1.ModelInfo{Id: "a", Provider: "anthropic"},
		&loomv1.ModelInfo{Id: "b", Provider: "anthropic"},
	)
	cached := NewCachedSource(fake, time.Minute)

	cached.Lookup(context.Background(), "anthropic", "a")
	cached.Lookup(context.Background(), "anthropic", "b")
	assert.EqualValues(t, 2, fake.lookupCnt.Load())

	cached.InvalidateModel("anthropic", "a")

	cached.Lookup(context.Background(), "anthropic", "a")
	cached.Lookup(context.Background(), "anthropic", "b")
	assert.EqualValues(t, 3, fake.lookupCnt.Load(), "only 'a' should re-read")
}

func TestCachedSource_InvalidateModelNormalizesProvider(t *testing.T) {
	fake := newFakeSource(&loomv1.ModelInfo{Id: "m", Provider: "azure-openai"})
	cached := NewCachedSource(fake, time.Minute)

	cached.Lookup(context.Background(), "azure-openai", "m")
	assert.EqualValues(t, 1, fake.lookupCnt.Load())

	// Invalidation with the alias should still hit the canonical cache key.
	cached.InvalidateModel("azureopenai", "m")
	cached.Lookup(context.Background(), "azure-openai", "m")
	assert.EqualValues(t, 2, fake.lookupCnt.Load())
}

func TestCachedSource_ConcurrentLookupsAreSafe(t *testing.T) {
	fake := newFakeSource(&loomv1.ModelInfo{Id: "m", Provider: "anthropic"})
	cached := NewCachedSource(fake, time.Minute)

	const goroutines = 64
	const perGoroutine = 250

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				_ = cached.Lookup(context.Background(), "anthropic", "m")
				if i%50 == 0 {
					cached.InvalidateModel("anthropic", "m")
				}
			}
		}()
	}
	wg.Wait()
}

func TestNewCachedSource_PanicsOnNilInner(t *testing.T) {
	assert.Panics(t, func() { NewCachedSource(nil, time.Second) })
}

// slowFakeSource blocks the first Lookup until release is closed, so the test
// can assert that concurrent callers queue on singleflight rather than each
// firing an independent inner.Lookup.
type slowFakeSource struct {
	fakeSource
	release chan struct{}
}

func (s *slowFakeSource) Lookup(ctx context.Context, provider, modelID string) *loomv1.ModelInfo {
	<-s.release
	return s.fakeSource.Lookup(ctx, provider, modelID)
}

func TestCachedSource_SingleflightCoalescesConcurrentMisses(t *testing.T) {
	slow := &slowFakeSource{
		fakeSource: fakeSource{
			models: map[string]map[string]*loomv1.ModelInfo{
				"anthropic": {"m": {Id: "m", Provider: "anthropic"}},
			},
		},
		release: make(chan struct{}),
	}
	cached := NewCachedSource(slow, time.Minute)

	const callers = 32
	var wg sync.WaitGroup
	results := make(chan *loomv1.ModelInfo, callers)

	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			results <- cached.Lookup(context.Background(), "anthropic", "m")
		}()
	}

	// Give every caller time to enter sf.Do and block on the slow Lookup.
	time.Sleep(20 * time.Millisecond)
	close(slow.release)
	wg.Wait()
	close(results)

	for info := range results {
		require.NotNil(t, info)
		assert.Equal(t, "m", info.Id)
	}
	assert.EqualValues(t, 1, slow.lookupCnt.Load(),
		"singleflight should collapse %d concurrent misses to a single inner.Lookup", callers)
}

// ─────────────────────────── Composition (docstring example) ───────────────────────────

// TestRegisterChainPattern documents the intended loom-cloud usage: wrap a
// dynamic source in a cache, chain the built-in static source behind it,
// register as the default.
func TestRegisterChainPattern(t *testing.T) {
	original := DefaultSource()
	t.Cleanup(func() { Register(original) })

	db := newFakeSource(&loomv1.ModelInfo{
		Id: "claude-opus-4-6", Provider: "anthropic", ContextWindow: 2_000_000,
		Name: "overridden-from-db",
	})

	Register(MultiSource{
		NewCachedSource(db, 5*time.Minute),
		StaticSource(),
	})

	// DB entry shadows the built-in.
	override := Lookup("anthropic", "claude-opus-4-6")
	require.NotNil(t, override)
	assert.EqualValues(t, 2_000_000, override.ContextWindow)
	assert.Equal(t, "overridden-from-db", override.Name)

	// Built-in entries not in the DB fall through.
	sonnet := Lookup("anthropic", "claude-sonnet-4-6")
	require.NotNil(t, sonnet)
	assert.Equal(t, "Claude Sonnet 4.6", sonnet.Name)
}

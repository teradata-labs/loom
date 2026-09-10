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

package factory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/teradata-labs/loom/pkg/llm/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installedOllamaModels mirrors what a real `ollama list` reports: every entry
// carries a tag, which is exactly the shape the bare-name catalog cannot match.
var installedOllamaModels = []string{
	"llama2:latest",
	"llama3:latest",
	"llama3.1:latest",
	"llama3.2:latest",
	"deepseek-r1:latest",
}

// fakeOllamaTags serves an /api/tags response listing the given model names.
func fakeOllamaTags(t *testing.T, names ...string) *httptest.Server {
	t.Helper()
	entries := make([]ollamaModelEntry, 0, len(names))
	for _, n := range names {
		entries = append(entries, ollamaModelEntry{Name: n, Model: n, Size: 1})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ollamaTagsResponse{Models: entries})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOllamaCatalogSource_ServesDiscoveredTaggedIDs(t *testing.T) {
	srv := fakeOllamaTags(t, installedOllamaModels...)

	src, err := OllamaCatalogSource(srv.URL)
	require.NoError(t, err)
	require.NotNil(t, src)

	ctx := context.Background()
	for _, id := range installedOllamaModels {
		info := src.Lookup(ctx, "ollama", id)
		require.NotNil(t, info, "discovered model %q should be in the source", id)
		assert.Equal(t, id, info.Id)
		assert.Equal(t, "ollama", info.Provider)
		assert.Zero(t, info.CostPer_1MInputUsd)
		assert.Zero(t, info.CostPer_1MOutputUsd)
	}

	// Scoped to ollama: composing this ahead of the static catalog cannot
	// shadow another provider's entries.
	assert.Nil(t, src.Lookup(ctx, "anthropic", "claude-opus-4-7"))
	listed := src.List(ctx)
	assert.Len(t, listed, 1)
	assert.Len(t, listed["ollama"], len(installedOllamaModels))
}

func TestOllamaCatalogSource_UnreachableReturnsError(t *testing.T) {
	src, err := OllamaCatalogSource("http://localhost:1")
	assert.Error(t, err)
	assert.Nil(t, src)
}

// TestRegisterOllamaCatalogSource_DiscoveredModelIsTierLocal is the assertion
// that makes capability leveling reachable: before this bridge existed,
// TierOf("ollama", "llama2:latest") was TierUnknown, and TierUnknown
// short-circuits leveling.
func TestRegisterOllamaCatalogSource_DiscoveredModelIsTierLocal(t *testing.T) {
	// llama2 has no catalog entry under any tag, so tag stripping alone cannot
	// classify it. Only discovery can.
	require.Equal(t, catalog.TierUnknown, catalog.TierOf("ollama", "llama2:latest"),
		"precondition: llama2 is absent from the built-in catalog")

	srv := fakeOllamaTags(t, installedOllamaModels...)
	require.NoError(t, RegisterOllamaCatalogSource(srv.URL))
	t.Cleanup(func() { catalog.Register(nil) })

	for _, id := range installedOllamaModels {
		assert.Equal(t, catalog.TierLocal, catalog.TierOf("ollama", id),
			"discovered model %q should classify as local", id)
	}

	// The built-in catalog is still reachable behind the discovered source.
	assert.Equal(t, catalog.TierFrontier, catalog.TierOf("anthropic", "claude-opus-4-7"))
	// A model that is neither discovered nor cataloged stays unknown.
	assert.Equal(t, catalog.TierUnknown, catalog.TierOf("ollama", "not-installed:latest"))
}

func TestRegisterOllamaCatalogSource_UnreachableLeavesSourceIntact(t *testing.T) {
	before := catalog.DefaultSource()
	err := RegisterOllamaCatalogSource("http://localhost:1")
	assert.Error(t, err)
	assert.Equal(t, before, catalog.DefaultSource(),
		"a failed discovery must not replace the registered source")
}

// TestRegisterOllamaCatalogSource_RepeatedCallsDoNotNest pins the documented
// idempotence: re-registering replaces the chain rather than wrapping it, so a
// process that re-discovers on a timer does not grow an unbounded Source chain.
func TestRegisterOllamaCatalogSource_RepeatedCallsDoNotNest(t *testing.T) {
	srv := fakeOllamaTags(t, "llama3.1:latest")
	t.Cleanup(func() { catalog.Register(nil) })

	require.NoError(t, RegisterOllamaCatalogSource(srv.URL))
	first, ok := catalog.DefaultSource().(catalog.MultiSource)
	require.True(t, ok)

	require.NoError(t, RegisterOllamaCatalogSource(srv.URL))
	second, ok := catalog.DefaultSource().(catalog.MultiSource)
	require.True(t, ok)

	assert.Len(t, second, len(first), "chain length must not grow across calls")
	assert.Len(t, second, 2)
}

// TestCatalogSource_SnapshotIsIsolatedFromRegistry pins that the Source is a
// snapshot, not a live view of the registry's unsynchronized map.
func TestCatalogSource_SnapshotIsIsolatedFromRegistry(t *testing.T) {
	srv := fakeOllamaTags(t, "llama3.1:latest")

	reg := NewModelRegistry()
	before := reg.CatalogSource()

	require.NoError(t, reg.DiscoverOllamaModels(srv.URL))
	after := reg.CatalogSource()

	ctx := context.Background()
	assert.Nil(t, before.Lookup(ctx, "ollama", "llama3.1:latest"),
		"a snapshot taken before discovery must not see discovered models")
	require.NotNil(t, after.Lookup(ctx, "ollama", "llama3.1:latest"))
}

// TestCatalogSource_ConcurrentLookupsAreSafe exercises the concurrency the
// catalog.Source contract requires, under -race.
func TestCatalogSource_ConcurrentLookupsAreSafe(t *testing.T) {
	srv := fakeOllamaTags(t, installedOllamaModels...)
	src, err := OllamaCatalogSource(srv.URL)
	require.NoError(t, err)

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := installedOllamaModels[i%len(installedOllamaModels)]
			if got := src.Lookup(ctx, "ollama", id); got == nil {
				t.Errorf("concurrent lookup of %q returned nil", id)
			}
			_ = src.List(ctx)
		}(i)
	}
	wg.Wait()
}

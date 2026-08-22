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
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBaseModelID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		modelID string
		want    string
	}{
		{"no tag is unchanged", "llama3.1", "llama3.1"},
		{"latest tag stripped", "llama3.1:latest", "llama3.1"},
		{"size tag stripped", "qwen3:30b", "qwen3"},
		{"cloud tag stripped", "deepseek-v3.1:671b-cloud", "deepseek-v3.1"},
		{"empty is unchanged", "", ""},
		{"leading colon is unchanged", ":latest", ":latest"},
		{"only a colon is unchanged", ":", ":"},
		{"trailing colon strips to base", "llama3.1:", "llama3.1"},
		{"last colon wins", "ns/model:tag:extra", "ns/model:tag"},
		{
			"registry port is not a tag",
			"localhost:5000/library/llama3",
			"localhost:5000/library/llama3",
		},
		{
			"tag after a registry port is stripped",
			"localhost:5000/library/llama3:latest",
			"localhost:5000/library/llama3",
		},
		{
			"openai fine-tune id keeps its trailing colon segment shape",
			"ft:gpt-4o-mini-2024-07-18:acme::abc123",
			"ft:gpt-4o-mini-2024-07-18:acme:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, BaseModelID(tt.modelID))
		})
	}
}

// TestBaseModelIDZeroAllocs pins the documented substring (non-allocating)
// property. No t.Parallel: AllocsPerRun is unreliable under concurrency.
func TestBaseModelIDZeroAllocs(t *testing.T) {
	allocs := testing.AllocsPerRun(100, func() {
		_ = BaseModelID("llama3.1:latest")
	})
	assert.Zero(t, allocs, "BaseModelID should not allocate")
}

// TestLookupTagFallback covers the whole point of the fallback: Ollama reports
// installed models as "name:tag" while the built-in catalog keys them on the
// bare name, so an exact-only lookup misses every real Ollama model.
func TestLookupTagFallback(t *testing.T) {
	tests := []struct {
		name    string
		modelID string
		wantID  string // "" means the lookup must miss
	}{
		{"bare catalog id still resolves", "llama3.1", "llama3.1"},
		{"tagged id resolves via fallback", "llama3.1:latest", "llama3.1"},
		{"tagged id, different model", "llama3.2:latest", "llama3.2"},
		{"tagged reasoning model resolves", "deepseek-r1:latest", "deepseek-r1"},
		{"size-tagged id resolves", "qwen3:30b", "qwen3"},
		// Tag stripping is not a synonym table: these have no catalog entry
		// under any tag, so they still miss.
		{"model absent from catalog still misses", "llama2:latest", ""},
		{"bare name absent from catalog still misses", "llama3:latest", ""},
		{"version not in catalog still misses", "deepseek-v3.1:671b-cloud", ""},
		{"untagged unknown still misses", "not-a-real-model", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Lookup("ollama", tt.modelID)
			if tt.wantID == "" {
				assert.Nil(t, got, "expected %q to be absent from the catalog", tt.modelID)
				return
			}
			require.NotNil(t, got, "expected %q to resolve", tt.modelID)
			assert.Equal(t, tt.wantID, got.Id)
		})
	}
}

// TestLookupTagFallbackTiers is the end-to-end assertion the leveling executor
// depends on: a tagged Ollama model must classify as TierLocal, not TierUnknown,
// because TierUnknown short-circuits leveling entirely.
func TestLookupTagFallbackTiers(t *testing.T) {
	tests := []struct {
		modelID string
		want    ModelTier
	}{
		{"llama3.1", TierLocal},
		{"llama3.1:latest", TierLocal},
		{"llama3.2:latest", TierLocal},
		{"deepseek-r1:latest", TierLocal},
		{"llama2:latest", TierUnknown},
		{"deepseek-v3.1:671b-cloud", TierUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			assert.Equal(t, tt.want, TierOf("ollama", tt.modelID))
		})
	}
}

// TestLookupExactMatchWinsOverTagStrip is the no-regression guarantee: the
// fallback runs only after an exact miss, so a source holding the exact tagged
// ID is preferred over the bare-name catalog entry.
func TestLookupExactMatchWinsOverTagStrip(t *testing.T) {
	tagged := &loomv1.ModelInfo{
		Id:                  "llama3.1:latest",
		Provider:            "ollama",
		CostPer_1MInputUsd:  0,
		CostPer_1MOutputUsd: 0,
		ContextWindow:       999,
	}
	Register(MultiSource{newFakeSource(tagged), StaticSource()})
	t.Cleanup(func() { Register(nil) })

	got := Lookup("ollama", "llama3.1:latest")
	require.NotNil(t, got)
	assert.Equal(t, "llama3.1:latest", got.Id, "exact match must win over the tag-stripped fallback")
	assert.Equal(t, int32(999), got.ContextWindow)

	// The bare name still resolves from the static fallback entry.
	bare := Lookup("ollama", "llama3.1")
	require.NotNil(t, bare)
	assert.Equal(t, "llama3.1", bare.Id)
}

// TestLookupExactMatchPreferredAcrossChainOrder pins why the fallback lives in
// the package-level Lookup rather than inside a Source: with per-source
// stripping, a static entry earlier in the chain would shadow a later exact
// match. The two passes over the whole chain make that impossible.
func TestLookupExactMatchPreferredAcrossChainOrder(t *testing.T) {
	tagged := &loomv1.ModelInfo{
		Id:                  "llama3.1:latest",
		Provider:            "ollama",
		CostPer_1MOutputUsd: 0,
		ContextWindow:       777,
	}
	// StaticSource first: it holds the bare "llama3.1" that a stripping Source
	// would have matched before the chain ever reached the tagged entry.
	Register(MultiSource{StaticSource(), newFakeSource(tagged)})
	t.Cleanup(func() { Register(nil) })

	got := Lookup("ollama", "llama3.1:latest")
	require.NotNil(t, got)
	assert.Equal(t, int32(777), got.ContextWindow, "exact match must win regardless of chain order")
}

// TestLookupFallbackDoesNotDoubleQueryOnHit documents the cost of the fallback:
// zero extra source queries when the exact ID resolves, exactly one extra when
// it does not.
func TestLookupFallbackDoesNotDoubleQueryOnHit(t *testing.T) {
	src := newFakeSource(&loomv1.ModelInfo{Id: "m1", Provider: "p"})
	Register(src)
	t.Cleanup(func() { Register(nil) })

	require.NotNil(t, Lookup("p", "m1"))
	assert.Equal(t, int64(1), src.lookupCnt.Load(), "exact hit must not trigger the fallback pass")

	tagged := Lookup("p", "m1:tag")
	require.NotNil(t, tagged, "the fallback pass should resolve the bare id")
	assert.Equal(t, "m1", tagged.Id)
	assert.Equal(t, int64(3), src.lookupCnt.Load(), "a tagged lookup costs exactly one extra query")

	assert.Nil(t, Lookup("p", "untagged-miss"))
	assert.Equal(t, int64(4), src.lookupCnt.Load(), "an untagged miss must not trigger the fallback pass")

	assert.Nil(t, Lookup("p", "tagged-miss:tag"))
	assert.Equal(t, int64(6), src.lookupCnt.Load(), "a tagged miss costs exactly two queries and no more")
}

// TestLookupTaggedZeroAllocs pins that the fallback path stays allocation-free,
// since the leveling executor calls TierOf on the request path.
func TestLookupTaggedZeroAllocs(t *testing.T) {
	if got := TierOf("ollama", "llama3.1:latest"); got != TierLocal {
		t.Fatalf("warmup tier = %v, want %v", got, TierLocal)
	}
	allocs := testing.AllocsPerRun(100, func() {
		_ = TierOf("ollama", "llama3.1:latest")
	})
	assert.Zero(t, allocs, "the tag-stripping fallback should not allocate")
}

func TestLookupTagFallbackHonorsProviderNormalization(t *testing.T) {
	tagged := &loomv1.ModelInfo{Id: "gpt-4.1", Provider: "azure-openai"}
	Register(newFakeSource(tagged))
	t.Cleanup(func() { Register(nil) })

	got := Lookup("azureopenai", "gpt-4.1:v1")
	require.NotNil(t, got, "provider normalization must still apply on the fallback pass")
	assert.Equal(t, "gpt-4.1", got.Id)
}

func BenchmarkLookupTaggedFallback(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = TierOf("ollama", "llama3.1:latest")
	}
}

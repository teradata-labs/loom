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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestCatalogSource_SkipsNilModelEntries pins that a nil entry in a provider's
// model slice is skipped rather than stored. catalog.Lookup callers dereference
// what a Source returns, so a nil that made it into the snapshot would turn a
// registry bookkeeping slip into a panic in tier resolution.
func TestCatalogSource_SkipsNilModelEntries(t *testing.T) {
	reg := &ModelRegistry{
		models: map[string][]*loomv1.ModelInfo{
			"ollama": {
				nil,
				{Id: "llama3.1:latest", Provider: "ollama"},
				nil,
				{Id: "deepseek-r1:latest", Provider: "ollama"},
			},
		},
	}

	src := reg.CatalogSource()
	ctx := context.Background()

	require.NotNil(t, src.Lookup(ctx, "ollama", "llama3.1:latest"))
	require.NotNil(t, src.Lookup(ctx, "ollama", "deepseek-r1:latest"))

	listed := src.List(ctx)
	require.Len(t, listed["ollama"], 2, "nil entries must not reach the snapshot")
	for i, m := range listed["ollama"] {
		assert.NotNil(t, m, "listed entry %d must not be nil", i)
	}
}

// TestCatalogSource_EmptyRegistryServesNothing pins the empty case: a Source over
// no models must answer every lookup with nil rather than panicking on a missing
// per-provider map.
func TestCatalogSource_EmptyRegistryServesNothing(t *testing.T) {
	src := (&ModelRegistry{models: map[string][]*loomv1.ModelInfo{}}).CatalogSource()
	ctx := context.Background()

	assert.Nil(t, src.Lookup(ctx, "ollama", "llama3.1:latest"))
	assert.Nil(t, src.Lookup(ctx, "", ""))
	assert.Empty(t, src.List(ctx))
}

// TestCatalogSource_NormalizesProviderKeys pins that provider keys go through
// catalog.NormalizeProvider on both sides of the snapshot, so the registry's
// "azureopenai" spelling and a caller's "azure-openai" reach the same entries —
// and that a registry holding both spellings collapses into one bucket rather
// than shadowing half its models.
func TestCatalogSource_NormalizesProviderKeys(t *testing.T) {
	reg := &ModelRegistry{
		models: map[string][]*loomv1.ModelInfo{
			"azureopenai":  {{Id: "gpt-4o", Provider: "azure-openai"}},
			"azure-openai": {{Id: "gpt-4o-mini", Provider: "azure-openai"}},
			"ollama":       {{Id: "llama3.1:latest", Provider: "ollama"}},
		},
	}

	src := reg.CatalogSource()
	ctx := context.Background()

	for _, spelling := range []string{"azureopenai", "azure-openai"} {
		assert.NotNil(t, src.Lookup(ctx, spelling, "gpt-4o"), "spelling %q", spelling)
		assert.NotNil(t, src.Lookup(ctx, spelling, "gpt-4o-mini"), "spelling %q", spelling)
	}
	assert.Len(t, src.List(ctx)["azure-openai"], 2,
		"both registry spellings land in the normalized bucket")

	require.NotNil(t, src.Lookup(ctx, "ollama", "llama3.1:latest"))
	assert.Nil(t, src.Lookup(ctx, "ollama", "llama3.1"),
		"the Source matches model IDs exactly; tag tolerance belongs to catalog.Lookup")
}

// TestCatalogSource_SnapshotDeepCopiesEntries pins that the snapshot holds copies
// rather than the registry's own messages, so mutating a registry entry after the
// Source is built cannot race with a concurrent Lookup.
func TestCatalogSource_SnapshotDeepCopiesEntries(t *testing.T) {
	entry := &loomv1.ModelInfo{Id: "llama3.1:latest", Provider: "ollama", Name: "before"}
	reg := &ModelRegistry{models: map[string][]*loomv1.ModelInfo{"ollama": {entry}}}

	src := reg.CatalogSource()
	entry.Name = "after"

	got := src.Lookup(context.Background(), "ollama", "llama3.1:latest")
	require.NotNil(t, got)
	assert.Equal(t, "before", got.Name, "the snapshot must not alias the registry's messages")
}

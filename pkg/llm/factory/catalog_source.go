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
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	"google.golang.org/protobuf/proto"
)

// ModelRegistry and the package-level catalog are two separate inventories:
// DiscoverOllamaModels writes into the registry's own map, which nothing outside
// the registry reads, while catalog.Lookup (and everything derived from it, such
// as catalog.TierOf) reads whichever catalog.Source is registered. The functions
// below are the supported bridge between them. They are explicit and opt-in on
// purpose: no Loom process performs Ollama discovery unless a caller asks for it,
// so importing this package never causes network I/O to localhost:11434.

// snapshotSource is an immutable catalog.Source over a fixed set of ModelInfo
// entries, indexed by normalized provider and exact model ID. It is built once
// and never mutated afterwards, which is what makes it safe for the concurrent
// use catalog.Source requires — the registry's own map has no lock and must not
// be aliased into a Source.
type snapshotSource struct {
	byID   map[string]map[string]*loomv1.ModelInfo
	byList map[string][]*loomv1.ModelInfo
}

// newSnapshotSource deep-copies models into an immutable Source. Provider keys
// are normalized so lookups agree with catalog.NormalizeProvider.
func newSnapshotSource(models map[string][]*loomv1.ModelInfo) *snapshotSource {
	s := &snapshotSource{
		byID:   make(map[string]map[string]*loomv1.ModelInfo, len(models)),
		byList: make(map[string][]*loomv1.ModelInfo, len(models)),
	}
	for provider, entries := range models {
		provider = catalog.NormalizeProvider(provider)
		byID, ok := s.byID[provider]
		if !ok {
			byID = make(map[string]*loomv1.ModelInfo, len(entries))
			s.byID[provider] = byID
		}
		for _, m := range entries {
			if m == nil {
				continue
			}
			cloned, ok := proto.Clone(m).(*loomv1.ModelInfo)
			if !ok {
				continue
			}
			byID[cloned.Id] = cloned
			s.byList[provider] = append(s.byList[provider], cloned)
		}
	}
	return s
}

// Lookup implements catalog.Source with an exact match on the model ID, the
// contract every Source follows. Tag tolerance for IDs like "llama3.1:latest"
// lives in catalog.Lookup, which retries the whole chain with the tag stripped
// only after an exact miss — a Source that stripped tags itself could shadow an
// exact match held by another entry in the chain.
func (s *snapshotSource) Lookup(_ context.Context, provider, modelID string) *loomv1.ModelInfo {
	if byID, ok := s.byID[catalog.NormalizeProvider(provider)]; ok {
		return byID[modelID]
	}
	return nil
}

// List implements catalog.Source.
func (s *snapshotSource) List(_ context.Context) map[string][]*loomv1.ModelInfo {
	return s.byList
}

// CatalogSource returns an immutable catalog.Source over a snapshot of this
// registry's models, taken at call time. Later calls to DiscoverOllamaModels do
// not affect an already-returned Source; call CatalogSource again to pick up
// changes.
func (r *ModelRegistry) CatalogSource() catalog.Source {
	return newSnapshotSource(r.models)
}

// OllamaCatalogSource queries the Ollama instance at endpoint and returns a
// catalog.Source carrying one entry per installed model, keyed on the full
// tagged ID Ollama reports ("llama3.1:latest"). An empty endpoint resolves the
// same way DiscoverOllamaModels resolves it: OLLAMA_ENDPOINT, then
// OLLAMA_BASE_URL, then http://localhost:11434.
//
// The returned Source covers the "ollama" provider only, so composing it ahead
// of catalog.StaticSource() cannot shadow any other provider's entries. Because
// discovered models are priced 0/0, catalog.TierOf classifies them as
// catalog.TierLocal.
//
// An unreachable Ollama, a non-200 response, or an undecodable body returns an
// error and no Source. If Ollama is reachable but reports no installed models,
// DiscoverOllamaModels keeps the built-in Ollama defaults, and the returned
// Source mirrors those bare-name defaults rather than being empty.
func OllamaCatalogSource(endpoint string) (catalog.Source, error) {
	reg := NewModelRegistry()
	if err := reg.DiscoverOllamaModels(endpoint); err != nil {
		return nil, fmt.Errorf("ollama catalog source: %w", err)
	}
	return newSnapshotSource(map[string][]*loomv1.ModelInfo{
		"ollama": reg.models["ollama"],
	}), nil
}

// RegisterOllamaCatalogSource discovers the models installed on the Ollama
// instance at endpoint and installs them as the package-level catalog source,
// ahead of the built-in static catalog. After a successful call,
// catalog.Lookup("ollama", "llama3.1:latest") resolves and catalog.TierOf
// reports catalog.TierLocal for it, which is what makes capability leveling
// activate on real Ollama models.
//
// This is opt-in and performs one HTTP request; call it once during startup of a
// process that wants live Ollama metadata. It replaces the registered source
// with catalog.MultiSource{discovered, catalog.StaticSource()} rather than
// wrapping whatever is already registered, so repeated calls re-discover instead
// of growing an ever-longer chain. A caller that has its own source to preserve
// should use OllamaCatalogSource and build the chain itself:
//
//	src, err := factory.OllamaCatalogSource("")
//	if err == nil {
//	    catalog.Register(catalog.MultiSource{src, myDBSource, catalog.StaticSource()})
//	}
//
// On error nothing is registered and the previous source stays in place, so a
// machine without Ollama keeps the built-in catalog.
func RegisterOllamaCatalogSource(endpoint string) error {
	src, err := OllamaCatalogSource(endpoint)
	if err != nil {
		return err
	}
	catalog.Register(catalog.MultiSource{src, catalog.StaticSource()})
	return nil
}

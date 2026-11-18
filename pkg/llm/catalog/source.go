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

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Source is a dynamic model-metadata provider. Implementations may back their
// data with the built-in static map, a database, a remote service, a config
// file, etc. Register a Source at program startup to extend the package-level
// Lookup function beyond the built-in catalog.
//
// Implementations must be safe for concurrent use.
type Source interface {
	// Lookup returns the ModelInfo for (provider, modelID), or nil when the
	// pair is not in this source. Callers fall back to the next source in
	// the chain on a nil return.
	Lookup(ctx context.Context, provider, modelID string) *loomv1.ModelInfo

	// List returns every known ModelInfo grouped by provider. A Source that
	// cannot enumerate (e.g. a lazy remote lookup) may return nil or an
	// empty map; callers must tolerate either.
	List(ctx context.Context) map[string][]*loomv1.ModelInfo
}

// StaticSource returns a Source backed by the built-in catalog map returned
// by BuildCatalog. Use this as the last (fallback) entry in a MultiSource so
// dynamic sources can override or extend the built-in entries.
func StaticSource() Source { return staticSource{} }

type staticSource struct{}

func (staticSource) Lookup(_ context.Context, provider, modelID string) *loomv1.ModelInfo {
	provider = NormalizeProvider(provider)
	if byID, ok := staticIndex()[provider]; ok {
		return byID[modelID]
	}
	return nil
}

func (staticSource) List(_ context.Context) map[string][]*loomv1.ModelInfo {
	return staticList()
}

// Memoized catalog indexes used by staticSource. BuildCatalog() itself still
// returns a fresh map on every call (part of its public contract), but
// staticSource is read-only and lives on a request-serving hot path, so we
// pay the allocation and O(n) scan once per process.
var (
	staticCacheOnce sync.Once
	staticByID      map[string]map[string]*loomv1.ModelInfo
	staticByList    map[string][]*loomv1.ModelInfo
)

func loadStaticCache() {
	staticCacheOnce.Do(func() {
		staticByList = BuildCatalog()
		staticByID = make(map[string]map[string]*loomv1.ModelInfo, len(staticByList))
		for provider, models := range staticByList {
			byID := make(map[string]*loomv1.ModelInfo, len(models))
			for _, m := range models {
				if m == nil {
					continue
				}
				byID[m.Id] = m
			}
			staticByID[provider] = byID
		}
	})
}

func staticIndex() map[string]map[string]*loomv1.ModelInfo {
	loadStaticCache()
	return staticByID
}

// staticList returns the memoized provider→models map used by staticSource.List.
// Source.List is documented as read-only for callers; we rely on that contract
// rather than defensively deep-copying on every call.
func staticList() map[string][]*loomv1.ModelInfo {
	loadStaticCache()
	return staticByList
}

// Default source state. Writes happen via Register and RegisterNamed
// (intended for program startup and periodic refreshes); reads happen on
// every Lookup call.
//
// The effective default is MultiSource{named sources in registration order...,
// base}. Network-discovered catalogs (Ollama's installed models, the Vercel AI
// Gateway's /v1/models) register by name so each can be refreshed without
// evicting the others; Register sets the base and drops every named source.
var (
	defaultMu     sync.RWMutex
	baseSource    Source = staticSource{}
	namedSources  []namedSource
	defaultSource Source = staticSource{}
)

type namedSource struct {
	name string
	src  Source
}

// rebuildLocked recomputes defaultSource from base and the named sources.
// defaultMu must be held for writing.
func rebuildLocked() {
	if len(namedSources) == 0 {
		defaultSource = baseSource
		return
	}
	chain := make(MultiSource, 0, len(namedSources)+1)
	for _, n := range namedSources {
		chain = append(chain, n.src)
	}
	defaultSource = append(chain, baseSource)
}

// Register replaces the package-level default source used by Lookup, and
// removes every source added with RegisterNamed. The new source is consulted
// by every subsequent call into the package-level Lookup, including calls
// from factory and agent that were previously hardcoded to the built-in
// catalog.
//
// Typical usage at program startup:
//
//	catalog.Register(catalog.MultiSource{
//	    catalog.NewCachedSource(myDBSource, 5*time.Minute),
//	    catalog.StaticSource(),
//	})
//
// Calling Register(nil) restores the built-in StaticSource.
func Register(s Source) {
	if s == nil {
		s = staticSource{}
	}
	defaultMu.Lock()
	baseSource = s
	namedSources = nil
	rebuildLocked()
	defaultMu.Unlock()
}

// RegisterNamed installs src ahead of the base source under name. A second
// call with the same name replaces that source in place (its position in the
// chain is kept), so a periodic refresh never grows the chain. Different
// names stack: the earliest registered is consulted first. RegisterNamed(name,
// nil) removes the named source.
func RegisterNamed(name string, src Source) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	for i, n := range namedSources {
		if n.name != name {
			continue
		}
		if src == nil {
			namedSources = append(namedSources[:i:i], namedSources[i+1:]...)
		} else {
			namedSources[i].src = src
		}
		rebuildLocked()
		return
	}
	if src == nil {
		return
	}
	namedSources = append(namedSources, namedSource{name: name, src: src})
	rebuildLocked()
}

// DefaultSource returns the currently registered default Source. Tests and
// advanced callers can use this to compose on top of whatever has already
// been registered.
func DefaultSource() Source {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultSource
}

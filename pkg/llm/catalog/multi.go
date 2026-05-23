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

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// MultiSource is an ordered chain of Sources. Lookup returns the first
// non-nil result; List merges every underlying source, with earlier entries
// winning on (provider, modelID) collisions so dynamic sources can shadow
// built-in entries.
//
// MultiSource itself holds no state; each entry is consulted on every call.
// Wrap individual entries in CachedSource to add per-Source caching.
type MultiSource []Source

// Lookup returns the first non-nil ModelInfo found across the chain.
func (m MultiSource) Lookup(ctx context.Context, provider, modelID string) *loomv1.ModelInfo {
	for _, s := range m {
		if s == nil {
			continue
		}
		if info := s.Lookup(ctx, provider, modelID); info != nil {
			return info
		}
	}
	return nil
}

// List returns the union of every Source's catalog, with earlier entries in
// the chain taking precedence for duplicate (provider, modelID) pairs.
// Provider keys are normalized before deduplication so "azureopenai" and
// "azure-openai" entries merge cleanly.
func (m MultiSource) List(ctx context.Context) map[string][]*loomv1.ModelInfo {
	out := map[string][]*loomv1.ModelInfo{}
	seen := map[string]map[string]bool{}

	for _, s := range m {
		if s == nil {
			continue
		}
		for provider, models := range s.List(ctx) {
			provider = NormalizeProvider(provider)
			if _, ok := seen[provider]; !ok {
				seen[provider] = map[string]bool{}
			}
			for _, mi := range models {
				if mi == nil || seen[provider][mi.Id] {
					continue
				}
				out[provider] = append(out[provider], mi)
				seen[provider][mi.Id] = true
			}
		}
	}
	return out
}

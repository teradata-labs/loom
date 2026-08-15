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
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Model tiering is a pure mapping over fields that already exist on
// loomv1.ModelInfo (per-token pricing and IsReasoning). It introduces no new
// store, no config file, and nothing persisted: a tier is recomputed from the
// resolved ModelInfo on every call.
//
// Because TierOf goes through the package-level Lookup, any dynamic Source
// installed with Register — a DB-backed catalog, a CachedSource, the models
// added by DiscoverOllamaModels — is honored automatically. Tiers are
// deliberately not cached here: a tier cache would go stale the moment a
// caller swaps the source, and the underlying static Lookup is already
// memoized behind sync.Once (see source.go), so TierOf on the static path is
// two map reads under an RWMutex read.

// ModelTier classifies a catalog model by capability class, derived purely
// from existing catalog fields (pricing, IsReasoning). Ordinal ordering is
// meaningful: higher values are more capable tiers.
type ModelTier int

const (
	// TierUnknown means the model is not in the catalog, so no tier can be
	// derived. Callers decide their own fallback.
	TierUnknown ModelTier = iota
	// TierLocal is a self-hosted model with zero per-token cost (e.g. models
	// served by Ollama).
	TierLocal
	// TierSmallOpen is a cheap hosted model without reasoning.
	TierSmallOpen
	// TierMid is a reasoning model priced below the frontier threshold, or a
	// non-reasoning model at mid-range pricing.
	TierMid
	// TierFrontier is a reasoning model at frontier pricing.
	TierFrontier
)

// Derivation thresholds, expressed in USD per 1M output tokens. Output cost is
// the discriminator rather than input cost because it spreads the tiers apart
// more clearly across providers.
const (
	// FrontierMinOutputCostUSD is the output price at or above which a
	// reasoning model is treated as frontier. Stated in $/1M output tokens.
	FrontierMinOutputCostUSD = 10.0
	// MidMinOutputCostUSD is the output price at or above which a
	// non-reasoning model is treated as mid rather than small-open. Stated in
	// $/1M output tokens.
	MidMinOutputCostUSD = 1.5
)

// String returns the lowercase hyphenated tier name. Values outside the
// defined range render as "unknown".
func (t ModelTier) String() string {
	switch t {
	case TierLocal:
		return "local"
	case TierSmallOpen:
		return "small-open"
	case TierMid:
		return "mid"
	case TierFrontier:
		return "frontier"
	case TierUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

// TierOf returns the capability tier for (provider, modelID) using the
// package-level catalog Lookup, which applies NormalizeProvider and consults
// whichever Source is currently registered. Returns TierUnknown when the pair
// is not cataloged. On the static-source path this is one memoized lookup and
// allocates nothing.
func TierOf(provider, modelID string) ModelTier {
	return TierFromInfo(Lookup(provider, modelID))
}

// TierFromInfo derives the tier from an already-resolved ModelInfo, for
// callers that hold one and want to avoid a second lookup. A nil info returns
// TierUnknown.
//
// The rules are applied in order, and the order matters: a zero-cost model is
// local even when it advertises reasoning (a self-hosted deepseek-r1 is a
// local model, not a frontier one), and an expensive non-reasoning model lands
// in mid rather than frontier because frontier requires reasoning.
func TierFromInfo(info *loomv1.ModelInfo) ModelTier {
	if info == nil {
		return TierUnknown
	}
	// The catalog uses 0/0 pricing exclusively for self-hosted models.
	if info.CostPer_1MInputUsd == 0 && info.CostPer_1MOutputUsd == 0 {
		return TierLocal
	}
	if info.IsReasoning && info.CostPer_1MOutputUsd >= FrontierMinOutputCostUSD {
		return TierFrontier
	}
	if info.IsReasoning || info.CostPer_1MOutputUsd >= MidMinOutputCostUSD {
		return TierMid
	}
	return TierSmallOpen
}

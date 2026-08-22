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

// ParseModelTier converts a tier name back into a ModelTier. It is the exact
// inverse of ModelTier.String(): the accepted inputs are "unknown", "local",
// "small-open", "mid" and "frontier". Matching is exact — no case folding and
// no whitespace trimming, so callers own any normalization. Any other input
// returns (TierUnknown, false), which lets callers distinguish an explicit
// "unknown" from an unrecognized string.
func ParseModelTier(s string) (ModelTier, bool) {
	switch s {
	case "unknown":
		return TierUnknown, true
	case "local":
		return TierLocal, true
	case "small-open":
		return TierSmallOpen, true
	case "mid":
		return TierMid, true
	case "frontier":
		return TierFrontier, true
	default:
		return TierUnknown, false
	}
}

// TierThresholds carries the pricing cutoffs used to derive a tier, expressed
// in USD per 1M output tokens. A zero or negative field means "use the
// built-in constant" (FrontierMinOutputCostUSD, MidMinOutputCostUSD), so the
// zero value behaves exactly like DefaultTierThresholds.
//
// A consequence of that rule: a threshold of 0 cannot be expressed. A caller
// who wants every priced model to clear a cutoff passes a small positive
// number (e.g. 0.0001) rather than 0. This is deliberate — it keeps the zero
// value usable as "defaults" so callers embedding TierThresholds in a config
// struct do not have to populate both fields to get the built-in behavior.
type TierThresholds struct {
	// FrontierMinOutputCostUSD is the output price at or above which a
	// reasoning model is treated as frontier.
	FrontierMinOutputCostUSD float64
	// MidMinOutputCostUSD is the output price at or above which a
	// non-reasoning model is treated as mid rather than small-open.
	MidMinOutputCostUSD float64
}

// DefaultTierThresholds returns the built-in cutoffs, FrontierMinOutputCostUSD
// and MidMinOutputCostUSD.
func DefaultTierThresholds() TierThresholds {
	return TierThresholds{
		FrontierMinOutputCostUSD: FrontierMinOutputCostUSD,
		MidMinOutputCostUSD:      MidMinOutputCostUSD,
	}
}

// withDefaults substitutes the built-in constant for each non-positive field.
// It returns a value, so it allocates nothing.
func (th TierThresholds) withDefaults() TierThresholds {
	if th.FrontierMinOutputCostUSD <= 0 {
		th.FrontierMinOutputCostUSD = FrontierMinOutputCostUSD
	}
	if th.MidMinOutputCostUSD <= 0 {
		th.MidMinOutputCostUSD = MidMinOutputCostUSD
	}
	return th
}

// TierOf returns the capability tier for (provider, modelID) using the
// package-level catalog Lookup, which applies NormalizeProvider and consults
// whichever Source is currently registered. Returns TierUnknown when the pair
// is not cataloged. On the static-source path this is one memoized lookup and
// allocates nothing.
func TierOf(provider, modelID string) ModelTier {
	return TierOfWith(provider, modelID, DefaultTierThresholds())
}

// TierOfWith is TierOf with caller-supplied thresholds. Non-positive fields in
// th fall back to the built-in constants, so a zero-value TierThresholds gives
// the same answers as TierOf. Like TierOf it allocates nothing on the
// static-source path.
func TierOfWith(provider, modelID string, th TierThresholds) ModelTier {
	return TierFromInfoWith(Lookup(provider, modelID), th)
}

// TierFromInfo derives the tier from an already-resolved ModelInfo, for
// callers that hold one and want to avoid a second lookup. A nil info returns
// TierUnknown. It is TierFromInfoWith with the built-in thresholds.
func TierFromInfo(info *loomv1.ModelInfo) ModelTier {
	return TierFromInfoWith(info, DefaultTierThresholds())
}

// TierFromInfoWith derives the tier from an already-resolved ModelInfo,
// comparing output price against th rather than the built-in constants.
// Non-positive fields in th fall back to those constants. A nil info returns
// TierUnknown.
//
// The rules are applied in order, and the order matters: a zero-cost model is
// local even when it advertises reasoning (a self-hosted deepseek-r1 is a
// local model, not a frontier one), and an expensive non-reasoning model lands
// in mid rather than frontier because frontier requires reasoning. Thresholds
// shift where the price boundaries fall; they do not change the rules or their
// order.
func TierFromInfoWith(info *loomv1.ModelInfo, th TierThresholds) ModelTier {
	if info == nil {
		return TierUnknown
	}
	th = th.withDefaults()
	// The catalog uses 0/0 pricing exclusively for self-hosted models.
	if info.CostPer_1MInputUsd == 0 && info.CostPer_1MOutputUsd == 0 {
		return TierLocal
	}
	if info.IsReasoning && info.CostPer_1MOutputUsd >= th.FrontierMinOutputCostUSD {
		return TierFrontier
	}
	if info.IsReasoning || info.CostPer_1MOutputUsd >= th.MidMinOutputCostUSD {
		return TierMid
	}
	return TierSmallOpen
}

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

// priceIndex is a provider -> modelID -> {inputPer1M, outputPer1M} lookup built
// once from BuildCatalog(). It is the static fallback behind LookupPricing;
// the registered default Source is consulted first.
var (
	priceOnce  sync.Once
	priceIndex map[string]map[string][2]float64
)

func buildPriceIndex() {
	priceIndex = make(map[string]map[string][2]float64)
	for provider, models := range BuildCatalog() {
		m := make(map[string][2]float64, len(models))
		for _, mi := range models {
			m[mi.Id] = [2]float64{mi.CostPer_1MInputUsd, mi.CostPer_1MOutputUsd}
		}
		priceIndex[provider] = m
	}
}

// LookupPricing returns the per-million-token input/output cost in USD for a
// model. found is false when the provider is unknown or the model is not
// cataloged anywhere (the caller should then use its own fallback).
//
// The registered default Source (see Register) is consulted first, so an
// embedder that registers a DB- or gateway-backed catalog prices its models
// with the rates it actually pays — previously this read only the static
// built-in table, and every provider client's calculateCost silently fell to
// its hardcoded default for any id the static table did not list (a LiteLLM
// gateway alias, for example, was billed at gpt-4o rates whatever it was).
// A registered entry with no positive rate is treated as "unpriced" and the
// static table is tried next, so a metadata-only row cannot zero out a price.
//
// Rates returned here are base (global-endpoint) rates, whichever Source
// supplied them. The Bedrock client multiplies them by
// BedrockEndpointMultiplier, which adds a 10% premium for regional endpoints
// on Claude 4.5 and later — so an embedder registering Bedrock rates must
// register the global rate, not the regional one, or the premium is counted
// twice.
//
// The lookup is keyed by both provider and model id because the same id can
// appear under multiple providers (e.g. "gpt-4.1" under openai and azure-openai)
// with different pricing.
func LookupPricing(provider, modelID string) (inputPer1M, outputPer1M float64, found bool) {
	inputPer1M, outputPer1M, _, found = lookupPricing(provider, modelID)
	return inputPer1M, outputPer1M, found
}

// lookupPricing is LookupPricing plus where the rates came from: published is
// true when they are the built-in catalog's row (served by the registered
// chain's StaticSource or by the static fallback below), false when a
// registered Source supplied its own row.
func lookupPricing(provider, modelID string) (inputPer1M, outputPer1M float64, published, found bool) {
	provider = NormalizeProvider(provider)
	if info := DefaultSource().Lookup(context.Background(), provider, modelID); info != nil {
		if info.CostPer_1MInputUsd > 0 || info.CostPer_1MOutputUsd > 0 {
			return info.CostPer_1MInputUsd, info.CostPer_1MOutputUsd, isStaticEntry(provider, modelID, info), true
		}
	}
	priceOnce.Do(buildPriceIndex)
	if models, ok := priceIndex[provider]; ok {
		if p, ok := models[modelID]; ok {
			return p[0], p[1], true, true
		}
	}
	return 0, 0, false, false
}

// isStaticEntry reports whether info is the built-in catalog's own row for
// (provider, modelID). StaticSource hands out the memoized row itself, and
// CachedSource and MultiSource pass it through, so identity holds whenever the
// built-in table answered — however the registered chain is composed. provider
// must already be normalized.
func isStaticEntry(provider, modelID string, info *loomv1.ModelInfo) bool {
	byID, ok := staticIndex()[provider]
	return ok && byID[modelID] == info
}

// RequestPricing returns the per-million-token input/output rates in USD to
// bill one request whose prompt is promptTokens long — every input token of
// the request, cache reads and cache writes included. found is false when
// neither source below prices the model; the caller then applies its own
// default.
//
// Rates resolve as LookupPricing(provider, modelID) first, then
// ClaudeFamilyPricing. When they are Anthropic's published card — the
// built-in catalog's row or the family fallback — the published long-context
// card replaces them above its threshold (ClaudeLongContextPricing), carrying
// any premium the row holds over the first-party card (scaleLongContextCard):
// a bedrock-mantle row at first-party × 1.10 gets the long-context card
// × 1.10. A rate a registered Source supplies is the embedder's pricing
// authority and is returned unchanged at every prompt length: the
// long-context card is part of the published price list, not of an
// embedder's negotiated or gateway rate, and a Source row has no field to
// express a long-context rate of its own.
//
// This is the one rate resolution shared by the anthropic, bedrock and
// openai clients' cost estimates.
func RequestPricing(provider, modelID string, promptTokens int) (inputPer1M, outputPer1M float64, found bool) {
	inputPer1M, outputPer1M, published, found := lookupPricing(provider, modelID)
	if !found {
		inputPer1M, outputPer1M, found = ClaudeFamilyPricing(modelID)
		published = found
	}
	if published {
		if in, out, long := ClaudeLongContextPricing(modelID, promptTokens); long {
			inputPer1M, outputPer1M = scaleLongContextCard(modelID, inputPer1M, outputPer1M, in, out)
		}
	}
	return inputPer1M, outputPer1M, found
}

// scaleLongContextCard returns the long-context card (longIn, longOut) scaled
// by the ratio of a published row's base rates (rowIn, rowOut) to the model's
// first-party card (ClaudeFamilyPricing). A row that is the first-party card
// (the anthropic namespace, a family-fallback id, a bedrock row at the global
// rate) has ratio 1 and gets the card unchanged. A row that already includes a
// premium over the card (the bedrock-mantle rows, first-party × 1.10) gets the
// same premium on the long-context card. Each bucket is scaled by its own
// ratio. With no family card to compare against, the card is returned as is.
func scaleLongContextCard(modelID string, rowIn, rowOut, longIn, longOut float64) (float64, float64) {
	cardIn, cardOut, ok := ClaudeFamilyPricing(modelID)
	if !ok || cardIn <= 0 || cardOut <= 0 {
		return longIn, longOut
	}
	return longIn * (rowIn / cardIn), longOut * (rowOut / cardOut)
}

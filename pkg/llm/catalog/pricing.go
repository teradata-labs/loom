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
// The lookup is keyed by both provider and model id because the same id can
// appear under multiple providers (e.g. "gpt-4.1" under openai and azure-openai)
// with different pricing.
func LookupPricing(provider, modelID string) (inputPer1M, outputPer1M float64, found bool) {
	provider = NormalizeProvider(provider)
	if info := DefaultSource().Lookup(context.Background(), provider, modelID); info != nil {
		if info.CostPer_1MInputUsd > 0 || info.CostPer_1MOutputUsd > 0 {
			return info.CostPer_1MInputUsd, info.CostPer_1MOutputUsd, true
		}
	}
	priceOnce.Do(buildPriceIndex)
	if models, ok := priceIndex[provider]; ok {
		if p, ok := models[modelID]; ok {
			return p[0], p[1], true
		}
	}
	return 0, 0, false
}

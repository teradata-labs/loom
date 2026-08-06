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

import "sync"

// priceIndex is a provider -> modelID -> {inputPer1M, outputPer1M} lookup built
// once from BuildCatalog(). The catalog is the single source of truth for
// pricing; calculateCost in each provider client consults this before falling
// back to any provider-local rates.
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
// model in the static catalog. found is false when the provider is unknown or
// the model is not cataloged (the caller should then use its own fallback).
//
// The lookup is keyed by both provider and model id because the same id can
// appear under multiple providers (e.g. "gpt-4.1" under openai and azure-openai)
// with different pricing.
func LookupPricing(provider, modelID string) (inputPer1M, outputPer1M float64, found bool) {
	priceOnce.Do(buildPriceIndex)
	if models, ok := priceIndex[provider]; ok {
		if p, ok := models[modelID]; ok {
			return p[0], p[1], true
		}
	}
	return 0, 0, false
}

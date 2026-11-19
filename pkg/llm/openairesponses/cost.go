// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package openairesponses

import (
	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

// Cache-tier multipliers on the input rate, used only when the catalog does
// not publish cache rates for the model (catalog.CacheRates) and the model is
// not Claude-family (whose multipliers come from pkg/llm/catalog). They are
// OpenAI's default card, the same values pkg/llm/openai falls back to: a
// cached read at 0.5x and no separate write premium.
const (
	defaultCacheReadMultiplier  = 0.5
	defaultCacheWriteMultiplier = 1.0
)

// estimateCost prices a call from the catalog. inputTokens is the raw,
// cache-inclusive input_tokens; cacheRead and cacheWrite are inside it.
// known is false when the catalog has no rate for the model (cost 0).
func (c *Client) estimateCost(inputTokens, outputTokens, cacheRead, cacheWrite int) (usd float64, known bool) {
	inPerM, outPerM, ok := catalog.RequestPricing(c.catalogProvider, c.model, inputTokens)
	if !ok {
		return 0, false
	}
	readPerM, writePerM := inPerM*defaultCacheReadMultiplier, inPerM*defaultCacheWriteMultiplier
	if _, _, claude := catalog.ClaudeFamilyPricing(c.model); claude {
		readPerM = inPerM * catalog.ClaudeCacheReadMultiplier(c.model)
		writePerM = inPerM * catalog.ClaudeCacheWriteMultiplier
	}
	if r, w := catalog.CacheRates(c.catalogProvider, c.model); r > 0 || w > 0 {
		if r > 0 {
			readPerM = r
		}
		if w > 0 {
			writePerM = w
		}
	}
	uncached := max(0, inputTokens-cacheRead-cacheWrite)
	usd = (float64(uncached)*inPerM +
		float64(cacheRead)*readPerM +
		float64(cacheWrite)*writePerM +
		float64(outputTokens)*outPerM) / 1_000_000
	return usd, true
}

// finishCost applies the unpriced rule: with no reported cost and no catalog
// rate, the call is flagged llm.cost_unpriced (CostUSD 0 means unknown). A
// ReportUnpriced client then estimates it at the median rate of the same
// creator's catalog models (catalog.CreatorMedianPricing) and flags
// llm.cost_estimated. It returns md, allocated when nil.
func (c *Client) finishCost(usage *llmtypes.Usage, reportedCost float64, md map[string]interface{}) map[string]interface{} {
	if reportedCost > 0 {
		return md
	}
	prompt := usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	if _, _, ok := catalog.RequestPricing(c.catalogProvider, c.model, prompt); ok {
		return md
	}
	if md == nil {
		md = make(map[string]interface{}, 2)
	}
	md[llm.MetadataCostUnpriced] = true
	usage.CostUSD = 0
	if !c.reportUnpriced {
		return md
	}
	inPerM, outPerM, ok := catalog.CreatorMedianPricing(c.catalogProvider, c.model)
	if !ok {
		return md
	}
	usage.CostUSD = (float64(usage.InputTokens)*inPerM +
		float64(usage.CacheReadInputTokens)*inPerM*defaultCacheReadMultiplier +
		float64(usage.CacheCreationInputTokens)*inPerM*defaultCacheWriteMultiplier +
		float64(usage.OutputTokens)*outPerM) / 1_000_000
	usage.CostEstimated = true
	md[llm.MetadataCostEstimated] = true
	return md
}

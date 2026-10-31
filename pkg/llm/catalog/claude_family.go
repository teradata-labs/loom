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
	"regexp"
	"strconv"
	"strings"
)

// ClaudeFamilyPricing returns Anthropic's published first-party per-million
// input/output rates for a Claude model id the catalog does not list, matched
// by family substring so it works on every id shape a client sees: native
// ("claude-opus-5"), Bedrock inference profiles ("us.anthropic.claude-opus-5"),
// and gateway aliases ("coding-agent/claude-sonnet-5"). matched is false when
// the id is not recognisably a Claude model — callers then apply their own
// default.
//
// This is the single fallback shared by the anthropic, bedrock and openai
// clients. Each used to carry its own copy of the switch; the copies drifted
// (the openai one never learned Fable and priced it as gpt-4o), which is the
// failure this function exists to prevent.
//
// Order matters: a more specific id must be checked before any id it contains
// — "claude-opus-5-5" before "claude-opus-5", "claude-opus-4-1" before the
// generic "claude-opus", "claude-sonnet-5" before the generic "claude-sonnet".
func ClaudeFamilyPricing(modelID string) (inputPer1M, outputPer1M float64, matched bool) {
	id := strings.ToLower(modelID)
	switch {
	case strings.Contains(id, "claude-fable"), strings.Contains(id, "claude-mythos"):
		return 10.0, 50.0, true // Fable 5 / 5.1 (and the Mythos counterparts)
	// Claude 3 ids name the generation before the family ("claude-3-haiku",
	// not "claude-haiku-3"), so the generic family cases below never match
	// them. Rates: Anthropic's published list prices (anthropic.com/pricing,
	// legacy models); the Claude 3.5 Haiku card also appears in the v1.0.0
	// provider reference (website/content/en/docs/reference/llm-providers.md).
	case strings.Contains(id, "claude-3-5-haiku"):
		return 0.80, 4.0, true
	case strings.Contains(id, "claude-3-haiku"):
		return 0.25, 1.25, true
	case strings.Contains(id, "claude-3-opus"):
		return 15.0, 75.0, true
	case strings.Contains(id, "claude-opus-5-5"):
		return 4.0, 20.0, true
	case strings.Contains(id, "claude-opus-5"):
		return 5.0, 25.0, true
	case strings.Contains(id, "claude-opus-4-1"):
		return 15.0, 75.0, true
	case strings.Contains(id, "claude-opus"):
		// Opus 4.5 through 4.8, and any Opus not yet listed above: never the
		// sonnet rate — a missing entry must not under-price Opus.
		return 5.0, 25.0, true
	case strings.Contains(id, "claude-sonnet-5"):
		return 2.0, 10.0, true
	case strings.Contains(id, "claude-haiku-5"):
		// Prompts up to 100K tokens; ClaudeLongContextPricing has the card above that.
		return 0.10, 0.50, true
	case strings.Contains(id, "claude-haiku"):
		return 1.0, 5.0, true
	case strings.Contains(id, "claude-sonnet"), strings.Contains(id, "claude-3-5-sonnet"):
		return 3.0, 15.0, true // Sonnet 4.x and earlier
	}
	return 0, 0, false
}

// Cache-tier multipliers on a Claude model's base input rate.
const (
	ClaudeCacheWriteMultiplier = 1.25 // 5-minute cache write, every Claude model
	claudeCacheReadDefault     = 0.10
)

// ClaudeCacheReadMultiplier returns the cache-hit price as a fraction of the
// model's base input rate. Anthropic prices it per model: 0.025x on Fable 5.1
// and Mythos 5.1, 0.05x on Opus 5.5 and Sonnet 5.5, and 0.10x on every other
// Claude model (including Fable 5 / Mythos 5).
func ClaudeCacheReadMultiplier(modelID string) float64 {
	id := strings.ToLower(modelID)
	switch {
	case strings.Contains(id, "claude-fable-5-1"), strings.Contains(id, "claude-mythos-5-1"):
		return 0.025
	case strings.Contains(id, "claude-opus-5-5"), strings.Contains(id, "claude-sonnet-5-5"):
		return 0.05
	}
	return claudeCacheReadDefault
}

// claudeLongContextThreshold is the prompt length (all input tokens, cache
// reads and writes included) above which Claude Haiku 5.5 bills its higher card.
const claudeLongContextThreshold = 100_000

// ClaudeLongContextPricing returns the per-million input/output rates that
// replace the base card when a request's prompt exceeds the model's
// long-context threshold. Only Claude Haiku 5.5 is priced by prompt length
// ($0.50 / $2.50 over 100K tokens); every other model returns ok=false.
// promptTokens must count every input token of the request, cache reads and
// cache writes included — each request is priced on its own.
func ClaudeLongContextPricing(modelID string, promptTokens int) (inputPer1M, outputPer1M float64, ok bool) {
	if promptTokens <= claudeLongContextThreshold {
		return 0, 0, false
	}
	if strings.Contains(strings.ToLower(modelID), "claude-haiku-5-5") {
		return 0.50, 2.50, true
	}
	return 0, 0, false
}

const bedrockRegionalPremium = 1.10

// claudeVersionRe captures the major and (optional, 1–2 digit) minor version
// from a dated or undated Claude id. The minor must not be followed by another
// digit, so the date in "claude-sonnet-4-20250514" is not read as a minor.
var claudeVersionRe = regexp.MustCompile(`claude-(?:opus|sonnet|haiku)-(\d+)(?:-(\d{1,2}))?(?:[^0-9]|$)`)

// claudeAtLeast45 reports whether a Claude id names Claude 4.5 or later — the
// generation from which Bedrock prices regional endpoints above global ones.
func claudeAtLeast45(id string) bool {
	if strings.Contains(id, "claude-fable") || strings.Contains(id, "claude-mythos") {
		return true
	}
	m := claudeVersionRe.FindStringSubmatch(id)
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	minor := 0
	if m[2] != "" {
		minor, _ = strconv.Atoi(m[2])
	}
	return major > 4 || (major == 4 && minor >= 5)
}

// BedrockEndpointMultiplier returns the factor Bedrock applies to every token
// bucket (input, output, cache writes, cache reads) for a model id, by endpoint.
// For Claude 4.5 and later, regional endpoints — geographic inference profiles
// ("us.", "eu.", "apac.", …) and in-region ids ("anthropic.…") — carry a 10%
// premium over global endpoints ("global."). Earlier models and non-Claude ids
// return 1.0.
func BedrockEndpointMultiplier(modelID string) float64 {
	id := strings.ToLower(modelID)
	if strings.HasPrefix(id, "global.") || !claudeAtLeast45(id) {
		return 1.0
	}
	return bedrockRegionalPremium
}

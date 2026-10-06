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

import "strings"

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
	case strings.Contains(id, "claude-haiku"):
		return 1.0, 5.0, true
	case strings.Contains(id, "claude-sonnet"), strings.Contains(id, "claude-3-5-sonnet"):
		return 3.0, 15.0, true // Sonnet 4.x and earlier
	}
	return 0, 0, false
}

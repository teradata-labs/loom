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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mantleRegionalPremium is the factor Bedrock applies to Claude 4.5+ on a
// regional (single-Region) endpoint, which is what a bare bedrock-mantle id
// addresses. Anthropic: "Regional endpoints carry a 10 percent pricing
// premium over global endpoints."
const mantleRegionalPremium = 1.10

func TestBedrockMantleNamespace(t *testing.T) {
	models := BuildCatalog()["bedrock-mantle"]
	require.NotEmpty(t, models)

	seen := make(map[string]bool, len(models))
	for _, m := range models {
		assert.Equal(t, "bedrock-mantle", m.Provider)
		assert.False(t, m.ShowInDropdown, "%s is endpoint-configured", m.Id)
		assert.NotContains(t, m.Id, "us.", "Mantle ids carry no inference-profile prefix")
		assert.NotContains(t, m.Id, "global.")
		assert.False(t, seen[m.Id], "duplicate Mantle id %s", m.Id)
		seen[m.Id] = true
		assert.Greater(t, m.CostPer_1MInputUsd, 0.0, "%s must carry a verified input rate", m.Id)
		assert.Greater(t, m.CostPer_1MOutputUsd, 0.0, "%s must carry a verified output rate", m.Id)
		assert.True(t, strings.HasSuffix(m.Name, "(Bedrock Mantle)"), "%s name %q", m.Id, m.Name)
		assert.Contains(t, m.Capabilities, "text", m.Id)
		assert.Contains(t, m.Capabilities, "tool-use", "%s: every Mantle chat model supports client-side tool calling", m.Id)
		if strings.HasPrefix(m.Id, "anthropic.") {
			assert.True(t, m.IsReasoning, "%s: every Claude on Mantle supports thinking", m.Id)
			assert.Contains(t, m.Capabilities, "vision", m.Id)
		}
		// "thinking" and IsReasoning must agree.
		hasThinking := false
		for _, c := range m.Capabilities {
			if c == "thinking" {
				hasThinking = true
			}
		}
		assert.Equal(t, hasThinking, m.IsReasoning, "%s: thinking capability and IsReasoning disagree", m.Id)
	}
}

// Claude on Mantle is Anthropic's first-party card times the regional premium.
// The anthropic namespace holds the first-party card for the ids both share,
// so the relation is asserted against the catalog itself rather than against
// copied numbers.
func TestBedrockMantleClaudeRates_AreRegionalPremiumOverFirstParty(t *testing.T) {
	shared := map[string]string{
		"anthropic.claude-opus-4-7":  "claude-opus-4-7",
		"anthropic.claude-haiku-4-5": "claude-haiku-4-5-20251001",
	}
	for mantleID, anthropicID := range shared {
		mIn, mOut, ok := LookupPricing("bedrock-mantle", mantleID)
		require.True(t, ok, mantleID)
		aIn, aOut, ok := LookupPricing("anthropic", anthropicID)
		require.True(t, ok, anthropicID)
		assert.InDelta(t, aIn*mantleRegionalPremium, mIn, 1e-9, "%s input", mantleID)
		assert.InDelta(t, aOut*mantleRegionalPremium, mOut, 1e-9, "%s output", mantleID)
	}

	// Claude 5.x cards (platform.claude.com/docs/en/about-claude/pricing,
	// read 2026-11-05) × 1.10. The anthropic namespace does not list these
	// ids yet, so the first-party card is spelled out here.
	firstParty := map[string][2]float64{
		"anthropic.claude-fable-5-1":  {10.0, 50.0},
		"anthropic.claude-fable-5":    {10.0, 50.0},
		"anthropic.claude-mythos-5":   {10.0, 50.0},
		"anthropic.claude-opus-5-5":   {4.0, 20.0},
		"anthropic.claude-opus-5":     {5.0, 25.0},
		"anthropic.claude-opus-4-8":   {5.0, 25.0},
		"anthropic.claude-sonnet-5-5": {2.0, 10.0},
		"anthropic.claude-sonnet-5":   {2.0, 10.0},
		"anthropic.claude-haiku-5-5":  {0.10, 0.50},
	}
	for id, card := range firstParty {
		in, out, ok := LookupPricing("bedrock-mantle", id)
		require.True(t, ok, id)
		assert.InDelta(t, card[0]*mantleRegionalPremium, in, 1e-9, "%s input", id)
		assert.InDelta(t, card[1]*mantleRegionalPremium, out, 1e-9, "%s output", id)
	}

	// The Mantle rate is above the global bedrock-runtime rate the bedrock
	// namespace lists, never below it: a missing premium under-prices.
	bIn, bOut, ok := LookupPricing("bedrock", "us.anthropic.claude-haiku-4-5-20251001-v1:0")
	require.True(t, ok)
	mIn, mOut, _ := LookupPricing("bedrock-mantle", "anthropic.claude-haiku-4-5")
	assert.Greater(t, mIn, bIn)
	assert.Greater(t, mOut, bOut)
}

// Non-Claude rows are the AWS Price List API standard-tier Mantle SKUs
// (offer version 20261102165015). Spot checks across vendors and routes.
func TestBedrockMantleOpenModelRates_MatchPriceList(t *testing.T) {
	cases := map[string][2]float64{
		"openai.gpt-oss-120b":                 {0.15, 0.60},
		"openai.gpt-oss-20b":                  {0.07, 0.30},
		"google.gemma-4-31b":                  {0.14, 0.40},
		"google.gemma-3-4b-it":                {0.04, 0.08},
		"xai.grok-4.3":                        {1.25, 2.50},
		"xai.grok-4.6":                        {2.20, 6.60},
		"deepseek.v3.2":                       {0.62, 1.85},
		"mistral.devstral-2-123b":             {0.40, 2.00},
		"moonshotai.kimi-k2.5":                {0.60, 3.00},
		"nvidia.nemotron-super-3-120b":        {0.15, 0.65},
		"qwen.qwen3-coder-480b-a35b-instruct": {0.45, 1.80},
		"zai.glm-5":                           {1.00, 3.20},
		"writer.palmyra-vision-7b":            {0.15, 0.60},
		"minimax.minimax-m2.5":                {0.30, 1.20},
		"mistral.ministral-3-3b-instruct":     {0.10, 0.10},
	}
	for id, want := range cases {
		in, out, ok := LookupPricing("bedrock-mantle", id)
		require.True(t, ok, id)
		assert.InDelta(t, want[0], in, 1e-9, "%s input", id)
		assert.InDelta(t, want[1], out, 1e-9, "%s output", id)
	}
}

func TestBedrockMantleContextWindows(t *testing.T) {
	cases := map[string][2]int32{
		"anthropic.claude-opus-4-7":  {1_000_000, 128_000},
		"anthropic.claude-haiku-4-5": {200_000, 64_000},
		"anthropic.claude-sonnet-5":  {1_000_000, 128_000},
		"openai.gpt-oss-120b":        {128_000, 16_000},
		"google.gemma-4-31b":         {256_000, 8_000},
		"xai.grok-4.3":               {1_000_000, 131_072},
		"writer.palmyra-vision-7b":   {4_000, 4_000},
		"zai.glm-5":                  {200_000, 128_000},
	}
	for id, want := range cases {
		info := Lookup("bedrock-mantle", id)
		require.NotNil(t, info, id)
		assert.Equal(t, want[0], info.ContextWindow, "%s context", id)
		assert.Equal(t, want[1], info.MaxOutputTokens, "%s max output", id)
	}
}

// Tier classification must not regress for the cheap open models: a $0.08
// output model is small-open even when it reasons only on an expensive one.
func TestBedrockMantleTiers(t *testing.T) {
	assert.Equal(t, TierFrontier, TierFromInfo(Lookup("bedrock-mantle", "anthropic.claude-opus-4-7")))
	assert.Equal(t, TierSmallOpen, TierFromInfo(Lookup("bedrock-mantle", "google.gemma-3-4b-it")))
	assert.NotEqual(t, TierLocal, TierFromInfo(Lookup("bedrock-mantle", "openai.gpt-oss-20b")), "a billed Mantle model is never local")
}

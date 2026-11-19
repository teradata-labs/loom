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
	"testing"

	"github.com/stretchr/testify/assert"
)

// The Vercel AI Gateway spells Claude versions with dots
// ("anthropic/claude-opus-5.5"). Each dotted id must price, cache and
// long-context exactly like its hyphenated twin. Before NormalizeClaudeID the
// dotted Opus 5.5 id fell into the broader "claude-opus-5" case and priced at
// Opus 5's $5/$25 instead of $4/$20.
func TestClaudeFamily_DottedIDsMatchHyphenated(t *testing.T) {
	pairs := []struct{ dotted, hyphenated string }{
		{"anthropic/claude-opus-5.5", "claude-opus-5-5"},
		{"anthropic/claude-sonnet-5.5", "claude-sonnet-5-5"},
		{"anthropic/claude-haiku-5.5", "claude-haiku-5-5"},
		{"anthropic/claude-fable-5.1", "claude-fable-5-1"},
		{"anthropic/claude-opus-4.1", "claude-opus-4-1"},
		{"anthropic/claude-sonnet-4.5", "claude-sonnet-4-5"},
		{"anthropic/claude-haiku-4.5", "claude-haiku-4-5"},
		{"anthropic/claude-3.5-haiku", "claude-3-5-haiku"},
		{"anthropic/claude-3.5-sonnet", "claude-3-5-sonnet"},
	}
	for _, p := range pairs {
		t.Run(p.dotted, func(t *testing.T) {
			di, do, dok := ClaudeFamilyPricing(p.dotted)
			hi, ho, hok := ClaudeFamilyPricing(p.hyphenated)
			assert.True(t, dok, "dotted id must match a family")
			assert.Equal(t, hok, dok)
			assert.Equal(t, hi, di, "input rate")
			assert.Equal(t, ho, do, "output rate")
			assert.Equal(t, ClaudeCacheReadMultiplier(p.hyphenated), ClaudeCacheReadMultiplier(p.dotted), "cache read multiplier")

			hli, hlo, hlok := ClaudeLongContextPricing(p.hyphenated, 200_000)
			dli, dlo, dlok := ClaudeLongContextPricing(p.dotted, 200_000)
			assert.Equal(t, hlok, dlok, "long-context applicability")
			assert.Equal(t, hli, dli)
			assert.Equal(t, hlo, dlo)
		})
	}
}

func TestClaudeFamily_DottedOpus55Rate(t *testing.T) {
	in, out, ok := ClaudeFamilyPricing("anthropic/claude-opus-5.5")
	assert.True(t, ok)
	assert.Equal(t, 4.0, in)
	assert.Equal(t, 20.0, out)
}

func TestNormalizeClaudeID(t *testing.T) {
	tests := []struct{ in, want string }{
		{"anthropic/Claude-Opus-5.5", "anthropic/claude-opus-5-5"},
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4-5-20250929"},
		{"us.anthropic.claude-haiku-4-5-20251001-v1:0", "us-anthropic-claude-haiku-4-5-20251001-v1:0"},
		{"", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, NormalizeClaudeID(tt.in), tt.in)
	}
}

// BedrockEndpointMultiplier keys on the "global." prefix and must keep seeing
// raw ids, never NormalizeClaudeID output.
func TestBedrockEndpointMultiplier_UnaffectedByNormalization(t *testing.T) {
	assert.Equal(t, 1.0, BedrockEndpointMultiplier("global.anthropic.claude-sonnet-4-5-20250929-v1:0"))
	assert.Equal(t, bedrockRegionalPremium, BedrockEndpointMultiplier("us.anthropic.claude-sonnet-4-5-20250929-v1:0"))
}

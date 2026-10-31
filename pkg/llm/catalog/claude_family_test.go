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

func TestClaudeFamilyPricing(t *testing.T) {
	tests := []struct {
		id      string
		in, out float64
		matched bool
	}{
		// Claude 5 family, every id shape a client sees.
		{"claude-fable-5-1", 10, 50, true},
		{"claude-fable-5", 10, 50, true},
		{"us.anthropic.claude-fable-5", 10, 50, true},
		{"coding-agent/claude-fable-5-1", 10, 50, true}, // was gpt-4o rates on the openai client
		{"claude-mythos-5-1", 10, 50, true},
		{"claude-opus-5-5", 4, 20, true}, // must not fall into the opus-5 case
		{"global.anthropic.claude-opus-5-5", 4, 20, true},
		{"claude-opus-5", 5, 25, true},
		{"us.anthropic.claude-opus-5", 5, 25, true},
		{"claude-sonnet-5", 2, 10, true}, // not the sonnet-4 rate
		{"litellm/claude-sonnet-5", 2, 10, true},
		{"CLAUDE-SONNET-5", 2, 10, true},
		// Earlier generations keep their rates.
		{"claude-opus-4-1-20250805", 15, 75, true},
		{"claude-opus-4-8", 5, 25, true},
		{"us.anthropic.claude-opus-4-7-v1:0", 5, 25, true},
		{"claude-sonnet-4-6", 3, 15, true},
		{"claude-3-5-sonnet-20241022", 3, 15, true},
		{"claude-haiku-4-5", 1, 5, true},
		// Claude 3 ids put the generation before the family name, so the
		// generic "claude-haiku" / "claude-opus" cases never see them.
		{"claude-3-5-haiku-20241022", 0.80, 4, true},
		{"claude-3-5-haiku-latest", 0.80, 4, true},
		{"claude-3-haiku-20240307", 0.25, 1.25, true},
		{"anthropic.claude-3-haiku-20240307-v1:0", 0.25, 1.25, true},
		{"claude-3-opus-20240229", 15, 75, true},
		{"us.anthropic.claude-3-opus-20240229-v1:0", 15, 75, true},
		// Not Claude: the caller's own default applies.
		{"gpt-4o", 0, 0, false},
		{"opus", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			in, out, matched := ClaudeFamilyPricing(tt.id)
			assert.Equal(t, tt.matched, matched)
			assert.Equal(t, tt.in, in)
			assert.Equal(t, tt.out, out)
		})
	}
}

// Every Claude entry in the static catalog must agree with the family
// fallback, so the two sources of truth cannot drift apart again.
func TestClaudeFamilyPricing_AgreesWithCatalog(t *testing.T) {
	for provider, models := range BuildCatalog() {
		if provider != "anthropic" && provider != "bedrock" {
			continue
		}
		for _, m := range models {
			in, out, matched := ClaudeFamilyPricing(m.Id)
			if !matched {
				continue
			}
			assert.Equal(t, m.CostPer_1MInputUsd, in, "%s/%s input rate", provider, m.Id)
			assert.Equal(t, m.CostPer_1MOutputUsd, out, "%s/%s output rate", provider, m.Id)
		}
	}
}

// Cache-read rates are per model (Anthropic pricing page): Fable 5.1 and
// Mythos 5.1 read at 0.025x, Opus 5.5 and Sonnet 5.5 at 0.05x, the rest 0.10x.
func TestClaudeCacheReadMultiplier(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want float64
	}{
		{"claude-fable-5-1", 0.025},
		{"us.anthropic.claude-fable-5-1", 0.025},
		{"coding-agent/claude-mythos-5-1", 0.025},
		{"claude-fable-5", 0.10},
		{"claude-opus-5-5", 0.05},
		{"global.anthropic.claude-opus-5-5", 0.05},
		{"claude-sonnet-5-5", 0.05},
		{"claude-opus-5", 0.10},
		{"claude-sonnet-5", 0.10},
		{"claude-haiku-5-5", 0.10},
		{"us.anthropic.claude-sonnet-4-5-20250929-v1:0", 0.10},
	} {
		assert.InDelta(t, tc.want, ClaudeCacheReadMultiplier(tc.id), 1e-12, tc.id)
	}
}

// Haiku 5.5 is the only model priced by prompt length: $0.50/$2.50 once the
// prompt passes 100K tokens, the $0.10/$0.50 base card at or below it.
func TestClaudeLongContextPricing(t *testing.T) {
	in, out, ok := ClaudeLongContextPricing("claude-haiku-5-5", 100_001)
	assert.True(t, ok)
	assert.Equal(t, 0.50, in)
	assert.Equal(t, 2.50, out)

	_, _, ok = ClaudeLongContextPricing("us.anthropic.claude-haiku-5-5", 100_000)
	assert.False(t, ok, "the threshold itself bills the base card")
	_, _, ok = ClaudeLongContextPricing("claude-sonnet-5-5", 900_000)
	assert.False(t, ok, "other models keep standard pricing across the full window")

	in, out, ok = ClaudeFamilyPricing("claude-haiku-5-5")
	assert.True(t, ok)
	assert.Equal(t, 0.10, in, "Haiku 5.5 must not take Haiku 4.5's $1 card")
	assert.Equal(t, 0.50, out)
}

// Regional Bedrock endpoints carry a 10% premium over global ones for Claude
// 4.5 and later; earlier models and global profiles are unchanged.
func TestBedrockEndpointMultiplier(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want float64
	}{
		{"global.anthropic.claude-fable-5", 1.0},
		{"us.anthropic.claude-fable-5", 1.1},
		{"eu.anthropic.claude-opus-5-5", 1.1},
		{"global.anthropic.claude-opus-5-5", 1.0},
		{"anthropic.claude-sonnet-5-5", 1.1}, // in-region id
		{"apac.anthropic.claude-haiku-4-5-20251001-v1:0", 1.1},
		{"us.anthropic.claude-sonnet-4-5-20250929-v1:0", 1.1},
		{"us.anthropic.claude-opus-4-6-v1", 1.1},
		{"us.anthropic.claude-opus-4-1-20250805-v1:0", 1.0},
		{"us.anthropic.claude-sonnet-4-20250514-v1:0", 1.0}, // date is not a minor version
		{"anthropic.claude-3-5-sonnet-20240620-v1:0", 1.0},
		{"us.meta.llama3-70b-instruct-v1:0", 1.0},
	} {
		assert.InDelta(t, tc.want, BedrockEndpointMultiplier(tc.id), 1e-12, tc.id)
	}
}

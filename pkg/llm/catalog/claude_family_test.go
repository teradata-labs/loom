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

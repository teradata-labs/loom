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

package catwalk

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm/catalog"
)

// The Mantle provider lists exactly the Claude ids the catalog prices under
// "bedrock-mantle", with the same context window, output cap and rates.
func TestGetProviders_BedrockMantleClaudeMatchesCatalog(t *testing.T) {
	var mantle *Provider
	for _, p := range GetProviders() {
		if p.ID == InferenceProviderBedrockMantle {
			mantle = &p
			break
		}
	}
	require.NotNil(t, mantle, "catwalk must list the bedrock-mantle provider")

	listed := make(map[string]Model, len(mantle.Models))
	for _, m := range mantle.Models {
		_, dup := listed[m.ID]
		require.False(t, dup, "duplicate catwalk id %q", m.ID)
		listed[m.ID] = m
	}

	cataloged := 0
	for _, info := range catalog.BuildCatalog()["bedrock-mantle"] {
		if !strings.HasPrefix(info.Id, "anthropic.") {
			continue
		}
		cataloged++
		m, ok := listed[info.Id]
		if !assert.True(t, ok, "catwalk Mantle list is missing cataloged Claude id %q", info.Id) {
			continue
		}
		assert.Equal(t, int(info.ContextWindow), m.ContextWindow, "%s context window", info.Id)
		assert.Equal(t, int(info.MaxOutputTokens), m.MaxOutputTokens, "%s max output tokens", info.Id)
		assert.InDelta(t, info.CostPer_1MInputUsd, m.InputCost, 1e-9, "%s input cost", info.Id)
		assert.InDelta(t, info.CostPer_1MOutputUsd, m.OutputCost, 1e-9, "%s output cost", info.Id)
	}
	assert.Equal(t, cataloged, len(listed), "catwalk lists a Claude id the catalog does not price")
}

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

package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bedrock-mantle namespace carries Mantle's own ids and must price them
// at the bedrock-runtime rates AWS documents as identical.
func TestBedrockMantleNamespace(t *testing.T) {
	models := BuildCatalog()["bedrock-mantle"]
	require.NotEmpty(t, models)

	for _, m := range models {
		assert.Equal(t, "bedrock-mantle", m.Provider)
		assert.False(t, m.ShowInDropdown, "%s is endpoint-configured", m.Id)
		assert.NotContains(t, m.Id, "us.", "Mantle ids carry no inference-profile prefix")
		assert.NotContains(t, m.Id, "global.")
	}

	in, out, ok := LookupPricing("bedrock-mantle", "anthropic.claude-haiku-4-5")
	require.True(t, ok)
	assert.Equal(t, 1.0, in)
	assert.Equal(t, 5.0, out)

	bin, bout, ok := LookupPricing("bedrock", "us.anthropic.claude-haiku-4-5-20251001-v1:0")
	require.True(t, ok)
	assert.Equal(t, bin, in, "Mantle and bedrock-runtime bill the same rate")
	assert.Equal(t, bout, out)

	info := Lookup("bedrock-mantle", "anthropic.claude-opus-4-7")
	require.NotNil(t, info)
	assert.Equal(t, int32(1_000_000), info.ContextWindow)
	assert.Equal(t, int32(128_000), info.MaxOutputTokens)
}

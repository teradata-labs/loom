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

package bedrockmantle

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm/catalog"
)

// Every cataloged Mantle id must start on the route AWS documents for it:
// Claude on the Messages API, Gemma 4 and Grok on /openai/v1, everything
// else on /v1. The catalog and the route table are maintained separately, so
// this pins them together; a new catalog row on the wrong route would
// otherwise cost one failed request and a fallback per process.
func TestCatalogIDs_StartOnDocumentedRoute(t *testing.T) {
	models := catalog.BuildCatalog()[CatalogProvider]
	require.NotEmpty(t, models)

	for _, m := range models {
		switch {
		case strings.HasPrefix(m.Id, "anthropic."):
			assert.True(t, IsAnthropicModel(m.Id), m.Id)
		case strings.HasPrefix(m.Id, "google.gemma-4"), strings.HasPrefix(m.Id, "xai."):
			assert.False(t, IsAnthropicModel(m.Id), m.Id)
			assert.Equal(t, RouteOpenAIV1, RoutePrefix(m.Id), "%s is served on /openai/v1 per its AWS model card", m.Id)
		default:
			assert.False(t, IsAnthropicModel(m.Id), m.Id)
			assert.Equal(t, RouteV1, RoutePrefix(m.Id), "%s is served on /v1 per its AWS model card", m.Id)
		}
	}
}

// The catalog namespace is what the inner clients price under, so its key
// must be the one the client passes to them.
func TestCatalogProvider_MatchesNamespace(t *testing.T) {
	assert.Equal(t, ProviderName, CatalogProvider)
	_, ok := catalog.BuildCatalog()[CatalogProvider]
	assert.True(t, ok, "catalog has no %q namespace", CatalogProvider)
}

// A client built for a cataloged Claude id resolves its price through the
// Mantle namespace, so the cost attached to responses is the Mantle rate and
// not the first-party anthropic card.
func TestCatalog_DefaultModelIsCataloged(t *testing.T) {
	in, out, ok := catalog.LookupPricing(CatalogProvider, DefaultModel)
	require.True(t, ok, "DefaultModel %q must be priced in the catalog", DefaultModel)
	assert.Greater(t, in, 0.0)
	assert.Greater(t, out, 0.0)
}

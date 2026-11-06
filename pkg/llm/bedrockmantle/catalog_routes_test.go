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
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// The Mantle Claude rows already include the 10% regional premium (first-party
// card x 1.10), and Mantle prices through the anthropic client, which applies
// no endpoint multiplier. A bare Mantle id ("anthropic.claude-…") is one that
// catalog.BedrockEndpointMultiplier classifies as regional, so applying it on
// this path would count the premium twice (x 1.21). This pins the cost a
// response carries to the row: Opus 4.7 is $5.50 / $27.50 per 1M tokens, the
// same as the bedrock client's estimate for a us. or eu. profile (card x
// BedrockEndpointMultiplier) and 10% above a global. profile.
func TestClaudeCost_RegionalPremiumAppliedOnce(t *testing.T) {
	const model = "anthropic.claude-opus-4-7"
	in, out, ok := catalog.RequestPricing(CatalogProvider, model, 1)
	require.True(t, ok)
	require.InDelta(t, 5.50, in, 1e-9, "Mantle Opus 4.7 input row")
	require.InDelta(t, 27.50, out, 1e-9, "Mantle Opus 4.7 output row")

	tests := []struct {
		name          string
		input, output int
		want          float64
	}{
		{name: "1M input tokens", input: 1_000_000, want: 5.50},
		{name: "1M output tokens", output: 1_000_000, want: 27.50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"id":"msg_1","type":"message","role":"assistant","model":%q,"stop_reason":"end_turn","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`, model, tt.input, tt.output)
			srv := httptest.NewServer(newRecorder(jsonResponder(http.StatusOK, body)))
			defer srv.Close()

			c, err := NewClientForModel(Config{Endpoint: srv.URL, Region: "us-west-2", APIKey: "ABSKtest", Model: model})
			require.NoError(t, err)
			resp, err := c.Chat(context.Background(), userMessage("Reply OK"), nil)
			require.NoError(t, err)
			assert.InDelta(t, tt.want, resp.Usage.CostUSD, 1e-9, "premium counted once, not x 1.21")
		})
	}

	// Parity with the bedrock client's formula for the same model.
	for _, id := range []string{"us.anthropic.claude-opus-4-7-v1:0", "eu.anthropic.claude-opus-4-7-v1:0"} {
		bIn, bOut, ok := catalog.RequestPricing("bedrock", id, 1)
		require.True(t, ok, id)
		m := catalog.BedrockEndpointMultiplier(id)
		assert.InDelta(t, in, bIn*m, 1e-9, "%s input", id)
		assert.InDelta(t, out, bOut*m, 1e-9, "%s output", id)
	}
	gIn, gOut, ok := catalog.RequestPricing("bedrock", "global.anthropic.claude-opus-4-7-v1:0", 1)
	require.True(t, ok)
	assert.InDelta(t, 1.0, catalog.BedrockEndpointMultiplier("global.anthropic.claude-opus-4-7-v1:0"), 1e-9)
	assert.InDelta(t, in, gIn*1.10, 1e-9, "global. input is the card, 10%% below Mantle")
	assert.InDelta(t, out, gOut*1.10, 1e-9, "global. output is the card, 10%% below Mantle")
}

// The Haiku 5.5 long-context card (over a 100K-token prompt) carries the same
// premium as the row it replaces. Mantle rows are first-party × 1.10, so a
// Mantle prompt over 100K is $0.55/$2.75; the anthropic namespace keeps the
// card ($0.50/$2.50); the bedrock client applies BedrockEndpointMultiplier on
// top of the card, so us. is $0.55/$2.75 (not × 1.21) and global. is
// $0.50/$2.50. Haiku 5.5 is the only model with a long-context card.
func TestClaudeCost_LongContextCardCarriesRowPremium(t *testing.T) {
	const output = 1_000_000
	tests := []struct {
		name            string
		namespace, id   string
		mantleClient    bool
		prompt          int
		wantIn, wantOut float64
	}{
		{name: "mantle at 100K", namespace: CatalogProvider, id: "anthropic.claude-haiku-5-5", mantleClient: true, prompt: 100_000, wantIn: 0.11, wantOut: 0.55},
		{name: "mantle over 100K", namespace: CatalogProvider, id: "anthropic.claude-haiku-5-5", mantleClient: true, prompt: 200_000, wantIn: 0.55, wantOut: 2.75},
		{name: "anthropic over 100K", namespace: "anthropic", id: "claude-haiku-5-5", prompt: 200_000, wantIn: 0.50, wantOut: 2.50},
		{name: "bedrock us. over 100K", namespace: "bedrock", id: "us.anthropic.claude-haiku-5-5", prompt: 200_000, wantIn: 0.55, wantOut: 2.75},
		{name: "bedrock global. over 100K", namespace: "bedrock", id: "global.anthropic.claude-haiku-5-5", prompt: 200_000, wantIn: 0.50, wantOut: 2.50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.mantleClient {
				body := fmt.Sprintf(`{"id":"msg_1","type":"message","role":"assistant","model":%q,"stop_reason":"end_turn","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`, tt.id, tt.prompt, output)
				srv := httptest.NewServer(newRecorder(jsonResponder(http.StatusOK, body)))
				defer srv.Close()

				c, err := NewClientForModel(Config{Endpoint: srv.URL, Region: "us-west-2", APIKey: "ABSKtest", Model: tt.id})
				require.NoError(t, err)
				resp, err := c.Chat(context.Background(), userMessage("Reply OK"), nil)
				require.NoError(t, err)
				want := float64(tt.prompt)*tt.wantIn/1e6 + float64(output)*tt.wantOut/1e6
				assert.InDelta(t, want, resp.Usage.CostUSD, 1e-9)
				return
			}
			in, out, ok := catalog.RequestPricing(tt.namespace, tt.id, tt.prompt)
			require.True(t, ok)
			if tt.namespace == "bedrock" {
				// The bedrock client's formula (pkg/llm/bedrock bedrockClaudeCost).
				m := catalog.BedrockEndpointMultiplier(tt.id)
				in, out = in*m, out*m
			}
			assert.InDelta(t, tt.wantIn, in, 1e-9, "input")
			assert.InDelta(t, tt.wantOut, out, 1e-9, "output")
		})
	}
}

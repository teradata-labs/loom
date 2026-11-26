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

package openai

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
)

// gatewayCatalog is a catalog.Source standing in for an embedder's registered
// DB/gateway catalog: it knows one LiteLLM alias under provider "litellm" and
// otherwise defers to the static table.
type gatewayCatalog struct{ static catalog.Source }

func (g gatewayCatalog) Lookup(ctx context.Context, provider, modelID string) *loomv1.ModelInfo {
	if provider == "litellm" && modelID == "deepseek.v3.2" {
		return &loomv1.ModelInfo{Id: modelID, Provider: provider, CostPer_1MInputUsd: 0.27, CostPer_1MOutputUsd: 1.10}
	}
	return g.static.Lookup(ctx, provider, modelID)
}

func (g gatewayCatalog) List(ctx context.Context) map[string][]*loomv1.ModelInfo {
	return g.static.List(ctx)
}

// TestCalculateCost_CatalogProvider pins the gateway case: the same client
// pointed at a LiteLLM alias prices at the gateway's registered rate when told
// which catalog namespace to read, and at the gpt-4o default when not — which
// is the 5–10× overbilling this option exists to end.
func TestCalculateCost_CatalogProvider(t *testing.T) {
	original := catalog.DefaultSource()
	t.Cleanup(func() { catalog.Register(original) })
	catalog.Register(gatewayCatalog{static: catalog.StaticSource()})

	const in, out = 1_000_000, 100_000

	priced := NewClient(Config{APIKey: "k", Model: "deepseek.v3.2", Endpoint: "http://gateway/v1/chat/completions", CatalogProvider: "litellm"})
	assert.InDelta(t, 0.27+0.11, priced.calculateCost(in, out, 0, 0), 1e-9, "gateway alias priced from the registered catalog")

	unpriced := NewClient(Config{APIKey: "k", Model: "deepseek.v3.2", Endpoint: "http://gateway/v1/chat/completions"})
	assert.Equal(t, DefaultCatalogProvider, unpriced.catalogProvider)
	assert.InDelta(t, 2.50+1.00, unpriced.calculateCost(in, out, 0, 0), 1e-9, "no namespace → the alias misses \"openai\" and falls to the gpt-4o default")

	// A real OpenAI id is unaffected by the option's default.
	assert.Equal(t, NewClient(Config{APIKey: "k", Model: "gpt-4.1-mini"}).calculateCost(in, out, 0, 0),
		NewClient(Config{APIKey: "k", Model: "gpt-4.1-mini", CatalogProvider: "openai"}).calculateCost(in, out, 0, 0))
}

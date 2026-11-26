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

package bedrock

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
)

// registeredCard is a catalog.Source standing in for an embedder's catalog: it
// prices one (provider, id) at the embedder's own rates and defers every other
// lookup to the built-in table.
type registeredCard struct {
	provider, id string
	in, out      float64
}

func (r registeredCard) Lookup(ctx context.Context, provider, modelID string) *loomv1.ModelInfo {
	if provider == r.provider && modelID == r.id {
		return &loomv1.ModelInfo{Id: modelID, Provider: provider, CostPer_1MInputUsd: r.in, CostPer_1MOutputUsd: r.out}
	}
	return catalog.StaticSource().Lookup(ctx, provider, modelID)
}

func (r registeredCard) List(ctx context.Context) map[string][]*loomv1.ModelInfo {
	return catalog.StaticSource().List(ctx)
}

// A registered card is the embedder's pricing authority on both Bedrock
// clients: a Haiku 5.5 prompt over 100K tokens keeps it instead of switching to
// the published $0.50/$2.50 long-context card. The global id carries no
// regional premium, so the figures are the cards themselves.
func TestCalculateCost_RegisteredCardSurvivesLongContext(t *testing.T) {
	const model = "global.anthropic.claude-haiku-5-5"
	clients := map[string]func(in, out, cacheRead, cacheWrite int) float64{
		"Client":    (&Client{modelID: model}).calculateCost,
		"SDKClient": (&SDKClient{modelID: model}).calculateCost,
	}

	published := (150_000*0.50 + 1_000*2.50) / 1e6
	for name, cost := range clients {
		assert.InDelta(t, published, cost(150_000, 1_000, 0, 0), 1e-12,
			"%s, no registration: the published long-context card applies", name)
	}

	original := catalog.DefaultSource()
	t.Cleanup(func() { catalog.Register(original) })
	catalog.Register(registeredCard{provider: "bedrock", id: model, in: 0.25, out: 1.25})

	for name, cost := range clients {
		assert.InDelta(t, 0.03875, cost(150_000, 1_000, 0, 0), 1e-12,
			"%s: registered $0.25/$1.25 card is kept above 100K tokens", name)
	}
}

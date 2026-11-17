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

package anthropic

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

// A registered card is the embedder's pricing authority: a Haiku 5.5 prompt
// over 100K tokens keeps it instead of switching to the published $0.50/$2.50
// long-context card. The published card still applies without a registration.
func TestCalculateCost_RegisteredCardSurvivesLongContext(t *testing.T) {
	const model = "claude-haiku-5-5"
	client := &Client{model: model}

	published := (150_000*0.50 + 1_000*2.50) / 1e6
	assert.InDelta(t, published, client.calculateCost(150_000, 1_000, 0, 0), 1e-12,
		"no registration: the published long-context card applies")

	original := catalog.DefaultSource()
	t.Cleanup(func() { catalog.Register(original) })
	catalog.Register(registeredCard{provider: "anthropic", id: model, in: 0.25, out: 1.25})

	assert.InDelta(t, 0.03875, client.calculateCost(150_000, 1_000, 0, 0), 1e-12,
		"registered $0.25/$1.25 card is kept above 100K tokens")
}

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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

type listOnly map[string][]*loomv1.ModelInfo

func (l listOnly) Lookup(context.Context, string, string) *loomv1.ModelInfo { return nil }
func (l listOnly) List(context.Context) map[string][]*loomv1.ModelInfo  { return l }

func TestCreatorMedianPricing(t *testing.T) {
	t.Cleanup(func() { Register(nil) })
	Register(nil)
	m := func(id string, in, out float64) *loomv1.ModelInfo {
		return &loomv1.ModelInfo{Id: id, CostPer_1MInputUsd: in, CostPer_1MOutputUsd: out}
	}
	RegisterNamed("gw-test", listOnly{"gw-test": {
		m("openai/a", 1, 4), m("openai/b", 3, 12), m("openai/c", 2, 8),
		m("google/x", 10, 40), m("google/y", 20, 80), // google median 15/60
		m("meta/free", 0, 0),      // unpriced rows are ignored
		m("openai/self", 100, 400), // counts for other openai ids, excluded for its own
	}})

	tests := []struct {
		id       string
		in, out  float64
		found    bool
		provider string
	}{
		// openai: 1,2,3,100 / 4,8,12,400 -> 2.5 / 10
		{id: "openai/new-model", in: 2.5, out: 10, found: true},
		{id: "OpenAI/new-model", in: 2.5, out: 10, found: true},
		{id: "google/new-model", in: 15, out: 60, found: true},
		// Unknown creator: every priced model, 1,2,3,10,20,100 / 4,8,12,40,80,400.
		{id: "acme/new-model", in: 6.5, out: 26, found: true},
		{id: "no-slash-id", in: 6.5, out: 26, found: true},
		// Its own row is excluded: 1,2,3 / 4,8,12.
		{id: "openai/self", in: 2, out: 8, found: true},
		{id: "openai/x", provider: "other-ns", found: false},
	}
	for _, tt := range tests {
		p := tt.provider
		if p == "" {
			p = "gw-test"
		}
		in, out, ok := CreatorMedianPricing(p, tt.id)
		assert.Equal(t, tt.found, ok, tt.id)
		assert.InDelta(t, tt.in, in, 1e-9, tt.id)
		assert.InDelta(t, tt.out, out, 1e-9, tt.id)
	}
}

func TestMedian(t *testing.T) {
	assert.Equal(t, 2.0, median([]float64{3, 1, 2}))
	assert.Equal(t, 2.5, median([]float64{4, 1, 3, 2}))
	assert.Equal(t, 7.0, median([]float64{7}))
}

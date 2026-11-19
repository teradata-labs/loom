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

package vercelgateway

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm/catalog"
)

// tieredModels is the alibaba/qwen3-max-thinking entry as the live catalog
// served it on 2026-11-18, plus a model whose cache-write list has coarser
// boundaries than its input list.
const tieredModels = `{"object":"list","data":[
{"id":"alibaba/qwen3-max-thinking","object":"model","type":"language","context_window":262144,"max_tokens":32768,
 "pricing":{"input":"0.0000012","output":"0.000006","input_cache_read":"0.00000024",
  "input_tiers":[{"cost":"0.0000012","min":0,"max":32001},{"cost":"0.0000024","min":32001,"max":128001},{"cost":"0.000003","min":128001}],
  "output_tiers":[{"cost":"0.000006","min":0,"max":32001},{"cost":"0.000012","min":32001,"max":128001},{"cost":"0.000015","min":128001}],
  "input_cache_read_tiers":[{"cost":"0.00000024","min":0,"max":32001},{"cost":"0.00000048","min":32001,"max":128001},{"cost":"0.0000006","min":128001}]}},
{"id":"acme/split","object":"model","type":"language","context_window":1000000,
 "pricing":{"input":"0.000001","output":"0.000002","input_cache_write":"0.0000015",
  "input_tiers":[{"cost":"0.000001","min":0,"max":100000},{"cost":"0.000002","min":100000,"max":200000},{"cost":"0.000003","min":200000}],
  "input_cache_write_tiers":[{"cost":"0.0000015","min":0,"max":200000},{"cost":"0.000004","min":200000}]}},
{"id":"acme/flat","object":"model","type":"language","pricing":{"input":"0.000001","output":"0.000002"}}
]}`

func TestDecodeModels_PriceTiers(t *testing.T) {
	rows, err := DecodeModels([]byte(tieredModels))
	require.NoError(t, err)
	require.Len(t, rows, 3)

	q := rows[0].GetPriceTiers()
	require.Len(t, q, 3)
	assert.Equal(t, int64(0), q[0].GetMinPromptTokens())
	assert.Equal(t, int64(32001), q[0].GetMaxPromptTokens())
	assert.InDelta(t, 2.4, q[1].GetInputPer_1MUsd(), 1e-9)
	assert.InDelta(t, 12.0, q[1].GetOutputPer_1MUsd(), 1e-9)
	assert.InDelta(t, 0.48, q[1].GetCacheReadPer_1MUsd(), 1e-9)
	assert.Zero(t, q[1].GetCacheWritePer_1MUsd(), "no cache-write tiers published")
	assert.Equal(t, int64(0), q[2].GetMaxPromptTokens(), "last tier is unbounded")

	split := rows[1].GetPriceTiers()
	require.Len(t, split, 3, "boundaries of all lists combine")
	assert.InDelta(t, 2.0, split[1].GetInputPer_1MUsd(), 1e-9)
	assert.InDelta(t, 1.5, split[1].GetCacheWritePer_1MUsd(), 1e-9, "the coarser list still prices the finer range")
	assert.InDelta(t, 4.0, split[2].GetCacheWritePer_1MUsd(), 1e-9)

	assert.Empty(t, rows[2].GetPriceTiers())
}

func TestRequestPricing_UsesCatalogTiers(t *testing.T) {
	t.Cleanup(func() { catalog.Register(nil) })
	catalog.Register(nil)
	ms := &modelsServer{}
	ms.set(http.StatusOK, tieredModels)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := RegisterCatalog(ctx, ms.start(t).URL, time.Hour, nil)
	require.NoError(t, err)

	tests := []struct {
		prompt          int
		in, out, cacheR float64
	}{
		{0, 1.2, 6, 0.24},
		{32000, 1.2, 6, 0.24},
		{32001, 2.4, 12, 0.48},
		{128000, 2.4, 12, 0.48},
		{128001, 3, 15, 0.6},
		{1_000_000, 3, 15, 0.6},
	}
	for _, tt := range tests {
		in, out, ok := catalog.RequestPricing(CatalogProvider, "alibaba/qwen3-max-thinking", tt.prompt)
		require.True(t, ok)
		assert.InDelta(t, tt.in, in, 1e-9, "input at %d", tt.prompt)
		assert.InDelta(t, tt.out, out, 1e-9, "output at %d", tt.prompt)
		r, _ := catalog.RequestCacheRates(CatalogProvider, "alibaba/qwen3-max-thinking", tt.prompt)
		assert.InDelta(t, tt.cacheR, r, 1e-9, "cache read at %d", tt.prompt)
	}

	in, out, ok := catalog.RequestPricing(CatalogProvider, "acme/flat", 500_000)
	require.True(t, ok)
	assert.InDelta(t, 1.0, in, 1e-9, "a flat row is flat at every size")
	assert.InDelta(t, 2.0, out, 1e-9)
}

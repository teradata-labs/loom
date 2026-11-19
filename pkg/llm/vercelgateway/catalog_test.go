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
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
)

// sampleModels follows the documented /v1/models entry shape (REST API docs,
// 2026-11), plus entries that must be skipped.
const sampleModels = `{"object":"list","data":[
{"id":"google/gemini-3.1-pro-preview","object":"model","created":1755815280,"released":1763424000,"owned_by":"google",
 "name":"Gemini 3.1 Pro Preview","context_window":1000000,"max_tokens":64000,"type":"language",
 "tags":["file-input","tool-use","reasoning","vision"],"modalities":{"input":["text","image","pdf"],"output":["text"]},
 "pricing":{"input":"0.000002","output":"0.000012","input_cache_read":"0.0000002","input_cache_write":"0.000002",
  "input_tiers":[{"cost":"0.000002","min":0,"max":200000},{"cost":"0.000004","min":200000}]}},
{"id":"anthropic/claude-sonnet-4.5","name":"Claude Sonnet 4.5","type":"language","context_window":200000,"max_tokens":64000,
 "tags":["tool-use","reasoning"],"pricing":{"input":"0.000003","output":"0.000015","input_cache_read":"0.0000003","input_cache_write":"0.00000375"}},
{"id":"meta/llama-plain","type":"language","context_window":128000,"max_tokens":8192,"tags":["tool-use"],"pricing":{"input":"bogus","output":"-1"}},
{"id":"openai/text-embedding-3-small","type":"embedding","pricing":{"input":"0.00000002"}},
{"id":"no-slash","type":"language"},
{"id":"huge/ctx","type":"language","context_window":99999999999}
]}`

func TestDecodeModels(t *testing.T) {
	models, err := DecodeModels([]byte(sampleModels))
	require.NoError(t, err)
	byID := map[string]*loomv1.ModelInfo{}
	for _, m := range models {
		byID[m.Id] = m
	}
	require.Len(t, byID, 4, "embedding and invalid ids skipped")

	g := byID["google/gemini-3.1-pro-preview"]
	require.NotNil(t, g)
	assert.Equal(t, CatalogProvider, g.Provider)
	assert.Equal(t, "Gemini 3.1 Pro Preview", g.Name)
	assert.Equal(t, int32(1000000), g.ContextWindow)
	assert.Equal(t, int32(64000), g.MaxOutputTokens)
	assert.True(t, g.IsReasoning)
	assert.ElementsMatch(t, []string{"file-input", "tool-use", "reasoning", "vision"}, g.Capabilities)
	assert.InDelta(t, 2.0, g.CostPer_1MInputUsd, 1e-9)
	assert.InDelta(t, 12.0, g.CostPer_1MOutputUsd, 1e-9)
	assert.InDelta(t, 0.2, g.CostPer_1MCacheReadUsd, 1e-9)
	assert.InDelta(t, 2.0, g.CostPer_1MCacheWriteUsd, 1e-9)

	plain := byID["meta/llama-plain"]
	require.NotNil(t, plain)
	assert.Equal(t, "meta/llama-plain", plain.Name, "name defaults to id")
	assert.False(t, plain.IsReasoning)
	assert.Zero(t, plain.CostPer_1MInputUsd, "unparseable price is unpublished, not guessed")
	assert.Zero(t, plain.CostPer_1MOutputUsd, "negative price is unpublished")

	assert.Equal(t, int32(math.MaxInt32), byID["huge/ctx"].ContextWindow)

	_, err = DecodeModels([]byte(`{"data":`))
	require.Error(t, err)
}

// FuzzDecodeModels: arbitrary bodies never panic; every returned row has a
// valid id, the gateway provider, and finite non-negative prices.
func FuzzDecodeModels(f *testing.F) {
	f.Add([]byte(sampleModels))
	f.Add([]byte(`{"data":[{"id":"a/b","type":"language","pricing":{"input":"1e300"}}]}`))
	f.Add([]byte(`{"data":null}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		models, err := DecodeModels(body)
		if err != nil {
			return
		}
		for _, m := range models {
			require.NoError(t, ValidateModelID(m.Id))
			require.Equal(t, CatalogProvider, m.Provider)
			for _, v := range []float64{m.CostPer_1MInputUsd, m.CostPer_1MOutputUsd, m.CostPer_1MCacheReadUsd, m.CostPer_1MCacheWriteUsd} {
				require.False(t, math.IsNaN(v) || math.IsInf(v, 0) || v < 0, "price %v", v)
			}
			require.GreaterOrEqual(t, m.ContextWindow, int32(0))
			require.GreaterOrEqual(t, m.MaxOutputTokens, int32(0))
		}
	})
}

type modelsServer struct {
	mu     sync.Mutex
	body   string
	status int
	hits   atomic.Int32
}

func (m *modelsServer) set(status int, body string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status, m.body = status, body
}

func (m *modelsServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.hits.Add(1)
		if r.URL.Path != ModelsPath || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		assert.Empty(t, r.Header.Get("Authorization"), "the catalog is public; no credential is sent")
		m.mu.Lock()
		status, body := m.status, m.body
		m.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCatalogSource_RefreshKeepsLastGood(t *testing.T) {
	ms := &modelsServer{}
	ms.set(http.StatusOK, sampleModels)
	srv := ms.start(t)
	src, err := NewCatalogSource(srv.URL, nil)
	require.NoError(t, err)
	assert.Nil(t, src.Lookup(context.Background(), CatalogProvider, "openai/gpt-5"))
	assert.Nil(t, src.List(context.Background()))

	n, err := src.Refresh(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.NotNil(t, src.Lookup(context.Background(), CatalogProvider, "anthropic/claude-sonnet-4.5"))
	assert.NotNil(t, src.Lookup(context.Background(), ProviderAlias, "anthropic/claude-sonnet-4.5"), "alias namespace resolves too")
	assert.Nil(t, src.Lookup(context.Background(), "anthropic", "anthropic/claude-sonnet-4.5"))
	assert.Len(t, src.List(context.Background())[CatalogProvider], 4)

	for _, bad := range []struct {
		status int
		body   string
	}{
		{http.StatusInternalServerError, "oops"},
		{http.StatusOK, "not json"},
		{http.StatusOK, `{"data":[]}`},
	} {
		ms.set(bad.status, bad.body)
		_, err := src.Refresh(context.Background())
		require.Error(t, err)
		assert.Equal(t, 4, src.Len(), "a failed refresh keeps the previous snapshot")
	}
}

func TestCatalogSource_ConcurrentLookupAndRefresh(t *testing.T) {
	ms := &modelsServer{}
	ms.set(http.StatusOK, sampleModels)
	src, err := NewCatalogSource(ms.start(t).URL, nil)
	require.NoError(t, err)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_, _ = src.Refresh(context.Background())
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				_ = src.Lookup(context.Background(), CatalogProvider, "anthropic/claude-sonnet-4.5")
				_ = src.List(context.Background())
			}
		}()
	}
	wg.Wait()
}

// Regression for the composition fix: registering the gateway catalog must
// not evict Ollama's discovered source, and vice versa.
func TestRegisterCatalog_CoexistsWithOllamaSource(t *testing.T) {
	t.Cleanup(func() { catalog.Register(nil) })
	catalog.Register(nil)

	ollama := staticRows{"ollama": {{Id: "discovered-only:latest", Provider: "ollama", ContextWindow: 8192, MaxOutputTokens: 2048}}}
	catalog.RegisterNamed("ollama", ollama)

	ms := &modelsServer{}
	ms.set(http.StatusOK, sampleModels)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := RegisterCatalog(ctx, ms.start(t).URL, time.Hour, nil)
	require.NoError(t, err)

	assert.NotNil(t, catalog.Lookup("ollama", "discovered-only:latest"), "Ollama source survives the gateway registration")
	info := catalog.Lookup(CatalogProvider, "google/gemini-3.1-pro-preview")
	require.NotNil(t, info)
	in, out, ok := catalog.LookupPricing(CatalogProvider, "google/gemini-3.1-pro-preview")
	assert.True(t, ok)
	assert.InDelta(t, 2.0, in, 1e-9)
	assert.InDelta(t, 12.0, out, 1e-9)
	r, w := catalog.CacheRates(CatalogProvider, "google/gemini-3.1-pro-preview")
	assert.InDelta(t, 0.2, r, 1e-9)
	assert.InDelta(t, 2.0, w, 1e-9)

	// Re-registering Ollama keeps the gateway source.
	catalog.RegisterNamed("ollama", ollama)
	assert.NotNil(t, catalog.Lookup(CatalogProvider, "anthropic/claude-sonnet-4.5"))
	assert.NotNil(t, catalog.Lookup("anthropic", "claude-sonnet-4-5-20250929"), "static catalog still behind both")
}

func TestRegisterCatalog_FailedFirstFetchStillRegistersAndRecovers(t *testing.T) {
	t.Cleanup(func() { catalog.Register(nil) })
	ms := &modelsServer{}
	ms.set(http.StatusServiceUnavailable, "down")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src, err := RegisterCatalog(ctx, ms.start(t).URL, 20*time.Millisecond, nil)
	require.Error(t, err)
	require.NotNil(t, src)
	assert.Nil(t, catalog.Lookup(CatalogProvider, "openai/gpt-5"))

	ms.set(http.StatusOK, sampleModels)
	require.Eventually(t, func() bool {
		return catalog.Lookup(CatalogProvider, "anthropic/claude-sonnet-4.5") != nil
	}, 5*time.Second, 10*time.Millisecond, "the background refresh fills the catalog once the gateway answers")
}

type staticRows map[string][]*loomv1.ModelInfo

func (s staticRows) Lookup(_ context.Context, provider, id string) *loomv1.ModelInfo {
	for _, m := range s[provider] {
		if m.Id == id {
			return m
		}
	}
	return nil
}

func (s staticRows) List(context.Context) map[string][]*loomv1.ModelInfo { return s }

// Two gateway endpoints (global and regional) register two catalogs; the
// second does not replace the first.
func TestRegisterCatalog_TwoEndpointsCoexist(t *testing.T) {
	t.Cleanup(func() { catalog.Register(nil) })
	catalog.Register(nil)
	a := &modelsServer{}
	a.set(http.StatusOK, `{"data":[{"id":"acme/only-a","type":"language","pricing":{"input":"0.000001","output":"0.000002"}}]}`)
	b := &modelsServer{}
	b.set(http.StatusOK, `{"data":[{"id":"acme/only-b","type":"language","pricing":{"input":"0.000003","output":"0.000004"}}]}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := RegisterCatalog(ctx, a.start(t).URL, time.Hour, nil)
	require.NoError(t, err)
	_, err = RegisterCatalog(ctx, b.start(t).URL, time.Hour, nil)
	require.NoError(t, err)
	assert.NotNil(t, catalog.Lookup(CatalogProvider, "acme/only-a"))
	assert.NotNil(t, catalog.Lookup(CatalogProvider, "acme/only-b"))
}

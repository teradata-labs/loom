// Copyright 2026 Teradata
//
// The wrapped OpenAI client reports Usage.InputTokens with the prompt-cache
// buckets subtracted out (loom's disjoint-bucket convention). This client
// recomputes CostUSD with cache-blind rates, so it must price the FULL prompt;
// pricing InputTokens alone would drop cached tokens out of the cost.
package mistral

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm/openai"
	"github.com/teradata-labs/loom/pkg/types"
)

// cachedUsageJSON: prompt_tokens 2000 of which 1536 were cache hits
// (OpenAI-style prompt_tokens_details.cached_tokens), 50 completion.
const cachedUsageJSON = `{"prompt_tokens":2000,"completion_tokens":50,"total_tokens":2050,"prompt_tokens_details":{"cached_tokens":1536}}`

func newCachedUsageClient(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":`+cachedUsageJSON+`}`)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Config{APIKey: "test-key", Model: "mistral-large-latest"})
	c.openai = openai.NewClient(openai.Config{APIKey: "test-key", Model: "mistral-large-latest", Endpoint: srv.URL})
	return c
}

func TestChat_CostPricesFullPromptIncludingCache(t *testing.T) {
	c := newCachedUsageClient(t)
	resp, err := c.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)

	assert.Equal(t, 464, resp.Usage.InputTokens, "uncached remainder")
	assert.Equal(t, 1536, resp.Usage.CacheReadInputTokens)
	assert.InDelta(t, c.calculateCost(2000, 50), resp.Usage.CostUSD, 1e-12,
		"cost must price the full 2000-token prompt, as before the convention change")
}

func TestChatStream_CostPricesFullPromptIncludingCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"index\":0}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}],\"usage\":"+cachedUsageJSON+"}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Config{APIKey: "test-key", Model: "mistral-large-latest"})
	c.openai = openai.NewClient(openai.Config{APIKey: "test-key", Model: "mistral-large-latest", Endpoint: srv.URL})

	resp, err := c.ChatStream(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 464, resp.Usage.InputTokens)
	assert.InDelta(t, c.calculateCost(2000, 50), resp.Usage.CostUSD, 1e-12)
}

// Copyright 2026 Teradata
//
// Pins loom's token convention on the Gemini path: Usage.InputTokens and
// Usage.TotalTokens exclude implicit-cache hits (as on Anthropic and Bedrock),
// while cost is still computed from the raw promptTokenCount.
package gemini

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/types"
)

func TestUsageMetadata_UncachedTokens(t *testing.T) {
	tests := []struct {
		name       string
		usage      UsageMetadata
		wantInput  int
		wantTotals int
	}{
		{"implicit cache hit", UsageMetadata{PromptTokenCount: 10000, CandidatesTokenCount: 200, TotalTokenCount: 10200, CachedContentTokenCount: 8192}, 1808, 2008},
		{"no cache", UsageMetadata{PromptTokenCount: 25, CandidatesTokenCount: 12, TotalTokenCount: 37}, 25, 37},
		{"cached exceeds prompt clamps at zero", UsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 5, TotalTokenCount: 15, CachedContentTokenCount: 50}, 0, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantInput, tt.usage.UncachedPromptTokens())
			assert.Equal(t, tt.wantTotals, tt.usage.UncachedTotalTokens())
		})
	}
}

func TestConvertResponse_InputTokensExcludeCache(t *testing.T) {
	c := NewClient(Config{APIKey: "test", Model: "gemini-2.5-flash"})
	got := c.convertResponse(&GenerateContentResponse{UsageMetadata: UsageMetadata{
		PromptTokenCount: 10000, CandidatesTokenCount: 200, TotalTokenCount: 10200, CachedContentTokenCount: 8192,
	}}).Usage

	assert.Equal(t, 1808, got.InputTokens)
	assert.Equal(t, 2008, got.TotalTokens)
	assert.Equal(t, 8192, got.CacheReadInputTokens)
	assert.Equal(t, 10200, got.RateLimitTokens, "Gemini meters cached tokens against rate limits")
	assert.InDelta(t, c.calculateCost(10000, 200), got.CostUSD, 1e-12)
}

// The streaming path carries the raw promptTokenCount / totalTokenCount from
// the final usage chunk: InputTokens and TotalTokens exclude the cache, cost
// and RateLimitTokens use the raw figures.
func TestChatStream_InputTokensExcludeCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"index":0}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"!"}]},"finishReason":"STOP","index":0}],`+
			`"usageMetadata":{"promptTokenCount":10000,"candidatesTokenCount":200,"totalTokenCount":10200,"cachedContentTokenCount":8192}}`+"\n\n")
	}))
	defer server.Close()

	c := NewClient(Config{APIKey: "test-key", Model: "gemini-2.5-flash"})
	c.httpClient.Transport = &mockTransport{baseURL: server.URL, original: http.DefaultTransport}

	resp, err := c.ChatStream(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, 1808, resp.Usage.InputTokens)
	assert.Equal(t, 2008, resp.Usage.TotalTokens)
	assert.Equal(t, 8192, resp.Usage.CacheReadInputTokens)
	assert.Equal(t, 10200, resp.Usage.RateLimitTokens)
	assert.InDelta(t, c.calculateCost(10000, 200), resp.Usage.CostUSD, 1e-12)
}

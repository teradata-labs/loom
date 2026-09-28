// Copyright 2026 Teradata
//
// Pins loom's token convention on the OpenAI-compatible path: Usage.InputTokens
// and Usage.TotalTokens exclude the prompt-cache buckets (as on Anthropic and
// Bedrock), while cost is still computed from the raw, cache-inclusive
// prompt_tokens.
package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatCompletionUsage_UncachedTokens(t *testing.T) {
	tests := []struct {
		name       string
		usage      ChatCompletionUsage
		wantInput  int
		wantTotals int
	}{
		{
			name: "litellm anthropic shape: read and write both inside prompt_tokens",
			usage: ChatCompletionUsage{PromptTokens: 17183, CompletionTokens: 336, TotalTokens: 17519,
				CacheReadInputTokens: 16817, CacheCreationInputTokens: 361},
			wantInput: 5, wantTotals: 341,
		},
		{
			name: "openai shape: prompt_tokens_details.cached_tokens",
			usage: func() ChatCompletionUsage {
				u := ChatCompletionUsage{PromptTokens: 2000, CompletionTokens: 50, TotalTokens: 2050}
				u.PromptTokensDetails = &struct {
					CachedTokens int `json:"cached_tokens,omitempty"`
				}{CachedTokens: 1536}
				return u
			}(),
			wantInput: 464, wantTotals: 514,
		},
		{
			name:      "no cache",
			usage:     ChatCompletionUsage{PromptTokens: 900, CompletionTokens: 100, TotalTokens: 1000},
			wantInput: 900, wantTotals: 1000,
		},
		{
			name: "gateway reporting buckets outside prompt_tokens clamps at zero",
			usage: ChatCompletionUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
				CacheReadInputTokens: 500},
			wantInput: 0, wantTotals: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantInput, tt.usage.UncachedPromptTokens())
			assert.Equal(t, tt.wantTotals, tt.usage.UncachedTotalTokens())
		})
	}
}

func TestConvertResponse_InputTokensExcludeCache(t *testing.T) {
	c := NewClient(Config{Model: "coding-agent/claude-sonnet-4-6", APIKey: "test"})
	resp := &ChatCompletionResponse{Usage: ChatCompletionUsage{
		PromptTokens: 17183, CompletionTokens: 336, TotalTokens: 17519,
		CacheReadInputTokens: 16817, CacheCreationInputTokens: 361,
	}}

	got := c.convertResponse(resp, 0).Usage

	assert.Equal(t, 5, got.InputTokens)
	assert.Equal(t, 336, got.OutputTokens)
	assert.Equal(t, 341, got.TotalTokens)
	assert.Equal(t, 16817, got.CacheReadInputTokens)
	assert.Equal(t, 361, got.CacheCreationInputTokens)
	// OpenAI-style TPM counts cached tokens: the scheduler charges the raw total.
	assert.Equal(t, 17519, got.RateLimitTokens)
	// Full prompt is recoverable from the disjoint buckets.
	assert.Equal(t, 17183, got.InputTokens+got.CacheReadInputTokens+got.CacheCreationInputTokens)
	// Cost is priced from the raw prompt_tokens, unchanged by the convention.
	assert.InDelta(t, c.calculateCost(17183, 336, 16817, 361), got.CostUSD, 1e-12)
}

func TestChatStream_CostUsesRawPromptTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"index\":0}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}],"+
			"\"usage\":{\"prompt_tokens\":18000,\"completion_tokens\":400,\"total_tokens\":18400,"+
			"\"cache_read_input_tokens\":15000,\"cache_creation_input_tokens\":2000}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewClient(Config{Model: "coding-agent/claude-sonnet-4-6", Endpoint: srv.URL, MaxTokens: 100})
	resp, err := c.ChatStream(context.Background(), nil, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, 1000, resp.Usage.InputTokens)
	assert.Equal(t, 1400, resp.Usage.TotalTokens)
	assert.Equal(t, 18400, resp.Usage.RateLimitTokens)
	assert.InDelta(t, c.calculateCost(18000, 400, 15000, 2000), resp.Usage.CostUSD, 1e-12)
}

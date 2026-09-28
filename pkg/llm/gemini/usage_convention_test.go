// Copyright 2026 Teradata
//
// Pins loom's token convention on the Gemini path: Usage.InputTokens and
// Usage.TotalTokens exclude implicit-cache hits (as on Anthropic and Bedrock),
// while cost is still computed from the raw promptTokenCount.
package gemini

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
	assert.InDelta(t, c.calculateCost(10000, 200), got.CostUSD, 1e-12)
}

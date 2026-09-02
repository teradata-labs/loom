// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestTierFromInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info *loomv1.ModelInfo
		want ModelTier
	}{
		{
			name: "nil info is unknown",
			info: nil,
			want: TierUnknown,
		},
		{
			name: "zero cost non-reasoning is local",
			info: &loomv1.ModelInfo{},
			want: TierLocal,
		},
		{
			name: "zero cost reasoning is local (rule order beats frontier)",
			info: &loomv1.ModelInfo{IsReasoning: true},
			want: TierLocal,
		},
		{
			name: "reasoning at frontier boundary 10.0 is frontier",
			info: &loomv1.ModelInfo{
				CostPer_1MInputUsd:  2.0,
				CostPer_1MOutputUsd: FrontierMinOutputCostUSD,
				IsReasoning:         true,
			},
			want: TierFrontier,
		},
		{
			name: "reasoning above frontier boundary is frontier",
			info: &loomv1.ModelInfo{
				CostPer_1MInputUsd:  5.0,
				CostPer_1MOutputUsd: 25.0,
				IsReasoning:         true,
			},
			want: TierFrontier,
		},
		{
			name: "reasoning just below frontier boundary is mid",
			info: &loomv1.ModelInfo{
				CostPer_1MInputUsd:  1.25,
				CostPer_1MOutputUsd: 9.99,
				IsReasoning:         true,
			},
			want: TierMid,
		},
		{
			name: "cheap reasoning is still mid",
			info: &loomv1.ModelInfo{
				CostPer_1MInputUsd:  0.05,
				CostPer_1MOutputUsd: 0.10,
				IsReasoning:         true,
			},
			want: TierMid,
		},
		{
			name: "non-reasoning at mid boundary 1.5 is mid",
			info: &loomv1.ModelInfo{
				CostPer_1MInputUsd:  0.4,
				CostPer_1MOutputUsd: MidMinOutputCostUSD,
			},
			want: TierMid,
		},
		{
			name: "non-reasoning expensive is mid, not frontier",
			info: &loomv1.ModelInfo{
				CostPer_1MInputUsd:  3.0,
				CostPer_1MOutputUsd: 14.0,
			},
			want: TierMid,
		},
		{
			name: "non-reasoning just below mid boundary is small-open",
			info: &loomv1.ModelInfo{
				CostPer_1MInputUsd:  0.3,
				CostPer_1MOutputUsd: 1.49,
			},
			want: TierSmallOpen,
		},
		{
			name: "non-reasoning cheap is small-open",
			info: &loomv1.ModelInfo{
				CostPer_1MInputUsd:  0.075,
				CostPer_1MOutputUsd: 0.20,
			},
			want: TierSmallOpen,
		},
		{
			name: "input priced but output free is not local",
			info: &loomv1.ModelInfo{CostPer_1MInputUsd: 0.5},
			want: TierSmallOpen,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, TierFromInfo(tt.info))
		})
	}
}

func TestTierOfRealCatalogEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		modelID  string
		want     ModelTier
	}{
		{
			name:     "claude opus 4.7 is frontier (reasoning, $25/1M out)",
			provider: "anthropic",
			modelID:  "claude-opus-4-7",
			want:     TierFrontier,
		},
		{
			name:     "gpt-5.4-pro is frontier",
			provider: "openai",
			modelID:  "gpt-5.4-pro",
			want:     TierFrontier,
		},
		{
			name:     "gemini 2.5 pro is frontier at the $10/1M out boundary",
			provider: "gemini",
			modelID:  "gemini-2.5-pro",
			want:     TierFrontier,
		},
		{
			name:     "claude haiku 4.5 is mid (reasoning, $5/1M out)",
			provider: "anthropic",
			modelID:  "claude-haiku-4-5-20251001",
			want:     TierMid,
		},
		{
			name:     "o3 is mid (reasoning, $8/1M out)",
			provider: "openai",
			modelID:  "o3",
			want:     TierMid,
		},
		{
			name:     "gpt-4.1-mini is mid (non-reasoning, $1.60/1M out)",
			provider: "openai",
			modelID:  "gpt-4.1-mini",
			want:     TierMid,
		},
		{
			name:     "mistral small is small-open ($0.20/1M out)",
			provider: "mistral",
			modelID:  "mistral-small-latest",
			want:     TierSmallOpen,
		},
		{
			name:     "gpt-4.1-nano is small-open",
			provider: "openai",
			modelID:  "gpt-4.1-nano",
			want:     TierSmallOpen,
		},
		{
			name:     "gemini 2.5 flash lite is small-open",
			provider: "gemini",
			modelID:  "gemini-2.5-flash-lite",
			want:     TierSmallOpen,
		},
		{
			name:     "ollama llama3.2 is local",
			provider: "ollama",
			modelID:  "llama3.2",
			want:     TierLocal,
		},
		{
			name:     "ollama deepseek-r1 is local despite reasoning (rule order)",
			provider: "ollama",
			modelID:  "deepseek-r1",
			want:     TierLocal,
		},
		{
			name:     "uncataloged pair is unknown",
			provider: "nope",
			modelID:  "not-a-model",
			want:     TierUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, TierOf(tt.provider, tt.modelID),
				"tier for %s/%s", tt.provider, tt.modelID)
		})
	}
}

func TestModelTierString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tier ModelTier
		want string
	}{
		{name: "unknown", tier: TierUnknown, want: "unknown"},
		{name: "local", tier: TierLocal, want: "local"},
		{name: "small open", tier: TierSmallOpen, want: "small-open"},
		{name: "mid", tier: TierMid, want: "mid"},
		{name: "frontier", tier: TierFrontier, want: "frontier"},
		{name: "above range", tier: TierFrontier + 1, want: "unknown"},
		{name: "below range", tier: ModelTier(-1), want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.tier.String())
		})
	}
}

func TestModelTierOrdering(t *testing.T) {
	t.Parallel()

	assert.Greater(t, TierFrontier, TierMid)
	assert.Greater(t, TierMid, TierSmallOpen)
	assert.Greater(t, TierSmallOpen, TierLocal)
	assert.Greater(t, TierLocal, TierUnknown)
}

// TestTierOfZeroAllocs backs the documented claim that TierOf allocates
// nothing on the static-source path. No t.Parallel: AllocsPerRun is unreliable
// when other tests run concurrently.
func TestTierOfZeroAllocs(t *testing.T) {
	// Warm the memoized static index so its one-time build is not counted.
	if got := TierOf("anthropic", "claude-opus-4-7"); got != TierFrontier {
		t.Fatalf("warmup tier = %v, want %v", got, TierFrontier)
	}

	allocs := testing.AllocsPerRun(100, func() {
		_ = TierOf("anthropic", "claude-opus-4-7")
	})
	assert.Zero(t, allocs, "TierOf should not allocate on the static-source path")
}

func BenchmarkTierOf(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = TierOf("anthropic", "claude-opus-4-7")
	}
}

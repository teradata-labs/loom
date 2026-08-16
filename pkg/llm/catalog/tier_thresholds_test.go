// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestDefaultTierThresholds(t *testing.T) {
	t.Parallel()

	th := DefaultTierThresholds()
	assert.Equal(t, FrontierMinOutputCostUSD, th.FrontierMinOutputCostUSD)
	assert.Equal(t, MidMinOutputCostUSD, th.MidMinOutputCostUSD)
}

// TestTierFromInfoWithCustomThresholds pins the reclassification behavior: the
// same ModelInfo lands in a different tier once the cutoffs move.
func TestTierFromInfoWithCustomThresholds(t *testing.T) {
	t.Parallel()

	reasoningAt5 := &loomv1.ModelInfo{
		CostPer_1MInputUsd:  1.0,
		CostPer_1MOutputUsd: 5.0,
		IsReasoning:         true,
	}
	nonReasoningAt1 := &loomv1.ModelInfo{
		CostPer_1MInputUsd:  0.3,
		CostPer_1MOutputUsd: 1.0,
	}

	tests := []struct {
		name string
		info *loomv1.ModelInfo
		th   TierThresholds
		want ModelTier
	}{
		{
			name: "reasoning at 5.0 is mid with defaults",
			info: reasoningAt5,
			th:   DefaultTierThresholds(),
			want: TierMid,
		},
		{
			name: "reasoning at 5.0 is frontier when frontier cutoff drops to 4",
			info: reasoningAt5,
			th:   TierThresholds{FrontierMinOutputCostUSD: 4.0},
			want: TierFrontier,
		},
		{
			name: "reasoning at 5.0 stays mid when frontier cutoff rises to 20",
			info: reasoningAt5,
			th:   TierThresholds{FrontierMinOutputCostUSD: 20.0},
			want: TierMid,
		},
		{
			name: "non-reasoning at 1.0 is small-open with defaults",
			info: nonReasoningAt1,
			th:   DefaultTierThresholds(),
			want: TierSmallOpen,
		},
		{
			name: "non-reasoning at 1.0 is mid when mid cutoff drops to 0.5",
			info: nonReasoningAt1,
			th:   TierThresholds{MidMinOutputCostUSD: 0.5},
			want: TierMid,
		},
		{
			name: "non-reasoning at 1.0 stays small-open when mid cutoff rises to 5",
			info: nonReasoningAt1,
			th:   TierThresholds{MidMinOutputCostUSD: 5.0},
			want: TierSmallOpen,
		},
		{
			name: "custom cutoffs are inclusive at the boundary",
			info: &loomv1.ModelInfo{
				CostPer_1MInputUsd:  1.0,
				CostPer_1MOutputUsd: 4.0,
				IsReasoning:         true,
			},
			th:   TierThresholds{FrontierMinOutputCostUSD: 4.0},
			want: TierFrontier,
		},
		{
			name: "both cutoffs custom, non-reasoning expensive is still mid",
			info: &loomv1.ModelInfo{
				CostPer_1MInputUsd:  8.0,
				CostPer_1MOutputUsd: 40.0,
			},
			th:   TierThresholds{FrontierMinOutputCostUSD: 4.0, MidMinOutputCostUSD: 0.5},
			want: TierMid,
		},
		{
			name: "zero cost stays local regardless of cutoffs",
			info: &loomv1.ModelInfo{IsReasoning: true},
			th:   TierThresholds{FrontierMinOutputCostUSD: 0.0001, MidMinOutputCostUSD: 0.0001},
			want: TierLocal,
		},
		{
			name: "nil info is unknown regardless of cutoffs",
			info: nil,
			th:   TierThresholds{FrontierMinOutputCostUSD: 1.0, MidMinOutputCostUSD: 0.1},
			want: TierUnknown,
		},
		{
			name: "negative fields fall back to the built-in constants",
			info: reasoningAt5,
			th:   TierThresholds{FrontierMinOutputCostUSD: -1, MidMinOutputCostUSD: -1},
			want: TierMid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, TierFromInfoWith(tt.info, tt.th))
		})
	}
}

// TestTierThresholdsCannotExpressZero documents the "0 means default" rule: a
// caller who wants a cutoff below the built-in one passes a small positive
// number, because 0 is indistinguishable from "unset".
func TestTierThresholdsCannotExpressZero(t *testing.T) {
	t.Parallel()

	info := &loomv1.ModelInfo{CostPer_1MInputUsd: 0.05, CostPer_1MOutputUsd: 0.10}

	// Asking for a 0 mid cutoff does not make this small-open model mid; the
	// field reads as "unset" and the built-in 1.5 applies.
	assert.Equal(t, TierSmallOpen,
		TierFromInfoWith(info, TierThresholds{MidMinOutputCostUSD: 0}),
		"a 0 threshold means default, not 'everything clears it'")

	// A small positive number is how the intent is expressed.
	assert.Equal(t, TierMid,
		TierFromInfoWith(info, TierThresholds{MidMinOutputCostUSD: 0.0001}),
		"a small positive threshold is the way to lower the cutoff")
}

// TestZeroValueTierThresholdsMatchesTierFromInfo covers every rule branch and
// asserts the zero value is indistinguishable from the defaults path.
func TestZeroValueTierThresholdsMatchesTierFromInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info *loomv1.ModelInfo
		want ModelTier
	}{
		{
			name: "nil",
			info: nil,
			want: TierUnknown,
		},
		{
			name: "zero cost non-reasoning",
			info: &loomv1.ModelInfo{},
			want: TierLocal,
		},
		{
			name: "zero cost reasoning",
			info: &loomv1.ModelInfo{IsReasoning: true},
			want: TierLocal,
		},
		{
			name: "reasoning expensive",
			info: &loomv1.ModelInfo{
				CostPer_1MInputUsd:  5.0,
				CostPer_1MOutputUsd: 25.0,
				IsReasoning:         true,
			},
			want: TierFrontier,
		},
		{
			name: "reasoning at frontier boundary",
			info: &loomv1.ModelInfo{
				CostPer_1MInputUsd:  2.0,
				CostPer_1MOutputUsd: FrontierMinOutputCostUSD,
				IsReasoning:         true,
			},
			want: TierFrontier,
		},
		{
			name: "reasoning cheap",
			info: &loomv1.ModelInfo{
				CostPer_1MInputUsd:  0.05,
				CostPer_1MOutputUsd: 0.10,
				IsReasoning:         true,
			},
			want: TierMid,
		},
		{
			name: "non-reasoning expensive",
			info: &loomv1.ModelInfo{
				CostPer_1MInputUsd:  3.0,
				CostPer_1MOutputUsd: 14.0,
			},
			want: TierMid,
		},
		{
			name: "non-reasoning at mid boundary",
			info: &loomv1.ModelInfo{
				CostPer_1MInputUsd:  0.4,
				CostPer_1MOutputUsd: MidMinOutputCostUSD,
			},
			want: TierMid,
		},
		{
			name: "non-reasoning cheap",
			info: &loomv1.ModelInfo{
				CostPer_1MInputUsd:  0.075,
				CostPer_1MOutputUsd: 0.20,
			},
			want: TierSmallOpen,
		},
		{
			name: "input priced but output free",
			info: &loomv1.ModelInfo{CostPer_1MInputUsd: 0.5},
			want: TierSmallOpen,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			legacy := TierFromInfo(tt.info)
			assert.Equal(t, tt.want, legacy, "TierFromInfo")
			assert.Equal(t, legacy, TierFromInfoWith(tt.info, TierThresholds{}),
				"zero-value thresholds must match TierFromInfo")
			assert.Equal(t, legacy, TierFromInfoWith(tt.info, DefaultTierThresholds()),
				"explicit defaults must match TierFromInfo")
		})
	}
}

// TestTierOfWithMatchesTierOf checks the lookup-based wrapper agrees with
// TierOf when handed default or zero-value thresholds.
func TestTierOfWithMatchesTierOf(t *testing.T) {
	t.Parallel()

	pairs := []struct{ provider, modelID string }{
		{"anthropic", "claude-opus-4-7"},
		{"anthropic", "claude-haiku-4-5-20251001"},
		{"openai", "gpt-4.1-nano"},
		{"ollama", "llama3.2"},
		{"nope", "not-a-model"},
	}

	for _, p := range pairs {
		t.Run(p.provider+"/"+p.modelID, func(t *testing.T) {
			t.Parallel()

			want := TierOf(p.provider, p.modelID)
			assert.Equal(t, want, TierOfWith(p.provider, p.modelID, TierThresholds{}))
			assert.Equal(t, want, TierOfWith(p.provider, p.modelID, DefaultTierThresholds()))
		})
	}
}

// TestTierOfWithCustomThresholds shows a real catalog entry moving tiers when
// the frontier cutoff is lowered.
func TestTierOfWithCustomThresholds(t *testing.T) {
	t.Parallel()

	// claude-haiku-4-5 is a reasoning model at $5/1M output: mid by default,
	// frontier once the frontier cutoff drops below its output price.
	require.Equal(t, TierMid, TierOf("anthropic", "claude-haiku-4-5-20251001"))
	assert.Equal(t, TierFrontier,
		TierOfWith("anthropic", "claude-haiku-4-5-20251001",
			TierThresholds{FrontierMinOutputCostUSD: 4.0}))
}

func TestParseModelTier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		in     string
		want   ModelTier
		wantOK bool
	}{
		{name: "unknown", in: "unknown", want: TierUnknown, wantOK: true},
		{name: "local", in: "local", want: TierLocal, wantOK: true},
		{name: "small open", in: "small-open", want: TierSmallOpen, wantOK: true},
		{name: "mid", in: "mid", want: TierMid, wantOK: true},
		{name: "frontier", in: "frontier", want: TierFrontier, wantOK: true},
		{name: "empty string", in: "", want: TierUnknown, wantOK: false},
		{name: "title case is rejected", in: "Frontier", want: TierUnknown, wantOK: false},
		{name: "upper case is rejected", in: "FRONTIER", want: TierUnknown, wantOK: false},
		{name: "underscore separator is rejected", in: "small_open", want: TierUnknown, wantOK: false},
		{name: "leading space is rejected", in: " mid", want: TierUnknown, wantOK: false},
		{name: "trailing space is rejected", in: "mid ", want: TierUnknown, wantOK: false},
		{name: "arbitrary string is rejected", in: "enormous", want: TierUnknown, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseModelTier(tt.in)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestParseModelTierRoundTrip asserts ParseModelTier is the exact inverse of
// ModelTier.String() for every defined tier.
func TestParseModelTierRoundTrip(t *testing.T) {
	t.Parallel()

	for _, tier := range []ModelTier{TierUnknown, TierLocal, TierSmallOpen, TierMid, TierFrontier} {
		t.Run(tier.String(), func(t *testing.T) {
			t.Parallel()
			got, ok := ParseModelTier(tier.String())
			require.True(t, ok, "String() output must parse back")
			assert.Equal(t, tier, got)
		})
	}
}

// TestTierOfWithZeroAllocs extends the TierOf zero-alloc guarantee to the
// threshold-aware entry points. No t.Parallel: AllocsPerRun is unreliable when
// other tests run concurrently.
func TestTierOfWithZeroAllocs(t *testing.T) {
	custom := TierThresholds{FrontierMinOutputCostUSD: 4.0, MidMinOutputCostUSD: 0.5}

	// Warm the memoized static index so its one-time build is not counted.
	if got := TierOfWith("anthropic", "claude-opus-4-7", custom); got != TierFrontier {
		t.Fatalf("warmup tier = %v, want %v", got, TierFrontier)
	}

	allocs := testing.AllocsPerRun(100, func() {
		_ = TierOfWith("anthropic", "claude-opus-4-7", custom)
	})
	assert.Zero(t, allocs, "TierOfWith should not allocate on the static-source path")

	defaults := testing.AllocsPerRun(100, func() {
		_ = TierOfWith("anthropic", "claude-opus-4-7", TierThresholds{})
	})
	assert.Zero(t, defaults, "TierOfWith with zero-value thresholds should not allocate")
}

func BenchmarkTierOfWith(b *testing.B) {
	th := TierThresholds{FrontierMinOutputCostUSD: 4.0, MidMinOutputCostUSD: 0.5}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = TierOfWith("anthropic", "claude-opus-4-7", th)
	}
}

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

package memory

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultBudgetConfig(t *testing.T) {
	bc := DefaultBudgetConfig(2000)
	assert.Equal(t, 2000, bc.MaxTokens)
	assert.Equal(t, DefaultEntityProfileBudget, bc.EntityProfileBudget)
	assert.Equal(t, DefaultGraphBudget, bc.GraphBudget)
	assert.Equal(t, 1500, bc.MemoryBudget()) // 2000 - 200 - 300
}

func TestDefaultBudgetConfig_ZeroMaxTokens(t *testing.T) {
	bc := DefaultBudgetConfig(0)
	assert.Equal(t, DefaultMaxTokens, bc.MaxTokens)
}

func TestBudgetConfig_MemoryBudget(t *testing.T) {
	tests := []struct {
		name      string
		config    BudgetConfig
		wantBudge int
	}{
		{
			name:      "normal budget",
			config:    BudgetConfig{MaxTokens: 5000, EntityProfileBudget: 200, GraphBudget: 300},
			wantBudge: 4500,
		},
		{
			name:      "minimal budget - overcommitted phases",
			config:    BudgetConfig{MaxTokens: 300, EntityProfileBudget: 200, GraphBudget: 300},
			wantBudge: 0,
		},
		{
			name:      "zero max tokens",
			config:    BudgetConfig{MaxTokens: 0, EntityProfileBudget: 200, GraphBudget: 300},
			wantBudge: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantBudge, tt.config.MemoryBudget())
		})
	}
}

func TestAllocateMemoryBudget(t *testing.T) {
	tests := []struct {
		name            string
		candidates      []ScoredMemory
		remainingTokens int
		wantCount       int
		wantTokens      int
		wantSummaryAt   []int // indices that should use summary
	}{
		{
			name: "all content fits",
			candidates: []ScoredMemory{
				{Memory: &Memory{ID: "a", TokenCount: 100, SummaryTokenCount: 30}, ComputedSalience: 0.9},
				{Memory: &Memory{ID: "b", TokenCount: 100, SummaryTokenCount: 30}, ComputedSalience: 0.8},
			},
			remainingTokens: 500,
			wantCount:       2,
			wantTokens:      200,
		},
		{
			name: "summary fallback",
			candidates: []ScoredMemory{
				{Memory: &Memory{ID: "a", TokenCount: 100, SummaryTokenCount: 30}, ComputedSalience: 0.9},
				{Memory: &Memory{ID: "b", TokenCount: 200, SummaryTokenCount: 50}, ComputedSalience: 0.8},
			},
			remainingTokens: 160,
			wantCount:       2,
			wantTokens:      150, // 100 (full) + 50 (summary)
			wantSummaryAt:   []int{1},
		},
		{
			name: "skip when neither fits",
			candidates: []ScoredMemory{
				{Memory: &Memory{ID: "a", TokenCount: 100, SummaryTokenCount: 30}, ComputedSalience: 0.9},
				{Memory: &Memory{ID: "b", TokenCount: 200, SummaryTokenCount: 80}, ComputedSalience: 0.8},
			},
			remainingTokens: 110,
			wantCount:       1,
			wantTokens:      100,
		},
		{
			name: "mixed content and summary results",
			candidates: []ScoredMemory{
				{Memory: &Memory{ID: "a", TokenCount: 50, SummaryTokenCount: 20}, ComputedSalience: 0.9},
				{Memory: &Memory{ID: "b", TokenCount: 300, SummaryTokenCount: 40}, ComputedSalience: 0.8},
				{Memory: &Memory{ID: "c", TokenCount: 50, SummaryTokenCount: 20}, ComputedSalience: 0.7},
			},
			remainingTokens: 120,
			wantCount:       3,
			wantTokens:      110, // 50 (full) + 40 (summary) + 20 (summary)
			wantSummaryAt:   []int{1, 2},
		},
		{
			name:            "zero budget",
			candidates:      []ScoredMemory{{Memory: &Memory{ID: "a", TokenCount: 10, SummaryTokenCount: 5}}},
			remainingTokens: 0,
			wantCount:       0,
			wantTokens:      0,
		},
		{
			name: "exact budget boundary - content",
			candidates: []ScoredMemory{
				{Memory: &Memory{ID: "a", TokenCount: 100, SummaryTokenCount: 30}},
			},
			remainingTokens: 100,
			wantCount:       1,
			wantTokens:      100,
		},
		{
			name: "exact budget boundary - summary",
			candidates: []ScoredMemory{
				{Memory: &Memory{ID: "a", TokenCount: 200, SummaryTokenCount: 100}},
			},
			remainingTokens: 100,
			wantCount:       1,
			wantTokens:      100,
			wantSummaryAt:   []int{0},
		},
		{
			name:            "nil candidates",
			candidates:      nil,
			remainingTokens: 1000,
			wantCount:       0,
			wantTokens:      0,
		},
		{
			name: "nil memory skipped",
			candidates: []ScoredMemory{
				{Memory: nil},
				{Memory: &Memory{ID: "a", TokenCount: 50, SummaryTokenCount: 20}},
			},
			remainingTokens: 1000,
			wantCount:       1,
			wantTokens:      50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, tokensUsed := AllocateMemoryBudget(tt.candidates, tt.remainingTokens)
			assert.Equal(t, tt.wantCount, len(result), "result count")
			assert.Equal(t, tt.wantTokens, tokensUsed, "tokens used")

			// Verify summary flags.
			summarySet := make(map[int]bool)
			for _, idx := range tt.wantSummaryAt {
				summarySet[idx] = true
			}
			for i, sm := range result {
				if summarySet[i] {
					assert.True(t, sm.UsedSummary, "index %d should use summary", i)
				} else {
					assert.False(t, sm.UsedSummary, "index %d should use full content", i)
				}
			}

			// Token budget never exceeded.
			require.LessOrEqual(t, tokensUsed, tt.remainingTokens, "budget exceeded")
		})
	}
}

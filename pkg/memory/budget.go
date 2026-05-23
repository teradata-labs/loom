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

// BudgetConfig configures the phased token budget allocation for ContextFor queries.
type BudgetConfig struct {
	// MaxTokens is the total token budget.
	MaxTokens int
	// EntityProfileBudget is the budget reserved for entity profile (phase 1).
	EntityProfileBudget int
	// GraphBudget is the budget reserved for graph neighborhood (phase 2).
	GraphBudget int
}

// Default budget allocations.
const (
	DefaultEntityProfileBudget = 200
	DefaultGraphBudget         = 300
	DefaultMaxTokens           = 2000
)

// DefaultBudgetConfig returns a config with spec defaults for a given total budget.
func DefaultBudgetConfig(maxTokens int) BudgetConfig {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	return BudgetConfig{
		MaxTokens:           maxTokens,
		EntityProfileBudget: DefaultEntityProfileBudget,
		GraphBudget:         DefaultGraphBudget,
	}
}

// MemoryBudget returns the remaining budget available for memories after
// entity profile and graph neighborhood are allocated.
func (bc BudgetConfig) MemoryBudget() int {
	remaining := bc.MaxTokens - bc.EntityProfileBudget - bc.GraphBudget
	if remaining < 0 {
		return 0
	}
	return remaining
}

// AllocateMemoryBudget takes sorted candidates (highest salience first) and
// packs them into the remaining token budget with graceful degradation:
//   - If full content fits -> include content
//   - Elif summary fits -> include summary (set UsedSummary=true)
//   - Else -> skip
//
// Returns the packed memories and total tokens consumed.
func AllocateMemoryBudget(candidates []ScoredMemory, remainingTokens int) ([]ScoredMemory, int) {
	if remainingTokens <= 0 {
		return nil, 0
	}

	result := make([]ScoredMemory, 0, len(candidates))
	tokensUsed := 0

	for _, sm := range candidates {
		if sm.Memory == nil {
			continue
		}

		contentTokens := sm.Memory.TokenCount
		summaryTokens := sm.Memory.SummaryTokenCount

		if contentTokens > 0 && tokensUsed+contentTokens <= remainingTokens {
			// Full content fits.
			packed := sm
			packed.UsedSummary = false
			result = append(result, packed)
			tokensUsed += contentTokens
		} else if summaryTokens > 0 && tokensUsed+summaryTokens <= remainingTokens {
			// Summary fits.
			packed := sm
			packed.UsedSummary = true
			result = append(result, packed)
			tokensUsed += summaryTokens
		}
		// Else: neither fits, skip this memory.
	}

	return result, tokensUsed
}

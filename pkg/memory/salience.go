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
	"math"
	"sort"
	"time"
)

// Salience engine defaults.
const (
	DefaultDecayRate          = 0.995
	DefaultBoostAmount        = 0.05
	DefaultMinSalience        = 0.1
	DefaultSalience           = 0.5
	DefaultConsolidationDecay = 0.5
)

// SalienceConfig holds parameters for salience computation.
type SalienceConfig struct {
	DecayRate          float64
	BoostAmount        float64
	MinSalience        float64
	ConsolidationDecay float64
}

// DefaultSalienceConfig returns a config with spec defaults.
func DefaultSalienceConfig() SalienceConfig {
	return SalienceConfig{
		DecayRate:          DefaultDecayRate,
		BoostAmount:        DefaultBoostAmount,
		MinSalience:        DefaultMinSalience,
		ConsolidationDecay: DefaultConsolidationDecay,
	}
}

// ComputeSalience applies time-based decay to a base salience value.
// Formula: S_current = S_base * decay_rate^(days_since_last_access_or_creation)
func ComputeSalience(base float64, lastAccessOrCreation time.Time, now time.Time, decayRate float64) float64 {
	if base <= 0 {
		return 0
	}
	if decayRate <= 0 || decayRate > 1 {
		return base
	}

	days := now.Sub(lastAccessOrCreation).Hours() / 24.0
	if days < 0 {
		// Future timestamp — no decay.
		return base
	}

	decayed := base * math.Pow(decayRate, days)

	// Clamp to [0, 1].
	if decayed < 0 {
		return 0
	}
	if decayed > 1 {
		return 1
	}
	return decayed
}

// BoostSalience increases salience by the boost amount, capped at 1.0.
func BoostSalience(current, boost float64) float64 {
	result := current + boost
	if result > 1.0 {
		return 1.0
	}
	if result < 0 {
		return 0
	}
	return result
}

// RankBySalience computes salience for each memory, filters by threshold, and sorts descending.
func RankBySalience(candidates []*Memory, config SalienceConfig, now time.Time) []ScoredMemory {
	scored := make([]ScoredMemory, 0, len(candidates))

	for _, mem := range candidates {
		// Use accessed_at if available, otherwise created_at.
		refTime := mem.CreatedAt
		if mem.AccessedAt != nil {
			refTime = *mem.AccessedAt
		}

		computed := ComputeSalience(mem.Salience, refTime, now, config.DecayRate)
		if computed < config.MinSalience {
			continue
		}

		scored = append(scored, ScoredMemory{
			Memory:           mem,
			ComputedSalience: computed,
			CombinedScore:    computed, // no relevance score yet
		})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].CombinedScore > scored[j].CombinedScore
	})

	return scored
}

// CombineScores combines salience and relevance into a final score.
// Uses multiplicative combination: combined = salience * relevance.
func CombineScores(salience, relevance float64) float64 {
	return salience * relevance
}

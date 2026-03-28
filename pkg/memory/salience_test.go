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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeSalience(t *testing.T) {
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		base      float64
		refTime   time.Time
		decayRate float64
		wantMin   float64
		wantMax   float64
	}{
		{
			name:      "zero days - no decay",
			base:      0.8,
			refTime:   now,
			decayRate: DefaultDecayRate,
			wantMin:   0.799,
			wantMax:   0.801,
		},
		{
			name:      "one day",
			base:      0.8,
			refTime:   now.Add(-24 * time.Hour),
			decayRate: DefaultDecayRate,
			wantMin:   0.795,
			wantMax:   0.800,
		},
		{
			name:      "30 days",
			base:      0.8,
			refTime:   now.Add(-30 * 24 * time.Hour),
			decayRate: DefaultDecayRate,
			wantMin:   0.68,
			wantMax:   0.72,
		},
		{
			name:      "138 days - half-life",
			base:      1.0,
			refTime:   now.Add(-138 * 24 * time.Hour),
			decayRate: DefaultDecayRate,
			wantMin:   0.49,
			wantMax:   0.51,
		},
		{
			name:      "365 days - significant decay",
			base:      1.0,
			refTime:   now.Add(-365 * 24 * time.Hour),
			decayRate: DefaultDecayRate,
			wantMin:   0.15,
			wantMax:   0.17,
		},
		{
			name:      "future timestamp - no decay",
			base:      0.8,
			refTime:   now.Add(24 * time.Hour),
			decayRate: DefaultDecayRate,
			wantMin:   0.799,
			wantMax:   0.801,
		},
		{
			name:      "zero base - stays zero",
			base:      0.0,
			refTime:   now.Add(-10 * 24 * time.Hour),
			decayRate: DefaultDecayRate,
			wantMin:   0.0,
			wantMax:   0.001,
		},
		{
			name:      "negative base - clamped to zero",
			base:      -0.5,
			refTime:   now,
			decayRate: DefaultDecayRate,
			wantMin:   0.0,
			wantMax:   0.001,
		},
		{
			name:      "invalid decay rate (>1) - no decay",
			base:      0.8,
			refTime:   now.Add(-10 * 24 * time.Hour),
			decayRate: 1.5,
			wantMin:   0.799,
			wantMax:   0.801,
		},
		{
			name:      "invalid decay rate (<=0) - no decay",
			base:      0.8,
			refTime:   now.Add(-10 * 24 * time.Hour),
			decayRate: 0.0,
			wantMin:   0.799,
			wantMax:   0.801,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeSalience(tt.base, tt.refTime, now, tt.decayRate)
			assert.GreaterOrEqual(t, got, tt.wantMin, "salience too low")
			assert.LessOrEqual(t, got, tt.wantMax, "salience too high")
		})
	}
}

func TestBoostSalience(t *testing.T) {
	tests := []struct {
		name    string
		current float64
		boost   float64
		want    float64
	}{
		{"normal boost", 0.5, 0.05, 0.55},
		{"boost caps at 1.0", 0.97, 0.05, 1.0},
		{"boost from 0.95", 0.95, 0.05, 1.0},
		{"zero boost", 0.5, 0.0, 0.5},
		{"large boost caps at 1.0", 0.3, 0.9, 1.0},
		{"negative current", -0.1, 0.05, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BoostSalience(tt.current, tt.boost)
			assert.InDelta(t, tt.want, got, 0.001)
		})
	}
}

func TestRankBySalience(t *testing.T) {
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	config := DefaultSalienceConfig()

	recentAccess := now.Add(-1 * 24 * time.Hour)
	staleAccess := now.Add(-200 * 24 * time.Hour)

	candidates := []*Memory{
		{ID: "fresh", Salience: 0.8, CreatedAt: now, MemoryType: "fact"},
		{ID: "stale", Salience: 0.3, CreatedAt: now.Add(-300 * 24 * time.Hour), MemoryType: "fact"},
		{ID: "accessed", Salience: 0.6, CreatedAt: now.Add(-100 * 24 * time.Hour), AccessedAt: &recentAccess, MemoryType: "fact"},
		{ID: "below-threshold", Salience: 0.05, CreatedAt: now, MemoryType: "fact"},
		{ID: "stale-accessed", Salience: 0.5, CreatedAt: now.Add(-300 * 24 * time.Hour), AccessedAt: &staleAccess, MemoryType: "fact"},
	}

	scored := RankBySalience(candidates, config, now)

	// "below-threshold" should be filtered out (0.05 < 0.1).
	for _, sm := range scored {
		assert.NotEqual(t, "below-threshold", sm.Memory.ID, "below threshold should be filtered")
	}

	// Should be sorted by computed salience descending.
	for i := 1; i < len(scored); i++ {
		assert.GreaterOrEqual(t, scored[i-1].CombinedScore, scored[i].CombinedScore,
			"should be sorted descending")
	}

	// "fresh" should rank first (highest salience, no decay).
	require.NotEmpty(t, scored)
	assert.Equal(t, "fresh", scored[0].Memory.ID)
}

func TestRankBySalience_EmptyInput(t *testing.T) {
	now := time.Now()
	config := DefaultSalienceConfig()

	scored := RankBySalience(nil, config, now)
	assert.Empty(t, scored)

	scored = RankBySalience([]*Memory{}, config, now)
	assert.Empty(t, scored)
}

func TestCombineScores(t *testing.T) {
	tests := []struct {
		name      string
		salience  float64
		relevance float64
		want      float64
	}{
		{"both 1.0", 1.0, 1.0, 1.0},
		{"both 0.5", 0.5, 0.5, 0.25},
		{"one zero", 0.0, 0.8, 0.0},
		{"mixed", 0.8, 0.6, 0.48},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CombineScores(tt.salience, tt.relevance)
			assert.InDelta(t, tt.want, got, 0.001)
		})
	}
}

func TestDefaultDecayRate_HalfLife(t *testing.T) {
	// Verify the documented half-life: 0.995^138 ≈ 0.5
	halfLife := math.Pow(DefaultDecayRate, 138)
	assert.InDelta(t, 0.5, halfLife, 0.01, "half-life should be ~138 days")
}

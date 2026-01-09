// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build hawk

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package judges

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestNewRegistry(t *testing.T) {
	registry := NewRegistry()
	require.NotNil(t, registry)
	assert.Equal(t, 0, registry.Count())
}

func TestRegistry_Register(t *testing.T) {
	registry := NewRegistry()

	judge := &simpleJudge{
		id:   "test-judge",
		name: "Test Judge",
	}

	err := registry.Register(judge)
	require.NoError(t, err)
	assert.Equal(t, 1, registry.Count())
}

func TestRegistry_Register_Duplicate(t *testing.T) {
	registry := NewRegistry()

	judge := &simpleJudge{
		id:   "test-judge",
		name: "Test Judge",
	}

	// Register once
	err := registry.Register(judge)
	require.NoError(t, err)

	// Try to register again with same ID
	err = registry.Register(judge)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
	assert.Equal(t, 1, registry.Count())
}

func TestRegistry_Unregister(t *testing.T) {
	registry := NewRegistry()

	judge := &simpleJudge{id: "test-judge", name: "Test Judge"}
	_ = registry.Register(judge)

	err := registry.Unregister("test-judge")
	require.NoError(t, err)
	assert.Equal(t, 0, registry.Count())
}

func TestRegistry_Unregister_NotFound(t *testing.T) {
	registry := NewRegistry()

	err := registry.Unregister("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRegistry_Get(t *testing.T) {
	registry := NewRegistry()

	judge := &simpleJudge{id: "test-judge", name: "Test Judge"}
	_ = registry.Register(judge)

	retrieved, err := registry.Get("test-judge")
	require.NoError(t, err)
	assert.Equal(t, judge, retrieved)
	assert.Equal(t, "test-judge", retrieved.ID())
}

func TestRegistry_Get_NotFound(t *testing.T) {
	registry := NewRegistry()

	_, err := registry.Get("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRegistry_GetJudges(t *testing.T) {
	registry := NewRegistry()

	judge1 := &simpleJudge{id: "judge1", name: "Judge 1"}
	judge2 := &simpleJudge{id: "judge2", name: "Judge 2"}
	judge3 := &simpleJudge{id: "judge3", name: "Judge 3"}

	_ = registry.Register(judge1)
	_ = registry.Register(judge2)
	_ = registry.Register(judge3)

	// Get multiple judges
	judges, err := registry.GetJudges([]string{"judge1", "judge3"})
	require.NoError(t, err)
	assert.Len(t, judges, 2)
	assert.Equal(t, "judge1", judges[0].ID())
	assert.Equal(t, "judge3", judges[1].ID())
}

func TestRegistry_GetJudges_SomeNotFound(t *testing.T) {
	registry := NewRegistry()

	judge1 := &simpleJudge{id: "judge1", name: "Judge 1"}
	_ = registry.Register(judge1)

	// Request includes one that doesn't exist
	_, err := registry.GetJudges([]string{"judge1", "nonexistent"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent")
}

func TestRegistry_GetAll(t *testing.T) {
	registry := NewRegistry()

	judge1 := &simpleJudge{id: "judge1", name: "Judge 1"}
	judge2 := &simpleJudge{id: "judge2", name: "Judge 2"}

	_ = registry.Register(judge1)
	_ = registry.Register(judge2)

	all := registry.GetAll()
	assert.Len(t, all, 2)
}

func TestRegistry_List(t *testing.T) {
	registry := NewRegistry()

	judge1 := &simpleJudge{id: "judge1", name: "Judge 1"}
	judge2 := &simpleJudge{id: "judge2", name: "Judge 2"}

	_ = registry.Register(judge1)
	_ = registry.Register(judge2)

	infos := registry.List()
	assert.Len(t, infos, 2)

	// Extract IDs
	ids := make([]string, len(infos))
	for i, info := range infos {
		ids[i] = info.ID
	}
	assert.Contains(t, ids, "judge1")
	assert.Contains(t, ids, "judge2")
}

func TestRegistry_FilterByCriticality(t *testing.T) {
	registry := NewRegistry()

	critical := &simpleJudge{
		id:          "critical",
		name:        "Critical Judge",
		criticality: loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL,
	}

	nonCritical := &simpleJudge{
		id:          "non-critical",
		name:        "Non-Critical Judge",
		criticality: loomv1.JudgeCriticality_JUDGE_CRITICALITY_NON_CRITICAL,
	}

	safetyCritical := &simpleJudge{
		id:          "safety",
		name:        "Safety Judge",
		criticality: loomv1.JudgeCriticality_JUDGE_CRITICALITY_SAFETY_CRITICAL,
	}

	_ = registry.Register(critical)
	_ = registry.Register(nonCritical)
	_ = registry.Register(safetyCritical)

	// Filter for critical only
	filtered := registry.FilterByCriticality(loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL)
	assert.Len(t, filtered, 1)
	assert.Equal(t, "critical", filtered[0].ID())

	// Filter for safety critical
	filtered = registry.FilterByCriticality(loomv1.JudgeCriticality_JUDGE_CRITICALITY_SAFETY_CRITICAL)
	assert.Len(t, filtered, 1)
	assert.Equal(t, "safety", filtered[0].ID())
}

func TestRegistry_FilterByDimension(t *testing.T) {
	registry := NewRegistry()

	qualityJudge := &simpleJudge{
		id:         "quality",
		name:       "Quality Judge",
		dimensions: []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY},
	}

	costJudge := &simpleJudge{
		id:         "cost",
		name:       "Cost Judge",
		dimensions: []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_COST},
	}

	multiDimensionJudge := &simpleJudge{
		id:   "multi",
		name: "Multi Judge",
		dimensions: []loomv1.JudgeDimension{
			loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY,
			loomv1.JudgeDimension_JUDGE_DIMENSION_SAFETY,
		},
	}

	_ = registry.Register(qualityJudge)
	_ = registry.Register(costJudge)
	_ = registry.Register(multiDimensionJudge)

	// Filter for quality dimension
	filtered := registry.FilterByDimension(loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY)
	assert.Len(t, filtered, 2) // qualityJudge and multiDimensionJudge
	ids := []string{filtered[0].ID(), filtered[1].ID()}
	assert.Contains(t, ids, "quality")
	assert.Contains(t, ids, "multi")

	// Filter for cost dimension
	filtered = registry.FilterByDimension(loomv1.JudgeDimension_JUDGE_DIMENSION_COST)
	assert.Len(t, filtered, 1)
	assert.Equal(t, "cost", filtered[0].ID())
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	registry := NewRegistry()

	// Test thread safety with concurrent register/get operations
	done := make(chan bool, 10)

	// Concurrent registers
	for i := 0; i < 5; i++ {
		go func(idx int) {
			judge := &simpleJudge{
				id:   string(rune('A' + idx)),
				name: string(rune('A' + idx)),
			}
			_ = registry.Register(judge)
			done <- true
		}(i)
	}

	// Concurrent gets
	for i := 0; i < 5; i++ {
		go func(idx int) {
			_ = registry.GetAll()
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify state is consistent
	assert.Equal(t, 5, registry.Count())
}

// simpleJudge is a minimal judge implementation for testing registry
type simpleJudge struct {
	id          string
	name        string
	weight      float64
	criticality loomv1.JudgeCriticality
	criteria    []string
	dimensions  []loomv1.JudgeDimension
}

func (j *simpleJudge) ID() string   { return j.id }
func (j *simpleJudge) Name() string { return j.name }
func (j *simpleJudge) Weight() float64 {
	if j.weight == 0 {
		return 1.0
	}
	return j.weight
}
func (j *simpleJudge) Criticality() loomv1.JudgeCriticality { return j.criticality }
func (j *simpleJudge) Criteria() []string                   { return j.criteria }
func (j *simpleJudge) Dimensions() []loomv1.JudgeDimension  { return j.dimensions }
func (j *simpleJudge) Config() *loomv1.JudgeConfig {
	return &loomv1.JudgeConfig{
		Id:          j.id,
		Name:        j.name,
		Weight:      j.weight,
		Criticality: j.criticality,
		Dimensions:  j.dimensions,
	}
}
func (j *simpleJudge) Evaluate(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
	return &loomv1.JudgeResult{
		JudgeId:      j.id,
		JudgeName:    j.name,
		Verdict:      "PASS",
		OverallScore: 90.0,
	}, nil
}

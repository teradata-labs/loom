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
package metaagent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
)

func TestNewPatternSelector(t *testing.T) {
	ps := NewPatternSelector(observability.NewNoOpTracer())

	assert.NotNil(t, ps)
	assert.NotNil(t, ps.capabilityMap)
	assert.Greater(t, len(ps.capabilityMap), 0, "Should have capability mappings")
}

func TestPatternSelector_SelectPatterns_SQL_DataQuality(t *testing.T) {
	ps := NewPatternSelector(observability.NewNoOpTracer())
	ctx := context.Background()

	analysis := &Analysis{
		Domain: DomainSQL,
		Capabilities: []Capability{
			{Name: "data_quality", Category: "data_quality", Priority: 1},
			{Name: "data_profiling", Category: "data_quality", Priority: 2},
		},
		Complexity: ComplexityMedium,
	}

	patterns, err := ps.SelectPatterns(ctx, analysis)
	require.NoError(t, err)
	assert.NotEmpty(t, patterns, "Should select patterns for data quality")

	// Should include data quality patterns
	hasDataQuality := false
	for _, p := range patterns {
		if containsAny(p, []string{"data_quality", "data_profiling", "duplicate_detection"}) {
			hasDataQuality = true
			break
		}
	}
	assert.True(t, hasDataQuality, "Should include data quality patterns")
}

func TestPatternSelector_SelectPatterns_SQL_Performance(t *testing.T) {
	ps := NewPatternSelector(observability.NewNoOpTracer())
	ctx := context.Background()

	analysis := &Analysis{
		Domain: DomainSQL,
		Capabilities: []Capability{
			{Name: "performance_analysis", Category: "analytics", Priority: 1},
			{Name: "index_optimization", Category: "analytics", Priority: 2},
		},
		Complexity: ComplexityHigh,
	}

	patterns, err := ps.SelectPatterns(ctx, analysis)
	require.NoError(t, err)
	assert.NotEmpty(t, patterns, "Should select patterns for performance")

	// Should include performance patterns
	hasPerformance := false
	for _, p := range patterns {
		if containsAny(p, []string{"sequential_scan", "missing_index", "query_rewrite", "join_optimization"}) {
			hasPerformance = true
			break
		}
	}
	assert.True(t, hasPerformance, "Should include performance patterns")
}

func TestPatternSelector_SelectPatterns_REST_API(t *testing.T) {
	ps := NewPatternSelector(observability.NewNoOpTracer())
	ctx := context.Background()

	analysis := &Analysis{
		Domain: DomainREST,
		Capabilities: []Capability{
			{Name: "api_monitoring", Category: "api", Priority: 1},
		},
		Complexity: ComplexityLow,
	}

	patterns, err := ps.SelectPatterns(ctx, analysis)
	require.NoError(t, err)

	// REST domain might not have many patterns yet, so just check no error
	assert.NotNil(t, patterns)
}

func TestPatternSelector_SelectPatterns_NoCapabilities(t *testing.T) {
	ps := NewPatternSelector(observability.NewNoOpTracer())
	ctx := context.Background()

	analysis := &Analysis{
		Domain:       DomainSQL,
		Capabilities: []Capability{}, // Empty capabilities
		Complexity:   ComplexityLow,
	}

	patterns, err := ps.SelectPatterns(ctx, analysis)
	require.NoError(t, err)

	// Should return domain defaults
	assert.NotEmpty(t, patterns, "Should return domain defaults when no capabilities")
}

func TestPatternSelector_SelectPatterns_NilAnalysis(t *testing.T) {
	ps := NewPatternSelector(observability.NewNoOpTracer())
	ctx := context.Background()

	patterns, err := ps.SelectPatterns(ctx, nil)
	assert.Error(t, err)
	assert.Nil(t, patterns)
	assert.Contains(t, err.Error(), "nil")
}

func TestPatternSelector_SelectPatterns_MachineLearning(t *testing.T) {
	ps := NewPatternSelector(observability.NewNoOpTracer())
	ctx := context.Background()

	analysis := &Analysis{
		Domain: DomainSQL,
		Capabilities: []Capability{
			{Name: "regression", Category: "ml", Priority: 1},
			{Name: "classification", Category: "ml", Priority: 2},
		},
		Complexity: ComplexityHigh,
	}

	patterns, err := ps.SelectPatterns(ctx, analysis)
	require.NoError(t, err)
	assert.NotEmpty(t, patterns)

	// Should include ML patterns
	hasML := false
	for _, p := range patterns {
		if containsAny(p, []string{"regression", "classification", "ml/"}) {
			hasML = true
			break
		}
	}
	assert.True(t, hasML, "Should include ML patterns")
}

func TestPatternSelector_SelectPatterns_TimeSeries(t *testing.T) {
	ps := NewPatternSelector(observability.NewNoOpTracer())
	ctx := context.Background()

	analysis := &Analysis{
		Domain: DomainSQL,
		Capabilities: []Capability{
			{Name: "time_series", Category: "timeseries", Priority: 1},
			{Name: "forecasting", Category: "timeseries", Priority: 2},
		},
		Complexity: ComplexityMedium,
	}

	patterns, err := ps.SelectPatterns(ctx, analysis)
	require.NoError(t, err)
	assert.NotEmpty(t, patterns)

	// Should include time series patterns
	hasTimeSeries := false
	for _, p := range patterns {
		if containsAny(p, []string{"timeseries", "moving_average", "arima"}) {
			hasTimeSeries = true
			break
		}
	}
	assert.True(t, hasTimeSeries, "Should include time series patterns")
}

func TestPatternSelector_SelectPatterns_TextAnalysis(t *testing.T) {
	ps := NewPatternSelector(observability.NewNoOpTracer())
	ctx := context.Background()

	analysis := &Analysis{
		Domain: DomainSQL,
		Capabilities: []Capability{
			{Name: "text_analysis", Category: "text", Priority: 1},
			{Name: "sentiment_analysis", Category: "text", Priority: 2},
		},
		Complexity: ComplexityMedium,
	}

	patterns, err := ps.SelectPatterns(ctx, analysis)
	require.NoError(t, err)
	assert.NotEmpty(t, patterns)

	// Should include text patterns
	hasText := false
	for _, p := range patterns {
		if containsAny(p, []string{"text", "sentiment", "ngram"}) {
			hasText = true
			break
		}
	}
	assert.True(t, hasText, "Should include text patterns")
}

func TestPatternSelector_ScorePattern(t *testing.T) {
	ps := NewPatternSelector(observability.NewNoOpTracer())

	analysis := &Analysis{
		Domain:     DomainSQL,
		Complexity: ComplexityHigh,
	}

	// Test scoring with high-priority capability
	highPriorityCapability := Capability{
		Name:     "test_capability",
		Priority: 1,
	}
	score1 := ps.scorePattern("sql/test_pattern", highPriorityCapability, analysis)

	// Test scoring with low-priority capability
	lowPriorityCapability := Capability{
		Name:     "test_capability",
		Priority: 10,
	}
	score2 := ps.scorePattern("sql/test_pattern", lowPriorityCapability, analysis)

	// High priority should score higher
	assert.Greater(t, score1, score2, "High priority capability should score higher")

	// Domain match should boost score
	assert.Greater(t, score1, 0.5, "Domain match should boost score above base")
}

func TestPatternSelector_MatchCapability(t *testing.T) {
	ps := NewPatternSelector(observability.NewNoOpTracer())

	tests := []struct {
		name       string
		capability Capability
		domain     DomainType
		wantCount  int
	}{
		{
			name: "exact match - data_quality",
			capability: Capability{
				Name:     "data_quality",
				Category: "data_quality",
			},
			domain:    DomainSQL,
			wantCount: 5, // Should match multiple data quality patterns
		},
		{
			name: "fuzzy match - performance",
			capability: Capability{
				Name:     "sql_performance_analysis",
				Category: "analytics",
			},
			domain:    DomainSQL,
			wantCount: 2, // Should match performance patterns
		},
		{
			name: "no match",
			capability: Capability{
				Name:     "nonexistent_capability",
				Category: "nonexistent",
			},
			domain:    DomainSQL,
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patterns := ps.matchCapability(tt.capability, tt.domain)
			assert.Len(t, patterns, tt.wantCount, "Pattern count mismatch")
		})
	}
}

func TestPatternSelector_ResolveDependencies(t *testing.T) {
	ps := NewPatternSelector(observability.NewNoOpTracer())

	// Test with pattern that has dependencies
	selectedPatterns := map[string]float64{
		"postgres/analytics/join_optimization": 0.9,
	}

	result := ps.resolveDependencies(selectedPatterns)

	// Should include the original pattern
	hasOriginal := false
	for _, p := range result {
		if p == "postgres/analytics/join_optimization" {
			hasOriginal = true
			break
		}
	}
	assert.True(t, hasOriginal, "Should include original pattern")

	// May include dependencies (if they exist in the dependency map)
	// This is a loose check since dependencies are defined in the function
	assert.NotEmpty(t, result)
}

func TestPatternSelector_GetDomainDefaults(t *testing.T) {
	ps := NewPatternSelector(observability.NewNoOpTracer())

	tests := []struct {
		domain         DomainType
		expectNonEmpty bool
	}{
		{DomainSQL, true},
		{DomainREST, true},
		{DomainFile, true},
		{DomainDocument, true},
		{DomainGraphQL, false}, // May not have defaults
	}

	for _, tt := range tests {
		t.Run(string(tt.domain), func(t *testing.T) {
			defaults := ps.getDomainDefaults(tt.domain)
			if tt.expectNonEmpty {
				assert.NotEmpty(t, defaults, "Should have defaults for %s", tt.domain)
			}
		})
	}
}

func TestPatternSelector_FilterByDomain(t *testing.T) {
	ps := NewPatternSelector(observability.NewNoOpTracer())

	allPatterns := []string{
		"sql/data_quality/data_profiling",
		"postgres/analytics/missing_index",
		"teradata/ml/regression",
		"rest_api/health_check",
	}

	// Test SQL domain filtering - SQL domain returns all patterns (no filtering for SQL)
	sqlPatterns := ps.filterByDomain(allPatterns, DomainSQL)
	assert.NotEmpty(t, sqlPatterns)
	assert.Equal(t, allPatterns, sqlPatterns, "SQL domain returns all patterns")

	// Test REST domain filtering
	restPatterns := ps.filterByDomain(allPatterns, DomainREST)
	// Should return all patterns if no domain-specific match, or filtered list
	assert.NotNil(t, restPatterns)
}

func TestPatternSelector_MatchCategory(t *testing.T) {
	ps := NewPatternSelector(observability.NewNoOpTracer())

	tests := []struct {
		category       string
		domain         DomainType
		expectNonEmpty bool
	}{
		{"analytics", DomainSQL, true},
		{"data_quality", DomainSQL, true},
		{"ml", DomainSQL, true},
		{"timeseries", DomainSQL, true},
		{"text", DomainSQL, true},
		{"api", DomainREST, true},
		{"file", DomainFile, true},
		{"unknown_category", DomainSQL, false},
	}

	for _, tt := range tests {
		t.Run(tt.category, func(t *testing.T) {
			patterns := ps.matchCategory(tt.category, tt.domain)
			if tt.expectNonEmpty {
				assert.NotEmpty(t, patterns, "Should have patterns for category %s", tt.category)
			} else {
				assert.Empty(t, patterns, "Should not have patterns for unknown category")
			}
		})
	}
}

func TestPatternSelector_BuildCapabilityMap(t *testing.T) {
	capMap := buildCapabilityMap()

	assert.NotEmpty(t, capMap, "Capability map should not be empty")

	// Check for key capabilities
	assert.Contains(t, capMap, "data_quality")
	assert.Contains(t, capMap, "performance_analysis")
	assert.Contains(t, capMap, "index_optimization")
	assert.Contains(t, capMap, "regression")
	assert.Contains(t, capMap, "time_series")

	// Check that each mapping has patterns
	for capability, patterns := range capMap {
		assert.NotEmpty(t, patterns, "Capability %s should have patterns", capability)
	}
}

// Helper function
func containsAny(s string, substrs []string) bool {
	for _, substr := range substrs {
		if contains(s, substr) {
			return true
		}
	}
	return false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		len(s) > len(substr) && (s[:len(substr)] == substr ||
			s[len(s)-len(substr):] == substr ||
			indexOfSubstring(s, substr) >= 0))
}

func indexOfSubstring(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

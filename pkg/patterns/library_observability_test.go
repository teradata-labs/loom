// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// End-to-end observability tests for pattern library operations.

package patterns

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
)

// TestLibrary_LoadObservability verifies that pattern loading operations are instrumented.
func TestLibrary_LoadObservability(t *testing.T) {
	tracer := observability.NewMockTracer()
	lib := NewLibrary(nil, "testdata/patterns")
	lib = lib.WithTracer(tracer)

	// Attempt to load a pattern (may or may not exist)
	_, err := lib.Load("test-pattern")

	// We expect either success or not found
	// Both paths should emit spans
	spans := tracer.GetSpans()
	require.NotEmpty(t, spans, "Expected spans to be captured")

	// Find the load span
	loadSpans := findSpansByName(spans, "patterns.library.load")
	assert.NotEmpty(t, loadSpans, "Expected patterns.library.load span")

	if len(loadSpans) > 0 {
		span := loadSpans[0]
		assert.Contains(t, span.Attributes, "pattern.name")
		assert.Contains(t, span.Attributes, "cache.hit")
		assert.Contains(t, span.Attributes, "source")
		assert.Contains(t, span.Attributes, "duration_ms")
	}

	t.Logf("✅ Pattern load observability verified: %d spans, error=%v", len(spans), err)
}

// TestLibrary_SearchObservability verifies that pattern search operations are instrumented.
func TestLibrary_SearchObservability(t *testing.T) {
	tracer := observability.NewMockTracer()
	lib := NewLibrary(nil, "testdata/patterns")
	lib = lib.WithTracer(tracer)

	// Search for patterns (may return empty)
	results := lib.Search("test")

	// Verify spans were captured
	spans := tracer.GetSpans()
	require.NotEmpty(t, spans, "Expected spans to be captured")

	// Find the search span
	searchSpans := findSpansByName(spans, "patterns.library.search")
	assert.NotEmpty(t, searchSpans, "Expected patterns.library.search span")

	if len(searchSpans) > 0 {
		span := searchSpans[0]
		assert.Contains(t, span.Attributes, "search.query")
		assert.Contains(t, span.Attributes, "search.query_length")
		assert.Contains(t, span.Attributes, "result.count")
		assert.Contains(t, span.Attributes, "duration_ms")
	}

	t.Logf("✅ Pattern search observability verified: %d results, %d spans", len(results), len(spans))
}

// TestLibrary_ListAllObservability verifies that list operations are instrumented.
func TestLibrary_ListAllObservability(t *testing.T) {
	tracer := observability.NewMockTracer()
	lib := NewLibrary(nil, "testdata/patterns")
	lib = lib.WithTracer(tracer)

	// List all patterns
	results := lib.ListAll()

	// Verify spans were captured
	spans := tracer.GetSpans()
	require.NotEmpty(t, spans, "Expected spans to be captured")

	// Find the list span
	listSpans := findSpansByName(spans, "patterns.library.list_all")
	assert.NotEmpty(t, listSpans, "Expected patterns.library.list_all span")

	if len(listSpans) > 0 {
		span := listSpans[0]
		assert.Contains(t, span.Attributes, "index.cached")
		assert.Contains(t, span.Attributes, "result.count")
		assert.Contains(t, span.Attributes, "duration_ms")
	}

	t.Logf("✅ Pattern list observability verified: %d results, %d spans", len(results), len(spans))
}

// TestLibrary_FilterByCategoryObservability verifies filter operations are instrumented.
func TestLibrary_FilterByCategoryObservability(t *testing.T) {
	tracer := observability.NewMockTracer()
	lib := NewLibrary(nil, "testdata/patterns")
	lib = lib.WithTracer(tracer)

	// Filter by category
	results := lib.FilterByCategory("analytics")

	// Verify spans were captured
	spans := tracer.GetSpans()
	require.NotEmpty(t, spans, "Expected spans to be captured")

	// Find the filter span
	filterSpans := findSpansByName(spans, "patterns.library.filter_by_category")
	assert.NotEmpty(t, filterSpans, "Expected patterns.library.filter_by_category span")

	if len(filterSpans) > 0 {
		span := filterSpans[0]
		assert.Contains(t, span.Attributes, "filter.category")
		assert.Contains(t, span.Attributes, "result.count")
		assert.Contains(t, span.Attributes, "duration_ms")
	}

	t.Logf("✅ Pattern filter observability verified: %d results, %d spans", len(results), len(spans))
}

// TestLibrary_ClearCacheObservability verifies cache operations are instrumented.
func TestLibrary_ClearCacheObservability(t *testing.T) {
	tracer := observability.NewMockTracer()
	lib := NewLibrary(nil, "testdata/patterns")
	lib = lib.WithTracer(tracer)

	// Load a pattern first to populate cache
	_, _ = lib.Load("test-pattern")

	// Clear tracer
	tracer = observability.NewMockTracer()
	lib = lib.WithTracer(tracer)

	// Clear cache
	lib.ClearCache()

	// Verify spans were captured
	spans := tracer.GetSpans()
	require.NotEmpty(t, spans, "Expected spans to be captured")

	// Find the clear cache span
	clearSpans := findSpansByName(spans, "patterns.library.clear_cache")
	assert.NotEmpty(t, clearSpans, "Expected patterns.library.clear_cache span")

	if len(clearSpans) > 0 {
		span := clearSpans[0]
		assert.Contains(t, span.Attributes, "cache.patterns_cleared")
		assert.Contains(t, span.Attributes, "cache.index_entries_cleared")
		assert.Contains(t, span.Attributes, "duration_ms")
	}

	t.Logf("✅ Cache clear observability verified: %d spans", len(spans))
}

// Helper to find spans by name
func findSpansByName(spans []*observability.Span, name string) []*observability.Span {
	result := make([]*observability.Span, 0)
	for _, span := range spans {
		if span.Name == name {
			result = append(result, span)
		}
	}
	return result
}

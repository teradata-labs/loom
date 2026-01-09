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
package patterns

import (
	"context"
	"fmt"

	"github.com/teradata-labs/loom/pkg/prompts"
)

// PatternABTestingLibrary wraps a pattern Library with automatic variant selection.
// This enables A/B testing and canary deployments for patterns.
//
// Canary testing is implemented as A/B testing with weighted selection:
//   - Control version (current): 90% traffic
//   - Treatment version (new): 10% traffic
//
// Example:
//
//	library := patterns.NewLibrary()
//	selector := prompts.NewWeightedSelector(map[string]int{
//	    "control": 90,
//	    "treatment": 10,
//	}, 0)
//	abLibrary := patterns.NewPatternABTestingLibrary(library, selector)
//
//	// Automatically routes 10% to treatment variant
//	pattern, _ := abLibrary.LoadForSession(ctx, "sql.joins.optimize", "sess-123")
type PatternABTestingLibrary struct {
	underlying *Library
	selector   prompts.VariantSelector
}

// NewPatternABTestingLibrary creates an A/B testing wrapper around a pattern library.
func NewPatternABTestingLibrary(underlying *Library, selector prompts.VariantSelector) *PatternABTestingLibrary {
	return &PatternABTestingLibrary{
		underlying: underlying,
		selector:   selector,
	}
}

// Load retrieves a pattern with automatic variant selection based on context.
// Uses "default" as session ID if not found in context.
func (l *PatternABTestingLibrary) Load(ctx context.Context, name string) (*Pattern, error) {
	sessionID := prompts.GetSessionIDFromContext(ctx)
	return l.LoadForSession(ctx, name, sessionID)
}

// LoadWithVariant retrieves a specific pattern variant (bypasses selector).
// This is useful for:
//   - Explicit variant selection (manual A/B testing)
//   - Rollback scenarios (force control variant)
//   - Testing specific variants
func (l *PatternABTestingLibrary) LoadWithVariant(ctx context.Context, name string, variant string) (*Pattern, error) {
	// If variant is empty or "default", use base pattern name
	if variant == "" || variant == "default" {
		return l.underlying.Load(name)
	}

	// Try loading with variant suffix: "pattern_name.variant"
	variantName := fmt.Sprintf("%s.%s", name, variant)
	pattern, err := l.underlying.Load(variantName)
	if err != nil {
		// Variant not found, fall back to default
		return l.underlying.Load(name)
	}

	return pattern, nil
}

// LoadForSession retrieves a pattern with variant selection based on session ID.
// This enables deterministic A/B testing - same session always gets same variant.
func (l *PatternABTestingLibrary) LoadForSession(ctx context.Context, name string, sessionID string) (*Pattern, error) {
	// Get available variants for this pattern
	variants, err := l.getAvailableVariants(ctx, name)
	if err != nil || len(variants) == 0 {
		// No variants, return default pattern
		return l.underlying.Load(name)
	}

	// Select variant using the selector strategy
	selectedVariant, err := l.selector.SelectVariant(ctx, name, variants, sessionID)
	if err != nil {
		// Selection failed, fall back to default
		return l.underlying.Load(name)
	}

	// Load pattern with selected variant
	return l.LoadWithVariant(ctx, name, selectedVariant)
}

// getAvailableVariants discovers available variants for a pattern.
// Variants are patterns with suffix naming: "pattern_name.variant"
//
// For example, if we have:
//   - sql.joins.optimize (default)
//   - sql.joins.optimize.control
//   - sql.joins.optimize.treatment
//
// This returns ["control", "treatment"] (default is implicit)
func (l *PatternABTestingLibrary) getAvailableVariants(ctx context.Context, baseName string) ([]string, error) {
	// Get all pattern summaries from library
	allPatterns := l.underlying.ListAll()

	// Find variants with matching prefix
	var variants []string
	prefix := baseName + "."

	for _, pattern := range allPatterns {
		if len(pattern.Name) > len(prefix) && pattern.Name[:len(prefix)] == prefix {
			// Extract variant name (everything after the prefix)
			variant := pattern.Name[len(prefix):]
			variants = append(variants, variant)
		}
	}

	// Always include "default" as an option if variants exist
	if len(variants) > 0 {
		variants = append([]string{"default"}, variants...)
	}

	return variants, nil
}

// ListAll lists all pattern summaries (including variants) from the underlying library.
func (l *PatternABTestingLibrary) ListAll() []PatternSummary {
	return l.underlying.ListAll()
}

// ClearCache clears the underlying library's pattern cache.
// This forces patterns to be reloaded on next access.
func (l *PatternABTestingLibrary) ClearCache() {
	l.underlying.ClearCache()
}

// CanaryTestConfig represents a canary test configuration.
// This is a specialized A/B test with 90/10 traffic split.
type CanaryTestConfig struct {
	PatternName       string  // Base pattern name (e.g., "sql.joins.optimize")
	ControlVariant    string  // Usually "control" or "default"
	TreatmentVariant  string  // Usually "treatment" or version number
	TrafficPercentage float64 // 0.10 for 10% canary
	DurationMinutes   int     // 30 for 30-minute canary test
}

// NewCanarySelector creates a WeightedSelector for canary testing.
// This is a convenience function for the common 90/10 split.
//
// Example:
//
//	selector := patterns.NewCanarySelector("control", "treatment", 0.10)
//	// Routes 10% to treatment, 90% to control
func NewCanarySelector(controlVariant string, treatmentVariant string, treatmentPercentage float64) prompts.VariantSelector {
	controlWeight := int((1.0 - treatmentPercentage) * 100)
	treatmentWeight := int(treatmentPercentage * 100)

	weights := map[string]int{
		controlVariant:   controlWeight,   // 90
		treatmentVariant: treatmentWeight, // 10
	}

	return prompts.NewWeightedSelector(weights, 0)
}

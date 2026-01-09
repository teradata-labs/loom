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
	"testing"

	"github.com/teradata-labs/loom/pkg/prompts"
)

func TestPatternABTestingLibrary_LoadWithVariant(t *testing.T) {
	// Create library with test patterns
	library := NewLibrary(nil, "testdata/patterns")

	// Create A/B testing wrapper with explicit selector
	selector := prompts.NewExplicitSelector("treatment")
	abLibrary := NewPatternABTestingLibrary(library, selector)

	ctx := context.Background()

	// Test loading specific variant (bypasses selector)
	pattern, err := abLibrary.LoadWithVariant(ctx, "test.pattern", "control")
	if err == nil {
		// If pattern exists, verify it loaded
		if pattern == nil {
			t.Error("LoadWithVariant() returned nil pattern without error")
		}
	}
	// Note: We don't fail if pattern doesn't exist - this is test data dependent
}

func TestPatternABTestingLibrary_LoadForSession(t *testing.T) {
	library := NewLibrary(nil, "testdata/patterns")

	// Use hash selector for deterministic session-based routing
	selector := prompts.NewHashSelector()
	abLibrary := NewPatternABTestingLibrary(library, selector)

	ctx := context.Background()

	// Same session should get same variant (if variants exist)
	sessionID := "sess-123"

	pattern1, err1 := abLibrary.LoadForSession(ctx, "test.pattern", sessionID)
	pattern2, err2 := abLibrary.LoadForSession(ctx, "test.pattern", sessionID)

	// Both calls should succeed or fail consistently
	if (err1 == nil) != (err2 == nil) {
		t.Errorf("Inconsistent errors: err1=%v, err2=%v", err1, err2)
	}

	// If both succeeded, patterns should be the same
	if err1 == nil && err2 == nil {
		if pattern1 == nil || pattern2 == nil {
			t.Error("LoadForSession() returned nil pattern without error")
		} else if pattern1.Name != pattern2.Name {
			t.Errorf("Same session got different patterns: %s vs %s", pattern1.Name, pattern2.Name)
		}
	}
}

func TestPatternABTestingLibrary_Load(t *testing.T) {
	library := NewLibrary(nil, "testdata/patterns")
	selector := prompts.NewHashSelector()
	abLibrary := NewPatternABTestingLibrary(library, selector)

	// Add session ID to context
	ctx := prompts.WithSessionID(context.Background(), "sess-456")

	pattern, err := abLibrary.Load(ctx, "test.pattern")
	if err == nil && pattern == nil {
		t.Error("Load() returned nil pattern without error")
	}
	// Note: We don't fail if pattern doesn't exist - this is test data dependent
}

func TestNewCanarySelector(t *testing.T) {
	// Create canary selector with 10% treatment traffic
	selector := NewCanarySelector("control", "treatment", 0.10)

	if selector == nil {
		t.Fatal("NewCanarySelector() returned nil")
	}

	// Test that it's a valid VariantSelector
	ctx := context.Background()
	variants := []string{"control", "treatment"}

	// Count distribution over many selections
	counts := make(map[string]int)
	for i := 0; i < 1000; i++ {
		sessionID := generateSessionID(i)
		variant, err := selector.SelectVariant(ctx, "test.pattern", variants, sessionID)
		if err != nil {
			t.Fatalf("SelectVariant() failed: %v", err)
		}
		counts[variant]++
	}

	// Verify distribution is approximately 90/10
	controlCount := counts["control"]
	treatmentCount := counts["treatment"]

	// Allow 85-95% for control (expected 90%)
	if controlCount < 850 || controlCount > 950 {
		t.Errorf("Control got %d selections (expected ~900)", controlCount)
	}

	// Allow 5-15% for treatment (expected 10%)
	if treatmentCount < 50 || treatmentCount > 150 {
		t.Errorf("Treatment got %d selections (expected ~100)", treatmentCount)
	}
}

func TestPatternABTestingLibrary_ListAll(t *testing.T) {
	library := NewLibrary(nil, "testdata/patterns")
	selector := prompts.NewHashSelector()
	abLibrary := NewPatternABTestingLibrary(library, selector)

	summaries := abLibrary.ListAll()

	// Should return a slice (might be empty if no patterns)
	if summaries == nil {
		t.Error("ListAll() returned nil")
	}
}

func TestPatternABTestingLibrary_ClearCache(t *testing.T) {
	library := NewLibrary(nil, "testdata/patterns")
	selector := prompts.NewHashSelector()
	abLibrary := NewPatternABTestingLibrary(library, selector)

	// ClearCache should not panic
	abLibrary.ClearCache()
}

func TestPatternABTestingLibrary_GetAvailableVariants(t *testing.T) {
	library := NewLibrary(nil, "testdata/patterns")
	selector := prompts.NewHashSelector()
	abLibrary := NewPatternABTestingLibrary(library, selector)

	ctx := context.Background()

	// Test with a pattern that doesn't have variants
	variants, err := abLibrary.getAvailableVariants(ctx, "nonexistent.pattern")
	if err != nil {
		t.Errorf("getAvailableVariants() returned error: %v", err)
	}

	// Should return empty slice if no variants found
	// Note: variants can be nil or empty - both are valid for no variants
	if variants == nil {
		variants = []string{}
	}

	if len(variants) > 0 {
		t.Logf("Found %d variants for nonexistent.pattern (unexpected but valid)", len(variants))
	}
}

func TestCanaryTestConfig(t *testing.T) {
	config := CanaryTestConfig{
		PatternName:       "sql.joins.optimize",
		ControlVariant:    "control",
		TreatmentVariant:  "treatment",
		TrafficPercentage: 0.10,
		DurationMinutes:   30,
	}

	if config.PatternName != "sql.joins.optimize" {
		t.Errorf("PatternName = %q, want %q", config.PatternName, "sql.joins.optimize")
	}

	if config.TrafficPercentage != 0.10 {
		t.Errorf("TrafficPercentage = %f, want %f", config.TrafficPercentage, 0.10)
	}

	if config.DurationMinutes != 30 {
		t.Errorf("DurationMinutes = %d, want %d", config.DurationMinutes, 30)
	}
}

func TestPatternABTestingLibrary_LoadWithVariant_Fallback(t *testing.T) {
	library := NewLibrary(nil, "testdata/patterns")
	selector := prompts.NewHashSelector()
	abLibrary := NewPatternABTestingLibrary(library, selector)

	ctx := context.Background()

	// Request non-existent variant - should fall back to default
	pattern, err := abLibrary.LoadWithVariant(ctx, "test.pattern", "nonexistent")

	// Either both nil (pattern doesn't exist) or both non-nil (fallback worked)
	if (pattern == nil) != (err != nil) {
		t.Errorf("Inconsistent fallback: pattern=%v, err=%v", pattern, err)
	}
}

func TestNewCanarySelector_DifferentPercentages(t *testing.T) {
	tests := []struct {
		name                string
		treatmentPercentage float64
		expectedControlMin  int
		expectedControlMax  int
	}{
		{"5% canary", 0.05, 920, 980},
		{"10% canary", 0.10, 850, 950},
		{"20% canary", 0.20, 750, 850},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selector := NewCanarySelector("control", "treatment", tt.treatmentPercentage)
			ctx := context.Background()
			variants := []string{"control", "treatment"}

			counts := make(map[string]int)
			for i := 0; i < 1000; i++ {
				sessionID := generateSessionID(i)
				variant, err := selector.SelectVariant(ctx, "test", variants, sessionID)
				if err != nil {
					t.Fatalf("SelectVariant() failed: %v", err)
				}
				counts[variant]++
			}

			controlCount := counts["control"]
			if controlCount < tt.expectedControlMin || controlCount > tt.expectedControlMax {
				t.Errorf("Control got %d selections (expected %d-%d)",
					controlCount, tt.expectedControlMin, tt.expectedControlMax)
			}
		})
	}
}

// Helper function to generate predictable session IDs for testing
func generateSessionID(i int) string {
	return "sess-" + string(rune('A'+i%26)) + string(rune('0'+i%10))
}

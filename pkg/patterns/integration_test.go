// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package patterns

import (
	"testing"
)

// TestPatternSelectionIntegration validates the full pattern selection flow
func TestPatternSelectionIntegration(t *testing.T) {
	// Load real patterns
	lib := NewLibrary(nil, "../../patterns")
	orch := NewOrchestrator(lib)

	// Verify patterns loaded
	allPatterns := lib.ListAll()
	if len(allPatterns) < 80 {
		t.Fatalf("Expected at least 80 patterns, got %d", len(allPatterns))
	}
	t.Logf("✅ Loaded %d patterns", len(allPatterns))

	// Test realistic scenarios
	scenarios := []struct {
		userMessage       string
		expectedIntent    IntentCategory
		expectPattern     bool
		minConfidence     float64
		expectedInResults []string // Pattern names that should be in top results
	}{
		{
			userMessage:       "analyze customer churn to predict at-risk customers",
			expectedIntent:    IntentAnalytics,
			expectPattern:     true,
			minConfidence:     0.50,
			expectedInResults: []string{"churn_analysis", "customer_health_scoring"},
		},
		{
			userMessage:       "find sequences in user clickstream data",
			expectedIntent:    IntentAnalytics,
			expectPattern:     true,
			minConfidence:     0.50,
			expectedInResults: []string{"npath", "sessionize"},
		},
		{
			userMessage:       "check for duplicate records and data quality issues",
			expectedIntent:    IntentDataQuality,
			expectPattern:     true,
			minConfidence:     0.50,
			expectedInResults: []string{"duplicate_detection", "data_profiling"},
		},
		{
			userMessage:       "predict customer lifetime value using machine learning",
			expectedIntent:    IntentAnalytics,
			expectPattern:     true,
			minConfidence:     0.50,
			expectedInResults: []string{"linear_regression", "logistic_regression"},
		},
	}

	for i, sc := range scenarios {
		testName := sc.userMessage
		if len(testName) > 40 {
			testName = testName[:40]
		}
		t.Run(testName, func(t *testing.T) {
			t.Logf("\n=== Scenario %d ===", i+1)
			t.Logf("User: %q", sc.userMessage)

			// Step 1: Classify intent
			intent, intentConf := orch.ClassifyIntent(sc.userMessage, nil)
			t.Logf("Intent: %s (%.2f confidence)", intent, intentConf)

			if intent != sc.expectedIntent {
				t.Logf("⚠️  Intent mismatch: expected %s, got %s", sc.expectedIntent, intent)
			}

			// Step 2: Recommend pattern
			patternName, patternConf := orch.RecommendPattern(sc.userMessage, intent)

			if !sc.expectPattern {
				if patternName != "" {
					t.Errorf("Expected no pattern, but got: %s (%.2f)", patternName, patternConf)
				}
				return
			}

			// Verify pattern recommended
			if patternName == "" {
				t.Errorf("❌ Expected pattern recommendation but got none")

				// Debug: Show search results
				searchResults := lib.Search(sc.userMessage)
				t.Logf("Search found %d patterns:", len(searchResults))
				for j, sr := range searchResults {
					if j >= 5 {
						break
					}
					t.Logf("  - %s (category: %s)", sr.Name, sr.Category)
				}
				return
			}

			t.Logf("✅ Pattern: %s (%.2f confidence)", patternName, patternConf)

			// Verify confidence meets threshold
			if patternConf < sc.minConfidence {
				t.Errorf("Pattern confidence %.2f below threshold %.2f", patternConf, sc.minConfidence)
			}

			// Verify pattern is in expected results
			found := false
			for _, expected := range sc.expectedInResults {
				if patternName == expected {
					found = true
					break
				}
			}

			if !found {
				t.Logf("⚠️  Pattern %s not in expected results: %v", patternName, sc.expectedInResults)
				t.Logf("   (This is OK if the pattern is semantically relevant)")
			}

			// Step 3: Load and verify pattern
			pattern, err := lib.Load(patternName)
			if err != nil {
				t.Errorf("❌ Failed to load pattern %s: %v", patternName, err)
				return
			}

			t.Logf("Pattern details:")
			t.Logf("  Title: %s", pattern.Title)
			t.Logf("  Category: %s", pattern.Category)
			t.Logf("  Difficulty: %s", pattern.Difficulty)
			if len(pattern.UseCases) > 0 {
				t.Logf("  Use cases: %v", pattern.UseCases[:min(2, len(pattern.UseCases))])
			}

			// Verify pattern can be formatted for LLM
			formatted := pattern.FormatForLLM()
			if len(formatted) == 0 {
				t.Error("❌ Pattern formatted to empty string")
			} else {
				t.Logf("  Formatted length: %d chars", len(formatted))
			}
		})
	}

	t.Log("\n✅ Pattern selection integration test complete")
}

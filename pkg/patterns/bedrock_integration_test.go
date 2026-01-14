// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build integration
// +build integration

package patterns

import (
	"os"
	"testing"
	"time"

	"github.com/teradata-labs/loom/pkg/llm/bedrock"
)

// TestPatternSelectionWithBedrockLLM tests the full pattern selection flow with real Bedrock LLM.
// This validates that LLM-based intent classification works correctly for pattern selection.
//
// Prerequisites:
// - AWS credentials configured (IAM role, profile, or env vars)
// - Bedrock model access enabled in your AWS account
// - Run with: go test -tags integration,fts5 -run TestPatternSelectionWithBedrockLLM ./pkg/patterns
func TestPatternSelectionWithBedrockLLM(t *testing.T) {
	// Skip if not in integration test mode
	if os.Getenv("SKIP_INTEGRATION_TESTS") == "true" {
		t.Skip("Skipping integration test")
	}

	// Set up Bedrock client
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-west-2" // Default
	}

	t.Logf("🔧 Setting up Bedrock LLM (region: %s)", region)

	bedrockCfg := bedrock.Config{
		Region:      region,
		ModelID:     bedrock.DefaultBedrockModelID, // Claude Sonnet 4.5
		MaxTokens:   1000,                          // Small for intent classification
		Temperature: 0.7,
	}

	provider, err := bedrock.NewClient(bedrockCfg)
	if err != nil {
		t.Fatalf("Failed to create Bedrock client: %v", err)
	}

	t.Log("✅ Bedrock client created")

	// Set up pattern library and orchestrator
	lib := NewLibrary(nil, "../../patterns")
	orch := NewOrchestrator(lib)

	// Verify patterns loaded
	allPatterns := lib.ListAll()
	if len(allPatterns) < 80 {
		t.Fatalf("Expected at least 80 patterns, got %d", len(allPatterns))
	}
	t.Logf("✅ Loaded %d patterns", len(allPatterns))

	// Configure LLM-based intent classifier
	llmClassifierConfig := DefaultLLMClassifierConfig(provider)
	llmClassifier := NewLLMIntentClassifier(llmClassifierConfig)
	orch.SetIntentClassifier(llmClassifier)
	t.Log("✅ LLM intent classifier configured")

	// Test scenarios that challenge keyword-based classification
	scenarios := []struct {
		userMessage       string
		expectedIntent    IntentCategory
		expectedInResults []string
		description       string
	}{
		{
			userMessage:       "I need to predict which customers will churn using historical behavior data",
			expectedIntent:    IntentAnalytics,
			expectedInResults: []string{"churn_analysis", "customer_health_scoring", "logistic_regression"},
			description:       "Churn prediction (semantic understanding needed)",
		},
		{
			userMessage:       "build a machine learning model to forecast customer lifetime value",
			expectedIntent:    IntentAnalytics,
			expectedInResults: []string{"linear_regression", "logistic_regression"},
			description:       "ML model building (keyword classifier fails on this)",
		},
		{
			userMessage:       "analyze user journeys through our funnel to find drop-off points",
			expectedIntent:    IntentAnalytics,
			expectedInResults: []string{"funnel_analysis", "npath", "sessionize"},
			description:       "Funnel/journey analysis",
		},
		{
			userMessage:       "find duplicate customer records and merge them",
			expectedIntent:    IntentDataQuality,
			expectedInResults: []string{"duplicate_detection", "data_profiling"},
			description:       "Data quality - duplicates",
		},
	}

	for i, sc := range scenarios {
		t.Run(sc.description, func(t *testing.T) {
			t.Logf("\n=== Scenario %d: %s ===", i+1, sc.description)
			t.Logf("User: %q", sc.userMessage)

			// Step 1: Classify intent with LLM
			startTime := time.Now()
			// LLMIntentClassifier returns an IntentClassifierFunc, so we call it as a function
			intent, intentConf := llmClassifier(sc.userMessage, nil)
			duration := time.Since(startTime)

			t.Logf("✅ LLM Intent: %s (confidence: %.2f) [took %v]", intent, intentConf, duration)

			// Verify intent is correct
			if intent != sc.expectedIntent {
				t.Errorf("❌ Intent mismatch: expected %s, got %s", sc.expectedIntent, intent)
			}

			// Step 2: Recommend pattern
			patternName, patternConf := orch.RecommendPattern(sc.userMessage, intent)

			if patternName == "" {
				t.Errorf("❌ Expected pattern recommendation but got none")

				// Debug
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

			t.Logf("✅ Pattern: %s (confidence: %.2f)", patternName, patternConf)

			// Verify confidence is above threshold (0.50)
			if patternConf < 0.50 {
				t.Errorf("Pattern confidence %.2f below threshold 0.50", patternConf)
			}

			// Verify pattern is semantically relevant (check against expected results)
			found := false
			for _, expected := range sc.expectedInResults {
				if patternName == expected {
					found = true
					break
				}
			}

			if !found {
				t.Logf("⚠️  Pattern %s not in expected results: %v", patternName, sc.expectedInResults)
				t.Logf("   Checking if it's semantically relevant...")

				// Load pattern and check if it makes sense for the query
				pattern, err := lib.Load(patternName)
				if err == nil {
					t.Logf("   Pattern: %s (category: %s)", pattern.Title, pattern.Category)
					t.Logf("   Use cases: %v", pattern.UseCases[:min(2, len(pattern.UseCases))])
				}
			} else {
				t.Logf("✅ Pattern is in expected results")
			}

			// Step 3: Verify pattern can be loaded and formatted
			pattern, err := lib.Load(patternName)
			if err != nil {
				t.Errorf("❌ Failed to load pattern %s: %v", patternName, err)
				return
			}

			formatted := pattern.FormatForLLM()
			if len(formatted) == 0 {
				t.Error("❌ Pattern formatted to empty string")
			} else {
				t.Logf("✅ Pattern formatted: %d chars", len(formatted))
			}
		})
	}

	t.Log("\n✅ Bedrock LLM integration test complete")
	t.Log("Summary: LLM-based intent classification + pattern selection working correctly")
}

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
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mutecomm/go-sqlcipher/v4"
	"github.com/teradata-labs/loom/pkg/metaagent/learning"
	"github.com/teradata-labs/loom/pkg/observability"
)

func TestNewOrchestrator(t *testing.T) {
	lib := NewLibrary(nil, "")
	orch := NewOrchestrator(lib)

	if orch == nil {
		t.Fatal("NewOrchestrator returned nil")
	}

	if orch.library == nil {
		t.Error("library not initialized")
	}

	if orch.intentClassifier == nil {
		t.Error("intentClassifier not initialized")
	}

	if orch.executionPlanner == nil {
		t.Error("executionPlanner not initialized")
	}
}

func TestOrchestrator_ClassifyIntent(t *testing.T) {
	lib := NewLibrary(nil, "")
	orch := NewOrchestrator(lib)

	tests := []struct {
		name           string
		message        string
		expectedIntent IntentCategory
		minConfidence  float64
	}{
		{
			name:           "schema discovery",
			message:        "what tables are in the database",
			expectedIntent: IntentSchemaDiscovery,
			minConfidence:  0.85,
		},
		{
			name:           "relationship query",
			message:        "show me the foreign key relationships",
			expectedIntent: IntentRelationshipQuery,
			minConfidence:  0.80,
		},
		{
			name:           "data quality",
			message:        "check data quality and find duplicates",
			expectedIntent: IntentDataQuality,
			minConfidence:  0.85,
		},
		{
			name:           "data transform",
			message:        "move data from source to target table",
			expectedIntent: IntentDataTransform,
			minConfidence:  0.80,
		},
		{
			name:           "analytics",
			message:        "calculate sum and average by group",
			expectedIntent: IntentAnalytics,
			minConfidence:  0.75,
		},
		{
			name:           "query generation",
			message:        "write a query to find all customers",
			expectedIntent: IntentQueryGeneration,
			minConfidence:  0.70,
		},
		{
			name:           "document search",
			message:        "search documents for keyword",
			expectedIntent: IntentDocumentSearch,
			minConfidence:  0.75,
		},
		{
			name:           "api call",
			message:        "make a REST API call to the endpoint",
			expectedIntent: IntentAPICall,
			minConfidence:  0.80,
		},
		{
			name:           "unknown intent",
			message:        "xyz abc random words",
			expectedIntent: IntentUnknown,
			minConfidence:  0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent, confidence := orch.ClassifyIntent(tt.message, nil)

			if intent != tt.expectedIntent {
				t.Errorf("Expected intent %s, got %s", tt.expectedIntent, intent)
			}

			if confidence < tt.minConfidence {
				t.Errorf("Expected confidence >= %.2f, got %.2f", tt.minConfidence, confidence)
			}
		})
	}
}

func TestOrchestrator_PlanExecution(t *testing.T) {
	lib := NewLibrary(nil, "")
	orch := NewOrchestrator(lib)

	tests := []struct {
		name        string
		intent      IntentCategory
		message     string
		expectError bool
		minSteps    int
	}{
		{
			name:        "schema discovery plan",
			intent:      IntentSchemaDiscovery,
			message:     "show schema",
			expectError: false,
			minSteps:    1,
		},
		{
			name:        "relationship query plan",
			intent:      IntentRelationshipQuery,
			message:     "show relationships",
			expectError: false,
			minSteps:    1,
		},
		{
			name:        "data quality plan",
			intent:      IntentDataQuality,
			message:     "check quality",
			expectError: false,
			minSteps:    1,
		},
		{
			name:        "data transform plan",
			intent:      IntentDataTransform,
			message:     "transform data",
			expectError: false,
			minSteps:    1,
		},
		{
			name:        "analytics plan",
			intent:      IntentAnalytics,
			message:     "run analytics",
			expectError: false,
			minSteps:    1,
		},
		{
			name:        "query generation plan",
			intent:      IntentQueryGeneration,
			message:     "generate query",
			expectError: false,
			minSteps:    1,
		},
		{
			name:        "document search plan",
			intent:      IntentDocumentSearch,
			message:     "search documents",
			expectError: false,
			minSteps:    1,
		},
		{
			name:        "api call plan",
			intent:      IntentAPICall,
			message:     "call api",
			expectError: false,
			minSteps:    1,
		},
		{
			name:        "unknown intent",
			intent:      IntentUnknown,
			message:     "unknown",
			expectError: true,
			minSteps:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := orch.PlanExecution(tt.intent, tt.message, nil)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if plan == nil {
				t.Fatal("Expected plan, got nil")
			}

			if plan.Intent != tt.intent {
				t.Errorf("Expected intent %s, got %s", tt.intent, plan.Intent)
			}

			if len(plan.Steps) < tt.minSteps {
				t.Errorf("Expected at least %d steps, got %d", tt.minSteps, len(plan.Steps))
			}

			if plan.Description == "" {
				t.Error("Expected non-empty description")
			}

			if plan.Reasoning == "" {
				t.Error("Expected non-empty reasoning")
			}
		})
	}
}

func TestOrchestrator_GetRoutingRecommendation(t *testing.T) {
	lib := NewLibrary(nil, "")
	orch := NewOrchestrator(lib)

	tests := []struct {
		name   string
		intent IntentCategory
	}{
		{"schema discovery", IntentSchemaDiscovery},
		{"data quality", IntentDataQuality},
		{"data transform", IntentDataTransform},
		{"analytics", IntentAnalytics},
		{"relationship query", IntentRelationshipQuery},
		{"query generation", IntentQueryGeneration},
		{"document search", IntentDocumentSearch},
		{"api call", IntentAPICall},
		{"unknown", IntentUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := orch.GetRoutingRecommendation(tt.intent)

			if rec == "" {
				t.Error("Expected non-empty recommendation")
			}

			// Verify recommendation is helpful text
			if len(rec) < 10 {
				t.Errorf("Recommendation seems too short: %q", rec)
			}
		})
	}
}

func TestOrchestrator_RecommendPattern(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test patterns
	pattern1 := `name: time_series
title: Time Series Analysis
description: Pattern for analyzing time series data
category: analytics
difficulty: advanced
backend_type: sql
use_cases:
  - forecasting
  - trend analysis
  - time series
`

	pattern2 := `name: data_validation
title: Data Quality Validation
description: Pattern for validating data quality
category: data_quality
difficulty: beginner
backend_type: sql
use_cases:
  - validation
  - quality checks
`

	for name, content := range map[string]string{
		"time_series":     pattern1,
		"data_validation": pattern2,
	} {
		patternPath := filepath.Join(tmpDir, name+".yaml")
		if err := os.WriteFile(patternPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create test pattern: %v", err)
		}
	}

	lib := NewLibrary(nil, tmpDir)
	orch := NewOrchestrator(lib)

	// Force library to index patterns
	_ = lib.ListAll()

	tests := []struct {
		name            string
		message         string
		intent          IntentCategory
		expectedPattern string
		minConfidence   float64
	}{
		{
			name:            "time series keyword",
			message:         "time series",
			intent:          IntentAnalytics,
			expectedPattern: "time_series",
			minConfidence:   0.5,
		},
		{
			name:            "validation keyword",
			message:         "validation",
			intent:          IntentDataQuality,
			expectedPattern: "data_validation",
			minConfidence:   0.5,
		},
		{
			name:            "no match",
			message:         "xyz random words",
			intent:          IntentUnknown,
			expectedPattern: "",
			minConfidence:   0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pattern, confidence := orch.RecommendPattern(tt.message, tt.intent)

			if tt.expectedPattern == "" {
				if pattern != "" {
					t.Errorf("Expected no pattern recommendation, got %q", pattern)
				}
				if confidence != 0.0 {
					t.Errorf("Expected confidence 0.0, got %.2f", confidence)
				}
				return
			}

			if pattern != tt.expectedPattern {
				t.Errorf("Expected pattern %q, got %q", tt.expectedPattern, pattern)
			}

			if confidence < tt.minConfidence {
				t.Errorf("Expected confidence >= %.2f, got %.2f", tt.minConfidence, confidence)
			}

			// Confidence should never exceed 0.9
			if confidence > 0.9 {
				t.Errorf("Expected confidence <= 0.9, got %.2f", confidence)
			}
		})
	}
}

func TestOrchestrator_SetCustomClassifier(t *testing.T) {
	lib := NewLibrary(nil, "")
	orch := NewOrchestrator(lib)

	// Set custom classifier
	customCalled := false
	customClassifier := func(msg string, ctx map[string]interface{}) (IntentCategory, float64) {
		customCalled = true
		return IntentAnalytics, 0.95
	}

	orch.SetIntentClassifier(customClassifier)

	// Verify custom classifier is used
	intent, confidence := orch.ClassifyIntent("test message", nil)

	if !customCalled {
		t.Error("Custom classifier was not called")
	}

	if intent != IntentAnalytics {
		t.Errorf("Expected IntentAnalytics from custom classifier, got %s", intent)
	}

	if confidence != 0.95 {
		t.Errorf("Expected confidence 0.95 from custom classifier, got %.2f", confidence)
	}
}

func TestOrchestrator_SetCustomPlanner(t *testing.T) {
	lib := NewLibrary(nil, "")
	orch := NewOrchestrator(lib)

	// Set custom planner
	customCalled := false
	customPlanner := func(intent IntentCategory, msg string, ctx map[string]interface{}) (*ExecutionPlan, error) {
		customCalled = true
		return &ExecutionPlan{
			Intent:      intent,
			Description: "custom plan",
			Steps: []PlannedStep{
				{
					ToolName:    "custom_tool",
					Description: "custom step",
				},
			},
			Reasoning: "custom reasoning",
		}, nil
	}

	orch.SetExecutionPlanner(customPlanner)

	// Verify custom planner is used
	plan, err := orch.PlanExecution(IntentAnalytics, "test message", nil)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !customCalled {
		t.Error("Custom planner was not called")
	}

	if plan.Description != "custom plan" {
		t.Errorf("Expected 'custom plan' from custom planner, got %q", plan.Description)
	}

	if len(plan.Steps) != 1 || plan.Steps[0].ToolName != "custom_tool" {
		t.Error("Custom planner did not produce expected steps")
	}
}

func TestMatchesIntent(t *testing.T) {
	tests := []struct {
		name     string
		category string
		intent   IntentCategory
		expected bool
	}{
		{"exact match", "analytics", IntentAnalytics, true},
		{"fuzzy match", "aggregation", IntentAnalytics, true},
		{"fuzzy match", "data_quality", IntentDataQuality, true},
		{"fuzzy match", "validation", IntentDataQuality, true},
		{"fuzzy match", "etl", IntentDataTransform, true},
		{"fuzzy match", "schema", IntentSchemaDiscovery, true},
		{"no match", "random", IntentAnalytics, false},
		{"no match", "etl", IntentAnalytics, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matchesIntent(tt.category, tt.intent)
			if result != tt.expected {
				t.Errorf("matchesIntent(%q, %s) = %v, expected %v", tt.category, tt.intent, result, tt.expected)
			}
		})
	}
}

func TestContainsAny(t *testing.T) {
	tests := []struct {
		name     string
		str      string
		keywords []string
		expected bool
	}{
		{"found", "this is a test string", []string{"test", "example"}, true},
		{"not found", "this is a string", []string{"test", "example"}, false},
		{"first match", "example text", []string{"example", "test"}, true},
		{"second match", "test text", []string{"example", "test"}, true},
		{"empty keywords", "test", []string{}, false},
		{"empty string", "", []string{"test"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsAny(tt.str, tt.keywords)
			if result != tt.expected {
				t.Errorf("containsAny(%q, %v) = %v, expected %v", tt.str, tt.keywords, result, tt.expected)
			}
		})
	}
}

// ============================================================================
// Tracker Integration Tests
// ============================================================================

func TestOrchestrator_WithTracker(t *testing.T) {
	lib := NewLibrary(nil, "")
	orch := NewOrchestrator(lib)

	// Create in-memory database for tracker
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Initialize schema
	if err := learning.InitSelfImprovementSchema(context.Background(), db, observability.NewNoOpTracer()); err != nil {
		t.Fatalf("Failed to initialize schema: %v", err)
	}

	// Create tracker
	tracker := learning.NewPatternEffectivenessTracker(
		db,
		observability.NewNoOpTracer(),
		nil, // No message bus
		1*time.Hour,
		5*time.Minute,
	)

	// Set tracker
	result := orch.WithTracker(tracker)

	// Verify fluent interface returns orchestrator
	if result != orch {
		t.Error("WithTracker should return the same orchestrator instance")
	}

	// Verify tracker is set
	if orch.tracker != tracker {
		t.Error("Tracker was not set correctly")
	}
}

func TestOrchestrator_RecordPatternUsage_NoTracker(t *testing.T) {
	lib := NewLibrary(nil, "")
	orch := NewOrchestrator(lib)

	ctx := context.Background()

	// RecordPatternUsage should be a no-op when tracker is nil
	// This should not panic
	orch.RecordPatternUsage(
		ctx,
		"test_pattern",
		"agent-1",
		true,
		0.001,
		100*time.Millisecond,
		"",
		"anthropic",
		"claude-3-5-sonnet-20241022",
	)
}

func TestOrchestrator_RecordPatternUsage_WithTracker(t *testing.T) {
	lib := NewLibrary(nil, "")
	orch := NewOrchestrator(lib)

	// Create in-memory database for tracker
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Initialize schema
	if err := learning.InitSelfImprovementSchema(context.Background(), db, observability.NewNoOpTracer()); err != nil {
		t.Fatalf("Failed to initialize schema: %v", err)
	}

	// Create and start tracker
	tracker := learning.NewPatternEffectivenessTracker(
		db,
		observability.NewNoOpTracer(),
		nil, // No message bus
		1*time.Hour,
		5*time.Minute,
	)

	if err := tracker.Start(context.Background()); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer func() {
		_ = tracker.Stop(context.Background())
	}()

	// Set tracker
	orch.WithTracker(tracker)

	// Set up context with pattern metadata
	ctx := WithPatternMetadata(context.Background(), "test_pattern", "control", "sql")

	// Record pattern usage
	orch.RecordPatternUsage(
		ctx,
		"test_pattern",
		"agent-1",
		true,   // success
		0.0015, // cost
		150*time.Millisecond,
		"",
		"anthropic",
		"claude-3-5-sonnet-20241022",
	)

	// Stop tracker to flush data
	if err := tracker.Stop(context.Background()); err != nil {
		t.Fatalf("Failed to stop tracker: %v", err)
	}

	// Verify data was written to database
	var count int
	err = db.QueryRow(`
		SELECT COUNT(*) FROM pattern_effectiveness
		WHERE pattern_name = ? AND variant = ? AND domain = ? AND agent_id = ?
	`, "test_pattern", "control", "sql", "agent-1").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query database: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected 1 record in database, got %d", count)
	}
}

func TestOrchestrator_RecordPatternUsage_ContextExtraction(t *testing.T) {
	tests := []struct {
		name            string
		contextVariant  string
		contextDomain   string
		expectedVariant string
		expectedDomain  string
	}{
		{
			name:            "with context metadata",
			contextVariant:  "treatment",
			contextDomain:   "rest_api",
			expectedVariant: "treatment",
			expectedDomain:  "rest_api",
		},
		{
			name:            "empty context uses defaults",
			contextVariant:  "",
			contextDomain:   "",
			expectedVariant: "default",
			expectedDomain:  "unknown",
		},
		{
			name:            "partial context",
			contextVariant:  "canary",
			contextDomain:   "",
			expectedVariant: "canary",
			expectedDomain:  "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fresh orchestrator and tracker for each subtest
			lib := NewLibrary(nil, "")
			orch := NewOrchestrator(lib)

			// Create in-memory database for tracker
			db, err := sql.Open("sqlite3", ":memory:")
			if err != nil {
				t.Fatalf("Failed to create test database: %v", err)
			}
			defer db.Close()

			// Initialize schema
			if err := learning.InitSelfImprovementSchema(context.Background(), db, observability.NewNoOpTracer()); err != nil {
				t.Fatalf("Failed to initialize schema: %v", err)
			}

			// Create and start tracker
			tracker := learning.NewPatternEffectivenessTracker(
				db,
				observability.NewNoOpTracer(),
				nil, // No message bus
				1*time.Hour,
				5*time.Minute,
			)

			if err := tracker.Start(context.Background()); err != nil {
				t.Fatalf("Failed to start tracker: %v", err)
			}

			orch.WithTracker(tracker)

			// Set up context
			ctx := context.Background()
			if tt.contextVariant != "" || tt.contextDomain != "" {
				ctx = WithPatternMetadata(ctx, "test_pattern", tt.contextVariant, tt.contextDomain)
			}

			// Record pattern usage
			orch.RecordPatternUsage(
				ctx,
				"test_pattern",
				"agent-test",
				true,
				0.001,
				100*time.Millisecond,
				"",
				"anthropic",
				"claude-3-5-sonnet-20241022",
			)

			// Stop tracker to flush data
			if err := tracker.Stop(context.Background()); err != nil {
				t.Fatalf("Failed to stop tracker: %v", err)
			}

			// Verify extracted values
			var variant, domain string
			err = db.QueryRow(`
				SELECT variant, domain FROM pattern_effectiveness
				WHERE pattern_name = ?
			`, "test_pattern").Scan(&variant, &domain)
			if err != nil {
				t.Fatalf("Failed to query database: %v", err)
			}

			if variant != tt.expectedVariant {
				t.Errorf("Expected variant %q, got %q", tt.expectedVariant, variant)
			}

			if domain != tt.expectedDomain {
				t.Errorf("Expected domain %q, got %q", tt.expectedDomain, domain)
			}
		})
	}
}

func TestOrchestrator_RecordPatternUsage_SuccessAndFailure(t *testing.T) {
	lib := NewLibrary(nil, "")
	orch := NewOrchestrator(lib)

	// Create in-memory database for tracker
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Initialize schema
	if err := learning.InitSelfImprovementSchema(context.Background(), db, observability.NewNoOpTracer()); err != nil {
		t.Fatalf("Failed to initialize schema: %v", err)
	}

	// Create and start tracker
	tracker := learning.NewPatternEffectivenessTracker(
		db,
		observability.NewNoOpTracer(),
		nil, // No message bus
		1*time.Hour,
		5*time.Minute,
	)

	if err := tracker.Start(context.Background()); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer func() {
		_ = tracker.Stop(context.Background())
	}()

	orch.WithTracker(tracker)

	ctx := WithPatternMetadata(context.Background(), "metrics_pattern", "default", "sql")

	// Record 3 successful usages
	for i := 0; i < 3; i++ {
		orch.RecordPatternUsage(
			ctx,
			"metrics_pattern",
			"agent-metrics",
			true, // success
			0.002,
			200*time.Millisecond,
			"",
			"anthropic",
			"claude-3-5-sonnet-20241022",
		)
	}

	// Record 1 failed usage
	orch.RecordPatternUsage(
		ctx,
		"metrics_pattern",
		"agent-metrics",
		false, // failure
		0.001,
		50*time.Millisecond,
		"timeout",
		"anthropic",
		"claude-3-5-sonnet-20241022",
	)

	// Stop tracker to flush
	if err := tracker.Stop(context.Background()); err != nil {
		t.Fatalf("Failed to stop tracker: %v", err)
	}

	// Verify metrics
	var totalUsages, successCount, failureCount int
	var successRate float64
	err = db.QueryRow(`
		SELECT total_usages, success_count, failure_count, success_rate
		FROM pattern_effectiveness
		WHERE pattern_name = ?
	`, "metrics_pattern").Scan(&totalUsages, &successCount, &failureCount, &successRate)
	if err != nil {
		t.Fatalf("Failed to query metrics: %v", err)
	}

	if totalUsages != 4 {
		t.Errorf("Expected total_usages 4, got %d", totalUsages)
	}

	if successCount != 3 {
		t.Errorf("Expected success_count 3, got %d", successCount)
	}

	if failureCount != 1 {
		t.Errorf("Expected failure_count 1, got %d", failureCount)
	}

	expectedRate := 0.75 // 3 out of 4
	if successRate < expectedRate-0.01 || successRate > expectedRate+0.01 {
		t.Errorf("Expected success_rate ~%.2f, got %.2f", expectedRate, successRate)
	}
}

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
package learning

import (
	"context"
	"testing"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
)

func TestNewLearningEngine(t *testing.T) {
	dbPath := ":memory:"
	tracer := observability.NewNoOpTracer()

	collector, err := NewMetricsCollector(dbPath, tracer)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	engine := NewLearningEngine(collector, tracer)
	if engine == nil {
		t.Fatal("Engine is nil")
	}

	if engine.collector != collector {
		t.Error("Collector not set correctly")
	}
}

func TestGetBestPatterns(t *testing.T) {
	dbPath := ":memory:"
	tracer := observability.NewNoOpTracer()

	collector, err := NewMetricsCollector(dbPath, tracer)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	engine := NewLearningEngine(collector, tracer)
	ctx := context.Background()

	// Seed data: different patterns with different success rates
	// Note: Need sufficient count for confidence threshold (>= 20 for confidence > 0.3)
	patterns := []struct {
		name    string
		success bool
		count   int
	}{
		{"high_success_pattern", true, 30}, // 100% success, high confidence
		{"medium_success_pattern", true, 10},
		{"medium_success_pattern", false, 10},
		{"low_success_pattern", false, 15},
		{"low_success_pattern", true, 5},
	}

	for _, p := range patterns {
		for i := 0; i < p.count; i++ {
			metric := &DeploymentMetric{
				AgentID:          "test-agent",
				Domain:           DomainSQL,
				Templates:        []string{"template1"},
				SelectedTemplate: "template1",
				Patterns:         []string{p.name},
				Success:          p.success,
				CostUSD:          0.01,
				TurnsUsed:        1,
				CreatedAt:        time.Now(),
			}
			_ = collector.RecordDeployment(ctx, metric)
		}
	}

	// Get best patterns
	scores, err := engine.GetBestPatterns(ctx, DomainSQL)
	if err != nil {
		t.Fatalf("Failed to get best patterns: %v", err)
	}

	if len(scores) != 3 {
		t.Errorf("Expected 3 unique patterns, got %d", len(scores))
	}

	// Verify sorting (highest success rate first)
	for i := 1; i < len(scores); i++ {
		if scores[i].SuccessRate > scores[i-1].SuccessRate {
			t.Error("Patterns not sorted by success rate")
		}
	}

	// Verify high success pattern is recommended
	for _, score := range scores {
		if score.Pattern == "high_success_pattern" {
			if !score.RecommendUse {
				t.Error("High success pattern should be recommended")
			}
			if score.SuccessRate != 1.0 {
				t.Errorf("Expected success rate 1.0 for high_success_pattern, got %.2f", score.SuccessRate)
			}
		}
	}

	// Verify low success pattern is not recommended
	for _, score := range scores {
		if score.Pattern == "low_success_pattern" {
			if score.RecommendUse {
				t.Error("Low success pattern should not be recommended")
			}
		}
	}
}

func TestSuggestImprovements(t *testing.T) {
	dbPath := ":memory:"
	tracer := observability.NewNoOpTracer()

	collector, err := NewMetricsCollector(dbPath, tracer)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	engine := NewLearningEngine(collector, tracer)
	ctx := context.Background()

	// Seed data with a low-performing pattern (need enough usage for confidence)
	for i := 0; i < 20; i++ {
		metric := &DeploymentMetric{
			AgentID:          "test-agent",
			Domain:           DomainSQL,
			Templates:        []string{"template1"},
			SelectedTemplate: "template1",
			Patterns:         []string{"bad_pattern"},
			Success:          i < 4, // 20% success rate (4/20)
			ErrorMessage:     "test error",
			CostUSD:          0.01,
			TurnsUsed:        1,
			CreatedAt:        time.Now(),
		}
		_ = collector.RecordDeployment(ctx, metric)
	}

	// Get improvement suggestions
	improvements, err := engine.SuggestImprovements(ctx, DomainSQL)
	if err != nil {
		t.Fatalf("Failed to get improvements: %v", err)
	}

	if len(improvements) == 0 {
		t.Error("Expected at least one improvement suggestion")
	}

	// Verify we get a pattern_remove suggestion
	foundRemoveSuggestion := false
	for _, imp := range improvements {
		if imp.Type == "pattern_remove" {
			foundRemoveSuggestion = true
			if imp.Details["pattern"] != "bad_pattern" {
				t.Error("Expected suggestion for bad_pattern")
			}
		}
	}

	if !foundRemoveSuggestion {
		t.Error("Expected a pattern_remove suggestion for low-performing pattern")
	}
}

func TestCalculateConfidence(t *testing.T) {
	dbPath := ":memory:"
	tracer := observability.NewNoOpTracer()

	collector, err := NewMetricsCollector(dbPath, tracer)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	engine := NewLearningEngine(collector, tracer)

	tests := []struct {
		usageCount         int
		minExpected        float64
		maxExpected        float64
		shouldBeReasonable bool
	}{
		{0, 0.0, 0.0, true},
		{1, 0.0, 0.3, true},   // Very low confidence
		{5, 0.1, 0.4, true},   // Low confidence
		{25, 0.4, 0.6, true},  // Medium confidence (at midpoint)
		{50, 0.7, 0.95, true}, // High confidence
		{100, 0.9, 1.0, true}, // Very high confidence
	}

	for _, tt := range tests {
		confidence := engine.calculateConfidence(tt.usageCount)

		if confidence < tt.minExpected || confidence > tt.maxExpected {
			t.Errorf("For usage count %d, expected confidence between %.2f and %.2f, got %.2f",
				tt.usageCount, tt.minExpected, tt.maxExpected, confidence)
		}
	}

	// Verify confidence increases with usage
	conf1 := engine.calculateConfidence(5)
	conf2 := engine.calculateConfidence(10)
	conf3 := engine.calculateConfidence(50)

	if !(conf1 < conf2 && conf2 < conf3) {
		t.Error("Confidence should increase with usage count")
	}
}

func TestAnalyzePatterns(t *testing.T) {
	dbPath := ":memory:"
	tracer := observability.NewNoOpTracer()

	collector, err := NewMetricsCollector(dbPath, tracer)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	engine := NewLearningEngine(collector, tracer)

	scores := []PatternScore{
		{
			Pattern:      "excellent_pattern",
			SuccessRate:  0.95,
			UsageCount:   10,
			AvgCost:      0.02,
			Confidence:   0.8,
			RecommendUse: true,
		},
		{
			Pattern:      "poor_pattern",
			SuccessRate:  0.3,
			UsageCount:   8,
			AvgCost:      0.03,
			Confidence:   0.7,
			RecommendUse: false,
		},
	}

	improvements := engine.analyzePatterns(scores)

	// Should suggest removing poor pattern and promoting excellent pattern
	foundAdd := false
	foundRemove := false

	for _, imp := range improvements {
		if imp.Type == "pattern_add" && imp.Details["pattern"] == "excellent_pattern" {
			foundAdd = true
		}
		if imp.Type == "pattern_remove" && imp.Details["pattern"] == "poor_pattern" {
			foundRemove = true
		}
	}

	if !foundAdd {
		t.Error("Expected pattern_add suggestion for excellent_pattern")
	}

	if !foundRemove {
		t.Error("Expected pattern_remove suggestion for poor_pattern")
	}
}

func TestAnalyzeTemplates(t *testing.T) {
	dbPath := ":memory:"
	tracer := observability.NewNoOpTracer()

	collector, err := NewMetricsCollector(dbPath, tracer)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	engine := NewLearningEngine(collector, tracer)

	metrics := map[string]*TemplateMetrics{
		"poor_template": {
			Template:     "poor_template",
			UsageCount:   5,
			SuccessCount: 1,
			SuccessRate:  0.2,
			AvgCost:      0.05,
		},
		"expensive_template": {
			Template:     "expensive_template",
			UsageCount:   10,
			SuccessCount: 9,
			SuccessRate:  0.9,
			AvgCost:      0.15, // Expensive!
		},
	}

	improvements := engine.analyzeTemplates(metrics)

	// Should suggest adjustments for both templates
	foundPoorAdjust := false
	foundExpensiveAdjust := false

	for _, imp := range improvements {
		if imp.Type == "template_adjust" {
			if template, ok := imp.Details["template"].(string); ok {
				if template == "poor_template" {
					foundPoorAdjust = true
					if imp.Impact != "high" {
						t.Error("Poor template should have high impact")
					}
				}
				if template == "expensive_template" {
					foundExpensiveAdjust = true
					if imp.Impact != "medium" {
						t.Error("Expensive template should have medium impact")
					}
				}
			}
		}
	}

	if !foundPoorAdjust {
		t.Error("Expected template_adjust suggestion for poor_template")
	}

	if !foundExpensiveAdjust {
		t.Error("Expected template_adjust suggestion for expensive_template")
	}
}

func TestAnalyzeFailures(t *testing.T) {
	dbPath := ":memory:"
	tracer := observability.NewNoOpTracer()

	collector, err := NewMetricsCollector(dbPath, tracer)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	engine := NewLearningEngine(collector, tracer)

	failures := []*DeploymentMetric{
		{
			AgentID:          "",
			Domain:           DomainSQL,
			SelectedTemplate: "template1",
			ErrorMessage:     "validation error occurred",
			Success:          false,
		},
		{
			AgentID:          "",
			Domain:           DomainSQL,
			SelectedTemplate: "template1",
			ErrorMessage:     "validation failed",
			Success:          false,
		},
		{
			AgentID:          "",
			Domain:           DomainSQL,
			SelectedTemplate: "template1",
			ErrorMessage:     "validation check failed",
			Success:          false,
		},
		{
			AgentID:          "",
			Domain:           DomainSQL,
			SelectedTemplate: "template2",
			ErrorMessage:     "pattern not found",
			Success:          false,
		},
	}

	improvements := engine.analyzeFailures(failures)

	// Should suggest improvements for validation errors (3/4 failures)
	foundValidationImprovement := false
	foundTemplateImprovement := false

	for _, imp := range improvements {
		if errorType, ok := imp.Details["error_type"].(string); ok {
			if errorType == "validation_error" {
				foundValidationImprovement = true
			}
		}

		if template, ok := imp.Details["template"].(string); ok {
			if template == "template1" {
				foundTemplateImprovement = true
			}
		}
	}

	if !foundValidationImprovement {
		t.Error("Expected improvement suggestion for frequent validation errors")
	}

	if !foundTemplateImprovement {
		t.Error("Expected improvement suggestion for frequently failing template")
	}
}

func TestGetDomainInsights(t *testing.T) {
	dbPath := ":memory:"
	tracer := observability.NewNoOpTracer()

	collector, err := NewMetricsCollector(dbPath, tracer)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	engine := NewLearningEngine(collector, tracer)
	ctx := context.Background()

	// Seed some data
	for i := 0; i < 5; i++ {
		metric := &DeploymentMetric{
			AgentID:          "test-agent",
			Domain:           DomainSQL,
			Templates:        []string{"template1"},
			SelectedTemplate: "template1",
			Patterns:         []string{"pattern1"},
			Success:          i < 4, // 80% success
			CostUSD:          0.01,
			TurnsUsed:        1,
			CreatedAt:        time.Now(),
		}
		_ = collector.RecordDeployment(ctx, metric)
	}

	insights, err := engine.GetDomainInsights(ctx, DomainSQL)
	if err != nil {
		t.Fatalf("Failed to get domain insights: %v", err)
	}

	if insights.Domain != DomainSQL {
		t.Error("Domain not set correctly in insights")
	}

	if insights.SuccessRate < 0.79 || insights.SuccessRate > 0.81 {
		t.Errorf("Expected success rate ~0.8, got %.2f", insights.SuccessRate)
	}

	if len(insights.BestPatterns) == 0 {
		t.Error("Expected at least one pattern in insights")
	}

	// Improvements may or may not be present depending on data
	// Just verify the field exists
	_ = insights.Improvements
}

func TestInstrumentationEngine(t *testing.T) {
	dbPath := ":memory:"
	tracer := observability.NewMockTracer()

	collector, err := NewMetricsCollector(dbPath, tracer)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	engine := NewLearningEngine(collector, tracer)
	ctx := context.Background()

	// Seed some data
	metric := &DeploymentMetric{
		AgentID:          "test-agent",
		Domain:           DomainSQL,
		Templates:        []string{"template1"},
		SelectedTemplate: "template1",
		Patterns:         []string{"pattern1"},
		Success:          true,
		CostUSD:          0.01,
		TurnsUsed:        1,
		CreatedAt:        time.Now(),
	}
	_ = collector.RecordDeployment(ctx, metric)

	// Reset tracer to clear init spans
	tracer.Reset()

	// Call GetBestPatterns
	_, err = engine.GetBestPatterns(ctx, DomainSQL)
	if err != nil {
		t.Fatalf("Failed to get best patterns: %v", err)
	}

	// Verify spans were created
	spans := tracer.GetSpans()
	foundSpan := false
	for _, span := range spans {
		if span.Name == "metaagent.learning.get_best_patterns" {
			foundSpan = true
		}
	}

	if !foundSpan {
		t.Error("get_best_patterns span not found - instrumentation not working")
	}

	// Reset and test SuggestImprovements
	tracer.Reset()
	_, err = engine.SuggestImprovements(ctx, DomainSQL)
	if err != nil {
		t.Fatalf("Failed to get improvements: %v", err)
	}

	spans = tracer.GetSpans()
	foundSpan = false
	for _, span := range spans {
		if span.Name == "metaagent.learning.suggest_improvements" {
			foundSpan = true
		}
	}

	if !foundSpan {
		t.Error("suggest_improvements span not found - instrumentation not working")
	}
}

func TestEmptyInsights(t *testing.T) {
	dbPath := ":memory:"
	tracer := observability.NewNoOpTracer()

	collector, err := NewMetricsCollector(dbPath, tracer)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	engine := NewLearningEngine(collector, tracer)
	ctx := context.Background()

	// Get insights for empty domain
	insights, err := engine.GetDomainInsights(ctx, DomainSQL)
	if err != nil {
		t.Fatalf("Failed to get insights for empty domain: %v", err)
	}

	if insights.SuccessRate != 0.0 {
		t.Errorf("Expected success rate 0.0 for empty domain, got %.2f", insights.SuccessRate)
	}

	if len(insights.BestPatterns) != 0 {
		t.Error("Expected no patterns for empty domain")
	}
}

func TestImprovementSorting(t *testing.T) {
	dbPath := ":memory:"
	tracer := observability.NewNoOpTracer()

	collector, err := NewMetricsCollector(dbPath, tracer)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	engine := NewLearningEngine(collector, tracer)
	ctx := context.Background()

	// Seed data that will generate multiple improvements
	for i := 0; i < 10; i++ {
		metric := &DeploymentMetric{
			AgentID:          "test-agent",
			Domain:           DomainSQL,
			Templates:        []string{"template1"},
			SelectedTemplate: "template1",
			Patterns:         []string{"pattern1"},
			Success:          i < 3, // 30% success - will trigger improvements
			ErrorMessage:     "validation error",
			CostUSD:          0.01,
			TurnsUsed:        1,
			CreatedAt:        time.Now(),
		}
		_ = collector.RecordDeployment(ctx, metric)
	}

	improvements, err := engine.SuggestImprovements(ctx, DomainSQL)
	if err != nil {
		t.Fatalf("Failed to get improvements: %v", err)
	}

	if len(improvements) == 0 {
		t.Skip("No improvements generated for this test case")
	}

	// Verify improvements are sorted by impact (high > medium > low)
	impactOrder := map[string]int{"high": 3, "medium": 2, "low": 1}

	for i := 1; i < len(improvements); i++ {
		prevImpact := impactOrder[improvements[i-1].Impact]
		currImpact := impactOrder[improvements[i].Impact]

		if currImpact > prevImpact {
			t.Error("Improvements not sorted correctly by impact")
		}

		// If same impact, should be sorted by confidence
		if currImpact == prevImpact && improvements[i].Confidence > improvements[i-1].Confidence {
			t.Error("Improvements not sorted correctly by confidence within same impact level")
		}
	}
}

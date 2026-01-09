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
	"fmt"
	"math"
	"sort"

	"github.com/teradata-labs/loom/pkg/observability"
)

// LearningEngine analyzes deployment metrics and provides insights
type LearningEngine struct {
	collector *MetricsCollector
	tracer    observability.Tracer
}

// PatternScore represents a pattern with its performance score
type PatternScore struct {
	Pattern      string
	SuccessRate  float64
	UsageCount   int
	AvgCost      float64
	Confidence   float64 // Based on sample size
	RecommendUse bool    // Should this pattern be used?
}

// Improvement represents a suggested improvement
type Improvement struct {
	Type        string // "pattern_add", "pattern_remove", "template_adjust"
	Description string
	Confidence  float64
	Impact      string // "high", "medium", "low"
	Details     map[string]interface{}
}

// NewLearningEngine creates a new learning engine
func NewLearningEngine(collector *MetricsCollector, tracer observability.Tracer) *LearningEngine {
	return &LearningEngine{
		collector: collector,
		tracer:    tracer,
	}
}

// GetBestPatterns returns the best performing patterns for a domain
func (le *LearningEngine) GetBestPatterns(ctx context.Context, domain DomainType) ([]PatternScore, error) {
	ctx, span := le.tracer.StartSpan(ctx, "metaagent.learning.get_best_patterns")
	defer le.tracer.EndSpan(span)

	span.SetAttribute("domain", string(domain))

	// Get pattern performance from collector
	patternMetrics, err := le.collector.GetPatternPerformance(ctx, domain)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to get pattern performance: %w", err)
	}

	// Convert to scored patterns
	var scores []PatternScore
	for _, metrics := range patternMetrics {
		confidence := le.calculateConfidence(metrics.UsageCount)

		// Only recommend patterns with good success rate and reasonable confidence
		recommendUse := metrics.SuccessRate >= 0.7 && confidence >= 0.3

		scores = append(scores, PatternScore{
			Pattern:      metrics.Pattern,
			SuccessRate:  metrics.SuccessRate,
			UsageCount:   metrics.UsageCount,
			AvgCost:      metrics.AvgCost,
			Confidence:   confidence,
			RecommendUse: recommendUse,
		})
	}

	// Sort by success rate (descending), then by confidence (descending)
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].SuccessRate != scores[j].SuccessRate {
			return scores[i].SuccessRate > scores[j].SuccessRate
		}
		return scores[i].Confidence > scores[j].Confidence
	})

	span.SetAttribute("patterns_scored", len(scores))
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Best patterns calculated"}

	// Record metrics
	le.tracer.RecordMetric("metaagent.learning.patterns_analyzed", float64(len(scores)), map[string]string{
		"domain": string(domain),
	})

	return scores, nil
}

// SuggestImprovements analyzes metrics and suggests improvements
func (le *LearningEngine) SuggestImprovements(ctx context.Context, domain DomainType) ([]Improvement, error) {
	ctx, span := le.tracer.StartSpan(ctx, "metaagent.learning.suggest_improvements")
	defer le.tracer.EndSpan(span)

	span.SetAttribute("domain", string(domain))

	var improvements []Improvement

	// 1. Analyze pattern performance
	patternScores, err := le.GetBestPatterns(ctx, domain)
	if err != nil {
		span.RecordError(err)
		// Don't fail if pattern analysis fails, continue with other checks
	} else {
		improvements = append(improvements, le.analyzePatterns(patternScores)...)
	}

	// 2. Analyze template performance
	templateMetrics, err := le.collector.GetTemplatePerformance(ctx, domain)
	if err != nil {
		span.RecordError(err)
		// Don't fail if template analysis fails
	} else {
		improvements = append(improvements, le.analyzeTemplates(templateMetrics)...)
	}

	// 3. Analyze recent failures
	failures, err := le.collector.GetRecentFailures(ctx, domain, 10)
	if err != nil {
		span.RecordError(err)
		// Don't fail if failure analysis fails
	} else {
		improvements = append(improvements, le.analyzeFailures(failures)...)
	}

	// Sort improvements by confidence and impact
	sort.Slice(improvements, func(i, j int) bool {
		// High impact first
		if improvements[i].Impact != improvements[j].Impact {
			impactOrder := map[string]int{"high": 3, "medium": 2, "low": 1}
			return impactOrder[improvements[i].Impact] > impactOrder[improvements[j].Impact]
		}
		// Then by confidence
		return improvements[i].Confidence > improvements[j].Confidence
	})

	span.SetAttribute("improvements_found", len(improvements))
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Improvements suggested"}

	// Record metrics
	le.tracer.RecordMetric("metaagent.learning.improvements_suggested", float64(len(improvements)), map[string]string{
		"domain": string(domain),
	})

	return improvements, nil
}

// analyzePatterns suggests pattern-related improvements
func (le *LearningEngine) analyzePatterns(scores []PatternScore) []Improvement {
	var improvements []Improvement

	// Find low-performing patterns (recommend removal)
	for _, score := range scores {
		if score.UsageCount >= 5 && score.SuccessRate < 0.5 && score.Confidence >= 0.3 {
			improvements = append(improvements, Improvement{
				Type: "pattern_remove",
				Description: fmt.Sprintf("Consider removing pattern '%s' (success rate: %.1f%%, %d uses)",
					score.Pattern, score.SuccessRate*100, score.UsageCount),
				Confidence: score.Confidence,
				Impact:     "medium",
				Details: map[string]interface{}{
					"pattern":      score.Pattern,
					"success_rate": score.SuccessRate,
					"usage_count":  score.UsageCount,
				},
			})
		}
	}

	// Find high-performing patterns (recommend more usage)
	for _, score := range scores {
		if score.UsageCount >= 3 && score.SuccessRate >= 0.9 && score.Confidence >= 0.3 {
			improvements = append(improvements, Improvement{
				Type: "pattern_add",
				Description: fmt.Sprintf("Pattern '%s' has excellent success rate (%.1f%%, %d uses) - consider using more",
					score.Pattern, score.SuccessRate*100, score.UsageCount),
				Confidence: score.Confidence,
				Impact:     "high",
				Details: map[string]interface{}{
					"pattern":      score.Pattern,
					"success_rate": score.SuccessRate,
					"usage_count":  score.UsageCount,
				},
			})
		}
	}

	return improvements
}

// analyzeTemplates suggests template-related improvements
func (le *LearningEngine) analyzeTemplates(metrics map[string]*TemplateMetrics) []Improvement {
	var improvements []Improvement

	for _, metric := range metrics {
		// Find templates with low success rates
		if metric.UsageCount >= 3 && metric.SuccessRate < 0.6 {
			confidence := le.calculateConfidence(metric.UsageCount)
			improvements = append(improvements, Improvement{
				Type: "template_adjust",
				Description: fmt.Sprintf("Template '%s' has low success rate (%.1f%%, %d uses) - review template configuration",
					metric.Template, metric.SuccessRate*100, metric.UsageCount),
				Confidence: confidence,
				Impact:     "high",
				Details: map[string]interface{}{
					"template":     metric.Template,
					"success_rate": metric.SuccessRate,
					"usage_count":  metric.UsageCount,
					"avg_cost":     metric.AvgCost,
				},
			})
		}

		// Find expensive templates
		if metric.UsageCount >= 3 && metric.AvgCost > 0.10 {
			confidence := le.calculateConfidence(metric.UsageCount)
			improvements = append(improvements, Improvement{
				Type: "template_adjust",
				Description: fmt.Sprintf("Template '%s' has high average cost ($%.3f per deployment) - consider optimizing prompts",
					metric.Template, metric.AvgCost),
				Confidence: confidence,
				Impact:     "medium",
				Details: map[string]interface{}{
					"template":    metric.Template,
					"avg_cost":    metric.AvgCost,
					"usage_count": metric.UsageCount,
				},
			})
		}
	}

	return improvements
}

// analyzeFailures analyzes recent failures for common patterns
func (le *LearningEngine) analyzeFailures(failures []*DeploymentMetric) []Improvement {
	var improvements []Improvement

	if len(failures) == 0 {
		return improvements
	}

	// Count common error patterns
	errorPatterns := make(map[string]int)
	templateFailures := make(map[string]int)

	for _, failure := range failures {
		// Categorize errors
		if failure.ErrorMessage != "" {
			// Simple categorization (can be enhanced with NLP)
			if contains(failure.ErrorMessage, "validation") {
				errorPatterns["validation_error"]++
			} else if contains(failure.ErrorMessage, "pattern") {
				errorPatterns["pattern_error"]++
			} else if contains(failure.ErrorMessage, "template") {
				errorPatterns["template_error"]++
			} else {
				errorPatterns["other_error"]++
			}
		}

		// Track template failures
		if failure.SelectedTemplate != "" {
			templateFailures[failure.SelectedTemplate]++
		}
	}

	// Suggest improvements based on error patterns
	for errorType, count := range errorPatterns {
		if count >= 3 {
			confidence := float64(count) / float64(len(failures))
			improvements = append(improvements, Improvement{
				Type: "pattern_add",
				Description: fmt.Sprintf("Frequent %s errors (%d/%d failures) - review error handling",
					errorType, count, len(failures)),
				Confidence: confidence,
				Impact:     "high",
				Details: map[string]interface{}{
					"error_type":     errorType,
					"failure_count":  count,
					"total_failures": len(failures),
				},
			})
		}
	}

	// Suggest improvements for failing templates
	for template, count := range templateFailures {
		if count >= 2 {
			confidence := float64(count) / float64(len(failures))
			improvements = append(improvements, Improvement{
				Type: "template_adjust",
				Description: fmt.Sprintf("Template '%s' failed %d times recently - investigate template issues",
					template, count),
				Confidence: confidence,
				Impact:     "high",
				Details: map[string]interface{}{
					"template":      template,
					"failure_count": count,
				},
			})
		}
	}

	return improvements
}

// calculateConfidence calculates confidence score based on sample size
// Uses sigmoid-like function: confidence increases with usage count
// Reaches ~0.9 at 50 uses, ~0.99 at 100 uses
func (le *LearningEngine) calculateConfidence(usageCount int) float64 {
	if usageCount == 0 {
		return 0.0
	}

	// Sigmoid function: 1 / (1 + e^(-k*(x-x0)))
	// k controls steepness, x0 is midpoint
	k := 0.1
	x0 := 25.0

	confidence := 1.0 / (1.0 + math.Exp(-k*(float64(usageCount)-x0)))

	// Ensure minimum confidence for small samples
	if usageCount < 3 {
		confidence = math.Min(confidence, 0.3)
	}

	return confidence
}

// GetDomainInsights provides comprehensive insights for a domain
func (le *LearningEngine) GetDomainInsights(ctx context.Context, domain DomainType) (*DomainInsights, error) {
	ctx, span := le.tracer.StartSpan(ctx, "metaagent.learning.get_domain_insights")
	defer le.tracer.EndSpan(span)

	span.SetAttribute("domain", string(domain))

	insights := &DomainInsights{
		Domain: domain,
	}

	// Get success rate
	successRate, err := le.collector.GetSuccessRate(ctx, domain)
	if err != nil {
		span.RecordError(err)
		// Continue even if this fails
	} else {
		insights.SuccessRate = successRate
	}

	// Get best patterns
	bestPatterns, err := le.GetBestPatterns(ctx, domain)
	if err != nil {
		span.RecordError(err)
		// Continue even if this fails
	} else {
		insights.BestPatterns = bestPatterns
	}

	// Get improvements
	improvements, err := le.SuggestImprovements(ctx, domain)
	if err != nil {
		span.RecordError(err)
		// Continue even if this fails
	} else {
		insights.Improvements = improvements
	}

	span.Status = observability.Status{Code: observability.StatusOK, Message: "Domain insights generated"}

	return insights, nil
}

// DomainInsights contains comprehensive insights for a domain
type DomainInsights struct {
	Domain       DomainType
	SuccessRate  float64
	BestPatterns []PatternScore
	Improvements []Improvement
}

// contains checks if a string contains a substring (case-insensitive)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(len(s) > len(substr) && (s[:len(substr)] == substr ||
			s[len(s)-len(substr):] == substr ||
			containsMiddle(s, substr))))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

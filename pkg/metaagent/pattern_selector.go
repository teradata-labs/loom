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
	"fmt"
	"strings"

	"github.com/teradata-labs/loom/pkg/observability"
)

// PatternSelector selects relevant patterns based on agent capabilities
type PatternSelector struct {
	capabilityMap map[string][]string // capability → pattern names
	tracer        observability.Tracer
}

// NewPatternSelector creates a new pattern selector with built-in capability mapping
func NewPatternSelector(tracer observability.Tracer) *PatternSelector {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &PatternSelector{
		capabilityMap: buildCapabilityMap(),
		tracer:        tracer,
	}
}

// SelectPatterns selects relevant patterns based on capabilities
// Returns list of pattern names (e.g., "sql/data_quality/data_profiling")
func (ps *PatternSelector) SelectPatterns(ctx context.Context, analysis *Analysis) ([]string, error) {
	// Start span for observability
	_, span := ps.tracer.StartSpan(ctx, "metaagent.pattern_selector.select_patterns")
	defer ps.tracer.EndSpan(span)

	if analysis == nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: "Analysis is nil",
		}
		ps.tracer.RecordMetric("metaagent.pattern_selector.select_patterns.failed", 1.0, map[string]string{
			"error": "nil_analysis",
		})
		return nil, fmt.Errorf("analysis cannot be nil")
	}

	// Add span attributes
	span.Attributes["domain"] = analysis.Domain
	span.Attributes["capabilities_count"] = fmt.Sprintf("%d", len(analysis.Capabilities))

	// Use map to deduplicate patterns
	selectedPatterns := make(map[string]float64) // pattern name → score

	// Map each capability to patterns
	for _, capability := range analysis.Capabilities {
		patterns := ps.matchCapability(capability, analysis.Domain)
		for _, pattern := range patterns {
			score := ps.scorePattern(pattern, capability, analysis)
			// Keep highest score if pattern already selected
			if existingScore, exists := selectedPatterns[pattern]; !exists || score > existingScore {
				selectedPatterns[pattern] = score
			}
		}
	}

	// If no patterns selected, use domain defaults
	if len(selectedPatterns) == 0 {
		defaults := ps.getDomainDefaults(analysis.Domain)
		span.Attributes["patterns_selected"] = fmt.Sprintf("%d", len(defaults))
		span.Attributes["used_defaults"] = "true"
		span.Status = observability.Status{
			Code:    observability.StatusOK,
			Message: "Using domain defaults (no capability matches)",
		}
		ps.tracer.RecordMetric("metaagent.pattern_selector.select_patterns.defaults", 1.0, map[string]string{
			"domain": string(analysis.Domain),
		})
		return defaults, nil
	}

	// Resolve dependencies
	finalPatterns := ps.resolveDependencies(selectedPatterns)

	// Record success metrics
	span.Attributes["patterns_selected"] = fmt.Sprintf("%d", len(finalPatterns))
	span.Attributes["used_defaults"] = "false"
	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: fmt.Sprintf("Selected %d patterns", len(finalPatterns)),
	}
	ps.tracer.RecordMetric("metaagent.pattern_selector.select_patterns.success", 1.0, map[string]string{
		"domain": string(analysis.Domain),
	})
	ps.tracer.RecordMetric("metaagent.pattern_selector.patterns_count", float64(len(finalPatterns)), map[string]string{
		"domain": string(analysis.Domain),
	})

	return finalPatterns, nil
}

// matchCapability matches a capability to pattern names
func (ps *PatternSelector) matchCapability(capability Capability, domain DomainType) []string {
	// Normalize capability name for matching
	capName := strings.ToLower(strings.TrimSpace(capability.Name))

	// Direct match in capability map
	if patterns, exists := ps.capabilityMap[capName]; exists {
		return ps.filterByDomain(patterns, domain)
	}

	// Fuzzy match - check if capability name contains any mapped capability
	for mappedCap, patterns := range ps.capabilityMap {
		if strings.Contains(capName, mappedCap) || strings.Contains(mappedCap, capName) {
			return ps.filterByDomain(patterns, domain)
		}
	}

	// Category-based match
	if capability.Category != "" {
		return ps.matchCategory(capability.Category, domain)
	}

	return nil
}

// scorePattern calculates relevance score for a pattern
func (ps *PatternSelector) scorePattern(patternName string, capability Capability, analysis *Analysis) float64 {
	score := 0.5 // Base score

	// Boost for direct domain match
	if strings.Contains(patternName, string(analysis.Domain)) {
		score += 0.3
	}

	// Boost for high-priority capabilities
	if capability.Priority <= 3 {
		score += 0.2
	} else if capability.Priority <= 5 {
		score += 0.1
	}

	// Boost for complexity match
	if strings.Contains(patternName, "advanced") && analysis.Complexity == ComplexityHigh {
		score += 0.1
	}

	return score
}

// filterByDomain filters patterns to match the domain
func (ps *PatternSelector) filterByDomain(patterns []string, domain DomainType) []string {
	// If SQL domain, include generic sql/ patterns and domain-specific (postgres/, teradata/)
	if domain == DomainSQL {
		return patterns // Return all SQL-related patterns
	}

	// For other domains, filter to matching patterns
	filtered := make([]string, 0, len(patterns))
	domainPrefix := string(domain) + "/"
	for _, pattern := range patterns {
		if strings.HasPrefix(pattern, domainPrefix) || strings.HasPrefix(pattern, "sql/") {
			filtered = append(filtered, pattern)
		}
	}

	// If no domain-specific patterns, return all (generic patterns)
	if len(filtered) == 0 {
		return patterns
	}

	return filtered
}

// matchCategory matches patterns by category
func (ps *PatternSelector) matchCategory(category string, domain DomainType) []string {
	category = strings.ToLower(category)

	// Category → pattern prefix mapping
	categoryMap := map[string][]string{
		"analytics":      {"analytics/", "postgres/analytics/", "teradata/analytics/"},
		"data_quality":   {"sql/data_quality/", "teradata/data_quality/"},
		"data_discovery": {"teradata/data_discovery/"},
		"ml":             {"teradata/ml/"},
		"timeseries":     {"sql/timeseries/", "teradata/timeseries/"},
		"text":           {"sql/text/", "teradata/text/", "text/"},
		"api":            {"rest_api/"},
		"file":           {"document/"},
	}

	if prefixes, exists := categoryMap[category]; exists {
		return prefixes
	}

	return nil
}

// resolveDependencies resolves pattern dependencies and returns final list
func (ps *PatternSelector) resolveDependencies(selectedPatterns map[string]float64) []string {
	// Pattern dependencies (pattern → required dependencies)
	dependencies := map[string][]string{
		"postgres/analytics/missing_index_analysis":     {"postgres/analytics/sequential_scan_detection"},
		"postgres/analytics/join_optimization":          {"postgres/analytics/missing_index_analysis"},
		"sql/data_quality/data_validation":              {"sql/data_quality/data_profiling"},
		"teradata/analytics/funnel_analysis":            {"teradata/analytics/sessionize"},
		"teradata/data_discovery/key_detection":         {"teradata/data_discovery/signature_generation"},
		"teradata/data_discovery/foreign_key_detection": {"teradata/data_discovery/signature_generation", "teradata/data_discovery/key_detection"},
		"teradata/data_discovery/column_similarity":     {"teradata/data_discovery/signature_generation"},
		"teradata/data_discovery/data_profiling":        {"teradata/data_discovery/signature_generation"},
	}

	// Add dependencies to selection
	for pattern := range selectedPatterns {
		if deps, hasDeps := dependencies[pattern]; hasDeps {
			for _, dep := range deps {
				if _, exists := selectedPatterns[dep]; !exists {
					// Add dependency with lower score
					selectedPatterns[dep] = 0.3
				}
			}
		}
	}

	// Convert to sorted list (highest scores first)
	result := make([]string, 0, len(selectedPatterns))
	for pattern := range selectedPatterns {
		result = append(result, pattern)
	}

	return result
}

// getDomainDefaults returns default patterns for a domain when no capabilities match
func (ps *PatternSelector) getDomainDefaults(domain DomainType) []string {
	defaults := map[DomainType][]string{
		DomainSQL: {
			"sql/data_quality/data_profiling",
			"sql/data_quality/data_validation",
		},
		DomainREST: {
			"rest_api/health_check",
		},
		DomainFile: {
			"document/file_parser",
		},
		DomainDocument: {
			"document/document_analyzer",
		},
	}

	if patterns, exists := defaults[domain]; exists {
		return patterns
	}

	return []string{}
}

// buildCapabilityMap creates the mapping from capability names to pattern names
func buildCapabilityMap() map[string][]string {
	return map[string][]string{
		// SQL Performance
		"sql_performance_analysis": {
			"postgres/analytics/sequential_scan_detection",
			"postgres/analytics/query_rewrite",
		},
		"performance_analysis": {
			"postgres/analytics/sequential_scan_detection",
			"postgres/analytics/query_rewrite",
		},
		"slow_query_analysis": {
			"postgres/analytics/sequential_scan_detection",
		},

		// Index Optimization
		"index_optimization": {
			"postgres/analytics/missing_index_analysis",
			"postgres/analytics/join_optimization",
		},
		"missing_index_detection": {
			"postgres/analytics/missing_index_analysis",
		},

		// Query Optimization
		"query_optimization": {
			"postgres/analytics/query_rewrite",
			"postgres/analytics/subquery_to_join",
			"postgres/analytics/count_optimization",
			"postgres/analytics/distinct_elimination",
		},
		"join_optimization": {
			"postgres/analytics/join_optimization",
		},

		// Data Quality
		"data_quality": {
			"sql/data_quality/data_profiling",
			"sql/data_quality/duplicate_detection",
			"sql/data_quality/outlier_detection",
			"sql/data_quality/missing_value_analysis",
			"sql/data_quality/data_validation",
		},
		"data_profiling": {
			"sql/data_quality/data_profiling",
		},
		"duplicate_detection": {
			"sql/data_quality/duplicate_detection",
		},
		"outlier_detection": {
			"sql/data_quality/outlier_detection",
		},
		"missing_value_analysis": {
			"sql/data_quality/missing_value_analysis",
		},

		// Analytics
		"funnel_analysis": {
			"teradata/analytics/funnel_analysis",
			"teradata/analytics/sessionize",
		},
		"sessionization": {
			"teradata/analytics/sessionize",
		},
		"path_analysis": {
			"teradata/analytics/npath",
		},
		"attribution": {
			"teradata/analytics/attribution",
		},
		"churn_analysis": {
			"teradata/analytics/churn_analysis",
		},

		// Machine Learning
		"regression": {
			"teradata/ml/linear_regression",
			"teradata/ml/logistic_regression",
		},
		"classification": {
			"teradata/ml/logistic_regression",
			"teradata/ml/decision_tree",
		},
		"clustering": {
			"teradata/ml/kmeans",
		},

		// Time Series
		"time_series": {
			"sql/timeseries/moving_average",
			"sql/timeseries/arima",
		},
		"forecasting": {
			"sql/timeseries/arima",
		},

		// Text Processing
		"text_analysis": {
			"sql/text/ngram",
			"text/sentiment-analysis",
			"text/summarization",
		},
		"sentiment_analysis": {
			"text/sentiment-analysis",
		},
		"text_summarization": {
			"text/summarization",
		},

		// Code
		"code_generation": {
			"code/test-generation",
			"code/doc-generation",
		},

		// Prompt Engineering
		"prompt_engineering": {
			"prompt_engineering/chain-of-thought",
			"prompt_engineering/few-shot-learning",
			"prompt_engineering/structured-output",
		},

		// Vision
		"image_analysis": {
			"vision/chart-interpretation",
			"vision/form-extraction",
		},

		// Debugging
		"debugging": {
			"debugging/root-cause-analysis",
		},
		"error_analysis": {
			"debugging/root-cause-analysis",
		},

		// Semantic Mapping / Data Discovery (Teradata)
		"semantic_mapping": {
			"teradata/data_discovery/signature_generation",
			"teradata/data_discovery/key_detection",
			"teradata/data_discovery/foreign_key_detection",
			"teradata/data_discovery/column_similarity",
			"teradata/data_discovery/data_profiling",
		},
		"data_discovery": {
			"teradata/data_discovery/signature_generation",
			"teradata/data_discovery/key_detection",
			"teradata/data_discovery/foreign_key_detection",
			"teradata/data_discovery/column_similarity",
			"teradata/data_discovery/data_profiling",
		},
		"schema_matching": {
			"teradata/data_discovery/column_similarity",
			"teradata/data_discovery/signature_generation",
		},
		"key_detection": {
			"teradata/data_discovery/key_detection",
			"teradata/data_discovery/signature_generation",
		},
		"foreign_key_detection": {
			"teradata/data_discovery/foreign_key_detection",
			"teradata/data_discovery/key_detection",
			"teradata/data_discovery/signature_generation",
		},
		"column_profiling": {
			"teradata/data_discovery/data_profiling",
			"teradata/data_discovery/signature_generation",
		},
		"signature_generation": {
			"teradata/data_discovery/signature_generation",
		},
		"domain_discovery": {
			"teradata/data_discovery/domain_discovery",
			"teradata/data_discovery/column_similarity",
			"teradata/data_discovery/signature_generation",
		},
		"corpus_search": {
			"teradata/data_discovery/domain_discovery",
			"teradata/data_discovery/corpus_navigation",
		},
	}
}

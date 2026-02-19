// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package evals

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MetricsCalculator calculates evaluation metrics from test results
type MetricsCalculator struct {
	suite *loomv1.EvalSuite
}

// NewMetricsCalculator creates a new metrics calculator
func NewMetricsCalculator(suite *loomv1.EvalSuite) *MetricsCalculator {
	return &MetricsCalculator{
		suite: suite,
	}
}

// Calculate calculates all metrics for eval results
func (m *MetricsCalculator) Calculate(results []*loomv1.TestCaseResult) *loomv1.EvalMetrics {
	metrics := &loomv1.EvalMetrics{
		TotalTests:     types.SafeInt32(len(results)),
		PassedTests:    0,
		FailedTests:    0,
		Accuracy:       0,
		TotalCostUsd:   0,
		TotalLatencyMs: 0,
		TotalToolCalls: 0,
		CustomMetrics:  make(map[string]float64),
	}

	// Count passed/failed tests
	for _, result := range results {
		if result.Passed {
			metrics.PassedTests++
		} else {
			metrics.FailedTests++
		}

		// Accumulate cost and latency
		metrics.TotalCostUsd += result.CostUsd
		metrics.TotalLatencyMs += result.LatencyMs
		metrics.TotalToolCalls += types.SafeInt32(len(result.ToolsUsed))
	}

	// Calculate accuracy
	if metrics.TotalTests > 0 {
		metrics.Accuracy = float64(metrics.PassedTests) / float64(metrics.TotalTests)
	}

	// Calculate custom metrics based on suite configuration
	for _, metricName := range m.suite.Spec.Metrics {
		switch strings.ToLower(metricName) {
		case "accuracy":
			// Already calculated
		case "cost_efficiency":
			metrics.CustomMetrics["cost_efficiency"] = m.calculateCostEfficiency(results)
		case "latency":
			metrics.CustomMetrics["avg_latency_ms"] = m.calculateAvgLatency(results)
		case "tool_usage":
			metrics.CustomMetrics["avg_tools_per_test"] = m.calculateAvgToolUsage(results)
		}
	}

	return metrics
}

// calculateCostEfficiency calculates cost efficiency score (accuracy / cost)
func (m *MetricsCalculator) calculateCostEfficiency(results []*loomv1.TestCaseResult) float64 {
	if len(results) == 0 {
		return 0
	}

	passedTests := 0
	totalCost := 0.0

	for _, result := range results {
		if result.Passed {
			passedTests++
		}
		totalCost += result.CostUsd
	}

	if totalCost == 0 {
		return 0
	}

	// Cost efficiency = (passed tests / total cost) * 100
	// Higher is better
	return (float64(passedTests) / totalCost) * 100
}

// calculateAvgLatency calculates average latency in milliseconds
func (m *MetricsCalculator) calculateAvgLatency(results []*loomv1.TestCaseResult) float64 {
	if len(results) == 0 {
		return 0
	}

	totalLatency := int64(0)
	for _, result := range results {
		totalLatency += result.LatencyMs
	}

	return float64(totalLatency) / float64(len(results))
}

// calculateAvgToolUsage calculates average tool calls per test
func (m *MetricsCalculator) calculateAvgToolUsage(results []*loomv1.TestCaseResult) float64 {
	if len(results) == 0 {
		return 0
	}

	totalTools := 0
	for _, result := range results {
		totalTools += len(result.ToolsUsed)
	}

	return float64(totalTools) / float64(len(results))
}

// CreateEvalResult creates a complete eval result from test case results
func (m *MetricsCalculator) CreateEvalResult(
	suiteName string,
	agentID string,
	testResults []*loomv1.TestCaseResult,
) *loomv1.EvalResult {
	metrics := m.Calculate(testResults)

	result := &loomv1.EvalResult{
		SuiteName:   suiteName,
		AgentId:     agentID,
		RunAt:       timestamppb.Now(),
		Overall:     metrics,
		TestResults: testResults,
		Passed:      metrics.FailedTests == 0,
	}

	// Set failure reason if any tests failed
	if !result.Passed {
		result.FailureReason = fmt.Sprintf("%d of %d tests failed", metrics.FailedTests, metrics.TotalTests)
	}

	return result
}

// ValidateTestResult validates a single test case result against expectations
func ValidateTestResult(tc *loomv1.TestCase, actualOutput string, toolsUsed []string, costUsd float64, latencyMs int64) *loomv1.TestCaseResult {
	result := &loomv1.TestCaseResult{
		TestName:     tc.Name,
		Passed:       true,
		ActualOutput: actualOutput,
		ToolsUsed:    toolsUsed,
		CostUsd:      costUsd,
		LatencyMs:    latencyMs,
	}

	var failures []string

	// Check expected output contains
	for _, expected := range tc.ExpectedOutputContains {
		if !strings.Contains(actualOutput, expected) {
			failures = append(failures, fmt.Sprintf("output does not contain: %q", expected))
		}
	}

	// Check expected output not contains
	for _, notExpected := range tc.ExpectedOutputNotContains {
		if strings.Contains(actualOutput, notExpected) {
			failures = append(failures, fmt.Sprintf("output should not contain: %q", notExpected))
		}
	}

	// Check expected output regex
	if tc.ExpectedOutputRegex != "" {
		re, err := regexp.Compile(tc.ExpectedOutputRegex)
		if err != nil {
			failures = append(failures, fmt.Sprintf("invalid regex pattern: %q: %v", tc.ExpectedOutputRegex, err))
		} else if !re.MatchString(actualOutput) {
			failures = append(failures, fmt.Sprintf("output does not match regex: %q", tc.ExpectedOutputRegex))
		}
	}

	// Check expected tools
	if len(tc.ExpectedTools) > 0 {
		toolsUsedMap := make(map[string]bool)
		for _, tool := range toolsUsed {
			toolsUsedMap[tool] = true
		}

		for _, expectedTool := range tc.ExpectedTools {
			if !toolsUsedMap[expectedTool] {
				failures = append(failures, fmt.Sprintf("expected tool not used: %q", expectedTool))
			}
		}
	}

	// Check cost constraint
	if tc.MaxCostUsd > 0 && costUsd > tc.MaxCostUsd {
		failures = append(failures, fmt.Sprintf("cost %.4f exceeds maximum %.4f", costUsd, tc.MaxCostUsd))
	}

	// Check latency constraint
	if tc.MaxLatencyMs > 0 && latencyMs > int64(tc.MaxLatencyMs) {
		failures = append(failures, fmt.Sprintf("latency %dms exceeds maximum %dms", latencyMs, tc.MaxLatencyMs))
	}

	// Set result
	if len(failures) > 0 {
		result.Passed = false
		result.FailureReason = strings.Join(failures, "; ")
	}

	return result
}

// CompareEvalResults compares two eval results for A/B testing
func CompareEvalResults(baseline *loomv1.EvalResult, candidate *loomv1.EvalResult, metrics []string) map[string]float64 {
	comparison := make(map[string]float64)

	for _, metric := range metrics {
		switch strings.ToLower(metric) {
		case "accuracy":
			comparison["accuracy_delta"] = candidate.Overall.Accuracy - baseline.Overall.Accuracy
		case "cost_efficiency":
			baselineCost := baseline.Overall.CustomMetrics["cost_efficiency"]
			candidateCost := candidate.Overall.CustomMetrics["cost_efficiency"]
			comparison["cost_efficiency_delta"] = candidateCost - baselineCost
		case "latency":
			baselineLatency := baseline.Overall.CustomMetrics["avg_latency_ms"]
			candidateLatency := candidate.Overall.CustomMetrics["avg_latency_ms"]
			comparison["latency_delta_ms"] = candidateLatency - baselineLatency
		case "tool_usage":
			baselineTools := baseline.Overall.CustomMetrics["avg_tools_per_test"]
			candidateTools := candidate.Overall.CustomMetrics["avg_tools_per_test"]
			comparison["tool_usage_delta"] = candidateTools - baselineTools
		}
	}

	return comparison
}

// FormatEvalResult formats an eval result for human-readable output
func FormatEvalResult(result *loomv1.EvalResult) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Eval Suite: %s\n", result.SuiteName))
	sb.WriteString(fmt.Sprintf("Agent: %s\n", result.AgentId))
	sb.WriteString(fmt.Sprintf("Run At: %s\n", result.RunAt.AsTime().Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("Status: %s\n", map[bool]string{true: "✓ PASSED", false: "✗ FAILED"}[result.Passed]))
	sb.WriteString("\n")

	// Overall metrics
	sb.WriteString("Overall Metrics:\n")
	sb.WriteString(fmt.Sprintf("  Tests: %d total, %d passed, %d failed\n",
		result.Overall.TotalTests,
		result.Overall.PassedTests,
		result.Overall.FailedTests))
	sb.WriteString(fmt.Sprintf("  Accuracy: %.2f%%\n", result.Overall.Accuracy*100))
	sb.WriteString(fmt.Sprintf("  Total Cost: $%.4f\n", result.Overall.TotalCostUsd))
	sb.WriteString(fmt.Sprintf("  Total Latency: %dms\n", result.Overall.TotalLatencyMs))
	sb.WriteString(fmt.Sprintf("  Tool Calls: %d\n", result.Overall.TotalToolCalls))

	// Custom metrics
	if len(result.Overall.CustomMetrics) > 0 {
		sb.WriteString("\n  Custom Metrics:\n")
		for name, value := range result.Overall.CustomMetrics {
			sb.WriteString(fmt.Sprintf("    %s: %.2f\n", name, value))
		}
	}

	// Test results summary
	sb.WriteString("\nTest Results:\n")
	for i, testResult := range result.TestResults {
		status := map[bool]string{true: "✓", false: "✗"}[testResult.Passed]
		sb.WriteString(fmt.Sprintf("  %s %s (%.2fs, $%.4f)\n",
			status,
			testResult.TestName,
			float64(testResult.LatencyMs)/1000.0,
			testResult.CostUsd))

		if !testResult.Passed {
			sb.WriteString(fmt.Sprintf("    Reason: %s\n", testResult.FailureReason))
		}

		// Only show details for first 5 tests to avoid clutter
		if i >= 4 && len(result.TestResults) > 5 {
			sb.WriteString(fmt.Sprintf("  ... and %d more tests\n", len(result.TestResults)-5))
			break
		}
	}

	return sb.String()
}

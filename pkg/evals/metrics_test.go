// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package evals

import (
	"testing"

	"github.com/stretchr/testify/assert"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestMetricsCalculator_Calculate(t *testing.T) {
	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{Name: "test-suite"},
		Spec: &loomv1.EvalSpec{
			Metrics: []string{"accuracy", "cost_efficiency", "latency", "tool_usage"},
		},
	}

	calc := NewMetricsCalculator(suite)

	results := []*loomv1.TestCaseResult{
		{
			TestName:  "test1",
			Passed:    true,
			CostUsd:   0.10,
			LatencyMs: 1000,
			ToolsUsed: []string{"tool1", "tool2"},
		},
		{
			TestName:  "test2",
			Passed:    true,
			CostUsd:   0.15,
			LatencyMs: 1500,
			ToolsUsed: []string{"tool1"},
		},
		{
			TestName:  "test3",
			Passed:    false,
			CostUsd:   0.20,
			LatencyMs: 2000,
			ToolsUsed: []string{"tool1", "tool2", "tool3"},
		},
	}

	metrics := calc.Calculate(results)

	assert.Equal(t, int32(3), metrics.TotalTests)
	assert.Equal(t, int32(2), metrics.PassedTests)
	assert.Equal(t, int32(1), metrics.FailedTests)
	assert.InDelta(t, 0.667, metrics.Accuracy, 0.01)
	assert.InDelta(t, 0.45, metrics.TotalCostUsd, 0.01)
	assert.Equal(t, int64(4500), metrics.TotalLatencyMs)
	assert.Equal(t, int32(6), metrics.TotalToolCalls)

	// Check custom metrics
	assert.Contains(t, metrics.CustomMetrics, "cost_efficiency")
	assert.Contains(t, metrics.CustomMetrics, "avg_latency_ms")
	assert.Contains(t, metrics.CustomMetrics, "avg_tools_per_test")

	assert.InDelta(t, 1500.0, metrics.CustomMetrics["avg_latency_ms"], 1.0)
	assert.InDelta(t, 2.0, metrics.CustomMetrics["avg_tools_per_test"], 0.1)
}

func TestMetricsCalculator_Calculate_EmptyResults(t *testing.T) {
	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{Name: "test-suite"},
		Spec:     &loomv1.EvalSpec{},
	}

	calc := NewMetricsCalculator(suite)
	metrics := calc.Calculate([]*loomv1.TestCaseResult{})

	assert.Equal(t, int32(0), metrics.TotalTests)
	assert.Equal(t, int32(0), metrics.PassedTests)
	assert.Equal(t, int32(0), metrics.FailedTests)
	assert.Equal(t, 0.0, metrics.Accuracy)
	assert.Equal(t, 0.0, metrics.TotalCostUsd)
	assert.Equal(t, int64(0), metrics.TotalLatencyMs)
}

func TestCalculateCostEfficiency(t *testing.T) {
	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{Name: "test-suite"},
		Spec:     &loomv1.EvalSpec{},
	}

	calc := NewMetricsCalculator(suite)

	tests := []struct {
		name     string
		results  []*loomv1.TestCaseResult
		expected float64
	}{
		{
			name: "all passed, low cost",
			results: []*loomv1.TestCaseResult{
				{Passed: true, CostUsd: 0.10},
				{Passed: true, CostUsd: 0.10},
			},
			expected: 1000.0, // (2 / 0.20) * 100
		},
		{
			name: "half passed",
			results: []*loomv1.TestCaseResult{
				{Passed: true, CostUsd: 0.10},
				{Passed: false, CostUsd: 0.10},
			},
			expected: 500.0, // (1 / 0.20) * 100
		},
		{
			name:     "no results",
			results:  []*loomv1.TestCaseResult{},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			efficiency := calc.calculateCostEfficiency(tt.results)
			assert.InDelta(t, tt.expected, efficiency, 1.0)
		})
	}
}

func TestCalculateAvgLatency(t *testing.T) {
	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{Name: "test-suite"},
		Spec:     &loomv1.EvalSpec{},
	}

	calc := NewMetricsCalculator(suite)

	results := []*loomv1.TestCaseResult{
		{LatencyMs: 1000},
		{LatencyMs: 2000},
		{LatencyMs: 3000},
	}

	avgLatency := calc.calculateAvgLatency(results)
	assert.InDelta(t, 2000.0, avgLatency, 0.1)
}

func TestCalculateAvgToolUsage(t *testing.T) {
	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{Name: "test-suite"},
		Spec:     &loomv1.EvalSpec{},
	}

	calc := NewMetricsCalculator(suite)

	results := []*loomv1.TestCaseResult{
		{ToolsUsed: []string{"tool1", "tool2"}},
		{ToolsUsed: []string{"tool1"}},
		{ToolsUsed: []string{"tool1", "tool2", "tool3"}},
	}

	avgTools := calc.calculateAvgToolUsage(results)
	assert.InDelta(t, 2.0, avgTools, 0.1)
}

func TestValidateTestResult(t *testing.T) {
	tests := []struct {
		name          string
		testCase      *loomv1.TestCase
		actualOutput  string
		toolsUsed     []string
		costUsd       float64
		latencyMs     int64
		expectPassed  bool
		expectFailure string
	}{
		{
			name: "all expectations met",
			testCase: &loomv1.TestCase{
				Name:                   "test1",
				ExpectedOutputContains: []string{"SELECT", "FROM"},
				ExpectedTools:          []string{"query_table"},
				MaxCostUsd:             0.20,
				MaxLatencyMs:           2000,
			},
			actualOutput: "SELECT * FROM users",
			toolsUsed:    []string{"query_table"},
			costUsd:      0.10,
			latencyMs:    1000,
			expectPassed: true,
		},
		{
			name: "missing expected output",
			testCase: &loomv1.TestCase{
				Name:                   "test2",
				ExpectedOutputContains: []string{"SELECT", "JOIN"},
			},
			actualOutput:  "SELECT * FROM users",
			expectPassed:  false,
			expectFailure: "does not contain",
		},
		{
			name: "contains forbidden output",
			testCase: &loomv1.TestCase{
				Name:                      "test3",
				ExpectedOutputNotContains: []string{"DROP", "DELETE"},
			},
			actualOutput:  "DROP TABLE users",
			expectPassed:  false,
			expectFailure: "should not contain",
		},
		{
			name: "missing expected tool",
			testCase: &loomv1.TestCase{
				Name:          "test4",
				ExpectedTools: []string{"query_table", "insert_row"},
			},
			toolsUsed:     []string{"query_table"},
			expectPassed:  false,
			expectFailure: "expected tool not used",
		},
		{
			name: "cost exceeds maximum",
			testCase: &loomv1.TestCase{
				Name:       "test5",
				MaxCostUsd: 0.10,
			},
			costUsd:       0.20,
			expectPassed:  false,
			expectFailure: "cost",
		},
		{
			name: "latency exceeds maximum",
			testCase: &loomv1.TestCase{
				Name:         "test6",
				MaxLatencyMs: 1000,
			},
			latencyMs:     2000,
			expectPassed:  false,
			expectFailure: "latency",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateTestResult(
				tt.testCase,
				tt.actualOutput,
				tt.toolsUsed,
				tt.costUsd,
				tt.latencyMs,
			)

			assert.Equal(t, tt.expectPassed, result.Passed)
			if !tt.expectPassed {
				assert.Contains(t, result.FailureReason, tt.expectFailure)
			}
		})
	}
}

func TestCreateEvalResult(t *testing.T) {
	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{Name: "test-suite"},
		Spec: &loomv1.EvalSpec{
			Metrics: []string{"accuracy"},
		},
	}

	calc := NewMetricsCalculator(suite)

	testResults := []*loomv1.TestCaseResult{
		{TestName: "test1", Passed: true, CostUsd: 0.10, LatencyMs: 1000},
		{TestName: "test2", Passed: true, CostUsd: 0.15, LatencyMs: 1500},
	}

	result := calc.CreateEvalResult("test-suite", "test-agent", testResults)

	assert.Equal(t, "test-suite", result.SuiteName)
	assert.Equal(t, "test-agent", result.AgentId)
	assert.True(t, result.Passed)
	assert.Equal(t, "", result.FailureReason)
	assert.NotNil(t, result.RunAt)
	assert.NotNil(t, result.Overall)
	assert.Len(t, result.TestResults, 2)
}

func TestCreateEvalResult_WithFailures(t *testing.T) {
	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{Name: "test-suite"},
		Spec:     &loomv1.EvalSpec{},
	}

	calc := NewMetricsCalculator(suite)

	testResults := []*loomv1.TestCaseResult{
		{TestName: "test1", Passed: true},
		{TestName: "test2", Passed: false, FailureReason: "output mismatch"},
	}

	result := calc.CreateEvalResult("test-suite", "test-agent", testResults)

	assert.False(t, result.Passed)
	assert.Contains(t, result.FailureReason, "1 of 2 tests failed")
}

func TestCompareEvalResults(t *testing.T) {
	baseline := &loomv1.EvalResult{
		Overall: &loomv1.EvalMetrics{
			Accuracy: 0.80,
			CustomMetrics: map[string]float64{
				"cost_efficiency":    100.0,
				"avg_latency_ms":     1500.0,
				"avg_tools_per_test": 2.0,
			},
		},
	}

	candidate := &loomv1.EvalResult{
		Overall: &loomv1.EvalMetrics{
			Accuracy: 0.90,
			CustomMetrics: map[string]float64{
				"cost_efficiency":    120.0,
				"avg_latency_ms":     1200.0,
				"avg_tools_per_test": 1.5,
			},
		},
	}

	metrics := []string{"accuracy", "cost_efficiency", "latency", "tool_usage"}
	comparison := CompareEvalResults(baseline, candidate, metrics)

	assert.InDelta(t, 0.10, comparison["accuracy_delta"], 0.01)
	assert.InDelta(t, 20.0, comparison["cost_efficiency_delta"], 0.1)
	assert.InDelta(t, -300.0, comparison["latency_delta_ms"], 0.1) // Negative is better!
	assert.InDelta(t, -0.5, comparison["tool_usage_delta"], 0.1)
}

func TestFormatEvalResult(t *testing.T) {
	result := &loomv1.EvalResult{
		SuiteName: "test-suite",
		AgentId:   "test-agent",
		Passed:    true,
		Overall: &loomv1.EvalMetrics{
			TotalTests:     10,
			PassedTests:    9,
			FailedTests:    1,
			Accuracy:       0.90,
			TotalCostUsd:   1.50,
			TotalLatencyMs: 15000,
			TotalToolCalls: 20,
			CustomMetrics: map[string]float64{
				"cost_efficiency": 600.0,
			},
		},
		TestResults: []*loomv1.TestCaseResult{
			{TestName: "test1", Passed: true, CostUsd: 0.15, LatencyMs: 1500},
			{TestName: "test2", Passed: false, CostUsd: 0.20, LatencyMs: 2000, FailureReason: "output mismatch"},
		},
	}

	output := FormatEvalResult(result)

	assert.Contains(t, output, "test-suite")
	assert.Contains(t, output, "test-agent")
	assert.Contains(t, output, "✓ PASSED")
	assert.Contains(t, output, "90.00%")
	assert.Contains(t, output, "$1.5000")
	assert.Contains(t, output, "15000ms")
	assert.Contains(t, output, "cost_efficiency")
	assert.Contains(t, output, "✓ test1")
	assert.Contains(t, output, "✗ test2")
	assert.Contains(t, output, "output mismatch")
}

// Benchmark tests
func BenchmarkMetricsCalculate(b *testing.B) {
	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{Name: "test-suite"},
		Spec: &loomv1.EvalSpec{
			Metrics: []string{"accuracy", "cost_efficiency", "latency", "tool_usage"},
		},
	}

	calc := NewMetricsCalculator(suite)

	results := make([]*loomv1.TestCaseResult, 100)
	for i := 0; i < 100; i++ {
		results[i] = &loomv1.TestCaseResult{
			TestName:  "test",
			Passed:    i%2 == 0,
			CostUsd:   0.10,
			LatencyMs: 1000,
			ToolsUsed: []string{"tool1", "tool2"},
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calc.Calculate(results)
	}
}

func BenchmarkValidateTestResult(b *testing.B) {
	tc := &loomv1.TestCase{
		Name:                   "test",
		ExpectedOutputContains: []string{"SELECT", "FROM", "WHERE"},
		ExpectedTools:          []string{"query_table"},
		MaxCostUsd:             0.20,
		MaxLatencyMs:           2000,
	}

	actualOutput := "SELECT * FROM users WHERE status = 'active'"
	toolsUsed := []string{"query_table"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ValidateTestResult(tc, actualOutput, toolsUsed, 0.10, 1000)
	}
}

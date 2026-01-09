// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build hawk

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package evals

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_FullEvalFlow tests the complete eval workflow:
// Load suite -> Run with agent -> Save results -> Verify persistence
func TestIntegration_FullEvalFlow(t *testing.T) {
	// Create temporary directory for test files
	tmpDir := t.TempDir()

	// Create a realistic eval suite YAML
	suiteYAML := `apiVersion: loom/v1
kind: EvalSuite
metadata:
  name: sql-assistant-quality
  description: Evaluates SQL generation quality
spec:
  agent_id: sql-assistant-v1
  metrics:
    - accuracy
    - cost_efficiency
    - latency
    - tool_usage
  test_cases:
    - name: simple-select
      input: "Show me all active users"
      expected_output_contains:
        - "SELECT"
        - "FROM users"
        - "WHERE status"
      expected_tools:
        - query_table
      max_cost_usd: 0.20
      max_latency_ms: 2000

    - name: join-query
      input: "Show users with their orders"
      expected_output_contains:
        - "SELECT"
        - "JOIN"
      expected_tools:
        - query_table
      max_cost_usd: 0.30
      max_latency_ms: 3000

    - name: aggregation
      input: "Count total users"
      expected_output_contains:
        - "COUNT"
        - "users"
      expected_tools:
        - query_table
      max_cost_usd: 0.15
      max_latency_ms: 1500
`

	suitePath := filepath.Join(tmpDir, "sql-quality.yaml")
	err := os.WriteFile(suitePath, []byte(suiteYAML), 0644)
	require.NoError(t, err)

	// Load the eval suite
	suite, err := LoadEvalSuite(suitePath)
	require.NoError(t, err)
	assert.Equal(t, "sql-assistant-quality", suite.Metadata.Name)
	assert.Len(t, suite.Spec.TestCases, 3)

	// Create a smart mock agent that generates appropriate SQL based on input
	agent := &smartMockAgent{}

	// Create store for persistence
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	// Create runner
	runner := NewRunner(suite, agent, store, nil)

	// Run the eval suite
	ctx := context.Background()
	result, err := runner.Run(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify result structure
	assert.Equal(t, "sql-assistant-quality", result.SuiteName)
	assert.Equal(t, "sql-assistant-v1", result.AgentId)
	assert.NotNil(t, result.RunAt)
	assert.True(t, result.Passed, "All tests should pass")

	// Verify test results
	assert.Len(t, result.TestResults, 3)
	for i, testResult := range result.TestResults {
		t.Logf("Test %d: %s - Cost: %.4f, Latency: %d", i, testResult.TestName, testResult.CostUsd, testResult.LatencyMs)
		assert.True(t, testResult.Passed, "Test %d should pass: %s", i, testResult.TestName)
		assert.NotEmpty(t, testResult.ActualOutput)
		assert.NotEmpty(t, testResult.ToolsUsed)
		assert.Greater(t, testResult.CostUsd, 0.0, "Test %d should have cost > 0", i)
		assert.Greater(t, testResult.LatencyMs, int64(0), "Test %d should have latency > 0", i)
		assert.Equal(t, "integration-test-trace", testResult.TraceId)
	}

	// Verify metrics
	assert.Equal(t, int32(3), result.Overall.TotalTests)
	assert.Equal(t, int32(3), result.Overall.PassedTests)
	assert.Equal(t, int32(0), result.Overall.FailedTests)
	assert.Equal(t, 1.0, result.Overall.Accuracy)
	assert.Greater(t, result.Overall.TotalCostUsd, 0.0)
	assert.Greater(t, result.Overall.TotalLatencyMs, int64(0))
	assert.Equal(t, int32(3), result.Overall.TotalToolCalls) // 1 tool per test

	// Verify custom metrics were calculated
	assert.Contains(t, result.Overall.CustomMetrics, "cost_efficiency")
	assert.Contains(t, result.Overall.CustomMetrics, "avg_latency_ms")
	assert.Contains(t, result.Overall.CustomMetrics, "avg_tools_per_test")

	// Verify persistence - result should be in store
	results, err := store.ListBySuite(ctx, "sql-assistant-quality", 10)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, result.SuiteName, results[0].SuiteName)
	assert.Equal(t, result.AgentId, results[0].AgentId)

	// Verify we can retrieve it by ID
	latest, err := store.GetLatest(ctx, "sql-assistant-quality")
	require.NoError(t, err)
	assert.Equal(t, result.SuiteName, latest.SuiteName)

	// Verify summary statistics
	summary, err := store.GetSummary(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.TotalRuns)
	assert.Equal(t, 1, summary.PassedRuns)
	assert.Equal(t, 1.0, summary.AvgAccuracy)
	assert.Greater(t, summary.TotalCost, 0.0)
}

// TestIntegration_WithGoldenFiles tests the eval flow with golden file comparison
func TestIntegration_WithGoldenFiles(t *testing.T) {
	// Create temporary directory
	tmpDir := t.TempDir()
	goldenDir := filepath.Join(tmpDir, "golden")
	err := os.MkdirAll(goldenDir, 0755)
	require.NoError(t, err)

	// Create golden files
	goldenSQL := "SELECT id, name, email FROM users WHERE status = 'active' ORDER BY created_at DESC"
	goldenPath := filepath.Join(goldenDir, "active-users.sql")
	err = os.WriteFile(goldenPath, []byte(goldenSQL), 0644)
	require.NoError(t, err)

	// Create eval suite with golden file
	suiteYAML := `apiVersion: loom/v1
kind: EvalSuite
metadata:
  name: sql-golden-test
spec:
  agent_id: sql-agent
  golden_files:
    directory: ` + goldenDir + `
    similarity_threshold: 0.90
  test_cases:
    - name: active-users-query
      input: "Get active users sorted by creation date"
      golden_file: ` + goldenPath + `
      expected_tools:
        - query_table
`

	suitePath := filepath.Join(tmpDir, "golden-suite.yaml")
	err = os.WriteFile(suitePath, []byte(suiteYAML), 0644)
	require.NoError(t, err)

	// Load suite
	suite, err := LoadEvalSuite(suitePath)
	require.NoError(t, err)

	// Create agent that returns similar but not identical SQL
	agent := NewMockAgent(
		"SELECT id, name, email FROM users WHERE status='active' ORDER BY created_at DESC", // Slightly different whitespace
		[]string{"query_table"},
	)

	// Create runner
	runner := NewRunner(suite, agent, nil, nil)

	// Run eval
	ctx := context.Background()
	result, err := runner.Run(ctx)
	require.NoError(t, err)

	// Verify golden file comparison passed
	assert.True(t, result.Passed)
	assert.Len(t, result.TestResults, 1)
	testResult := result.TestResults[0]
	assert.True(t, testResult.Passed)
	assert.NotNil(t, testResult.GoldenResult)
	assert.True(t, testResult.GoldenResult.Matched)
	assert.Greater(t, testResult.GoldenResult.SimilarityScore, 0.90)
	t.Logf("Golden file similarity: %.2f%%", testResult.GoldenResult.SimilarityScore*100)
}

// TestIntegration_FailureScenarios tests various failure scenarios
func TestIntegration_FailureScenarios(t *testing.T) {
	tmpDir := t.TempDir()

	suiteYAML := `apiVersion: loom/v1
kind: EvalSuite
metadata:
  name: failure-test
spec:
  agent_id: test-agent
  test_cases:
    - name: missing-expected-output
      input: "test input"
      expected_output_contains:
        - "MISSING_TEXT"

    - name: wrong-tool
      input: "test input"
      expected_tools:
        - wrong_tool

    - name: cost-exceeded
      input: "test input"
      max_cost_usd: 0.01  # Agent costs 0.10, so this will fail
`

	suitePath := filepath.Join(tmpDir, "failure-suite.yaml")
	err := os.WriteFile(suitePath, []byte(suiteYAML), 0644)
	require.NoError(t, err)

	suite, err := LoadEvalSuite(suitePath)
	require.NoError(t, err)

	// Create agent
	agent := NewMockAgent("test output", []string{"test_tool"})

	// Create store
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	// Run eval
	runner := NewRunner(suite, agent, store, nil)
	ctx := context.Background()
	result, err := runner.Run(ctx)
	require.NoError(t, err)

	// Verify all tests failed
	assert.False(t, result.Passed, "Suite should fail because tests failed")
	assert.Equal(t, int32(3), result.Overall.TotalTests)
	assert.Equal(t, int32(0), result.Overall.PassedTests)
	assert.Equal(t, int32(3), result.Overall.FailedTests)
	assert.Equal(t, 0.0, result.Overall.Accuracy)

	// Verify each test has a failure reason
	for _, testResult := range result.TestResults {
		assert.False(t, testResult.Passed)
		assert.NotEmpty(t, testResult.FailureReason)
		t.Logf("Test %s failed: %s", testResult.TestName, testResult.FailureReason)
	}

	// Verify result was still saved to store (even with failures)
	results, err := store.ListBySuite(ctx, "failure-test", 10)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.False(t, results[0].Passed)
}

// smartMockAgent generates appropriate responses based on input
type smartMockAgent struct{}

func (s *smartMockAgent) Execute(ctx context.Context, input string) (*AgentResponse, error) {
	var output string
	var tools []string

	// Generate SQL based on input
	switch {
	case contains(input, "all active users"):
		output = "SELECT * FROM users WHERE status = 'active'"
		tools = []string{"query_table"}
	case contains(input, "users with their orders"):
		output = "SELECT u.*, o.* FROM users u JOIN orders o ON u.id = o.user_id"
		tools = []string{"query_table"}
	case contains(input, "Count total users"):
		output = "SELECT COUNT(*) FROM users"
		tools = []string{"query_table"}
	default:
		output = "SELECT * FROM users"
		tools = []string{"query_table"}
	}

	return &AgentResponse{
		Output:     output,
		ToolsUsed:  tools,
		CostUsd:    0.10,
		LatencyMs:  800,
		TraceID:    "integration-test-trace",
		Successful: true,
	}, nil
}

// Helper function for case-insensitive contains check
func contains(str, substr string) bool {
	return len(str) >= len(substr) &&
		(str == substr ||
			len(str) > 0 && (str[0:len(substr)] == substr || contains(str[1:], substr)))
}

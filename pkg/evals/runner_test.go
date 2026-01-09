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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestNewRunner(t *testing.T) {
	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{Name: "test-suite"},
		Spec: &loomv1.EvalSpec{
			AgentId:   "test-agent",
			TestCases: []*loomv1.TestCase{},
		},
	}

	agent := NewMockAgent("test output", []string{"tool1"})
	runner := NewRunner(suite, agent, nil, nil) // nil store, nil judge orchestrator

	assert.NotNil(t, runner)
	assert.Equal(t, suite, runner.suite)
	assert.Equal(t, agent, runner.agent)
	assert.NotNil(t, runner.calculator)
}

func TestRunner_Run_Success(t *testing.T) {
	// Create a simple eval suite
	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{Name: "test-suite"},
		Spec: &loomv1.EvalSpec{
			AgentId: "test-agent",
			TestCases: []*loomv1.TestCase{
				{
					Name:                   "test1",
					Input:                  "SELECT * FROM users",
					ExpectedOutputContains: []string{"SELECT"},
					ExpectedTools:          []string{"query_table"},
				},
				{
					Name:                   "test2",
					Input:                  "COUNT users",
					ExpectedOutputContains: []string{"COUNT"},
					ExpectedTools:          []string{"query_table"},
				},
			},
		},
	}

	// Create mock agent
	agent := NewMockAgent("SELECT * FROM users WHERE id = 1", []string{"query_table"})

	// Create runner without store
	runner := NewRunner(suite, agent, nil, nil)

	// Run eval
	ctx := context.Background()
	result, err := runner.Run(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify result
	assert.Equal(t, "test-suite", result.SuiteName)
	assert.Equal(t, "test-agent", result.AgentId)
	assert.Len(t, result.TestResults, 2)
	assert.NotNil(t, result.Overall)
}

func TestRunner_Run_WithStore(t *testing.T) {
	// Create eval suite
	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{Name: "test-suite"},
		Spec: &loomv1.EvalSpec{
			AgentId: "test-agent",
			TestCases: []*loomv1.TestCase{
				{
					Name:                   "test1",
					Input:                  "test input",
					ExpectedOutputContains: []string{"output"},
				},
			},
		},
	}

	// Create mock agent
	agent := NewMockAgent("test output", []string{"tool1"})

	// Create store
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	// Create runner with store
	runner := NewRunner(suite, agent, store, nil)

	// Run eval
	ctx := context.Background()
	result, err := runner.Run(ctx)
	require.NoError(t, err)

	// Verify result was saved
	results, err := store.ListBySuite(ctx, "test-suite", 10)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, result.SuiteName, results[0].SuiteName)
}

func TestRunner_Run_AgentError(t *testing.T) {
	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{Name: "test-suite"},
		Spec: &loomv1.EvalSpec{
			AgentId: "test-agent",
			TestCases: []*loomv1.TestCase{
				{
					Name:  "test1",
					Input: "test input",
				},
			},
		},
	}

	// Create mock agent that returns error
	agent := &MockAgent{
		Error: errors.New("agent execution failed"),
	}

	runner := NewRunner(suite, agent, nil, nil)

	// Run eval
	ctx := context.Background()
	result, err := runner.Run(ctx)
	require.NoError(t, err) // Runner should not fail, but test should be marked as failed

	// Verify test result shows failure
	assert.Len(t, result.TestResults, 1)
	assert.False(t, result.TestResults[0].Passed)
	assert.Contains(t, result.TestResults[0].FailureReason, "agent execution failed")
}

func TestRunner_Run_AgentInternalFailure(t *testing.T) {
	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{Name: "test-suite"},
		Spec: &loomv1.EvalSpec{
			AgentId: "test-agent",
			TestCases: []*loomv1.TestCase{
				{
					Name:  "test1",
					Input: "test input",
				},
			},
		},
	}

	// Create mock agent with internal failure
	agent := &MockAgent{
		Response: &AgentResponse{
			Output:     "partial output",
			ToolsUsed:  []string{"tool1"},
			CostUsd:    0.05,
			LatencyMs:  500,
			TraceID:    "trace-123",
			Successful: false,
			Error:      "internal error: database connection failed",
		},
	}

	runner := NewRunner(suite, agent, nil, nil)

	// Run eval
	ctx := context.Background()
	result, err := runner.Run(ctx)
	require.NoError(t, err)

	// Verify test result shows internal failure
	assert.Len(t, result.TestResults, 1)
	assert.False(t, result.TestResults[0].Passed)
	assert.Contains(t, result.TestResults[0].FailureReason, "internal error")
	assert.Equal(t, "partial output", result.TestResults[0].ActualOutput)
	assert.Equal(t, "trace-123", result.TestResults[0].TraceId)
}

func TestRunner_runTestCase_ValidateExpectations(t *testing.T) {
	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{Name: "test-suite"},
		Spec: &loomv1.EvalSpec{
			AgentId:   "test-agent",
			TestCases: []*loomv1.TestCase{},
		},
	}

	tests := []struct {
		name          string
		testCase      *loomv1.TestCase
		agentOutput   string
		agentTools    []string
		expectPassed  bool
		expectFailure string
	}{
		{
			name: "all expectations met",
			testCase: &loomv1.TestCase{
				Name:                   "test1",
				Input:                  "query users",
				ExpectedOutputContains: []string{"SELECT", "FROM"},
				ExpectedTools:          []string{"query_table"},
				MaxCostUsd:             0.20,
				MaxLatencyMs:           2000,
			},
			agentOutput:  "SELECT * FROM users",
			agentTools:   []string{"query_table"},
			expectPassed: true,
		},
		{
			name: "missing expected output",
			testCase: &loomv1.TestCase{
				Name:                   "test2",
				Input:                  "query users",
				ExpectedOutputContains: []string{"JOIN"},
			},
			agentOutput:   "SELECT * FROM users",
			agentTools:    []string{"query_table"},
			expectPassed:  false,
			expectFailure: "does not contain",
		},
		{
			name: "missing expected tool",
			testCase: &loomv1.TestCase{
				Name:          "test3",
				Input:         "insert data",
				ExpectedTools: []string{"insert_row"},
			},
			agentOutput:   "INSERT INTO users VALUES (1, 'test')",
			agentTools:    []string{"query_table"},
			expectPassed:  false,
			expectFailure: "expected tool not used",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := NewMockAgent(tt.agentOutput, tt.agentTools)
			runner := NewRunner(suite, agent, nil, nil)

			ctx := context.Background()
			result, err := runner.runTestCase(ctx, tt.testCase)
			require.NoError(t, err)

			assert.Equal(t, tt.expectPassed, result.Passed)
			if !tt.expectPassed {
				assert.Contains(t, result.FailureReason, tt.expectFailure)
			}
		})
	}
}

func TestRunner_runTestCase_GoldenFile(t *testing.T) {
	// Create temporary golden file
	tmpDir := t.TempDir()
	goldenPath := filepath.Join(tmpDir, "golden.txt")
	err := os.WriteFile(goldenPath, []byte("SELECT * FROM users WHERE status = 'active'"), 0644)
	require.NoError(t, err)

	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{Name: "test-suite"},
		Spec: &loomv1.EvalSpec{
			AgentId: "test-agent",
			GoldenFiles: &loomv1.GoldenFileConfig{
				SimilarityThreshold: 0.90,
			},
		},
	}

	tests := []struct {
		name          string
		agentOutput   string
		expectPassed  bool
		expectFailure string
	}{
		{
			name:         "exact match",
			agentOutput:  "SELECT * FROM users WHERE status = 'active'",
			expectPassed: true,
		},
		{
			name:         "similar (should pass)",
			agentOutput:  "SELECT * FROM users WHERE status='active'", // Minor whitespace difference
			expectPassed: true,
		},
		{
			name:          "different (should fail)",
			agentOutput:   "SELECT * FROM orders",
			expectPassed:  false,
			expectFailure: "golden file mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testCase := &loomv1.TestCase{
				Name:       "test1",
				Input:      "query active users",
				GoldenFile: goldenPath,
			}

			agent := NewMockAgent(tt.agentOutput, []string{"query_table"})
			runner := NewRunner(suite, agent, nil, nil)

			ctx := context.Background()
			result, err := runner.runTestCase(ctx, testCase)
			require.NoError(t, err)

			assert.Equal(t, tt.expectPassed, result.Passed, "Test: %s", tt.name)
			if !tt.expectPassed {
				assert.Contains(t, result.FailureReason, tt.expectFailure)
			}
			if result.GoldenResult != nil {
				t.Logf("Similarity: %.2f%%", result.GoldenResult.SimilarityScore*100)
			}
		})
	}
}

func TestRunner_getGoldenThreshold(t *testing.T) {
	tests := []struct {
		name              string
		suiteThreshold    float64
		testCaseThreshold float64
		expected          float64
	}{
		{
			name:              "test case specific takes precedence",
			suiteThreshold:    0.90,
			testCaseThreshold: 0.95,
			expected:          0.95,
		},
		{
			name:              "suite level if no test case specific",
			suiteThreshold:    0.85,
			testCaseThreshold: 0,
			expected:          0.85,
		},
		{
			name:              "default if neither specified",
			suiteThreshold:    0,
			testCaseThreshold: 0,
			expected:          0.90,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			suite := &loomv1.EvalSuite{
				Metadata: &loomv1.EvalMetadata{Name: "test-suite"},
				Spec: &loomv1.EvalSpec{
					AgentId: "test-agent",
					GoldenFiles: &loomv1.GoldenFileConfig{
						SimilarityThreshold: tt.suiteThreshold,
					},
				},
			}

			testCase := &loomv1.TestCase{
				Name:                      "test1",
				Input:                     "test",
				GoldenSimilarityThreshold: tt.testCaseThreshold,
			}

			runner := NewRunner(suite, nil, nil, nil)
			threshold := runner.getGoldenThreshold(testCase)

			assert.Equal(t, tt.expected, threshold)
		})
	}
}

func TestMockAgent(t *testing.T) {
	agent := NewMockAgent("test output", []string{"tool1", "tool2"})

	ctx := context.Background()
	response, err := agent.Execute(ctx, "test input")
	require.NoError(t, err)

	assert.Equal(t, "test output", response.Output)
	assert.Equal(t, []string{"tool1", "tool2"}, response.ToolsUsed)
	assert.Equal(t, 0.10, response.CostUsd)
	assert.Equal(t, int64(1000), response.LatencyMs)
	assert.Equal(t, "mock-trace-123", response.TraceID)
	assert.True(t, response.Successful)
}

func TestMockAgent_Error(t *testing.T) {
	agent := &MockAgent{
		Error: errors.New("mock error"),
	}

	ctx := context.Background()
	_, err := agent.Execute(ctx, "test input")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mock error")
}

// Benchmark tests
func BenchmarkRunner_Run(b *testing.B) {
	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{Name: "bench-suite"},
		Spec: &loomv1.EvalSpec{
			AgentId: "bench-agent",
			TestCases: []*loomv1.TestCase{
				{
					Name:                   "test1",
					Input:                  "query users",
					ExpectedOutputContains: []string{"SELECT"},
				},
				{
					Name:                   "test2",
					Input:                  "count users",
					ExpectedOutputContains: []string{"COUNT"},
				},
			},
		},
	}

	agent := NewMockAgent("SELECT * FROM users", []string{"query_table"})
	runner := NewRunner(suite, agent, nil, nil)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = runner.Run(ctx)
	}
}

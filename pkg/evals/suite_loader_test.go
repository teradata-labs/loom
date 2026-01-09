// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package evals

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadEvalSuite(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantErr  bool
		errMsg   string
		validate func(t *testing.T, suite interface{})
	}{
		{
			name: "minimal valid eval suite",
			yaml: `apiVersion: loom/v1
kind: EvalSuite
metadata:
  name: test-suite
spec:
  agent_id: test_agent
  test_cases:
    - name: test1
      input: "test input"
`,
			wantErr: false,
		},
		{
			name: "full eval suite with all fields",
			yaml: `apiVersion: loom/v1
kind: EvalSuite
metadata:
  name: comprehensive-test
  version: 1.0.0
  description: Comprehensive test suite
  labels:
    category: quality
    priority: high
spec:
  agent_id: sql_expert
  test_cases:
    - name: simple-query
      input: "SELECT * FROM users"
      expected_output_contains:
        - "SELECT"
        - "users"
      expected_output_not_contains:
        - "DROP"
      expected_output_regex: "SELECT.*FROM"
      expected_tools:
        - query_table
      max_cost_usd: 0.10
      max_latency_ms: 2000
      context:
        database: test_db
      golden_file: simple-query.sql
  metrics:
    - accuracy
    - cost_efficiency
    - latency
  hawk_export: true
  golden_files:
    directory: ./golden
    update_on_mismatch: false
    similarity_threshold: 0.85
  timeout_seconds: 600
  comparison:
    baseline_agent_id: sql_expert_v1
    comparison_metrics:
      - accuracy
      - cost_efficiency
`,
			wantErr: false,
		},
		{
			name: "missing apiVersion",
			yaml: `kind: EvalSuite
metadata:
  name: test
spec:
  agent_id: test
  test_cases:
    - name: test1
      input: "test"
`,
			wantErr: true,
			errMsg:  "apiVersion is required",
		},
		{
			name: "wrong kind",
			yaml: `apiVersion: loom/v1
kind: Project
metadata:
  name: test
spec:
  agent_id: test
  test_cases:
    - name: test1
      input: "test"
`,
			wantErr: true,
			errMsg:  "kind must be 'EvalSuite'",
		},
		{
			name: "missing agent_id",
			yaml: `apiVersion: loom/v1
kind: EvalSuite
metadata:
  name: test
spec:
  test_cases:
    - name: test1
      input: "test"
`,
			wantErr: true,
			errMsg:  "agent_id is required",
		},
		{
			name: "no test cases",
			yaml: `apiVersion: loom/v1
kind: EvalSuite
metadata:
  name: test
spec:
  agent_id: test
  test_cases: []
`,
			wantErr: true,
			errMsg:  "at least one test case",
		},
		{
			name: "test case missing name",
			yaml: `apiVersion: loom/v1
kind: EvalSuite
metadata:
  name: test
spec:
  agent_id: test
  test_cases:
    - input: "test"
`,
			wantErr: true,
			errMsg:  "name is required",
		},
		{
			name: "negative max cost",
			yaml: `apiVersion: loom/v1
kind: EvalSuite
metadata:
  name: test
spec:
  agent_id: test
  test_cases:
    - name: test1
      input: "test"
      max_cost_usd: -0.10
`,
			wantErr: true,
			errMsg:  "must be non-negative",
		},
		{
			name: "invalid similarity threshold",
			yaml: `apiVersion: loom/v1
kind: EvalSuite
metadata:
  name: test
spec:
  agent_id: test
  test_cases:
    - name: test1
      input: "test"
  golden_files:
    similarity_threshold: 1.5
`,
			wantErr: true,
			errMsg:  "must be between 0 and 1",
		},
		{
			name: "env var expansion",
			yaml: `apiVersion: loom/v1
kind: EvalSuite
metadata:
  name: test
spec:
  agent_id: ${TEST_AGENT_ID}
  test_cases:
    - name: test1
      input: "test"
`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file
			tmpDir := t.TempDir()
			suiteFile := filepath.Join(tmpDir, "eval.yaml")

			// Create golden file directory if needed
			goldenDir := filepath.Join(tmpDir, "golden")
			_ = os.MkdirAll(goldenDir, 0755)
			_ = os.WriteFile(filepath.Join(goldenDir, "simple-query.sql"), []byte("SELECT * FROM users;"), 0644)

			// Set env var for test
			os.Setenv("TEST_AGENT_ID", "test_agent")
			defer os.Unsetenv("TEST_AGENT_ID")

			// Write suite file
			err := os.WriteFile(suiteFile, []byte(tt.yaml), 0644)
			require.NoError(t, err)

			// Load suite
			suite, err := LoadEvalSuite(suiteFile)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, suite)

			// Basic validations
			assert.NotNil(t, suite.Metadata)
			assert.NotNil(t, suite.Spec)
			assert.NotEmpty(t, suite.Spec.TestCases)

			if tt.validate != nil {
				tt.validate(t, suite)
			}
		})
	}
}

func TestValidateEvalSuite(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid suite",
			yaml: `apiVersion: loom/v1
kind: EvalSuite
metadata:
  name: test
spec:
  agent_id: test
  test_cases:
    - name: test1
      input: "test"
`,
			wantErr: false,
		},
		{
			name: "invalid metric",
			yaml: `apiVersion: loom/v1
kind: EvalSuite
metadata:
  name: test
spec:
  agent_id: test
  metrics:
    - invalid_metric
  test_cases:
    - name: test1
      input: "test"
`,
			wantErr: true,
			errMsg:  "invalid metric",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			suiteFile := filepath.Join(tmpDir, "eval.yaml")
			err := os.WriteFile(suiteFile, []byte(tt.yaml), 0644)
			require.NoError(t, err)

			suite, err := LoadEvalSuite(suiteFile)
			require.NoError(t, err)

			err = ValidateEvalSuite(suite)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestResolveEvalFilePaths(t *testing.T) {
	tmpDir := t.TempDir()

	yaml := `apiVersion: loom/v1
kind: EvalSuite
metadata:
  name: test
spec:
  agent_id: test
  test_cases:
    - name: test1
      input: "test"
      golden_file: query1.sql
  golden_files:
    directory: ./golden
`

	suiteFile := filepath.Join(tmpDir, "eval.yaml")
	goldenDir := filepath.Join(tmpDir, "golden")
	_ = os.MkdirAll(goldenDir, 0755)
	_ = os.WriteFile(filepath.Join(goldenDir, "query1.sql"), []byte("SELECT 1;"), 0644)

	err := os.WriteFile(suiteFile, []byte(yaml), 0644)
	require.NoError(t, err)

	suite, err := LoadEvalSuite(suiteFile)
	require.NoError(t, err)

	// Check that golden file directory was resolved to absolute path
	assert.True(t, filepath.IsAbs(suite.Spec.GoldenFiles.Directory))
	assert.Contains(t, suite.Spec.GoldenFiles.Directory, "golden")

	// Check that golden file path was resolved
	assert.True(t, filepath.IsAbs(suite.Spec.TestCases[0].GoldenFile))
	assert.Contains(t, suite.Spec.TestCases[0].GoldenFile, "query1.sql")
}

func TestDefaultValues(t *testing.T) {
	yaml := `apiVersion: loom/v1
kind: EvalSuite
metadata:
  name: test
spec:
  agent_id: test
  test_cases:
    - name: test1
      input: "test"
`

	tmpDir := t.TempDir()
	suiteFile := filepath.Join(tmpDir, "eval.yaml")
	err := os.WriteFile(suiteFile, []byte(yaml), 0644)
	require.NoError(t, err)

	suite, err := LoadEvalSuite(suiteFile)
	require.NoError(t, err)

	// Check defaults
	assert.Equal(t, int32(600), suite.Spec.TimeoutSeconds, "default timeout should be 600 seconds")
	assert.Contains(t, suite.Spec.Metrics, "accuracy", "default metrics should include accuracy")
	assert.Contains(t, suite.Spec.Metrics, "cost_efficiency", "default metrics should include cost_efficiency")
	assert.Contains(t, suite.Spec.Metrics, "latency", "default metrics should include latency")
	assert.Equal(t, 0.9, suite.Spec.GoldenFiles.SimilarityThreshold, "default similarity threshold should be 0.9")
}

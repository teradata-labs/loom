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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdatePatternPriority(t *testing.T) {
	// Create a temporary directory for test files
	tmpDir, err := os.MkdirTemp("", "pattern-tuning-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create a test YAML file
	yamlContent := `apiVersion: loom/v1
kind: PatternLibrary

metadata:
  name: test-patterns
  version: 1.0.0
  domain: sql
  description: Test patterns for tuning

spec:
  entries:
    - name: pattern-1
      description: First test pattern
      priority: 50
      trigger_conditions:
        - "test condition"
      template: "SELECT * FROM test"
      tags:
        - test

    - name: pattern-2
      description: Second test pattern
      priority: 30
      trigger_conditions:
        - "another condition"
      template: "SELECT * FROM test2"
      tags:
        - test

    - name: pattern-3
      description: Third test pattern
      priority: 70
      trigger_conditions:
        - "third condition"
      template: "SELECT * FROM test3"
      tags:
        - test
`

	yamlPath := filepath.Join(tmpDir, "test-patterns.yaml")
	err = os.WriteFile(yamlPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	// Test updating pattern-2's priority
	err = UpdatePatternPriority(yamlPath, "pattern-2", 80)
	require.NoError(t, err)

	// Verify the update
	priority, err := GetCurrentPriority(yamlPath, "pattern-2")
	require.NoError(t, err)
	assert.Equal(t, int32(80), priority, "Priority should be updated to 80")

	// Verify other patterns are unchanged
	priority1, err := GetCurrentPriority(yamlPath, "pattern-1")
	require.NoError(t, err)
	assert.Equal(t, int32(50), priority1, "pattern-1 should remain at 50")

	priority3, err := GetCurrentPriority(yamlPath, "pattern-3")
	require.NoError(t, err)
	assert.Equal(t, int32(70), priority3, "pattern-3 should remain at 70")
}

func TestUpdatePatternPriority_NotFound(t *testing.T) {
	// Create a temporary directory for test files
	tmpDir, err := os.MkdirTemp("", "pattern-tuning-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create a test YAML file
	yamlContent := `apiVersion: loom/v1
kind: PatternLibrary

metadata:
  name: test-patterns
  version: 1.0.0
  domain: sql

spec:
  entries:
    - name: pattern-1
      description: Test pattern
      priority: 50
      trigger_conditions:
        - "test"
      template: "SELECT * FROM test"
`

	yamlPath := filepath.Join(tmpDir, "test-patterns.yaml")
	err = os.WriteFile(yamlPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	// Try to update non-existent pattern
	err = UpdatePatternPriority(yamlPath, "non-existent-pattern", 80)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pattern 'non-existent-pattern' not found")
}

func TestFindPatternYAMLFile(t *testing.T) {
	// Create a temporary directory with multiple YAML files
	tmpDir, err := os.MkdirTemp("", "pattern-tuning-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create test YAML files
	yamlContent1 := `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: patterns-1
  domain: sql
spec:
  entries:
    - name: sql-optimization
      description: Optimize SQL queries
      priority: 50
      trigger_conditions:
        - "slow query"
      template: "..."
`

	yamlContent2 := `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: patterns-2
  domain: rest
spec:
  entries:
    - name: rest-api
      description: REST API pattern
      priority: 60
      trigger_conditions:
        - "API call"
      template: "..."
`

	err = os.WriteFile(filepath.Join(tmpDir, "sql-patterns.yaml"), []byte(yamlContent1), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, "rest-patterns.yaml"), []byte(yamlContent2), 0644)
	require.NoError(t, err)

	// Test finding pattern in sql-patterns.yaml
	foundPath, err := FindPatternYAMLFile(tmpDir, "sql-optimization")
	require.NoError(t, err)
	assert.Contains(t, foundPath, "sql-patterns.yaml")

	// Test finding pattern in rest-patterns.yaml
	foundPath, err = FindPatternYAMLFile(tmpDir, "rest-api")
	require.NoError(t, err)
	assert.Contains(t, foundPath, "rest-patterns.yaml")

	// Test pattern not found
	_, err = FindPatternYAMLFile(tmpDir, "non-existent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestFindPatternYAMLFile_SingleFile(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "pattern-tuning-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create a single YAML file
	yamlContent := `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: test
  domain: sql
spec:
  entries:
    - name: test-pattern
      description: Test
      priority: 50
      trigger_conditions:
        - "test"
      template: "..."
`

	yamlPath := filepath.Join(tmpDir, "patterns.yaml")
	err = os.WriteFile(yamlPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	// Test with file path directly
	foundPath, err := FindPatternYAMLFile(yamlPath, "test-pattern")
	require.NoError(t, err)
	assert.Equal(t, yamlPath, foundPath)

	// Test with non-existent pattern
	_, err = FindPatternYAMLFile(yamlPath, "non-existent")
	require.Error(t, err)
}

func TestGetCurrentPriority(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "pattern-tuning-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create a test YAML file
	yamlContent := `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: test
  domain: sql
spec:
  entries:
    - name: high-priority
      description: High priority pattern
      priority: 90
      trigger_conditions:
        - "test"
      template: "..."
    - name: low-priority
      description: Low priority pattern
      priority: 10
      trigger_conditions:
        - "test"
      template: "..."
`

	yamlPath := filepath.Join(tmpDir, "patterns.yaml")
	err = os.WriteFile(yamlPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	// Test reading priorities
	priority, err := GetCurrentPriority(yamlPath, "high-priority")
	require.NoError(t, err)
	assert.Equal(t, int32(90), priority)

	priority, err = GetCurrentPriority(yamlPath, "low-priority")
	require.NoError(t, err)
	assert.Equal(t, int32(10), priority)

	// Test non-existent pattern
	_, err = GetCurrentPriority(yamlPath, "non-existent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestUpdatePatternPriority_PreservesFormat(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "pattern-tuning-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create a YAML file with comments and specific formatting
	yamlContent := `# This is a test pattern library
apiVersion: loom/v1
kind: PatternLibrary

metadata:
  name: test-patterns
  version: 1.0.0
  domain: sql # SQL patterns
  description: Test patterns with comments

spec:
  entries:
    # This is the first pattern
    - name: pattern-1
      description: First test pattern
      priority: 50 # Default priority
      trigger_conditions:
        - "test condition"
      template: "SELECT * FROM test"
      tags:
        - test
        - sql
`

	yamlPath := filepath.Join(tmpDir, "test-patterns.yaml")
	err = os.WriteFile(yamlPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	// Update the priority
	err = UpdatePatternPriority(yamlPath, "pattern-1", 80)
	require.NoError(t, err)

	// Read the file and verify priority was updated
	priority, err := GetCurrentPriority(yamlPath, "pattern-1")
	require.NoError(t, err)
	assert.Equal(t, int32(80), priority)

	// Read the raw file content to verify formatting is somewhat preserved
	content, err := os.ReadFile(yamlPath)
	require.NoError(t, err)

	// Verify the file still has YAML structure (basic check)
	assert.Contains(t, string(content), "apiVersion:")
	assert.Contains(t, string(content), "pattern-1")
	assert.Contains(t, string(content), "80") // New priority value
}

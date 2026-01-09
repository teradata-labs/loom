// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package patterns

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadPatternLibrary_SQL(t *testing.T) {
	tmpDir := t.TempDir()

	yamlContent := `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: sql-optimization
  version: 1.0.0
  domain: sql
  description: Common SQL optimization patterns
  labels:
    category: performance
    difficulty: intermediate

spec:
  entries:
    - name: avoid-select-star
      description: Use explicit column lists instead of SELECT *
      trigger_conditions:
        - "SELECT *"
      template: "SELECT {{columns}} FROM {{table}}"
      priority: 80
      tags:
        - performance
        - best-practice

    - name: use-indexed-columns
      description: Filter on indexed columns for better performance
      trigger_conditions:
        - "slow query"
        - "full table scan"
      rule:
        condition: "WHERE clause on non-indexed column"
        action: "Add index or use indexed column"
        rationale: "Indexed columns allow the database to quickly locate rows without scanning the entire table"
      priority: 90
      tags:
        - performance
        - indexing

    - name: limit-result-sets
      description: Use LIMIT to restrict result sets
      trigger_conditions:
        - "large result set"
      example: |
        -- Good: Limit results
        SELECT id, name FROM users
        ORDER BY created_at DESC
        LIMIT 100;

        -- Bad: No limit
        SELECT id, name FROM users
        ORDER BY created_at DESC;
      priority: 70
      tags:
        - performance
`

	libraryPath := filepath.Join(tmpDir, "sql-patterns.yaml")
	require.NoError(t, os.WriteFile(libraryPath, []byte(yamlContent), 0644))

	// Load library
	library, err := LoadPatternLibrary(libraryPath)
	require.NoError(t, err)
	require.NotNil(t, library)

	// Verify metadata
	assert.Equal(t, "sql-optimization", library.Metadata.Name)
	assert.Equal(t, "1.0.0", library.Metadata.Version)
	assert.Equal(t, "sql", library.Metadata.Domain)
	assert.Equal(t, "Common SQL optimization patterns", library.Metadata.Description)
	assert.Equal(t, "performance", library.Metadata.Labels["category"])
	assert.Equal(t, "intermediate", library.Metadata.Labels["difficulty"])

	// Verify entries
	require.Len(t, library.Spec.Entries, 3)

	// Entry 1: Template
	entry1 := library.Spec.Entries[0]
	assert.Equal(t, "avoid-select-star", entry1.Name)
	assert.Equal(t, "Use explicit column lists instead of SELECT *", entry1.Description)
	assert.Equal(t, []string{"SELECT *"}, entry1.TriggerConditions)
	assert.Equal(t, int32(80), entry1.Priority)
	assert.Equal(t, []string{"performance", "best-practice"}, entry1.Tags)
	assert.Equal(t, "SELECT {{columns}} FROM {{table}}", entry1.GetTemplate())

	// Entry 2: Rule
	entry2 := library.Spec.Entries[1]
	assert.Equal(t, "use-indexed-columns", entry2.Name)
	assert.Equal(t, int32(90), entry2.Priority)
	rule := entry2.GetRule()
	require.NotNil(t, rule)
	assert.Equal(t, "WHERE clause on non-indexed column", rule.Condition)
	assert.Equal(t, "Add index or use indexed column", rule.Action)
	assert.Contains(t, rule.Rationale, "Indexed columns")

	// Entry 3: Example
	entry3 := library.Spec.Entries[2]
	assert.Equal(t, "limit-result-sets", entry3.Name)
	example := entry3.GetExample()
	assert.Contains(t, example, "LIMIT 100")
}

func TestLoadPatternLibrary_Teradata(t *testing.T) {
	tmpDir := t.TempDir()

	yamlContent := `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: teradata-npath
  version: 2.0.0
  domain: teradata
  description: Teradata nPath patterns for sequence analysis

spec:
  entries:
    - name: basic-sequence
      description: Find basic sequences in event data
      trigger_conditions:
        - "sequence analysis"
        - "event pattern"
      template: |
        SELECT * FROM nPath(
          ON {{input_table}} PARTITION BY {{partition_col}} ORDER BY {{time_col}}
          USING
          Mode(NONOVERLAPPING)
          Pattern('{{pattern}}')
          Symbols({{symbols}})
          Result({{result_cols}})
        ) AS dt;
      priority: 95
      tags:
        - sequence-analysis
        - npath
`

	libraryPath := filepath.Join(tmpDir, "teradata-patterns.yaml")
	require.NoError(t, os.WriteFile(libraryPath, []byte(yamlContent), 0644))

	// Load library
	library, err := LoadPatternLibrary(libraryPath)
	require.NoError(t, err)

	// Verify
	assert.Equal(t, "teradata", library.Metadata.Domain)
	assert.Equal(t, "2.0.0", library.Metadata.Version)
	require.Len(t, library.Spec.Entries, 1)
	assert.Contains(t, library.Spec.Entries[0].GetTemplate(), "nPath")
}

func TestLoadPatternLibrary_ValidationErrors(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		expectedErr string
	}{
		{
			name: "missing apiVersion",
			yaml: `kind: PatternLibrary
metadata:
  name: test`,
			expectedErr: "apiVersion is required",
		},
		{
			name: "wrong apiVersion",
			yaml: `apiVersion: loom/v2
kind: PatternLibrary
metadata:
  name: test`,
			expectedErr: "unsupported apiVersion",
		},
		{
			name: "wrong kind",
			yaml: `apiVersion: loom/v1
kind: NotPattern
metadata:
  name: test`,
			expectedErr: "kind must be 'PatternLibrary'",
		},
		{
			name: "missing name",
			yaml: `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  domain: sql`,
			expectedErr: "metadata.name is required",
		},
		{
			name: "missing domain",
			yaml: `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: test`,
			expectedErr: "metadata.domain is required",
		},
		{
			name: "invalid domain",
			yaml: `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: test
  domain: invalid-domain
spec:
  entries:
    - name: test-entry
      description: test`,
			expectedErr: "invalid domain",
		},
		{
			name: "empty entries",
			yaml: `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: test
  domain: sql
spec:
  entries: []`,
			expectedErr: "spec.entries cannot be empty",
		},
		{
			name: "entry missing name",
			yaml: `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: test
  domain: sql
spec:
  entries:
    - description: test pattern
      template: SELECT *`,
			expectedErr: "entries[0].name is required",
		},
		{
			name: "entry missing description",
			yaml: `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: test
  domain: sql
spec:
  entries:
    - name: test-pattern
      template: SELECT *`,
			expectedErr: "entries[0].description is required",
		},
		{
			name: "entry missing content",
			yaml: `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: test
  domain: sql
spec:
  entries:
    - name: test-pattern
      description: test`,
			expectedErr: "entries[0] must have at least one of: template, example, or rule",
		},
		{
			name: "rule missing condition",
			yaml: `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: test
  domain: sql
spec:
  entries:
    - name: test-pattern
      description: test
      rule:
        action: do something`,
			expectedErr: "entries[0].rule.condition is required",
		},
		{
			name: "rule missing action",
			yaml: `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: test
  domain: sql
spec:
  entries:
    - name: test-pattern
      description: test
      rule:
        condition: something`,
			expectedErr: "entries[0].rule.action is required",
		},
		{
			name: "priority out of range",
			yaml: `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: test
  domain: sql
spec:
  entries:
    - name: test-pattern
      description: test
      template: SELECT *
      priority: 150`,
			expectedErr: "entries[0].priority must be between 0 and 100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			libraryPath := filepath.Join(tmpDir, "pattern.yaml")
			require.NoError(t, os.WriteFile(libraryPath, []byte(tt.yaml), 0644))

			_, err := LoadPatternLibrary(libraryPath)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func TestLoadPatternLibrary_Defaults(t *testing.T) {
	tmpDir := t.TempDir()

	// Minimal config to test defaults
	yamlContent := `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: minimal
  domain: sql
spec:
  entries:
    - name: test-pattern
      description: test pattern
      template: SELECT *
`

	libraryPath := filepath.Join(tmpDir, "minimal.yaml")
	require.NoError(t, os.WriteFile(libraryPath, []byte(yamlContent), 0644))

	library, err := LoadPatternLibrary(libraryPath)
	require.NoError(t, err)

	// Verify default version was set
	assert.Equal(t, "1.0.0", library.Metadata.Version, "default version should be 1.0.0")
}

func TestLoadPatternLibrary_EnvVarExpansion(t *testing.T) {
	tmpDir := t.TempDir()

	yamlContent := `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: ${PATTERN_NAME}
  domain: sql
  description: ${PATTERN_DESC}
spec:
  entries:
    - name: test-pattern
      description: ${PATTERN_ENTRY_DESC}
      template: SELECT * FROM ${TABLE_NAME}
`

	// Set env vars
	os.Setenv("PATTERN_NAME", "dynamic-patterns")
	os.Setenv("PATTERN_DESC", "Dynamically configured patterns")
	os.Setenv("PATTERN_ENTRY_DESC", "Dynamic entry")
	os.Setenv("TABLE_NAME", "users")
	defer func() {
		os.Unsetenv("PATTERN_NAME")
		os.Unsetenv("PATTERN_DESC")
		os.Unsetenv("PATTERN_ENTRY_DESC")
		os.Unsetenv("TABLE_NAME")
	}()

	libraryPath := filepath.Join(tmpDir, "pattern.yaml")
	require.NoError(t, os.WriteFile(libraryPath, []byte(yamlContent), 0644))

	library, err := LoadPatternLibrary(libraryPath)
	require.NoError(t, err)

	// Verify env vars were expanded
	assert.Equal(t, "dynamic-patterns", library.Metadata.Name)
	assert.Equal(t, "Dynamically configured patterns", library.Metadata.Description)
	assert.Equal(t, "Dynamic entry", library.Spec.Entries[0].Description)
	assert.Contains(t, library.Spec.Entries[0].GetTemplate(), "users")
}

func TestLoadPatternLibrary_FileNotFound(t *testing.T) {
	_, err := LoadPatternLibrary("/nonexistent/pattern.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read pattern library file")
}

func TestLoadPatternLibrary_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()

	invalidYAML := `apiVersion: loom/v1
kind: PatternLibrary
name: [test`

	libraryPath := filepath.Join(tmpDir, "invalid.yaml")
	require.NoError(t, os.WriteFile(libraryPath, []byte(invalidYAML), 0644))

	_, err := LoadPatternLibrary(libraryPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse pattern library YAML")
}

func TestLoadPatternLibrary_AllDomains(t *testing.T) {
	domains := []string{
		"sql", "teradata", "postgres", "mysql",
		"code-review", "rest-api", "graphql", "document",
		"ml", "analytics", "data-quality",
	}

	for _, domain := range domains {
		t.Run(domain, func(t *testing.T) {
			tmpDir := t.TempDir()

			yamlContent := `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: ` + domain + `-patterns
  domain: ` + domain + `
spec:
  entries:
    - name: test-pattern
      description: test pattern
      template: test content
`

			libraryPath := filepath.Join(tmpDir, "pattern.yaml")
			require.NoError(t, os.WriteFile(libraryPath, []byte(yamlContent), 0644))

			library, err := LoadPatternLibrary(libraryPath)
			require.NoError(t, err)
			assert.Equal(t, domain, library.Metadata.Domain)
		})
	}
}

func TestLoadPatternLibrary_MultipleContentTypes(t *testing.T) {
	tmpDir := t.TempDir()

	yamlContent := `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: content-types
  domain: sql
spec:
  entries:
    - name: template-pattern
      description: Pattern with template
      template: SELECT {{columns}} FROM {{table}}
      priority: 50

    - name: example-pattern
      description: Pattern with example
      example: |
        SELECT id, name FROM users
        WHERE status = 'active'
      priority: 60

    - name: rule-pattern
      description: Pattern with rule
      rule:
        condition: query takes > 1s
        action: Add index on filter columns
        rationale: Indexes speed up lookups
      priority: 70
`

	libraryPath := filepath.Join(tmpDir, "patterns.yaml")
	require.NoError(t, os.WriteFile(libraryPath, []byte(yamlContent), 0644))

	library, err := LoadPatternLibrary(libraryPath)
	require.NoError(t, err)
	require.Len(t, library.Spec.Entries, 3)

	// Verify each content type
	assert.NotEmpty(t, library.Spec.Entries[0].GetTemplate())
	assert.NotEmpty(t, library.Spec.Entries[1].GetExample())
	assert.NotNil(t, library.Spec.Entries[2].GetRule())
}

func TestLoadPatternLibrary_ComplexTriggers(t *testing.T) {
	tmpDir := t.TempDir()

	yamlContent := `apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: trigger-tests
  domain: sql
spec:
  entries:
    - name: multi-trigger
      description: Pattern with multiple triggers
      trigger_conditions:
        - "slow query"
        - "high CPU"
        - "table scan"
      template: USE INDEX ON {{table}}
      priority: 90
      tags:
        - performance
        - optimization
        - indexing
`

	libraryPath := filepath.Join(tmpDir, "patterns.yaml")
	require.NoError(t, os.WriteFile(libraryPath, []byte(yamlContent), 0644))

	library, err := LoadPatternLibrary(libraryPath)
	require.NoError(t, err)
	require.Len(t, library.Spec.Entries, 1)

	entry := library.Spec.Entries[0]
	assert.Len(t, entry.TriggerConditions, 3)
	assert.Contains(t, entry.TriggerConditions, "slow query")
	assert.Contains(t, entry.TriggerConditions, "high CPU")
	assert.Contains(t, entry.TriggerConditions, "table scan")
	assert.Len(t, entry.Tags, 3)
}

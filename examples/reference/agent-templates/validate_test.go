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
package agenttemplates

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/orchestration"
)

// Test that all example agent templates are valid and can be loaded
func TestExampleTemplates(t *testing.T) {
	examples := []struct {
		name     string
		file     string
		extends  string // Parent template if any
		testVars map[string]string
	}{
		{
			name:    "base-expert",
			file:    "base-expert.yaml",
			extends: "",
		},
		{
			name:    "sql-expert",
			file:    "sql-expert.yaml",
			extends: "base-expert",
			testVars: map[string]string{
				"database": "postgres",
				"schema":   "analytics",
			},
		},
		{
			name:    "security-analyst",
			file:    "security-analyst.yaml",
			extends: "base-expert",
			testVars: map[string]string{
				"focus_area":         "web",
				"severity_threshold": "high",
			},
		},
		{
			name:    "code-reviewer",
			file:    "code-reviewer.yaml",
			extends: "base-expert",
			testVars: map[string]string{
				"language":     "go",
				"style_guide":  "effective-go",
				"review_depth": "standard",
			},
		},
	}

	for _, ex := range examples {
		t.Run(ex.name, func(t *testing.T) {
			registry := orchestration.NewTemplateRegistry()

			// Load parent template if needed
			if ex.extends != "" {
				parentPath := filepath.Join(".", ex.extends+".yaml")
				err := registry.LoadTemplate(parentPath)
				require.NoError(t, err, "Failed to load parent template %s", ex.extends)
			}

			// Load template
			path := filepath.Join(".", ex.file)
			err := registry.LoadTemplate(path)
			require.NoError(t, err, "Failed to load template %s", ex.file)

			// Verify template is registered
			tmpl, err := registry.GetTemplate(ex.name)
			require.NoError(t, err, "Template %s not registered", ex.name)
			assert.Equal(t, ex.name, tmpl.Metadata.Name)

			// If template has test vars, apply them and verify config generation
			if ex.testVars != nil {
				config, err := registry.ApplyTemplate(ex.name, ex.testVars)
				require.NoError(t, err, "Failed to apply template %s", ex.name)
				require.NotNil(t, config, "Config is nil for %s", ex.name)

				// Verify basic config properties
				assert.NotEmpty(t, config.Name, "Config name is empty")
				assert.NotEmpty(t, config.SystemPrompt, "System prompt is empty")
				// LLM config may be nil (inherits from server defaults)
				// Tools should be configured
				assert.NotNil(t, config.Tools, "Tools config should not be nil")
				assert.NotEmpty(t, config.Tools.Builtin, "Builtin tools should be configured")
			}
		})
	}
}

// Test SQL expert with different databases
func TestSQLExpertVariants(t *testing.T) {
	databases := []struct {
		name      string
		schema    string
		maxTokens string
	}{
		{"postgres", "public", "8192"},
		{"mysql", "production", "8192"},
		{"teradata", "analytics_db", "16384"},
		{"oracle", "sys", "8192"},
	}

	registry := orchestration.NewTemplateRegistry()

	// Load parent and template
	require.NoError(t, registry.LoadTemplate("base-expert.yaml"))
	require.NoError(t, registry.LoadTemplate("sql-expert.yaml"))

	for _, db := range databases {
		t.Run(db.name, func(t *testing.T) {
			vars := map[string]string{
				"database":   db.name,
				"schema":     db.schema,
				"max_tokens": db.maxTokens,
			}

			config, err := registry.ApplyTemplate("sql-expert", vars)
			require.NoError(t, err)

			// Verify variable substitution
			expectedName := db.name + "-sql-expert"
			assert.Equal(t, expectedName, config.Name)
			assert.Contains(t, config.SystemPrompt, db.name)
			assert.Contains(t, config.SystemPrompt, db.schema)
			// Memory path is optional - not checked
			assert.Equal(t, db.name, config.Metadata["database_type"])
			assert.Equal(t, db.schema, config.Metadata["default_schema"])
		})
	}
}

// Test security analyst with different focus areas
func TestSecurityAnalystVariants(t *testing.T) {
	focusAreas := []struct {
		area      string
		threshold string
	}{
		{"web", "high"},
		{"api", "medium"},
		{"infrastructure", "critical"},
		{"code", "low"},
	}

	registry := orchestration.NewTemplateRegistry()

	// Load parent and template
	require.NoError(t, registry.LoadTemplate("base-expert.yaml"))
	require.NoError(t, registry.LoadTemplate("security-analyst.yaml"))

	for _, focus := range focusAreas {
		t.Run(focus.area, func(t *testing.T) {
			vars := map[string]string{
				"focus_area":         focus.area,
				"severity_threshold": focus.threshold,
			}

			config, err := registry.ApplyTemplate("security-analyst", vars)
			require.NoError(t, err)

			// Verify variable substitution
			expectedName := "security-analyst-" + focus.area
			assert.Equal(t, expectedName, config.Name)
			assert.Contains(t, config.SystemPrompt, focus.area)
			assert.Contains(t, config.SystemPrompt, focus.threshold)
			// Memory path is optional - not checked
			assert.Equal(t, focus.area, config.Metadata["focus_area"])
			assert.Equal(t, focus.threshold, config.Metadata["severity_threshold"])
		})
	}
}

// Test template inheritance chain
func TestTemplateInheritance(t *testing.T) {
	registry := orchestration.NewTemplateRegistry()

	// Load base template
	require.NoError(t, registry.LoadTemplate("base-expert.yaml"))

	// Load child template
	require.NoError(t, registry.LoadTemplate("sql-expert.yaml"))

	// Apply child template
	vars := map[string]string{
		"database": "postgres",
	}
	config, err := registry.ApplyTemplate("sql-expert", vars)
	require.NoError(t, err)

	// Verify inherited values from base-expert
	assert.NotNil(t, config.Behavior)
	assert.Equal(t, int32(10), config.Behavior.MaxTurns)
	assert.Equal(t, int32(30), config.Behavior.MaxToolExecutions)
	assert.Equal(t, int32(600), config.Behavior.TimeoutSeconds)

	// Verify overridden values from sql-expert
	if config.Llm != nil {
		assert.Equal(t, float32(0.3), config.Llm.Temperature) // Overridden
	}
	assert.Equal(t, "postgres-sql-expert", config.Name) // From template

	// Verify metadata inheritance
	assert.Equal(t, "loom-template-system", config.Metadata["created_by"]) // From base
	assert.Equal(t, "postgres", config.Metadata["database_type"])          // From child
}

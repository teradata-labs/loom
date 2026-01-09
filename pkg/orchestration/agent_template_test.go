// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTemplateRegistry_LoadTemplate(t *testing.T) {
	template := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: sql-expert
  description: SQL query expert template
  version: "1.0"
parameters:
  - name: database
    type: string
    required: true
    description: Database type (postgres, mysql, etc.)
  - name: max_tokens
    type: int
    default: "4096"
spec:
  name: "{{database}}-expert"
  description: "Expert in {{database}} databases"
  system_prompt: |
    You are an expert in {{database}} databases.
    Help users write efficient SQL queries.
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
    temperature: 0.7
    max_tokens: "{{max_tokens}}"
  memory:
    type: sqlite
    path: "./sessions/{{database}}-expert.db"
`

	tmpDir := t.TempDir()
	templatePath := filepath.Join(tmpDir, "sql-expert.yaml")
	err := os.WriteFile(templatePath, []byte(template), 0644)
	require.NoError(t, err)

	registry := NewTemplateRegistry()
	err = registry.LoadTemplate(templatePath)
	require.NoError(t, err)

	// Verify template registered
	tmpl, err := registry.GetTemplate("sql-expert")
	require.NoError(t, err)
	assert.Equal(t, "sql-expert", tmpl.Metadata.Name)
	assert.Len(t, tmpl.Parameters, 2)
}

func TestTemplateRegistry_ApplyTemplate(t *testing.T) {
	template := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: simple-agent
  description: Simple agent template
parameters:
  - name: agent_name
    type: string
    required: true
  - name: domain
    type: string
    required: true
spec:
  name: "{{agent_name}}"
  description: "Expert in {{domain}}"
  system_prompt: |
    You are {{agent_name}}, an expert in {{domain}}.
    Help users with {{domain}}-related questions.
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
    temperature: 0.7
    max_tokens: 4096
`

	registry := NewTemplateRegistry()
	err := registry.LoadTemplateFromString(template)
	require.NoError(t, err)

	// Apply template with variables
	vars := map[string]string{
		"agent_name": "python-guru",
		"domain":     "Python programming",
	}

	config, err := registry.ApplyTemplate("simple-agent", vars)
	require.NoError(t, err)
	require.NotNil(t, config)

	// Check variable substitution
	assert.Equal(t, "python-guru", config.Name)
	assert.Equal(t, "Expert in Python programming", config.Description)
	assert.Contains(t, config.SystemPrompt, "You are python-guru")
	assert.Contains(t, config.SystemPrompt, "expert in Python programming")
	assert.Contains(t, config.SystemPrompt, "Python programming-related questions")
}

func TestTemplateRegistry_TemplateInheritance(t *testing.T) {
	baseTemplate := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: base-expert
  description: Base expert template
spec:
  system_prompt: |
    You are a helpful expert.
    Always be clear and concise.
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
    temperature: 0.7
    max_tokens: 4096
  behavior:
    max_iterations: 5
    timeout_seconds: 300
`

	childTemplate := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: sql-expert
  description: SQL expert derived from base
extends: base-expert
parameters:
  - name: database
    type: string
    required: true
spec:
  name: "{{database}}-expert"
  system_prompt: |
    You are an expert in {{database}} databases.
    Help users write efficient SQL queries.
  llm:
    temperature: 0.5
`

	registry := NewTemplateRegistry()
	err := registry.LoadTemplateFromString(baseTemplate)
	require.NoError(t, err)
	err = registry.LoadTemplateFromString(childTemplate)
	require.NoError(t, err)

	// Apply child template
	vars := map[string]string{"database": "postgres"}
	config, err := registry.ApplyTemplate("sql-expert", vars)
	require.NoError(t, err)

	// Check inheritance worked
	assert.Equal(t, "postgres-expert", config.Name)
	assert.Contains(t, config.SystemPrompt, "expert in postgres databases")
	assert.NotNil(t, config.Llm)
	assert.Equal(t, "anthropic", config.Llm.Provider)
	assert.Equal(t, "claude-3-5-sonnet-20250131", config.Llm.Model)
	assert.NotNil(t, config.Behavior)
	assert.Equal(t, int32(5), config.Behavior.MaxIterations)
}

func TestTemplateRegistry_CircularReference(t *testing.T) {
	template1 := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: template-a
extends: template-b
spec:
  name: agent-a
`

	template2 := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: template-b
extends: template-a
spec:
  name: agent-b
`

	registry := NewTemplateRegistry()
	err := registry.LoadTemplateFromString(template1)
	require.NoError(t, err)
	err = registry.LoadTemplateFromString(template2)
	require.NoError(t, err)

	// Should detect cycle
	_, err = registry.ApplyTemplate("template-a", nil)
	assert.ErrorIs(t, err, ErrCircularReference)
}

func TestTemplateRegistry_MissingRequiredParameter(t *testing.T) {
	template := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: test-template
parameters:
  - name: required_param
    type: string
    required: true
spec:
  name: "{{required_param}}-agent"
`

	registry := NewTemplateRegistry()
	err := registry.LoadTemplateFromString(template)
	require.NoError(t, err)

	// Apply without required parameter
	_, err = registry.ApplyTemplate("test-template", nil)
	assert.ErrorIs(t, err, ErrMissingParameter)
}

func TestTemplateRegistry_DefaultParameters(t *testing.T) {
	template := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: test-template
parameters:
  - name: agent_name
    type: string
    required: false
    default: "default-agent"
  - name: max_tokens
    type: int
    required: false
    default: "2048"
spec:
  name: "{{agent_name}}"
  llm:
    max_tokens: "{{max_tokens}}"
`

	registry := NewTemplateRegistry()
	err := registry.LoadTemplateFromString(template)
	require.NoError(t, err)

	// Apply without providing parameters (should use defaults)
	config, err := registry.ApplyTemplate("test-template", nil)
	require.NoError(t, err)
	assert.Equal(t, "default-agent", config.Name)
}

func TestTemplateRegistry_EnvironmentVariables(t *testing.T) {
	template := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: env-template
spec:
  name: test-agent
  system_prompt: "API Key: ${LOOM_TEST_KEY}"
`

	// Set environment variable
	os.Setenv("LOOM_TEST_KEY", "secret-123")
	defer os.Unsetenv("LOOM_TEST_KEY")

	registry := NewTemplateRegistry()
	err := registry.LoadTemplateFromString(template)
	require.NoError(t, err)

	config, err := registry.ApplyTemplate("env-template", nil)
	require.NoError(t, err)
	assert.Contains(t, config.SystemPrompt, "API Key: secret-123")
}

func TestTemplateRegistry_BothVariableSyntaxes(t *testing.T) {
	template := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: syntax-test
parameters:
  - name: name1
    type: string
    required: true
  - name: name2
    type: string
    required: true
spec:
  name: test-agent
  description: "Using {{name1}} and ${name2}"
`

	registry := NewTemplateRegistry()
	err := registry.LoadTemplateFromString(template)
	require.NoError(t, err)

	vars := map[string]string{
		"name1": "curly-braces",
		"name2": "dollar-braces",
	}

	config, err := registry.ApplyTemplate("syntax-test", vars)
	require.NoError(t, err)
	assert.Equal(t, "Using curly-braces and dollar-braces", config.Description)
}

func TestTemplateRegistry_CompleteAgentConfig(t *testing.T) {
	template := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: complete-agent
  description: Agent with all fields
parameters:
  - name: agent_name
    type: string
    required: true
spec:
  name: "{{agent_name}}"
  description: Complete agent configuration
  system_prompt: "You are {{agent_name}}"
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
    temperature: 0.7
    max_tokens: 4096
    top_p: 0.9
    top_k: 40
    stop_sequences:
      - "STOP"
      - "END"
  tools:
    builtin:
      - web_search
      - calculator
    mcp:
      - server: filesystem
        tools:
          - read_file
          - write_file
    custom:
      - name: custom_tool
        implementation: "./tools/custom.go"
  memory:
    type: sqlite
    path: "./sessions/{{agent_name}}.db"
    max_history: 100
  behavior:
    max_iterations: 10
    timeout_seconds: 600
    allow_code_execution: false
    allowed_domains:
      - example.com
      - api.example.com
  metadata:
    author: test
    version: "1.0"
`

	registry := NewTemplateRegistry()
	err := registry.LoadTemplateFromString(template)
	require.NoError(t, err)

	vars := map[string]string{"agent_name": "test-agent"}
	config, err := registry.ApplyTemplate("complete-agent", vars)
	require.NoError(t, err)

	// Validate all fields
	assert.Equal(t, "test-agent", config.Name)
	assert.Equal(t, "Complete agent configuration", config.Description)
	assert.Contains(t, config.SystemPrompt, "You are test-agent")

	// LLM config
	require.NotNil(t, config.Llm)
	assert.Equal(t, "anthropic", config.Llm.Provider)
	assert.Equal(t, "claude-3-5-sonnet-20250131", config.Llm.Model)
	assert.Equal(t, float32(0.7), config.Llm.Temperature)
	assert.Equal(t, int32(4096), config.Llm.MaxTokens)
	assert.Equal(t, float32(0.9), config.Llm.TopP)
	assert.Equal(t, int32(40), config.Llm.TopK)
	assert.Equal(t, []string{"STOP", "END"}, config.Llm.StopSequences)

	// Tools config
	require.NotNil(t, config.Tools)
	assert.Equal(t, []string{"web_search", "calculator"}, config.Tools.Builtin)
	assert.Len(t, config.Tools.Mcp, 1)
	assert.Equal(t, "filesystem", config.Tools.Mcp[0].Server)
	assert.Equal(t, []string{"read_file", "write_file"}, config.Tools.Mcp[0].Tools)
	assert.Len(t, config.Tools.Custom, 1)
	assert.Equal(t, "custom_tool", config.Tools.Custom[0].Name)

	// Memory config
	require.NotNil(t, config.Memory)
	assert.Equal(t, "sqlite", config.Memory.Type)
	assert.Equal(t, "./sessions/test-agent.db", config.Memory.Path)
	assert.Equal(t, int32(100), config.Memory.MaxHistory)

	// Behavior config
	require.NotNil(t, config.Behavior)
	assert.Equal(t, int32(10), config.Behavior.MaxIterations)
	assert.Equal(t, int32(600), config.Behavior.TimeoutSeconds)
	assert.False(t, config.Behavior.AllowCodeExecution)
	assert.Equal(t, []string{"example.com", "api.example.com"}, config.Behavior.AllowedDomains)

	// Metadata
	assert.Equal(t, "test", config.Metadata["author"])
	assert.Equal(t, "1.0", config.Metadata["version"])
}

func TestTemplateRegistry_MissingTemplate(t *testing.T) {
	registry := NewTemplateRegistry()
	_, err := registry.GetTemplate("nonexistent")
	assert.ErrorIs(t, err, ErrTemplateNotFound)
}

func TestTemplateRegistry_InvalidAPIVersion(t *testing.T) {
	template := `apiVersion: loom/v2
kind: AgentTemplate
metadata:
  name: test
spec:
  name: test
`

	registry := NewTemplateRegistry()
	err := registry.LoadTemplateFromString(template)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "unsupported apiVersion")
}

func TestTemplateRegistry_InvalidKind(t *testing.T) {
	template := `apiVersion: loom/v1
kind: InvalidKind
metadata:
  name: test
spec:
  name: test
`

	registry := NewTemplateRegistry()
	err := registry.LoadTemplateFromString(template)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "unsupported kind")
}

func TestTemplateRegistry_MissingMetadataName(t *testing.T) {
	template := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  description: test
spec:
  name: test
`

	registry := NewTemplateRegistry()
	err := registry.LoadTemplateFromString(template)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "missing metadata.name")
}

func TestTemplateRegistry_ListTemplates(t *testing.T) {
	template1 := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: template1
spec:
  name: agent1
`

	template2 := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: template2
spec:
  name: agent2
`

	registry := NewTemplateRegistry()
	err := registry.LoadTemplateFromString(template1)
	require.NoError(t, err)
	err = registry.LoadTemplateFromString(template2)
	require.NoError(t, err)

	templates := registry.ListTemplates()
	assert.Len(t, templates, 2)
	assert.Contains(t, templates, "template1")
	assert.Contains(t, templates, "template2")
}

func TestTemplateRegistry_ThreeLevelInheritance(t *testing.T) {
	base := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: base
spec:
  system_prompt: "Base prompt"
  llm:
    provider: anthropic
    max_tokens: 4096
  behavior:
    max_iterations: 5
`

	middle := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: middle
extends: base
spec:
  system_prompt: "Middle prompt"
  llm:
    temperature: 0.7
`

	child := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: child
extends: middle
spec:
  name: final-agent
  llm:
    model: claude-3-5-sonnet-20250131
`

	registry := NewTemplateRegistry()
	require.NoError(t, registry.LoadTemplateFromString(base))
	require.NoError(t, registry.LoadTemplateFromString(middle))
	require.NoError(t, registry.LoadTemplateFromString(child))

	config, err := registry.ApplyTemplate("child", nil)
	require.NoError(t, err)

	// Should inherit from base and middle
	assert.Equal(t, "final-agent", config.Name)
	assert.NotNil(t, config.Llm)
	assert.Equal(t, "anthropic", config.Llm.Provider)
	assert.Equal(t, "claude-3-5-sonnet-20250131", config.Llm.Model)
	assert.NotNil(t, config.Behavior)
	assert.Equal(t, int32(5), config.Behavior.MaxIterations)
}

func TestTemplateRegistry_ParameterOverride(t *testing.T) {
	template := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: test
parameters:
  - name: name
    type: string
    default: "default-name"
spec:
  name: "{{name}}"
`

	registry := NewTemplateRegistry()
	err := registry.LoadTemplateFromString(template)
	require.NoError(t, err)

	// Test with default
	config1, err := registry.ApplyTemplate("test", nil)
	require.NoError(t, err)
	assert.Equal(t, "default-name", config1.Name)

	// Test with override
	config2, err := registry.ApplyTemplate("test", map[string]string{"name": "custom-name"})
	require.NoError(t, err)
	assert.Equal(t, "custom-name", config2.Name)
}

func TestLoadAgentFromTemplate(t *testing.T) {
	template := `apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: quick-template
parameters:
  - name: agent_name
    type: string
    required: true
spec:
  name: "{{agent_name}}"
  description: Test agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
`

	tmpDir := t.TempDir()
	templatePath := filepath.Join(tmpDir, "template.yaml")
	err := os.WriteFile(templatePath, []byte(template), 0644)
	require.NoError(t, err)

	vars := map[string]string{"agent_name": "my-agent"}
	config, err := LoadAgentFromTemplate(templatePath, vars)
	require.NoError(t, err)
	assert.Equal(t, "my-agent", config.Name)
}

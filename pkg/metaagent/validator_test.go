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
	"strings"
	"testing"

	"github.com/teradata-labs/loom/pkg/observability"
)

// Helper function to count errors and warnings
func countValidationIssues(result *ValidationResult) (int, int) {
	return len(result.Errors), len(result.Warnings)
}

// Test valid configuration passes
func TestValidate_ValidConfig(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	validYAML := `
agent:
  name: test-agent
  description: Test agent for validation
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
    temperature: 0.7
    max_tokens: 4096
  system_prompt: |
    Analyze SQL queries for performance issues and suggest optimizations.
    Return results in JSON format with identified problems and recommendations.
  memory:
    type: memory
    max_history: 50
  tools:
    mcp:
      - server: http://localhost:3000
        tools: ["query", "schema"]
`

	result, err := validator.Validate(ctx, validYAML)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if !result.Valid {
		t.Errorf("Expected valid=true, got valid=false. Errors: %+v", result.Errors)
	}

	errorCount, _ := countValidationIssues(result)
	if errorCount > 0 {
		t.Errorf("Expected 0 errors, got %d: %+v", errorCount, result.Errors)
	}
}

// Test YAML syntax error
func TestValidate_InvalidYAMLSyntax(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	invalidYAML := `
agent:
  name: test-agent
  llm:
    provider: anthropic
	model: claude-3-5-sonnet-20250131  # Invalid indentation (tab instead of spaces)
`

	result, err := validator.Validate(ctx, invalidYAML)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for invalid YAML syntax")
	}

	errorCount, _ := countValidationIssues(result)
	if errorCount == 0 {
		t.Errorf("Expected at least 1 error for invalid YAML")
	}

	// Check error type
	if len(result.Errors) > 0 && result.Errors[0].Type != "syntax_error" {
		t.Errorf("Expected error type 'syntax_error', got '%s'", result.Errors[0].Type)
	}
}

// Test missing required fields
func TestValidate_MissingAgentName(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: Analyze data
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for missing agent name")
	}

	// Find the specific error
	found := false
	for _, e := range result.Errors {
		if e.Field == "agent.name" && e.Type == "required_field" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected error for missing agent.name field")
	}
}

func TestValidate_MissingLLMProvider(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    model: claude-3-5-sonnet-20250131
  system_prompt: Analyze data
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for missing LLM provider")
	}

	found := false
	for _, e := range result.Errors {
		if e.Field == "agent.llm.provider" && e.Type == "required_field" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected error for missing agent.llm.provider field")
	}
}

func TestValidate_MissingLLMModel(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
  system_prompt: Analyze data
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for missing LLM model")
	}

	found := false
	for _, e := range result.Errors {
		if e.Field == "agent.llm.model" && e.Type == "required_field" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected error for missing agent.llm.model field")
	}
}

// Test anti-pattern detection
func TestValidate_AntiPattern_YouAreA(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: You are a helpful SQL analyst. Analyze queries for performance issues.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for role-prompting anti-pattern")
	}

	found := false
	for _, e := range result.Errors {
		if e.Type == "anti_pattern" && strings.Contains(e.Message, "You are a/an") {
			found = true
			if e.Line == 0 {
				t.Errorf("Expected line number > 0, got %d", e.Line)
			}
			if e.Suggestion == "" {
				t.Errorf("Expected suggestion for anti-pattern")
			}
			break
		}
	}

	if !found {
		t.Errorf("Expected anti-pattern error for 'You are a'")
	}
}

func TestValidate_AntiPattern_AsA(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: As a SQL expert, analyze queries for performance issues.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for role-prompting anti-pattern")
	}

	found := false
	for _, e := range result.Errors {
		if e.Type == "anti_pattern" && strings.Contains(e.Message, "As a/an") {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected anti-pattern error for 'As a'")
	}
}

func TestValidate_AntiPattern_ActAs(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: Act as a database administrator and analyze SQL queries.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for role-prompting anti-pattern")
	}

	found := false
	for _, e := range result.Errors {
		if e.Type == "anti_pattern" && strings.Contains(e.Message, "Act as") {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected anti-pattern error for 'Act as'")
	}
}

func TestValidate_AntiPattern_PretendToBe(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: Pretend to be an SQL optimization expert and help with queries.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for role-prompting anti-pattern")
	}

	found := false
	for _, e := range result.Errors {
		if e.Type == "anti_pattern" && strings.Contains(e.Message, "Pretend to be") {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected anti-pattern error for 'Pretend to be'")
	}
}

func TestValidate_AntiPattern_ImagineYouAre(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: Imagine you are a data analyst. Analyze the queries provided.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for role-prompting anti-pattern")
	}

	found := false
	for _, e := range result.Errors {
		if e.Type == "anti_pattern" && strings.Contains(e.Message, "Imagine you are") {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected anti-pattern error for 'Imagine you are'")
	}
}

func TestValidate_AntiPattern_YourRoleIs(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: Your role is to analyze SQL queries and suggest improvements.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for role-prompting anti-pattern")
	}

	found := false
	for _, e := range result.Errors {
		if e.Type == "anti_pattern" && strings.Contains(e.Message, "Your role is") {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected anti-pattern error for 'Your role is'")
	}
}

func TestValidate_AntiPattern_YouWillBe(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: You will be helping users optimize their database queries.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for role-prompting anti-pattern")
	}

	found := false
	for _, e := range result.Errors {
		if e.Type == "anti_pattern" && strings.Contains(e.Message, "You will be") {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected anti-pattern error for 'You will be'")
	}
}

func TestValidate_AntiPattern_YoureA(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: You're a SQL performance tuning assistant. Help optimize queries.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for role-prompting anti-pattern")
	}

	found := false
	for _, e := range result.Errors {
		if e.Type == "anti_pattern" && strings.Contains(e.Message, "You're a/an") {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected anti-pattern error for 'You're a'")
	}
}

// Test system prompt length validation
func TestValidate_SystemPromptTooShort(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: Short prompt
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for system prompt too short")
	}

	found := false
	for _, e := range result.Errors {
		if strings.Contains(e.Field, "system_prompt") && e.Type == "length_error" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected length_error for short system prompt")
	}
}

func TestValidate_SystemPromptVeryLong(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	// Create a very long prompt (>2000 chars)
	longPrompt := strings.Repeat("Analyze SQL queries for performance issues. ", 50)

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: ` + longPrompt

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	// Long prompt is a warning, not an error
	_, warningCount := countValidationIssues(result)
	if warningCount == 0 {
		t.Errorf("Expected at least 1 warning for very long system prompt")
	}
}

// Test hardcoded credentials detection
func TestValidate_HardcodedPassword(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: |
    Connect to the database using password="secret123" and run queries.
    Analyze the results and return optimizations.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for hardcoded password")
	}

	found := false
	for _, e := range result.Errors {
		if e.Type == "security_risk" && strings.Contains(e.Message, "password") {
			found = true
			if !strings.Contains(e.Suggestion, "environment") {
				t.Errorf("Expected suggestion to use environment variables")
			}
			break
		}
	}

	if !found {
		t.Errorf("Expected security_risk error for hardcoded password")
	}
}

func TestValidate_HardcodedAPIKey(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: |
    Use api_key="sk-12345" to authenticate.
    Query the API and return results.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for hardcoded API key")
	}

	found := false
	for _, e := range result.Errors {
		if e.Type == "security_risk" && strings.Contains(strings.ToLower(e.Message), "api key") {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected security_risk error for hardcoded API key")
	}
}

// Test LLM config validation
func TestValidate_InvalidLLMProvider(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: invalid_provider
    model: some-model
  system_prompt: Analyze data and return results in JSON format.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for invalid LLM provider")
	}

	found := false
	for _, e := range result.Errors {
		if e.Field == "agent.llm.provider" && e.Type == "invalid_value" {
			found = true
			if !strings.Contains(e.Suggestion, "anthropic") {
				t.Errorf("Expected suggestion to include valid providers")
			}
			break
		}
	}

	if !found {
		t.Errorf("Expected invalid_value error for LLM provider")
	}
}

func TestValidate_TemperatureOutOfRange_TooLow(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
    temperature: -0.5
  system_prompt: Analyze data and return results in JSON format.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for temperature out of range")
	}

	found := false
	for _, e := range result.Errors {
		if e.Field == "agent.llm.temperature" && e.Type == "invalid_value" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected invalid_value error for temperature out of range")
	}
}

func TestValidate_TemperatureOutOfRange_TooHigh(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
    temperature: 3.0
  system_prompt: Analyze data and return results in JSON format.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for temperature out of range")
	}

	found := false
	for _, e := range result.Errors {
		if e.Field == "agent.llm.temperature" && e.Type == "invalid_value" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected invalid_value error for temperature out of range")
	}
}

func TestValidate_MaxTokensNegative(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
    max_tokens: -100
  system_prompt: Analyze data and return results in JSON format.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for negative max_tokens")
	}

	found := false
	for _, e := range result.Errors {
		if e.Field == "agent.llm.max_tokens" && e.Type == "invalid_value" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected invalid_value error for negative max_tokens")
	}
}

func TestValidate_MaxTokensVeryHigh(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
    max_tokens: 150000
  system_prompt: Analyze data and return results in JSON format.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	// Very high max_tokens is a warning, not an error
	_, warningCount := countValidationIssues(result)
	if warningCount == 0 {
		t.Errorf("Expected at least 1 warning for very high max_tokens")
	}
}

// Test MCP backend validation
func TestValidate_MCPServerMissing(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: Analyze data and return results in JSON format.
  tools:
    mcp:
      - tools: ["query"]
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for missing MCP server")
	}

	found := false
	for _, e := range result.Errors {
		if strings.Contains(e.Field, "mcp") && strings.Contains(e.Field, "server") && e.Type == "required_field" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected required_field error for missing MCP server")
	}
}

func TestValidate_MCPServerInvalidURL(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: Analyze data and return results in JSON format.
  tools:
    mcp:
      - server: "not a valid url ://bad"
        tools: ["query"]
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for invalid MCP server URL")
	}

	found := false
	for _, e := range result.Errors {
		if strings.Contains(e.Field, "mcp") && strings.Contains(e.Field, "server") && e.Type == "format_error" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected format_error for invalid MCP server URL")
	}
}

// Test custom tool validation
func TestValidate_CustomToolMissingName(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: Analyze data and return results in JSON format.
  tools:
    custom:
      - implementation: ./my-tool.so
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for missing custom tool name")
	}

	found := false
	for _, e := range result.Errors {
		if strings.Contains(e.Field, "custom") && strings.Contains(e.Field, "name") && e.Type == "required_field" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected required_field error for missing custom tool name")
	}
}

func TestValidate_CustomToolMissingImplementation(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: Analyze data and return results in JSON format.
  tools:
    custom:
      - name: my-tool
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for missing custom tool implementation")
	}

	found := false
	for _, e := range result.Errors {
		if strings.Contains(e.Field, "custom") && strings.Contains(e.Field, "implementation") && e.Type == "required_field" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected required_field error for missing custom tool implementation")
	}
}

// Test memory validation
func TestValidate_InvalidMemoryType(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: Analyze data and return results in JSON format.
  memory:
    type: redis
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for invalid memory type")
	}

	found := false
	for _, e := range result.Errors {
		if e.Field == "agent.memory.type" && e.Type == "invalid_value" {
			found = true
			if !strings.Contains(e.Suggestion, "memory") {
				t.Errorf("Expected suggestion to include valid memory types")
			}
			break
		}
	}

	if !found {
		t.Errorf("Expected invalid_value error for memory type")
	}
}

func TestValidate_SQLiteMemoryMissingPath(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: Analyze data and return results in JSON format.
  memory:
    type: sqlite
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for SQLite memory without path")
	}

	found := false
	for _, e := range result.Errors {
		if e.Field == "agent.memory" && e.Type == "required_field" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected required_field error for SQLite memory without path")
	}
}

func TestValidate_MaxHistoryNegative(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: Analyze data and return results in JSON format.
  memory:
    type: memory
    max_history: -10
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for negative max_history")
	}

	found := false
	for _, e := range result.Errors {
		if e.Field == "agent.memory.max_history" && e.Type == "invalid_value" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected invalid_value error for negative max_history")
	}
}

// Test warnings
func TestValidate_NoTools(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: Analyze data and return results in JSON format.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	// No tools is a warning, not an error
	_, warningCount := countValidationIssues(result)
	if warningCount == 0 {
		t.Errorf("Expected at least 1 warning for no tools configured")
	}

	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w.Field, "tools") {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected warning for no tools configured")
	}
}

func TestValidate_NoPatterns(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
  system_prompt: Analyze data and return results in JSON format.
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	// No patterns is a warning, not an error
	_, warningCount := countValidationIssues(result)
	if warningCount == 0 {
		t.Errorf("Expected at least 1 warning for no patterns specified")
	}

	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w.Field, "patterns") {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected warning for no patterns specified")
	}
}

// Test multiple errors
func TestValidate_MultipleErrors(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := `
agent:
  llm:
    temperature: 5.0
    max_tokens: -100
  system_prompt: Bad
`

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for multiple errors")
	}

	errorCount, _ := countValidationIssues(result)
	if errorCount < 3 {
		t.Errorf("Expected at least 3 errors, got %d", errorCount)
	}
}

// Test edge cases
func TestValidate_EmptyConfig(t *testing.T) {
	validator := NewValidator(observability.NewNoOpTracer())
	ctx := context.Background()

	yamlConfig := ``

	result, err := validator.Validate(ctx, yamlConfig)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if result.Valid {
		t.Errorf("Expected valid=false for empty config")
	}

	errorCount, _ := countValidationIssues(result)
	if errorCount == 0 {
		t.Errorf("Expected at least 1 error for empty config")
	}
}

// Test line number detection
func TestFindLineNumber(t *testing.T) {
	text := `Line 1
Line 2
Line 3
Line 4`

	tests := []struct {
		offset   int
		expected int
	}{
		{0, 1},   // Start of line 1
		{6, 1},   // End of line 1
		{7, 2},   // Start of line 2
		{14, 3},  // Start of line 3
		{21, 4},  // Start of line 4
		{-1, 0},  // Invalid offset
		{100, 0}, // Offset beyond text
	}

	for _, tt := range tests {
		result := findLineNumber(text, tt.offset)
		if result != tt.expected {
			t.Errorf("findLineNumber(%d) = %d, expected %d", tt.offset, result, tt.expected)
		}
	}
}

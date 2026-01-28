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
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"gopkg.in/yaml.v3"
)

// AgentValidator implements Validator
type AgentValidator struct {
	patternLoader     *PatternSelector
	antiPatterns      []*regexp.Regexp
	antiPatternNames  []string
	validProviders    map[string]bool
	validBackendTypes map[string]bool
	validMemoryTypes  map[string]bool
	tracer            observability.Tracer
}

// NewValidator creates a new AgentValidator
func NewValidator(tracer observability.Tracer) *AgentValidator {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}

	antiPatterns, antiPatternNames := compileAntiPatterns()

	return &AgentValidator{
		patternLoader:    NewPatternSelector(tracer),
		antiPatterns:     antiPatterns,
		antiPatternNames: antiPatternNames,
		validProviders: map[string]bool{
			"anthropic": true,
			"openai":    true,
			"bedrock":   true,
			"ollama":    true,
		},
		validBackendTypes: map[string]bool{
			"postgres": true,
			"file":     true,
			"http":     true,
			"rest":     true,
			"mcp":      true,
			"sqlite":   true,
			"mysql":    true,
			"teradata": true,
		},
		validMemoryTypes: map[string]bool{
			"memory":   true,
			"sqlite":   true,
			"postgres": true,
		},
		tracer: tracer,
	}
}

// Validate validates agent configuration YAML
func (v *AgentValidator) Validate(ctx context.Context, config string) (*ValidationResult, error) {
	// Start span for observability
	_, span := v.tracer.StartSpan(ctx, "metaagent.validator.validate")
	defer v.tracer.EndSpan(span)

	// Add span attributes
	span.Attributes["config_size_bytes"] = fmt.Sprintf("%d", len(config))

	result := &ValidationResult{
		Valid:    true,
		Errors:   []ValidationError{},
		Warnings: []ValidationWarning{},
	}

	// 1. YAML syntax validation
	// Split at YAML document terminator if present
	// Everything after "..." is documentation/ROM and should not be parsed
	parts := strings.SplitN(config, "\n...\n", 2)
	yamlOnly := parts[0]

	var yamlConfig agent.AgentConfigYAML
	if err := yaml.Unmarshal([]byte(yamlOnly), &yamlConfig); err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, ValidationError{
			Field:      "yaml",
			Message:    fmt.Sprintf("Failed to parse YAML: %v", err),
			Type:       "syntax_error",
			Line:       0,
			Suggestion: "Check YAML syntax (indentation, colons, quotes)",
		})
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: fmt.Sprintf("YAML syntax error: %v", err),
		}
		v.tracer.RecordMetric("metaagent.validator.validate.failed", 1.0, map[string]string{
			"error": "syntax_error",
		})
		return result, nil
	}

	// Run all validations
	v.validateRequiredFields(&yamlConfig, result)
	v.validateSystemPrompt(&yamlConfig, result)
	v.validateLLMConfig(&yamlConfig, result)
	v.validateBackends(&yamlConfig, result)
	v.validatePatterns(&yamlConfig, result)
	v.validateMemory(&yamlConfig, result)

	// Record validation results
	span.Attributes["valid"] = fmt.Sprintf("%t", result.Valid)
	span.Attributes["errors_count"] = fmt.Sprintf("%d", len(result.Errors))
	span.Attributes["warnings_count"] = fmt.Sprintf("%d", len(result.Warnings))

	if result.Valid {
		span.Status = observability.Status{
			Code:    observability.StatusOK,
			Message: fmt.Sprintf("Validation passed (%d warnings)", len(result.Warnings)),
		}
		v.tracer.RecordMetric("metaagent.validator.validate.success", 1.0, nil)
		v.tracer.RecordMetric("metaagent.validator.validation_warnings", float64(len(result.Warnings)), nil)
	} else {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: fmt.Sprintf("Validation failed (%d errors)", len(result.Errors)),
		}
		v.tracer.RecordMetric("metaagent.validator.validate.failed", 1.0, map[string]string{
			"error": "validation_errors",
		})
		v.tracer.RecordMetric("metaagent.validator.validation_errors", float64(len(result.Errors)), nil)
	}

	return result, nil
}

// validateRequiredFields checks that all required fields are present
func (v *AgentValidator) validateRequiredFields(config *agent.AgentConfigYAML, result *ValidationResult) {
	// Agent name is required
	if config.Agent.Name == "" {
		result.Valid = false
		result.Errors = append(result.Errors, ValidationError{
			Field:      "agent.name",
			Message:    "Agent name is required",
			Type:       "required_field",
			Suggestion: "Add a unique name for the agent (e.g., 'sql-analyst-agent')",
		})
	}

	// LLM provider is required
	if config.Agent.LLM.Provider == "" {
		result.Valid = false
		result.Errors = append(result.Errors, ValidationError{
			Field:      "agent.llm.provider",
			Message:    "LLM provider is required",
			Type:       "required_field",
			Suggestion: "Specify LLM provider (anthropic, openai, bedrock, ollama)",
		})
	}

	// LLM model is required
	if config.Agent.LLM.Model == "" {
		result.Valid = false
		result.Errors = append(result.Errors, ValidationError{
			Field:      "agent.llm.model",
			Message:    "LLM model is required",
			Type:       "required_field",
			Suggestion: "Specify model name (e.g., 'claude-3-5-sonnet-20250131')",
		})
	}
}

// validateSystemPrompt validates the system prompt with anti-pattern detection
func (v *AgentValidator) validateSystemPrompt(config *agent.AgentConfigYAML, result *ValidationResult) {
	prompt := config.Agent.SystemPrompt

	// Check length
	if len(prompt) < 50 {
		result.Valid = false
		result.Errors = append(result.Errors, ValidationError{
			Field:      "agent.system_prompt",
			Message:    fmt.Sprintf("System prompt too short (%d chars, minimum 50 chars)", len(prompt)),
			Type:       "length_error",
			Suggestion: "Provide a more detailed system prompt with clear instructions",
		})
	}

	if len(prompt) > 2000 {
		result.Warnings = append(result.Warnings, ValidationWarning{
			Field:   "agent.system_prompt",
			Message: fmt.Sprintf("System prompt very long (%d chars, >2000 chars may increase costs)", len(prompt)),
		})
	}

	// Check for anti-patterns (role-prompting)
	for i, pattern := range v.antiPatterns {
		if matches := pattern.FindAllStringIndex(prompt, -1); len(matches) > 0 {
			for _, match := range matches {
				lineNum := findLineNumber(prompt, match[0])
				matchedText := prompt[match[0]:match[1]]

				result.Valid = false
				result.Errors = append(result.Errors, ValidationError{
					Field:      "agent.system_prompt",
					Message:    fmt.Sprintf("Role-prompting anti-pattern detected (%s): '%s'", v.antiPatternNames[i], matchedText),
					Type:       "anti_pattern",
					Line:       lineNum,
					Suggestion: "Use task-oriented language instead (e.g., 'Analyze...', 'Generate...', 'Extract...', 'Transform...')",
				})
			}
		}
	}

	// Check for hardcoded credentials
	credPatterns := []struct {
		pattern string
		name    string
	}{
		{`password\s*[=:]\s*["'][^"']+["']`, "password"},
		{`api[_-]?key\s*[=:]\s*["'][^"']+["']`, "API key"},
		{`secret\s*[=:]\s*["'][^"']+["']`, "secret"},
		{`token\s*[=:]\s*["'][^"']+["']`, "token"},
	}

	for _, cp := range credPatterns {
		matched, _ := regexp.MatchString(cp.pattern, prompt)
		if matched {
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Field:      "agent.system_prompt",
				Message:    fmt.Sprintf("Hardcoded %s detected in prompt", cp.name),
				Type:       "security_risk",
				Suggestion: "Use environment variables or secret management instead (e.g., ${API_KEY})",
			})
		}
	}

	// Check for task-oriented language
	taskWords := []string{"analyze", "generate", "extract", "transform", "identify", "detect", "calculate", "process", "create", "build"}
	hasTaskOriented := false
	lowerPrompt := strings.ToLower(prompt)
	for _, word := range taskWords {
		if strings.Contains(lowerPrompt, word) {
			hasTaskOriented = true
			break
		}
	}

	if !hasTaskOriented {
		result.Warnings = append(result.Warnings, ValidationWarning{
			Field:   "agent.system_prompt",
			Message: "Prompt may be too generic, consider adding specific task instructions (e.g., 'Analyze SQL queries...', 'Extract data from...')",
		})
	}

	// Check for output format specification
	formatKeywords := []string{"output", "format", "return", "respond", "structure", "json", "yaml", "table"}
	hasOutputFormat := false
	for _, keyword := range formatKeywords {
		if strings.Contains(lowerPrompt, keyword) {
			hasOutputFormat = true
			break
		}
	}

	if !hasOutputFormat {
		result.Warnings = append(result.Warnings, ValidationWarning{
			Field:   "agent.system_prompt",
			Message: "Consider specifying expected output format (e.g., 'Return results as JSON', 'Format output as a table')",
		})
	}
}

// validateLLMConfig validates LLM configuration
func (v *AgentValidator) validateLLMConfig(config *agent.AgentConfigYAML, result *ValidationResult) {
	llm := &config.Agent.LLM

	// Validate provider
	if llm.Provider != "" && !v.validProviders[strings.ToLower(llm.Provider)] {
		result.Valid = false
		validProviders := make([]string, 0, len(v.validProviders))
		for p := range v.validProviders {
			validProviders = append(validProviders, p)
		}
		result.Errors = append(result.Errors, ValidationError{
			Field:      "agent.llm.provider",
			Message:    fmt.Sprintf("Invalid LLM provider: %s", llm.Provider),
			Type:       "invalid_value",
			Suggestion: fmt.Sprintf("Valid providers: %s", strings.Join(validProviders, ", ")),
		})
	}

	// Validate temperature range
	if llm.Temperature < 0.0 || llm.Temperature > 2.0 {
		result.Valid = false
		result.Errors = append(result.Errors, ValidationError{
			Field:      "agent.llm.temperature",
			Message:    fmt.Sprintf("Temperature out of range: %.2f (must be between 0.0 and 2.0)", llm.Temperature),
			Type:       "invalid_value",
			Suggestion: "Use 0.0 for deterministic, 0.7 for balanced, 1.0+ for creative responses",
		})
	}

	// Validate max_tokens
	if llm.MaxTokens < 0 {
		result.Valid = false
		result.Errors = append(result.Errors, ValidationError{
			Field:      "agent.llm.max_tokens",
			Message:    fmt.Sprintf("max_tokens must be positive: %d", llm.MaxTokens),
			Type:       "invalid_value",
			Suggestion: "Use 4096 for most tasks, 8192 for longer responses",
		})
	}

	if llm.MaxTokens > 100000 {
		result.Warnings = append(result.Warnings, ValidationWarning{
			Field:   "agent.llm.max_tokens",
			Message: fmt.Sprintf("max_tokens very high (%d), may increase costs significantly", llm.MaxTokens),
		})
	}

	// Warn about deprecated models (example - expand as needed)
	deprecatedModels := map[string]string{
		"claude-2":     "Use claude-3-5-sonnet-20250131 instead",
		"gpt-3.5":      "Consider gpt-4 for better quality",
		"text-davinci": "Use chat models instead",
	}

	for deprecated, suggestion := range deprecatedModels {
		if strings.Contains(strings.ToLower(llm.Model), deprecated) {
			result.Warnings = append(result.Warnings, ValidationWarning{
				Field:   "agent.llm.model",
				Message: fmt.Sprintf("Model may be deprecated: %s. %s", llm.Model, suggestion),
			})
		}
	}
}

// validateBackends validates backend configurations
func (v *AgentValidator) validateBackends(config *agent.AgentConfigYAML, result *ValidationResult) {
	// Check if there are any MCP tools (which act as backends)
	hasMCPBackend := len(config.Agent.Tools.MCP) > 0
	hasCustomTools := len(config.Agent.Tools.Custom) > 0
	hasBuiltinTools := len(config.Agent.Tools.Builtin) > 0

	if !hasMCPBackend && !hasCustomTools && !hasBuiltinTools {
		result.Warnings = append(result.Warnings, ValidationWarning{
			Field:   "agent.tools",
			Message: "No tools configured, agent may have limited functionality",
		})
	}

	// Validate MCP tool configurations
	for i, mcp := range config.Agent.Tools.MCP {
		if mcp.Server == "" {
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Field:      fmt.Sprintf("agent.tools.mcp[%d].server", i),
				Message:    "MCP server address is required",
				Type:       "required_field",
				Suggestion: "Specify MCP server address (e.g., 'http://localhost:3000')",
			})
		}

		// Validate server URL format
		if mcp.Server != "" {
			if _, err := url.Parse(mcp.Server); err != nil {
				result.Valid = false
				result.Errors = append(result.Errors, ValidationError{
					Field:      fmt.Sprintf("agent.tools.mcp[%d].server", i),
					Message:    fmt.Sprintf("Invalid MCP server URL: %v", err),
					Type:       "format_error",
					Suggestion: "Use valid URL format (e.g., 'http://localhost:3000' or 'unix:///tmp/mcp.sock')",
				})
			}
		}
	}

	// Validate custom tools
	for i, custom := range config.Agent.Tools.Custom {
		if custom.Name == "" {
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Field:      fmt.Sprintf("agent.tools.custom[%d].name", i),
				Message:    "Custom tool name is required",
				Type:       "required_field",
				Suggestion: "Provide a unique name for the custom tool",
			})
		}

		if custom.Implementation == "" {
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Field:      fmt.Sprintf("agent.tools.custom[%d].implementation", i),
				Message:    "Custom tool implementation is required",
				Type:       "required_field",
				Suggestion: "Specify implementation path or reference",
			})
		}
	}
}

// validatePatterns validates pattern references
func (v *AgentValidator) validatePatterns(config *agent.AgentConfigYAML, result *ValidationResult) {
	// Get available patterns from patterns directory
	availablePatterns := v.getAvailablePatterns()

	// Check metadata for pattern references
	if config.Agent.Metadata != nil {
		if patternsVal, ok := config.Agent.Metadata["patterns"]; ok {
			var patterns []string

			// Handle different types
			switch v := patternsVal.(type) {
			case string:
				// Comma-separated string
				patterns = strings.Split(v, ",")
			case []interface{}:
				// List of patterns
				for _, p := range v {
					if str, ok := p.(string); ok {
						patterns = append(patterns, str)
					}
				}
			case []string:
				patterns = v
			}

			for _, pattern := range patterns {
				pattern = strings.TrimSpace(pattern)
				if pattern == "" {
					continue
				}

				// Check if pattern exists
				if !v.patternExists(pattern, availablePatterns) {
					result.Warnings = append(result.Warnings, ValidationWarning{
						Field:   "agent.metadata.patterns",
						Message: fmt.Sprintf("Pattern may not exist: %s", pattern),
					})
				}
			}
		}
	}

	// Warn if no patterns specified (not an error, but may indicate incomplete config)
	hasPatterns := config.Agent.Metadata != nil && config.Agent.Metadata["patterns"] != ""
	if !hasPatterns {
		result.Warnings = append(result.Warnings, ValidationWarning{
			Field:   "agent.metadata.patterns",
			Message: "No patterns specified, consider adding domain-specific patterns for better performance",
		})
	}
}

// validateMemory validates memory configuration
func (v *AgentValidator) validateMemory(config *agent.AgentConfigYAML, result *ValidationResult) {
	mem := &config.Agent.Memory

	// Validate memory type
	if mem.Type != "" && !v.validMemoryTypes[mem.Type] {
		result.Valid = false
		validTypes := make([]string, 0, len(v.validMemoryTypes))
		for t := range v.validMemoryTypes {
			validTypes = append(validTypes, t)
		}
		result.Errors = append(result.Errors, ValidationError{
			Field:      "agent.memory.type",
			Message:    fmt.Sprintf("Invalid memory type: %s", mem.Type),
			Type:       "invalid_value",
			Suggestion: fmt.Sprintf("Valid types: %s", strings.Join(validTypes, ", ")),
		})
	}

	// Validate path/DSN based on type
	if mem.Type == "sqlite" || mem.Type == "postgres" {
		if mem.DSN == "" && mem.Path == "" {
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Field:      "agent.memory",
				Message:    fmt.Sprintf("DSN or path required for %s memory type", mem.Type),
				Type:       "required_field",
				Suggestion: "Specify connection DSN (for postgres) or file path (for sqlite)",
			})
		}

		// Validate SQLite path
		if mem.Type == "sqlite" && mem.Path != "" {
			dir := filepath.Dir(mem.Path)
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				result.Warnings = append(result.Warnings, ValidationWarning{
					Field:   "agent.memory.path",
					Message: fmt.Sprintf("SQLite directory does not exist: %s", dir),
				})
			}
		}

		// Validate Postgres DSN format
		if mem.Type == "postgres" && mem.DSN != "" {
			if !strings.Contains(mem.DSN, "://") {
				result.Warnings = append(result.Warnings, ValidationWarning{
					Field:   "agent.memory.dsn",
					Message: "DSN format may be invalid (expected: postgres://user:pass@host:port/db)",
				})
			}
		}
	}

	// Validate max_history
	if mem.MaxHistory < 0 {
		result.Valid = false
		result.Errors = append(result.Errors, ValidationError{
			Field:      "agent.memory.max_history",
			Message:    fmt.Sprintf("max_history must be non-negative: %d", mem.MaxHistory),
			Type:       "invalid_value",
			Suggestion: "Use 50 for typical conversations, 100+ for longer context",
		})
	}

	if mem.MaxHistory > 1000 {
		result.Warnings = append(result.Warnings, ValidationWarning{
			Field:   "agent.memory.max_history",
			Message: fmt.Sprintf("max_history very high (%d), may increase memory usage and costs", mem.MaxHistory),
		})
	}
}

// compileAntiPatterns compiles regex patterns for role-prompting detection
func compileAntiPatterns() ([]*regexp.Regexp, []string) {
	patterns := []struct {
		name    string
		pattern string
	}{
		{"You are a/an", `(?i)\byou\s+are\s+(a|an)\s+\w+`},
		{"As a/an", `(?i)\bas\s+(a|an)\s+\w+[,\s]`},
		{"Act as", `(?i)\bact\s+as\s+(a|an)?\s*\w+`},
		{"Pretend to be", `(?i)\bpretend\s+to\s+be\s+(a|an)?\s*\w+`},
		{"Imagine you are", `(?i)\bimagine\s+you\s+are\s+(a|an)?\s*\w+`},
		{"Your role is", `(?i)\byour\s+role\s+is\s+(to|a|an)?\s*\w+`},
		{"You will be", `(?i)\byou\s+will\s+be\s+(a|an)?\s*\w+`},
		{"You're a/an", `(?i)\byou're\s+(a|an)\s+\w+`},
	}

	compiled := make([]*regexp.Regexp, len(patterns))
	names := make([]string, len(patterns))

	for i, p := range patterns {
		compiled[i] = regexp.MustCompile(p.pattern)
		names[i] = p.name
	}

	return compiled, names
}

// findLineNumber finds the line number for a given character offset in text
func findLineNumber(text string, offset int) int {
	if offset < 0 || offset >= len(text) {
		return 0
	}

	line := 1
	for i := 0; i < offset; i++ {
		if text[i] == '\n' {
			line++
		}
	}
	return line
}

// getAvailablePatterns returns list of available patterns from patterns directory
func (v *AgentValidator) getAvailablePatterns() []string {
	patterns := []string{}

	// Use the same upward search logic as Generator.findPatternsDir()
	patternsDir, err := v.findPatternsDir()
	if err != nil {
		// Pattern directory not found - return empty list
		// This is a warning, not an error, since patterns are optional
		return patterns
	}

	// Walk the patterns directory
	_ = filepath.Walk(patternsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if !info.IsDir() && strings.HasSuffix(path, ".yaml") {
			// Extract pattern name from path
			relPath, _ := filepath.Rel(patternsDir, path)
			patternName := strings.TrimSuffix(relPath, ".yaml")
			patternName = strings.ReplaceAll(patternName, string(filepath.Separator), "/")
			patterns = append(patterns, patternName)
		}

		return nil
	})

	return patterns
}

// findPatternsDir finds the patterns directory, searching multiple locations
// This matches the logic in generator_patterns.go
func (v *AgentValidator) findPatternsDir() (string, error) {
	// 1. Check user's $LOOM_DATA_DIR/patterns directory first (installed patterns)
	homeDir, err := os.UserHomeDir()
	if err == nil {
		userPatternsDir := filepath.Join(homeDir, ".loom", "patterns")
		if v.isPatternsDir(userPatternsDir) {
			return userPatternsDir, nil
		}
	}

	// 2. Check current working directory (development mode)
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	patternsDir := filepath.Join(cwd, "patterns")
	if v.isPatternsDir(patternsDir) {
		return patternsDir, nil
	}

	// 3. Search upward for patterns directory (for test context and binary execution)
	dir := cwd
	for i := 0; i < 10; i++ { // Limit depth to prevent infinite loop
		patternsDir := filepath.Join(dir, "patterns")
		if v.isPatternsDir(patternsDir) {
			return patternsDir, nil
		}

		// Move up one directory
		parentDir := filepath.Dir(dir)
		if parentDir == dir {
			break // Reached filesystem root
		}
		dir = parentDir
	}

	return "", fmt.Errorf("patterns directory not found (searched $LOOM_DATA_DIR/patterns, %s, and upward)", cwd)
}

// isPatternsDir checks if a directory is the patterns directory with YAML files
func (v *AgentValidator) isPatternsDir(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}

	// Check if directory contains at least one .yaml file (patterns are organized in subdirs)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		if entry.IsDir() {
			// Check subdirectories for YAML files
			subEntries, err := os.ReadDir(filepath.Join(dir, entry.Name()))
			if err != nil {
				continue
			}
			for _, subEntry := range subEntries {
				if !subEntry.IsDir() && strings.HasSuffix(subEntry.Name(), ".yaml") {
					return true
				}
			}
		}
	}

	return false
}

// patternExists checks if a pattern exists in the available patterns list
func (v *AgentValidator) patternExists(pattern string, availablePatterns []string) bool {
	pattern = strings.ToLower(pattern)

	for _, available := range availablePatterns {
		// Check exact match
		if strings.ToLower(available) == pattern {
			return true
		}

		// Check if pattern is contained in available pattern name
		if strings.Contains(strings.ToLower(available), pattern) {
			return true
		}
	}

	return false
}

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"gopkg.in/yaml.v3"
)

// Template-related errors
var (
	ErrTemplateNotFound  = fmt.Errorf("template not found")
	ErrCircularReference = fmt.Errorf("circular template reference detected")
	ErrMissingParameter  = fmt.Errorf("required template parameter missing")
	ErrInvalidParameter  = fmt.Errorf("invalid parameter value")
)

// AgentTemplateConfig represents the YAML structure for agent templates
type AgentTemplateConfig struct {
	APIVersion string                    `yaml:"apiVersion"`
	Kind       string                    `yaml:"kind"`
	Metadata   TemplateMetadata          `yaml:"metadata"`
	Parameters []TemplateParameterConfig `yaml:"parameters,omitempty"`
	Extends    string                    `yaml:"extends,omitempty"`
	Spec       AgentSpec                 `yaml:"spec"`
}

// TemplateMetadata contains template identification
type TemplateMetadata struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description,omitempty"`
	Version     string            `yaml:"version,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"`
}

// TemplateParameterConfig defines a template parameter
type TemplateParameterConfig struct {
	Name         string `yaml:"name"`
	Type         string `yaml:"type"`
	Required     bool   `yaml:"required,omitempty"`
	DefaultValue string `yaml:"default,omitempty"`
	Description  string `yaml:"description,omitempty"`
}

// AgentSpec contains the agent configuration with templatable fields
type AgentSpec struct {
	Name         string                 `yaml:"name,omitempty"`
	Description  string                 `yaml:"description,omitempty"`
	SystemPrompt string                 `yaml:"system_prompt,omitempty"`
	LLM          map[string]interface{} `yaml:"llm,omitempty"`
	Tools        map[string]interface{} `yaml:"tools,omitempty"`
	Memory       map[string]interface{} `yaml:"memory,omitempty"`
	Behavior     map[string]interface{} `yaml:"behavior,omitempty"`
	Metadata     map[string]string      `yaml:"metadata,omitempty"`
}

// UnmarshalYAML implements custom YAML unmarshaling for AgentSpec
// Supports both "config" and "behavior" field names for backward compatibility
func (s *AgentSpec) UnmarshalYAML(value *yaml.Node) error {
	// Define a temporary struct with all fields as interface{}
	type rawSpec struct {
		Name         string                 `yaml:"name,omitempty"`
		Description  string                 `yaml:"description,omitempty"`
		SystemPrompt string                 `yaml:"system_prompt,omitempty"`
		LLM          map[string]interface{} `yaml:"llm,omitempty"`
		Tools        map[string]interface{} `yaml:"tools,omitempty"`
		Memory       map[string]interface{} `yaml:"memory,omitempty"`
		Config       map[string]interface{} `yaml:"config,omitempty"`   // Alias for Behavior
		Behavior     map[string]interface{} `yaml:"behavior,omitempty"` // Canonical name
		Metadata     map[string]string      `yaml:"metadata,omitempty"`
	}

	var raw rawSpec
	if err := value.Decode(&raw); err != nil {
		return err
	}

	// Copy fields
	s.Name = raw.Name
	s.Description = raw.Description
	s.SystemPrompt = raw.SystemPrompt
	s.LLM = raw.LLM
	s.Tools = raw.Tools
	s.Memory = raw.Memory
	s.Metadata = raw.Metadata

	// Support both "config" and "behavior" - "behavior" takes precedence
	if raw.Behavior != nil {
		s.Behavior = raw.Behavior
	} else if raw.Config != nil {
		s.Behavior = raw.Config
	}

	return nil
}

// TemplateRegistry manages agent templates
type TemplateRegistry struct {
	templates map[string]*AgentTemplateConfig
}

// NewTemplateRegistry creates a new template registry
func NewTemplateRegistry() *TemplateRegistry {
	return &TemplateRegistry{
		templates: make(map[string]*AgentTemplateConfig),
	}
}

// LoadTemplate loads a template from a YAML file
func (r *TemplateRegistry) LoadTemplate(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read template file: %w", err)
	}

	var template AgentTemplateConfig
	if err := yaml.Unmarshal(data, &template); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidYAML, err.Error())
	}

	// Validate template structure
	if err := r.validateTemplate(&template); err != nil {
		return err
	}

	// Register template
	r.templates[template.Metadata.Name] = &template
	return nil
}

// LoadTemplateFromString loads a template from YAML string
func (r *TemplateRegistry) LoadTemplateFromString(yamlContent string) error {
	var template AgentTemplateConfig
	if err := yaml.Unmarshal([]byte(yamlContent), &template); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidYAML, err.Error())
	}

	if err := r.validateTemplate(&template); err != nil {
		return err
	}

	r.templates[template.Metadata.Name] = &template
	return nil
}

// ApplyTemplate applies a template with given variables to create an agent config
func (r *TemplateRegistry) ApplyTemplate(templateName string, vars map[string]string) (*loomv1.AgentConfig, error) {
	template, err := r.resolveTemplate(templateName, make(map[string]bool))
	if err != nil {
		return nil, err
	}

	// Merge environment variables
	allVars := make(map[string]string)
	for _, envVar := range os.Environ() {
		parts := strings.SplitN(envVar, "=", 2)
		if len(parts) == 2 {
			allVars[parts[0]] = parts[1]
		}
	}
	for k, v := range vars {
		allVars[k] = v
	}

	// Validate required parameters
	if err := r.validateParameters(template, allVars); err != nil {
		return nil, err
	}

	// Apply variable substitution
	config, err := r.substituteVariables(template, allVars)
	if err != nil {
		return nil, err
	}

	// Convert to proto
	return r.convertToAgentConfig(config)
}

// resolveTemplate resolves a template with inheritance
func (r *TemplateRegistry) resolveTemplate(name string, visited map[string]bool) (*AgentTemplateConfig, error) {
	// Check for cycles
	if visited[name] {
		return nil, fmt.Errorf("%w: %s", ErrCircularReference, name)
	}

	// Get template
	template, ok := r.templates[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrTemplateNotFound, name)
	}

	// No inheritance - return as-is
	if template.Extends == "" {
		return template, nil
	}

	// Resolve parent
	visited[name] = true
	parent, err := r.resolveTemplate(template.Extends, visited)
	if err != nil {
		return nil, err
	}

	// Merge with parent
	merged := r.mergeTemplates(parent, template)
	return merged, nil
}

// mergeMaps merges two maps, with child values overriding parent values
func mergeMaps(parent, child map[string]interface{}) map[string]interface{} {
	if parent == nil && child == nil {
		return nil
	}
	if parent == nil {
		return child
	}
	if child == nil {
		return parent
	}

	// Create result map starting with parent
	result := make(map[string]interface{})
	for k, v := range parent {
		result[k] = v
	}

	// Override with child values
	for k, v := range child {
		result[k] = v
	}

	return result
}

// mergeTemplates merges child template with parent (child overrides parent)
func (r *TemplateRegistry) mergeTemplates(parent, child *AgentTemplateConfig) *AgentTemplateConfig {
	result := &AgentTemplateConfig{
		APIVersion: child.APIVersion,
		Kind:       child.Kind,
		Metadata:   child.Metadata,
		Parameters: append(parent.Parameters, child.Parameters...),
		Extends:    "", // Clear extends after resolution
		Spec:       child.Spec,
	}

	// Merge spec fields (child overrides parent)
	if child.Spec.Name == "" && parent.Spec.Name != "" {
		result.Spec.Name = parent.Spec.Name
	}
	if child.Spec.Description == "" && parent.Spec.Description != "" {
		result.Spec.Description = parent.Spec.Description
	}
	if child.Spec.SystemPrompt == "" && parent.Spec.SystemPrompt != "" {
		result.Spec.SystemPrompt = parent.Spec.SystemPrompt
	}

	// Deep merge maps (child overrides parent for each key)
	result.Spec.LLM = mergeMaps(parent.Spec.LLM, child.Spec.LLM)
	result.Spec.Tools = mergeMaps(parent.Spec.Tools, child.Spec.Tools)
	result.Spec.Memory = mergeMaps(parent.Spec.Memory, child.Spec.Memory)
	result.Spec.Behavior = mergeMaps(parent.Spec.Behavior, child.Spec.Behavior)

	// Merge metadata
	if result.Spec.Metadata == nil {
		result.Spec.Metadata = make(map[string]string)
	}
	for k, v := range parent.Spec.Metadata {
		if _, exists := result.Spec.Metadata[k]; !exists {
			result.Spec.Metadata[k] = v
		}
	}

	return result
}

// validateTemplate validates template structure
func (r *TemplateRegistry) validateTemplate(template *AgentTemplateConfig) error {
	// Check apiVersion
	if template.APIVersion != "loom/v1" {
		return fmt.Errorf("%w: unsupported apiVersion '%s'", ErrInvalidWorkflow, template.APIVersion)
	}

	// Check kind
	if template.Kind != "AgentTemplate" && template.Kind != "Agent" {
		return fmt.Errorf("%w: unsupported kind '%s', expected 'AgentTemplate' or 'Agent'", ErrInvalidWorkflow, template.Kind)
	}

	// Check metadata
	if template.Metadata.Name == "" {
		return fmt.Errorf("%w: missing metadata.name", ErrInvalidWorkflow)
	}

	return nil
}

// validateParameters validates required parameters are provided
func (r *TemplateRegistry) validateParameters(template *AgentTemplateConfig, vars map[string]string) error {
	for _, param := range template.Parameters {
		if param.Required {
			if _, exists := vars[param.Name]; !exists {
				return fmt.Errorf("%w: parameter '%s' is required", ErrMissingParameter, param.Name)
			}
		}
	}
	return nil
}

// substituteVariables performs variable substitution in template
func (r *TemplateRegistry) substituteVariables(template *AgentTemplateConfig, vars map[string]string) (*AgentSpec, error) {
	// Apply defaults for missing variables
	finalVars := make(map[string]string)
	for k, v := range vars {
		finalVars[k] = v
	}

	for _, param := range template.Parameters {
		if _, exists := finalVars[param.Name]; !exists && param.DefaultValue != "" {
			finalVars[param.Name] = param.DefaultValue
		}
	}

	// Deep clone spec to avoid modifying the template
	spec := AgentSpec{
		Name:         r.substitute(template.Spec.Name, finalVars),
		Description:  r.substitute(template.Spec.Description, finalVars),
		SystemPrompt: r.substitute(template.Spec.SystemPrompt, finalVars),
		LLM:          r.substituteInMap(template.Spec.LLM, finalVars),
		Tools:        r.substituteInMap(template.Spec.Tools, finalVars),
		Memory:       r.substituteInMap(template.Spec.Memory, finalVars),
		Behavior:     r.substituteInMap(template.Spec.Behavior, finalVars),
	}

	// Clone and substitute metadata
	if template.Spec.Metadata != nil {
		spec.Metadata = make(map[string]string)
		for k, v := range template.Spec.Metadata {
			spec.Metadata[k] = r.substitute(v, finalVars)
		}
	}

	return &spec, nil
}

// substituteInMap recursively substitutes variables in a map[string]interface{}
func (r *TemplateRegistry) substituteInMap(m map[string]interface{}, vars map[string]string) map[string]interface{} {
	if m == nil {
		return nil
	}

	result := make(map[string]interface{})
	for k, v := range m {
		switch val := v.(type) {
		case string:
			// Substitute in string values
			result[k] = r.substitute(val, vars)
		case map[string]interface{}:
			// Recursively substitute in nested maps
			result[k] = r.substituteInMap(val, vars)
		case []interface{}:
			// Substitute in array elements
			result[k] = r.substituteInArray(val, vars)
		default:
			// Keep other types as-is
			result[k] = v
		}
	}
	return result
}

// substituteInArray substitutes variables in array elements
func (r *TemplateRegistry) substituteInArray(arr []interface{}, vars map[string]string) []interface{} {
	result := make([]interface{}, len(arr))
	for i, v := range arr {
		switch val := v.(type) {
		case string:
			result[i] = r.substitute(val, vars)
		case map[string]interface{}:
			result[i] = r.substituteInMap(val, vars)
		case []interface{}:
			result[i] = r.substituteInArray(val, vars)
		default:
			result[i] = v
		}
	}
	return result
}

// substitute replaces variables in a string using {{var}} or ${var} syntax
func (r *TemplateRegistry) substitute(text string, vars map[string]string) string {
	// Support both {{var}} and ${var} syntax
	result := text

	// Replace {{var}}
	re1 := regexp.MustCompile(`\{\{(\w+)\}\}`)
	result = re1.ReplaceAllStringFunc(result, func(match string) string {
		varName := re1.FindStringSubmatch(match)[1]
		if val, ok := vars[varName]; ok {
			return val
		}
		return match // Keep original if not found
	})

	// Replace ${var}
	re2 := regexp.MustCompile(`\$\{(\w+)\}`)
	result = re2.ReplaceAllStringFunc(result, func(match string) string {
		varName := re2.FindStringSubmatch(match)[1]
		if val, ok := vars[varName]; ok {
			return val
		}
		return match // Keep original if not found
	})

	return result
}

// convertToAgentConfig converts AgentSpec to proto AgentConfig
func (r *TemplateRegistry) convertToAgentConfig(spec *AgentSpec) (*loomv1.AgentConfig, error) {
	config := &loomv1.AgentConfig{
		Name:         spec.Name,
		Description:  spec.Description,
		SystemPrompt: spec.SystemPrompt,
		Metadata:     spec.Metadata,
	}

	// Convert LLM config
	if spec.LLM != nil {
		config.Llm = &loomv1.LLMConfig{
			Provider:    getStringValue(spec.LLM, "provider"),
			Model:       getStringValue(spec.LLM, "model"),
			Temperature: getFloat32Value(spec.LLM, "temperature"),
			MaxTokens:   getInt32Value(spec.LLM, "max_tokens"),
			TopP:        getFloat32Value(spec.LLM, "top_p"),
			TopK:        getInt32Value(spec.LLM, "top_k"),
		}

		// Handle stop_sequences
		if stopSeqRaw, ok := spec.LLM["stop_sequences"].([]interface{}); ok {
			stopSeq := make([]string, 0, len(stopSeqRaw))
			for _, s := range stopSeqRaw {
				if str, ok := s.(string); ok {
					stopSeq = append(stopSeq, str)
				}
			}
			config.Llm.StopSequences = stopSeq
		}
	}

	// Convert Tools config
	if spec.Tools != nil {
		config.Tools = &loomv1.ToolsConfig{}

		// Handle builtin tools
		if builtinRaw, ok := spec.Tools["builtin"].([]interface{}); ok {
			builtin := make([]string, 0, len(builtinRaw))
			for _, b := range builtinRaw {
				if str, ok := b.(string); ok {
					builtin = append(builtin, str)
				}
			}
			config.Tools.Builtin = builtin
		}

		// Handle MCP tools
		if mcpRaw, ok := spec.Tools["mcp"].([]interface{}); ok {
			mcpTools := make([]*loomv1.MCPToolConfig, 0, len(mcpRaw))
			for _, m := range mcpRaw {
				if mcpMap, ok := m.(map[string]interface{}); ok {
					mcpTool := &loomv1.MCPToolConfig{
						Server: getStringValue(mcpMap, "server"),
					}
					if toolsRaw, ok := mcpMap["tools"].([]interface{}); ok {
						tools := make([]string, 0, len(toolsRaw))
						for _, t := range toolsRaw {
							if str, ok := t.(string); ok {
								tools = append(tools, str)
							}
						}
						mcpTool.Tools = tools
					}
					mcpTools = append(mcpTools, mcpTool)
				}
			}
			config.Tools.Mcp = mcpTools
		}

		// Handle custom tools
		if customRaw, ok := spec.Tools["custom"].([]interface{}); ok {
			customTools := make([]*loomv1.CustomToolConfig, 0, len(customRaw))
			for _, c := range customRaw {
				if customMap, ok := c.(map[string]interface{}); ok {
					customTools = append(customTools, &loomv1.CustomToolConfig{
						Name:           getStringValue(customMap, "name"),
						Implementation: getStringValue(customMap, "implementation"),
					})
				}
			}
			config.Tools.Custom = customTools
		}
	}

	// Convert Memory config
	if spec.Memory != nil {
		config.Memory = &loomv1.MemoryConfig{
			Type:       getStringValue(spec.Memory, "type"),
			Path:       getStringValue(spec.Memory, "path"),
			Dsn:        getStringValue(spec.Memory, "dsn"),
			MaxHistory: getInt32Value(spec.Memory, "max_history"),
		}
	}

	// Convert Behavior config
	if spec.Behavior != nil {
		config.Behavior = &loomv1.BehaviorConfig{
			MaxIterations:      getInt32Value(spec.Behavior, "max_iterations"),
			TimeoutSeconds:     getInt32Value(spec.Behavior, "timeout_seconds"),
			AllowCodeExecution: getBoolValue(spec.Behavior, "allow_code_execution"),
			MaxTurns:           getInt32Value(spec.Behavior, "max_turns"),
			MaxToolExecutions:  getInt32Value(spec.Behavior, "max_tool_executions"),
		}

		// Handle allowed_domains
		if domainsRaw, ok := spec.Behavior["allowed_domains"].([]interface{}); ok {
			domains := make([]string, 0, len(domainsRaw))
			for _, d := range domainsRaw {
				if str, ok := d.(string); ok {
					domains = append(domains, str)
				}
			}
			config.Behavior.AllowedDomains = domains
		}
	}

	return config, nil
}

// Helper functions to extract typed values from map[string]interface{}
func getStringValue(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getIntValue(m map[string]interface{}, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

// getInt32Value safely extracts an int32 value from a map
func getInt32Value(m map[string]interface{}, key string) int32 {
	switch v := m[key].(type) {
	case int:
		// Safe conversion with bounds check
		if v > 2147483647 || v < -2147483648 {
			return 0 // Out of int32 range
		}
		return int32(v)
	case int32:
		return v
	case int64:
		// Safe conversion with bounds check
		if v > 2147483647 || v < -2147483648 {
			return 0 // Out of int32 range
		}
		return int32(v)
	case float64:
		// Safe conversion with bounds check
		if v > 2147483647 || v < -2147483648 {
			return 0 // Out of int32 range
		}
		return int32(v)
	}
	return 0
}

// getFloat32Value safely extracts a float32 value from a map
func getFloat32Value(m map[string]interface{}, key string) float32 {
	switch v := m[key].(type) {
	case float64:
		return float32(v)
	case float32:
		return v
	case int:
		return float32(v)
	}
	return 0.0
}

func getFloatValue(m map[string]interface{}, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	}
	return 0.0
}

func getBoolValue(m map[string]interface{}, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

// LoadAgentFromTemplate loads an agent config from a template file with variables
func LoadAgentFromTemplate(templatePath string, vars map[string]string) (*loomv1.AgentConfig, error) {
	registry := NewTemplateRegistry()
	if err := registry.LoadTemplate(templatePath); err != nil {
		return nil, err
	}

	// Extract template name from file
	data, _ := os.ReadFile(templatePath)
	var temp AgentTemplateConfig
	if err := yaml.Unmarshal(data, &temp); err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}

	return registry.ApplyTemplate(temp.Metadata.Name, vars)
}

// GetTemplate returns a registered template
func (r *TemplateRegistry) GetTemplate(name string) (*AgentTemplateConfig, error) {
	template, ok := r.templates[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrTemplateNotFound, name)
	}
	return template, nil
}

// ListTemplates returns all registered template names
func (r *TemplateRegistry) ListTemplates() []string {
	names := make([]string, 0, len(r.templates))
	for name := range r.templates {
		names = append(names, name)
	}
	return names
}

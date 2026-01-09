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
package templates

import (
	"embed"
	"fmt"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed *.yaml
var templateFS embed.FS

// Template represents an agent configuration template
type Template struct {
	Name         string        `yaml:"-"` // Derived from filename
	Content      string        `yaml:"-"` // Raw YAML content
	Agent        AgentTemplate `yaml:"agent"`
	Variables    []string      `yaml:"-"` // Extracted from {{...}} placeholders
	Domain       string        `yaml:"-"` // From agent.metadata.domain
	Capabilities []string      `yaml:"-"` // From agent.metadata.capabilities
	Patterns     []string      `yaml:"-"` // From agent.metadata.patterns
}

// AgentTemplate represents the agent configuration in template
type AgentTemplate struct {
	Name         string                 `yaml:"name"`
	Description  string                 `yaml:"description"`
	BackendPath  string                 `yaml:"backend_path,omitempty"` // Path to backend configuration
	LLM          LLMTemplate            `yaml:"llm"`
	SystemPrompt string                 `yaml:"system_prompt"`
	Tools        ToolsTemplate          `yaml:"tools"`
	Memory       MemoryTemplate         `yaml:"memory"`
	Behavior     BehaviorTemplate       `yaml:"behavior"`
	Metadata     map[string]interface{} `yaml:"metadata"`
}

// LLMTemplate represents LLM configuration
type LLMTemplate struct {
	Provider    string  `yaml:"provider"`
	Model       string  `yaml:"model"`
	Temperature float64 `yaml:"temperature"`
	MaxTokens   int     `yaml:"max_tokens"`
	TopP        float64 `yaml:"top_p"`
}

// ToolsTemplate represents tools configuration
type ToolsTemplate struct {
	Builtin []string      `yaml:"builtin"`
	MCP     []interface{} `yaml:"mcp"`
	Custom  []interface{} `yaml:"custom"`
}

// MemoryTemplate represents memory configuration
type MemoryTemplate struct {
	Type       string `yaml:"type"`
	Path       string `yaml:"path"`
	MaxHistory int    `yaml:"max_history"`
}

// BehaviorTemplate represents behavior configuration
type BehaviorTemplate struct {
	MaxIterations      int      `yaml:"max_iterations"`
	TimeoutSeconds     int      `yaml:"timeout_seconds"`
	AllowCodeExecution bool     `yaml:"allow_code_execution"`
	AllowedDomains     []string `yaml:"allowed_domains"`
}

// Registry manages built-in agent templates
type Registry struct {
	mu        sync.RWMutex
	templates map[string]*Template
}

// NewRegistry creates a new template registry and loads all embedded templates
func NewRegistry() (*Registry, error) {
	r := &Registry{
		templates: make(map[string]*Template),
	}

	if err := r.loadTemplates(); err != nil {
		return nil, fmt.Errorf("failed to load templates: %w", err)
	}

	return r, nil
}

// loadTemplates loads all embedded YAML templates
func (r *Registry) loadTemplates() error {
	entries, err := templateFS.ReadDir(".")
	if err != nil {
		return fmt.Errorf("failed to read template directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		// Read template file
		content, err := templateFS.ReadFile(entry.Name())
		if err != nil {
			return fmt.Errorf("failed to read template %s: %w", entry.Name(), err)
		}

		// Parse template
		tmpl := &Template{
			Name:    strings.TrimSuffix(entry.Name(), ".yaml"),
			Content: string(content),
		}

		if err := yaml.Unmarshal(content, tmpl); err != nil {
			return fmt.Errorf("failed to parse template %s: %w", entry.Name(), err)
		}

		// Extract metadata
		r.extractMetadata(tmpl)

		// Extract variables
		tmpl.Variables = extractVariables(string(content))

		// Store template
		r.templates[tmpl.Name] = tmpl
	}

	return nil
}

// extractMetadata extracts metadata fields from template
func (r *Registry) extractMetadata(tmpl *Template) {
	if tmpl.Agent.Metadata != nil {
		if domain, ok := tmpl.Agent.Metadata["domain"].(string); ok {
			tmpl.Domain = domain
		}
		if caps, ok := tmpl.Agent.Metadata["capabilities"].([]interface{}); ok {
			tmpl.Capabilities = interfaceSliceToStringSlice(caps)
		}
		if patterns, ok := tmpl.Agent.Metadata["patterns"].([]interface{}); ok {
			tmpl.Patterns = interfaceSliceToStringSlice(patterns)
		}
	}
}

// Get retrieves a template by name
func (r *Registry) Get(name string) (*Template, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tmpl, ok := r.templates[name]
	if !ok {
		return nil, fmt.Errorf("template not found: %s", name)
	}

	return tmpl, nil
}

// List returns all available template names
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.templates))
	for name := range r.templates {
		names = append(names, name)
	}
	return names
}

// ListByDomain returns templates matching the given domain
func (r *Registry) ListByDomain(domain string) []*Template {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*Template
	for _, tmpl := range r.templates {
		if tmpl.Domain == domain {
			result = append(result, tmpl)
		}
	}
	return result
}

// GetAll returns all templates
func (r *Registry) GetAll() []*Template {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Template, 0, len(r.templates))
	for _, tmpl := range r.templates {
		result = append(result, tmpl)
	}
	return result
}

// extractVariables extracts {{variable}} placeholders from template content
func extractVariables(content string) []string {
	vars := make(map[string]bool)

	// Simple regex-like extraction (find {{...}})
	for i := 0; i < len(content); i++ {
		if i+1 < len(content) && content[i] == '{' && content[i+1] == '{' {
			// Find closing }}
			start := i + 2
			for j := start; j < len(content)-1; j++ {
				if content[j] == '}' && content[j+1] == '}' {
					varName := strings.TrimSpace(content[start:j])
					vars[varName] = true
					i = j + 1
					break
				}
			}
		}
	}

	// Convert map to slice
	result := make([]string, 0, len(vars))
	for v := range vars {
		result = append(result, v)
	}
	return result
}

// interfaceSliceToStringSlice converts []interface{} to []string
func interfaceSliceToStringSlice(input []interface{}) []string {
	result := make([]string, 0, len(input))
	for _, v := range input {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

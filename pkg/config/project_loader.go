// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"gopkg.in/yaml.v3"
)

// ProjectYAML represents the YAML structure for project configuration
type ProjectYAML struct {
	APIVersion string              `yaml:"apiVersion"`
	Kind       string              `yaml:"kind"`
	Metadata   ProjectMetadataYAML `yaml:"metadata"`
	Spec       ProjectSpecYAML     `yaml:"spec"`
}

type ProjectMetadataYAML struct {
	Name        string            `yaml:"name"`
	Version     string            `yaml:"version"`
	Description string            `yaml:"description"`
	Labels      map[string]string `yaml:"labels"`
}

type ProjectSpecYAML struct {
	Observability ObservabilityConfigYAML `yaml:"observability"`
	Prompts       PromptsConfigYAML       `yaml:"prompts"`
	MCP           MCPConfigYAML           `yaml:"mcp"`
	Backends      []BackendReferenceYAML  `yaml:"backends"`
	Agents        []AgentReferenceYAML    `yaml:"agents"`
	Workflows     []WorkflowReferenceYAML `yaml:"workflows"`
	Evals         []EvalReferenceYAML     `yaml:"evals"`
	Patterns      []PatternReferenceYAML  `yaml:"patterns"`
	Settings      GlobalSettingsYAML      `yaml:"settings"`
}

type ObservabilityConfigYAML struct {
	Enabled       bool              `yaml:"enabled"`
	HawkEndpoint  string            `yaml:"hawk_endpoint"`
	ExportTraces  bool              `yaml:"export_traces"`
	ExportMetrics bool              `yaml:"export_metrics"`
	Tags          map[string]string `yaml:"tags"`
}

type PromptsConfigYAML struct {
	Provider        string `yaml:"provider"`
	Endpoint        string `yaml:"endpoint"`
	CacheEnabled    bool   `yaml:"cache_enabled"`
	CacheTTLSeconds int    `yaml:"cache_ttl_seconds"`
}

type MCPConfigYAML struct {
	ConfigFile string                `yaml:"config_file"`
	Inline     *MCPServersConfigYAML `yaml:"inline"`
}

type MCPServersConfigYAML struct {
	Servers map[string]MCPServerConfigYAML `yaml:"servers"`
}

type MCPServerConfigYAML struct {
	Enabled        bool                 `yaml:"enabled"`
	Transport      string               `yaml:"transport"`
	Command        string               `yaml:"command"`
	Args           []string             `yaml:"args"`
	TimeoutSeconds int                  `yaml:"timeout_seconds"`
	Tools          MCPToolSelectionYAML `yaml:"tools"`
	Env            map[string]string    `yaml:"env"`
}

type MCPToolSelectionYAML struct {
	All     bool     `yaml:"all"`
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

type BackendReferenceYAML struct {
	ConfigFile string `yaml:"config_file"`
}

type AgentReferenceYAML struct {
	ConfigFile string `yaml:"config_file"`
}

type WorkflowReferenceYAML struct {
	ConfigFile string `yaml:"config_file"`
}

type EvalReferenceYAML struct {
	ConfigFile string `yaml:"config_file"`
}

type PatternReferenceYAML struct {
	ConfigFile string `yaml:"config_file"`
}

type GlobalSettingsYAML struct {
	DefaultTimeoutSeconds int    `yaml:"default_timeout_seconds"`
	MaxConcurrentAgents   int    `yaml:"max_concurrent_agents"`
	DebugMode             bool   `yaml:"debug_mode"`
	LogLevel              string `yaml:"log_level"`
}

// LoadProject loads a project configuration from a YAML file
func LoadProject(path string) (*loomv1.Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read project file %s: %w", path, err)
	}

	// Expand environment variables
	dataStr := expandEnvVars(string(data))

	var yamlConfig ProjectYAML
	if err := yaml.Unmarshal([]byte(dataStr), &yamlConfig); err != nil {
		return nil, fmt.Errorf("failed to parse project YAML: %w", err)
	}

	// Validate structure
	if err := validateProjectYAML(&yamlConfig); err != nil {
		return nil, fmt.Errorf("invalid project config: %w", err)
	}

	// Convert to proto
	project := yamlToProtoProject(&yamlConfig)

	// Resolve file paths (make them absolute based on project file location)
	projectDir := filepath.Dir(path)
	if err := resolveFilePaths(project, projectDir); err != nil {
		return nil, fmt.Errorf("failed to resolve file paths: %w", err)
	}

	return project, nil
}

// validateProjectYAML validates the YAML structure (basic validation during parsing)
func validateProjectYAML(yaml *ProjectYAML) error {
	if yaml.APIVersion == "" {
		return fmt.Errorf("apiVersion is required")
	}
	if yaml.APIVersion != "loom/v1" {
		return fmt.Errorf("unsupported apiVersion: %s (expected: loom/v1)", yaml.APIVersion)
	}
	if yaml.Kind != "Project" {
		return fmt.Errorf("kind must be 'Project', got: %s", yaml.Kind)
	}
	if yaml.Metadata.Name == "" {
		return fmt.Errorf("metadata.name is required")
	}
	return nil
}

// yamlToProtoProject converts YAML to proto
func yamlToProtoProject(yaml *ProjectYAML) *loomv1.Project {
	project := &loomv1.Project{
		Metadata: &loomv1.ProjectMetadata{
			Name:        yaml.Metadata.Name,
			Version:     yaml.Metadata.Version,
			Description: yaml.Metadata.Description,
			Labels:      yaml.Metadata.Labels,
		},
		Spec: &loomv1.ProjectSpec{},
	}

	// Observability
	project.Spec.Observability = &loomv1.ObservabilityConfig{
		Enabled:       yaml.Spec.Observability.Enabled,
		HawkEndpoint:  yaml.Spec.Observability.HawkEndpoint,
		ExportTraces:  yaml.Spec.Observability.ExportTraces,
		ExportMetrics: yaml.Spec.Observability.ExportMetrics,
		Tags:          yaml.Spec.Observability.Tags,
	}

	// Prompts
	project.Spec.Prompts = &loomv1.PromptsConfig{
		Provider:        yaml.Spec.Prompts.Provider,
		Endpoint:        yaml.Spec.Prompts.Endpoint,
		CacheEnabled:    yaml.Spec.Prompts.CacheEnabled,
		CacheTtlSeconds: int32(yaml.Spec.Prompts.CacheTTLSeconds),
	}

	// MCP configuration
	if yaml.Spec.MCP.ConfigFile != "" {
		project.Spec.McpConfig = &loomv1.ProjectSpec_McpConfigFile{
			McpConfigFile: yaml.Spec.MCP.ConfigFile,
		}
	} else if yaml.Spec.MCP.Inline != nil {
		mcpConfig := convertMCPConfig(yaml.Spec.MCP.Inline)
		project.Spec.McpConfig = &loomv1.ProjectSpec_McpInline{
			McpInline: mcpConfig,
		}
	}

	// Backends
	for _, backend := range yaml.Spec.Backends {
		project.Spec.Backends = append(project.Spec.Backends, &loomv1.BackendReference{
			Ref: &loomv1.BackendReference_ConfigFile{
				ConfigFile: backend.ConfigFile,
			},
		})
	}

	// Agents
	for _, agent := range yaml.Spec.Agents {
		project.Spec.Agents = append(project.Spec.Agents, &loomv1.AgentReference{
			ConfigFile: agent.ConfigFile,
		})
	}

	// Workflows
	for _, workflow := range yaml.Spec.Workflows {
		project.Spec.Workflows = append(project.Spec.Workflows, &loomv1.WorkflowReference{
			ConfigFile: workflow.ConfigFile,
		})
	}

	// Evals
	for _, eval := range yaml.Spec.Evals {
		project.Spec.Evals = append(project.Spec.Evals, &loomv1.EvalReference{
			ConfigFile: eval.ConfigFile,
		})
	}

	// Patterns
	for _, pattern := range yaml.Spec.Patterns {
		project.Spec.Patterns = append(project.Spec.Patterns, &loomv1.PatternReference{
			ConfigFile: pattern.ConfigFile,
		})
	}

	// Global settings
	project.Spec.Settings = &loomv1.GlobalSettings{
		DefaultTimeoutSeconds: int32(yaml.Spec.Settings.DefaultTimeoutSeconds), // #nosec G115 -- config value bounded in practice
		MaxConcurrentAgents:   int32(yaml.Spec.Settings.MaxConcurrentAgents),   // #nosec G115 -- config value bounded in practice
		DebugMode:             yaml.Spec.Settings.DebugMode,
		LogLevel:              yaml.Spec.Settings.LogLevel,
	}

	// Set defaults
	if project.Spec.Settings.DefaultTimeoutSeconds == 0 {
		project.Spec.Settings.DefaultTimeoutSeconds = 300
	}
	if project.Spec.Settings.MaxConcurrentAgents == 0 {
		project.Spec.Settings.MaxConcurrentAgents = 10
	}
	if project.Spec.Settings.LogLevel == "" {
		project.Spec.Settings.LogLevel = "info"
	}

	return project
}

// convertMCPConfig converts MCP YAML to proto
func convertMCPConfig(yaml *MCPServersConfigYAML) *loomv1.MCPServersConfig {
	config := &loomv1.MCPServersConfig{
		Servers: make(map[string]*loomv1.MCPServerConfig),
	}

	for name, server := range yaml.Servers {
		config.Servers[name] = &loomv1.MCPServerConfig{
			Enabled:        server.Enabled,
			Transport:      server.Transport,
			Command:        server.Command,
			Args:           server.Args,
			TimeoutSeconds: int32(server.TimeoutSeconds),
			Env:            server.Env,
		}

		// Tool selection
		if server.Tools.All {
			config.Servers[name].Tools = &loomv1.MCPToolSelection{
				Selection: &loomv1.MCPToolSelection_All{All: true},
			}
		} else if len(server.Tools.Include) > 0 {
			config.Servers[name].Tools = &loomv1.MCPToolSelection{
				Selection: &loomv1.MCPToolSelection_Include{
					Include: &loomv1.MCPToolInclude{Tools: server.Tools.Include},
				},
			}
		} else if len(server.Tools.Exclude) > 0 {
			config.Servers[name].Tools = &loomv1.MCPToolSelection{
				Selection: &loomv1.MCPToolSelection_Exclude{
					Exclude: &loomv1.MCPToolExclude{Tools: server.Tools.Exclude},
				},
			}
		}
	}

	return config
}

// resolveFilePaths makes all file paths absolute relative to project directory
func resolveFilePaths(project *loomv1.Project, projectDir string) error {
	// Resolve MCP config file
	if mcpFile := project.Spec.GetMcpConfigFile(); mcpFile != "" {
		project.Spec.McpConfig = &loomv1.ProjectSpec_McpConfigFile{
			McpConfigFile: resolveRelativePath(projectDir, mcpFile),
		}
	}

	// Resolve backend config files
	for _, backend := range project.Spec.Backends {
		if file := backend.GetConfigFile(); file != "" {
			backend.Ref = &loomv1.BackendReference_ConfigFile{
				ConfigFile: resolveRelativePath(projectDir, file),
			}
		}
	}

	// Resolve agent config files
	for _, agent := range project.Spec.Agents {
		agent.ConfigFile = resolveRelativePath(projectDir, agent.ConfigFile)
	}

	// Resolve workflow config files
	for _, workflow := range project.Spec.Workflows {
		workflow.ConfigFile = resolveRelativePath(projectDir, workflow.ConfigFile)
	}

	// Resolve eval config files
	for _, eval := range project.Spec.Evals {
		eval.ConfigFile = resolveRelativePath(projectDir, eval.ConfigFile)
	}

	// Resolve pattern config files
	for _, pattern := range project.Spec.Patterns {
		pattern.ConfigFile = resolveRelativePath(projectDir, pattern.ConfigFile)
	}

	return nil
}

// resolveRelativePath resolves a relative path to absolute
func resolveRelativePath(baseDir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir, path)
}

// expandEnvVars expands environment variables in YAML content
func expandEnvVars(s string) string {
	return os.Expand(s, func(key string) string {
		return os.Getenv(key)
	})
}

// ValidateProject validates a project configuration
func ValidateProject(project *loomv1.Project) error {
	if project.Metadata == nil {
		return fmt.Errorf("project metadata is required")
	}
	if project.Metadata.Name == "" {
		return fmt.Errorf("project name is required")
	}
	if project.Spec == nil {
		return fmt.Errorf("project spec is required")
	}

	// Validate log level
	if project.Spec.Settings != nil {
		validLogLevels := map[string]bool{
			"debug": true,
			"info":  true,
			"warn":  true,
			"error": true,
		}
		if project.Spec.Settings.LogLevel != "" && !validLogLevels[strings.ToLower(project.Spec.Settings.LogLevel)] {
			return fmt.Errorf("invalid log level: %s (must be: debug, info, warn, error)", project.Spec.Settings.LogLevel)
		}
	}

	// Validate MCP configuration
	if err := validateMCPConfiguration(project.Spec); err != nil {
		return err
	}

	// Validate referenced files exist
	if err := validateReferencedFiles(project); err != nil {
		return err
	}

	return nil
}

// validateMCPConfiguration validates MCP server configurations
func validateMCPConfiguration(spec *loomv1.ProjectSpec) error {
	// Get inline MCP config if present
	inline := spec.GetMcpInline()
	if inline == nil {
		return nil // No inline MCP config to validate
	}

	if inline.Servers == nil {
		return nil // No servers defined
	}

	validTransports := map[string]bool{
		"stdio": true,
		"sse":   true,
		"http":  true,
	}

	for serverName, server := range inline.Servers {
		if server.Transport != "" && !validTransports[strings.ToLower(server.Transport)] {
			return fmt.Errorf("invalid MCP transport for server '%s': %s (valid transports: stdio, sse, http)", serverName, server.Transport)
		}
	}

	return nil
}

// validateReferencedFiles checks that all referenced files exist
func validateReferencedFiles(project *loomv1.Project) error {
	// Check MCP config file
	if mcpFile := project.Spec.GetMcpConfigFile(); mcpFile != "" {
		if err := checkFileExists(mcpFile); err != nil {
			return fmt.Errorf("MCP config file: %w", err)
		}
	}

	// Check backend config files
	for i, backend := range project.Spec.Backends {
		if file := backend.GetConfigFile(); file != "" {
			if err := checkFileExists(file); err != nil {
				return fmt.Errorf("backend[%d] config file: %w", i, err)
			}
		}
	}

	// Check agent config files
	for i, agent := range project.Spec.Agents {
		if err := checkFileExists(agent.ConfigFile); err != nil {
			return fmt.Errorf("agent[%d] config file: %w", i, err)
		}
	}

	// Check workflow config files
	for i, workflow := range project.Spec.Workflows {
		if err := checkFileExists(workflow.ConfigFile); err != nil {
			return fmt.Errorf("workflow[%d] config file: %w", i, err)
		}
	}

	// Check eval config files
	for i, eval := range project.Spec.Evals {
		if err := checkFileExists(eval.ConfigFile); err != nil {
			return fmt.Errorf("eval[%d] config file: %w", i, err)
		}
	}

	// Check pattern config files
	for i, pattern := range project.Spec.Patterns {
		if err := checkFileExists(pattern.ConfigFile); err != nil {
			return fmt.Errorf("pattern[%d] config file: %w", i, err)
		}
	}

	return nil
}

// checkFileExists checks if a file exists
func checkFileExists(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("file not found: %s", path)
	} else if err != nil {
		return fmt.Errorf("failed to access file %s: %w", path, err)
	}
	return nil
}

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"gopkg.in/yaml.v3"
)

// safeInt32 safely converts an int to int32 with bounds checking.
// Returns an error if the value is outside the int32 range.
func safeInt32(val int, fieldName string) (int32, error) {
	if val > math.MaxInt32 {
		return 0, fmt.Errorf("%s value %d exceeds maximum int32 value (%d)", fieldName, val, math.MaxInt32)
	}
	if val < math.MinInt32 {
		return 0, fmt.Errorf("%s value %d is below minimum int32 value (%d)", fieldName, val, math.MinInt32)
	}
	return int32(val), nil
}

// AgentConfigYAML represents the YAML structure for agent configuration.
// This struct mirrors the proto AgentConfig but uses YAML-friendly types.
// Legacy format with "agent:" as root key.
type AgentConfigYAML struct {
	Agent struct {
		Name         string                 `yaml:"name"`
		Description  string                 `yaml:"description"`
		BackendPath  string                 `yaml:"backend_path"`
		LLM          LLMConfigYAML          `yaml:"llm"`
		SystemPrompt string                 `yaml:"system_prompt"`
		ROM          string                 `yaml:"rom"` // ROM identifier: "TD", "teradata", "auto", or ""
		Tools        ToolsConfigYAML        `yaml:"tools"`
		Memory       MemoryConfigYAML       `yaml:"memory"`
		Behavior     BehaviorConfigYAML     `yaml:"behavior"`
		Metadata     map[string]interface{} `yaml:"metadata"`
	} `yaml:"agent"`
}

// K8sStyleAgentConfig represents the new k8s-style YAML format with apiVersion, kind, metadata, spec.
type K8sStyleAgentConfig struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name        string                 `yaml:"name"`
		Version     string                 `yaml:"version"`
		Description string                 `yaml:"description"`
		Role        string                 `yaml:"role"`
		Workflow    string                 `yaml:"workflow"`
		Labels      map[string]interface{} `yaml:"labels"`
	} `yaml:"metadata"`
	Spec struct {
		Backend struct {
			Name   string                 `yaml:"name"`
			Type   string                 `yaml:"type"`
			Config map[string]interface{} `yaml:"config"`
		} `yaml:"backend"`
		LLM           LLMConfigYAML          `yaml:"llm"`
		Tools         interface{}            `yaml:"tools"` // Can be ToolsConfigYAML or []interface{}
		SystemPrompt  string                 `yaml:"system_prompt"`
		ROM           string                 `yaml:"rom"` // ROM identifier: "TD", "teradata", "auto", or ""
		Config        BehaviorConfigYAML     `yaml:"config"`
		Memory        MemoryConfigYAML       `yaml:"memory"`
		Observability map[string]interface{} `yaml:"observability"`
	} `yaml:"spec"`
}

// LLMConfigYAML represents LLM configuration in YAML
type LLMConfigYAML struct {
	Provider             string   `yaml:"provider"`
	Model                string   `yaml:"model"`
	Temperature          float64  `yaml:"temperature"`
	MaxTokens            int      `yaml:"max_tokens"`
	StopSequences        []string `yaml:"stop_sequences"`
	TopP                 float64  `yaml:"top_p"`
	TopK                 int      `yaml:"top_k"`
	MaxContextTokens     int      `yaml:"max_context_tokens"`
	ReservedOutputTokens int      `yaml:"reserved_output_tokens"`
}

// ToolsConfigYAML represents tools configuration in YAML
type ToolsConfigYAML struct {
	MCP     []MCPToolConfigYAML    `yaml:"mcp"`
	Custom  []CustomToolConfigYAML `yaml:"custom"`
	Builtin []string               `yaml:"builtin"`
}

// MCPToolConfigYAML represents MCP tool configuration in YAML
type MCPToolConfigYAML struct {
	Server string   `yaml:"server"`
	Tools  []string `yaml:"tools"`
}

// CustomToolConfigYAML represents custom tool configuration in YAML
type CustomToolConfigYAML struct {
	Name           string `yaml:"name"`
	Implementation string `yaml:"implementation"`
}

// MemoryConfigYAML represents memory configuration in YAML
type MemoryConfigYAML struct {
	Type              string                       `yaml:"type"`
	Path              string                       `yaml:"path"`
	DSN               string                       `yaml:"dsn"`
	MaxHistory        int                          `yaml:"max_history"`
	MemoryCompression *MemoryCompressionConfigYAML `yaml:"memory_compression"`
}

// MemoryCompressionConfigYAML represents memory compression configuration in YAML
type MemoryCompressionConfigYAML struct {
	WorkloadProfile          string                           `yaml:"workload_profile"`
	MaxL1Messages            int                              `yaml:"max_l1_messages"`
	MinL1Messages            int                              `yaml:"min_l1_messages"`
	WarningThresholdPercent  int                              `yaml:"warning_threshold_percent"`
	CriticalThresholdPercent int                              `yaml:"critical_threshold_percent"`
	BatchSizes               *MemoryCompressionBatchSizesYAML `yaml:"batch_sizes"`
}

// MemoryCompressionBatchSizesYAML represents compression batch sizes in YAML
type MemoryCompressionBatchSizesYAML struct {
	Normal   int `yaml:"normal"`
	Warning  int `yaml:"warning"`
	Critical int `yaml:"critical"`
}

// BehaviorConfigYAML represents behavior configuration in YAML
type BehaviorConfigYAML struct {
	MaxIterations      int                `yaml:"max_iterations"`
	TimeoutSeconds     int                `yaml:"timeout_seconds"`
	AllowCodeExecution bool               `yaml:"allow_code_execution"`
	AllowedDomains     []string           `yaml:"allowed_domains"`
	MaxTurns           int                `yaml:"max_turns"`
	MaxToolExecutions  int                `yaml:"max_tool_executions"`
	Patterns           *PatternConfigYAML `yaml:"patterns"`
}

// PatternConfigYAML represents pattern configuration in YAML
type PatternConfigYAML struct {
	Enabled            *bool    `yaml:"enabled"`
	MinConfidence      *float64 `yaml:"min_confidence"`
	MaxPatternsPerTurn *int     `yaml:"max_patterns_per_turn"`
	EnableTracking     *bool    `yaml:"enable_tracking"`
	UseLLMClassifier   *bool    `yaml:"use_llm_classifier"`
}

// LoadAgentConfig loads agent configuration from a YAML file and converts it to proto.
func LoadAgentConfig(path string) (*loomv1.AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	return LoadConfigFromString(string(data))
}

// LoadConfigFromString loads agent configuration from a YAML string and converts it to proto.
// This is used by the meta-agent factory to spawn agents from generated YAML configs.
// Supports both legacy format (agent:) and k8s-style format (apiVersion/kind/metadata/spec).
func LoadConfigFromString(yamlContent string) (*loomv1.AgentConfig, error) {
	// Support environment variable expansion
	dataStr := expandEnvVars(yamlContent)

	// Split at YAML document terminator if present
	// Everything after "..." is documentation/ROM and should not be parsed
	parts := strings.SplitN(dataStr, "\n...\n", 2)
	yamlOnly := parts[0]

	// Detect format by checking for apiVersion field
	var formatDetector struct {
		APIVersion string `yaml:"apiVersion"`
		Agent      struct {
			Name string `yaml:"name"`
		} `yaml:"agent"`
	}
	if err := yaml.Unmarshal([]byte(yamlOnly), &formatDetector); err != nil {
		return nil, fmt.Errorf("failed to parse YAML config: %w", err)
	}

	var yamlConfig AgentConfigYAML

	// If apiVersion is present, it's k8s-style format
	if formatDetector.APIVersion != "" {
		var k8sConfig K8sStyleAgentConfig
		if err := yaml.Unmarshal([]byte(yamlOnly), &k8sConfig); err != nil {
			return nil, fmt.Errorf("failed to parse k8s-style YAML config: %w", err)
		}
		yamlConfig = convertK8sToLegacy(&k8sConfig)
	} else {
		// Legacy format
		if err := yaml.Unmarshal([]byte(yamlOnly), &yamlConfig); err != nil {
			return nil, fmt.Errorf("failed to parse YAML config: %w", err)
		}
	}

	// Validate required fields
	if yamlConfig.Agent.Name == "" {
		return nil, fmt.Errorf("agent name is required")
	}

	// LLM config is optional - agents inherit server's LLM provider if not specified
	// If partially specified, both provider and model are required
	hasProvider := yamlConfig.Agent.LLM.Provider != ""
	hasModel := yamlConfig.Agent.LLM.Model != ""

	if hasProvider && !hasModel {
		return nil, fmt.Errorf("LLM model is required when provider is specified")
	}
	if hasModel && !hasProvider {
		return nil, fmt.Errorf("LLM provider is required when model is specified")
	}

	// Convert YAML to proto
	return yamlToProto(&yamlConfig)
}

// convertK8sToLegacy converts k8s-style config to legacy format.
func convertK8sToLegacy(k8s *K8sStyleAgentConfig) AgentConfigYAML {
	var legacy AgentConfigYAML

	// Basic fields from metadata
	legacy.Agent.Name = k8s.Metadata.Name
	legacy.Agent.Description = k8s.Metadata.Description

	// LLM config
	legacy.Agent.LLM = k8s.Spec.LLM

	// System prompt
	legacy.Agent.SystemPrompt = k8s.Spec.SystemPrompt

	// ROM identifier
	legacy.Agent.ROM = k8s.Spec.ROM

	// Tools - handle both old ToolsConfigYAML and new simplified format
	switch tools := k8s.Spec.Tools.(type) {
	case map[string]interface{}:
		// Old format with mcp/custom/builtin
		if mcp, ok := tools["mcp"].([]interface{}); ok {
			for _, m := range mcp {
				if mcpMap, ok := m.(map[string]interface{}); ok {
					server, _ := mcpMap["server"].(string)
					var toolsList []string
					if t, ok := mcpMap["tools"].([]interface{}); ok {
						for _, tool := range t {
							if toolStr, ok := tool.(string); ok {
								toolsList = append(toolsList, toolStr)
							}
						}
					}
					legacy.Agent.Tools.MCP = append(legacy.Agent.Tools.MCP, MCPToolConfigYAML{
						Server: server,
						Tools:  toolsList,
					})
				}
			}
		}
		if builtin, ok := tools["builtin"].([]interface{}); ok {
			for _, b := range builtin {
				if builtinStr, ok := b.(string); ok {
					legacy.Agent.Tools.Builtin = append(legacy.Agent.Tools.Builtin, builtinStr)
				}
			}
		}
		if custom, ok := tools["custom"].([]interface{}); ok {
			for _, c := range custom {
				if customMap, ok := c.(map[string]interface{}); ok {
					name, _ := customMap["name"].(string)
					impl, _ := customMap["implementation"].(string)
					legacy.Agent.Tools.Custom = append(legacy.Agent.Tools.Custom, CustomToolConfigYAML{
						Name:           name,
						Implementation: impl,
					})
				}
			}
		}
	case []interface{}:
		// New simplified format - array of tool names (assume builtin)
		for _, tool := range tools {
			if toolStr, ok := tool.(string); ok {
				legacy.Agent.Tools.Builtin = append(legacy.Agent.Tools.Builtin, toolStr)
			}
		}
	}

	// Behavior/config
	legacy.Agent.Behavior = k8s.Spec.Config

	// Memory
	legacy.Agent.Memory = k8s.Spec.Memory

	// Metadata - merge labels and other metadata fields
	legacy.Agent.Metadata = make(map[string]interface{})
	if k8s.Metadata.Version != "" {
		legacy.Agent.Metadata["version"] = k8s.Metadata.Version
	}
	if k8s.Metadata.Role != "" {
		legacy.Agent.Metadata["role"] = k8s.Metadata.Role
	}
	if k8s.Metadata.Workflow != "" {
		legacy.Agent.Metadata["workflow"] = k8s.Metadata.Workflow
	}
	for k, v := range k8s.Metadata.Labels {
		legacy.Agent.Metadata[k] = v
	}

	// Backend path from spec.backend
	if k8s.Spec.Backend.Name != "" {
		// For now, just store backend name in metadata
		// TODO: Support full backend config conversion if needed
		legacy.Agent.Metadata["backend_name"] = k8s.Spec.Backend.Name
		legacy.Agent.Metadata["backend_type"] = k8s.Spec.Backend.Type
	}

	return legacy
}

// convertMetadata converts map[string]interface{} to map[string]string
// Complex values (lists, maps) are JSON-encoded
func convertMetadata(metadata map[string]interface{}) map[string]string {
	result := make(map[string]string)
	for k, v := range metadata {
		switch val := v.(type) {
		case string:
			result[k] = val
		case []interface{}, map[string]interface{}:
			// JSON-encode complex types
			jsonBytes, err := json.Marshal(val)
			if err == nil {
				result[k] = string(jsonBytes)
			}
		default:
			// Convert other types to string
			result[k] = fmt.Sprintf("%v", val)
		}
	}
	return result
}

// convertMetadataToInterface converts map[string]string to map[string]interface{}
func convertMetadataToInterface(metadata map[string]string) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range metadata {
		result[k] = v
	}
	return result
}

// yamlToProto converts YAML config to proto AgentConfig
func yamlToProto(yaml *AgentConfigYAML) (*loomv1.AgentConfig, error) {
	// Start with existing metadata
	metadata := convertMetadata(yaml.Agent.Metadata)
	if metadata == nil {
		metadata = make(map[string]string)
	}

	// Add backend_path to metadata if specified
	if yaml.Agent.BackendPath != "" {
		metadata["backend_path"] = yaml.Agent.BackendPath
	}

	config := &loomv1.AgentConfig{
		Name:         yaml.Agent.Name,
		Description:  yaml.Agent.Description,
		SystemPrompt: yaml.Agent.SystemPrompt,
		Rom:          yaml.Agent.ROM, // ROM identifier from YAML
		Metadata:     metadata,
	}

	// Convert LLM config with safe integer conversions
	maxTokens, err := safeInt32(yaml.Agent.LLM.MaxTokens, "LLM.MaxTokens")
	if err != nil {
		return nil, fmt.Errorf("invalid LLM config: %w", err)
	}
	topK, err := safeInt32(yaml.Agent.LLM.TopK, "LLM.TopK")
	if err != nil {
		return nil, fmt.Errorf("invalid LLM config: %w", err)
	}
	maxContextTokens, err := safeInt32(yaml.Agent.LLM.MaxContextTokens, "LLM.MaxContextTokens")
	if err != nil {
		return nil, fmt.Errorf("invalid LLM config: %w", err)
	}
	reservedOutputTokens, err := safeInt32(yaml.Agent.LLM.ReservedOutputTokens, "LLM.ReservedOutputTokens")
	if err != nil {
		return nil, fmt.Errorf("invalid LLM config: %w", err)
	}

	config.Llm = &loomv1.LLMConfig{
		Provider:             yaml.Agent.LLM.Provider,
		Model:                yaml.Agent.LLM.Model,
		Temperature:          float32(yaml.Agent.LLM.Temperature),
		MaxTokens:            maxTokens,
		StopSequences:        yaml.Agent.LLM.StopSequences,
		TopP:                 float32(yaml.Agent.LLM.TopP),
		TopK:                 topK,
		MaxContextTokens:     maxContextTokens,
		ReservedOutputTokens: reservedOutputTokens,
	}

	// Set defaults for LLM if not specified
	if config.Llm.MaxTokens == 0 {
		config.Llm.MaxTokens = 4096
	}
	if config.Llm.Temperature == 0 {
		config.Llm.Temperature = 0.7
	}

	// Convert tools config
	config.Tools = &loomv1.ToolsConfig{
		Mcp:     make([]*loomv1.MCPToolConfig, len(yaml.Agent.Tools.MCP)),
		Custom:  make([]*loomv1.CustomToolConfig, len(yaml.Agent.Tools.Custom)),
		Builtin: yaml.Agent.Tools.Builtin,
	}

	for i, mcp := range yaml.Agent.Tools.MCP {
		config.Tools.Mcp[i] = &loomv1.MCPToolConfig{
			Server: mcp.Server,
			Tools:  mcp.Tools,
		}
	}

	for i, custom := range yaml.Agent.Tools.Custom {
		config.Tools.Custom[i] = &loomv1.CustomToolConfig{
			Name:           custom.Name,
			Implementation: custom.Implementation,
		}
	}

	// Convert memory config with safe integer conversion
	maxHistory, err := safeInt32(yaml.Agent.Memory.MaxHistory, "Memory.MaxHistory")
	if err != nil {
		return nil, fmt.Errorf("invalid memory config: %w", err)
	}

	config.Memory = &loomv1.MemoryConfig{
		Type:       yaml.Agent.Memory.Type,
		Path:       yaml.Agent.Memory.Path,
		Dsn:        yaml.Agent.Memory.DSN,
		MaxHistory: maxHistory,
	}

	// Set defaults for memory
	if config.Memory.Type == "" {
		config.Memory.Type = "memory" // In-memory by default
	}
	if config.Memory.MaxHistory == 0 {
		config.Memory.MaxHistory = 50
	}

	// Convert memory compression config if specified
	if yaml.Agent.Memory.MemoryCompression != nil {
		config.Memory.MemoryCompression = parseMemoryCompressionConfig(yaml.Agent.Memory.MemoryCompression)
	}

	// Convert behavior config with safe integer conversions
	maxIterations, err := safeInt32(yaml.Agent.Behavior.MaxIterations, "MaxIterations")
	if err != nil {
		return nil, fmt.Errorf("invalid behavior config: %w", err)
	}
	timeoutSeconds, err := safeInt32(yaml.Agent.Behavior.TimeoutSeconds, "TimeoutSeconds")
	if err != nil {
		return nil, fmt.Errorf("invalid behavior config: %w", err)
	}
	maxTurns, err := safeInt32(yaml.Agent.Behavior.MaxTurns, "MaxTurns")
	if err != nil {
		return nil, fmt.Errorf("invalid behavior config: %w", err)
	}
	maxToolExecutions, err := safeInt32(yaml.Agent.Behavior.MaxToolExecutions, "MaxToolExecutions")
	if err != nil {
		return nil, fmt.Errorf("invalid behavior config: %w", err)
	}

	config.Behavior = &loomv1.BehaviorConfig{
		MaxIterations:      maxIterations,
		TimeoutSeconds:     timeoutSeconds,
		AllowCodeExecution: yaml.Agent.Behavior.AllowCodeExecution,
		AllowedDomains:     yaml.Agent.Behavior.AllowedDomains,
		MaxTurns:           maxTurns,
		MaxToolExecutions:  maxToolExecutions,
	}

	// Parse pattern config if present
	if yaml.Agent.Behavior.Patterns != nil {
		pc := yaml.Agent.Behavior.Patterns
		config.Behavior.Patterns = &loomv1.PatternConfig{
			Enabled:            true, // default
			MinConfidence:      0.75, // default
			MaxPatternsPerTurn: 1,    // default
			EnableTracking:     true, // default
		}

		if pc.Enabled != nil {
			config.Behavior.Patterns.Enabled = *pc.Enabled
		}
		if pc.MinConfidence != nil {
			config.Behavior.Patterns.MinConfidence = float32(*pc.MinConfidence)
		}
		if pc.MaxPatternsPerTurn != nil {
			maxPatternsPerTurn, err := safeInt32(*pc.MaxPatternsPerTurn, "MaxPatternsPerTurn")
			if err != nil {
				return nil, fmt.Errorf("invalid pattern config: %w", err)
			}
			config.Behavior.Patterns.MaxPatternsPerTurn = maxPatternsPerTurn
		}
		if pc.EnableTracking != nil {
			config.Behavior.Patterns.EnableTracking = *pc.EnableTracking
		}
		if pc.UseLLMClassifier != nil {
			config.Behavior.Patterns.UseLlmClassifier = *pc.UseLLMClassifier
		}
	}

	// Set defaults for behavior
	if config.Behavior.MaxIterations == 0 {
		config.Behavior.MaxIterations = 10
	}
	if config.Behavior.TimeoutSeconds == 0 {
		config.Behavior.TimeoutSeconds = 300
	}
	if config.Behavior.MaxTurns == 0 {
		config.Behavior.MaxTurns = 25 // Default conversation turns
	}
	if config.Behavior.MaxToolExecutions == 0 {
		config.Behavior.MaxToolExecutions = 50 // Default tool executions
	}

	return config, nil
}

// parseMemoryCompressionConfig converts YAML memory compression config to proto
func parseMemoryCompressionConfig(yaml *MemoryCompressionConfigYAML) *loomv1.MemoryCompressionConfig {
	if yaml == nil {
		return nil
	}

	// Safe integer conversions for memory compression config
	maxL1, err := safeInt32(yaml.MaxL1Messages, "MaxL1Messages")
	if err != nil {
		return nil
	}
	minL1, err := safeInt32(yaml.MinL1Messages, "MinL1Messages")
	if err != nil {
		return nil
	}
	warningThreshold, err := safeInt32(yaml.WarningThresholdPercent, "WarningThresholdPercent")
	if err != nil {
		return nil
	}
	criticalThreshold, err := safeInt32(yaml.CriticalThresholdPercent, "CriticalThresholdPercent")
	if err != nil {
		return nil
	}

	config := &loomv1.MemoryCompressionConfig{
		MaxL1Messages:            maxL1,
		MinL1Messages:            minL1,
		WarningThresholdPercent:  warningThreshold,
		CriticalThresholdPercent: criticalThreshold,
	}

	// Parse workload profile string to enum
	switch strings.ToLower(yaml.WorkloadProfile) {
	case "data_intensive":
		config.WorkloadProfile = loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE
	case "conversational":
		config.WorkloadProfile = loomv1.WorkloadProfile_WORKLOAD_PROFILE_CONVERSATIONAL
	case "balanced":
		config.WorkloadProfile = loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED
	case "":
		// Unspecified means use balanced as default
		config.WorkloadProfile = loomv1.WorkloadProfile_WORKLOAD_PROFILE_UNSPECIFIED
	default:
		// Unknown profile, default to unspecified (will use balanced)
		config.WorkloadProfile = loomv1.WorkloadProfile_WORKLOAD_PROFILE_UNSPECIFIED
	}

	// Parse batch sizes if specified with safe conversions
	if yaml.BatchSizes != nil {
		normal, err := safeInt32(yaml.BatchSizes.Normal, "BatchSizes.Normal")
		if err != nil {
			return nil
		}
		warning, err := safeInt32(yaml.BatchSizes.Warning, "BatchSizes.Warning")
		if err != nil {
			return nil
		}
		critical, err := safeInt32(yaml.BatchSizes.Critical, "BatchSizes.Critical")
		if err != nil {
			return nil
		}
		config.BatchSizes = &loomv1.MemoryCompressionBatchSizes{
			Normal:   normal,
			Warning:  warning,
			Critical: critical,
		}
	}

	return config
}

// expandEnvVars replaces ${VAR} or $VAR with environment variable values
func expandEnvVars(s string) string {
	return os.Expand(s, func(key string) string {
		return os.Getenv(key)
	})
}

// ValidateAgentConfig validates an agent configuration
func ValidateAgentConfig(config *loomv1.AgentConfig) error {
	if config.Name == "" {
		return fmt.Errorf("agent name is required")
	}

	// LLM config is optional - agents inherit server's LLM provider if not specified
	// Only validate if LLM config is actually provided (has provider or model set)
	if config.Llm != nil && (config.Llm.Provider != "" || config.Llm.Model != "") {
		// If LLM config provided, validate it's complete
		if config.Llm.Provider == "" {
			return fmt.Errorf("LLM provider is required when model is specified")
		}

		if config.Llm.Model == "" {
			return fmt.Errorf("LLM model is required when provider is specified")
		}

		// Validate provider is supported
		validProviders := map[string]bool{
			"anthropic": true,
			"bedrock":   true,
			"ollama":    true,
		}
		provider := strings.ToLower(config.Llm.Provider)
		if !validProviders[provider] {
			return fmt.Errorf("unsupported LLM provider: %s (must be one of: anthropic, bedrock, ollama)", config.Llm.Provider)
		}

		// Validate temperature range
		if config.Llm.Temperature < 0 || config.Llm.Temperature > 1 {
			return fmt.Errorf("temperature must be between 0 and 1, got: %f", config.Llm.Temperature)
		}
	}

	// Validate memory type
	if config.Memory != nil && config.Memory.Type != "" {
		validMemoryTypes := map[string]bool{
			"memory":   true,
			"sqlite":   true,
			"postgres": true,
		}
		if !validMemoryTypes[config.Memory.Type] {
			return fmt.Errorf("unsupported memory type: %s (must be one of: memory, sqlite, postgres)", config.Memory.Type)
		}
	}

	return nil
}

// SaveAgentConfig saves an agent configuration to a YAML file
func SaveAgentConfig(config *loomv1.AgentConfig, path string) error {
	yamlConfig := protoToYAML(config)

	data, err := yaml.Marshal(yamlConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal config to YAML: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// protoToYAML converts proto AgentConfig to YAML structure
func protoToYAML(config *loomv1.AgentConfig) *AgentConfigYAML {
	yaml := &AgentConfigYAML{}

	yaml.Agent.Name = config.Name
	yaml.Agent.Description = config.Description
	yaml.Agent.SystemPrompt = config.SystemPrompt
	yaml.Agent.Metadata = convertMetadataToInterface(config.Metadata)

	// Convert LLM config
	if config.Llm != nil {
		yaml.Agent.LLM = LLMConfigYAML{
			Provider:      config.Llm.Provider,
			Model:         config.Llm.Model,
			Temperature:   float64(config.Llm.Temperature),
			MaxTokens:     int(config.Llm.MaxTokens),
			StopSequences: config.Llm.StopSequences,
			TopP:          float64(config.Llm.TopP),
			TopK:          int(config.Llm.TopK),
		}
	}

	// Convert tools config
	if config.Tools != nil {
		yaml.Agent.Tools.MCP = make([]MCPToolConfigYAML, len(config.Tools.Mcp))
		for i, mcp := range config.Tools.Mcp {
			yaml.Agent.Tools.MCP[i] = MCPToolConfigYAML{
				Server: mcp.Server,
				Tools:  mcp.Tools,
			}
		}

		yaml.Agent.Tools.Custom = make([]CustomToolConfigYAML, len(config.Tools.Custom))
		for i, custom := range config.Tools.Custom {
			yaml.Agent.Tools.Custom[i] = CustomToolConfigYAML{
				Name:           custom.Name,
				Implementation: custom.Implementation,
			}
		}

		yaml.Agent.Tools.Builtin = config.Tools.Builtin
	}

	// Convert memory config
	if config.Memory != nil {
		yaml.Agent.Memory = MemoryConfigYAML{
			Type:       config.Memory.Type,
			Path:       config.Memory.Path,
			DSN:        config.Memory.Dsn,
			MaxHistory: int(config.Memory.MaxHistory),
		}
	}

	// Convert behavior config
	if config.Behavior != nil {
		yaml.Agent.Behavior = BehaviorConfigYAML{
			MaxIterations:      int(config.Behavior.MaxIterations),
			TimeoutSeconds:     int(config.Behavior.TimeoutSeconds),
			AllowCodeExecution: config.Behavior.AllowCodeExecution,
			AllowedDomains:     config.Behavior.AllowedDomains,
			MaxTurns:           int(config.Behavior.MaxTurns),
			MaxToolExecutions:  int(config.Behavior.MaxToolExecutions),
		}
	}

	return yaml
}

// LoadWorkflowAgents loads a workflow file and extracts ALL agent configs (coordinator + sub-agents).
// Returns a slice of AgentConfigs with proper namespacing:
//   - Coordinator: registered as {workflow-name}
//   - Sub-agents: registered as {workflow-name}:{agent-id}
//
// Supports two formats:
// 1. Orchestration format (apiVersion/kind/spec) - used by looms workflow run
// 2. Weaver format (agent config with embedded workflow section)
//
// This allows connecting to individual agents while ensuring the workflow uses the same registered instances.
func LoadWorkflowAgents(path string, llmProvider LLMProvider) ([]*loomv1.AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read workflow file %s: %w", path, err)
	}

	// Parse workflow structure
	var workflowData map[string]interface{}
	if err := yaml.Unmarshal(data, &workflowData); err != nil {
		return nil, fmt.Errorf("failed to parse workflow YAML: %w", err)
	}

	// Detect format: orchestration (apiVersion) vs weaver (name + workflow)
	if apiVersion, ok := workflowData["apiVersion"].(string); ok && strings.HasPrefix(apiVersion, "loom") {
		// Orchestration format
		return loadOrchestrationWorkflow(path, workflowData, llmProvider)
	}

	// Try weaver format (agent config with embedded workflow section)
	if _, hasName := workflowData["name"]; hasName {
		if _, hasWorkflow := workflowData["workflow"]; hasWorkflow {
			return loadWeaverWorkflow(path, workflowData, llmProvider)
		}
	}

	return nil, fmt.Errorf("unrecognized workflow format (must have 'apiVersion: loom/v1' or 'name' + 'workflow' sections)")
}

// loadAgentReferenceWorkflow parses agent-reference workflows (references existing agent configs)
// Format: spec.entrypoint + spec.agents with "agent" references
// Creates namespaced copies: workflow-name (entrypoint), workflow-name:agent-id (sub-agents)
func loadAgentReferenceWorkflow(path, workflowName, description, entrypoint string, agentsData []interface{}, llmProvider LLMProvider) ([]*loomv1.AgentConfig, error) {
	// Parse agents list
	type agentRef struct {
		Name        string
		AgentConfig string
		Description string
	}

	agentRefs := make(map[string]agentRef)
	var entrypointRef *agentRef

	for _, agentItem := range agentsData {
		agentMap, ok := agentItem.(map[string]interface{})
		if !ok {
			continue
		}

		name, _ := agentMap["name"].(string)
		agentConfig, _ := agentMap["agent"].(string)
		desc, _ := agentMap["description"].(string)

		if name == "" || agentConfig == "" {
			continue
		}

		ref := agentRef{
			Name:        name,
			AgentConfig: agentConfig,
			Description: desc,
		}

		agentRefs[name] = ref

		if name == entrypoint {
			entrypointRef = &ref
		}
	}

	if entrypointRef == nil {
		return nil, fmt.Errorf("workflow entrypoint '%s' not found in agents list", entrypoint)
	}

	// Load agent configs and create namespaced copies
	var configs []*loomv1.AgentConfig

	// Determine config directory (assume same directory as workflow file or $LOOM_DATA_DIR/agents)
	configDir := filepath.Dir(path)
	agentsDir := filepath.Join(filepath.Dir(configDir), "agents")

	for name, ref := range agentRefs {
		// Try to find the referenced agent config
		var agentConfigPath string

		// Try in agents directory first
		candidatePath := filepath.Join(agentsDir, ref.AgentConfig+".yaml")
		if _, err := os.Stat(candidatePath); err == nil {
			agentConfigPath = candidatePath
		} else {
			// Try subdirectories
			err := filepath.WalkDir(agentsDir, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() && filepath.Base(p) == ref.AgentConfig+".yaml" {
					agentConfigPath = p
					return filepath.SkipAll
				}
				return nil
			})
			if err != nil || agentConfigPath == "" {
				return nil, fmt.Errorf("agent config '%s' not found in %s", ref.AgentConfig, agentsDir)
			}
		}

		// Load the referenced agent config
		baseConfig, err := LoadAgentConfig(agentConfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load agent config '%s': %w", ref.AgentConfig, err)
		}

		// Create namespaced copy
		var namespacedName string
		var role string

		if name == entrypoint {
			// Entrypoint becomes the workflow coordinator
			namespacedName = workflowName
			role = "coordinator"
		} else {
			// Sub-agents get namespaced
			namespacedName = fmt.Sprintf("%s:%s", workflowName, name)
			role = "executor"
		}

		// Clone the config with namespaced name
		namespacedConfig := &loomv1.AgentConfig{
			Name:         namespacedName,
			Description:  baseConfig.Description,
			SystemPrompt: baseConfig.SystemPrompt,
			Llm:          baseConfig.Llm,
			Tools:        baseConfig.Tools,
			Memory:       baseConfig.Memory,
			Behavior:     baseConfig.Behavior,
			Metadata: map[string]string{
				"workflow":            workflowName,
				"role":                role,
				"workflow_file":       path,
				"agent_name":          name,
				"base_agent":          ref.AgentConfig,
				"type":                "workflow_agent",
				"workflow_entrypoint": entrypoint,
			},
		}

		// Preserve any existing metadata and add workflow metadata
		if baseConfig.Metadata != nil {
			for k, v := range baseConfig.Metadata {
				if _, exists := namespacedConfig.Metadata[k]; !exists {
					namespacedConfig.Metadata[k] = v
				}
			}
		}

		configs = append(configs, namespacedConfig)
	}

	return configs, nil
}

// loadOrchestrationWorkflow parses orchestration format workflows (apiVersion/kind/spec)
func loadOrchestrationWorkflow(path string, data map[string]interface{}, llmProvider LLMProvider) ([]*loomv1.AgentConfig, error) {
	// Extract metadata
	metadata, ok := data["metadata"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("orchestration workflow missing 'metadata' section")
	}

	workflowName, _ := metadata["name"].(string)
	if workflowName == "" {
		return nil, fmt.Errorf("orchestration workflow missing 'metadata.name'")
	}

	description, _ := metadata["description"].(string)

	// Extract spec
	spec, ok := data["spec"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("orchestration workflow missing 'spec' section")
	}

	// Check if this is an agent-reference workflow (has entrypoint and agents list)
	if entrypoint, hasEntrypoint := spec["entrypoint"].(string); hasEntrypoint {
		if agentsData, hasAgents := spec["agents"].([]interface{}); hasAgents {
			return loadAgentReferenceWorkflow(path, workflowName, description, entrypoint, agentsData, llmProvider)
		}
	}

	patternType, _ := spec["type"].(string)
	if patternType == "" {
		return nil, fmt.Errorf("orchestration workflow missing 'spec.type' (or use spec.entrypoint for agent-reference workflows)")
	}

	// Extract agent IDs based on pattern type
	var agentIDs []string
	switch patternType {
	case "debate":
		if ids, ok := spec["agent_ids"].([]interface{}); ok {
			for _, id := range ids {
				if idStr, ok := id.(string); ok {
					agentIDs = append(agentIDs, idStr)
				}
			}
		}
		// Include optional moderator
		if moderator, ok := spec["moderator_agent_id"].(string); ok && moderator != "" {
			agentIDs = append(agentIDs, moderator)
		}

	case "fork_join", "parallel":
		if ids, ok := spec["agent_ids"].([]interface{}); ok {
			for _, id := range ids {
				if idStr, ok := id.(string); ok {
					agentIDs = append(agentIDs, idStr)
				}
			}
		}

	case "pipeline", "iterative_pipeline":
		if stages, ok := spec["stages"].([]interface{}); ok {
			seen := make(map[string]bool)
			for _, stage := range stages {
				if stageMap, ok := stage.(map[string]interface{}); ok {
					if agentID, ok := stageMap["agent_id"].(string); ok && !seen[agentID] {
						agentIDs = append(agentIDs, agentID)
						seen[agentID] = true
					}
				}
			}
		}

	case "conditional":
		if branches, ok := spec["branches"].(map[string]interface{}); ok {
			seen := make(map[string]bool)
			for _, branch := range branches {
				if branchMap, ok := branch.(map[string]interface{}); ok {
					if agentID, ok := branchMap["agent_id"].(string); ok && !seen[agentID] {
						agentIDs = append(agentIDs, agentID)
						seen[agentID] = true
					}
				}
			}
		}

	default:
		return nil, fmt.Errorf("unsupported workflow pattern type: %s", patternType)
	}

	if len(agentIDs) == 0 {
		return nil, fmt.Errorf("no agents found in workflow")
	}

	// Build LLM config from provider
	llmConfig := &loomv1.LLMConfig{
		Provider:    llmProvider.Name(),
		Model:       llmProvider.Model(),
		Temperature: 0.7,
		MaxTokens:   4096,
	}

	var configs []*loomv1.AgentConfig

	// Create coordinator agent
	coordinatorPrompt := fmt.Sprintf(`You are the coordinator for the "%s" workflow.
Pattern: %s
Description: %s

Your role is to orchestrate the workflow execution, manage agent coordination, and ensure proper sequencing of tasks.

Sub-agents: %s

You can use session_memory to search past workflow sessions:
- session_memory(action="list") - list your own coordinator sessions
- session_memory(action="list", agent_id="agent-name") - list sessions for a specific sub-agent
- session_memory(action="summary", session_id="...") - retrieve conversation summary from a session`,
		workflowName, patternType, description, strings.Join(agentIDs, ", "))

	coordinatorConfig := &loomv1.AgentConfig{
		Name:         workflowName,
		Description:  fmt.Sprintf("%s workflow coordinator", workflowName),
		SystemPrompt: coordinatorPrompt,
		Llm:          llmConfig,
		Tools: &loomv1.ToolsConfig{
			Builtin: []string{"send_message", "publish", "shared_memory_read", "shared_memory_write", "session_memory"},
		},
		Memory: &loomv1.MemoryConfig{
			Type:       "sqlite",
			Path:       fmt.Sprintf("$LOOM_DATA_DIR/memory/%s-coordinator.db", workflowName),
			MaxHistory: 100,
		},
		Behavior: &loomv1.BehaviorConfig{
			MaxIterations:  20,
			TimeoutSeconds: 900,
		},
		Metadata: map[string]string{
			"workflow":      workflowName,
			"role":          "coordinator",
			"pattern":       patternType,
			"workflow_file": path,
			"type":          "workflow_coordinator",
		},
	}
	configs = append(configs, coordinatorConfig)

	// Create sub-agent configs (placeholders - actual agents should exist in registry)
	for _, agentID := range agentIDs {
		subAgentName := fmt.Sprintf("%s:%s", workflowName, agentID)
		subAgentConfig := &loomv1.AgentConfig{
			Name:         subAgentName,
			Description:  fmt.Sprintf("%s sub-agent for %s workflow", agentID, workflowName),
			SystemPrompt: fmt.Sprintf("You are the %s agent in the %s workflow. Follow the workflow coordinator's instructions.", agentID, workflowName),
			Llm:          llmConfig,
			Tools: &loomv1.ToolsConfig{
				Builtin: []string{"shared_memory_read", "shared_memory_write", "send_message", "publish"},
			},
			Memory: &loomv1.MemoryConfig{
				Type:       "sqlite",
				Path:       fmt.Sprintf("$LOOM_DATA_DIR/memory/%s-%s.db", workflowName, agentID),
				MaxHistory: 100,
			},
			Behavior: &loomv1.BehaviorConfig{
				MaxIterations:  15,
				TimeoutSeconds: 600,
			},
			Metadata: map[string]string{
				"workflow":      workflowName,
				"role":          "executor",
				"agent_id":      agentID,
				"workflow_file": path,
				"type":          "workflow_agent",
			},
		}
		configs = append(configs, subAgentConfig)
	}

	return configs, nil
}

// loadWeaverWorkflow parses weaver-generated workflows (agent config with embedded workflow section)
func loadWeaverWorkflow(path string, data map[string]interface{}, llmProvider LLMProvider) ([]*loomv1.AgentConfig, error) {
	// Extract root-level agent config (this is the coordinator)
	workflowName, _ := data["name"].(string)
	if workflowName == "" {
		return nil, fmt.Errorf("weaver workflow missing 'name' field")
	}

	// Use filename (without .yaml) as the display name instead of the name field
	// This preserves what the user actually typed when creating the workflow
	// Example: user types "dnd-campaign-creator" but weaver generates name: "dnd-campaign-orchestrator"
	// We want to show "dnd-campaign-creator" in the sidebar
	displayName := filepath.Base(path)
	displayName = strings.TrimSuffix(displayName, ".yaml")
	displayName = strings.TrimSuffix(displayName, ".yml")

	description, _ := data["description"].(string)
	systemPrompt, _ := data["system_prompt"].(string)

	// Extract LLM config from file or use provider default
	var llmConfig *loomv1.LLMConfig
	if llmData, ok := data["llm"].(map[string]interface{}); ok {
		provider, _ := llmData["provider"].(string)
		model, _ := llmData["model"].(string)
		temperature := 0.7
		if tempVal, ok := llmData["temperature"].(float64); ok {
			temperature = tempVal
		}
		maxTokens := int32(4096)
		if tokensVal, ok := llmData["max_tokens"].(float64); ok {
			maxTokensVal, err := safeInt32(int(tokensVal), "max_tokens")
			if err != nil {
				// Ignore error, keep default
				maxTokens = 4096
			} else {
				maxTokens = maxTokensVal
			}
		}

		llmConfig = &loomv1.LLMConfig{
			Provider:    provider,
			Model:       model,
			Temperature: float32(temperature),
			MaxTokens:   maxTokens,
		}
	} else {
		// Fallback to provider default
		llmConfig = &loomv1.LLMConfig{
			Provider:    llmProvider.Name(),
			Model:       llmProvider.Model(),
			Temperature: 0.7,
			MaxTokens:   4096,
		}
	}

	// Extract tools
	var toolStrings []string
	if toolsData, ok := data["tools"].(map[string]interface{}); ok {
		if builtin, ok := toolsData["builtin"].([]interface{}); ok {
			for _, tool := range builtin {
				if toolStr, ok := tool.(string); ok {
					toolStrings = append(toolStrings, toolStr)
				}
			}
		}
	}

	// Extract workflow section
	workflowData, ok := data["workflow"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("weaver workflow missing 'workflow' section")
	}

	pattern, _ := workflowData["pattern"].(string)
	agents, ok := workflowData["agents"].([]interface{})
	if !ok || len(agents) == 0 {
		return nil, fmt.Errorf("weaver workflow missing 'workflow.agents' list")
	}

	var configs []*loomv1.AgentConfig

	// Create coordinator agent
	coordinatorConfig := &loomv1.AgentConfig{
		Name:         displayName, // Use display name (from filename) for sidebar
		Description:  description,
		SystemPrompt: systemPrompt,
		Llm:          llmConfig,
		Tools: &loomv1.ToolsConfig{
			Builtin: toolStrings,
		},
		Memory: &loomv1.MemoryConfig{
			Type:       "sqlite",
			Path:       fmt.Sprintf("$LOOM_DATA_DIR/memory/%s-coordinator.db", displayName),
			MaxHistory: 100,
		},
		Behavior: &loomv1.BehaviorConfig{
			MaxIterations:  20,
			TimeoutSeconds: 900,
		},
		Metadata: map[string]string{
			"workflow":      displayName,
			"role":          "coordinator",
			"pattern":       pattern,
			"workflow_file": path,
			"type":          "workflow_coordinator",
		},
	}
	configs = append(configs, coordinatorConfig)

	// Create sub-agent configs
	for _, agentInterface := range agents {
		agentData, ok := agentInterface.(map[string]interface{})
		if !ok {
			continue
		}

		agentName, _ := agentData["name"].(string)
		if agentName == "" {
			continue
		}

		role, _ := agentData["role"].(string)
		if role == "" {
			role = agentName // Use name as fallback
		}

		// Extract tools for this agent
		var agentTools []string
		if tools, ok := agentData["tools"].([]interface{}); ok {
			for _, tool := range tools {
				if toolStr, ok := tool.(string); ok {
					agentTools = append(agentTools, toolStr)
				}
			}
		}

		// Create namespaced agent ID
		subAgentName := fmt.Sprintf("%s:%s", displayName, agentName)

		subAgentConfig := &loomv1.AgentConfig{
			Name:         subAgentName,
			Description:  fmt.Sprintf("%s sub-agent for %s workflow", agentName, displayName),
			SystemPrompt: role, // Use role as system prompt
			Llm:          llmConfig,
			Tools: &loomv1.ToolsConfig{
				Builtin: agentTools,
			},
			Memory: &loomv1.MemoryConfig{
				Type:       "sqlite",
				Path:       fmt.Sprintf("$LOOM_DATA_DIR/memory/%s-%s.db", displayName, agentName),
				MaxHistory: 100,
			},
			Behavior: &loomv1.BehaviorConfig{
				MaxIterations:  15,
				TimeoutSeconds: 600,
			},
			Metadata: map[string]string{
				"workflow":      displayName,
				"role":          "executor",
				"agent_id":      agentName,
				"workflow_file": path,
				"type":          "workflow_agent",
			},
		}
		configs = append(configs, subAgentConfig)
	}

	return configs, nil
}

// LoadWorkflowCoordinator loads a workflow file and extracts only the coordinator agent.
// This is a convenience wrapper around LoadWorkflowAgents for backward compatibility.
// Deprecated: Use LoadWorkflowAgents to register all agents in the workflow.
func LoadWorkflowCoordinator(path string, llmProvider LLMProvider) (*loomv1.AgentConfig, error) {
	configs, err := LoadWorkflowAgents(path, llmProvider)
	if err != nil {
		return nil, err
	}

	// Find and return the coordinator
	for _, config := range configs {
		if config.Metadata["role"] == "coordinator" {
			return config, nil
		}
	}

	return nil, fmt.Errorf("no coordinator found in workflow")
}

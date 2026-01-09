// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"fmt"
	"os"
	"path/filepath"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"gopkg.in/yaml.v3"
)

// WorkflowYAML represents the top-level structure of a workflow YAML file.
type WorkflowYAML struct {
	APIVersion    string               `yaml:"apiVersion"`
	Kind          string               `yaml:"kind"`
	Metadata      WorkflowMetadata     `yaml:"metadata"`
	Spec          WorkflowSpec         `yaml:"spec"`
	Orchestration *OrchestrationConfig `yaml:"orchestration,omitempty"`
}

// WorkflowSpec defines the workflow pattern and agents.
type WorkflowSpec struct {
	Pattern string                 `yaml:"pattern"`
	Agents  []AgentDefinition      `yaml:"agents"`
	Config  map[string]interface{} `yaml:"config,omitempty"` // Pattern-specific config
}

// AgentDefinition describes an agent participating in the workflow.
// Agents can be defined inline with full configuration, or referenced by ID or path.
type AgentDefinition struct {
	// Agent identifier (required)
	ID string `yaml:"id"`

	// Path to agent configuration file (optional, for referencing external agents)
	// If specified, loads agent config from this path (absolute or relative to workflow file)
	Path string `yaml:"path,omitempty"`

	// Inline agent configuration (optional if Path is specified)
	Name           string   `yaml:"name,omitempty"`
	Role           string   `yaml:"role,omitempty"`
	SystemPrompt   string   `yaml:"system_prompt,omitempty"`
	Tools          []string `yaml:"tools,omitempty"`
	PromptTemplate string   `yaml:"prompt_template,omitempty"` // For pipeline stages
}

// OrchestrationConfig represents orchestration settings from YAML.
type OrchestrationConfig struct {
	Type                 string `yaml:"type"`
	MaxRounds            int    `yaml:"max_rounds,omitempty"`
	TerminationCondition string `yaml:"termination_condition,omitempty"`
	ConsensusRequired    bool   `yaml:"consensus_required,omitempty"`
	VotingStrategy       string `yaml:"voting_strategy,omitempty"`
	PassFullHistory      bool   `yaml:"pass_full_history,omitempty"`
	TimeoutSeconds       int32  `yaml:"timeout_seconds,omitempty"`
}

// ParseWorkflowFromYAML reads a workflow YAML file and builds a WorkflowPattern proto.
// This enables automatic execution of multi-agent workflows defined in YAML.
func ParseWorkflowFromYAML(path string) (*loomv1.WorkflowPattern, *WorkflowMetadata, error) {
	// Read YAML file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read workflow file: %w", err)
	}

	// Parse into struct
	var workflow WorkflowYAML
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		return nil, nil, fmt.Errorf("failed to parse workflow YAML: %w", err)
	}

	// Validate basic structure
	if workflow.APIVersion != "loom/v1" {
		return nil, nil, fmt.Errorf("unsupported apiVersion: %s (expected loom/v1)", workflow.APIVersion)
	}
	if workflow.Kind != "Workflow" {
		return nil, nil, fmt.Errorf("invalid kind: %s (expected Workflow)", workflow.Kind)
	}
	if workflow.Spec.Pattern == "" {
		return nil, nil, fmt.Errorf("spec.pattern is required")
	}
	if len(workflow.Spec.Agents) == 0 {
		return nil, nil, fmt.Errorf("spec.agents is required and must not be empty")
	}

	// Resolve agent definitions (load from paths if specified)
	workflowDir := filepath.Dir(path)
	resolvedAgents, err := resolveAgentDefinitions(workflow.Spec.Agents, workflowDir)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve agent definitions: %w", err)
	}
	workflow.Spec.Agents = resolvedAgents

	// Build WorkflowPattern based on pattern type
	var pattern *loomv1.WorkflowPattern
	switch workflow.Spec.Pattern {
	case "pipeline":
		pattern, err = buildPipelinePattern(workflow.Spec, workflow.Orchestration)
	case "fork_join":
		pattern, err = buildForkJoinPattern(workflow.Spec, workflow.Orchestration)
	case "parallel":
		pattern, err = buildParallelPattern(workflow.Spec, workflow.Orchestration)
	case "debate":
		pattern, err = buildDebatePattern(workflow.Spec, workflow.Orchestration)
	case "conditional":
		pattern, err = buildConditionalPattern(workflow.Spec, workflow.Orchestration)
	default:
		return nil, nil, fmt.Errorf("unsupported pattern type: %s", workflow.Spec.Pattern)
	}

	if err != nil {
		return nil, nil, fmt.Errorf("failed to build %s pattern: %w", workflow.Spec.Pattern, err)
	}

	return pattern, &workflow.Metadata, nil
}

// AgentConfigYAML represents an agent configuration file structure.
type AgentConfigYAML struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name        string            `yaml:"name"`
		Version     string            `yaml:"version,omitempty"`
		Description string            `yaml:"description,omitempty"`
		Labels      map[string]string `yaml:"labels,omitempty"`
	} `yaml:"metadata"`
	Spec struct {
		SystemPrompt string   `yaml:"system_prompt"`
		Tools        []string `yaml:"tools,omitempty"`
	} `yaml:"spec"`
}

// resolveAgentDefinitions resolves agent definitions by loading referenced agent config files.
// For agents with a Path field, it loads the agent config from the file and merges it.
// workflowDir is the directory containing the workflow file (for resolving relative paths).
func resolveAgentDefinitions(agents []AgentDefinition, workflowDir string) ([]AgentDefinition, error) {
	resolved := make([]AgentDefinition, len(agents))

	for i, agentDef := range agents {
		// If no path specified, use as-is (inline definition or registry reference)
		if agentDef.Path == "" {
			resolved[i] = agentDef
			continue
		}

		// Resolve path (absolute or relative to workflow file)
		agentPath := agentDef.Path
		if !filepath.IsAbs(agentPath) {
			agentPath = filepath.Join(workflowDir, agentPath)
		}

		// Load agent config from file
		agentConfig, err := loadAgentConfig(agentPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load agent from %s: %w", agentDef.Path, err)
		}

		// Merge: path-loaded config as base, workflow-specified fields override
		merged := AgentDefinition{
			ID:             agentDef.ID, // Always use workflow-specified ID
			Path:           agentDef.Path,
			Name:           agentConfig.Metadata.Name,
			SystemPrompt:   agentConfig.Spec.SystemPrompt,
			Tools:          agentConfig.Spec.Tools,
			Role:           agentDef.Role, // Workflow can override role
			PromptTemplate: agentDef.PromptTemplate,
		}

		// Allow workflow to override name if specified
		if agentDef.Name != "" {
			merged.Name = agentDef.Name
		}

		// Allow workflow to override system prompt if specified
		if agentDef.SystemPrompt != "" {
			merged.SystemPrompt = agentDef.SystemPrompt
		}

		resolved[i] = merged
	}

	return resolved, nil
}

// loadAgentConfig loads an agent configuration from a YAML file.
func loadAgentConfig(path string) (*AgentConfigYAML, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read agent config: %w", err)
	}

	var config AgentConfigYAML
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse agent config: %w", err)
	}

	// Validate
	if config.APIVersion != "loom/v1" {
		return nil, fmt.Errorf("unsupported apiVersion: %s (expected loom/v1)", config.APIVersion)
	}
	if config.Kind != "Agent" {
		return nil, fmt.Errorf("invalid kind: %s (expected Agent)", config.Kind)
	}
	if config.Metadata.Name == "" {
		return nil, fmt.Errorf("agent metadata.name is required")
	}
	if config.Spec.SystemPrompt == "" {
		return nil, fmt.Errorf("agent spec.system_prompt is required")
	}

	return &config, nil
}

// buildPipelinePattern creates a PipelinePattern from workflow spec.
func buildPipelinePattern(spec WorkflowSpec, orch *OrchestrationConfig) (*loomv1.WorkflowPattern, error) {
	stages := make([]*loomv1.PipelineStage, 0, len(spec.Agents))

	for _, agent := range spec.Agents {
		// Use prompt_template if provided, otherwise use a placeholder
		promptTemplate := agent.PromptTemplate
		if promptTemplate == "" {
			promptTemplate = "{{previous}}" // Default: pass previous output
		}

		stage := &loomv1.PipelineStage{
			AgentId:        agent.ID,
			PromptTemplate: promptTemplate,
		}
		stages = append(stages, stage)
	}

	pipeline := &loomv1.PipelinePattern{
		InitialPrompt: "{{user_query}}", // Will be replaced by request prompt
		Stages:        stages,
	}

	// Apply orchestration config if present
	if orch != nil {
		pipeline.PassFullHistory = orch.PassFullHistory
	}

	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: pipeline,
		},
	}, nil
}

// buildForkJoinPattern creates a ForkJoinPattern from workflow spec.
func buildForkJoinPattern(spec WorkflowSpec, orch *OrchestrationConfig) (*loomv1.WorkflowPattern, error) {
	agentIDs := make([]string, 0, len(spec.Agents))
	for _, agent := range spec.Agents {
		agentIDs = append(agentIDs, agent.ID)
	}

	forkJoin := &loomv1.ForkJoinPattern{
		Prompt:        "{{user_query}}",
		AgentIds:      agentIDs,
		MergeStrategy: loomv1.MergeStrategy_SUMMARY, // Default
	}

	// Apply orchestration config
	if orch != nil && orch.TimeoutSeconds > 0 {
		forkJoin.TimeoutSeconds = orch.TimeoutSeconds
	}

	// Check for merge strategy in spec.config
	if mergeStr, ok := spec.Config["merge_strategy"].(string); ok {
		forkJoin.MergeStrategy = parseMergeStrategy(mergeStr)
	}

	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_ForkJoin{
			ForkJoin: forkJoin,
		},
	}, nil
}

// buildParallelPattern creates a ParallelPattern from workflow spec.
func buildParallelPattern(spec WorkflowSpec, orch *OrchestrationConfig) (*loomv1.WorkflowPattern, error) {
	tasks := make([]*loomv1.AgentTask, 0, len(spec.Agents))

	for _, agent := range spec.Agents {
		task := &loomv1.AgentTask{
			AgentId: agent.ID,
			Prompt:  agent.PromptTemplate,
		}
		if task.Prompt == "" {
			task.Prompt = "{{user_query}}"
		}
		tasks = append(tasks, task)
	}

	parallel := &loomv1.ParallelPattern{
		Tasks:         tasks,
		MergeStrategy: loomv1.MergeStrategy_CONCATENATE,
	}

	// Check for merge strategy in spec.config
	if mergeStr, ok := spec.Config["merge_strategy"].(string); ok {
		parallel.MergeStrategy = parseMergeStrategy(mergeStr)
	}

	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Parallel{
			Parallel: parallel,
		},
	}, nil
}

// buildDebatePattern creates a DebatePattern from workflow spec.
func buildDebatePattern(spec WorkflowSpec, orch *OrchestrationConfig) (*loomv1.WorkflowPattern, error) {
	agentIDs := make([]string, 0, len(spec.Agents))
	var moderatorID string

	for _, agent := range spec.Agents {
		if agent.Role == "moderator" {
			moderatorID = agent.ID
		} else {
			agentIDs = append(agentIDs, agent.ID)
		}
	}

	if len(agentIDs) < 2 {
		return nil, fmt.Errorf("debate requires at least 2 debating agents")
	}

	rounds := int32(3) // Default
	if orch != nil && orch.MaxRounds > 0 {
		rounds = int32(orch.MaxRounds)
	}
	if roundsInt, ok := spec.Config["rounds"].(int); ok {
		rounds = int32(roundsInt)
	}

	debate := &loomv1.DebatePattern{
		Topic:            "{{user_query}}",
		AgentIds:         agentIDs,
		Rounds:           rounds,
		MergeStrategy:    loomv1.MergeStrategy_CONSENSUS,
		ModeratorAgentId: moderatorID,
	}

	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Debate{
			Debate: debate,
		},
	}, nil
}

// buildConditionalPattern creates a ConditionalPattern from workflow spec.
// Note: This is a simplified implementation. Full nested workflow support requires
// recursive parsing which is complex. For production use, consider using the Go API directly.
func buildConditionalPattern(spec WorkflowSpec, orch *OrchestrationConfig) (*loomv1.WorkflowPattern, error) {
	// Find the condition agent (should have role="classifier" or be the first agent)
	var conditionAgentID string
	var conditionPrompt string

	for _, agentDef := range spec.Agents {
		if agentDef.Role == "classifier" || agentDef.Role == "condition" {
			conditionAgentID = agentDef.ID
			conditionPrompt = agentDef.PromptTemplate
			if conditionPrompt == "" {
				conditionPrompt = agentDef.SystemPrompt
			}
			break
		}
	}

	if conditionAgentID == "" && len(spec.Agents) > 0 {
		// Use first agent as condition agent if no classifier specified
		conditionAgentID = spec.Agents[0].ID
		conditionPrompt = spec.Agents[0].PromptTemplate
		if conditionPrompt == "" {
			conditionPrompt = spec.Agents[0].SystemPrompt
		}
	}

	if conditionAgentID == "" {
		return nil, fmt.Errorf("conditional pattern requires at least one agent for condition evaluation")
	}

	if conditionPrompt == "" {
		conditionPrompt = "{{user_query}}"
	}

	conditional := &loomv1.ConditionalPattern{
		ConditionAgentId: conditionAgentID,
		ConditionPrompt:  conditionPrompt,
		Branches:         make(map[string]*loomv1.WorkflowPattern),
		DefaultBranch:    nil,
	}

	// Note: Branch definitions require nested workflow patterns which aren't
	// easily expressed in simple YAML. For now, return a valid but minimal pattern.
	// Full conditional workflow support should use the Go API or enhanced YAML schema.

	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Conditional{
			Conditional: conditional,
		},
	}, nil
}

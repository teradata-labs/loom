// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// buildCreateAgentSchema returns the JSONSchema for create_agent action.
// This schema maps to the K8s-style agent YAML structure (apiVersion, kind, metadata, spec).
func buildCreateAgentSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Create a new agent configuration",
		map[string]*shuttle.JSONSchema{
			"action": shuttle.NewStringSchema("Must be 'create_agent'").
				WithEnum("create_agent"),
			"config": buildAgentConfigSchema(),
		},
		[]string{"action", "config"},
	)
}

// buildUpdateAgentSchema returns the JSONSchema for update_agent action.
func buildUpdateAgentSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Update an existing agent configuration",
		map[string]*shuttle.JSONSchema{
			"action": shuttle.NewStringSchema("Must be 'update_agent'").
				WithEnum("update_agent"),
			"name":   shuttle.NewStringSchema("Name of the agent to update (filename without .yaml)"),
			"config": buildAgentConfigSchema(),
		},
		[]string{"action", "name", "config"},
	)
}

// buildCreateWorkflowSchema returns the JSONSchema for create_workflow action.
func buildCreateWorkflowSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Create a new workflow configuration",
		map[string]*shuttle.JSONSchema{
			"action": shuttle.NewStringSchema("Must be 'create_workflow'").
				WithEnum("create_workflow"),
			"config": buildWorkflowConfigSchema(),
		},
		[]string{"action", "config"},
	)
}

// buildUpdateWorkflowSchema returns the JSONSchema for update_workflow action.
func buildUpdateWorkflowSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Update an existing workflow configuration",
		map[string]*shuttle.JSONSchema{
			"action": shuttle.NewStringSchema("Must be 'update_workflow'").
				WithEnum("update_workflow"),
			"name":   shuttle.NewStringSchema("Name of the workflow to update (filename without .yaml)"),
			"config": buildWorkflowConfigSchema(),
		},
		[]string{"action", "name", "config"},
	)
}

// buildAgentConfigSchema builds the schema for agent configuration.
// Maps to K8s-style structure with apiVersion, kind, metadata, spec.
func buildAgentConfigSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Agent configuration in Kubernetes-style format",
		map[string]*shuttle.JSONSchema{
			"apiVersion": shuttle.NewStringSchema("API version (must be 'loom/v1')").
				WithEnum("loom/v1").
				WithDefault("loom/v1"),
			"kind": shuttle.NewStringSchema("Resource kind (must be 'Agent')").
				WithEnum("Agent").
				WithDefault("Agent"),
			"metadata": buildMetadataSchema(),
			"spec":     buildAgentSpecSchema(),
		},
		[]string{"metadata", "spec"},
	)
}

// buildWorkflowConfigSchema builds the schema for workflow configuration.
func buildWorkflowConfigSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Workflow configuration in Kubernetes-style format",
		map[string]*shuttle.JSONSchema{
			"apiVersion": shuttle.NewStringSchema("API version (must be 'loom/v1')").
				WithEnum("loom/v1").
				WithDefault("loom/v1"),
			"kind": shuttle.NewStringSchema("Resource kind (must be 'Workflow')").
				WithEnum("Workflow").
				WithDefault("Workflow"),
			"metadata": buildMetadataSchema(),
			"spec":     buildWorkflowSpecSchema(),
			"schedule": buildScheduleSchema(),
		},
		[]string{"metadata", "spec"},
	)
}

// buildMetadataSchema builds the schema for metadata section (shared by agents and workflows).
func buildMetadataSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Metadata for the resource",
		map[string]*shuttle.JSONSchema{
			"name": shuttle.NewStringSchema("Unique name for the agent or workflow"),
			"version": shuttle.NewStringSchema("Version string (default: '1.0.0')").
				WithDefault("1.0.0"),
			"description": shuttle.NewStringSchema("Human-readable description"),
			"labels": shuttle.NewObjectSchema(
				"Optional key-value labels (any string keys/values)",
				map[string]*shuttle.JSONSchema{},
				[]string{},
			),
		},
		[]string{"name"},
	)
}

// buildAgentSpecSchema builds the schema for agent spec section.
func buildAgentSpecSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Agent specification",
		map[string]*shuttle.JSONSchema{
			"system_prompt": shuttle.NewStringSchema("System prompt defining agent behavior and persona"),
			"tools":         buildToolsSchema(),
			"llm":           buildLLMConfigSchema(),
			"memory":        buildMemoryConfigSchema(),
			"behavior":      buildBehaviorConfigSchema(),
			"backend_path":  shuttle.NewStringSchema("Optional path to execution backend configuration"),
			"rom": shuttle.NewStringSchema("ROM identifier for domain-specific knowledge").
				WithEnum("", "auto", "TD", "teradata").
				WithDefault("auto"),
			"enable_finding_extraction": shuttle.NewBooleanSchema("Enable automatic finding extraction (default: true)").
				WithDefault(true),
			"extraction_cadence": shuttle.NewNumberSchema("Tool executions between automatic finding extractions (default: 3)").
				WithDefault(3),
			"max_findings": shuttle.NewNumberSchema("Maximum findings to keep in cache (default: 50)").
				WithDefault(50),
		},
		[]string{"system_prompt", "tools"},
	)
}

// buildToolsSchema builds the schema for tools configuration.
func buildToolsSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Tools configuration",
		map[string]*shuttle.JSONSchema{
			"builtin": shuttle.NewArraySchema(
				"Built-in tool names",
				shuttle.NewStringSchema("Tool name"),
			),
			"mcp": shuttle.NewArraySchema(
				"MCP (Model Context Protocol) tool configurations",
				shuttle.NewObjectSchema(
					"MCP tool config",
					map[string]*shuttle.JSONSchema{
						"server": shuttle.NewStringSchema("MCP server name"),
						"tools": shuttle.NewArraySchema(
							"Specific tool names to enable (empty = all tools)",
							shuttle.NewStringSchema("Tool name"),
						),
					},
					[]string{"server"},
				),
			),
			"custom": shuttle.NewArraySchema(
				"Custom tool implementations",
				shuttle.NewObjectSchema(
					"Custom tool config",
					map[string]*shuttle.JSONSchema{
						"name":           shuttle.NewStringSchema("Tool name"),
						"implementation": shuttle.NewStringSchema("Path to tool implementation"),
					},
					[]string{"name", "implementation"},
				),
			),
		},
		[]string{},
	)
}

// buildLLMConfigSchema builds the schema for LLM configuration.
func buildLLMConfigSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"LLM configuration",
		map[string]*shuttle.JSONSchema{
			"provider": shuttle.NewStringSchema("LLM provider").
				WithEnum("anthropic", "bedrock", "ollama").
				WithDefault("anthropic"),
			"model": shuttle.NewStringSchema("Model identifier (e.g., 'claude-3-5-sonnet-20241022-v2:0')").
				WithDefault("claude-3-5-sonnet-20241022-v2:0"),
			"temperature": shuttle.NewNumberSchema("Temperature (0.0-1.0) controls randomness").
				WithDefault(0.7),
			"max_tokens": shuttle.NewNumberSchema("Maximum tokens in response").
				WithDefault(4096),
			"stop_sequences": shuttle.NewArraySchema(
				"Stop sequences to end generation",
				shuttle.NewStringSchema("Stop sequence"),
			),
			"top_p":                  shuttle.NewNumberSchema("Top-p sampling parameter (0.0-1.0)"),
			"top_k":                  shuttle.NewNumberSchema("Top-k sampling parameter"),
			"max_context_tokens":     shuttle.NewNumberSchema("Maximum context window tokens"),
			"reserved_output_tokens": shuttle.NewNumberSchema("Tokens reserved for model output"),
		},
		[]string{},
	)
}

// buildMemoryConfigSchema builds the schema for memory configuration.
func buildMemoryConfigSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Memory and session storage configuration",
		map[string]*shuttle.JSONSchema{
			"type": shuttle.NewStringSchema("Storage type").
				WithEnum("memory", "sqlite", "postgres").
				WithDefault("memory"),
			"path": shuttle.NewStringSchema("File path for sqlite storage"),
			"dsn":  shuttle.NewStringSchema("Database DSN for postgres storage"),
			"max_history": shuttle.NewNumberSchema("Maximum conversation history to retain").
				WithDefault(50),
			"memory_compression": buildMemoryCompressionSchema(),
		},
		[]string{},
	)
}

// buildMemoryCompressionSchema builds the schema for memory compression configuration.
func buildMemoryCompressionSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Memory compression configuration",
		map[string]*shuttle.JSONSchema{
			"workload_profile": shuttle.NewStringSchema("Workload profile preset").
				WithEnum("balanced", "data_intensive", "conversational").
				WithDefault("balanced"),
			"max_l1_messages":            shuttle.NewNumberSchema("Maximum messages in L1 cache before compression"),
			"min_l1_messages":            shuttle.NewNumberSchema("Minimum messages in L1 cache after compression"),
			"warning_threshold_percent":  shuttle.NewNumberSchema("Warning threshold percentage (0-100)"),
			"critical_threshold_percent": shuttle.NewNumberSchema("Critical threshold percentage (0-100)"),
			"batch_sizes": shuttle.NewObjectSchema(
				"Batch sizes for compression operations",
				map[string]*shuttle.JSONSchema{
					"normal":   shuttle.NewNumberSchema("Messages to compress in normal conditions").WithDefault(3),
					"warning":  shuttle.NewNumberSchema("Messages to compress under warning threshold").WithDefault(5),
					"critical": shuttle.NewNumberSchema("Messages to compress under critical threshold").WithDefault(7),
				},
				[]string{},
			),
		},
		[]string{},
	)
}

// buildBehaviorConfigSchema builds the schema for behavior configuration.
func buildBehaviorConfigSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Behavior constraints and limits",
		map[string]*shuttle.JSONSchema{
			"max_iterations": shuttle.NewNumberSchema("Maximum tool call iterations per turn").
				WithDefault(10),
			"timeout_seconds": shuttle.NewNumberSchema("Timeout in seconds for each message").
				WithDefault(300),
			"allow_code_execution": shuttle.NewBooleanSchema("Whether to allow code execution").
				WithDefault(false),
			"allowed_domains": shuttle.NewArraySchema(
				"Allowed domains for web access (empty = all)",
				shuttle.NewStringSchema("Domain"),
			),
			"max_turns": shuttle.NewNumberSchema("Maximum conversation turns").
				WithDefault(25),
			"max_tool_executions": shuttle.NewNumberSchema("Maximum tool executions per conversation").
				WithDefault(50),
			"patterns": buildPatternConfigSchema(),
		},
		[]string{},
	)
}

// buildPatternConfigSchema builds the schema for pattern configuration.
func buildPatternConfigSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Pattern-guided learning configuration",
		map[string]*shuttle.JSONSchema{
			"enabled": shuttle.NewBooleanSchema("Enable pattern injection").
				WithDefault(true),
			"min_confidence": shuttle.NewNumberSchema("Minimum confidence threshold (0.0-1.0)").
				WithDefault(0.75),
			"max_patterns_per_turn": shuttle.NewNumberSchema("Maximum patterns to inject per turn").
				WithDefault(1),
			"enable_tracking": shuttle.NewBooleanSchema("Enable pattern effectiveness tracking").
				WithDefault(true),
			"use_llm_classifier": shuttle.NewBooleanSchema("Use LLM-based intent classification").
				WithDefault(true),
		},
		[]string{},
	)
}

// buildWorkflowSpecSchema builds the schema for workflow spec section.
func buildWorkflowSpecSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Workflow specification",
		map[string]*shuttle.JSONSchema{
			"type": shuttle.NewStringSchema("Workflow pattern type").
				WithEnum("debate", "fork-join", "pipeline", "parallel", "conditional", "iterative", "swarm"),
			// Pattern-specific fields (using additionalProperties for flexibility)
			"topic":                shuttle.NewStringSchema("Topic or question (for debate pattern)"),
			"prompt":               shuttle.NewStringSchema("Prompt sent to agents (for fork-join pattern)"),
			"initial_prompt":       shuttle.NewStringSchema("Initial prompt (for pipeline pattern)"),
			"agent_ids":            shuttle.NewArraySchema("Agent IDs", shuttle.NewStringSchema("Agent ID")),
			"rounds":               shuttle.NewNumberSchema("Number of debate rounds (for debate pattern)"),
			"merge_strategy":       shuttle.NewStringSchema("Strategy for merging results").WithEnum("consensus", "voting", "concatenate", "first", "best", "summary"),
			"moderator_agent_id":   shuttle.NewStringSchema("Moderator agent ID (for debate pattern)"),
			"timeout_seconds":      shuttle.NewNumberSchema("Timeout for execution (seconds)"),
			"pass_full_history":    shuttle.NewBooleanSchema("Pass full history to each stage (for pipeline)"),
			"stages":               buildPipelineStagesSchema(),
			"tasks":                buildParallelTasksSchema(),
			"condition_agent_id":   shuttle.NewStringSchema("Agent ID for condition evaluation (for conditional)"),
			"condition_prompt":     shuttle.NewStringSchema("Prompt for condition agent (for conditional)"),
			"max_iterations":       shuttle.NewNumberSchema("Maximum iterations (for iterative)"),
			"restart_policy":       buildRestartPolicySchema(),
			"restart_triggers":     shuttle.NewArraySchema("Stage IDs allowed to trigger restarts", shuttle.NewStringSchema("Stage ID")),
			"restart_topic":        shuttle.NewStringSchema("Topic for restart coordination messages"),
			"swarm_size":           shuttle.NewNumberSchema("Number of agents in swarm"),
			"task_description":     shuttle.NewStringSchema("Task description for swarm"),
			"aggregation_strategy": shuttle.NewStringSchema("Aggregation strategy for swarm results"),
		},
		[]string{"type"},
	)
}

// buildPipelineStagesSchema builds the schema for pipeline stages.
func buildPipelineStagesSchema() *shuttle.JSONSchema {
	return shuttle.NewArraySchema(
		"Pipeline stages",
		shuttle.NewObjectSchema(
			"Pipeline stage",
			map[string]*shuttle.JSONSchema{
				"agent_id":          shuttle.NewStringSchema("Agent ID for this stage (references agent config filename without .yaml)"),
				"prompt_template":   shuttle.NewStringSchema("Prompt template (can include {{previous}} or {{history}} placeholders)"),
				"validation_prompt": shuttle.NewStringSchema("Optional validation function prompt"),
			},
			[]string{"agent_id"},
		),
	)
}

// buildParallelTasksSchema builds the schema for parallel tasks.
func buildParallelTasksSchema() *shuttle.JSONSchema {
	return shuttle.NewArraySchema(
		"Independent tasks for parallel execution",
		shuttle.NewObjectSchema(
			"Agent task",
			map[string]*shuttle.JSONSchema{
				"agent_id": shuttle.NewStringSchema("Agent ID for this task (references agent config filename without .yaml)"),
				"prompt":   shuttle.NewStringSchema("Task-specific prompt"),
				"metadata": shuttle.NewObjectSchema(
					"Optional task metadata (any string keys/values)",
					map[string]*shuttle.JSONSchema{},
					[]string{},
				),
			},
			[]string{"agent_id", "prompt"},
		),
	)
}

// buildRestartPolicySchema builds the schema for restart policy configuration.
func buildRestartPolicySchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Restart policy for iterative workflows",
		map[string]*shuttle.JSONSchema{
			"enabled": shuttle.NewBooleanSchema("Whether to allow restarts").
				WithDefault(true),
			"restartable_stages": shuttle.NewArraySchema(
				"Which stages can be restarted (empty = all)",
				shuttle.NewStringSchema("Stage ID"),
			),
			"cooldown_seconds": shuttle.NewNumberSchema("Cooldown period between restarts").
				WithDefault(0),
			"reset_shared_memory": shuttle.NewBooleanSchema("Reset shared memory on restart").
				WithDefault(false),
			"preserve_outputs": shuttle.NewBooleanSchema("Preserve stage outputs from previous iteration").
				WithDefault(true),
			"max_validation_retries": shuttle.NewNumberSchema("Maximum validation retries per stage").
				WithDefault(2),
		},
		[]string{},
	)
}

// buildScheduleSchema builds the schema for workflow schedule configuration.
func buildScheduleSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Optional schedule configuration for automatic execution",
		map[string]*shuttle.JSONSchema{
			"cron": shuttle.NewStringSchema("Cron expression (e.g., '0 */6 * * *' for every 6 hours)"),
			"timezone": shuttle.NewStringSchema("Timezone for cron evaluation (e.g., 'America/New_York', 'UTC')").
				WithDefault("UTC"),
			"enabled": shuttle.NewBooleanSchema("Whether this schedule is enabled").
				WithDefault(true),
			"skip_if_running": shuttle.NewBooleanSchema("Skip execution if previous run is still active").
				WithDefault(true),
			"max_execution_seconds": shuttle.NewNumberSchema("Maximum execution time before considering workflow stuck").
				WithDefault(3600),
			"variables": shuttle.NewObjectSchema(
				"Variables to pass to workflow execution (any string keys/values)",
				map[string]*shuttle.JSONSchema{},
				[]string{},
			),
		},
		[]string{"cron"},
	)
}

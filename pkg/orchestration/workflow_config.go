// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/types"
	"github.com/teradata-labs/loom/pkg/validation"
	"gopkg.in/yaml.v3"
)

// Custom errors for workflow config loading
var (
	ErrFileNotFound       = fmt.Errorf("workflow file not found")
	ErrInvalidPermissions = fmt.Errorf("insufficient permissions to read workflow file")
	ErrInvalidYAML        = fmt.Errorf("invalid YAML syntax in workflow file")
	ErrInvalidWorkflow    = fmt.Errorf("invalid workflow structure")
	ErrUnsupportedPattern = fmt.Errorf("unsupported workflow pattern type")
)

// WorkflowConfig represents the Kubernetes-style YAML structure.
// Based on dogfooding recommendations: apiVersion, kind, metadata, spec
type WorkflowConfig struct {
	APIVersion string                 `yaml:"apiVersion"`
	Kind       string                 `yaml:"kind"`
	Metadata   WorkflowMetadata       `yaml:"metadata"`
	Spec       map[string]interface{} `yaml:"spec"`
	Schedule   *ScheduleYAML          `yaml:"schedule,omitempty"`
}

// ScheduleYAML represents the schedule configuration in workflow YAML files.
type ScheduleYAML struct {
	Cron                string            `yaml:"cron"`
	Timezone            string            `yaml:"timezone,omitempty"`
	Enabled             bool              `yaml:"enabled"`
	SkipIfRunning       bool              `yaml:"skip_if_running,omitempty"`
	MaxExecutionSeconds int32             `yaml:"max_execution_seconds,omitempty"`
	Variables           map[string]string `yaml:"variables,omitempty"`
	SessionMode         string            `yaml:"session_mode,omitempty"` // "new" or "resume"
}

// WorkflowMetadata contains workflow identification information
type WorkflowMetadata struct {
	Name        string            `yaml:"name"`
	Version     string            `yaml:"version,omitempty"`
	Description string            `yaml:"description,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"`
}

// LoadWorkflowFromYAML loads and parses a workflow definition from a YAML file.
//
// Parameters:
//   - path: File system path to the YAML workflow definition file
//
// Returns:
//   - *loomv1.WorkflowPattern: Parsed workflow proto message
//   - error: Error if file cannot be read or contains invalid YAML/workflow structure
//
// Errors:
//   - ErrFileNotFound: If the specified path does not exist
//   - ErrInvalidPermissions: If the file cannot be read
//   - ErrInvalidYAML: If the YAML syntax is invalid
//   - ErrInvalidWorkflow: If the workflow structure is invalid
//   - ErrUnsupportedPattern: If the pattern type is not recognized
func LoadWorkflowFromYAML(path string) (*loomv1.WorkflowPattern, error) {
	return LoadWorkflowFromYAMLWithLogger(path, nil)
}

// LoadWorkflowFromYAMLWithLogger is LoadWorkflowFromYAML with a logger for the
// loader's tolerance warnings — the diagnostics for YAML the loader accepts but
// does not take at face value (see parseOutputRetryPolicy). A nil logger
// discards them; nothing else about the load changes.
func LoadWorkflowFromYAMLWithLogger(path string, logger *zap.Logger) (*loomv1.WorkflowPattern, error) {
	data, err := readWorkflowFile(path)
	if err != nil {
		return nil, err
	}
	return LoadWorkflowFromYAMLBytesWithLogger(data, logger)
}

// LoadWorkflowFromYAMLBytes parses, validates, and converts a workflow YAML
// document already in memory into a WorkflowPattern. It is the path-free core
// of LoadWorkflowFromYAML, used when the YAML arrives over the wire (e.g. the
// loom_execute_workflow MCP tool's workflow_yaml argument) rather than from a
// file on disk.
func LoadWorkflowFromYAMLBytes(data []byte) (*loomv1.WorkflowPattern, error) {
	return LoadWorkflowFromYAMLBytesWithLogger(data, nil)
}

// LoadWorkflowFromYAMLBytesWithLogger is LoadWorkflowFromYAMLBytes with a
// logger for the loader's tolerance warnings. A nil logger discards them.
func LoadWorkflowFromYAMLBytesWithLogger(data []byte, logger *zap.Logger) (*loomv1.WorkflowPattern, error) {
	config, err := parseWorkflowYAML(data)
	if err != nil {
		return nil, err
	}
	if err := validateWorkflowStructure(config); err != nil {
		return nil, err
	}
	pattern, err := convertToProto(config, loaderLogger(logger))
	if err != nil {
		return nil, err
	}
	// HITL gates are only valid on pipeline/iterative patterns and must have
	// resolvable, non-forward revise targets.
	if err := validateGatePlacement(pattern); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidWorkflow, err.Error())
	}
	return pattern, nil
}

// loaderLogger normalizes the optional logger the exported loaders accept. The
// loader has no logger of its own and takes no global one: a caller that wants
// the tolerance warnings passes its logger in, and nil — which is what the
// logger-free entry points pass — means "discard them".
func loaderLogger(logger *zap.Logger) *zap.Logger {
	if logger == nil {
		return zap.NewNop()
	}
	return logger
}

// readWorkflowFile reads the workflow file from disk
func readWorkflowFile(path string) ([]byte, error) {
	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: %s", ErrFileNotFound, path)
	}

	// Read file content
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		if os.IsPermission(err) {
			return nil, fmt.Errorf("%w: %s", ErrInvalidPermissions, path)
		}
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return data, nil
}

// parseWorkflowYAML parses YAML data into WorkflowConfig
func parseWorkflowYAML(data []byte) (*WorkflowConfig, error) {
	var config WorkflowConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidYAML, err.Error())
	}
	return &config, nil
}

// validateWorkflowStructure validates the top-level workflow structure
func validateWorkflowStructure(config *WorkflowConfig) error {
	// Check apiVersion
	if config.APIVersion == "" {
		return fmt.Errorf("%w: missing apiVersion — a workflow YAML needs 'apiVersion: loom/v1', 'kind: Workflow', and a 'spec:' block with a 'type:'. To run a workflow you already saved, pass workflow_ref (its name; see loom_list_workflows) instead of re-supplying the full YAML", ErrInvalidWorkflow)
	}
	if config.APIVersion != "loom/v1" {
		return fmt.Errorf("%w: unsupported apiVersion '%s', expected 'loom/v1'", ErrInvalidWorkflow, config.APIVersion)
	}

	// Check kind
	if config.Kind == "" {
		return fmt.Errorf("%w: missing kind", ErrInvalidWorkflow)
	}
	if config.Kind != "Workflow" {
		return fmt.Errorf("%w: unsupported kind '%s', expected 'Workflow'", ErrInvalidWorkflow, config.Kind)
	}

	// Check metadata
	if config.Metadata.Name == "" {
		return fmt.Errorf("%w: missing metadata.name", ErrInvalidWorkflow)
	}

	// Check spec
	if len(config.Spec) == 0 {
		return fmt.Errorf("%w: missing spec", ErrInvalidWorkflow)
	}

	// Check spec.type
	patternType, ok := config.Spec["type"].(string)
	if !ok {
		return fmt.Errorf("%w: missing spec.type", ErrInvalidWorkflow)
	}

	// Validate pattern type
	validTypes := validation.CanonicalWorkflowPatternTypes()
	isValid := false
	for _, validType := range validTypes {
		if patternType == validType {
			isValid = true
			break
		}
	}
	if !isValid {
		return fmt.Errorf("%w: '%s', must be one of: %v", ErrUnsupportedPattern, patternType, validTypes)
	}

	return nil
}

// convertToProto converts WorkflowConfig to loomv1.WorkflowPattern. logger
// carries the loader's tolerance warnings and is never nil on this path — the
// exported entry points run it through loaderLogger first. The debate and
// fork-join converters take no logger because neither parses a block that can
// warn.
func convertToProto(config *WorkflowConfig, logger *zap.Logger) (*loomv1.WorkflowPattern, error) {
	patternType, ok := config.Spec["type"].(string)
	if !ok {
		return nil, fmt.Errorf("%w: spec.type must be a string", ErrInvalidWorkflow)
	}

	switch patternType {
	case "debate":
		return convertDebatePattern(config.Spec)
	case "fork-join":
		return convertForkJoinPattern(config.Spec)
	case "pipeline":
		return convertPipelinePattern(config.Spec, logger)
	case "parallel":
		return convertParallelPattern(config.Spec, logger)
	case "conditional":
		return convertConditionalPattern(config.Spec, logger)
	case "iterative":
		return convertIterativePattern(config.Spec, logger)
	case "swarm":
		return convertSwarmPattern(config.Spec, logger)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedPattern, patternType)
	}
}

// convertDebatePattern converts spec to DebatePattern proto
func convertDebatePattern(spec map[string]interface{}) (*loomv1.WorkflowPattern, error) {
	// Extract topic
	topic, ok := spec["topic"].(string)
	if !ok {
		return nil, fmt.Errorf("%w: debate pattern requires 'topic' field", ErrInvalidWorkflow)
	}

	// Extract agent_ids
	agentIDsRaw, ok := spec["agent_ids"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: debate pattern requires 'agent_ids' field", ErrInvalidWorkflow)
	}
	agentIDs := make([]string, len(agentIDsRaw))
	for i, id := range agentIDsRaw {
		agentIDs[i], ok = id.(string)
		if !ok {
			return nil, fmt.Errorf("%w: agent_ids must be strings", ErrInvalidWorkflow)
		}
	}

	// Extract rounds (default to 1)
	rounds := int32(1)
	if roundsRaw, ok := spec["rounds"]; ok {
		switch v := roundsRaw.(type) {
		case int:
			rounds = types.SafeInt32(v)
		case int32:
			rounds = v
		case int64:
			rounds = types.SafeInt32FromInt64(v)
		}
	}

	// Extract merge_strategy (default to CONSENSUS)
	mergeStrategy := loomv1.MergeStrategy_CONSENSUS
	if strategyRaw, ok := spec["merge_strategy"].(string); ok {
		mergeStrategy = parseMergeStrategy(strategyRaw)
	}

	// Extract optional moderator_agent_id
	moderatorID, _ := spec["moderator_agent_id"].(string)

	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Debate{
			Debate: &loomv1.DebatePattern{
				Topic:            topic,
				AgentIds:         agentIDs,
				Rounds:           rounds,
				MergeStrategy:    mergeStrategy,
				ModeratorAgentId: moderatorID,
			},
		},
	}, nil
}

// convertForkJoinPattern converts spec to ForkJoinPattern proto
func convertForkJoinPattern(spec map[string]interface{}) (*loomv1.WorkflowPattern, error) {
	// Extract prompt
	prompt, ok := spec["prompt"].(string)
	if !ok {
		return nil, fmt.Errorf("%w: fork-join pattern requires 'prompt' field", ErrInvalidWorkflow)
	}

	// Extract agent_ids
	agentIDsRaw, ok := spec["agent_ids"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: fork-join pattern requires 'agent_ids' field", ErrInvalidWorkflow)
	}
	agentIDs := make([]string, len(agentIDsRaw))
	for i, id := range agentIDsRaw {
		agentIDs[i], ok = id.(string)
		if !ok {
			return nil, fmt.Errorf("%w: agent_ids must be strings", ErrInvalidWorkflow)
		}
	}

	// Extract merge_strategy (default to CONCATENATE)
	mergeStrategy := loomv1.MergeStrategy_CONCATENATE
	if strategyRaw, ok := spec["merge_strategy"].(string); ok {
		mergeStrategy = parseMergeStrategy(strategyRaw)
	}

	// Extract optional timeout_seconds
	timeoutSeconds := int32(0)
	if timeoutRaw, ok := spec["timeout_seconds"]; ok {
		switch v := timeoutRaw.(type) {
		case int:
			timeoutSeconds = types.SafeInt32(v)
		case int32:
			timeoutSeconds = v
		case int64:
			timeoutSeconds = types.SafeInt32FromInt64(v)
		}
	}

	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_ForkJoin{
			ForkJoin: &loomv1.ForkJoinPattern{
				Prompt:         prompt,
				AgentIds:       agentIDs,
				MergeStrategy:  mergeStrategy,
				TimeoutSeconds: timeoutSeconds,
			},
		},
	}, nil
}

// convertPipelinePattern converts spec to PipelinePattern proto
func convertPipelinePattern(spec map[string]interface{}, logger *zap.Logger) (*loomv1.WorkflowPattern, error) {
	// Extract initial_prompt
	initialPrompt, ok := spec["initial_prompt"].(string)
	if !ok {
		return nil, fmt.Errorf("%w: pipeline pattern requires 'initial_prompt' field", ErrInvalidWorkflow)
	}

	// Extract stages
	stagesRaw, ok := spec["stages"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: pipeline pattern requires 'stages' field", ErrInvalidWorkflow)
	}

	stages := make([]*loomv1.PipelineStage, len(stagesRaw))
	for i, stageRaw := range stagesRaw {
		stageMap, ok := stageRaw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("%w: each stage must be an object", ErrInvalidWorkflow)
		}

		agentID, ok := stageMap["agent_id"].(string)
		if !ok {
			return nil, fmt.Errorf("%w: stage %d missing 'agent_id'", ErrInvalidWorkflow, i)
		}

		promptTemplate, ok := stageMap["prompt_template"].(string)
		if !ok {
			return nil, fmt.Errorf("%w: stage %d missing 'prompt_template'", ErrInvalidWorkflow, i)
		}

		stagePath := fmt.Sprintf("spec.stages[%d]", i)

		validationPrompt, _ := stageMap["validation_prompt"].(string)
		outputSchema, _ := stageMap["output_schema"].(string)
		retryPolicy, err := parseOutputRetryPolicy(stageMap, stagePath, logger)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidWorkflow, err.Error())
		}
		outputPolicy, err := parseOutputPolicy(stageMap, stagePath, logger)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidWorkflow, err.Error())
		}
		levelingPolicy, err := parseLevelingPolicy(stageMap, stagePath)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidWorkflow, err.Error())
		}
		hitlGate, err := parseHITLGate(stageMap)
		if err != nil {
			return nil, fmt.Errorf("%w: stage %d: %s", ErrInvalidWorkflow, i, err.Error())
		}

		stages[i] = &loomv1.PipelineStage{
			AgentId:          agentID,
			PromptTemplate:   promptTemplate,
			ValidationPrompt: validationPrompt,
			OutputSchema:     outputSchema,
			RetryPolicy:      retryPolicy,
			OutputPolicy:     outputPolicy,
			HitlGate:         hitlGate,
			LevelingPolicy:   levelingPolicy,
		}
	}

	// Extract pass_full_history (default false)
	passFullHistory := false
	if historyRaw, ok := spec["pass_full_history"].(bool); ok {
		passFullHistory = historyRaw
	}

	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt:   initialPrompt,
				Stages:          stages,
				PassFullHistory: passFullHistory,
			},
		},
	}, nil
}

// convertParallelPattern converts spec to ParallelPattern proto
func convertParallelPattern(spec map[string]interface{}, logger *zap.Logger) (*loomv1.WorkflowPattern, error) {
	// Extract tasks
	tasksRaw, ok := spec["tasks"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: parallel pattern requires 'tasks' field", ErrInvalidWorkflow)
	}

	tasks := make([]*loomv1.AgentTask, len(tasksRaw))
	for i, taskRaw := range tasksRaw {
		taskMap, ok := taskRaw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("%w: each task must be an object", ErrInvalidWorkflow)
		}

		agentID, ok := taskMap["agent_id"].(string)
		if !ok {
			return nil, fmt.Errorf("%w: task %d missing 'agent_id'", ErrInvalidWorkflow, i)
		}

		prompt, ok := taskMap["prompt"].(string)
		if !ok {
			return nil, fmt.Errorf("%w: task %d missing 'prompt'", ErrInvalidWorkflow, i)
		}

		// Extract optional metadata
		metadata := make(map[string]string)
		if metadataRaw, ok := taskMap["metadata"].(map[string]interface{}); ok {
			for k, v := range metadataRaw {
				if strVal, ok := v.(string); ok {
					metadata[k] = strVal
				}
			}
		}

		taskPath := fmt.Sprintf("spec.tasks[%d]", i)
		outputPolicy, err := parseOutputPolicy(taskMap, taskPath, logger)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidWorkflow, err.Error())
		}
		levelingPolicy, err := parseLevelingPolicy(taskMap, taskPath)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidWorkflow, err.Error())
		}

		tasks[i] = &loomv1.AgentTask{
			AgentId:        agentID,
			Prompt:         prompt,
			Metadata:       metadata,
			OutputPolicy:   outputPolicy,
			LevelingPolicy: levelingPolicy,
		}
	}

	// Extract merge_strategy (optional)
	mergeStrategy := loomv1.MergeStrategy_CONCATENATE
	if strategyRaw, ok := spec["merge_strategy"].(string); ok {
		mergeStrategy = parseMergeStrategy(strategyRaw)
	}

	// Extract optional timeout_seconds
	timeoutSeconds := int32(0)
	if timeoutRaw, ok := spec["timeout_seconds"]; ok {
		switch v := timeoutRaw.(type) {
		case int:
			timeoutSeconds = types.SafeInt32(v)
		case int32:
			timeoutSeconds = v
		case int64:
			timeoutSeconds = types.SafeInt32FromInt64(v)
		}
	}

	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Parallel{
			Parallel: &loomv1.ParallelPattern{
				Tasks:          tasks,
				MergeStrategy:  mergeStrategy,
				TimeoutSeconds: timeoutSeconds,
			},
		},
	}, nil
}

// convertConditionalPattern converts spec to ConditionalPattern proto
func convertConditionalPattern(spec map[string]interface{}, logger *zap.Logger) (*loomv1.WorkflowPattern, error) {
	// Extract condition_agent_id
	conditionAgentID, ok := spec["condition_agent_id"].(string)
	if !ok {
		return nil, fmt.Errorf("%w: conditional pattern requires 'condition_agent_id' field", ErrInvalidWorkflow)
	}

	// Extract condition_prompt
	conditionPrompt, ok := spec["condition_prompt"].(string)
	if !ok {
		return nil, fmt.Errorf("%w: conditional pattern requires 'condition_prompt' field", ErrInvalidWorkflow)
	}

	// Extract branches
	branchesRaw, ok := spec["branches"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: conditional pattern requires 'branches' field", ErrInvalidWorkflow)
	}

	branches := make(map[string]*loomv1.WorkflowPattern)
	for key, branchRaw := range branchesRaw {
		branchConfig, ok := branchRaw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("%w: branch '%s' must be an object with 'type' field", ErrInvalidWorkflow, key)
		}

		// Each branch is a nested workflow pattern
		branchPattern, err := convertToProto(&WorkflowConfig{
			APIVersion: "loom/v1",
			Kind:       "Workflow",
			Metadata:   WorkflowMetadata{Name: "nested-" + key},
			Spec:       branchConfig,
		}, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to parse branch '%s': %w", key, err)
		}

		branches[key] = branchPattern
	}

	// Extract optional default_branch
	var defaultBranch *loomv1.WorkflowPattern
	if defaultRaw, ok := spec["default_branch"].(map[string]interface{}); ok {
		var err error
		defaultBranch, err = convertToProto(&WorkflowConfig{
			APIVersion: "loom/v1",
			Kind:       "Workflow",
			Metadata:   WorkflowMetadata{Name: "default-branch"},
			Spec:       defaultRaw,
		}, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to parse default_branch: %w", err)
		}
	}

	// Extract optional retry_policy
	retryPolicy, err := parseOutputRetryPolicy(spec, "spec", logger)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidWorkflow, err.Error())
	}

	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Conditional{
			Conditional: &loomv1.ConditionalPattern{
				ConditionAgentId: conditionAgentID,
				ConditionPrompt:  conditionPrompt,
				Branches:         branches,
				DefaultBranch:    defaultBranch,
				RetryPolicy:      retryPolicy,
			},
		},
	}, nil
}

// convertIterativePattern converts spec to IterativeWorkflowPattern proto
func convertIterativePattern(spec map[string]interface{}, logger *zap.Logger) (*loomv1.WorkflowPattern, error) {
	// Extract pipeline configuration (nested under "pipeline" key)
	pipelineSpec, ok := spec["pipeline"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: iterative pattern requires 'pipeline' field", ErrInvalidWorkflow)
	}

	// Parse the base pipeline using existing converter
	// We need to add the "type" field to make it compatible with convertPipelinePattern
	pipelineSpec["type"] = "pipeline"
	pipelinePattern, err := convertPipelinePattern(pipelineSpec, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to parse base pipeline: %w", err)
	}

	// Extract max_iterations (default: 3)
	maxIterations := int32(3)
	if maxIter, ok := spec["max_iterations"]; ok {
		switch v := maxIter.(type) {
		case int:
			maxIterations = types.SafeInt32(v)
		case int32:
			maxIterations = v
		case int64:
			maxIterations = types.SafeInt32FromInt64(v)
		default:
			// If not a valid integer type, this is an error (e.g., string)
			return nil, fmt.Errorf("%w: max_iterations must be an integer, got %T", ErrInvalidWorkflow, v)
		}
	}

	// Extract restart_policy (optional)
	var restartPolicy *loomv1.RestartPolicy
	if policyRaw, ok := spec["restart_policy"].(map[string]interface{}); ok {
		restartPolicy = &loomv1.RestartPolicy{}

		// Extract enabled (required)
		if enabled, ok := policyRaw["enabled"].(bool); ok {
			restartPolicy.Enabled = enabled
		}

		// Extract restartable_stages (optional)
		if stagesRaw, ok := policyRaw["restartable_stages"].([]interface{}); ok {
			restartableStages := make([]string, len(stagesRaw))
			for i, stage := range stagesRaw {
				if stageID, ok := stage.(string); ok {
					restartableStages[i] = stageID
				}
			}
			restartPolicy.RestartableStages = restartableStages
		}

		// Extract cooldown_seconds (optional)
		if cooldown, ok := policyRaw["cooldown_seconds"]; ok {
			switch v := cooldown.(type) {
			case int:
				restartPolicy.CooldownSeconds = types.SafeInt32(v)
			case int32:
				restartPolicy.CooldownSeconds = v
			case int64:
				restartPolicy.CooldownSeconds = types.SafeInt32FromInt64(v)
			}
		}

		// Extract reset_shared_memory (optional)
		if resetMem, ok := policyRaw["reset_shared_memory"].(bool); ok {
			restartPolicy.ResetSharedMemory = resetMem
		}

		// Extract preserve_outputs (optional, default: true)
		if preserveOut, ok := policyRaw["preserve_outputs"].(bool); ok {
			restartPolicy.PreserveOutputs = preserveOut
		} else {
			restartPolicy.PreserveOutputs = true // default
		}

		// Extract max_validation_retries (optional, default: 2)
		if maxRetries, ok := policyRaw["max_validation_retries"].(int); ok {
			restartPolicy.MaxValidationRetries = types.SafeInt32(maxRetries)
		}
	}

	// Extract restart_triggers (optional)
	var restartTriggers []string
	if triggersRaw, ok := spec["restart_triggers"].([]interface{}); ok {
		restartTriggers = make([]string, len(triggersRaw))
		for i, trigger := range triggersRaw {
			if triggerID, ok := trigger.(string); ok {
				restartTriggers[i] = triggerID
			}
		}
	}

	// Extract restart_topic (optional, default: "workflow.restart")
	restartTopic := "workflow.restart"
	if topic, ok := spec["restart_topic"].(string); ok {
		restartTopic = topic
	}

	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Iterative{
			Iterative: &loomv1.IterativeWorkflowPattern{
				Pipeline:        pipelinePattern.GetPipeline(),
				MaxIterations:   maxIterations,
				RestartPolicy:   restartPolicy,
				RestartTriggers: restartTriggers,
				RestartTopic:    restartTopic,
			},
		},
	}, nil
}

// convertSwarmPattern converts spec to SwarmPattern proto
func convertSwarmPattern(spec map[string]interface{}, logger *zap.Logger) (*loomv1.WorkflowPattern, error) {
	// Extract question
	question, ok := spec["question"].(string)
	if !ok {
		return nil, fmt.Errorf("%w: swarm pattern requires 'question' field", ErrInvalidWorkflow)
	}

	// Extract agent_ids
	agentIDsRaw, ok := spec["agent_ids"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: swarm pattern requires 'agent_ids' field", ErrInvalidWorkflow)
	}
	agentIDs := make([]string, len(agentIDsRaw))
	for i, id := range agentIDsRaw {
		agentIDs[i], ok = id.(string)
		if !ok {
			return nil, fmt.Errorf("%w: agent_ids must be strings", ErrInvalidWorkflow)
		}
	}

	// Extract strategy (default to MAJORITY)
	strategy := loomv1.VotingStrategy_MAJORITY
	if strategyRaw, ok := spec["strategy"].(string); ok {
		strategy = parseVotingStrategy(strategyRaw)
	}

	// Extract confidence_threshold (default: 0.5)
	confidenceThreshold := float32(0.5)
	if thresholdRaw, ok := spec["confidence_threshold"]; ok {
		switch v := thresholdRaw.(type) {
		case float64:
			confidenceThreshold = float32(v)
		case float32:
			confidenceThreshold = v
		case int:
			confidenceThreshold = float32(v)
		}
	}

	// Extract share_votes (default: false)
	shareVotes := false
	if shareVotesRaw, ok := spec["share_votes"].(bool); ok {
		shareVotes = shareVotesRaw
	}

	// Extract optional judge_agent_id
	judgeAgentID, _ := spec["judge_agent_id"].(string)

	// Extract optional retry_policy
	retryPolicy, err := parseOutputRetryPolicy(spec, "spec", logger)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidWorkflow, err.Error())
	}

	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Swarm{
			Swarm: &loomv1.SwarmPattern{
				Question:            question,
				AgentIds:            agentIDs,
				Strategy:            strategy,
				ConfidenceThreshold: confidenceThreshold,
				ShareVotes:          shareVotes,
				JudgeAgentId:        judgeAgentID,
				RetryPolicy:         retryPolicy,
			},
		},
	}, nil
}

// parseVotingStrategy converts string to VotingStrategy enum
func parseVotingStrategy(strategy string) loomv1.VotingStrategy {
	switch strategy {
	case "majority":
		return loomv1.VotingStrategy_MAJORITY
	case "supermajority":
		return loomv1.VotingStrategy_SUPERMAJORITY
	case "unanimous":
		return loomv1.VotingStrategy_UNANIMOUS
	case "weighted":
		return loomv1.VotingStrategy_WEIGHTED
	case "ranked_choice":
		return loomv1.VotingStrategy_RANKED_CHOICE
	default:
		return loomv1.VotingStrategy_VOTING_STRATEGY_UNSPECIFIED
	}
}

// retryPolicyRetryOnlyKeys are the retry_policy fields that only mean something
// once at least one retry is allowed. They are named in the warning logged when
// they appear alongside max_retries <= 0, because silently dropping them (which
// is what returning nil does) is how a typo becomes a mystery.
var retryPolicyRetryOnlyKeys = []string{"session_mode", "feedback_template", "cooldown_ms"}

// parseOutputRetryPolicy parses an optional retry_policy from YAML config.
// path is the YAML location of the enclosing block, used only for errors.
//
// Returns nil if no retry_policy is present or if max_retries is 0/negative —
// both mean "no retry policy".
//
// # Three tolerated shapes, each warned about
//
// This block predates the strict scalar helpers, so three shapes that loaded
// before them keep loading, with a warning naming the value and what it resolved
// to instead of a load error. Rejecting them would break configs that already
// run, which is a worse outcome than a warning:
//
//   - max_retries: 2.5 — truncated toward zero, as the pre-helper decode did.
//   - max_retries: "3" — ignored, i.e. treated as absent, as before. A string is
//     not silently parsed: that would turn a no-retry config into a retrying one.
//   - a retry-only key (session_mode, feedback_template, cooldown_ms) without a
//     positive max_retries — the whole retry_policy is dropped, as before.
//
// The tolerance is local to this function and reads the raw map itself, so the
// shared yamlInt32Field/yamlStringField helpers stay strict for every other key,
// on this block and on the leveling blocks that have no legacy configs to keep
// loading. Every other malformation here — max_retries: true, a fractional
// cooldown_ms, an unknown session_mode — still fails the load.
//
// YAML shape:
//
//	retry_policy:
//	  max_retries: 2                      # required for the policy to exist
//	  include_valid_values: true          # optional; absent = true
//	  session_mode: fresh                 # optional; fresh | continue | escalate
//	  feedback_template: "..."            # optional; {{error}}, {{previous_output}},
//	  cooldown_ms: 250                    #   {{attempt}}, {{max_retries}}
func parseOutputRetryPolicy(raw map[string]interface{}, path string, logger *zap.Logger) (*loomv1.OutputRetryPolicy, error) {
	retryRaw, ok := raw["retry_policy"].(map[string]interface{})
	if !ok {
		return nil, nil
	}
	path = path + ".retry_policy"

	policy := &loomv1.OutputRetryPolicy{}

	maxRetries, err := retryPolicyMaxRetries(retryRaw, path, logger)
	if err != nil {
		return nil, err
	}
	policy.MaxRetries = maxRetries

	// Return nil if max_retries is 0 or negative — same as "no retry policy"
	if policy.MaxRetries <= 0 {
		if dropped := presentRetryOnlyKeys(retryRaw); len(dropped) > 0 {
			logger.Warn("workflow retry_policy dropped: its retry-only keys need max_retries >= 1",
				zap.String("field", path),
				zap.Int32("max_retries", policy.MaxRetries),
				zap.Strings("ignored_keys", dropped),
				zap.String("resolved_to", "no retry policy"))
		}
		return nil, nil
	}

	// Default to true — proto3 bool defaults to false, but including valid values
	// in retry prompts is the safe default (helps the LLM produce correct output).
	// Only set to false if explicitly configured.
	policy.IncludeValidValues = true
	includeValues, present, err := yamlBoolField(retryRaw, path, "include_valid_values")
	if err != nil {
		return nil, err
	}
	if present {
		policy.IncludeValidValues = includeValues
	}

	sessionMode, present, err := yamlStringField(retryRaw, path, "session_mode")
	if err != nil {
		return nil, err
	}
	if present {
		mode, ok := parseRetrySessionMode(sessionMode)
		if !ok {
			return nil, fmt.Errorf("%s.session_mode %q is not a known retry session mode (valid: %s; the full enum name such as RETRY_SESSION_MODE_FRESH is also accepted)",
				path, sessionMode, strings.Join(retrySessionModeShortNames(), ", "))
		}
		policy.SessionMode = mode
	}

	if policy.FeedbackTemplate, _, err = yamlStringField(retryRaw, path, "feedback_template"); err != nil {
		return nil, err
	}

	cooldownMs, _, err := yamlInt32Field(retryRaw, path, "cooldown_ms")
	if err != nil {
		return nil, err
	}
	if cooldownMs < 0 {
		return nil, fmt.Errorf("%s.cooldown_ms must be >= 0, got %d", path, cooldownMs)
	}
	policy.CooldownMs = cooldownMs

	return policy, nil
}

// retryPolicyMaxRetries reads retry_policy.max_retries with the two value-level
// tolerances parseOutputRetryPolicy documents, and delegates everything else to
// the shared strict helper so this key keeps yamlInt32Field's accepted types and
// its error wording.
//
// A fractional value truncates toward zero (int64 conversion, matching
// math.Trunc) and a string is ignored, both with a warning. The second return of
// yamlInt32Field is dropped: an ignored string and an absent key are the same
// answer here, which is what makes the string tolerance byte-identical to the
// behavior it restores.
func retryPolicyMaxRetries(retryRaw map[string]interface{}, path string, logger *zap.Logger) (int32, error) {
	switch value := retryRaw["max_retries"].(type) {
	case float64:
		if value != math.Trunc(value) {
			truncated := types.SafeInt32FromInt64(int64(value))
			logger.Warn("workflow retry_policy.max_retries is not a whole number: truncating toward zero",
				zap.String("field", path+".max_retries"),
				zap.Float64("yaml_value", value),
				zap.Int32("resolved_to", truncated))
			return truncated, nil
		}
	case string:
		logger.Warn("workflow retry_policy.max_retries is a string: ignoring it as if the key were absent",
			zap.String("field", path+".max_retries"),
			zap.String("yaml_value", value),
			zap.String("resolved_to", "no retry policy"))
		return 0, nil
	}

	value, _, err := yamlInt32Field(retryRaw, path, "max_retries")
	return value, err
}

// presentRetryOnlyKeys lists the retry-only keys the block actually carries, in
// retryPolicyRetryOnlyKeys order so the warning reads the same for a given
// config every time.
func presentRetryOnlyKeys(retryRaw map[string]interface{}) []string {
	present := make([]string, 0, len(retryPolicyRetryOnlyKeys))
	for _, key := range retryPolicyRetryOnlyKeys {
		if _, ok := retryRaw[key]; ok {
			present = append(present, key)
		}
	}
	return present
}

// parseOutputPolicy parses an optional unified output_policy block from a
// pipeline stage or parallel task.
//
// Returns nil when the block is absent. Parsing it does not make it enforced:
// the only executors that read OutputPolicy are the capability-leveling paths,
// so a stage or task carrying output_policy without an enabled leveling policy
// behaves exactly as it did before — see the leveling doc's OutputPolicy safety
// constraint.
//
// YAML shape:
//
//	output_policy:
//	  output_schema: '{"type":"object", ...}'   # optional
//	  acceptance_criteria: "..."                # optional (no executor consumes it yet)
//	  validator_agent_id: reviewer              # optional (ditto)
//	  judge_config_id: strict-judge             # optional (ditto)
//	  retry_policy: { ... }                     # optional; see parseOutputRetryPolicy
func parseOutputPolicy(enclosing map[string]interface{}, path string, logger *zap.Logger) (*loomv1.OutputPolicy, error) {
	raw, ok := enclosing["output_policy"]
	if !ok || raw == nil {
		return nil, nil
	}
	block, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%s.output_policy must be an object, got %T", path, raw)
	}
	path = path + ".output_policy"

	policy := &loomv1.OutputPolicy{}
	var err error
	if policy.OutputSchema, _, err = yamlStringField(block, path, "output_schema"); err != nil {
		return nil, err
	}
	if policy.AcceptanceCriteria, _, err = yamlStringField(block, path, "acceptance_criteria"); err != nil {
		return nil, err
	}
	if policy.ValidatorAgentId, _, err = yamlStringField(block, path, "validator_agent_id"); err != nil {
		return nil, err
	}
	if policy.JudgeConfigId, _, err = yamlStringField(block, path, "judge_config_id"); err != nil {
		return nil, err
	}
	if policy.RetryPolicy, err = parseOutputRetryPolicy(block, path, logger); err != nil {
		return nil, err
	}

	return policy, nil
}

// parseHITLGate parses an optional hitl_gate block from a pipeline stage.
// Returns nil when the stage has no gate.
//
// YAML shape:
//
//	hitl_gate:
//	  prompt_template: "Review this DDL:\n{{output}}"   # optional
//	  request_type: approval                             # optional
//	  timeout_seconds: 1800                              # optional
//	  revise_target_stage_id: ddl-designer               # optional
//	  max_revisions: 3                                   # optional
//	  on_timeout: fail | reject | approve                # optional
func parseHITLGate(stageMap map[string]interface{}) (*loomv1.HITLGate, error) {
	gateRaw, ok := stageMap["hitl_gate"]
	if !ok || gateRaw == nil {
		return nil, nil
	}
	gateMap, ok := gateRaw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("hitl_gate must be an object")
	}

	gate := &loomv1.HITLGate{}
	gate.PromptTemplate, _ = gateMap["prompt_template"].(string)
	gate.RequestType, _ = gateMap["request_type"].(string)
	gate.ReviseTargetStageId, _ = gateMap["revise_target_stage_id"].(string)

	if raw, ok := gateMap["timeout_seconds"]; ok {
		switch v := raw.(type) {
		case int:
			gate.TimeoutSeconds = types.SafeInt32(v)
		case int32:
			gate.TimeoutSeconds = v
		case int64:
			gate.TimeoutSeconds = types.SafeInt32FromInt64(v)
		default:
			return nil, fmt.Errorf("hitl_gate.timeout_seconds must be an integer, got %T", v)
		}
	}
	if raw, ok := gateMap["max_revisions"]; ok {
		switch v := raw.(type) {
		case int:
			gate.MaxRevisions = types.SafeInt32(v)
		case int32:
			gate.MaxRevisions = v
		case int64:
			gate.MaxRevisions = types.SafeInt32FromInt64(v)
		default:
			return nil, fmt.Errorf("hitl_gate.max_revisions must be an integer, got %T", v)
		}
	}
	if raw, ok := gateMap["on_timeout"]; ok {
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("hitl_gate.on_timeout must be a string")
		}
		switch s {
		case "", "fail":
			gate.OnTimeout = loomv1.GateTimeoutAction_GATE_TIMEOUT_ACTION_FAIL
		case "reject":
			gate.OnTimeout = loomv1.GateTimeoutAction_GATE_TIMEOUT_ACTION_REJECT
		case "approve":
			gate.OnTimeout = loomv1.GateTimeoutAction_GATE_TIMEOUT_ACTION_APPROVE
		default:
			return nil, fmt.Errorf("hitl_gate.on_timeout must be one of fail, reject, approve (got %q)", s)
		}
	}

	return gate, nil
}

// parseMergeStrategy converts string to MergeStrategy enum
func parseMergeStrategy(strategy string) loomv1.MergeStrategy {
	switch strategy {
	case "consensus":
		return loomv1.MergeStrategy_CONSENSUS
	case "voting":
		return loomv1.MergeStrategy_VOTING
	case "concatenate":
		return loomv1.MergeStrategy_CONCATENATE
	case "first":
		return loomv1.MergeStrategy_FIRST
	case "best":
		return loomv1.MergeStrategy_BEST
	case "summary":
		return loomv1.MergeStrategy_SUMMARY
	default:
		return loomv1.MergeStrategy_MERGE_STRATEGY_UNSPECIFIED
	}
}

// LoadWorkflowConfigFromYAML loads a workflow YAML file and returns the parsed config.
// This is used by the scheduler to access the schedule section.
func LoadWorkflowConfigFromYAML(path string) (*WorkflowConfig, error) {
	// Read file
	data, err := readWorkflowFile(path)
	if err != nil {
		return nil, err
	}

	// Parse YAML
	config, err := parseWorkflowYAML(data)
	if err != nil {
		return nil, err
	}

	return config, nil
}

// ConvertConfigToProto converts a WorkflowConfig to a WorkflowPattern proto.
// This is used by the scheduler after loading a workflow YAML file.
func ConvertConfigToProto(config *WorkflowConfig) (*loomv1.WorkflowPattern, error) {
	return ConvertConfigToProtoWithLogger(config, nil)
}

// ConvertConfigToProtoWithLogger is ConvertConfigToProto with a logger for the
// loader's tolerance warnings. A nil logger discards them.
func ConvertConfigToProtoWithLogger(config *WorkflowConfig, logger *zap.Logger) (*loomv1.WorkflowPattern, error) {
	// Validate structure
	if err := validateWorkflowStructure(config); err != nil {
		return nil, err
	}

	// Convert to proto
	pattern, err := convertToProto(config, loaderLogger(logger))
	if err != nil {
		return nil, err
	}

	return pattern, nil
}

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"fmt"
	"os"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
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
	// 1. Read file
	data, err := readWorkflowFile(path)
	if err != nil {
		return nil, err
	}

	// 2. Parse YAML
	config, err := parseWorkflowYAML(data)
	if err != nil {
		return nil, err
	}

	// 3. Validate structure
	if err := validateWorkflowStructure(config); err != nil {
		return nil, err
	}

	// 4. Convert to proto
	pattern, err := convertToProto(config)
	if err != nil {
		return nil, err
	}

	return pattern, nil
}

// readWorkflowFile reads the workflow file from disk
func readWorkflowFile(path string) ([]byte, error) {
	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: %s", ErrFileNotFound, path)
	}

	// Read file content
	data, err := os.ReadFile(path)
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
		return fmt.Errorf("%w: missing apiVersion", ErrInvalidWorkflow)
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
	validTypes := []string{"debate", "fork-join", "pipeline", "parallel", "conditional", "iterative", "swarm"}
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

// convertToProto converts WorkflowConfig to loomv1.WorkflowPattern
func convertToProto(config *WorkflowConfig) (*loomv1.WorkflowPattern, error) {
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
		return convertPipelinePattern(config.Spec)
	case "parallel":
		return convertParallelPattern(config.Spec)
	case "conditional":
		return convertConditionalPattern(config.Spec)
	case "iterative":
		return convertIterativePattern(config.Spec)
	case "swarm":
		return convertSwarmPattern(config.Spec)
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
			rounds = int32(v)
		case int32:
			rounds = v
		case int64:
			rounds = int32(v)
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
			timeoutSeconds = int32(v)
		case int32:
			timeoutSeconds = v
		case int64:
			timeoutSeconds = int32(v)
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
func convertPipelinePattern(spec map[string]interface{}) (*loomv1.WorkflowPattern, error) {
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

		validationPrompt, _ := stageMap["validation_prompt"].(string)

		stages[i] = &loomv1.PipelineStage{
			AgentId:          agentID,
			PromptTemplate:   promptTemplate,
			ValidationPrompt: validationPrompt,
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
func convertParallelPattern(spec map[string]interface{}) (*loomv1.WorkflowPattern, error) {
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

		tasks[i] = &loomv1.AgentTask{
			AgentId:  agentID,
			Prompt:   prompt,
			Metadata: metadata,
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
			timeoutSeconds = int32(v)
		case int32:
			timeoutSeconds = v
		case int64:
			timeoutSeconds = int32(v)
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
func convertConditionalPattern(spec map[string]interface{}) (*loomv1.WorkflowPattern, error) {
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
		})
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
		})
		if err != nil {
			return nil, fmt.Errorf("failed to parse default_branch: %w", err)
		}
	}

	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Conditional{
			Conditional: &loomv1.ConditionalPattern{
				ConditionAgentId: conditionAgentID,
				ConditionPrompt:  conditionPrompt,
				Branches:         branches,
				DefaultBranch:    defaultBranch,
			},
		},
	}, nil
}

// convertIterativePattern converts spec to IterativeWorkflowPattern proto
func convertIterativePattern(spec map[string]interface{}) (*loomv1.WorkflowPattern, error) {
	// Extract pipeline configuration (nested under "pipeline" key)
	pipelineSpec, ok := spec["pipeline"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: iterative pattern requires 'pipeline' field", ErrInvalidWorkflow)
	}

	// Parse the base pipeline using existing converter
	// We need to add the "type" field to make it compatible with convertPipelinePattern
	pipelineSpec["type"] = "pipeline"
	pipelinePattern, err := convertPipelinePattern(pipelineSpec)
	if err != nil {
		return nil, fmt.Errorf("failed to parse base pipeline: %w", err)
	}

	// Extract max_iterations (default: 3)
	maxIterations := int32(3)
	if maxIter, ok := spec["max_iterations"]; ok {
		switch v := maxIter.(type) {
		case int:
			maxIterations = int32(v)
		case int32:
			maxIterations = v
		case int64:
			maxIterations = int32(v)
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
				restartPolicy.CooldownSeconds = int32(v)
			case int32:
				restartPolicy.CooldownSeconds = v
			case int64:
				restartPolicy.CooldownSeconds = int32(v)
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
			restartPolicy.MaxValidationRetries = int32(maxRetries)
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
func convertSwarmPattern(spec map[string]interface{}) (*loomv1.WorkflowPattern, error) {
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

	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Swarm{
			Swarm: &loomv1.SwarmPattern{
				Question:            question,
				AgentIds:            agentIDs,
				Strategy:            strategy,
				ConfidenceThreshold: confidenceThreshold,
				ShareVotes:          shareVotes,
				JudgeAgentId:        judgeAgentID,
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
	// Validate structure
	if err := validateWorkflowStructure(config); err != nil {
		return nil, err
	}

	// Convert to proto
	pattern, err := convertToProto(config)
	if err != nil {
		return nil, err
	}

	return pattern, nil
}

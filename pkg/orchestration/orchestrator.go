// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"
	"sync"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/collaboration"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

// WorkflowProgressEvent represents a progress update during workflow execution.
type WorkflowProgressEvent struct {
	// Pattern type being executed
	PatternType string

	// Current stage/step description
	Message string

	// Progress percentage (0-100)
	Progress int32

	// Current agent executing (if applicable)
	CurrentAgentID string

	// Partial results available so far
	PartialResults []*loomv1.AgentResult
}

// WorkflowProgressCallback is called during workflow execution to report progress.
type WorkflowProgressCallback func(event WorkflowProgressEvent)

// Orchestrator coordinates multiple agents using workflow patterns.
// It provides a fluent API for building and executing multi-agent workflows
// like debates, pipelines, and parallel execution.
type Orchestrator struct {
	mu sync.RWMutex

	// Agents registered with the orchestrator
	agents map[string]*agent.Agent

	// Agent registry for looking up agents by name
	registry *agent.Registry

	// LLM provider for merge operations
	llmProvider agent.LLMProvider

	// Tracer for observability
	tracer observability.Tracer

	// Logger
	logger *zap.Logger

	// Collaboration engine for multi-agent patterns
	collaborationEngine *collaboration.Engine

	// MessageBus for agent-to-agent communication (optional)
	messageBus *communication.MessageBus

	// SharedMemory for inter-stage data sharing (optional)
	sharedMemory *communication.SharedMemoryStore

	// Progress callback for workflow execution updates (optional)
	progressCallback WorkflowProgressCallback

	// LLM concurrency semaphore to limit parallel LLM calls (optional)
	// If nil, no concurrency control is applied
	llmSemaphore chan struct{}
}

// Config configures the orchestrator.
type Config struct {
	// Agent registry for looking up agents
	Registry *agent.Registry

	// LLM provider for merge operations
	LLMProvider agent.LLMProvider

	// Tracer for observability
	Tracer observability.Tracer

	// Logger
	Logger *zap.Logger

	// MessageBus for agent-to-agent communication (optional, for iterative workflows)
	MessageBus *communication.MessageBus

	// SharedMemory for inter-stage data sharing (optional, for iterative workflows)
	SharedMemory *communication.SharedMemoryStore

	// ProgressCallback for reporting workflow execution progress (optional)
	ProgressCallback WorkflowProgressCallback

	// LLMSemaphore for limiting concurrent LLM calls (optional)
	// If nil, no concurrency control is applied
	// Use make(chan struct{}, N) where N is the max concurrent LLM calls
	LLMSemaphore chan struct{}
}

// NewOrchestrator creates a new orchestrator instance.
func NewOrchestrator(config Config) *Orchestrator {
	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}
	if config.Tracer == nil {
		config.Tracer = observability.NewNoOpTracer()
	}

	o := &Orchestrator{
		agents:           make(map[string]*agent.Agent),
		registry:         config.Registry,
		llmProvider:      config.LLMProvider,
		tracer:           config.Tracer,
		logger:           config.Logger,
		messageBus:       config.MessageBus,
		sharedMemory:     config.SharedMemory,
		progressCallback: config.ProgressCallback,
		llmSemaphore:     config.LLMSemaphore,
	}

	// Initialize collaboration engine with orchestrator as provider
	o.collaborationEngine = collaboration.NewEngineWithObservability(o, config.Tracer, config.Logger)

	return o
}

// SetProgressCallback sets or updates the progress callback.
// This allows updating the callback after orchestrator creation.
func (o *Orchestrator) SetProgressCallback(callback WorkflowProgressCallback) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.progressCallback = callback
}

// emitProgress sends a progress event if a callback is configured.
func (o *Orchestrator) emitProgress(event WorkflowProgressEvent) {
	o.mu.RLock()
	callback := o.progressCallback
	o.mu.RUnlock()

	if callback != nil {
		callback(event)
	}
}

// RegisterAgent registers an agent with a specific ID for this orchestration.
// This allows the orchestrator to reference agents in workflow patterns.
func (o *Orchestrator) RegisterAgent(id string, ag *agent.Agent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.agents[id] = ag
	o.logger.Debug("Registered agent with orchestrator",
		zap.String("id", id),
		zap.String("name", ag.GetName()))
}

// GetAgent retrieves a registered agent by ID.
// If the agent is not in the local registry, it attempts to load it from the agent registry.
func (o *Orchestrator) GetAgent(ctx context.Context, id string) (*agent.Agent, error) {
	o.mu.RLock()
	ag, exists := o.agents[id]
	o.mu.RUnlock()

	if exists {
		return ag, nil
	}

	// Try to load from agent registry if available
	if o.registry != nil {
		ag, err := o.registry.GetAgent(ctx, id)
		if err == nil {
			// Cache it locally
			o.RegisterAgent(id, ag)
			return ag, nil
		}
	}

	return nil, fmt.Errorf("agent not found: %s", id)
}

// Debate creates a debate pattern builder for multi-agent debates.
//
// Example:
//
//	result := orchestrator.
//	    Debate("Should we use SQLite or Postgres?").
//	    WithAgents(agent1, agent2).
//	    WithRounds(3).
//	    Execute(ctx)
func (o *Orchestrator) Debate(topic string) *DebateBuilder {
	return &DebateBuilder{
		orchestrator:  o,
		topic:         topic,
		agentIDs:      make([]string, 0),
		rounds:        1,
		mergeStrategy: loomv1.MergeStrategy_CONSENSUS,
	}
}

// Fork creates a fork-join pattern builder for parallel execution.
//
// Example:
//
//	result := orchestrator.
//	    Fork("Analyze this codebase").
//	    WithAgents(securityExpert, performanceExpert).
//	    Join(loomv1.MergeStrategy_SUMMARY).
//	    Execute(ctx)
func (o *Orchestrator) Fork(prompt string) *ForkJoinBuilder {
	return &ForkJoinBuilder{
		orchestrator:  o,
		prompt:        prompt,
		agentIDs:      make([]string, 0),
		mergeStrategy: loomv1.MergeStrategy_CONSENSUS,
	}
}

// Pipeline creates a pipeline pattern builder for sequential execution.
//
// Example:
//
//	result := orchestrator.
//	    Pipeline("Design a new feature").
//	    WithStage(architect, "Create architecture").
//	    WithStage(implementer, "Implement: {{previous}}").
//	    Execute(ctx)
func (o *Orchestrator) Pipeline(initialPrompt string) *PipelineBuilder {
	return &PipelineBuilder{
		orchestrator:  o,
		initialPrompt: initialPrompt,
		stages:        make([]*loomv1.PipelineStage, 0),
	}
}

// Parallel creates a parallel pattern builder for independent tasks.
//
// Example:
//
//	result := orchestrator.
//	    Parallel().
//	    WithTask(analyzer, "Analyze code quality").
//	    WithTask(scanner, "Check for vulnerabilities").
//	    Execute(ctx)
func (o *Orchestrator) Parallel() *ParallelBuilder {
	return &ParallelBuilder{
		orchestrator:  o,
		tasks:         make([]*loomv1.AgentTask, 0),
		mergeStrategy: loomv1.MergeStrategy_CONCATENATE,
	}
}

// Conditional creates a conditional pattern builder for routing logic.
//
// Example:
//
//	result := orchestrator.
//	    Conditional(classifier, "Is this a bug or feature?").
//	    When("bug", debugWorkflow).
//	    When("feature", designWorkflow).
//	    Execute(ctx)
func (o *Orchestrator) Conditional(classifier *agent.Agent, conditionPrompt string) *ConditionalBuilder {
	// Register classifier agent
	classifierID := "classifier_" + fmt.Sprintf("%p", classifier)
	o.RegisterAgent(classifierID, classifier)

	return &ConditionalBuilder{
		orchestrator:    o,
		classifierID:    classifierID,
		conditionPrompt: conditionPrompt,
		branches:        make(map[string]*loomv1.WorkflowPattern),
		defaultBranch:   nil,
	}
}

// ExecutePattern executes a workflow pattern and returns the result.
// This is the low-level execution method used by pattern builders.
func (o *Orchestrator) ExecutePattern(ctx context.Context, pattern *loomv1.WorkflowPattern) (*loomv1.WorkflowResult, error) {
	patternType := GetPatternType(pattern)

	// Emit initial progress
	o.emitProgress(WorkflowProgressEvent{
		PatternType: patternType,
		Message:     fmt.Sprintf("Starting %s workflow execution", patternType),
		Progress:    0,
	})

	// Check if this is a collaboration pattern first
	switch pattern.Pattern.(type) {
	case *loomv1.WorkflowPattern_Debate,
		*loomv1.WorkflowPattern_PairProgramming,
		*loomv1.WorkflowPattern_TeacherStudent:
		// Route to collaboration engine (with observability)
		return o.collaborationEngine.Execute(ctx, pattern)
	}

	// Orchestration patterns
	// Start tracing
	ctx, span := o.tracer.StartSpan(ctx, observability.SpanWorkflowExecution)
	defer o.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("pattern_type", patternType)
	}

	// Emit progress after initialization
	o.emitProgress(WorkflowProgressEvent{
		PatternType: patternType,
		Message:     "Executing workflow pattern",
		Progress:    20,
	})

	// Route to appropriate executor based on pattern type
	var result *loomv1.WorkflowResult
	var err error

	switch p := pattern.Pattern.(type) {
	case *loomv1.WorkflowPattern_ForkJoin:
		executor := NewForkJoinExecutor(o, p.ForkJoin)
		result, err = executor.Execute(ctx)

	case *loomv1.WorkflowPattern_Pipeline:
		executor := NewPipelineExecutor(o, p.Pipeline)
		result, err = executor.Execute(ctx)

	case *loomv1.WorkflowPattern_Parallel:
		executor := NewParallelExecutor(o, p.Parallel)
		result, err = executor.Execute(ctx)

	case *loomv1.WorkflowPattern_Conditional:
		executor := NewConditionalExecutor(o, p.Conditional)
		result, err = executor.Execute(ctx)

	case *loomv1.WorkflowPattern_Iterative:
		executor := NewIterativePipelineExecutor(o, p.Iterative, o.messageBus)
		result, err = executor.Execute(ctx)

	case *loomv1.WorkflowPattern_Swarm:
		executor := NewSwarmExecutor(o, p.Swarm)
		result, err = executor.Execute(ctx)

	default:
		return nil, fmt.Errorf("unknown workflow pattern type")
	}

	if err != nil {
		// Emit error progress
		o.emitProgress(WorkflowProgressEvent{
			PatternType: patternType,
			Message:     fmt.Sprintf("Workflow failed: %v", err),
			Progress:    0,
		})
		return nil, err
	}

	// Emit completion progress
	o.emitProgress(WorkflowProgressEvent{
		PatternType:    patternType,
		Message:        "Workflow completed successfully",
		Progress:       100,
		PartialResults: result.AgentResults,
	})

	return result, nil
}

// GetMergeLLM returns the LLM provider to use for merge/synthesis operations.
// Resolution order:
//  1. The orchestrator's explicitly configured llmProvider (Config.LLMProvider)
//  2. The orchestrator role LLM from the first registered agent that has one
//     (agent.GetLLMForRole(LLM_ROLE_ORCHESTRATOR) which falls back to the agent's main LLM)
//  3. nil (caller must handle this, typically by returning an error)
//
// Note: When falling back to agent LLMs (step 2), the selection is non-deterministic
// if multiple agents have orchestrator LLMs, because agents are stored in a map.
// A warning is logged in this case. Set Config.LLMProvider for deterministic behavior.
func (o *Orchestrator) GetMergeLLM() agent.LLMProvider {
	// 1. Use explicitly configured LLM provider
	if o.llmProvider != nil {
		return o.llmProvider
	}

	// 2. Try to get an orchestrator role LLM from a registered agent
	o.mu.RLock()
	defer o.mu.RUnlock()

	// Collect all agents that have a usable orchestrator LLM so we can warn
	// when the fallback selection is non-deterministic (map iteration order).
	var candidates []string
	var firstLLM agent.LLMProvider
	var firstName string
	for name, ag := range o.agents {
		llm := ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR)
		if llm != nil {
			candidates = append(candidates, name)
			if firstLLM == nil {
				firstLLM = llm
				firstName = name
			}
		}
	}

	if firstLLM == nil {
		return nil
	}

	if len(candidates) > 1 {
		o.logger.Warn("Multiple agents have orchestrator LLMs; selection is non-deterministic. "+
			"Set orchestrator-level LLMProvider in Config for deterministic behavior",
			zap.String("selected_agent", firstName),
			zap.Strings("candidate_agents", candidates),
		)
	} else {
		o.logger.Debug("Using orchestrator role LLM from registered agent",
			zap.String("agent", firstName))
	}

	return firstLLM
}

// GetPatternType returns a string representation of the pattern type.
// This is used for logging, progress messages, and workflow execution tracking.
func GetPatternType(pattern *loomv1.WorkflowPattern) string {
	switch pattern.Pattern.(type) {
	case *loomv1.WorkflowPattern_Debate:
		return "debate"
	case *loomv1.WorkflowPattern_ForkJoin:
		return "fork_join"
	case *loomv1.WorkflowPattern_Pipeline:
		return "pipeline"
	case *loomv1.WorkflowPattern_Parallel:
		return "parallel"
	case *loomv1.WorkflowPattern_Conditional:
		return "conditional"
	case *loomv1.WorkflowPattern_Iterative:
		return "iterative_pipeline"
	case *loomv1.WorkflowPattern_Swarm:
		return "swarm"
	default:
		return "unknown"
	}
}

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/collaboration"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/task"
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

	// HITL gate awaiting a human decision (set when a gate fires, right
	// before the workflow suspends or an inline handler is consulted)
	HITLRequest *loomv1.HITLGateRequest

	// Where a state machine is, outermost state first. An interface needs the
	// position itself, not a sentence describing it.
	StatePath []string

	// Which run this is about. The callback is set on the orchestrator, and a
	// run that delegates has children running through the same orchestrator —
	// so without saying who is speaking, a parent's walk fills up with its
	// children's states.
	WorkflowID string
}

// WorkflowProgressCallback is called during workflow execution to report progress.
type WorkflowProgressCallback func(event WorkflowProgressEvent)

// RunObserver is told what a state machine is doing as it does it.
//
// A delegated child is a state machine of its own — its own id, directory,
// budget and walk — nested inside a parent's state. Both are reported the same
// way, so the owner of the runs sees the whole tree and not only its root.
type RunObserver interface {
	// ChildStarted is called when a state delegates to a child, before the
	// child's first state runs. parentID and parentState say who delegated;
	// the child's own id, spec and directory are on the pattern.
	ChildStarted(ctx context.Context, parentID, parentState string, child *loomv1.WorkflowPattern)
	// Transitioned is called after a state completes and the next state is
	// decided, for the top machine and every child. vars is what the run has
	// worked out so far; cost is what the completed state spent.
	Transitioned(ctx context.Context, workflowID, from, signal, entering string, vars map[string]string, cost float64)
	// ChildEnded is called when a delegated child reaches a terminal, with the
	// terminal it reached and the parent's vars as they now stand — which is
	// where the parent keeps which of its children have finished.
	ChildEnded(ctx context.Context, parentID, parentState, childID, terminal string, parentVars map[string]string)
}

// StopObserver hears each visit that ended without a transition, for the top
// machine and every child: the state finished and the walk stopped there — at
// a gate, an escalation, a ceiling, a signal it could not route — or a person
// sent it back. With Transitioned it gives every visit exactly one record,
// carrying all the visit spent. A state stopped part-way — its model could not
// be reached, a child it handed work to is waiting — has not ended its visit
// and is not reported; the visit is recorded, with what it spent before the
// stop, when it ends. An observer that does not implement it is not told.
type StopObserver interface {
	Stopped(ctx context.Context, workflowID, state, signal string, cost float64)
}

// Orchestrator coordinates multiple agents using workflow patterns.
// It provides a fluent API for building and executing multi-agent workflows
// like debates, pipelines, and parallel execution.
type Orchestrator struct {
	mu sync.RWMutex

	// observer, when set, is told every child started and every transition made.
	observer RunObserver
	// places keeps runs' records when the observer keeps none.
	places *memRecords

	// resolver finds a workflow a state delegates to by name.
	resolver WorkflowResolver

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

	// TaskManager for persistent workflow tracking (optional).
	taskManager *task.Manager

	// HITLHandler for inline gate decisions (optional). When nil, HITL gates
	// suspend the workflow with a checkpoint instead (the durable default).
	hitlHandler HITLHandler

	// Tool executor for state-machine tool states and edge hooks (optional).
	// The same executor an agent's tool calls go through, so a walker's tool
	// call is admitted, sandboxed and audited exactly as a model's would be.
	toolExecutor ToolExecutor
}

// taskTrackingKey is a context key to prevent recursive task tracking.
type taskTrackingKey struct{}

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

	// TaskManager for persistent workflow tracking (optional).
	// When set, ExecutePattern wraps execution with task-based state tracking,
	// providing persistence, resume capability, and progress visibility.
	TaskManager *task.Manager

	// RunObserver, when set, is told what every state machine the orchestrator
	// walks is doing — the top one and every child a state delegates to — as
	// it happens. Whoever owns the runs keeps the record; this is how a walk
	// that dies is picked up from where it was, at every level.
	RunObserver RunObserver

	// HITLHandler decides HITL gates inline (optional). Leave nil to have
	// gates suspend with a durable WorkflowCheckpoint (see ResumeWorkflow).
	HITLHandler HITLHandler

	// ToolExecutor runs a state machine's tool states and edge hooks. Usually
	// an agent's *shuttle.Executor: the walker goes through the same door a
	// model's tool call does.
	ToolExecutor ToolExecutor

	// WorkflowResolver finds the workflow a state delegates to by name
	// (optional). Without one, a state can only delegate to a workflow written
	// into it.
	WorkflowResolver WorkflowResolver
}

// WorkflowResolver finds a workflow by the name a state delegates to. The
// engine asks each time such a state is entered: where workflows are kept,
// and what a name means, is the host's to say.
type WorkflowResolver func(ctx context.Context, name string) (*loomv1.WorkflowPattern, error)

// NewOrchestrator creates a new orchestrator instance.
func NewOrchestrator(config Config) *Orchestrator {
	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}
	if config.Tracer == nil {
		config.Tracer = observability.NewNoOpTracer()
	}

	o := &Orchestrator{
		observer:         config.RunObserver,
		places:           newMemRecords(),
		resolver:         config.WorkflowResolver,
		agents:           make(map[string]*agent.Agent),
		registry:         config.Registry,
		llmProvider:      config.LLMProvider,
		tracer:           config.Tracer,
		logger:           config.Logger,
		messageBus:       config.MessageBus,
		sharedMemory:     config.SharedMemory,
		progressCallback: config.ProgressCallback,
		llmSemaphore:     config.LLMSemaphore,
		taskManager:      config.TaskManager,
		hitlHandler:      config.HITLHandler,
		toolExecutor:     config.ToolExecutor,
	}

	// Initialize collaboration engine with orchestrator as provider
	o.collaborationEngine = collaboration.NewEngineWithObservability(o, config.Tracer, config.Logger)

	return o
}

// registeredAgents returns a snapshot of the registered agents. Used by
// state-machine dismissal, which must reach every live session of a run.
func (o *Orchestrator) registeredAgents() []*agent.Agent {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]*agent.Agent, 0, len(o.agents))
	for _, ag := range o.agents {
		out = append(out, ag)
	}
	return out
}

// SetToolExecutor sets or updates the tool executor. Hosts that build the
// orchestrator before their tool registry exists can wire it afterwards.
func (o *Orchestrator) SetToolExecutor(exec ToolExecutor) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.toolExecutor = exec
}

// SetWorkflowResolver sets or updates how a workflow delegated to by name is
// found. Hosts that build the orchestrator before they know where workflows
// are kept can wire it afterwards.
func (o *Orchestrator) SetWorkflowResolver(find WorkflowResolver) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.resolver = find
}

// workflowNamed finds the workflow of a name, as it is defined now.
func (o *Orchestrator) workflowNamed(ctx context.Context, name string) (*loomv1.WorkflowPattern, error) {
	o.mu.RLock()
	find := o.resolver
	o.mu.RUnlock()
	if find == nil {
		return nil, fmt.Errorf("no workflow %q: this engine was given no way to find a workflow by name", name)
	}
	found, err := find(ctx, name)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("no workflow %q", name)
	}
	return found, nil
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
// When a TaskManager is configured, workflow execution is automatically
// wrapped with persistent task tracking (board creation, stage tasks,
// progress recording, resume support).
func (o *Orchestrator) ExecutePattern(ctx context.Context, pattern *loomv1.WorkflowPattern) (*loomv1.WorkflowResult, error) {
	// If task tracking is configured and this isn't already a tracked call,
	// delegate to the TaskTrackedOrchestrator.
	if o.taskManager != nil && ctx.Value(taskTrackingKey{}) == nil {
		ctx = context.WithValue(ctx, taskTrackingKey{}, true)
		tracked := NewTaskTrackedOrchestrator(o, o.taskManager, o.tracer, o.logger)
		return tracked.ExecutePattern(ctx, pattern)
	}

	patternType := GetPatternType(pattern)

	// Emit initial progress
	o.emitProgress(WorkflowProgressEvent{
		PatternType: patternType,
		WorkflowID:  pattern.GetWorkflowId(),
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
		WorkflowID:  pattern.GetWorkflowId(),
		Message:     "Executing workflow pattern",
		Progress:    20,
	})

	// Route to appropriate executor based on pattern type
	var result *loomv1.WorkflowResult
	var err error

	switch p := pattern.Pattern.(type) {
	case *loomv1.WorkflowPattern_ForkJoin:
		wfID := o.resolveWorkflowID("fork-join", pattern)
		executor := NewForkJoinExecutor(o, p.ForkJoin, wfID)
		result, err = executor.Execute(ctx)

	case *loomv1.WorkflowPattern_Pipeline:
		wfID := o.resolveWorkflowID("pipeline", pattern)
		executor := NewPipelineExecutor(o, p.Pipeline, wfID)
		result, err = executor.Execute(ctx)

	case *loomv1.WorkflowPattern_Parallel:
		wfID := o.resolveWorkflowID("parallel", pattern)
		executor := NewParallelExecutor(o, p.Parallel, wfID)
		result, err = executor.Execute(ctx)

	case *loomv1.WorkflowPattern_Conditional:
		wfID := o.resolveWorkflowID("conditional", pattern)
		executor := NewConditionalExecutor(o, p.Conditional, wfID)
		result, err = executor.Execute(ctx)

	case *loomv1.WorkflowPattern_Iterative:
		wfID := o.resolveWorkflowID("iterative", pattern)
		executor := NewIterativePipelineExecutor(o, p.Iterative, o.messageBus, wfID)
		result, err = executor.Execute(ctx)

	case *loomv1.WorkflowPattern_StateMachine:
		wfID := o.resolveWorkflowID("state-machine", pattern)
		executor := NewStateMachineExecutor(o, p.StateMachine, wfID)
		result, err = executor.Execute(ctx)

	case *loomv1.WorkflowPattern_Swarm:
		wfID := o.resolveWorkflowID("swarm", pattern)
		executor := NewSwarmExecutor(o, p.Swarm, wfID)
		result, err = executor.Execute(ctx)

	default:
		return nil, fmt.Errorf("unknown workflow pattern type")
	}

	if err != nil {
		// Emit error progress
		o.emitProgress(WorkflowProgressEvent{
			PatternType: patternType,
			WorkflowID:  pattern.GetWorkflowId(),
			Message:     fmt.Sprintf("Workflow failed: %v", err),
			Progress:    0,
		})
		return nil, err
	}

	// Emit completion progress
	o.emitProgress(WorkflowProgressEvent{
		PatternType:    patternType,
		WorkflowID:     pattern.GetWorkflowId(),
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

// resolveWorkflowID returns a stable or random workflow ID.
// If the pattern has a workflow_id set (for scheduled RESUME mode), use it.
// Otherwise generate a random UUID-based ID with the pattern type prefix.
func (o *Orchestrator) resolveWorkflowID(patternType string, pattern *loomv1.WorkflowPattern) string {
	if pattern != nil && pattern.WorkflowId != "" {
		return pattern.WorkflowId
	}
	return fmt.Sprintf("%s-%s", patternType, uuid.New().String()[:8])
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

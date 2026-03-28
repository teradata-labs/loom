
# Multi-Agent Orchestration Architecture

Architecture of Loom's multi-agent orchestration system with 9 workflow patterns, fluent API, Kubernetes-style YAML configuration, and tri-modal communication for coordinating multiple agents.

**Target Audience**: Architects, academics, and advanced developers

**Version**: v1.2.0


## Table of Contents

- [Overview](#overview)
- [Design Goals](#design-goals)
- [System Context](#system-context)
- [Architecture Overview](#architecture-overview)
- [Components](#components)
  - [Orchestrator](#orchestrator)
  - [Pattern Builders](#pattern-builders)
  - [Pattern Executors](#pattern-executors)
  - [YAML Config Loader](#yaml-config-loader)
  - [Collaboration Engine](#collaboration-engine)
  - [Multi-Agent Server](#multi-agent-server)
- [Key Interactions](#key-interactions)
  - [Pipeline Execution Flow](#pipeline-execution-flow)
  - [Fork-Join Parallel Execution](#fork-join-parallel-execution)
  - [YAML Workflow Loading](#yaml-workflow-loading)
- [Data Structures](#data-structures)
- [Algorithms](#algorithms)
  - [Pattern Routing](#pattern-routing)
  - [Merge Strategy Selection](#merge-strategy-selection)
  - [Template Variable Substitution](#template-variable-substitution)
- [Design Trade-offs](#design-trade-offs)
- [Constraints and Limitations](#constraints-and-limitations)
- [Performance Characteristics](#performance-characteristics)
- [Concurrency Model](#concurrency-model)
- [Error Handling](#error-handling)
- [Security Considerations](#security-considerations)
- [Related Work](#related-work)
- [References](#references)
- [Further Reading](#further-reading)


## Overview

The Multi-Agent Orchestration System coordinates **multiple agents using workflow patterns** that enable complex multi-agent behaviors: debate, pipeline execution, parallel processing, fork-join synchronization, conditional routing, and iterative refinement with autonomous restart.

**9 Workflow Patterns**:
1. **Pipeline**: Sequential agent chaining (A -> B -> C)
2. **Fork-Join**: Parallel execution with merge (A, B, C -> merge)
3. **Parallel**: Independent parallel tasks (no merge required)
4. **Conditional**: Dynamic routing based on classifier agent output
5. **Debate**: Multi-round consensus building with optional moderator
6. **Swarm**: Collective voting with configurable strategies (majority, supermajority, ranked choice)
7. **Pair Programming**: Driver/navigator role-based code generation and review
8. **Teacher-Student**: Knowledge transfer from expert to learner
9. **Iterative Pipeline**: Self-correcting pipeline with restart coordination

The system provides **both programmatic (fluent API) and declarative (Kubernetes-style YAML)** workflow definition, with full observability (Hawk tracing), tri-modal communication (broadcast, queue, shared memory), and cost tracking per agent.

**Key Innovation**: Fluent builder API + Kubernetes-style YAML + iterative workflows with autonomous restart coordination via pub/sub messaging.


## Design Goals

1. **Composable Patterns**: Mix and match 9 patterns for complex workflows
2. **Fluent API**: Type-safe builder pattern for programmatic construction
3. **Kubernetes-Style YAML**: Declarative workflows (apiVersion/kind/metadata/spec)
4. **Zero External Dependencies**: Single-process multi-agent coordination
5. **Full Observability**: Hawk tracing for every pattern, stage, and agent execution
6. **Cost Attribution**: Track LLM costs per agent, per stage, per workflow
7. **Tri-Modal Communication**: Broadcast events, queue messages, shared memory data

**Non-goals**:
- Distributed orchestration across processes (single-process multi-agent server only)
- Long-running workflow persistence (ephemeral workflows, use external orchestrator for persistence)
- GUI workflow builder (YAML-first, CLI/programmatic execution)


## System Context

```mermaid
graph TB
    subgraph External["External Environment"]
        User[User/Client<br/>YAML/API]
        Agents[Agents<br/>executor]
        Comm[Communication<br/>tri-modal]
        Hawk[Hawk<br/>tracing]
    end

    subgraph MultiAgent["Multi-Agent Orchestration System"]
        subgraph Orchestrator["Orchestrator (Pattern Coordinator)"]
            Registry[Agent Registry<br/>lookup by ID]
            Builders[Pattern Builders<br/>fluent API]
            Executors[Pattern Executors<br/>9 types]
            YAMLLoader[YAML Config Loader<br/>Kubernetes-style]
        end

        subgraph CommSystem["Communication System (Tri-Modal)"]
            Bus[Broadcast Bus<br/>pub/sub events]
            Queue[Message Queue<br/>P2P persistent]
            SharedMem[Shared Memory<br/>large datasets]
        end

        subgraph CollabEngine["Collaboration Engine (Advanced Patterns)"]
            Debate[Debate<br/>multi-round consensus]
            PairProg[Pair Programming<br/>driver/navigator]
            TeacherStudent[Teacher-Student<br/>knowledge transfer]
        end
    end

    User --> Orchestrator
    Agents --> Orchestrator
    Comm --> CommSystem
    Hawk --> Orchestrator
```

**External Dependencies**:
- **Agents**: Registered agent instances (agent.Agent)
- **Communication System**: Tri-modal communication (MessageBus, MessageQueue, SharedMemory)
- **Hawk**: Observability tracing (optional, can use NoOpTracer)
- **LLM Provider**: For merge operations (consensus, summary merge strategies)


## Architecture Overview

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                  Multi-Agent Orchestration System                            │
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │                   Orchestrator Core                          │         │  │
│  │                                                              │         │  │
│  │  agents: map[string]*Agent   (local registry)               │          │  │
│  │  registry: *agent.Registry   (global lookup)                │          │  │
│  │  llmProvider: LLMProvider    (for merge operations)         │          │  │
│  │  tracer: observability.Tracer (Hawk integration)            │          │  │
│  │  collaborationEngine: *Engine (advanced patterns)           │          │  │
│  │  messageBus: *MessageBus      (pub/sub events)              │          │  │
│  │  sharedMemory: *SharedMemory  (K-V store)                   │          │  │
│  │                                                              │         │  │
│  │  Methods:                                                    │         │  │
│  │    RegisterAgent(id, agent)                                 │          │  │
│  │    GetAgent(id) → agent                                     │          │  │
│  │    Debate(topic) → *DebateBuilder                           │          │  │
│  │    Fork(prompt) → *ForkJoinBuilder                          │          │  │
│  │    Pipeline(prompt) → *PipelineBuilder                      │          │  │
│  │    Parallel() → *ParallelBuilder                            │          │  │
│  │    Conditional(classifier, prompt) → *ConditionalBuilder    │          │  │
│  │    ExecutePattern(pattern) → *WorkflowResult                │          │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                            │                                                 │
│                            ▼                                                 │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │              Pattern Builders (Fluent API)                   │         │  │
│  │                                                              │         │  │
│  │  DebateBuilder:                                              │         │  │
│  │    .WithAgents(agents...)                                     │         │  │
│  │    .WithRounds(n)                                            │         │  │
│  │    .WithMergeStrategy(strategy)                              │         │  │
│  │    .Execute(ctx) → Result                                    │         │  │
│  │                                                              │         │  │
│  │  PipelineBuilder:                                            │         │  │
│  │    .WithStage(agent, promptTemplate)                         │         │  │
│  │    .WithStageValidation(agent, promptTemplate, validation)   │         │  │
│  │    .WithFullHistory()                                        │         │  │
│  │    .Execute(ctx) → Result                                    │         │  │
│  │                                                              │         │  │
│  │  ForkJoinBuilder, ParallelBuilder, ConditionalBuilder...     │         │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                            │                                                 │
│                            ▼                                                 │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │              Pattern Executors (9 types)                     │         │  │
│  │                                                              │         │  │
│  │  PipelineExecutor:                                           │         │  │
│  │    1. Validate all agents exist                              │         │  │
│  │    2. Execute stages sequentially                            │         │  │
│  │    3. Build prompt: substitute {{previous}}, {{history}}     │         │  │
│  │    4. Optional validation per stage                          │         │  │
│  │    5. Pass output to next stage                              │         │  │
│  │    6. Return final stage output                              │         │  │
│  │                                                              │         │  │
│  │  ForkJoinExecutor:                                           │         │  │
│  │    1. Spawn goroutines for each agent                        │         │  │
│  │    2. Execute same prompt on all agents                      │         │  │
│  │    3. Collect results via channel                            │         │  │
│  │    4. Apply merge strategy (voting, consensus, summary)      │         │  │
│  │    5. Return merged result                                   │         │  │
│  │                                                              │         │  │
│  │  ParallelExecutor, ConditionalExecutor, IterativeExecutor,   │         │  │
│  │  SwarmExecutor                                               │         │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                            │                                                 │
│                            ▼                                                 │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │           YAML Config Loader (Kubernetes-Style)              │         │  │
│  │                                                              │         │  │
│  │  WorkflowConfig:                                             │         │  │
│  │    apiVersion: "loom/v1"                                     │         │  │
│  │    kind: "Workflow"                                          │         │  │
│  │    metadata:                                                 │         │  │
│  │      name: "analytics-pipeline"                              │         │  │
│  │      version: "v1"                                           │         │  │
│  │      labels: {domain: "analytics"}                           │         │  │
│  │    spec:                                                     │         │  │
│  │      type: "pipeline|debate|fork-join|..."                  │          │  │
│  │      ...pattern-specific fields...                           │         │  │
│  │                                                              │         │  │
│  │  LoadWorkflowFromYAML(path) → *WorkflowPattern               │         │  │
│  │    1. Read file (check permissions)                          │         │  │
│  │    2. Parse YAML (validate syntax)                           │         │  │
│  │    3. Validate structure (apiVersion, kind, spec.type)       │         │  │
│  │    4. Convert to proto (pattern-specific conversion)         │         │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                            │                                                 │
│                            ▼                                                 │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │          Collaboration Engine (Advanced Patterns)            │         │  │
│  │                                                              │         │  │
│  │  Debate:                                                     │         │  │
│  │    - Multi-round consensus building                          │         │  │
│  │    - Each round sees previous responses                      │         │  │
│  │    - Optional moderator agent                                │         │  │
│  │                                                              │         │  │
│  │  Pair Programming:                                           │         │  │
│  │    - Driver/Navigator roles                                  │         │  │
│  │    - Turn-based code generation and review                   │         │  │
│  │                                                              │         │  │
│  │  Teacher-Student:                                            │         │  │
│  │    - Knowledge transfer from expert to learner              │          │  │
│  │    - Iterative learning with feedback                        │         │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────────┘
```


## Components

### Orchestrator

**Responsibility**: Coordinate multiple agents using workflow patterns with fluent API and YAML configuration.

**Core Structure** (`pkg/orchestration/orchestrator.go:46`):
```go
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
    llmSemaphore chan struct{}
}
```

**Agent Registration**:
```go
// Two-level registry:
// 1. Local cache (o.agents) for workflow-specific agents
// 2. Global registry (o.registry) for server-wide agent lookup

func (o *Orchestrator) RegisterAgent(id string, ag *agent.Agent) {
    o.mu.Lock()
    defer o.mu.Unlock()
    o.agents[id] = ag
}

func (o *Orchestrator) GetAgent(ctx context.Context, id string) (*agent.Agent, error) {
    // Try local cache first
    if ag, exists := o.agents[id]; exists {
        return ag, nil
    }

    // Fall back to global registry
    if o.registry != nil {
        ag, err := o.registry.GetAgent(ctx, id)
        if err == nil {
            o.RegisterAgent(id, ag)  // Cache locally
            return ag, nil
        }
    }

    return nil, fmt.Errorf("agent not found: %s", id)
}
```

**Pattern Factory Methods**:
```go
// Fluent API entry points
orchestrator.Debate(topic) → *DebateBuilder
orchestrator.Fork(prompt) → *ForkJoinBuilder
orchestrator.Pipeline(initialPrompt) → *PipelineBuilder
orchestrator.Parallel() → *ParallelBuilder
orchestrator.Conditional(classifier, prompt) → *ConditionalBuilder
```

**Pattern Routing Algorithm**:
```go
func (o *Orchestrator) ExecutePattern(ctx context.Context, pattern *loomv1.WorkflowPattern) (*loomv1.WorkflowResult, error) {
    // Route collaboration patterns to collaboration engine first
    switch pattern.Pattern.(type) {
    case *loomv1.WorkflowPattern_Debate,
         *loomv1.WorkflowPattern_PairProgramming,
         *loomv1.WorkflowPattern_TeacherStudent:
        // Collaboration patterns → collaboration engine
        return o.collaborationEngine.Execute(ctx, pattern)
    }

    // Route to orchestration executors (each takes a workflowID for session determinism)
    switch p := pattern.Pattern.(type) {
    case *loomv1.WorkflowPattern_ForkJoin:
        wfID := o.resolveWorkflowID("fork-join", pattern)
        executor := NewForkJoinExecutor(o, p.ForkJoin, wfID)
        return executor.Execute(ctx)

    case *loomv1.WorkflowPattern_Pipeline:
        wfID := o.resolveWorkflowID("pipeline", pattern)
        executor := NewPipelineExecutor(o, p.Pipeline, wfID)
        return executor.Execute(ctx)

    case *loomv1.WorkflowPattern_Parallel:
        wfID := o.resolveWorkflowID("parallel", pattern)
        executor := NewParallelExecutor(o, p.Parallel, wfID)
        return executor.Execute(ctx)

    case *loomv1.WorkflowPattern_Conditional:
        wfID := o.resolveWorkflowID("conditional", pattern)
        executor := NewConditionalExecutor(o, p.Conditional, wfID)
        return executor.Execute(ctx)

    case *loomv1.WorkflowPattern_Iterative:
        wfID := o.resolveWorkflowID("iterative", pattern)
        executor := NewIterativePipelineExecutor(o, p.Iterative, o.messageBus, wfID)
        return executor.Execute(ctx)

    case *loomv1.WorkflowPattern_Swarm:
        wfID := o.resolveWorkflowID("swarm", pattern)
        executor := NewSwarmExecutor(o, p.Swarm, wfID)
        return executor.Execute(ctx)

    default:
        return nil, fmt.Errorf("unknown workflow pattern type")
    }
}
```

**Rationale**:
- **Two-level registry**: Local cache for workflow-specific agents, global registry for server-wide lookup
- **Fluent API**: Type-safe builder pattern prevents invalid workflow construction
- **Pattern routing**: Collaboration patterns (debate, pair programming, teacher-student) use separate engine, orchestration patterns (pipeline, fork-join, parallel, conditional, iterative, swarm) use dedicated executors
- **Communication integration**: Optional MessageBus and SharedMemory for iterative workflows with autonomous restart


### Pattern Builders

**Responsibility**: Fluent API for type-safe workflow construction with compile-time validation.

**Pipeline Builder** (`pkg/orchestration/pipeline_builder.go`):
```go
type PipelineBuilder struct {
    orchestrator  *Orchestrator
    initialPrompt string
    stages        []*loomv1.PipelineStage
    passFullHistory bool
}

// Fluent API chain -- first parameter is *agent.Agent, not string.
// The builder auto-generates an agent ID from the pointer address.
func (b *PipelineBuilder) WithStage(ag *agent.Agent, promptTemplate string) *PipelineBuilder {
    agentID := fmt.Sprintf("pipeline_agent_%p", ag)
    b.orchestrator.RegisterAgent(agentID, ag)
    b.stages = append(b.stages, &loomv1.PipelineStage{
        AgentId:        agentID,
        PromptTemplate: promptTemplate,
    })
    return b
}

// WithFullHistory enables passing full history to each stage (no parameter).
func (b *PipelineBuilder) WithFullHistory() *PipelineBuilder {
    b.passFullHistory = true
    return b
}

func (b *PipelineBuilder) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
    pattern := &loomv1.WorkflowPattern{
        Pattern: &loomv1.WorkflowPattern_Pipeline{
            Pipeline: &loomv1.PipelinePattern{
                InitialPrompt:   b.initialPrompt,
                Stages:          b.stages,
                PassFullHistory: b.passFullHistory,
            },
        },
    }
    return b.orchestrator.ExecutePattern(ctx, pattern)
}
```

**Usage Example**:
```go
// Template placeholders use strings.ReplaceAll, not text/template:
//   {{previous}} = previous stage output
//   {{history}}  = all previous outputs
// There is no {{initial}} placeholder; the initial prompt is passed to Pipeline().
result, err := orchestrator.
    Pipeline("Design a new feature").
    WithStage(architectAgent, "Create architecture for this: {{previous}}").
    WithStage(implementerAgent, "Implement: {{previous}}").
    WithStage(testerAgent, "Test: {{previous}}").
    Execute(ctx)
```

**Fork-Join Builder** (`pkg/orchestration/fork_join_builder.go`):
```go
type ForkJoinBuilder struct {
    orchestrator  *Orchestrator
    prompt        string
    agentIDs      []string
    mergeStrategy loomv1.MergeStrategy
    timeoutSecs   int32
}

// WithAgents takes *agent.Agent pointers, not strings.
// The builder auto-generates agent IDs from pointer addresses.
func (b *ForkJoinBuilder) WithAgents(agents ...*agent.Agent) *ForkJoinBuilder {
    for _, ag := range agents {
        agentID := fmt.Sprintf("fork_join_agent_%p", ag)
        b.orchestrator.RegisterAgent(agentID, ag)
        b.agentIDs = append(b.agentIDs, agentID)
    }
    return b
}

func (b *ForkJoinBuilder) Join(strategy loomv1.MergeStrategy) *ForkJoinBuilder {
    b.mergeStrategy = strategy
    return b
}

// Execute spawns parallel goroutines for each agent
func (b *ForkJoinBuilder) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
    pattern := &loomv1.WorkflowPattern{
        Pattern: &loomv1.WorkflowPattern_ForkJoin{
            ForkJoin: &loomv1.ForkJoinPattern{
                Prompt:         b.prompt,
                AgentIds:       b.agentIDs,
                MergeStrategy:  b.mergeStrategy,
                TimeoutSeconds: b.timeoutSecs,
            },
        },
    }
    return b.orchestrator.ExecutePattern(ctx, pattern)
}
```

**Conditional Builder** (`pkg/orchestration/conditional_builder.go`):
```go
type ConditionalBuilder struct {
    orchestrator    *Orchestrator
    classifierID    string
    conditionPrompt string
    branches        map[string]*loomv1.WorkflowPattern
    defaultBranch   *loomv1.WorkflowPattern
}

func (b *ConditionalBuilder) When(condition string, workflow *loomv1.WorkflowPattern) *ConditionalBuilder {
    b.branches[condition] = workflow
    return b
}

// Default (not "Otherwise") sets the default branch.
func (b *ConditionalBuilder) Default(pattern *loomv1.WorkflowPattern) *ConditionalBuilder {
    b.defaultBranch = pattern
    return b
}
```

**Rationale**:
- **Fluent API**: Method chaining prevents invalid states (e.g., can't execute without stages)
- **Type safety**: Compile-time validation of pattern structure
- **Immutable patterns**: Builders produce immutable proto messages for thread-safe execution
- **Composability**: Conditional branches can contain any workflow pattern (recursive composition)


### Pattern Executors

**Responsibility**: Execute specific workflow patterns with stage-level observability and error handling.

**Pipeline Executor** (`pkg/orchestration/pipeline_executor.go:21`):
```go
type PipelineExecutor struct {
    orchestrator *Orchestrator
    pattern      *loomv1.PipelinePattern
    workflowID   string
}

func (e *PipelineExecutor) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
    workflowID := fmt.Sprintf("pipeline-%s", uuid.New().String()[:8])

    // Workflow-level span
    ctx, workflowSpan := e.orchestrator.tracer.StartSpan(ctx, "workflow.pipeline")
    defer e.orchestrator.tracer.EndSpan(workflowSpan)

    // Validate all agents exist (fail fast)
    for i, stage := range e.pattern.Stages {
        if _, err := e.orchestrator.GetAgent(ctx, stage.AgentId); err != nil {
            return nil, fmt.Errorf("agent not found for stage %d: %s: %w", i, stage.AgentId, err)
        }
    }

    // Execute stages sequentially
    allResults := make([]*loomv1.AgentResult, 0)
    stageOutputs := make([]string, 0)
    currentInput := e.pattern.InitialPrompt

    for i, stage := range e.pattern.Stages {
        // Stage-level span
        ctx, stageSpan := e.orchestrator.tracer.StartSpan(ctx, fmt.Sprintf("pipeline.stage.%d", i+1))

        // Build prompt with variable substitution
        prompt := e.buildStagePrompt(stage, currentInput, stageOutputs)

        // Execute agent
        result, model, err := e.executeStageWithSpan(ctx, workflowID, stage, prompt, i+1)
        e.orchestrator.tracer.EndSpan(stageSpan)

        if err != nil {
            return nil, fmt.Errorf("stage %d failed: %w", i+1, err)
        }

        // Optional validation
        if stage.ValidationPrompt != "" {
            valid, err := e.validateStageOutput(ctx, workflowID, stage, result.Output, i+1)
            if err != nil || !valid {
                return nil, fmt.Errorf("stage %d validation failed", i+1)
            }
        }

        allResults = append(allResults, result)
        stageOutputs = append(stageOutputs, result.Output)
        currentInput = result.Output  // Pass to next stage
    }

    return &loomv1.WorkflowResult{
        PatternType:  "pipeline",
        AgentResults: allResults,
        MergedOutput: stageOutputs[len(stageOutputs)-1],
        DurationMs:   time.Since(startTime).Milliseconds(),
        Cost:         e.calculateCost(allResults),  // *WorkflowCost (not CostBreakdown)
        ModelsUsed:   modelsUsed,
    }, nil
}
```

**Fork-Join Executor** (`pkg/orchestration/fork_join_executor.go`):
```go
func (e *ForkJoinExecutor) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
    // Spawn goroutines for parallel execution
    type agentResult struct {
        agentID string
        result  *loomv1.AgentResult
        err     error
    }

    resultsCh := make(chan agentResult, len(e.pattern.AgentIds))
    var wg sync.WaitGroup

    for _, agentID := range e.pattern.AgentIds {
        wg.Add(1)
        go func(id string) {
            defer wg.Done()

            agent, err := e.orchestrator.GetAgent(ctx, id)
            if err != nil {
                resultsCh <- agentResult{agentID: id, err: err}
                return
            }

            // Execute agent with same prompt
            // agent.Chat returns (*Response, error)
            response, err := agent.Chat(ctx, sessionID, e.pattern.Prompt)
            resultsCh <- agentResult{
                agentID: id,
                result:  &loomv1.AgentResult{AgentId: id, Output: response.Content},
                err:     err,
            }
        }(agentID)
    }

    // Wait for all goroutines
    wg.Wait()
    close(resultsCh)

    // Collect results
    results := make([]*loomv1.AgentResult, 0)
    for r := range resultsCh {
        if r.err != nil {
            return nil, fmt.Errorf("agent %s failed: %w", r.agentID, r.err)
        }
        results = append(results, r.result)
    }

    // Apply merge strategy
    mergedOutput, err := e.mergeResults(ctx, results, e.pattern.MergeStrategy)
    if err != nil {
        return nil, fmt.Errorf("merge failed: %w", err)
    }

    return &loomv1.WorkflowResult{
        PatternType:  "fork_join",
        AgentResults: results,
        MergedOutput: mergedOutput,
    }, nil
}
```

**Iterative Pipeline Executor** (with autonomous restart):
```go
func (e *IterativePipelineExecutor) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
    iteration := 1
    maxIterations := e.pattern.MaxIterations

    // Subscribe to restart topic
    restartTopic := e.pattern.RestartTopic  // default: "workflow.restart"
    restartCh := e.messageBus.Subscribe(restartTopic)

    for iteration <= maxIterations {
        // Execute base pipeline
        result, err := e.executePipeline(ctx, iteration)

        // Check for restart requests
        select {
        case restartReq := <-restartCh:
            // Agent requested restart of earlier stage
            targetStageID := restartReq.TargetStageId

            // Validate restart policy
            if !e.isRestartAllowed(targetStageID) {
                continue  // Ignore invalid restart
            }

            // Restart from target stage
            e.logger.Info("Restarting stage", zap.String("stage", targetStageID))
            iteration++
            continue

        default:
            // No restart, workflow complete
            return result, err
        }
    }

    return nil, fmt.Errorf("max iterations exceeded: %d", maxIterations)
}
```

**Rationale**:
- **Goroutine per agent**: Fork-join uses goroutines for true parallel execution
- **Channel communication**: Results collected via buffered channel (thread-safe)
- **Stage-level spans**: Every stage traced separately for cost attribution
- **Fail-fast validation**: Agent existence validated before execution starts
- **Autonomous restart**: Iterative executor subscribes to pub/sub for restart coordination


### YAML Config Loader

**Responsibility**: Load Kubernetes-style YAML workflows with validation and proto conversion.

**Workflow YAML Structure** (`pkg/orchestration/workflow_config.go:29`):
```yaml
apiVersion: loom/v1
kind: Workflow
metadata:
  name: analytics-pipeline
  version: v1
  description: Multi-stage analytics workflow
  labels:
    domain: analytics
    team: data-science
spec:
  type: pipeline
  initial_prompt: "Analyze sales data"
  stages:
    - agent_id: schema-agent
      prompt_template: "Discover schema: {{previous}}"
    - agent_id: optimizer-agent
      prompt_template: "Optimize query: {{previous}}"
    - agent_id: executor-agent
      prompt_template: "Execute: {{previous}}"
  pass_full_history: false
```

**Loading Algorithm** (`pkg/orchestration/workflow_config.go:71`):
```go
func LoadWorkflowFromYAML(path string) (*loomv1.WorkflowPattern, error) {
    // 1. Read file (check permissions)
    data, err := readWorkflowFile(path)
    if err != nil {
        return nil, err  // ErrFileNotFound, ErrInvalidPermissions
    }

    // 2. Parse YAML (validate syntax)
    config, err := parseWorkflowYAML(data)
    if err != nil {
        return nil, err  // ErrInvalidYAML
    }

    // 3. Validate structure
    if err := validateWorkflowStructure(config); err != nil {
        return nil, err  // ErrInvalidWorkflow, ErrUnsupportedPattern
    }

    // 4. Convert to proto (pattern-specific conversion)
    pattern, err := convertToProto(config)
    if err != nil {
        return nil, err
    }

    return pattern, nil
}
```

**Validation Rules**:
```go
func validateWorkflowStructure(config *WorkflowConfig) error {
    // Check apiVersion (must be "loom/v1")
    if config.APIVersion != "loom/v1" {
        return fmt.Errorf("unsupported apiVersion '%s', expected 'loom/v1'", config.APIVersion)
    }

    // Check kind (must be "Workflow")
    if config.Kind != "Workflow" {
        return fmt.Errorf("unsupported kind '%s', expected 'Workflow'", config.Kind)
    }

    // Check metadata.name (required)
    if config.Metadata.Name == "" {
        return fmt.Errorf("missing metadata.name")
    }

    // Check spec.type (must be one of 7 valid types)
    patternType, ok := config.Spec["type"].(string)
    if !ok {
        return fmt.Errorf("missing spec.type")
    }

    validTypes := []string{"debate", "fork-join", "pipeline", "parallel", "conditional", "iterative", "swarm"}
    if !contains(validTypes, patternType) {
        return fmt.Errorf("unsupported pattern type: %s", patternType)
    }

    return nil
}
```

**Proto Conversion** (pattern-specific):
```go
func convertPipelinePattern(spec map[string]interface{}) (*loomv1.WorkflowPattern, error) {
    initialPrompt := spec["initial_prompt"].(string)
    stagesRaw := spec["stages"].([]interface{})

    stages := make([]*loomv1.PipelineStage, len(stagesRaw))
    for i, stageRaw := range stagesRaw {
        stageMap := stageRaw.(map[string]interface{})
        stages[i] = &loomv1.PipelineStage{
            AgentId:          stageMap["agent_id"].(string),
            PromptTemplate:   stageMap["prompt_template"].(string),
            ValidationPrompt: stageMap["validation_prompt"].(string),
        }
    }

    return &loomv1.WorkflowPattern{
        Pattern: &loomv1.WorkflowPattern_Pipeline{
            Pipeline: &loomv1.PipelinePattern{
                InitialPrompt:   initialPrompt,
                Stages:          stages,
                PassFullHistory: spec["pass_full_history"].(bool),
            },
        },
    }, nil
}
```

**Rationale**:
- **Kubernetes-style YAML**: Familiar apiVersion/kind/metadata/spec structure
- **Proto conversion**: YAML → WorkflowPattern proto for type-safe execution
- **Validation errors**: Clear error messages with ErrFileNotFound, ErrInvalidYAML, ErrUnsupportedPattern
- **Pattern-specific conversion**: Each pattern type has dedicated conversion function


### Collaboration Engine

**Responsibility**: Execute advanced multi-agent patterns (debate, swarm, pair programming, teacher-student) with specialized coordination logic.

**Engine Structure** (`pkg/collaboration/engine.go`):
```go
type Engine struct {
    provider       AgentProvider  // Lookup agents by ID
    tracer         observability.Tracer
    logger         *zap.Logger
    debater        *DebateOrchestrator
    swarm          *SwarmOrchestrator
    pairProgrammer *PairProgrammingOrchestrator
    teacherStudent *TeacherStudentOrchestrator
}

func (e *Engine) Execute(ctx context.Context, pattern *loomv1.WorkflowPattern) (*loomv1.WorkflowResult, error) {
    switch p := pattern.Pattern.(type) {
    case *loomv1.WorkflowPattern_Debate:
        result, err = e.debater.Execute(ctx, p.Debate)
    case *loomv1.WorkflowPattern_Swarm:
        result, err = e.swarm.Execute(ctx, p.Swarm)
    case *loomv1.WorkflowPattern_PairProgramming:
        result, err = e.pairProgrammer.Execute(ctx, p.PairProgramming)
    case *loomv1.WorkflowPattern_TeacherStudent:
        result, err = e.teacherStudent.Execute(ctx, p.TeacherStudent)
    default:
        return nil, fmt.Errorf("unsupported collaboration pattern: %T", p)
    }
    // ...
}
// Note: Swarm is also available as a standalone SwarmExecutor in pkg/orchestration.
// The orchestrator routes Swarm to the orchestration executor, not the collaboration engine.
```

**Debate Pattern** (multi-round consensus):
```go
func (e *Engine) executeDebate(ctx context.Context, debate *loomv1.DebatePattern) (*loomv1.WorkflowResult, error) {
    topic := debate.Topic
    rounds := debate.Rounds
    agentIDs := debate.AgentIds

    allResults := make([]*loomv1.AgentResult, 0)
    debateHistory := make([]string, 0)

    for round := 1; round <= int(rounds); round++ {
        roundResults := make([]*loomv1.AgentResult, 0)

        for _, agentID := range agentIDs {
            agent, _ := e.provider.GetAgent(ctx, agentID)

            // Build prompt with debate history
            prompt := fmt.Sprintf("Topic: %s\n\nPrevious rounds:\n%s\n\nYour position for round %d:",
                topic, strings.Join(debateHistory, "\n---\n"), round)

            // agent.Chat returns (*Response, error)
            response, _ := agent.Chat(ctx, sessionID, prompt)

            result := &loomv1.AgentResult{
                AgentId: agentID,
                Output:  response.Content,
            }
            roundResults = append(roundResults, result)
            debateHistory = append(debateHistory, fmt.Sprintf("[%s]: %s", agentID, response.Content))
        }

        allResults = append(allResults, roundResults...)
    }

    // Apply merge strategy (consensus, voting, summary)
    mergedOutput, _ := e.applyMergeStrategy(ctx, allResults, debate.MergeStrategy)

    return &loomv1.WorkflowResult{
        PatternType:  "debate",
        AgentResults: allResults,
        MergedOutput: mergedOutput,
    }, nil
}
```

**Rationale**:
- **Separate engine**: Collaboration patterns have different coordination logic than orchestration patterns
- **Multi-round debate**: Each agent sees previous responses, builds consensus iteratively
- **Moderator support**: Optional moderator agent guides debate (not implemented in example above)
- **Merge strategies**: Consensus (LLM synthesizes agreement), Voting (majority wins), Summary (LLM summarizes all positions)


### Multi-Agent Server

**Responsibility**: Host multiple agents in single process with gRPC service, HTTP gateway, and agent registry.

**Server Structure** (`pkg/server/multi_agent.go:48`):
```go
type MultiAgentServer struct {
    loomv1.UnimplementedLoomServiceServer

    agents       map[string]*agent.Agent       // Agent instances by ID
    sessionStore agent.SessionStorage           // Session persistence
    mu           sync.RWMutex

    // Communication system (tri-modal: broadcast, point-to-point, shared memory)
    messageBus       *communication.MessageBus
    messageQueue     *communication.MessageQueue
    sharedMemoryComm *communication.SharedMemoryStore

    // Workflow execution
    workflowStore *WorkflowStore
    registry      *agent.Registry               // Agent registry for workflow execution
    scheduler     *scheduler.Scheduler           // Cron-based workflow scheduling
    tracer        observability.Tracer

    // LLM concurrency control
    llmSemaphore        chan struct{}
    llmConcurrencyLimit int
    // ... (additional fields for TLS, MCP, artifacts, etc.)
}

// Server startup is handled by cmd/looms/cmd_serve.go
// which reads looms.yaml for multi-agent configuration.
// The gRPC server registers LoomServiceServer and starts
// listening on the configured port.
```

**CLI Usage**:
```bash
# Start multi-agent server (reads looms.yaml for agent configuration)
looms serve

# Connect TUI client to specific agent
loom --thread agent1 --server localhost:5006

# Execute workflow across agents
looms workflow run analytics-pipeline.yaml
```

**Rationale**:
- **Single process**: All agents in one process for efficiency (no network overhead)
- **gRPC + HTTP**: gRPC for TUI client (streaming), HTTP for web dashboard
- **Agent registry**: Centralized agent lookup across workflows
- **Pattern hot-reload**: Separate pattern library per agent with file watch


### Concurrent Session Support

**Responsibility**: Enable multiple workflow sessions to run simultaneously without interference.

**Problem Statement**:

In multi-agent async workflows, coordinators delegate to sub-agents (e.g., vacation-coordinator → weather-analyst, vacation-planner). Initial implementation used single `agentID` keys, causing session conflicts:

```
Session 1: coordinator-abc spawns weather-analyst
Session 2: coordinator-xyz spawns weather-analyst → CANCELS Session 1's instance
```

This violated the **session isolation invariant**: workflows in different sessions must not interfere.

**Solution: Composite Key Architecture**:

```
┌──────────────────────────────────────────────────────────────────┐
│              Workflow Sub-Agent Registry                         │
│                                                                  │
│  workflowSubAgents: map[string]*workflowSubAgentContext         │
│                                                                  │
│  Key Format: "sessionID:agentID"                                │
│                                                                  │
│  ┌────────────────────────────────────────────────────┐         │
│  │ Session A                                          │         │
│  │  ┌──────────────────────────────────┐             │         │
│  │  │ Key: "sess-A:coordinator"        │             │         │
│  │  │ Context:                         │             │         │
│  │  │   - agent: *Agent                │             │         │
│  │  │   - sessionID: "sess-A"          │             │         │
│  │  │   - notifyChan: chan struct{}    │             │         │
│  │  │   - cancelFunc: context.Cancel   │             │         │
│  │  └──────────────────────────────────┘             │         │
│  │                                                    │         │
│  │  ┌──────────────────────────────────┐             │         │
│  │  │ Key: "sess-A:weather-analyst"    │             │         │
│  │  │ Context: {...}                   │             │         │
│  │  └──────────────────────────────────┘             │         │
│  └────────────────────────────────────────────────────┘         │
│                                                                  │
│  ┌────────────────────────────────────────────────────┐         │
│  │ Session B (concurrent, isolated)                   │         │
│  │  ┌──────────────────────────────────┐             │         │
│  │  │ Key: "sess-B:coordinator"        │             │         │
│  │  │ Context: {...}                   │             │         │
│  │  └──────────────────────────────────┘             │         │
│  │                                                    │         │
│  │  ┌──────────────────────────────────┐             │         │
│  │  │ Key: "sess-B:weather-analyst"    │             │         │
│  │  │ Context: {...}                   │             │         │
│  │  └──────────────────────────────────┘             │         │
│  └────────────────────────────────────────────────────┘         │
└──────────────────────────────────────────────────────────────────┘
```

**Implementation** (`pkg/server/multi_agent.go:1277+`):

```go
// Coordinator registration with composite key
coordinatorKey := fmt.Sprintf("%s:%s", sessionID, coordinatorID)
s.workflowSubAgents[coordinatorKey] = &workflowSubAgentContext{
    agent:       coordinatorAgent,
    sessionID:   sessionID,
    workflowID:  workflowName,
    notifyChan:  coordinatorNotifyChan,
    cancelFunc:  coordinatorCancel,
    lastChecked: time.Now(),
}

// Sub-agent registration with conflict prevention
subAgentKey := fmt.Sprintf("%s:%s", sessionID, subAgentID)
if existingCtx, exists := s.workflowSubAgents[subAgentKey]; exists {
    // Cancel existing before spawning new (prevents orphaned goroutines)
    existingCtx.cancelFunc()
    delete(s.workflowSubAgents, subAgentKey)
}

s.workflowSubAgents[subAgentKey] = &workflowSubAgentContext{
    agent:               subAgent,
    sessionID:           sessionID,
    workflowID:          workflowName,
    notifyChan:          subAgentNotifyChan,
    cancelFunc:          subAgentCancel,
    lastChecked:         time.Now(),
    consecutiveFailures: 0,
}
```

**Lookup Algorithm** (monitor goroutine finds sub-agents by suffix):

```go
// Find all sub-agents for a session's workflow
for key, ctx := range s.workflowSubAgents {
    // Match by suffix (agentID part of composite key)
    if strings.HasSuffix(key, ":"+agentID) && ctx.sessionID == sessionID {
        // Found matching sub-agent
    }
}
```

**Design Trade-offs**:

| Approach | Pros | Cons | Decision |
|----------|------|------|----------|
| **Single agentID keys** (original) | Simple map structure, fast lookup | Session conflicts, agents cancel each other | ❌ Rejected |
| **Composite keys** (chosen) | Session isolation, concurrent workflows | Suffix matching O(n) for lookups, 2x key length | ✅ Chosen |
| **Nested maps** (session → agent) | Fast lookup by session, type-safe | Higher memory overhead (N maps), complex iteration | ❌ Rejected |

**Formal Properties**:

**Invariant 1: Session Isolation**
```
∀ sessions s1, s2 where s1 ≠ s2:
  Sub-agents spawned in s1 do not affect s2's execution
```

**Invariant 2: Composite Key Uniqueness**
```
∀ key = "sessionID:agentID" ∈ workflowSubAgents:
  key is unique (one entry per session-agent pair)
```

**Invariant 3: Context Cleanup**
```
When new sub-agent spawned with existing key:
  1. Cancel existing context
  2. Delete old entry
  3. Insert new entry
  (Prevents orphaned goroutines)
```

**Performance**:
- Map insert/delete: O(1)
- Lookup by composite key: O(1)
- Monitor suffix matching: O(n) where n = total sub-agents (acceptable, runs every 5s)

**Rationale**:
- Composite keys chosen over nested maps for simpler iteration patterns
- Suffix matching acceptable since n is small (typically <10 sub-agents per server)
- Explicit conflict prevention (cancel before spawn) ensures clean teardown


### Exponential Backoff for Resilience

**Responsibility**: Retry failed sub-agent operations with progressive delays to handle transient failures and API rate limits.

**Problem Statement**:

Sub-agents call LLM APIs (Bedrock, Anthropic) which can throttle with 429 errors. Original implementation retried immediately:

```
Attempt 1: Bedrock ThrottlingException → retry immediately
Attempt 2: Bedrock ThrottlingException → retry immediately
Attempt 3: Bedrock ThrottlingException → retry immediately
...
(Tight loop exhausts API quota, delays recovery)
```

**Solution: Exponential Backoff Algorithm**:

```
┌──────────────────────────────────────────────────────────────────┐
│               Sub-Agent Retry Flow                               │
│                                                                  │
│  ┌─────────┐                                                     │
│  │ Attempt │                                                     │
│  │ Chat()  │                                                     │
│  └────┬────┘                                                     │
│       │                                                          │
│       ▼                                                          │
│  ┌─────────┐     Success      ┌──────────────┐                 │
│  │ Check   │─────────────────▶│ Reset        │                 │
│  │ Error   │                   │ failures = 0 │                 │
│  └────┬────┘                   └──────────────┘                 │
│       │                                                          │
│       │ Failure                                                  │
│       ▼                                                          │
│  ┌─────────────────────────────────────────┐                   │
│  │ failures++                              │                   │
│  │ backoff = min(2^(failures-1), 32)       │                   │
│  │                                         │                   │
│  │ Examples:                               │                   │
│  │   failures=1 → backoff=1s               │                   │
│  │   failures=2 → backoff=2s               │                   │
│  │   failures=3 → backoff=4s               │                   │
│  │   failures=4 → backoff=8s               │                   │
│  │   failures=5 → backoff=16s              │                   │
│  │   failures=6 → backoff=32s (capped)     │                   │
│  └─────────────┬───────────────────────────┘                   │
│                │                                                │
│                ▼                                                │
│  ┌─────────────────────────────────────────┐                   │
│  │ select {                                │                   │
│  │   case <-time.After(backoffDuration):   │                   │
│  │     // Backoff completed, retry          │                   │
│  │   case <-ctx.Done():                    │                   │
│  │     // Context cancelled, abort          │                   │
│  │ }                                       │                   │
│  └─────────────┬───────────────────────────┘                   │
│                │                                                │
│                └─────────── Loop back to Attempt               │
└──────────────────────────────────────────────────────────────────┘
```

**Implementation** (`pkg/server/multi_agent.go:2200+`):

```go
// Context structure with failure tracking
type workflowSubAgentContext struct {
    agent               *agent.Agent
    sessionID           string
    workflowID          string
    notifyChan          chan struct{}
    cancelFunc          context.CancelFunc
    lastChecked         time.Time
    consecutiveFailures int // NEW: For exponential backoff
}

// Monitor loop with exponential backoff
for {
    // Execute agent Chat (returns *Response, error)
    _, err := subAgentCtx.agent.Chat(ctx, subAgentCtx.sessionID, "")

    if err != nil {
        // On failure - apply exponential backoff
        subAgentCtx.consecutiveFailures++
        failures := subAgentCtx.consecutiveFailures

        // Calculate: 2^(n-1) seconds, max 32s
        backoffSeconds := min(1<<(failures-1), 32)
        backoffDuration := time.Duration(backoffSeconds) * time.Second

        s.logger.Info("Sub-agent backoff after failure",
            zap.String("agent", subAgentID),
            zap.Int("failures", failures),
            zap.Duration("backoff", backoffDuration),
            zap.Error(err))

        // Sleep with context cancellation support
        select {
        case <-time.After(backoffDuration):
            // Backoff completed, continue to next iteration
        case <-ctx.Done():
            return // Context cancelled
        }

        continue // Retry after backoff
    }

    // On success - reset counter
    if subAgentCtx.consecutiveFailures > 0 {
        s.logger.Info("Sub-agent recovered",
            zap.String("agent", subAgentID),
            zap.Int("previous_failures", subAgentCtx.consecutiveFailures))
        subAgentCtx.consecutiveFailures = 0
    }
}
```

**Backoff Progression**:

| Failure # | Backoff | Total Time | Cumulative |
|-----------|---------|------------|------------|
| 1         | 1s      | 1s         | 1s         |
| 2         | 2s      | 2s         | 3s         |
| 3         | 4s      | 4s         | 7s         |
| 4         | 8s      | 8s         | 15s        |
| 5         | 16s     | 16s        | 31s        |
| 6+        | 32s     | 32s each   | 63s, 95s...|

**Design Trade-offs**:

| Approach | Pros | Cons | Decision |
|----------|------|------|----------|
| **Immediate retry** (original) | Simple, fast recovery for transient errors | Tight loop exacerbates rate limits, wastes API quota | ❌ Rejected |
| **Fixed backoff** (e.g., 5s) | Predictable timing, simple | Slow for transient errors, fast for sustained issues | ❌ Rejected |
| **Exponential backoff** (chosen) | Fast recovery for transient, backs off for sustained | Complexity (failure tracking), longer worst-case | ✅ Chosen |
| **Jittered exponential** | Prevents thundering herd | Added complexity, not needed for small agent count | ❌ Rejected |

**Formal Properties**:

**Property 1: Exponential Growth**
```
backoff(n) = min(2^(n-1), 32) seconds
where n = consecutiveFailures
```

**Property 2: Bounded Backoff**
```
∀ n: backoff(n) ≤ 32 seconds
(Prevents indefinite blocking)
```

**Property 3: Reset on Success**
```
After successful Chat():
  consecutiveFailures := 0
  (Next failure starts from 1s again)
```

**Property 4: Context Cancellation**
```
Backoff sleep respects ctx.Done():
  select {
    case <-time.After(backoff):
    case <-ctx.Done(): return
  }
(Prevents zombie goroutines during shutdown)
```

**Performance Characteristics**:

**Best Case** (transient error):
- 1 failure → 1s delay → recovery
- Total overhead: 1s

**Typical Case** (API rate limit):
- 3-4 failures → 1s + 2s + 4s + 8s = 15s cumulative
- API quota replenishes during backoff
- Recovery likely by attempt 5

**Worst Case** (sustained outage):
- 6+ failures → capped at 32s per attempt
- Total time unbounded (relies on external recovery)

**Rationale**:
- Exponential chosen to balance fast recovery with API quota preservation
- 32s cap prevents indefinite blocking while allowing sustained retries
- Reset on success prevents penalty accumulation across unrelated failures
- Context-aware sleep enables clean shutdown


### LLM Concurrency Control

**Responsibility**: Limit concurrent LLM API calls to prevent rate limit exhaustion and deadlock.

**Problem Statement**:

Original implementation used concurrency limit of 1:

```
Coordinator acquires semaphore (1/1 slots)
  → Spawns sub-agent
  → Sub-agent tries to acquire semaphore (0/1 slots available)
  → DEADLOCK: Coordinator blocks sub-agent, sub-agent blocks coordinator
```

With limit=1, only one agent (coordinator OR sub-agent) could call LLM at a time, causing serialization and deadlock.

**Solution: Semaphore with Limit=5**:

```
┌──────────────────────────────────────────────────────────────────┐
│                LLM Concurrency Semaphore                         │
│                                                                  │
│  llmSemaphore: chan struct{} (buffered, size=5)                 │
│                                                                  │
│  ┌────────────────────────────────────────────────────┐         │
│  │ Slot 1: coordinator-agent-A                        │         │
│  ├────────────────────────────────────────────────────┤         │
│  │ Slot 2: sub-agent-weather-analyst                  │         │
│  ├────────────────────────────────────────────────────┤         │
│  │ Slot 3: sub-agent-vacation-planner                 │         │
│  ├────────────────────────────────────────────────────┤         │
│  │ Slot 4: coordinator-agent-B                        │         │
│  ├────────────────────────────────────────────────────┤         │
│  │ Slot 5: (available)                                │         │
│  └────────────────────────────────────────────────────┘         │
│                                                                  │
│  Acquire Pattern:                                               │
│    select {                                                     │
│      case s.llmSemaphore <- struct{}{}: // Acquired            │
│        defer func() { <-s.llmSemaphore }() // Release          │
│        // Make LLM call                                         │
│    }                                                            │
└──────────────────────────────────────────────────────────────────┘
```

**Implementation** (`pkg/server/multi_agent.go:48, 184`):

```go
// Server structure
type MultiAgentServer struct {
    agents              *agent.Registry
    patterns            *patterns.Library
    workflows           map[string]*Workflow
    workflowSubAgents   map[string]*workflowSubAgentContext
    messageBus          *communication.MessageBus
    messageQueue        *communication.MessageQueue

    llmSemaphore        chan struct{} // Buffered channel acts as semaphore
    llmConcurrencyLimit int

    tracer              observability.Tracer
    logger              *zap.Logger
    mu                  sync.RWMutex
}

// Initialization with limit=5
func NewMultiAgentServer(...) *MultiAgentServer {
    defaultLLMConcurrency := 5 // Increased from 1

    llmSemaphore := make(chan struct{}, defaultLLMConcurrency)

    return &MultiAgentServer{
        llmSemaphore:        llmSemaphore,
        llmConcurrencyLimit: defaultLLMConcurrency,
        // ...
    }
}

// Acquire/Release Pattern (used in sub-agent monitor goroutines and workflow execution)
// Example from fork-join executor branch goroutines:
func executeWithSemaphore(ctx context.Context, s *MultiAgentServer, ag *agent.Agent, sessionID, prompt string) {
    // Acquire semaphore slot
    s.llmSemaphore <- struct{}{}
    defer func() { <-s.llmSemaphore }() // Release on return

    // Make LLM call (holding semaphore slot)
    response, err := ag.Chat(ctx, sessionID, prompt)
    // ... handle response
}
```

**Concurrency Flow Example**:

```
Time │ Coordinator A │ Sub-Agent 1 │ Sub-Agent 2 │ Coordinator B │ Semaphore (5 slots)
─────┼───────────────┼─────────────┼─────────────┼───────────────┼─────────────────────
 0s  │ Acquire (1/5) │             │             │               │ [C-A, _, _, _, _]
 1s  │ LLM call...   │ Acquire (2/5)│             │               │ [C-A, S1, _, _, _]
 2s  │ LLM call...   │ LLM call... │ Acquire (3/5)│               │ [C-A, S1, S2, _, _]
 3s  │ LLM call...   │ LLM call... │ LLM call... │ Acquire (4/5) │ [C-A, S1, S2, C-B, _]
 4s  │ Release (3/5) │ LLM call... │ LLM call... │ LLM call...   │ [S1, S2, C-B, _, _]
 5s  │               │ Release (2/5)│ LLM call... │ LLM call...   │ [S2, C-B, _, _, _]
```

**Design Trade-offs**:

| Approach | Pros | Cons | Decision |
|----------|------|------|----------|
| **No limit** (unlimited) | Maximum parallelism, no blocking | Exhausts API rate limits, high cost burst | ❌ Rejected |
| **Limit=1** (original) | Simple, prevents rate limits | Deadlock (coordinator blocks sub-agents) | ❌ Rejected |
| **Limit=5** (chosen) | Prevents deadlock, allows parallel work, respects rate limits | May still hit rate limits with >5 concurrent workflows | ✅ Chosen |
| **Dynamic limit** (adjust based on rate limit headers) | Optimal utilization | Complex (HTTP header parsing, state management) | ❌ Deferred |

**Formal Properties**:

**Property 1: Bounded Concurrency**
```
∀ time t: concurrent_llm_calls(t) ≤ 5
(Enforced by buffered channel size)
```

**Property 2: Fairness**
```
Acquisition order = FIFO (channel blocking order)
(Go runtime channel fairness guarantees)
```

**Property 3: Deadlock Freedom**
```
Limit=5 > max(coordinators + sub-agents per workflow)
Typical: 1 coordinator + 2 sub-agents = 3 < 5
(Sufficient slots prevent deadlock)
```

**Property 4: Release Guarantee**
```
defer func() { <-s.llmSemaphore }()
ensures release even on panic or early return
(Go defer mechanism)
```

**Performance Characteristics**:

**Latency Impact**:
- Uncontended: <1ms (channel send/receive)
- Contended (6th concurrent call): Blocks until slot available
- Worst-case: Waits for slowest LLM call (5-15s for Bedrock)

**Throughput**:
- With limit=5: 5 concurrent LLM calls
- Bedrock rate limit: 10 req/s (us-east-1)
- **Headroom**: 5 < 10, leaves capacity for retries and other traffic

**Why Limit=5?**

1. **Deadlock Prevention**: 1 coordinator + 2-3 sub-agents = 3-4 < 5 (sufficient)
2. **Rate Limit Safety**: 5 < 10 (Bedrock limit), provides 50% margin
3. **Empirical**: Testing showed 5 enables parallel work without throttling
4. **Memory**: 5 slots × ~0 bytes = negligible overhead (empty struct{})

**Rationale**:
- Buffered channel chosen over sync.Semaphore for idiomatic Go pattern
- Limit=5 balances parallelism with rate limit safety
- FIFO fairness prevents starvation
- defer ensures cleanup even on panic


### Async Workflow Updates (SubscribeToSession)

**Responsibility**: Stream real-time updates for async workflow sessions where sub-agents run independently.

**Problem Statement**:

Async workflows (coordinator delegates to sub-agents) complete asynchronously:

```
User → Chat("Plan Rome vacation") → Coordinator → spawn sub-agents → return
                                                       │
                                                       ├─ weather-analyst (runs async)
                                                       └─ vacation-planner (runs async)

Sub-agents send results via send_message
User sees intermediate results... when? How?
```

Traditional blocking RPCs don't work for async patterns. User needs real-time updates as sub-agents complete.

**Solution: gRPC Server Streaming RPC**:

```
┌──────────────────────────────────────────────────────────────────┐
│           SubscribeToSession Streaming Pattern                   │
│                                                                  │
│  Client                Server              SessionStore          │
│    │                      │                      │               │
│    ├─ SubscribeToSession ▶│                      │               │
│    │  (sessionID)         │                      │               │
│    │                      ├─ Verify session ─────▶│               │
│    │                      │                      │               │
│    │◀───── stream open ───┤                      │               │
│    │                      │                      │               │
│    │                      │  ┌─ Ticker (500ms) ──┐               │
│    │                      │  │                   │               │
│    │                      ├──┤ LoadMessages() ───▶│               │
│    │                      │  │                   │               │
│    │                      │◀─┤ Messages ─────────┤               │
│    │                      │  │                   │               │
│    │                      │  │ (new messages?)   │               │
│    │                      │  └───────────────────┘               │
│    │                      │                      │               │
│    │◀─ SessionUpdate ─────┤                      │               │
│    │   (new messages)     │                      │               │
│    │                      │                      │               │
│    │     (stream remains open until client cancels)              │
│    │                      │                      │               │
│    ├─ Cancel stream ──────▶│                      │               │
│    │                      │                      │               │
└──────────────────────────────────────────────────────────────────┘
```

**Proto Definition** (`proto/loom/v1/loom.proto:119-125`):

```protobuf
// SubscribeToSession subscribes to real-time updates for a session.
// Returns a stream of updates when new messages arrive or session state changes.
rpc SubscribeToSession(SubscribeToSessionRequest) returns (stream SessionUpdate) {
    option (google.api.http) = {
        get: "/v1/sessions/{session_id}:subscribe"
    };
}

message SubscribeToSessionRequest {
    string session_id = 1; // Session to monitor
    string agent_id = 2;   // Optional: filter updates by agent
}

message SessionUpdate {
    string session_id = 1;
    string agent_id = 2;    // Agent that produced this update
    int64 timestamp = 3;     // Unix timestamp in seconds

    oneof update_type {
        NewMessageUpdate new_message = 4;
        SessionStatusUpdate status_change = 5;
    }
}
```

**Implementation** (`pkg/server/multi_agent.go:4079`):

```go
func (s *MultiAgentServer) SubscribeToSession(
    req *loomv1.SubscribeToSessionRequest,
    stream loomv1.LoomService_SubscribeToSessionServer,
) error {
    sessionID := req.SessionId
    ctx := stream.Context()

    // Verify session exists
    // ... (checks agent sessions)

    // Track the last message count we've seen
    lastMessageCount := 0

    // Poll for new messages every 500ms
    ticker := time.NewTicker(500 * time.Millisecond)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return ctx.Err() // Client cancelled

        case <-ticker.C:
            // Load all messages from session store
            messages, err := s.sessionStore.LoadMessages(ctx, sessionID)
            if err != nil {
                continue
            }

            // Send updates for new messages only
            if len(messages) > lastMessageCount {
                for i := lastMessageCount; i < len(messages); i++ {
                    msg := messages[i]
                    update := &loomv1.SessionUpdate{
                        SessionId: sessionID,
                        UpdateType: &loomv1.SessionUpdate_NewMessage{
                            NewMessage: &loomv1.NewMessageUpdate{
                                Role:    msg.Role,
                                Content: msg.Content,
                            },
                        },
                    }
                    if err := stream.Send(update); err != nil {
                        return err
                    }
                }
                lastMessageCount = len(messages)
            }
        }
    }
}
```

**Note**: The current implementation uses polling (500ms ticker) against the session store rather than event-driven listener registration. This is simpler to implement but trades optimal latency for reliability.

**Usage Example (TUI Client)**:

```go
// TUI subscribes to session updates
stream, err := client.SubscribeToSession(ctx, &loomv1.SubscribeToSessionRequest{
    SessionId: sessionID,
})

go func() {
    for {
        update, err := stream.Recv()
        if err != nil {
            return // Stream closed
        }

        switch u := update.UpdateType.(type) {
        case *loomv1.SessionUpdate_NewMessage:
            // New message from sub-agent
            fmt.Printf("New message [%s]: %s\n", u.NewMessage.Role, u.NewMessage.Content)

        case *loomv1.SessionUpdate_StatusChange:
            // Session state changed (e.g., workflow completed)
            fmt.Printf("Session state: %s - %s\n", u.StatusChange.Status, u.StatusChange.Message)
        }
    }
}()
```

**Design Trade-offs**:

| Approach | Pros | Cons | Decision |
|----------|------|------|----------|
| **Polling** (repeated GetMessages calls) | Simple, works over HTTP | High latency (poll interval), wastes bandwidth | ❌ Rejected |
| **WebSockets** | Real-time, bidirectional | Not gRPC-native, requires separate server | ❌ Rejected |
| **gRPC Server Streaming** (chosen) | Real-time, low latency, gRPC-native | Requires persistent connection | ✅ Chosen |
| **HTTP SSE** (Server-Sent Events) | Works over HTTP, browser-friendly | One-way only, not gRPC-native | ❌ Considered for web dashboard |

**Formal Properties**:

**Property 1: At-Least-Once Delivery**
```
∀ message m enqueued during active subscription:
  Client receives SessionUpdate for m
(Listener registration before first message)
```

**Property 2: Ordering Guarantee**
```
If message m1 enqueued before m2:
  Client receives update(m1) before update(m2)
(Channel FIFO order)
```

**Property 3: Stream Lifecycle**
```
Stream lifetime = min(client_cancel, server_error)
(Either party can terminate)
```

**Property 4: Poll-Based Delivery**
```
Messages are read from session store on each tick (500ms interval).
No event bus required; session store is the single source of truth.
lastMessageCount tracking ensures each message is sent exactly once.
```

**Performance Characteristics**:

**Latency**:
- Message enqueued → Client receives update: 0-500ms (polling interval)
- Components:
  - Database insert: 1-3ms
  - Poll cycle: up to 500ms
  - gRPC stream send: 3-15ms

**Throughput**:
- Updates per session: ~100/sec (limited by client processing, not server)
- Concurrent streams: 1000+ (goroutine per stream, ~2KB each)

**Resource Usage**:
- Per subscription: 1 goroutine + 1 ticker (500ms poll)
- Memory: ~2KB per active subscription
- **Scaling**: 10,000 concurrent subscriptions = 20MB + poll load on session store

**Rationale**:
- gRPC server streaming chosen for real-time updates without client polling for RPC results
- Poll-based implementation (500ms ticker) trades optimal latency for implementation simplicity
- Server-side streaming (not bidirectional) sufficient for one-way updates
- Session store used as source of truth (no separate event bus needed)

**Integration with Event-Driven Sub-Agents**:

Sub-agents use notification channels for auto-injection of messages. SubscribeToSession provides a separate mechanism for clients:

```
Sub-Agent Notification:          Client Notification:
MessageQueue.Enqueue()            SessionStore.LoadMessages()
  └─▶ NotifyChannel               └─▶ Poll (500ms ticker)
      (auto-inject via Chat())       (stream.Send new messages)
```

Sub-agents have messages automatically injected into their conversation via notification channels and `agent.Chat()`. Clients receive updates via gRPC server streaming with poll-based message detection from the session store.


## Key Interactions

### Pipeline Execution Flow

```
Client            Orchestrator     Pipeline        Stage 1        Stage 2        Stage 3
  │                    │           Executor         Agent          Agent        
  │                    │              │               │              │          
  ├─ Pipeline() ──────▶│              │               │              │          
  ├─ WithStage(1) ────▶│              │               │              │          
  ├─ WithStage(2) ────▶│              │               │              │          
  ├─ WithStage(3) ────▶│              │               │              │          
  ├─ Execute(ctx) ─────▶│              │               │              │         
  │                    ├─ Build ──────▶│               │              │         
  │                    │  pattern      │               │              │         
  │                    │              │               │              │          
  │                    │              ├─ Validate ────┤              │          
  │                    │              │  agents       │              │          
  │                    │              │  exist        │              │          
  │                    │              │               │              │          
  │                    │              ├─ Execute ─────▶│              │         
  │                    │              │  Stage 1      │              │          
  │                    │              │  (initial)    │              │          
  │                    │              │◀─ Output 1 ───┤              │          
  │                    │              │               │              │          
  │                    │              ├─ Build ───────┼──────────────┤          
  │                    │              │  prompt       │              │          
  │                    │              │  {{previous}} │              │          
  │                    │              │               │              │          
  │                    │              ├─ Execute ─────┼─────────────▶│          
  │                    │              │  Stage 2      │              │          
  │                    │              │◀─ Output 2 ───┼──────────────┤          
  │                    │              │               │              │          
  │                    │              ├─ Build ───────┼──────────────┼──────────
  │                    │              │  prompt       │              │          
  │                    │              │  {{previous}} │              │          
  │                    │              │               │              │          
  │                    │              ├─ Execute ─────┼──────────────┼──────────
  │                    │              │  Stage 3      │              │          
  │                    │              │◀─ Output 3 ───┼──────────────┼──────────
  │                    │              │               │              │          
  │                    │              ├─ Calculate ───┤              │          
  │                    │              │  cost         │              │          
  │                    │              │  (sum stages) │              │          
  │                    │◀─ Result ────┤               │              │          
  │◀─ Result ──────────┤              │               │              │          
  │  (final output:    │              │               │              │          
  │   Output 3)        │              │               │              │          
  │                    │              │               │              │          
```

**Variable Substitution**:
```
Stage 1: "Discover schema: {{previous}}"
  → Substitutes: {{previous}} = initialPrompt
  → Result: "Discover schema: Analyze sales data"

Stage 2: "Optimize query: {{previous}}"
  → Substitutes: {{previous}} = Output 1
  → Result: "Optimize query: [Stage 1 output]"

Stage 3: "Execute: {{previous}}"
  → Substitutes: {{previous}} = Output 2
  → Result: "Execute: [Stage 2 output]"
```

**Cost Attribution**:
```
Total Cost = Stage 1 Cost + Stage 2 Cost + Stage 3 Cost

Stage Cost = (input_tokens * $3/1M) + (output_tokens * $15/1M)
```


### Fork-Join Parallel Execution

```
Client         Orchestrator    ForkJoin       Agent A    Agent B    Agent C    Merger
  │                 │          Executor          │          │          │        
  ├─ Fork(prompt) ─▶│             │              │          │          │        
  ├─ WithAgents() ─▶│             │              │          │          │        
  ├─ Join(VOTING) ─▶│             │              │          │          │        
  ├─ Execute(ctx) ──▶│             │              │          │          │       
  │                 ├─ Build ─────▶│              │          │          │       
  │                 │  pattern     │              │          │          │       
  │                 │              │              │          │          │       
  │                 │              ├─ Spawn ──────┤          │          │       
  │                 │              │  goroutines  │          │          │       
  │                 │              │              │          │          │       
  │                 │              ├─ Execute ────▶│          │          │      
  │                 │              │  (parallel)   │          │          │      
  │                 │              ├───────────────┼─ Execute ▶│          │     
  │                 │              │               │          │          │      
  │                 │              ├───────────────┼──────────┼─ Execute ▶│     
  │                 │              │               │          │          │      
  │                 │              │◀─ Result A ───┤          │          │      
  │                 │              │◀─ Result B ───┼──────────┤          │      
  │                 │              │◀─ Result C ───┼──────────┼──────────┤      
  │                 │              │               │          │          │      
  │                 │              ├─ Collect ─────┤          │          │      
  │                 │              │  results      │          │          │      
  │                 │              │  (channel)    │          │          │      
  │                 │              │               │          │          │      
  │                 │              ├─ Merge ───────┼──────────┼──────────┼──────
  │                 │              │  (VOTING)     │          │          │      
  │                 │              │◀─ Merged ─────┼──────────┼──────────┼──────
  │                 │              │  output       │          │          │      
  │                 │◀─ Result ────┤               │          │          │      
  │◀─ Result ───────┤              │               │          │          │      
  │                 │              │               │          │          │      
```

**Goroutine Coordination**:
```
var wg sync.WaitGroup
resultsCh := make(chan agentResult, N)

for _, agentID := range agentIDs {
    wg.Add(1)
    go func(id string) {
        defer wg.Done()
        result := executeAgent(id, prompt)
        resultsCh <- result
    }(agentID)
}

wg.Wait()
close(resultsCh)

// Collect results
for result := range resultsCh {
    results = append(results, result)
}
```


### YAML Workflow Loading

```
User           LoadWorkflow      YAML Parser    Validator    Proto        Executor
  │                 │                 │             │        Converter       │  
  ├─ looms ─────────▶│                 │             │           │           │  
  │  workflow run   │                 │             │           │           │   
  │  pipeline.yaml  │                 │             │           │           │   
  │                 │                 │             │           │           │   
  │                 ├─ Read file ─────┤             │           │           │   
  │                 │  (check perms)  │             │           │           │   
  │                 │◀─ YAML bytes ───┤             │           │           │   
  │                 │                 │             │           │           │   
  │                 ├─ Parse YAML ────▶│             │           │           │  
  │                 │◀─ Config ────────┤             │           │           │  
  │                 │  struct          │             │           │           │  
  │                 │                 │             │           │           │   
  │                 ├─ Validate ───────┼────────────▶│           │           │  
  │                 │  apiVersion      │             │           │           │  
  │                 │  kind            │             │           │           │  
  │                 │  spec.type       │             │           │           │  
  │                 │◀─ Valid ─────────┼─────────────┤           │           │  
  │                 │                 │             │           │           │   
  │                 ├─ Convert to ─────┼─────────────┼──────────▶│           │  
  │                 │  proto           │             │           │           │  
  │                 │◀─ Pattern ───────┼─────────────┼───────────┤           │  
  │                 │  proto           │             │           │           │  
  │                 │                 │             │           │           │   
  │                 ├─ Execute ────────┼─────────────┼───────────┼──────────▶│  
  │                 │  pattern         │             │           │           │  
  │                 │◀─ Result ────────┼─────────────┼───────────┼───────────┤  
  │◀─ Result ───────┤                 │             │           │           │   
  │                 │                 │             │           │           │   
```

**Validation Errors**:
```yaml
# Missing apiVersion
kind: Workflow
→ Error: ErrInvalidWorkflow: missing apiVersion

# Invalid pattern type
spec:
  type: invalid_pattern
→ Error: ErrUnsupportedPattern: 'invalid_pattern', must be one of: [debate, fork-join, pipeline, parallel, conditional, iterative, swarm]

# Missing required field
spec:
  type: pipeline
  # Missing initial_prompt
→ Error: ErrInvalidWorkflow: pipeline pattern requires 'initial_prompt' field
```


## Data Structures

### WorkflowPattern Proto

**Definition** (`proto/loom/v1/orchestration.proto:17`):
```protobuf
message WorkflowPattern {
  oneof pattern {
    DebatePattern debate = 1;
    ForkJoinPattern fork_join = 2;
    PipelinePattern pipeline = 3;
    ParallelPattern parallel = 4;
    ConditionalPattern conditional = 5;
    SwarmPattern swarm = 6;
    PairProgrammingPattern pair_programming = 7;
    TeacherStudentPattern teacher_student = 8;
    IterativeWorkflowPattern iterative = 9;
  }

  // Stable workflow ID for session determinism across scheduled executions.
  // If set, executors use this instead of generating a random UUID.
  string workflow_id = 10;
}

message PipelinePattern {
  string initial_prompt = 1;
  repeated PipelineStage stages = 2;
  bool pass_full_history = 3;
}

message PipelineStage {
  string agent_id = 1;
  string prompt_template = 2;
  string validation_prompt = 3;
}

message ForkJoinPattern {
  string prompt = 1;
  repeated string agent_ids = 2;
  MergeStrategy merge_strategy = 3;
  int32 timeout_seconds = 4;
}

message DebatePattern {
  string topic = 1;
  repeated string agent_ids = 2;
  int32 rounds = 3;
  MergeStrategy merge_strategy = 4;
  string moderator_agent_id = 5;
}
```

**Merge Strategies**:
```protobuf
enum MergeStrategy {
  MERGE_STRATEGY_UNSPECIFIED = 0;
  CONSENSUS = 1;        // LLM synthesizes agreement
  VOTING = 2;           // Majority wins (requires odd number of agents)
  CONCATENATE = 3;      // Simple concatenation (preserves all outputs)
  FIRST = 4;            // First result only
  BEST = 5;             // LLM selects highest quality result
  SUMMARY = 6;          // LLM summarizes all results
}
```


### WorkflowResult

**Definition** (`proto/loom/v1/orchestration.proto`):
```protobuf
message WorkflowResult {
  string pattern_type = 1;                  // "pipeline", "fork_join", etc.
  repeated AgentResult agent_results = 2;   // All agent executions
  string merged_output = 3;                 // Final merged result
  map<string, string> metadata = 4;         // Pattern-specific metadata
  int64 duration_ms = 5;                    // Total execution time
  string trace_id = 6;                      // Trace ID for observability
  WorkflowCost cost = 7;                    // Cost per agent
  // Fields 8-11: collaboration-specific results (oneof)
  CollaborationMetrics metrics = 12;        // Collaboration quality metrics
  map<string, string> models_used = 13;     // agent_id → model
}

message WorkflowCost {
  double total_cost_usd = 1;
  map<string, double> agent_costs_usd = 2;  // Per-agent cost breakdown
  int32 total_tokens = 3;
  int32 llm_calls = 4;
}
```


### WorkflowConfig YAML

**Definition** (`pkg/orchestration/workflow_config.go:29`):
```go
type WorkflowConfig struct {
    APIVersion string                 `yaml:"apiVersion"`
    Kind       string                 `yaml:"kind"`
    Metadata   WorkflowMetadata       `yaml:"metadata"`
    Spec       map[string]interface{} `yaml:"spec"`
    Schedule   *ScheduleYAML          `yaml:"schedule,omitempty"`
}

type WorkflowMetadata struct {
    Name        string            `yaml:"name"`
    Version     string            `yaml:"version,omitempty"`
    Description string            `yaml:"description,omitempty"`
    Labels      map[string]string `yaml:"labels,omitempty"`
}
```


## Algorithms

### Pattern Routing

**Problem**: Route workflow pattern to appropriate executor (orchestration vs collaboration).

**Solution**: Type-based routing with fallback.

**Algorithm**:
```go
func (o *Orchestrator) ExecutePattern(ctx context.Context, pattern *loomv1.WorkflowPattern) (*loomv1.WorkflowResult, error) {
    // 1. Check if collaboration pattern (debate, pair programming, teacher-student)
    switch pattern.Pattern.(type) {
    case *loomv1.WorkflowPattern_Debate,
         *loomv1.WorkflowPattern_PairProgramming,
         *loomv1.WorkflowPattern_TeacherStudent:
        return o.collaborationEngine.Execute(ctx, pattern)
    }

    // 2. Route to orchestration executor (all take workflowID for session determinism)
    switch p := pattern.Pattern.(type) {
    case *loomv1.WorkflowPattern_Pipeline:
        wfID := o.resolveWorkflowID("pipeline", pattern)
        executor := NewPipelineExecutor(o, p.Pipeline, wfID)
        return executor.Execute(ctx)
    case *loomv1.WorkflowPattern_ForkJoin:
        wfID := o.resolveWorkflowID("fork-join", pattern)
        executor := NewForkJoinExecutor(o, p.ForkJoin, wfID)
        return executor.Execute(ctx)
    case *loomv1.WorkflowPattern_Parallel:
        wfID := o.resolveWorkflowID("parallel", pattern)
        executor := NewParallelExecutor(o, p.Parallel, wfID)
        return executor.Execute(ctx)
    case *loomv1.WorkflowPattern_Conditional:
        wfID := o.resolveWorkflowID("conditional", pattern)
        executor := NewConditionalExecutor(o, p.Conditional, wfID)
        return executor.Execute(ctx)
    case *loomv1.WorkflowPattern_Iterative:
        wfID := o.resolveWorkflowID("iterative", pattern)
        executor := NewIterativePipelineExecutor(o, p.Iterative, o.messageBus, wfID)
        return executor.Execute(ctx)
    case *loomv1.WorkflowPattern_Swarm:
        wfID := o.resolveWorkflowID("swarm", pattern)
        executor := NewSwarmExecutor(o, p.Swarm, wfID)
        return executor.Execute(ctx)
    default:
        return nil, fmt.Errorf("unknown workflow pattern type")
    }
}
```

**Complexity**: O(1) type switch

**Rationale**: Separate collaboration engine for patterns requiring specialized coordination logic (debate rounds, pair programming turn-taking, teacher-student knowledge transfer). Swarm is handled as an orchestration executor in `pkg/orchestration/swarm_executor.go`.


### Merge Strategy Selection

**Problem**: Combine multiple agent outputs into single result using specified strategy.

**Solution**: Strategy pattern with LLM-powered merging for complex strategies.

**Algorithm**:
```go
func (e *ForkJoinExecutor) mergeResults(ctx context.Context, workflowID string, results []*loomv1.AgentResult) (string, error) {
    switch e.pattern.MergeStrategy {
    case loomv1.MergeStrategy_CONCATENATE:
        // Concatenation with agent ID headers
        var builder strings.Builder
        for i, result := range results {
            builder.WriteString(fmt.Sprintf("=== Agent %s ===\n", result.AgentId))
            builder.WriteString(result.Output)
            if i < len(results)-1 { builder.WriteString("\n\n") }
        }
        return builder.String(), nil

    case loomv1.MergeStrategy_FIRST:
        // Return first result
        return results[0].Output, nil

    case loomv1.MergeStrategy_CONSENSUS, loomv1.MergeStrategy_SUMMARY,
         loomv1.MergeStrategy_VOTING, loomv1.MergeStrategy_BEST:
        // These strategies require LLM-based merging via GetMergeLLM()
        // Each builds a strategy-specific prompt and makes a single LLM call
        return e.llmMerge(ctx, workflowID, results)

    default:
        return "", fmt.Errorf("unsupported merge strategy: %s", e.pattern.MergeStrategy)
    }
}
```

**Complexity**:
- CONCATENATE, FIRST: O(1)
- VOTING, CONSENSUS, SUMMARY, BEST: O(1) LLM call each (cost: ~$0.001-0.003)

**Voting Algorithm** (LLM-based selection):

The actual implementation uses a single LLM call to select the most compelling answer, not pairwise similarity comparison:
```go
func (e *ForkJoinExecutor) buildVotingPrompt(results []*loomv1.AgentResult) string {
    var builder strings.Builder
    builder.WriteString(fmt.Sprintf("Original prompt: %s\n\n", e.pattern.Prompt))
    builder.WriteString("Review these responses and select the most compelling answer:\n\n")
    for i, result := range results {
        builder.WriteString(fmt.Sprintf("Option %d:\n%s\n\n", i+1, result.Output))
    }
    builder.WriteString("Identify which option is most convincing and explain why.")
    return builder.String()
}

// All LLM-based strategies (VOTING, CONSENSUS, SUMMARY, BEST) use the same
// pattern: build a strategy-specific prompt, call GetMergeLLM().Chat() once.
```


### Template Variable Substitution

**Problem**: Substitute placeholders in prompt templates with actual values from workflow context.

**Solution**: Simple `strings.ReplaceAll` substitution (not `text/template`). This avoids the complexity and escaping pitfalls of Go templates.

**Algorithm**:
```go
func (e *PipelineExecutor) buildStagePrompt(stage *loomv1.PipelineStage, previousOutput string, allOutputs []string) string {
    prompt := stage.PromptTemplate

    // Replace {{previous}} with the previous stage's output
    if strings.Contains(prompt, "{{previous}}") {
        prompt = strings.ReplaceAll(prompt, "{{previous}}", previousOutput)
    }

    // Replace {{history}} with all previous outputs
    if strings.Contains(prompt, "{{history}}") {
        history := e.buildHistoryString(allOutputs)
        prompt = strings.ReplaceAll(prompt, "{{history}}", history)
    }

    // Replace {{structured_context}} with JSON context
    if strings.Contains(prompt, "{{structured_context}}") {
        if structuredCtx != nil {
            contextJSON, _ := structuredCtx.ToJSON()
            prompt = strings.ReplaceAll(prompt, "{{structured_context}}", contextJSON)
        }
    }

    // If pass_full_history is enabled and no placeholders used, append history
    if e.pattern.PassFullHistory && len(allOutputs) > 0 {
        if !strings.Contains(stage.PromptTemplate, "{{previous}}") &&
           !strings.Contains(stage.PromptTemplate, "{{history}}") {
            prompt = fmt.Sprintf("%s\n\nPrevious stages:\n%s", prompt, history)
        }
    }

    return prompt
}
```

**Supported Placeholders** (plain string substitution, not Go template syntax):
- `{{previous}}`: Previous stage output (for stage 1, this is the initial prompt)
- `{{history}}`: All previous outputs concatenated
- `{{structured_context}}`: JSON-serialized StructuredContext

There is no `{{initial}}` placeholder. The initial prompt is passed as the first stage's `previousOutput`.

**Example**:
```
Template: "Optimize query: {{previous}}"
Previous output: "SELECT * FROM customers WHERE age > 30"
Result: "Optimize query: SELECT * FROM customers WHERE age > 30"
```

**Complexity**: O(N) where N = template length


## Design Trade-offs

### Decision 1: Fluent API vs YAML-Only Configuration

**Chosen**: Both fluent API (programmatic) and YAML (declarative)

**Rationale**:
- **Fluent API**: Type-safe, compile-time validation, IDE autocomplete
- **YAML**: Version control, non-programmers can define workflows, Kubernetes-familiar
- **Both**: Serve different use cases (embedded vs CLI)

**Alternatives**:
1. **Fluent API only**:
   - ✅ Type safety, compile-time validation
   - ❌ No version control for workflows
   - ❌ Requires programming knowledge

2. **YAML only**:
   - ✅ Declarative, version control
   - ❌ No compile-time validation
   - ❌ Harder to embed in Go applications

**Consequences**:
- ✅ Best of both worlds (type safety + declarative)
- ✅ Two user personas (developers + operators)
- ❌ More code to maintain (two interfaces to same functionality)
- ❌ YAML validation errors are runtime, not compile-time


### Decision 2: Goroutines vs Worker Pool for Fork-Join

**Chosen**: Goroutine per agent (no worker pool)

**Rationale**:
- **Agent count is typically small** (3-10 agents per fork-join)
- **Goroutines are cheap** (~2KB stack per goroutine)
- **Simplicity**: No need for complex worker pool management
- **Isolation**: Each agent gets own goroutine (crash doesn't affect others)

**Alternatives**:
1. **Worker pool** (fixed N workers):
   - ✅ Bounded resource usage
   - ❌ Complexity (task queue, worker management)
   - ❌ Overhead not justified for small N

2. **Sequential execution** (no parallelism):
   - ✅ Simplest implementation
   - ❌ Slow (N × agent_latency)
   - ❌ Defeats purpose of fork-join pattern

**Consequences**:
- ✅ Simple, readable code (spawn N goroutines)
- ✅ Fast parallel execution
- ❌ Memory usage scales with agent count (N × 2KB)
- ❌ No backpressure (could spawn 100+ goroutines if N=100)

**Mitigation**: Add optional MaxConcurrency config if needed in future.


### Decision 3: Single-Process Multi-Agent vs Distributed Orchestration

**Chosen**: Single-process multi-agent server (no distributed orchestration)

**Rationale**:
- **Zero network overhead**: All agents in same process (in-memory communication)
- **Simplicity**: No distributed coordination, no service mesh
- **Cost efficiency**: Single server runs 10+ agents easily
- **Observability**: Hawk tracing captures full workflow (no distributed tracing needed)

**Alternatives**:
1. **Distributed agents** (1 agent per process):
   - ✅ Fault isolation (1 agent crash doesn't kill others)
   - ✅ Scale horizontally (add more servers)
   - ❌ Network overhead (gRPC calls between agents)
   - ❌ Distributed tracing complexity
   - ❌ Service discovery, load balancing

2. **Kubernetes-native orchestration** (Argo Workflows, Tekton):
   - ✅ Cloud-native, horizontal scaling
   - ❌ Overkill for LLM agent workflows (agents are stateless, ephemeral)
   - ❌ Requires Kubernetes cluster

**Consequences**:
- ✅ Fast, efficient single-process execution
- ✅ Simple deployment (one binary)
- ✅ Low latency (in-memory agent-to-agent communication)
- ❌ Vertical scaling only (add more CPU/memory to server)
- ❌ No fault isolation (server crash kills all agents)

**Note**: For distributed deployments, use external orchestrator (Argo, Temporal) with Loom as worker.


### Decision 4: Synchronous vs Asynchronous Workflow Execution

**Chosen**: Synchronous execution (blocking until workflow completes)

**Rationale**:
- **User experience**: Users want immediate results (CLI blocks until done)
- **Simplicity**: No job queue, no polling for status
- **Observability**: Single trace captures entire workflow

**Alternatives**:
1. **Asynchronous execution** (return job ID, poll for completion):
   - ✅ Non-blocking (user can do other work)
   - ✅ Long-running workflows don't time out
   - ❌ Complexity (job queue, status polling, result storage)
   - ❌ Worse UX (must poll for results)

2. **Streaming execution** (stream results as they arrive):
   - ✅ Real-time feedback (see each agent's output immediately)
   - ✅ Good UX for long workflows
   - ❌ Complexity (streaming gRPC, HTTP SSE)
   - ❌ Client must handle partial results

**Consequences**:
- ✅ Simple, blocking execution model
- ✅ Good UX for short workflows (<5 min)
- ❌ Timeouts for long workflows (>10 min)
- ❌ No real-time feedback during execution

**Mitigation**: Add streaming support for iterative workflows in future (using gRPC server streaming).


## Constraints and Limitations

### Constraint 1: Single-Process Only

**Description**: All agents must run in same process (no distributed orchestration).

**Impact**: Vertical scaling only (cannot add more servers for horizontal scaling).

**Workaround**: Use external orchestrator (Argo Workflows, Temporal) with Loom as worker for distributed deployments.


### Constraint 2: No Long-Running Workflow Persistence

**Description**: Workflows are ephemeral (not persisted across server restarts).

**Rationale**: Simplicity (no database for workflow state).

**Impact**: Long-running workflows (>1 hour) may fail on server restart.

**Workaround**: Use external orchestrator with checkpoint/resume support.


### Constraint 3: YAML Validation Errors Are Runtime

**Description**: YAML syntax/structure errors detected at load time, not development time.

**Impact**: Typos in YAML discovered when workflow executes, not when editing.

**Workaround**: Use `looms workflow validate <file>` CLI command to validate YAML before execution.


### Constraint 4: Merge Strategies Require LLM Provider

**Description**: CONSENSUS, SUMMARY, BEST merge strategies require LLM provider for orchestrator.

**Impact**: Fork-join with these strategies fails if orchestrator has no LLM provider.

**Workaround**: Use CONCATENATE or FIRST merge strategy (no LLM required), or provide LLM provider to orchestrator.


## Performance Characteristics

### Latency (P50/P99)

| Operation | P50 | P99 | Notes |
|-----------|-----|-----|-------|
| Pipeline (3 stages) | 12s | 18s | Sequential agent execution (4s per agent × 3) |
| Fork-Join (3 agents) | 5s | 8s | Parallel execution (max agent latency) |
| Parallel (3 tasks) | 5s | 8s | True parallel (no merge) |
| Conditional (1 branch) | 8s | 12s | Classifier + branch execution |
| Debate (3 agents, 2 rounds) | 24s | 36s | Sequential rounds (2 × 3 agents × 4s) |
| YAML load + validate | 15ms | 35ms | File I/O + YAML parse + validation |
| Pattern routing | <1ms | 2ms | Type switch |
| Merge (CONCATENATE) | <1ms | 5ms | String concatenation |
| Merge (CONSENSUS) | 3s | 6s | LLM call for synthesis |

### Memory Usage

| Component | Size |
|-----------|------|
| Orchestrator | ~50KB (agent registry, config) |
| Pipeline executor (3 stages) | ~30KB (stage outputs buffered) |
| Fork-join executor (3 agents) | ~20KB (3 goroutines × 2KB + result channel) |
| YAML config (loaded) | ~10KB per workflow |
| **Total per workflow** | **~100KB** |

### Throughput

- **Sequential workflows**: 1 workflow every (N × agent_latency) seconds
- **Parallel workflows**: 1 workflow every max(agent_latency) seconds
- **Server capacity**: 100+ concurrent workflows (limited by agent LLM rate limits, not orchestrator)

**Bottleneck**: LLM API rate limits (not orchestrator CPU/memory).


## Concurrency Model

### Orchestrator

**Model**: RWMutex for agent registry (concurrent agent lookups, exclusive writes).

**Readers**: GetAgent (concurrent)
**Writers**: RegisterAgent (exclusive)

**Rationale**: Read-heavy workload (many GetAgent calls during workflow execution, few RegisterAgent calls).


### Fork-Join Executor

**Model**: Goroutine per agent + buffered channel for results.

**Synchronization**:
```go
var wg sync.WaitGroup
resultsCh := make(chan agentResult, N)

for _, agentID := range agentIDs {
    wg.Add(1)
    go func(id string) {
        defer wg.Done()
        result := executeAgent(id, prompt)
        resultsCh <- result  // Thread-safe channel send
    }(agentID)
}

wg.Wait()          // Wait for all goroutines
close(resultsCh)   // Close channel after all sends
```

**Race Prevention**: Zero race conditions (all tests run with `-race` detector).


### Pipeline Executor

**Model**: Sequential execution (no concurrency within pipeline, but different pipelines can run concurrently).

**Thread Safety**: Each executor instance is independent (no shared state).


## Error Handling

### Strategy

1. **Fail-Fast Validation**: Validate all agents exist before workflow starts (prevents partial execution)
2. **Stage-Level Errors**: Pipeline stage errors abort workflow (no partial results)
3. **Graceful Degradation**: Optional validation failures log warning but continue (configurable)
4. **Wrapped Errors**: Error context includes stage number, agent ID for debugging

### Error Propagation

```
Agent Execution Error ───▶ Stage Error ───▶ Workflow Error ───▶ Client Error    
     │                        │                   │                    │        
     ├─ LLM API timeout       ├─ "stage 2 failed" ├─ "pipeline failed" ├─ HTTP 5
     ├─ Agent not found       └─ Span status: ERROR └─ Log + Trace     └─ Error 
     └─ Generation error                                │                       
                                                         ▼
                                                   Hawk trace with error
```

**Validation Errors** (YAML load):
```
ErrFileNotFound       → HTTP 404
ErrInvalidYAML        → HTTP 400 (syntax error)
ErrUnsupportedPattern → HTTP 400 (invalid spec.type)
ErrInvalidWorkflow    → HTTP 400 (missing required fields)
```

**Execution Errors**:
```
Agent Not Found       → Fail fast (before execution starts)
LLM API Error         → Retry 3 times (exponential backoff), then fail
Validation Failed     → Log warning, continue (if optional)
                      → Abort workflow (if required)
```


## Security Considerations

### Threat Model

1. **Malicious YAML Injection**: Attacker provides YAML with code execution
2. **Resource Exhaustion**: Attacker creates workflow with 1000+ agents
3. **Sensitive Data Leakage**: Workflow outputs contain PII/credentials

### Mitigations

**YAML Injection**:
- ✅ YAML parser has no code execution (safe library: gopkg.in/yaml.v3)
- ✅ Proto conversion validates all fields (no arbitrary data injection)
- ✅ Agent IDs validated against registry (no arbitrary agent execution)

**Resource Exhaustion**:
- ✅ Agent count limit per workflow (default: 20 agents)
- ✅ Stage count limit per pipeline (default: 10 stages)
- ✅ Debate rounds limit (default: 5 rounds)
- ⚠️ TODO: Add workflow timeout (abort after N minutes)

**Sensitive Data Leakage**:
- ✅ Hawk PII redaction (email, phone, SSN, credit card patterns removed)
- ✅ Workflow results can be filtered before returning to client
- ❌ No built-in secrets management (user must handle credentials)

**Recommendations**:
1. Validate YAML files from untrusted sources
2. Set resource limits in orchestrator config
3. Use Hawk PII redaction for all workflows
4. Store credentials in secure keyring, not in YAML


## Related Work

### Workflow Orchestration Systems

1. **Argo Workflows** (Kubernetes-native):
   - **Similar**: YAML-based workflow definition, DAG execution
   - **Loom differs**: Single-process (not Kubernetes), LLM-agent-specific patterns

2. **Temporal** (Distributed workflows):
   - **Similar**: Durable execution, retry logic, workflow versioning
   - **Loom differs**: Ephemeral workflows, no persistence, simpler deployment

3. **Apache Airflow** (Data pipelines):
   - **Similar**: DAG-based workflow orchestration
   - **Loom differs**: LLM agents (not data tasks), real-time execution (not scheduled)

### Multi-Agent Systems

1. **LangGraph** (LangChain):
   - **Similar**: Multi-agent workflows, conditional routing
   - **Loom differs**: Proto-first, observability-native, tri-modal communication

2. **AutoGen** (Microsoft):
   - **Similar**: Multi-agent debate, group chat patterns
   - **Loom differs**: Kubernetes-style YAML, pluggable backends, Hawk integration


## References

1. Kubernetes API Conventions. Resource Types (apiVersion, kind, metadata, spec). https://kubernetes.io/docs/reference/using-api/api-concepts/

2. Hohpe, G., & Woolf, B. (2003). *Enterprise Integration Patterns*. Addison-Wesley. (Fork-Join, Pipeline patterns)

3. OpenAI. (2023). *AutoGen: Enabling Next-Gen LLM Applications*. Microsoft Research. (Multi-agent debate patterns)


## Further Reading

### Architecture Deep Dives

- [Communication System Architecture](communication-system-design.md) - Tri-modal communication (broadcast, queue, shared memory)
- [Agent System Architecture](agent-system-design.md) - Agent runtime and conversation loop
- [Pattern System Architecture](pattern-system.md) - Pattern hot-reload and selection
- [Observability Architecture](observability.md) - Hawk tracing integration
- [Loom System Architecture](loom-system-architecture.md) - Overall system design

### Reference Documentation

- [CLI Reference](../reference/cli.md) - `looms workflow` commands

### Guides

- [Getting Started](../guides/quickstart.md) - Quick start guide

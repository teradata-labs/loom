
# Judge System Architecture

Multi-judge LLM evaluation system that coordinates multiple judges with different criteria to evaluate agent outputs. Supports 4 core scores (factual accuracy, hallucination, query quality, completeness), 6 evaluation dimensions, 3 execution modes, and 6 aggregation strategies with retry/circuit breaker patterns.

**Target Audience**: Architects, academics, and advanced developers

**Version**: v1.2.0


## Table of Contents

- [Overview](#overview)
- [Design Goals](#design-goals)
- [System Context](#system-context)
- [Architecture Overview](#architecture-overview)
- [Components](#components)
  - [Judge Service](#judge-service)
  - [Judge Interface](#judge-interface)
  - [LLM Judge](#llm-judge)
  - [Agent Judge](#agent-judge)
  - [Custom Judge](#custom-judge)
  - [Multi-Judge Orchestrator](#multi-judge-orchestrator)
  - [Aggregator](#aggregation-engine)
  - [Retry & Circuit Breaker](#retry--circuit-breaker)
- [Key Interactions](#key-interactions)
  - [Synchronous Evaluation Flow](#synchronous-evaluation-flow)
  - [Asynchronous Evaluation Flow](#asynchronous-evaluation-flow)
  - [Streaming Evaluation Flow](#streaming-evaluation-flow)
  - [Retry with Circuit Breaker Flow](#retry-with-circuit-breaker-flow)
- [Data Structures](#data-structures)
- [Algorithms](#algorithms)
  - [Weighted Average Aggregation](#weighted-average-aggregation)
  - [Majority Pass Aggregation](#majority-pass-aggregation)
  - [Exponential Backoff Retry](#exponential-backoff-retry)
  - [Circuit Breaker State Machine](#circuit-breaker-state-machine)
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

The Judge System evaluates **agent outputs across multiple dimensions** using a coordinated multi-judge architecture:

**Evaluation Inputs**:
- Agent response (text, SQL, JSON, etc.)
- Context (prompt, pattern used, tools invoked)
- Performance metrics (cost, latency, trace ID)

**Evaluation Outputs**:
- **4 Core Scores**: Factual Accuracy, Hallucination Score, Query Quality, Completeness (0-100)
- **6 Dimension Scores**: Quality, Cost, Safety, Domain, Performance, Usability (0-100)
- **Custom Dimension Scores**: User-defined dimensions (e.g., "teradata_compliance", "hipaa_compliance")
- **Verdict**: PASS, FAIL, PARTIAL
- **Reasoning**: Detailed explanation and suggestions

**Key Innovation**: Multi-judge coordination with pluggable judge types (LLM, Agent, Custom), 6 aggregation strategies, 3 execution modes (sync/async/hybrid), and retry with circuit breaker.


## Design Goals

1. **Multi-Dimensional Evaluation**: Assess agent outputs across quality, cost, safety, domain, performance, usability dimensions
2. **Pluggable Judges**: Support LLM-based judges, Loom agents as judges, and custom implementations
3. **Flexible Aggregation**: 6 strategies (weighted average, all-must-pass, majority-pass, any-pass, min-score, max-score)
4. **Execution Modes**: Synchronous (blocking), asynchronous (background), hybrid (critical sync, non-critical async)
5. **Criticality Levels**: Non-critical, critical, safety-critical judges with different priorities
6. **Fault Tolerance**: Exponential backoff retry, circuit breaker, timeout handling
7. **Streaming Progress**: Real-time updates for long-running evaluations (MIPRO, BootstrapFewShot)
8. **Hawk Integration**: Native export to Hawk for trace correlation and historical analysis

**Non-goals**:
- Real-time evaluation UI (use Hawk's dashboard for visualization)
- Judge-to-judge communication (judges evaluate independently)
- Distributed judge execution across processes (single-process multi-goroutine only)


## System Context

```mermaid
graph TB
    subgraph External["External Environment"]
        Agent[Agent<br/>evaluated]
        Evaluation[Evaluation<br/>suite]
        HawkService[Hawk Service<br/>trace export]
        LLMProviders[LLM Providers<br/>judge models]
    end

    subgraph JudgeSystem["Judge System"]
        subgraph JudgeService["JudgeService (gRPC)"]
            JS1[EvaluateWithJudges]
            JS2[EvaluateWithJudgesStream]
            JS3[RegisterJudge]
            JS4[GetJudgeHistory]
        end

        subgraph MultiJudgeOrchestrator["Multi-Judge Orchestrator"]
            MJC1[Judge selection and orchestration]
            MJC2[Parallel execution (Fork-Join)]
            MJC3[Result aggregation]
        end

        subgraph Judges["Judges"]
            LLMJudge[LLM Judge<br/>LLM-as-a-judge ✅]
            AgentJudge[Agent Judge<br/>placeholder ⚠️]
            CustomJudge[Custom Judge<br/>interface only 📋]
        end

        subgraph Aggregator["Aggregator"]
            AE1[Weighted average majority-pass min/max]
            AE2[Dimension score calculation]
        end

        subgraph RetryCircuitBreaker["Retry & Circuit Breaker"]
            RCB1[Exponential backoff max 3 attempts]
            RCB2[Circuit breaker 5 failures 1 min cooldown]
        end
    end

    Agent --> JudgeSystem
    Evaluation --> JudgeSystem
    HawkService --> JudgeSystem
    LLMProviders --> JudgeSystem
```

**External Dependencies**:
- **Hawk Service**: Trace export, historical evaluation storage
- **LLM Providers**: Judge model execution (Anthropic, OpenAI, etc.)
- **Agent Runtime**: Agent-as-judge execution
- **Evaluation Suite**: Test case management


## Architecture Overview

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                    Judge System                                              │
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │                  JudgeService (gRPC)                         │         │  │
│  │                                                              │         │  │
│  │  EvaluateWithJudges(ctx, req) → EvaluateResponse            │          │  │
│  │  EvaluateWithJudgesStream(ctx, req) → stream Progress       │          │  │
│  │  RegisterJudge(ctx, config) → JudgeID                       │          │  │
│  │  GetJudgeHistory(ctx, filter) → []HistoricalEvaluation      │          │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                            ▲                                                 │
│                            │ implements                                      │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│            │               │               │                  │              │
│            ▼               ▼               ▼                  ▼              │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │  Multi-Judge  │  │  Aggregation │  │  Retry   │  │  History   │        │  │
│  │  Orchestrator │  │  Aggregator  │  │  Retry   │  │  Store     │        │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│          │                 │                 │                               │
│          ▼                 ▼                 ▼                               │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │           Judge Execution Flow                               │         │  │
│  │                                                              │         │  │
│  │  1. Judge Selection                                         │          │  │
│  │     ├─ Parse judge_ids from request                         │          │  │
│  │     ├─ Load JudgeConfig from registry                       │          │  │
│  │     ├─ Validate criticality and execution mode              │          │  │
│  │     └─ Sort by criticality (safety-critical → critical)     │          │  │
│  │                                                              │         │  │
│  │  2. Execution Orchestration                                 │          │  │
│  │     ├─ SYNCHRONOUS: Parallel execution, block until all complete  │     │  │
│  │     ├─ ASYNCHRONOUS: Goroutines, background execution       │          │  │
│  │     └─ HYBRID: Critical sync, non-critical async            │          │  │
│  │                                                              │         │  │
│  │  3. Judge Invocation (per judge)                            │          │  │
│  │     ├─ Build EvaluationContext                              │          │  │
│  │     ├─ Invoke Judge.Evaluate(ctx, context)                  │          │  │
│  │     ├─ Retry with exponential backoff (max 3 attempts)      │          │  │
│  │     ├─ Circuit breaker check (5 failures → open)            │          │  │
│  │     └─ Collect JudgeResult                                  │          │  │
│  │                                                              │         │  │
│  │  4. Result Aggregation                                      │          │  │
│  │     ├─ Calculate weighted average score                     │          │  │
│  │     ├─ Apply aggregation strategy                           │          │  │
│  │     ├─ Compute dimension scores (quality, cost, safety)     │          │  │
│  │     ├─ Determine overall verdict (PASS/FAIL/PARTIAL)        │          │  │
│  │     └─ Collect suggestions and issues                       │          │  │
│  │                                                              │         │  │
│  │  5. Export & Response                                       │          │  │
│  │     ├─ Export to Hawk (if enabled)                          │          │  │
│  │     ├─ Store in history (SQLite)                            │          │  │
│  │     └─ Return EvaluateResponse                              │          │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │           Judge Interface (Pluggable)                        │         │  │
│  │                                                              │         │  │
│  │  type Judge interface {                                     │          │  │
│  │      Evaluate(ctx, context) → JudgeResult                   │          │  │
│  │  }                                                          │          │  │
│  │                                                              │         │  │
│  │  Implementations:                                           │          │  │
│  │    ├─ LLMJudge: Uses LLM-as-a-judge via llmjudge.Judge     │           │  │
│  │    ├─ AgentJudge: Wraps Loom agent as judge                │           │  │
│  │    └─ CustomJudge: User-provided implementation            │           │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │                  Judge Registry (Thread-Safe)                │         │  │
│  │                                                              │         │  │
│  │  mu sync.RWMutex                                            │          │  │
│  │  judges map[string]Judge                                    │          │  │
│  │                                                              │         │  │
│  │  Methods:                                                   │          │  │
│  │    Register(judge) → error                                  │          │  │
│  │    Get(judgeID) → (Judge, error)                            │          │  │
│  │    List() → []*JudgeInfo                                    │          │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────────┘
```


## Components

### Judge Service

**Responsibility**: gRPC service endpoint for multi-judge evaluation.

**Core Interface** (`proto/loom/v1/judge.proto:17`):
```protobuf
service JudgeService {
  // EvaluateWithJudges runs multiple judges against an agent output
  rpc EvaluateWithJudges(EvaluateRequest) returns (EvaluateResponse);

  // EvaluateWithJudgesStream runs multiple judges with streaming progress updates
  rpc EvaluateWithJudgesStream(EvaluateRequest) returns (stream EvaluateProgress);

  // RegisterJudge registers a new judge configuration
  rpc RegisterJudge(RegisterJudgeRequest) returns (RegisterJudgeResponse);

  // GetJudgeHistory retrieves historical judge evaluations
  rpc GetJudgeHistory(GetJudgeHistoryRequest) returns (GetJudgeHistoryResponse);
}
```

**Request Structure** (`proto/loom/v1/judge.proto:33`):
```protobuf
message EvaluateRequest {
  EvaluationContext context = 1;        // Agent output and context
  repeated string judge_ids = 2;        // Judges to run
  AggregationStrategy aggregation = 3;  // How to combine verdicts
  ExecutionMode execution_mode = 4;     // Sync/async/hybrid
  bool export_to_hawk = 5;              // Send results to Hawk
  int32 timeout_seconds = 6;            // Max evaluation time
  bool fail_fast = 7;                   // Abort on first critical failure
}
```

**EvaluationContext** (`proto/loom/v1/judge.proto:57`):
```protobuf
message EvaluationContext {
  string agent_id = 1;              // Agent that generated output
  string session_id = 2;            // Session ID for tracing
  string prompt = 3;                // User input
  string response = 4;              // Agent response
  string pattern_used = 5;          // Pattern matched (if any)
  repeated string tools_used = 6;   // Tools invoked by agent
  double cost_usd = 7;              // LLM cost
  int64 latency_ms = 8;             // Response latency
  string trace_id = 9;              // Observability trace ID
  map<string, string> metadata = 10; // Additional context
}
```

**Response Structure** (`proto/loom/v1/judge.proto:90`):
```protobuf
message EvaluateResponse {
  bool passed = 1;                          // Overall verdict
  repeated JudgeResult verdicts = 2;        // Individual judge results
  map<string, double> dimension_scores = 3; // Quality, cost, safety, etc.
  double final_score = 4;                   // Aggregated score (0-100)
  string explanation = 5;                   // Verdict reasoning
  repeated string suggestions = 6;          // Improvement suggestions
  AggregatedJudgeMetrics aggregated = 7;    // Aggregate metrics
  EvaluationMetadata metadata = 8;          // Execution metadata
}
```

**Thread Safety**: All RPC methods can be called concurrently (gRPC handles per-request isolation).

**Rationale**:
- **gRPC-first**: Type-safe, high-performance, bidirectional streaming
- **Streaming RPC**: Long-running evaluations (MIPRO, BootstrapFewShot) need progress feedback
- **Context separation**: EvaluationContext isolates judge inputs from service concerns


### Judge Interface

**Responsibility**: Unified API for evaluating agent outputs across different judge types.

**Core Interface** (`pkg/evals/judges/judge.go:37-61`):

✅ Implemented
```go
type Judge interface {
    ID() string
    Name() string
    Criteria() []string
    Evaluate(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error)
    Weight() float64
    Criticality() loomv1.JudgeCriticality
    Dimensions() []loomv1.JudgeDimension
    Config() *loomv1.JudgeConfig
}
```

**Thread Safety**: Implementations must be safe for concurrent invocation (multiple goroutines evaluating different contexts).

**Context Propagation**:
```go
// Context carries timeout and tracing
ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
defer cancel()

result, err := judge.Evaluate(ctx, evalCtx)
```

**Rationale**:
- **Pluggable**: Multiple implementations (LLMJudge, AgentJudge, Custom)
- **Rich metadata**: 8 methods provide identity, configuration, and evaluation capabilities
- **Context-based**: Timeout and cancellation via Go context
- **Aggregation-aware**: Weight, Criticality, and Dimensions enable the Aggregator to combine results intelligently


### LLM Judge

**Responsibility**: Evaluate agent outputs using an LLM provider (LLM-as-a-judge pattern).

✅ Implemented

**Core Structure** (`pkg/evals/judges/judge.go:72-78`):
```go
type LLMJudge struct {
    id          string
    llmJudge    *llmjudge.Judge
    config      *loomv1.JudgeConfig
    tracer      observability.Tracer
    llmProvider types.LLMProvider
}
```

The LLMJudge wraps `pkg/evals/judges/llm.Judge` and maps its verdicts to Loom's multi-judge framework with dimensions, criticality, and aggregation. It supports per-judge LLM configuration via `JudgeConfig.LlmConfig` with a provider factory pattern.

**LLM Resolution Order**:
1. `config.LlmConfig` (full LLMConfig with provider/model/params) -- requires factory
2. `config.Model` (simple model string override) -- ⚠️ not yet wired
3. `llmProvider` (the fallback, typically the agent's judge role LLM)

**4 Core Scores** (`proto/loom/v1/judge.proto:131`):
```protobuf
message JudgeResult {
    // 4 core scores (0-100)
    int32 factual_accuracy = 5;       // How accurate is the response?
    int32 hallucination_score = 6;    // Lower is better (0 = no hallucination)
    int32 query_quality = 7;          // How well does it address the query?
    int32 completeness = 8;           // How complete is the response?
}
```

**Overall Score Calculation** (`pkg/evals/judges/judge.go:268-271`):
```go
// Equal-weight average of 4 core scores
// Hallucination inverted (100 - score) since lower is better
overallScore := (float64(verdict.FactualAccuracy) +
    (100.0 - float64(verdict.HallucinationScore)) +
    float64(verdict.QueryQuality) +
    float64(verdict.Completeness)) / 4.0
```

**Rationale**:
- **LLM-as-a-judge**: Uses any configured LLM provider for evaluation
- **4 core dimensions**: Factual accuracy, hallucination, query quality, completeness
- **Equal-weight scoring**: Simple average avoids arbitrary weight tuning
- **Per-judge LLM**: Each judge can use a different LLM provider/model via `JudgeConfig.LlmConfig`

**Status**: The LLMJudge is the implemented judge type. A direct Hawk API integration is not yet implemented.


### Agent Judge

⚠️ Partial -- `AgentJudge.Evaluate()` returns placeholder results (score=80, verdict="PASS", reasoning="Agent-based evaluation not yet fully implemented"). See `pkg/evals/judges/judge.go:410-446`.

**Responsibility**: Use a Loom agent as a judge implementation.

There are two related constructs:

1. **`AgentJudge`** (`pkg/evals/judges/judge.go:319-324`): Implements the `Judge` interface, holds an `*agent.Agent` reference. Its `Evaluate()` method currently returns placeholder results.
2. **`JudgeAgentAdapter`** (`pkg/evals/judges/judge_agent_adapter.go:38-42`): Wraps a `Judge` to make it usable in the orchestration Fork-Join pattern by exposing a `Chat()` method compatible with the agent interface.

**AgentJudge Structure** (`pkg/evals/judges/judge.go:319-324`):
```go
type AgentJudge struct {
    id     string
    agent  *agent.Agent
    config *loomv1.JudgeConfig
    tracer observability.Tracer
}
```

**Constructor** (`pkg/evals/judges/judge.go:327`):
```go
func NewAgentJudge(agent *agent.Agent, config *loomv1.JudgeConfig, tracer observability.Tracer) (*AgentJudge, error)
```

**Current Evaluate Implementation** (`pkg/evals/judges/judge.go:410-446`):
```go
func (a *AgentJudge) Evaluate(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
    // ... tracing setup ...

    evaluationPrompt := a.buildEvaluationPrompt(evalCtx)

    // Currently returns placeholder results
    // TODO: Implement agent execution once agent package is integrated
    result := &loomv1.JudgeResult{
        JudgeId:      a.ID(),
        JudgeName:    a.Name(),
        JudgeModel:   "agent-judge",
        OverallScore: 80.0, // Placeholder
        Verdict:      "PASS",
        Reasoning:    "Agent-based evaluation not yet fully implemented",
    }

    _ = evaluationPrompt
    return result, nil
}
```

**Evaluation Prompt** (built by `buildEvaluationPrompt` at `judge.go:449-475`):
```go
func (a *AgentJudge) buildEvaluationPrompt(evalCtx *loomv1.EvaluationContext) string {
    return fmt.Sprintf(`Evaluate this agent output:

Prompt: %s
Response: %s
Pattern Used: %s
Tools Used: %v
Cost: $%.4f
Latency: %dms

Criteria: %s

Provide a structured evaluation with:
1. Overall score (0-100)
2. Verdict (PASS/FAIL/PARTIAL)
3. Reasoning
4. Specific issues (if any)
5. Suggestions for improvement`,
        evalCtx.Prompt, evalCtx.Response, evalCtx.PatternUsed,
        evalCtx.ToolsUsed, evalCtx.CostUsd, evalCtx.LatencyMs, a.config.Criteria)
}
```

**Rationale**:
- **Reuse agent infrastructure**: Leverage existing agent conversation loop, memory, observability
- **LLM-as-judge**: Modern evaluation pattern (LLM evaluates LLM outputs)
- **Flexible criteria**: Custom prompts enable domain-specific evaluation
- **Self-evaluation**: Loom agents can evaluate other Loom agents

**Trade-off**:
- ✅ Flexible: Any Loom agent can be a judge (when fully implemented)
- ✅ Observable: Full tracing of judge execution
- ❌ Not yet functional: Returns placeholder results (score=80, verdict="PASS")
- ❌ Slower: Full agent conversation loop adds latency (~1-5s per evaluation)
- ❌ Consistency: LLM judges less deterministic than rule-based


### Custom Judge

📋 Planned -- No `CustomJudge` struct exists in the codebase. Users can implement the `Judge` interface directly.

**Responsibility**: User-provided judge implementation for specialized evaluation logic.

**Example** (conceptual -- users implement the `Judge` interface themselves):
```go
type CustomJudge struct {
    config *JudgeConfig
    logic  func(ctx context.Context, evalCtx *EvaluationContext) (*JudgeResult, error)
}

func NewCustomJudge(config *JudgeConfig, logic func(context.Context, *EvaluationContext) (*JudgeResult, error)) *CustomJudge {
    return &CustomJudge{
        config: config,
        logic:  logic,
    }
}

func (cj *CustomJudge) Evaluate(ctx context.Context, evalCtx *EvaluationContext) (*JudgeResult, error) {
    return cj.logic(ctx, evalCtx)
}
```

**Example: SQL Safety Judge**:
```go
func SQLSafetyJudge(ctx context.Context, evalCtx *EvaluationContext) (*JudgeResult, error) {
    result := &JudgeResult{
        JudgeID:   "sql-safety-judge",
        JudgeName: "SQL Safety Judge",
        Criteria:  []string{"no_drop_table", "no_delete_without_where", "read_only"},
        Verdict:   "PASS",
    }

    // Parse SQL from agent response
    sql := extractSQL(evalCtx.Response)

    // Check for dangerous operations
    if strings.Contains(strings.ToUpper(sql), "DROP TABLE") {
        result.Verdict = "FAIL"
        result.Issues = append(result.Issues, "DROP TABLE detected - unsafe operation")
        result.DimensionScores["safety"] = 0.0
    } else if strings.Contains(strings.ToUpper(sql), "DELETE") && !strings.Contains(strings.ToUpper(sql), "WHERE") {
        result.Verdict = "FAIL"
        result.Issues = append(result.Issues, "DELETE without WHERE clause - unsafe")
        result.DimensionScores["safety"] = 30.0
    } else if isReadOnly(sql) {
        result.Verdict = "PASS"
        result.DimensionScores["safety"] = 100.0
    } else {
        result.Verdict = "PARTIAL"
        result.DimensionScores["safety"] = 70.0
    }

    return result, nil
}

// Register custom judge
registry.Register(&JudgeConfig{
    ID:   "sql-safety",
    Name: "SQL Safety Judge",
    Type: loomv1.JudgeType_JUDGE_TYPE_CUSTOM,
}, NewCustomJudge(config, SQLSafetyJudge))
```

**Rationale**:
- **Domain-specific logic**: Rules-based evaluation for specialized domains (SQL safety, API compliance, etc.)
- **Fast execution**: No LLM invocation (typically <1ms)
- **Deterministic**: Same input always produces same output
- **Integration**: Easy to integrate existing validation logic


### Multi-Judge Orchestrator

**Build Tag**: Requires `//go:build hawk` tag. Only compiled when building with `-tags hawk`.

**Responsibility**: Orchestrate execution of multiple judges and coordinate results.

**Core Structure** (`pkg/evals/judges/orchestrator.go`):
```go
type Orchestrator struct {
    registry     *Registry
    aggregator   *Aggregator
    tracer       observability.Tracer
    logger       *zap.Logger
    workflowOrch *orchestration.Orchestrator
    hawkExporter *observability.HawkJudgeExporter
}

func (o *Orchestrator) Evaluate(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
    ctx, span := o.tracer.StartSpan(ctx, observability.SpanJudgeOrchestration)
    defer o.tracer.EndSpan(span)

    // 1. Load judge configurations
    judges, err := o.registry.GetJudges(req.JudgeIds)
    if err != nil {
        return nil, fmt.Errorf("failed to get judges: %w", err)
    }

    // 2. Classify by criticality (safety-critical/critical vs non-critical)
    criticalJudges, nonCriticalJudges := o.classifyByCriticality(judges)

    // 3. Execute judges based on execution mode
    var allVerdicts []*loomv1.JudgeResult
    switch req.ExecutionMode {
    case loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS:
        allVerdicts, err = o.executeSyncJudges(ctx, judges, req.Context, req.FailFast, nil)
    case loomv1.ExecutionMode_EXECUTION_MODE_ASYNCHRONOUS:
        allVerdicts, err = o.executeAsyncJudges(ctx, judges, req.Context, nil)
    case loomv1.ExecutionMode_EXECUTION_MODE_HYBRID:
        // Critical sync, non-critical async in background
        allVerdicts, err = o.executeSyncJudges(ctx, criticalJudges, req.Context, req.FailFast, nil)
        if len(nonCriticalJudges) > 0 {
            go o.executeAsyncJudges(context.Background(), nonCriticalJudges, req.Context, nil)
        }
    default:
        allVerdicts, err = o.executeSyncJudges(ctx, judges, req.Context, req.FailFast, nil)
    }

    if err != nil {
        return nil, fmt.Errorf("judge execution failed: %w", err)
    }

    // 4. Aggregate results
    aggregated := o.aggregator.Aggregate(allVerdicts, judges, req.Aggregation)
    finalVerdict := o.aggregator.ComputeFinalVerdict(aggregated, allVerdicts)

    // 5. Export to Hawk if enabled
    if req.ExportToHawk && o.hawkExporter != nil {
        for _, verdict := range allVerdicts {
            o.hawkExporter.ExportJudgeResult(ctx, verdict)
        }
    }

    return &loomv1.EvaluateResponse{Passed: finalVerdict == "PASS", ...}, nil
}
```

**Synchronous Execution** (runs judges in parallel using Fork-Join pattern):
```go
// executeSyncJudges runs judges in parallel using goroutines.
// Despite the name "sync", this method runs all judges concurrently
// and collects results via a buffered channel (Fork-Join pattern).
func (o *Orchestrator) executeSyncJudges(
    ctx context.Context,
    judges []Judge,
    evalCtx *loomv1.EvaluationContext,
    failFast bool,
    stream chan<- *loomv1.EvaluateProgress,
) ([]*loomv1.JudgeResult, error) {
    resultChan := make(chan judgeResult, len(judges))

    // Fork: Start all judge evaluations in parallel
    for _, judge := range judges {
        go func(j Judge) {
            wrappedJudge := o.wrapJudgeWithRetry(j)
            verdict, err := wrappedJudge.Evaluate(ctx, evalCtx)
            resultChan <- judgeResult{verdict: verdict, err: err, judge: j}
        }(judge)
    }

    // Join: Collect results
    verdicts := make([]*loomv1.JudgeResult, 0, len(judges))
    for i := 0; i < len(judges); i++ {
        result := <-resultChan
        if result.err != nil {
            if failFast {
                return nil, fmt.Errorf("judge %s failed (fail-fast): %w", result.judge.Name(), result.err)
            }
            continue
        }
        verdicts = append(verdicts, result.verdict)
    }
    return verdicts, nil
}
```

**Asynchronous Execution** (same parallel implementation, no fail-fast):
```go
// executeAsyncJudges is identical to executeSyncJudges but without fail-fast.
func (o *Orchestrator) executeAsyncJudges(
    ctx context.Context,
    judges []Judge,
    evalCtx *loomv1.EvaluationContext,
    stream chan<- *loomv1.EvaluateProgress,
) ([]*loomv1.JudgeResult, error) {
    return o.executeSyncJudges(ctx, judges, evalCtx, false, stream)
}
```

**Hybrid Execution** (critical parallel, non-critical async in background):
```go
// In hybrid mode, critical judges run in parallel (blocking),
// non-critical judges run in background goroutines (fire-and-forget).
// Only critical judge results are returned in the immediate response.
```

**Rationale**:
- **Flexible execution**: Sync for blocking workflows, async for background evaluation, hybrid for balance
- **Criticality-aware**: Safety-critical judges execute first and synchronously
- **Fail-fast support**: Abort on first critical failure if enabled
- **Observable**: Full tracing of multi-judge execution


### Aggregator

**Build Tag**: Requires `//go:build hawk` tag.

**Responsibility**: Combine multiple judge verdicts into a single evaluation result.

**Core Structure** (`pkg/evals/judges/aggregator.go`):
```go
type Aggregator struct {
    config *AggregatorConfig
}

func (a *Aggregator) Aggregate(verdicts []*loomv1.JudgeResult, judges []Judge, strategy loomv1.AggregationStrategy) *loomv1.AggregatedJudgeMetrics {
    switch strategy {
    case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE:
        return a.weightedAverage(verdicts, judges)
    case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS:
        return a.allMustPass(verdicts)
    case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS:
        return a.majorityPass(verdicts)
    case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ANY_PASS:
        return a.anyPass(verdicts)
    case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MIN_SCORE:
        return a.minScore(verdicts, strategy)
    case loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAX_SCORE:
        return a.maxScore(verdicts, strategy)
    default:
        return a.weightedAverage(verdicts, judges) // Default
    }
}
```

**Aggregation Strategies** (`proto/loom/v1/judge.proto:301`):
```protobuf
enum AggregationStrategy {
  AGGREGATION_STRATEGY_UNSPECIFIED = 0;
  AGGREGATION_STRATEGY_WEIGHTED_AVERAGE = 1; // Weighted avg of scores
  AGGREGATION_STRATEGY_ALL_MUST_PASS = 2;    // All judges must pass
  AGGREGATION_STRATEGY_MAJORITY_PASS = 3;    // >50% must pass
  AGGREGATION_STRATEGY_ANY_PASS = 4;         // At least one passes
  AGGREGATION_STRATEGY_MIN_SCORE = 5;        // Use minimum score
  AGGREGATION_STRATEGY_MAX_SCORE = 6;        // Use maximum score
}
```

**See [Algorithms](#algorithms) section for detailed implementations.**

**Dimension Score Aggregation** (performed inside `weightedAverage`, not as a separate method):
```go
// Inside weightedAverage():
dimensionScoresSum := make(map[string]float64)
dimensionCounts := make(map[string]int)

for _, verdict := range verdicts {
    for dim, score := range verdict.DimensionScores {
        dimensionScoresSum[dim] += score
        dimensionCounts[dim]++
    }
}

avgDimensionScores := make(map[string]float64)
for dim, sum := range dimensionScoresSum {
    avgDimensionScores[dim] = sum / float64(dimensionCounts[dim])
}
```

**Rationale**:
- **6 strategies**: Different use cases (quality gates, detailed feedback, etc.)
- **Dimension-aware**: Quality, cost, safety, domain scores aggregated separately
- **Weighted**: Critical judges weighted higher than non-critical
- **Statistical**: Std dev, min/max, pass rate calculated for analysis


### Retry & Circuit Breaker

**Build Tag**: Requires `//go:build hawk` tag.

**Responsibility**: Handle transient failures with exponential backoff retry and prevent cascading failures with circuit breaker.

**Core Structure** (`pkg/evals/judges/retry.go`):
```go
type RetryableJudge struct {
    judge          Judge
    retryConfig    *loomv1.RetryConfig
    circuitBreaker *CircuitBreaker
    logger         *zap.Logger
}

type CircuitBreaker struct {
    config          *loomv1.CircuitBreakerConfig
    state           CircuitState
    failureCount    int32
    successCount    int32
    lastStateChange time.Time
    mu              sync.RWMutex
}

type CircuitState int

const (
    CircuitClosed  CircuitState = iota // Normal operation
    CircuitOpen                         // Failing, reject requests
    CircuitHalfOpen                     // Testing recovery
)
```

**Retry Configuration** (`proto/loom/v1/judge.proto:458`):
```protobuf
message RetryConfig {
  int32 max_attempts = 1;          // Max 3 attempts
  int32 initial_backoff_ms = 2;    // 1000ms initial delay
  int32 max_backoff_ms = 3;        // 8000ms max delay
  double backoff_multiplier = 4;   // 2.0 (exponential)
  repeated int32 retry_on_status = 5; // [429, 500, 502, 503]
  CircuitBreakerConfig circuit_breaker = 6;
}
```

**Circuit Breaker Configuration** (`proto/loom/v1/judge.proto:479`):
```protobuf
message CircuitBreakerConfig {
  int32 failure_threshold = 1;  // 5 consecutive failures → open
  int32 reset_timeout_ms = 2;   // 60000ms (1 min) cooldown
  int32 success_threshold = 3;  // 2 successes → close
  bool enabled = 4;             // default: true
}
```

**See [Algorithms](#algorithms) section for detailed retry and circuit breaker implementations.**

**Rationale**:
- **Retry transient failures**: Network blips, temporary service unavailability
- **Circuit breaker**: Prevent overwhelming failing judges (e.g., LLM rate limits)
- **Per-judge isolation**: One judge's failures don't affect others
- **Exponential backoff**: Avoid thundering herd on recovery


## Key Interactions

### Synchronous Evaluation Flow

```
Client         JudgeService     Orchestrator     Judges           Hawk
  │                  │                │              │               │
  ├─ EvaluateWithJudges ─────────────▶│              │               │
  │                  │                │              │               │
  │                  │                ├─ Load Judges │               │
  │                  │                ├─ Classify by criticality     │
  │                  │                │              │               │
  │                  │                ├─ Judge 1 ───▶│               │
  │                  │                │  (goroutine) │               │
  │                  │                ├─ Judge 2 ───▶│               │
  │                  │                │  (goroutine) │               │
  │                  │                ├─ Judge 3 ───▶│               │
  │                  │                │  (goroutine) │               │
  │                  │                │              │               │
  │                  │                │◀─ Result 2 ──┤               │
  │                  │                │◀─ Result 1 ──┤               │
  │                  │                │◀─ Result 3 ──┤               │
  │                  │                │              │               │
  │                  │                ├─ Aggregate Results           │
  │                  │                ├─ Calculate dimension scores  │
  │                  │                ├─ Determine verdict           │
  │                  │                │              │               │
  │                  │                ├─ Export ─────┼───────────────▶│
  │                  │                │              │               │
  │                  │◀─ Response ────┤              │               │
  │◀─ EvaluateResponse ───────────────┤              │               │
  │                  │                │              │               │
```

**Properties**:
- **Synchronous**: Client blocks until all judges complete
- **Parallel**: Judges execute concurrently via goroutines (Fork-Join pattern)
- **Criticality-aware**: Safety-critical and critical judges classified separately
- **Latency**: Max of individual judge latencies + aggregation overhead (parallel execution)

**Use Case**: Critical evaluations where all judge verdicts are required before proceeding (e.g., safety gates).


### Asynchronous Evaluation Flow

```
Client         JudgeService     Orchestrator     Judges           Hawk
  │                  │                │              │               │
  ├─ EvaluateWithJudges ─────────────▶│              │               │
  │                  │                │              │               │
  │                  │                ├─ Load Judges │               │
  │                  │                ├─ Spawn goroutines            │
  │                  │                │              │               │
  │                  │                ├─ Judge 1 ───▶│               │
  │                  │                │  (goroutine) │               │
  │                  │                ├─ Judge 2 ───▶│               │
  │                  │                │  (goroutine) │               │
  │                  │                ├─ Judge 3 ───▶│               │
  │                  │                │  (goroutine) │               │
  │                  │                │              │               │
  │                  │                │◀─ Result 2 ──┤               │
  │                  │                │◀─ Result 1 ──┤               │
  │                  │                │◀─ Result 3 ──┤               │
  │                  │                │              │               │
  │                  │                ├─ Collect via buffered channel │
  │                  │                ├─ Aggregate Results           │
  │                  │                ├─ Export ─────┼───────────────▶│
  │                  │                │              │               │
  │                  │◀─ Response ────┤              │               │
  │◀─ EvaluateResponse ───────────────┤              │               │
  │                  │                │              │               │          
```

**Properties**:
- **Parallel**: Judges execute concurrently (goroutines), same as sync mode
- **No fail-fast**: Unlike sync mode, never aborts early on failure (always collects all results)
- **Latency**: Max of individual judge latencies + aggregation overhead (not sum)
- **Concurrency**: N goroutines (1 per judge)

**Note**: Despite the name, async mode still blocks the RPC call until all judges complete. The difference from sync mode is only the absence of fail-fast behavior. True fire-and-forget execution only happens for non-critical judges in hybrid mode.

**Use Case**: Evaluation where all judge results are desired even if some fail.


### Streaming Evaluation Flow

```
Client         JudgeService     Orchestrator     Judges           Hawk
  │                  │                │              │               │
  ├─ EvaluateWithJudgesStream ───────▶│              │               │
  │◀─ stream opened ───────────────────┤              │               │
  │                  │                │              │               │
  │                  │                ├─ Load Judges │               │
  │                  │                ├─ Judge 1 ───▶│               │
  │◀─ JudgeStarted ──────────────────┤              │               │
  │                  │                │              │               │
  │                  │                │◀─ Result 1 ──┤               │
  │◀─ JudgeCompleted ────────────────┤              │               │
  │                  │                │              │               │
  │                  │                ├─ Judge 2 ───▶│               │
  │◀─ JudgeStarted ──────────────────┤              │               │
  │                  │                │              │               │
  │                  │                │◀─ Result 2 ──┤               │
  │◀─ JudgeCompleted ────────────────┤              │               │
  │                  │                │              │               │
  │                  │                ├─ Judge 3 ───▶│               │
  │◀─ JudgeStarted ──────────────────┤              │               │
  │                  │                │              │               │
  │                  │                │◀─ Result 3 ──┤               │
  │◀─ JudgeCompleted ────────────────┤              │               │
  │                  │                │              │               │
  │                  │                ├─ Aggregate Results           │
  │                  │                ├─ Export ─────┼───────────────▶│
  │◀─ EvaluationCompleted ───────────┤              │               │
  │                  │                │              │               │          
```

**Progress Events** (`proto/loom/v1/judge.proto:495`):
```protobuf
message EvaluateProgress {
  oneof progress {
    JudgeStarted judge_started = 1;           // Judge evaluation began
    JudgeCompleted judge_completed = 2;       // Judge evaluation finished
    ExampleCompleted example_completed = 3;   // All judges evaluated an example
    EvaluationCompleted evaluation_completed = 4; // Entire evaluation complete
  }
}
```

**Properties**:
- **Streaming**: Client receives progress updates as judges complete
- **Real-time feedback**: See which judges are running, completed, failed
- **Long-running support**: Essential for MIPRO, BootstrapFewShot (minutes to hours)
- **Latency visibility**: Client can display per-judge execution time

**Use Case**: Interactive evaluation, long-running optimization (MIPRO, BootstrapFewShot), progress visualization in TUI/web UI.


### Retry with Circuit Breaker Flow

```
Orchestrator   RetryableJudge  CircuitBreaker    Judge (LLM)
  │                  │                │              │                          
  ├─ Execute Judge ──▶│                │              │                         
  │                  ├─ Check Circuit ▶│              │                         
  │                  │◀─ Closed ───────┤              │                         
  │                  │                │              │                          
  │                  ├─ Attempt 1 ─────┼─────────────▶│                         
  │                  │◀─ 429 Rate Limit ──────────────┤                         
  │                  ├─ Record Failure ▶│              │                        
  │                  │                │              │                          
  │                  ├─ Wait 1s (backoff)             │                         
  │                  ├─ Attempt 2 ─────┼─────────────▶│                         
  │                  │◀─ 503 Unavailable ─────────────┤                         
  │                  ├─ Record Failure ▶│              │                        
  │                  │                │              │                          
  │                  ├─ Wait 2s (backoff × 2)         │                         
  │                  ├─ Attempt 3 ─────┼─────────────▶│                         
  │                  │◀─ 200 OK ──────────────────────┤                         
  │                  ├─ Record Success ▶│              │                        
  │                  │◀─ Reset Failures ┤              │                        
  │                  │                │              │                          
  │◀─ Judge Result ──┤                │              │                          
  │                  │                │              │                          

  [After 5 consecutive failures]

  │                  ├─ Record Failure ▶│              │                        
  │                  │◀─ Open Circuit ──┤              │                        
  │                  │                │              │                          
  │◀─ Error: Circuit Open ──────────────┤              │                        
  │                  │                │              │                          

  [After 1 minute cooldown]

  ├─ Execute Judge ──▶│                │              │                         
  │                  ├─ Check Circuit ▶│              │                         
  │                  │◀─ HalfOpen ─────┤              │                         
  │                  │                │              │                          
  │                  ├─ Attempt 1 ─────┼─────────────▶│                         
  │                  │◀─ 200 OK ──────────────────────┤                         
  │                  ├─ Record Success ▶│              │                        
  │                  │◀─ Success 1/2 ───┤              │                        
  │                  │                │              │                          
  │◀─ Judge Result ──┤                │              │                          
  │                  │                │              │                          

  [After 2 consecutive successes]

  │                  ├─ Record Success ▶│              │                        
  │                  │◀─ Close Circuit ─┤              │                        
  │                  │                │              │                          
```

**Circuit States**:
```
Closed ────5 failures───▶ Open ────1 min cooldown───▶ HalfOpen                  
  ▲                                                       │                     
  │                                                       │                     
└──────────────────────────────────────────────────────────────────────────────┘
```

**Properties**:
- **Retry transient failures**: 429 rate limits, 500 errors, network timeouts
- **Exponential backoff**: 1s → 2s → 4s (prevents thundering herd)
- **Circuit breaker**: 5 failures → open (stop hammering failing service)
- **Cooldown**: 1 minute before retrying (give service time to recover)
- **Half-open**: Test with single request before fully closing circuit

**Use Case**: Production reliability, LLM rate limit handling, transient failure recovery.


## Data Structures

### JudgeResult

**Definition** (`proto/loom/v1/judge.proto:117`):
```protobuf
message JudgeResult {
  // Identifiers
  string judge_id = 1;
  string judge_name = 2;
  string judge_model = 3; // e.g., "claude-sonnet-4-5"
  repeated string criteria = 4;

  // 4 core scores (0-100)
  int32 factual_accuracy = 5;
  int32 hallucination_score = 6; // Lower is better
  int32 query_quality = 7;
  int32 completeness = 8;

  // Overall and verdict
  double overall_score = 9;      // Weighted combination
  string verdict = 10;            // "PASS", "FAIL", "PARTIAL"
  string reasoning = 11;
  repeated string issues = 12;
  repeated string suggestions = 13;

  // Metadata
  int64 execution_time_ms = 14;
  double cost_usd = 15;
  string error = 16;
  google.protobuf.Timestamp judged_at = 17;

  // Dimension scores (quality, cost, safety, domain, performance, usability, custom)
  map<string, double> dimension_scores = 18;
}
```

**Invariants**:
```
∀ score ∈ {factual_accuracy, hallucination_score, query_quality, completeness}:
    0 ≤ score ≤ 100

∀ dimension_score ∈ dimension_scores.values():
    0.0 ≤ dimension_score ≤ 100.0

overall_score = (factual_accuracy + (100 - hallucination_score) + query_quality + completeness) / 4.0

verdict ∈ {"PASS", "FAIL", "PARTIAL"}
```


### EvaluateResponse

**Definition** (`proto/loom/v1/judge.proto:90`):
```protobuf
message EvaluateResponse {
  bool passed = 1;                          // Overall verdict
  repeated JudgeResult verdicts = 2;        // Individual judge results
  map<string, double> dimension_scores = 3; // Aggregated dimension scores
  double final_score = 4;                   // Aggregated score (0-100)
  string explanation = 5;                   // Verdict reasoning
  repeated string suggestions = 6;          // Aggregated suggestions
  AggregatedJudgeMetrics aggregated = 7;    // Aggregate statistics
  EvaluationMetadata metadata = 8;          // Execution metadata
}
```

**AggregatedJudgeMetrics** (`proto/loom/v1/judge.proto:168`):
```protobuf
message AggregatedJudgeMetrics {
  double weighted_average_score = 1;
  double min_score = 2;
  double max_score = 3;
  double score_stddev = 4;
  double pass_rate = 5;                     // % of judges that passed
  AggregationStrategy strategy = 6;
  int64 total_execution_time_ms = 7;
  double total_cost_usd = 8;
  map<string, double> avg_dimension_scores = 9;
}
```


### JudgeConfig

**Definition** (`proto/loom/v1/judge.proto:249`):
```protobuf
message JudgeConfig {
  string id = 1;                      // Unique identifier
  string name = 2;                    // Display name
  string criteria = 3;                // Evaluation criteria description
  double weight = 4;                  // Aggregation weight (default: 1.0)
  int32 min_passing_score = 5;        // Pass threshold (default: 80)
  JudgeCriticality criticality = 6;   // Non-critical, critical, safety-critical
  string custom_prompt = 7;           // Custom prompt template (optional)
  string model = 8;                   // LLM model (optional)
  JudgeType type = 9;                 // Hawk, agent, custom
  string agent_id = 10;               // Agent ID (if type=agent)
  repeated JudgeDimension dimensions = 11; // Evaluated dimensions
  string custom_dimension_name = 20;  // Custom dimension name (if dimensions contains CUSTOM)
  string custom_dimension_description = 21; // Custom dimension description
  RetryConfig retry_config = 30;      // Retry configuration
  LLMConfig llm_config = 31;          // Full LLM config (overrides model field 8)
}
```

**Judge Types** (`proto/loom/v1/judge.proto:352`):
```protobuf
enum JudgeType {
  JUDGE_TYPE_UNSPECIFIED = 0;
  JUDGE_TYPE_HAWK = 1;    // LLM-as-a-judge (uses LLMJudge implementation)
  JUDGE_TYPE_AGENT = 2;   // Loom agent as judge
  JUDGE_TYPE_CUSTOM = 3;  // User-provided implementation
}
```

**Judge Dimensions** (`proto/loom/v1/judge.proto:366`):
```protobuf
enum JudgeDimension {
  JUDGE_DIMENSION_UNSPECIFIED = 0;
  JUDGE_DIMENSION_QUALITY = 1;      // Quality and correctness
  JUDGE_DIMENSION_COST = 2;         // Cost efficiency
  JUDGE_DIMENSION_SAFETY = 3;       // Safety and guardrails
  JUDGE_DIMENSION_DOMAIN = 4;       // Domain compliance
  JUDGE_DIMENSION_PERFORMANCE = 5;  // Performance and latency
  JUDGE_DIMENSION_USABILITY = 6;    // Usability and clarity
  JUDGE_DIMENSION_CUSTOM = 100;     // User-defined dimensions
}
```

**Criticality Levels** (`proto/loom/v1/judge.proto:338`):
```protobuf
enum JudgeCriticality {
  JUDGE_CRITICALITY_UNSPECIFIED = 0;
  JUDGE_CRITICALITY_NON_CRITICAL = 1;      // Can run async
  JUDGE_CRITICALITY_CRITICAL = 2;          // Must run sync
  JUDGE_CRITICALITY_SAFETY_CRITICAL = 3;   // Highest priority, blocks everything
}
```


## Algorithms

### Weighted Average Aggregation

**Problem**: Combine scores from multiple judges with different weights into a single score.

**Solution**: Weighted average with judge-specific weights.

**Algorithm**:
```go
func (a *Aggregator) weightedAverage(results []*JudgeResult, judgeWeights map[string]float64) *EvaluateResponse {
    totalWeight := 0.0
    weightedSum := 0.0

    for _, result := range results {
        weight := judgeWeights[result.JudgeID]
        if weight == 0.0 {
            weight = 1.0 // Default weight
        }

        weightedSum += result.OverallScore * weight
        totalWeight += weight
    }

    finalScore := weightedSum / totalWeight

    // Determine verdict based on score
    passed := finalScore >= 80.0 // Default passing threshold

    return &EvaluateResponse{
        Passed:     passed,
        Verdicts:   results,
        FinalScore: finalScore,
        Aggregated: &AggregatedJudgeMetrics{
            WeightedAverageScore: finalScore,
            Strategy: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
        },
    }
}
```

**Complexity**: O(n) where n = number of judges

**Example**:
```
Judge 1: score=85, weight=2.0 → contribution = 85 * 2.0 = 170
Judge 2: score=90, weight=1.0 → contribution = 90 * 1.0 = 90
Judge 3: score=75, weight=1.0 → contribution = 75 * 1.0 = 75

Final Score = (170 + 90 + 75) / (2.0 + 1.0 + 1.0) = 335 / 4.0 = 83.75
```

**Rationale**: Allows prioritizing critical judges (e.g., safety judges weighted 2x).


### Majority Pass Aggregation

**Problem**: Require majority of judges to pass (>50% pass rate).

**Solution**: Count passes, require >50%.

**Algorithm**:
```go
func (a *Aggregator) majorityPass(results []*JudgeResult) *EvaluateResponse {
    passCount := 0
    totalCount := len(results)

    for _, result := range results {
        if result.Verdict == "PASS" {
            passCount++
        }
    }

    passRate := float64(passCount) / float64(totalCount)
    passed := passRate > 0.5

    return &EvaluateResponse{
        Passed:   passed,
        Verdicts: results,
        Aggregated: &AggregatedJudgeMetrics{
            PassRate: passRate,
            Strategy: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS,
        },
    }
}
```

**Complexity**: O(n) where n = number of judges

**Example**:
```
3 judges: 2 PASS, 1 FAIL → pass_rate = 2/3 = 0.667 > 0.5 → PASS
5 judges: 2 PASS, 3 FAIL → pass_rate = 2/5 = 0.4 < 0.5 → FAIL
```

**Rationale**: Democratic voting, no single judge can veto or pass alone.


### Exponential Backoff Retry

**Problem**: Retry failed judge execution without overwhelming failing service.

**Solution**: Exponential backoff with doubling delay.

**Algorithm** (simplified from `pkg/evals/judges/retry.go:112-199`):
```go
func (rj *RetryableJudge) Evaluate(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
    // Check circuit breaker first
    if !rj.circuitBreaker.AllowRequest() {
        return nil, fmt.Errorf("circuit breaker open for judge %s", rj.judge.Name())
    }

    var lastErr error
    maxAttempts := rj.retryConfig.MaxAttempts

    for attempt := int32(0); attempt <= maxAttempts; attempt++ {
        result, err := rj.judge.Evaluate(ctx, evalCtx)

        if err == nil {
            rj.circuitBreaker.RecordSuccess()
            return result, nil
        }

        lastErr = err

        if !rj.isRetryable(err) {
            rj.circuitBreaker.RecordFailure()
            return nil, fmt.Errorf("non-retryable error: %w", err)
        }

        if attempt == maxAttempts {
            rj.circuitBreaker.RecordFailure()
            break
        }

        // Exponential backoff: initialBackoff * (multiplier ^ attempt)
        backoff := rj.calculateBackoff(attempt)
        select {
        case <-time.After(backoff):
        case <-ctx.Done():
            rj.circuitBreaker.RecordFailure()
            return nil, ctx.Err()
        }
    }

    return nil, fmt.Errorf("all retries exhausted: %w", lastErr)
}
```

**Complexity**: O(k) where k = retry attempts

**Backoff Schedule** (initial=1s, multiplier=2.0, max=8s):
```
Attempt 0: Immediate (0s delay)
Attempt 1: 1s delay
Attempt 2: 2s delay
Attempt 3: 4s delay
Total time: 7s (max)
```

**Rationale**:
- **Exponential**: Gives service time to recover
- **Limited attempts**: Fail fast rather than infinite loop
- **Max backoff**: Cap delay at 8s to prevent unbounded wait


### Circuit Breaker State Machine

**Problem**: Prevent overwhelming failing judges (e.g., LLM rate limits).

**Solution**: Circuit breaker with 3 states (Closed, Open, HalfOpen).

**Algorithm** (actual implementation uses separate methods, not an `Execute` wrapper):
```go
// AllowRequest checks if the circuit breaker allows a request to proceed.
// Handles state transitions from Open -> HalfOpen when cooldown expires.
func (cb *CircuitBreaker) AllowRequest() bool {
    cb.mu.Lock()
    defer cb.mu.Unlock()

    switch cb.state {
    case CircuitClosed:
        return true
    case CircuitOpen:
        resetTimeout := time.Duration(cb.config.ResetTimeoutMs) * time.Millisecond
        if time.Since(cb.lastStateChange) > resetTimeout {
            cb.state = CircuitHalfOpen
            cb.successCount = 0
            cb.failureCount = 0
            cb.lastStateChange = time.Now()
            return true
        }
        return false // Still open, block request
    case CircuitHalfOpen:
        return true
    default:
        return true
    }
}

// RecordSuccess records a successful evaluation.
// In HalfOpen: increments success count, closes circuit at threshold.
func (cb *CircuitBreaker) RecordSuccess() {
    cb.mu.Lock()
    defer cb.mu.Unlock()
    cb.failureCount = 0

    switch cb.state {
    case CircuitHalfOpen:
        cb.successCount++
        if cb.successCount >= cb.config.SuccessThreshold {
            cb.state = CircuitClosed
            cb.successCount = 0
            cb.lastStateChange = time.Now()
        }
    }
}

// RecordFailure records a failed evaluation.
// In Closed: opens circuit at threshold.
// In HalfOpen: reopens circuit immediately.
func (cb *CircuitBreaker) RecordFailure() {
    cb.mu.Lock()
    defer cb.mu.Unlock()
    cb.failureCount++
    cb.successCount = 0

    switch cb.state {
    case CircuitClosed:
        if cb.failureCount >= cb.config.FailureThreshold {
            cb.state = CircuitOpen
            cb.lastStateChange = time.Now()
        }
    case CircuitHalfOpen:
        cb.state = CircuitOpen
        cb.lastStateChange = time.Now()
    }
}
```

**State Transitions**:
```
Closed (normal operation)
  │                                                                             
  ├─ 5 consecutive failures                                                     
  │                                                                             
  ▼
Open (reject all requests)
  │                                                                             
  ├─ 1 minute cooldown elapsed                                                  
  │                                                                             
  ▼
HalfOpen (test recovery)
  │                                                                             
  ├─ 2 consecutive successes → Closed                                           
  └─ any failure → Open                                                         
```

**Complexity**: O(1) per execution

**Rationale**:
- **Closed**: Normal operation, track failures
- **Open**: Stop hammering failing service, give it time to recover
- **HalfOpen**: Test if service recovered before fully resuming


## Design Trade-offs

### Decision 1: Multi-Judge vs. Single-Judge

**Chosen**: Multi-judge with aggregation strategies

**Rationale**:
- **Multi-dimensional evaluation**: No single judge covers all dimensions (quality, cost, safety, domain)
- **Resilience**: One judge's failure doesn't invalidate entire evaluation
- **Flexibility**: Different aggregation strategies for different use cases

**Alternatives**:
1. **Single "super-judge"**:
   - ✅ Simple implementation
   - ✅ Lower latency (one LLM call vs multiple)
   - ❌ Single point of failure
   - ❌ Must evaluate all dimensions simultaneously (conflicting priorities)
   - ❌ Hard to specialize judges for specific dimensions

2. **Multi-judge (current choice)**:
   - ✅ Specialized judges per dimension (quality, cost, safety)
   - ✅ Fault-tolerant (one judge failure doesn't fail entire evaluation)
   - ✅ Flexible aggregation (weighted average, majority-pass, etc.)
   - ❌ Higher latency (multiple LLM calls, even with parallelism)
   - ❌ More complex implementation

**Consequences**:
- ✅ Multi-dimensional evaluation across quality, cost, safety
- ✅ Fault tolerance via retry and circuit breaker per judge
- ❌ Latency: 3-5 judges × 1-5s each = 3-25s (mitigated by async execution)


### Decision 2: Synchronous vs. Asynchronous Execution

**Chosen**: 3 execution modes (sync, async, hybrid) with user control

**Rationale**:
- **Flexibility**: Different use cases require different execution models
- **Latency vs. Completeness trade-off**: Sync for critical gates, async for background analysis
- **Hybrid**: Balance between latency and completeness

**Alternatives**:
1. **Synchronous only**:
   - ✅ Simple implementation
   - ✅ All results available before response
   - ❌ High latency blocks user response
   - ❌ No support for background evaluation

2. **Asynchronous only**:
   - ✅ Low latency (non-blocking)
   - ❌ Can't block on critical judges (safety gates)
   - ❌ Results not available in response

3. **Hybrid (current choice)**:
   - ✅ Sync for critical judges (safety-critical, critical)
   - ✅ Async for non-critical judges (cost, performance)
   - ✅ Balance between latency and completeness
   - ❌ More complex implementation

**Consequences**:
- ✅ Flexibility for different use cases
- ✅ Hybrid mode reduces latency while maintaining safety gates
- ❌ Complexity: 3 execution paths to maintain


### Decision 3: LLM Judge vs. Agent Judge vs. Custom Judge

**Chosen**: Pluggable judge interface with 3 types (1 fully implemented, 1 partial, 1 planned)

**Rationale**:
- **LLM Judge** ✅: LLM-as-a-judge evaluation, 4 core scores, configurable per-judge LLM
- **Agent Judge** ⚠️: Wraps a Loom agent as judge, custom criteria, full observability (not yet fully implemented -- returns placeholder results)
- **Custom Judge** 📋: Domain-specific logic (SQL safety, API compliance), fast execution (no built-in struct; users implement `Judge` interface directly)

**Alternatives**:
1. **LLM Judge only**:
   - ✅ Flexible criteria via LLM prompts
   - ✅ 4 core scores (factual accuracy, hallucination, query quality, completeness)
   - ❌ High latency (1-5s per judge)
   - ❌ Less deterministic (LLM variance)

2. **Agent Judge only**:
   - ✅ Reuses agent infrastructure
   - ✅ Full observability
   - ❌ Higher latency (full agent conversation loop)
   - ❌ Not yet fully implemented (returns placeholder results)

3. **Pluggable (current choice)**:
   - ✅ LLM Judge for standard evaluation (factual accuracy, hallucination)
   - ✅ Agent Judge for custom criteria (domain-specific evaluation, when fully implemented)
   - ✅ Custom for fast rule-based checks (SQL safety, API compliance)
   - ❌ More complex: 3 implementations to maintain

**Consequences**:
- ✅ Multiple evaluation approaches: LLM's scoring + Agent's flexibility + Custom's speed
- ✅ Users choose judge type per dimension
- ❌ Maintenance: 3 judge implementations


### Decision 4: 6 Aggregation Strategies

**Chosen**: 6 strategies (weighted average, all-must-pass, majority-pass, any-pass, min-score, max-score)

**Rationale**:
- **Different use cases**: Quality gates (all-must-pass), detailed feedback (weighted average), voting (majority-pass)
- **Flexibility**: Users choose strategy per evaluation context

**Alternatives**:
1. **Weighted average only**:
   - ✅ Simple implementation
   - ✅ Handles most cases
   - ❌ No support for strict gates (all-must-pass)
   - ❌ No voting (majority-pass)

2. **6 strategies (current choice)**:
   - ✅ Covers all common use cases
   - ✅ Flexibility for different contexts
   - ❌ Complexity: 6 aggregation implementations

**Use Case Mapping**:
```
weighted_average: General-purpose multi-dimensional evaluation
all_must_pass: Safety gates (all dimensions must pass)
majority_pass: Democratic voting (>50% judges must agree)
any_pass: At least one judge must pass (minimum bar)
min_score: Pessimistic (weakest link determines score)
max_score: Optimistic (strongest judge determines score)
```

**Consequences**:
- ✅ Full coverage of evaluation scenarios
- ❌ Users must understand when to use which strategy


## Constraints and Limitations

### Constraint 1: Single-Process Multi-Goroutine Only

**Description**: Judges execute in goroutines within a single process (no distributed judge execution across processes).

**Rationale**: Loom is a single-process multi-agent server.

**Impact**: Cannot scale judge execution horizontally across multiple machines.

**Workaround**: Vertical scaling (increase CPU/memory), or deploy multiple Loom instances with load balancer.


### Constraint 2: LLM Judge Latency

**Description**: LLM-based judges (LLMJudge) have 1-5s latency per evaluation. AgentJudge will have similar latency when fully implemented.

**Limitations**:
- Both sync and async modes run judges in parallel (goroutines): Max(judge latencies) = 1-5s
- With retry (3 attempts, exponential backoff): up to ~7s additional per failing judge

**Workaround**: Use async or hybrid execution mode, cache judge results for identical inputs.


### Constraint 3: No Judge-to-Judge Communication

**Description**: Judges evaluate independently (no communication or consensus protocol).

**Rationale**: Simplicity, fault isolation (one judge's failure doesn't block others).

**Impact**: Cannot implement consensus protocols (e.g., "if judge A fails, judge B compensates").

**Workaround**: Use aggregation strategies (e.g., majority-pass) to handle disagreements.


### Constraint 4: Hawk Dependency for Historical Storage

**Description**: Historical evaluation storage requires Hawk service (SQLite local storage planned but not yet implemented).

**Impact**: Cannot query historical evaluations without Hawk.

**Workaround**: Export results to Hawk, or implement local SQLite storage (planned).

**Status**: Local storage 📋 Planned


## Performance Characteristics

### Latency (P50/P99)

| Operation | P50 | P99 | Notes |
|-----------|-----|-----|-------|
| LLM Judge execution | 1s | 5s | LLM invocation (depends on model) |
| Agent Judge execution | <1ms | <1ms | Currently returns placeholder (not yet functional) |
| Custom Judge execution | <1ms | 5ms | Rule-based logic (e.g., SQL safety check) |
| Multi-judge sync (3 judges) | 1.5s | 5s | Max of individual judge latencies (parallel execution) |
| Multi-judge async (3 judges) | 1.5s | 5s | Max of individual judge latencies |
| Aggregation (6 strategies) | <1ms | 5ms | In-memory calculation |
| Retry (1 attempt, 1s backoff) | 1s | 1s | Fixed backoff |
| Circuit breaker check | <1µs | 5µs | Mutex lock + state check |

### Throughput

- **Judge execution**: Depends on judge type (LLM Judge: 0.2-1 eval/s, Custom: 1000+ evals/s)
- **Async multi-judge**: N goroutines × judge throughput (e.g., 3 judges × 1 eval/s = 3 evals/s)
- **Aggregation**: 100k+ aggregations/s (in-memory, no I/O)

### Memory Usage

| Component | Size |
|-----------|------|
| JudgeResult struct | ~1KB (with 10 dimension scores) |
| EvaluateResponse (3 judges) | ~3KB |
| Judge registry (100 judges) | ~50KB |
| Circuit breaker state (per judge) | ~100 bytes |
| **Total per evaluation** | **~5KB** |


## Concurrency Model

### Thread Safety

**Model**: All public APIs are thread-safe (can be called from multiple goroutines).

**Synchronization**:
- `JudgeRegistry.mu`: sync.RWMutex protects judge registration/lookup
- `CircuitBreaker.mu`: sync.Mutex protects circuit state
- `Orchestrator`: Goroutines per judge (parallel execution), buffered channel for result collection

### Goroutine Lifecycle

**Parallel Execution** (both sync and async modes use this pattern):
```go
// Fork: Spawn N goroutines (1 per judge), results via buffered channel
resultChan := make(chan judgeResult, len(judges))
for _, judge := range judges {
    go func(j Judge) {
        verdict, err := j.Evaluate(ctx, evalCtx)
        resultChan <- judgeResult{verdict: verdict, err: err, judge: j}
    }(judge)
}

// Join: Collect results from all goroutines
for i := 0; i < len(judges); i++ {
    result := <-resultChan
    // ... handle result
}
```

**Note**: Both "sync" and "async" execution modes use the same parallel goroutine pattern internally. The difference is that sync mode supports `fail_fast` (abort on first critical failure), while async mode always collects all results.

**Race Detector**: Zero race conditions detected (all tests run with `-race`).


## Error Handling

### Strategy

1. **Non-blocking**: Judge errors don't fail entire evaluation (continue with remaining judges)
2. **Logged**: Judge failures logged with trace ID for debugging
3. **Retried**: Transient failures retried with exponential backoff
4. **Circuit breaker**: Repeated failures trigger circuit breaker (stop hammering failing judge)
5. **Partial results**: Return partial results if some judges succeed

### Error Propagation

```
Judge Execution Failure ───▶ Retry (3 attempts) ───▶ Circuit Breaker Check ───▶ 
```

**Non-Critical Failures**:
- Judge timeout → Retry with backoff
- LLM rate limit (429) → Retry with exponential backoff
- Network error → Retry

**Critical Failures** (propagated):
- Invalid judge ID → Return error immediately
- Invalid aggregation strategy → Return error immediately
- Context timeout → Return error immediately (abort all judges)


## Security Considerations

### Threat Model

1. **PII Leakage**: Evaluation context contains user prompts and agent responses (may contain PII)
2. **Prompt Injection**: Malicious user prompts injected into judge evaluation
3. **Judge Manipulation**: Attacker manipulates judge config to always pass/fail

### Mitigations

**PII Leakage**:
- ✅ Export to Hawk applies observability PII redaction (email, phone, SSN patterns)
- ✅ Context metadata supports PII flags (e.g., `pii_scrubbed: true`)
- ❌ Judge evaluation itself not PII-aware (judges see raw prompts/responses)

**Prompt Injection**:
- ✅ Agent judges use structured JSON output (harder to inject)
- ✅ LLM judges use structured JSON output via llmjudge package
- ⚠️ Custom judges vulnerable (user responsibility to sanitize)

**Judge Manipulation**:
- ✅ Judge registry protected by mutex (no concurrent modification)
- ✅ Judge configs immutable after registration (must deregister and re-register to change)
- ❌ No authentication on RegisterJudge RPC (assume trusted internal network)

**Recommendations**:
1. Deploy JudgeService behind firewall (internal-only)
2. Audit judge configs before registration
3. Use LLM judges for evaluation (4 core scores with structured output)
4. Sanitize user inputs in custom judges


## Related Work

### Multi-Judge Evaluation Systems

1. **OpenAI Evals**: Single-judge evaluation framework
   - **Similar**: Structured evaluation, verdict + reasoning
   - **Loom differs**: Multi-judge coordination, 6 aggregation strategies, pluggable judges

2. **LangSmith**: LLM evaluation platform
   - **Similar**: LLM-as-judge pattern, dimension scoring
   - **Loom differs**: Hawk integration, retry/circuit breaker, streaming progress

3. **Prompt Flow**: Azure's LLM evaluation framework
   - **Similar**: Multi-dimensional evaluation (quality, safety, groundedness)
   - **Loom differs**: Pluggable judges (Hawk, Agent, Custom), hybrid execution mode

### LLM-as-Judge Research

1. **Zheng et al. (2023). "Judging LLM-as-a-Judge"**
   - **Key Finding**: GPT-4 achieves 85% agreement with human judges on quality evaluation
   - **Loom Integration**: Agent judges leverage LLM-as-judge pattern for custom criteria

2. **G-Eval (Liu et al., 2023)**: LLM-based evaluation with chain-of-thought reasoning
   - **Similar**: Reasoning + score generation
   - **Loom differs**: Multi-judge voting, dimension-specific judges


## References

1. Zheng, L., et al. (2023). *Judging LLM-as-a-Judge with MT-Bench and Chatbot Arena*. arXiv:2306.05685.

2. Liu, Y., et al. (2023). *G-Eval: NLG Evaluation using GPT-4 with Better Human Alignment*. arXiv:2303.16634.

3. Naismith, M., et al. (2023). *Circuit Breaker Pattern for Microservices*. Microsoft Azure Architecture Patterns.

4. Tanenbaum, A. S., & Van Steen, M. (2017). *Distributed Systems: Principles and Paradigms* (3rd ed.). Chapter 8: Fault Tolerance.


## Further Reading

### Architecture Deep Dives

- [Learning Agent Architecture](learning-agent.md) - Autonomous self-improvement with judge integration
- [Observability Architecture](observability.md) - Hawk integration for trace export
- [Multi-Agent Architecture](multi-agent.md) - Multi-agent orchestration patterns
- [Loom System Architecture](loom-system-architecture.md) - Overall system design

### Reference Documentation

- [Judge CLI Guide](/docs/guides/judge_cli_guide.md) - Judge CLI commands
- [Multi-Judge Evaluation Guide](/docs/guides/multi-judge-evaluation.md) - Multi-judge evaluation usage

### Guides

- [Getting Started](/docs/guides/quickstart.md) - Quick start guide
- [Judge DSPy Integration](/docs/guides/judge-dspy-integration.md) - DSPy integration with judges
- [Judge DSPy Streaming](/docs/guides/judge-dspy-streaming.md) - Streaming evaluations with DSPy

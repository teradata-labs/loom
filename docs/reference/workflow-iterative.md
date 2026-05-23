
# Iterative Workflow Pattern Reference

**Version**: v1.2.0

Complete technical reference for Loom's iterative workflow pattern - multi-stage pipelines with autonomous restart coordination, validation retry with fresh agent context, and hybrid context passing for hallucination prevention.


## Table of Contents

- [Quick Reference](#quick-reference)
- [Overview](#overview)
- [Architecture](#architecture)
- [Restart Coordination](#restart-coordination)
- [Validation Retry Mechanism](#validation-retry-mechanism)
- [Stage Output Management](#stage-output-management)
- [Structured Context](#structured-context)
- [SharedMemory Integration](#sharedmemory-integration)
- [Configuration Reference](#configuration-reference)
- [Protocol Messages](#protocol-messages)
- [Execution Flow](#execution-flow)
- [Best Practices](#best-practices)
- [Monitoring](#monitoring)
- [Troubleshooting](#troubleshooting)
- [Error Codes](#error-codes)
- [See Also](#see-also)


## Quick Reference

### Pattern Comparison

| Feature | Pipeline | Iterative Pipeline |
|---------|----------|-------------------|
| **Stage Sequence** | Linear, one-pass | Linear with restart capability |
| **Iterations** | 1 (single pass) | Configurable (default: 3 max) |
| **Restart Coordination** | No | Yes (pub/sub messaging) |
| **Validation Retry** | Yes (via `retry_policy` per stage) | Yes (fresh agent context per retry) |
| **JSON Schema Validation** | Yes (via `output_schema` per stage) | No (uses structural JSON/XML check) |
| **Context Management** | Simple concatenation | Hybrid (truncated + SharedMemory) |
| **Structured Output** | Optional | Recommended (JSON/XML) |
| **Use Case** | Simple workflows | Complex discovery/refinement |


### Quick Start

**Minimal Configuration**:
```yaml
workflow_pattern:
  iterative:
    pipeline:
      initial_prompt: "Start multi-stage discovery"
      stages:
        - agent_id: "discovery"
          prompt_template: "Discover available data"
        - agent_id: "analysis"
          prompt_template: "Analyze: {{previous}}"
    max_iterations: 5
    restart_policy:
      enabled: true
```

**With Restart Coordination**:
```yaml
workflow_pattern:
  iterative:
    pipeline:
      initial_prompt: "Start nPath discovery workflow"
      stages:
        - agent_id: "discover_events"
          prompt_template: "Discover event types in {{table_name}}"
        - agent_id: "build_npath"
          prompt_template: "Build nPath query using: {{previous}}"
    max_iterations: 10
    restart_policy:
      enabled: true
      restartable_stages: ["discover_events"]  # Only discovery can restart
      cooldown_seconds: 5
      preserve_outputs: false
      max_validation_retries: 2
    restart_triggers: ["build_npath"]  # Analysis stage can trigger restarts
    restart_topic: "workflow.restart"
```


### Configuration Parameters Summary

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `max_iterations` | int32 | 3 | Maximum iterations before forcibly stopping |
| `restart_policy.enabled` | bool | false | Enable autonomous restart coordination |
| `restart_policy.restartable_stages` | []string | [] (all) | Which stages can be restarted |
| `restart_policy.cooldown_seconds` | int32 | 0 | Minimum seconds between restarts |
| `restart_policy.reset_shared_memory` | bool | false | Clear SharedMemory on restart |
| `restart_policy.preserve_outputs` | bool | true* | Keep previous iteration outputs (*YAML config default; proto3 wire default is false) |
| `restart_policy.max_validation_retries` | int32 | 2 | Retry count with fresh agent context |
| `restart_triggers` | []string | [] (all) | Which stages can trigger restarts |
| `restart_topic` | string | "workflow.restart" | Pub/sub topic for restart messages |

**Constants** (internal, not configurable):
- `MaxStageOutputBytes`: 8192 bytes (truncation threshold)
- `StageOutputTruncationNoticeTemplate`: SharedMemory reference message


### Restart Coordination Flow

```
Stage 2 (Analysis) executes
     │
     ├─ Detects issue: "Need different event subset"
     │
     ├─ Publishes RestartRequest
     │   • target_stage_id: "stage1"
     │   • reason: "Missing key events"
     │   • iteration: 2
     │
     ▼
IterativePipelineExecutor receives request
     │
     ├─ Validates: Can restart stage1? Yes (in restartable_stages)
     ├─ Validates: Cooldown elapsed? Yes (5s passed)
     ├─ Validates: Within max_iterations? Yes (2 < 10)
     │
     ├─ Clears outputs from stage1 onward
     ├─ Resets SharedMemory (if configured)
     │
     ├─ Publishes RestartResponse (success=true)
     │
     ▼
Restarts from stage1 with iteration=3
```


## Overview

The **Iterative Workflow Pattern** extends the standard pipeline pattern with **autonomous restart capabilities**, enabling agents to refine their approach by restarting earlier stages based on validation results or discovered insights.

**Key Features**:
- ✅ **Autonomous Restart Coordination**: Stages can request restarts of earlier stages via pub/sub messaging
- ✅ **Validation Retry with Fresh Context**: Retry failed validations with new session IDs to reset conversation history
- ✅ **Hybrid Context Passing**: Truncated summaries in prompts + full outputs in SharedMemory for hallucination prevention
- ✅ **Structured Output Parsing**: JSON/XML schemas for evidence-based reasoning
- ✅ **Iteration Protection**: Maximum iteration limit prevents infinite loops
- ✅ **Per-Stage Cooldown**: Rate limiting for restart requests

**Use Cases**:
- **Data Discovery**: Discover schema, refine based on analysis needs
- **nPath Query Building**: Discover events, build pattern, retry if pattern doesn't match data
- **Multi-Step Validation**: Execute, validate, refine parameters, re-execute
- **Iterative Optimization**: Run analysis, detect bottlenecks, adjust strategy, re-run

**Implementation**: `pkg/orchestration/iterative_pipeline_executor.go`
**Available Since**: v1.0.0-beta.2
**Thread Safety**: All operations are thread-safe


## Architecture

### Component Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                  IterativePipelineExecutor                      │
│  ┌───────────────────────────────────────────────────────────┐ │
│  │  Restart Coordination                                     │ │
│  │  • restartRequests chan *RestartRequest                   │ │
│  │  • restartResponses map[string]chan *RestartResponse      │ │
│  │  • lastRestartTime map[string]time.Time (cooldown)        │ │
│  └───────────────────────────────────────────────────────────┘ │
│  ┌───────────────────────────────────────────────────────────┐ │
│  │  Context Management                                       │ │
│  │  • structuredContext *StructuredContext                   │ │
│  │  • stageOutputs map[string]string (in-memory)             │ │
│  │  • MaxStageOutputBytes = 8192 (truncation)                │ │
│  └───────────────────────────────────────────────────────────┘ │
│  ┌───────────────────────────────────────────────────────────┐ │
│  │  Validation Retry                                         │ │
│  │  • max_validation_retries configuration                   │ │
│  │  • Fresh session ID per retry: "wf-retry1", "wf-retry2"   │ │
│  │  • ValidateOutputStructure() parsing                      │ │
│  └───────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│                       MessageBus                                │
│  Pub/Sub Infrastructure for Restart Coordination               │
│  • Subscribe(topic, handler)                                    │
│  • Publish(topic, message)                                      │
│  • Topics: "workflow.restart" (default)                         │
└─────────────────────────────────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│                      SharedMemory                               │
│  Full Stage Output Storage                                      │
│  • Namespace: WORKFLOW                                          │
│  • Keys: "stage-N-output"                                       │
│  • Auto-compression: > 1KB                                      │
│  • Metadata: agent_id, stored_at, full_size                     │
└─────────────────────────────────────────────────────────────────┘
```


### Execution Phases

**Phase 1: Initialization**
```
1. Parse IterativeWorkflowPattern configuration
2. Initialize MessageBus for restart coordination
3. Create restart request/response channels
4. Subscribe to restart topic
5. Initialize StructuredContext for hallucination prevention
6. Set currentIteration = 1
```

**Phase 2: Stage Execution Loop**
```
for currentIteration <= maxIterations {
    for each stage in pipeline.stages {
        1. Build prompt with previous outputs (truncated)
        2. Execute stage with validation retry loop:
           for retry = 0; retry <= max_validation_retries; retry++ {
               a. Create fresh session ID: "wf-retry{N}"
               b. Execute agent with current prompt
               c. Validate output structure (JSON/XML)
               d. If valid: break
               e. If invalid: continue to next retry
           }
        3. Store full output in SharedMemory (WORKFLOW namespace)
        4. Truncate output if > 8KB (add SharedMemory reference)
        5. Update stageOutputs map
        6. Check for restart requests (non-blocking):
           select {
           case req := <-restartRequests:
               - Validate restart request
               - Apply cooldown if configured
               - Clear outputs from target stage onward
               - Reset iteration counter
               - Jump to target stage
           default:
               - Continue to next stage
           }
    }
    currentIteration++
}
```

**Phase 3: Completion**
```
1. Unsubscribe from restart topic
2. Close restart channels
3. Return workflow result with:
   - All stage outputs
   - Total iterations executed
   - Final structured context
   - Execution metadata
```


### Key Design Decisions

**1. Pub/Sub Restart Coordination (Not Direct Method Calls)**

**Why**: Decouples stage execution from restart logic. Stages don't need direct references to executor.

**How**: Stages publish `RestartRequest` to topic, executor subscribes and handles asynchronously.

**Trade-off**: Adds MessageBus dependency, but enables multi-agent coordination (future: stages in different processes).


**2. Validation Retry with Fresh Session IDs**

**Why**: Conversation history can bias retry attempts. Fresh context prevents "doubling down" on errors.

**How**: Each retry uses unique session ID (`wf-retry1`, `wf-retry2`) to reset conversation state.

**Trade-off**: Loses conversation context, but prevents bias. Worth it for validation scenarios.


**3. Hybrid Context Passing (Truncated + SharedMemory)**

**Why**: Balance between token efficiency and hallucination prevention.

**How**:
- Truncate outputs > 8KB in prompt context
- Store full outputs in SharedMemory
- Add reference message: "Full data in shared_memory_read(key='stage-N-output')"

**Trade-off**: Requires SharedMemory implementation, but prevents token bloat and provides escape hatch.


**4. Per-Stage Cooldown (Not Global)**

**Why**: Allow independent restart rates for different stages.

**How**: Track `lastRestartTime` per stage ID, check against `cooldown_seconds`.

**Trade-off**: More complex tracking, but prevents single fast-restarting stage from blocking others.


## Restart Coordination

### Overview

**Restart coordination** enables stages to autonomously request reruns of earlier stages when validation fails or better data is needed. Communication happens via **pub/sub messaging** (MessageBus).

**Roles**:
- **Requester Stage**: Publishes `RestartRequest` message
- **Target Stage**: The stage to be restarted (must be earlier in pipeline)
- **Executor**: Validates and coordinates restart


### RestartRequest Message

**Purpose**: Stage publishes this message to request a restart of an earlier stage.

**Proto Definition**:
```protobuf
message RestartRequest {
  string requester_stage_id = 1;  // Stage requesting restart
  string target_stage_id = 2;     // Stage to be restarted
  string reason = 3;              // Human-readable reason
  map<string, string> parameters = 4;  // Optional params for target
  int32 iteration = 5;            // Current iteration number
  int64 timestamp_ms = 6;         // Request timestamp
}
```

**Fields**:

#### requester_stage_id

**Type**: `string`
**Required**: Yes

**Description**: Agent ID of the stage requesting the restart.

**Example**: `"build_npath"` (analysis stage requesting discovery stage restart)


#### target_stage_id

**Type**: `string`
**Required**: Yes

**Description**: Agent ID of the stage to be restarted.

**Validation**: Must be earlier stage in pipeline (no forward jumps).

**Example**: `"discover_events"` (discovery stage that needs to re-run)


#### reason

**Type**: `string`
**Required**: Yes (for debugging/observability)

**Description**: Human-readable explanation for restart request.

**Example**: `"nPath pattern contains events not found in table - need to rediscover available events"`


#### parameters

**Type**: `map<string, string>`
**Required**: No

**Description**: Optional parameters to pass to restarted stage (e.g., refined constraints).

**Example**:
```yaml
parameters:
  event_filter: "event_type LIKE 'purchase%'"
  min_frequency: "100"
```


#### iteration

**Type**: `int32`
**Required**: Yes

**Description**: Current iteration number when restart was requested.

**Example**: `2` (requesting restart during 2nd iteration)


#### timestamp_ms

**Type**: `int64`
**Required**: Yes

**Description**: Unix timestamp in milliseconds when request was created.

**Use**: Cooldown calculation, observability.


### RestartResponse Message

**Purpose**: Executor sends this message back to requester after restart completes.

**Proto Definition**:
```protobuf
message RestartResponse {
  string target_stage_id = 1;  // Stage that was restarted
  bool success = 2;            // Whether restart succeeded
  string error = 3;            // Error message if failed
  string output = 4;           // Output from restarted stage
  int32 iteration = 5;         // Iteration after restart
}
```

**Fields**:

#### success

**Type**: `bool`
**Required**: Yes

**Description**: Whether restart validation passed and stage was restarted.

**Values**:
- `true`: Restart validated and executed
- `false`: Restart rejected (validation failed or error occurred)


#### error

**Type**: `string`
**Required**: If `success = false`

**Description**: Error message explaining why restart failed.

**Examples**:
- `"cannot restart forward (stage2 -> stage3)"`
- `"cooldown not elapsed (3s remaining)"`
- `"stage not in restartable_stages list"`
- `"max_iterations exceeded"`


#### output

**Type**: `string`
**Required**: If `success = true`

**Description**: Output from the restarted stage after completion.

**Note**: May be truncated (> 8KB). Full output in SharedMemory.


### Restart Validation Rules

**Validation checks performed before executing restart**:

1. **Target Stage Exists**:
   ```go
   targetIndex := findStageIndex(targetStageID)
   if targetIndex < 0 {
       return error: "target stage not found"
   }
   ```

2. **No Forward Restarts**:
   ```go
   if targetIndex >= currentStageIndex {
       return error: "cannot restart forward"
   }
   ```

3. **Restartable Stages Policy**:
   ```go
   if len(restartPolicy.RestartableStages) > 0 {
       if !contains(restartPolicy.RestartableStages, targetStageID) {
           return error: "stage not in restartable_stages list"
       }
   }
   ```

4. **Restart Triggers Policy**:
   ```go
   if len(pattern.RestartTriggers) > 0 {
       if !contains(pattern.RestartTriggers, requesterStageID) {
           return error: "requester not authorized to trigger restarts"
       }
   }
   ```

5. **Cooldown Elapsed**:
   ```go
   if cooldownSeconds > 0 {
       lastRestart := lastRestartTime[targetStageID]
       if time.Since(lastRestart) < cooldownDuration {
           return error: "cooldown not elapsed"
       }
   }
   ```

6. **Max Iterations Not Exceeded**:
   ```go
   if currentIteration >= maxIterations {
       return error: "max_iterations exceeded"
   }
   ```

**All validation checks must pass** for restart to execute.


### Restart Execution

**When restart is approved**:

1. **Update Last Restart Time**:
   ```go
   lastRestartTime[targetStageID] = time.Now()
   ```

2. **Clear Downstream Outputs** (if `preserve_outputs = false`):
   ```go
   for i := targetIndex; i < len(stages); i++ {
       delete(stageOutputs, stages[i].AgentId)
   }
   ```

3. **Reset SharedMemory** (if `reset_shared_memory = true`):
   ```go
   sharedMemory.Reset(ctx, WORKFLOW namespace)
   ```

4. **Jump to Target Stage**:
   ```go
   stageIndex = targetIndex
   currentIteration++
   ```

5. **Send Response**:
   ```go
   response := &RestartResponse{
       TargetStageId: targetStageID,
       Success:       true,
       Iteration:     currentIteration,
   }
   responseChan <- response
   ```


### Publishing Restart Requests

**From Stage Agent** (via tool):

```go
// Publish restart request tool
func PublishRestartRequest(ctx context.Context, req *RestartRequest) error {
    payload, err := json.Marshal(req)
    if err != nil {
        return err
    }

    msg := &loomv1.BusMessage{
        Topic: "workflow.restart",  // From pattern.restart_topic
        Payload: &loomv1.MessagePayload{
            Data: &loomv1.MessagePayload_Value{Value: payload},
        },
    }

    _, _, err = messageBus.Publish(ctx, "workflow.restart", msg)
    return err
}
```

**From External Orchestrator** (programmatic):

```go
restartReq := &loomv1.RestartRequest{
    RequesterStageId: "build_npath",
    TargetStageId:    "discover_events",
    Reason:           "nPath contains undefined event: 'checkout_started'",
    Iteration:        2,
    TimestampMs:      time.Now().UnixMilli(),
}

payload, _ := json.Marshal(restartReq)
msg := &loomv1.BusMessage{
    Topic: "workflow.restart",
    Payload: &loomv1.MessagePayload{
        Data: &loomv1.MessagePayload_Value{Value: payload},
    },
}

_, _, err = messageBus.Publish(ctx, "workflow.restart", msg)
```


## Validation Retry Mechanism

### Overview

**Validation retry** allows stages to retry execution with **fresh agent context** when structured output parsing fails. Each retry uses a new session ID to reset conversation history.

**Key Insight**: Conversation history can bias retry attempts. If an agent fails to produce valid JSON once, retrying with the same conversation context often repeats the same error. Fresh context enables true retry.


### Configuration

```yaml
restart_policy:
  max_validation_retries: 2  # Default: 2 retries (3 total attempts)
```

**Values**:
- `0`: No retries (validation failure = immediate error)
- `1`: 1 retry (2 total attempts)
- `2`: 2 retries (3 total attempts) - **default**
- `3+`: Up to 3 retries (4 total attempts) - **maximum recommended**

**Recommendation**: Use default (2 retries). Higher values risk excessive cost without improvement.


### Validation Process

**Retry Loop**:

```go
maxRetries := pattern.RestartPolicy.MaxValidationRetries
skipValidation := (maxRetries == 0)

for retryNum := 0; retryNum <= maxRetries; retryNum++ {
    // 1. Create retry-specific session ID for fresh context
    retryWorkflowID := workflowID
    if retryNum > 0 {
        retryWorkflowID = fmt.Sprintf("%s-retry%d", workflowID, retryNum)
    }

    // 2. Execute stage with current session ID
    result, model, err := executor.executeStageWithSpan(
        ctx,
        retryWorkflowID,
        stage,
        currentPrompt,
        stageNum,
    )
    if err != nil {
        return nil, "", err  // Execution error (not validation)
    }

    // 3. Skip validation if max_validation_retries is explicitly 0
    if skipValidation {
        return result, model, nil
    }

    // 4. Validate output structure
    validationErr := ValidateOutputStructure(result.Output)
    if validationErr == nil {
        // Validation passed - return immediately
        return result, model, nil
    }

    // 5. Log validation failure and continue to retry
    logger.Warn("validation failed, retrying with fresh context",
        zap.String("workflow_id", workflowID),
        zap.Int("retry", retryNum),
        zap.String("error", validationErr.Error()),
    )

    // Continue to next retry with new session ID...
}

// All retries exhausted - log error and continue workflow with failed validation.
// The workflow is NOT aborted; it proceeds with the last output even if validation failed.
logger.Error("Stage output failed structure validation after max retries",
    zap.String("stage_id", stage.AgentId),
    zap.Int("retries_attempted", maxRetries),
    zap.Error(validationErr),
    zap.String("hint", "Agent must output valid JSON or XML with required structure"))
// Continue workflow with failed validation
```


### Session ID Strategy

**Why Fresh Session IDs?**

**Problem**: Conversation history can reinforce errors.

**Example**:
```
Attempt 1:
Agent: {"event": "purchase", "invalid_field": 123}  ❌ Invalid structure

Retry with same session:
System: "Output validation failed. Try again."
Agent: {"event": "purchase", "invalid_field": "fixed"}  ❌ Still has invalid_field
```

**Solution**: Fresh session ID resets conversation.

```
Attempt 1 (session: "wf-1"):
Agent: {"event": "purchase", "invalid_field": 123}  ❌ Invalid

Retry 1 (session: "wf-1-retry1"):
Agent: {"event": "purchase", "attributes": {...}}  ✅ Valid structure
```


### Session ID Format

**Base Workflow**: `workflowID` (e.g., `"npath-discovery-123"`)

**Retry 1**: `workflowID + "-retry1"` (e.g., `"npath-discovery-123-retry1"`)

**Retry 2**: `workflowID + "-retry2"` (e.g., `"npath-discovery-123-retry2"`)

**Implementation**:
```go
retryWorkflowID := workflowID
if retryNum > 0 {
    retryWorkflowID = fmt.Sprintf("%s-retry%d", workflowID, retryNum)
}
```


### ValidateOutputStructure Function

**Purpose**: Parse and validate structured output (JSON/XML).

**Implementation**: `pkg/orchestration/structured_context.go`

**Supported Formats**:
- **JSON flat format (v3.9)**: `{"stage_id": "...", "outputs": {...}}`
- **JSON nested format (v3.8)**: `{"stage_outputs": {"stage-N": {"stage_id": "...", "outputs": {...}}}}`
- **XML format (v3.10)**: `<stage_output><outputs>...</outputs></stage_output>`

**Pre-processing**:
- Strips `<thinking>...</thinking>` tags before parsing
- Extracts JSON from markdown code blocks (` ```json ... ``` `) or raw `{...}` delimiters

**Validation Rules (flat JSON format)**:

1. **Valid JSON or XML**: Must parse without syntax errors
2. **Required Fields**: `stage_id` and `outputs` must be present
3. **Field Types**: `outputs` must be an object
4. **Evidence Optional**: If `evidence` is present, `evidence.tool_calls` must be an array

**Validation Rules (nested JSON format)**:

1. **Required Fields**: `stage_outputs` must be a non-empty object
2. **Per-stage**: Each stage entry must have `stage_id`, `status`, and `outputs`
3. **Evidence Optional**: Same as flat format

**Example** (flat format validated successfully):
```json
{
  "stage_id": "discover_events",
  "status": "completed",
  "outputs": {
    "events": ["page_view", "add_to_cart", "purchase"]
  }
}
```

**Example** (validation failure):
```json
{
  "stage_id": "discover_events"
}
// Error: missing required field: 'outputs'
```


### Zero-Cost Validation

**Optimization**: Validation checks run **before** LLM execution (when possible).

**Example**: Pre-flight checks in prompt construction:

```go
// Check inputs before calling LLM
if inputs.TableName == "" {
    return error: "table_name required"  // Fail fast, no LLM call
}

// Validate query syntax before execution
if !isValidSQL(query) {
    return error: "invalid SQL syntax"  // Fail fast, no execution
}
```

**When to Use**:
- Input validation (required fields, format checks)
- Syntax validation (SQL, regex patterns)
- Business rule validation (ranges, constraints)

**When NOT to Use**:
- Output structure validation (requires LLM execution first)
- Content quality checks (requires LLM-generated content)


### Disabling Validation Retry

**Explicit Opt-Out**:

```yaml
restart_policy:
  max_validation_retries: 0  # No retries
```

**Behavior**:
- Validation checks skipped entirely
- First execution result returned immediately
- Use when: Output structure not critical, or validation too expensive

**Trade-off**: Faster execution, but risk of downstream errors from malformed output.


## Stage Output Management

### Overview

**Stage output management** balances two competing concerns:

1. **Token Efficiency**: Avoid overwhelming LLM context with large outputs
2. **Hallucination Prevention**: Provide complete data for evidence-based reasoning

**Solution**: **Hybrid approach**:
- Truncate outputs > 8KB in prompt context
- Store full outputs in SharedMemory
- Add reference message for on-demand retrieval


### Output Truncation

**Threshold**: `MaxStageOutputBytes = 8192` (8KB)

**Rationale**:
- 8KB ≈ 2000-3000 tokens (depending on tokenizer)
- Large enough for most summaries
- Small enough to prevent context bloat in multi-stage pipelines

**Implementation**:
```go
const MaxStageOutputBytes = 8192

func truncateStageOutput(output string, maxBytes int, memoryKey string) (string, bool) {
    if len(output) <= maxBytes {
        return output, false  // No truncation needed
    }

    // Try to truncate at a sensible boundary (newline)
    truncated := output[:maxBytes]
    if lastNewline := strings.LastIndex(truncated, "\n"); lastNewline > maxBytes/2 {
        truncated = truncated[:lastNewline]
    }

    // Include reference to SharedMemory key
    notice := fmt.Sprintf(
        StageOutputTruncationNoticeTemplate,
        memoryKey,  // e.g., "stage-1-output"
    )

    return truncated + notice, true
}
```

**Truncation Notice Template**:
```
[OUTPUT TRUNCATED - Full data stored in SharedMemory. Use shared_memory_read(namespace="workflow", key="%s") to fetch complete output]
```

**Example**:
```
Input (12KB):
{
  "events": ["purchase", "add_to_cart", ...],  // 500 events
  "schema": {...},  // Full schema
  "statistics": {...}
}

Truncated Output (8KB):
{
  "events": ["purchase", "add_to_cart", ...],  // First 200 events
  "schema": {...},

[OUTPUT TRUNCATED - Full data stored in SharedMemory. Use shared_memory_read(namespace="workflow", key="stage-1-output") to fetch complete output]
```


### Full Output Storage

**All outputs stored in SharedMemory**, regardless of size.

**Storage Call**:
```go
func (e *IterativePipelineExecutor) storeStageOutputInMemory(
    ctx context.Context,
    key string,      // "stage-1-output", "stage-2-output", etc.
    agentID string,  // Agent that produced output
    output string,   // Full output (no truncation)
) error {
    req := &loomv1.PutSharedMemoryRequest{
        Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
        Key:       key,
        Value:     []byte(output),
        AgentId:   agentID,
        Metadata: map[string]string{
            "type":       "stage_output",
            "agent_id":   agentID,
            "stored_at":  time.Now().Format(time.RFC3339),
            "full_size":  fmt.Sprintf("%d", len(output)),
            "compressed": "auto",  // Auto-compress if > 1KB
        },
    }

    _, err := e.orchestrator.sharedMemory.Put(ctx, req)
    return err
}
```

**Metadata Fields**:
- `type`: Always `"stage_output"`
- `stored_at`: RFC3339 timestamp
- `full_size`: Original size before compression
- `compressed`: `"auto"` (compressed if > 1KB, else plain)


### Stage Key Format

**Format**: `stage-{N}-output`

**Where**:
- `N`: 1-based stage number (not 0-based)

**Examples**:
- Stage 1: `stage-1-output`
- Stage 2: `stage-2-output`
- Stage 10: `stage-10-output`

**Implementation**:
```go
stageNum := stageIndex + 1  // Convert 0-based to 1-based
stageKey := fmt.Sprintf("stage-%d-output", stageNum)
```


### Prompt Context Construction

The pipeline executor's `buildStagePromptWithContext` substitutes template variables in `prompt_template`:

```go
// Replace {{previous}} placeholder with the previous stage's output
if strings.Contains(prompt, "{{previous}}") {
    prompt = strings.ReplaceAll(prompt, "{{previous}}", previousOutput)
}

// Replace {{history}} placeholder with all previous outputs
if strings.Contains(prompt, "{{history}}") {
    history := e.buildHistoryString(allOutputs)
    prompt = strings.ReplaceAll(prompt, "{{history}}", history)
}

// Replace {{structured_context}} placeholder with JSON context
if strings.Contains(prompt, "{{structured_context}}") {
    contextJSON, _ := structuredCtx.ToJSON()
    prompt = strings.ReplaceAll(prompt, "{{structured_context}}", contextJSON)
}
```

**Built-in Template Variables** (substituted by pipeline executor):
- `{{previous}}`: Last stage's output (truncated to 8KB)
- `{{history}}`: All previous outputs formatted with stage numbers
- `{{structured_context}}`: Full structured context as JSON

**User-defined Template Variables** (substituted by `InterpolateVariables` from `ExecuteWorkflowRequest.variables`):
- `{{table_name}}`, `{{database}}`, etc. - any key in the `variables` map

**Note**: There is no `{{stage-N-output}}` template variable. To access specific previous stage outputs, agents should use the `shared_memory_read` tool with key `"stage-N-output"`.


### Retrieval Pattern

**Agent retrieving full output**:

```yaml
tools:
  - name: "shared_memory_read"
    description: "Retrieve full stage output from SharedMemory"
    parameters:
      namespace: "workflow"
      key: "stage-1-output"
```

**Tool Execution**:
```go
func (t *SharedMemoryReadTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
    namespace := params["namespace"].(string)  // "workflow"
    key := params["key"].(string)              // "stage-1-output"

    req := &loomv1.GetSharedMemoryRequest{
        Namespace: parseNamespace(namespace),
        Key:       key,
    }

    resp, err := sharedMemory.Get(ctx, req)
    if err != nil {
        return nil, err
    }
    if !resp.Found {
        return nil, fmt.Errorf("key not found: %s", key)
    }

    return &ToolResult{
        Output: string(resp.Value.Value),  // Full output, decompressed
    }, nil
}
```


### Memory Management

**Namespace Reset** (when `reset_shared_memory = true`):

The actual implementation lists all keys in the WORKFLOW namespace, then deletes each one individually. There is no bulk-delete-by-prefix operation in the `DeleteSharedMemoryRequest` proto.

```go
func (e *IterativePipelineExecutor) resetWorkflowNamespace(ctx context.Context) error {
    // List all keys in the WORKFLOW namespace
    listReq := &loomv1.ListSharedMemoryKeysRequest{
        Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
    }

    listResp, err := e.orchestrator.sharedMemory.List(ctx, listReq)
    if err != nil {
        return fmt.Errorf("failed to list workflow namespace keys: %w", err)
    }

    // Delete each key in the namespace
    for _, key := range listResp.Keys {
        deleteReq := &loomv1.DeleteSharedMemoryRequest{
            Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
            Key:       key,
        }
        _, _ = e.orchestrator.sharedMemory.Delete(ctx, deleteReq)
    }

    return nil
}
```


## Structured Context

### Overview

**Structured context** uses JSON or XML schemas to enforce evidence-based reasoning and prevent hallucinations in multi-stage workflows.

**Key Concept**: Instead of free-form text, stages output structured data with explicit evidence tracking.


### StageOutput Schema

**Standard Format** (flat v3.9):
```json
{
  "stage_id": "discover_events",
  "status": "completed",
  "inputs": {
    "table_name": "web_events",
    "event_column": "event_type"
  },
  "outputs": {
    "events": ["page_view", "add_to_cart", "purchase"],
    "event_count": 3
  },
  "evidence": {
    "tool_calls": [
      {"tool_name": "query_database", "parameters": {"query": "SELECT DISTINCT event_type FROM web_events"}, "result_summary": "3 rows returned"}
    ],
    "queries_executed": ["SELECT DISTINCT event_type FROM web_events"]
  }
}
```

**Note**: The `stage_id` and `outputs` fields are required by `ValidateOutputStructure`. The `inputs`, `evidence`, and `status` fields are optional but recommended.


### Schema Fields

**Note**: `schema_version` is a workflow-level field on `ContextMetadata`, not on individual `StageOutput`. The `StructuredContext.WorkflowContext.SchemaVer` is set to `"1.0"` at workflow initialization.

#### stage_id

**Type**: `string`
**Required**: Yes

**Description**: Agent ID that produced this output. Required by `ValidateOutputStructure`.

**Example**: `"discover_events"`

#### status

**Type**: `string`
**Required**: No (but recommended)

**Description**: Stage completion status. Values: `"completed"`, `"failed"`, `"skipped"`.


#### inputs

**Type**: `object`
**Required**: No (optional, for traceability)

**Description**: Inputs provided to this stage. Not enforced by `ValidateOutputStructure`, but useful for debugging.

**Example**:
```json
"inputs": {
  "table_name": "web_events",
  "user_id_column": "user_id",
  "timestamp_column": "event_time"
}
```


#### outputs

**Type**: `object`
**Required**: Yes (enforced by `ValidateOutputStructure`)

**Description**: Outputs produced by this stage (results). Must be a JSON object.

**Example**:
```json
"outputs": {
  "session_query": "SELECT user_id, SESSIONIZE(...) ...",
  "estimated_sessions": 15000
}
```


#### evidence

**Type**: `object`
**Required**: No (optional but recommended for hallucination prevention)

**Description**: Evidence supporting outputs (tool calls, queries). If present, `ValidateOutputStructure` validates that `tool_calls` is an array.

**Subfields**:

##### tool_calls

**Type**: `array of objects`
**Required**: No (optional but recommended)

**Description**: All tool calls made during stage execution.

**Fields per entry**: `tool_name` (string), `parameters` (object), `result_summary` (string)

**Example**:
```json
"tool_calls": [
  {"tool_name": "get_table_schema", "parameters": {"table": "web_events"}, "result_summary": "Schema with 5 columns"},
  {"tool_name": "query_database", "parameters": {"query": "SELECT DISTINCT ..."}, "result_summary": "3 rows returned"}
]
```


##### queries_executed

**Type**: `array of strings`
**Required**: No (optional)

**Description**: List of queries executed during stage (for cost tracking and traceability).


**Note**: Result summaries are captured per-tool-call in the `result_summary` field of each `tool_calls` entry (see above), not as a separate top-level evidence field.


**Note**: The `reasoning` and `confidence` fields shown in some prompt templates are conventions for agent instructions, not enforced by `ValidateOutputStructure` or present in the Go `StageOutput` struct. Agents may include them in freeform `outputs` data if desired.


### StructuredContext Manager

**Purpose**: Accumulate structured context across stages.

**Implementation**:
```go
// StructuredContext has no mutex -- it is not designed for concurrent access.
// Pipeline stages execute sequentially, so no synchronization is needed.
type StructuredContext struct {
    WorkflowContext ContextMetadata        `json:"workflow_context"`
    StageOutputs    map[string]StageOutput `json:"stage_outputs"`
}

type ContextMetadata struct {
    WorkflowID   string    `json:"workflow_id"`
    WorkflowType string    `json:"workflow_type"`
    SchemaVer    string    `json:"schema_version"`
    StartedAt    time.Time `json:"started_at"`
}

func NewStructuredContext(workflowID, workflowType string) *StructuredContext {
    return &StructuredContext{
        WorkflowContext: ContextMetadata{
            WorkflowID:   workflowID,
            WorkflowType: workflowType,
            SchemaVer:    "1.0",
            StartedAt:    time.Now(),
        },
        StageOutputs: make(map[string]StageOutput),
    }
}

// GetStageOutput retrieves output from a specific stage
func (ctx *StructuredContext) GetStageOutput(stageKey string) (StageOutput, bool) {
    output, exists := ctx.StageOutputs[stageKey]
    return output, exists
}
```


### Prompt Integration

**Instructing agents to use structured output**:

```yaml
stages:
  - agent_id: "discover_events"
    prompt_template: |
      Discover event types in table: {{table_name}}

      **IMPORTANT**: Return output in structured JSON format:
      {
        "stage_id": "discover_events",
        "status": "completed",
        "inputs": { ... },
        "outputs": { ... },
        "evidence": {
          "tool_calls": [
            {"tool_name": "...", "parameters": {...}, "result_summary": "..."}
          ],
          "queries_executed": ["..."]
        }
      }

      Use the schema inspection and query tools to gather evidence.
      Include ALL tool calls in evidence.tool_calls.
```


### Validation Rules

**Enforced by `ValidateOutputStructure()`**:

1. **Valid JSON or XML**: Must parse without syntax errors
2. **Required Fields (flat format)**: `stage_id` and `outputs` must be present
3. **Required Fields (nested format)**: `stage_outputs` with per-stage `stage_id`, `status`, and `outputs`
4. **Evidence Optional**: If `evidence` is present, `evidence.tool_calls` must be an array (but evidence itself is not required)

**Example Validation Failure** (flat format):
```json
{
  "stage_id": "discover_events"
}
// ❌ Error: missing required field: 'outputs'
```

**Valid Output** (flat format):
```json
{
  "stage_id": "discover_events",
  "outputs": {"events": ["purchase", "add_to_cart"]},
  "evidence": {
    "tool_calls": [
      {"tool_name": "query_database", "parameters": {}, "result_summary": "Found 2 events"}
    ]
  }
}
// ✅ stage_id and outputs present, evidence is valid
```


## SharedMemory Integration

### Overview

**SharedMemory** provides cross-stage data sharing with namespace isolation, auto-compression, and lifecycle management.

**Namespace**: `WORKFLOW` (dedicated namespace for iterative workflows)


### WORKFLOW Namespace

**Purpose**: Isolated namespace for workflow stage outputs.

**Lifecycle**: Created at workflow start, deleted at workflow end.

**Scope**: All stages in same workflow can read/write.

**Proto Definition**:
```protobuf
enum SharedMemoryNamespace {
  SHARED_MEMORY_NAMESPACE_UNSPECIFIED = 0;
  SHARED_MEMORY_NAMESPACE_GLOBAL = 1;      // Global namespace accessible to all agents
  SHARED_MEMORY_NAMESPACE_WORKFLOW = 2;    // Workflow-scoped (isolated per workflow instance)
  SHARED_MEMORY_NAMESPACE_SWARM = 3;       // Swarm-scoped (isolated per agent swarm)
  SHARED_MEMORY_NAMESPACE_DEBATE = 4;      // Debate-scoped (isolated per debate session)
  SHARED_MEMORY_NAMESPACE_SESSION = 5;     // Session-scoped (isolated per user session)
  SHARED_MEMORY_NAMESPACE_AGENT = 6;       // Agent-private (isolated per agent instance)
}
```


### Put Operation

**Store Stage Output**:

```go
req := &loomv1.PutSharedMemoryRequest{
    Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
    Key:       "stage-1-output",
    Value:     []byte(fullOutput),
    AgentId:   "discover_events",
    Metadata: map[string]string{
        "type":       "stage_output",
        "stored_at":  "2025-12-11T10:00:00Z",
        "full_size":  "12345",
        "compressed": "auto",
    },
}

resp, err := sharedMemory.Put(ctx, req)
```

**Auto-Compression**:
- Payloads > 1KB automatically compressed (gzip)
- Metadata `"compressed": "auto"` indicates compression used
- Decompression automatic on retrieval


### Get Operation

**Retrieve Stage Output**:

```go
req := &loomv1.GetSharedMemoryRequest{
    Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
    Key:       "stage-1-output",
}

resp, err := sharedMemory.Get(ctx, req)
if err != nil {
    // Handle error
}
if !resp.Found {
    // Key not found
}

fullOutput := string(resp.Value.Value)      // Automatically decompressed
metadata := resp.Value.Metadata             // map[string]string
version := resp.Value.Version               // Optimistic concurrency version
```


### Delete Operation

**Delete Specific Key**:

```go
req := &loomv1.DeleteSharedMemoryRequest{
    Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
    Key:       "stage-1-output",
}

_, err := sharedMemory.Delete(ctx, req)
```

**Clear Namespace** (on restart or completion):

There is no bulk-delete operation. List keys first, then delete each one:

```go
listReq := &loomv1.ListSharedMemoryKeysRequest{
    Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
}
listResp, err := sharedMemory.List(ctx, listReq)

for _, key := range listResp.Keys {
    deleteReq := &loomv1.DeleteSharedMemoryRequest{
        Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
        Key:       key,
    }
    _, _ = sharedMemory.Delete(ctx, deleteReq)
}
```


### List Operation

**Enumerate All Stage Outputs**:

```go
req := &loomv1.ListSharedMemoryKeysRequest{
    Namespace:  loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
    KeyPattern: "stage-*",  // List all stage outputs
}

resp, err := sharedMemory.List(ctx, req)
for _, key := range resp.Keys {
    fmt.Printf("Key: %s\n", key)
}
```

**Output**:
```
Key: stage-1-output
Key: stage-2-output
```

**Note**: `ListSharedMemoryKeysResponse` returns key names only (not values or metadata). To get metadata for a specific key, use a separate `Get` call.


### Metadata Conventions

**Standard Metadata Fields**:

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | Always `"stage_output"` |
| `stored_at` | string | RFC3339 timestamp |
| `full_size` | string | Original size (bytes) before compression |
| `compressed` | string | `"auto"` (compressed) or `"none"` (uncompressed) |
| `agent_id` | string | Agent that produced output |
| `stage_num` | string | Stage number (1-based) |

**Example**:
```json
{
  "type": "stage_output",
  "stored_at": "2025-12-11T10:00:00Z",
  "full_size": "12345",
  "compressed": "auto",
  "agent_id": "discover_events",
  "stage_num": "1"
}
```


### Namespace Reset Strategy

**When to Reset** (`reset_shared_memory = true`):

1. **On Restart**: Clear all outputs to prevent stale data
2. **On Validation Failure**: Clear outputs from failed stage onward
3. **On Workflow Completion**: Clean up namespace

**When NOT to Reset** (`reset_shared_memory = false`):

1. **Preserve Context**: Keep outputs from previous iteration
2. **Incremental Refinement**: Build on previous results

**Configuration**:
```yaml
restart_policy:
  reset_shared_memory: true  # Clear on restart
```


## Configuration Reference

### IterativeWorkflowPattern Message

**Proto Definition**:
```protobuf
message IterativeWorkflowPattern {
  PipelinePattern pipeline = 1;
  int32 max_iterations = 2;
  RestartPolicy restart_policy = 3;
  repeated string restart_triggers = 4;
  string restart_topic = 5;
}
```


### pipeline

**Type**: `PipelinePattern`
**Required**: Yes

**Description**: Base pipeline configuration (stages, initial prompt).

**See**: Proto definition in `proto/loom/v1/orchestration.proto` (`PipelinePattern` message) for pipeline configuration.

**Example**:
```yaml
pipeline:
  initial_prompt: "Start workflow"
  stages:
    - agent_id: "stage1"
      prompt_template: "Execute stage 1"
    - agent_id: "stage2"
      prompt_template: "Execute stage 2: {{previous}}"
```


### max_iterations

**Type**: `int32`
**Default**: `3`
**Range**: `1` - `100`
**Required**: No

**Description**: Maximum number of iterations before forcibly stopping workflow.

**Purpose**: Prevent infinite loops from restart cycles.

**Behavior**:
- Iteration counter increments after each restart
- When `currentIteration > max_iterations`, reject further restarts
- Final result includes `iterations_executed` count

**Example**:
```yaml
max_iterations: 10  # Allow up to 10 restart cycles
```

**Recommendation**:
- Simple workflows: 3-5 iterations
- Complex discovery: 10-20 iterations
- Never use > 100 (likely indicates design issue)


### restart_policy

**Type**: `RestartPolicy`
**Required**: No (defaults to disabled)

**Description**: Restart coordination configuration.

**See**: [RestartPolicy Section](#restartpolicy-message) for complete details.


### restart_triggers

**Type**: `[]string` (array of agent IDs)
**Default**: `[]` (all stages can trigger)
**Required**: No

**Description**: Which stages are authorized to publish restart requests.

**Validation**: If non-empty, only listed stages can trigger restarts. Others' requests rejected.

**Example**:
```yaml
restart_triggers:
  - "analysis_stage"
  - "validation_stage"
# Only these two stages can trigger restarts
```

**Use Case**: Restrict restart authority to validation/analysis stages.


### restart_topic

**Type**: `string`
**Default**: `"workflow.restart"`
**Required**: No

**Description**: Pub/sub topic for restart coordination messages.

**Constraints**:
- Must be unique per workflow (if running multiple workflows concurrently)
- Cannot be empty

**Example**:
```yaml
restart_topic: "npath-discovery.restart"
# Custom topic for nPath workflow
```

**When to Change**: Running multiple iterative workflows in parallel (avoid topic conflicts).


## RestartPolicy Message

**Proto Definition**:
```protobuf
message RestartPolicy {
  bool enabled = 1;
  repeated string restartable_stages = 2;
  int32 cooldown_seconds = 3;
  bool reset_shared_memory = 4;
  bool preserve_outputs = 5;
  int32 max_validation_retries = 6;
}
```


### enabled

**Type**: `bool`
**Default**: `false`
**Required**: No

**Description**: Master switch for restart coordination.

**Behavior**:
- `false`: Restart requests rejected, falls back to standard pipeline
- `true`: Restart coordination active

**Example**:
```yaml
restart_policy:
  enabled: true
```

**⚠️ Important**: When `enabled: false`, pattern behaves exactly like standard `PipelinePattern`.


### restartable_stages

**Type**: `[]string` (array of agent IDs)
**Default**: `[]` (all stages can be restarted)
**Required**: No

**Description**: Which stages can be restarted.

**Validation**: If non-empty, only listed stages can be restart targets. Requests for other stages rejected.

**Example**:
```yaml
restart_policy:
  restartable_stages:
    - "discover_events"
    - "analyze_schema"
# Only these stages can be restarted
```

**Use Case**: Prevent restart of expensive computation stages (e.g., model training).


### cooldown_seconds

**Type**: `int32`
**Default**: `0` (no cooldown)
**Range**: `0` - `3600` (1 hour)
**Required**: No

**Description**: Minimum seconds between restarts of same stage.

**Purpose**: Rate limiting for restart requests.

**Behavior**:
- `0`: No cooldown (restart anytime)
- `N`: Must wait N seconds since last restart of same stage

**Example**:
```yaml
restart_policy:
  cooldown_seconds: 5  # 5 second cooldown
```

**Validation**:
```go
if cooldownSeconds > 0 {
    lastRestart := lastRestartTime[targetStageID]
    elapsed := time.Since(lastRestart)
    if elapsed < time.Duration(cooldownSeconds) * time.Second {
        return error: "cooldown not elapsed"
    }
}
```

**Recommendation**: Use 5-10 seconds for discovery stages, 0 for validation stages.


### reset_shared_memory

**Type**: `bool`
**Default**: `false`
**Required**: No

**Description**: Whether to clear SharedMemory WORKFLOW namespace on restart.

**Behavior**:
- `false`: Preserve SharedMemory contents across restarts
- `true`: Delete all keys in WORKFLOW namespace on restart

**Example**:
```yaml
restart_policy:
  reset_shared_memory: true  # Clear SharedMemory on restart
```

**Use Case**:
- `true`: Fresh discovery (e.g., rediscovering schema from scratch)
- `false`: Incremental refinement (e.g., adjusting parameters, not re-discovering)


### preserve_outputs

**Type**: `bool`
**Default**: `true` (applied by YAML config layer; proto3 wire default is `false`)
**Required**: No

**Description**: Whether to keep previous iteration's stage outputs in `stageOutputs` map.

**Behavior**:
- `true`: Keep outputs from previous iteration (available via `{{stage-N-output}}`)
- `false`: Clear outputs from restarted stage onward

**Example**:
```yaml
restart_policy:
  preserve_outputs: false  # Clear outputs on restart
```

**Use Case**:
- `true`: Comparison between iterations (e.g., "previous attempt found X, current found Y")
- `false`: Fresh start (e.g., avoid confusion from stale outputs)


### max_validation_retries

**Type**: `int32`
**Default**: `2`
**Range**: `0` - `3`
**Required**: No

**Description**: Number of retries with fresh agent context when validation fails.

**Behavior**:
- `0`: No retries (validation failure = immediate error)
- `N`: Retry N times with fresh session IDs

**Example**:
```yaml
restart_policy:
  max_validation_retries: 2  # 3 total attempts (1 initial + 2 retries)
```

**See**: [Validation Retry Mechanism](#validation-retry-mechanism) section for details.


## Protocol Messages

### BusMessage

**Purpose**: Wrapper for pub/sub messages on MessageBus.

**Proto Definition**:
```protobuf
message BusMessage {
  string id = 1;                      // Unique message ID for tracking and deduplication
  string topic = 2;                   // Topic this message is published to
  string from_agent = 3;              // Source agent ID that published this message
  MessagePayload payload = 4;         // Message payload (value or reference)
  map<string, string> metadata = 5;   // Arbitrary key/value pairs for filtering and routing
  int64 timestamp = 6;                // Publish timestamp (Unix milliseconds)
  int32 ttl_seconds = 7;              // Message TTL in seconds (0 = no expiry)
}
```

**Example** (publishing RestartRequest):
```go
restartReq := &loomv1.RestartRequest{...}
payload, _ := json.Marshal(restartReq)

msg := &loomv1.BusMessage{
    Id:    uuid.NewString(),
    Topic: "workflow.restart",
    Payload: &loomv1.MessagePayload{
        Data: &loomv1.MessagePayload_Value{Value: payload},
    },
    Timestamp: time.Now().UnixMilli(),
}

_, _, err = messageBus.Publish(ctx, "workflow.restart", msg)
```


## Execution Flow

### Complete Execution Diagram

```
START: ExecutePattern(IterativeWorkflowPattern)
     │
     ├─ 1. Initialize
     │   • Parse pattern configuration
     │   • Create MessageBus for restart coordination
     │   • Subscribe to restart topic
     │   • Initialize StructuredContext
     │   • Set currentIteration = 1
     │
     ▼
┌────────────────────────────────────────────────────────────┐
│              Iteration Loop                                │
│  (while currentIteration <= max_iterations)                │
│                                                            │
│  ┌──────────────────────────────────────────────────────┐ │
│  │  Stage Loop                                          │ │
│  │  (for each stage in pipeline.stages)                 │ │
│  │                                                      │ │
│  │  ┌────────────────────────────────────────────────┐ │ │
│  │  │  2. Build Prompt                               │ │ │
│  │  │  • Start with stage.prompt_template            │ │ │
│  │  │  • Substitute {{previous}} with last output    │ │ │
│  │  │  • Substitute {{stage-N-output}} placeholders  │ │ │
│  │  └────────────────────────────────────────────────┘ │ │
│  │                    │                                 │ │
│  │                    ▼                                 │ │
│  │  ┌────────────────────────────────────────────────┐ │ │
│  │  │  3. Validation Retry Loop                      │ │ │
│  │  │  (retry = 0; retry <= max_validation_retries)  │ │ │
│  │  │                                                 │ │ │
│  │  │  • Create retry session ID: "wf-retry{N}"      │ │ │
│  │  │  • Execute agent with current prompt           │ │ │
│  │  │  • Parse output structure (JSON/XML)           │ │ │
│  │  │  • Validate required fields                    │ │ │
│  │  │  • If valid: break                             │ │ │
│  │  │  • If invalid: retry with fresh session        │ │ │
│  │  └────────────────────────────────────────────────┘ │ │
│  │                    │                                 │ │
│  │                    ▼                                 │ │
│  │  ┌────────────────────────────────────────────────┐ │ │
│  │  │  4. Store Output                               │ │ │
│  │  │  • Store full output in SharedMemory           │ │ │
│  │  │  • Truncate if > 8KB (add reference message)   │ │ │
│  │  │  • Update stageOutputs map                     │ │ │
│  │  │  • Update StructuredContext                    │ │ │
│  │  └────────────────────────────────────────────────┘ │ │
│  │                    │                                 │ │
│  │                    ▼                                 │ │
│  │  ┌────────────────────────────────────────────────┐ │ │
│  │  │  5. Check for Restart Requests (non-blocking)  │ │ │
│  │  │                                                 │ │ │
│  │  │  select {                                       │ │ │
│  │  │  case req := <-restartRequests:                │ │ │
│  │  │      • Validate restart request                │ │ │
│  │  │      • Check cooldown                          │ │ │
│  │  │      • Clear outputs from target onward        │ │ │
│  │  │      • Reset SharedMemory (if configured)      │ │ │
│  │  │      • Jump to target stage                    │ │ │
│  │  │      • Increment currentIteration              │ │ │
│  │  │      • Send RestartResponse (success)          │ │ │
│  │  │  default:                                       │ │ │
│  │  │      • Continue to next stage                  │ │ │
│  │  │  }                                              │ │ │
│  │  └────────────────────────────────────────────────┘ │ │
│  │                                                      │ │
│  └──────────────────────────────────────────────────────┘ │
│                                                            │
│  currentIteration++                                        │
│                                                            │
└────────────────────────────────────────────────────────────┘
     │
     ▼
COMPLETE: Return WorkflowResult
     • All stage outputs
     • Iterations executed
     • Structured context
     • Execution metadata
```


### Stage Execution Detailed Flow

```
ExecuteStage(stage, currentPrompt)
     │
     ├─ 1. Create span for observability
     │   tracer.StartSpan("workflow.stage.execute")
     │
     ├─ 2. Validation retry loop
     │   for retry = 0; retry <= max_validation_retries; retry++ {
     │
     │       ├─ 2a. Generate retry session ID
     │       │   if retry > 0:
     │       │       sessionID = workflowID + "-retry" + retry
     │
     │       ├─ 2b. Execute agent
     │       │   result, model, err := agent.Chat(ctx, sessionID, currentPrompt)
     │       │   if err != nil:
     │       │       return err  // Execution error (not validation)
     │
     │       ├─ 2c. Skip validation if explicitly disabled
     │       │   if max_validation_retries == 0:
     │       │       return result  // No validation
     │
     │       ├─ 2d. Validate output structure
     │       │   validationErr := ValidateOutputStructure(result.Output)
     │       │   if validationErr == nil:
     │       │       return result  // Validation passed
     │
     │       └─ 2e. Log validation failure and retry
     │           logger.Warn("validation failed, retrying with fresh context")
     │   }
     │
     ├─ 3. All retries exhausted
     │   Log error and continue workflow (does NOT return error)
     │
     └─ 4. End span
```


## Best Practices

### 1. Use Restarts for Discovery, Not Correction

**Good** (discovery pattern):
```yaml
# Discover events → Build nPath → If events missing, rediscover
stages:
  - agent_id: "discover_events"
    prompt_template: "Discover event types in {{table}}"
  - agent_id: "build_npath"
    prompt_template: |
      Build nPath query using events: {{previous}}
      If nPath contains undefined events, publish restart request to rediscover
```

**Bad** (correction pattern):
```yaml
# Execute → Validate → If wrong, restart execution ❌
# Use validation retry instead of restart for corrections
```

**Rationale**: Restarts are for "I need different data". Validation retries are for "I need to fix my output".


### 2. Set Appropriate max_iterations

**Conservative** (simple workflows):
```yaml
max_iterations: 3  # 1 initial + 2 restart cycles
```

**Moderate** (discovery workflows):
```yaml
max_iterations: 10  # Allow multiple refinement cycles
```

**Liberal** (complex iterative):
```yaml
max_iterations: 20  # High complexity, many potential restarts
```

**Never**:
```yaml
max_iterations: 100  # ❌ Likely indicates design problem
```


### 3. Configure Cooldowns for Expensive Stages

**Fast stages** (schema inspection):
```yaml
restart_policy:
  cooldown_seconds: 0  # No cooldown needed
```

**Medium stages** (query execution):
```yaml
restart_policy:
  cooldown_seconds: 5  # Prevent thrashing
```

**Slow stages** (model training):
```yaml
restart_policy:
  cooldown_seconds: 60  # Rate limit expensive operations
```


### 4. Use preserve_outputs = false for Fresh Restarts

**Incremental refinement**:
```yaml
restart_policy:
  preserve_outputs: true  # Keep previous outputs for comparison
```

**Fresh discovery**:
```yaml
restart_policy:
  preserve_outputs: false  # Clear stale outputs
```


### 5. Limit Restartable Stages

**All stages restartable** (default):
```yaml
restart_policy:
  restartable_stages: []  # Any stage can be restarted
```

**Restricted restarts** (recommended):
```yaml
restart_policy:
  restartable_stages:
    - "discover_events"  # Only discovery can restart
# Prevents restart of expensive stages (analysis, model training)
```


### 6. Use Structured Output for Complex Workflows

**Enable validation**:
```yaml
restart_policy:
  max_validation_retries: 2  # Validate and retry
```

**Instruct agents**:
```yaml
stages:
  - agent_id: "discovery"
    prompt_template: |
      Discover events in table.

      **CRITICAL**: Return output in this JSON structure:
      {
        "schema_version": "1.0",
        "inputs": {...},
        "outputs": {...},
        "evidence": {...}
      }
```


### 7. Monitor Iteration Counts

**Track iterations**:
```go
result, err := executor.Execute(ctx)
if result.IterationsExecuted > 5 {
    logger.Warn("high iteration count",
        zap.String("workflow_id", workflowID),
        zap.Int("iterations", result.IterationsExecuted),
    )
}
```

**Alert on max_iterations reached**:
```go
if result.IterationsExecuted >= pattern.MaxIterations {
    logger.Error("workflow hit max iterations",
        zap.String("workflow_id", workflowID),
        zap.Int("max_iterations", pattern.MaxIterations),
    )
    // Alert operations team
}
```


### 8. Test Restart Coordination

**Unit test**:
```go
func TestIterativePipeline_RestartCoordination(t *testing.T) {
    // Create MessageBus
    messageBus := communication.NewMessageBus(...)

    // Execute workflow in background
    go func() {
        executor := NewIterativePipelineExecutor(orchestrator, pattern, messageBus, "test-workflow-123")
        result, err := executor.Execute(ctx)
        // ...
    }()

    // Publish restart request (wrapped in BusMessage)
    time.Sleep(100 * time.Millisecond)  // Wait for subscription
    restartReq := &loomv1.RestartRequest{
        RequesterStageId: "stage2",
        TargetStageId:    "stage1",
        Reason:           "Need different data",
        Iteration:        1,
        TimestampMs:      time.Now().UnixMilli(),
    }
    payload, _ := json.Marshal(restartReq)
    msg := &loomv1.BusMessage{
        Topic: "workflow.restart",
        Payload: &loomv1.MessagePayload{
            Data: &loomv1.MessagePayload_Value{Value: payload},
        },
    }
    _, _, _ = messageBus.Publish(ctx, "workflow.restart", msg)

    // Verify restart executed
    // ...
}
```


### 9. Use SharedMemory for Large Outputs

**Store full outputs**:
```go
// Always store full output in SharedMemory
storeStageOutputInMemory(ctx, "stage-1-output", agentID, fullOutput)
```

**Reference in prompts**:
```yaml
stages:
  - agent_id: "analysis"
    prompt_template: |
      Analyze data from discovery stage.

      **Full data available**: Use shared_memory_read(namespace="workflow", key="stage-1-output")
      to retrieve complete output if needed.

      **Summary**: {{previous}}
```


### 10. Clean Up SharedMemory on Completion

**Cleanup** (list keys then delete each):
```go
defer func() {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    listResp, err := sharedMemory.List(ctx, &loomv1.ListSharedMemoryKeysRequest{
        Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
    })
    if err != nil {
        return
    }

    for _, key := range listResp.Keys {
        _, _ = sharedMemory.Delete(ctx, &loomv1.DeleteSharedMemoryRequest{
            Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
            Key:       key,
        })
    }
}()
```


## Monitoring

### Key Metrics

| Metric | Type | Description | Alert Threshold |
|--------|------|-------------|-----------------|
| `workflow_iterations_executed` | Gauge | Iterations per workflow | > 10 |
| `workflow_restart_requests_total` | Counter | Total restart requests | - |
| `workflow_restart_requests_rejected` | Counter | Rejected restart requests | > 10% of total |
| `workflow_validation_retries_total` | Counter | Validation retries | - |
| `workflow_validation_failures_total` | Counter | Final validation failures | > 5% |
| `workflow_max_iterations_reached` | Counter | Workflows hitting max limit | > 0 |
| `workflow_stage_output_size_bytes` | Histogram | Stage output sizes | > 100KB |
| `workflow_stage_duration_seconds` | Histogram | Stage execution time | > 60s |
| `workflow_restart_cooldown_rejections` | Counter | Restarts rejected by cooldown | - |


### Observability Integration

**Hawk Tracing**:
```go
// Workflow span
ctx, span := tracer.StartSpan(ctx, "workflow.iterative.execute")
span.SetAttribute("workflow_id", workflowID)
span.SetAttribute("max_iterations", pattern.MaxIterations)

defer func() {
    span.SetAttribute("iterations_executed", currentIteration)
    tracer.EndSpan(span)
}()

// Stage spans
ctx, stageSpan := tracer.StartSpan(ctx, "workflow.stage.execute")
stageSpan.SetAttribute("stage_id", stage.AgentId)
stageSpan.SetAttribute("stage_num", stageNum)
// ...
tracer.EndSpan(stageSpan)

// Restart spans
ctx, restartSpan := tracer.StartSpan(ctx, "workflow.restart")
restartSpan.SetAttribute("requester_stage", req.RequesterStageId)
restartSpan.SetAttribute("target_stage", req.TargetStageId)
restartSpan.SetAttribute("reason", req.Reason)
// ...
tracer.EndSpan(restartSpan)
```


### Logging Best Practices

```go
// Log iteration start
logger.Info("iteration_start",
    zap.String("workflow_id", workflowID),
    zap.Int("iteration", currentIteration),
    zap.Int("max_iterations", maxIterations),
)

// Log stage execution
logger.Info("stage_execute",
    zap.String("workflow_id", workflowID),
    zap.String("stage_id", stage.AgentId),
    zap.Int("stage_num", stageNum),
    zap.Int("retry", retryNum),
)

// Log restart request
logger.Info("restart_request_received",
    zap.String("workflow_id", workflowID),
    zap.String("requester", req.RequesterStageId),
    zap.String("target", req.TargetStageId),
    zap.String("reason", req.Reason),
)

// Log validation failure
logger.Warn("validation_failed",
    zap.String("workflow_id", workflowID),
    zap.String("stage_id", stage.AgentId),
    zap.Int("retry", retryNum),
    zap.String("error", validationErr.Error()),
)

// Log max iterations reached
logger.Error("max_iterations_reached",
    zap.String("workflow_id", workflowID),
    zap.Int("iterations", currentIteration),
    zap.Int("max_iterations", maxIterations),
)
```


## Troubleshooting

### Issue: Workflow Hits max_iterations

**Symptoms**:
- Workflow terminates with "max iterations exceeded" error
- High iteration count in logs
- Restart requests continue after limit reached

**Diagnosis**:
```go
// Check iteration count in result
result, err := executor.Execute(ctx)
if err != nil && strings.Contains(err.Error(), "max iterations") {
    fmt.Printf("Workflow hit max iterations: %d\n", result.IterationsExecuted)
}
```

**Causes**:
1. Restart cycle without convergence (e.g., discovery keeps finding different data)
2. max_iterations too low for workflow complexity
3. Stages repeatedly requesting same restart

**Resolution**:

1. **Increase max_iterations** (if workflow legitimately needs more):
   ```yaml
   max_iterations: 20  # Increase from default 3
   ```

2. **Add convergence detection**:
   ```go
   // Track stage outputs across iterations
   previousOutputs := make(map[int]string)

   // Compare with previous iteration
   if previousOutputs[stageNum] == currentOutput {
       // Converged - stop requesting restarts
       break
   }
   ```

3. **Add cooldown**:
   ```yaml
   restart_policy:
     cooldown_seconds: 10  # Prevent rapid restart cycles
   ```


### Issue: Restart Requests Rejected

**Symptoms**:
- Restart requests published but not executed
- Error logs: "cannot restart forward", "stage not in restartable_stages", "cooldown not elapsed"

**Diagnosis**:
```bash
# Check logs for rejection reasons
grep "restart_request_rejected" loom.log
```

**Causes**:

1. **Forward Restart Attempt**:
   ```
   Error: cannot restart forward (stage2 -> stage3)
   ```
   **Resolution**: Can only restart earlier stages. Fix requester logic.

2. **Stage Not Restartable**:
   ```
   Error: stage 'expensive_stage' not in restartable_stages list
   ```
   **Resolution**: Add to `restartable_stages`:
   ```yaml
   restart_policy:
     restartable_stages:
       - "expensive_stage"
   ```

3. **Cooldown Not Elapsed**:
   ```
   Error: cooldown not elapsed (5s remaining)
   ```
   **Resolution**: Wait for cooldown or reduce `cooldown_seconds`.

4. **Requester Not Authorized**:
   ```
   Error: stage 'discovery' not in restart_triggers list
   ```
   **Resolution**: Add to `restart_triggers`:
   ```yaml
   restart_triggers:
     - "discovery"
   ```


### Issue: Validation Retries Exhausted

**Symptoms**:
- Stage fails with "validation failed after N retries" error
- High validation failure rate in metrics
- Agents not producing structured output

**Diagnosis**:
```bash
# Check validation errors
grep "validation_failed" loom.log

# Check retry count
grep "retry_num" loom.log | tail -20
```

**Causes**:

1. **Agent Not Following Structured Output Format**:
   ```
   Error: invalid JSON: unexpected token at line 1
   ```
   **Resolution**: Improve prompt instructions:
   ```yaml
   prompt_template: |
     **CRITICAL**: Output MUST be valid JSON in this exact format:
     {
       "schema_version": "1.0",
       "inputs": {...},
       "outputs": {...},
       "evidence": {...}
     }

     Do not include any text before or after the JSON.
   ```

2. **Missing Required Fields**:
   ```
   Error: missing required field: evidence
   ```
   **Resolution**: Add field requirement to prompt:
   ```yaml
   prompt_template: |
     Include "evidence" field with all tool calls made.
   ```

3. **Schema Version Mismatch**:
   ```
   Error: unsupported schema_version: 2.0
   ```
   **Resolution**: Update schema version in prompt or validator.


### Issue: High Iteration Count

**Symptoms**:
- Workflows consistently use > 5 iterations
- Discovery stages repeatedly restarted
- High costs from repeated LLM calls

**Diagnosis**:
```go
// Track average iterations per workflow
totalIterations := 0
workflowCount := 0

for _, result := range results {
    totalIterations += result.IterationsExecuted
    workflowCount++
}

avgIterations := float64(totalIterations) / float64(workflowCount)
fmt.Printf("Average iterations: %.2f\n", avgIterations)
```

**Causes**:
1. Discovery stages finding inconsistent data
2. Analysis stages requesting restarts too frequently
3. No convergence criteria

**Resolution**:

1. **Add convergence detection**:
   ```go
   // Stop requesting restarts after outputs stabilize
   if currentOutput == previousOutput {
       // Converged - no restart needed
   }
   ```

2. **Increase cooldown**:
   ```yaml
   restart_policy:
     cooldown_seconds: 30  # Slow down restart rate
   ```

3. **Limit restartable stages**:
   ```yaml
   restart_policy:
     restartable_stages:
       - "discover_events"  # Only discovery can restart
   ```

4. **Lower max_iterations**:
   ```yaml
   max_iterations: 5  # Force earlier termination
   ```


### Issue: SharedMemory Not Accessible

**Symptoms**:
- Error: "shared_memory_read failed: key not found"
- Truncation messages in output but retrieval fails
- Stages cannot access previous outputs

**Diagnosis**:

Use the `shared_memory_read` tool from within an agent, or check logs for SharedMemory errors:
```bash
# Check logs for SharedMemory errors
grep "SharedMemory" loom.log
grep "shared_memory" loom.log
```

**Causes**:

1. **Namespace Reset Between Stages**:
   ```
   Error: key 'stage-1-output' not found after restart
   ```
   **Resolution**: Set `reset_shared_memory: false`:
   ```yaml
   restart_policy:
     reset_shared_memory: false  # Preserve across restarts
   ```

2. **Wrong Key Format**:
   ```
   Error: key 'stage_1_output' not found (should be 'stage-1-output')
   ```
   **Resolution**: Use correct key format: `stage-{N}-output` (1-based, hyphenated).

3. **Workflow Namespace Not Initialized**:
   ```
   Error: namespace 'WORKFLOW' does not exist
   ```
   **Resolution**: Ensure SharedMemory initialized before workflow execution:
   ```go
   executor := NewIterativePipelineExecutor(orchestrator, pattern, messageBus, "my-workflow-id")
   // Executor initializes WORKFLOW namespace automatically
   ```


## Error Codes

### ERR_MAX_ITERATIONS_EXCEEDED

**Code**: `max_iterations_exceeded`
**HTTP Status**: 500 Internal Server Error
**gRPC Code**: `DEADLINE_EXCEEDED`

**Cause**: Workflow reached maximum iteration limit.

**Example**:
```
Error: max iterations exceeded (10 iterations), workflow terminated
```

**Resolution**:
1. Increase `max_iterations` if workflow legitimately needs more
2. Add convergence detection to stop requesting restarts
3. Review restart request logic for infinite loops

**Retry behavior**: Not retryable (workflow design issue)


### ERR_RESTART_VALIDATION_FAILED

**Code**: `restart_validation_failed`
**HTTP Status**: 400 Bad Request
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Restart request validation failed.

**Example**:
```
Error: restart validation failed: cannot restart forward (stage2 -> stage3)
```

**Resolution**:
1. Check target stage is earlier in pipeline
2. Verify target stage in `restartable_stages` list
3. Ensure cooldown elapsed
4. Confirm requester in `restart_triggers` list

**Retry behavior**: Not retryable until conditions fixed


### ERR_VALIDATION_RETRY_EXHAUSTED

**Code**: `validation_retry_exhausted`

**Cause**: All validation retries failed for stage.

**Behavior**: The workflow does NOT abort. It logs an error and continues with the last (invalid) output. This is a non-fatal condition -- downstream stages receive the unvalidated output.

**Log Example**:
```
ERROR: Stage output failed structure validation after max retries (stage_id=discover_events, retries_attempted=2)
```

**Resolution**:
1. Improve prompt instructions for structured output
2. Increase `max_validation_retries` if needed
3. Fix validation logic if too strict

**Note**: This is logged as an error, not returned as an RPC error to the caller.


### ERR_RESTART_COOLDOWN_NOT_ELAPSED

**Code**: `restart_cooldown_not_elapsed`
**HTTP Status**: 429 Too Many Requests
**gRPC Code**: `RESOURCE_EXHAUSTED`

**Cause**: Cooldown period not elapsed since last restart.

**Example**:
```
Error: cooldown not elapsed (5s remaining)
```

**Resolution**:
1. Wait for cooldown period
2. Reduce `cooldown_seconds` if too aggressive

**Retry behavior**: Retryable after cooldown elapsed


### ERR_STAGE_NOT_RESTARTABLE

**Code**: `stage_not_restartable`
**HTTP Status**: 403 Forbidden
**gRPC Code**: `PERMISSION_DENIED`

**Cause**: Target stage not in `restartable_stages` list.

**Example**:
```
Error: stage 'expensive_computation' not in restartable_stages list
```

**Resolution**:
1. Add stage to `restartable_stages`:
   ```yaml
   restart_policy:
     restartable_stages:
       - "expensive_computation"
   ```

**Retry behavior**: Not retryable until configuration updated


### ERR_REQUESTER_NOT_AUTHORIZED

**Code**: `requester_not_authorized`
**HTTP Status**: 403 Forbidden
**gRPC Code**: `PERMISSION_DENIED`

**Cause**: Requester stage not in `restart_triggers` list.

**Example**:
```
Error: stage 'discovery' not authorized to trigger restarts
```

**Resolution**:
1. Add requester to `restart_triggers`:
   ```yaml
   restart_triggers:
     - "discovery"
   ```

**Retry behavior**: Not retryable until configuration updated


### ERR_SHARED_MEMORY_KEY_NOT_FOUND

**Code**: `shared_memory_key_not_found`
**HTTP Status**: 404 Not Found
**gRPC Code**: `NOT_FOUND`

**Cause**: Stage output key not found in SharedMemory.

**Example**:
```
Error: shared memory key not found: stage-1-output
```

**Resolution**:
1. Verify key format: `stage-{N}-output` (1-based)
2. Check `reset_shared_memory` not clearing needed data
3. Ensure stage execution completed before retrieval

**Retry behavior**: Retryable after stage execution completes


## See Also

### Reference Documentation
- [Pattern Reference](./patterns.md) - Pattern library system
- [Agent Configuration Reference](./agent-configuration.md) - Agent configuration options
- [Tool Registry Reference](./tool-registry.md) - Tool system and registration

### Guides
- [Pattern Library Guide](../guides/pattern-library-guide.md) - Building domain patterns
- [Structured Context Pattern Guide](../guides/structured-context-pattern.md) - Structured context usage

### Architecture Documentation
- [Agent System Design](../architecture/agent-system-design.md) - Agent conversation loops
- [Communication System Design](../architecture/communication-system-design.md) - MessageBus pub/sub system
- [Multi-Agent Architecture](../architecture/multi-agent.md) - Workflow pattern design and orchestration
- [Memory Systems](../architecture/memory-systems.md) - SharedMemory architecture

### External Resources
- [Pub/Sub Pattern](https://en.wikipedia.org/wiki/Publish%E2%80%93subscribe_pattern) - Messaging architecture
- [Iterative Methods](https://en.wikipedia.org/wiki/Iterative_method) - Mathematical iterative algorithms

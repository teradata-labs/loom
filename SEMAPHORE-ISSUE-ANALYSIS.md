# Semaphore Issue Analysis - Orchestration Patterns

## Issue Summary

**Problem**: The LLM concurrency semaphore does NOT protect orchestration pattern workflows, leading to:
1. Uncontrolled parallel LLM calls (3+ simultaneous calls possible)
2. Circuit breakers triggering (output token limit, timeouts)
3. Potential rate limiting from LLM provider

**Root Cause**: Semaphore is only implemented for communication patterns, not orchestration patterns.

## Code Analysis

### Semaphore Configuration

**Location**: `cmd/looms/cmd_serve.go:1250`
```go
// Set LLM concurrency limit to prevent rate limiting
loomService.SetLLMConcurrencyLimit(2)
logger.Info("LLM concurrency limit configured to prevent rate limiting", zap.Int("limit", 2))
```

**Implementation**: `pkg/server/multi_agent.go:102-104`
```go
// LLM concurrency control to prevent rate limiting
llmSemaphore        chan struct{} // Semaphore to limit concurrent LLM calls
llmConcurrencyLimit int           // Max concurrent LLM calls (configurable)
```

### Where Semaphore IS Used ✅

The semaphore is correctly acquired/released in **communication patterns**:

1. **Hub-and-Spoke Message Injection** (`pkg/server/multi_agent.go:1043`)
```go
// Acquire semaphore to limit concurrent LLM calls
s.llmSemaphore <- struct{}{}
_, err = coordinatorAgent.Chat(context.Background(), sessionID, injectedPrompt)
<-s.llmSemaphore  // Release
```

2. **Pub-Sub Broadcast Injection** (`pkg/server/multi_agent.go:1393`)
```go
select {
case s.llmSemaphore <- struct{}{}:
    // Acquired
case <-time.After(30 * time.Second):
    s.logger.Error("Timeout acquiring semaphore...")
}
_, err := coordinatorAgent.Chat(...)
<-s.llmSemaphore  // Release
```

3. **Workflow Sub-Agent Polling** (`pkg/server/multi_agent.go:1793`)
```go
s.llmSemaphore <- struct{}{}
_, err := ag.Chat(ctx, sessionID, checkPrompt)
<-s.llmSemaphore
```

### Where Semaphore IS NOT Used ❌

**Orchestration Patterns** have NO semaphore protection:

#### Fork-Join Pattern (`pkg/orchestration/fork_join_executor.go:131-169`)
```go
// Launch goroutine for each agent
for idx, agentID := range e.pattern.AgentIds {
    wg.Add(1)
    go func(branchIdx int, id string) {
        defer wg.Done()

        // ❌ NO SEMAPHORE ACQUISITION HERE!
        result, model, err := e.executeAgentWithSpan(branchCtx, workflowID, id, e.pattern.Prompt, branchIdx+1)

        if err != nil {
            errorsChan <- fmt.Errorf("agent %s failed: %w", id, err)
        }
        resultsChan <- result
    }(idx, agentID)
}

// Wait for all goroutines to complete
wg.Wait()
```

**Problem**: All agents launch **simultaneously** in goroutines without semaphore control.

**Example**: Fork-join with 3 agents (quality, security, performance):
- All 3 agents call `Chat()` → `runConversationLoop()` → LLM simultaneously
- Semaphore limit of 2 is **ignored**
- 3 concurrent LLM calls happen anyway

#### Parallel Pattern (`pkg/orchestration/parallel_executor.go`)
Same issue - launches all tasks in parallel goroutines without semaphore.

#### Pipeline Pattern (`pkg/orchestration/pipeline_executor.go`)
Sequential execution, so less critical, but still no protection if pipeline stages themselves spawn parallel work.

## Test Evidence

### Fork-Join Test (Working but Unprotected)
```bash
$ ./bin/looms workflow run examples/reference/workflows/code-review.yaml

✅ WORKFLOW COMPLETED
Duration: 7.09s
Total cost: $0.0836
Total tokens: 25,108
LLM calls: 3  # ← All 3 happened in parallel, no semaphore control!
```

**What happened**: quality, security, performance agents all called LLM simultaneously, bypassing the concurrency limit of 2.

### Pipeline Test (Hit Circuit Breaker)
```bash
$ ./bin/looms workflow run examples/reference/workflows/feature-pipeline.yaml

❌ Execution failed: output token circuit breaker:

🔴 OUTPUT TOKEN CIRCUIT BREAKER TRIGGERED

The model has hit the output token limit 3 times in a row.
```

**What happened**:
1. api-architect agent tried to generate full authentication system design
2. Hit 8,192 token output limit (model max)
3. Retried 3 times (circuit breaker threshold)
4. Circuit breaker correctly stopped the loop

**Why semaphore didn't help**: Pipeline is sequential, but even if parallel, semaphore isn't used in orchestration code path.

### Debate Test (Timeout)
```bash
$ ./bin/looms workflow run examples/reference/workflows/architecture-debate.yaml

❌ Execution failed: context deadline exceeded
Round 2 completed successfully
Round 3 failed: agent execution failed
```

**What happened**:
1. Debate requires multiple LLM calls per round (2 debaters + 1 moderator)
2. Round 3 hit context deadline (timeout)
3. No semaphore control for these calls

## Architecture Issue

### Current Architecture

```
┌─────────────────────────────────────────────────────────┐
│                     looms CLI                            │
│                                                          │
│  looms serve              looms workflow run            │
│      ↓                           ↓                       │
│  MultiAgentServer         cmd_workflow.go               │
│  (has semaphore)          (no semaphore)                │
│      ↓                           ↓                       │
│  Communication            Orchestration                  │
│  Patterns ✅              Patterns ❌                    │
│  - Hub-and-Spoke          - Fork-Join                   │
│  - Pub-Sub                - Parallel                     │
│  (semaphore used)         - Pipeline                     │
│                           - Debate                       │
│                           (semaphore NOT used)           │
└─────────────────────────────────────────────────────────┘
```

### Why This Happened

1. **Semaphore lives in MultiAgentServer**: `pkg/server/multi_agent.go`
2. **Orchestration patterns live in separate package**: `pkg/orchestration/`
3. **`looms workflow run` doesn't use MultiAgentServer**: Calls orchestration directly
4. **No shared concurrency control**: Two separate code paths

## Impact

### Observed Issues

1. **Fork-Join**: 3 simultaneous LLM calls (bypasses limit of 2)
   - Still worked, but exceeded concurrency limit
   - Could trigger rate limiting on large workflows

2. **Pipeline**: Hit output token circuit breaker
   - Root cause: Prompt too complex, not concurrency
   - But shows lack of protection at orchestration level

3. **Debate**: Timeout (context deadline exceeded)
   - Multiple LLM calls per round with no throttling
   - Could benefit from semaphore to serialize calls

### Potential Issues

1. **Rate Limiting**:
   - Fork-join with 10 agents = 10 concurrent LLM calls
   - Parallel with 5 tasks = 5 concurrent LLM calls
   - Bedrock/Anthropic rate limits could be exceeded

2. **Resource Exhaustion**:
   - Memory usage from many concurrent LLM requests
   - Network connection limits

3. **Cost Spikes**:
   - No throttling = all requests fire immediately
   - Higher cost if provider charges per-second

## Solutions

### Option 1: Pass Semaphore to Orchestration Patterns

**Approach**: Add semaphore parameter to orchestration executors

**Changes Needed**:

1. **Update Orchestrator** (`pkg/orchestration/orchestrator.go`)
```go
type Orchestrator struct {
    // ... existing fields ...
    llmSemaphore chan struct{}  // Add concurrency control
}

func NewOrchestrator(agents map[string]*agent.Agent, llmSemaphore chan struct{}) *Orchestrator {
    return &Orchestrator{
        agents:       agents,
        llmSemaphore: llmSemaphore,  // Store semaphore
        // ...
    }
}
```

2. **Update Fork-Join Executor** (`pkg/orchestration/fork_join_executor.go:143`)
```go
go func(branchIdx int, id string) {
    defer wg.Done()

    // Acquire semaphore before agent execution
    if e.orchestrator.llmSemaphore != nil {
        e.orchestrator.llmSemaphore <- struct{}{}
        defer func() { <-e.orchestrator.llmSemaphore }()
    }

    result, model, err := e.executeAgentWithSpan(...)
    // ...
}(idx, agentID)
```

3. **Update cmd_workflow.go** to create and pass semaphore
```go
// Create LLM concurrency semaphore
llmConcurrencyLimit := 2
llmSemaphore := make(chan struct{}, llmConcurrencyLimit)

// Create orchestrator with semaphore
orchestrator := orchestration.NewOrchestrator(registeredAgents, llmSemaphore)
```

**Pros**:
- Enforces concurrency limit across all patterns
- Simple to implement
- Consistent with MultiAgentServer approach

**Cons**:
- Adds parameter to every orchestrator call
- Serializes fork-join execution (may reduce performance)

### Option 2: Global Rate Limiter

**Approach**: Create package-level rate limiter in `pkg/llm`

```go
// pkg/llm/rate_limiter.go
package llm

var globalRateLimiter = NewRateLimiter(2)  // Configurable

type RateLimiter struct {
    semaphore chan struct{}
}

func (r *RateLimiter) Acquire() {
    r.semaphore <- struct{}{}
}

func (r *RateLimiter) Release() {
    <-r.semaphore
}
```

Then use in both:
- `pkg/server/multi_agent.go` (replace local semaphore)
- `pkg/orchestration/*_executor.go` (add semaphore calls)

**Pros**:
- Single source of truth for concurrency control
- Works across server and CLI
- No parameter passing needed

**Cons**:
- Global state (testing complexity)
- Less flexible per-workflow configuration

### Option 3: Agent-Level Semaphore

**Approach**: Move semaphore to `agent.Chat()` method

```go
// pkg/agent/agent.go
var globalLLMSemaphore chan struct{}

func (a *Agent) Chat(ctx context.Context, sessionID string, userMessage string) (*Response, error) {
    // Acquire semaphore at agent level
    if globalLLMSemaphore != nil {
        globalLLMSemaphore <- struct{}{}
        defer func() { <-globalLLMSemaphore }()
    }

    // ... rest of Chat implementation ...
}
```

**Pros**:
- Centralized at the right level (all LLM calls go through Agent.Chat)
- Works automatically for all patterns
- No changes needed to orchestration code

**Cons**:
- Global state in agent package
- May not be appropriate for non-LLM operations

## Recommended Solution

**Recommendation**: **Option 1** (Pass Semaphore to Orchestration)

**Rationale**:
1. Explicit and clear (semaphore passed as parameter)
2. Follows existing MultiAgentServer pattern
3. Easy to test (can pass nil to disable)
4. Configurable per-workflow if needed

**Implementation Priority**:
1. Update `Orchestrator` struct and constructor
2. Update fork-join executor (highest impact)
3. Update parallel executor
4. Update debate executor (collaboration patterns)
5. Add integration tests with concurrency=2
6. Document in ARCHITECTURE.md

## Testing Strategy

### Unit Tests

1. **Test semaphore acquisition**:
```go
func TestForkJoinWithSemaphore(t *testing.T) {
    semaphore := make(chan struct{}, 2)
    orchestrator := NewOrchestrator(agents, semaphore)

    // Launch 3 agents, verify only 2 run concurrently
    // Track semaphore length during execution
}
```

2. **Test without semaphore** (backward compat):
```go
func TestForkJoinWithoutSemaphore(t *testing.T) {
    orchestrator := NewOrchestrator(agents, nil)
    // Should work without semaphore (existing behavior)
}
```

### Integration Tests

1. **Fork-join with 5 agents, semaphore=2**:
   - Verify max 2 concurrent LLM calls
   - Check total execution time increased (serialization)

2. **Pipeline with semaphore=1**:
   - Verify sequential execution (pipeline already sequential)

3. **Parallel with 10 tasks, semaphore=3**:
   - Verify max 3 concurrent LLM calls
   - Verify all 10 tasks complete successfully

## Related Files

- `cmd/looms/cmd_serve.go:1250` - Semaphore configuration
- `pkg/server/multi_agent.go:102-104, 1043, 1393, 1793` - Semaphore usage (communication patterns)
- `pkg/orchestration/orchestrator.go:280-340` - Pattern routing
- `pkg/orchestration/fork_join_executor.go:131-169` - Missing semaphore
- `pkg/orchestration/parallel_executor.go` - Missing semaphore
- `pkg/collaboration/debate.go` - Missing semaphore
- `cmd/looms/cmd_workflow.go:422` - Orchestration invocation

## Conclusion

The semaphore exists and works correctly for communication patterns (hub-and-spoke, pub-sub) but is **completely bypassed** for orchestration patterns (fork-join, parallel, pipeline, debate).

This is a **critical gap** that can lead to:
- Rate limiting from LLM providers
- Uncontrolled resource usage
- Circuit breaker triggers

**Next Steps**:
1. Implement Option 1 (pass semaphore to orchestration)
2. Add integration tests with concurrency limits
3. Document concurrency behavior in workflow examples
4. Update ARCHITECTURE.md with concurrency control design

# Output Token Circuit Breaker — Bug Diagnosis

**File:** `pkg/agent/conversation_helpers.go` + `pkg/agent/agent.go:1342-1384`
**Reported by:** Dan Bo (evaluation), Anthropic users, OpenAI users
**Status:** 🔴 Bug confirmed — incorrect counter accumulation logic

---

## The Bug in One Sentence

The `outputTokenExhaustions` counter accumulates across an **entire session** and is only cleared when `stop_reason != "max_tokens"` — but a **successful text response** that hits `max_tokens` never clears the counter, causing the circuit breaker to fire on legitimate verbose responses.

---

## Code Flow (Current Behavior)

In `agent.go` inside the agentic turn loop:

```
LLM call returns (StopReason, Content, ToolCalls)
│
├── StopReason == "max_tokens"?
│   ├── YES → recordOutputTokenExhaustion(hasEmptyToolCall)   ← counter++
│   │         check CB (threshold=3)
│   │         if count >= 3 → return ERROR  ← CB fires
│   │         // counter stays incremented regardless
│   │
│   └── NO  → clearOutputTokenExhaustion()                    ← counter=0
│
└── len(ToolCalls) == 0?
    └── YES → return Response (success)                       ← counter NOT reset
```

---

## Why This Is Wrong

There are three distinct scenarios when `max_tokens` is hit:

| Scenario | ToolCalls | Truncated? | Is it a failure? | Current behavior |
|----------|-----------|------------|-----------------|-----------------|
| 1. Long text response, no tools | empty | N/A | **No** — valid response | Counts as failure ❌ |
| 2. Tool calls returned, not truncated | present | No | Borderline — can still make progress | Counts as failure ❌ |
| 3. Tool calls returned, truncated mid-generation | present | Yes | **Yes** — agent stuck, can't execute | Counts as failure ✅ |

The CB is designed to catch **Scenario 3** (infinite loop, truncated tool calls, no progress). But it counts **all three** identically.

---

## The Failing Sequence (Reproduced from Dan's Session)

```
Session created → FailureTracker.outputTokenExhaustions = 0

Turn 1: User: "Create HIPAA compliance report"
  → LLM generates verbose report → StopReason=max_tokens, ToolCalls=[]
  → counter = 1, response returned successfully (user sees the report)

Turn 2: User: "Check data quality on PATIENTS table"
  → LLM generates detailed analysis → StopReason=max_tokens, ToolCalls=[]
  → counter = 2, response returned successfully (user sees the analysis)

Turn 3: User: "Generate the final summary"
  → LLM starts generating → StopReason=max_tokens, ToolCalls=[]
  → counter = 3
  → checkOutputTokenCircuitBreaker(3) → threshold reached
  → return ERROR: "OUTPUT TOKEN CIRCUIT BREAKER TRIGGERED"
  → User sees an error. No output. Session effectively dead.
```

The user's three queries were all legitimate. All three responses were useful (text returned, no tool calls). The CB fired because the healthcare agents are configured to generate comprehensive reports — which is what they were asked to do.

---

## The Fix

**Principle:** Only count a `max_tokens` event as an "exhaustion failure" when it actually prevents the agent from making progress. A text response that hits `max_tokens` is a complete (if truncated) response — not a failure.

**Required changes in `agent.go` (lines 1342-1384):**

```go
// === OUTPUT TOKEN CIRCUIT BREAKER ===
if failureTracker, ok := session.FailureTracker.(*consecutiveFailureTracker); ok && failureTracker != nil {
    if llmResp.StopReason == "max_tokens" {
        hasEmptyToolCall := detectEmptyToolCall(llmResp.ToolCalls)

        if len(llmResp.ToolCalls) > 0 && hasEmptyToolCall {
            // REAL FAILURE: Agent is in tool loop, tool calls truncated — can't make progress
            exhaustionCount := failureTracker.recordOutputTokenExhaustion(hasEmptyToolCall)
            // ... tracing ...
            if err := failureTracker.checkOutputTokenCircuitBreaker(threshold); err != nil {
                // ... tracing ...
                return nil, fmt.Errorf("output token circuit breaker: %w", err)
            }
        } else if len(llmResp.ToolCalls) == 0 {
            // NOT A FAILURE: Agent returned a complete text response, just hit token limit
            // Clear counter — a successful response resets the streak
            failureTracker.clearOutputTokenExhaustion()
        }
        // else: max_tokens with non-truncated tool calls — agent may still make progress
        // Don't count as failure yet, but don't clear either
    } else {
        // Normal completion — clear the counter
        failureTracker.clearOutputTokenExhaustion()
    }
}
```

**Also needed:**
1. Make the threshold configurable per-agent (not hardcoded at 3)
2. Add agent config field: `output_token_cb_threshold` (default: 5, was: 3)
3. Raise the default — threshold of 3 is too low even for the corrected logic

---

## Secondary Issue: Threshold Hardcoded at 3

Even with the logic fix, a threshold of 3 consecutive *actual* failures (truncated tool calls) is aggressive for agents doing complex multi-step work. In the evals circuit breaker (`pkg/evals/judges/circuit_breaker.go`), this is configurable. The agent CB should be too.

---

## Test Coverage Gaps

The existing tests in `pkg/agent/circuit_breaker_test.go` only test the counter mechanics. They don't test:
- CB behavior when max_tokens fires but ToolCalls is empty (should clear, not count)
- CB behavior across multiple user messages in a session
- CB behavior in a pipeline context

These tests need to be added as part of the fix.

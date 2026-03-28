> **Status: ✅ COMPLETED** — Fix implemented in v1.2.0. This plan is archived for historical reference.

# Output Token Circuit Breaker — Bug Diagnosis

**File:** `pkg/agent/conversation_helpers.go` + `pkg/agent/agent.go:1722-1794`
**Reported by:** Dan Bo (evaluation), Anthropic users, OpenAI users
**Status:** ✅ Bug fixed — incorrect counter accumulation logic corrected in v1.2.0

---

## The Bug in One Sentence

The `outputTokenExhaustions` counter accumulates across an **entire session** and is only cleared when `stop_reason != "max_tokens"` — but a **successful text response** that hits `max_tokens` never clears the counter, causing the circuit breaker to fire on legitimate verbose responses.

---

## Code Flow (Old Buggy Behavior)

In `agent.go` inside the agentic turn loop, **before the fix**:

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

## Why The Old Behavior Was Wrong

There are three distinct scenarios when `max_tokens` is hit:

| Scenario | ToolCalls | Truncated? | Is it a failure? | Old behavior |
|----------|-----------|------------|-----------------|-----------------|
| 1. Long text response, no tools | empty | N/A | **No** — valid response | Counts as failure ❌ |
| 2. Tool calls returned, not truncated | present | No | Borderline — can still make progress | Counts as failure ❌ |
| 3. Tool calls returned, truncated mid-generation | present | Yes | **Yes** — agent stuck, can't execute | Counts as failure ✅ |

The CB is designed to catch **Scenario 3** (infinite loop, truncated tool calls, no progress). But the old code counted **all three** identically.

---

## The Failing Sequence (Reproduced from Dan's Session, Before Fix)

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

The user's three queries were all legitimate. All three responses were useful (text returned, no tool calls). The CB fired because the healthcare agents are configured to generate detailed reports — which is what they were asked to do.

---

## The Fix

**Principle:** Only count a `max_tokens` event as an "exhaustion failure" when it actually prevents the agent from making progress. A text response that hits `max_tokens` is a complete (if truncated) response — not a failure.

**Changes implemented in `agent.go` (lines 1722-1794):**

```go
// === OUTPUT TOKEN CIRCUIT BREAKER ===
if failureTracker, ok := session.FailureTracker.(*consecutiveFailureTracker); ok && failureTracker != nil {
    threshold := a.config.OutputTokenCBThreshold
    if threshold == 0 {
        threshold = 8 // Default if not configured
    }

    if llmResp.StopReason == "max_tokens" {
        hasEmptyToolCall := detectEmptyToolCall(llmResp.ToolCalls)

        switch {
        case threshold < 0:
            // CB disabled entirely — do nothing

        case len(llmResp.ToolCalls) > 0 && hasEmptyToolCall:
            // TRUE FAILURE: agent is stuck in agentic loop with truncated tool calls.
            exhaustionCount := failureTracker.recordOutputTokenExhaustion(hasEmptyToolCall)
            // ... tracing ...
            if err := failureTracker.checkOutputTokenCircuitBreaker(threshold); err != nil {
                // ... tracing ...
                return nil, fmt.Errorf("output token circuit breaker: %w", err)
            }

        case len(llmResp.ToolCalls) == 0:
            // NOT A FAILURE: the LLM returned a complete text response that hit the
            // token limit. Reset the counter.
            failureTracker.clearOutputTokenExhaustion()

        default:
            // max_tokens with non-truncated tool calls: agent may still make progress.
            // Don't count, don't clear — let it continue.
        }
    } else {
        // Normal completion — clear the counter.
        failureTracker.clearOutputTokenExhaustion()
    }
}
```

**All three requirements were implemented:**
1. ✅ Threshold is configurable per-agent via `output_token_cb_threshold` in agent YAML
2. ✅ Proto field added: `agent_config.proto` → `AgentBehaviorConfig.output_token_cb_threshold`
3. ✅ Default raised from 3 to 8; set to `-1` to disable entirely

---

## Secondary Issue: Threshold Was Hardcoded at 3 (✅ Fixed)

The original threshold of 3 consecutive failures was too aggressive for agents doing complex multi-step work. This has been addressed:

- ✅ Threshold is now configurable via `output_token_cb_threshold` in agent YAML (default: 8)
- ✅ Set to `0` to use default (8), or `-1` to disable the CB entirely
- ✅ Consistent with the evals circuit breaker (`pkg/evals/judges/circuit_breaker.go`), which uses `CircuitBreakerConfig` with configurable thresholds

---

## Test Coverage (✅ Addressed)

Tests in `pkg/agent/circuit_breaker_test.go` now cover all identified gaps:

- ✅ `TestOutputTokenCB_TextResponseClearsCounter` — CB clears counter when max_tokens fires with empty ToolCalls (text response)
- ✅ `TestOutputTokenCB_SessionAccumulation_Regression` — exact regression scenario from Dan Bo's session (3 verbose text responses across turns)
- ✅ `TestOutputTokenCB_OnlyTruncatedToolCallsCount` — only truncated tool calls count toward threshold
- ✅ `TestOutputTokenCircuitBreaker_ThresholdCustomization` — custom thresholds (tests with threshold=8)
- ✅ `TestOutputTokenCB_ThresholdInErrorMessage` — error message reports configured threshold
- ✅ `TestOutputTokenCircuitBreaker_Recovery` — counter resets after successful response
- ✅ `TestOutputTokenCircuitBreaker_Integration` — full flow with truncated tool calls
- 📋 CB behavior in a pipeline context — not yet tested

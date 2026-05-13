# Self-Healing Error Recovery for the Loom Agent Framework

## Problem

The conversation loop hard-fails on circuit breakers and token budget exhaustion. The error propagates to the user as a dead-end — no recovery attempted, no partial output preserved. Agents stop after 10–20 tool calls and all the user sees is an error.

## Architecture: 3-Tier Recovery

| Tier | Behavior | Where it lives |
|---|---|---|
| **Tier 1: Self-heal** | Invisible recovery inside the loop — trim context, disable broken tools, re-enter the loop | `pkg/agent/recovery.go` + wiring in `agent.go` |
| **Tier 2: Graceful degradation** | When the loop ends at limits, force one final text-only LLM call to synthesize whatever the agent has | Already partially exists; needs prompt improvement |
| **Tier 3: Structured error** | When self-heal fails, return a `RecoverableError` with type/action/payload so the cloud layer can offer recovery to the user | New error type in `recovery.go` |

---

## Implementation Plan (8 phases, dependency-ordered)

### Phase 1: New Types + Config

**New file: `pkg/agent/recovery.go`**

```go
type RecoverableError struct {
    ErrorType       string                 // "output_token_circuit_breaker", "tool_circuit_breaker", "token_budget_exceeded"
    Message         string                 // Human-readable
    RecoveryAction  string                 // "rewind_and_retry", "reset_context", "disable_tool_and_retry"
    RecoveryPayload map[string]interface{} // e.g. {"trim_messages": 8, "disabled_tool": "web_search"}
    Retryable       bool
    Cause           error
}
func (e *RecoverableError) Error() string { return e.Message }
func (e *RecoverableError) Unwrap() error { return e.Cause }

type recoveryOrchestrator struct {
    config                         *RecoveryConfig
    outputTokenCBRecoveryAttempted bool
    disabledTools                  map[string]bool
    tracer                         observability.Tracer
    span                           *observability.Span
}
```

Methods on `recoveryOrchestrator`:
- `recoverOutputTokenCB(ctx, session, segMem, failureTracker, threshold) (recovered bool, err error)`
- `recoverToolCB(ctx, toolName, tools *[]shuttle.Tool) (recovered bool, err error)`
- `recoverTokenBudget(ctx, session, segMem) (recovered bool, err error)`
- `buildRecoverableError(errorType, cause, action, payload) *RecoverableError`

**`pkg/agent/types.go`** — add to `Config`:
```go
EnableSelfHealing bool  // default true
```

**`pkg/agent/config_loader.go`** — wire YAML `behavior.enable_self_healing` → Config.

---

### Phase 2: SegmentedMemory Trim Support

**`pkg/agent/segmented_memory.go`** — two new methods:

```go
// TrimLastN removes last N messages from L1 (respects tool_use/tool_result pair boundaries).
func (sm *SegmentedMemory) TrimLastN(n int) int

// AggressiveTrim keeps only the last keepLastN messages, clears L2 summaries entirely.
func (sm *SegmentedMemory) AggressiveTrim(keepLastN int) (beforeTokens, afterTokens int)
```

Both acquire `sm.mu` (write lock), recount tokens after mutation.

Use a `TrimableMemory` interface (type-asserted at runtime) rather than extending the existing `SegmentedMemoryInterface` — avoids a breaking interface change:
```go
type TrimableMemory interface {
    TrimLastN(n int) int
    AggressiveTrim(keepLastN int) (int, int)
}
```

---

### Phase 3: Tier 1 Recovery Logic

**`pkg/agent/recovery.go`** — implement the three recovery methods:

**Output Token CB Recovery:**
1. If already attempted → return `(false, nil)` (one shot per conversation).
2. Set `outputTokenCBRecoveryAttempted = true`.
3. `session.SegmentedMem.(TrimableMemory).TrimLastN(threshold)` — removes the broken loop turns.
4. `session.TrimLastN(threshold)` — keeps flat Messages list in sync.
5. `failureTracker.clearOutputTokenExhaustion()`.
6. Inject recovery message (user role): "Your previous responses exceeded the output limit and were truncated. Simplify your approach — break the task into smaller steps, call one tool at a time, and keep responses concise."
7. Emit span event `"recovery.output_token_cb.attempted"`.
8. Return `(true, nil)` — loop continues.

**Tool CB Recovery:**
1. Filter the tool out of the local `tools` slice (NOT `a.tools.Unregister()` — that's global).
2. Record in `disabledTools`.
3. Return a synthetic tool result: `{success: false, error: "tool_disabled", message: "Tool X unavailable due to repeated failures. Use alternatives."}`.
4. Emit span event `"recovery.tool_cb.disabled"`.
5. Return `(true, nil)` — loop continues with reduced tool set.

**Token Budget Recovery:**
1. `session.SegmentedMem.(TrimableMemory).AggressiveTrim(4)` — keep last 4 messages.
2. Also trim `session.Messages`.
3. Re-check budget. If still over 85% → return `(false, nil)`.
4. Emit span event with before/after token counts.
5. Return `(true, nil)`.

---

### Phase 4: Wire into `agent.go` Loop

At the top of `runConversationLoop`, create the orchestrator:
```go
var recovery *recoveryOrchestrator
if a.config.EnableSelfHealing {
    recovery = newRecoveryOrchestrator(...)
}
```

**Three replacement sites:**

1. **Output token CB** (~line 2254): Instead of `return nil, fmt.Errorf(...)`:
   ```go
   if recovery != nil {
       recovered, _ := recovery.recoverOutputTokenCB(...)
       if recovered { continue }
   }
   return nil, recovery.buildRecoverableError("output_token_circuit_breaker", err, "rewind_and_retry", ...)
   ```

2. **Tool CB** (~line 2835): At the call site after `executeToolWithSelfCorrection` returns a CB error:
   ```go
   if strings.Contains(err.Error(), "circuit breaker open") && recovery != nil {
       recovered, _ := recovery.recoverToolCB(ctx, toolCall.Name, &tools)
       if recovered { err = nil; result = syntheticToolDisabledResult }
   }
   ```

3. **Token budget** (~line 2131): Instead of `return nil, fmt.Errorf(...)`:
   ```go
   if recovery != nil {
       recovered, _ := recovery.recoverTokenBudget(...)
       if !recovered { return nil, recovery.buildRecoverableError("token_budget_exceeded", ...) }
   }
   ```

---

### Phase 5: Tier 2 Enhancement

In the existing end-of-loop synthesis (~line 2715), update the forced prompt:

> "You must provide your final answer NOW with whatever information you have gathered so far. Summarize your findings and any remaining steps the user would need to complete manually."

Disable tools for this final call (already done — passes `nil` tools). Consider reducing max_tokens if the LLM provider supports per-call overrides (document as follow-up if not).

---

### Phase 6: Session.TrimLastN

**`pkg/types/types.go`** — add to `Session`:
```go
func (s *Session) TrimLastN(n int) {
    // Lock, trim Messages slice from the end, UpdatedAt = now
}
```

---

### Phase 7: Tests (`pkg/agent/recovery_test.go`)

All with `-race`:

| # | Test | Validates |
|---|---|---|
| 1 | `TestRecovery_OutputTokenCB_SelfHeals` | Tier 1: after trim+nudge, agent produces clean response |
| 2 | `TestRecovery_OutputTokenCB_FailsAfterRetry` | Tier 1 → 3: recovery exhausted, RecoverableError returned |
| 3 | `TestRecovery_OutputTokenCB_DisabledConfig` | `EnableSelfHealing: false` → old hard-fail behavior |
| 4 | `TestRecovery_ToolCB_DisablesTool` | Tool removed, LLM sees disabled result, loop continues |
| 5 | `TestRecovery_TokenBudget_AggressiveTrim` | CompactMemory fails → AggressiveTrim → loop continues |
| 6 | `TestRecovery_TokenBudget_Unrecoverable` | After aggressive trim still over → RecoverableError |
| 7 | `TestRecoverableError_Interface` | errors.As, Unwrap, Error() |
| 8 | `TestRecovery_Observability` | Span events emitted per recovery path |
| 9 | `TestRecovery_ConcurrentAccess` | Race detector validates shared state |
| 10 | `TestSegmentedMemory_TrimLastN` | Tool pair integrity, token recount |
| 11 | `TestSegmentedMemory_AggressiveTrim` | Keep-last-N, L2 cleared |

---

## Dependency Graph

```
Phase 1 (types)  ──┐
Phase 2 (trim)  ───┼──> Phase 3 (recovery logic) ──> Phase 4 (wiring) ──> Phase 7 (tests)
Phase 6 (session) ─┘                                     │
                                                          v
                                                    Phase 5 (Tier 2 prompt)
```

Phases 1, 2, 6 are independent — can be built in parallel.

---

## Design Decisions

| Decision | Rationale |
|---|---|
| One recovery attempt per type per conversation | Prevents infinite retry loops |
| Local tools slice, not registry mutation | Cross-session safety (concurrent chats share the agent's registry) |
| `TrimableMemory` interface (type-asserted) | Doesn't break existing SegmentedMemoryInterface consumers |
| Recovery messages use `"user"` role | Maintains valid alternating turn structure required by Anthropic API |
| `EnableSelfHealing` defaults to true | Opt-out, not opt-in — fix the common case by default |
| `RecoverableError` implements `error` | Backward-compatible — existing callers see a normal error |

---

## Open Questions for Implementer

1. **Does `LLMProvider.Chat()` support per-call `max_tokens` override?** If yes, use a reduced value for the Tier 2 synthesis call. If no, document as follow-up.
2. **Is `session.FailureTracker` accessed by background goroutines (e.g., finding extraction)?** If yes, add a dedicated mutex to `consecutiveFailureTracker`. If no, the existing session.mu coverage is sufficient.
3. **Aggressive trim constant (`keepLastN = 4`)** — should this be configurable via `RecoveryConfig`? Recommend: yes, as a field with default 4.

---

## Files to Touch

| File | Action |
|---|---|
| `pkg/agent/recovery.go` | NEW — types + orchestrator + methods |
| `pkg/agent/recovery_test.go` | NEW — all tests |
| `pkg/agent/types.go` | Add `EnableSelfHealing` to Config, add to DefaultConfig |
| `pkg/agent/config_loader.go` | Wire YAML field |
| `pkg/agent/segmented_memory.go` | Add `TrimLastN`, `AggressiveTrim` |
| `pkg/agent/agent.go` | Wire recovery at 3 sites + instantiate orchestrator |
| `pkg/types/types.go` | Add `TrimableMemory` interface, `Session.TrimLastN` |

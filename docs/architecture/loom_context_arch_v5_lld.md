# Loom Context v5 — LLD (closed-world specification)

**Status:** Implementation design. Replaces the context pipeline; does not amend it.
**Method:** This spec is **closed-world**: it defines the *complete* content of the compiled context and the *only* code allowed to mutate memory. Anything in today's code that produces context content or mutates memory and is not named in Contract 1 or Contract 2 **is deleted** — the deletion manifests below are derived from the contracts, not from review. All file:line anchors verified against source (2026-07-04).
**Absorbs:** `loom_context_arch_v4.md` (Part D here) and `loom-segmented-memory-redesign.md` (the HLD this implements).

---

## Contract 1 — The Assembler

`GetMessagesForLLM` (`segmented_memory.go:1074`) is **rewritten** to produce exactly this, and nothing else:

```go
func (sm *SegmentedMemory) GetMessagesForLLM() []Message {
    out := []Message{}
    if sm.romContent != "" {
        out = append(out, Message{Role: "system", Content: sm.romContent})   // ROM
    }
    if sm.l2Summary != "" {
        out = append(out, Message{Role: "system",
            Content: "Previous conversation summary: " + sm.l2Summary})     // fold residue
    }
    return append(out, sm.l1Messages...)                                     // the conversation
}
```

**Three parts: ROM · fold residue · conversation. A channel not in this function does not exist.** Per-beat dynamic content (skill menu, soft reminders) is appended by the loop to its **local** `messages` slice after `prepareContext()` returns — one user-role note, this beat only, never stored, never system.

> **Implementation note — residue role divergence.** The pseudocode emits the
> fold residue as `Role: "system"`. The shipped implementation emits it as
> `Role: "user"` (`assembler.go`). Both compile the same three-part output;
> the divergence exists because the byte-stability contract Contract 1 buys
> unconditionally requires the `system` field to be identical between folds
> — and different Anthropic/Bedrock provider adapters hoist assistant/user
> vs. system messages differently. Emitting the residue as user-role keeps
> the provider's system field byte-identical (holding only the ROM); the
> assembler contract test in `assembler_test.go` pins this by grepping the
> LLM-bound message path to ensure no other code constructs a `Role:"system"`
> message. The pseudocode above documents the design *intent* (residue is a
> system-level continuation summary); the implementation carries it as
> user-role for the same reason it never persists it as an L1 message —
> byte stability of the prompt-cache prefix trumps role semantics.

Properties this buys unconditionally: the `system` field is byte-stable except at a fold; the messages prefix is append-only between folds (cache-correct); position is carried by order (no standing-state channel can exist because no slot for one exists).

**Deletion manifest 1** (everything the current assembler renders that Contract 1 doesn't):

| Deleted | Where today |
|---|---|
| skill body render ("# Active Skills … for this interaction") + `skillContent`/`skillNames`/`InjectSkills`/`FormatActiveSkillsForLLM` | `segmented_memory.go:126-130, 1060-1069, 1104-1110`; `orchestrator.go:556-597` |
| pattern render + `patternContent`/`patternName`/`InjectPattern` | `segmented_memory.go:1096-1102`; `agent.go:2152, 2244` |
| findings render + `findingsCache` + finding-extractor wiring + findings builtin | `segmented_memory.go:1112-1120`; `finding_extractor.go`; `builtin_tools.go:1023-1140` |
| promoted-context render + `promotedContext` slot + promote path | `segmented_memory.go:93, 1122-1131, 1642-1658` |
| soft-reminder append into `messages[0].Content` (mutates system per beat) | `agent.go:2346-2350` → becomes a tail note |
| **graph-memory context inject** — appends a `Role:"system"` message **into the session as a persisted conversation message every user turn** (`injectGraphMemoryContext`, `agent.go:2029` → `:3384-3387`); the provider converter hoists all system-role messages into the system field → per-turn system churn + standing-state blocks accumulating in L1 forever | rewired, not deleted: the recall result becomes the beat's **tail note** (turn-scoped, never persisted via `session.AddMessage`) — same mechanism as the menu/reminders. Graph extraction (`extractGraphMemoryAsync`) untouched |
| **kernel phantom token weight** — `cachedKernelTokens` (toolResults + schema cache) is summed into the budget (`segmented_memory.go:984,1017`) but its only renderer was the deleted `GetContextWindow`; zones would fire on tokens the LLM never receives. `AddToolResult`/`schemaCache` accumulation has no surviving consumer | token accounting = tokens of Contract 1's compiled output only (ROM + residue + L1) + a fixed tool-schema allowance; kernel result/schema caches and their accounting retired |

**LLM call-surface closure (verified):** every LLM call in the agent goes through `chatWithRetry` (`llm_retry.go:43,51,115` are its internals); its only callers are `agent.go:2369` and `:2972`; `Chat` and `ChatWithProgress` share the single `runConversationLoop` (`:1505`, `:1705`) — **there is no separate streaming compile point.** Contract 2's two call sites are the complete set.

Rationale in one line: the findings and promoted channels were re-assertion mechanisms compensating for L1 shredding; with a verbatim conversation they are redundant, and they violate byte-stability. They are not moved — they are gone.

---

## Contract 2 — The single-writer pipeline

**`prepareContext(ctx, session) ([]Message, error)` is the only code that mutates `l1Messages` or `l2Summary` after admission.** It runs at the start of every assistant beat, before the LLM is called:

```go
func (a *Agent) prepareContext(ctx context.Context, session *Session) ([]Message, error) {
    if segMem := sessionSegMem(session); segMem != nil {
        pct := segMem.BudgetPct()                       // used/total of the SAME budget basis
        switch {
        case pct >= redPct:                             // 85 (config-overridable)
            if err := segMem.Fold(ctx, userLedgerCount(session)); err != nil {
                return nil, err                          // breaker error → recovery path
            }
        case pct >= yellowPct:                          // 70
            segMem.ValveEvict(ctx)
        }
    }
    return session.GetMessages(), nil
}
```

**Call sites — all `GetMessages()→LLM` paths, exhaustively:** the main loop (`agent.go:2330-2369`) and the max-turns synthesis call (`agent.go:2972`). Both call `prepareContext`. `chatWithRetry`'s doc comment gains the rule: *messages passed here must come from `prepareContext`.*

**Deletion manifest 2** (every other memory-mutation site):

| Deleted | Where today | Replaced by |
|---|---|---|
| per-message compression block in `AddMessage` | `segmented_memory.go:345-392` | nothing — `AddMessage` is pure admission (token accounting `:321-343` kept) |
| restore-time compression block in `ReplayMessages` | `segmented_memory.go:509-540` (its own `shouldCompress` pass) | nothing — restore is pure bulk-load + reclassify (Part C) |
| `enforceTokenBudget` body (calls old `CompactMemory`) | `conversation_helpers.go:154-172` | `prepareContext` |
| old `CompactMemory` FIFO-batch semantics | `segmented_memory.go:1314` | thin wrapper over `Fold` (same exported signature — second caller `session_memory_tool.go:382` keeps working and is counted by the breaker) |
| `maxL1Tokens` / `WarningThresholdPercent` as triggers | `compression_profiles.go:51,61,71` consumers | zones = 70/85% of the existing `tokenBudget` basis (`GetTokenBudgetUsage`); profiles remain only as fallback when the window is unknown |
| automatic `AggressiveTrim` at RED (keeps last 4 messages **regardless of class**, and truncates the flat archive via `session.TrimLastN(0)`) | loop post-check `agent.go:2315-2325` → `recovery.go:160-162` | if still RED **after** a fold, that is by definition the breaker condition (an oversized item pressure can't relieve) → recoverable error. `AggressiveTrim`/`TrimLastN` survive **only** inside the explicit, user-surfaced `reset_context` recovery action — never as silent pressure handling. Class-blind trimming as an automatic path violates every retention invariant at once. |
| `GetContextWindow()` — a second, string-form assembler | `segmented_memory.go:1141-1197` | delete — zero non-test callers (verified); a second assembler is a standing Contract-1 violation waiting to be called |

**Recovery clause:** code in `recovery.go` may mutate memory only as a *terminal, user-surfaced* action (`reset_context`). No recovery path may silently trim, compress, or drop messages as pressure handling — pressure handling is exclusively `prepareContext`.

---

## Part A — Classification (feeds both contracts)

**`ContextClass` field on `types.Message` (`types.go:95`)** — `""`=narrative (zero value), `"charter"`, `"ledger"`, `"ballast"`. Persisted as one nullable column.

Tagging is structural, at exactly three places:
1. **Genuine user messages → ledger**, tagged at their two construction sites (`agent.go:1467`, `:1662`). *Not* a role-based rule: the harness also creates `Role:"user"` messages (`nudgeMsg:2498`, `synthesisMsg:2953`, v5's tail notes) and `AgentID` cannot distinguish them (genuine messages set it too, `:1470`); synthetic ones stay narrative.
2. **Tool results**, at toolMsg construction (`agent.go:~2855`): loader tools (`manage_skills`, `manage_patterns`) → charter; whitelisted read-only data tools → ballast (opt-in via a `ContextClass()` method on the tool / MCP `readOnlyHint`; **whitelist, never blacklist**); everything else — including all mutating tools (execution records) and `contact_human` — → ledger.
3. Assistant messages → narrative (default).

**On restore**, classes come from the persisted column; rows predating the column are reclassified by the same rules (user-site provenance for ledger; tool name recovered by pairing `ToolUseID` to the preceding assistant's `ToolCalls[].Name`).

---

## Part B — The pressure stages (the only three memory operations)

### B-1 Admission
- `SharedMemoryThresholdBytes = 4096` in Tera agent config activates the existing `handleLargeResult` (`executor.go:284`) — oversized ballast enters as preview + `DataReference`.
- **Constants policy (decided):** implement with these defaults and tune post-implementation from real traces — admission 4096 bytes · yellow 70 · red 85 · `minValvePayoff` 20000 tokens · `keepRecentBallast` 3 · active-set cap 20. All are config-surfaced; none block implementation.
- **Exemption rule (part of the admission spec, not an afterthought): only ballast-class results are wrappable.** Charter/ledger-class tools (`manage_skills`, `manage_patterns`, `contact_human`, `recall_context`) are exempt, same mechanism as the existing `get_tool_result`/`query_tool_result` exclusion (`executor.go:181-183`). Without this the 24KB skill body would be wrapped into a ref and the charter would never enter context.

### B-2 Valve — `ValveEvict(ctx)`
- Candidates: `l1Messages` oldest→newest where `Role=="tool" && ContextClass==ballast`, excluding the newest `keepRecentBallast=3` ballast items and existing stubs.
- Token math via `sm.tokenCounter.CountTokens(msg.Content)` — never `msg.TokenCount` (unpopulated on tool messages).
- Fires only if total reclaim ≥ `minValvePayoff=20000` tokens (cache-economics bar). If `sm.sharedMemory == nil`: valve disabled (log once) — never evict without persist.
- Per item: persist bytes to the shared store → replace `Content` in place with `"[evicted: <tool> result, <n> tok → recall_context('<ref>')]"`. `ToolUseID` preserved — the stub remains a valid tool_result; pairing intact.

### B-3 Fold — `Fold(ctx, userTurnCount)`
Runs once, at RED, entirely inside `prepareContext`:
1. **Breaker first:** if this fold is within 3 *user turns* of the previous one (user turns = count of `ClassLedger` user messages, not the loop's iteration counter), 3 consecutive times → return error; surfaces via the existing recoverable-error path (`agent.go:2321`). Repeated folding is an alarm, not an operating mode.
2. Persist entire `l1Messages` verbatim to the shared store → `foldRef`.
3. **Partition with carry closure** (reuses the pair-walking of `adjustCompressionBoundary`, `segmented_memory.go:403+`):
   - carry := all charter + all ledger + for each ledger user message its immediately-preceding assistant message (adjacency);
   - **closure:** carrying any tool_result ⇒ carry its paired assistant message (via `ToolUseID`) and *all sibling tool_results* of that assistant message; carrying any assistant message that has `ToolCalls` ⇒ carry all its tool_results. Closure may pull ballast along — safe by the retention asymmetry. No carried set can produce an API-invalid sequence, by construction.
   - remaining ballast → evict via the valve path (no payoff bar); remaining narrative → the compressor.
4. `l2Summary = LLMCompressor.CompressMessages(narrative)` with the structured prompt (*state reached / decisions / open commitments / every ref id; never restate instructions or approvals — they are preserved elsewhere*) + `"Full pre-fold transcript: recall_context('<foldRef>')"`. Heuristic `summarizeMessages` only as logged degraded fallback.
5. `l1Messages = carry` (carried items remain **real messages in original order** — Contract 1's assembler and the Anthropic client need zero changes; assert `l1Messages[0].Role=="user"`, which holds because M1 is ledger).
6. **Persist `{l2Summary, foldIndex}`** via the session store (foldIndex = offset into the flat history). The carry set is *not* persisted — classification + closure are deterministic, so restore recomputes it.

> **Design note — residue non-composition across folds.** Successive folds
> do NOT compose their residues: fold N+1's compressor input is *only* the
> narrative surviving into L1 at the moment fold N+1 fires. The residue
> from fold N is at that point in `l2Summary` (a single system-level slot,
> overwritten by fold N+1's own residue). Older-era detail is not lost —
> the pre-fold-N transcript is still recoverable via `recall_context(fold:<foldIndex_N>)`
> and the fold N residue itself is retrievable via the swap-layer snapshot
> readers — but it is *not* automatically fed into the next residue's LLM
> compression pass. This is intentional: composing residues would grow the
> `l2Summary` slot without bound and violate the byte-stability contract
> Contract 1 buys. The recovery model is pull-based (recall on demand),
> not push-based (accumulate in place).

### Recall — `recall_context` (the fault handler)
One new builtin: `{ref, query?}` → shared-store `Get`; optional plain-text excerpting; output capped at the admission threshold. Result is an ordinary tool message, class ballast (re-evictable). Returns **once, at the tail** — there is no other recall path (the promote-forever channel is deleted by Contract 1). SQL refs keep using the existing `query_tool_result`; stub text names the right tool.

---

## Part C — Session restore

`ReplayMessages` becomes pure: bulk-load flat history into `l1Messages`, reclassify (Part A), recount. **No pressure at restore** — the first beat's `prepareContext` evaluates zones with the budget-derived thresholds, exactly like any other beat.
If a persisted fold exists: load `l2Summary`; `l1Messages = carrySet(flat[:foldIndex]) + flat[foldIndex:]` (carry set recomputed). If absent or unreadable: full verbatim restore; the next RED beat re-folds — self-healing at the cost of one summarization call.

---

## Part D — Skills (v4, integrated)

1. **`manage_skills` / `manage_patterns`** — `list`/`load` wrappers over the existing narrowing, high-risk gate (now an explicit tool result, not a silent log-skip), `RegisterTools`, task emission, and the library loader. The load result is the load event carrying the skill body (class set per Part A) and includes the skill's folder path (remote-exec seam). There is no `unload`: a load is an event in the append-only context, retired only when the pressure pipeline reclaims its body.
2. **Discovery menu** — the per-turn discovery block (`agent.go:2048-2165`) stops force-activating and stops injecting; it produces the candidate menu appended as the beat's tail note (Contract 1).
3. **Active set: no implicit eviction, no unload.** The score-based eviction (`orchestrator.go:443-449`, `MaxConcurrentSkills=3`) is deleted. The cap was sized for injected-body cost, which no longer exists; silent eviction drops a skill's tool wiring (`excluded_tools`, required tools) mid-workflow while its body remains in the conversation. Cap becomes a safety limit (default 20) on distinct loads per session, returning an explicit `manage_skills` error. Skill bodies are reclaimed by the pressure pipeline (fold). The active set keeps driving `applySkillExcludedTools` / `enforceRequiredSkillTools` / task emission; tool wiring is the effect of the load event and fires when the load body enters the context — at `executeLoad`, and again on session restore when replay puts a resident body back.
4. ROM guidance (config text, not code): *when a loaded skill instructs waiting for user approval, ending the turn to ask IS task progress.*

---

## Sequencing (each step independently shippable)

1. **Contract 2 core:** zones + `prepareContext` at both call sites + delete the `AddMessage` and `ReplayMessages` compression blocks + admission config with the exemption rule. *Kills the conveyor belt and the restore-shred in one step.*
2. **Contract 1 core:** the new assembler + deletion manifest 1 + tail-note mechanism (reminders, then menu). *System becomes byte-stable.*
3. **Part A:** `ContextClass` + tagging + persistence column.
4. **B-2 + recall:** valve + `recall_context`.
5. **B-3 + Part C:** fold + breaker + residue persistence + restore.
6. **Part D:** skills. *Parallel track; only coupling is the charter tag.*
7. **Closure sweep (part of every step's definition-of-done):** grep the tree for every deleted symbol (`InjectSkills`, `FormatActiveSkillsForLLM`, `InjectPattern`, `findingsCache`, `promotedContext`, `GetContextWindow`, `maxL1Tokens`, `AggressiveTrim` outside `reset_context`) — zero non-test references may remain, and affected tests are rewritten against the contracts, not preserved against the old behavior. Verified today: no production consumers outside the listed sites; blast radius is tests only.

## Test anchors

- The B1–B18 worked trace (`loom-segmented-memory-redesign.md §7`) as a table-driven test, **plus a resume-after-B15 variant** (restore must reproduce B16's compiled context from the persisted residue).
- Assembler: golden test — compiled output contains exactly ROM + residue + L1 for every fixture; grep-level test that no other code path constructs `Role:"system"` messages into the LLM path.
- Single-writer: no compression below yellow regardless of message count or restore size; mutation only ever observed inside `prepareContext`.
- Fold: carried set closure produces API-valid sequences (property test over random tool-pair layouts); breaker trips on 3 folds within 3 ledger-user turns; `session_memory_tool` route counted.
- Eval: regression gates must drive **this** runtime (`looms`), not the Claude SDK.

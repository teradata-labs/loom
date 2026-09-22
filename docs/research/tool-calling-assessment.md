# Tool-Calling Efficiency Assessment

**Date**: 2026-09-15
**Codebase**: `fix/hitl-hold-heartbeat` @ `bf64528d` (= main + heartbeat fix), v1.4.0
**Trigger**: recurring external feedback that "Loom uses excessive tool calls"
**Data**: local telemetry from `~/.loom/loom.db` — 46,934 tool executions, 98,100 messages, 6,502 sessions, 2026-03 → 2026-09 ($831 of tracked LLM spend, 171.5M tracked tokens)

---

## 1. Summary

The feedback is credible, and the causes are identifiable. Measured across ~6,500 local sessions, a Loom agent averages **7.07 tool executions and 4.02 main-loop LLM calls per user message** (median 6 executions), and behind those visible calls a default configuration adds **2–3 auxiliary LLM calls per turn plus one memory-extraction call per 5 tool executions** that no budget counts. The excess is not one bug; it is five compounding mechanisms, ranked by measured impact:

1. **Errors beget retries with no working brake.** 29.2% of all tool executions end in error, and 75.9% of errors are immediately followed by the model retrying the same tool. The tool circuit breaker cannot see the dominant failure shape at all: MCP tool failures return `Result{Success: false}` with a **nil Go error** (`pkg/mcp/adapter/shuttle.go:332-336`), and the breaker only counts Go errors (`pkg/agent/agent.go:3024-3034`). Observed worst case: **43 consecutive identical failing `teradata:connect` calls in one session** (see §3.4).
2. **The memory tool dominates traffic.** `graph_memory` is **42.7% of all tool executions** (20,063), 77% of them explicit `remember` writes — on top of an automatic extraction pipeline that already runs per turn. A **live** FK-constraint bug (`STORE_ERROR: link entity user: FOREIGN KEY constraint failed`) caused 4,792 of those calls to fail (2,783 in August 2026 alone), each failure inviting a retry.
3. **Result offloading makes re-calling the designed recovery path.** Results >16 KiB become stubs; pages fetched via `query_tool_result` are turn-scoped and never persisted; durable rows are truncated at the same threshold; and the prompts explicitly instruct: *"re-run the producing tool instead"*. The exemption mechanism (`SetOffloadExemptTools`) has **no production caller**.
4. **Nothing steers the model toward fewer calls.** Repo-wide, there is zero prompt text advising call efficiency (no "batch independent calls", no budget statement, no "reuse what you have"), while ROMs, skills, and the task board mandate multi-step tool sequences. The only "wrap up" nudge fires at 75–90% of a 50-call budget; the "do NOT retry" escalation after 2 identical failures is advisory text the data shows being ignored 40+ times.
5. **Auxiliary LLM work is invisible and unbounded.** Memory extraction fires on every `Chat()` entry *and* every 5 tool executions; recall costs 2 more LLM calls per turn (query distillation + rerank); `tool_search`'s default mode adds a hidden rerank call per search; compression can add 1–5 calls per relief pass; none are counted against `MaxTurns`, `MaxToolExecutions`, or any cost ceiling.

The storms also explain the *external* perception: the 43-call connect storms were agents hammering a Teradata MCP server that was already refusing (`session_handle_budget_full`) — Loom amplified load into a saturated server ~40× instead of backing off. A server operator experiencing that will report "excessive tool calls".

§8 has the prioritized fix checklist. The single highest-leverage change is making result-level failures count toward the circuit breaker and turning the identical-failure escalation into an enforced refusal.

---

## 2. Method and data caveats

- Four parallel code sweeps (tool surface, background LLM activity, prompt steering, retry multipliers) over this branch, plus direct reading of `runConversationLoop` (`pkg/agent/agent.go:2230-2960`) and SQL analysis of local telemetry. All claims carry `file:line` evidence from this branch.
- **Data caveat**: `~/.loom/loom.db` is a development machine. Traffic is dominated by benchmark campaigns (LongMemEval memory content is visible in sampled inputs; August = MCP gauntlet + lessons-pipeline fleets). Absolute rates (e.g., the 42.7% `graph_memory` share) are workload-specific; the *mechanisms* — error→retry with no brake, uncounted auxiliary calls, offload re-fetching — are workload-independent and verified in code.
- `messages.cost_usd`/`token_count` are partially populated; token/cost figures are lower bounds.

---

## 3. Measured behavior

### 3.1 Headline distribution

| Metric | Value |
|---|---|
| Tool executions per user message (mean / median) | **7.07 / 6.0** |
| Main-loop LLM calls per user message (mean) | **4.02** |
| Tool executions per session (p50 / p90 / max) | 7 / 19 / **198** |
| Sessions at ≥45 executions (≈ the 50-cap) | 38 |
| Executions ending in error | **13,726 (29.2%)** |
| Errors immediately followed by same-tool retry | **10,415 (75.9% of errors)** |
| Byte-identical duplicate calls within a session | **5,962 (12.7% of all calls)** |
| Assistant messages carrying >1 tool call | 4,102 of 32,328 (**12.7%**; mean 1.46 calls/message) |

The last row matters: Loom executes every `tool_use` block in a response (serially — `agent.go:2640`; no goroutines), so batching works — but the model batches in only ~13% of tool-bearing responses, because nothing asks it to (§5, RC5).

### 3.2 Where the calls go (top tools)

| Tool | Calls | Errors | Error rate |
|---|---|---|---|
| `graph_memory` (77% `remember`) | 20,063 | 4,981 | 24.8% |
| `teradata:connect` | 6,449 | 5,207 | **80.7%** |
| `teradata:execute_statement` | 4,542 | 1,220 | 26.9% |
| `teradata:execute_query` | 3,838 | 94 | 2.4% |
| `teradata_execute_sql` | 2,723 | 450 | 16.5% |
| `teradata:describe_table` | 1,746 | 30 | 1.7% |
| `query_tool_result` (offload fetch) | 1,620 | 834 | **51.5%** (797 of the errors are 2026-03) |
| `shared_memory_write` | 1,379 | 0 | — |
| `tool_search` | 842 | 0 | — (each call hides +1 LLM rerank, §5 RC6) |

`graph_memory` breakdown: `remember` 15,402 (31.1% error), `relate` 1,804, `entities` 1,206, `recall` 1,145. 4,792 of the `remember` failures are the FK-constraint bug — **2,783 of them in 2026-08**, so it is live, not the fixed 2026-03 entity-confusion issue.

### 3.3 The error→retry loop

Error rate by month: 2026-03 27.0% → 2026-04 22.2% → 2026-08 **32.2%** (29,301 calls). Not improving. Since a failed tool is never retried at the tool layer (by design — `pkg/shuttle/executor.go` marks errors `Retryable: false` and the loop just appends the error as a tool result), **every failure costs one full LLM round-trip**, and 75.9% of the time that round-trip re-issues the same tool.

### 3.4 Case study: the connect storm

Session `sess_48da9a25` (2026-08, MCP gauntlet era): **43 `teradata:connect` calls in 710 s, every one failing** with `MCP_CALL_FAILED: {"code":"session_handle_budget_full"}`, interleaved with successful `describe_table`/`execute_query` calls on an existing handle. The session eventually completed its task. Six sessions show 41–43-call storms; `teradata:connect` overall is 6,449 calls at an 80.7% failure rate.

Why nothing stopped it:
- The **tool circuit breaker never counted a single failure**: the MCP adapter wraps failures as `Result{Success:false}, nil` — the comment at `pkg/mcp/adapter/shuttle.go:336` says it: *"Return nil error since we wrapped it in Result.Error"* — while `breaker.Execute` counts only returned Go errors (`agent.go:3026-3028`).
- The **within-turn dedup** (`agent.go:2666-2684`) only spans one LLM response; cross-turn identical calls re-execute.
- The **identical-failure escalation** (threshold 2, `agent.go:2829`; text: "⛔ … Do NOT retry this same tool call again") is advisory. The model retried ~40 more times.
- Guardrails *did* see the failures (`agent.go:3053` checks `result.Success`) but only append suggestion text.

---

## 4. Anatomy of one default turn

For the **median turn** (6 tool executions over 4 main-loop LLM calls) on a default `looms serve` config (graph memory on — the default; `pkg/agent/config_loader.go:927`):

| # | LLM call | Counted by a budget? |
|---|---|---|
| 1–4 | Main conversation loop (`MaxTurns` 25 / `MaxToolExecutions` 50 / 10 calls-per-response — `pkg/agent/types.go:390-392`) | ✅ yes |
| 5 | Graph-memory extraction, fired on **every** `Chat()` entry, no cadence gate (`agent.go:1884-1891`) | ❌ no |
| 6 | Recall query distillation (`agent.go:3200`, `extractSearchQuery`) | ❌ no |
| 7 | Recall LLM rerank over candidates (`agent.go:3283`, `rerankMemories`) | ❌ no |
| 8 | Extraction again at the 5-tool cadence (`agent.go:2882-2891`, default 5) | ❌ no |

**≈ 8 LLM calls for 4 visible ones — a 2× amplification** before counting: `tool_search` reranks (+1 per search, BALANCED default — `pkg/tools/registry/registry.go:562`), compression folds (1–5 per relief pass at ≥90% context — `pkg/agent/context_compilation.go:465-527`), hygiene REQUIRE_FIX re-entries (up to 2 extra full turns, default ON when skills+tasks wired — `pkg/skills/hygiene/auditor.go:25,107`), the empty-response retry (1), and the cap-hit synthesis call (`agent.go:2906-2921`). In multi-agent topologies, **each inbound sub-agent message triggers a full coordinator turn** with all of the above (`pkg/server/multi_agent.go:1672-1691`).

Side effects beyond count: recall **blocks on in-flight extraction** (`a.graphExtractionWG.Wait()`, `agent.go:3176`), putting the previous turn's uncounted extraction call on the latency critical path of the next turn's first token.

---

## 5. Root causes

### RC1 — Result-level failures are invisible to the circuit breaker (verified defect)
`executeToolWithSelfCorrection` wraps execution in a per-tool breaker whose closure returns only the Go `error` (`agent.go:3024-3034`). The MCP adapter (`pkg/mcp/adapter/shuttle.go:332-336`) — and shuttle builtins generally — report failures inside `Result.Error` with `err == nil`. Consequence: `FailureThreshold: 5` (`pkg/fabric/circuit_breaker.go:57-63`) can never trip on the most common failure shape. The recovery tier that would drop a broken tool from the advertised set (`pkg/agent/recovery.go:70-100`, self-healing default ON — `types.go:395`) is therefore unreachable for MCP failures.

### RC2 — No enforced stop on repeated identical failures
The consecutive-identical-failure tracker exists (threshold 2, `pkg/agent/conversation_helpers.go:32-84`, `agent.go:2829`) but only injects text. Nothing refuses to execute a byte-identical call that has already failed N times. Measured: 5,962 in-session duplicate calls; 43-call storms.

### RC3 — Memory double-path and a live storage bug
Memory writes happen twice: automatic extraction (per `Chat()` + per 5 tool executions) *and* an advertised `graph_memory` tool the model uses heavily (20,063 calls). The tool's `remember` fails 31% of the time, dominated by the live FK bug (§3.2) — and every failure recruits RC2. Additionally `conversation_extraction_cadence` is parsed but never read (inert — `config_loader.go:266`, `agent.go:131`), and extraction "PASS 2" (`graph_memory_extractor.go:198-202`) is dead code.

### RC4 — Offloading design makes re-calling the recovery path
- Threshold 16 KiB (`pkg/storage/shared_memory.go:36-41`); stubs render with instructions to call `query_tool_result` (`context_compilation.go:49`).
- Pages are bounded to ~`threshold−512` bytes: reading a 1 MB result ≈ **64 `query_tool_result` calls**, each counted against `MaxToolExecutions` (no exemption in the loop).
- Cross-turn reads return `not_this_turn` whose error text says *"Re-run the producing call for fresh data"* (`pkg/agent/builtin_tools.go:103-113`); durable rows are truncated at persist (`pkg/agent/memory.go:747`, `write_rules.go:36-57`); fetched pages are **never persisted** (`memory.go:705-745`) — a page read in turn T must be re-fetched (or the producer re-run) in turn T+1.
- `prompts/tools/progressive_disclosure.yaml:11-13,24` instructs re-running producers as the normal workflow.
- `SetOffloadExemptTools` has **no production caller** (setters at `agent.go:3905-3915`; nil default), confirming the v1.4.0 release finding.
- Measured: `query_tool_result` fails 51.5% overall — mostly a 2026-03 UX failure where the model applied `sql=` to text refs (`invalid_input: Data type 'text' requires specific query method`, 678×). Recent months are quieter, but the structural re-run incentive stands.

### RC5 — Steering pushes toward more calls, never fewer
Verified absences (repo-wide grep over `prompts/`, `pkg/agent/roms/`, `skills/`, `patterns/`, `examples/`): no "minimize/batch/budget/reuse" guidance anywhere; no `tool_choice` forcing; no parallel-call encouragement (and no `disable_parallel_tool_use` either). Meanwhile:
- `pkg/agent/roms/TD.rom:49-54` mandates a 5-step discovery sequence (per table at step 3); `TD.rom:106` preview-before-aggregate.
- `skills/teradata-sql-analytics.yaml:196,230` mandates EXPLAIN-before-execute; `:53` adds a validation query per non-trivial query.
- Task board (when enabled) prescribes decompose→ready→claim→update→close→ready per task (`agent.go:1299-1324`).
- Patterns are pull-only since the recut: reaching one costs `manage_skills(load)` + `load_pattern` = 2 calls (`pkg/agent/load_pattern_tool.go:24-28`).
- The soft reminder ("Only call more tools if absolutely necessary") exists **only in the 75–90% band** of the budgets (`conversation_helpers.go:90-129`) — silent for the first ~37 of 50 calls.
- The prompt registry is OFF by default (`prompts.source` = `""`, `cmd/looms/config.go:1236`), so most curated prompt text (including `tool_search` usage guidance) never reaches a default deployment.

### RC6 — Auxiliary LLM calls are unbounded and unattributed
§4 table. No budget, quota, or metric covers extraction, distillation, rerank, compression, `tool_search` rerank, skill-index warm-up (**1 LLM call per index node on every boot**, even when a persisted index loaded — `pkg/agent/registry.go:2591-2615`), or multi-agent injected turns. The only cost cap in the tree is `max_cost_usd` in eval suites (`pkg/evals/suite_loader.go:52`). This aligns with the P1 "cost budget as a stop rule" gap in `docs/architecture/loop-engineering.md`.

### RC7 — Retry layering with no global cap
Each layer is individually sane; nothing bounds the product:
- Iterative workflows: 3 iterations × 3 validation attempts = **up to 9 full agent loops per stage** (`iterative_pipeline_executor.go:150-152,328-334`), each loop itself ≤25 turns/≤50 executions; +1 validator LLM call per attempt when `validation_prompt` is set.
- Rate-limiter throttle retries ×6 nested under agent LLM retries ×4 = up to 24 HTTP calls per logical LLM call — though the agent-level retry is **effectively disabled on the serve path** (`Retry.Enabled` false because `WithConfig` replaces the config without it — `llm_retry.go:104`, `cmd_serve.go:1837-1846`; also bypassed under streaming). Whether that gating is intentional needs a decision.
- Good news: `OutputRetryPolicy` defaults to 0; pipeline retries use fresh sessions (no context replay); the MCP backpressure freeze retries **without any LLM calls** (`pkg/mcp/adapter/backpressure_wait.go`) — the right shape, but unbounded in attempts (15-min time budget only).

### RC8 — Tool surface and discovery frictions
- Default `looms serve` agent ≈ **16 tools + N MCP tools** (~18–22 KB of schema JSON; ~170 tokens/tool); bare `NewAgent` = 3. No cap or count-triggered hiding exists — only a log warning >100 MCP tools (`mcp_integration.go:127-132`).
- The `tool_search` index contains **only the 10 builtins + MCP tools**: `graph_memory`, `task_board`, `recall`, `query_tool_result`, `workspace`, comm tools are never indexed (`pkg/tools/registry/indexers.go:76-79`) — searches for them burn a call and miss. No periodic re-index (boot + MCP add/delete only), matching the stale-index incident of 2026-07.
- Mid-conversation registrations (lazy UI-tool promotion on a 20-keyword trigger — permanent once fired, `agent.go:965-988`; skill loads; dynamic MCP registration) **invalidate the provider's cached tool-block prefix** (`pkg/llm/anthropic/client.go:374-405`), raising cost per subsequent call.
- `graph_memory`/`task_board` are re-registered after every `Chat` (`agent.go:1908-1910`), silently undoing the registry's unregister pass — a config-respect defect.

---

## 6. What already works (credit where due)

| Mitigation | Where |
|---|---|
| ✅ Hard caps: 25 turns / 50 executions / 10 per response | `types.go:390-392`, loop `agent.go:2278,2625` |
| ✅ Within-response identical-call dedup | `agent.go:2666-2684` |
| ✅ Soft budget reminders (75–90% band) | `conversation_helpers.go:90-129` |
| ✅ Output-token circuit breaker (hard stop, threshold 8) | `agent.go:2462-2503` |
| ✅ Cap-hit → tools-off synthesis call instead of hard error | `agent.go:2906-2921` |
| ✅ Recall injected automatically (costs no *tool* call) | `agent.go:2275,3298` |
| ✅ MCP backpressure freeze retries below the model (0 LLM calls) | `pkg/mcp/adapter/backpressure_wait.go` |
| ✅ Tool-schema prompt caching (`cache_control` on last tool) | `pkg/llm/anthropic/client.go:374-405` |
| ✅ Parallel `tool_use` blocks all execute (batching is possible) | `agent.go:2640` |
| ✅ `OutputRetryPolicy` defaults to 0; workflow retries don't replay context | `collaboration.proto:15-37`, `pipeline_executor.go:845` |

---

## 7. Verified defects and doc drift found during this assessment

1. **Circuit breaker blind to `Result`-level failures** — `agent.go:3026-3028` vs `pkg/mcp/adapter/shuttle.go:332-336` (RC1).
2. **Live `graph_memory` FK failure** — `STORE_ERROR: link entity <name>: FOREIGN KEY constraint failed`, 2,783 occurrences in 2026-08.
3. **`SetOffloadExemptTools` unwired** — no production caller; nil default (`agent.go:3905`, `memory.go:65`).
4. **Serve path runs with LLM transport retry disabled** — `WithConfig` drops `Retry` (`llm_retry.go:104`; only `cmd_eval.go:164` keeps defaults). Intentional? Decide and document.
5. **`graph_memory`/`task_board` re-registered after every `Chat`**, overriding explicit unregistration (`agent.go:1908-1910`).
6. **Inert config/dead code**: `conversation_extraction_cadence` (parsed, never read); `UseLLMClassifier: true` with no caller of `ClassifyIntent`; extraction PASS 2; `Emitter.EmitForActivation`; `OutputValidator.ValidateAndRetry` (no production callers).
7. **Doc drift**: 64 KiB offload threshold claimed in 5 docs (`agent-runtime.md:516`, `agent-system-design.md:461`, `12-factor-architecture.md:469`, `data-flows.md:455-457`, `pattern-system.md:632`) — code says 16 KiB; `agent-system-design.md:461` claims three hardcoded offload-exempt tools — no such set exists.
8. **`tool_search` index gaps** — Loom's own builtins (`graph_memory`, `task_board`, …) are unfindable via `tool_search`; index built with a nil prompt registry so descriptions can diverge from what agents advertise (`indexers.go:76-79`).

---

## 8. Recommendations (prioritized)

### P0 — stop the storms
- [ ] **Count `Result.Success == false` toward the tool circuit breaker** (`agent.go:3026`). Respect a `Retryable`/backpressure hint so business errors with legitimate retry semantics (e.g., the backpressure freeze) aren't over-penalized.
- [ ] **Enforce the identical-failure escalation**: after the existing threshold-2 tracker fires, refuse byte-identical re-executions with a synthetic result ("identical call already failed twice — change the input or the approach") instead of only advising. The dedup-key infrastructure (`agent.go:2666`) is reusable session-wide.
- [ ] **Root-cause the `graph_memory` FK failure** (entity linking on `remember`) — it is the single largest error source in the data and every failure recruits the retry loop.
- [ ] **Decide `SetOffloadExemptTools`**: wire it (YAML knob) or delete it. Its absence plus 16 KiB truncation is what forces re-runs of large-result tools.

### P1 — remove the structural incentives to re-call
- [ ] **Let offloaded data survive the turn**: allow `query_tool_result` against durable prior-turn rows (or persist fetched pages), replacing `not_this_turn` + "re-run the producing call" with a cheap re-read. Update `prompts/tools/progressive_disclosure.yaml` accordingly.
- [ ] **Add call-efficiency guidance to `START_HERE.md` ROM** (every agent sees it): batch independent calls in one response; never repeat a call that just failed with the same input; reuse results already in context. Zero runtime cost; directly targets the 12.7% duplicate rate and 13% batching rate.
- [ ] **State the budget early**: agents discover the 50-call budget only at 75% consumption; include remaining budget in the system prompt or lower the reminder band.
- [ ] **Meter auxiliary LLM calls**: emit per-turn observability for extraction/distillation/rerank/compression (they're currently invisible), and add a per-session auxiliary budget. De-duplicate the double extraction trigger (per-`Chat()` at `agent.go:1884` *and* per-5-tools at `agent.go:2882`).
- [ ] **Codify the metrics from §3 as the regression yardstick** (calls/user-turn, error rate, retry-after-error rate, duplicate rate, aux amplification) — e.g., a `loom analytics tools` subcommand over `tool_executions`, in the spirit of `loom scheduler`.

### P2 — composition hygiene
- [ ] Index Loom's own builtins in `tool_search`; rebuild the index with the active prompt registry; add re-index on registration change.
- [ ] Optional session-scoped result cache for idempotent tools (idempotency flag on `shuttle.Tool`), turning cross-turn duplicates into cache hits.
- [ ] Fix doc drift (16 KiB; exempt-tools claim) and resolve §7.6 dead paths (wire or remove).
- [ ] Decide serve-path LLM retry gating (§7.4) and document the intent.
- [ ] Skill-index warm-up: skip rebuild when a persisted index loads (`registry.go:2591-2597`).

### How to verify improvement
Re-run the §3 queries after each P0/P1 lands (same DB, time-windowed). Targets worth proposing: error rate <10%, retry-after-error <30%, duplicate rate <3%, aux amplification visible in traces. The MCP gauntlet rig is the natural A/B harness for the connect-storm scenario.

---

## 9. Cross-references

- `docs/architecture/loop-engineering.md` — P1 "cost budget as a stop rule" (this assessment quantifies why), P0 runtime verification loop.
- `docs/research/ccn-authorization-assessment.md` — sibling assessment format.
- v1.4.0 release findings — `SetOffloadExemptTools` unwired, 64 KiB doc drift (both confirmed here with call-site evidence).
- MCP 2026-07-28 backpressure design (issue #354) — the model-invisible retry pattern P0.1 should generalize.

# Loop Engineering and Loom

Analysis of Loom's conversation loop and surrounding machinery against the "loop engineering" discipline that crystallized in mid-2026. Companion to [12-factor-architecture.md](12-factor-architecture.md).

**Target audience**: Contributors, architects
**Version**: v1.3.0 (analyzed on `main` @ `68208a0a`, 2026-07-30)

---

## Table of Contents

- [What Loop Engineering Is](#what-loop-engineering-is)
- [The Four-Loop Model Mapped to Loom](#the-four-loop-model-mapped-to-loom)
  - [Loop 1: The Agent Loop](#loop-1-the-agent-loop--implemented)
  - [Loop 2: The Verification Loop](#loop-2-the-verification-loop--implemented-for-workflows-missing-in-the-agent-loop)
  - [Loop 3: The Event-Driven Loop](#loop-3-the-event-driven-loop--partial)
  - [Loop 4: The Hill-Climbing Loop](#loop-4-the-hill-climbing-loop--implemented-offline-not-closed)
- [Principles Scorecard](#principles-scorecard)
- [Gap Analysis and Recommendations](#gap-analysis-and-recommendations)
- [References](#references)

---

## What Loop Engineering Is

"Loop engineering" named a discipline in June 2026 through three converging statements: Boris Cherny (Claude Code creator): *"I don't prompt Claude anymore... my job is to write loops"*; Peter Steinberger (June 7): *"You shouldn't be prompting coding agents anymore. You should be designing loops that prompt your agents"*; and Addy Osmani's essay "Loop Engineering" (June 8, canonized by O'Reilly Radar June 22). The substance predates the name — most of it is in Anthropic's engineering posts (2024–25), Simon Willison's "Designing agentic loops" (Sept 2025), Cognition's "Don't Build Multi-Agents" (June 2025), and HumanLayer's 12-Factor Agents.

The now-standard layered stack:

| Layer | Optimizes | Era |
|---|---|---|
| Prompt engineering | One message | 2022–24 |
| Context engineering | What fills the context window each turn | 2025 |
| Harness engineering | The environment around one agent run (tools, prompts, sandbox, hooks) | early 2026 |
| **Loop engineering** | **The machinery across runs: triggers, topology, verifiers, stop rules, budgets, persistent state, escalation, cross-run improvement** | mid 2026 |

The empirical case that this matters: Harness-Bench ([arXiv:2605.27922](https://arxiv.org/abs/2605.27922)) measured 13–21 point swings on the *same model* across harnesses, concluding "agent capability should be reported at the model-harness configuration level rather than attributed to the base model alone." The loop is a first-order capability variable.

Loom is a loop-engineering framework by construction — the framework, not the user, owns the control flow (12-Factor Agents' "own your control flow"). This document assesses how much of the discipline Loom actually covers.

---

## The Four-Loop Model Mapped to Loom

LangChain's "The Art of Loop Engineering" (June 2026) describes four stacked loops. This is the cleanest skeleton for mapping Loom.

### Loop 1: The Agent Loop — ✅ Implemented

The inner loop: model calls tools until done. Loom's implementation is `Agent.runConversationLoop` (`pkg/agent/agent.go:2187`).

Pre-loop, once per `Chat`: freeze the base advertised tool set (`captureBaseTools`, `agent.go:2197`), initialize the self-healing recovery orchestrator if enabled (`agent.go:2208`), run lazy tool disclosure against the incoming user message, and inject graph-memory context (`injectGraphMemoryContext`, `agent.go:3216`).

Per-iteration order:

1. **Budget guard**: `for turnCount < MaxTurns && toolExecutionCount < MaxToolExecutions` (`agent.go:2235`) — defaults 25 / 50, plus a per-turn tool-call cap `MaxIterations` (default 10, `pkg/agent/types.go:371-373`)
2. **Token-budget enforcement** before the LLM call: `enforceTokenBudget` (`pkg/agent/conversation_helpers.go:154`) force-compacts memory above 70% budget; if still >85% after compaction, the recovery tier trims aggressively or the loop fails with a recoverable error (`agent.go:2246-2287`)
3. **Message assembly**: `SegmentedMemory.GetMessagesForLLM` (`pkg/agent/segmented_memory.go:1003`) assembles ROM system prompt → L2 summary → promoted-from-swap context → L1 hot messages. The system slot carries **only ROM + L2 summary** — a test-enforced invariant (`pattern_findings_channels_deletion_black_box_test.go`); everything else enters context as tool results or sidecars
4. **Soft reminders**: nudge text appended in the 75–90% window of the tool/turn budgets (`conversation_helpers.go:177-216`)
5. **Tools re-derived every iteration**: `advertisedTools(session)` filtered by `recovery.activeTools` (`agent.go:2331`) — progressive disclosure and circuit-breaker exclusions take effect mid-conversation
6. **LLM call** with retry/backoff (`chatWithRetry`, `pkg/agent/llm_retry.go`), optionally tapped by the context dumper (see Loop 4)
7. **Output-token circuit breaker**: trips after 8 consecutive `max_tokens` responses that contain a truncated/empty tool call; plain text hitting the limit clears the counter; configurable via `OutputTokenCBThreshold` (`agent.go:2387-2470`, `types.go:304-307`)
8. **Tool dispatch**: dedup, HITL `contact_human` detection, then `executeToolWithSelfCorrection` (`agent.go:2684`) wrapping each call in per-tool circuit breakers and guardrail error annotation; consecutive identical failures inject a "stop retrying, change approach" escalation at threshold 2 (`agent.go:2794`)
9. **Background graph-memory extraction** fires asynchronously after tool execution (`agent.go:2858`); `text_body` sidecars (e.g. skill bodies) are buffered and drained after the tool batch to preserve tool_use/tool_result adjacency (`agent.go:2844-2883`)
10. **On turn/execution exhaustion**: one final tools-disabled synthesis call forces a text answer instead of dying mid-thought (`agent.go:2911-2912`)

Mapped to Anthropic's "gather context → take action → verify work → repeat":

| Phase | Anthropic guidance | Loom mechanism | Status |
|---|---|---|---|
| Gather | Compaction preserving decisions | Segmented memory (ROM/Kernel/L1/L2/Swap) with LLM compaction default-on (registry prompt `memory.compaction`) and a tool-pair-preserving compression boundary (`segmented_memory.go:466`) | ✅ |
| Gather | Structured note-taking / memory | Graph memory: context injected pre-loop, entities extracted asynchronously post-tool (`WithGraphMemoryStore`, `agent.go:4361`) | ✅ |
| Gather | Just-in-time retrieval via lightweight identifiers | Skills: ROM carries only a name+description menu; bodies pull-loaded via `manage_skills(load)` as sidecars. Patterns loaded on demand via `load_pattern` tool. Large tool results offloaded to shared memory as `DataReference`; SQL results queryable via `query_tool_result` | ✅ |
| Act | Tools reflect primary actions; progressive disclosure | Tool surface re-derived per iteration (`agent.go:2331`); lazy disclosure; MCP dynamic discovery | ✅ |
| Verify | Deterministic feedback > judges | — (agent loop) | ⚠️ See Loop 2 |

**Notable strengths vs. published practice**:
- The system-slot minimalism (ROM + L2 only, everything else pull-based) is a deliberate redesign directly aligned with Anthropic's just-in-time retrieval guidance — earlier designs auto-injected pattern guidance and a findings block into the system slot; both were removed in favor of tool-loaded context
- The tool-pair-preserving compaction boundary addresses Cognition's warning that compression "is hard to get right"
- The forced-synthesis exit, truncation-gated output-token breaker, and adaptive failure escalation are stop-rule refinements most frameworks lack
- A self-healing recovery tier (`pkg/agent/recovery.go`: token-budget trim, output-token rewind, tool circuit-breaker exclusion) sits between "retry" and "abort"

**Limitations**:
- No dollar/cost ceiling on the loop: `Usage.CostUSD` is recorded to spans but is not a stop condition (`max_cost_usd` exists only in `eval.proto` for offline evals)
- Pattern intent classification and recommendation are dormant: the classifier infrastructure exists (`pkg/patterns/llm_classifier.go`) but nothing in the runtime calls `ClassifyIntent`/`RecommendPattern` — pattern choice is delegated to the model via `load_pattern`

### Loop 2: The Verification Loop — ✅ Implemented for workflows, 📋 missing in the agent loop

The discipline's near-unanimous core claim: **verification is the reward signal**. "Agent runs, output is scored against a rubric, retried with feedback if it fails" (LangChain). Deterministic verifiers (tests, schemas) beat LLM judges, which are "generally not very robust" (Anthropic). Without a verifier that can *reject* output, a loop is theater.

**At the workflow-stage level (`pkg/orchestration`), Loom now implements the full pattern:**
- ✅ JSON-Schema structural validation: `PipelineStage.output_schema` → `validateStageOutputSchema` (`pipeline_executor.go:350-361`)
- ✅ LLM judge per stage: `stage.ValidationPrompt` scored by a merge LLM (`pipeline_executor.go:720-764`)
- ✅ `OutputRetryPolicy` with feedback retry: max retries, cooldown, feedback template, and `RetrySessionMode` (CONTINUE / FRESH / ESCALATE) driving `retryStage` (`pipeline_executor.go:374-395`) — retries carry an explanation of what failed, matching the "retries must adapt" principle
- ✅ HITL gates: human approve/revise/reject between stages, with revision feedback threaded back to a target stage (see Loop 3)

**At the single-agent loop level:**
- 📋 Nothing scores the assistant's *final output* before it is returned — no verifier hook, no output-schema contract on `Chat`, no judge-in-loop
- ⚠️ A universal `OutputValidator.ValidateAndRetry` exists (`pkg/orchestration/output_validator.go`) but has **no production callers** — the live path is the pipeline executor's own `retryStage`
- ✅ Error-path correction exists (guardrail annotation, circuit breakers, escalation), but that verifies *tool failures*, not *answer quality*

This split is the highest-leverage gap: the machinery exists one layer up — see [recommendations](#gap-analysis-and-recommendations).

### Loop 3: The Event-Driven Loop — ⚠️ Partial

External triggers fire runs (Osmani's "automations — the heartbeat").

**What Loom has:**
- ✅ Cron scheduler: `pkg/scheduler/` (robfig/cron, 5-field) triggers workflow executions on schedule, persisted in `scheduler.db`, wired into `looms serve`
- ✅ API triggers: `Weave`/`StreamWeave` over gRPC/HTTP — any external system can fire a run
- ✅ MCP bridge: `loom_weave` exposes the loop as an MCP tool to external clients (`pkg/mcp/server/bridge_tools.go`)
- ✅ Durable suspend/resume: HITL gates raise `WorkflowSuspended` with a `WorkflowCheckpoint` (shared memory + stage outputs snapshotted); hosts persist it and later call `ResumeWorkflow` with a `GateDecision`; a workflow fingerprint check refuses resume against edited definitions (`pkg/orchestration/hitl_gate.go`, `resume.go`, `proto/loom/v1/orchestration.proto`) — long-lived loops survive process restarts
- ✅ Inter-agent events: communication bus topics with auto-subscription; inbound messages injected into agent context

**What Loom lacks:**
- 📋 Webhook/event-source triggers (file watch, repo events, queue consumers) — cron and explicit API calls only
- 📋 User-definable lifecycle hooks (deterministic pre/post-tool interception, as in Claude Code hooks); the guardrail engine and end-of-turn skill hygiene enforcer are internal, not user extension points

### Loop 4: The Hill-Climbing Loop — ✅ Implemented offline, not closed

"Traces from production runs feed an analysis agent that improves the harness config" (LangChain Loop 4). This is the frontier of the discipline ("Self-Harness" research, swyx's "go up a loop as models improve") — and Loom's strongest differentiator relative to other frameworks.

**What Loom has:**
- ✅ Traces → evals automatically: `EmbeddedTracer.spanToEvalRun` (`pkg/observability/embedded.go:282`) converts every root conversation span into an eval run
- ✅ Prompt optimizers: MIPRO (`pkg/metaagent/teleprompter/mipro.go:56`), COPRO, BootstrapFewShot, TextGrad — judge-scored, DSPy-derived; TextGrad has a proto-level `AutoApplyMode` for applying optimized prompts (defaults to MANUAL)
- ✅ Deployment metrics collection (`pkg/metaagent/learning/collector.go`)
- ✅ Ground-truth loop observability: the context dumper (`pkg/agent/context_dump.go`) captures the exact `(messages, tools)` sent on every provider call as local JSONL (off by default, never exported) — the raw material for analyzing loop behavior offline

**What Loom lacks:**
- 📋 The loop around the loop is not closed: teleprompter runs are human-initiated, not scheduled; nothing automatically re-optimizes prompts/patterns from accumulated eval runs
- ⚠️ Pattern effectiveness tracking is present but unwired: `PatternEffectivenessTracker` and `RecordPatternUsage` exist (`pkg/patterns/orchestrator.go:426`) and `PatternConfig.EnableTracking` defaults true, but **no production code calls the recording path** — pattern usage currently generates no learning signal

### Cross-cutting: Osmani's six components

| Component | Loom analogue | Status |
|---|---|---|
| Automations (triggers) | Cron scheduler, API/MCP triggers | ⚠️ cron + API only |
| Worktrees (isolation) | Docker executor sandbox (`pkg/docker/executor.go`); ephemeral agents with per-parent cap and cost limits | ✅ |
| Skills (codified knowledge) | `pkg/skills/`: 3-tier library, LLM-routed discovery index, ROM menu + pull-loaded bodies, end-of-turn hygiene auditor, hot reload, docs importer; skills reference patterns via `PatternRefs` | ✅ |
| Plugins & connectors (MCP) | MCP client + server bridge (both directions) | ✅ |
| Subagents | `manage_ephemeral_agents` tool — mid-loop spawn/despawn with per-parent cap and `cost_limit_usd` (`pkg/server/spawn_agent.go`); declarative workflows (pipeline, fork-join, debate, swarm) | ✅ |
| External state | SQLite sessions, swap-layer "forever conversations", graph memory, shared memory store, task board (`pkg/agent/task_board_tool.go`, opt-in kanban with LLM decomposition) | ✅ |

---

## Principles Scorecard

Synthesized principles from the literature, scored against Loom:

| # | Principle (source) | Loom | Evidence |
|---|---|---|---|
| 1 | Own the control flow in code, not the model (12-Factor Agents) | ✅ | Framework owns loop, budgets, retries, HITL (`agent.go:2187`) |
| 2 | Termination is a first-class design requirement — multiple independent exits (Masood) | ✅ | MaxTurns + MaxToolExecutions + per-turn MaxIterations + truncation-gated output-token breaker + failure escalation + forced synthesis + self-healing recovery tier; ⚠️ no cost ceiling |
| 3 | Verification is the reward signal; prefer deterministic verifiers (Anthropic, LangChain) | ⚠️ | Workflow stages: output_schema + ValidationPrompt + OutputRetryPolicy ✅; agent loop: nothing 📋 |
| 4 | Maker/checker split (Black Matter, Osmani) | ⚠️ | HITL gates = human checker; stage ValidationPrompt = LLM checker; no verifier-subagent primitive for the agent loop |
| 5 | Pick loop-shaped problems: clear success criteria + trial-and-error (Willison) | ⚠️ | Skills/patterns encode problem shape; success criteria machine-checkable only at workflow-stage level (see #3) |
| 6 | Externalize state; treat context as disposable (Huntley, Anthropic) | ✅ | SQLite sessions, swap layer, graph memory, shared memory, task board |
| 7 | Manage the attention budget every iteration (Anthropic) | ✅ | Token counting, layered memory, pre-call enforcement, LLM compaction default-on, system-slot minimalism (ROM + L2 only) |
| 8 | Single-threaded default; share full traces if parallel (Cognition) | ⚠️ | Single loop is the default ✅; multi-agent shares messages via bus, not full traces |
| 9 | Sandbox + least privilege + scoped credentials (Willison) | ✅ | Docker executor, guardrails, `allow_code_execution`/`allowed_domains` config, high-risk skill approval gating |
| 10 | Transactional, idempotent, auditable loop actions (Cockroach Labs) | ⚠️ | Every step persisted + span-traced; workflow checkpoints are fingerprint-verified; tool actions themselves have no idempotency/transaction contract |
| 11 | Retries must adapt, not repeat (Masood) | ✅ | Consecutive-failure escalation in-loop; stage retries with feedback templates and FRESH/ESCALATE session modes |
| 12 | Stay the engineer — observability against comprehension debt (Osmani) | ✅ | Span taxonomy per iteration/LLM/tool call; OTLP export with GenAI semconv (`pkg/observability/otel.go`); exact per-call context dumps (`context_dump.go`) |

---

## Gap Analysis and Recommendations

Prioritized. Each follows "proto is law" — API surface changes start in `proto/loom/v1/`.

### P0: Verification in the agent loop

The single highest-leverage addition, and most of the machinery already exists one layer up in `pkg/orchestration`:

1. Add a verification config to `BehaviorConfig` (proto), mirroring the stage-level surface: output schema, validation prompt, retry policy with feedback template
2. On loop exit (the terminal check, before returning), run the verifier against the final response; on failure, re-enter the loop with a retry message that **explains why it failed and shows the expected form** — feedback-free retries just repeat the failure
3. Concrete starting point: `OutputValidator.ValidateAndRetry` (`pkg/orchestration/output_validator.go`) already composes schema validation with retry-session modes but has no callers — wire it rather than writing new machinery
4. Order verifiers by cost and determinism: schema validation → command execution (exit code) → LLM judge, per Anthropic's hierarchy

### P1: Cost budget as a stop rule

Cost per conversation is already computed for spans/metrics. Promote it to a loop guard alongside MaxTurns: `max_cost_usd` in `BehaviorConfig`, checked at the top of each iteration, with the same forced-synthesis exit. Ephemeral agents already have `cost_limit_usd`, and `eval.proto` already models `max_cost_usd` for offline evals — this closes the gap for the primary loop. Directly addresses the discipline's most-cited operational failure (silent cost blowup; agent loops ≈ 4x chat token burn).

### P1: Re-wire pattern effectiveness recording

`RecordPatternUsage` and `PatternEffectivenessTracker` exist but nothing calls them since pattern loading moved to the `load_pattern` tool. Without this signal, pattern usage generates no learning data and Loop 4 has a blind spot. The natural hook is the `load_pattern` tool handler plus end-of-conversation outcome attribution.

### P2: Maker/checker as a first-class pattern

A verifier-subagent template: ephemeral agent spawned with the output to check, different prompt (and optionally different model), returning pass/fail + critique. The spawn machinery and collaboration patterns already exist; this is a template plus a documented convention, not new infrastructure. HITL gates already cover the human-checker variant at workflow level.

### P2: Close the hill-climbing loop

Schedule teleprompter/analysis runs via the existing cron scheduler: accumulate eval runs (already automatic via `spanToEvalRun`) → periodic optimization pass → propose prompt/pattern updates gated by a HITL gate (dogfooding the workflow machinery). `AutoApplyMode` already exists in proto for TextGrad (default MANUAL) — the missing piece is the trigger and the approval flow, not the optimizer.

### P2: Loop-config fingerprint on spans

Harness-Bench's conclusion is that results are only meaningful at the model+harness configuration level. Record the loop configuration (max_turns, max_tool_executions, compaction profile, verification config, skill/pattern bindings) as attributes on the root conversation span — the OTel attribute mapping (`otel_attrs.go`) already forwards unknown keys with a `loom.` prefix, so eval runs become attributable to a specific loop design and A/B-ing loop changes works with existing infrastructure.

### P3: Event-source triggers and lifecycle hooks

Webhook/queue/file-watch trigger sources for the scheduler; user-definable pre/post-tool hooks as a guardrail-engine extension point. Lower priority: the API-trigger path already covers most integration needs.

---

## References

**Coinage and discipline:**
- Osmani, "Loop Engineering" — [O'Reilly Radar](https://www.oreilly.com/radar/loop-engineering/) (June 2026)
- LangChain, "The Art of Loop Engineering" — [langchain.com](https://www.langchain.com/blog/the-art-of-loop-engineering) (June 2026)
- swyx, "Loopcraft: The Art of Stacking Loops" — [latent.space](https://www.latent.space/p/loopcraft) (June 2026)
- Masood, "Loop Engineering: A Guide for Engineers and Practitioners" — [Medium](https://medium.com/@adnanmasood/loop-engineering-a-guide-for-engineers-and-practitioners-893bb65ea943) (June 2026)

**Precursors (the substance):**
- Anthropic, "Building Effective Agents" — [anthropic.com](https://www.anthropic.com/engineering/building-effective-agents) (Dec 2024)
- Anthropic, "Effective Context Engineering for AI Agents" — [anthropic.com](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents) (Sept 2025)
- Anthropic, "Building Agents with the Claude Agent SDK" — [claude.com](https://claude.com/blog/building-agents-with-the-claude-agent-sdk) (Sept 2025)
- Willison, "Designing Agentic Loops" — [simonwillison.net](https://simonwillison.net/2025/Sep/30/designing-agentic-loops/) (Sept 2025)
- Cognition, "Don't Build Multi-Agents" — [cognition.com](https://cognition.com/blog/dont-build-multi-agents) (June 2025)
- HumanLayer, "12-Factor Agents" (2025)

**Evaluation and criticism:**
- Harness-Bench — [arXiv:2605.27922](https://arxiv.org/abs/2605.27922) (May 2026)
- "When Agents Do Not Stop: Uncovering Infinite Agentic Loops in LLM Agents" — [arXiv:2607.01641](https://arxiv.org/abs/2607.01641) (July 2026)
- Hagoel, "Loop Engineering Minus the Hype" — [dev.to](https://dev.to/isaachagoel/loop-engineering-minus-the-hype-4ibn) (July 2026)
- Cockroach Labs, "Agent Loops: Production Database Patterns" — [cockroachlabs.com](https://www.cockroachlabs.com/blog/agent-loops-production-database-patterns/) (July 2026)

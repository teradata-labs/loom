# Apprentice: Turning Observed Work into Reusable Automation

**Status**: ⚠️ Partial — the P0 distiller (`pkg/apprentice`) and a research spike are implemented.
v1 is specified below and not built.
**Scope**: `loom` only. Everything touching `avmo-tera-cloud` or `up-tera` is deferred — see
[Deferred](#deferred-with-reasons).
**Working name**: apprentice. Prior working name "track task" is rejected — see [Naming](#naming).

---

## Problem

A user works through a multi-step procedure — profile a table, fix the skew, re-run the load,
check the row counts. It works. Nothing captures it. The next person re-derives it from scratch,
and so does the same person next month.

Loom can already *author* automation from intent: the weaver turns "build me a research workflow"
into `workflows/research.yaml`. What it cannot do is author from **evidence** — take work that
actually happened and turn it into something reusable.

That division is why this belongs in loom: **the weaver authors from intent; the apprentice
authors from evidence.** Both emit the same artifact types through the same restricted write path
([`pkg/shuttle/builtin/agent_management.go:188`](../pkg/shuttle/builtin/agent_management.go:188)).

---

## v1: one command

```
loom apprentice watch
```

At the end of a session: read that session's `tool_executions` and `messages`, abstract the
concrete literals into named parameters, emit **one candidate skill YAML**, and show it in the TUI
for review. The user edits it, names it, and deputizes it — or discards it.

That is the whole of v1. Explicitly **not** in it: observe mode, recurrence detection, AGENT and
TRIGGER candidates, workflow candidates, UI-event capture, multi-tenancy, derived task boards.
Each is deferred with a reason below.

The scope is this small on purpose. Fifty years of prior art says general demonstration systems
fail and narrow ones ship — see [Prior art](#prior-art). An earlier draft of this document had
three capture channels, four candidate kinds, two modes, and work in three repositories. That was
scope accretion, not design.

### v1 exit criterion

Point it at ten real sessions and count how many candidates **someone other than the author**
would deputize. Measured yield in the corpus surveyed was roughly six worthwhile procedures per
284 sessions (constraint 6), so the bar is modest — but the judge has to be a human other than the
tool's author, or the number means nothing.

---

## Naming

The feature is **apprentice**; the thing it emits is a **candidate**; the act of accepting one is
**deputizing** it.

| Term | Meaning |
|---|---|
| **apprentice** | The meta-agent that watches and proposes. Sits alongside `weaver` and `guide`. |
| **candidate** | A proposed skill, with provenance and a confidence score. Not yet active. |
| **deputize** | Accepting a candidate: it is written through the existing create path and becomes live. |

### Names rejected, with reasons

| Name | Why not |
|---|---|
| **track task** | Collides with `pkg/task`, cloud's `cloud_tasks`, and up-tera's `features/tasks`, all of which are different from this. The task board is not even this feature's subject — see constraint 2. |
| **draft** | Correct in the weaving sense, but already taken in the same domain: `draft_skill_name` / `draft_skill_content` (`proto/loomcloud/v1/session.proto:197`) and `AdminDraftSkillCache` mean "an unsaved skill under test in an admin session". Also one character from `pkg/drift`. |
| **shadow** | 15 existing hits across the two repos, and the wrong tone for a feature whose adoption depends on people consenting to be recorded. |
| **duet** | Implies real-time collaboration. This is capture-then-reuse; the timing is wrong. Also a live product name elsewhere. |
| **deputy** | Names the end state rather than the mechanism. Retained as the *verb*, which is exactly what it describes. |

`apprentice`, `candidate`, and `deputize` have zero hits across `loom` and `tera-backend`.

---

## Prior art

This is a 50-year-old research field called **programming by demonstration**, and the canonical
text is literally titled *Watch What I Do: Programming by Demonstration* (Cypher et al., MIT Press
1993). The lineage runs Pygmalion (1975) → macro recorders → Eager (1991) → CoScripter/Vegemite →
RPA (UiPath, Blue Prism) → LLM agents. **None of the general systems achieved broad adoption**, and
Lau's retrospective is blunt that the barrier was usability rather than algorithmic capability. The
things that did ship were narrow: the spreadsheet macro recorder, Selenium IDE, Playwright codegen.

Every documented failure falls into two buckets:

1. **Generalizing from one example.** Turning a demonstration into a program requires inferring
   intent, and automatic generalization routinely gets it wrong. The literature's mitigations are
   multi-shot demonstration or hand-configuration. Eager waited for **two complete iterations**
   (three for trivial patterns) before offering anything — it inferred from *repetition*, not from a
   single demo.
2. **Brittleness of the capture substrate.** GUI, DOM, and pixel capture breaks whenever the
   interface changes. This killed CoScripter and Vegemite, is the standing critique of RPA, and
   remains a stated limitation of ALLOY (2025), which is confined to web environments.

**Where the apprentice differs, and it is the whole bet:** it captures at the *semantic* layer —
tool calls with structured inputs — not pixels or DOM. Tool schemas are versioned contracts; GUIs
are not. That sidesteps bucket 2, the bucket that killed most of the lineage.

v1 does **not** escape bucket 1: it generalizes from a single session, which is the documented
failure mode. That is a deliberate, stated risk. The mitigation is not an algorithm but a human —
every candidate is reviewed and edited before it can be deputized, and nothing is ever
auto-published. Repetition-based inference is the right long-term answer, and it is deferred.

**Concurrent competition.** xAI shipped **Grok Bot** on 11 Aug 2026: demonstrate a task once via
screen recording, it stores and replays the sequence, refines from corrections, and each bot gets
its own cloud computer. Same product idea, squarely in bucket 2. Fair validation that the problem
is worth solving, and a reason to be explicit that our differentiator is substrate, not concept.

**What nobody escapes.** Three limitations recur from Eager (1991) through ALLOY (2025) and should
be treated as boundaries rather than bugs: no conditionals, loops, or error recovery from a single
linear demonstration; granularity is an open empirical question (task-level misses procedural
constraints, action-level overfits to the interface); and capture records *how*, not *why*.

Two design details borrowed outright:

- **Split abstraction from instantiation** (ALLOY's Identifier and Filter agents) rather than
  generalizing in one pass.
- **Do not build a live step display.** Only 2 of ALLOY's 12 participants noticed the workflow
  updating during their demonstration; nearly everyone reviewed afterwards.

---

## Constraints from evidence

These came out of building P0 and running a read-only spike over a real 284-session corpus. They
are not features — they are guardrails, and each prevents a specific wrong turn. They cost days to
learn and they outlive any particular scope.

1. **Authored step order survives only where step indices do.** With the emitter's
   `SkillIdempotencyKey` present, recovery is exact. Without it, order can only be inferred from a
   dependency graph, and that is genuinely ambiguous wherever the graph fans out — two of the three
   shipped templates fan out. Anything needing true ordering must get it from a richer trace
   source. `Distill` reports unconstrained ordering as a warning rather than presenting a guess as
   evidence.

2. **Tasks are an output, not an input.** The board is empty in practice for a deeper reason than
   its default flag: maintaining it is extra work an agent must choose to do, while tool calls are
   recorded automatically as a byproduct of acting. `pkg/skills/hygiene` exists precisely to
   compensate for agents that do not keep it honest. Nothing in `pkg/task` or `pkg/skills` reads
   `tool_executions`, and `auto_decompose` decomposes a *goal* up front rather than reconstructing
   what happened. Compare Claude Code's background tasks, which do get created because they are a
   byproduct of noticing out-of-scope work — same data model, opposite creation dynamics. So the
   apprentice must never depend on tasks existing.

3. **`tool_executions` + `messages` is the only substrate that reliably exists.** A local corpus of
   284 sessions held 4,651 messages and 2,047 tool executions and **zero** tasks or boards, because
   `TaskBoardConfig` defaults to `Enabled: false`
   ([`pkg/agent/registry.go:785`](../pkg/agent/registry.go:785)).

4. **Do not drop failed steps.** The best procedure in the corpus is in a trace where all nine of
   its steps failed. One session asked for complete schema metadata on a Teradata database and
   issued nine well-formed catalog queries — database metadata, tables, row counts, columns,
   primary keys, foreign keys, indexes, table constraints, column constraints. All nine failed for
   a purely environmental reason (MCP client unavailable, then the circuit breaker opened) while
   the surrounding ~29 steps of genuine flailing mostly succeeded. **Intent lives in tool call
   inputs, not outcomes.** Any "only distill successful sessions" filter throws away the single most
   valuable candidate available.

5. **Fan-out means multiple agents, not merely independent steps.** Those nine catalog queries are
   mutually independent, so a naive "fan-out → workflow" rule classifies them as a nine-branch
   parallel workflow. They are nine queries one agent runs in a turn.

6. **Yield is a trickle, not a stream.** Of 284 sessions: 121 (43%) made zero tool calls, 76 more
   made 1–4, and only 35 (12%) made ≥15 — the plausible floor for holding a procedure worth
   capturing. Hand-reviewing the 22 richest produced roughly **six distinct deputizable
   procedures**. Useful, since each is a skill nobody had to author, but small.

7. **Watch mode beats observe mode at single-user scale.** Observe mode has to pan 250 near-empty
   sessions to find six procedures; watch mode has a human pointing at one. Observe mode only pays
   off at org scale.

8. **The largest category in the corpus is meta-work that must not be deputized.** Eight of the 22
   richest sessions are weaver-authoring runs ("create an agent that…"). Distilling those yields
   candidates duplicating `weaver-from-scratch`. The apprentice needs an explicit guard against
   proposing skills for using loom itself, or its first suggestions will all be noise.

9. **Recurrence detection is the crux, and both naive approaches fail.** Two sessions diagnosing
   the same "agent not showing up in the list" problem share intent and tool vocabulary but differ
   in order — sequence alignment **false-negatives**. Two sessions naming the same database for
   schema discovery ran completely unrelated procedures, one issuing catalog queries and the other
   coordinating agents via `send_message` — intent-text similarity **false-positives**. A
   fingerprint needs tool multiset *and* object-level structure. This is why observe mode is
   deferred rather than merely descoped.

10. **`SkillTaskTemplate.RootTitle` is documented but not implemented.** Its comment says it "names
    the parent task created to group emitted children", but `emitTemplate` never creates that
    parent or sets `ParentID`. All three shipped templates set `root_title` and all three lose it on
    a round trip. `TestRoundTrip_UnrecoverableTemplateFields` pins the gap.

---

## What already exists (verified — reuse, do not rebuild)

v1 is mostly assembly. The novel code is the trace reader and the abstract/instantiate pass.

| Capability | Where | Note |
|---|---|---|
| Skill type with machine-authorship fields | [`pkg/skills/types.go`](../pkg/skills/types.go) | `Confidence`, `Status`, `LastValidatedMs`, `RiskLevel` already on `Skill` |
| Skill write path, restricted to meta-agents | [`agent_management_skill.go`](../pkg/shuttle/builtin/agent_management_skill.go), [`agent_management.go:188`](../pkg/shuttle/builtin/agent_management.go:188) | Gate allows `weaver` and `guide`; `apprentice` must be added |
| Skill → tasks (the inverse direction) | [`pkg/skills/tasks/emitter.go`](../pkg/skills/tasks/emitter.go) | Materializes `SkillTaskTemplate.Steps`; P0 inverts it |
| Target output type | `SkillTaskTemplate` in [`pkg/skills/types.go`](../pkg/skills/types.go) | Not free-form YAML — the structure the emitter already consumes |
| Trace substrate | `tool_executions` in [`000001_initial_schema.up.sql`](../pkg/storage/postgres/migrations/000001_initial_schema.up.sql) | `session_id`, `tool_name`, `input_json`, `result_json`, `error`, `execution_time_ms`, `timestamp` |
| Skill YAML rendering | [`pkg/skills/importer/`](../pkg/skills/importer) | `render` and `classify` are reusable; the importer itself is not a distiller |
| Validation | [`pkg/skills/hygiene/`](../pkg/skills/hygiene), `pkg/evals` | |
| TUI command registration | [`commands.go:308`](../internal/tui/components/dialogs/commands/commands.go:308) | Same shape as `new_session` / `toggle_yolo` / `browse_apps` |
| TUI review dialog precedent | [`internal/tui/components/dialogs/`](../internal/tui/components/dialogs) | Existing `workflows`, `pattern`, `agents`, `sessions` browsers |
| Parameter confirmation channel | `QuestionAskedMsg` / `QuestionAnsweredMsg` + the `clarification` dialog | The weaver already uses this; v1's hardest interaction is already plumbed |
| Progress streaming | [`pkg/metaagent/tui_listener.go`](../pkg/metaagent/tui_listener.go) | `ProgressMultiplexer` with TUI and console listeners |

---

## v1 design

```
tool_executions + messages for one session
      │
      ▼
  segment ─► abstract ─► instantiate ─► emit ─► validate ─► candidate ─► review ─► deputize
```

- **segment** — the session's calls → one episode, with the goal taken from the first user message.
  Drop genuine noise (repeated identical searches, status-file churn, environment probing). **Keep
  failed steps** (constraint 4). Reject the session outright if it is weaver meta-work
  (constraint 8) or holds too few calls to contain a procedure (constraint 6).
- **abstract** — replace task-specific literals with named placeholders, preserving structure.
- **instantiate** — bind placeholders to typed parameters with defaults and descriptions.
- **emit** — a `SkillTaskTemplate` plus prompt instructions, rendered to skill YAML.
- **validate** — hygiene audit and `ValidateSkill`. Nothing reaches a user unvalidated.
- **review** — TUI dialog. Every candidate starts `PROPOSED` with low `Confidence`. No
  auto-deputizing, ever.

Splitting abstraction from instantiation is deliberate; generalizing in one pass from one example
is the documented failure mode.

### Where P0 fits, honestly

`pkg/apprentice.Distill` reads a *task board* and recovers a `SkillTaskTemplate`. Since v1 reads
`tool_executions` instead, **P0 is not on v1's execution path.** It keeps three kinds of value:

1. It established the output type, the `Reader` seam, and the warnings-not-errors discipline that
   v1's trace reader should copy. v1 adds a second source producing the same `SkillTaskTemplate`.
2. Its round-trip oracle stays in CI permanently as a regression suite for the emitter's template
   contract, independent of the apprentice.
3. It surfaced constraints 1 and 10, which is what a P0 is for.

---

## Testing

The feature is LLM-driven at exactly one step (abstract/instantiate) and deterministic everywhere
else. Testing follows that seam. **`just test` will not catch a worse prompt** — that is what the
eval tier is for, and conflating the two is how quality regressions ship unnoticed.

| Tier | Contents |
|---|---|
| **Deterministic units** | Trace assembly from fixture `tool_executions` + `messages`; noise filters; the meta-work guard; the too-few-calls gate; YAML emission against golden files. Table-driven, `-tags fts5 -race`. |
| **Round-trip oracle** ✅ | Real emitter → real sqlite board → `Distill` → structural diff, over every embedded skill authoring a `task_template`, plus re-emission as a fixpoint. In CI at 99.4% coverage. |
| **LLM step** | Recorded fixtures for CI (tests the code path, not the model) and `pkg/evals` for quality on a schedule, against a hand-labeled corpus of (trace → expected candidate) pairs. |
| **Adversarial** | **Highest-risk surface.** A captured trace is untrusted input whose text becomes a skill prompt. Cover prompt injection from captured tool output into `prompt.instructions` and `tools.required`, path traversal via candidate name, oversized candidates, candidates naming nonexistent tools, and any candidate whose `risk_level` would bypass the approval gate. Each must fail closed. |
| **Privacy** | Redaction as assertions, not policy: fixtures seeded with known secrets and PII must not appear in the persisted candidate. |
| **TUI** | Bubbletea message-flow tests following [`events_test.go`](../internal/tui/adapter/events_test.go) — watch started → progress → clarification asked → answered → candidate ready. Deterministic, no LLM. |

Coverage targets: `pkg/apprentice` ≥ 60%; validation and redaction paths ≥ 80%.

---

## Deferred, with reasons

Nothing here is rejected. Each item is out of v1 because it costs more than it returns until v1
proves candidates are worth reviewing.

| Deferred | Reason |
|---|---|
| **Observe mode + recurrence** | The hardest part of the design, and constraint 9 shows both naive fingerprinting approaches fail on real data. Constraint 7 says it only pays off at org scale. |
| **WORKFLOW candidates** | Second, when a multi-agent trace shows up. 41 of 284 sessions used `send_message`, so the data exists — but constraint 5 says the decision rule needs tightening first. |
| **AGENT and TRIGGER candidates** | TRIGGER is the cheapest future win: `SkillTrigger` (`keywords`, `intent_categories`, `mode`, `min_confidence`) and `ScheduleWorkflow` both exist, and nothing currently proposes values for them. |
| **First-party UI event capture** | The real gap: v1 can only watch agents, and a person clicking through Tera Cloud produces zero tool calls — which is why 43% of the corpus has none. The pipe exists and is unused: `trackEvent(name, metadata)` in up-tera's `src/lib/pendo.ts` has exactly one call site (`session.create`). Deferred because it is a second repo and a frontend commitment nobody has made. Generic DOM or pixel observation stays last — that is bucket 2. |
| **Derived task boards** | Synthesizing a board from tool calls would make boards useful for the first time and give the apprentice a *why* layer. Open question: whether it writes to the live `tasks` table (visible and useful, but the apprentice would then mutate state agents read) or stays in candidate storage. |
| **Multi-tenancy, consent, org promotion** | `ApprenticeService` RPCs, RLS tables, redaction policy, and `Deputize` wired to `ContributeSkillToOrg` / `CreateWorkflow`. Entirely conditional on v1. The cloud layer is a wrapper over loom's RPCs, not a new surface — `proto/loomcloud/v1/skill.proto` already holds the promotion path. |
| **loom-knowledge tier** | Exists only to serve observe mode. Its README lists Loom integration as not started, its ingest path is deliberately LLM-free, and its output type is entities rather than executable YAML. The right eventual home for org-scale recurrence via `ContextService`, not a host for the engine. |
| **Live step display** | Explicitly not worth building — only 2 of ALLOY's 12 participants noticed one. |

---

## Risks and open questions

1. **v1 generalizes from a single example**, which is bucket 1 of the prior art. Mitigated by
   mandatory human review, not by an algorithm. If reviewers end up rewriting every candidate, the
   feature is a novelty and repetition-based inference becomes mandatory rather than deferred.
2. **A captured trace is untrusted input, and it becomes a prompt.** Tool output, table comments,
   and query results flow into a candidate's `prompt.instructions` and `tools.required`, which a
   later session loads with tool access. The most severe risk in the feature; sanitization plus the
   adversarial tier is required in v1, not deferred.
3. **Cost.** Watch mode is explicitly invoked and runs once per session, so LLM cost is bounded and
   opt-in. Any always-on variant must be batch and offline.
4. **Redaction happens before persist, not before display.** Captured traces contain prompts, SQL
   with real object names, and possibly PII.
5. **Validation cannot live in the destination repo.** Org repos are arbitrary GitHub slugs with no
   guaranteed CI. `Teradata-PE/aai-tera-agentic-skills` does run `skill-validation.yml` plus single-
   and multi-turn skill evals — decide whether to call those pre-PR or port the checks into
   `pkg/skillvalidation`.
6. **Candidate lifecycle** — `PROPOSED | VALIDATED | DEPUTIZED | DISCARDED`. A discarded candidate
   must suppress re-proposal of the same procedure, which needs the fingerprint from constraint 9.
   Not a v1 problem, since v1 only runs on request.
7. **Open**: does deputizing go straight to an org PR, or land in the user's private skill set first
   with promotion as a separate act? The marketplace supports both; private-first is safer.
8. **Open**: should `TaskBoardConfig.Enabled` default to true? Cost is bounded — 500
   `context_budget_tokens` — and the expensive behaviors are separately gated (`auto_decompose`
   defaults false; hygiene `REQUIRE_FIX` is scoped to skill-emitted tasks only). But constraint 2
   says flipping the flag does not make boards get *maintained*. Separable from this feature, and it
   deserves its own PR.

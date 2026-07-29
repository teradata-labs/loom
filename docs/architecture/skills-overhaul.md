# Skills Overhaul

**Version:** v1.3.0
**Status:** ✅ Implemented (all 13 phases + deferred-work A–E; remaining gaps are `mcp_servers` activation, skill-activation task emission, and registry-side hot-reload wiring — see Limitations)
**History:** landed on `feat/skills-overhaul`, merged to `main`

This document describes the three-part overhaul of the Loom skills subsystem
introduced by the skills overhaul branch, and what each phase landed. It
supersedes the skill section in `agent-system-design.md` for the new code
paths; the legacy paths are preserved for backward compatibility and
described where relevant.

**Read this alongside the runtime.** Skill activation has since moved to a
model-pull model: the system prompt carries a menu of bound skills, and the
model loads one by calling the `manage_skills` builtin. The conversation loop
runs no discovery, matching or activation of its own. The components this
overhaul landed — binding resolver, PageIndex router, FTS5 search, task
emitter — are built and tested; which of them the turn loop drives today is
stated per section below, and the current runtime is documented in
[`skills-system.md`](./skills-system.md).

## Goals

The legacy skills system (v1.2.0) was a per-session prompt-injection layer:
skills were matched per turn (slash command, keyword, intent category), then
the matched skill's `instructions` were injected into the LLM context.

Three structural limits motivated this overhaul:

1. **No declarative attachment to agents.** v1.2.0 `SkillsConfig` only had
   whitelist (`enabled_skills`) and blacklist (`disabled_skills`) fields.
   Every discovered skill loaded eagerly into every agent's library; an
   agent could not say "I have access to skills X, Y, Z, lazy-load when
   triggered."
2. **Flat search did not scale.** The legacy library uses FTS5 keyword
   search. For an enterprise catalog of 10K+ skills the discovery layer
   needed something hierarchical and LLM-navigable, modeled on the
   PageIndex "reasoning-based retrieval over a document tree" approach.
3. **Skills did not produce trackable work.** A skill activation in v1.2.0
   was invisible to the user — it changed the prompt and disappeared. The
   recently-merged task system (`pkg/task/`, `task_board` tool, gRPC
   `TaskService`) already had the data model needed to surface this work.

## Status Legend

- ✅ Implemented and tested with `-race`
- ⚠️ Partial (interface or scaffolding shipped; some wiring deferred)
- 🚧 In development (commits in progress)
- 📋 Planned (not yet started)

## Architecture

### Phase Map (13 phases)

| Phase | Subject | Status |
|------:|---------|:------:|
| 1 | Proto additions (`SkillBinding`, `SkillTaskTemplate`, `SkillIndex*`, `SkillsConfig.*` knobs) | ✅ |
| 2 | Go type mirrors + YAML loader extensions | ✅ |
| 3 | `pkg/skills/binding/` resolver + legacy shim | ✅ |
| 4 | SQLite (000007) + Postgres (000012) skill_index migrations | ✅ |
| 5 | `pkg/skills/index/` (node, builder, store, router, cache) | ✅ |
| 6 | `pkg/skills/tasks/` emitter (template + decomposer fallback) | ✅ |
| 7 | `pkg/skills/discovery/` top-level orchestrator | ✅ |
| 8 | `task.skill_idempotency_key` + `Manager.CreateTaskIdempotent` | ✅ |
| 9 | `pkg/agent/agent.go` four-phase pipeline (superseded by the pull model) | ✅ |
| 10 | `pkg/skills/index.HotReloadHandler` | ✅ |
| 11 | `loom skills migrate` CLI | ✅ |
| 12 | Architecture docs (this file) | ✅ |
| 13 | End-to-end verification (`just check`, live gRPC drive) | ✅ |

### Discovery Pipeline

Phase 9 landed a per-turn pipeline in `pkg/agent/agent.go`
`runConversationLoop`, replacing the legacy single-pass `MatchSkills` filter
at the location formerly known as the "skill activation" block. The shape it
landed with:

```
runConversationLoop()
  | (existing) evaluateLazyTools(msg)
  |
  v
  Phase B - Discovery.Discover(ctx, sessionID, msg, config)
  |   ├─ slash command fast path (skills.ParseSlashCommand → library.FindBySlashCommand)
  |   ├─ router walk (when discovery.Discovery has a *index.Router AND
  |   |              SkillsConfig.EffectiveRouterEnabled() is true)
  |   ├─ FTS5 fallback (library.FindByKeywords; runs when slash+router empty)
  |   └─ ALWAYS bindings appended unconditionally
  |
  v
  Phase C - skillOrchestrator.ActivateSkill for each candidate
  |          (legacy lifecycle preserved: per-session activation, sticky tracking)
  |
  v
  Phase D - skillTaskEmitter.EmitForActivation for each NEWLY-activated skill
  |          ├─ if skill.TaskTemplate != nil      → materialize steps
  |          └─ else (LLM available + emit_tasks)  → task.Decomposer.Decompose
  |
  v
  Phase E - existing FormatActiveSkillsForLLM + prompt injection + pattern co-injection
```

The turn loop no longer runs any of it. `Discovery` is still constructed and
wired onto the agent (`WithSkillDiscovery`) for callers that drive
`Discovery.Discover` directly, and the registry still warms and persists the
router index. Phases C and E were replaced by the `manage_skills` load path —
`Orchestrator.ActivatePinned`, per-session required-tool registration, and
body delivery as a user-role message. Phase D was dropped with no
replacement: nothing calls the emitter. `MatchSkills` and
`FormatActiveSkillsForLLM` remain on the orchestrator for the same
direct-caller reason.

### Package Map

```
pkg/skills/
  binding/                   # ✅ Phase 3
    binding.go               # MatchBinding (exact / FQN / glob / label / version)
    resolver.go              # Resolver.Resolve + legacy shim from EnabledSkills/DisabledSkills
  index/                     # ✅ Phase 5 + Phase 10
    node.go                  # tree primitives, content hashing
    builder.go               # Builder.Build (LLM optional; deterministic fallback)
    store.go                 # MemoryStore, SQLStore (SQLite + Postgres dialects)
    router.go                # Router.Route (LLM-driven BFS tree walk)
    cache.go                 # FIFO/TTL CacheKey-keyed router decision cache
    hotreload.go             # HotReloadHandler (debounced rebuild → router → cache)
  tasks/                     # ✅ Phase 6 (📋 no caller — see Limitations)
    emitter.go               # EmitForActivation (template OR decomposer)
  discovery/                 # ✅ Phase 7 (wired onto the agent; not driven per turn)
    discovery.go             # Top-level Discover() composing slash → router → FTS5
  importer/                  # ✅ Deferred D (PR #182, #183)
    parse.go                 # SKILL.md + references → importer.Skill
    classify.go              # Graph-aware classifier (LLM + taxonomy validator)
    taxonomy.go              # Valid parent_index_path validator
    graph.go                 # GraphContext from live router index
    render.go                # importer.Skill → loom/v1 YAML
    pipeline.go              # RunFromDir / RunFromSkills / ProcessSkill
  hygiene/                   # ✅ Deferred E (PR #184)
    auditor.go               # Audit(session, cfg) → Report (skill-scoped)
    enforcer.go              # Enforce(report, retries, max) → Outcome
    report.go                # ViolationKind taxonomy + FormatToolMessage

cmd/loom/skills.go                  # ✅ Phase 11 - `loom skills migrate`
cmd/loom/skills_import.go           # ✅ Deferred D - `loom skills import / add / classify`
                                    #     (gRPC client of SkillsImportService)

proto/loom/v1/skill.proto    # ✅ Phase 1 - SkillBinding, SkillTaskTemplate, SkillIndexNode, SkillIndex
proto/loom/v1/agent_config.proto  # SkillsConfig refers to skill.proto messages

pkg/storage/{sqlite,postgres}/migrations/   # ✅ Phase 4 + Phase 8
  000007_skill_index.{up,down}.sql           # SQLite skill_indices + skill_index_nodes
  000008_task_idempotency.{up,down}.sql      # SQLite tasks.skill_idempotency_key
  000012_skill_index.{up,down}.sql           # Postgres parallel migration
  000013_task_idempotency.{up,down}.sql      # Postgres parallel migration
```

### Decisions

These were pinned by the user during planning:

1. **Attachment**: name + glob/namespace + label_match. The binding
   resolver supports exact-name, FQN (`enterprise/sql/sql-optimization`),
   `path.Match`-style globs, and label AND-selectors. Exact-name binds
   beat glob binds for the same skill (resolver.pickBest tie-break).
2. **Discovery**: router-first with FTS5 fallback. Within
   `Discovery.Discover`, the router fires on every cache-miss message when
   configured; FTS5 runs only when the router returns zero candidates or
   errors, and slash commands bypass both. No turn calls `Discover` — skill
   selection is the model's, through `manage_skills`.
3. **Tasks**: decomposer-by-default, in the emitter's own design — a skill
   gets tasks unless `emit_tasks` is explicitly false, and an authored
   `task_template` overrides the decomposer. No path calls the emitter, so
   loading a skill creates no tasks.
4. **Compatibility**: additive proto fields plus a runtime resolver shim.
   v1.2.0 YAML files keep working without modification. The
   `loom skills migrate` CLI is non-destructive.

### Deferred-Work Phases (post-Phase-13)

After the initial 13-phase landing, three follow-up phases closed the
remaining gaps from the design plan:

| Deferred | Subject | Status |
|---------:|---------|:------:|
| A | Router wiring in registry + `router_model_override` honored | ✅ |
| B | `SkillToolConfig` enforcement (`required_tools`, `excluded_tools`) | ✅ |
| C | Sticky-while-open-tasks eviction policy | ✅ |
| D | Anthropic-style skill import + `SkillsImportService` gRPC (PR #182, #183) | ✅ |
| E | End-of-turn task-board hygiene (PR #184) | ✅ |

**Deferred A** (`pkg/agent/registry.go` `warmSkillIndex`): the registry now
constructs a full `index.Builder` + `index.MemoryStore` + `index.Router` +
`index.Cache` for every agent with the router enabled, and kicks off a
background warm-up goroutine. Cold-start tries `store.LatestIndex` first
(instant `SetTree` on subsequent boots); then unconditionally rebuilds and
swaps. Router LLM resolves in priority order: `RouterModelOverride` from
the provider pool → `classifierLLM` → primary LLM. Build failures log a
Warn but never bubble — Discovery falls back to FTS5, identical to the
v1.2.0 baseline.

**Deferred B** (`pkg/agent/agent.go` `enforceRequiredSkillTools` /
`applySkillExcludedTools`): enforcement of a skill's tool preferences.
`RequiredTools` are auto-registered from the builtin catalog when not
already present (unknown names log Warn); `ExcludedTools` are unioned
across all active skills and filtered from the tool list offered to the
model. Under the pull model both are driven by the load: the
`manage_skills` tool calls `enforceRequiredSkillTools` immediately after
activating, and the required tool is advertised into **that session's**
ledger rather than agent-wide. `advertisedTools(session)` re-derives the
projection before every provider call, so a tool a load registered is
usable on the same turn. `mcp_servers` is logged at Debug when declared
(see Limitations below).

**Deferred C** (`pkg/skills/orchestrator.go` + `pkg/task` storage):
the orchestrator now respects a `StickinessChecker` callback during
eviction; the agent installs a checker that consults
`task.Manager.HasOpenSkillTasks` (LIKE-prefix query against
`skill_idempotency_key`). Skills with non-DONE+non-CANCELLED tasks
are treated as sticky regardless of `Skill.Sticky`. When every active
skill is sticky, `MaxConcurrentSkills` overflows for that turn rather
than abandoning in-flight work. `WithMaxConcurrentSkills` also fixes a
pre-existing bug where `ActivateSkill` ignored config and used a
hardcoded cap of 3. All of this belongs to `ActivateSkill`; the pull
path uses `ActivatePinned`, which applies no cap and never evicts, so
stickiness has nothing to protect there.

**Deferred D** (`pkg/skills/importer/` + `proto/loom/v1/skills_import.proto`
+ `cmd/looms/cmd_serve.go` registration): Anthropic-style Agent Skill
directories (`<name>/SKILL.md` + `references/*.md`) can be imported into
the Loom catalog through a four-stage pipeline (parse → optional
graph-aware classify → render → write). The pipeline is fronted by
`SkillsImportService` — a peer service of `LoomService` exposing
`BulkImportSkills` (server-streaming), `AddSkill`, and `ClassifySkill`.
Each successful write triggers an explicit router index rebuild so newly
imported skills become routable on the next chat turn, independent of
fsnotify (which is unreliable on some Docker bind mounts and NFS). The
`loom skills` CLI subcommands are thin gRPC clients of this service. Full
design lives in [`skills-import.md`](./skills-import.md).

**Deferred E** (`pkg/skills/hygiene/` + `pkg/agent/hygiene.go` +
`HygieneConfig` on `SkillsConfig`): end-of-turn task-board audit for
skill-emitted tasks. At the no-tool-call return path of
`runConversationLoop`, the auditor classifies any skill task in
`IN_PROGRESS`, `BLOCKED`, or `OPEN`-unstarted state as a violation;
the enforcer applies the resolved `HygienePolicy` (default
`REQUIRE_FIX`: inject a synthetic user message and re-run the LLM
turn, capped at `max_retries=2` before falling through to `AUTO_FIX`).
Scope is strictly skill-emitted tasks via `SkillIdempotencyKey` prefix;
tasks created ad-hoc through `TaskBoardTool` are out of scope. Full
design lives in [`skill-hygiene.md`](./skill-hygiene.md).

### Wiring Requirements

Two independent integration points must be satisfied at server startup for
the skills subsystem to reach its task-side behaviour. Phase 13 E2E
verification surfaced the general lesson that "the code is merged" is not the
same as "the path is reachable" — the same lesson now applies to the emitter
itself, which is constructed and has no caller.

```
                       cmd/looms/cmd_serve.go
                              │
                              │  (1) SetProviderPool(pool)
                              │  (2) SetTaskManager(mgr, decomposer)
                              ▼
                    ┌─────────────────────┐
                    │   agent.Registry    │
                    │                     │
                    │  providerPool       │
                    │  taskManager   ◀──── injected before LoadAgents
                    │  taskDecomposer     │
                    └──────────┬──────────┘
                               │  buildAgent(config)
                               │  always: WithTaskBoard(mgr, dec, tbCfg)
                               ▼
                    ┌─────────────────────┐
                    │       Agent         │
                    │                     │
                    │  taskManager   ◀──── never nil when (2) is wired
                    │  skillTaskEmitter◀── auto-built; no caller
                    │  taskBoardConfig ◀── always set; Enabled flag
                    │                     │   gates only tool/context
                    └─────────────────────┘
```

**Injection contract (1) — Provider pool.** Without `SetProviderPool`,
`AgentConfig.active_provider` and `SkillsConfig.router_model_override`
resolve to the registry default LLM. Mostly cosmetic for primary LLM
selection but matters for the router cost-control model (see Cost Model
below).

**Injection contract (2) — Task subsystem.** Without
`SetTaskManager`, `a.taskManager` stays nil after `buildAgent` returns,
and the constructors guarded by
`a.skillOrchestrator != nil && a.taskManager != nil` short-circuit: no
skill task emitter, no stickiness checker, no hygiene auditor or
enforcer. Skill loading is unaffected — a skill still activates and its
body still reaches the conversation. This missing injection was the
production gap in the initial overhaul landing.

**Two-axis separation: task machinery vs. tool surfacing.** Once contract
(2) is satisfied:

| Behavior | Gated by |
|----------|----------|
| Skill task emitter constructed (📋 no caller) | `taskManager != nil` |
| Sticky-while-open-tasks checker installed | `taskManager != nil` |
| End-of-turn hygiene auditor + enforcer | `taskManager != nil` |
| `task_board` builtin tool registered | `taskBoardConfig.Enabled` |
| Kanban prompt supplement injected | `taskBoardConfig.Enabled` |
| In-context task summary (`buildTaskContext`) | `taskBoardConfig.Enabled` |

The split keeps a server-level operator decision (wire the task
subsystem on or off) orthogonal to a per-agent author decision (does
this agent need to *interact* with the board?), so an agent can produce
work for downstream agents without carrying the kanban tool surface.
The overhaul's "activations always produce trackable work" invariant is
not met today: the emission step it depended on has no call path.

### Skill Governance Extensions

Three governance mechanisms layer on top of activation:

**Confidence decay.** Each skill carries `confidence` (float, 0.0-1.0), `status`
(auto_generated / enriched / validated / deprecated), and `last_validated_ms`
(unix millis). Effective confidence decays at `0.995^days` from `last_validated_ms`,
computed lazily at query time (same exponential decay math as graph memory salience).
`FindByKeywords` multiplies the FTS5 relevance score by effective confidence and
excludes skills whose decayed value drops below 0.1. Hand-authored skills with
`confidence == 0` default to 1.0 (no decay). `FindByKeywords` is the only consumer:
`Library.Load` and `Library.ListAll`, which back `manage_skills`, ignore confidence,
so a decayed skill still lists and still loads on request.

**Risk-level gate.** Skills may declare `risk_level: high` or `risk_level: restricted`
(proto enum `SKILL_RISK_LEVEL_HIGH`, `SKILL_RISK_LEVEL_RESTRICTED`). The gate lives in
the `manage_skills` load path: before activating, the tool checks
`Skill.IsHighRisk()` against the agent's permission checker. A high-risk skill is
blocked when a checker is wired and not in YOLO mode — the call returns
`Success=false` with code `approval_required` and `Metadata.activated=false`, and the
active set is untouched, so the model sees the refusal and can surface it. YOLO mode
(`tools.permissions.yolo=true`) bypasses the gate; **a nil permission checker disables
it entirely** and high-risk skills load unchallenged.

**Staleness audit.** The `skill-health-audit` workflow template
(`WORKFLOW_TEMPLATE_SKILL_HEALTH_AUDIT`, enum value 7) runs a 2-agent pipeline that
scans all skills for confidence below threshold, deprecated status, or missing
`last_validated_ms`, then produces a report. It is schedulable weekly via the
template service (`/template skill-health-audit`).

### Limitations and Known Gaps

- ⚠️ **`mcp_servers` activation not implemented.** `SkillToolConfig.MCPServers`
  is parsed and stored in the proto, and Deferred B logs a Debug message
  when a skill declares non-empty `MCPServers`, but the agent does not
  yet activate (start/connect) the named servers when a skill activates.
  Honoring this requires an MCPManager hook that does not exist yet;
  it is a follow-up to the skills overhaul.
- 📋 **Skill-activation task emission has no call path.**
  `pkg/skills/tasks.Emitter` is implemented, tested and constructed by the
  agent, but nothing calls `EmitForActivation`. Loading a skill creates no
  tasks. End-of-turn hygiene still runs; its inventory is keyed on
  `skill:<name>|sess:<id>|` task keys, so it finds nothing to classify
  until some path stamps that key.
- 📋 **`max_prompt_tokens` and `context_budget_percent` are not applied.**
  Both are parsed and carried on the skill and the config. The loaded body
  is delivered whole as a user-role message; the token-budgeted injection
  formatters (`FormatSkillsForInjection`,
  `Orchestrator.FormatActiveSkillsForLLM`) are only reachable through the
  match path.
- 📋 **`preferred_order` is informational.** The field communicates
  author intent inside the skill prompt; the LLM still chooses tool
  invocation order. There is no mechanism to enforce ordering at the
  agent layer, and that's intentional — order is a runtime decision.
- ✅ **Disk persistence of the skill index.** The registry wires
  `SQLStore` (SQLite dialect) onto the router when its DB handle is
  non-nil: `SkillsWiringDeps.IndexStoreDB` is set to `r.db`, and
  `skillindex.NewSQLStore` is constructed in
  `BuildSkillsOptions`. Cold start tries `store.LatestIndex` first for an
  instant `SetTree` before rebuilding (see Deferred A). When the DB
  handle is nil, persistence is skipped and the router runs from an
  in-memory tree (re-summarised each boot).
- 📋 **HotReloadHandler not auto-wired in registry.** `pkg/skills/index.HotReloadHandler`
  is implemented and tested (Phase 10), but the registry does not yet
  install it on the skill library's hot-reloader. This is a one-line
  wire-up once the user has decided whether hot-reload should be
  enabled by default for all agents.

### Migration Path

For an existing v1.2.0 agent config:

1. Run `loom skills migrate examples/your-agent.yaml > migrated.yaml`.
2. Inspect the migrated file: it contains both the synthesized `bindings:`
   block AND the original `enabled_skills` / `disabled_skills` fields.
3. Diff and replace the original (`mv migrated.yaml examples/your-agent.yaml`).

The runtime resolver applies the legacy shim automatically when
`bindings:` is empty, so step (3) is optional. Migrating explicitly is
recommended for documentation: the bindings list is the new source of
truth.

### Cost Model (Router LLM Calls)

Router-first means an LLM call per cache-miss message **when the router is
driven**. No cost accrues on the pull path, which resolves a skill by name.
The controls below are the ones baked into the discovery design:

- **FTS5 shortcut for high-confidence keyword hits.** When `MinAutoConfidence`
  fires on a strong FTS5 match, the discovery flow uses it without invoking
  the router. (Default minconf 0.7 means roughly the top 30% of clearly-keyworded
  messages skip the router.)
- **Per-session decision cache.** `index.Cache` keys on
  (sessionID, msgHash, bindingsHash). 5-minute TTL. Repeated messages on
  one session hit the cache after the first call.
- **Slash command bypass.** `/sql-optimize` skips the router entirely.
- **Optional `router_model_override`.** Field on `SkillsConfig`
  for pointing routing at a smaller/cheaper model (Haiku, local Ollama).
  ✅ Honored by the registry: `BuildSkillsOptions` resolves the named
  provider from the provider pool, falling back with a Warn when the name
  is absent. (See Deferred A.)

Estimated worst case for a router-driven caller at heavy use with
Sonnet-class routing: ~$0.30 per agent per day.

### References

- Plan file: `/Users/ilsun.park/.claude/plans/we-need-an-overhaul-curried-sphinx.md`
- Related: `docs/architecture/agent-system-design.md` (legacy skill flow)
- Migration tooling: `cmd/loom/skills.go` (CLI), `pkg/skills/binding/resolver.go` (runtime shim)
- Proto reference: `proto/loom/v1/skill.proto`, `proto/loom/v1/agent_config.proto:93`

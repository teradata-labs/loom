# Skills Overhaul

**Status:** ⚠️ Partial (Phases 1–11 of 13 implemented; router-in-registry wiring and end-to-end verification still pending)
**Branch:** `feat/skills-overhaul`
**Version target:** post-v1.2.0

This document describes the three-part overhaul of the Loom skills subsystem
introduced by the skills overhaul branch. It supersedes the skill section
in `agent-system-design.md` for the new code paths; the legacy paths are
preserved for backward compatibility and described where relevant.

## Goals

The legacy skills system (v1.2.0) is a per-session prompt-injection layer:
skills are matched per turn (slash command, keyword, intent category), then
the matched skill's `instructions` are injected into the LLM context.

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
| 9 | `pkg/agent/agent.go` four-phase pipeline | ✅ |
| 10 | `pkg/skills/index.HotReloadHandler` | ✅ |
| 11 | `loom skills migrate` CLI | ✅ |
| 12 | Architecture docs (this file) | ✅ |
| 13 | End-to-end verification (`just check`, manual TUI) | 📋 |

### Discovery Pipeline

The new per-turn pipeline lives in `pkg/agent/agent.go` `runConversationLoop`
and replaces the legacy single-pass `MatchSkills` filter at the location
formerly known as the "skill activation" block. The legacy path is preserved
when `WithSkillDiscovery` is not wired, so v1.2.0 agents continue to work.

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
  Phase E - existing FormatActiveSkillsForLLM + InjectSkills + pattern co-injection
```

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
  tasks/                     # ✅ Phase 6
    emitter.go               # EmitForActivation (template OR decomposer)
  discovery/                 # ✅ Phase 7
    discovery.go             # Top-level Discover() composing slash → router → FTS5

cmd/loom/skills.go           # ✅ Phase 11 - `loom skills migrate`

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
2. **Discovery**: router-first with FTS5 fallback. The router fires on
   every (cache-miss) message when configured; FTS5 runs only when the
   router returns zero candidates or errors. Slash commands bypass both.
3. **Tasks**: decomposer-by-default. Skills get tasks unless `emit_tasks`
   is explicitly false. Authored `task_template` overrides the decomposer.
4. **Compatibility**: additive proto fields plus a runtime resolver shim.
   v1.2.0 YAML files keep working without modification. The
   `loom skills migrate` CLI is non-destructive.

### Limitations and Known Gaps

- ⚠️ **Tool preferences not enforced.** `SkillToolConfig.required_tools`,
  `excluded_tools`, `mcp_servers`, and `preferred_order` are parsed and
  stored in the proto but not enforced by the agent yet. This was a
  pre-existing gap before the overhaul (see `pkg/skills/types.go`); the
  overhaul preserves the surface but does not fill it. Tracked separately.
- ⚠️ **Router not wired in the registry by default.** Phase 9 wires
  `Discovery` into the agent registry, but Phase 5's Router is not yet
  constructed there: it requires a Builder + Source + LLM lifecycle that
  has not been chosen for production. Discovery degrades to slash + FTS5
  in that case. Wiring is a one-line `WithRouter(...)` once the Builder
  schedule is decided.
- ⚠️ **End-to-end verification (Phase 13) not run.** Each phase has unit
  tests with `-race`; an end-to-end TUI test against a running
  loom-server has not been performed.
- 📋 **Index router-model override is parsed only.** `SkillsConfig.router_model_override`
  is in the proto and Go mirror but the registry does not yet consult it
  to pick a separate LLM provider for routing.
- 📋 **`MaxConcurrentSkills` eviction with open tasks.** The plan called
  for "skills with open tasks count as sticky for eviction." This is not
  yet implemented; skills can in principle still be evicted while they
  have open task rows. The idempotency key keeps re-emission safe but
  does not address the activation-lifecycle aspect.

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

Router-first means an LLM call per cache-miss message. Default cost
controls baked into the design:

- **FTS5 shortcut for high-confidence keyword hits.** When `MinAutoConfidence`
  fires on a strong FTS5 match, the discovery flow uses it without invoking
  the router. (Default minconf 0.7 means roughly the top 30% of clearly-keyworded
  messages skip the router.)
- **Per-session decision cache.** `index.Cache` keys on
  (sessionID, msgHash, bindingsHash). 5-minute TTL. Repeated messages on
  one session hit the cache after the first call.
- **Slash command bypass.** `/sql-optimize` skips the router entirely.
- **Optional `router_model_override`.** Field exists in `SkillsConfig`
  for pointing routing at a smaller/cheaper model (Haiku, local Ollama).
  📋 Not yet honored by the registry.

Estimated worst case at heavy use with Sonnet-class routing: ~$0.30 per
agent per day. Acceptable for the discovery quality gain at scale.

### References

- Plan file: `/Users/ilsun.park/.claude/plans/we-need-an-overhaul-curried-sphinx.md`
- Related: `docs/architecture/agent-system-design.md` (legacy skill flow)
- Migration tooling: `cmd/loom/skills.go` (CLI), `pkg/skills/binding/resolver.go` (runtime shim)
- Proto reference: `proto/loom/v1/skill.proto`, `proto/loom/v1/agent_config.proto:93`

# Skills System Architecture

## Overview

Skills are YAML-defined units of expertise that the **model pulls into a session on demand**. The system prompt carries a menu of the skills bound to the agent — name and description only. When the model decides it needs one, it calls the `manage_skills` builtin with the `load` action: the skill activates for that session, the tools it declares as required are registered and advertised to that session, and the verbatim skill body enters the conversation as a user-role message. Skill bodies are never written into the system prompt, and nothing activates a skill on the model's behalf.

**Version**: v1.3.0
**Status**: ✅ Shipped. The pull path (`manage_skills` list/load, sidecar body delivery, per-session required-tool registration, restore replay) is wired and live. Anthropic-style skill import (PR #182) and the `SkillsImportService` gRPC surface with post-write router reload (PR #183) are live; end-of-turn task-board hygiene (PR #184) gates each turn's return. Hierarchical discovery (PR #174) is built and wired onto the agent as a component, but the conversation loop does not drive it — see [Discovery Components](#discovery-components-off-the-pull-path). Remaining gaps are tracked in [`skills-overhaul.md`](./skills-overhaul.md#limitations-and-known-gaps) (notably `mcp_servers` activation, skill-activation task emission, and registry-side hot-reload wiring).

**Companion deep-dives**: This document is the cohesive overview. See [`skills-overhaul.md`](./skills-overhaul.md) for the hierarchical discovery design, [`skills-import.md`](./skills-import.md) for the import pipeline and `SkillsImportService` internals, and [`skill-hygiene.md`](./skill-hygiene.md) for the end-of-turn audit design.

---

## End-to-End Flow

```
 SESSION CREATED
      |
      v
 +--------------------------------------------------------+
 | System prompt (ROM)                                    |
 |   AVAILABLE SKILLS                                     |
 |     - <name> — <description>     (bound skills only)   |
 |   Rendered once at session creation; byte-stable for   |
 |   the whole session. No skill body here.               |
 +--------------------------------------------------------+
      |
 USER MESSAGE
      |
      v
 +--------------------------------------------------------+
 |               runConversationLoop (per turn)           |
 |   evaluateLazyTools(msg)                               |
 |   advertisedTools(session)  ← re-derived per call      |
 +--------------------------------------------------------+
      |
      v
 LLM CHAT CALL ────────── model picks a skill off the menu
      |
      v
 manage_skills { action: "load", name: X }
      |
      +--- high-risk gate ---> blocked: Success=false
      |                        code "approval_required"
      |                        Metadata activated:false
      |                        (active set untouched)
      v
 Orchestrator.ActivatePinned(sessionID, X)
      |   append to the session's active set
      |   no cap, no eviction, same name replaced in place
      v
 enforceRequiredSkillTools(sessionID)
      |   register the tool definition (agent-wide, once)
      |   advertise it into THIS session's ledger
      v
 +--------------------------+   +---------------------------+
 | tool_result              |   | sidecar (Role="user")     |
 | "Skill loaded: X"        |   | verbatim skill body       |
 | + Metadata marker        |   | + pattern-ref line if any |
 +--------------------------+   +---------------------------+
      |                                |
      | placed with its tool_use       | appended after EVERY
      |                                | tool_result of the batch
      +----------------+---------------+
                       |
                       v
      next provider call, same turn:
        skill body in the conversation,
        required tools in the advertised set
                       |
                       | (no tool calls → end of turn)
                       v
      End-of-turn hygiene
      +---------------------------+
      | Auditor.Audit             |
      | for each active skill:    |
      |   ListBySkillRun(name,ses)|
      |   classify violations     |
      +-------------+-------------+
                    |
          violations? clean?
                    |
      +-------------v-------------+
      | Enforcer.Enforce          |
      |   REQUIRE_FIX: inject     |
      |     fixup msg, retry      |
      |   AUTO_FIX: transition +  |
      |     spawn HITL            |
      |   WARN_ONLY: log only     |
      +-------------+-------------+
                    |
                    v
              Response → USER
```

There is no per-turn matching, scoring, or auto-activation step anywhere on this path. A skill is in the session because the model asked for it, or because a restore replayed an earlier ask.

---

## The `manage_skills` Builtin

`pkg/agent/manage_skills_tool.go`. Registered at agent construction whenever a skill orchestrator is wired and the builtin is not suppressed. It is a base advertised tool — present from the first turn of every session — so the model can always act on the menu in its system prompt.

Two actions, and only two:

| Action | Input | Result |
|--------|-------|--------|
| `list` | — | JSON: `{session_id, active_count, skills[]}`. Every skill in the library, each annotated with an `active` boolean for the calling session. |
| `load` | `name` | Activates the skill for this session, registers its required tools, returns `"Skill loaded: <name>"`. The body arrives as a separate message. |

**There is no `unload`.** A load appends to the session's active set and the required-tool wiring stays until the session ends (`Orchestrator.CleanupSession`). The active set has a single source — the orchestrator's per-session map — read by both the load path and the `list` annotation, so "which skills are loaded" is never re-derived from the conversation.

`list` returns the **whole library**; the system-prompt menu lists only the skills **bound to this agent** (see [Binding Resolution](#binding-resolution)). The two views differ by design: the menu is what the agent is meant to reach for, `list` is what exists.

### Load Sequence

```
  manage_skills{action: "load", name: X}
       |
       v
  library.Load(X)  --- miss ---> Success=false, code "not_found"
       |
       v
  +----------------------------------------------+
  | High-risk gate                               |
  |   skill.IsHighRisk()                         |
  |   AND permissionChecker != nil               |
  |   AND !permissionChecker.IsYOLOMode()        |
  |                                              |
  |   → Success=false, code "approval_required"  |
  |     Metadata{skill, risk, activated:false}   |
  |     nothing is activated                     |
  +----------------------------------------------+
       |  (gate passed, or no checker wired)
       v
  Orchestrator.ActivatePinned(session, skill,
       trigger "manual_load", confidence 1.0)
       |
       v
  enforceRequiredSkillTools(session)
       |
       v
  Result{
    Success: true,
    Data:    "Skill loaded: X",
    Metadata: {
      skill, source_path, risk, activated_at,
      text_body: <skill body + pattern refs>
    }
  }
```

A **nil permission checker disables the gate** — high-risk skills load unchallenged. YOLO mode (`tools.permissions.yolo=true`) also bypasses it, and the error message says so.

### Two-Message Delivery

The tool result is deliberately short. The skill's instructional body rides in `Metadata["text_body"]`, and the tool-execution loop turns it into a **separate `Role="user"` message** appended after every `tool_result` of the batch has been placed (`pendingSidecars` in `pkg/agent/agent.go`).

```
  assistant: [tool_use manage_skills X] [tool_use other_tool]
  tool:      [tool_result "Skill loaded: X"]
  tool:      [tool_result <other_tool output>]
  user:      ## Skill: X
             <verbatim instructions, constraints, output format, examples>
             Referenced patterns (load via load_pattern): a, b
```

Two properties fall out of draining the sidecars only after the batch:

- `tool_use ↔ tool_result` adjacency survives parallel tool calls, which the Anthropic, Bedrock and Ollama clients all require.
- The body carries user-instruction weight rather than tool-output weight.

The body is `Skill.FormatForLLM()` — title header, instructions, constraints, output format, examples. It omits `pattern_refs`, so the load appends them explicitly: this is the only place the model receives a reference string it can hand to the separate `load_pattern` tool. Patterns are never co-loaded with the skill; they load on demand.

`manage_skills` is exempt from large-tool-result offloading (alongside `get_tool_result` and `query_tool_result`) precisely because its payload is the short confirmation, not the body.

### Per-Session Tool Registration

`enforceRequiredSkillTools(sessionID)` runs immediately after the activation, walking every active skill of that session:

- A `required_tools` name not yet registered is looked up in the builtin catalog and registered agent-wide. An unknown name logs a Warn and is skipped — the turn continues.
- The name is then advertised into **this session's** ledger. Base (always-advertised) tools are left alone, so one session requiring a common tool never hides it from another.
- `excluded_tools` from all active skills are unioned and filtered out of the advertised set.

`advertisedTools(session)` is re-derived before every provider call, so a tool registered mid-turn by a load is visible to the model on the very next call **of the same turn**.

`mcp_servers` declared by a skill are logged at Debug and otherwise ignored — see [Limitations](./skills-overhaul.md#limitations-and-known-gaps).

### Restore Replay

A load result carries a durable `{skill, activated}` marker on the tool message. On session restore, the memory manager's replay walk calls `reFireSkillActivation` for each marker: `ActivatePinned` + `enforceRequiredSkillTools`, so a restored session's active set and advertised tools match a live one. A blocked load carries `activated:false` and is not re-fired. Replay re-activates and re-registers only — it adds no message to the conversation (the original body is already in the restored history) and emits nothing else.

---

## Discovery Components (off the pull path)

`pkg/skills/discovery`, `pkg/skills/index` and `pkg/skills/binding` implement binding resolution, a hierarchical LLM router and an FTS5 keyword fallback. Of these:

- **Binding resolution is live on the pull path** — it decides which skills appear in the system-prompt menu.
- **The router and FTS5 search are not called by the conversation loop.** `WithSkillDiscovery` is wired by the registry and `warmSkillIndex` still builds and persists the index, so the component is available to callers that drive `Discovery.Discover` directly. No turn invokes it, and no candidate it returns is auto-activated.

The rest of this section documents that component as built.

The discovery pipeline runs **4 sequential phases**, short-circuiting on match:

```
+================================================================+
|                     Discovery Pipeline                          |
+================================================================+

  User Message: "Optimize my Teradata sales query"
       |
       v
  +---------------------------+
  | Resolve Bindings (cached) |  <-- Agent's SkillsConfig declares
  | → eligible skill set      |      which skills this agent uses
  +---------------------------+
       |
       v
  Phase 1: SLASH COMMAND
  +---------------------------+
  | Message starts with "/"?  |  Confidence: 1.0
  | Match against registered  |  Cost: O(1) hash lookup
  | slash_commands             |
  +---------------------------+
       |
       | No match
       v
  Phase 2: HIERARCHICAL ROUTER
  +---------------------------+
  | LLM-guided tree walk      |  Confidence: 0.85
  | through PageIndex nodes   |  Cost: 1-3 LLM calls
  | (cached per session+msg)  |  (cached on repeat)
  +---------------------------+
       |
       | No match / router disabled
       v
  Phase 3: FTS5 KEYWORD FALLBACK
  +---------------------------+
  | Tokenize message          |  Confidence: score * decay
  | Match against skill       |  Cost: O(skills * keywords)
  | trigger.keywords          |  Min threshold: 0.7
  +---------------------------+
       |
       | Always (additive)
       v
  Phase 4: ALWAYS-MODE BINDINGS
  +---------------------------+
  | Skills bound as ALWAYS    |  Confidence: 1.0
  | Unconditionally active    |  Cost: O(bindings)
  +---------------------------+
       |
       v
  Sort by confidence (DESC)
  Cap by MaxConcurrentSkills (default: 3)
  Return []*Candidate
```

### Binding Resolution

Bindings declare which skills an agent is allowed to use. This is the one piece of the discovery stack the pull path depends on: `skillMenuPromptSupplement` runs the same resolver, and the resolved set is exactly what appears on the system-prompt menu. The activation `mode` on a binding (`ALWAYS` / `EAGER` / `LAZY`) is a search-path input — it does not change what the menu shows or auto-load anything.

```
  SkillsConfig (from agent YAML)
       |
       v
  +-------------------------------------------+
  | Binding Source Selection                   |
  | (precedence order)                        |
  |                                           |
  | 1. Explicit Bindings[] (new path)         |
  |    - name: "teradata-*"                   |
  |      mode: EAGER                          |
  |      priority: 10                         |
  |                                           |
  | 2. Legacy enabled_skills[] shim           |
  |    - converted to EAGER bindings          |
  |                                           |
  | 3. "All skills minus disabled_skills[]"   |
  |    - when no bindings or enabled_skills   |
  +-------------------------------------------+
       |
       v
  +-------------------------------------------+
  | For each binding, match against Library:  |
  |                                           |
  |   Exact:  "teradata-sql-analytics"        |
  |   Glob:   "teradata-*"                    |
  |   Label:  {category: database}            |
  +-------------------------------------------+
       |
       v
  +-------------------------------------------+
  | Tie-breaking (same skill, multiple binds):|
  |                                           |
  |   1. Exact > Glob > Label                 |
  |   2. Higher Priority wins                 |
  |   3. ALWAYS > EAGER > LAZY               |
  +-------------------------------------------+
       |
       v
  ResolvedBinding[] (one per eligible skill)
```

### Hierarchical Router (PageIndex)

The router walks a pre-built tree of skill categories using LLM guidance:

```
                    +-----------+
                    |   ROOT    |
                    +-----------+
                   /      |      \
                  v       v       v
         +--------+  +--------+  +--------+
         | data   |  | ops    |  | unclass|
         +--------+  +--------+  +--------+
         /      \         |           |
        v        v        v           v
  +------+  +------+  +------+  +----------+
  | sql  |  | ml   |  | audit|  | teradata |
  +------+  +------+  +------+  +----------+
     |         |          |           |
  skills:   skills:    skills:     skills:
  [sql-*]   [ml-*]    [dq-*]     [td-sql-*]


  Router Walk for "Optimize my Teradata query":

  Depth 0 (root):
    LLM sees children: [data, ops, unclassified]
    LLM decides: descend into [unclassified]

  Depth 1 (unclassified):
    LLM sees children: [teradata, general]
    LLM sees direct skills: []
    LLM decides: descend into [teradata]

  Depth 2 (teradata):
    LLM sees children: []
    LLM sees direct skills: [teradata-sql-analytics, teradata-ml]
    LLM decides: select [teradata-sql-analytics]

  Result: Candidate{
    Skill: teradata-sql-analytics,
    Confidence: 0.85,
    TriggerType: "router"
  }
```

**Caching**: Decisions are cached per `(sessionID, messageHash, bindingsHash)` with 5-minute TTL and a 256-entry cap. Eviction is FIFO (oldest-by-insertion), not strict LRU — routing decisions are short-lived enough that the distinction does not matter (`pkg/skills/index/cache.go`).

---

## Activation

The orchestrator owns the per-session active set and offers two entry points.

**`ActivatePinned`** — the pull path (`manage_skills` load, and restore replay). The skill is appended to the session's active set with confidence `1.0`. No cap, no eviction, no confidence check: an explicit load never displaces an already-loaded skill. A skill already active under the same name is replaced in place rather than duplicated. `MaxConcurrentSkills` does not bound this path.

**`ActivateSkill`** — the legacy match path, driven by `MatchSkills` / `Discovery` candidates. It applies the `MaxConcurrentSkills` cap and the eviction policy below. Nothing in the conversation loop calls it; it is reachable only by callers that drive matching themselves.

```
  Candidate arrives at Orchestrator (ActivateSkill only)
       |
       v
  +------------------------------------+
  | Already active for this session?   |
  |                                    |
  |   YES → refresh confidence, skip  |
  |   NO  → proceed to activation     |
  +------------------------------------+
       |
       v
  +------------------------------------+
  | At capacity? (active >= max)       |
  |                                    |
  |   NO  → activate immediately      |
  |   YES → eviction logic            |
  +------------------------------------+
       |
       v (eviction needed)
  +------------------------------------+
  | Find lowest-confidence evictable:  |
  |                                    |
  | For each active skill (ascending): |
  |   - Skip if skill.Sticky == true   |
  |   - Skip if stickinessChecker()    |
  |     returns true (has open tasks)  |
  |   - First non-sticky = evict       |
  |                                    |
  | All sticky? Allow overflow this    |
  | turn (don't evict in-flight work)  |
  +------------------------------------+
       |
       v
  +------------------------------------+
  | Activate:                          |
  | - Record ActivatedAt              |
  | - Track in session map             |
  | - Fire onSkillEviction callback    |
  |   (if something was evicted)       |
  +------------------------------------+
```

**Stickiness** prevents evicting skills that have in-progress work:
- `skill.Sticky = true` — author-declared (always sticky)
- `stickinessChecker(name, sessionID)` — agent-provided callback that checks if the skill has open tasks on the kanban board

Both are inputs to eviction, so both are inert on the pull path, which never evicts.

---

## Task Emission — 📋 not wired

`pkg/skills/tasks/emitter.go` is implemented and tested, and the agent constructs it whenever a task manager is present. **No call path invokes `EmitForActivation`**: loading a skill creates no tasks on the kanban board. The design below describes the emitter as built, not behaviour you will observe today.

When a skill is activated, the emitter would materialize tasks:

```
  Newly activated skill
       |
       v
  +------------------------------------+
  | Guard checks:                      |
  | - AgentTasksEnabled? (config)      |
  | - Skill.EffectiveEmitTasks()?      |
  |                                    |
  |   Both true → proceed              |
  |   Either false → no-op            |
  +------------------------------------+
       |
       v
  +------------------------------------+
  | Skill has TaskTemplate?            |
  |                                    |
  |   YES → emitTemplate()            |
  |   NO  → emitDecomposed()          |
  +------------------------------------+

  emitTemplate():                       emitDecomposed():
  +------------------+                  +------------------+
  | For each Step:   |                  | LLM call:        |
  | - title          |                  | Decompose skill  |
  | - objective      |                  | prompt into      |
  | - category       |                  | 3-8 steps        |
  | - priority       |                  +------------------+
  +------------------+                         |
       |                                       v
       +-------------------+-------------------+
                           |
                           v
  +--------------------------------------------+
  | For each step → task.Task:                 |
  |                                            |
  | - Idempotency: skill:<name>|sess:<id>|     |
  |                step:<index>                |
  | - Wire DependsOn edges (sequential)        |
  | - Cap at min(template.MaxTasks, 8)         |
  | - Stamp metadata: skill_name, session_id   |
  +--------------------------------------------+
       |
       v
  Tasks visible on kanban board
  (open tasks make the skill "sticky")
```

---

## Where Skills Live in the Context Window

Two surfaces, and no third:

```
  Context Window
  +============================================+
  | ROM (system prompt)                        |
  |   ...agent prompt, task-board supplement   |
  |   AVAILABLE SKILLS                         |
  |     - teradata-sql-analytics — Expertise…  |
  |     - dq-profiling — Column profiling…     |
  |   (name + description only, bound skills,  |
  |    rendered once, byte-stable per session) |
  +--------------------------------------------+
  | Kernel Layer (tools, schemas, findings)    |
  +--------------------------------------------+
  | L1 Messages (recent conversation)          |
  |   ...                                      |
  |   assistant: tool_use manage_skills(load)  |
  |   tool:      "Skill loaded: X"             |
  |   user:      ## Skill: X  ← BODY HERE      |
  |              <instructions, constraints,   |
  |               output format, examples>     |
  |   ...                                      |
  +--------------------------------------------+
  | L2 Summary (compressed history)            |
  +============================================+
```

The menu is ROM: it is rendered once at session creation by `skillMenuPromptSupplement` and never changes for the life of the session, which keeps the system prompt byte-stable for prompt caching. It is empty when no library is wired, skills are disabled, or no binding resolves.

The body is a conversation message. It is subject to the same L1/L2 lifecycle as any other message — nothing pins or re-injects it. `max_prompt_tokens` and `context_budget_percent` are parsed and carried on the config but are not applied on this path; the body is delivered whole.

---

## End-of-Turn Hygiene

At the no-tool-call return path of `runConversationLoop`, before the agent hands control back to the user, the hygiene auditor inspects the tasks of every skill loaded in the session for incoherent end-of-turn state. It runs only at that return point; mid-turn tool execution is not audited. Its inventory is keyed on `skill:<name>|sess:<id>|` task keys, so while activation-time emission is unwired the audit finds nothing to classify unless some other path stamps that key.

```
  LLM returns text-only response
       |
       v
  +------------------------------------+
  | Auditor.Audit(sessionID, cfg)      |
  |                                    |
  | For each active skill:             |
  |   ListBySkillRun(name, sessionID)  |
  |     → tasks via SkillIdempotency-  |
  |       Key prefix scan              |
  |   For each task:                   |
  |     classify(task) → ViolationKind |
  +------------------------------------+
       |
       v
  +------------------------------------+
  | ViolationKind taxonomy             |
  |                                    |
  | IN_PROGRESS_ORPHAN  agent claimed   |
  |                     but never closed|
  | BLOCKED_NO_HITL     never surfaced  |
  |                     as a question   |
  | OPEN_UNSTARTED      created but     |
  |                     never claimed   |
  |                                    |
  | DEFERRED/DONE/CANCELLED = healthy   |
  +------------------------------------+
       |
       v
  +------------------------------------+
  | Enforcer.Enforce(report, retries,  |
  |                  maxRetries)       |
  |                                    |
  | Policy resolution:                 |
  |   REQUIRE_FIX (default):            |
  |     inject synthetic user msg with  |
  |     violations → continue loop      |
  |   AUTO_FIX:                         |
  |     OPEN-unstarted -> DEFERRED      |
  |     IN_PROGRESS    -> OPEN          |
  |     BLOCKED        -> HITL request  |
  |     stamp [hygiene] note on each    |
  |   WARN_ONLY:                        |
  |     log + emit event only           |
  |                                    |
  | retries >= maxRetries (REQUIRE_FIX) |
  |   → fall through to AUTO_FIX so the |
  |     loop terminates                 |
  +------------------------------------+
       |
       v
  Response.Metadata stamped with:
    hygiene_policy
    hygiene_violations_found
    hygiene_by_kind
    hygiene_resolved
    hygiene_hitl_spawned
    hygiene_fallthrough (if applicable)
```

**Scope**: only tasks emitted by currently-active skills are audited (matched via `SkillIdempotencyKey` prefix `skill:<name>|sess:<sessionID>|`). Tasks the agent created directly via `TaskBoardTool` with no idempotency key are out of scope — that is general agent task discipline, governed by the agent's prompt and the user's expectations, not by the hygiene mechanism.

**Non-fatal**: audit or enforcement errors are logged and the agent returns its existing response. A broken hygiene path must not become an availability incident.

**Configuration**: `SkillsConfig.Hygiene` (proto `HygieneConfig`):
- `enabled` — `optional bool`; default `true` (unset = enabled).
- `policy` — `HygienePolicy`; default `REQUIRE_FIX`.
- `max_retries` — `int32`; default `2`. `REQUIRE_FIX` retries are capped here.

Full design rationale, classification rules, and trade-off analysis live in [`skill-hygiene.md`](./skill-hygiene.md).

---

## Skill Import Pipeline

Skills can be authored as Loom YAML directly, or imported from Anthropic-style Agent Skill directories (`<name>/SKILL.md` + `references/*.md`). The import pipeline lives in `pkg/skills/importer` and is fronted by the `SkillsImportService` gRPC service (`proto/loom/v1/skills_import.proto`).

```
  Source (one of):
    - directory path on server filesystem
    - zip archive uploaded via gRPC
    - inline record (frontmatter + body + refs map)
       |
       v
  +----------------------------------------+
  | parse.go: ReadSkill                    |
  |   YAML frontmatter + body separation   |
  |   reference file resolution            |
  | → *Skill (importer-internal type)      |
  +----------------------------------------+
       |
       | (optional, when classify=true)
       v
  +----------------------------------------+
  | classify.go: ClassifyAgainstGraph      |
  |   build GraphContext from live router  |
  |   LLM picks parent_index_path from     |
  |     valid taxonomy + existing buckets  |
  |   taxonomy.go validates the response   |
  | → Skill.ParentIndexPath stamped         |
  +----------------------------------------+
       |
       v
  +----------------------------------------+
  | render.go: RenderYAML                  |
  |   importer.Skill → loom/v1 Skill YAML  |
  |   per-source format normalization      |
  |   keyword extraction                   |
  | → []byte YAML                          |
  +----------------------------------------+
       |
       v
  +----------------------------------------+
  | pipeline.go: write or report           |
  |   Outcome ∈ {wrote, would-write,       |
  |              skip, fail}               |
  |   skip when dest exists + !overwrite   |
  +----------------------------------------+
       |
       v
  +----------------------------------------+
  | Post-write router reload (PR #183)     |
  |   trigger IndexBuilder.Rebuild         |
  |   running server picks up new tree     |
  |   without restart                      |
  +----------------------------------------+
```

**RPC surface** (`SkillsImportService`):

| RPC | Purpose | Streaming? |
|-----|---------|------------|
| `BulkImportSkills` | Tree of Anthropic-style skills → loom/v1 YAMLs | Server-streaming progress per skill |
| `AddSkill` | Single skill (dir, zip, or inline) → catalog | Unary |
| `ClassifySkill` | Re-classify an existing skill in `skills_dir` | Unary |

**Why a separate service** rather than methods on `LoomService`: skills import is a bounded subsystem with its own lifecycle (taxonomy management, classifier provider setup, source-format parsing) that does not share state with multi-agent coordination. The proto comment on `service SkillsImportService` documents the boundary.

**Classification is opt-in**. Without `classify=true`, the importer writes YAML using whatever `parent_index_path` the source declared (or empty → router places it under `unclassified/<domain>`). With it, the importer asks the LLM to pick from the valid taxonomy *as it exists in the live router*, so newly-imported skills tend to join existing buckets rather than invent parallel siblings.

Full design (taxonomy validator, graph-aware classifier, source-format adapters) is in [`skills-import.md`](./skills-import.md).

---

## Hot Reload

`pkg/skills/hotreload.go` watches the configured `skills_dir` and rebuilds the in-memory `Library` cache when YAMLs change on disk. Debounced (default 500ms, `HotReloadConfig.DebounceMs`) so rapid editor saves don't thrash the index.

```
  fsnotify event (Create | Write | Rename)
       |
       v
  +-----------------------------+
  | debounce window (500ms)     |
  | coalesce burst of events    |
  +-----------------------------+
       |
       v
  +-----------------------------+
  | validateSkill(path)         |
  | Library.RemoveFromCache(n)  |
  |   (next Load reads disk)    |
  | OnUpdate callback fires     |
  +-----------------------------+
       |
       v
  index.HotReloadHandler invoked
  IndexBuilder.Rebuild → Router cache cleared
```

**Scope**: hot reload applies only to skill YAML files. Skill *bindings* (declared on agent configs) require an agent reload to pick up. The system-prompt skill menu is rendered at session creation, so a hot-reloaded catalog shows up in new sessions; existing ones keep the menu they started with. The `SkillsImportService` complements hot reload by triggering an explicit router rebuild after a write completes — it does not depend on fsnotify, and so works even when the server's `skills_dir` is on a volume that doesn't emit reliable filesystem events (some Docker bind mounts, NFS).

---

## Skill Lifecycle & Confidence

### Confidence Decay

Skills carry a time-decaying confidence that reflects staleness. It is consumed in exactly one place: `Library.FindByKeywords`, the FTS5 search used by `MatchSkills` and by the discovery pipeline's keyword fallback. `Library.Load` and `Library.ListAll` — the calls behind `manage_skills` — ignore it, so a decayed skill still appears on the menu and still loads on request.

```
  confidence(t) = base_confidence * 0.995^days_since_validation


  Example: base = 0.9, validated 2025-01-01

  Day 0:    0.9 * 0.995^0   = 0.900  (fresh)
  Day 30:   0.9 * 0.995^30  = 0.766  (still confident)
  Day 100:  0.9 * 0.995^100 = 0.543  (moderate decay)
  Day 365:  0.9 * 0.995^365 = 0.140  (nearly stale)
  Day 500:  0.9 * 0.995^500 = 0.073  (below threshold)


  Thresholds (FTS5 search only):
  +-----------+---------------------------------------+
  | < 0.1     | Skill dropped from search results     |
  | < 0.7     | Below min_auto_confidence             |
  | >= 0.7    | Strong enough to short-circuit router |
  | = 1.0     | Fresh, or hand-authored (no decay)    |
  +-----------+---------------------------------------+
```

### Skill Lifecycle States

```
  +----------+  manage_skills   +----------+
  | ON MENU  |  ------------->  |  LOADED  |
  | (bound;  |  action: load    | (body in |
  |  name +  |  or restore      |  the     |
  |  descr.  |  replay          |  convo)  |
  |  in ROM) |                  +----------+
  +----------+                       |
                                     | CleanupSession
                                     | (session ends /
                                     |  agent shutdown)
                                     v
                                +---------+
                                | CLEARED |
                                +---------+

  A load is one-way for the life of the session:
  1. No unload action
  2. No eviction — ActivatePinned ignores the concurrency cap
  3. Re-loading the same name replaces the entry in place

  There is no "success" state. What a loaded skill leaves behind
  is its body in the conversation and its required tools in the
  session's advertised set.
```

---

## Skill YAML Structure

```yaml
apiVersion: loom/v1
kind: Skill

metadata:
  name: teradata-sql-analytics        # Unique ID (bindings + manage_skills load)
  title: Teradata Native SQL Analytics # Header of the loaded body
  description: |                       # THE menu line the model chooses from
    Expertise in Teradata-specific SQL functions...
  version: "1.0.0"
  domain: teradata                     # Groups into index tree
  labels:                              # Arbitrary k/v for label matching
    category: database
    backend: teradata
  confidence: 0.9                      # Base confidence (0.0-1.0), FTS5 search only
  last_validated_ms: 1735689600000     # Decay anchor (epoch ms)
  status: active                       # active | deprecated | experimental
  risk_level: ""                       # "" | LOW | MEDIUM | HIGH | RESTRICTED
                                       #   HIGH/RESTRICTED gate the load on approval

trigger:                               # Search-path metadata; no effect on
  slash_commands: ["/td-analytics"]    #   manage_skills, which loads by name
  keywords: ["teradata", "vantage"]
  mode: HYBRID                         # MANUAL | AUTO | HYBRID | ALWAYS

prompt:                                # Body returned by manage_skills(load),
  instructions: |                      #   delivered as a user-role message
    Use Teradata native functions...
  constraints:                         # Formatted as bullet list
    - Prefer native functions
    - Push predicates down
  examples:                            # Few-shot examples
    - user_input: "Find outliers"
      expected_output: "Use TD_OutlierFilterFit..."

tools:
  required_tools: []                   # Registered + advertised to the loading session
  preferred_order: [execute_sql]       # Informational; the LLM picks the order
  excluded_tools: []                   # Filtered out while the skill is loaded

pattern_refs: [teradata-data-prep]     # Surfaced in the load result; the model
                                       #   fetches them via load_pattern
sticky: true                           # Eviction input; inert on the load path
max_prompt_tokens: 3000                # Parsed, not applied
parent_index_path: "data/sql"          # Position in router tree

# Task emission (📋 not wired — see Task Emission)
emit_tasks: true
task_template:
  steps:
    - title: "Schema Discovery"
      objective: "Explore available tables and columns"
      category: research
      priority: P0
    - title: "Query Generation"
      objective: "Write optimized SQL using native functions"
      category: implementation
      priority: P1
  max_tasks: 8
  ephemeral_on_deactivate: false       # Keep tasks after skill eviction
```

---

## Component Map

```
pkg/skills/
  |
  +-- types.go              Skill, ActiveSkill, SkillBinding, SkillsConfig
  +-- loader.go             YAML parser (file → Skill struct)
  +-- library.go            In-memory skill cache; Load / ListAll / FTS5 search
  +-- orchestrator.go       ActivatePinned (pull path), ActivateSkill (match path),
  |                         per-session active set, CleanupSession
  +-- hotreload.go          fsnotify watcher → cache invalidation (debounced)
  +-- format.go             Injection formatters (match path only)
  |
  +-- discovery/
  |     +-- discovery.go    4-phase pipeline (slash → router → FTS5 → always);
  |                         wired onto the agent, not driven by the turn loop
  |
  +-- binding/
  |     +-- binding.go      MatchBinding (exact / FQN / glob / label / version)
  |     +-- resolver.go     Resolver.Resolve + legacy enabled/disabled shim
  |
  +-- index/
  |     +-- builder.go      Build skill tree from Library
  |     +-- router.go       LLM-guided tree walk
  |     +-- cache.go        Per-session decision cache (FIFO, 256-entry cap, 5min TTL)
  |     +-- store.go        Persistence interface (memory, SQL)
  |     +-- node.go         SkillIndexNode utilities
  |     +-- hotreload.go    HotReloadHandler (debounced rebuild → router → cache)
  |
  +-- tasks/
  |     +-- emitter.go      Task materialization (template or LLM decomp);
  |                         📋 no caller — see Task Emission
  |
  +-- importer/                                   [PR #182, #183]
  |     +-- parse.go        SKILL.md + references → importer.Skill
  |     +-- classify.go     Graph-aware classifier (LLM + taxonomy)
  |     +-- taxonomy.go     Valid parent_index_path validator
  |     +-- graph.go        GraphContext from live router index
  |     +-- render.go       importer.Skill → loom/v1 YAML
  |     +-- pipeline.go     Orchestration (RunFromDir, ProcessSkill)
  |     +-- keywords.go     FTS5 keyword extraction
  |     +-- types.go        Skill, SkillResult, Outcome
  |
  +-- hygiene/                                    [PR #184]
        +-- auditor.go      Audit(ctx, sessionID, cfg) → (*Report, error)
        +-- enforcer.go     Enforce(ctx, report, retryCount, maxRetries) → (*EnforcementOutcome, error)
        +-- report.go       Violation kinds + FormatToolMessage
        +-- doc.go          Package overview


pkg/agent/
  |
  +-- manage_skills_tool.go       list / load, high-risk gate, body sidecar
  +-- load_pattern_tool.go        on-demand pattern fetch by reference
  |
  +-- agent.go
  |     +-- skillMenuPromptSupplement   ROM menu of bound skills
  |     +-- runConversationLoop         drains text_body sidecars after the batch
  |     +-- enforceRequiredSkillTools   register + advertise required tools
  |     +-- applySkillExcludedTools     filter the advertised set
  |     +-- advertisedTools             per-session tool projection, per call
  |     +-- reFireSkillActivation       restore replay of load markers
  |
  +-- memory.go                   durable load markers on tool messages
  +-- hygiene.go                  runEndOfTurnHygiene helper
  |
  +-- skillOrchestrator field  → skills.Orchestrator (active set)
  +-- skillDiscovery field     → discovery.Discovery (wired, unused by the loop)
  +-- skillTaskEmitter field   → tasks.Emitter (constructed, no call path)
  +-- hygieneAuditor field     → hygiene.Auditor
  +-- hygieneEnforcer field    → hygiene.Enforcer


cmd/looms/cmd_serve.go
  |
  +-- SkillsImportService registration              [PR #183]
        gRPC server for BulkImportSkills / AddSkill / ClassifySkill;
        wires the importer's classifier provider and triggers router
        rebuild after every successful write.
```

---

## Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| Model pulls skills; nothing pushes them | The model knows what it is about to do better than a keyword match does. A menu plus a load action costs one tool call and removes a whole class of wrong-skill activations. |
| Menu in ROM, body in the conversation | The menu is small and identical every turn, so the system prompt stays byte-stable and cacheable. Bodies are large and situational, so they belong in the message stream where the L1/L2 lifecycle already governs them. |
| Short `tool_result`, body as a user-role message | A multi-thousand-token instruction set carries more weight as a user instruction than as tool output, and keeping the result short keeps it out of the large-result offload path. |
| Sidecars drained after the whole tool batch | Appending the body immediately would split a `tool_use`/`tool_result` pair when several tools run in parallel — which the Anthropic, Bedrock and Ollama clients reject. |
| No `unload` action | A skill already spoke into the conversation; removing its registration would not remove its influence, and the tool wiring it justified may still be in use. Session end is the honest boundary. |
| Loads are uncapped and never evict | The cap exists to bound *search* candidates. An explicit load is a stated intent — silently dropping one skill to make room for another would be a surprise the model cannot see. |
| Patterns by reference, not co-loaded | A skill naming five patterns should not drag five bodies into context. The load surfaces the names; `load_pattern` fetches only what gets used. |
| High-risk gate at load time, not menu time | The model can see and reason about a restricted skill; only the act of loading it needs approval. A blocked load returns an explicit `approval_required` error the model can surface to the user. |
| Restore replays activation only | The body and its tool results are already in the restored history. Re-firing activation restores the *machine* state (active set, advertised tools) without duplicating conversation. |
| Binding resolution separate from discovery | Agent config declares *eligibility* — which skills reach the menu. Search, where it runs, determines *relevance*. Decoupled concerns. |
| 4-phase pipeline with short-circuit (discovery component) | Slash commands are free; router is expensive. Don't pay for LLM calls when a `/` prefix gives certainty. |
| Router caching per (session, message, bindings) | Same question in same session always routes to same skills. Avoids redundant LLM calls on retries. |
| Confidence decay (0.995/day) | Skills rot. Stale knowledge is dangerous. Forces periodic re-validation of the search corpus. |
| Hygiene default: `REQUIRE_FIX` over `AUTO_FIX` | Machine state changes destroy diagnostic signal. Forcing the agent to fix its own dirty state preserves audit trail and the agent's learning loop; `AUTO_FIX` is the safety net, not the default. |
| Hygiene scope: skill-emitted tasks only | Tasks the agent created ad-hoc via `TaskBoardTool` are general agent discipline, not a skill-lifecycle failure mode. Auditing all tasks creates high false-positive rates on long-lived intentional state. |
| Hygiene: bounded retry with fallthrough | `REQUIRE_FIX` is capped at `max_retries=2` and falls through to `AUTO_FIX` so the loop always terminates even if the LLM is stuck. |
| `SkillsImportService` as a peer of `LoomService` | Skills import is a bounded subsystem (taxonomy, classifier, source-format parsing) with no shared state with multi-agent coordination. Separation keeps `LoomService` focused on conversation. |
| Classification opt-in, not default | Source skills often already declare a sensible `parent_index_path`; forcing every import through an LLM classifier costs tokens and risks regressing hand-tuned placements. Opt-in keeps the cheap path cheap. |
| Post-write router reload triggered by importer | fsnotify hot-reload is unreliable on some Docker bind mounts and NFS. An explicit reload after the import RPC guarantees the new tree is routable on the next chat turn, independent of filesystem watching. |

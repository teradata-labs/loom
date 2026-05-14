# Skills System Architecture

## Overview

The skills system provides **domain-specific prompt injection and task decomposition** for Loom agents. Skills are YAML-defined units of expertise that get activated based on user intent, injected into the LLM context, and optionally decomposed into tracked tasks on the kanban board.

**Status**: v1.2.0 — Hierarchical discovery (skills overhaul) merged.

---

## End-to-End Flow

```
 USER MESSAGE
      |
      v
+-------------------------------------------------------------+
|                 runConversationLoop (per turn)               |
+-------------------------------------------------------------+
      |
      |  Phase A         Phase B           Phase C
      |  (Tools)         (Discovery)       (Activation)
      v                  |                 |
 +---------+    +--------v--------+   +----v-----------+
 | Lazy    |    | Skill Discovery |   | Orchestrator   |
 | Tool    |    | Pipeline        |   | ActivateSkill  |
 | Promote |    | (4 phases)      |   | (evict if cap) |
 +---------+    +-----------------+   +----------------+
                         |                     |
                         v                     v
                   []*Candidate          []*ActiveSkill
                                               |
      +----------------------------------------+
      |
      |  Phase D                    Phase E
      |  (Task Emit)               (Injection)
      v                            |
 +--------------------+   +--------v------------------+
 | Emitter            |   | FormatActiveSkillsForLLM  |
 | (template or LLM)  |   | InjectSkills → segMem    |
 | → kanban tasks     |   | InjectPattern (refs)     |
 +--------------------+   | enforceRequiredTools     |
                          +---------------------------+
                                    |
                                    v
                             LLM CHAT CALL
                          (skills in context)
```

---

## Phase B: Skill Discovery Pipeline

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

Bindings declare which skills an agent is allowed to use and how they activate:

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

**Caching**: Decisions are cached per `(sessionID, messageHash, bindingsHash)` with 5-minute TTL and 256-entry LRU.

---

## Phase C: Activation & Eviction

```
  Candidate arrives at Orchestrator
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

---

## Phase D: Task Emission

When a skill is **newly activated this turn**, the emitter materializes tasks:

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

## Phase E: Prompt Injection

Active skills are formatted and injected into the LLM context window:

```
  SegmentedMemory Context Window
  +============================================+
  | ROM Layer (system prompt, static docs)     |
  +--------------------------------------------+
  | Kernel Layer (tools, schemas, findings)    |
  +--------------------------------------------+
  | Skill Injection ← HERE                    |
  | +----------------------------------------+ |
  | | ## Active Skill: Teradata SQL Analytics| |
  | |                                        | |
  | | [instructions from skill.prompt]       | |
  | | ### Constraints                        | |
  | | - Prefer native functions...           | |
  | | ### Examples                           | |
  | | - user: "Find outliers"               | |
  | |   assistant: "Use TD_OutlierFilter..."| |
  | +----------------------------------------+ |
  +--------------------------------------------+
  | Pattern Layer (co-injected pattern_refs)   |
  +--------------------------------------------+
  | L1 Messages (recent conversation)         |
  +--------------------------------------------+
  | L2 Summary (compressed history)           |
  +============================================+

  Token Budget:
  - Per skill: max_prompt_tokens (default 1500)
  - Total: maxContextTokens * contextBudgetPercent / 100
           (default 5% of context window)
  - Skills ordered by activation time (FIFO)
  - Overflow: later skills truncated or dropped
```

---

## Skill Lifecycle & Confidence

### Confidence Decay

Skills have a time-decaying confidence that reflects staleness:

```
  confidence(t) = base_confidence * 0.995^days_since_validation


  Example: base = 0.9, validated 2025-01-01

  Day 0:    0.9 * 0.995^0   = 0.900  (fresh)
  Day 30:   0.9 * 0.995^30  = 0.766  (still confident)
  Day 100:  0.9 * 0.995^100 = 0.543  (moderate decay)
  Day 365:  0.9 * 0.995^365 = 0.140  (nearly stale)
  Day 500:  0.9 * 0.995^500 = 0.073  (below threshold)


  Thresholds:
  +-----------+--------------------------------------+
  | < 0.1     | Skill skipped entirely (too stale)  |
  | < 0.7     | Below min_auto_confidence (no auto) |
  | >= 0.7    | Eligible for auto-activation        |
  | = 1.0     | Fresh, slash-command, or ALWAYS      |
  +-----------+--------------------------------------+
```

### Skill Lifecycle States

```
  +----------+     activate      +---------+
  | ELIGIBLE |  ---------------> | ACTIVE  |
  | (loaded) |                   | (in LLM |
  +----------+                   | context)|
       ^                         +---------+
       |                           |     |
       |    evict (capacity)       |     |  session ends
       +---------------------------+     |
       |                                 v
       |                         +---------+
       +-------------------------| CLEANUP |
                                 +---------+

  No explicit "success" state. Skills remain active until:
  1. Evicted by a higher-confidence newcomer
  2. Session ends (CleanupSession)
  3. Agent shutdown

  "Success" is inferred from:
  - Tool executions completed without errors
  - Tasks on kanban marked complete
  - Pattern effectiveness metrics (if tracking enabled)
```

---

## Skill YAML Structure

```yaml
apiVersion: loom/v1
kind: Skill

metadata:
  name: teradata-sql-analytics        # Unique ID (used in bindings)
  title: Teradata Native SQL Analytics # Human-readable
  description: |                       # Used by router & FTS5
    Expertise in Teradata-specific SQL functions...
  version: "1.0.0"
  domain: teradata                     # Groups into index tree
  labels:                              # Arbitrary k/v for label matching
    category: database
    backend: teradata
  confidence: 0.9                      # Base confidence (0.0-1.0)
  last_validated_ms: 1735689600000     # Decay anchor (epoch ms)
  status: active                       # active | deprecated | experimental
  risk_level: ""                       # "" | LOW | MEDIUM | HIGH | RESTRICTED

trigger:
  slash_commands: ["/td-analytics"]    # Phase 1: exact match
  keywords: ["teradata", "vantage"]    # Phase 3: FTS5 scoring
  mode: HYBRID                         # MANUAL | AUTO | HYBRID | ALWAYS

prompt:
  instructions: |                      # Injected into LLM context
    Use Teradata native functions...
  constraints:                         # Formatted as bullet list
    - Prefer native functions
    - Push predicates down
  examples:                            # Few-shot examples
    - user_input: "Find outliers"
      expected_output: "Use TD_OutlierFilterFit..."

tools:
  required_tools: []                   # Auto-registered on activation
  preferred_order: [execute_sql]       # Hint to LLM
  excluded_tools: []                   # Blocked during this skill

pattern_refs: [teradata-data-prep]     # Co-injected patterns
sticky: true                           # Don't evict while active
max_prompt_tokens: 3000                # Token budget for injection
parent_index_path: "data/sql"          # Position in router tree

# Task emission (Phase D)
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
  +-- library.go            In-memory skill cache + FTS5 search
  +-- orchestrator.go       Activation, eviction, formatting
  +-- config.go             SkillsConfig defaults + accessors
  |
  +-- discovery/
  |     +-- discovery.go    4-phase pipeline (slash → router → FTS5 → always)
  |
  +-- binding/
  |     +-- resolver.go     Binding resolution (exact/glob/label → mode)
  |
  +-- index/
  |     +-- builder.go      Build skill tree from Library
  |     +-- router.go       LLM-guided tree walk
  |     +-- cache.go        Per-session decision cache (LRU, 5min TTL)
  |     +-- store.go        Persistence interface (memory, SQL)
  |     +-- node.go         SkillIndexNode utilities
  |
  +-- tasks/
        +-- emitter.go      Task materialization (template or LLM decomp)


pkg/agent/agent.go
  |
  +-- runConversationLoop (lines ~1953-2080)
  |     Phases A-E orchestration
  |
  +-- skillDiscovery field     → discovery.Discovery (new path)
  +-- skillOrchestrator field  → skills.Orchestrator (activation)
  +-- skillTaskEmitter field   → tasks.Emitter (Phase D)
  +-- skillsTurnState field    → map[session][skill]bool (new-this-turn tracker)
```

---

## Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| 4-phase pipeline with short-circuit | Slash commands are free; router is expensive. Don't pay for LLM calls when a `/` prefix gives certainty. |
| Binding resolution separate from discovery | Agent config declares *eligibility*; discovery determines *relevance*. Decoupled concerns. |
| Router caching per (session, message, bindings) | Same question in same session always routes to same skills. Avoids redundant LLM calls on retries. |
| Stickiness via open tasks | Prevents evicting a skill whose decomposed work is in progress on the kanban board. |
| ALWAYS mode bypasses confidence | Unconditional skills (logging, guardrails) should never be gated by staleness. |
| Legacy MatchSkills fallback | Agents without `skillDiscovery` wired still work via the older keyword-only path. |
| Idempotent task emission | Re-activating a skill in the same session reuses prior tasks instead of duplicating. |
| Confidence decay (0.995/day) | Skills rot. Stale knowledge is dangerous. Forces periodic re-validation. |

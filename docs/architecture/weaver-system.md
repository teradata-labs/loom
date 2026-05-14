# Weaver System Architecture

## Overview

The weaver is Loom's **meta-agent** — a standard Loom agent whose job is to create other agents, workflows, and skills from natural language. It is not special infrastructure; it uses the same ROM, skills, tools, and memory system as any other agent. Its power comes from curated domain knowledge (ROM), restrictive tooling (agent_management), and skill-routed creation flows.

**Status**: v1.2.0 — Skills overhaul integrated (task emission for creation steps).

---

## What the Weaver Does

```
  USER                                         DISK
  "Create a SQL optimizer agent"    ──────>    agents/sql-optimizer.yaml
  "Build a research workflow"       ──────>    workflows/research.yaml
                                               agents/researcher.yaml
                                               agents/writer.yaml
  "Make a data-quality skill"       ──────>    skills/dq-audit.yaml
```

The weaver takes natural language descriptions and produces validated, hot-reloadable YAML configurations that the Loom server picks up immediately.

---

## System Architecture

```
+================================================================+
|                         USER                                    |
|          "I need a research report workflow"                    |
+================================================================+
         |
         | Weave RPC (or StreamWeave for streaming)
         v
+================================================================+
|                      LOOM SERVER                                |
|                                                                |
|  Registry loads weaver from:                                   |
|  $LOOM_DATA_DIR/agents/weaver.yaml                            |
|  (deployed from embedded/ on first boot)                       |
+================================================================+
         |
         | Standard agent execution
         v
+================================================================+
|                   WEAVER AGENT INSTANCE                         |
|                                                                |
|  +---------------------------------------------------------+  |
|  |              CONTEXT WINDOW (per turn)                   |  |
|  |                                                          |  |
|  |  +--------------------------------------------------+   |  |
|  |  | ROM LAYER                                        |   |  |
|  |  |  BASE ROM (operational norms)                    |   |  |
|  |  |  + WEAVER ROM (343 lines)                        |   |  |
|  |  |    Sec 1: Agent YAML schema                      |   |  |
|  |  |    Sec 2: Tool availability matrix               |   |  |
|  |  |    Sec 3: 7 workflow types + schemas             |   |  |
|  |  |    Sec 4: Communication rules                    |   |  |
|  |  |    Sec 5: Common mistakes (12 anti-patterns)     |   |  |
|  |  |    Sec 6: Minimal templates                      |   |  |
|  |  +--------------------------------------------------+   |  |
|  |  +--------------------------------------------------+   |  |
|  |  | SYSTEM PROMPT                                    |   |  |
|  |  |  3-flow router:                                  |   |  |
|  |  |    1. Preset (single agent, common roles)        |   |  |
|  |  |    2. Template (multi-agent, curated patterns)   |   |  |
|  |  |    3. From-scratch (anything else)               |   |  |
|  |  |  + House rules (confirm, no role prompts, etc.)  |   |  |
|  |  +--------------------------------------------------+   |  |
|  |  +--------------------------------------------------+   |  |
|  |  | SKILL INJECTION (activated per-turn)             |   |  |
|  |  |  weaver-creation    (always on, YAML hygiene)    |   |  |
|  |  |  weaver-presets     (/preset or keywords)        |   |  |
|  |  |  weaver-templates   (/template or keywords)      |   |  |
|  |  |  weaver-from-scratch(/from-scratch)              |   |  |
|  |  +--------------------------------------------------+   |  |
|  |  +--------------------------------------------------+   |  |
|  |  | TASK BOARD CONTEXT                               |   |  |
|  |  |  Live kanban state (creation steps in progress)  |   |  |
|  |  +--------------------------------------------------+   |  |
|  |  +--------------------------------------------------+   |  |
|  |  | CONVERSATION (L1 recent + L2 compressed)         |   |  |
|  |  +--------------------------------------------------+   |  |
|  +---------------------------------------------------------+  |
|                                                                |
|  TOOLS:                                                        |
|  +--------------------+  +---------------+  +---------------+  |
|  | agent_management   |  | shell_execute |  | tool_search   |  |
|  | (restricted caller)|  | (read refs)   |  | (FTS lookup)  |  |
|  +--------------------+  +---------------+  +---------------+  |
|  +---------------+                                             |
|  | task_board    |                                             |
|  | (track work)  |                                             |
|  +---------------+                                             |
+================================================================+
         |
         | LLM generates YAML, calls agent_management tool
         v
+================================================================+
|              AGENT MANAGEMENT TOOL                              |
|                                                                |
|  Security: Only "weaver" and "guide" agents may call this     |
|                                                                |
|  Actions:                                                      |
|    presets / apply_preset      ─ single-agent scaffolding     |
|    templates / apply_template  ─ multi-agent workflow scaffold |
|    create_agent / update_agent ─ write agent YAML             |
|    create_workflow / update_workflow ─ write workflow YAML     |
|    create_skill / update_skill ─ write skill YAML             |
|    read / list / validate / delete / discover                 |
|                                                                |
|  3-Layer Validation:                                          |
|    1. SYNTAX  ─ Valid YAML parsing                            |
|    2. STRUCTURE ─ Required fields, correct types              |
|    3. SEMANTIC ─ Tool references exist, no circular deps      |
|                                                                |
|  On success: writes to $LOOM_DATA_DIR/{agents,workflows,     |
|              skills}/<name>.yaml                               |
+================================================================+
         |
         | File written to disk
         v
+================================================================+
|                    HOT-RELOAD                                   |
|                                                                |
|  fsnotify watcher detects new/changed YAML                    |
|  Registry.handleConfigChange() fires                          |
|  Agent becomes immediately available via Weave/StreamWeave    |
+================================================================+
```

---

## The Three Creation Flows

The weaver routes user requests to one of three specialized skill-driven flows:

```
  User Request
       |
       v
  +-------------------------------------------+
  | ROUTING (system prompt + skill triggers)  |
  +-------------------------------------------+
       |
       +----------+-----------+-----------+
       |          |           |           |
       v          v           v           |
  +---------+ +---------+ +-----------+   |
  | PRESET  | |TEMPLATE | |FROM-SCRATCH|  |
  | Flow    | | Flow    | | Flow       |  |
  +---------+ +---------+ +-----------+   |
       |          |           |           |
       v          v           v           |
  Single       Multi-agent  Custom       |
  agent        workflow     anything     |
  from         from curated              |
  8 presets    7 templates               |
       |          |           |           |
       +----------+-----------+-----------+
       |
       v
  agent_management tool
  (validate → write → hot-reload)
```

### Flow 1: Preset (`/preset`)

For single agents in common roles. Triggered by `/preset` or keywords like "personal assistant", "SQL agent", "creative writer".

```
  /preset
       |
       v
  +-----------------------------+
  | Skill: weaver-presets       |
  | Injects:                    |
  |   - 8 available presets     |
  |   - JSON schema for input   |
  |   - Customization options   |
  +-----------------------------+
       |
       v
  +-----------------------------+
  | LLM: "Which preset fits?"  |
  | → action: presets (list)    |
  | → action: apply_preset     |
  |   (writes configured YAML) |
  +-----------------------------+
       |
       v
  agents/<name>.yaml written

  Presets:
    RESEARCH_ANALYST, CREATIVE_WRITER,
    TERADATA_ANALYST, UI_SPECIALIST,
    TASK_AUTOMATOR, QUICK_CHAT,
    COORDINATOR, PERSONAL_ASSISTANT
```

### Flow 2: Template (`/template`)

For multi-agent workflows matching curated patterns. Triggered by `/template` or keywords like "research report", "competitive intel", "data quality audit".

```
  /template
       |
       v
  +-----------------------------+
  | Skill: weaver-templates     |
  | Injects:                    |
  |   - 7 workflow templates    |
  |   - JSON schema for input   |
  |   - Agent composition rules |
  +-----------------------------+
       |
       v
  +-----------------------------+
  | LLM: "Which template?"     |
  | → action: templates (list)  |
  | → action: apply_template   |
  |   (writes N agents + 1 wf) |
  +-----------------------------+
       |
       v
  agents/<name>-researcher.yaml
  agents/<name>-writer.yaml
  agents/<name>-dashboard.yaml
  workflows/<name>.yaml

  Templates:
    RESEARCH_REPORT, DATA_TO_DASHBOARD,
    COMPETITIVE_INTEL, DATA_QUALITY_AUDIT,
    PERFORMANCE_REPORT, DEEP_RESEARCH,
    SKILL_HEALTH_AUDIT
```

### Flow 3: From-Scratch (`/from-scratch`)

For anything that doesn't fit a preset or template. Triggered by `/from-scratch` or when neither preset nor template matches.

```
  /from-scratch
       |
       v
  +-----------------------------+
  | Skill: weaver-from-scratch  |
  | Injects:                    |
  |   - YAML schema reference   |
  |   - Tool matrix             |
  |   - Workflow type selector  |
  |   - Step-by-step guide      |
  +-----------------------------+
       |
       v
  +-----------------------------+
  | LLM: Guided conversation    |
  | 1. Clarify requirements     |
  | 2. tool_search (find tools) |
  | 3. Draft YAML               |
  | 4. validate (dry run)       |
  | 5. Confirm with user        |
  | 6. create_agent/workflow    |
  +-----------------------------+
       |
       v
  Custom agents + workflows written
```

---

## ROM: Domain Knowledge

The WEAVER.rom (343 lines) is loaded as read-only memory on every turn. It provides ground-truth reference that the LLM cannot hallucinate away from:

```
+================================================================+
|                    WEAVER.rom (6 sections)                      |
+================================================================+

  Section 1: AGENT YAML SCHEMA
  +----------------------------------------------------------+
  | k8s-style format (apiVersion, kind, metadata, spec)      |
  | Critical rules:                                           |
  |   - tools MUST be flat array                             |
  |   - NEVER include llm: unless user asks                  |
  |   - spec.system_prompt is required                       |
  +----------------------------------------------------------+

  Section 2: TOOL AVAILABILITY MATRIX
  +----------------------------------------------------------+
  | Configurable tools:   11 tools (list in spec.tools)      |
  | Auto-registered:       5 tools (NEVER list)              |
  | Workflow-injected:     4 tools (NEVER list in agents)    |
  +----------------------------------------------------------+

  Section 3: WORKFLOW TYPES (7 types)
  +----------------------------------------------------------+
  | Orchestration (spec.type):                               |
  |   debate, fork-join, pipeline, parallel,                 |
  |   swarm, conditional, iterative                          |
  | Coordination (spec.entrypoint):                          |
  |   hub-and-spoke, peer-to-peer-pub-sub                    |
  |                                                           |
  | Each type has REQUIRED fields that must NOT be mixed     |
  +----------------------------------------------------------+

  Section 4: COMMUNICATION RULES
  +----------------------------------------------------------+
  | Messages are event-driven (no receive_message tool)      |
  | Sub-agents auto-spawn on message receipt                 |
  | Workflow tools are auto-injected (never list manually)   |
  +----------------------------------------------------------+

  Section 5: COMMON MISTAKES (12 anti-patterns)
  +----------------------------------------------------------+
  | Explicit "never do this → do this instead" pairs         |
  | Covers: tool format, workflow fields, role prompting,    |
  |         agent naming, tool guessing                       |
  +----------------------------------------------------------+

  Section 6: MINIMAL TEMPLATES
  +----------------------------------------------------------+
  | Copy-paste-ready YAML for each workflow type             |
  | Minimal viable configs (10-20 lines each)               |
  +----------------------------------------------------------+
```

---

## Skill Activation During Creation

The weaver's skills activate based on the conversation turn:

```
  Turn 1: User says "I need a research workflow"
       |
       v
  +-----------------------------------------------+
  | Skill Discovery Pipeline                      |
  |                                                |
  | Phase 1: Slash? NO                            |
  | Phase 3: Keywords? "research" + "workflow"    |
  |   → weaver-templates matches (keywords hit)   |
  |   → weaver-creation matches (ALWAYS on)       |
  | Phase 4: ALWAYS bindings                      |
  |   → weaver-creation (priority 100)            |
  +-----------------------------------------------+
       |
       v
  Active this turn:
    1. weaver-creation  (YAML hygiene rules)
    2. weaver-templates (template flow instructions)

  ─────────────────────────────────────────────

  Turn 2: User says "/preset researcher"
       |
       v
  +-----------------------------------------------+
  | Skill Discovery Pipeline                      |
  |                                                |
  | Phase 1: Slash? YES → "/preset"              |
  |   → weaver-presets matches (slash_commands)   |
  | Phase 4: ALWAYS                               |
  |   → weaver-creation (priority 100)            |
  +-----------------------------------------------+
       |
       v
  Active this turn:
    1. weaver-creation  (YAML hygiene rules)
    2. weaver-presets   (preset flow instructions)
  (weaver-templates evicted — lower confidence)
```

---

## Task Emission for Creation Steps

When a creation skill activates, it emits tasks onto the weaver's kanban board:

```
  Skill "weaver-templates" activates
       |
       v
  +----------------------------------------------+
  | Emitter checks:                              |
  |   tasks_enabled: true ✓                      |
  |   skill.emit_tasks: true ✓                   |
  |   task_template defined? YES ✓               |
  +----------------------------------------------+
       |
       v
  +----------------------------------------------+
  | Materialize task_template steps:             |
  |                                              |
  |   Task 1: "Select Template"                  |
  |     objective: List templates, user picks    |
  |     priority: P0                             |
  |     idempotency: skill:weaver-templates|     |
  |                  sess:abc123|step:0          |
  |                                              |
  |   Task 2: "Customize Configuration"         |
  |     objective: Gather user preferences       |
  |     priority: P1                             |
  |     depends_on: [Task 1]                     |
  |                                              |
  |   Task 3: "Create Agents"                   |
  |     objective: Write agent YAML files        |
  |     priority: P1                             |
  |     depends_on: [Task 2]                     |
  |                                              |
  |   Task 4: "Create Workflow"                  |
  |     objective: Write workflow YAML           |
  |     priority: P2                             |
  |     depends_on: [Task 3]                     |
  +----------------------------------------------+
       |
       v
  Tasks visible on weaver-board
  (weaver claims → updates → closes as it works)
  (open tasks make skill "sticky" — won't evict)
```

---

## Validation Pipeline

The agent_management tool validates YAML before writing:

```
  LLM generates YAML content
       |
       v
  +==========================================+
  |        agent_management tool call        |
  |   action: create_agent                   |
  |   content: "<generated YAML>"            |
  +==========================================+
       |
       v
  Layer 1: SYNTAX
  +------------------------------------------+
  | Can it parse as YAML?                    |
  | Are there encoding issues?               |
  | Is it valid UTF-8?                       |
  +------------------------------------------+
       | pass
       v
  Layer 2: STRUCTURE
  +------------------------------------------+
  | Has apiVersion: loom/v1?                 |
  | Has kind: Agent (or Workflow/Skill)?     |
  | Has metadata.name (kebab-case)?          |
  | Has spec.system_prompt?                  |
  | Are tools a flat array?                  |
  | Are fields the correct types?            |
  +------------------------------------------+
       | pass
       v
  Layer 3: SEMANTIC
  +------------------------------------------+
  | Do referenced tools actually exist?      |
  | Are workflow agent_ids valid?            |
  | No circular dependencies?                |
  | No conflicting workflow fields?          |
  +------------------------------------------+
       | pass
       v
  Write to $LOOM_DATA_DIR/<kind>s/<name>.yaml
  Return success + file path
       |
       | fail (any layer)
       v
  Return structured error:
    { layer: "structure",
      errors: ["spec.tools must be array, got object"],
      suggestion: "Use flat array: tools: [shell_execute]" }
  LLM reads error, fixes YAML, retries
```

---

## Deployment & Embedding

The weaver ships as an embedded resource in the Loom binary:

```
  BUILD TIME
  +--------------------------------------------------+
  | cmd/generate-weaver/main.go                      |
  |   Reads: embedded/weaver.yaml.tmpl               |
  |   Generates: embedded/weaver.yaml                |
  |   (Currently template == output; infra ready     |
  |    for future CLI help embedding)                |
  +--------------------------------------------------+
       |
       v
  +--------------------------------------------------+
  | embedded/agents.go                               |
  |   //go:embed weaver.yaml                         |
  |   var WeaverYAML []byte                          |
  |                                                   |
  |   //go:embed skills/weaver-creation.yaml         |
  |   //go:embed skills/weaver-presets.yaml          |
  |   //go:embed skills/weaver-templates.yaml        |
  |   //go:embed skills/weaver-from-scratch.yaml     |
  +--------------------------------------------------+
       |
       v (compiled into binary)

  RUNTIME (server startup)
  +--------------------------------------------------+
  | cmd/looms/cmd_serve.go                           |
  |                                                   |
  |   if !exists($LOOM_DATA_DIR/agents/weaver.yaml): |
  |     write(embedded.GetWeaver())                  |
  |                                                   |
  |   for each bundled skill:                        |
  |     if !exists($LOOM_DATA_DIR/skills/<name>):    |
  |       write(embedded.GetSkill(name))             |
  +--------------------------------------------------+
       |
       v
  +--------------------------------------------------+
  | ON DISK (user can modify/delete)                 |
  |                                                   |
  | $LOOM_DATA_DIR/                                  |
  |   agents/                                         |
  |     weaver.yaml        ← meta-agent config       |
  |     guide.yaml         ← read-only companion     |
  |   skills/                                         |
  |     weaver-creation.yaml                          |
  |     weaver-presets.yaml                           |
  |     weaver-templates.yaml                         |
  |     weaver-from-scratch.yaml                      |
  +--------------------------------------------------+
       |
       v
  Hot-reload picks up files
  Weaver available via: looms weave weaver
```

---

## ROM Loading Path

```
  Agent config: rom: "weaver"
       |
       v
  getSystemPrompt() [agent.go:889]
       |
       v
  LoadROMContent("weaver", backendPath) [rom_loader.go:58]
       |
       v
  switch romID:
    case "weaver":
      domainROM = weaverROM  (//go:embed roms/WEAVER.rom)
       |
       v
  finalROM = baseROM + "\n\n---\n\n" + domainROM
       |
       v
  systemPrompt = finalROM + "\n\n---\n\n" + config.SystemPrompt
       |
       v
  Injected as system message in every LLM call
```

---

## Component Map

```
  embedded/
    weaver.yaml.tmpl          Template (source of truth)
    weaver.yaml               Generated output (deployed)
    agents.go                 go:embed declarations
    guide.yaml                Read-only companion agent
    skills/
      weaver-creation.yaml    ALWAYS-on hygiene rules
      weaver-presets.yaml     /preset flow instructions
      weaver-templates.yaml   /template flow instructions
      weaver-from-scratch.yaml /from-scratch flow

  cmd/generate-weaver/
    main.go                   Template → YAML generator

  pkg/agent/roms/
    WEAVER.rom                Domain knowledge (343 lines)
    START_HERE.md             Base ROM (shared by all agents)

  pkg/agent/
    rom_loader.go             Loads ROM by ID, composes layers
    agent.go:889-995          System prompt assembly

  pkg/shuttle/builtin/
    agent_management.go       Restricted tool (create/validate/write)
    agent_management_skill.go Skill-specific actions
```

---

## Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| Weaver is a regular agent | No special infrastructure. Same lifecycle, tools, memory as any agent. Easier to maintain and extend. |
| ROM for domain knowledge | Static, compiled-in, always available. Cannot be hallucinated away. 343 lines of ground truth. |
| Skills for flow routing | Keeps per-turn context small. Only the active flow's instructions are injected. |
| 3-layer validation | Catches errors before disk write. Structured error messages let the LLM self-correct. |
| Restricted tool access | Only weaver/guide can call agent_management. Prevents arbitrary agents from self-modifying. |
| Embedded deployment | Ships with binary. Zero external dependencies. Users can opt-out by deleting files. |
| Hot-reload integration | Created agents are immediately usable. No server restart needed. |
| Task board for tracking | Weaver tracks its own creation steps. Skills emit structured tasks. |
| No role prompting rule | Enforced in ROM, system prompt, and weaver-creation skill. Direct instructions produce better agents. |
| Agents before workflows | ROM explicitly states: create agents first, then wire workflows. Prevents dangling references. |

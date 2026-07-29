
# Agent Configuration Reference

Technical specification for Loom agent configuration. Covers both the **server-level** agent
definitions (in `looms.yaml`) and the **standalone agent YAML** files loaded by the server.

**Version**: v1.3.0
**API Version**: loom/v1
**Configuration Kind**: `Agent`

---

## Table of Contents

- [Quick Reference](#quick-reference)
- [Two Configuration Layers](#two-configuration-layers)
- [Standalone Agent YAML Schema](#standalone-agent-yaml-schema)
  - [K8s-Style Format](#k8s-style-format)
  - [Legacy Format](#legacy-format)
- [Configuration Fields](#configuration-fields)
  - [Metadata](#metadata)
  - [LLM Configuration](#llm-configuration)
  - [Role-Specific LLM Overrides](#role-specific-llm-overrides)
  - [Provider Pool](#provider-pool)
  - [System Prompt and ROM](#system-prompt-and-rom)
  - [Tools Configuration](#tools-configuration)
  - [Memory Configuration](#memory-configuration)
  - [Graph Memory Configuration](#graph-memory-configuration)
  - [Memory Compression Configuration](#memory-compression-configuration)
  - [Behavior Configuration](#behavior-configuration)
  - [Pattern Configuration](#pattern-configuration)
  - [Skills Configuration](#skills-configuration)
  - [Ephemeral Agents](#ephemeral-agents)
  - [Backend (Inline or Path)](#backend-inline-or-path)
- [Server-Level Agent Configuration](#server-level-agent-configuration)
- [LLM Providers](#llm-providers)
- [Backend Types](#backend-types)
- [Environment Variable Expansion](#environment-variable-expansion)
- [Validation Rules](#validation-rules)
- [Examples](#examples)
- [See Also](#see-also)

---

## Quick Reference

| Field (spec) | Type | Default | Description |
|---|---|---|---|
| `metadata.name` | `string` | **required** | Agent identifier |
| `metadata.description` | `string` | optional | Human-readable description |
| `spec.llm` | `LLMConfigYAML` | inherits server | LLM provider and model |
| `spec.system_prompt` | `string` | optional | System prompt text |
| `spec.rom` | `string` | `""` | Read-only memory ID (`TD`, `teradata`, `weaver`, `auto`, `none`, `""`) |
| `spec.tools` | `[]string` or `ToolsConfigYAML` | optional | Tool list |
| `spec.memory` | `MemoryConfigYAML` | `type: memory` | Session storage |
| `spec.config` | `BehaviorConfigYAML` | defaults below | Behavior limits |
| `spec.backend_path` | `string` | optional | Path to backend YAML |
| `spec.backend` | inline object | optional | Inline backend config |
| `spec.judge_llm` | `LLMConfigYAML` | inherits main | Judge LLM override |
| `spec.orchestrator_llm` | `LLMConfigYAML` | inherits main | Orchestrator LLM override |
| `spec.classifier_llm` | `LLMConfigYAML` | inherits main | Classifier LLM override |
| `spec.compressor_llm` | `LLMConfigYAML` | inherits main | Compressor LLM override |
| `spec.active_provider` | `string` | optional | Named provider from global pool |
| `spec.allowed_providers` | `[]string` | optional | Restrict to subset of pool |

---

## Two Configuration Layers

Loom has two distinct configuration surfaces:

1. **Server config (`looms.yaml`)** -- configures the `looms` server process: gRPC port, default
   LLM provider, storage backend, MCP servers, observability, and optionally a map of inline
   agent definitions under the `agents.agents:` key. See [Server-Level Agent Configuration](#server-level-agent-configuration).

2. **Standalone agent YAML** -- a per-agent file loaded by the server at startup (or hot-reloaded).
   Uses `apiVersion: loom/v1 / kind: Agent` (k8s-style) or the legacy `agent:` root key.
   The remainder of this document focuses on this format.

If an agent YAML omits `spec.llm`, the agent inherits the server's default LLM provider.

---

## Standalone Agent YAML Schema

### K8s-Style Format

The recommended format. Detected by the presence of the `apiVersion` field.

```yaml
apiVersion: loom/v1       # Required
kind: Agent               # Required

metadata:
  name: my-agent          # Required
  version: "1.0.0"        # Optional
  description: "..."      # Optional
  role: executor           # Optional (for workflow agents)
  workflow: my-workflow    # Optional (for workflow agents)
  labels:                  # Optional (arbitrary key-value pairs)
    team: platform

spec:
  llm:                     # Optional (inherits from server)
    provider: anthropic
    model: claude-sonnet-4-5-20250929
    temperature: 0.7
    max_tokens: 4096

  system_prompt: |
    Direct, task-oriented instructions for the agent.

  rom: "auto"              # Optional

  tools:                   # Flat array of tool names, or structured ToolsConfigYAML
    - shell_execute
    - web_search
    - tool_search

  memory:
    type: sqlite
    max_history: 1000

  config:                  # Behavior settings
    max_turns: 25
    max_tool_executions: 50
    timeout_seconds: 300
```

### Legacy Format

Detected when `apiVersion` is absent and an `agent:` root key is present.

```yaml
agent:
  name: my-agent          # Required
  description: "..."      # Optional
  backend_path: ./backends/my-backend.yaml  # Optional
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  system_prompt: |
    Direct, task-oriented instructions.
  tools:
    builtin:
      - shell_execute
    mcp:
      - server: vantage
        tools: [execute_query]
  memory:
    type: memory
    max_history: 50
  behavior:
    max_turns: 25
    max_tool_executions: 50
    timeout_seconds: 300
```

Both formats are converted to the same `loomv1.AgentConfig` proto at load time.

---

## Configuration Fields

### Metadata

**k8s-style**: fields live under `metadata:`.
**Legacy**: fields live directly under `agent:`.

#### metadata.name

**Type**: `string`
**Required**: Yes

Agent identifier. Used to address the agent in gRPC calls and in multi-agent workflows.

#### metadata.version

**Type**: `string`
**Required**: No

Semantic version string. Stored in agent metadata map.

#### metadata.description

**Type**: `string`
**Required**: No

Human-readable description of the agent.

#### metadata.labels

**Type**: `map[string]interface{}`
**Required**: No

Arbitrary key-value pairs. Merged into the proto `metadata` map as strings (complex values are JSON-encoded).

---

### LLM Configuration

Under `spec.llm` (k8s-style) or `agent.llm` (legacy).

All fields are optional. If `spec.llm` is omitted entirely, the agent inherits the server's default LLM provider. If partially specified, **both** `provider` and `model` are required.

| Field | Type | Default | Description |
|---|---|---|---|
| `provider` | `string` | (server default) | Provider name. See [LLM Providers](#llm-providers). |
| `model` | `string` | (server default) | Model identifier |
| `temperature` | `float64` | `0.7` | Sampling temperature (0.0 - 1.0) |
| `max_tokens` | `int` | `4096` | Maximum response tokens |
| `stop_sequences` | `[]string` | `[]` | Stop generation sequences |
| `top_p` | `float64` | `0.0` | Nucleus sampling (0.0 - 1.0) |
| `top_k` | `int` | `0` | Top-k sampling |
| `max_context_tokens` | `int` | auto-detected | Context window size (e.g., 200000 for Claude) |
| `reserved_output_tokens` | `int` | 10% of context | Tokens reserved for model output |

---

### Role-Specific LLM Overrides

Each role can use a different provider/model optimized for its task.
Fallback chain: role-specific LLM -> agent default LLM -> server default LLM.

| Field | Purpose | Example use case |
|---|---|---|
| `spec.judge_llm` | Evaluation / judging | Gemini for unbiased evaluation |
| `spec.orchestrator_llm` | Fork-join merge / synthesis | Fast model for combining results |
| `spec.classifier_llm` | Intent classification | Small local model (Ollama) for pattern selection |
| `spec.compressor_llm` | Memory compression / reranking | Cost-effective model (Haiku) for compression |

Each accepts the same `LLMConfigYAML` schema as the main `llm` field.

```yaml
spec:
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  judge_llm:
    provider: gemini
    model: gemini-2.5-flash
  classifier_llm:
    provider: ollama
    model: llama3.1:8b
```

---

### Provider Pool

Agents can reference named providers from the server's global provider pool instead of (or in addition to) specifying their own LLM config.

| Field | Type | Description |
|---|---|---|
| `spec.active_provider` | `string` | Name of a provider entry from the global pool |
| `spec.allowed_providers` | `[]string` | Restrict this agent to a subset of pool entries (empty = all) |

```yaml
spec:
  active_provider: claude-opus
  allowed_providers:
    - claude-opus
    - llama-local
```

---

### System Prompt and ROM

#### spec.system_prompt

**Type**: `string`
**Required**: No (but recommended)

Direct system prompt text. Defines the agent's behavior and instructions.

Per project convention: **no role prompting**. Use direct, task-oriented instructions.

```yaml
spec:
  system_prompt: |
    Analyze SQL queries for performance issues.
    Use EXPLAIN output to identify bottlenecks.
    Suggest index and partitioning improvements.
```

#### spec.rom

**Type**: `string`
**Default**: `""` (no ROM)

Read-Only Memory identifier. Embeds domain-specific documentation into the system prompt.

| Value | Behavior |
|---|---|
| `"TD"` or `"teradata"` | Load embedded Teradata SQL guidance (~6KB domain + ~1KB base = ~7KB total) |
| `"weaver"` | Load embedded Weaver ROM |
| `"auto"` | Auto-detect from `backend_path` (looks for "teradata" or "vantage") |
| `"none"` | Explicit opt-out (no ROM at all, not even base ROM) |
| `""` | Base ROM only (operational guidance for all agents); auto-detects domain if `backend_path` is set |

---

### Tools Configuration

Under `spec.tools` (k8s-style) or `agent.tools` (legacy).

**K8s-style** supports two sub-formats:

**Flat array** (preferred for simplicity):
```yaml
spec:
  tools:
    - shell_execute
    - web_search
    - tool_search
    - http_request
```

**Structured format** (for MCP and custom tools):
```yaml
spec:
  tools:
    builtin:
      - shell_execute
      - web_search
    mcp:
      - server: vantage
        tools:
          - execute_query
          - list_tables
    custom:
      - name: my_tool
        implementation: /path/to/plugin.so
```

**Tool categories**:

| Category | Behavior |
|---|---|
| **Configurable** (list in `spec.tools`) | `http_request`, `web_search`, `file_read`, `file_write`, `analyze_image`, `parse_document`, `grpc_call`, `shell_execute`, `contact_human`, `agent_management`, `tool_search` |
| **Registered at construction** (not listed in `spec.tools`; present from the first turn when their subsystem is wired) | `load_pattern` (always), `manage_skills` (skill orchestrator wired), `graph_memory` (graph memory store configured and enabled), `task_board` (task manager wired and `task_board.enabled`) |
| **Progressively disclosed** (registered dynamically after triggering conditions) | `get_error_details` (after first error), `conversation_memory` (after first L2 swap), `session_memory` (after 3+ sessions), `query_tool_result` (after first large result or first tool result returned by reference) |
| **Workflow-injected** (auto-added for workflow agents) | `send_message`, `publish`, `shared_memory_read`, `shared_memory_write`, `top_n_query`, `group_by_query` |

The server's tool policy withholds some of these from the model while their subsystems keep running: `tools.minimal` suppresses `graph_memory` and `task_board`; `tools.none` additionally suppresses `conversation_memory`, `session_memory`, `get_error_details`, `query_tool_result`, the workflow-injected tools, and `manage_ephemeral_agents`. `manage_skills` and `load_pattern` are not suppressed by either policy.

#### manage_skills

**Registered when**: `spec.config.skills.enabled` is true and a skill library is wired.
**Available since**: v1.3.0

The model's only route to a skill body. Skills are never injected into the prompt: the system prompt carries a static `name — description` menu of the agent's bound skills, rendered once at session creation, and the body of a skill arrives only when the model loads it.

**Input**:

| Field | Type | Required | Description |
|---|---|---|---|
| `action` | `enum` (`list`, `load`) | Yes | Operation to perform |
| `name` | `string` | For `load` | Skill name |

**Behavior**:

- `list` returns the whole library as JSON: `session_id`, `active_count`, and one entry per skill with its catalog summary plus an `active` flag for this session.
- `load` activates the skill for this session, registers the skill's `required_tools` for this session, returns `Skill loaded: <name>` as the tool result, and delivers the verbatim skill body as a separate user-role message. Pattern references declared by the skill are appended to that message as `load_pattern` references.
- There is no `unload` action. A load appends to the session's active set; it never displaces an already-active skill, and no skill-count cap bounds the set. Loading a skill that is already active replaces its entry in place.
- Activation is per session and lasts until the session ends.

**Errors**:

| Code | Condition |
|---|---|
| `invalid_input` | `action` is neither `load` nor `list`, or `load` was called without `name` |
| `not_found` | No skill by that name in the library |
| `approval_required` | Skill's `risk_level` is `HIGH` or `RESTRICTED`, a permission checker is wired, and YOLO mode is off. The skill is not activated. A nil permission checker or `tools.permissions.yolo=true` disables the gate. |

#### load_pattern

**Registered when**: always — every agent gets a pattern library object, whose content comes from `patterns_dir`. With no pattern directory configured, every reference resolves to `PATTERN_NOT_FOUND`.
**Available since**: v1.3.0

Pulls one pattern's content into the conversation. Patterns are never injected; the reference comes from the pattern list surfaced by a `manage_skills` load.

**Input**:

| Field | Type | Required | Description |
|---|---|---|---|
| `reference` | `string` | Yes | Pattern reference, as surfaced by `manage_skills(load)` |

**Behavior**: returns the pattern's LLM rendering as ordinary string tool-result data. It lands in L1 like any other tool result and is subject to the same size threshold and compaction rules. It never populates the system-prompt pattern slot.

**Errors**:

| Code | Condition |
|---|---|
| `INVALID_PARAMETER` | `reference` empty |
| `PATTERN_LIBRARY_UNAVAILABLE` | No pattern library configured |
| `PATTERN_NOT_FOUND` | Unknown reference. No pattern content enters the conversation. |

---

### Memory Configuration

Under `spec.memory` (k8s-style) or `agent.memory` (legacy).

| Field | Type | Default | Description |
|---|---|---|---|
| `type` | `string` | `"memory"` | `memory` (ephemeral), `sqlite`, or `postgres` |
| `path` | `string` | `""` | SQLite database file path |
| `dsn` | `string` | `""` | PostgreSQL connection string |
| `max_history` | `int` | `50` | Max conversation messages to retain |
| `shared_memory_threshold_bytes` | `int64` | `65536` (64 KiB) | Byte threshold for offloading a tool result: `>0` offloads results of at least N bytes, `-1` selects the 64 KiB default. Only a non-zero value is applied, so `0` in YAML is indistinguishable from unset and leaves the agent on the 64 KiB default; the always-offload mode (threshold 0) is reachable only through the Go API `SetSharedMemoryThreshold`. |
| `max_tool_results` | `int` | `5` | Max tool results kept in conversation kernel |
| `memory_compression` | object | optional | See [Memory Compression](#memory-compression-configuration) |
| `graph_memory` | object | optional | See [Graph Memory](#graph-memory-configuration) |

**Large tool results**: the same byte threshold governs both offload sites (the tool executor and the agent's result formatter). A result at or above it is stored by reference and replaced in the conversation with a preview plus a handle; the model recalls the full data with `query_tool_result`. Three tools are exempt and always enter whole regardless of size: `manage_skills` (its load body is delivered as its own message), `get_tool_result` and `query_tool_result` (re-offloading a recall tool would recurse).

---

### Graph Memory Configuration

Salience-driven graph-backed episodic memory. Stores entities, relationships, and memories with FTS5 full-text search. **Enabled by default**: graph memory is automatically enabled for all agents when the storage backend provides a `GraphMemoryStore`. No YAML changes required -- pre-existing agents get graph memory on server restart via `DefaultGraphMemoryConfig()`.

Under `spec.memory.graph_memory`.

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | `*bool` | `true` (opt-out) | Enable graph memory |
| `context_budget_percent` | `int` | `20` | % of max context window for memory budget |
| `max_context_tokens` | `int` | `0` | Fixed token budget (overrides percent if > 0) |
| `decay_rate` | `float64` | `0.95` | Salience decay rate per day (~14-day half-life) |
| `boost_amount` | `float64` | `0.1` | Salience boost per access |
| `min_salience_threshold` | `float64` | `0.1` | Minimum salience for recall |
| `max_recall_candidates` | `int` | `50` | Max memories to consider during recall |
| `default_salience` | `float64` | `0.5` | Initial salience for new memories |

```yaml
spec:
  memory:
    type: sqlite
    max_history: 100
    graph_memory:
      enabled: true
      context_budget_percent: 15
      decay_rate: 0.99
      max_recall_candidates: 100
```

To opt out:

```yaml
spec:
  memory:
    graph_memory:
      enabled: false
```

---

### Memory Compression Configuration

Conversation history compression using a tiered L1/L2 cache with workload-aware thresholds.

Compression triggers on one criterion: token-budget usage above the profile's `warning_threshold_percent`, measured against the session's real context window. There is no fixed L1 message or token cap — L1 grows until the budget is under pressure. Once triggered, the batch size is chosen by severity (normal / warning / critical), and compression stops while L1 is down to `min_l1_messages`.

Compressed batches are summarized by the LLM compressor when both an LLM (the `compressor_llm` role if set, otherwise the agent LLM) and a prompt registry are configured; otherwise a heuristic summarizer is used, and it is also the fallback when a compression prompt fails to load.

Under `spec.memory.memory_compression`.

| Field | Type | Default | Description |
|---|---|---|---|
| `workload_profile` | `string` | `balanced` | `balanced`, `data_intensive`, or `conversational` |
| `max_l1_messages` | `int` | profile-dependent | No longer drives behaviour. Still parsed: it sets the profile's L1 token target (`max_l1_messages` × 800), which is reported in memory stats and startup logs but does not gate compression. |
| `min_l1_messages` | `int` | max_l1 / 2 | Min messages left in L1 after compression |
| `warning_threshold_percent` | `int` | profile-dependent | Warning threshold (0-100) |
| `critical_threshold_percent` | `int` | profile-dependent | Critical threshold (0-100) |
| `batch_sizes.normal` | `int` | profile-dependent | Messages per batch (normal) |
| `batch_sizes.warning` | `int` | profile-dependent | Messages per batch (warning) |
| `batch_sizes.critical` | `int` | profile-dependent | Messages per batch (critical) |

**Workload profiles**:

| Profile | L1 token target | min_l1 | warning | critical | batch (N/W/C) | Use case |
|---|---|---|---|---|---|---|
| `balanced` | 6400 | 4 | 60% | 75% | 3/5/7 | General-purpose agents |
| `data_intensive` | 4000 | 3 | 50% | 70% | 2/4/6 | SQL, large file operations |
| `conversational` | 9600 | 6 | 70% | 85% | 4/6/8 | Chat-heavy, minimal tool usage |

The L1 token target is a reporting figure only; `warning` is the value that triggers compression.

---

### Behavior Configuration

Under `spec.config` (k8s-style) or `agent.behavior` (legacy).

| Field | Type | Default | Description |
|---|---|---|---|
| `max_iterations` | `int` | `10` | Max tool call iterations per turn |
| `timeout_seconds` | `int` | `300` | Timeout per message (seconds) |
| `allow_code_execution` | `bool` | `false` | Allow shell execution |
| `allowed_domains` | `[]string` | `[]` | Domain whitelist for web access |
| `max_turns` | `int` | `25` | Max conversation turns |
| `max_tool_executions` | `int` | `50` | Max tool calls per conversation |
| `output_token_cb_threshold` | `int` | `8` | Consecutive truncated-tool-call turns before circuit breaker fires. `0` = default (8). `-1` = disabled. |
| `patterns` | object | optional | See [Pattern Configuration](#pattern-configuration) |
| `skills` | object | optional | See [Skills Configuration](#skills-configuration) |

---

### Pattern Configuration

Under `spec.config.patterns` (k8s-style) or `agent.behavior.patterns` (legacy).

Patterns reach the conversation on demand only, through the `load_pattern` tool. The conversation loop performs no intent classification, no pattern selection and no pattern injection, so the fields below no longer drive runtime behaviour. They remain in the YAML schema and the proto, and are still parsed.

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | `*bool` | `true` | No longer drives behaviour. There is no injection path to enable or disable; `load_pattern` is registered regardless. |
| `min_confidence` | `*float64` | `0.75` | No longer drives behaviour. Parsed and validated (0.0 - 1.0), never read at runtime. |
| `max_patterns_per_turn` | `*int` | `1` | No longer drives behaviour. Nothing injects patterns per turn; the model may call `load_pattern` as often as it needs. |
| `enable_tracking` | `*bool` | `true` | No longer drives behaviour on the agent path. |
| `use_llm_classifier` | `*bool` | `true` | Installs an LLM intent classifier on the pattern orchestrator, using `classifier_llm` when set. The conversation loop does not invoke it; only callers that drive the pattern orchestrator directly do. |

Pattern content is pulled with references a loaded skill declares. See [manage_skills](#manage_skills) and [load_pattern](#load_pattern).

---

### Skills Configuration

Under `spec.config.skills` (k8s-style) or `agent.behavior.skills` (legacy).

Skills reach the conversation on demand only, through the `manage_skills` tool. The system prompt carries a static `name — description` menu of the skills bound to this agent, rendered once at session creation; a skill body enters the conversation only after a load. There is no keyword or intent auto-activation, no slash-command activation, no always-on mode, and no per-turn discovery pass. The YAML trigger modes (`AUTO`, `ALWAYS`, `HYBRID`), `sticky`, and `max_prompt_tokens` in a skill file are parsed but no longer drive runtime behaviour.

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | `*bool` | `true` once any skills field is set | Master switch for the skills subsystem. A skills config is built when any skill field is present (`enabled`, `bindings`, `enabled_skills`, `disabled_skills`, the router knobs, the task knobs). With no skills config at all, or with `enabled: false`, no orchestrator is built, `manage_skills` is not registered, and no skill menu is rendered. |
| `bindings` | `[]SkillBinding` | `[]` | Declarative skill attachment: which skills this agent is bound to, and therefore what the prompt menu lists. Each entry takes `name` (exact name, fully-qualified name, or glob — `*` does not cross `/`), `mode`, `priority`, `label_match` (ANDed across keys), and `min_version`. `mode` (`EAGER`, `LAZY`, `ALWAYS`) is parsed but does not change what reaches the conversation: every bound skill appears in the menu, and none of them enters the context until the model loads it. |
| `enabled_skills` | `[]string` | `[]` | Whitelist of skill names. Legacy shim: used only when `bindings` is empty, where each name becomes an eager binding. |
| `disabled_skills` | `[]string` | `[]` | Blacklist of skill names. Legacy shim: used only when both `bindings` and `enabled_skills` are empty, where the binding set becomes the whole library minus these names. |
| `min_auto_confidence` | `*float64` | `0.7` | No longer drives behaviour on the conversation path: a load is always explicit, so nothing is scored against this floor. It still bounds keyword-match candidates for callers that drive skill discovery directly. |
| `max_concurrent_skills` | `*int` | `3` | No longer caps the active set. A `manage_skills` load appends to the session's active set and never evicts. The value still bounds search candidates for callers that drive skill matching directly. |
| `skills_dir` | `string` | `""` | Directory containing skill definitions |
| `context_budget_percent` | `*int` | `5` | No longer drives behaviour. A loaded skill body enters whole; it is not trimmed to a share of the context window. |
| `hygiene` | `HygieneConfig` | (defaults) | End-of-turn task-board hygiene for skill-emitted tasks. See below. |

**High-risk skills**: a skill whose `risk_level` is `HIGH` or `RESTRICTED` is not loaded when a permission checker is wired and YOLO mode is off; the load returns an `approval_required` error and the active set is untouched. A nil permission checker disables the gate, as does `tools.permissions.yolo=true`.

#### HygieneConfig

**Available since**: v1.2.0+ (PR #184)

Governs the end-of-turn audit that catches skill-emitted tasks left in `IN_PROGRESS`, `BLOCKED` (with no surfaced HITL), or `OPEN` (never started) state. Scope is strictly skill-emitted tasks (matched via `SkillIdempotencyKey`); tasks created ad-hoc through `TaskBoardTool` are not audited.

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | `*bool` | `true` (when unset) | Enable end-of-turn auditing. Uses proto3 optional so the loader distinguishes "not specified" (default true) from "explicitly false". |
| `policy` | enum `HygienePolicy` | `REQUIRE_FIX` | Action taken when violations are found. See enum below. |
| `max_retries` | `int32` | `2` | Cap on `REQUIRE_FIX` retries per turn before falling through to `AUTO_FIX` so the loop terminates. Values `<= 0` fall back to the default. |

**`HygienePolicy` enum values**:

| Value | Behavior |
|---|---|
| `HYGIENE_POLICY_UNSPECIFIED` | Falls back to `REQUIRE_FIX`. |
| `HYGIENE_POLICY_REQUIRE_FIX` | Inject a synthetic user message describing the violations and re-run the LLM turn. Capped by `max_retries`; on exhaustion the auditor downgrades to `AUTO_FIX` for that pass so the conversation loop terminates. |
| `HYGIENE_POLICY_AUTO_FIX` | Machine-transition each violating task: `OPEN`-unstarted → `DEFERRED`, `IN_PROGRESS` → `OPEN`, `BLOCKED` → spawn HITL (when a HITL spawner is wired; logged otherwise). Each mutated task gets a `[hygiene]`-prefixed note in `Task.Notes`. |
| `HYGIENE_POLICY_WARN_ONLY` | Emit observability events + add a violations summary to `Response.Metadata`. Do not change task state and do not retry. |

**Example**:

```yaml
spec:
  config:
    skills:
      enabled: true
      hygiene:
        enabled: true
        policy: HYGIENE_POLICY_REQUIRE_FIX
        max_retries: 2
```

**Response metadata stamped on every turn that ran the auditor**:

| Key | Type | Notes |
|---|---|---|
| `hygiene_policy` | string | Resolved policy name. |
| `hygiene_violations_found` | int | Total violations detected this turn. |
| `hygiene_by_kind` | `map[string]int` | Counts keyed by violation kind: `in_progress_orphan`, `blocked_no_hitl`, `open_unstarted`. |
| `hygiene_resolved` | int | Tasks transitioned under `AUTO_FIX`. |
| `hygiene_hitl_spawned` | int | `BLOCKED` tasks turned into HITL requests. |
| `hygiene_fallthrough` | string | Set only when `REQUIRE_FIX` exhausted retries and fell through to `AUTO_FIX`. |

**See also**:
- [`skill-hygiene.md`](../architecture/skill-hygiene.md) — design rationale and trade-off analysis.
- [`task-system.md`](../architecture/task-system.md) — the `ListBySkillRun` query backing the audit.

---

### Ephemeral Agents

Defined in proto as `AgentConfig.ephemeral_agents`. Policies for dynamically spawning agents at runtime.

> **Note**: Ephemeral agent policies are defined in the proto schema but are **not currently parsed from YAML** by the config loader. They must be set programmatically via the Go API or gRPC. The YAML example below shows the proto schema for reference.

```yaml
spec:
  ephemeral_agents:
    - role: judge
      trigger:
        type: CONSENSUS_NOT_REACHED
        threshold: 0.67
      template:
        name: ephemeral-judge
        system_prompt: |
          Analyze all perspectives and make an evidence-based decision.
        config:
          max_turns: 5
          timeout_seconds: 60
      max_spawns: 1
      cost_limit_usd: 0.50
```

**Spawn trigger types**:

| Type | Description | `threshold` usage |
|---|---|---|
| `CONSENSUS_NOT_REACHED` | Consensus failed in debate/swarm | Minimum agreement % |
| `CONFIDENCE_BELOW` | Average confidence below threshold | Minimum confidence |
| `TIE_DETECTED` | Voting tie | Not used |
| `ESCALATION_REQUESTED` | Agent requests escalation | Not used |
| `ALWAYS` | Unconditional (testing) | Not used |
| `CUSTOM` | Custom runtime expression | `condition` field |

---

### Backend (Inline or Path)

Agents connect to data sources via backend configuration. Two approaches:

**Path reference** (recommended -- keeps backend config separate):
```yaml
spec:
  backend_path: ./backends/analytics-postgres.yaml
```

**Inline backend** (k8s-style only):
```yaml
spec:
  backend:
    name: my-backend
    type: rest
    config:
      base_url: https://api.example.com
      auth_type: bearer
      auth_token_env: API_TOKEN
```

See [Backend Types](#backend-types) for supported backend types.

---

## Server-Level Agent Configuration

Agents can also be defined inline in the server config file (`looms.yaml`) under the `agents.agents` key. These use a simpler structure than standalone agent YAML.

Source: `AgentConfig` struct in `cmd/looms/config.go`.

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | `string` | required | Agent name |
| `description` | `string` | `""` | Agent description |
| `backend_path` | `string` | `""` | Path to backend YAML config file |
| `system_prompt` | `string` | `""` | Direct system prompt text (takes precedence over `system_prompt_key`) |
| `system_prompt_key` | `string` | `""` | Key for loading prompt from promptio |
| `max_turns` | `int` | `0` (use agent default) | Max conversation turns |
| `max_tool_executions` | `int` | `0` (use agent default) | Max tool executions per conversation |
| `enable_tracing` | `bool` | `false` | Enable observability tracing |
| `patterns_dir` | `string` | `""` | Directory containing pattern YAML files |
| `llm` | `LLMConfig` | inherits server | Per-agent LLM override |

```yaml
# looms.yaml
agents:
  agents:
    sql-agent:
      name: SQL Query Agent
      description: Executes SQL queries against configured databases
      backend_path: ./backends/postgres.yaml
      system_prompt_key: agent.system.sql
      max_turns: 25
      max_tool_executions: 50
      enable_tracing: true
      patterns_dir: ./patterns/sql
      llm:
        provider: anthropic
        anthropic_model: claude-sonnet-4-5-20250929
```

---

## LLM Providers

Eight providers are supported. Provider name is the value of `llm.provider` (in agent YAML) or `llm.provider` (in `looms.yaml`).

| Provider | Config key | Auth | Default model |
|---|---|---|---|
| `anthropic` | `anthropic` | API key | `claude-sonnet-4-5-20250929` |
| `bedrock` | `bedrock` | AWS credentials or profile | `us.anthropic.claude-sonnet-4-5-20250929-v1:0` |
| `ollama` | `ollama` | None (local) | `llama3.1` |
| `openai` | `openai` | API key | `gpt-4.1` |
| `azure-openai` or `azureopenai` | `azure-openai` | API key or Entra token | (deployment-specific) |
| `mistral` | `mistral` | API key | `mistral-large-latest` |
| `gemini` | `gemini` | API key | `gemini-3-flash-preview` |
| `huggingface` | `huggingface` | Token | `meta-llama/Meta-Llama-3.1-70B-Instruct` |

In standalone agent YAML, set `provider` and `model` under `spec.llm`:

```yaml
spec:
  llm:
    provider: gemini
    model: gemini-2.5-flash
    temperature: 0.5
    max_tokens: 8192
```

In the server config (`looms.yaml`), provider-specific fields use flat keys under `llm:`:

```yaml
llm:
  provider: anthropic
  anthropic_model: claude-sonnet-4-5-20250929
  # anthropic_api_key: set via keyring (looms config set-key anthropic_api_key)
  temperature: 1.0
  max_tokens: 4096
  timeout_seconds: 60
```

**Server-level provider-specific fields** (from `LLMConfig` in `config.go`):

| Provider | Model field | Auth field(s) | Extra fields |
|---|---|---|---|
| anthropic | `anthropic_model` | `anthropic_api_key` | -- |
| bedrock | `bedrock_model_id` | `bedrock_access_key_id`, `bedrock_secret_access_key`, `bedrock_session_token` | `bedrock_region`, `bedrock_profile` |
| ollama | `ollama_model` | -- | `ollama_endpoint` |
| openai | `openai_model` | `openai_api_key` | -- |
| azure-openai | -- | `azure_openai_api_key`, `azure_openai_entra_token` | `azure_openai_endpoint`, `azure_openai_deployment_id` |
| mistral | `mistral_model` | `mistral_api_key` | -- |
| gemini | `gemini_model` | `gemini_api_key` | -- |
| huggingface | `huggingface_model` | `huggingface_token` | -- |

API keys and tokens should be stored in the system keyring:

```bash
looms config set-key anthropic_api_key
looms config set-key openai_api_key
looms config set-key gemini_api_key
looms config set-key mistral_api_key
looms config set-key huggingface_token
looms config set-key azure_openai_api_key
```

---

## Backend Types

Backend configuration is defined in standalone YAML files (referenced by `backend_path`).
Backend files use `apiVersion: loom/v1` and `kind: Backend`.

Supported backend types (from `pkg/fabric/config.go`):

| Type | Connection section | Description |
|---|---|---|
| `postgres` | `database:` | PostgreSQL database |
| `mysql` | `database:` | MySQL database |
| `sqlite` | `database:` | SQLite database |
| `file` | `database:` (DSN = base dir) | File system operations |
| `rest` | `rest:` | REST API |
| `graphql` | `graphql:` | GraphQL API |
| `grpc` | `grpc:` | gRPC service |
| `mcp` | `mcp:` | Model Context Protocol server |
| `supabase` | `supabase:` | Supabase project |

Example backend file:

```yaml
apiVersion: loom/v1
kind: Backend
name: analytics-db
description: Analytics PostgreSQL database
type: postgres

database:
  dsn: "postgres://${DB_USER}:${DB_PASS}@localhost:5432/analytics?sslmode=require"
  max_connections: 10
  connection_timeout_seconds: 30

schema_discovery:
  enabled: true
  cache_ttl_seconds: 3600

health_check:
  enabled: true
  interval_seconds: 30
  query: "SELECT 1"
```

---

## Environment Variable Expansion

Agent YAML files support `${VAR}` and `$VAR` syntax. Variables are expanded from the process
environment at load time via `os.Expand`.

```yaml
spec:
  memory:
    type: sqlite
    path: $LOOM_DATA_DIR/memory/my-agent.db
```

---

## Validation Rules

Enforced by `ValidateAgentConfig()` and `LoadConfigFromString()`:

1. `name` is required.
2. If `llm.provider` is set, `llm.model` must also be set (and vice versa).
3. `llm.provider` must be one of: `anthropic`, `bedrock`, `ollama`, `openai`, `azure-openai`, `azureopenai`, `mistral`, `gemini`, `huggingface`.
4. `llm.temperature` must be between 0.0 and 1.0.
5. `memory.type` (if set) must be one of: `memory`, `sqlite`, `postgres`.
6. Role-specific LLM configs (`judge_llm`, etc.) follow the same provider+model co-requirement.
7. All `int` fields are bounds-checked to fit `int32`.

---

## Examples

### Minimal Agent (Inherits Server LLM)

```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: simple-agent
  description: Minimal agent that inherits server LLM defaults
spec:
  system_prompt: |
    Answer questions accurately. Use tools when needed.
  config:
    max_turns: 15
```

### SQL Expert with Teradata ROM

```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: teradata-expert
  version: "1.0.0"
  description: Teradata SQL analyst with bound performance skills
  labels:
    backend: teradata
spec:
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
    temperature: 0.0
    max_tokens: 4096
    max_context_tokens: 200000
    reserved_output_tokens: 20000

  system_prompt: |
    Analyze Teradata databases using available tools.
    Write efficient SQL following Teradata best practices.
    Use EXPLAIN for query plan analysis.

  rom: "TD"

  tools:
    - shell_execute
    - tool_search

  memory:
    type: sqlite
    max_history: 1000
    memory_compression:
      workload_profile: data_intensive
    graph_memory:
      enabled: true
      context_budget_percent: 15

  config:
    max_turns: 25
    max_tool_executions: 50
    timeout_seconds: 300
    skills:
      enabled: true
      bindings:
        - name: "teradata/performance/*"
```

The bound skills appear in the system prompt as a name-and-description menu. The agent loads one with `manage_skills`, and loads any pattern the skill references with `load_pattern`.

### Multi-Provider Agent with Role Overrides

```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: multi-model-agent
  description: Uses different models for different roles
spec:
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  judge_llm:
    provider: gemini
    model: gemini-2.5-flash
  classifier_llm:
    provider: ollama
    model: llama3.1:8b
  compressor_llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929

  system_prompt: |
    Multi-purpose agent with specialized model routing.

  config:
    max_turns: 30
    max_tool_executions: 60
```

### Agent Using Named Provider Pool

```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: pool-agent
spec:
  active_provider: claude-opus
  allowed_providers:
    - claude-opus
    - llama-local

  system_prompt: |
    Use the assigned provider from the global pool.
  config:
    max_turns: 20
```

### Agent with Graph Memory Disabled

```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: stateless-agent
spec:
  system_prompt: |
    Stateless query agent. No episodic memory needed.
  memory:
    type: memory
    max_history: 20
    graph_memory:
      enabled: false
  config:
    max_turns: 10
```

---

## See Also

- [CLI Reference](./cli.md) -- `looms serve`, `looms config`, agent management commands
- [LLM Providers Reference](./llm-providers.md) -- per-provider details, models, auth
- [Backend Reference](./backend.md) -- backend YAML schema, connection types
- [Patterns Reference](./patterns.md) -- pattern YAML schema, effectiveness tracking
- Example agent configs: `examples/reference/agents/` (15+ working examples)
- Proto definition: `proto/loom/v1/agent_config.proto`
- Config loader source: `pkg/agent/config_loader.go`
- Server config source: `cmd/looms/config.go`

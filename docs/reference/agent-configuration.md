
# Agent Configuration Reference

Technical specification for Loom agent configuration. Covers both the **server-level** agent
definitions (in `looms.yaml`) and the **standalone agent YAML** files loaded by the server.

**Version**: v1.2.0
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
| **Progressively disclosed** (registered dynamically after triggering conditions) | `get_error_details` (after first error), `conversation_memory` (after first L2 swap), `session_memory` (after 3+ sessions), `query_tool_result` (after first large result), `graph_memory` (when graph memory store configured) |
| **Workflow-injected** (auto-added for workflow agents) | `send_message`, `publish`, `shared_memory_read`, `shared_memory_write`, `top_n_query`, `group_by_query` |

---

### Memory Configuration

Under `spec.memory` (k8s-style) or `agent.memory` (legacy).

| Field | Type | Default | Description |
|---|---|---|---|
| `type` | `string` | `"memory"` | `memory` (ephemeral), `sqlite`, or `postgres` |
| `path` | `string` | `""` | SQLite database file path |
| `dsn` | `string` | `""` | PostgreSQL connection string |
| `max_history` | `int` | `50` | Max conversation messages to retain |
| `shared_memory_threshold_bytes` | `int64` | `0` | 0 = always reference; -1 = never reference; >0 = reference if result exceeds N bytes |
| `max_tool_results` | `int` | `5` | Max tool results kept in conversation kernel |
| `memory_compression` | object | optional | See [Memory Compression](#memory-compression-configuration) |
| `graph_memory` | object | optional | See [Graph Memory](#graph-memory-configuration) |

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

Under `spec.memory.memory_compression`.

| Field | Type | Default | Description |
|---|---|---|---|
| `workload_profile` | `string` | `balanced` | `balanced`, `data_intensive`, or `conversational` |
| `max_l1_messages` | `int` | profile-dependent | Messages in L1 before compression triggers |
| `min_l1_messages` | `int` | max_l1 / 2 | Min messages after compression |
| `warning_threshold_percent` | `int` | profile-dependent | Warning threshold (0-100) |
| `critical_threshold_percent` | `int` | profile-dependent | Critical threshold (0-100) |
| `batch_sizes.normal` | `int` | profile-dependent | Messages per batch (normal) |
| `batch_sizes.warning` | `int` | profile-dependent | Messages per batch (warning) |
| `batch_sizes.critical` | `int` | profile-dependent | Messages per batch (critical) |

**Workload profiles**:

| Profile | max_l1 | warning | critical | batch (N/W/C) | Use case |
|---|---|---|---|---|---|
| `balanced` | 8 | 60% | 75% | 3/5/7 | General-purpose agents |
| `data_intensive` | 5 | 50% | 70% | 2/4/6 | SQL, large file operations |
| `conversational` | 12 | 70% | 85% | 4/6/8 | Chat-heavy, minimal tool usage |

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

Pattern-guided learning injects domain-specific templates into LLM context based on user intent classification.

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | `*bool` | `true` | Enable pattern injection |
| `min_confidence` | `*float64` | `0.75` | Minimum confidence threshold (0.0 - 1.0) |
| `max_patterns_per_turn` | `*int` | `1` | Max patterns injected per turn |
| `enable_tracking` | `*bool` | `true` | Track pattern effectiveness metrics |
| `use_llm_classifier` | `*bool` | `true` | Use LLM for intent classification (more accurate, adds ~300ms latency) |

```yaml
spec:
  config:
    patterns:
      enabled: true
      use_llm_classifier: true
      min_confidence: 0.80
      max_patterns_per_turn: 2
      enable_tracking: true
```

---

### Skills Configuration

Under `spec.config.skills` (k8s-style) or `agent.behavior.skills` (legacy).

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | `*bool` | not set | Enable skills system |
| `enabled_skills` | `[]string` | `[]` | Whitelist of skill names |
| `disabled_skills` | `[]string` | `[]` | Blacklist of skill names |
| `min_auto_confidence` | `*float64` | (framework default) | Min confidence for auto-invocation |
| `max_concurrent_skills` | `*int` | (framework default) | Max concurrent skill executions |
| `skills_dir` | `string` | `""` | Directory containing skill definitions |
| `context_budget_percent` | `*int` | (framework default) | % of context for skill output |

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
  description: Teradata SQL analyst with pattern-guided learning
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
    patterns:
      enabled: true
      use_llm_classifier: true
      min_confidence: 0.75
```

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

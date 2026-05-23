# Plugins

**Status:** ⚠️ Partial — proto and loader implemented; server-side `CapabilityService` implementation not yet started.

A **plugin** is the unit of invocation in Loom. One slash command or keyword activates everything inside it — workflows, skills, MCP tools, and agents — regardless of which combination is present. Plugins with no workflows work exactly the same way as plugins with workflows.

## Concepts

| Concept | Role |
|---|---|
| **Plugin** | Distribution + invocation bundle. Installed once, activated per session. |
| **Workflow** | One capability inside a plugin (optional). Has its own multi-agent orchestration. |
| **Skill** | Prompt-injection capability. Many plugins ship only skills + tools. |
| **ActivatePlugin** | The primary invocation RPC. Handles all component types. |
| **ActivateWorkflow** | Advanced/standalone use only. Prefer `ActivatePlugin`. |

## Lifecycle

```
RegisterPlugin → ValidatePlugin → InstallPlugin
                                       ↓
                              ActivatePlugin (per session)
                                       ↓
                              DeactivatePlugin
```

`InstallPlugin` registers referenced components (skills, agents, MCP servers) in their respective stores. After install, those components are independent entries — they don't disappear if the plugin is unregistered.

`UninstallPlugin` reverses the install: removes synthesized components scoped to this plugin, optionally removes skills/agents/MCP registrations.

## Plugin YAML

Plugins are authored as YAML files with `apiVersion: loom/v1` and `kind: Plugin`.

```yaml
apiVersion: loom/v1
kind: Plugin
metadata:
  name: teradata-optimizer        # required; kebab-case
  title: Teradata Query Optimizer # human-readable
  description: >                  # required; what this plugin does
    Analyze and optimize slow Teradata SQL queries using EXPLAIN
    plans and rule-based rewrites.
  version: "1.0.0"
  author: loom-core
  domains: [teradata, sql, performance]
  labels:
    surface: teradata

trigger:
  slash_commands: [/td-opt, /slow-query]  # what users type
  keywords: [slow query, optimize sql, explain plan, query performance]
  min_confidence: 0.72                    # for keyword auto-detection
  description: Analyze and optimize Teradata SQL query performance

workflows:                         # optional; leave empty for skills-only plugins
  - name: query-optimizer
    required: true
    description: End-to-end query optimization workflow

skills:
  - name: teradata-sql
    required: true
    description: SQL analysis and rewrite rules for Teradata
  - name: explain-plan
    required: false
    synthesize: true               # generate if missing at install time
    description: >
      Parse Teradata EXPLAIN output. Identify full-table scans, product
      joins, skewed AMPs, and missing statistics. Return a prioritized
      list of recommendations with estimated impact.

agents:
  - id: dba-agent
    role: primary
    required: false
    synthesize: true
    description: Database administrator for Teradata optimization tasks

mcp_tools:
  - tool_name: td_explain
    required: true
    description: Execute EXPLAIN on a Teradata SQL statement

install:
  auto_register_skills: true
  auto_configure_mcp: true
  create_default_agent: false

default_binding_mode: LAZY         # EAGER | LAZY | ALWAYS

resolution:
  on_required_missing: FAIL        # what to do at activation if a required ref is gone
  on_optional_missing: SKIP_WARN   # FAIL | SKIP_WARN | SKIP_SILENT
  resynthesize_on_activation: false
```

## Reference Failures

Each ref has `required: bool`. The `resolution` block controls what happens at **activation time** if a component was deleted after install.

| `required` | `synthesize` | component missing | result |
|---|---|---|---|
| `true` | `false` | yes | activation fails (per `on_required_missing`) |
| `true` | `true` | yes | runtime generates it; fails if generation fails |
| `false` | `true` | yes | runtime generates it; warns if generation fails |
| `false` | `false` | yes | skipped per `on_optional_missing` |

### Synthesis

`synthesize: true` on a `SkillRef` or `AgentRef` tells the runtime to auto-generate the component if it cannot be resolved:

- **Skill** — generates from `description` using the auto-generation pipeline. Marked `SKILL_STATUS_AUTO_GENERATED` with confidence 0.5. Scoped to the plugin (removed on uninstall).
- **Agent** — creates a minimal agent using `description` as the system prompt. Scoped to the plugin.
- **Workflow / MCP tool** — synthesis is not supported; these must exist.

`synthesize: true` requires a non-empty `description`. The loader rejects the plugin if this invariant is violated.

## Invocation

### User-facing (slash command / keyword)

The user types `/td-opt` or says "my query is slow" in chat. The agent's skill router detects the trigger and calls `ActivatePlugin` automatically. No knowledge of internal components required.

### Via agent config (persistent binding)

```yaml
# agents/my-agent.yaml
spec:
  plugin_ids:
    - teradata-optimizer   # activated at session start per default_binding_mode
```

The runtime calls `ActivatePlugin` at session start using the plugin's `default_binding_mode`:
- `LAZY` (default) — wires the trigger; activates when fired
- `EAGER` — activates immediately on session start
- `ALWAYS` — active on every turn, overrides trigger

### Direct API

```http
POST /v1/capabilities/plugins/teradata-optimizer/activate
{
  "session_id": "sess-abc",
  "agent_id":   "agent-xyz",
  "context":    {"database": "prod_warehouse"},
  "activation_key": "idem-key-123"
}
```

## Creating a Plugin

### With the Weaver (recommended)

Type `/plugin` or "create a plugin for X" in any session with the weaver active. The weaver:

1. Asks one clarifying question if intent is ambiguous
2. Discovers available skills, agents, and tools matching the intent
3. Identifies gaps and marks them `synthesize: true` with descriptions
4. Drafts the plugin YAML
5. Confirms with you before writing
6. Writes to `plugins/<name>.yaml`

Then install:

```bash
loom plugin install <name>
loom plugin activate <name>   # activate in your current session
```

### Manually

Write the YAML following the schema above and place it in the `plugins/` directory (or a custom path configured at server startup). Then:

```bash
# Register without installing
loom plugin register plugins/my-plugin.yaml

# Validate references
loom plugin validate my-plugin

# Install (registers skills, configures MCP, optionally creates default agent)
loom plugin install my-plugin

# List all plugins
loom plugin list

# Read details
loom plugin get my-plugin
```

## API Reference

All plugin operations go through `CapabilityService` (gRPC) or the REST gateway:

| Operation | gRPC | REST |
|---|---|---|
| Create | `RegisterPlugin` | `POST /v1/capabilities/plugins` |
| Read | `GetPlugin` | `GET /v1/capabilities/plugins/{plugin_id}` |
| List | `ListPlugins` | `GET /v1/capabilities/plugins` |
| Validate | `ValidatePlugin` | `POST /v1/capabilities/plugins/{id}/validate` |
| Install | `InstallPlugin` | `POST /v1/capabilities/plugins/{id}/install` |
| Uninstall | `UninstallPlugin` | `POST /v1/capabilities/plugins/{id}/uninstall` |
| Activate | `ActivatePlugin` | `POST /v1/capabilities/plugins/{id}/activate` |
| Deactivate | `DeactivatePlugin` | `POST /v1/capabilities/plugins/{id}/deactivate` |
| Activation state | `GetPluginActivation` | `GET /v1/capabilities/plugins/{id}/activations/{session_id}` |
| List activations | `ListPluginActivations` | `GET /v1/capabilities/plugins/activations` |

## Implementation Status

| Component | Status |
|---|---|
| `proto/loom/v1/workflow.proto` — plugin messages + `CapabilityService` RPCs | ✅ Done |
| `proto/loom/v1/permissions.proto` — `PermissionMode` enum | ✅ Done |
| `pkg/plugins` — YAML loader + types | ✅ Done |
| `embedded/skills/weaver-plugin.yaml` — weaver skill | ✅ Done |
| `CapabilityService` server implementation | 📋 Planned |
| `loom plugin` CLI subcommands | 📋 Planned |
| Synthesis pipeline (auto-generate skills/agents at install) | 📋 Planned |
| Plugin hot-reload from `plugins/` directory | 📋 Planned |

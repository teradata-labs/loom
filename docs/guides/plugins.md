# Plugins Guide

**Status:** ⚠️ Partial — proto and YAML loader implemented; `CapabilityService` server, CLI subcommands, and synthesis pipeline are 📋 Planned.

A **plugin** is the unit of invocation in Loom. One slash command or keyword activates everything inside it — workflows, skills, MCP tools, and agents — regardless of which combination is present. Plugins with no workflows work exactly the same way as plugins that have them.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Concepts](#concepts)
- [Lifecycle](#lifecycle)
- [Plugin YAML Schema](#plugin-yaml-schema)
- [Common Tasks](#common-tasks)
  - [Create a plugin with the Weaver](#create-a-plugin-with-the-weaver-recommended)
  - [Create a plugin manually](#create-a-plugin-manually)
  - [List plugins](#list-plugins)
  - [Read plugin details](#read-plugin-details)
  - [Install a plugin](#install-a-plugin)
  - [Activate a plugin](#activate-a-plugin)
  - [Bind a plugin to an agent](#bind-a-plugin-to-an-agent)
- [Reference Failures and Synthesis](#reference-failures-and-synthesis)
- [Governance and Risk](#governance-and-risk)
- [MCP and Claude Integration](#mcp-and-claude-integration)
- [API Reference](#api-reference)
- [Troubleshooting](#troubleshooting)
- [Implementation Status](#implementation-status)
- [Next Steps](#next-steps)

---

## Prerequisites

- Loom v1.2.0+
- A running `looms` server with at least one agent configured
- For keyword-based activation: the agent must have the skill router enabled (`skills.router_enabled: true` in agent config)
- For MCP tool refs: the relevant MCP servers must be reachable at install time

> **Note:** `CapabilityService` (the gRPC server that backs plugin CRUD and activation) is not yet implemented. The YAML format, loader, and proto definitions are complete and stable. CLI commands shown below reflect the planned interface.

---

## Quick Start

```bash
# 1. Start a session with the weaver active
looms chat --agent weaver

# 2. Ask it to create a plugin
/plugin I want to analyze slow Teradata queries

# 3. The weaver discovers capabilities, drafts YAML, asks for confirmation,
#    then writes plugins/teradata-optimizer.yaml

# 4. Install and activate
loom plugin install teradata-optimizer
loom plugin activate teradata-optimizer
```

Expected output from step 4:

```
✅ Installed: teradata-optimizer v1.0.0
   Skills registered:  teradata-sql, explain-plan (synthesized)
   MCP tools wired:    td_explain
   Workflows linked:   query-optimizer

✅ Activated: teradata-optimizer (session sess-abc)
   Trigger: /td-opt, /slow-query
   Type /td-opt or mention "slow query" to invoke.
```

---

## Concepts

| Concept | Role |
|---|---|
| **Plugin** | Distribution + invocation bundle. Installed once, activated per session. |
| **Workflow** | One capability inside a plugin (optional). Has its own multi-agent orchestration. |
| **Skill** | Prompt-injection capability. Many plugins ship only skills + tools — no workflow needed. |
| **ActivatePlugin** | The primary invocation RPC. Handles all component types uniformly. |
| **ActivateWorkflow** | Advanced use only. Prefer `ActivatePlugin` for plugin-delivered workflows. |
| **Synthesis** | Auto-generating a missing skill or agent at install/activation time from its `description`. |

---

## Lifecycle

```
RegisterPlugin → ValidatePlugin → InstallPlugin
                                       ↓
                              ActivatePlugin  ←──── user trigger
                              (per session)          or agent binding
                                       ↓
                              DeactivatePlugin
```

`InstallPlugin` registers referenced components (skills, agents, MCP servers) in their respective stores. After install, those components are independent entries — they persist even if the plugin is later unregistered.

`UninstallPlugin` reverses the install: removes components that were synthesized and scoped to this plugin, and optionally removes explicitly referenced skills, agents, and MCP registrations.

---

## Plugin YAML Schema

Plugins are authored as YAML files with `apiVersion: loom/v1` and `kind: Plugin`. Place them in the `plugins/` directory in your Loom data directory.

```yaml
apiVersion: loom/v1
kind: Plugin
metadata:
  name: teradata-optimizer        # required; must be kebab-case
  title: Teradata Query Optimizer # human-readable display name
  description: >                  # required; plain-English description
    Analyze and optimize slow Teradata SQL queries using EXPLAIN
    plans and rule-based rewrites.
  version: "1.0.0"
  author: your-name
  domains: [teradata, sql, performance]
  labels:
    surface: teradata             # arbitrary key-value tags for filtering
  type: domain                   # domain|integration|meta|analysis|utility
  risk_level: low                # low|medium|high|restricted (default: unset)
  require_approval: false        # if true, blocks activation for high/restricted

trigger:
  slash_commands: [/td-opt, /slow-query]  # what users type to invoke
  keywords:                               # auto-detected from user messages
    - slow query
    - optimize sql
    - explain plan
    - query performance
  min_confidence: 0.72           # keyword detection threshold (default: 0.7)
  description: Analyze and optimize Teradata SQL query performance

workflows:                        # optional — leave empty for skills-only plugins
  - name: query-optimizer
    required: true                # true = activation fails if missing
    description: End-to-end query optimization workflow

skills:
  - name: teradata-sql
    required: true
    description: SQL analysis and rewrite rules for Teradata
  - name: explain-plan
    required: false
    synthesize: true              # auto-generate if missing at install time
    description: >
      Parse Teradata EXPLAIN output. Identify full-table scans, product
      joins, skewed AMPs, and missing statistics. Return a prioritized
      list of recommendations with estimated impact.

agents:
  - id: dba-agent
    role: primary
    required: false
    synthesize: true              # create a minimal agent if missing
    description: Database administrator for Teradata optimization tasks

mcp_tools:
  - tool_name: td_explain
    required: true
    description: Execute EXPLAIN on a Teradata SQL statement
    # server_name is optional; runtime discovers the server automatically

install:
  auto_register_skills: true     # register skill refs into the skill registry
  auto_configure_mcp: true       # register and connect MCP servers
  create_default_agent: false    # create an agent from the first agent_ref

default_binding_mode: LAZY       # EAGER | LAZY (default) | ALWAYS

resolution:
  on_required_missing: FAIL      # what to do at activation if a required ref disappeared
  on_optional_missing: SKIP_WARN # FAIL | SKIP_WARN (default) | SKIP_SILENT
  resynthesize_on_activation: false
```

### Required fields

| Field | Rule |
|---|---|
| `metadata.name` | Required. Must be kebab-case (`[a-z][a-z0-9-]*`). |
| `metadata.description` | Required. |
| At least one ref | `workflows`, `skills`, `agents`, or `mcp_tools` must be non-empty. |
| `description` on synthesize refs | Required when `synthesize: true`. |

### Optional governance fields

| Field | Values | Default |
|---|---|---|
| `metadata.type` | `domain` `integration` `meta` `analysis` `utility` | unset |
| `metadata.risk_level` | `low` `medium` `high` `restricted` | unset |
| `metadata.require_approval` | `true` / `false` | `false` |

`type` is validated at parse time — an unrecognised value is a loader error. `risk_level` is free-form and case-insensitive; the activation gate treats `high` and `restricted` as blocked when `require_approval: true`.

---

## Common Tasks

### Create a plugin with the Weaver (recommended)

The weaver's `/plugin` skill discovers what's available, matches it to your intent, flags gaps as `synthesize: true`, and writes the YAML after confirmation.

```bash
# In any session with the weaver active:
/plugin I want to monitor data quality in our Teradata warehouse daily
```

The weaver will:
1. Ask one clarifying question if the intent is ambiguous
2. Call `agent_management discover` to find matching skills and agents
3. Call `tool_search` to find relevant MCP tools
4. Draft the plugin YAML with correct `required` and `synthesize` flags
5. Show you the full YAML and ask for confirmation
6. Write `plugins/<name>.yaml` after approval

```
Weaver: Here's what I found and what I'll synthesize:

  ✅ exists:     data-quality-auditor workflow
  ✅ exists:     teradata-sql skill
  ✅ exists:     td_rowcount MCP tool
  🔧 synthesize: dq-reporter skill (doesn't exist yet)

Confirm this plugin YAML? [y/n]
```

### Create a plugin manually

Write the YAML following the schema above and place it in `plugins/`:

```bash
# $LOOM_DATA_DIR defaults to ~/.loom
cat > $LOOM_DATA_DIR/plugins/my-plugin.yaml << 'EOF'
apiVersion: loom/v1
kind: Plugin
metadata:
  name: my-plugin
  description: What this plugin does.
trigger:
  slash_commands: [/my-plugin]
  keywords: [my plugin, invoke my plugin]
skills:
  - name: my-skill
    required: true
    description: The core skill for this plugin.
EOF
```

### List plugins

```bash
loom plugin list
```

Expected output:

```
NAME                    VERSION  STATUS      TYPE         RISK    TRIGGER
teradata-optimizer      1.0.0    INSTALLED   domain       low     /td-opt, /slow-query
data-quality-monitor    1.1.0    INSTALLED   domain       low     /dq-check
my-plugin               1.0.0    REGISTERED  —            —       /my-plugin

3 plugins (2 installed, 1 registered)
```

Filter by status, domain, or type:

```bash
loom plugin list --status installed
loom plugin list --domain teradata
loom plugin list --type integration
```

### Read plugin details

```bash
loom plugin get teradata-optimizer
```

Expected output:

```
Plugin: teradata-optimizer v1.0.0
Status: INSTALLED
Type:   domain
Risk:   low  (require_approval: false)
Description: Analyze and optimize slow Teradata SQL queries using EXPLAIN plans and rule-based rewrites.
Domains: teradata, sql, performance

Trigger:
  Slash commands: /td-opt, /slow-query
  Keywords:       slow query, optimize sql, explain plan, query performance
  Min confidence: 0.72

Components:
  Workflows (1):
    ✅ query-optimizer         required
  Skills (2):
    ✅ teradata-sql            required
    🔧 explain-plan            optional, synthesized (confidence: 0.50)
  Agents (1):
    🔧 dba-agent               optional, synthesized
  MCP Tools (1):
    ✅ td_explain              required

Binding mode: LAZY
Resolution:    required-missing→FAIL, optional-missing→SKIP_WARN
```

### Validate a plugin

Check that all `required: true` refs resolve before installing:

```bash
loom plugin validate teradata-optimizer
```

Expected output (all clear):

```
✅ teradata-optimizer: valid
   Resolved 1/1 workflows, 1/2 skills, 0/1 agents, 1/1 mcp_tools
   ⚠️  explain-plan: not found — will be synthesized at install
   ⚠️  dba-agent: not found — will be synthesized at install
```

Expected output (hard failure):

```
❌ teradata-optimizer: invalid
   ERROR: query-optimizer workflow not found and required=true
   Fix: register the workflow first, or set required: false
```

### Install a plugin

```bash
loom plugin install teradata-optimizer
```

Expected output:

```
Installing teradata-optimizer v1.0.0...
  ✅ Skill registered:    teradata-sql
  🔧 Skill synthesized:   explain-plan (AUTO_GENERATED, confidence 0.50)
  🔧 Agent synthesized:   dba-agent
  ✅ MCP server wired:    td-mcp-server (provides td_explain)
  ✅ Workflow linked:     query-optimizer

✅ Installed successfully (4 components, 2 synthesized)
```

Force-overwrite existing components:

```bash
loom plugin install teradata-optimizer --overwrite
```

### Activate a plugin

```bash
# Activate for your current session
loom plugin activate teradata-optimizer

# Activate for a specific session
loom plugin activate teradata-optimizer --session sess-abc
```

Expected output:

```
✅ Activated: teradata-optimizer
   Skills injected:  teradata-sql, explain-plan
   MCP tools:        td_explain
   Workflow active:  query-optimizer (trigger: /td-opt, /slow-query)
```

After activation, the user can type `/td-opt` or mention "slow query" to invoke the plugin's capabilities.

### Bind a plugin to an agent

To have a plugin activate automatically at session start, add it to the agent's config:

```yaml
# agents/td-analyst.yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: td-analyst
spec:
  system_prompt: Answer questions about our Teradata data warehouse.
  plugin_ids:
    - teradata-optimizer   # activated per its default_binding_mode (LAZY)
    - data-quality-monitor # activated per its default_binding_mode
```

Binding modes:

| Mode | Behaviour |
|---|---|
| `LAZY` (default) | Wires the trigger at session start. Plugin activates when the trigger fires. |
| `EAGER` | Activates immediately when the session starts — no trigger needed. |
| `ALWAYS` | Forces the plugin active on every turn. Use for essential infrastructure only. |

---

## Reference Failures and Synthesis

### At activation time

If a component existed at install but was later deleted, the `resolution` block controls what happens:

| `required` | `synthesize` | component missing | result |
|---|---|---|---|
| `true` | `false` | yes | activation fails (`on_required_missing` policy) |
| `true` | `true` | yes | runtime re-generates it; fails if generation fails |
| `false` | `true` | yes | runtime re-generates it; warns if generation fails |
| `false` | `false` | yes | skipped (`on_optional_missing` policy) |

### Synthesis rules

`synthesize: true` auto-generates the component from its `description`:

- **Skill** — generated via the auto-generation pipeline. Marked `SKILL_STATUS_AUTO_GENERATED`, confidence 0.5. Scoped to the plugin (removed on `UninstallPlugin`).
- **Agent** — minimal agent created with `description` as the system prompt. Scoped to the plugin.
- **Workflow** — synthesis not supported. Must exist before install.
- **MCP tool** — synthesis not supported. The server must be registered.

**Good synthesis description** (specific, describes inputs/outputs/constraints):

```yaml
- name: explain-plan
  synthesize: true
  description: >
    Parse Teradata EXPLAIN output. Identify full-table scans, product joins,
    skewed AMPs, and missing statistics. Return a prioritized list of
    optimization recommendations with the estimated row count and AMP impact
    for each issue.
```

**Bad synthesis description** (too vague, will produce a low-quality skill):

```yaml
- name: explain-plan
  synthesize: true
  description: Analyzes queries.   # ← too vague
```

---

## Governance and Risk

### Risk levels

`risk_level` classifies the combined capability surface of the plugin — independent of its individual component risk levels. A plugin that bundles three individually low-risk components (read schema, format as CSV, send email) may represent a higher combined risk than any component alone.

| `risk_level` | Meaning |
|---|---|
| `low` | Read-only or informational capabilities. No side effects. |
| `medium` | Writes to internal systems or modifies agent state. |
| `high` | Writes to external systems, executes code, or accesses sensitive data. |
| `restricted` | Restricted to explicitly approved operators. Blocked by default. |

### Approval gate

When `require_approval: true`, `ActivatePlugin` is blocked for `high` and `restricted` plugins unless the caller holds explicit approval. This mirrors the skill-level gate already in the agent runtime.

```yaml
metadata:
  risk_level: high
  require_approval: true   # activation blocked until an operator approves
```

Plugins with `risk_level: low` or `medium` and no `require_approval` activate without a gate.

### Component risk still applies

Setting a low `risk_level` on the plugin does not override component-level risk. If a bundled skill has `risk_level: high`, the skill gate in the agent runtime still applies when that skill activates. Plugin governance is additive.

---

## MCP and Claude Integration

### Loom as an MCP consumer

Plugins reference MCP tools via `mcp_tools`. Any MCP server — Claude Desktop extensions, VS Code tools, custom servers — can be wired into a Loom plugin and used by its workflows and skills.

```yaml
mcp_tools:
  - tool_name: github_search    # from a GitHub MCP server
    required: true
  - tool_name: github_pr_read
    required: true
```

Use `type: integration` for plugins whose primary purpose is wrapping an external MCP server:

```yaml
metadata:
  name: github-tools
  type: integration
  description: Wraps the GitHub MCP server with Loom skills for code review workflows.
```

### Loom as an MCP server

`loom-mcp` is a binary that exposes the entire Loom capability surface as an MCP server. Claude Desktop, VS Code, and any MCP client can add it as a tool provider:

```json
// Claude Desktop config
{
  "mcpServers": {
    "loom": {
      "command": "/usr/local/bin/loom-mcp",
      "args": ["--grpc-addr", "localhost:60051"]
    }
  }
}
```

From the MCP client's perspective, all Loom workflows and skills are available as tools. From Loom's perspective, the client is just another agent calling `ActivatePlugin` and `Weave`.

### Relationship summary

| | MCP / Claude plugin | Loom plugin |
|---|---|---|
| **Abstraction** | Protocol — defines how tools are called | Bundle — packages capabilities together |
| **Unit** | A server exposing named tools | Workflows + skills + MCP tools + agents under one trigger |
| **Orchestration** | None | Workflows, multi-agent, task decomposition |
| **Authoring** | Implement a server (code) | Write YAML, or use `/plugin` in the weaver |

They are complementary: a Loom plugin can consume MCP tools from any Claude plugin, and Loom itself is available as a Claude plugin via `loom-mcp`.

---

## API Reference

All plugin operations go through `CapabilityService` (gRPC) or its REST gateway:

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

### ActivatePlugin request

```json
POST /v1/capabilities/plugins/teradata-optimizer/activate
{
  "session_id":     "sess-abc",
  "agent_id":       "agent-xyz",
  "context":        { "database": "prod_warehouse" },
  "activation_key": "idem-key-123",
  "dry_run":        false,
  "persona":        "DBA"
}
```

`activation_key` makes the call idempotent — a second call with the same key returns the existing activation without re-wiring components.

`dry_run: true` validates and plans the activation without executing it.

---

## Troubleshooting

### `synthesize=true requires description`

The loader rejects a ref that has `synthesize: true` but an empty `description`. Add a 2–4 sentence description explaining what the skill or agent should do, its inputs, outputs, and any constraints.

### Plugin activates but skills aren't injected

Check that `install.auto_register_skills: true` was set and `loom plugin install` was run before `activate`. Skills are not registered at activate time — only at install time.

### `required ref missing` at activation

A `required: true` component was deleted after install. Options:
1. Re-register the missing component and re-run `loom plugin install`.
2. Change `required: false` and add `synthesize: true` with a description if the component can be auto-generated.
3. Set `resolution.on_required_missing: SKIP_WARN` to activate with degraded capability.

### Plugin trigger not firing

- Check `loom plugin list` to confirm the plugin is `INSTALLED` (not just `REGISTERED`).
- Check `loom plugin get <name>` to confirm the session has an active activation.
- Verify the agent has `skills.router_enabled: true` in its config — keyword detection requires the router.
- For slash commands: confirm the exact command matches (e.g. `/td-opt` not `/tdopt`).

### `activation blocked: require_approval=true`

The plugin has `risk_level: high` or `restricted` and `require_approval: true`. An operator must explicitly approve the plugin before it can be activated. To test locally before getting approval, temporarily set `require_approval: false` in the YAML and re-install.

### `metadata.type "widget" is not valid`

`type` must be one of: `domain`, `integration`, `meta`, `analysis`, `utility`. Remove the field entirely if the plugin doesn't fit a category — it is optional.

### Weaver writes YAML but `loom plugin install` fails

The weaver writes to `plugins/<name>.yaml` relative to the working directory. The server looks for plugins in `$LOOM_DATA_DIR/plugins/`. Move the file:

```bash
mv plugins/my-plugin.yaml $LOOM_DATA_DIR/plugins/
loom plugin install my-plugin
```

---

## Implementation Status

| Component | Status |
|---|---|
| `proto/loom/v1/workflow.proto` — all plugin messages + `CapabilityService` RPCs | ✅ Done |
| `proto/loom/v1/permissions.proto` — `PermissionMode` enum (used by workflows) | ✅ Done |
| `proto/loom/v1/agent_config.proto` — `AgentConfig.plugin_ids` field | ✅ Done |
| `pkg/plugins` — YAML loader, types, round-trip serialization | ✅ Done |
| `pkg/plugins` — `type`, `risk_level`, `require_approval` + `IsHighRisk()` | ✅ Done |
| `embedded/skills/weaver-plugin.yaml` — `/plugin` weaver skill | ✅ Done |
| `cmd/loom-mcp` — exposes Loom as an MCP server for Claude Desktop / VS Code | ✅ Done |
| `CapabilityService` server implementation | 📋 Planned |
| `loom plugin` CLI subcommands | 📋 Planned |
| Approval gate wired into `ActivatePlugin` for high/restricted plugins | 📋 Planned |
| Synthesis pipeline (auto-generate skills/agents at install) | 📋 Planned |
| Plugin hot-reload from `plugins/` directory | 📋 Planned |
| Plugin activation wired into agent session start | 📋 Planned |

---

## Next Steps

- [Skills Guide](skills.md) — how skills work and how to author them; relevant because many plugins are skills-only
- [Workflows Guide](workflows.md) — how to author workflows that plugins can reference
- [Weaver Guide](meta-agent-usage.md) — the meta-agent that creates plugins, agents, and workflows
- [Agent Configuration Reference](../reference/) — full `AgentConfig` spec including `plugin_ids`


# Prompt Migration Guide

**Version**: v1.2.0

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Migration Steps](#migration-steps)
  - [Step 1: Create Prompts Directory](#step-1-create-prompts-directory)
  - [Step 2: Export Prompts to YAML](#step-2-export-prompts-to-yaml)
  - [Step 3: Configure looms.yaml](#step-3-configure-loomsyaml)
  - [Step 4: Test and Verify](#step-4-test-and-verify)
- [YAML Format Reference](#yaml-format-reference)
- [Variable Interpolation](#variable-interpolation)
- [A/B Testing with Variants](#ab-testing-with-variants)
- [Hot-Reload with Watch](#hot-reload-with-watch)
- [Caching](#caching)
- [Examples](#examples)
  - [Example 1: Basic Agent Prompt](#example-1-basic-agent-prompt)
  - [Example 2: Prompt with Variables and Variants](#example-2-prompt-with-variables-and-variants)
- [Troubleshooting](#troubleshooting)


## Overview

✅ **Available** -- All features described in this guide are implemented and tested.

Migrate from inline prompts to the `FileRegistry` system for centralized prompt management, hot-reload via `Watch()`, and A/B testing with variant selectors.


## Prerequisites

- Loom v1.2.0+
- Server running: `looms serve`
- Existing agent with inline prompts


## Quick Start

```bash
# Create prompts directory
mkdir -p prompts/agent

# Create a prompt YAML file using frontmatter format
cat > prompts/agent/system.yaml << 'EOF'
---
key: agent.system
version: 1.0.0
author: you@example.com
description: Base system prompt for agents
tags: [agent, system]
variants: [default]
variables: [backend_type]
---
Help users write {{.backend_type}} queries accurately and efficiently.
EOF

# Update looms.yaml
cat >> $LOOM_DATA_DIR/looms.yaml << 'EOF'
prompts:
  source: file
  file_dir: ./prompts
EOF

# Restart server
looms serve
```


## Migration Steps

### Step 1: Create Prompts Directory

```bash
mkdir -p prompts/{agent,tools,guidance,errors}
```

Directory structure:
```
prompts/
+-- agent/                     # Agent system prompts
|   +-- system.yaml            # Key: "agent.system", variant: "default"
|   +-- system.concise.yaml    # Key: "agent.system", variant: "concise"
+-- tools/                     # Tool descriptions
|   +-- execute_sql.yaml
+-- guidance/                  # Self-correction messages
|   +-- self_correction.yaml
+-- errors/                    # Error messages
    +-- validation.yaml
```

Keys are derived from the `key` field in the YAML frontmatter metadata. Variants are derived from the filename: `system.yaml` maps to the "default" variant, `system.concise.yaml` maps to the "concise" variant.

### Step 2: Export Prompts to YAML

Convert inline prompts to YAML files using the frontmatter format:

```yaml
# prompts/agent/system.yaml
---
key: agent.system
version: 1.0.0
author: you@example.com
description: Base system prompt for agents
tags: [agent, system]
variants: [default]
variables: [backend_type, tool_count]
---
Help users interact with {{.backend_type}} using {{.tool_count}} tools.
```

The file has two sections separated by `---`:

1. **Metadata block** (between the two `---` lines): YAML with flat fields
2. **Content block** (after the second `---`): The prompt text with `{{.variable}}` placeholders

Variables are a flat list of string names. There are no type codes -- all values are passed as `map[string]interface{}` at runtime and converted to strings during interpolation.

### Step 3: Configure looms.yaml

Add prompts configuration:

```yaml
# looms.yaml
prompts:
  source: file
  file_dir: ./prompts

agents:
  agents:
    sql-agent:
      name: SQL Agent
      system_prompt_key: agent.system
      max_turns: 25
```

### Step 4: Test and Verify

Start server and verify prompts load:

```bash
looms serve --config looms.yaml
```

Expected logs:
```
INFO  Prompts configuration  source=file file_dir=./prompts
INFO  Using FileRegistry for prompts  dir=./prompts
INFO  Loaded agents from looms.yaml  count=1
```


## YAML Format Reference

Every prompt YAML file uses frontmatter format with `---` separators:

```yaml
---
key: agent.system.base
version: 2.1.0
author: developer@example.com
description: Base system prompt for SQL agents
tags: [agent, system, sql]
variants: [default, concise, verbose]
variables: [backend_type, session_id, cost_threshold]
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-17T00:00:00Z
---
Assist with {{.backend_type}} queries and operations.
Session: {{.session_id}}
Cost threshold: ${{.cost_threshold}}
```

### Metadata Fields

| Field | Type | Description |
|---|---|---|
| `key` | string | Unique identifier (e.g., `agent.system.base`) |
| `version` | string | Semantic version (e.g., `2.1.0`) |
| `author` | string | Author email or username (optional) |
| `description` | string | What this prompt does |
| `tags` | []string | Categorization tags for filtering |
| `variants` | []string | Available variant names for A/B testing |
| `variables` | []string | Variable names used in the content |
| `created_at` | timestamp | Creation timestamp (optional) |
| `updated_at` | timestamp | Last update timestamp (optional) |


## Variable Interpolation

Variables use `{{.variable_name}}` syntax. All values are escaped to prevent prompt injection attacks.

```go
registry := prompts.NewFileRegistry("./prompts")
if err := registry.Reload(ctx); err != nil {
    log.Fatal(err)
}

vars := map[string]interface{}{
    "backend_type":   "PostgreSQL",
    "session_id":     "sess-abc123",
    "cost_threshold": 50.00,
}

prompt, err := registry.Get(ctx, "agent.system.base", vars)
```

Supported value types:
- `string` -- escaped for injection prevention (control characters removed, HTML entities escaped)
- `int`, `int64`, `int32`, `float64`, `float32` -- formatted as-is
- `bool` -- formatted as `true` or `false`
- `[]string` -- joined with commas, each element escaped

If a variable placeholder exists in the template but is not provided in `vars`, the placeholder is left as-is (e.g., `{{.missing_var}}` stays in the output).


## A/B Testing with Variants

✅ **Available** -- Variant selectors and ABTestingRegistry are implemented and tested.

### Variant Files

Variants are determined by filename:

- `system.yaml` --> variant `"default"`
- `system.concise.yaml` --> variant `"concise"`
- `system.verbose.yaml` --> variant `"verbose"`

Each variant file is a complete YAML file with its own frontmatter and content. Multiple variant files share the same `key` value but have different content.

### Variant Selectors

Four selector strategies are available:

```go
// Explicit: always returns a specific variant
selector := prompts.NewExplicitSelector("concise")

// Hash-based: deterministic based on session ID (consistent per session)
selector := prompts.NewHashSelector()

// Random: uniform random selection
selector := prompts.NewRandomSelector(seed) // pass 0 for random seed

// Weighted: weighted random (weights are relative, don't need to sum to 100)
selector := prompts.NewWeightedSelector(map[string]int{
    "default":      80,
    "experimental": 20,
}, seed) // pass 0 for random seed
```

### ABTestingRegistry

Wrap a `FileRegistry` with automatic variant selection:

```go
fileRegistry := prompts.NewFileRegistry("./prompts")
if err := fileRegistry.Reload(ctx); err != nil {
    log.Fatal(err)
}

selector := prompts.NewHashSelector()
abRegistry := prompts.NewABTestingRegistry(fileRegistry, selector)

// Automatically selects variant based on session ID in context
ctx = prompts.WithSessionID(ctx, "sess-123")
prompt, err := abRegistry.Get(ctx, "agent.system", vars)

// Or specify session ID directly
prompt, err = abRegistry.GetForSession(ctx, "agent.system", "sess-123", vars)

// Bypass selector and request a specific variant
prompt, err = abRegistry.GetWithVariant(ctx, "agent.system", "concise", vars)
```


## Hot-Reload with Watch

✅ `Watch()` uses `fsnotify` to monitor the prompts directory and all subdirectories for file changes, automatically reloading prompts when YAML files are created, modified, or deleted.

```go
registry := prompts.NewFileRegistry("./prompts")
if err := registry.Reload(ctx); err != nil {
    log.Fatal(err)
}

// Start watching for changes
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

updates, err := registry.Watch(ctx)
if err != nil {
    log.Fatal(err)
}

// Process updates
for update := range updates {
    if update.Error != nil {
        log.Printf("Watch error: %v", update.Error)
        continue
    }
    log.Printf("Prompt %s was %s at %v", update.Key, update.Action, update.Timestamp)
}
```

The `PromptUpdate` struct contains:
- `Key` -- the prompt key that changed
- `Version` -- the prompt version (from metadata)
- `Action` -- one of `"created"`, `"modified"`, `"deleted"`, `"error"`
- `Timestamp` -- when the change was detected
- `Error` -- set when `Action` is `"error"`

When a file changes, `Watch()` automatically calls `Reload()` to refresh all prompts from disk before sending the update notification.


## Caching

✅ **Available** -- TTL-based caching with hit/miss statistics and manual invalidation.

Wrap any `PromptRegistry` with a TTL cache to reduce file I/O:

```go
fileRegistry := prompts.NewFileRegistry("./prompts")
if err := fileRegistry.Reload(ctx); err != nil {
    log.Fatal(err)
}

cachedRegistry := prompts.NewCachedRegistry(fileRegistry, 5*time.Minute)

// First call: cache miss, reads from file
prompt1, _ := cachedRegistry.Get(ctx, "agent.system", vars)

// Second call: cache hit
prompt2, _ := cachedRegistry.Get(ctx, "agent.system", vars)

// Check cache statistics
hits, misses := cachedRegistry.Stats()
fmt.Printf("Hit rate: %.2f%%\n", float64(hits)/float64(hits+misses)*100)

// Manually invalidate
cachedRegistry.InvalidateKey("agent.system")  // One key
cachedRegistry.Invalidate()                   // Entire cache
```

The cache stores raw content (before variable interpolation), so different `vars` for the same key still benefit from cache hits.


## Examples

### Example 1: Basic Agent Prompt

**Before** (inline in looms.yaml):
```yaml
agents:
  agents:
    my-agent:
      system_prompt: "Help users with their tasks."
```

**After** (prompts/agent/system.yaml):
```yaml
---
key: agent.system
version: 1.0.0
author: developer@example.com
description: Base system prompt
tags: [agent, system]
variants: [default]
variables: []
---
Help users with their tasks.
```

**Updated looms.yaml**:
```yaml
prompts:
  source: file
  file_dir: ./prompts

agents:
  agents:
    my-agent:
      system_prompt_key: agent.system
```

### Example 2: Prompt with Variables and Variants

Create two files for the same prompt key -- one default, one concise:

**prompts/agent/system.yaml** (default variant):
```yaml
---
key: agent.system
version: 1.0.0
author: developer@example.com
description: System prompt with backend context
tags: [agent, system]
variants: [default, concise]
variables: [backend_type, session_id]
---
Help users interact with {{.backend_type}} for session {{.session_id}}.

Guidelines:
- Never fabricate data - only report what tools return
- If a tool fails, admit failure rather than guessing
- Provide clear explanations of your reasoning
```

**prompts/agent/system.concise.yaml** (concise variant):
```yaml
---
key: agent.system
version: 1.0.0
author: developer@example.com
description: Concise system prompt
tags: [agent, system]
variants: [default, concise]
variables: [backend_type, session_id]
---
{{.backend_type}} agent. Session: {{.session_id}}. Be accurate and concise.
```

**Usage in Go:**
```go
registry := prompts.NewFileRegistry("./prompts")
if err := registry.Reload(ctx); err != nil {
    log.Fatal(err)
}

vars := map[string]interface{}{
    "backend_type": "Teradata",
    "session_id":   "sess-123",
}

// Get default variant
defaultPrompt, _ := registry.Get(ctx, "agent.system", vars)
// Returns: "Help users interact with Teradata for session sess-123.\n\nGuidelines:\n..."

// Get concise variant
concisePrompt, _ := registry.GetWithVariant(ctx, "agent.system", "concise", vars)
// Returns: "Teradata agent. Session: sess-123. Be accurate and concise."
```


## Troubleshooting

### Prompt Not Found

**Error:**
```
prompt not found: agent.system
```

**Solution:**
1. Verify the file exists in the prompts directory
2. Verify the `key` field in the YAML frontmatter matches the key you are requesting
3. Verify you called `registry.Reload(ctx)` after creating the registry

```bash
# Check file exists
ls -la prompts/agent/

# Verify the key field in the frontmatter
head -5 prompts/agent/system.yaml
```

### Invalid YAML Format

**Error:**
```
invalid format: expected YAML frontmatter with --- separator
```

**Solution:**
The file must start with `---`, have metadata fields, then another `---`, then content. Ensure there are exactly two `---` separators:

```yaml
---
key: my.prompt
version: 1.0.0
---
Content goes here.
```

### Variant Not Found

**Error:**
```
variant not found: concise (key: agent.system)
```

**Solution:**
Ensure a file with the variant suffix exists. For variant `"concise"`, the file must be named `system.concise.yaml`:

```
prompts/agent/system.yaml          # "default" variant
prompts/agent/system.concise.yaml  # "concise" variant
```

### Hot-Reload Not Working

**Symptom**: Changed a prompt file but agent uses old content.

**Causes:**
1. `Watch()` not started -- you must call `registry.Watch(ctx)` to enable file monitoring
2. Existing sessions may cache the old prompt
3. If using `CachedRegistry`, the TTL has not expired yet

**Solution:**
1. Ensure `Watch()` is called and the returned channel is being consumed
2. Create a new session to pick up changes
3. Call `cachedRegistry.Invalidate()` to force cache refresh

### Variable Placeholder Not Replaced

**Symptom**: Output contains literal `{{.backend_type}}` instead of the value.

**Solution:**
Ensure the variable name in the `vars` map matches the placeholder exactly:

```go
// Placeholder in YAML: {{.backend_type}}
vars := map[string]interface{}{
    "backend_type": "Teradata",  // key must match exactly
}
prompt, err := registry.Get(ctx, "agent.system", vars)
```

Variable names are case-sensitive and must use only word characters (letters, digits, underscore).

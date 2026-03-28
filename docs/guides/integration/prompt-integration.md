
# Prompt Registry Integration Guide

**Version**: v1.2.0

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Common Tasks](#common-tasks)
  - [Create Prompt Files](#create-prompt-files)
  - [Render Prompts with Variables](#render-prompts-with-variables)
  - [List and Search Prompts](#list-and-search-prompts)
  - [Hot Reload Prompts](#hot-reload-prompts)
  - [A/B Testing with Variants](#ab-testing-with-variants)
  - [Caching](#caching)
- [Examples](#examples)
  - [Example 1: System Prompt](#example-1-system-prompt)
  - [Example 2: Tool Description](#example-2-tool-description)
- [Troubleshooting](#troubleshooting)


## Overview

Manage prompts in YAML files with variable substitution, hot-reload, and A/B
testing using the `pkg/prompts` package. The `FileRegistry` loads prompts from
YAML frontmatter files in a directory tree, supports variant-based A/B testing,
and watches for file changes via fsnotify.

### Feature Status

| Feature | Status |
|---------|--------|
| File-based prompt loading | ✅ Implemented |
| Variable interpolation (`{{.var}}`) | ✅ Implemented |
| Prompt injection sanitization | ✅ Implemented |
| A/B testing variants | ✅ Implemented |
| Hot-reload via `Reload()` | ✅ Implemented |
| File watching via `Watch()` (fsnotify) | ✅ Implemented |
| TTL-based caching (`CachedRegistry`) | ✅ Implemented |
| Tag and prefix filtering | ✅ Implemented |

## Prerequisites

- Loom v1.2.0+
- Prompts directory: `./prompts/`

## Quick Start

```go
import "github.com/teradata-labs/loom/pkg/prompts"

// Create registry
registry := prompts.NewFileRegistry("./prompts")

// Load prompts from disk (required before first use)
ctx := context.Background()
if err := registry.Reload(ctx); err != nil {
    log.Fatal(err)
}

// Render prompt with variables
systemPrompt, err := registry.Get(ctx, "agent.system", map[string]interface{}{
    "backend_type": "teradata",
    "session_id":   "sess-123",
})
```


## Configuration

### Prompt File Format

> **Important:** FileRegistry expects YAML frontmatter format with `---`
> separators. The file is split on `---` into metadata (frontmatter) and
> content (body). The `name/namespace/prompts` array format is **not**
> supported.

```yaml
# prompts/agent/system.yaml
---
key: agent.system
version: "1.0.0"
author: team@example.com
description: "System prompt for agent"
tags: [agent, system]
variants: [default]
variables: [backend_type, session_id]
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-01T00:00:00Z
---
Help users interact with {{.backend_type}} systems.
Session: {{.session_id}}
```

The prompt key is derived from the `key` field in the frontmatter metadata.
Variants are determined by filename convention: `system.yaml` is the default
variant, `system.concise.yaml` is the "concise" variant.

### Metadata Fields

| Field | Type | Description |
|-------|------|-------------|
| `key` | string | Unique identifier (e.g., `agent.system.base`) |
| `version` | string | Semantic version (e.g., `2.1.0`) |
| `author` | string | Author email or username |
| `description` | string | What this prompt does |
| `tags` | []string | Tags for filtering and search |
| `variants` | []string | Available variant names (e.g., `[default, concise]`) |
| `variables` | []string | Variable names used in the prompt body |
| `created_at` | time | Creation timestamp |
| `updated_at` | time | Last update timestamp |

> **Note:** The `variables` field is a list of variable **names** (strings),
> not typed definitions. Type enforcement is not performed at the registry
> level; all values are converted to strings via `fmt.Sprintf`.


## Common Tasks

### Create Prompt Files

Directory structure:
```
prompts/
├── agent/
│   ├── system.yaml           # Key: "agent.system", variant: default
│   └── system.concise.yaml   # Key: "agent.system", variant: concise
├── tools/
│   └── execute_sql.yaml      # Key: "tools.execute_sql", variant: default
└── errors/
    └── corrections.yaml      # Key: "errors.corrections", variant: default
```

Prompt file (YAML frontmatter format):
```yaml
# prompts/tools/execute_sql.yaml
---
key: tools.execute_sql
version: "1.0.0"
author: team@example.com
description: "SQL execution tool description"
tags: [tool, sql]
variants: [default]
variables: [backend_name]
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-01T00:00:00Z
---
Execute SQL queries against {{.backend_name}}.
```

### Render Prompts with Variables

> **Note:** Variable interpolation uses regex-based `{{.variable}}` replacement,
> **not** Go `text/template`. Constructs like `{{range}}`, `{{if}}`, and
> pipes (`|`) are not supported. Each `{{.name}}` placeholder is replaced
> with the string representation of the matching key from the vars map.
>
> Values are escaped to prevent prompt injection (HTML entity escaping,
> control character removal, and injection pattern sanitization).

```go
ctx := context.Background()

// Variables are passed as map[string]interface{}
prompt, err := registry.Get(ctx, "agent.system", map[string]interface{}{
    "backend_type": "teradata",
    "session_id":   "sess-123",
})
```

Supported value types for interpolation:
- `string` -- escaped and sanitized
- `int`, `int32`, `int64`, `float32`, `float64` -- formatted with `%v`
- `bool` -- formatted with `%t`
- `[]string` -- joined with `, ` (each element escaped)
- Other types -- formatted with `%v` then escaped

If a variable placeholder has no matching key in the vars map, the placeholder
is left unchanged in the output (e.g., `{{.missing}}` stays as `{{.missing}}`).

### List and Search Prompts

```go
// List all prompts
keys, err := registry.List(ctx, nil)

// List prompts with a specific tag
keys, err := registry.List(ctx, map[string]string{"tag": "agent"})

// List prompts by key prefix
keys, err := registry.List(ctx, map[string]string{"prefix": "agent."})

// Get metadata for a prompt
metadata, err := registry.GetMetadata(ctx, "agent.system")
fmt.Printf("Version: %s\n", metadata.Version)
fmt.Printf("Variables: %v\n", metadata.Variables)  // []string of variable names
```

### Hot Reload Prompts

✅ Both manual reload and file watching are implemented.

```go
// Manual reload -- re-reads all YAML files from disk
if err := registry.Reload(ctx); err != nil {
    log.Printf("Failed to reload: %v", err)
}

// File watching via fsnotify -- receives notifications on changes
updates, err := registry.Watch(ctx)
if err != nil {
    log.Fatal(err)
}
for update := range updates {
    log.Printf("Prompt %s %s", update.Key, update.Action)
    // update.Action is one of: "created", "modified", "deleted", "error"
    if update.Error != nil {
        log.Printf("Watch error: %v", update.Error)
    }
}
```

### A/B Testing with Variants

✅ Built-in variant selection with multiple strategies.

Create variant files using the filename convention `<name>.<variant>.yaml`:

```
prompts/agent/
├── system.yaml            # default variant
├── system.concise.yaml    # concise variant
└── system.verbose.yaml    # verbose variant
```

Each variant file uses the same `key` in its frontmatter metadata.

```go
// Explicitly select a variant
prompt, err := registry.GetWithVariant(ctx, "agent.system", "concise", vars)

// Use automatic variant selection with ABTestingRegistry
selector := prompts.NewHashSelector()  // Consistent per session
abRegistry := prompts.NewABTestingRegistry(registry, selector)

ctx = prompts.WithSessionID(ctx, "sess-123")
prompt, err = abRegistry.Get(ctx, "agent.system", vars)
// Same session always gets the same variant
```

Available selectors:
- `ExplicitSelector` -- always returns a fixed variant
- `HashSelector` -- deterministic per session ID (consistent experience)
- `RandomSelector` -- uniform random selection
- `WeightedSelector` -- weighted random (e.g., 80% default, 20% experimental)

### Caching

✅ TTL-based in-memory cache via `CachedRegistry`.

```go
fileRegistry := prompts.NewFileRegistry("./prompts")
if err := fileRegistry.Reload(ctx); err != nil {
    log.Fatal(err)
}

cachedRegistry := prompts.NewCachedRegistry(fileRegistry, 5*time.Minute)

// First call: cache miss, loads from underlying registry
prompt, _ := cachedRegistry.Get(ctx, "agent.system", vars)

// Second call: cache hit
prompt, _ = cachedRegistry.Get(ctx, "agent.system", vars)

// Check cache stats
hits, misses := cachedRegistry.Stats()
fmt.Printf("Hit rate: %.1f%%\n", float64(hits)/float64(hits+misses)*100)

// Manual cache invalidation
cachedRegistry.InvalidateKey("agent.system")
cachedRegistry.Invalidate()  // Clear entire cache
```


## Examples

### Example 1: System Prompt

**prompts/agent/system.yaml:**
```yaml
---
key: agent.system.base
version: "1.0.0"
author: developer@example.com
description: "Base system prompt for SQL agents"
tags: [agent, system, core]
variants: [default]
variables: [backend_type, session_id, cost_threshold]
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-17T00:00:00Z
---
Help users interact with data systems through natural language.

Backend: {{.backend_type}}
Session: {{.session_id}}
Cost threshold: ${{.cost_threshold}}

Guidelines:
- Verify table/column names before querying
- Provide clear explanations
- Handle errors gracefully
```

**Usage:**
```go
registry := prompts.NewFileRegistry("./prompts")
ctx := context.Background()

if err := registry.Reload(ctx); err != nil {
    log.Fatal(err)
}

prompt, err := registry.Get(ctx, "agent.system.base", map[string]interface{}{
    "backend_type":   "teradata",
    "session_id":     "sess-abc123",
    "cost_threshold": 50.00,
})
```

### Example 2: Tool Description

**prompts/tools/execute_sql.yaml:**
```yaml
---
key: tools.execute_sql
version: "1.0.0"
author: developer@example.com
description: "SQL execution tool prompt"
tags: [tool, sql]
variants: [default]
variables: [backend_name, capabilities]
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-01T00:00:00Z
---
Execute SQL queries against {{.backend_name}}.

Capabilities: {{.capabilities}}
```

**Usage:**
```go
// Note: []string values are joined with ", " during interpolation.
// {{range}} and other Go template constructs are NOT supported.
prompt, err := registry.Get(ctx, "tools.execute_sql", map[string]interface{}{
    "backend_name": "PostgreSQL",
    "capabilities": []string{"SELECT", "INSERT", "UPDATE", "DELETE"},
})
// Result includes: "Capabilities: SELECT, INSERT, UPDATE, DELETE"
```


## Troubleshooting

### Prompt Not Found

**Error:** `prompt not found: system`

**Solutions:**
1. Verify that `registry.Reload(ctx)` was called after `NewFileRegistry()`
2. Check file exists: `ls prompts/agent/system.yaml`
3. Verify the `key` field in the YAML frontmatter matches the requested key
4. Reload the registry: `registry.Reload(ctx)`

### Variable Placeholder Not Replaced

**Symptom:** Output contains literal `{{.variable_name}}` text

Unlike Go templates, the `Interpolate` function does **not** return an error for
missing variables. Instead, unresolved placeholders are left in the output
unchanged.

**Solutions:**
1. Pass all required variables in the vars map
2. Check variable names match exactly (case-sensitive)
3. View the declared variables:
   ```go
   meta, _ := registry.GetMetadata(ctx, "agent.system")
   fmt.Printf("Variables: %v\n", meta.Variables) // []string
   ```

### YAML Frontmatter Parse Error

**Error:** `invalid format: expected YAML frontmatter with --- separator`

**Solutions:**
1. Ensure the file starts with `---` on its own line
2. Ensure there is a second `---` separating metadata from content
3. Do not use the `name/namespace/prompts` array format -- FileRegistry
   only supports YAML frontmatter format

### YAML Multiline Issues

**Problem:** Extra newlines in output

**Solution:** Use `|-` instead of `|` in YAML to strip trailing newlines:
```yaml
---
key: example
# ... other metadata
---
Line 1
Line 2
```
Note: The body content is after the second `---` separator, not a YAML field,
so `|` vs `|-` does not apply here. The content is trimmed of leading/trailing
whitespace by the loader.

### Registry Not Finding Prompts

**Problem:** Registry returns empty results

**Solutions:**
1. Ensure `Reload(ctx)` was called after creating the registry
2. Check directory path is correct
3. Verify YAML syntax is valid
4. Check file permissions


# Prompt Management Guide

**Version**: v1.2.0

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Common Tasks](#common-tasks)
  - [Load Prompts from YAML Files](#load-prompts-from-yaml-files)
  - [Use Variable Interpolation](#use-variable-interpolation)
  - [Enable Hot-Reload](#enable-hot-reload)
  - [Set Up A/B Testing](#set-up-ab-testing)
- [Examples](#examples)
  - [Example 1: System Prompt with Variables](#example-1-system-prompt-with-variables)
  - [Example 2: Tool Descriptions](#example-2-tool-descriptions)
  - [Example 3: A/B Test Variants](#example-3-ab-test-variants)
- [Troubleshooting](#troubleshooting)

## Feature Status

- ✅ File-based prompt loading via `FileRegistry`
- ✅ Variable interpolation with `{{.variable}}` syntax
- ✅ Hot-reload via `fsnotify` (Watch)
- ✅ A/B testing with variant selectors (Hash, Random, Weighted, Explicit)
- ✅ Prompt caching via `CachedRegistry`
- ✅ Prompt injection sanitization

## Overview

Manage prompts externally using YAML files with hot-reload, variable interpolation, and A/B testing support.

## Prerequisites

- Loom v1.2.0+
- Prompt YAML files using frontmatter format (see [YAML Format](#yaml-format))

## Quick Start

Create a prompt file at `prompts/agent/system.yaml`:

```yaml
---
key: agent.system
version: 1.0.0
author: developer@example.com
description: Base system prompt for agents
tags: [agent, system]
variants: [default]
variables: [backend_type, tool_count]
---
Help users interact with data systems through natural language queries.
Backend Type: {{.backend_type}}
Available Tools: {{.tool_count}} registered tools
```

Load and use in your agent:

```go
import "github.com/teradata-labs/loom/pkg/prompts"

registry := prompts.NewFileRegistry("./prompts")
if err := registry.Reload(ctx); err != nil {
    log.Fatal(err)
}
agent := agent.NewAgent(backend, llm, agent.WithPrompts(registry))
```

> **Note:** You must call `Reload()` after creating a `FileRegistry` to load prompt files from disk. `NewFileRegistry()` only initializes the struct.

## Configuration

### Directory Structure

```
prompts/
├── agent/
│   ├── system.yaml            # Key: "agent.system" (default variant)
│   └── system.concise.yaml   # Key: "agent.system" (concise variant)
├── tools/
│   └── execute_sql.yaml       # Key: "tools.execute_sql"
├── errors/
│   └── validation.yaml
└── guidance/
    └── self_correction.yaml
```

Prompt keys are derived from the `key` field in the YAML frontmatter metadata, not from the file path. Variants are determined by the filename: `system.yaml` is the "default" variant, `system.concise.yaml` is the "concise" variant.

### YAML Format

Prompt files use YAML frontmatter separated by `---` delimiters:

```yaml
---
key: <dot.separated.key>
version: <semver>
author: <email-or-username>
description: <what this prompt does>
tags: [tag1, tag2]
variants: [default, concise, verbose]
variables: [var1, var2]
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-01T00:00:00Z
---
Prompt content with {{.var1}} and {{.var2}} placeholders.
```

The metadata section (between `---` markers) contains structured fields. The content section (after the second `---`) is the actual prompt text with `{{.variable}}` interpolation placeholders.

## Common Tasks

### Load Prompts from YAML Files

Create a FileRegistry, reload from disk, and retrieve prompts:

```go
registry := prompts.NewFileRegistry("./prompts")
if err := registry.Reload(ctx); err != nil {
    log.Fatal(err)
}

prompt, err := registry.Get(ctx, "agent.system", map[string]interface{}{
    "backend_type": "postgres",
    "tool_count":   5,
})
if err != nil {
    log.Fatal(err)
}

fmt.Println(prompt)
```

### Use Variable Interpolation

Variables use `{{.variable_name}}` syntax in prompt content. All string values are escaped to prevent prompt injection attacks (control characters removed, HTML entities escaped, injection patterns sanitized).

Define variables in your prompt YAML frontmatter:

```yaml
---
key: agent.system
version: 1.0.0
author: developer@example.com
description: System prompt with variables
tags: [agent]
variants: [default]
variables: [backend_type, table_count, user_name]
---
Connected to {{.backend_type}} database.
Available tables: {{.table_count}}
User: {{.user_name}}
```

Pass variables when loading:

```go
prompt, _ := registry.Get(ctx, "agent.system", map[string]interface{}{
    "backend_type": "postgres",
    "table_count":  42,
    "user_name":    "alice",
})
```

If a variable is not provided, the `{{.variable_name}}` placeholder is kept as-is in the output.

### Enable Hot-Reload

Watch for prompt file changes using `fsnotify`:

```go
registry := prompts.NewFileRegistry("./prompts")
if err := registry.Reload(ctx); err != nil {
    log.Fatal(err)
}

updates, err := registry.Watch(ctx)
if err != nil {
    log.Fatal(err)
}

go func() {
    for update := range updates {
        log.Printf("Prompt updated: %s (%s)", update.Key, update.Action)
    }
}()
```

The `Watch()` method monitors the prompts directory and all subdirectories. When YAML files are created, modified, or deleted, it automatically calls `Reload()` and sends a `PromptUpdate` on the channel.

### Set Up A/B Testing

#### Define Variants as Separate Files

Create separate files for each variant. The variant name is extracted from the filename:

- `system.yaml` -- "default" variant
- `system.concise.yaml` -- "concise" variant
- `system.verbose.yaml` -- "verbose" variant

Each file uses the same `key` in its frontmatter so they are grouped together.

#### Select a Specific Variant

```go
// Get default variant
prompt, _ := registry.Get(ctx, "agent.system", vars)

// Get specific variant
prompt, _ := registry.GetWithVariant(ctx, "agent.system", "concise", vars)
```

#### Use Automatic Variant Selection

Wrap a `FileRegistry` with an `ABTestingRegistry` for automatic selection:

```go
fileRegistry := prompts.NewFileRegistry("./prompts")
if err := fileRegistry.Reload(ctx); err != nil {
    log.Fatal(err)
}

// Hash-based selection (deterministic per session)
selector := prompts.NewHashSelector()
abRegistry := prompts.NewABTestingRegistry(fileRegistry, selector)

// Automatically selects variant based on session ID in context
ctx = prompts.WithSessionID(ctx, "session-123")
prompt, _ := abRegistry.Get(ctx, "agent.system", vars)
```

Available selectors:

```go
// Hash-based: same session always gets the same variant (deterministic)
selector := prompts.NewHashSelector()

// Random: uniform random selection (pass 0 for random seed)
selector := prompts.NewRandomSelector(0)

// Weighted: weighted random selection
// Weights are relative (don't need to sum to 100)
selector := prompts.NewWeightedSelector(map[string]int{
    "default": 4,      // 80% (4/5)
    "experimental": 1, // 20% (1/5)
}, 0)

// Explicit: always returns a specific variant
selector := prompts.NewExplicitSelector("concise")
```

## Examples

### Example 1: System Prompt with Variables

Create `prompts/agent/system.yaml`:

```yaml
---
key: agent.system
version: 1.0.0
author: developer@example.com
description: Base system prompt for SQL agents
tags: [agent, system, sql]
variants: [default]
variables: [backend_type, tool_count]
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-17T00:00:00Z
---
Help users interact with {{.backend_type}} data systems.

Available Tools: {{.tool_count}}

Guidelines:
1. Discover schema before querying
2. Warn about expensive operations
3. Use patterns from the library
4. Auto-correct errors
```

Load in your code:

```go
registry := prompts.NewFileRegistry("./prompts")
if err := registry.Reload(ctx); err != nil {
    log.Fatal(err)
}

prompt, _ := registry.Get(ctx, "agent.system", map[string]interface{}{
    "backend_type": "postgres",
    "tool_count":   5,
})
```

### Example 2: Tool Descriptions

Create `prompts/tools/execute_sql.yaml`:

```yaml
---
key: tools.execute_sql
version: 1.0.0
author: developer@example.com
description: SQL execution tool description
tags: [tool, sql]
variants: [default]
variables: []
---
Execute a SQL query against the database.

When to use:
- Retrieve data from tables
- Aggregate or analyze data

Prerequisites:
- Call get_table_schema first

Arguments:
- sql (required): The SQL query
- limit (optional): Max rows (default: 100)
```

### Example 3: A/B Test Variants

Create the default variant at `prompts/agent/system.yaml`:

```yaml
---
key: agent.system
version: 1.0.0
author: developer@example.com
description: System prompt for agents
tags: [agent, system]
variants: [default, concise, verbose]
variables: [backend_type]
---
Help users interact with {{.backend_type}} data systems through natural language.

Guidelines:
1. Schema Discovery: Understand data structures first
2. Cost Awareness: Estimate expensive operations
3. Pattern Usage: Use validated patterns
4. Error Recovery: Auto-detect and fix errors
```

Create the concise variant at `prompts/agent/system.concise.yaml`:

```yaml
---
key: agent.system
version: 1.0.0
author: developer@example.com
description: Concise system prompt variant
tags: [agent, system]
variants: [default, concise, verbose]
variables: [backend_type]
---
Query {{.backend_type}} data efficiently.
Guidelines: schema first, cost check, use patterns.
```

Create the verbose variant at `prompts/agent/system.verbose.yaml`:

```yaml
---
key: agent.system
version: 1.0.0
author: developer@example.com
description: Verbose system prompt variant
tags: [agent, system]
variants: [default, concise, verbose]
variables: [backend_type]
---
Help users interact with {{.backend_type}} data systems through natural language
with detailed explanations and step-by-step guidance.

Operating Principles:

1. Schema Discovery First
   Always understand table structures before generating queries.

2. Cost-Conscious Operations
   Estimate query costs and warn about expensive operations.

3. Pattern Library Integration
   Use pre-validated patterns from the library.

4. Autonomous Error Recovery
   Analyze errors and generate fixes automatically.
```

Configure A/B testing in Go:

```go
fileRegistry := prompts.NewFileRegistry("./prompts")
if err := fileRegistry.Reload(ctx); err != nil {
    log.Fatal(err)
}

// Use weighted selector: 60% default, 20% concise, 20% verbose
selector := prompts.NewWeightedSelector(map[string]int{
    "default": 60,
    "concise": 20,
    "verbose": 20,
}, 0)
abRegistry := prompts.NewABTestingRegistry(fileRegistry, selector)

// Session ID determines variant (consistent for same session with HashSelector)
ctx = prompts.WithSessionID(ctx, sessionID)
prompt, _ := abRegistry.Get(ctx, "agent.system", map[string]interface{}{
    "backend_type": "postgres",
})
```

## Troubleshooting

### Prompt Not Found

1. Check the file exists and uses frontmatter format:
   ```bash
   ls prompts/agent/system.yaml
   head -5 prompts/agent/system.yaml  # Should start with ---
   ```

2. Verify the `key` field in the frontmatter matches what you request:
   ```bash
   grep "key:" prompts/agent/system.yaml
   ```

3. Verify `Reload()` was called after creating the registry:
   ```go
   registry := prompts.NewFileRegistry("./prompts")
   if err := registry.Reload(ctx); err != nil {
       log.Fatal(err)  // Check this error!
   }
   ```

### Variable Not Replaced

1. Check variable name matches (case-sensitive):
   ```yaml
   ---
   variables: [backend_type]
   ---
   {{.backend_type}}  # Must match variable name exactly
   ```

2. Verify variable is passed when loading:
   ```go
   registry.Get(ctx, "agent.system", map[string]interface{}{
       "backend_type": "postgres",  // Key must match placeholder name
   })
   ```

3. Note: Missing variables are left as `{{.variable_name}}` in the output (not replaced).

### Hot-Reload Not Working

1. Verify Watch is detecting changes:
   ```go
   updates, _ := registry.Watch(ctx)
   go func() {
       for update := range updates {
           log.Printf("Detected: %s (%s)", update.Key, update.Action)
       }
   }()
   ```

2. Check file is in watched directory

3. On some systems, editors create new files instead of modifying (try saving directly)

### Frontmatter Parse Error

If you see `invalid format: expected YAML frontmatter with --- separator`, ensure your YAML file has the correct structure:

```yaml
---
key: my.prompt.key
version: 1.0.0
---
Prompt content here.
```

The file must have exactly two `---` delimiters: one before the metadata and one after. Content before the first `---` is ignored.

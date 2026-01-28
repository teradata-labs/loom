
# Prompt Management Guide

**Version**: v1.0.0

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


## Overview

Manage prompts externally using YAML files with hot-reload, variable interpolation, and A/B testing support.

## Prerequisites

- Loom v1.0.0-beta.1+
- Prompt YAML files in `prompts/` directory

## Quick Start

Create a prompt file at `prompts/agent/system.yaml`:

```yaml
name: agent
namespace: loom
prompts:
  - id: system
    content: |
      Help users interact with data systems through natural language queries.
      Backend Type: {{.backend_type}}
      Available Tools: {{.tool_count}} registered tools
    variables:
      backend_type:
        type: 1  # STRING
        required: true
      tool_count:
        type: 2  # INT
        required: true
```

Load and use in your agent:

```go
import "github.com/teradata-labs/loom/pkg/prompts"

registry := prompts.NewFileRegistry("./prompts")
agent := agent.NewAgent(backend, llm, agent.WithPrompts(registry))
```

## Configuration

### Directory Structure

```
prompts/
├── agent/
│   └── system.yaml         # System prompts
├── tools/
│   ├── sql.yaml            # SQL tool descriptions
│   ├── file.yaml           # File tool descriptions
│   └── rest_api.yaml       # REST API tool descriptions
├── errors/
│   └── validation.yaml     # Validation error messages
└── guidance/
    └── self_correction.yaml # Self-correction guidance
```

### YAML Format

```yaml
name: <prompt-name>
namespace: <namespace>
prompts:
  - id: <prompt-id>
    content: |
      Prompt content with {{.variable}} placeholders
    variables:
      variable_name:
        type: 1  # 1=STRING, 2=INT, 3=BOOL
        required: true
    tags:
      - tag1
      - tag2
    metadata:
      version: "v1.0"
```

## Common Tasks

### Load Prompts from YAML Files

Create a FileRegistry and load prompts:

```go
registry := prompts.NewFileRegistry("./prompts")

prompt, err := registry.Get(ctx, "system", map[string]interface{}{
    "backend_type": "postgres",
    "tool_count":   5,
})
if err != nil {
    log.Fatal(err)
}

fmt.Println(prompt)
```

### Use Variable Interpolation

Define variables in your prompt YAML:

```yaml
content: |
  Connected to {{.backend_type}} database.
  Available tables: {{.table_count}}
  User: {{.user_name}}
variables:
  backend_type:
    type: 1
    required: true
  table_count:
    type: 2
    required: true
  user_name:
    type: 1
    required: false
```

Pass variables when loading:

```go
prompt, _ := registry.Get(ctx, "system", map[string]interface{}{
    "backend_type": "postgres",
    "table_count":  42,
    "user_name":    "alice",
})
```

### Enable Hot-Reload

Watch for prompt file changes:

```go
ctx := context.Background()
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

### Set Up A/B Testing

Define variants in your prompt file:

```yaml
name: agent
namespace: loom
prompts:
  - id: system
    content: |
      Default system prompt content...
    variants:
      concise: |
        Short and concise prompt content...
      verbose: |
        Detailed and verbose prompt content...
```

Select a specific variant:

```go
// Get default variant
prompt, _ := registry.Get(ctx, "system", vars)

// Get specific variant
prompt, _ := registry.GetWithVariant(ctx, "system", "concise", vars)
```

Configure automatic variant selection:

```go
// Hash-based selection (deterministic per session)
selector := prompts.NewHashBasedSelector("session_id", map[string]float64{
    "default": 0.6,
    "concise": 0.2,
    "verbose": 0.2,
})

// Random selection
selector := prompts.NewRandomSelector(map[string]float64{
    "default": 0.6,
    "concise": 0.2,
    "verbose": 0.2,
})
```

## Examples

### Example 1: System Prompt with Variables

Create `prompts/agent/system.yaml`:

```yaml
name: agent
namespace: loom
prompts:
  - id: system
    content: |
      Help users interact with {{.backend_type}} data systems.

      Available Tools: {{.tool_count}}

      Guidelines:
      1. Discover schema before querying
      2. Warn about expensive operations
      3. Use patterns from the library
      4. Auto-correct errors
    variables:
      backend_type:
        type: 1
        required: true
      tool_count:
        type: 2
        required: true
```

Load in your code:

```go
registry := prompts.NewFileRegistry("./prompts")
prompt, _ := registry.Get(ctx, "system", map[string]interface{}{
    "backend_type": "postgres",
    "tool_count":   5,
})
```

### Example 2: Tool Descriptions

Create `prompts/tools/sql.yaml`:

```yaml
name: tools
namespace: loom
prompts:
  - id: execute_sql
    content: |
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

Create `prompts/agent/system.yaml` with variants:

```yaml
name: agent
namespace: loom
prompts:
  - id: system
    content: |
      Help users interact with {{.backend_type}} data systems through natural language.

      Capabilities: {{.capabilities}}

      Guidelines:
      1. Schema Discovery: Understand data structures first
      2. Cost Awareness: Estimate expensive operations
      3. Pattern Usage: Use validated patterns
      4. Error Recovery: Auto-detect and fix errors
    variants:
      concise: |
        Query {{.backend_type}} data efficiently.
        Guidelines: schema first, cost check, use patterns.
      verbose: |
        Help users interact with {{.backend_type}} data systems through natural language
        with detailed explanations and step-by-step guidance.

        Your capabilities: {{.capabilities}}

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

Configure A/B testing:

```yaml
# config.yaml
prompts:
  ab_testing:
    enabled: true
    variant_selection: hash
    hash_key: session_id
    splits:
      default: 0.6
      concise: 0.2
      verbose: 0.2
```

## Troubleshooting

### Prompt Not Found

1. Check the file exists:
   ```bash
   ls prompts/agent/system.yaml
   ```

2. Verify the prompt ID matches:
   ```bash
   grep "id:" prompts/agent/system.yaml
   ```

3. Check the registry path:
   ```go
   registry := prompts.NewFileRegistry("./prompts")  // Correct path?
   ```

### Variable Not Replaced

1. Check variable name matches (case-sensitive):
   ```yaml
   content: |
     {{.backend_type}}  # Must match variable name
   variables:
     backend_type:      # Exact match required
       type: 1
   ```

2. Verify variable is passed when loading:
   ```go
   registry.Get(ctx, "system", map[string]interface{}{
       "backend_type": "postgres",  // Key must match
   })
   ```

### Hot-Reload Not Working

1. Verify fsnotify is detecting changes:
   ```go
   updates, _ := registry.Watch(ctx)
   go func() {
       for update := range updates {
           log.Printf("Detected: %s", update.Key)
       }
   }()
   ```

2. Check file is in watched directory

3. On some systems, editors create new files instead of modifying (try saving directly)

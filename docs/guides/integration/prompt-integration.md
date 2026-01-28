
# Promptio Integration Guide

**Version**: v1.0.0-beta.1

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
- [Examples](#examples)
  - [Example 1: System Prompt](#example-1-system-prompt)
  - [Example 2: Tool Description](#example-2-tool-description)
- [Troubleshooting](#troubleshooting)


## Overview

Manage prompts in YAML files with variable substitution and hot-reload using Promptio.

## Prerequisites

- Loom v1.0.0-beta.1+
- Prompts directory: `./prompts/`

## Quick Start

```go
import "github.com/teradata-labs/loom/pkg/prompts"

// Create registry
registry := prompts.NewPromptioRegistry("./prompts")

// Render prompt with variables
ctx := context.Background()
systemPrompt, err := registry.Get(ctx, "system", map[string]interface{}{
    "backend_type": "teradata",
    "tool_count":   5,
})
```


## Configuration

### Prompt File Format

```yaml
# prompts/agent/system.yaml
name: agent
namespace: loom
prompts:
  - id: system
    content: |
      Help users interact with {{.backend_type}} systems.
      Available tools: {{.tool_count}}
    variables:
      backend_type:
        type: 1  # STRING
        required: true
      tool_count:
        type: 2  # INT
        required: true
    tags: [agent, system]
    metadata:
      version: "v1.0"
```

### Variable Types

| Code | Type | Description |
|------|------|-------------|
| 1 | STRING | Text values |
| 2 | INT | Integer values |
| 3 | FLOAT | Floating-point |
| 4 | BOOL | Boolean |
| 5 | OBJECT | Structured objects |
| 6 | ARRAY | Lists/arrays |


## Common Tasks

### Create Prompt Files

Directory structure:
```
prompts/
├── agent/
│   └── system.yaml
├── tools/
│   └── sql.yaml
└── errors/
    └── corrections.yaml
```

Basic prompt:
```yaml
name: tools
namespace: loom
prompts:
  - id: sql_query
    content: |
      Execute SQL against {{.backend_name}}.
    variables:
      backend_name:
        type: 1
        required: true
```

### Render Prompts with Variables

```go
ctx := context.Background()

// Simple variables
prompt, err := registry.Get(ctx, "system", map[string]interface{}{
    "backend_type": "teradata",
    "tool_count":   5,
})

// Array variable
prompt, err := registry.Get(ctx, "capabilities", map[string]interface{}{
    "features": []string{"read", "write", "analyze"},
})
```

### List and Search Prompts

```go
// List all prompts with tag
keys, err := registry.List(ctx, map[string]string{"tag": "agent"})

// List prompts by prefix
keys, err := registry.List(ctx, map[string]string{"prefix": "agent."})

// Get metadata
metadata, err := registry.GetMetadata(ctx, "system")
fmt.Printf("Version: %s\n", metadata.Version)
fmt.Printf("Variables: %v\n", metadata.Variables)
```

### Hot Reload Prompts

```go
// Manual reload
if err := registry.Reload(ctx); err != nil {
    log.Printf("Failed to reload: %v", err)
}

// File watching (planned)
updates, err := registry.Watch(ctx)
for update := range updates {
    log.Printf("Prompt %s updated", update.Key)
}
```


## Examples

### Example 1: System Prompt

**prompts/agent/system.yaml:**
```yaml
name: agent
namespace: loom
prompts:
  - id: system
    content: |
      Help users interact with data systems through natural language.

      Backend: {{.backend_type}}
      Tools: {{.tool_count}} available

      Guidelines:
      - Verify table/column names before querying
      - Provide clear explanations
      - Handle errors gracefully
    variables:
      backend_type:
        type: 1
        required: true
        description: "Backend system type"
      tool_count:
        type: 2
        required: true
        description: "Number of tools"
    tags: [agent, system, core]
    metadata:
      version: "v1.0"
```

**Usage:**
```go
registry := prompts.NewPromptioRegistry("./prompts")

prompt, err := registry.Get(ctx, "system", map[string]interface{}{
    "backend_type": "teradata",
    "tool_count":   8,
})
```

### Example 2: Tool Description

**prompts/tools/sql.yaml:**
```yaml
name: tools
namespace: loom
prompts:
  - id: execute_sql
    content: |
      Execute SQL queries against {{.backend_name}}.

      Capabilities:
      {{range .capabilities}}- {{.}}
      {{end}}
    variables:
      backend_name:
        type: 1
        required: true
      capabilities:
        type: 6  # ARRAY
        required: true
    tags: [tool, sql]
```

**Usage:**
```go
prompt, err := registry.Get(ctx, "execute_sql", map[string]interface{}{
    "backend_name": "PostgreSQL",
    "capabilities": []string{"SELECT", "INSERT", "UPDATE", "DELETE"},
})
```


## Troubleshooting

### Prompt Not Found

**Error:** `prompt not found: system`

**Solutions:**
1. Check file exists: `ls prompts/agent/system.yaml`
2. Verify prompt ID in YAML matches requested ID
3. Reload registry: `registry.Reload(ctx)`

### Variable Substitution Fails

**Error:** `template: <no value>`

**Solutions:**
1. Pass all required variables
2. Check variable names match exactly (case-sensitive)
3. View required variables:
   ```go
   meta, _ := registry.GetMetadata(ctx, "system")
   fmt.Printf("Variables: %v\n", meta.Variables)
   ```

### YAML Multiline Issues

**Problem:** Extra newlines in output

**Solution:** Use `|-` instead of `|`:
```yaml
content: |-
  Line 1
  Line 2
# No trailing newline
```

### Registry Not Finding Prompts

**Problem:** Registry returns empty results

**Solutions:**
1. Check directory path is correct
2. Verify YAML syntax: `yamllint prompts/`
3. Check file permissions
4. Reload: `registry.Reload(ctx)`

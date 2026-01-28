
# Prompt Migration Guide

**Version**: v1.0.0-beta.1

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Migration Steps](#migration-steps)
  - [Step 1: Create Prompts Directory](#step-1-create-prompts-directory)
  - [Step 2: Export Prompts to YAML](#step-2-export-prompts-to-yaml)
  - [Step 3: Configure looms.yaml](#step-3-configure-loomsyaml)
  - [Step 4: Test and Verify](#step-4-test-and-verify)
- [Examples](#examples)
  - [Example 1: Simple Agent Prompt](#example-1-simple-agent-prompt)
  - [Example 2: Prompt with Variables](#example-2-prompt-with-variables)
- [Troubleshooting](#troubleshooting)


## Overview

Migrate from inline prompts to the PromptRegistry system for centralized prompt management, hot-reload, and A/B testing.

## Prerequisites

- Loom v1.0.0-beta.1+
- Server running: `looms serve`
- Existing agent with inline prompts

## Quick Start

```bash
# Create prompts directory
mkdir -p prompts/agent

# Move prompts to YAML
cat > prompts/agent/system.yaml << 'EOF'
name: agent
namespace: loom
prompts:
  - id: system_sql
    content: |
      Help users write SQL queries accurately and efficiently.
EOF

# Update looms.yaml
cat >> $LOOM_DATA_DIR/looms.yaml << 'EOF'
prompts:
  source: file
  file_dir: ./prompts
  enable_reload: true
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
├── agent/           # Agent system prompts
│   └── system.yaml
├── tools/           # Tool descriptions
│   └── communication.yaml
├── guidance/        # Self-correction messages
│   └── self_correction.yaml
└── errors/          # Error messages
    └── validation.yaml
```

### Step 2: Export Prompts to YAML

Convert inline prompts to YAML files:

```yaml
# prompts/agent/system.yaml
name: agent
namespace: loom
prompts:
  - id: system
    content: |
      Help users with {{.backend_type}}.
      You have {{.tool_count}} tools available.
    variables:
      backend_type:
        type: 1  # STRING
        required: true
      tool_count:
        type: 2  # INT
        required: true
    metadata:
      version: "v1.0"
```

### Step 3: Configure looms.yaml

Add prompts configuration:

```yaml
# looms.yaml
prompts:
  source: file              # "file" | "promptio-library" | "promptio-service"
  file_dir: ./prompts       # Directory with YAML files
  cache_size: 1000          # LRU cache size
  enable_reload: true       # Hot-reload on file changes

agents:
  agents:
    sql-agent:
      name: SQL Agent
      system_prompt_key: agent.system_sql  # Reference PromptRegistry
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

Test hot-reload:

```bash
# Update prompt
echo "Updated content" >> prompts/agent/system.yaml

# Create NEW session (existing sessions keep old prompts)
loom chat --server localhost:9090
```


## Examples

### Example 1: Simple Agent Prompt

**Before** (inline in looms.yaml):
```yaml
agents:
  my-agent:
    system_prompt: "Help users with their tasks."
```

**After** (prompts/agent/system.yaml):
```yaml
name: agent
namespace: loom
prompts:
  - id: system_helper
    content: "Help users with their tasks."
    tags: [agent, system]
    metadata:
      version: "v1.0"
```

**Updated looms.yaml**:
```yaml
prompts:
  source: file
  file_dir: ./prompts

agents:
  my-agent:
    system_prompt_key: agent.system_helper
```

### Example 2: Prompt with Variables

**Before**:
```yaml
system_prompt: "Help users with Teradata using 5 available tools."
```

**After** (prompts/agent/system.yaml):
```yaml
prompts:
  - id: system
    content: |
      Help users interact with {{.backend_type}} using {{.tool_count}} tools.
    variables:
      backend_type:
        type: 1  # STRING
        required: true
      tool_count:
        type: 2  # INT
        required: true
```

Agent automatically provides these variables:
- `backend_type`: from backend.Name()
- `tool_count`: from len(tools)


## Troubleshooting

### Prompt Not Found

**Error:**
```
WARN  Failed to load prompt: agent.system_sql (error: prompt not found)
INFO  Using hardcoded fallback
```

**Solution:**
```bash
# Check file exists
ls -la prompts/agent/

# Validate YAML
yamllint prompts/agent/system.yaml

# Check prompt ID matches
grep "id:" prompts/agent/system.yaml
```

### Hot-Reload Not Working

**Symptom**: Changed prompt but agent uses old version

**Causes:**
1. Existing sessions keep old prompts (by design)
2. Hot-reload disabled in config

**Solution:**
1. Create a NEW session to see changes
2. Verify config:
   ```yaml
   prompts:
     enable_reload: true  # Must be true
   ```

### Variable Interpolation Fails

**Error:**
```
ERROR  Failed to render prompt: template error
```

**Solution:**
```yaml
# Ensure variables are defined
variables:
  backend_type:
    type: 1  # STRING
    required: true

# Provide required variables when calling
vars := map[string]interface{}{
    "backend_type": "teradata",
}
```

### Fallback to Hardcoded

**Symptom**: Agent works but logs show "using hardcoded fallback"

This is expected behavior. The system has a 3-tier fallback:
1. PromptRegistry
2. Inline config (`system_prompt`)
3. Hardcoded defaults

To use PromptRegistry instead:
1. Verify `prompts.source` is set in looms.yaml
2. Verify `prompts.file_dir` points to correct directory
3. Verify YAML files exist and are valid

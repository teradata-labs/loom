# Context Variable Interpolation Guide

**Version:** v1.1.0-beta
**Status:** ✅ Available (fully working with tests)

## Table of Contents

- [Overview](#overview)
- [Quick Start](#quick-start)
- [How It Works](#how-it-works)
- [Template Syntax](#template-syntax)
- [Common Variables](#common-variables)
- [Passing Context](#passing-context)
- [Complete Examples](#complete-examples)
- [Best Practices](#best-practices)
- [Troubleshooting](#troubleshooting)
- [API Reference](#api-reference)

## Overview

Context variable interpolation allows you to create workspace-aware agents by injecting runtime values into agent system prompts. Instead of hardcoding paths, IDs, or configuration in agent YAML, you pass them dynamically with each request.

**Use context variables to:**
- Make agents workspace-aware (no hardcoded paths)
- Inject user/workspace identity at runtime
- Adapt agent behavior based on environment (dev/staging/prod)
- Pass custom metadata to agents
- Avoid creating multiple similar agents for different contexts

### How It Differs from Session Context

**Context Variables (this guide):**
- Interpolated into system prompt using `{{.variable}}` syntax
- Passed via `context` field in WeaveRequest
- Affect agent's initial instructions
- Template-based (Go templates)

**Session Context (different feature):**
- Stored in session state
- Used by tools to track conversation state
- Not interpolated into prompts
- Key-value storage

## Quick Start

### 1. Create Agent with Template Variables

Create `~/.loom/agents/workspace-agent.yaml`:

```yaml
apiVersion: agent.loom.run/v1
kind: Agent
metadata:
  name: workspace-agent
  description: Workspace-aware file management agent
spec:
  systemPrompt: |
    You are a file management assistant.

    Your workspace: {{.project_path}}
    Workspace ID: {{.workspace_id}}

    When the user asks to list files, ALWAYS use {{.project_path}}.
    Never ask the user for the path.

  llm:
    provider: anthropic
    model: claude-sonnet-4
```

**Key points:**
- Use `{{.variable_name}}` syntax in system prompt
- Variables are placeholders that get replaced at runtime

### 2. Pass Context in Request

```bash
grpcurl -plaintext -d '{
  "agent_id": "workspace-agent",
  "query": "List all Python files in my workspace",
  "context": {
    "project_path": "/Users/josh/projects/my-app",
    "workspace_id": "workspace-123"
  }
}' localhost:50051 loom.v1.LoomService/Weave
```

### 3. Agent Receives Interpolated Prompt

The agent sees:

```
You are a file management assistant.

Your workspace: /Users/josh/projects/my-app
Workspace ID: workspace-123

When the user asks to list files, ALWAYS use /Users/josh/projects/my-app.
Never ask the user for the path.
```

**Result:** Agent knows the workspace path without you having to mention it in your query!

## How It Works

### Data Flow

```
1. Agent YAML Definition
   └─> systemPrompt: "Workspace: {{.project_path}}"

2. Frontend/Client Request
   └─> context: {"project_path": "/Users/josh/projects/app"}

3. Server Handler (Weave/StreamWeave)
   └─> Extracts context from proto request
   └─> Calls ChatWithProgressAndContext(ctx, sessionID, query, context, ...)

4. Agent.ChatWithProgressAndContext
   └─> Injects context into Go context via WithContextVariables()
   └─> Calls memory.GetOrCreateSessionWithAgent(ctx, ...)

5. Memory.GetOrCreateSessionWithAgent
   └─> Detects context variables present in Go context
   └─> Triggers SegmentedMemory regeneration
   └─> Calls systemPromptFunc(ctx)

6. Agent.systemPromptFunc
   └─> Gets base prompt: "Workspace: {{.project_path}}"
   └─> Gets context variables: {"project_path": "/Users/josh/projects/app"}
   └─> Interpolates using Go text/template
   └─> Returns: "Workspace: /Users/josh/projects/app"

7. Agent Chat Execution
   └─> Agent sees fully interpolated prompt with actual values
```

### Thread-Safe Implementation

Context variables are passed via Go `context.Context` to avoid mutex deadlocks during prompt generation. This is why the feature is reliable and safe for concurrent use.

### Regeneration Trigger

**Important:** When context variables are present, the agent's `SegmentedMemory` (ROM/Kernel/L1/L2 cache) is regenerated to ensure the system prompt includes interpolated values. This happens automatically.

## Template Syntax

Loom uses Go's `text/template` package for interpolation.

### Basic Interpolation

```yaml
# Simple variable substitution
Project: {{.project_path}}
User: {{.user_name}}
Environment: {{.environment}}
```

### Conditionals

```yaml
{{if .debug_mode}}
DEBUG MODE ENABLED - Provide verbose output with reasoning.
{{end}}

{{if eq .environment "production"}}
PRODUCTION: Be extra careful with destructive operations.
{{else}}
DEVELOPMENT: You can move faster and take shortcuts.
{{end}}
```

### Default Values

```yaml
# Provide fallback if variable not present
Environment: {{.environment | default "development"}}
Debug: {{.debug | default "false"}}
User: {{.user_name | default "Guest"}}
```

### Loops

```yaml
Available workspaces:
{{range .workspaces}}
- ID: {{.id}}, Path: {{.path}}
{{end}}
```

**Request:**
```json
{
  "context": {
    "workspaces": [
      {"id": "ws-1", "path": "/path/1"},
      {"id": "ws-2", "path": "/path/2"}
    ]
  }
}
```

**Result:**
```
Available workspaces:
- ID: ws-1, Path: /path/1
- ID: ws-2, Path: /path/2
```

### String Manipulation

```yaml
# Uppercase
Environment: {{.environment | upper}}

# Lowercase
User: {{.user_name | lower}}

# Title case
Project: {{.project_name | title}}
```

### Comparisons

```yaml
{{if eq .mode "planning"}}
Create detailed plans before executing.
{{else if eq .mode "execution"}}
Execute immediately without planning.
{{end}}

{{if ne .user_role "admin"}}
Request approval before modifying production resources.
{{end}}
```

### Complex Example

```yaml
spec:
  systemPrompt: |
    You are {{.agent_role | default "an assistant"}}.

    **Environment:** {{.environment | upper}}
    {{if eq .environment "production"}}
    ⚠️  PRODUCTION MODE - Extra validation required
    {{end}}

    **Your Workspace:**
    - Path: {{.project_path}}
    - ID: {{.workspace_id}}
    - Owner: {{.user_name | default "Unknown"}}

    **Available Tools:**
    {{range .enabled_tools}}
    - {{.}}
    {{end}}

    {{if .debug}}
    **Debug Mode:** Enabled
    Show reasoning for all decisions.
    {{end}}
```

## Common Variables

### Workspace Context

**project_path**
- Absolute path to user's workspace
- Most commonly used variable
- Example: `/Users/josh/projects/my-app`

```yaml
Your workspace: {{.project_path}}
```

**workspace_id**
- Unique identifier for workspace
- Example: `workspace-123` or UUID

```yaml
Workspace ID: {{.workspace_id}}
```

**workspace_name**
- Human-readable workspace name
- Example: `My Analytics Project`

```yaml
Working on: {{.workspace_name}}
```

### User Context

**user_id**
- Unique user identifier
- Example: `user-456`

```yaml
User: {{.user_id}}
```

**user_name**
- Display name
- Example: `Josh Schoen`

```yaml
Hello {{.user_name}}!
```

**user_email**
- User's email address
- Example: `josh@example.com`

```yaml
Contact: {{.user_email}}
```

**user_role**
- User's role or permission level
- Example: `admin`, `developer`, `viewer`

```yaml
{{if eq .user_role "admin"}}
You have full access to all operations.
{{end}}
```

### Mode Control

**mode**
- Current operating mode
- Example: `PLANNING`, `PRODUCTION_MODE`, `DEVELOPMENT`

```yaml
Mode: {{.mode}}
```

**permission_mode**
- Permission mode as string (for display in prompt)
- Example: `plan`, `execute`
- Note: This is different from the top-level `permission_mode` enum field

```yaml
Operating in {{.permission_mode}} mode.
```

**environment**
- Deployment environment
- Example: `development`, `staging`, `production`

```yaml
Environment: {{.environment}}
```

### Custom Variables

You can pass any custom variables:

```yaml
# Agent YAML
Database: {{.database_url}}
API Key: {{.api_key}}
Feature Flags: {{.feature_x_enabled}}
```

```json
// Request
{
  "context": {
    "database_url": "postgresql://localhost/mydb",
    "api_key": "sk-xxx",
    "feature_x_enabled": "true"
  }
}
```

## Passing Context

### gRPC (grpcurl)

```bash
grpcurl -plaintext -d '{
  "agent_id": "my-agent",
  "query": "Your query here",
  "context": {
    "project_path": "/Users/josh/projects/my-app",
    "workspace_id": "workspace-123",
    "environment": "production",
    "custom_var": "custom_value"
  }
}' localhost:50051 loom.v1.LoomService/Weave
```

### REST API (curl)

```bash
curl -X POST http://localhost:8080/v1/weave \
  -H "Content-Type: application/json" \
  -d '{
    "agentId": "my-agent",
    "query": "Your query",
    "context": {
      "project_path": "/path/to/project",
      "workspace_id": "ws-123"
    }
  }'
```

### Go Client

```go
import (
    loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
    "google.golang.org/grpc"
)

client := loomv1.NewLoomServiceClient(conn)

req := &loomv1.WeaveRequest{
    AgentId: "my-agent",
    Query: "List files",
    Context: map[string]string{
        "project_path": "/Users/josh/projects/app",
        "workspace_id": "workspace-123",
    },
}

resp, err := client.Weave(ctx, req)
```

### Python Client

```python
import grpc
from loom.v1 import loom_pb2, loom_pb2_grpc

channel = grpc.insecure_channel('localhost:50051')
client = loom_pb2_grpc.LoomServiceStub(channel)

request = loom_pb2.WeaveRequest(
    agent_id="my-agent",
    query="List files",
    context={
        "project_path": "/Users/josh/projects/app",
        "workspace_id": "workspace-123",
    }
)

response = client.Weave(request)
```

### JavaScript/TypeScript Client

```typescript
import { LoomServiceClient } from '@loom/client';

const client = new LoomServiceClient('localhost:50051');

const response = await client.weave({
  agentId: 'my-agent',
  query: 'List files',
  context: {
    project_path: '/Users/josh/projects/app',
    workspace_id: 'workspace-123',
  },
});
```

## Complete Examples

### Example 1: Terry Data Coder (Workspace-Aware Data Engineer)

**Agent:** `~/.loom/agents/terry-data-coder.yaml`

```yaml
apiVersion: agent.loom.run/v1
kind: Agent
metadata:
  name: terry-data-coder
  description: Teradata-focused data engineering assistant with workspace awareness
spec:
  systemPrompt: |
    You are Terry, a Teradata data engineering expert.

    **Context Variables (interpolated at runtime):**
    - Project Path: {{.project_path}}
    - Workspace ID: {{.workspace_id}}
    - Mode: {{.mode}}
    - Permission Mode: {{.permission_mode}}

    **Your Responsibilities:**
    When working with files or data:
    1. ALWAYS use {{.project_path}} for file operations
    2. Never ask the user for the project path
    3. Use shell_execute to list directory contents in {{.project_path}}
    4. Create all files in {{.project_path}}

    {{if eq .permission_mode "plan"}}
    **Planning Mode:**
    - Create detailed execution plans
    - Show all SQL queries before running
    - Wait for user approval
    {{else if eq .permission_mode "execute"}}
    **Execution Mode:**
    - Execute queries immediately
    - Provide results concisely
    {{end}}

  llm:
    provider: anthropic
    model: claude-sonnet-4

  tools:
    shell_execute: {}
    file_read: {}
    file_write: {}
```

**Usage:**

```bash
# Planning mode
grpcurl -plaintext -d '{
  "agent_id": "terry-data-coder",
  "query": "Create a data pipeline to analyze sales data",
  "permission_mode": 3,
  "context": {
    "project_path": "/Users/josh/.teradata-projects/analytics",
    "workspace_id": "analytics-workspace-42",
    "mode": "PLANNING",
    "permission_mode": "plan"
  }
}' localhost:50051 loom.v1.LoomService/Weave
```

**Agent receives:**
```
You are Terry, a Teradata data engineering expert.

**Context Variables (interpolated at runtime):**
- Project Path: /Users/josh/.teradata-projects/analytics
- Workspace ID: analytics-workspace-42
- Mode: PLANNING
- Permission Mode: plan

**Your Responsibilities:**
When working with files or data:
1. ALWAYS use /Users/josh/.teradata-projects/analytics for file operations
2. Never ask the user for the project path
3. Use shell_execute to list directory contents in /Users/josh/.teradata-projects/analytics
4. Create all files in /Users/josh/.teradata-projects/analytics

**Planning Mode:**
- Create detailed execution plans
- Show all SQL queries before running
- Wait for user approval
```

### Example 2: Multi-Environment Agent

**Agent:** Adapts behavior based on environment

```yaml
metadata:
  name: deploy-agent
spec:
  systemPrompt: |
    You are a deployment assistant.

    Environment: {{.environment | upper}}
    Project: {{.project_path}}

    {{if eq .environment "production"}}
    🚨 PRODUCTION ENVIRONMENT 🚨
    - Require approval for ALL operations
    - Validate extensively before executing
    - Create rollback plans
    - Log all actions to audit trail
    {{else if eq .environment "staging"}}
    📦 STAGING ENVIRONMENT
    - Test thoroughly before production
    - Can skip some validation
    - Rollback available
    {{else}}
    🔧 DEVELOPMENT ENVIRONMENT
    - Fast iteration mode
    - Skip extensive validation
    - Auto-execute most operations
    {{end}}
```

**Production deployment:**
```bash
grpcurl -d '{
  "agent_id": "deploy-agent",
  "query": "Deploy version 2.0",
  "permission_mode": 3,
  "context": {
    "environment": "production",
    "project_path": "/var/www/app"
  }
}' localhost:50051 loom.v1.LoomService/Weave
```

**Development deployment:**
```bash
grpcurl -d '{
  "agent_id": "deploy-agent",
  "query": "Deploy latest changes",
  "permission_mode": 2,
  "context": {
    "environment": "development",
    "project_path": "/Users/josh/dev/app"
  }
}' localhost:50051 loom.v1.LoomService/Weave
```

### Example 3: Multi-Workspace Agent

**Agent:** Works across multiple workspaces

```yaml
metadata:
  name: multi-workspace-agent
spec:
  systemPrompt: |
    You are a multi-workspace file manager.

    **Current Workspace:**
    - ID: {{.workspace_id}}
    - Path: {{.project_path}}

    **All Available Workspaces:**
    {{range .available_workspaces}}
    - {{.id}}: {{.path}}
    {{end}}

    When the user references "workspace A" or "workspace B", map to:
    {{range .available_workspaces}}
    - "{{.name}}" → {{.path}}
    {{end}}
```

**Request with workspace list:**
```bash
grpcurl -d '{
  "agent_id": "multi-workspace-agent",
  "query": "Compare test coverage between workspace A and workspace B",
  "context": {
    "workspace_id": "workspace-a",
    "project_path": "/Users/josh/projects/workspace-a",
    "available_workspaces": [
      {
        "id": "workspace-a",
        "name": "workspace A",
        "path": "/Users/josh/projects/workspace-a"
      },
      {
        "id": "workspace-b",
        "name": "workspace B",
        "path": "/Users/josh/projects/workspace-b"
      }
    ]
  }
}' localhost:50051 loom.v1.LoomService/Weave
```

### Example 4: User-Aware Agent

**Agent:** Adapts to user's role and permissions

```yaml
metadata:
  name: admin-agent
spec:
  systemPrompt: |
    You are a system administration assistant.

    **User:** {{.user_name}} ({{.user_email}})
    **Role:** {{.user_role}}

    {{if eq .user_role "admin"}}
    **Admin Privileges:**
    - Full access to all systems
    - Can modify production
    - Can delete resources
    {{else if eq .user_role "developer"}}
    **Developer Privileges:**
    - Read access to production
    - Full access to development
    - Cannot delete production resources
    {{else}}
    **Viewer Privileges:**
    - Read-only access
    - Cannot modify any resources
    {{end}}

    Enforce permissions based on role when executing commands.
```

**Request:**
```bash
grpcurl -d '{
  "agent_id": "admin-agent",
  "query": "Delete old log files",
  "context": {
    "user_name": "Josh Schoen",
    "user_email": "josh@example.com",
    "user_role": "developer"
  }
}' localhost:50051 loom.v1.LoomService/Weave
```

**Agent behavior:** Won't allow deletion in production (developer role), but will in development.

## Best Practices

### 1. Always Provide Defaults

Use the `default` filter for optional variables:

```yaml
✅ GOOD:
Environment: {{.environment | default "development"}}
Debug: {{.debug | default "false"}}

❌ BAD:
Environment: {{.environment}}  # Empty if not provided
```

### 2. Document Required Variables

Make it clear what context variables are required:

```yaml
metadata:
  name: workspace-agent
  description: |
    Workspace-aware file manager.

    Required context variables:
    - project_path: Absolute path to workspace
    - workspace_id: Unique workspace identifier

    Optional context variables:
    - environment: Deployment environment (default: development)
    - debug: Enable debug mode (default: false)
```

### 3. Use Descriptive Variable Names

```yaml
✅ GOOD:
{{.project_path}}
{{.workspace_id}}
{{.user_email}}

❌ BAD:
{{.path}}
{{.id}}
{{.email}}
```

### 4. Validate in Tools, Not Just Prompts

Don't rely solely on prompt to communicate paths. Tools should validate:

```go
// Tool implementation
func (t *FileReadTool) Execute(params map[string]interface{}) (*Result, error) {
    path := params["path"].(string)

    // Get workspace path from context
    workspacePath := getFromContext("project_path")

    // Validate path is within workspace
    if !strings.HasPrefix(path, workspacePath) {
        return nil, fmt.Errorf("path must be within workspace: %s", workspacePath)
    }

    // ... rest of implementation
}
```

### 5. Use Conditionals for Different Modes

Adapt agent behavior based on context:

```yaml
{{if eq .mode "training"}}
Explain your reasoning step-by-step.
{{else if eq .mode "production"}}
Provide concise results only.
{{end}}
```

### 6. Test Templates Before Deploying

Create a test script:

```bash
#!/bin/bash
# Test context variable interpolation

echo "Testing basic interpolation..."
grpcurl -d '{
  "agent_id": "test-agent",
  "query": "What is my workspace?",
  "context": {"project_path": "/test/path"}
}' localhost:50051 loom.v1.LoomService/Weave | grep "/test/path"

echo "Testing defaults..."
grpcurl -d '{
  "agent_id": "test-agent",
  "query": "What environment?",
  "context": {}  # No environment provided
}' localhost:50051 loom.v1.LoomService/Weave | grep "development"
```

### 7. Keep Templates Simple

```yaml
✅ GOOD - Simple and clear:
Your workspace: {{.project_path}}

❌ BAD - Overly complex:
{{with .workspace}}{{if .path}}{{.path}}{{else}}{{.default_path}}{{end}}{{end}}
```

### 8. Separate Configuration from Context

**Context:** Runtime values that change per request
**Configuration:** Static values in agent YAML

```yaml
# Static configuration (in YAML)
spec:
  llm:
    provider: anthropic
    model: claude-sonnet-4

  # Dynamic context (from request)
  systemPrompt: |
    Workspace: {{.project_path}}
```

## Troubleshooting

### Variables Not Interpolating

**Symptom:** Agent sees `{{.project_path}}` literally instead of actual path.

**Cause 1: Context not passed in request**

```bash
❌ WRONG - No context field:
grpcurl -d '{"query": "List files"}' localhost:50051 loom.v1.LoomService/Weave

✅ CORRECT - Context provided:
grpcurl -d '{
  "query": "List files",
  "context": {"project_path": "/path"}
}' localhost:50051 loom.v1.LoomService/Weave
```

**Cause 2: Template syntax error**

```yaml
❌ WRONG:
Project: {{.project_path  # Missing }}

✅ CORRECT:
Project: {{.project_path}}
```

**Cause 3: Variable name mismatch**

```yaml
# Agent YAML uses:
{{.workspace_path}}

# But request sends:
"context": {"project_path": "/path"}  # Different key!

# Fix: Match the variable names
```

**Debug steps:**

1. Check server logs:
   ```
   DEBUG: Interpolated system prompt with context variables
   original_len=1550 interpolated_len=1627
   ```

2. Verify context in request:
   ```
   DEBUG: ChatWithProgressAndContext called context_len=4
   ```

3. Test with simple variable:
   ```yaml
   systemPrompt: "Test: {{.test}}"
   ```
   ```bash
   grpcurl -d '{"context": {"test": "hello"}}' ...
   ```

### Template Parse Error

**Symptom:** Agent uses original prompt, logs show parse error.

**Cause:** Invalid Go template syntax

```yaml
❌ WRONG:
{{if .debug  # Missing closing if
Mode: {{.mode  # Missing closing braces

✅ CORRECT:
{{if .debug}}Debug enabled{{end}}
Mode: {{.mode}}
```

**Check logs for:**
```
WARN: Failed to parse system prompt template error="..."
```

**Fix:** Validate template syntax:
1. Ensure all `{{` have matching `}}`
2. All `{{if}}` have `{{end}}`
3. All `{{range}}` have `{{end}}`

### Empty Variable Values

**Symptom:** Variable interpolates but shows empty string.

**Cause:** Variable not in context map

```bash
# Agent expects {{.project_path}}
# But request sends:
"context": {"workspace_path": "/path"}  # Wrong key

# Agent sees empty: ""
```

**Fix 1: Provide the variable**
```bash
"context": {"project_path": "/path"}  # Correct key
```

**Fix 2: Use default**
```yaml
Path: {{.project_path | default "/default/path"}}
```

### SSE Streaming Not Working

**Symptom:** Context works with `/v1/weave` but not `/v1/weave:stream`.

**This was a bug (fixed in v1.1.0):** StreamWeave wasn't calling `ChatWithProgressAndContext()`.

**Verify fix:**
```bash
# Check logs during streaming
DEBUG: StreamWeave: context variables for interpolation context_len=4
```

**Test both endpoints:**
```bash
# Non-streaming
curl -X POST localhost:8080/v1/weave -d '{"context": {...}}'

# Streaming
curl -N -X POST localhost:8080/v1/weave:stream -d '{"context": {...}}'
```

Both should interpolate correctly.

### Context Persisted Across Requests

**Symptom:** Old context values appear in new requests.

**Explanation:** By design - context is merged into session and persisted.

**Solution 1: Use new session**
```bash
# Don't provide session_id - creates fresh session
grpcurl -d '{"query": "...", "context": {...}}' ...
```

**Solution 2: Override values**
```bash
# New values override old
grpcurl -d '{
  "session_id": "existing",
  "context": {"project_path": "/new/path"}
}' ...
```

## API Reference

### WeaveRequest Context Field

```protobuf
message WeaveRequest {
  // ... other fields ...

  // Context variables for prompt interpolation
  map<string, string> context = 6;
}
```

**Notes:**
- All values must be strings
- Converted to appropriate types during interpolation
- Keys can be any valid identifier (alphanumeric + underscore)

### Example Request

```json
{
  "query": "List files",
  "context": {
    "project_path": "/Users/josh/projects/app",
    "workspace_id": "workspace-123",
    "user_id": "user-456",
    "environment": "production",
    "debug": "true",
    "custom_key": "custom_value"
  }
}
```

### Template Functions Available

Standard Go template functions:
- `default` - Provide default value
- `eq`, `ne`, `lt`, `le`, `gt`, `ge` - Comparisons
- `and`, `or`, `not` - Boolean logic
- `printf`, `print`, `println` - Formatting
- `len` - Length of string/array
- `index` - Array indexing

**Examples:**
```yaml
{{.var | default "fallback"}}
{{if eq .mode "production"}}...{{end}}
{{printf "%s: %s" .key .value}}
{{len .items}} items
```

---

## Next Steps

- **Permission Modes:** [Permission Modes Guide](/docs/guides/permission-modes.md)
- **Examples:** [Agent Templates](/examples/reference/agents/)
- **API Reference:** [LoomService API](/docs/reference/api.md)
- **Advanced:** [Multi-Agent Workflows](/docs/guides/multi-agent-workflows.md)

---

**Version:** v1.1.0-beta
**Last Updated:** 2026-03-15

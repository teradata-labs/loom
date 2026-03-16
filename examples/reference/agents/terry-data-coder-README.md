# Terry Data Coder Agent

Teradata workflow planning and execution agent with project workspace awareness.

## Key Features

✅ **Workspace-Aware**: Understands and uses `project_path` from context
✅ **Dual Modes**: Supports both workflow planning and execution
✅ **Permission Control**: Respects `permission_mode` for plan vs execute
✅ **Context Variables**: Interpolates workspace context into prompts

## Message Structure

The agent expects this structure:

```json
{
  "agent_id": "terry-data-coder",
  "query": "<user's question or request>",
  "session_id": "<unique-conversation-id>",
  "context": {
    "mode": "workflow_planning" | "execution",
    "workspace_id": "<workspace-id>",
    "project_path": "/absolute/path/to/user/workspace",
    "permission_mode": "plan" | "execute",
    "workflow_state": {
      // Optional workflow tracking data
    }
  },
  "permission_mode": 3  // 2=AUTO_ACCEPT, 3=PLAN
}
```

## Context Variables

### How It Works

Context variables passed in the `context` field are interpolated into the agent's system prompt using Go template syntax:

**In the agent YAML:**
```yaml
system_prompt: |
  You are working in project: {{.project_path}}
  Current mode: {{.mode}}
  Workspace ID: {{.workspace_id}}
```

**At runtime with this request:**
```json
{
  "context": {
    "project_path": "/home/user/myproject",
    "mode": "workflow_planning",
    "workspace_id": "ws-123"
  }
}
```

**The agent sees:**
```
You are working in project: /home/user/myproject
Current mode: workflow_planning
Workspace ID: ws-123
```

### Available Context Variables

- **`project_path`**: Absolute path to user's project workspace
  - Agent will use this as the base directory for all file operations
  - Example: `/Users/josh.schoen/projects/analytics`

- **`workspace_id`**: Unique identifier for the workspace
  - Example: `analytics-workspace-42`

- **`mode`**: Operation mode
  - `workflow_planning`: Design and plan workflows
  - `execution`: Execute workflows

- **`permission_mode`**: Permission level
  - `plan`: Create execution plans for approval (use with permission_mode=3)
  - `execute`: Execute immediately (use with permission_mode=2)

- **`workflow_state`**: Custom workflow tracking data
  - Free-form JSON object for workflow state management

## Usage Examples

### Example 1: Workflow Planning

Create a plan without executing:

```bash
grpcurl -plaintext -d '{
  "agent_id": "terry-data-coder",
  "query": "Create a data pipeline for customer analytics",
  "context": {
    "mode": "workflow_planning",
    "project_path": "/Users/josh/analytics-project",
    "permission_mode": "plan"
  },
  "permission_mode": 3
}' localhost:50051 loom.v1.LoomService/Weave
```

**Expected behavior:**
1. Agent acknowledges: "I see you're working in /Users/josh/analytics-project"
2. Creates a detailed execution plan
3. Lists files that will be created in the project
4. Returns plan for approval (no tools executed)

### Example 2: Execute Approved Plan

After approving the plan:

```bash
# Get the plan ID from ListPlans
PLAN_ID="..."

# Approve it
grpcurl -plaintext -d '{"plan_id": "'$PLAN_ID'", "approved": true}' \
  localhost:50051 loom.v1.LoomService/ApprovePlan

# Execute it
grpcurl -plaintext -d '{"plan_id": "'$PLAN_ID'"}' \
  localhost:50051 loom.v1.LoomService/ExecutePlan
```

### Example 3: Direct Execution (YOLO Mode)

Execute immediately without planning:

```bash
grpcurl -plaintext -d '{
  "agent_id": "terry-data-coder",
  "query": "List all SQL files in the project",
  "context": {
    "project_path": "/Users/josh/analytics-project",
    "mode": "execution"
  },
  "permission_mode": 2
}' localhost:50051 loom.v1.LoomService/Weave
```

## Permission Modes

| Mode | Value | Behavior |
|------|-------|----------|
| AUTO_ACCEPT | 2 | Execute tools immediately |
| PLAN | 3 | Create execution plan for approval |

Use **PLAN mode** for:
- First-time workflows
- Sensitive operations
- Learning what the agent will do

Use **AUTO_ACCEPT mode** for:
- Trusted workflows
- Quick queries
- Iterative development

## Project Path Handling

The agent is programmed to:

✅ Use `project_path` as the base directory for all operations
✅ Create files/directories within this path
✅ Respect existing project structure
✅ Never operate outside the project boundary
✅ Check if files exist before overwriting
✅ Use relative paths for clarity in responses

**Example:**

If `project_path: /Users/josh/analytics`, the agent will:
- Create files in `/Users/josh/analytics/pipelines/pipeline.sql`
- Reference them as `pipelines/pipeline.sql` in responses
- Never write to `/tmp` or other directories outside the project

## Testing the Agent

Run the example script:

```bash
./terry-data-coder-example.sh
```

Or test manually:

```bash
# 1. Start the server
./bin/looms serve --port=50051

# 2. Send a request
grpcurl -plaintext -d '{
  "agent_id": "terry-data-coder",
  "query": "Help me set up a Teradata data pipeline",
  "context": {
    "project_path": "/Users/josh/test-project",
    "mode": "workflow_planning"
  },
  "permission_mode": 3
}' localhost:50051 loom.v1.LoomService/Weave
```

## Troubleshooting

### "query cannot be empty"
Make sure you're sending `query` not `message`:
```json
{"query": "...", "context": {...}}  // ✅ Correct
{"message": "...", "context": {...}}  // ❌ Wrong
```

### Context variables not interpolated
Context interpolation happens at runtime. The variables must be passed in the `context` field of the WeaveRequest.

### Agent not using project_path
Check that:
1. `project_path` is in the `context` field
2. The system prompt references `{{.project_path}}`
3. The agent is actually terry-data-coder (check agent_id)

## See Also

- [Runtime Permission Modes Guide](../../../docs/guides/testing-permission-modes.md)
- [Agent Configuration Reference](../../../docs/reference/agent-configuration.md)
- [Context Variables Documentation](../../../docs/guides/context-variables.md)

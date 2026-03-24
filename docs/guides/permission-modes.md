# Permission Modes Guide

**Version:** v1.1.0-beta
**Status:** ✅ Available (fully working with tests)

## Table of Contents

- [Overview](#overview)
- [Quick Start](#quick-start)
- [Available Modes](#available-modes)
- [Plan Management Workflow](#plan-management-workflow)
- [Mode Switching](#mode-switching)
- [Complete Examples](#complete-examples)
- [Troubleshooting](#troubleshooting)
- [API Reference](#api-reference)

## Overview

Permission modes give you runtime control over how agents execute tools. Instead of hardcoding behavior in agent configuration, you can specify the execution strategy per-request.

> **Implementation Note:** Permission modes are enabled by default in all agents (v1.1.0+). Every agent automatically receives a default permission checker with AUTO_ACCEPT mode, which can be switched at runtime via the `permission_mode` field in WeaveRequest. See [Permission Modes Architecture](/docs/architecture/permission-modes.md) for implementation details.

**Use permission modes to:**
- Get user approval before executing critical operations (PLAN mode)
- Execute read-only operations immediately (AUTO_ACCEPT mode)
- Switch between planning and execution in the same conversation
- Implement compliance requirements (audit all actions before execution)

### Available Modes

| Mode | Value | Behavior | When to Use |
|------|-------|----------|-------------|
| **PLAN** | 3 | Create plan → wait for approval → execute | Critical ops, compliance, training |
| **AUTO_ACCEPT** | 2 | Execute immediately (YOLO mode) | Safe ops, development, trusted agents |
| **ASK_BEFORE** | 1 | Request approval per tool | ⚠️ Not fully implemented |
| **UNSPECIFIED** | 0 | Use agent's default config | Legacy compatibility |

## Quick Start

### 1. Start the Server

```bash
# Build and start
just build
./bin/looms serve --port=50051
```

### 2. Test PLAN Mode (Creates Plan First)

```bash
grpcurl -plaintext -d '{
  "query": "Delete all temporary files",
  "permission_mode": 3
}' localhost:50051 loom.v1.LoomService/Weave
```

**Expected response:**
```json
{
  "text": "I've created an execution plan with 1 step. Please review and approve the plan to proceed.",
  "sessionId": "session-abc123",
  "plan": {
    "planId": "plan-xyz789",
    "status": "PLAN_STATUS_PENDING",
    "tools": [
      {
        "step": 1,
        "toolName": "shell_execute",
        "paramsJson": "{\"command\":\"rm -f /tmp/*.tmp\"}",
        "rationale": "Delete temporary files matching *.tmp pattern"
      }
    ]
  }
}
```

### 3. Test AUTO_ACCEPT Mode (Executes Immediately)

```bash
grpcurl -plaintext -d '{
  "query": "What files are in /tmp?",
  "permission_mode": 2
}' localhost:50051 loom.v1.LoomService/Weave
```

**Expected response:**
```json
{
  "text": "There are 15 files in /tmp:\n- file1.txt\n- file2.log\n...",
  "sessionId": "session-def456"
}
```

**Key difference:** Mode 3 returns a plan to review. Mode 2 returns actual results immediately.

## Available Modes

### PLAN Mode (3) - Create Plan First

**Best for:**
- Destructive operations (delete, modify, deploy)
- Financial transactions
- Production changes
- Compliance/audit requirements
- Educational scenarios (show what agent will do)

**How it works:**

1. Agent receives query
2. LLM generates tool calls
3. **Instead of executing**, creates `ExecutionPlan`
4. Returns plan to user with:
   - Each tool that would be executed
   - Parameters for each tool
   - Rationale for each step
   - Dependencies between steps
5. User reviews and approves/rejects
6. Only approved plans are executed

**Example:**

```bash
# Request creates a plan
grpcurl -plaintext -d '{
  "query": "Deploy new version to production",
  "permission_mode": 3
}' localhost:50051 loom.v1.LoomService/Weave
```

**Response includes plan:**
```json
{
  "plan": {
    "planId": "plan-123",
    "query": "Deploy new version to production",
    "tools": [
      {
        "step": 1,
        "toolName": "run_tests",
        "rationale": "Verify all tests pass before deployment"
      },
      {
        "step": 2,
        "toolName": "build_docker_image",
        "rationale": "Create production Docker image",
        "dependsOn": [1]
      },
      {
        "step": 3,
        "toolName": "deploy_to_kubernetes",
        "rationale": "Deploy to production cluster",
        "dependsOn": [2]
      }
    ],
    "reasoning": "Safe deployment workflow: test → build → deploy",
    "status": "PLAN_STATUS_PENDING"
  }
}
```

### AUTO_ACCEPT Mode (2) - Execute Immediately

**Best for:**
- Read-only operations
- Development/testing environments
- Trusted agents with safe tools
- Quick queries that don't modify state
- "YOLO mode" for power users

**How it works:**

1. Agent receives query
2. LLM generates tool calls
3. Tools execute immediately
4. Results returned directly

**Example:**

```bash
grpcurl -plaintext -d '{
  "query": "Show me the status of all running containers",
  "permission_mode": 2
}' localhost:50051 loom.v1.LoomService/Weave
```

**Response has results:**
```json
{
  "text": "Found 5 running containers:\n1. web-server (nginx) - healthy\n2. api-server (node) - healthy\n...",
  "sessionId": "session-xyz"
}
```

### ASK_BEFORE Mode (1) - Request Approval Per Tool

**Status:** ⚠️ Partial - Infrastructure exists, callback mechanism not yet implemented

**Planned behavior:**
1. Agent requests permission before EACH tool execution
2. User approves/denies via callback
3. Agent only executes approved tools
4. Rejected tools are skipped

**Current behavior:**
- Falls back to agent's `default_action` configuration
- Returns error explaining callback not implemented
- Suggests using AUTO_ACCEPT or PLAN mode instead

**Error message:**
```
tool 'shell_execute' requires user approval (permission_mode=ASK_BEFORE)
but permission request mechanism is not yet implemented.
To bypass: use permission_mode=AUTO_ACCEPT or add 'shell_execute' to tools.permissions.allowed_tools
```

### UNSPECIFIED Mode (0) - Use Agent Default

**How it works:**
- Uses agent's configuration from YAML
- Legacy flags mapped to modes:
  - `yolo: true` → AUTO_ACCEPT
  - `require_approval: true` → ASK_BEFORE
  - Neither flag → AUTO_ACCEPT (default)

**Example agent config:**
```yaml
spec:
  tools:
    permissions:
      yolo: true  # Maps to AUTO_ACCEPT mode
```

**When to use:**
- Backward compatibility with existing agents
- Agent should decide its own behavior
- Don't need per-request control

## Plan Management Workflow

When using PLAN mode, follow this workflow to review and execute plans.

### 1. Create Plan

Request with `permission_mode: 3` creates a plan instead of executing:

```bash
RESPONSE=$(grpcurl -plaintext -d '{
  "query": "Analyze code quality and fix issues",
  "permission_mode": 3
}' localhost:50051 loom.v1.LoomService/Weave)

# Extract session ID for later use
SESSION_ID=$(echo "$RESPONSE" | grep sessionId | sed 's/.*"sessionId"[^"]*"\([^"]*\)".*/\1/')
echo "Session: $SESSION_ID"
```

### 2. List Plans for Session

View all plans created in a session:

```bash
grpcurl -plaintext -d "{\"session_id\": \"$SESSION_ID\"}" \
  localhost:50051 loom.v1.LoomService/ListPlans
```

**Response:**
```json
{
  "plans": [
    {
      "planId": "plan-abc123",
      "status": "PLAN_STATUS_PENDING",
      "createdAt": "1710532800",
      "tools": [...]
    }
  ],
  "totalCount": 1
}
```

### 3. Get Specific Plan Details

Review a plan before approving:

```bash
PLAN_ID="plan-abc123"

grpcurl -plaintext -d "{\"plan_id\": \"$PLAN_ID\"}" \
  localhost:50051 loom.v1.LoomService/GetPlan
```

**Response includes:**
- `query` - Original user query
- `tools[]` - All planned tool executions
- `reasoning` - Agent's explanation
- `status` - Current status (PENDING, APPROVED, REJECTED, etc.)

### 4. Approve Plan

Approve the plan to allow execution:

```bash
grpcurl -plaintext -d "{
  \"plan_id\": \"$PLAN_ID\",
  \"approved\": true,
  \"feedback\": \"Looks good, proceed with execution\"
}" localhost:50051 loom.v1.LoomService/ApprovePlan
```

**Response:**
```json
{
  "plan": {
    "planId": "plan-abc123",
    "status": "PLAN_STATUS_APPROVED",
    "updatedAt": "1710532900"
  }
}
```

### 5. Execute Approved Plan

Execute the approved plan:

```bash
grpcurl -plaintext -d "{\"plan_id\": \"$PLAN_ID\"}" \
  localhost:50051 loom.v1.LoomService/ExecutePlan
```

**Response:**
```json
{
  "plan": {
    "planId": "plan-abc123",
    "status": "PLAN_STATUS_COMPLETED",
    "tools": [
      {
        "step": 1,
        "status": "STEP_STATUS_COMPLETED",
        "result": "Test results: 15/15 passed",
        "error": ""
      }
    ]
  },
  "summary": "Successfully executed 3 steps"
}
```

### 6. Reject Plan (Alternative)

Reject a plan if you don't want it executed:

```bash
grpcurl -plaintext -d "{
  \"plan_id\": \"$PLAN_ID\",
  \"approved\": false,
  \"feedback\": \"Too risky, let's try a different approach\"
}" localhost:50051 loom.v1.LoomService/ApprovePlan
```

**Response:**
```json
{
  "plan": {
    "planId": "plan-abc123",
    "status": "PLAN_STATUS_REJECTED"
  }
}
```

### Complete Workflow Script

Copy-paste this script to test the full workflow:

```bash
#!/bin/bash
PORT=50051

# 1. Create plan
echo "Creating plan..."
RESPONSE=$(grpcurl -plaintext -d '{
  "query": "List files in current directory",
  "permission_mode": 3
}' localhost:$PORT loom.v1.LoomService/Weave)

SESSION_ID=$(echo "$RESPONSE" | grep sessionId | sed 's/.*"sessionId"[^"]*"\([^"]*\)".*/\1/')
echo "✅ Session: $SESSION_ID"

# 2. Get plan ID
LIST_RESPONSE=$(grpcurl -plaintext -d "{\"session_id\": \"$SESSION_ID\"}" \
  localhost:$PORT loom.v1.LoomService/ListPlans)
PLAN_ID=$(echo "$LIST_RESPONSE" | grep planId | head -1 | sed 's/.*"planId"[^"]*"\([^"]*\)".*/\1/')
echo "✅ Plan: $PLAN_ID"

# 3. Review plan
echo "\nPlan details:"
grpcurl -plaintext -d "{\"plan_id\": \"$PLAN_ID\"}" \
  localhost:$PORT loom.v1.LoomService/GetPlan | grep -A 5 "tools"

# 4. Approve plan
echo "\nApproving plan..."
grpcurl -plaintext -d "{\"plan_id\": \"$PLAN_ID\", \"approved\": true}" \
  localhost:$PORT loom.v1.LoomService/ApprovePlan > /dev/null
echo "✅ Plan approved"

# 5. Execute plan
echo "Executing plan..."
grpcurl -plaintext -d "{\"plan_id\": \"$PLAN_ID\"}" \
  localhost:$PORT loom.v1.LoomService/ExecutePlan > /dev/null
echo "✅ Plan executed"

echo "\n🎉 Complete workflow finished!"
```

## Mode Switching

You can switch permission modes mid-conversation for flexible workflows.

### Example: PLAN → AUTO_ACCEPT

**Scenario:** Start with planning for critical operation, switch to auto-execute for follow-up queries.

```bash
# Start session in PLAN mode
SESSION_ID=$(grpcurl -plaintext -d '{
  "query": "Delete all log files older than 30 days",
  "permission_mode": 3
}' localhost:50051 loom.v1.LoomService/Weave | grep sessionId | sed 's/.*"\([^"]*\)".*/\1/')

# ... approve and execute plan ...

# Switch to AUTO_ACCEPT for follow-up
grpcurl -plaintext -d "{
  \"session_id\": \"$SESSION_ID\",
  \"query\": \"How much space did we free up?\",
  \"permission_mode\": 2
}" localhost:50051 loom.v1.LoomService/Weave
```

**Result:** First request creates plan. After approval, follow-up executes immediately.

### Example: AUTO_ACCEPT → PLAN

**Scenario:** Start with quick queries, switch to planning for destructive operation.

```bash
# Quick queries in AUTO_ACCEPT
SESSION_ID=$(grpcurl -plaintext -d '{
  "query": "Show disk usage",
  "permission_mode": 2
}' localhost:50051 loom.v1.LoomService/Weave | grep sessionId | sed 's/.*"\([^"]*\)".*/\1/')

grpcurl -plaintext -d "{
  \"session_id\": \"$SESSION_ID\",
  \"query\": \"What's the largest file?\",
  \"permission_mode\": 2
}" localhost:50051 loom.v1.LoomService/Weave

# Switch to PLAN mode for deletion
grpcurl -plaintext -d "{
  \"session_id\": \"$SESSION_ID\",
  \"query\": \"Delete the largest file\",
  \"permission_mode\": 3
}" localhost:50051 loom.v1.LoomService/Weave
```

**Result:** Analysis queries execute immediately, deletion requires approval.

### Mode Persistence

**Important:** Permission mode is set per-request, NOT per-session.

```bash
# Request 1: PLAN mode
grpcurl -d '{"session_id": "abc", "permission_mode": 3, ...}' ...

# Request 2: Still need to specify mode (doesn't inherit from request 1)
grpcurl -d '{"session_id": "abc", "permission_mode": 2, ...}' ...
```

**Best practice:** Always specify `permission_mode` in each request for clarity.

## Complete Examples

### Example 1: Code Review with Plan Mode

**Scenario:** Review code quality and suggest fixes, but don't auto-apply changes.

```bash
# Create review plan
grpcurl -plaintext -d '{
  "query": "Review code quality in src/ directory and suggest improvements",
  "permission_mode": 3
}' localhost:50051 loom.v1.LoomService/Weave
```

**Agent creates plan:**
```json
{
  "plan": {
    "tools": [
      {
        "step": 1,
        "toolName": "shell_execute",
        "paramsJson": "{\"command\":\"pylint src/\"}",
        "rationale": "Run linter to identify code quality issues"
      },
      {
        "step": 2,
        "toolName": "file_read",
        "paramsJson": "{\"path\":\"src/main.py\"}",
        "rationale": "Read problematic files to understand issues"
      },
      {
        "step": 3,
        "toolName": "file_write",
        "paramsJson": "{\"path\":\"src/main.py\",\"content\":\"...\"}",
        "rationale": "Apply recommended fixes",
        "dependsOn": [2]
      }
    ],
    "reasoning": "Will lint code, review issues, and apply fixes only after approval"
  }
}
```

**User reviews plan, decides:**
- ✅ Steps 1-2 (lint and read) are safe → approve
- ❌ Step 3 (auto-fix) is too aggressive → modify or reject plan

### Example 2: Development Workflow

**Scenario:** Rapid iteration in development, careful execution in production.

**Agent configuration includes environment awareness:**

```yaml
metadata:
  name: deploy-agent
spec:
  systemPrompt: |
    You are a deployment assistant.
    Environment: {{.environment}}

    {{if eq .environment "production"}}
    PRODUCTION: Always create plans for review.
    {{else}}
    DEVELOPMENT: Execute immediately for fast iteration.
    {{end}}
```

**Development request:**
```bash
grpcurl -d '{
  "agent_id": "deploy-agent",
  "query": "Deploy latest changes",
  "permission_mode": 2,
  "context": {"environment": "development"}
}' localhost:50051 loom.v1.LoomService/Weave
# Executes immediately
```

**Production request:**
```bash
grpcurl -d '{
  "agent_id": "deploy-agent",
  "query": "Deploy latest changes",
  "permission_mode": 3,
  "context": {"environment": "production"}
}' localhost:50051 loom.v1.LoomService/Weave
# Creates plan for approval
```

### Example 3: Compliance Audit Trail

**Scenario:** Regulatory requirement to log all actions before execution.

```bash
# All operations in PLAN mode
grpcurl -d '{
  "query": "Update customer credit limit to $50,000",
  "permission_mode": 3,
  "context": {
    "user_id": "user-123",
    "customer_id": "cust-456"
  }
}' localhost:50051 loom.v1.LoomService/Weave

# Plan logged to audit trail (external system)
# Manager reviews and approves via UI
# Execution logged with approval metadata
```

**Audit trail includes:**
- Who requested the action
- What action was planned
- Who approved it
- When it was executed
- What the result was

## Troubleshooting

### Permission Mode Not Working

**Symptom:** All requests behave the same regardless of `permission_mode`.

**Cause 1: Permission mode in wrong location**

```bash
❌ WRONG - Inside context (just a string for prompt interpolation):
grpcurl -d '{
  "query": "...",
  "context": {
    "permission_mode": "execute"
  }
}' ...

✅ CORRECT - Top-level field (integer enum):
grpcurl -d '{
  "query": "...",
  "permission_mode": 2,
  "context": {"workspace": "..."}
}' ...
```

**Cause 2: Wrong value type**

```bash
❌ WRONG: "permission_mode": "AUTO_ACCEPT"  # String
❌ WRONG: "permission_mode": "2"            # String number
✅ CORRECT: "permission_mode": 2            # Integer
```

**Debug:**
```bash
# Check your JSON is valid
echo '{"permission_mode": 2}' | jq .permission_mode
# Should output: 2 (number, not "2" string)
```

### Plan Not Created in PLAN Mode

**Symptom:** Tools execute immediately even with `permission_mode: 3`.

**Cause 1: Query doesn't require tools**

```bash
❌ No tools needed:
"query": "What is 2+2?"  # Agent can answer directly

✅ Tools required:
"query": "List files in /tmp"  # Needs shell_execute tool
```

**Cause 2: Permission mode not set**

```bash
# Verify mode is in request
grpcurl -d '{"query": "...", "permission_mode": 3}' ... | jq .
```

**Debug:** Check server logs for:
```
DEBUG: In PLAN mode - creating execution plan tool_count=2
```

### Plan Execution Failed

**Symptom:** ExecutePlan returns error.

**Cause 1: Plan not approved**

```bash
# Check plan status
grpcurl -d '{"plan_id": "..."}' localhost:50051 loom.v1.LoomService/GetPlan | grep status

# Should be PLAN_STATUS_APPROVED before execution
# If PLAN_STATUS_PENDING, approve first
```

**Cause 2: Plan already executed**

```bash
# Plans can only be executed once
# Status will be PLAN_STATUS_COMPLETED or PLAN_STATUS_FAILED

# Create new plan for retry:
grpcurl -d '{"query": "...", "permission_mode": 3}' ...
```

### Mode Switching Not Working

**Symptom:** Can't switch from PLAN to AUTO_ACCEPT in same session.

**This should work!** If it doesn't:

```bash
# Verify session ID is consistent
echo "Session: $SESSION_ID"

# Verify mode is different in each request
grpcurl -d "{
  \"session_id\": \"$SESSION_ID\",
  \"permission_mode\": 2
}" ... | jq .
```

**Debug:** Check logs for:
```
DEBUG: Setting permission mode mode=PERMISSION_MODE_AUTO_ACCEPT
```

## API Reference

### WeaveRequest

```protobuf
message WeaveRequest {
  string query = 1;
  string session_id = 2;
  PermissionMode permission_mode = 10;  // Runtime control
  // ... other fields
}
```

### PermissionMode Enum

```protobuf
enum PermissionMode {
  PERMISSION_MODE_UNSPECIFIED = 0;  // Use agent default
  PERMISSION_MODE_ASK_BEFORE = 1;   // Ask per tool (not implemented)
  PERMISSION_MODE_AUTO_ACCEPT = 2;  // Execute immediately
  PERMISSION_MODE_PLAN = 3;         // Create plan first
}
```

### ExecutionPlan

```protobuf
message ExecutionPlan {
  string plan_id = 1;
  string session_id = 2;
  string query = 3;
  repeated PlannedToolExecution tools = 4;
  string reasoning = 5;
  PlanStatus status = 6;
  int64 created_at = 7;
  int64 updated_at = 8;
}
```

### PlanStatus Enum

```protobuf
enum PlanStatus {
  PLAN_STATUS_UNSPECIFIED = 0;
  PLAN_STATUS_PENDING = 1;     // Awaiting approval
  PLAN_STATUS_APPROVED = 2;    // Approved, ready to execute
  PLAN_STATUS_REJECTED = 3;    // User rejected
  PLAN_STATUS_EXECUTING = 4;   // Currently executing
  PLAN_STATUS_COMPLETED = 5;   // Successfully completed
  PLAN_STATUS_FAILED = 6;      // Execution failed
}
```

### Plan Management RPCs

```protobuf
// Approve or reject a plan
rpc ApprovePlan(ApprovePlanRequest) returns (ApprovePlanResponse);

// Get plan details
rpc GetPlan(GetPlanRequest) returns (ExecutionPlan);

// List plans for a session
rpc ListPlans(ListPlansRequest) returns (ListPlansResponse);

// Execute an approved plan
rpc ExecutePlan(ExecutePlanRequest) returns (ExecutePlanResponse);
```

---

## Next Steps

- **Architecture:** [Permission Modes Architecture](/docs/architecture/permission-modes.md) - Design decisions and implementation details
- **Context Variables:** [Context Variable Interpolation Guide](/docs/guides/context-variables.md)
- **Testing:** [Testing Permission Modes](/docs/guides/testing-permission-modes.md)
- **API Reference:** [LoomService API](/docs/reference/api.md)
- **Examples:** [Agent Templates](/examples/reference/agents/)

---

**Version:** v1.1.0-beta
**Last Updated:** 2026-03-22

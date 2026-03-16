#!/bin/bash
# Example: Calling terry-data-coder agent with workspace context

PORT=50051

# Example 1: Workflow Planning Mode with Project Path
echo "=== Example 1: Workflow Planning ==="
grpcurl -plaintext -d '{
  "agent_id": "terry-data-coder",
  "query": "Create a data pipeline to analyze customer purchase patterns from our Teradata warehouse",
  "session_id": "workflow-session-001",
  "context": {
    "mode": "workflow_planning",
    "workspace_id": "analytics-workspace-42",
    "project_path": "/Users/josh.schoen/projects/customer-analytics",
    "permission_mode": "plan",
    "workflow_state": {
      "phase": "design",
      "iteration": 1
    }
  },
  "permission_mode": 3
}' localhost:$PORT loom.v1.LoomService/Weave

echo ""
echo "=== Example 2: Execution Mode ==="
# After approving the plan, execute it
grpcurl -plaintext -d '{
  "agent_id": "terry-data-coder",
  "query": "Execute the approved customer analytics pipeline",
  "session_id": "workflow-session-001",
  "context": {
    "mode": "execution",
    "workspace_id": "analytics-workspace-42",
    "project_path": "/Users/josh.schoen/projects/customer-analytics",
    "permission_mode": "execute",
    "workflow_state": {
      "phase": "execution",
      "plan_id": "plan-12345"
    }
  },
  "permission_mode": 2
}' localhost:$PORT loom.v1.LoomService/Weave

echo ""
echo "=== Example 3: Quick Task (Auto-Execute) ==="
# Quick task without planning mode
grpcurl -plaintext -d '{
  "agent_id": "terry-data-coder",
  "query": "List all SQL files in the project",
  "context": {
    "project_path": "/Users/josh.schoen/projects/customer-analytics",
    "mode": "execution"
  },
  "permission_mode": 2
}' localhost:$PORT loom.v1.LoomService/Weave

echo ""
echo "=== Key Points ==="
echo "1. project_path is passed via context field"
echo "2. Context variables are interpolated in system prompt using {{.project_path}}"
echo "3. permission_mode controls plan vs execute behavior"
echo "4. mode in context tells the agent whether to plan or execute"
echo "5. The agent is aware of and will use the project_path for all file operations"

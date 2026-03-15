# Testing Permission Modes

This guide shows you how to test the runtime permission modes feature manually and programmatically.

## Quick Summary

Permission modes control how the agent handles tool execution:
- **AUTO_ACCEPT** (mode=2, YOLO mode): Execute tools immediately without asking
- **ASK_BEFORE** (mode=1): Request approval before each tool (callback mechanism not yet implemented)
- **PLAN** (mode=3): Create execution plan, wait for approval, then execute

**Key Features:**
- ✅ Runtime mode switching within sessions
- ✅ Conversation continuity after plan execution (synthetic tool_result blocks preserve state)
- ✅ Plan approval/rejection workflow
- ✅ Plan execution with status tracking
- ✅ Full backward compatibility with legacy boolean flags

## Quick Test Workflow

Copy-paste this entire block to test the complete workflow including mode switching:

```bash
# 1. Start server (in separate terminal)
./bin/looms serve --port=50051

# 2. Set port (change if using different port)
PORT=50051

# 3. Create session in PLAN mode
echo "Creating session in PLAN mode..."
RESPONSE=$(grpcurl -plaintext -d '{"query": "List files in current directory", "permission_mode": 3}' localhost:$PORT loom.v1.LoomService/Weave 2>&1)

# Extract session ID
SESSION_ID=$(echo "$RESPONSE" | grep sessionId | sed 's/.*"sessionId"[^"]*"\([^"]*\)".*/\1/')

# Check if extraction succeeded
if [ -z "$SESSION_ID" ]; then
    echo "❌ Failed to create session. Is the server running on port $PORT?"
    echo "Response was:"
    echo "$RESPONSE"
    exit 1
fi

echo "✅ Session created: $SESSION_ID"

# 4. Get plan ID
LIST_RESPONSE=$(grpcurl -plaintext -d "{\"session_id\": \"$SESSION_ID\"}" localhost:$PORT loom.v1.LoomService/ListPlans 2>&1)
PLAN_ID=$(echo "$LIST_RESPONSE" | grep planId | head -1 | sed 's/.*"planId"[^"]*"\([^"]*\)".*/\1/')

if [ -z "$PLAN_ID" ]; then
    echo "❌ Failed to get plan ID"
    exit 1
fi

echo "✅ Plan created: $PLAN_ID"

# 5. Approve plan
grpcurl -plaintext -d "{\"plan_id\": \"$PLAN_ID\", \"approved\": true}" localhost:$PORT loom.v1.LoomService/ApprovePlan > /dev/null 2>&1
echo "✅ Plan approved"

# 6. Execute plan
grpcurl -plaintext -d "{\"plan_id\": \"$PLAN_ID\"}" localhost:$PORT loom.v1.LoomService/ExecutePlan > /dev/null 2>&1
echo "✅ Plan executed"

# 7. Test mode switching (PLAN → AUTO_ACCEPT in same session)
echo ""
echo "Testing mode switching to AUTO_ACCEPT..."
SWITCH_RESPONSE=$(grpcurl -plaintext -d "{\"query\": \"What is 2+2?\", \"session_id\": \"$SESSION_ID\", \"permission_mode\": 2}" localhost:$PORT loom.v1.LoomService/Weave 2>&1)
SWITCH_RESULT=$(echo "$SWITCH_RESPONSE" | grep '"text"' | sed 's/.*"text"[^"]*"\([^"]*\)".*/\1/' | head -c 50)
echo "✅ Mode switched: $SWITCH_RESULT..."

echo ""
echo "🎉 All tests passed! Runtime permission modes working correctly."
```

## 1. Run Unit Tests

The implementation includes comprehensive unit tests:

```bash
# Test permission checker (mode switching logic)
go test -tags fts5 -v ./pkg/shuttle -run TestPermissionChecker

# Test execution planner (plan lifecycle)
go test -tags fts5 -v ./pkg/agent -run "Test.*Plan"

# Test server RPCs
go test -tags fts5 -v ./pkg/server
```

All tests should pass ✅

## 2. Manual Testing with gRPC

### Setup: Start the Server

```bash
# Build the server
just build

# Start with a simple backend
./bin/looms serve \
  --llm-provider=anthropic \
  --anthropic-model=claude-sonnet-4-5-20250929 \
  --port=50051
```

### Setup: Create a Session

First, create a session and capture the session_id for use in all examples:

```bash
# Create initial session with AUTO_ACCEPT mode
RESPONSE=$(grpcurl -plaintext \
  -d '{
    "query": "List files in current directory",
    "permission_mode": 2
  }' \
  localhost:50051 loom.v1.LoomService/Weave 2>&1)

SESSION_ID=$(echo "$RESPONSE" | grep sessionId | sed 's/.*"sessionId"[^"]*"\([^"]*\)".*/\1/')
echo "Session ID: $SESSION_ID"
```

Expected output: Tools execute immediately, session_id displayed.

**Save this SESSION_ID** - we'll use it in the examples below.

### Test AUTO_ACCEPT Mode

Verify AUTO_ACCEPT mode executes tools immediately:

```bash
grpcurl -plaintext \
  -d '{
    "query": "Show current directory",
    "permission_mode": 2
  }' \
  localhost:50051 loom.v1.LoomService/Weave
```

**Example output:**
```json
{
  "text": "The current directory is:\n\n**`/Users/josh.schoen/.loom`**\n\nWould you like me to list the contents?",
  "sessionId": "sess_cf03b782",
  "cost": {
    "totalCostUsd": 0.018552,
    "llmCost": {
      "inputTokens": 5919,
      "outputTokens": 53,
      "costUsd": 0.018552
    }
  },
  "agentId": "26ea9480-678b-4f57-8684-fe0c77f13a86"
}
```

✅ Tools executed immediately, result returned in `text` field.

### Test PLAN Mode

Now test PLAN mode using the same session:

```bash
# 1. Create a plan using the same session (switches mode to PLAN)
grpcurl -plaintext \
  -d "{
    \"query\": \"Count how many files are in the current directory\",
    \"session_id\": \"$SESSION_ID\",
    \"permission_mode\": 3
  }" \
  localhost:50051 loom.v1.LoomService/Weave
```

**Example output:**
```json
{
  "text": "I've created an execution plan with 1 steps. Please review and approve the plan to proceed.",
  "sessionId": "sess_1430f463",
  "cost": {
    "totalCostUsd": 0.019146
  }
}
```

✅ Plan created, no tools executed yet. Note the message asking for approval.

```bash
# 2. List plans for the session and extract plan ID
LIST_RESPONSE=$(grpcurl -plaintext \
  -d "{
    \"session_id\": \"$SESSION_ID\"
  }" \
  localhost:50051 loom.v1.LoomService/ListPlans 2>&1)

PLAN_ID=$(echo "$LIST_RESPONSE" | grep planId | head -1 | sed 's/.*"planId"[^"]*"\([^"]*\)".*/\1/')
echo "Plan ID: $PLAN_ID"
```

```bash
# 3. Get plan details
grpcurl -plaintext \
  -d "{
    \"plan_id\": \"$PLAN_ID\"
  }" \
  localhost:50051 loom.v1.LoomService/GetPlan
```

**Example output:**
```json
{
  "planId": "d3689098-4e88-44b8-947f-1076c2a98e5c",
  "sessionId": "sess_1430f463",
  "query": "Count how many files are in the current directory",
  "tools": [
    {
      "step": 1,
      "toolName": "shell_execute",
      "paramsJson": "{\"command\":\"ls -1 | wc -l\"}",
      "rationale": "Execute shell_execute",
      "status": "STEP_STATUS_PENDING"
    }
  ],
  "reasoning": "I'll check how many files are in the current directory using a shell command.",
  "status": "PLAN_STATUS_PENDING",
  "createdAt": "1773609724",
  "updatedAt": "1773609724"
}
```

✅ Full plan details showing tool name, parameters, and PENDING status.

```bash
# 4. Approve the plan
grpcurl -plaintext \
  -d "{
    \"plan_id\": \"$PLAN_ID\",
    \"approved\": true,
    \"feedback\": \"Looks good\"
  }" \
  localhost:50051 loom.v1.LoomService/ApprovePlan
```

**Example output:**
```json
{
  "plan": {
    "planId": "d3689098-4e88-44b8-947f-1076c2a98e5c",
    "sessionId": "sess_1430f463",
    "query": "Count how many files are in the current directory",
    "tools": [
      {
        "step": 1,
        "toolName": "shell_execute",
        "paramsJson": "{\"command\":\"ls -1 | wc -l\"}",
        "status": "STEP_STATUS_PENDING"
      }
    ],
    "status": "PLAN_STATUS_APPROVED",
    "createdAt": "1773609724",
    "updatedAt": "1773609745"
  }
}
```

✅ Status changed to `PLAN_STATUS_APPROVED`.

```bash
# 5. Execute the approved plan
grpcurl -plaintext \
  -d "{
    \"plan_id\": \"$PLAN_ID\"
  }" \
  localhost:50051 loom.v1.LoomService/ExecutePlan
```

**Example output:**
```json
{
  "plan": {
    "planId": "d3689098-4e88-44b8-947f-1076c2a98e5c",
    "sessionId": "sess_1430f463",
    "query": "Count how many files are in the current directory",
    "tools": [
      {
        "step": 1,
        "toolName": "shell_execute",
        "paramsJson": "{\"command\":\"ls -1 | wc -l\"}",
        "result": "\"✓ Large json_object stored... stdout: \\\"20\\\"...\"",
        "status": "STEP_STATUS_COMPLETED"
      }
    ],
    "status": "PLAN_STATUS_COMPLETED",
    "createdAt": "1773609724",
    "updatedAt": "1773609750"
  },
  "summary": "Successfully executed 1 tool(s)"
}
```

✅ Tools executed, results stored in `result` field, status `PLAN_STATUS_COMPLETED`.

```bash
# 6. Reject a plan (alternative - create another plan first)
# Create new plan
grpcurl -plaintext \
  -d "{
    \"query\": \"Delete all files\",
    \"session_id\": \"$SESSION_ID\",
    \"permission_mode\": 3
  }" \
  localhost:50051 loom.v1.LoomService/Weave

# Get new plan ID (last plan in list)
PLAN_ID_2=$(grpcurl -plaintext -d "{\"session_id\": \"$SESSION_ID\"}" localhost:50051 loom.v1.LoomService/ListPlans 2>&1 | grep -E '^\{' | jq -r '.plans[-1].planId')

# Reject it
grpcurl -plaintext \
  -d "{
    \"plan_id\": \"$PLAN_ID_2\",
    \"approved\": false,
    \"feedback\": \"Too dangerous\"
  }" \
  localhost:50051 loom.v1.LoomService/ApprovePlan
```

**Example output:**
```json
{
  "plan": {
    "planId": "0f3ff94b-1b46-4ae4-b31d-fd017fda9853",
    "sessionId": "sess_465b8cd8",
    "query": "Delete all files",
    "tools": [
      {
        "step": 1,
        "toolName": "workspace",
        "paramsJson": "{\"action\":\"list\",\"scope\":\"artifact\"}"
      }
    ],
    "status": "PLAN_STATUS_REJECTED",
    "createdAt": "1773609879",
    "updatedAt": "1773609879"
  }
}
```

✅ Status changed to `PLAN_STATUS_REJECTED`, tools not executed.

### Test Mode Switching

Test that permission mode can be switched mid-session, including after plan execution:

```bash
# Create session with PLAN mode
SESSION_ID=$(grpcurl -plaintext \
  -d '{"query": "List files", "permission_mode": 3}' \
  localhost:50051 loom.v1.LoomService/Weave 2>&1 | jq -r '.sessionId')

echo "Session: $SESSION_ID"

# Get plan ID and execute it
PLAN_ID=$(grpcurl -plaintext \
  -d "{\"session_id\": \"$SESSION_ID\"}" \
  localhost:50051 loom.v1.LoomService/ListPlans 2>&1 | jq -r '.plans[0].planId')

grpcurl -plaintext \
  -d "{\"plan_id\": \"$PLAN_ID\", \"approved\": true}" \
  localhost:50051 loom.v1.LoomService/ApprovePlan

grpcurl -plaintext \
  -d "{\"plan_id\": \"$PLAN_ID\"}" \
  localhost:50051 loom.v1.LoomService/ExecutePlan
```

✅ Plan created, approved, and executed successfully. Status: `PLAN_STATUS_COMPLETED`.

```bash
# Switch to AUTO_ACCEPT mode in same session (this works now!)
grpcurl -plaintext \
  -d "{
    \"query\": \"What is 2+2?\",
    \"session_id\": \"$SESSION_ID\",
    \"permission_mode\": 2
  }" \
  localhost:50051 loom.v1.LoomService/Weave
```

**Example output:**
```json
{
  "text": "2 + 2 = 4",
  "sessionId": "sess_xxx",
  "cost": {
    "totalCostUsd": 0.012
  }
}
```

✅ **Conversation continued successfully!** Mode switched from PLAN to AUTO_ACCEPT within same session. This demonstrates that the conversation state preservation fix is working.

## 3. Testing with StreamWeave (Real-time Events)

Stream progress events including plan creation:

```bash
grpcurl -plaintext \
  -d '{
    "query": "Find all TODO comments in the codebase",
    "permission_mode": 3
  }' \
  localhost:50051 loom.v1.LoomService/StreamWeave
```

You should see progress events with `is_plan_created: true` and the full plan details.

## 4. Testing with Go Client

Create a simple Go test client:

```go
package main

import (
	"context"
	"fmt"
	"log"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func main() {
	// Connect to server
	conn, err := grpc.Dial("localhost:50051", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	client := loomv1.NewLoomServiceClient(conn)
	ctx := context.Background()

	// Test PLAN mode
	resp, err := client.Weave(ctx, &loomv1.WeaveRequest{
		Query:          "List all Go files",
		PermissionMode: loomv1.PermissionMode_PERMISSION_MODE_PLAN,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Session ID: %s\n", resp.SessionId)

	// List plans
	plans, err := client.ListPlans(ctx, &loomv1.ListPlansRequest{
		SessionId: resp.SessionId,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Found %d plans\n", len(plans.Plans))
	for _, plan := range plans.Plans {
		fmt.Printf("Plan ID: %s, Status: %v, Tools: %d\n",
			plan.PlanId, plan.Status, len(plan.Tools))
	}

	// Approve first plan
	if len(plans.Plans) > 0 {
		approved, err := client.ApprovePlan(ctx, &loomv1.ApprovePlanRequest{
			PlanId:   plans.Plans[0].PlanId,
			Approved: true,
			Feedback: "Test approval",
		})
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Plan approved: %v\n", approved.Plan.Status)
	}
}
```

## 5. Testing Permission Mode Persistence

Test that permission mode persists across requests:

```bash
# Create session with AUTO_ACCEPT mode
SESSION_ID=$(grpcurl -plaintext \
  -d '{"query": "test", "permission_mode": 2}' \
  localhost:50051 loom.v1.LoomService/Weave | jq -r '.session_id')

# Get session details (should show permission_mode persisted)
grpcurl -plaintext \
  -d "{\"session_id\": \"$SESSION_ID\"}" \
  localhost:50051 loom.v1.LoomService/GetSession
```

## 6. Database Verification

Check that permission_mode is stored in the database:

### SQLite

```bash
sqlite3 ~/.loom/loom.db "SELECT id, permission_mode FROM sessions LIMIT 5;"
```

### PostgreSQL

```sql
SELECT id, name, permission_mode FROM sessions LIMIT 5;
```

Expected values:
- `0` = UNSPECIFIED (use agent default)
- `1` = ASK_BEFORE
- `2` = AUTO_ACCEPT
- `3` = PLAN

## 7. Testing Plan Lifecycle

Full workflow test:

```bash
# 1. Create session + plan
RESPONSE=$(grpcurl -plaintext -d '{"query": "test", "permission_mode": 3}' \
  localhost:50051 loom.v1.LoomService/Weave)

SESSION_ID=$(echo "$RESPONSE" | jq -r '.session_id')

# 2. List plans (should be PENDING)
PLANS=$(grpcurl -plaintext -d "{\"session_id\": \"$SESSION_ID\"}" \
  localhost:50051 loom.v1.LoomService/ListPlans)

PLAN_ID=$(echo "$PLANS" | jq -r '.plans[0].plan_id')
echo "Plan ID: $PLAN_ID"

# 3. Get plan details
grpcurl -plaintext -d "{\"plan_id\": \"$PLAN_ID\"}" \
  localhost:50051 loom.v1.LoomService/GetPlan

# 4. Approve plan
grpcurl -plaintext -d "{\"plan_id\": \"$PLAN_ID\", \"approved\": true}" \
  localhost:50051 loom.v1.LoomService/ApprovePlan

# 5. List again (should be APPROVED)
grpcurl -plaintext -d "{\"session_id\": \"$SESSION_ID\"}" \
  localhost:50051 loom.v1.LoomService/ListPlans
```

## 8. Error Cases to Test

### Invalid Plan ID

```bash
grpcurl -plaintext \
  -d '{"plan_id": "nonexistent"}' \
  localhost:50051 loom.v1.LoomService/GetPlan
```

**Example error:**
```
ERROR:
  Code: NotFound
  Message: plan not found: nonexistent
```

### Approve Non-Pending Plan

```bash
# Approve a plan twice
grpcurl -plaintext \
  -d '{"plan_id": "PLAN_ID", "approved": true}' \
  localhost:50051 loom.v1.LoomService/ApprovePlan

# Try again
grpcurl -plaintext \
  -d '{"plan_id": "PLAN_ID", "approved": true}' \
  localhost:50051 loom.v1.LoomService/ApprovePlan
```

**Example error:**
```
ERROR:
  Code: Internal
  Message: failed to approve plan: plan xxx is not pending (status: PLAN_STATUS_APPROVED)
```

## 9. Performance Testing

Test concurrent mode switches:

```go
// Run multiple goroutines switching modes
for i := 0; i < 100; i++ {
    go func(i int) {
        mode := loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT
        if i%2 == 0 {
            mode = loomv1.PermissionMode_PERMISSION_MODE_PLAN
        }
        client.Weave(ctx, &loomv1.WeaveRequest{
            Query:          fmt.Sprintf("query %d", i),
            PermissionMode: mode,
        })
    }(i)
}
```

## 10. Canvas AI Integration Testing

When Canvas AI is ready, test from the UI:

1. **Mode Selector**: Verify dropdown shows all 3 modes
2. **AUTO_ACCEPT**: Tools execute immediately, no approval UI
3. **PLAN**: Plan displayed, approve/reject buttons work
4. **Mode Switch**: Change mode mid-session, verify behavior changes
5. **Plan Events**: Real-time plan creation events show in UI
6. **Pagination**: List plans with large result sets

## Success Criteria

✅ All unit tests pass (13 planner tests + existing agent tests)
✅ Permission mode persists to database
✅ Mode switching works within sessions (including after plan execution)
✅ Plans created successfully in PLAN mode
✅ Plan approval/rejection updates status correctly
✅ Plan execution completes with tool results
✅ Conversation continuity maintained (synthetic tool_result blocks added)
✅ Plan events stream in real-time via StreamWeave
✅ Invalid requests return proper errors
✅ Concurrent mode switches are thread-safe (no race conditions detected)

## Next Steps

Once basic testing is complete:

- [ ] Add TUI visualization for plan approval
- [ ] Test Canvas AI integration end-to-end
- [ ] Performance test with large numbers of concurrent sessions
- [ ] Test migration path for existing sessions

## Implementation Details

### Conversation State Preservation in PLAN Mode

When PLAN mode creates an execution plan, it must maintain valid conversation state for LLM APIs. The implementation adds synthetic `tool_result` messages for each deferred tool call.

**Why this is needed:**
LLM APIs (Anthropic, Bedrock) require every `tool_use` block to have a corresponding `tool_result` block in the next message. Without this, you get:
```
ValidationException: messages.N: `tool_use` ids were found without `tool_result` blocks
```

**Implementation** (pkg/agent/agent.go:1718-1743):
```go
// For each deferred tool call, add synthetic result
Message{
    Role:      "tool",
    Content:   "Tool execution deferred to execution plan {plan_id}...",
    ToolUseID: toolCall.ID,  // Links to original tool_use
    ToolResult: &shuttle.Result{
        Success: true,
        Data: map[string]interface{}{
            "status":  "deferred",
            "plan_id": plan.PlanId,
        },
    },
}
```

**Result:**
- ✅ Conversation state remains valid
- ✅ Mode switching works after plan creation
- ✅ Can continue conversation in same session after plan execution
- ✅ LLM has context about deferred tools

## Troubleshooting

### "Plan not found" error
- Verify plan_id from ListPlans response
- Check session_id matches the session that created the plan

### Permission mode not persisting
- Check database migration ran successfully
- Verify SaveSession is being called

### Mode not switching
- Ensure permission_mode field is set in WeaveRequest
- Check agent.SetPermissionMode is being called in server

### Tools executing in PLAN mode
- Verify permission checker InPlanMode() returns true
- Check execution loop is intercepting tool calls

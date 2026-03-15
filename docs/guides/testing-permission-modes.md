# Testing Permission Modes

This guide shows you how to test the runtime permission modes feature manually and programmatically.

## Quick Summary

Permission modes control how the agent handles tool execution:
- **AUTO_ACCEPT** (YOLO mode): Execute tools immediately without asking
- **ASK_BEFORE**: Request approval before each tool (callback required)
- **PLAN**: Create execution plan, wait for approval, then execute

## Quick Test Workflow

Copy-paste this entire block to test the complete workflow:

```bash
# 1. Start server (in separate terminal)
./bin/looms serve --port=50051

# 2. Create session and capture ID
SESSION_ID=$(grpcurl -plaintext -d '{"query": "pwd", "permission_mode": 2}' localhost:50051 loom.v1.LoomService/Weave | jq -r '.sessionId')
echo "Session: $SESSION_ID"

# 3. Create a plan (PLAN mode)
grpcurl -plaintext -d "{\"query\": \"List files\", \"session_id\": \"$SESSION_ID\", \"permission_mode\": 3}" localhost:50051 loom.v1.LoomService/Weave

# 4. Get plan ID
PLAN_ID=$(grpcurl -plaintext -d "{\"session_id\": \"$SESSION_ID\"}" localhost:50051 loom.v1.LoomService/ListPlans | jq -r '.plans[0].planId')
echo "Plan: $PLAN_ID"

# 5. Approve and execute plan
grpcurl -plaintext -d "{\"plan_id\": \"$PLAN_ID\", \"approved\": true}" localhost:50051 loom.v1.LoomService/ApprovePlan
grpcurl -plaintext -d "{\"plan_id\": \"$PLAN_ID\"}" localhost:50051 loom.v1.LoomService/ExecutePlan

# 6. Switch back to AUTO_ACCEPT
grpcurl -plaintext -d "{\"query\": \"echo done\", \"session_id\": \"$SESSION_ID\", \"permission_mode\": 2}" localhost:50051 loom.v1.LoomService/Weave
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
  localhost:50051 loom.v1.LoomService/Weave)

# Extract and save session ID
SESSION_ID=$(echo "$RESPONSE" | jq -r '.sessionId')
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

Expected: Tools execute immediately without approval.

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

Expected: Returns plan created message (no tool execution).

```bash
# 2. List plans for the session
PLANS=$(grpcurl -plaintext \
  -d "{
    \"session_id\": \"$SESSION_ID\"
  }" \
  localhost:50051 loom.v1.LoomService/ListPlans)

# Extract plan ID
PLAN_ID=$(echo "$PLANS" | jq -r '.plans[0].planId')
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

Expected: Shows full plan with tool details and PENDING status.

```bash
# 4. Approve the plan
grpcurl -plaintext \
  -d "{
    \"plan_id\": \"$PLAN_ID\",
    \"approved\": true,
    \"feedback\": \"Approved\"
  }" \
  localhost:50051 loom.v1.LoomService/ApprovePlan
```

Expected: Plan status changes to APPROVED.

```bash
# 5. Execute the approved plan
grpcurl -plaintext \
  -d "{
    \"plan_id\": \"$PLAN_ID\"
  }" \
  localhost:50051 loom.v1.LoomService/ExecutePlan
```

Expected: Tools execute, plan status becomes COMPLETED, results stored in plan.

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

# Get new plan ID
PLAN_ID_2=$(grpcurl -plaintext -d "{\"session_id\": \"$SESSION_ID\"}" localhost:50051 loom.v1.LoomService/ListPlans | jq -r '.plans[-1].planId')

# Reject it
grpcurl -plaintext \
  -d "{
    \"plan_id\": \"$PLAN_ID_2\",
    \"approved\": false,
    \"feedback\": \"Too dangerous\"
  }" \
  localhost:50051 loom.v1.LoomService/ApprovePlan
```

Expected: Plan status changes to REJECTED.

### Test Mode Switching

Test that permission mode can be switched mid-session:

```bash
# First request with AUTO_ACCEPT (tools execute)
grpcurl -plaintext \
  -d "{
    \"query\": \"List current directory\",
    \"session_id\": \"$SESSION_ID\",
    \"permission_mode\": 2
  }" \
  localhost:50051 loom.v1.LoomService/Weave
```

Expected: Tools execute immediately.

```bash
# Second request on same session with PLAN mode (creates plan)
grpcurl -plaintext \
  -d "{
    \"query\": \"Count files in directory\",
    \"session_id\": \"$SESSION_ID\",
    \"permission_mode\": 3
  }" \
  localhost:50051 loom.v1.LoomService/Weave
```

Expected: Creates plan, no tool execution (mode successfully switched).

```bash
# Verify we can switch back to AUTO_ACCEPT
grpcurl -plaintext \
  -d "{
    \"query\": \"Show disk usage\",
    \"session_id\": \"$SESSION_ID\",
    \"permission_mode\": 2
  }" \
  localhost:50051 loom.v1.LoomService/Weave
```

Expected: Tools execute immediately again (mode switched back).

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

Expected: `NotFound` error

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

Expected: Error "plan is not pending"

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

✅ All unit tests pass
✅ Permission mode persists to database
✅ Mode switching works within sessions
✅ Plans created successfully in PLAN mode
✅ Plan approval/rejection updates status
✅ Plan events stream in real-time
✅ Invalid requests return proper errors
✅ Concurrent mode switches are thread-safe

## Next Steps

Once basic testing is complete:

- [ ] Add TUI visualization for plan approval
- [ ] Test Canvas AI integration end-to-end
- [ ] Performance test with large numbers of concurrent sessions
- [ ] Test migration path for existing sessions

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

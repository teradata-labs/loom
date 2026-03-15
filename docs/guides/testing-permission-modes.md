# Testing Permission Modes

This guide shows you how to test the runtime permission modes feature manually and programmatically.

## Quick Summary

Permission modes control how the agent handles tool execution:
- **AUTO_ACCEPT** (YOLO mode): Execute tools immediately without asking
- **ASK_BEFORE**: Request approval before each tool (callback required)
- **PLAN**: Create execution plan, wait for approval, then execute

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

### Setup: Start the loom-server

```bash
# Build the server
just build

# Start with a simple backend
./bin/looms serve \
  --llm-provider=anthropic \
  --llm-model=claude-sonnet-4-5-20250929 \
  --port=50051
```

### Test AUTO_ACCEPT Mode

Use `grpcurl` to send a request with `AUTO_ACCEPT` mode:

```bash
grpcurl -plaintext \
  -d '{
    "query": "List files in current directory",
    "permission_mode": 2
  }' \
  localhost:50051 loom.v1.LoomService/Weave
```

Expected: Tools execute immediately without approval.

### Test PLAN Mode

```bash
# 1. Create a plan by sending a query with PLAN mode
grpcurl -plaintext \
  -d '{
    "query": "Read the README.md file and summarize it",
    "permission_mode": 3
  }' \
  localhost:50051 loom.v1.LoomService/Weave
```

Save the `session_id` from the response.

```bash
# 2. List plans for the session
grpcurl -plaintext \
  -d '{
    "session_id": "sess_abc123"
  }' \
  localhost:50051 loom.v1.LoomService/ListPlans
```

Save the `plan_id` from the response.

```bash
# 3. Get plan details
grpcurl -plaintext \
  -d '{
    "plan_id": "plan_xyz789"
  }' \
  localhost:50051 loom.v1.LoomService/GetPlan
```

```bash
# 4. Approve the plan
grpcurl -plaintext \
  -d '{
    "plan_id": "plan_xyz789",
    "approved": true,
    "feedback": "Looks good!"
  }' \
  localhost:50051 loom.v1.LoomService/ApprovePlan
```

```bash
# 5. Reject a plan (alternative)
grpcurl -plaintext \
  -d '{
    "plan_id": "plan_xyz789",
    "approved": false,
    "feedback": "Not what I wanted"
  }' \
  localhost:50051 loom.v1.LoomService/ApprovePlan
```

### Test Mode Switching

```bash
# First request with AUTO_ACCEPT
grpcurl -plaintext \
  -d '{
    "query": "Show current directory",
    "permission_mode": 2
  }' \
  localhost:50051 loom.v1.LoomService/Weave

# Second request on same session with PLAN mode
# (use session_id from first response)
grpcurl -plaintext \
  -d '{
    "query": "List all Python files",
    "session_id": "sess_abc123",
    "permission_mode": 3
  }' \
  localhost:50051 loom.v1.LoomService/Weave
```

Expected: Mode switches from AUTO_ACCEPT to PLAN within the same session.

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

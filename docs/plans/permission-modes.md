# Permission Modes Architecture

## Overview
Add runtime permission mode switching to support Canvas AI's dynamic permission control. Users can switch between "Ask before", "Auto accept", and "Plan mode" during a session without restarting the agent.

## Three Permission Modes

### 1. Ask Before (Default)
- **Behavior**: Agent requests user approval before executing each tool
- **Use case**: User wants control over every action
- **Implementation**: `requireApproval=true`, `yolo=false`

### 2. Auto Accept
- **Behavior**: Agent executes all tools automatically without asking
- **Use case**: User trusts the agent fully, wants speed
- **Implementation**: `requireApproval=false`, `yolo=true`

### 3. Plan Mode
- **Behavior**: Agent creates an execution plan and waits for approval before executing
- **Use case**: User wants to review the full plan before any execution
- **Implementation**: New mode that defers tool execution until plan approval

## Architecture

### Proto Changes

```protobuf
// proto/loom/v1/loom.proto

// PermissionMode controls how the agent handles tool execution permissions.
enum PermissionMode {
  // Default behavior - ask for permission before each tool execution
  PERMISSION_MODE_ASK_BEFORE = 0;

  // Automatically execute all tools without asking (YOLO mode)
  PERMISSION_MODE_AUTO_ACCEPT = 1;

  // Create execution plan and wait for approval before executing
  PERMISSION_MODE_PLAN = 2;
}

message WeaveRequest {
  string query = 1;
  string session_id = 2;
  map<string, string> backend_config = 3;
  int32 max_rounds = 4;
  int32 timeout_seconds = 5;
  map<string, string> context = 6;
  string force_pattern = 7;
  bool enable_trace = 8;
  string agent_id = 9;

  // Permission mode for this request/session (optional)
  // If not set, uses agent's default configuration
  PermissionMode permission_mode = 10;
}

// ExecutionPlan represents a plan created in PLAN mode
message ExecutionPlan {
  // Unique plan ID
  string plan_id = 1;

  // Session ID this plan belongs to
  string session_id = 2;

  // User query that generated this plan
  string query = 3;

  // Planned tool executions
  repeated PlannedToolExecution tools = 4;

  // Agent's reasoning for the plan
  string reasoning = 5;

  // Status of the plan
  enum PlanStatus {
    PLAN_STATUS_PENDING = 0;     // Waiting for approval
    PLAN_STATUS_APPROVED = 1;    // User approved, ready to execute
    PLAN_STATUS_REJECTED = 2;    // User rejected
    PLAN_STATUS_EXECUTING = 3;   // Currently executing
    PLAN_STATUS_COMPLETED = 4;   // Successfully completed
    PLAN_STATUS_FAILED = 5;      // Execution failed
  }
  PlanStatus status = 6;

  // Created timestamp
  int64 created_at = 7;

  // Updated timestamp
  int64 updated_at = 8;
}

// PlannedToolExecution represents a single tool call in a plan
message PlannedToolExecution {
  // Step number in the plan (1-indexed)
  int32 step = 1;

  // Tool name
  string tool_name = 2;

  // Tool parameters (JSON)
  string params_json = 3;

  // Rationale for this step
  string rationale = 4;

  // Dependencies (step numbers that must complete first)
  repeated int32 depends_on = 5;
}

// ApprovePlanRequest requests approval of an execution plan
message ApprovePlanRequest {
  // Plan ID to approve
  string plan_id = 1;

  // Whether to approve or reject
  bool approved = 2;

  // Optional user feedback/modifications
  string feedback = 3;
}

// ApprovePlanResponse contains the result of plan approval
message ApprovePlanResponse {
  // Updated plan status
  ExecutionPlan plan = 1;
}
```

### Service RPC Changes

```protobuf
service LoomService {
  // ... existing RPCs ...

  // ApprovePlan approves or rejects an execution plan created in PLAN mode
  rpc ApprovePlan(ApprovePlanRequest) returns (ApprovePlanResponse) {
    option (google.api.http) = {
      post: "/v1/plans/{plan_id}/approve"
      body: "*"
    };
  }

  // GetPlan retrieves an execution plan
  rpc GetPlan(GetPlanRequest) returns (ExecutionPlan) {
    option (google.api.http) = {
      get: "/v1/plans/{plan_id}"
    };
  }

  // ListPlans lists execution plans for a session
  rpc ListPlans(ListPlansRequest) returns (ListPlansResponse) {
    option (google.api.http) = {
      get: "/v1/sessions/{session_id}/plans"
    };
  }
}
```

## Implementation Plan

### Phase 1: Proto and Generated Code
1. Add `PermissionMode` enum to `proto/loom/v1/loom.proto`
2. Add `permission_mode` field to `WeaveRequest`
3. Add `ExecutionPlan`, `PlannedToolExecution` messages
4. Add `ApprovePlan`, `GetPlan`, `ListPlans` RPCs
5. Run `buf generate` to update Go code

### Phase 2: Permission Checker Updates
1. Update `pkg/shuttle/permission_checker.go`:
   - Add `mode PermissionMode` field
   - Add `SetMode(mode PermissionMode)` method
   - Update `CheckPermission` to handle three modes
   - Add `InPlanMode() bool` method

```go
// pkg/shuttle/permission_checker.go

type PermissionChecker struct {
	mode           loomv1.PermissionMode // Current permission mode
	requireApproval bool                  // Legacy flag (overridden by mode)
	yolo            bool                  // Legacy flag (overridden by mode)
	allowedTools    map[string]bool
	disabledTools   map[string]bool
	defaultAction   string
	timeoutSeconds  int
}

// SetMode updates the permission mode at runtime
func (pc *PermissionChecker) SetMode(mode loomv1.PermissionMode) {
	pc.mode = mode
}

// GetMode returns the current permission mode
func (pc *PermissionChecker) GetMode() loomv1.PermissionMode {
	return pc.mode
}

// InPlanMode returns true if currently in plan mode
func (pc *PermissionChecker) InPlanMode() bool {
	return pc.mode == loomv1.PermissionMode_PERMISSION_MODE_PLAN
}

// CheckPermission checks if a tool can be executed based on mode
func (pc *PermissionChecker) CheckPermission(ctx context.Context, toolName string, params map[string]interface{}) error {
	// Check disabled tools first (applies to all modes)
	if pc.disabledTools[toolName] {
		return fmt.Errorf("tool '%s' is disabled", toolName)
	}

	// Check allowed tools (whitelist)
	if pc.allowedTools[toolName] {
		return nil
	}

	// Mode-specific behavior
	switch pc.mode {
	case loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT:
		return nil // Auto-approve everything

	case loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE:
		// TODO: Implement actual permission request callback
		return fmt.Errorf("tool '%s' requires user approval", toolName)

	case loomv1.PermissionMode_PERMISSION_MODE_PLAN:
		// In plan mode, tools are collected into a plan, not executed immediately
		return fmt.Errorf("in plan mode - tool execution deferred")

	default:
		return fmt.Errorf("unknown permission mode: %v", pc.mode)
	}
}
```

### Phase 3: Session State Management
1. Update `pkg/types/types.go` Session struct:

```go
type Session struct {
	ID              string
	AgentID         string
	Messages        []Message
	PermissionMode  loomv1.PermissionMode // Runtime permission mode
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Metadata        map[string]string
}
```

2. Update session storage to persist permission_mode
3. Add migration for existing sessions (default to ASK_BEFORE)

### Phase 4: Plan Mode Execution Flow
1. Create `pkg/agent/planner.go`:

```go
package agent

// ExecutionPlanner creates and manages execution plans
type ExecutionPlanner struct {
	sessionID  string
	plans      map[string]*loomv1.ExecutionPlan // planID -> plan
	mu         sync.RWMutex
}

// CreatePlan creates a new execution plan from LLM-generated tool calls
func (ep *ExecutionPlanner) CreatePlan(query string, toolCalls []types.ToolCall, reasoning string) (*loomv1.ExecutionPlan, error) {
	planID := uuid.New().String()

	plan := &loomv1.ExecutionPlan{
		PlanId:    planID,
		SessionId: ep.sessionID,
		Query:     query,
		Reasoning: reasoning,
		Status:    loomv1.ExecutionPlan_PLAN_STATUS_PENDING,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		Tools:     convertToolCallsToPlannedExecutions(toolCalls),
	}

	ep.mu.Lock()
	ep.plans[planID] = plan
	ep.mu.Unlock()

	return plan, nil
}

// ApprovePlan marks a plan as approved and ready for execution
func (ep *ExecutionPlanner) ApprovePlan(planID string, approved bool, feedback string) (*loomv1.ExecutionPlan, error) {
	ep.mu.Lock()
	defer ep.mu.Unlock()

	plan, exists := ep.plans[planID]
	if !exists {
		return nil, fmt.Errorf("plan %s not found", planID)
	}

	if approved {
		plan.Status = loomv1.ExecutionPlan_PLAN_STATUS_APPROVED
	} else {
		plan.Status = loomv1.ExecutionPlan_PLAN_STATUS_REJECTED
	}

	plan.UpdatedAt = time.Now().Unix()
	return plan, nil
}
```

2. Update `pkg/agent/agent.go` execution loop:

```go
func (a *Agent) executeTurn(ctx Context, session *Session) error {
	// Get LLM response with tool calls
	resp, err := a.callLLM(ctx, session)
	if err != nil {
		return err
	}

	// Check permission mode
	permMode := session.PermissionMode
	if permMode == loomv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED {
		permMode = loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE // default
	}

	// Handle plan mode
	if permMode == loomv1.PermissionMode_PERMISSION_MODE_PLAN && len(resp.ToolCalls) > 0 {
		// Create plan instead of executing
		plan, err := a.planner.CreatePlan(session.Messages[0].Content, resp.ToolCalls, resp.Content)
		if err != nil {
			return err
		}

		// Emit plan created event
		a.emitPlanCreated(ctx, plan)

		// Wait for plan approval (blocking)
		return a.waitForPlanApproval(ctx, plan)
	}

	// Normal execution (ASK_BEFORE or AUTO_ACCEPT modes)
	for _, toolCall := range resp.ToolCalls {
		if err := a.executeTool(ctx, session, toolCall); err != nil {
			return err
		}
	}

	return nil
}
```

### Phase 5: Server Implementation
1. Update `pkg/server/server.go` Weave RPC:

```go
func (s *Server) Weave(ctx context.Context, req *loomv1.WeaveRequest) (*loomv1.WeaveResponse, error) {
	// ... existing code ...

	// Set permission mode if provided
	if req.PermissionMode != loomv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED {
		session.PermissionMode = req.PermissionMode

		// Update agent's permission checker
		s.agent.SetPermissionMode(req.PermissionMode)
	}

	// ... continue with execution ...
}
```

2. Implement new RPCs:

```go
func (s *Server) ApprovePlan(ctx context.Context, req *loomv1.ApprovePlanRequest) (*loomv1.ApprovePlanResponse, error) {
	plan, err := s.agent.ApprovePlan(req.PlanId, req.Approved, req.Feedback)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}

	return &loomv1.ApprovePlanResponse{Plan: plan}, nil
}

func (s *Server) GetPlan(ctx context.Context, req *loomv1.GetPlanRequest) (*loomv1.ExecutionPlan, error) {
	plan, err := s.agent.GetPlan(req.PlanId)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}

	return plan, nil
}

func (s *Server) ListPlans(ctx context.Context, req *loomv1.ListPlansRequest) (*loomv1.ListPlansResponse, error) {
	plans, err := s.agent.ListPlans(req.SessionId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &loomv1.ListPlansResponse{Plans: plans}, nil
}
```

### Phase 6: Progress Events
1. Add plan-related progress events:

```protobuf
message WeaveProgress {
	// ... existing fields ...

	// Plan created (only in PLAN mode)
	ExecutionPlan plan_created = 20;

	// Plan approved/rejected
	ExecutionPlan plan_updated = 21;
}
```

2. Emit events in agent execution loop

### Phase 7: Canvas AI Integration
1. Canvas AI can send permission_mode in WeaveRequest:

```typescript
// Canvas AI TypeScript client
const response = await loomClient.weave({
  query: userQuery,
  session_id: sessionId,
  permission_mode: PermissionMode.PERMISSION_MODE_PLAN // User selected from dropdown
});
```

2. Canvas AI can switch modes mid-session:

```typescript
// User switches from Auto Accept to Plan Mode
const response = await loomClient.weave({
  query: "analyze customer churn",
  session_id: existingSessionId,
  permission_mode: PermissionMode.PERMISSION_MODE_PLAN // Changed!
});
```

3. Canvas AI handles plan approval:

```typescript
// User reviews plan and approves
await loomClient.approvePlan({
  plan_id: plan.plan_id,
  approved: true,
  feedback: "Looks good!"
});
```

## Testing Strategy

### Unit Tests
- `pkg/shuttle/permission_checker_test.go`: Test mode switching
- `pkg/agent/planner_test.go`: Test plan creation and approval
- `pkg/server/server_test.go`: Test RPC handling

### Integration Tests
- Test mode switching during active session
- Test plan creation and approval flow
- Test backward compatibility (no permission_mode set)

### E2E Tests
- Canvas AI integration test with mode switching
- Plan mode full flow (create → approve → execute)

## Migration Path

### Backward Compatibility
- If `permission_mode` not set in request, default to `ASK_BEFORE`
- Existing `--yolo` and `--require-approval` flags map to modes:
  - `--yolo` → `PERMISSION_MODE_AUTO_ACCEPT`
  - `--require-approval` → `PERMISSION_MODE_ASK_BEFORE`
  - Neither flag → `PERMISSION_MODE_ASK_BEFORE` (default)

### Database Migration
```sql
-- Add permission_mode column to sessions table
ALTER TABLE sessions ADD COLUMN permission_mode INTEGER DEFAULT 0;

-- 0 = ASK_BEFORE, 1 = AUTO_ACCEPT, 2 = PLAN
```

## Timeline
- **Phase 1-2**: Proto and permission checker (1 day)
- **Phase 3**: Session state management (1 day)
- **Phase 4**: Plan mode execution flow (2 days)
- **Phase 5**: Server implementation (1 day)
- **Phase 6**: Progress events (0.5 days)
- **Phase 7**: Canvas AI integration (0.5 days)

**Total**: ~6 days

## Success Criteria
1. ✅ Canvas AI can switch permission modes dynamically per request
2. ✅ Plan mode creates execution plans that wait for approval
3. ✅ Ask before mode requests approval (when callback implemented)
4. ✅ Auto accept mode executes immediately without prompts
5. ✅ Backward compatible with existing `--yolo` and `--require-approval` flags
6. ✅ All unit and integration tests pass
7. ✅ Canvas AI integration verified

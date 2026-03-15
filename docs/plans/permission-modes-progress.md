# Permission Modes Implementation Progress

## Status: Phase 1 Complete ✅

## Completed Work

### Phase 1: Proto and Generated Code ✅
**Completed:** 2026-03-15

1. ✅ Added `PermissionMode` enum to `proto/loom/v1/loom.proto`:
   - `PERMISSION_MODE_UNSPECIFIED` (0) - Use agent's default
   - `PERMISSION_MODE_ASK_BEFORE` (1) - Ask before each tool
   - `PERMISSION_MODE_AUTO_ACCEPT` (2) - Auto-execute (YOLO mode)
   - `PERMISSION_MODE_PLAN` (3) - Create plan, wait for approval

2. ✅ Added `permission_mode` field to `WeaveRequest` (field 10)
   - Allows Canvas AI to switch modes per request
   - Defaults to UNSPECIFIED (uses agent config)

3. ✅ Added execution plan messages:
   - `PlanStatus` enum with 7 states (UNSPECIFIED, PENDING, APPROVED, REJECTED, EXECUTING, COMPLETED, FAILED)
   - `ExecutionPlan` message with plan metadata and tool list
   - `PlannedToolExecution` message with step details and dependencies
   - `StepStatus` enum for individual step tracking

4. ✅ Added plan approval messages:
   - `ApprovePlanRequest` - User approves/rejects plan
   - `ApprovePlanResponse` - Returns updated plan
   - `GetPlanRequest` / `ListPlansRequest` - Retrieve plans
   - `ListPlansResponse` - Returns plan list

5. ✅ Added new RPCs to `LoomService`:
   - `ApprovePlan` - POST /v1/plans/{plan_id}/approve
   - `GetPlan` - GET /v1/plans/{plan_id}
   - `ListPlans` - GET /v1/sessions/{session_id}/plans

6. ✅ Added plan events to `WeaveProgress`:
   - `is_plan_created` (field 21)
   - `is_plan_approved` (field 22)
   - `is_plan_rejected` (field 23)
   - `plan` (field 24) - ExecutionPlan data

7. ✅ Generated Go code with `buf generate`:
   - `gen/go/loom/v1/loom.pb.go` updated
   - All new types available: `PermissionMode`, `PlanStatus`, `ExecutionPlan`, etc.

8. ✅ Passed `buf lint` with proto style conventions
   - Fixed enum zero values to use `_UNSPECIFIED` suffix

## Files Modified
- `proto/loom/v1/loom.proto` - Added 3 enums, 8 messages, 3 RPCs
- `gen/go/loom/v1/loom.pb.go` - Auto-generated Go bindings
- `gen/go/loom/v1/loom_grpc.pb.go` - Auto-generated gRPC server/client

## Next Steps

### Phase 2: Permission Checker Updates
**Status:** Ready to start

**Tasks:**
1. Update `pkg/shuttle/permission_checker.go`:
   - Add `mode loomv1.PermissionMode` field
   - Add `SetMode(mode PermissionMode)` method
   - Add `GetMode()` and `InPlanMode()` methods
   - Update `CheckPermission()` to handle three modes
   - Plan mode should return special error indicating deferral

2. Write unit tests in `pkg/shuttle/permission_checker_test.go`:
   - Test mode switching
   - Test behavior in each mode (ASK_BEFORE, AUTO_ACCEPT, PLAN)
   - Test disabled tools across all modes
   - Test allowed tools (whitelist)

**Files to modify:**
- `pkg/shuttle/permission_checker.go`
- `pkg/shuttle/permission_checker_test.go` (new tests)

### Phase 3: Session State Management
**Status:** Pending Phase 2

**Tasks:**
1. Add `PermissionMode` field to `Session` struct in `pkg/types/types.go`
2. Update session storage to persist permission_mode
3. Add database migration for SQLite/Postgres backends
4. Update `CreateSession` and `UpdateSession` to handle permission_mode

**Files to modify:**
- `pkg/types/types.go`
- `pkg/storage/sqlite/*.go`
- `pkg/storage/postgres/*.go` (if applicable)
- Migration files

### Phase 4: Plan Mode Execution Flow
**Status:** Pending Phase 2-3

**Tasks:**
1. Create `pkg/agent/planner.go`:
   - `ExecutionPlanner` struct
   - `CreatePlan()` - Convert tool calls to plan
   - `ApprovePlan()` - Mark plan approved/rejected
   - `ExecutePlan()` - Execute approved plan
   - `GetPlan()`, `ListPlans()` - Retrieval methods

2. Update `pkg/agent/agent.go`:
   - Add `planner *ExecutionPlanner` field
   - Modify `executeTurn()` to check permission mode
   - If PLAN mode + tool calls → create plan, emit event, wait for approval
   - Add `waitForPlanApproval()` method (blocks until approved/rejected)
   - Add `SetPermissionMode()` method to update runtime mode

3. Wire plan execution:
   - When plan approved → execute tools sequentially/parallel based on dependencies
   - Update progress events (tool started/completed)
   - Update plan status as execution progresses

**Files to modify:**
- `pkg/agent/planner.go` (new file)
- `pkg/agent/agent.go`
- `pkg/agent/types.go` (add planner field)

### Phase 5: Server Implementation
**Status:** Pending Phase 2-4

**Tasks:**
1. Update `pkg/server/server.go` Weave RPC:
   - Extract `req.PermissionMode` from request
   - Set on session if provided
   - Call `agent.SetPermissionMode()` to update runtime behavior

2. Implement new plan RPCs:
   - `ApprovePlan()` - Call agent method, return updated plan
   - `GetPlan()` - Retrieve plan by ID
   - `ListPlans()` - List plans for session

3. Add plan storage:
   - In-memory plan cache or database table
   - Thread-safe access
   - Session cleanup on session delete

**Files to modify:**
- `pkg/server/server.go`
- Plan storage backend (TBD - in-memory vs DB)

### Phase 6: Progress Events
**Status:** Pending Phase 4-5

**Tasks:**
1. Emit plan events in agent execution:
   - `is_plan_created` when plan created
   - `is_plan_approved` when user approves
   - `is_plan_rejected` when user rejects
   - Include full `ExecutionPlan` in event

2. Update TUI to handle plan events:
   - Display plan details
   - Show approval UI
   - Stream plan execution progress

**Files to modify:**
- `pkg/agent/agent.go` (emit events)
- `pkg/tui/*.go` (handle events, if applicable)

### Phase 7: Canvas AI Integration
**Status:** Pending Phase 1-6

**Tasks:**
1. Update Canvas AI client to send `permission_mode`:
   ```typescript
   loomClient.weave({
     query: userQuery,
     session_id: sessionId,
     permission_mode: PermissionMode.PERMISSION_MODE_PLAN
   });
   ```

2. Add permission mode dropdown to Canvas AI UI
3. Handle plan approval flow in Canvas AI
4. Test mode switching during active session

**Files to modify:**
- Canvas AI TypeScript client
- Canvas AI React UI

## Testing Strategy

### Unit Tests
- ✅ Proto generation (verified with buf lint + generate)
- ⏳ Permission checker mode switching
- ⏳ Plan creation and approval logic
- ⏳ Agent execution flow with plan mode

### Integration Tests
- ⏳ End-to-end plan creation → approval → execution
- ⏳ Mode switching during active session
- ⏳ Backward compatibility (no permission_mode set)
- ⏳ Plan storage and retrieval

### E2E Tests
- ⏳ Canvas AI integration with mode switching
- ⏳ Plan mode full workflow

## Migration Path

### Backward Compatibility
- If `permission_mode` not set → defaults to UNSPECIFIED
- UNSPECIFIED → uses agent config (existing --yolo / --require-approval flags)
- Existing flags map to modes:
  - `--yolo` → `PERMISSION_MODE_AUTO_ACCEPT`
  - `--require-approval` → `PERMISSION_MODE_ASK_BEFORE`
  - Neither → `PERMISSION_MODE_ASK_BEFORE` (default safe behavior)

### Database Migration
```sql
-- sessions table
ALTER TABLE sessions ADD COLUMN permission_mode INTEGER DEFAULT 0;

-- plans table (new)
CREATE TABLE execution_plans (
  plan_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  query TEXT NOT NULL,
  reasoning TEXT,
  status INTEGER DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  result_json TEXT,
  error_message TEXT,
  FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX idx_plans_session ON execution_plans(session_id);
CREATE INDEX idx_plans_status ON execution_plans(status);

-- planned tool executions table
CREATE TABLE planned_tool_executions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  plan_id TEXT NOT NULL,
  step INTEGER NOT NULL,
  tool_name TEXT NOT NULL,
  params_json TEXT NOT NULL,
  rationale TEXT,
  depends_on TEXT, -- JSON array of step numbers
  result TEXT,
  error TEXT,
  status INTEGER DEFAULT 0,
  FOREIGN KEY (plan_id) REFERENCES execution_plans(plan_id) ON DELETE CASCADE
);

CREATE INDEX idx_planned_tools_plan ON planned_tool_executions(plan_id);
```

## Timeline Estimate

- ✅ **Phase 1**: Proto and generated code - **Completed**
- **Phase 2**: Permission checker - **1 day**
- **Phase 3**: Session state - **1 day**
- **Phase 4**: Plan mode execution - **2 days**
- **Phase 5**: Server implementation - **1 day**
- **Phase 6**: Progress events - **0.5 days**
- **Phase 7**: Canvas AI integration - **0.5 days**

**Remaining**: ~6 days

## Success Criteria

1. ✅ Proto definitions added and generated successfully
2. ✅ buf lint passes with proto style conventions
3. ⏳ Canvas AI can switch permission modes dynamically per request
4. ⏳ Plan mode creates execution plans that wait for approval
5. ⏳ Ask before mode requests approval (when callback implemented)
6. ⏳ Auto accept mode executes immediately without prompts
7. ⏳ Backward compatible with existing `--yolo` and `--require-approval` flags
8. ⏳ All unit and integration tests pass
9. ⏳ Canvas AI integration verified end-to-end

## Notes

- Proto changes are backward compatible (permission_mode is optional, defaults to UNSPECIFIED)
- Plan approval is blocking - agent waits for user decision before proceeding
- Plan mode integrates with existing tool execution infrastructure
- Progress events allow real-time UI updates during plan creation/execution

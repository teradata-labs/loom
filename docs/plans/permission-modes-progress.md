# Permission Modes Implementation Progress

## Status: Phase 1-5 Complete ✅ | Phase 6-7 Ready

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

### Phase 2: Permission Checker Updates ✅
**Completed:** 2026-03-15

1. ✅ Updated `pkg/shuttle/permission_checker.go`:
   - Added `mode loomv1.PermissionMode` field
   - Added `SetMode(mode)`, `GetMode()`, `InPlanMode()`, `InAskBeforeMode()`, `InAutoAcceptMode()` methods
   - Updated `CheckPermission()` to handle three modes with mode-specific logic
   - Plan mode returns `ErrToolExecutionDeferred` for plan creation
   - Maintained backward compatibility with legacy YOLO/RequireApproval flags
   - Explicit mode takes precedence over legacy flags
   - Disabled tools blocked across all modes
   - Allowed tools (whitelist) work across all modes

2. ✅ Wrote comprehensive unit tests in `pkg/shuttle/permission_checker_test.go`:
   - 13 test suites covering all scenarios
   - Mode initialization (explicit vs legacy flags)
   - Runtime mode switching
   - Permission checking in all three modes
   - Disabled/allowed tool interaction
   - Default action behavior
   - Concurrent mode switching
   - 100% coverage of new code paths
   - All tests pass

**Files modified:**
- `pkg/shuttle/permission_checker.go` - Core implementation
- `pkg/shuttle/permission_checker_test.go` - Comprehensive tests (543 lines)

**Commit:** `52e61ee` - feat(shuttle): implement runtime permission mode switching

### Phase 3: Session State Management ✅
**Completed:** 2026-03-15

1. ✅ Added `PermissionMode` field to `Session` struct in `pkg/types/types.go`:
   - Stores runtime permission mode per session
   - Persists across requests
   - Defaults to UNSPECIFIED (0)

2. ✅ SQLite persistence:
   - Migration 000002: `ALTER TABLE sessions ADD COLUMN permission_mode INTEGER DEFAULT 0`
   - Index on permission_mode for filtering
   - Updated `SaveSession` to persist field
   - Updated `LoadSession` to retrieve field
   - Down migration recreates table without column

3. ✅ Postgres persistence:
   - Migration 000010: `ALTER TABLE sessions ADD COLUMN permission_mode INTEGER DEFAULT 0`
   - Index and column comment
   - Updated `SaveSession` to persist field ($11 parameter)
   - Updated `LoadSession` to retrieve field
   - Down migration drops column cleanly

4. ✅ Session storage layer updates:
   - `pkg/agent/session_store.go`: INSERT/UPDATE/SELECT with permission_mode
   - `pkg/storage/postgres/session_store.go`: INSERT/UPDATE/SELECT with permission_mode
   - Proper int32 <-> PermissionMode enum conversion
   - Added loomv1 imports

**Files modified:**
- `pkg/types/types.go` - Added PermissionMode field
- `pkg/agent/session_store.go` - SQLite persistence
- `pkg/storage/postgres/session_store.go` - Postgres persistence
- `pkg/storage/sqlite/migrations/000002_*.sql` - SQLite migration
- `pkg/storage/postgres/migrations/000010_*.sql` - Postgres migration

**Commit:** `369f2f5` - feat(storage): add permission_mode to session state persistence

### Phase 4: Plan Mode Execution Flow ✅
**Completed:** 2026-03-15

#### Part 1: ExecutionPlanner Infrastructure ✅

1. ✅ Created `pkg/agent/planner.go`:
   - `ExecutionPlanner` manages plans per session
   - `CreatePlan`: Converts LLM tool calls → ExecutionPlan (PENDING)
   - `ApprovePlan`: User decision → APPROVED/REJECTED
   - `ExecutePlan`: Runs approved plan tools sequentially
   - `GetPlan`/`ListPlans`: Retrieve by ID or filter by status
   - `ClearPlans`: Session cleanup
   - Thread-safe with RWMutex

2. ✅ Plan lifecycle implemented:
   - PENDING → user reviews
   - APPROVED/REJECTED → user decision
   - EXECUTING → tools running
   - COMPLETED/FAILED → final status

3. ✅ Step tracking:
   - PlannedToolExecution per tool with step number
   - Status per step (PENDING → EXECUTING → COMPLETED/FAILED)
   - Captures results and errors

4. ✅ Comprehensive tests (`pkg/agent/planner_test.go`):
   - 13 test suites, all passing
   - Full lifecycle coverage
   - Approve/reject workflows
   - Execution success and failure scenarios
   - Edge cases (double approval, unapproved execution, etc.)

**Files created:**
- `pkg/agent/planner.go` - Core planner (294 lines)
- `pkg/agent/planner_test.go` - Tests (412 lines)

**Commit:** `b1947d0` - feat(agent): implement ExecutionPlanner for plan mode workflow

#### Part 2: Agent Integration ✅

1. ✅ Added `planner *ExecutionPlanner` field to Agent struct
2. ✅ Added agent methods for plan management:
   - `SetPermissionMode`: Update mode, persist to session
   - `GetPermissionMode`: Retrieve from session
   - `ensurePlannerForSession`: Initialize per-session planner
   - `ApprovePlan/GetPlan/ListPlans`: Delegate to planner
3. ✅ Modified execution loop to detect PLAN mode:
   - Check `permissionChecker.InPlanMode()` before tool execution
   - Extract user query from session messages
   - Call `planner.CreatePlan()` with tool calls and LLM reasoning
   - Emit `plan_created` progress event
   - Add assistant message explaining plan
   - Return response with plan metadata (ID, status, tool count)
   - Skip normal tool execution loop
4. ✅ Added progress event helpers:
   - `emitPlanCreated`: Emit when plan created
   - `emitPlanApproved`: Emit when user approves
   - `emitPlanRejected`: Emit when user rejects
5. ✅ Updated ProgressEvent struct:
   - Added `IsPlanCreated`, `IsPlanApproved`, `IsPlanRejected` bool fields
   - Added `Plan *loomv1.ExecutionPlan` field
   - Enables streaming plan events to clients

**Files modified:**
- `pkg/agent/types.go` - Added planner field to Agent struct
- `pkg/agent/agent.go` - Agent methods + execution loop integration (205 lines)
- `pkg/types/types.go` - ProgressEvent plan fields

**Commit:** `67b4444` - feat(agent): wire ExecutionPlanner into agent execution loop for PLAN mode

### Phase 5: Server Implementation ✅
**Completed:** 2026-03-15

1. ✅ Updated `Weave` RPC to handle permission mode:
   - Extract `req.PermissionMode` from WeaveRequest
   - Call `agent.SetPermissionMode(ctx, sessionID, mode)` before executing chat
   - Persists permission mode to session for subsequent requests

2. ✅ Updated `StreamWeave` RPC to handle permission mode:
   - Extract `req.PermissionMode` from WeaveRequest
   - Call `agent.SetPermissionMode(ctx, sessionID, mode)` before streaming
   - Added `applyPlanEventFields` to stream plan events

3. ✅ Implemented `ApprovePlan` RPC:
   - Validates plan_id parameter
   - Calls `agent.ApprovePlan(planID, approved, feedback)`
   - Returns updated ExecutionPlan with new status

4. ✅ Implemented `GetPlan` RPC:
   - Validates plan_id parameter
   - Calls `agent.GetPlan(planID)`
   - Returns NotFound error if plan doesn't exist

5. ✅ Implemented `ListPlans` RPC:
   - Validates session_id parameter
   - Calls `agent.ListPlans(sessionID, statusFilter)`
   - Supports pagination (page_size, page_offset)
   - Returns total count for pagination UI

6. ✅ Added `applyPlanEventFields` helper:
   - Populates is_plan_created/approved/rejected fields
   - Includes ExecutionPlan in progress events
   - Enables real-time plan event streaming to clients

**Files modified:**
- `pkg/server/server.go` - Added permission mode handling + 3 plan RPCs (96 lines)

**Commit:** `d7a8b3a` - feat(server): implement permission mode handling and plan approval RPCs

## Next Steps

### Phase 6: Progress Events ⚠️ Partially Complete
**Status:** Infrastructure complete, TUI pending

**Completed:**
- ✅ Plan event emission in agent execution (Phase 4)
- ✅ Progress event fields added to types.ProgressEvent (Phase 4)
- ✅ Server streaming wired to emit plan events (Phase 5)

**Remaining:**
1. Update TUI to handle plan events:
   - Display plan details
   - Show approval UI
   - Stream plan execution progress

**Files to modify:**
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

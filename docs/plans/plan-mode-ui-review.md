# Plan Mode UI Implementation Review

**Branch**: `fix/proto-reserved-fields-backward-compat`
**Date**: March 24, 2026
**Status**: Partial Implementation - CLI Complete, TUI Pending

## Summary

This branch implements plan approval UI for agents configured with `default_permission_mode: PERMISSION_MODE_PLAN`. The CLI `loom chat` command now fully supports creating, displaying, approving, and executing plans. The TUI requires additional work to display plans and handle approval.

## What Works ✅

### 1. CLI Chat Command (`loom chat`)

**Fully functional plan workflow:**

```bash
loom chat --thread file-helper-plan --server localhost:60051 "list my folders"
```

**Output:**
```
=== Execution Plan Created ===
Plan ID: e71a056f-8832-43cc-9e98-b7cad4629ccc
Reasoning: I'll help you list your folders. Let me check what's available in your workspace.

Steps:
  1. workspace
     Rationale: Execute workspace
     Params: {"action":"list","scope":"artifact"}
  2. workspace
     Rationale: Execute workspace
     Params: {"action":"list","scope":"scratchpad"}

Approve this plan? (yes/no): yes
[Plan approved, executing...]

Successfully executed 2 tool(s)
```

**Key Features:**
- ✅ Plan details displayed (ID, reasoning, steps with params)
- ✅ User prompted for approval (yes/no)
- ✅ Plan executed after approval
- ✅ Execution summary displayed
- ✅ No infinite loop when typing responses
- ✅ Works with piped input: `echo "yes" | loom chat ...`

### 2. Backend Plan Infrastructure

**Complete plan execution system:**
- ✅ Per-agent permission modes from YAML config
- ✅ `ApprovePlan` RPC endpoint
- ✅ `ExecutePlan` RPC endpoint
- ✅ Plan events in `WeaveProgress` stream (`IsPlanCreated`, `Plan` field)
- ✅ Backend automatically creates plans when `permission_mode=PLAN`
- ✅ Multi-agent server emits plan events via `applyPlanEventFields()`

### 3. REST API

**Plan workflow via HTTP:**
```bash
# Create plan
curl -X POST http://localhost:5006/v1/weave \
  -H "Content-Type: application/json" \
  -d '{"query":"list my folders","agent_id":"file-helper-plan"}' | jq '.plan'

# Approve plan
curl -X POST http://localhost:5006/v1/plans/{plan_id}/approve \
  -H "Content-Type: application/json" \
  -d '{"approved":true}'

# Execute plan
curl -X POST http://localhost:5006/v1/plans/{plan_id}/execute
```

## What Doesn't Work ❌

### TUI Plan Approval

**Current Behavior:**
- TUI gets stuck showing "Created execution plan with 2 steps"
- Plan details not displayed
- No approval dialog or yes/no prompt
- User cannot proceed without restarting

**Root Cause:**
The TUI receives `WeaveProgress` events with `IsPlanCreated=true` but has no UI component to:
1. Display plan details
2. Show approval prompt
3. Handle user approval/rejection
4. Call `ApprovePlan` and `ExecutePlan` RPCs

**What Needs to Be Built:**

1. **Create Plan Approval Dialog Component** (`internal/tui/components/dialogs/plan/`)
   - Similar to existing `clarification` dialog
   - Display plan ID, reasoning, steps with parameters
   - Show "Approve" / "Reject" buttons
   - Handle keyboard navigation

2. **Add Plan Event Message Types** (`internal/tui/adapter/coordinator.go`)
   ```go
   type PlanCreatedMsg struct {
       Plan      *loomv1.ExecutionPlan
       SessionID string
       AgentID   string
   }

   type PlanApprovedMsg struct {
       PlanID    string
       Approved  bool
   }
   ```

3. **Handle Plan Events in Chat Page** (`internal/tui/page/chat/chat.go`)
   - Listen for `PlanCreatedMsg`
   - Show plan dialog
   - Handle approval/rejection
   - Call `ApprovePlan` RPC via client
   - Call `ExecutePlan` RPC after approval
   - Wait for execution results

4. **Wire Up in Coordinator** (`internal/tui/adapter/coordinator.go:221`)
   ```go
   // Check for plan creation and emit plan approval request
   if progress.IsPlanCreated && progress.Plan != nil {
       c.events <- PlanCreatedMsg{
           Plan:      progress.Plan,
           SessionID: sessionID,
           AgentID:   agentID,
       }
   }
   ```

## Files Modified

### Key Changes

1. **`cmd/loom/chat.go`** - CLI plan approval implementation
   - Capture plan during `StreamWeave` callback
   - Display plan after stream completes
   - Prompt for yes/no approval
   - Call `ApprovePlan` and `ExecutePlan` RPCs
   - Display execution summary

2. **`pkg/tui/client/client.go`** - Added `GetLoomClient()` method
   - Exposes underlying `LoomServiceClient` for direct RPC calls
   - Required for `ApprovePlan` and `ExecutePlan` in CLI

3. **`pkg/server/multi_agent.go`** - Fixed plan event emission
   - Added `applyPlanEventFields()` call in `StreamWeave` (line 997)
   - Ensures plan events reach clients

4. **Backend Files** (from earlier permission modes work)
   - `pkg/agent/agent.go` - Default permission checker initialization
   - `pkg/agent/config_loader.go` - Parse `default_permission_mode` from YAML
   - `pkg/agent/registry.go` - Removed global permission checker override
   - `cmd/looms/cmd_serve.go` - Removed global permission checker
   - `proto/loom/v1/loom.proto` - `WeaveResponse.plan` field for Canvas AI

## Testing

### Test CLI Plan Approval

```bash
# Start server
looms serve

# Test in another terminal
echo "yes" | loom chat --thread file-helper-plan --server localhost:60051 "list my folders"

# Expected: Plan displayed, approved, executed, summary shown
```

### Test CLI Plan Rejection

```bash
echo "no" | loom chat --thread file-helper-plan --server localhost:60051 "list my folders"

# Expected: Plan displayed, rejected, no execution
```

### Test TUI (Currently Broken)

```bash
loom --thread file-helper-plan

# Type: list my folders
# Press Enter

# Current: Gets stuck on "Created execution plan with 2 steps"
# Expected: Should show plan approval dialog (NOT YET IMPLEMENTED)
```

## Workarounds for Users

Until TUI support is implemented, users have three options:

### Option 1: Use CLI Chat
```bash
loom chat --thread file-helper-plan "your query here"
# Then type "yes" or "no" when prompted
```

### Option 2: Use REST API
See REST API examples above

### Option 3: Switch Agent to AUTO_ACCEPT
Edit `~/.loom/agents/file-helper-plan.yaml`:
```yaml
default_permission_mode: PERMISSION_MODE_AUTO_ACCEPT
```
Restart server. Agent will execute tools immediately without approval.

## Architecture Notes

### Why CLI Works But TUI Doesn't

**CLI (`cmd/loom/chat.go`):**
- Simple synchronous flow: stream → display plan → read stdin → approve → execute → exit
- Stdin reading happens **outside** the streaming callback (critical to avoid infinite loop)
- Single-shot execution model

**TUI (`internal/tui/`):**
- Event-driven architecture with message passing
- Requires dialog component to display plan
- Must handle async approval/execution without blocking UI
- Needs proper state management for plan lifecycle

### Previous Bugs Fixed

1. **Infinite Loop Bug** - Reading stdin inside `StreamWeave` callback caused repeated characters
   - **Fix**: Moved stdin reading outside callback in CLI

2. **Symlink Issue** - `/Users/josh.schoen/.local/bin/loom` was a symlink to old binary
   - **Fix**: Replaced symlink with actual binary copy

3. **No Execution After Approval** - Plans approved but never executed
   - **Fix**: Added `ExecutePlan` RPC call after `ApprovePlan`

4. **Global Permission Checker Override** - Agent YAML config ignored
   - **Fix**: Removed global permission checker from registry and cmd_serve

## Recommendations

### For Maintainers

1. **Merge CLI Implementation** - CLI plan approval is production-ready
2. **Plan TUI Work** - Create issue/milestone for TUI plan approval dialog
3. **Consider Priority** - How critical is TUI plan approval vs other features?
4. **Estimate Effort** - TUI implementation is ~4-6 hours of work:
   - 2 hours: Plan dialog component
   - 1 hour: Message types and event handling
   - 1 hour: Wire up coordinator and chat page
   - 1-2 hours: Testing and polish

### For Documentation

1. **Update User Guides** - Document `loom chat` plan approval workflow
2. **Add TUI Limitation Note** - Explain TUI doesn't support plan approval yet
3. **Provide Workarounds** - Document the 3 options above
4. **Roadmap Item** - Add "TUI Plan Approval" to features roadmap

## Next Steps

**If moving forward with TUI implementation:**

1. Create plan dialog component (use clarification dialog as template)
2. Add `PlanCreatedMsg` and `PlanApprovedMsg` types
3. Handle plan events in chat page `Update()` method
4. Call `ApprovePlan` via `app.Client().GetLoomClient().ApprovePlan()`
5. Call `ExecutePlan` after approval
6. Test with file-helper-plan agent

**If deferring TUI work:**

1. Document CLI as the supported plan approval method
2. Add TUI limitation to known issues
3. Consider if REST API is sufficient for Canvas AI use case
4. Revisit when TUI refactor/modernization is planned

## Questions for Review

1. Is CLI-only plan approval sufficient for initial release?
2. Should we prioritize TUI plan approval or other features?
3. Is the current ExecutePlan response (just summary) adequate, or should it stream tool outputs?
4. Should we add plan approval timeout/expiration?
5. How should we handle plan approval in headless/automation scenarios?

---

**Conclusion**: CLI plan approval is complete and working. TUI requires additional implementation work but has a clear path forward. The backend infrastructure is solid and ready to support both clients.

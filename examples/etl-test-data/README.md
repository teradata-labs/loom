# Permission Modes Test - ETL Pipeline Demo

This directory contains a demonstration of the **permission modes feature** that was successfully rebased from the `fix/proto-reserved-fields-backward-compat` branch onto `main`.

## What Was Rebased

The rebase integrated permission mode features across **31 commits**, resolving conflicts in:

1. **Proto definitions** (`proto/loom/v1/loom.proto`)
   - Added `permission_mode` field to `WeaveRequest` (field 11)
   - Added plan-related fields to `TraceEvent` (fields 22-25)
   - Kept `reset_context` feature (field 10) alongside new permission modes

2. **Agent execution logic** (`pkg/agent/agent.go`)
   - Integrated PLAN mode checking with per-turn tool execution limits
   - Combined permission checker with deduplication logic

3. **Server implementations** (`pkg/server/server.go`, `pkg/server/multi_agent.go`)
   - Added permission mode setting before query execution
   - Integrated context variable interpolation with reset functionality

## Permission Modes

### 1. AUTO_ACCEPT (Default)
- **Behavior**: Executes tools immediately without asking
- **Use Case**: Trusted operations, automated pipelines, production workflows
- **Example**: ETL pipelines that run on schedule

```bash
curl -X POST http://localhost:9091/v1/weave \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Process customers.csv...",
    "permission_mode": "PERMISSION_MODE_AUTO_ACCEPT"
  }'
```

### 2. ASK_BEFORE
- **Behavior**: Asks permission before each tool execution
- **Use Case**: Interactive sessions, learning mode, dangerous operations
- **Example**: Manual data processing where user wants control

```bash
curl -X POST http://localhost:9091/v1/weave \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Process customers.csv...",
    "permission_mode": "PERMISSION_MODE_ASK_BEFORE"
  }'
```

### 3. PLAN (Canvas AI Mode)
- **Behavior**: Creates execution plan without executing tools
- **Use Case**: Complex workflows, audit requirements, Canvas AI integration
- **Example**: Review entire workflow before approving execution

```bash
# Create plan
curl -X POST http://localhost:9091/v1/weave \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Process customers.csv...",
    "permission_mode": "PERMISSION_MODE_PLAN"
  }'

# Returns plan_id for approval

# Approve and execute plan
curl -X POST http://localhost:9091/v1/plans/{plan_id}/approve \
  -H "Content-Type: application/json" \
  -d '{"session_id": "your-session-id"}'
```

## Demo Files

### 1. `etl-pipeline-agent.yaml`
Agent configuration optimized for ETL operations with permission mode support.

### 2. `customers.csv`
Sample customer data with:
- 8 rows (including 1 duplicate)
- Mixed case emails
- Inactive/suspended records

### 3. `simulate-permission-modes.sh`
Interactive simulation showing all three permission modes:

```bash
./examples/etl-test-data/simulate-permission-modes.sh
```

Output demonstrates:
- **AUTO_ACCEPT**: Immediate execution (2.3s)
- **ASK_BEFORE**: 2 approval prompts required
- **PLAN**: Creates plan without executing, shows risk analysis

### 4. `test-permission-modes.sh`
Full integration test (requires running Loom server):

```bash
# Start server and run tests
./examples/etl-test-data/test-permission-modes.sh
```

## Proto Field Numbers

The rebase resolved field number conflicts by keeping both features:

### WeaveRequest
```protobuf
message WeaveRequest {
  string agent_id = 9;
  bool reset_context = 10;           // Context reset feature (from main)
  PermissionMode permission_mode = 11; // Permission modes (from branch)
}
```

### TraceEvent
```protobuf
message TraceEvent {
  string tool_call_id = 20;
  ContextState context_state = 21;    // Context state (from main)
  bool is_plan_created = 22;          // Plan events (from branch)
  bool is_plan_approved = 23;
  bool is_plan_rejected = 24;
  ExecutionPlan plan = 25;
}
```

## Verification

All checks passed after rebase:

- ✅ Proto lint (`buf lint`)
- ✅ Proto generation (`buf generate`)
- ✅ Build (`just build`)
- ✅ Code formatting (`gofmt`)

## Use Cases by Mode

| Scenario | Recommended Mode | Why |
|----------|------------------|-----|
| Automated ETL pipelines | AUTO_ACCEPT | Fast, unattended execution |
| Interactive data exploration | ASK_BEFORE | User controls each step |
| Canvas AI integration | PLAN | Review workflow before execution |
| Production deployments | AUTO_ACCEPT | Reliable, tested workflows |
| Learning/training | ASK_BEFORE | Understand each tool call |
| Compliance/audit workflows | PLAN | Full audit trail, approval records |
| Dangerous operations (delete, modify) | ASK_BEFORE or PLAN | Safety review |

## Next Steps

1. **Run simulation**: `./examples/etl-test-data/simulate-permission-modes.sh`
2. **Review agent config**: `examples/reference/agents/etl-pipeline-agent.yaml`
3. **Test with real server**: Start Loom server and send requests with different modes
4. **Integrate with Canvas AI**: Use PLAN mode for Canvas AI workflows

## Related Documentation

- Proto definitions: `proto/loom/v1/loom.proto`
- Agent execution logic: `pkg/agent/agent.go` (line 1790+)
- Server handlers: `pkg/server/server.go` (line 115+)
- Permission modes progress: `docs/plans/permission-modes-progress.md`

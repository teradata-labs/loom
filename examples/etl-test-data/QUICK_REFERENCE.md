# Permission Modes - Quick Reference Card

## Output Patterns at a Glance

### 🟢 AUTO_ACCEPT - "Fire and Forget"

**Single Request → Complete Results**

```
Request:  permission_mode: "PERMISSION_MODE_AUTO_ACCEPT"

Response: {
            "text": "✓ Pipeline completed! 8 rows → 5 rows",
            "tool_executions": [
              {"tool_name": "file_read", "success": true},
              {"tool_name": "file_write", "success": true}
            ],
            "metadata": {"permission_mode": "AUTO_ACCEPT"}
          }
```

**Characteristics:**
- ✅ 1 API call total
- ✅ 0 user approvals
- ✅ All tools auto-execute
- ❌ No review opportunity

---

### 🟡 ASK_BEFORE - "Stop and Ask"

**Request → Approve #1 → Approve #2 → Results**

```
Request:  permission_mode: "PERMISSION_MODE_ASK_BEFORE"

Response 1: {
              "text": "Approve file_read?",
              "metadata": {"awaiting_approval": true}
            }

Request 2:  "yes, approved"

Response 2: {
              "text": "✓ Read done. Approve file_write?",
              "metadata": {"awaiting_approval": true}
            }

Request 3:  "yes, write it"

Response 3: {
              "text": "✓ Pipeline completed!",
              "metadata": {"total_approvals": 2}
            }
```

**Characteristics:**
- ⚠️ 3 API calls total
- ✅ 2 user approvals (one per tool)
- ✅ Can deny each tool
- ❌ Slower execution

---

### 🔵 PLAN - "Review Then Execute"

**Request → Review Plan → Approve → Results**

```
Request:  permission_mode: "PERMISSION_MODE_PLAN"

Response 1: {
              "text": "📋 Plan created with 3 steps",
              "metadata": {
                "plan_id": "plan_123",
                "plan_status": "PENDING_APPROVAL",
                "steps": [...]
              }
            }

Request 2:  POST /v1/plans/plan_123/approve

Response 2: {
              "status": "COMPLETED",
              "execution_results": {
                "steps": [
                  {"step": 1, "status": "SUCCESS"},
                  {"step": 2, "status": "SUCCESS"},
                  {"step": 3, "status": "SUCCESS"}
                ]
              }
            }
```

**Characteristics:**
- 📋 2 API calls total
- ✅ 1 approval (whole plan)
- ✅ NO tools run until approved
- ✅ Complete workflow review

---

## Decision Matrix

| Question | AUTO_ACCEPT | ASK_BEFORE | PLAN |
|----------|-------------|------------|------|
| **Do you trust the operation?** | Yes ✓ | No | Maybe |
| **Need to review individual tools?** | No | Yes ✓ | No |
| **Need to review entire workflow?** | No | No | Yes ✓ |
| **Speed is critical?** | Yes ✓ | No | Medium |
| **Compliance/audit required?** | No | Partial | Yes ✓ |
| **Dangerous operation?** | No | Yes ✓ | Yes ✓ |
| **Canvas AI integration?** | No | No | Yes ✓ |
| **Production automation?** | Yes ✓ | No | No |
| **Learning/exploration?** | No | Yes ✓ | No |

---

## Response Differences

### What you get in Response #1

| Mode | Response Content |
|------|------------------|
| **AUTO_ACCEPT** | ✅ Complete results, all tools executed |
| **ASK_BEFORE** | ⚠️ Permission request for first tool |
| **PLAN** | 📋 Execution plan (NO tools executed) |

### How many round trips?

```
AUTO_ACCEPT:  User → Server → User                    (1 call)

ASK_BEFORE:   User → Server → User → Server → User    (3 calls)
                                   → Server → User

PLAN:         User → Server → User → Server → User    (2 calls)
```

---

## JSON Field Indicators

### Detect permission mode in response:

```json
// AUTO_ACCEPT - tools already executed
{
  "tool_executions": [...],  // ← Tools present
  "metadata": {
    "permission_mode": "AUTO_ACCEPT"
  }
}

// ASK_BEFORE - waiting for approval
{
  "metadata": {
    "awaiting_approval": true,  // ← Key indicator
    "pending_tool": {...},
    "permission_mode": "ASK_BEFORE"
  }
}

// PLAN - plan created, not executed
{
  "metadata": {
    "plan_id": "plan_...",
    "plan_status": "PENDING_APPROVAL",  // ← Key indicator
    "steps": [...],
    "permission_mode": "PLAN"
  }
}
```

---

## Common Patterns

### Pattern 1: Automated ETL (AUTO_ACCEPT)
```python
response = client.weave(
    query="Process customers.csv...",
    permission_mode="PERMISSION_MODE_AUTO_ACCEPT"
)
print(response.text)  # Done! All tools executed
```

### Pattern 2: Interactive Session (ASK_BEFORE)
```python
# Initial request
response = client.weave(
    query="Process customers.csv...",
    permission_mode="PERMISSION_MODE_ASK_BEFORE"
)
print(response.text)  # "Approve file_read?"

# Approve first tool
response = client.weave(query="yes", session_id=session_id)
print(response.text)  # "Approve file_write?"

# Approve second tool
response = client.weave(query="yes", session_id=session_id)
print(response.text)  # "Done!"
```

### Pattern 3: Canvas AI Workflow (PLAN)
```python
# Create plan
response = client.weave(
    query="Process customers.csv...",
    permission_mode="PERMISSION_MODE_PLAN"
)
plan_id = response.metadata["plan_id"]
display_plan_in_canvas(response.metadata["steps"])

# User approves in Canvas UI
if user_clicked_approve():
    result = client.approve_plan(plan_id=plan_id)
    display_results(result.execution_results)
```

---

## Proto Field Reference

### WeaveRequest
```protobuf
message WeaveRequest {
  string query = 1;
  string session_id = 2;
  // ... other fields ...
  bool reset_context = 10;           // From main branch
  PermissionMode permission_mode = 11; // From rebased feature
}
```

### PermissionMode Enum
```protobuf
enum PermissionMode {
  PERMISSION_MODE_UNSPECIFIED = 0;  // Default (AUTO_ACCEPT)
  PERMISSION_MODE_AUTO_ACCEPT = 1;  // Execute immediately
  PERMISSION_MODE_ASK_BEFORE = 2;   // Ask before each tool
  PERMISSION_MODE_PLAN = 3;         // Create plan, execute later
}
```

### TraceEvent (for observability)
```protobuf
message TraceEvent {
  // ... other fields ...
  ContextState context_state = 21;    // From main branch
  bool is_plan_created = 22;          // From rebased feature
  bool is_plan_approved = 23;
  bool is_plan_rejected = 24;
  ExecutionPlan plan = 25;
}
```

---

## Testing Commands

### Run simulation (no server needed)
```bash
./examples/etl-test-data/simulate-permission-modes.sh
```

### Show output differences
```bash
./examples/etl-test-data/show-output-differences.sh
```

### Test with real server
```bash
./examples/etl-test-data/test-permission-modes.sh
```

---

## See Also

- **PERMISSION_MODES_COMPARISON.md** - Full JSON examples and detailed explanations
- **README.md** - Complete test suite documentation
- **etl-pipeline-agent.yaml** - Agent configuration
- **Proto definitions:** `proto/loom/v1/loom.proto`

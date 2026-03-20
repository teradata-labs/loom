# Permission Modes - Output & Interaction Comparison

This document shows the **actual outputs and interactions** for each permission mode using an ETL pipeline example.

## Scenario
Process customer data: read CSV, remove duplicates, normalize emails, filter inactive records, write cleaned data.

---

## Mode 1: AUTO_ACCEPT (Immediate Execution)

### Request
```json
POST /v1/weave
{
  "query": "Process customers.csv: remove duplicates, normalize emails to lowercase, filter out inactive/suspended customers, and save to customers_cleaned.csv",
  "permission_mode": "PERMISSION_MODE_AUTO_ACCEPT",
  "session_id": "etl-session-auto"
}
```

### Response (Single call returns complete result)
```json
{
  "text": "✓ ETL pipeline completed successfully!\n\n**Execution Summary:**\n\n1. **Extract Phase**\n   - Read source file: examples/etl-test-data/customers.csv\n   - Loaded 8 customer records\n\n2. **Transform Phase**\n   - Removed duplicates: 1 record (Bob Jones)\n   - Normalized emails: 1 record (eve@EXAMPLE.com → eve@example.com)\n   - Filtered inactive/suspended: 2 records\n   - Result: 5 clean records\n\n3. **Load Phase**\n   - Wrote cleaned data to: examples/etl-test-data/customers_cleaned.csv\n   - Output records: 5\n\n**Metrics:**\n- Input rows: 8\n- Output rows: 5\n- Duplicates removed: 1\n- Emails normalized: 1\n- Inactive filtered: 2\n- Duration: 2.3 seconds\n- Tools executed: 2 (file_read, file_write)",

  "usage": {
    "input_tokens": 1247,
    "output_tokens": 342,
    "total_tokens": 1589
  },

  "tool_executions": [
    {
      "tool_name": "file_read",
      "input": {
        "path": "examples/etl-test-data/customers.csv"
      },
      "result": {
        "success": true,
        "output": "customer_id,name,email,signup_date,status\n1001,Alice Smith,alice@example.com,2024-01-15,active\n...",
        "metadata": {
          "rows_read": 8,
          "file_size_bytes": 412
        }
      }
    },
    {
      "tool_name": "file_write",
      "input": {
        "path": "examples/etl-test-data/customers_cleaned.csv",
        "content": "customer_id,name,email,signup_date,status\n1001,Alice Smith,alice@example.com,2024-01-15,active\n..."
      },
      "result": {
        "success": true,
        "metadata": {
          "rows_written": 5,
          "bytes_written": 287
        }
      }
    }
  ],

  "metadata": {
    "turns": 3,
    "tool_executions": 2,
    "stop_reason": "end_turn",
    "permission_mode": "AUTO_ACCEPT"
  }
}
```

### Interaction Flow
```
User → Request with query
       ↓
Agent → Reads file (AUTO-EXECUTED)
       ↓
Agent → Processes data internally
       ↓
Agent → Writes file (AUTO-EXECUTED)
       ↓
User ← Complete response with results
```

### Key Characteristics
- ✅ **Single API call** - One request, one response
- ✅ **No human intervention** - Tools execute automatically
- ✅ **Fast execution** - Completes in one go
- ❌ **No review opportunity** - Tools run before user sees plan
- **Use case**: Trusted automated pipelines, production workflows

---

## Mode 2: ASK_BEFORE (Require Confirmation)

### Request 1: Initial Query
```json
POST /v1/weave
{
  "query": "Process customers.csv: remove duplicates, normalize emails to lowercase, filter out inactive/suspended customers, and save to customers_cleaned.csv",
  "permission_mode": "PERMISSION_MODE_ASK_BEFORE",
  "session_id": "etl-session-ask"
}
```

### Response 1: Permission Request for First Tool
```json
{
  "text": "I'll help you process the customer data. Let me start by reading the source file.\n\n**Proposed Action:**\n- Tool: file_read\n- File: examples/etl-test-data/customers.csv\n- Purpose: Load customer data for processing\n- Risk: Low (read-only operation)\n\n**Do you approve this operation?**",

  "metadata": {
    "awaiting_approval": true,
    "pending_tool": {
      "tool_name": "file_read",
      "tool_input": {
        "path": "examples/etl-test-data/customers.csv"
      },
      "risk_level": "low",
      "reversible": true
    },
    "permission_mode": "ASK_BEFORE"
  }
}
```

### Request 2: User Approves First Tool
```json
POST /v1/weave
{
  "query": "yes, approved",
  "session_id": "etl-session-ask"
}
```

### Response 2: Tool Executed, Next Permission Request
```json
{
  "text": "✓ File read successfully!\n\n**Data Analysis:**\n- Total records: 8\n- Duplicates found: 1 (Bob Jones appears twice)\n- Emails to normalize: 1 (eve@EXAMPLE.com)\n- Inactive records: 2 (Charlie Brown, Frank Miller)\n\n**Transformation Summary:**\nAfter processing, you'll have 5 clean records.\n\n**Next Action:**\n- Tool: file_write\n- File: examples/etl-test-data/customers_cleaned.csv\n- Content: 5 cleaned customer records\n- Risk: Medium (will create/overwrite file)\n\n**⚠️ Warning:** This will create a new file or overwrite existing data.\n\n**Do you approve this operation?**",

  "tool_executions": [
    {
      "tool_name": "file_read",
      "result": {
        "success": true,
        "metadata": {"rows_read": 8}
      }
    }
  ],

  "metadata": {
    "awaiting_approval": true,
    "pending_tool": {
      "tool_name": "file_write",
      "tool_input": {
        "path": "examples/etl-test-data/customers_cleaned.csv",
        "content": "..."
      },
      "risk_level": "medium",
      "reversible": false,
      "impact": "Creates or overwrites file"
    },
    "permission_mode": "ASK_BEFORE"
  }
}
```

### Request 3: User Approves Second Tool
```json
POST /v1/weave
{
  "query": "yes, write the file",
  "session_id": "etl-session-ask"
}
```

### Response 3: Final Result
```json
{
  "text": "✓ ETL pipeline completed successfully!\n\n**Final Results:**\n- Source file read: ✓\n- Data transformed: ✓\n- Output file written: ✓\n\n**Summary:**\n- Input rows: 8\n- Output rows: 5\n- Duplicates removed: 1\n- Emails normalized: 1\n- Inactive filtered: 2\n- File created: examples/etl-test-data/customers_cleaned.csv\n\nAll operations completed with your approval.",

  "tool_executions": [
    {
      "tool_name": "file_write",
      "result": {
        "success": true,
        "metadata": {"rows_written": 5}
      }
    }
  ],

  "metadata": {
    "total_approvals": 2,
    "total_tools": 2,
    "permission_mode": "ASK_BEFORE"
  }
}
```

### Interaction Flow
```
User → Request with query
       ↓
Agent → "I need to read file, approve?"
       ↓
User → "yes, approved"
       ↓
Agent → Executes file_read
       → "I need to write file, approve?"
       ↓
User → "yes, write the file"
       ↓
Agent → Executes file_write
       ↓
User ← Final results
```

### Key Characteristics
- ⚠️ **Multiple round trips** - 3 API calls total (1 initial + 2 approvals)
- ✅ **User controls each tool** - Can deny dangerous operations
- ✅ **Risk assessment** - Agent explains what each tool does
- ❌ **Slower execution** - Waits for human approval
- **Use case**: Interactive sessions, learning mode, untrusted operations

---

## Mode 3: PLAN (Create Plan, Execute Later)

### Request 1: Create Plan
```json
POST /v1/weave
{
  "query": "Process customers.csv: remove duplicates, normalize emails to lowercase, filter out inactive/suspended customers, and save to customers_cleaned.csv",
  "permission_mode": "PERMISSION_MODE_PLAN",
  "session_id": "etl-session-plan"
}
```

### Response 1: Execution Plan Created (NO TOOLS EXECUTED)
```json
{
  "text": "I've analyzed your request and created an execution plan. Please review the plan below before approving.\n\n**Execution Plan: ETL Customer Data Processing**\n\n┌─────────────────────────────────────────────────────────────────┐\n│ Step 1: Extract Data                                            │\n├─────────────────────────────────────────────────────────────────┤\n│ Tool: file_read                                                 │\n│ File: examples/etl-test-data/customers.csv                      │\n│ Purpose: Load customer records for processing                   │\n│ Expected output: ~8 customer records                            │\n│ Risk: Low (read-only)                                           │\n│ Reversible: Yes                                                 │\n└─────────────────────────────────────────────────────────────────┘\n\n┌─────────────────────────────────────────────────────────────────┐\n│ Step 2: Transform Data (Internal Processing)                    │\n├─────────────────────────────────────────────────────────────────┤\n│ Operations:                                                     │\n│ • Remove duplicate records (estimated: 1 duplicate)             │\n│ • Normalize email addresses to lowercase                        │\n│ • Filter out inactive/suspended customers (estimated: 2)        │\n│ Expected result: ~5 clean records                               │\n│ Risk: None (memory operation)                                   │\n└─────────────────────────────────────────────────────────────────┘\n\n┌─────────────────────────────────────────────────────────────────┐\n│ Step 3: Load Data                                               │\n├─────────────────────────────────────────────────────────────────┤\n│ Tool: file_write                                                │\n│ File: examples/etl-test-data/customers_cleaned.csv              │\n│ Content: Cleaned customer records                               │\n│ Expected size: ~5 records, ~300 bytes                           │\n│ Risk: Medium (will create/overwrite file)                       │\n│ Reversible: No (recommend backup first)                         │\n│ ⚠️  Impact: Creates new file or overwrites existing data        │\n└─────────────────────────────────────────────────────────────────┘\n\n**Plan Summary:**\n- Total steps: 3 (1 read, 1 transform, 1 write)\n- Tools to execute: 2 (file_read, file_write)\n- Files affected: 2 (1 read, 1 write)\n- Estimated duration: 2-3 seconds\n- Risk level: Medium (file write operation)\n- Recommendation: Backup customers_cleaned.csv if it exists\n\n**To proceed:**\n1. Review this plan carefully\n2. Approve the plan to execute all steps\n3. Or reject to cancel without making changes",

  "metadata": {
    "plan_id": "plan_etl_20260319_143052_8a7f",
    "plan_status": "PENDING_APPROVAL",
    "tool_count": 2,
    "steps": [
      {
        "step_number": 1,
        "tool_name": "file_read",
        "tool_input": {
          "path": "examples/etl-test-data/customers.csv"
        },
        "risk_level": "low",
        "reversible": true,
        "estimated_duration_ms": 100
      },
      {
        "step_number": 2,
        "description": "Internal data transformation",
        "operations": ["deduplicate", "normalize_emails", "filter_inactive"],
        "risk_level": "none"
      },
      {
        "step_number": 3,
        "tool_name": "file_write",
        "tool_input": {
          "path": "examples/etl-test-data/customers_cleaned.csv",
          "content": "[TRANSFORMED_DATA]"
        },
        "risk_level": "medium",
        "reversible": false,
        "estimated_duration_ms": 50
      }
    ],
    "created_at": "2026-03-19T14:30:52Z",
    "permission_mode": "PLAN"
  }
}
```

### Request 2: Approve Plan
```json
POST /v1/plans/plan_etl_20260319_143052_8a7f/approve
{
  "session_id": "etl-session-plan"
}
```

### Response 2: Plan Executed, Results Returned
```json
{
  "plan_id": "plan_etl_20260319_143052_8a7f",
  "status": "COMPLETED",

  "execution_results": {
    "steps": [
      {
        "step_number": 1,
        "tool_name": "file_read",
        "status": "SUCCESS",
        "duration_ms": 87,
        "result": {
          "success": true,
          "output": "customer_id,name,email...",
          "metadata": {"rows_read": 8}
        }
      },
      {
        "step_number": 2,
        "description": "Data transformation",
        "status": "SUCCESS",
        "duration_ms": 12,
        "result": {
          "duplicates_removed": 1,
          "emails_normalized": 1,
          "inactive_filtered": 2,
          "output_rows": 5
        }
      },
      {
        "step_number": 3,
        "tool_name": "file_write",
        "status": "SUCCESS",
        "duration_ms": 43,
        "result": {
          "success": true,
          "metadata": {"rows_written": 5, "bytes_written": 287}
        }
      }
    ],

    "summary": {
      "total_steps": 3,
      "successful_steps": 3,
      "failed_steps": 0,
      "total_duration_ms": 142,
      "tools_executed": 2
    }
  },

  "text": "✓ Plan executed successfully!\n\nAll 3 steps completed:\n  ✓ Step 1: Read source file (8 rows)\n  ✓ Step 2: Transform data (5 clean rows)\n  ✓ Step 3: Write output file (5 rows)\n\nETL pipeline completed in 142ms.",

  "completed_at": "2026-03-19T14:31:15Z"
}
```

### Alternative: Reject Plan
```json
POST /v1/plans/plan_etl_20260319_143052_8a7f/reject
{
  "session_id": "etl-session-plan",
  "reason": "Need to review source data first"
}
```

```json
{
  "plan_id": "plan_etl_20260319_143052_8a7f",
  "status": "REJECTED",
  "rejected_at": "2026-03-19T14:30:58Z",
  "rejected_reason": "Need to review source data first",
  "text": "Plan rejected. No changes were made to any files."
}
```

### Interaction Flow
```
User → Request with query
       ↓
Agent → Creates plan (NO EXECUTION)
       → Returns plan for review
       ↓
User → Reviews plan
       → Approves OR Rejects
       ↓ (if approved)
Agent → Executes all steps
       → Returns results
       ↓
User ← Complete execution report
```

### Key Characteristics
- 📋 **Two-phase execution** - Plan creation separate from execution
- ✅ **Review entire workflow** - See all steps before any execution
- ✅ **Risk analysis** - Detailed impact assessment
- ✅ **Atomic approval** - Approve/reject whole plan
- ❌ **Extra round trip** - Requires separate approval call
- **Use case**: Complex workflows, audit requirements, Canvas AI

---

## Side-by-Side Comparison

| Aspect | AUTO_ACCEPT | ASK_BEFORE | PLAN |
|--------|-------------|------------|------|
| **API Calls** | 1 | 3 (1 + 2 approvals) | 2 (create + approve) |
| **Tools Executed** | 2 (automatic) | 2 (after each approval) | 2 (after plan approval) |
| **User Intervention** | None | Before each tool | Before execution batch |
| **Review Granularity** | None | Per-tool | Entire workflow |
| **Response Time** | Fast (1 call) | Slow (3 calls) | Medium (2 calls) |
| **Risk Assessment** | No | Yes (per tool) | Yes (comprehensive) |
| **Reversibility** | No | Partial | No (but no execution if rejected) |
| **Audit Trail** | Basic | Detailed | Complete |
| **Best For** | Trusted automation | Interactive control | Workflow review |

---

## Canvas AI Integration

The **PLAN mode** is specifically designed for Canvas AI integration:

### Canvas AI Workflow
```
User (in Canvas) → "Process customer data..."
                   ↓
Loom Agent        → Creates execution plan
                   → Sends plan to Canvas AI
                   ↓
Canvas AI         → Displays plan visually
                   → Shows risk assessment
                   → User reviews in Canvas UI
                   ↓
User              → Clicks "Approve" in Canvas
                   ↓
Canvas AI         → Sends approval to Loom
                   ↓
Loom Agent        → Executes plan
                   → Returns results to Canvas
                   ↓
Canvas AI         → Displays results
```

### Canvas AI Benefits
- Visual plan representation
- Risk highlighting
- Step-by-step execution tracking
- Integrated approval UI
- Execution progress updates
- Complete audit log

---

## Summary

**Choose AUTO_ACCEPT when:**
- Running trusted, tested workflows
- Speed is critical
- User supervision not needed
- Production automation

**Choose ASK_BEFORE when:**
- Learning how the agent works
- Untrusted or experimental operations
- Need fine-grained control
- Each step may need different decision

**Choose PLAN when:**
- Complex multi-step workflows
- Audit/compliance requirements
- Canvas AI integration
- Want to review entire plan before commitment
- Need comprehensive risk assessment

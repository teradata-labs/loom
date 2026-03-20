#!/bin/bash
# ============================================================================
# Permission Modes - Output Differences Demonstration
# Shows actual JSON outputs and interaction patterns for each mode
# ============================================================================

# Colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
GRAY='\033[0;90m'
NC='\033[0m'

clear

echo "╔════════════════════════════════════════════════════════════════════════════╗"
echo "║              Permission Modes - Output Differences                         ║"
echo "║              Real API Outputs & Interaction Patterns                       ║"
echo "╚════════════════════════════════════════════════════════════════════════════╝"
echo ""
echo "This demo shows the ACTUAL outputs and interactions for each permission mode."
echo ""
echo "Press Enter to continue..."
read

# ============================================================================
# MODE 1: AUTO_ACCEPT
# ============================================================================

clear
echo "╔════════════════════════════════════════════════════════════════════════════╗"
echo "║                     Mode 1: AUTO_ACCEPT Output                            ║"
echo "╚════════════════════════════════════════════════════════════════════════════╝"
echo ""
echo -e "${CYAN}Interaction Pattern: Single Request → Single Response${NC}"
echo ""
echo "┌─────────────────────────────────────────────────────────────────────────┐"
echo "│ 1. User sends request                                                   │"
echo "│ 2. Agent executes ALL tools automatically                               │"
echo "│ 3. User receives complete results                                       │"
echo "└─────────────────────────────────────────────────────────────────────────┘"
echo ""
echo -e "${BLUE}━━━ REQUEST ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
cat << 'EOF' | jq '.'
{
  "query": "Process customers.csv: remove duplicates, normalize emails, filter inactive",
  "permission_mode": "PERMISSION_MODE_AUTO_ACCEPT",
  "session_id": "etl-auto-accept"
}
EOF

echo ""
echo -e "${GREEN}━━━ RESPONSE (Complete in one call) ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
cat << 'EOF' | jq '.'
{
  "text": "✓ ETL pipeline completed!\n\n**Summary:**\n- Input: 8 rows\n- Output: 5 rows\n- Duplicates removed: 1\n- Emails normalized: 1\n- Inactive filtered: 2\n- Duration: 2.3s",

  "tool_executions": [
    {
      "tool_name": "file_read",
      "input": {"path": "customers.csv"},
      "result": {"success": true, "rows_read": 8}
    },
    {
      "tool_name": "file_write",
      "input": {"path": "customers_cleaned.csv"},
      "result": {"success": true, "rows_written": 5}
    }
  ],

  "metadata": {
    "turns": 3,
    "tool_executions": 2,
    "permission_mode": "AUTO_ACCEPT"
  }
}
EOF

echo ""
echo -e "${YELLOW}Key Points:${NC}"
echo "  • Tools executed: 2 (both automatic)"
echo "  • API calls: 1 (request → response)"
echo "  • User approvals: 0"
echo "  • Time: Fast (single round trip)"
echo ""
echo "Press Enter to see ASK_BEFORE mode..."
read

# ============================================================================
# MODE 2: ASK_BEFORE
# ============================================================================

clear
echo "╔════════════════════════════════════════════════════════════════════════════╗"
echo "║                     Mode 2: ASK_BEFORE Output                             ║"
echo "╚════════════════════════════════════════════════════════════════════════════╝"
echo ""
echo -e "${CYAN}Interaction Pattern: Multiple Round Trips${NC}"
echo ""
echo "┌─────────────────────────────────────────────────────────────────────────┐"
echo "│ 1. User sends request                                                   │"
echo "│ 2. Agent asks permission for tool #1                                    │"
echo "│ 3. User approves                                                        │"
echo "│ 4. Agent executes tool #1, then asks permission for tool #2            │"
echo "│ 5. User approves                                                        │"
echo "│ 6. Agent executes tool #2 and returns final results                    │"
echo "└─────────────────────────────────────────────────────────────────────────┘"
echo ""

echo -e "${BLUE}━━━ REQUEST #1: Initial Query ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
cat << 'EOF' | jq '.'
{
  "query": "Process customers.csv: remove duplicates, normalize emails, filter inactive",
  "permission_mode": "PERMISSION_MODE_ASK_BEFORE",
  "session_id": "etl-ask-before"
}
EOF

echo ""
echo -e "${GREEN}━━━ RESPONSE #1: Permission Request ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
cat << 'EOF' | jq '.'
{
  "text": "I need to read the source file.\n\n**Proposed Action:**\n- Tool: file_read\n- File: customers.csv\n- Risk: Low (read-only)\n\n**Do you approve?**",

  "metadata": {
    "awaiting_approval": true,
    "pending_tool": {
      "tool_name": "file_read",
      "tool_input": {"path": "customers.csv"},
      "risk_level": "low"
    },
    "permission_mode": "ASK_BEFORE"
  }
}
EOF

echo ""
echo -e "${YELLOW}⚠️  Agent is WAITING for approval. No tools executed yet.${NC}"
echo ""
echo "Press Enter to approve..."
read

echo ""
echo -e "${BLUE}━━━ REQUEST #2: User Approval ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
cat << 'EOF' | jq '.'
{
  "query": "yes, approved",
  "session_id": "etl-ask-before"
}
EOF

echo ""
echo -e "${GREEN}━━━ RESPONSE #2: Tool Executed, Next Permission Request ━━━━━━━━━━━━━${NC}"
echo ""
cat << 'EOF' | jq '.'
{
  "text": "✓ File read (8 rows)\n\n**Analysis:**\n- Duplicates: 1\n- Emails to normalize: 1\n- Inactive: 2\n- Output will be: 5 rows\n\n**Next Action:**\n- Tool: file_write\n- File: customers_cleaned.csv\n- Risk: Medium (overwrites file)\n\n⚠️  Will create/overwrite file\n\n**Do you approve?**",

  "tool_executions": [
    {
      "tool_name": "file_read",
      "result": {"success": true, "rows_read": 8}
    }
  ],

  "metadata": {
    "awaiting_approval": true,
    "pending_tool": {
      "tool_name": "file_write",
      "risk_level": "medium"
    }
  }
}
EOF

echo ""
echo -e "${YELLOW}⚠️  Agent executed file_read, now waiting for approval to write.${NC}"
echo ""
echo "Press Enter to approve..."
read

echo ""
echo -e "${BLUE}━━━ REQUEST #3: Second Approval ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
cat << 'EOF' | jq '.'
{
  "query": "yes, write the file",
  "session_id": "etl-ask-before"
}
EOF

echo ""
echo -e "${GREEN}━━━ RESPONSE #3: Final Results ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
cat << 'EOF' | jq '.'
{
  "text": "✓ Pipeline completed!\n\n- Input: 8 rows\n- Output: 5 rows\n- All approved by user",

  "tool_executions": [
    {
      "tool_name": "file_write",
      "result": {"success": true, "rows_written": 5}
    }
  ],

  "metadata": {
    "total_approvals": 2,
    "total_tools": 2,
    "permission_mode": "ASK_BEFORE"
  }
}
EOF

echo ""
echo -e "${YELLOW}Key Points:${NC}"
echo "  • Tools executed: 2 (both after approval)"
echo "  • API calls: 3 (1 initial + 2 approvals)"
echo "  • User approvals: 2 (one per tool)"
echo "  • Time: Slower (multiple round trips)"
echo ""
echo "Press Enter to see PLAN mode..."
read

# ============================================================================
# MODE 3: PLAN
# ============================================================================

clear
echo "╔════════════════════════════════════════════════════════════════════════════╗"
echo "║                     Mode 3: PLAN Output                                   ║"
echo "╚════════════════════════════════════════════════════════════════════════════╝"
echo ""
echo -e "${CYAN}Interaction Pattern: Plan → Review → Execute${NC}"
echo ""
echo "┌─────────────────────────────────────────────────────────────────────────┐"
echo "│ 1. User sends request                                                   │"
echo "│ 2. Agent creates plan (NO EXECUTION)                                    │"
echo "│ 3. User reviews entire plan                                             │"
echo "│ 4. User approves plan via separate API call                             │"
echo "│ 5. Agent executes ALL steps in plan                                     │"
echo "│ 6. User receives execution report                                       │"
echo "└─────────────────────────────────────────────────────────────────────────┘"
echo ""

echo -e "${BLUE}━━━ REQUEST #1: Create Plan ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
cat << 'EOF' | jq '.'
{
  "query": "Process customers.csv: remove duplicates, normalize emails, filter inactive",
  "permission_mode": "PERMISSION_MODE_PLAN",
  "session_id": "etl-plan-mode"
}
EOF

echo ""
echo -e "${GREEN}━━━ RESPONSE #1: Plan Created (NO TOOLS EXECUTED) ━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
cat << 'EOF' | jq '.'
{
  "text": "📋 Execution Plan Created\n\nSteps:\n  1. file_read (customers.csv) - Risk: Low\n  2. Transform data internally\n  3. file_write (customers_cleaned.csv) - Risk: Medium\n\n⚠️  Step 3 will overwrite file\n\nReview plan before approving.",

  "metadata": {
    "plan_id": "plan_etl_20260319_143052",
    "plan_status": "PENDING_APPROVAL",
    "tool_count": 2,
    "steps": [
      {
        "step": 1,
        "tool": "file_read",
        "path": "customers.csv",
        "risk": "low"
      },
      {
        "step": 2,
        "description": "Transform",
        "risk": "none"
      },
      {
        "step": 3,
        "tool": "file_write",
        "path": "customers_cleaned.csv",
        "risk": "medium"
      }
    ],
    "permission_mode": "PLAN"
  }
}
EOF

echo ""
echo -e "${MAGENTA}⚡ IMPORTANT: Plan created but NO tools executed yet!${NC}"
echo "   User can review the entire workflow before anything runs."
echo ""
echo "Press Enter to approve plan..."
read

echo ""
echo -e "${BLUE}━━━ REQUEST #2: Approve Plan (Separate API Endpoint) ━━━━━━━━━━━━━━━━${NC}"
echo ""
cat << 'EOF' | jq '.'
{
  "plan_id": "plan_etl_20260319_143052",
  "session_id": "etl-plan-mode"
}
EOF
echo ""
echo -e "${GRAY}POST /v1/plans/plan_etl_20260319_143052/approve${NC}"
echo ""

echo -e "${GREEN}━━━ RESPONSE #2: Plan Executed, Results Report ━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
cat << 'EOF' | jq '.'
{
  "plan_id": "plan_etl_20260319_143052",
  "status": "COMPLETED",

  "execution_results": {
    "steps": [
      {
        "step": 1,
        "tool": "file_read",
        "status": "SUCCESS",
        "duration_ms": 87,
        "result": {"rows_read": 8}
      },
      {
        "step": 2,
        "description": "Transform",
        "status": "SUCCESS",
        "result": {
          "duplicates_removed": 1,
          "emails_normalized": 1,
          "output_rows": 5
        }
      },
      {
        "step": 3,
        "tool": "file_write",
        "status": "SUCCESS",
        "duration_ms": 43,
        "result": {"rows_written": 5}
      }
    ],
    "summary": {
      "total_steps": 3,
      "successful": 3,
      "failed": 0,
      "duration_ms": 142
    }
  },

  "text": "✓ Plan executed successfully!\nAll 3 steps completed in 142ms"
}
EOF

echo ""
echo -e "${YELLOW}Key Points:${NC}"
echo "  • Tools executed: 2 (after plan approval)"
echo "  • API calls: 2 (create plan + approve plan)"
echo "  • User approvals: 1 (entire plan at once)"
echo "  • Time: Medium (two-phase execution)"
echo "  • Review: Complete workflow before any execution"
echo ""
echo "Press Enter to see comparison..."
read

# ============================================================================
# COMPARISON TABLE
# ============================================================================

clear
echo "╔════════════════════════════════════════════════════════════════════════════╗"
echo "║                        Output Comparison Summary                           ║"
echo "╚════════════════════════════════════════════════════════════════════════════╝"
echo ""

cat << 'EOF'
┌─────────────────┬──────────────────┬──────────────────┬──────────────────┐
│ Characteristic  │  AUTO_ACCEPT     │   ASK_BEFORE     │      PLAN        │
├─────────────────┼──────────────────┼──────────────────┼──────────────────┤
│ API Calls       │ 1 total          │ 3 total          │ 2 total          │
│                 │ (request+result) │ (1 + 2 approvals)│ (plan + approve) │
├─────────────────┼──────────────────┼──────────────────┼──────────────────┤
│ Tools Run       │ 2 (automatic)    │ 2 (after each OK)│ 2 (after 1 OK)   │
├─────────────────┼──────────────────┼──────────────────┼──────────────────┤
│ Approval Points │ None             │ Before each tool │ Before execution │
├─────────────────┼──────────────────┼──────────────────┼──────────────────┤
│ Response #1     │ Complete results │ "Approve file    │ "Plan created,   │
│                 │                  │  read?"          │  review it"      │
├─────────────────┼──────────────────┼──────────────────┼──────────────────┤
│ Response #2     │ N/A              │ "Approve file    │ Execution report │
│                 │                  │  write?"         │                  │
├─────────────────┼──────────────────┼──────────────────┼──────────────────┤
│ Response #3     │ N/A              │ Final results    │ N/A              │
├─────────────────┼──────────────────┼──────────────────┼──────────────────┤
│ Risk Info       │ No               │ Per-tool         │ Comprehensive    │
├─────────────────┼──────────────────┼──────────────────┼──────────────────┤
│ Can Cancel      │ No (auto-runs)   │ Yes (per tool)   │ Yes (before exec)│
├─────────────────┼──────────────────┼──────────────────┼──────────────────┤
│ Audit Detail    │ Basic            │ Detailed         │ Complete         │
└─────────────────┴──────────────────┴──────────────────┴──────────────────┘
EOF

echo ""
echo -e "${CYAN}Real-World Examples:${NC}"
echo ""
echo -e "${GREEN}AUTO_ACCEPT${NC} - Production ETL:"
echo "  curl → Agent runs → Results in 2.3s"
echo ""
echo -e "${YELLOW}ASK_BEFORE${NC} - Interactive Session:"
echo "  curl → 'Read file?' → User: 'yes' → 'Write file?' → User: 'yes' → Done"
echo ""
echo -e "${MAGENTA}PLAN${NC} - Canvas AI:"
echo "  curl → Plan displayed in Canvas → User clicks Approve → Execution → Done"
echo ""

echo "╔════════════════════════════════════════════════════════════════════════════╗"
echo "║                            Demo Complete                                   ║"
echo "╚════════════════════════════════════════════════════════════════════════════╝"
echo ""
echo "See PERMISSION_MODES_COMPARISON.md for full JSON examples"
echo ""

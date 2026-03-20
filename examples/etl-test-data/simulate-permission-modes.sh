#!/bin/bash
# ============================================================================
# Permission Modes Simulation Script
# Simulates the output for AUTO_ACCEPT, ASK_BEFORE, and PLAN modes
# Demonstrates the new permission mode features from the rebase
# ============================================================================

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
NC='\033[0m' # No Color

clear

echo "╔════════════════════════════════════════════════════════════════════════════╗"
echo "║                    ETL Pipeline - Permission Modes Demo                   ║"
echo "║                   Testing Rebased Permission Features                      ║"
echo "╚════════════════════════════════════════════════════════════════════════════╝"
echo ""

# ETL Query
ETL_QUERY="Process customers.csv: remove duplicates, normalize emails, filter inactive, save to customers_cleaned.csv"

echo -e "${CYAN}Sample Data: examples/etl-test-data/customers.csv${NC}"
echo "─────────────────────────────────────────────────────────────────────"
cat << 'EOF'
customer_id,name,email,signup_date,status
1001,Alice Smith,alice@example.com,2024-01-15,active
1002,Bob Jones,bob@example.com,2024-01-20,active
1003,Charlie Brown,charlie@example.com,2024-02-01,inactive
1004,Diana Prince,diana@example.com,2024-02-10,active
1005,Eve Wilson,eve@EXAMPLE.com,2024-02-15,active
1006,Frank Miller,frank@example.com,2024-03-01,suspended
1007,Grace Lee,grace@example.com,2024-03-05,active
1008,Bob Jones,bob@example.com,2024-01-20,active  [DUPLICATE]
EOF
echo "─────────────────────────────────────────────────────────────────────"
echo ""
echo -e "${YELLOW}ETL Query:${NC} ${ETL_QUERY}"
echo ""
echo "Press Enter to see permission modes..."
read

# ============================================================================
# Mode 1: AUTO_ACCEPT (Immediate Execution)
# ============================================================================

clear
echo "╔════════════════════════════════════════════════════════════════════════════╗"
echo "║            Mode 1: PERMISSION_MODE_AUTO_ACCEPT (0)                        ║"
echo "║            Executes tools immediately without asking                       ║"
echo "╚════════════════════════════════════════════════════════════════════════════╝"
echo ""

echo -e "${BLUE}Request:${NC}"
cat << 'EOF'
{
  "query": "Process customers.csv: remove duplicates, normalize emails...",
  "permission_mode": "PERMISSION_MODE_AUTO_ACCEPT",
  "session_id": "etl-auto-accept"
}
EOF
echo ""

sleep 1

echo -e "${GREEN}Agent Response:${NC}"
echo "─────────────────────────────────────────────────────────────────────"
echo ""
echo -e "${CYAN}[Turn 1]${NC} Reading source data..."
echo -e "  ${YELLOW}▶${NC} Executing tool: file_read(path='customers.csv')"
sleep 0.5
echo -e "  ${GREEN}✓${NC} Read 8 rows from customers.csv"
echo ""

echo -e "${CYAN}[Turn 2]${NC} Processing data..."
echo -e "  ${YELLOW}▶${NC} Applying transformations:"
echo "    - Removing duplicates: Found 1 duplicate (Bob Jones)"
echo "    - Normalizing emails: Converted eve@EXAMPLE.com → eve@example.com"
echo "    - Filtering: Removed 2 rows (inactive/suspended)"
sleep 0.5
echo -e "  ${GREEN}✓${NC} Transformations complete"
echo ""

echo -e "${CYAN}[Turn 3]${NC} Writing output..."
echo -e "  ${YELLOW}▶${NC} Executing tool: file_write(path='customers_cleaned.csv')"
sleep 0.5
echo -e "  ${GREEN}✓${NC} Wrote 5 rows to customers_cleaned.csv"
echo ""

echo -e "${GREEN}Response Summary:${NC}"
cat << 'EOF'
{
  "text": "✓ ETL pipeline completed successfully\n\nMetrics:\n- Input rows: 8\n- Duplicates removed: 1\n- Emails normalized: 1\n- Inactive filtered: 2\n- Output rows: 5\n- Duration: 2.3s",
  "tool_executions": [
    {"tool_name": "file_read", "success": true},
    {"tool_name": "file_write", "success": true}
  ],
  "metadata": {
    "turns": 3,
    "tool_executions": 2,
    "permission_mode": "AUTO_ACCEPT"
  }
}
EOF
echo ""
echo "─────────────────────────────────────────────────────────────────────"
echo ""
echo -e "${GREEN}✓ Tools executed automatically (no human intervention)${NC}"
echo ""
echo "Press Enter to see ASK_BEFORE mode..."
read

# ============================================================================
# Mode 2: ASK_BEFORE (Require Confirmation)
# ============================================================================

clear
echo "╔════════════════════════════════════════════════════════════════════════════╗"
echo "║            Mode 2: PERMISSION_MODE_ASK_BEFORE (1)                         ║"
echo "║            Asks for confirmation before each tool execution                ║"
echo "╚════════════════════════════════════════════════════════════════════════════╝"
echo ""

echo -e "${BLUE}Request:${NC}"
cat << 'EOF'
{
  "query": "Process customers.csv: remove duplicates, normalize emails...",
  "permission_mode": "PERMISSION_MODE_ASK_BEFORE",
  "session_id": "etl-ask-before"
}
EOF
echo ""

sleep 1

echo -e "${GREEN}Agent Response (Turn 1):${NC}"
echo "─────────────────────────────────────────────────────────────────────"
echo ""
echo -e "${CYAN}Agent:${NC} I need to read the source file to begin processing."
echo ""
echo -e "${YELLOW}⚠ Requesting Permission:${NC}"
echo "  Tool: file_read"
echo "  Path: examples/etl-test-data/customers.csv"
echo "  Action: Read customer data for ETL processing"
echo ""
echo -e "${RED}Do you approve? (y/n):${NC} ${GREEN}y${NC}"
echo ""
sleep 1

echo -e "${GREEN}✓ Permission granted${NC}"
echo -e "  ${YELLOW}▶${NC} Executing file_read..."
echo -e "  ${GREEN}✓${NC} Read 8 rows"
echo ""

echo -e "${GREEN}Agent Response (Turn 2):${NC}"
echo "─────────────────────────────────────────────────────────────────────"
echo ""
echo -e "${CYAN}Agent:${NC} Data analysis complete. Ready to write cleaned data."
echo ""
echo "  Transformations applied:"
echo "    - Removed 1 duplicate (Bob Jones)"
echo "    - Normalized 1 email (eve@example.com)"
echo "    - Filtered 2 inactive records"
echo "  Result: 5 rows ready for output"
echo ""
echo -e "${YELLOW}⚠ Requesting Permission:${NC}"
echo "  Tool: file_write"
echo "  Path: examples/etl-test-data/customers_cleaned.csv"
echo "  Action: Write 5 cleaned customer records"
echo ""
echo -e "${RED}Do you approve? (y/n):${NC} ${GREEN}y${NC}"
echo ""
sleep 1

echo -e "${GREEN}✓ Permission granted${NC}"
echo -e "  ${YELLOW}▶${NC} Executing file_write..."
echo -e "  ${GREEN}✓${NC} Wrote 5 rows to customers_cleaned.csv"
echo ""

echo -e "${GREEN}Final Response:${NC}"
cat << 'EOF'
{
  "text": "✓ ETL pipeline completed with user approval\n\nMetrics:\n- Output rows: 5\n- Approvals required: 2",
  "metadata": {
    "permission_mode": "ASK_BEFORE",
    "approvals_required": 2,
    "approvals_granted": 2
  }
}
EOF
echo ""
echo "─────────────────────────────────────────────────────────────────────"
echo ""
echo -e "${YELLOW}⚠ Required human approval before each tool execution${NC}"
echo ""
echo "Press Enter to see PLAN mode..."
read

# ============================================================================
# Mode 3: PLAN (Create Plan Without Executing)
# ============================================================================

clear
echo "╔════════════════════════════════════════════════════════════════════════════╗"
echo "║            Mode 3: PERMISSION_MODE_PLAN (2)                                ║"
echo "║            Creates execution plan without running tools                    ║"
echo "╚════════════════════════════════════════════════════════════════════════════╝"
echo ""

echo -e "${BLUE}Request:${NC}"
cat << 'EOF'
{
  "query": "Process customers.csv: remove duplicates, normalize emails...",
  "permission_mode": "PERMISSION_MODE_PLAN",
  "session_id": "etl-plan-mode"
}
EOF
echo ""

sleep 1

echo -e "${GREEN}Agent Response:${NC}"
echo "─────────────────────────────────────────────────────────────────────"
echo ""
echo -e "${CYAN}[PLAN MODE]${NC} Agent is creating execution plan..."
echo ""
sleep 0.5

echo -e "${MAGENTA}Execution Plan Created${NC}"
echo ""
echo "  Plan ID: plan_etl_20260319_143052"
echo "  Status: PENDING_APPROVAL"
echo "  Created: 2026-03-19T14:30:52Z"
echo ""
echo "  Steps:"
echo "  ┌─────────────────────────────────────────────────────────────┐"
echo "  │ 1. file_read                                                │"
echo "  │    Path: examples/etl-test-data/customers.csv               │"
echo "  │    Purpose: Read source data for ETL processing             │"
echo "  │    Risk: Low (read-only operation)                          │"
echo "  ├─────────────────────────────────────────────────────────────┤"
echo "  │ 2. [Internal] Data transformation                           │"
echo "  │    - Remove duplicates (estimated: 1 row)                   │"
echo "  │    - Normalize email addresses                              │"
echo "  │    - Filter inactive/suspended (estimated: 2 rows)          │"
echo "  │    Risk: None (memory operation)                            │"
echo "  ├─────────────────────────────────────────────────────────────┤"
echo "  │ 3. file_write                                               │"
echo "  │    Path: examples/etl-test-data/customers_cleaned.csv       │"
echo "  │    Purpose: Save cleaned customer data                      │"
echo "  │    Risk: Medium (will overwrite existing file)              │"
echo "  │    Safety: Backup recommended before approval               │"
echo "  └─────────────────────────────────────────────────────────────┘"
echo ""
echo "  Estimated impact:"
echo "    - Files read: 1"
echo "    - Files written: 1"
echo "    - Rows processed: ~8"
echo "    - Expected duration: 2-3 seconds"
echo ""

echo -e "${GREEN}Response:${NC}"
cat << 'EOF'
{
  "text": "I've created an execution plan with 3 steps. Please review and approve to proceed.",
  "metadata": {
    "plan_id": "plan_etl_20260319_143052",
    "plan_status": "PENDING_APPROVAL",
    "tool_count": 2,
    "permission_mode": "PLAN"
  }
}
EOF
echo ""
echo "─────────────────────────────────────────────────────────────────────"
echo ""
echo -e "${MAGENTA}⚡ Plan created but NOT executed${NC}"
echo -e "${YELLOW}   User can review plan before approving execution${NC}"
echo ""

echo "To approve and execute this plan:"
echo ""
cat << 'EOF'
POST /v1/plans/plan_etl_20260319_143052/approve
{
  "session_id": "etl-plan-mode"
}
EOF
echo ""
echo "Press Enter to see comparison..."
read

# ============================================================================
# Comparison Summary
# ============================================================================

clear
echo "╔════════════════════════════════════════════════════════════════════════════╗"
echo "║                     Permission Modes Comparison                            ║"
echo "╚════════════════════════════════════════════════════════════════════════════╝"
echo ""

echo "┌──────────────────┬─────────────────────────────────────────────────────┐"
echo "│ Mode             │ Behavior                                            │"
echo "├──────────────────┼─────────────────────────────────────────────────────┤"
echo "│ AUTO_ACCEPT      │ ✓ Executes tools immediately                        │"
echo "│                  │ ✓ No user intervention required                     │"
echo "│                  │ ✓ Fast execution                                    │"
echo "│                  │ ⚠ No review before execution                        │"
echo "├──────────────────┼─────────────────────────────────────────────────────┤"
echo "│ ASK_BEFORE       │ ⚠ Asks permission before each tool                  │"
echo "│                  │ ✓ User reviews each action                          │"
echo "│                  │ ✓ Can deny dangerous operations                     │"
echo "│                  │ ⚠ Slower (requires human approval)                  │"
echo "├──────────────────┼─────────────────────────────────────────────────────┤"
echo "│ PLAN             │ 📋 Creates execution plan only                      │"
echo "│                  │ ✓ Review entire workflow before execution           │"
echo "│                  │ ✓ Approve/reject whole plan                         │"
echo "│                  │ ✓ See impact analysis                               │"
echo "│                  │ ⚠ Requires separate approval step                   │"
echo "└──────────────────┴─────────────────────────────────────────────────────┘"
echo ""

echo "Use Cases:"
echo ""
echo "  ${GREEN}AUTO_ACCEPT${NC}  → Trusted operations, automated pipelines"
echo "  ${YELLOW}ASK_BEFORE${NC}   → Interactive sessions, learning mode"
echo "  ${MAGENTA}PLAN${NC}         → Complex workflows, audit requirements, Canvas AI"
echo ""

echo "Proto Field Numbers (from rebase):"
echo ""
echo "  WeaveRequest:"
echo "    - reset_context: field 10 (bool)"
echo "    - permission_mode: field 11 (PermissionMode enum)"
echo ""
echo "  TraceEvent:"
echo "    - context_state: field 21 (ContextState)"
echo "    - is_plan_created: field 22 (bool)"
echo "    - is_plan_approved: field 23 (bool)"
echo "    - is_plan_rejected: field 24 (bool)"
echo "    - plan: field 25 (ExecutionPlan)"
echo ""

echo "╔════════════════════════════════════════════════════════════════════════════╗"
echo "║                     Simulation Complete                                    ║"
echo "╚════════════════════════════════════════════════════════════════════════════╝"
echo ""

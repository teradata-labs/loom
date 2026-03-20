#!/bin/bash
# ============================================================================
# Permission Modes Test Script
# Demonstrates AUTO_ACCEPT, ASK_BEFORE, and PLAN modes with ETL pipeline
# ============================================================================

set -e

echo "╔══════════════════════════════════════════════════════════════════════╗"
echo "║         ETL Pipeline - Permission Modes Test                        ║"
echo "║         Testing rebased permission mode features                    ║"
echo "╚══════════════════════════════════════════════════════════════════════╝"
echo ""

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Configuration
SERVER_PORT=9091
BASE_URL="http://localhost:${SERVER_PORT}"
AGENT_CONFIG="examples/reference/agents/etl-pipeline-agent.yaml"

# Test query
ETL_QUERY="Process the customer data in examples/etl-test-data/customers.csv: remove duplicates, normalize email addresses to lowercase, filter out inactive/suspended customers, and save the cleaned data to examples/etl-test-data/customers_cleaned.csv"

# ============================================================================
# Helper Functions
# ============================================================================

start_server() {
    echo -e "${BLUE}Starting Loom server on port ${SERVER_PORT}...${NC}"

    # Check if server is already running
    if lsof -Pi :${SERVER_PORT} -sTCP:LISTEN -t >/dev/null 2>&1; then
        echo -e "${YELLOW}Server already running on port ${SERVER_PORT}${NC}"
        return 0
    fi

    # Start server in background
    ./bin/looms \
        --config tests/config/looms-test.yaml \
        --port ${SERVER_PORT} \
        --agent-path ${AGENT_CONFIG} \
        > /tmp/loom-server.log 2>&1 &

    SERVER_PID=$!
    echo "Server PID: ${SERVER_PID}"

    # Wait for server to start
    echo "Waiting for server to start..."
    for i in {1..30}; do
        if curl -s "${BASE_URL}/healthz" > /dev/null 2>&1; then
            echo -e "${GREEN}✓ Server started successfully${NC}"
            return 0
        fi
        sleep 1
    done

    echo -e "${RED}✗ Failed to start server${NC}"
    return 1
}

stop_server() {
    echo -e "${BLUE}Stopping server...${NC}"
    if [ ! -z "$SERVER_PID" ]; then
        kill $SERVER_PID 2>/dev/null || true
        wait $SERVER_PID 2>/dev/null || true
    fi

    # Also kill any servers on the port
    lsof -ti:${SERVER_PORT} | xargs kill -9 2>/dev/null || true
    echo -e "${GREEN}✓ Server stopped${NC}"
}

test_mode() {
    local mode=$1
    local mode_name=$2

    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo -e "${BLUE}Testing Permission Mode: ${mode_name}${NC}"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""

    # Create request payload
    local payload=$(cat <<EOF
{
  "query": "${ETL_QUERY}",
  "permission_mode": "${mode}",
  "session_id": "test-session-${mode}"
}
EOF
)

    echo -e "${YELLOW}Request:${NC}"
    echo "$payload" | jq '.'
    echo ""

    echo -e "${YELLOW}Sending request...${NC}"

    # Send request
    response=$(curl -s -X POST \
        "${BASE_URL}/v1/weave" \
        -H "Content-Type: application/json" \
        -d "$payload")

    echo ""
    echo -e "${GREEN}Response:${NC}"
    echo "$response" | jq '.'
    echo ""

    # Extract key information
    if echo "$response" | jq -e '.text' > /dev/null 2>&1; then
        echo -e "${GREEN}✓ Request completed${NC}"

        # Show tool executions if in response
        if echo "$response" | jq -e '.tool_executions' > /dev/null 2>&1; then
            local tool_count=$(echo "$response" | jq '.tool_executions | length')
            echo -e "${BLUE}Tools executed: ${tool_count}${NC}"
            echo "$response" | jq '.tool_executions[] | {tool: .tool_name, success: .result.success}'
        fi

        # Show plan info if in PLAN mode
        if echo "$response" | jq -e '.metadata.plan_id' > /dev/null 2>&1; then
            echo -e "${YELLOW}Plan created:${NC}"
            echo "$response" | jq '.metadata | {plan_id, plan_status, tool_count}'
        fi
    else
        echo -e "${RED}✗ Request failed${NC}"
        echo "$response" | jq '.error // .'
    fi

    echo ""
    echo "Press Enter to continue..."
    read
}

# ============================================================================
# Main Test Flow
# ============================================================================

# Trap to ensure cleanup
trap stop_server EXIT

# Build binaries
echo -e "${BLUE}Building Loom binaries...${NC}"
just build || {
    echo -e "${RED}Build failed${NC}"
    exit 1
}
echo -e "${GREEN}✓ Build complete${NC}"
echo ""

# Start server
start_server || {
    echo -e "${RED}Failed to start server${NC}"
    exit 1
}

echo ""
echo "╔══════════════════════════════════════════════════════════════════════╗"
echo "║                     Test Scenarios                                   ║"
echo "╚══════════════════════════════════════════════════════════════════════╝"
echo ""
echo "We'll test 3 permission modes with the same ETL query:"
echo ""
echo "1. AUTO_ACCEPT (0)   - Executes tools automatically"
echo "2. ASK_BEFORE (1)    - Asks before executing each tool"
echo "3. PLAN (2)          - Creates execution plan without executing"
echo ""
echo "Query: ${ETL_QUERY}"
echo ""
echo "Press Enter to start tests..."
read

# Test Mode 1: AUTO_ACCEPT
test_mode "PERMISSION_MODE_AUTO_ACCEPT" "AUTO_ACCEPT (Immediate Execution)"

# Test Mode 2: ASK_BEFORE
test_mode "PERMISSION_MODE_ASK_BEFORE" "ASK_BEFORE (Require Confirmation)"

# Test Mode 3: PLAN
test_mode "PERMISSION_MODE_PLAN" "PLAN (Create Plan Only)"

echo ""
echo "╔══════════════════════════════════════════════════════════════════════╗"
echo "║                     Test Complete                                    ║"
echo "╚══════════════════════════════════════════════════════════════════════╝"
echo ""
echo -e "${GREEN}All permission mode tests completed!${NC}"
echo ""
echo "Summary:"
echo "- AUTO_ACCEPT: Executed tools immediately"
echo "- ASK_BEFORE: Required confirmation for each tool"
echo "- PLAN: Created execution plan without executing"
echo ""
echo "Check /tmp/loom-server.log for detailed server logs"
echo ""

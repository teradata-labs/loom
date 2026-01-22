#!/bin/bash
# Test all workflow patterns in Loom

set -e

echo "======================================"
echo "Testing All Loom Workflow Patterns"
echo "======================================"
echo ""

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Test counter
PASSED=0
FAILED=0

test_workflow() {
    local name="$1"
    local thread="$2"
    local prompt="$3"

    echo -e "${BLUE}Testing: $name${NC}"
    echo "Thread: $thread"
    echo "Prompt: $prompt"
    echo ""

    if ./bin/loom chat --thread "$thread" "$prompt" 2>&1 | grep -q "Query completed successfully\|completed successfully"; then
        echo -e "${GREEN}✓ PASSED: $name${NC}"
        ((PASSED++))
    else
        echo -e "${RED}✗ FAILED: $name${NC}"
        ((FAILED++))
    fi
    echo ""
    echo "--------------------------------------"
    echo ""
}

# Ensure server is running
if ! pgrep -f "looms serve" > /dev/null; then
    echo -e "${RED}Error: loom server is not running${NC}"
    echo "Start with: looms serve --llm-provider bedrock --yolo"
    exit 1
fi

echo "Server detected. Running tests..."
echo ""

# ====================
# DYNAMIC COMMUNICATION PATTERNS
# ====================

echo "======================================"
echo "1. DYNAMIC COMMUNICATION PATTERNS"
echo "======================================"
echo ""

# Pub-Sub Pattern
test_workflow \
    "Pub-Sub: Brainstorm Session" \
    "brainstorm-session" \
    "Brainstorm creative names for a new AI coding assistant"

test_workflow \
    "Pub-Sub: Dungeon Crawler" \
    "dungeon-crawl-workflow" \
    "DM, the party discovers a mysterious glowing portal"

# Hub-and-Spoke Pattern
test_workflow \
    "Hub-and-Spoke: DND Campaign Builder" \
    "dnd-campaign-workflow" \
    "Create a 2-session campaign for level 3 players in a haunted forest"

# ====================
# STATIC ORCHESTRATION PATTERNS
# ====================

echo ""
echo "======================================"
echo "2. STATIC ORCHESTRATION PATTERNS"
echo "======================================"
echo ""

# Note: These require workflow execution support, not chat
# For now, we'll document them as needing workflow CLI

echo -e "${BLUE}Static patterns require workflow execution CLI${NC}"
echo "These examples exist but need 'looms workflow run' command:"
echo ""
echo "  1. Pipeline: examples/reference/workflows/feature-pipeline.yaml"
echo "  2. Fork-Join: examples/reference/workflows/code-review.yaml"
echo "  3. Parallel: examples/reference/workflows/doc-generation.yaml"
echo "  4. Debate: examples/reference/workflows/architecture-debate.yaml"
echo "  5. Conditional: examples/reference/workflows/complexity-routing.yaml"
echo "  6. Swarm: examples/reference/workflows/technology-swarm.yaml"
echo ""
echo -e "${BLUE}To test these, use:${NC}"
echo "  looms workflow run examples/reference/workflows/feature-pipeline.yaml"
echo ""

# ====================
# RESULTS
# ====================

echo "======================================"
echo "TEST RESULTS"
echo "======================================"
echo ""
echo -e "Passed: ${GREEN}$PASSED${NC}"
echo -e "Failed: ${RED}$FAILED${NC}"
echo ""

if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}All dynamic communication pattern tests passed!${NC}"
    exit 0
else
    echo -e "${RED}Some tests failed. Check output above.${NC}"
    exit 1
fi

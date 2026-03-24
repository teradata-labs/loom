#!/bin/bash
set -e

echo "=== Complex Multi-Step Plan Test ==="
echo
echo "This test creates a plan with 3+ steps to verify permission modes"
echo "work correctly for complex workflows."
echo

# Multi-step query that requires several tool calls
QUERY="Create a directory called test_workflow, create three text files in it (file1.txt with content 'hello', file2.txt with content 'world', file3.txt with content 'test'), then list all files in the directory to verify they were created"

echo "Query:"
echo "  $QUERY"
echo

# Step 1: Create plan
echo "Step 1: Creating execution plan with PLAN mode..."
RESPONSE=$(curl -s -X POST http://localhost:5006/v1/weave \
  -H "Content-Type: application/json" \
  -d "{\"query\":\"$QUERY\",\"permission_mode\":\"PERMISSION_MODE_PLAN\"}")

echo "$RESPONSE" | jq '.'
echo

# Extract session ID
SESSION_ID=$(echo "$RESPONSE" | jq -r '.sessionId')
if [ -z "$SESSION_ID" ] || [ "$SESSION_ID" = "null" ]; then
    echo "❌ Failed to get session ID"
    exit 1
fi

echo "Session ID: $SESSION_ID"
echo

# Step 2: Get the plan
echo "Step 2: Fetching execution plan..."
PLANS=$(curl -s http://localhost:5006/v1/sessions/$SESSION_ID/plans)
echo "$PLANS" | jq '.'
echo

PLAN_ID=$(echo "$PLANS" | jq -r '.plans[0].planId')
TOOL_COUNT=$(echo "$PLANS" | jq '.plans[0].tools | length')

if [ -z "$PLAN_ID" ] || [ "$PLAN_ID" = "null" ]; then
    echo "❌ Failed to get plan ID"
    exit 1
fi

echo "Plan ID: $PLAN_ID"
echo "Number of steps: $TOOL_COUNT"
echo

# Verify we have 3+ steps
if [ "$TOOL_COUNT" -lt 3 ]; then
    echo "⚠️  Warning: Expected 3+ steps, got $TOOL_COUNT"
    echo "    Plan may be simpler than expected, but continuing..."
    echo
fi

# Step 3: Show detailed plan
echo "Step 3: Plan details..."
echo "=== Execution Plan ==="
echo "$PLANS" | jq -r '.plans[0].tools[] | "Step \(.step): \(.toolName) - \(.rationale)\n  Params: \(.paramsJson)"'
echo

# Step 4: Ask for approval
read -p "Approve this plan? (y/n): " -n 1 -r
echo
echo

if [[ $REPLY =~ ^[Yy]$ ]]; then
    # Approve
    echo "Step 4: Approving plan..."
    curl -s -X POST http://localhost:5006/v1/plans/$PLAN_ID/approve \
      -H "Content-Type: application/json" \
      -d '{"approved":true}' | jq '.'
    echo

    # Execute
    echo "Step 5: Executing plan..."
    EXEC_RESULT=$(curl -s -X POST http://localhost:5006/v1/plans/$PLAN_ID/execute)
    echo "$EXEC_RESULT" | jq '.'
    echo

    # Verify execution
    echo "Step 6: Verifying execution..."
    FINAL_PLAN=$(curl -s http://localhost:5006/v1/plans/$PLAN_ID)

    # Check if all steps completed
    COMPLETED=$(echo "$FINAL_PLAN" | jq '[.tools[] | select(.status == "STEP_STATUS_COMPLETED")] | length')
    TOTAL=$(echo "$FINAL_PLAN" | jq '.tools | length')

    echo "Steps completed: $COMPLETED / $TOTAL"

    if [ "$COMPLETED" -eq "$TOTAL" ]; then
        echo "✅ All steps completed successfully!"

        # Verify the directory and files were created
        echo
        echo "Step 7: Verifying file system changes..."
        if [ -d "test_workflow" ]; then
            echo "✅ Directory 'test_workflow' exists"
            ls -la test_workflow/

            # Check file contents
            echo
            for file in file1.txt file2.txt file3.txt; do
                if [ -f "test_workflow/$file" ]; then
                    content=$(cat "test_workflow/$file")
                    echo "✅ $file exists with content: '$content'"
                else
                    echo "❌ $file not found"
                fi
            done
        else
            echo "❌ Directory 'test_workflow' not found"
        fi

        echo
        echo "=== Test Complete - SUCCESS ==="
    else
        echo "⚠️  Some steps did not complete"
        echo "$FINAL_PLAN" | jq '.tools[] | {step: .step, tool: .toolName, status: .status}'
    fi

else
    echo "Step 4: Rejecting plan..."
    curl -s -X POST http://localhost:5006/v1/plans/$PLAN_ID/approve \
      -H "Content-Type: application/json" \
      -d '{"approved":false,"feedback":"User rejected the plan"}' | jq '.'
    echo

    echo "❌ Plan rejected - no tools executed"

    # Verify nothing was created
    if [ -d "test_workflow" ]; then
        echo "⚠️  Warning: test_workflow directory exists (should not)"
    else
        echo "✅ Verified: test_workflow directory was not created"
    fi
fi

echo
echo "=== Test Complete ==="

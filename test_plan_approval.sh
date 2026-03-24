#!/bin/bash
set -e

echo "=== Interactive Plan Approval Test ==="
echo

# Step 1: Create a plan
echo "Step 1: Creating execution plan..."
RESPONSE=$(curl -s -X POST http://localhost:5006/v1/weave -H "Content-Type: application/json" -d '{"query":"list files in /tmp directory","permission_mode":"PERMISSION_MODE_PLAN"}')

echo "Response:"
echo "$RESPONSE" | jq '.'
echo

# Step 2: Extract session and get plans
SESSION_ID=$(echo "$RESPONSE" | jq -r '.sessionId')
if [ -z "$SESSION_ID" ] || [ "$SESSION_ID" = "null" ]; then
    echo "❌ Failed to get session ID"
    exit 1
fi

echo "Session ID: $SESSION_ID"
echo

echo "Step 2: Fetching plans for session..."
PLANS=$(curl -s http://localhost:5006/v1/sessions/$SESSION_ID/plans)
echo "$PLANS" | jq '.'
echo

# Step 3: Get plan ID and details
PLAN_ID=$(echo "$PLANS" | jq -r '.plans[0].planId')
if [ -z "$PLAN_ID" ] || [ "$PLAN_ID" = "null" ]; then
    echo "❌ Failed to get plan ID"
    exit 1
fi

echo "Plan ID: $PLAN_ID"
echo

echo "Step 3: Getting full plan details..."
PLAN=$(curl -s http://localhost:5006/v1/plans/$PLAN_ID)
echo "$PLAN" | jq '.'
echo

# Show what will be executed
echo "=== Plan Summary ==="
echo "Query: $(echo "$PLAN" | jq -r '.query')"
echo "Status: $(echo "$PLAN" | jq -r '.status')"
echo "Tools to execute:"
echo "$PLAN" | jq -r '.tools[] | "  - \(.toolName): \(.paramsJson)"'
echo

# Step 4: Ask user for approval
read -p "Do you want to approve this plan? (y/n): " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    echo
    echo "Step 4: Approving plan..."
    APPROVAL=$(curl -s -X POST http://localhost:5006/v1/plans/$PLAN_ID/approve -H "Content-Type: application/json" -d '{"approved":true}')
    echo "$APPROVAL" | jq '.'
    echo

    echo "Step 5: Executing approved plan..."
    EXECUTION=$(curl -s -X POST http://localhost:5006/v1/plans/$PLAN_ID/execute)
    echo "$EXECUTION" | jq '.'
    echo

    echo "Step 6: Checking final plan status..."
    FINAL=$(curl -s http://localhost:5006/v1/plans/$PLAN_ID)
    echo "$FINAL" | jq '.'
    echo

    echo "✅ Plan executed successfully!"
else
    echo
    echo "Step 4: Rejecting plan..."
    REJECTION=$(curl -s -X POST http://localhost:5006/v1/plans/$PLAN_ID/approve -H "Content-Type: application/json" -d '{"approved":false,"feedback":"User rejected the plan"}')
    echo "$REJECTION" | jq '.'
    echo

    echo "❌ Plan rejected - no tools were executed"
fi

echo
echo "=== Test Complete ==="

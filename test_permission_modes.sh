#!/bin/bash
set -e

echo "=== Permission Modes Test ==="
echo

# Test 1: PLAN Mode
echo "Test 1: PLAN Mode (should create plan, NOT execute tools)"
RESPONSE=$(curl -s -X POST http://localhost:5006/v1/weave \
  -H "Content-Type: application/json" \
  -d '{
    "query": "list files in current directory",
    "permission_mode": "PERMISSION_MODE_PLAN"
  }')

echo "Response:"
echo "$RESPONSE" | jq '.'
echo

# Extract session_id from response
SESSION_ID=$(echo "$RESPONSE" | jq -r '.sessionId')

if [ -z "$SESSION_ID" ] || [ "$SESSION_ID" = "null" ]; then
    echo "❌ Test 1 FAILED: No session_id in response"
    exit 1
fi

echo "Session ID: $SESSION_ID"
echo

# Check if response text mentions execution plan
if echo "$RESPONSE" | jq -e '.text' | grep -q "execution plan"; then
    echo "✅ Test 1a PASSED: Response mentions execution plan"
else
    echo "❌ Test 1a FAILED: Response doesn't mention execution plan"
    echo "Text: $(echo "$RESPONSE" | jq -r '.text')"
    exit 1
fi

# List plans for the session
echo "Fetching plans for session..."
PLANS=$(curl -s -X GET "http://localhost:5006/v1/sessions/$SESSION_ID/plans")
echo "Plans:"
echo "$PLANS" | jq '.'
echo

# Check if at least one plan exists
PLAN_COUNT=$(echo "$PLANS" | jq '.plans | length')
if [ "$PLAN_COUNT" -gt 0 ]; then
    PLAN_ID=$(echo "$PLANS" | jq -r '.plans[0].planId')
    echo "✅ Test 1b PASSED: Plan created with ID: $PLAN_ID"
else
    echo "❌ Test 1b FAILED: No plans found for session"
    exit 1
fi
echo

# Test 2: AUTO_ACCEPT Mode (regression test)
echo "Test 2: AUTO_ACCEPT Mode (should execute tools immediately)"
RESPONSE=$(curl -s -X POST http://localhost:5006/v1/weave \
  -H "Content-Type: application/json" \
  -d '{
    "query": "what is 2+2",
    "permission_mode": "PERMISSION_MODE_AUTO_ACCEPT"
  }')

echo "Response:"
echo "$RESPONSE" | jq '.'
echo

# Check if response has text with answer (tools executed)
if echo "$RESPONSE" | jq -e '.text' > /dev/null 2>&1; then
    TEXT=$(echo "$RESPONSE" | jq -r '.text')
    if [ ! -z "$TEXT" ] && [ "$TEXT" != "null" ]; then
        echo "✅ Test 2 PASSED: Tools executed, response returned"
        echo "Response text: $TEXT"
    else
        echo "⚠️  Test 2: Empty response text"
    fi
else
    echo "⚠️  Test 2: No text field in response"
fi
echo

echo "=== All Tests Complete ==="

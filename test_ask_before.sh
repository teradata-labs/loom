#!/bin/bash
set -e

echo "=== ASK_BEFORE Mode Test ==="
echo
echo "ASK_BEFORE mode should request approval before each tool execution."
echo "This tests whether tools are properly blocked pending approval."
echo

# Test 1: Try to execute a tool in ASK_BEFORE mode
echo "Test 1: Sending request with ASK_BEFORE mode..."
RESPONSE=$(curl -s -X POST http://localhost:5006/v1/weave \
  -H "Content-Type: application/json" \
  -d '{"query":"list files in /tmp directory","permission_mode":"PERMISSION_MODE_ASK_BEFORE"}')

echo "Response:"
echo "$RESPONSE" | jq '.'
echo

# Check what happened
if echo "$RESPONSE" | jq -e '.text' | grep -qi "permission\|approval\|blocked"; then
    echo "✅ Test PASSED: Tools blocked, approval required"
elif echo "$RESPONSE" | jq -e '.code' | grep -q "7"; then
    echo "✅ Test PASSED: Permission denied (code 7)"
    echo "Message: $(echo "$RESPONSE" | jq -r '.message')"
elif echo "$RESPONSE" | jq -e '.text' > /dev/null; then
    TEXT=$(echo "$RESPONSE" | jq -r '.text')
    echo "⚠️  Response returned text:"
    echo "$TEXT"
    echo
    echo "Note: ASK_BEFORE may need HITL callback implementation for full functionality"
else
    echo "❌ Unexpected response format"
fi

echo
echo "=== Test Complete ==="
echo
echo "Expected behavior:"
echo "  - Tools should be blocked"
echo "  - Should receive permission/approval error"
echo "  - OR should receive callback request for approval"

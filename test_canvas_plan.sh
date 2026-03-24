#!/bin/bash

echo "Testing file-helper-plan agent..."
curl -s -X POST http://localhost:5006/v1/weave \
  -H "Content-Type: application/json" \
  -d '{"query":"what folders do I have access to","permission_mode":"PERMISSION_MODE_PLAN","agent_id":"file-helper-plan"}' | jq '.'

#!/usr/bin/env bash
# Poll for release asset availability
# Usage: poll-release-asset.sh <tag_name> <asset_name> [max_attempts]

set -euo pipefail

if [ $# -lt 2 ]; then
  echo "Usage: $0 <tag_name> <asset_name> [max_attempts]"
  exit 1
fi

TAG_NAME="$1"
ASSET_NAME="$2"
MAX_ATTEMPTS="${3:-60}"  # 60 attempts * 2 seconds = 120 second timeout

echo "Polling for asset: $ASSET_NAME (max attempts: $MAX_ATTEMPTS)"

attempt=0
while [ $attempt -lt $MAX_ATTEMPTS ]; do
  if gh release view "$TAG_NAME" --json assets \
     --jq ".assets[].name" | grep -q "^${ASSET_NAME}$"; then
    echo "✅ Asset available: $ASSET_NAME"
    exit 0
  fi
  attempt=$((attempt + 1))
  sleep 2
done

echo "::error::Timeout waiting for asset: $ASSET_NAME"
exit 1

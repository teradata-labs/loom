#!/bin/sh
# Runs looms (loopback) + loom-mcp (the authenticated public edge) in one machine.
# If either process exits, the script exits non-zero so Fly restarts the machine.
set -eu

term() { kill -TERM "${loom_pid:-}" "${mcp_pid:-}" 2>/dev/null || true; }
trap term TERM INT

looms serve --config /etc/loom/looms.yaml &
loom_pid=$!

loom-mcp --transport=http --http-addr=0.0.0.0:8765 --grpc-addr=127.0.0.1:60051 &
mcp_pid=$!

while kill -0 "$loom_pid" 2>/dev/null && kill -0 "$mcp_pid" 2>/dev/null; do
  sleep 5
done

echo "a process exited; shutting down for restart" >&2
term
exit 1

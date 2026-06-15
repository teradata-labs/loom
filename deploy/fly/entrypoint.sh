#!/bin/sh
# Runs looms (loopback) + loom-mcp (the authenticated public edge) in one machine.
# If either process exits, the script exits non-zero so Fly restarts the machine.
set -eu

term() { kill -TERM "${loom_pid:-}" "${mcp_pid:-}" 2>/dev/null || true; }
trap term TERM INT

# Config is selectable so one image serves both the hardened public edge and
# the capability-enabled lab. Defaults to the hardened config, so the existing
# app's behavior is unchanged; fly.lab.toml sets LOOM_CONFIG to the lab overlay.
looms serve --config "${LOOM_CONFIG:-/etc/loom/looms.yaml}" &
loom_pid=$!

# Gate the public edge on looms readiness. The Fly proxy routes traffic the
# moment 8765 binds -- including cold auto-starts triggered by an incoming
# request -- and loom-mcp binds instantly while looms needs ~15s (Supabase
# migrations + LLM preflight). Without this gate the machine's first requests
# fail with "dial 127.0.0.1:60051: connection refused".
waited=0
until nc -z 127.0.0.1 60051 2>/dev/null; do
  kill -0 "$loom_pid" 2>/dev/null || { echo "looms exited before listening on 60051" >&2; exit 1; }
  waited=$((waited + 1))
  [ "$waited" -ge 120 ] && { echo "timed out waiting for looms on 60051" >&2; exit 1; }
  sleep 1
done
echo "looms ready on 60051 after ${waited}s; starting loom-mcp edge" >&2

loom-mcp --transport=http --http-addr=0.0.0.0:8765 --grpc-addr=127.0.0.1:60051 &
mcp_pid=$!

while kill -0 "$loom_pid" 2>/dev/null && kill -0 "$mcp_pid" 2>/dev/null; do
  sleep 5
done

echo "a process exited; shutting down for restart" >&2
term
exit 1

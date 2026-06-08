# HTTP-MCP Endpoint Guide

> **Note:** Added after v1.3.0. The HTTP transport and endpoint authentication require a build that includes the `loom-mcp --transport=http` flag.

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Common Tasks](#common-tasks)
  - [Stream loom_weave progress over SSE](#task-stream-loom_weave-progress-over-sse)
  - [Authenticate the endpoint with Supabase JWTs](#task-authenticate-the-endpoint-with-supabase-jwts)
  - [Expose the endpoint to a remote client](#task-expose-the-endpoint-to-a-remote-client)
- [Examples](#examples)
- [Reverse-proxy alternative](#reverse-proxy-alternative)
- [Troubleshooting](#troubleshooting)
- [Next Steps](#next-steps)

## Overview

`loom-mcp` bridges MCP clients to a running `looms` server. It supports two transports:

- ✅ **stdio** (default) — for Claude Desktop and IDE clients.
- ✅ **http** — Streamable HTTP (MCP 2025-03-26) for remote MCP clients. The `loom_weave` tool streams progress as Server-Sent Events (SSE), and the endpoint can be authenticated with Supabase-issued JWTs.

## Prerequisites

This guide requires:
- A running `looms` server (default gRPC address `localhost:60051`).
- The `loom-mcp` binary.
- For authentication: a Supabase project JWT secret (HS256) or its JWKS URL (asymmetric keys).

## Quick Start

Start the HTTP-MCP endpoint (localhost-only by default):

```bash
loom-mcp --transport=http --http-addr=127.0.0.1:8765 --grpc-addr=localhost:60051
```

Expected log:

```
{"level":"info","msg":"HTTP-MCP server ready","addr":"127.0.0.1:8765","endpoint":"POST http://127.0.0.1:8765/ (Streamable HTTP, MCP 2025-03-26)"}
```

Initialize a session:

```bash
curl -s -D - \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}' \
  http://127.0.0.1:8765/
```

Expected response (note the `Mcp-Session-Id` header):

```
HTTP/1.1 200 OK
Content-Type: application/json
Mcp-Session-Id: 46cd867d-42e4-4884-bb97-6be8c0df8259

{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{},"resources":{"listChanged":true}},"serverInfo":{"name":"loom-mcp","version":"1.3.0"}}}
```

A `GET` to the endpoint returns `405` (the standalone server-push stream is not offered):

```bash
curl -s -D - -o /dev/null http://127.0.0.1:8765/
# HTTP/1.1 405 Method Not Allowed
# Allow: POST, DELETE
```

## Common Tasks

### Task: Stream loom_weave progress over SSE

Send a `tools/call` for `loom_weave` with `Accept: text/event-stream` and a progress token. The server streams `notifications/progress` events, then the final result:

```bash
curl -s \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d '{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"loom_weave","arguments":{"query":"Summarize today'\''s errors"},"_meta":{"progressToken":"t1"}}}' \
  http://127.0.0.1:8765/
```

Expected SSE stream (progress events then the final tools/call result):

```
id: 1
event: message
data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"t1","progress":10,"total":100}}

id: 4
event: message
data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"t1","progress":100,"total":100}}

id: 5
event: message
data: {"jsonrpc":"2.0","id":9,"result":{"content":[{"type":"text","text":"..."}]}}
```

A `tools/call` sent **without** `Accept: text/event-stream` returns a single `application/json` response instead (progress is opt-in).

### Task: Authenticate the endpoint with Supabase JWTs

The HTTP endpoint has no authentication by default. Set the same `LOOM_SERVER_AUTH_*` variables `looms` uses to require a Supabase JWT on every request:

```bash
export LOOM_SERVER_AUTH_ENABLED=true
export LOOM_SERVER_AUTH_MODE=required          # or "optional"
export LOOM_SERVER_AUTH_SUPABASE_PROJECT_REF=abcdefghijklmnop
export LOOM_SERVER_AUTH_SUPABASE_JWT_SECRET=...   # HS256 secret (or set a JWKS URL)
export LOOM_SERVER_AUTH_SUPABASE_AUDIENCE=authenticated

loom-mcp --transport=http --http-addr=127.0.0.1:8765
```

Behavior:
- A request with **no** bearer (in `required` mode) returns `401 Unauthorized`.
- A request with an **invalid** bearer returns `401` (always, even in `optional` mode).
- A request with a **valid** Supabase JWT is accepted; the token subject (`sub`) is forwarded to `looms` as the user identity (on streaming `loom_weave` calls), so Row-Level Security scopes data to that user.

The JWKS URL and issuer are derived from the project ref when not set. For asymmetric-key projects, set `LOOM_SERVER_AUTH_SUPABASE_JWKS_URL` instead of the HS256 secret.

### Task: Expose the endpoint to a remote client

The HTTP transport binds to `127.0.0.1` by default and warns if bound to a non-loopback interface (it has no built-in TLS). To reach it from a cloud MCP client, front it with an HTTPS tunnel and **enable authentication** (above):

```bash
# Terminal 1: auth-enabled endpoint on loopback
loom-mcp --transport=http --http-addr=127.0.0.1:8765

# Terminal 2: HTTPS tunnel
ngrok http 8765
```

Give the remote client the tunnel URL and a bearer token (a Supabase JWT) in its auth header.

## Examples

### Example 1: Local smoke test (no auth)

```bash
loom-mcp --transport=http --http-addr=127.0.0.1:8765 &
curl -s -o /dev/null -w "GET -> %{http_code}\n" http://127.0.0.1:8765/
# GET -> 405
```

### Example 2: Authenticated endpoint rejects unauthenticated calls

With `LOOM_SERVER_AUTH_ENABLED=true` and `LOOM_SERVER_AUTH_MODE=required`:

```bash
curl -s -o /dev/null -w "%{http_code}\n" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
  http://127.0.0.1:8765/
# 401

curl -s -o /dev/null -w "%{http_code}\n" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -H "Authorization: Bearer $VALID_SUPABASE_JWT" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
  http://127.0.0.1:8765/
# 200
```

## Reverse-proxy alternative

Native endpoint auth (above) is the recommended approach. If you prefer to keep `loom-mcp` unauthenticated and enforce auth at the edge, front it with an authenticating reverse proxy (for example `oauth2-proxy` or nginx with a JWT-validating module) that validates the bearer and forwards the request to `127.0.0.1:8765`. In that topology, leave `LOOM_SERVER_AUTH_ENABLED` unset on `loom-mcp` and rely on the proxy for both TLS and authentication.

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `401` on every request | Auth enabled, missing/invalid bearer | Send a valid Supabase JWT in `Authorization: Bearer` |
| Endpoint logs "binding to non-localhost ... INSECURE" | Bound to `0.0.0.0`/public IP without a proxy | Bind `127.0.0.1` and use a tunnel/proxy, and enable auth |
| `loom_weave` returns an `isError` result mentioning the LLM provider | `looms` has no working LLM credentials | Configure an LLM provider for `looms` |
| Tool call blocks instead of streaming | Client did not send `Accept: text/event-stream` | Add the header to opt into SSE |

## Next Steps

- [Supabase Integration Guide](supabase-integration.md) — run Loom storage on the same Supabase database.
- [Task Board Guide](task-board.md) — the kanban tasks surfaced by workflows.

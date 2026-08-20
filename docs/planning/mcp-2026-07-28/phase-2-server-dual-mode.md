# Phase 2 Brief: Server Dual-Mode Core

Parent spec: §5.2, §7.1, §7.5
Depends on: Phase 0. Decision D1 ratified 2026-08-17 (option a) — unblocked.
Size: Medium

## Objective

`pkg/mcp/server` + the streamable HTTP transport serve both revision families per request: stateless requests (identified by `_meta`) bypass sessions and get stamped results; legacy requests are untouched. `server/discover` answers. Idempotency keys make spec-mandated re-issues safe for side-effectful tools.

## Decision D1 — idempotency dedupe placement **[ratified 2026-08-17: option (a) below]**

The stateless revision means any request lands on any bridge replica, so a per-replica map cannot dedupe alone. Sessions and runs live in looms (bridge handlers are gRPC passthroughs), so the state owner is looms:

- The bridge extracts `_meta["com.teradata.loom/idempotencyKey"]` and forwards it as outgoing gRPC metadata `x-loom-idempotency-key` on side-effectful RPCs (`Weave`, `Build`, session mutations). Precedent: migration `000008_task_idempotency` already implements key-based dedupe for the task board — reuse its storage pattern.
- looms dedupes per (authenticated principal, key): duplicate while original in flight **blocks until the original completes and returns its final result** (not a replayed stream — see stream-join semantics below); duplicate after completion within TTL returns the stored result; TTL 10 minutes.
- **Table shape (per ratified D3: defer the run store but make this table growable into it):** `run_dedupe(id, principal, idempotency_key, status, result_json, created_at, expires_at)` with a unique index on `(principal, idempotency_key)`. The `id`/`status`/`result_json` columns are exactly what the deferred Tasks extension and §7.4 durable-reference tier will need — when those land, this table gains columns, not a replacement.
- The bridge additionally keeps a small per-replica in-memory map for bridge-local effects (none today; guard rail).
- **Stream-join semantics [proposed]:** a joining duplicate receives only the terminal result, not a replay of `notifications/progress`. Rationale: replay requires buffering the stream server-side, which is the resumption machinery the revision just deleted. Clients that lost a stream mid-weave lose progress events but not the result or correctness.
- Third-party clients that send no key get at-least-once semantics, as the spec prescribes; the bridge's side-effectful tool descriptions document the key.

## Pinned decisions

- **[proposed] One body parse in `handlePost`.** Extend the existing `isInitializeRequest`-style peek into a single `peekRequest(body) (method string, statelessVersion string, ok bool)` that extracts `method` and `_meta[protocolVersion]`. Admission logic branches on it; no second parse.
- **[proposed] `Mcp-Method` mismatch check lives in the transport** (`handlePost`), where the header exists: mismatch → JSON-RPC error `-32020` in an HTTP 200 response (it is a protocol error, not a transport error). Missing `Mcp-Method` on a stateless request is tolerated with a Debug log during the window.
- **[proposed] Result stamping is central in `HandleMessage`.** After the handler returns, if the request context carries stateless `_meta`: marshal the result, inject `resultType` (default `"complete"`) and `_meta[io.modelcontextprotocol/serverInfo]`. JSON-RPC **error** responses are not stamped (`resultType` is a result field). `HandleMessageStream` (`server_stream.go`) gets the same wrap on its terminal result.
- **[proposed] `server/discover` advertises only implemented revisions: `["2026-07-28", "2024-11-05"]`.** The server has never implemented `2025-11-25` semantics; advertising it would be a false claim. Add revisions to the list only when conformance cells for them exist. (Parent spec §7.1 lists three; this brief narrows it — update the spec when ratified.)
- **Context keys are typed.** One unexported `metaContext` struct carrying protocol version, client info, client caps, logLevel, otel keys; one getter `protocol.MetaFromContext(ctx)`. No handler parses `_meta` (parent spec §7.1).

## Work items

1. `peekRequest` in the transport; dual-mode admission in `handlePost` exactly per parent §5.2 rules 1–3 (stateless: no session lookup, no mint, no session response header).
2. `_meta` extraction middleware at the top of `HandleMessage` (`server.go:130`) and `HandleMessageStream` (`server_stream.go:34`); result stamping at the bottom of both.
3. `server/discover` `MethodHandler` returning `DiscoverResult` (`protocol/version.go:114`) with real `ServerCapabilities` and `ServerInfo` from the existing initialize path.
4. `handlePing` answers `MethodNotFound` when the request context is stateless.
5. Idempotency per D1: bridge-side `_meta` key extraction + gRPC metadata forwarding; looms-side dedupe storage (new migration if the task-idempotency table shape doesn't generalize); TTL sweep.
6. The client stamps `com.teradata.loom/idempotencyKey` (fresh UUIDv4 per logical call, reused on re-issue after stream loss) in `sendRequest` alongside the existing `StampMeta` call — client work, but it ships in this phase because the server side is what makes it testable end-to-end.

## Wire reference

Stateless request (client → server):

```json
{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{
  "name":"loom_weave","arguments":{"prompt":"...","session_id":"..."},
  "_meta":{
    "io.modelcontextprotocol/protocolVersion":"2026-07-28",
    "io.modelcontextprotocol/clientInfo":{"name":"loom","version":"1.4.0"},
    "io.modelcontextprotocol/clientCapabilities":{},
    "com.teradata.loom/idempotencyKey":"9be0…"}}}
```

Stamped result (server → client):

```json
{"jsonrpc":"2.0","id":7,"result":{"content":[…],"resultType":"complete",
  "_meta":{"io.modelcontextprotocol/serverInfo":{"name":"loom-mcp","version":"1.4.0"}}}}
```

## Test scenarios

| # | Given | When | Then |
|---|---|---|---|
| 1 | Stateless request, no session header | POST | Admitted; no session minted; no `Mcp-Session-Id` response header; result stamped |
| 2 | Stateless request bearing a stale legacy `Mcp-Session-Id` | POST | Admitted as stateless; session ignored, not 404 |
| 3 | Legacy `initialize` | POST | Session minted; response header set (unchanged path) |
| 4 | Legacy request, unknown session header | POST | 404 (unchanged) |
| 5 | Legacy request, no header, non-initialize | POST | Admitted (pins today's behavior; ownership enforced per Immediate brief) |
| 6 | `Mcp-Method: tools/list` on a body whose method is `tools/call` | POST | JSON-RPC error `-32020`, HTTP 200 |
| 7 | Stateless request without `Mcp-Method` | POST | Admitted; Debug log |
| 8 | `server/discover` | Any mode | `["2026-07-28","2024-11-05"]`, real capabilities/serverInfo |
| 9 | `ping` with stateless `_meta` / legacy ping | POST ×2 | `MethodNotFound` / pong |
| 10 | Handler returns JSON-RPC error on stateless request | POST | Error response NOT stamped with `resultType` |
| 11 | Streaming `loom_weave`, stateless | POST (SSE accept) | Progress notifications flow; terminal result stamped |
| 12 | Same idempotency key re-issued while original weave in flight | 2 POSTs | Second blocks, returns the same final result; exactly one agent run (assert via run count) |
| 13 | Same key after completion, within TTL | POST | Stored result; no new run |
| 14 | Same key after TTL expiry | POST | Fresh run |
| 15 | Same key, different principals | 2 POSTs | Two independent runs (key scoped per principal) |
| 16 | No key on a side-effectful call | 2 identical POSTs | Two runs (at-least-once documented) |
| 17 (race) | Janitor expiring sessions while stateless requests flow | `-race`, sustained | Clean |
| 18 (race) | N concurrent duplicates of one key | `-race` | Exactly one run; all callers get its result |
| 19 (race) | Concurrent legacy (sessionful) + stateless traffic on one server | `-race` | Clean; modes don't interfere |
| 20 | Malformed `_meta` (non-object) | POST | Treated as legacy; no panic |

## Acceptance criteria

Standing criteria, plus: scenario 12's "exactly one run" is asserted against looms state, not bridge logs; golden files added for the two wire shapes above under `pkg/mcp/conformance/testdata/`.

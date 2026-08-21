# MCP Probe: real-server testing for Loom's MCP client

**Status**: ✅ Implemented (`cmd/loom-mcp-probe`, wire-level tests included)

`loom-mcp-probe` connects Loom's MCP client to a **real** MCP server — streamable HTTP or stdio — and reports what actually negotiated: protocol revision, era, server identity, tools, an optional tool call, an optional MRTR elicitation exchange, and an optional `subscriptions/listen` watch. The probe binary runs the shipped client and transports unmodified — the exact negotiation, fallback, MRTR, and transport code paths the manager uses in production — against real servers. (Its automated tests are the exception: they drive the same `run()` against scripted wire-level HTTP fixtures.)

Use it to verify a server before wiring it into an agent config, to reproduce interop problems, or to watch what era/revision a fleet server actually speaks.

## Running

```bash
just mcp-probe -url http://localhost:8971 -call test_simple_text -watch 5
# or without just:
go run -tags fts5 ./cmd/loom-mcp-probe -cmd npx -arg -y -arg @modelcontextprotocol/server-everything -arg stdio \
    -call echo -args '{"message":"hello"}'
```

## Flags

| Flag | Meaning |
|---|---|
| `-url` | streamable HTTP endpoint (mutually exclusive with `-cmd`) |
| `-cmd` | stdio server executable — the path is taken verbatim (spaces survive); pass arguments via `-arg` |
| `-arg` | one argument for the `-cmd` executable; repeatable, order-preserving |
| `-headers-env` | name of an env var holding a JSON object of HTTP headers (auth tokens ride the environment, never argv) |
| `-pin` | `protocol_version` pin: `auto` (default), `legacy`, or an exact revision such as `2026-07-28` |
| `-call` | tool to invoke |
| `-args` | JSON arguments object for `-call` (default `{}`) |
| `-answer` | JSON object accepted for **every** elicitation; enables the MRTR driver. Unset = the client's fail-fast default on `input_required` |
| `-watch` | seconds to hold a `subscriptions/listen` stream (stateless connections only) |
| `-timeout` | per-operation timeout in ms (default 15000): bounds connect (incl. the negotiation probe), `tools/list`, `tools/call` with its MRTR rounds, and the subscription acknowledgment. The `-watch` hold runs on its own clock |
| `-v` | debug logging |

The probe exits non-zero on any failure, so it can gate scripts. For `-watch` that guarantee is strict: a legacy connection, a missing acknowledgment, or a subscription that ends — even gracefully — before the requested window all fail the probe.

## What each mode exercises

- **Auto negotiation**: the `server/discover` probe with the full fallback ladder — non-modern JSON-RPC errors, bare HTTP 404/405/501, 400 without a modern error body, and (stdio only) probe silence within the bounded timeout.
- **`-answer`**: the Multi Round-Trip Requests loop (2026-07-28, SEP-2322). A server's `input_required` interim result is answered by accepting every elicitation with the `-answer` object — a canned human standing in for the manager's HITL adapter — and the original call is retried with `inputResponses` plus the exact echoed `requestState`. Non-elicitation input requests (sampling, roots) are refused so the exchange fails loudly rather than fabricating a model response.
- **`-watch`**: a live `subscriptions/listen` stream — the acknowledgment is required before the hold starts, change notifications are demultiplexed during it, and the clean end is client-side cancellation at the deadline (closing the SSE stream on HTTP, `notifications/cancelled` on stdio). Anything short of that sequence is a probe failure.

## Known-good real servers

All from official sources:

| Server | Era | How to run |
|---|---|---|
| Go SDK conformance everything-server | **2026-07-28** (stateless HTTP + stdio); sessionful legacy with `-stateless=false` | `go install github.com/modelcontextprotocol/go-sdk/conformance/everything-server@v1.7.0` then `everything-server -http localhost:8971` |
| `@modelcontextprotocol/server-everything` (npm, TypeScript) | legacy 2025-11-25 | `npx -y @modelcontextprotocol/server-everything stdio` |
| Loom's own `looms` + `loom-mcp --transport=http` | **dual-era**: 2026-07-28 stateless AND legacy 2024-11-05 handshake on one endpoint (server side landed in PR #328; transcript below) | `looms serve` then `loom-mcp --transport=http --http-addr=127.0.0.1:8765 --grpc-addr=localhost:60051` |

As of 2026-08-19 the Go SDK is the only official SDK with a 2026-07-28 runtime; the TypeScript SDK tops out at 2025-11-25, which makes its servers exactly the legacy peers the fallback ladder needs.

## Example sessions

Real transcripts against the official Go SDK conformance server and the npm TypeScript server (2026-08-19), shown in the probe's current output format (the acknowledgment is consumed by the watch gate rather than listed as a notification).

**Stateless 2026-07-28 over streamable HTTP, with a live subscription:**

```text
$ loom-mcp-probe -url http://localhost:8971 -call test_simple_text -watch 4
CONNECTED in 6ms
  negotiated : 2026-07-28
  era        : stateless (2026-07-28 core)
  serverInfo : mcp-conformance-test-server 1.0.0
  tools      : 28 (json_schema_2020_12_tool, test_audio_content, test_elicitation, …)
  call test_simple_text → "This is a simple text response for testing."
  watch      : subscription 4 acknowledged, holding for 4s
    notif #1 : notifications/resources/updated
```

**MRTR elicitation round-trip, including the server-verified `requestState` echo:**

```text
$ loom-mcp-probe -url http://localhost:8971 -call test_input_required_result_elicitation -answer '{"name":"Loom"}'
  elicited   : "What is your name?" → accepting with -answer
  call test_input_required_result_elicitation → "Hello, Loom!"

$ loom-mcp-probe -url http://localhost:8971 -call test_input_required_result_request_state -answer '{"name":"Loom"}'
  elicited   : "Please confirm" → accepting with -answer
  call test_input_required_result_request_state → "state-ok: requestState received and confirmation accepted"
```

**Legacy fallback against a real 2025-era server (TypeScript SDK):**

```text
$ loom-mcp-probe -cmd npx -arg -y -arg @modelcontextprotocol/server-everything -arg stdio \
    -call echo -args '{"message":"hello from loom client"}'
CONNECTED in 1.31s
  negotiated : 2025-11-25
  era        : legacy (initialize handshake)
  serverInfo : mcp-servers/everything 2.0.0
  tools      : 13 (echo, get-annotated-message, get-env, …)
  call echo → "Echo: hello from loom client"
```

The sessionful mode of the Go SDK server (`-stateless=false`) also negotiates down to `2025-11-25` via the handshake — a live check that the fallback requests the newest legacy revision instead of silently under-negotiating.

**Sessionful streamable HTTP against the TypeScript server** — the real-wire test of the 400-fallback rule and legacy session capture. The server rejects the pre-initialize `server/discover` probe with HTTP 400 and a non-modern JSON-RPC body (`-32000 "Bad Request: Server not initialized"`, id null); the client classifies that as a legacy signal, runs the handshake, adopts the minted `Mcp-Session-Id`, and echoes it on every later request — this server enforces sessions strictly, so the tool call succeeding proves the echo:

```text
$ npx -y @modelcontextprotocol/server-everything streamableHttp   # listens on :3001
$ loom-mcp-probe -url http://localhost:3001/mcp -call echo -args '{"message":"session test"}'
CONNECTED in 6ms
  negotiated : 2025-11-25
  era        : legacy (initialize handshake)
  serverInfo : mcp-servers/everything 2.0.0
  tools      : 13 (echo, get-annotated-message, get-env, …)
  call echo → "Echo: session test"
```

**Dogfood: Loom's own dual-era endpoint** (`loom-mcp --transport=http` bridging a running `looms`; server side landed in PR #328). One endpoint serves both eras:

```text
$ loom-mcp-probe -url http://127.0.0.1:8765/ -call loom_list_agents -watch 3
CONNECTED in 3ms
  negotiated : 2026-07-28
  era        : stateless (2026-07-28 core)
  serverInfo : loom-mcp 1.4.0
  tools      : 55 (loom_activate_skill, loom_answer_clarification, loom_build, …)
  call loom_list_agents → "{\"agents\":[{\"id\":\"…\",\"name\":\"guide\",\"status\":\"running\",…"
  watch      : subscription 4 acknowledged, holding for 3s

$ loom-mcp-probe -url http://127.0.0.1:8765/ -pin legacy
CONNECTED in 3ms
  negotiated : 2024-11-05
  era        : legacy (initialize handshake)
  serverInfo : loom-mcp 1.4.0
```

## Limitations

- The MRTR driver answers **elicitations only**, with one canned object for all of them; sampling and roots input requests fail the exchange by design.
- `-watch` requires a stateless (2026-07-28) connection; `subscriptions/listen` does not exist on legacy revisions.
- This is a probe, not a conformance suite: it reports what happened on one connection. The dual-revision conformance matrix and official-SDK interop suite live in `pkg/mcp/conformance` (migration spec §10, landed in PR #328).
- On stdio, a child process that stops draining stdin while a watch is being cancelled can wedge the final teardown write (a pre-existing transport limitation — `Send` checks the context only before writing). Unattended stdio gates should wrap the probe in an outer `timeout(1)` as a belt; the HTTP path has no such edge.
- Flags that cannot take effect under the chosen mode are rejected up front (`-args`/`-answer` without `-call`, `-arg` with `-url`, `-headers-env` with `-cmd`, negative `-watch`): a silently ignored flag would be a gate that verified nothing.
- Authenticated HTTP endpoints need `-headers-env`: the manager passes configured headers from agent config, and the probe reads the same shape from the named environment variable.

## Field notes from real servers

- The Go SDK conformance server's `test_elicitation` tool still uses the legacy server-initiated `Session.Elicit` API, which the SDK itself rejects on 2026-07-28 sessions ("return an InputRequests map instead"); use the `test_input_required_result_*` tools for MRTR.
- The same server exits with status 1 after a cancelled `subscriptions/listen` over stdio (a bare stdin EOF exits 0). Loom's teardown is unaffected; the probe logs the exit as a warning.
- The Go SDK server is dual-era even with `-stateless=true`: it still answers the `initialize` handshake, so a `legacy` pin against it connects at `2025-11-25` rather than failing. In sessionful mode it answers `server/discover` with `-32022` listing its legacy versions — a recognized modern error body, which correctly never triggers fallback under a stateless pin.
- The TypeScript SDK server on streamable HTTP rejects every pre-initialize request with HTTP 400 and JSON-RPC `-32000` (id null) — the reason the fallback rule inspects 400 bodies instead of treating 400 as fatal.

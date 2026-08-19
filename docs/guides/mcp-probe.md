# MCP Probe: real-server testing for Loom's MCP client

**Status**: ✅ Implemented (`cmd/loom-mcp-probe`, wire-level tests included)

`loom-mcp-probe` connects Loom's MCP client to a **real** MCP server — streamable HTTP or stdio — and reports what actually negotiated: protocol revision, era, server identity, tools, an optional tool call, an optional MRTR elicitation exchange, and an optional `subscriptions/listen` watch. It exercises the exact negotiation, fallback, MRTR, and transport code paths the manager uses in production. No fakes anywhere.

Use it to verify a server before wiring it into an agent config, to reproduce interop problems, or to watch what era/revision a fleet server actually speaks.

## Running

```bash
just mcp-probe -url http://localhost:8971 -call test_simple_text -watch 5
# or without just:
go run -tags fts5 ./cmd/loom-mcp-probe -cmd "npx -y @modelcontextprotocol/server-everything stdio" \
    -call echo -args '{"message":"hello"}'
```

## Flags

| Flag | Meaning |
|---|---|
| `-url` | streamable HTTP endpoint (mutually exclusive with `-cmd`) |
| `-cmd` | stdio server command line, space-separated |
| `-pin` | `protocol_version` pin: `auto` (default), `legacy`, or an exact revision such as `2026-07-28` |
| `-call` | tool to invoke |
| `-args` | JSON arguments object for `-call` (default `{}`) |
| `-answer` | JSON object accepted for **every** elicitation; enables the MRTR driver. Unset = the client's fail-fast default on `input_required` |
| `-watch` | seconds to hold a `subscriptions/listen` stream (stateless connections only) |
| `-timeout` | negotiation-probe/request timeout in ms (default 15000) |
| `-v` | debug logging |

The probe exits non-zero on any failure, so it can gate scripts.

## What each mode exercises

- **Auto negotiation**: the `server/discover` probe with the full fallback ladder — non-modern JSON-RPC errors, bare HTTP 404/405/501, 400 without a modern error body, and (stdio only) probe silence within the bounded timeout.
- **`-answer`**: the Multi Round-Trip Requests loop (2026-07-28, SEP-2322). A server's `input_required` interim result is answered by accepting every elicitation with the `-answer` object — a canned human standing in for the manager's HITL adapter — and the original call is retried with `inputResponses` plus the exact echoed `requestState`. Non-elicitation input requests (sampling, roots) are refused so the exchange fails loudly rather than fabricating a model response.
- **`-watch`**: a live `subscriptions/listen` stream (acknowledgment, demultiplexed change notifications, client-side cancellation — closing the SSE stream on HTTP, `notifications/cancelled` on stdio).

## Known-good real servers

All from official sources:

| Server | Era | How to run |
|---|---|---|
| Go SDK conformance everything-server | **2026-07-28** (stateless HTTP + stdio); sessionful legacy with `-stateless=false` | `go install github.com/modelcontextprotocol/go-sdk/conformance/everything-server@v1.7.0` then `everything-server -http localhost:8971` |
| `@modelcontextprotocol/server-everything` (npm, TypeScript) | legacy 2025-11-25 | `npx -y @modelcontextprotocol/server-everything stdio` |
| Loom's own `looms` / `loom-mcp` | per branch | `looms serve` |

As of 2026-08-19 the Go SDK is the only official SDK with a 2026-07-28 runtime; the TypeScript SDK tops out at 2025-11-25, which makes its servers exactly the legacy peers the fallback ladder needs.

## Example sessions

Real transcripts against the official Go SDK conformance server and the npm TypeScript server (2026-08-19).

**Stateless 2026-07-28 over streamable HTTP, with a live subscription:**

```text
$ loom-mcp-probe -url http://localhost:8971 -call test_simple_text -watch 4
CONNECTED in 6ms
  negotiated : 2026-07-28
  era        : stateless (2026-07-28 core)
  serverInfo : mcp-conformance-test-server 1.0.0
  tools      : 28 (json_schema_2020_12_tool, test_audio_content, test_elicitation, …)
  call test_simple_text → "This is a simple text response for testing."
  watch      : subscription 4 open for 4s
    notif #1 : notifications/subscriptions/acknowledged
    notif #2 : notifications/resources/updated
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
$ loom-mcp-probe -cmd "npx -y @modelcontextprotocol/server-everything stdio" -call echo -args '{"message":"hello from loom client"}'
CONNECTED in 1.31s
  negotiated : 2025-11-25
  era        : legacy (initialize handshake)
  serverInfo : mcp-servers/everything 2.0.0
  tools      : 13 (echo, get-annotated-message, get-env, …)
  call echo → "Echo: hello from loom client"
```

The sessionful mode of the Go SDK server (`-stateless=false`) also negotiates down to `2025-11-25` via the handshake — a live check that the fallback requests the newest legacy revision instead of silently under-negotiating.

## Limitations

- The MRTR driver answers **elicitations only**, with one canned object for all of them; sampling and roots input requests fail the exchange by design.
- `-watch` requires a stateless (2026-07-28) connection; `subscriptions/listen` does not exist on legacy revisions.
- This is a probe, not a conformance suite: it reports what happened on one connection. The dual-revision conformance matrix and official-SDK interop CI live in `pkg/mcp/conformance` (migration spec §10).

## Field notes from real servers

- The Go SDK conformance server's `test_elicitation` tool still uses the legacy server-initiated `Session.Elicit` API, which the SDK itself rejects on 2026-07-28 sessions ("return an InputRequests map instead"); use the `test_input_required_result_*` tools for MRTR.
- The same server exits with status 1 after a cancelled `subscriptions/listen` over stdio (a bare stdin EOF exits 0). Loom's teardown is unaffected; the probe logs the exit as a warning.

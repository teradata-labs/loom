# Phase 1 Brief: Client Exposure Reduction

Parent spec: §4.1, §4.2 (wiring only), §5.1, §9.2 (freeze markers)
Depends on: Phase 0 (landed)
Size: Small

## Objective

Loom's outbound MCP client (the manager) negotiates revisions with every upstream, survives non-conformant probe answers, carries the required headers, and fails loudly-and-typed on MRTR results it cannot yet drive. Nothing server-side changes.

## Pinned decisions

- **[proposed] Envelope check is central.** `checkResultEnvelope` moves into `sendRequest` (`pkg/mcp/client/client.go:317-331` vicinity): when `statelessMode` is set, parse the envelope of every successful response and return a typed `*protocol.InputRequiredNotSupportedError{Method string}` on `input_required`. One choke point covers `CallTool` (`tools.go:60`) and every other decoding method. Phase 4 replaces this error with the MRTR driver at the same choke point.
- **[proposed] Revision pin lives on `ServerConfig` and flows into `client.Config`.** New field `ProtocolVersion string yaml:"protocol_version"`; `""`/`"auto"` = negotiate; `"legacy"` = call `Initialize` directly, never probe; an explicit revision (e.g. `"2026-07-28"`) = probe, and fail unless the server offers exactly it.
- **[proposed] Transport surfaces HTTP status as a typed error.** `handleHTTPStatus` (`pkg/mcp/transport/streamable_http.go:344`) wraps non-2xx as `*transport.HTTPStatusError{Code int}`. `Connect` falls back to `Initialize` on `errors.As` codes 404, 405, 501 in addition to JSON-RPC `MethodNotFound`. 401/403/5xx stay hard errors — auth failures and outages must not be masked as "legacy server".
- **Freeze marker is the godoc convention**, one exact string so Phase 9 is a grep: `// Deprecated: frozen legacy MCP surface (docs/architecture/mcp-2026-07-28-migration.md §9.2); removal no earlier than 2027-07-28.` Applied to every §9.2 symbol. Godoc `Deprecated:` makes staticcheck flag new call sites, which *is* the freeze enforcement.

## Work items

1. **Finish the started header work.** The working tree has a half-landed hunk in `streamable_http.go` (`Send` calls `requestHeaderFields` at :139; the helper doesn't exist; `encoding/json` import dangles). Implement `requestHeaderFields(message []byte) (method, toolName string)`: unmarshal `{method, params.name}`; `toolName` non-empty only when `method == "tools/call"`. Sent unconditionally on every POST (harmless to legacy servers, required by 2026-07-28). Until this lands, `pkg/mcp/transport` does not compile — this item is first.
2. **`x-mcp-header` parameter pass-through.** Implement the SEP-2243 mechanism exactly as the spec defines the schema flag (extract the flag definition from the spec — the parent doc deliberately doesn't restate it). Loom's own bridge tools don't use header parameters, so this is client-side conformance only: when a tool schema flags a parameter, emit it as `x-mcp-header-<name>` and omit it from the body arguments.
3. **Manager switch.** `pkg/mcp/manager/manager.go:170`: `Initialize` → `Connect`. Add a getter on the client for the negotiated revision (field `c.protocolVersion` exists; expose `NegotiatedVersion() string`) and log it per server at Info.
4. **Revision pin** per the pinned decision: `config.go` field + validation (reject unknown values at load), `Connect` honors it.
5. **Wider fallback** per the pinned decision: typed status error + `Connect` triggers.
6. **Envelope wiring** per the pinned decision.
7. **Freeze markers** on: `Initialize` (client), `Ping` (client) + `handlePing` (server), `session.go` symbols, `httpSession`/janitor/`handleDelete`, `SubscribeResource` (`resources.go:79`), `NewHTTPTransport`/`HTTPTransport` (`transport/http.go`), `SupportsRoots`, capability struct fields kept by the Immediate brief.

## Test scenarios

| # | Given | When | Then |
|---|---|---|---|
| 1 | Fake server implementing `server/discover` offering `["2026-07-28"]` | `Connect` | Stateless mode; `tools/list` carries stamped `_meta`; `Mcp-Method: tools/list` header present; `Mcp-Name` absent |
| 2 | Fake legacy server (`MethodNotFound` on discover) | `Connect` | Falls back to handshake; legacy mode (exists: `connect_test.go:131`; keep) |
| 3 | Fake HTTP server answering the probe with 404 / 405 / 501 (three cases) | `Connect` | Falls back to handshake for each |
| 4 | Probe answered 401 | `Connect` | Hard error naming the status; no fallback |
| 5 | Probe answered 500 | `Connect` | Hard error; no fallback |
| 6 | Discover offers only `["2031-01-01"]` | `Connect` | `UnsupportedProtocolVersion` typed error listing offered versions |
| 7 | Discover returns empty `protocolVersions` | `Connect` | Error (exists in `discover()`; add test) |
| 8 | Discover offers `["2025-11-25"]` (legacy-family only) | `Connect` | Handshake runs; legacy mode |
| 9 | Pin `protocol_version: "legacy"` vs a discover-capable server | `Connect` | No probe issued (assert on transport); handshake directly |
| 10 | Pin `"2026-07-28"` vs server offering only legacy | `Connect` | Hard error; no silent downgrade |
| 11 | Pin `"bogus"` | Config load | Load-time validation error |
| 12 | Stateless server returns `resultType: "input_required"` to `tools/call` | `CallTool` | `*InputRequiredNotSupportedError` naming `tools/call`; result not decoded as final |
| 13 | Legacy server, result with no `resultType` | Any client method | Treated as complete (no envelope error) |
| 14 | `tools/call` with a header-flagged parameter | Send | Parameter on the wire as `x-mcp-header-<name>`, absent from body |
| 15 (race) | 20 goroutines calling `CallTool` on one stateless client | `-race` | Clean; `_meta` stamping and envelope check are concurrency-safe |
| 16 | `notifications/*` (no id) sent in stateless mode | Send | Headers present; no envelope check attempted on the absent response |

## Acceptance criteria

Standing criteria, plus: manager logs one `negotiated revision` line per configured server on startup against the existing test fixtures; staticcheck reports zero new uses of `Deprecated:` symbols in non-frozen code.

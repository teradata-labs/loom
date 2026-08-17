# MCP 2026-07-28 Migration: Loom Redesign Specification

Status: Draft (revision 2)
Author: Ilsun Park
Date: 2026-08-17
Scope: `pkg/mcp/*`, `pkg/mcp/server` (bridge), `pkg/mcp/manager`, `pkg/fabric/factory`, deployment configuration for HTTP-exposed MCP endpoints.

Revision 2 incorporates a full verification pass (2026-08-17): every inventory row was checked against the working tree, and every specification claim was checked against the official 2026-07-28 changelog. Corrections from that pass are marked inline as **[verified]** where the original draft's claim changed.

## Contents

1. [Background](#1-background)
2. [Current-State Inventory](#2-current-state-inventory)
3. [Design Principles](#3-design-principles)
4. [Workstream: Client](#4-workstream-client)
5. [Workstream: Transport](#5-workstream-transport)
6. [Workstream: Weaver Session Handles](#6-workstream-weaver-session-handles)
7. [Workstream: Server Core](#7-workstream-server-core)
8. [Workstream: Extensions, Apps, Tasks, Auth](#8-workstream-extensions-apps-tasks-auth)
9. [Workstream: Deprecation Removals](#9-workstream-deprecation-removals)
10. [Workstream: Conformance and Testing](#10-workstream-conformance-and-testing)
11. [Alternative Considered: Adopting the Official Go SDK](#11-alternative-considered-adopting-the-official-go-sdk)
12. [Sequencing](#12-sequencing)
13. [Risks and Open Questions](#13-risks-and-open-questions)
14. [Compatibility Statement](#14-compatibility-statement)

## 1. Background

The MCP 2026-07-28 revision was published on July 28, 2026. It is the largest revision since the protocol launched and it replaces the bidirectional stateful core with a request/response stateless core. The revision removes the `initialize` handshake and protocol-level sessions (SEP-2575), introduces the Multi Round-Trip Requests (MRTR) pattern in place of server-initiated requests (SEP-2322), moves Tasks into an official extension (SEP-2663), deprecates Roots, Sampling, and Logging (SEP-2577), reclassifies the HTTP+SSE transport as Deprecated (SEP-2596), and hardens authorization. A formal feature lifecycle policy guarantees a minimum twelve-month window between deprecation and removal, so features deprecated in this revision remain functional until at least July 2027.

Loom ships its own MCP implementation in `pkg/mcp` rather than the official Go SDK, so no part of this migration happens for free. The implementation is currently pinned at protocol revision `2024-11-05` (`pkg/mcp/protocol/types.go:18`), which was already three revisions behind before this release.

The negotiation shim (revision registry, `server/discover` client probe, `_meta` stamping, `resultType` envelope handling) landed in `pkg/mcp/protocol/version.go` and `pkg/mcp/client/connect.go` on 2026-08-17 and is the starting point this specification builds on. **[verified]** One Phase 0 gap remains: `checkResultEnvelope` (`connect.go:133`) is defined but not yet called from any request path, so an `input_required` interim result would today be decoded as a final result. Wiring it into `CallTool` is the first Phase 1 item (§4.2).

## 2. Current-State Inventory

The table lists every component the revision touches and its disposition. All rows were verified against the working tree on 2026-08-17.

| Component | Location | 2026-07-28 status | Disposition |
|---|---|---|---|
| Protocol version constant | `pkg/mcp/protocol/types.go:18` | Pinned `2024-11-05` | Registry landed alongside in `version.go`; the constant remains the legacy handshake default and is still asserted by tests **[verified]** |
| Client handshake | `pkg/mcp/client/client.go` `Initialize` | Handshake removed in new revision | Retained for legacy mode; `Connect` negotiates (landed) |
| Client session tracking | `pkg/mcp/transport/session.go` | `Mcp-Session-Id` removed | Retained for legacy mode; delete after window |
| SSE resumption | `pkg/mcp/transport/resumption.go` | `Last-Event-ID` removed | **[verified]** Already unreachable today: events are buffered but `Last-Event-ID` appears nowhere in the repo and the server offers no GET stream to resume onto. Delete now (§9.1) |
| Legacy HTTP+SSE transport | `pkg/mcp/transport/http.go`; call sites `pkg/mcp/manager/manager.go:145`, `pkg/fabric/factory/factory.go:221` (config values `http`, `sse`) | Reclassified Deprecated (SEP-2596) | **[verified — missing from revision 1]** Freeze; remove after window (§9.2) |
| Server session lifecycle | `pkg/mcp/transport/streamable_http_server.go` (`handlePost`, `httpSession`) | Sessions removed | Dual-mode during window; see §5.2 |
| Standalone GET stream | `streamable_http_server.go` `ServeHTTP` | Replaced by `subscriptions/listen` | Already answers 405; add `subscriptions/listen` (§5.3) |
| `ping` | server `handlePing`, client `Ping` | Removed | Legacy-only during window |
| `logging/setLevel`, `LogNotification` | `pkg/mcp/protocol/types.go` | Removed; per-request `_meta` log level | **[verified]** Neither was ever implemented: `logging/setLevel` is handled nowhere and `LogNotification` is never constructed. §5.4 is new capability, not a replacement |
| Sampling | client `handleSamplingRequest`, `SamplingParams`/`SamplingResult` | Deprecated | **[verified]** Handler plumbing is dead code — `SetSamplingHandler` has no callers, so the handler is always nil and `sampling/createMessage` is already always rejected. Delete plumbing now (§9.1); capability struct fields ride the window (§9.2) |
| Roots capability | `RootsCapability`; `SupportsRoots` config flag | Deprecated | Freeze, then delete (§9.2) |
| `resources/subscribe` | `pkg/mcp/client/resources.go:98` | Replaced by `subscriptions/listen` | Migrate (§5.3) |
| Weaver sessions | `pkg/mcp/server/bridge_tools.go` (`loom_create_session`, `session_id` parameters) | Handle model matches SEP-2567 | No structural change; documented as canonical (§6) |
| MCP Apps | `pkg/mcp/apps`; extension ID at `pkg/mcp/protocol/types_apps.go:22` | Now an official extension | **[verified]** `ExtensionID = "io.modelcontextprotocol/ui"` already exists; the work is declaring it in `ServerCapabilities.Extensions`, not minting it (§8.2) |
| Tool schema validation | `pkg/mcp/protocol/validation.go` (`ValidateToolArguments`) | SEP-2106 loosens schemas to full JSON Schema 2020-12 incl. `$ref` | Audit validator against loosened schemas (§7.6) |
| Auth forwarding | `pkg/mcp/server/auth_forward.go` | Orthogonal to revision | Unchanged; registration/validation work is client-side (§8.3) |
| Manager | `pkg/mcp/manager/manager.go:170` | Calls `Initialize` directly | Switch to `Connect` (§4.1) |
| Conformance | `pkg/mcp/conformance` (single test file) | No dual-revision coverage | Expand to matrix (§10) |

The revision also renumbers the resource-not-found error code from `-32002` to `-32602`. Checked: Loom never uses `-32002`, so no work item exists; recorded here so the check is not repeated.

Three facts discovered during inventory shape the whole design.

First, the bridge already implements exactly the state model SEP-2567 prescribes: `loom_create_session` mints a server-side handle and every session-dependent tool (`loom_weave`, `loom_build`, `loom_get_session`, and the rest) takes `session_id` as an ordinary tool argument.

Second, the server already refuses the standalone GET stream with 405 and delivers request-scoped streaming via POST-response SSE, which is the delivery model the new revision keeps. The migration is therefore narrower than the size of the spec revision suggests: the protocol plumbing changes substantially, but the application-level state architecture is already correct.

Third — **[verified]** and sharper than revision 1 stated — the transport session layer does not gate admission even today. `handlePost` (`streamable_http_server.go:189-201`) validates `Mcp-Session-Id` only when the header is present; a POST that omits the header entirely reaches the handler for any method. The session was only ever bookkeeping. This converts the §6 handle-ownership audit from a migration prerequisite into immediate work (§6, item 2).

## 3. Design Principles

**Dual-revision, no flag day.** Every component speaks both the legacy handshake family (`2024-11-05` through `2025-11-25`) and the stateless revision until the deprecation window closes. Mode is selected per connection on the client (via `Connect`) and per request on the server (via `_meta` inspection and request shape). Nothing behavioral changes for existing deployments until they opt in.

**Client before server.** `pkg/mcp/manager` connects outward to third-party servers that upgrade on their own schedules. GitHub's MCP server announced 2026-07-28 support on July 23, ahead of the official release. Hardening the client first protects Loom against the ecosystem; server-side support is on our own timeline.

**Handles are the state model.** The weaver `session_id` is the canonical cross-call state mechanism. No new protocol-level state is introduced anywhere, and the transport session layer is treated as legacy compatibility machinery from this point forward.

**Idempotency at the handler boundary.** Any tool handler may execute its entry path more than once for one logical operation, from two distinct retry sources:

1. **MRTR retries** — the client retries the original request with a new request ID, carrying the server's `requestState`. Handlers can correlate these: idempotency with respect to arguments plus `requestState` suffices.
2. **Broken-stream re-issues** — SSE resumption is removed, so the specification requires a client that loses a response stream to re-issue the request as a new request. This re-issue carries **no** `requestState` and is byte-identical to a genuinely new call. Arguments alone cannot distinguish "retry of the same logical operation" from "the caller wants this done again" — and `loom_weave` is inherently non-idempotent (the same `session_id` and prompt appends a new turn and runs the agent again).

Source 1 is an invariant handlers can satisfy locally. Source 2 cannot be satisfied by any handler-side check; it requires a client-supplied idempotency key on the wire (§7.5). The dedupe design is a blocker for Phase 2, because stateless admission is what makes spec-mandated re-issues routine.

## 4. Workstream: Client

*Diagram B (§4.1) shows the negotiation flow.*

```
 Manager             Client                                                Server
    │                   │                                                     │
    ├─ Connect() ──────▶│                                                     │
    │                   │                                                     │
    │                   ├─ POST server/discover (unstamped, no _meta) ───────▶│
    │                   │                                                     │
    │     ──────────────────────── alt — 2026-07-28 server responds ──────────────────────────────
    │                   │                                                     │
    │                   │◀─────────────────────────────────── DiscoverResult ─┤
    │                   │                                                     │
    │         ┌──────────────────────────────────────────────┐                │
    │         │ {protocolVersions, capabilities, serverInfo} │                │
    │         └──────────────────────────────────────────────┘                │
    │                   │                                                     │
    │               ┌──────────────────────────────────┐                      │
    │               │ Client negotiates highest mutual │                      │
    │               │    revision → stateless mode     │                      │
    │               └──────────────────────────────────┘                      │
    │                   │                                                     │
    │         ┌───────────────────────────────────────────────────────┐       │
    │         │ Every later request stamped with _meta identity keys; │       │
    │         │            results checked for resultType             │       │
    │         └───────────────────────────────────────────────────────┘       │
    │                   │                                                     │
    │     ────────────────────────────── else — legacy server ────────────────────────────────────
    │                   │                                                     │
    │                   │◀─────────────────── JSON-RPC error: MethodNotFound ─┤
    │                   │                                                     │
    │               ┌───────────────────┐                                     │
    │               │ Client falls back │                                     │
    │               └───────────────────┘                                     │
    │                   │                                                     │
    │                   ├─ initialize ───────────────────────────────────────▶│
    │                   │◀───────────────────────────────── InitializeResult ─┤
    │                   ├- notifications/initialized - - - - - - - - - - - - ▶│
    │                   │                                                     │
    │                   │      ┌───────────────────────────────┐              │
    │                   │      │ legacy mode: sessions, ping,  │              │
    │                   │      │ resources/subscribe available │              │
    │                   │      └───────────────────────────────┘              │
    │                   │                                                     │
    │     ─────────────────────────────────────── end ────────────────────────────────────────────
    │                   │                                                     │
```

### 4.1 Manager migration

`pkg/mcp/manager/manager.go:170` switches from `mcpClient.Initialize(startCtx, clientInfo)` to `mcpClient.Connect(startCtx, clientInfo)`. `Connect` already falls back to `Initialize` on `MethodNotFound`, so this is safe against every conformant server the manager talks to today. Two hardening items accompany the switch:

- **Per-server revision pin.** The manager's server config gains an optional `protocol_version` override that forces a specific revision (or forces the legacy handshake). When an upstream's stateless implementation misbehaves, operators pin it back without waiting for a Loom release.
- **Wider fallback trigger.** `Connect` currently falls back only on JSON-RPC `MethodNotFound`. A legacy server behind a strict gateway may answer the `server/discover` probe with HTTP 404, 405, or 501 instead. Those transport-level statuses also trigger the initialize fallback; other statuses (401, 5xx transients) remain hard errors so real failures are not masked.

The manager gains a per-server log line recording the negotiated revision so operators can see which upstreams have moved.

### 4.2 MRTR retry loop

The `checkResultEnvelope` helper in `connect.go:133` is currently **defined but unwired** — no request path calls it, so an `input_required` interim result would be decoded as a final result today. Wiring it into `CallTool` (fail-fast on `input_required`) is the first Phase 1 item and restores the intended pre-MRTR behavior. The full design then replaces the fail-fast with a driver:

```go
// InputHandler answers a server's inputRequests during an MRTR exchange.
// Returning an error aborts the exchange and fails the original call.
type InputHandler func(ctx context.Context, reqs []protocol.InputRequest) ([]protocol.InputResponse, error)

type MRTRConfig struct {
    Handler   InputHandler
    MaxRounds int // default 5; hard cap on retry rounds per logical call
}
```

`CallTool` (and any other method that can receive `input_required`) drives the loop: parse envelope; on `input_required`, invoke the handler, attach `inputResponses` and the server's `requestState` to a retry of the original request under a new request ID, decrement the round budget, repeat. On budget exhaustion the call fails with a typed error naming the tool and round count. A nil `Handler` preserves fail-fast, which is correct for headless contexts that cannot answer elicitations.

**HITL gate integration.** An `input_required` from a third-party server is the same "human must confirm" event Loom's existing HITL approval gate already models. The default non-nil `InputHandler` shipped with Loom is an adapter that routes `inputRequests` through the HITL gate, so MCP confirmations and Loom-native approvals share one policy surface, one audit trail, and one configuration. A second, parallel confirmation mechanism is explicitly rejected.

New protocol types in `pkg/mcp/protocol` (file `mrtr.go`): `InputRequest`, `InputResponse`, `InputRequiredResult` with `resultType`, `inputRequests`, and `requestState` fields, mirroring the specification schema.

### 4.3 subscriptions/listen client

A new `Subscribe` method opens the single long-lived POST whose response stream carries opted-in change notifications. The client requests only the types it consumes (`toolsListChanged` initially) and demultiplexes on `io.modelcontextprotocol/subscriptionId`. The existing `resources/subscribe` path in `pkg/mcp/client/resources.go` is retained for legacy mode and capability-gated: it is only issued when the negotiated revision is pre-2026 and the server advertised `resources.subscribe`.

A dropped `subscriptions/listen` stream is re-opened with backoff. There is no resumption; missed notifications are reconciled by refetching the affected list, which the `ttlMs` freshness hints make cheap.

### 4.4 Ping and health

`Client.Ping` becomes legacy-gated. On stateless connections, connection health is a transport property (an HTTP request either completes or fails), so the manager's health checks issue a lightweight `tools/list` bounded by `ttlMs` caching, or simply drop protocol-level health checking for HTTP transports.

## 5. Workstream: Transport

### 5.1 Client-side headers and stream semantics

The Streamable HTTP client sends `Mcp-Method` and `Mcp-Name` on every POST unconditionally. The headers are required by the new revision and harmless to old servers, so there is no need to gate them on negotiated mode. `Mcp-Name` carries the tool name on `tools/call` and is omitted otherwise per the specification. Tool parameters flagged for header transport are emitted via `x-mcp-header`.

Session behavior in `streamable_http.go` becomes conditional: the client stores and echoes `Mcp-Session-Id` only when a server sets it (legacy servers), and never fabricates one. On a broken response stream in stateless mode, the in-flight request is failed back to the caller for re-issue as a new request carrying the same idempotency key (§7.5); `resumption.go` is deleted outright since it never had a read path (§9.1).

### 5.2 Server-side session removal

```
                           ┌────────────────────────────────────────────┐
                           │       POST (Streamable HTTP server)        │
                           │ parse JSON-RPC body → extract params._meta │
                           └────────────────────────────────────────────┘
                                                  ▼
                         ┌────────────────────────────────────────────────┐
                         │ _meta[io.modelcontextprotocol/protocolVersion] │
                         │                >= 2026-07-28 ?                 │
                         └────────────────────────────────────────────────┘
                                                  │
                                    ┌─────────────┴────────────┐
                                   yes                        no
                                    ▼                          ▼
                            ┌───────────────┐          ┌───────────────┐
                            │   STATELESS   │          │    LEGACY     │
                            │ (2a–2c below) │          │ (3a–3d below) │
                            └───────────────┘          └───────────────┘

                   ┌───────────────────────────────────────────────────────────┐
                   │ STATELESS branch — dispatch admission (§5.2, steps 2a–2c) │
                   └───────────────────────────────────────────────────────────┘
                                                 ▼
                               ┌────────────────────────────────────┐
                               │ Mcp-Method header == body method ? │
                               └────────────────────────────────────┘
                                                  │
                               ┌──────────────────┴──────────────────┐
                           mismatch                                match
                               ▼                                     ▼
                   ┌───────────────────────┐              ┌─────────────────────┐
                   │    JSON-RPC error     │              │ dispatch to handler │
                   │ -32020 HeaderMismatch │              │ NO session lookup,  │
                   └───────────────────────┘              │  NO session minted  │
                                                          └─────────────────────┘
                                                                     ▼
                                            ┌────────────────────────────────────────────────┐
                                            │ Result stamped: resultType + _meta[serverInfo] │
                                            └────────────────────────────────────────────────┘

                     ┌────────────────────────────────────────────────────────┐
                     │ LEGACY branch — dispatch admission (§5.2, steps 3a–3d) │
                     └────────────────────────────────────────────────────────┘
                                                  ▼
                      ┌──────────────────────────────────────────────────────┐
                      │ no stateless _meta — Mcp-Session-Id header present ? │
                      └──────────────────────────────────────────────────────┘
                                                  │
                     ┌────────────────────────────┴──────────────────┐
                  present                                         absent
                     ▼                                               ▼
          ┌────────────────────┐                        ┌────────────────────────┐
          │ session ID known ? │                        │ method == initialize ? │
          └────────────────────┘                        └────────────────────────┘
                     │                                               │
              ┌──────┴──────────┐                    ┌───────────────┴──────────┐
            known            unknown           == initialize              other method
              ▼                 ▼                    ▼                          ▼
     ┌────────────────┐   ┌──────────┐      ┌─────────────────┐   ┌──────────────────────────┐
     │ touch session, │   │ HTTP 404 │      │ dispatch, mint  │   │ dispatch (admitted today │
     │    dispatch    │   └──────────┘      │  session, set   │   │  with no gate — flagged  │
     └────────────────┘                     │ response header │   │   for the §6 ownership   │
                                            └─────────────────┘   │          audit)          │
                                                                  └──────────────────────────┘
```

`streamable_http_server.go` `handlePost` currently rejects a request bearing an *unknown* `Mcp-Session-Id` with 404, mints a session only on `initialize`, and — as recorded in §2 — admits header-less requests for any method. The dual-mode behavior is:

1. A request carrying `_meta` with `io.modelcontextprotocol/protocolVersion` >= `2026-07-28` is stateless. It is admitted without session lookup, no session is minted, and no `Mcp-Session-Id` response header is set.
2. A request without stateless `_meta` follows the existing legacy path unchanged, including session minting on `initialize`. (Loom's own unstamped `server/discover` probe rides this path safely: no session header means no session check, and dispatch answers `MethodNotFound` on pre-migration servers, which is exactly the fallback signal `Connect` expects.)
3. A request whose `Mcp-Method` header disagrees with the JSON-RPC `method` field is rejected with `HeaderMismatch` (`-32020`).

The `httpSession` map, its janitor, and `handleDelete` become legacy-only machinery and carry a removal marker for the post-window cleanup.

### 5.3 subscriptions/listen server

The server gains a `subscriptions/listen` handler that upgrades the POST response to a long-lived SSE stream, records the requested notification types, and assigns a `subscriptionId`. The existing `notifyCh` in `MCPServer` becomes the publication bus: list-change events are fanned out to active listen streams that opted into the corresponding type. Request-scoped notifications (`notifications/progress`, which `loom_weave` uses to stream partial responses) continue to flow on the originating request's POST-response stream exactly as today; this part of Loom's design already matches the new revision and does not change.

### 5.4 Logging

**[verified]** Loom never implemented the Logging feature: `logging/setLevel` is handled nowhere and `LogNotification` is never constructed. This section is therefore new capability, not a replacement. The `_meta` extraction middleware (§7.1) parses `io.modelcontextprotocol/logLevel` per request and places it in the request context; handlers emit `notifications/message` on the response stream only when the field was present, as the specification mandates. `LoggingCapability` is not advertised to stateless clients, and any existing advertisement to legacy clients is removed now rather than frozen, since nothing has ever backed it.

## 6. Workstream: Weaver Session Handles

No structural change is required, and that is the finding worth recording. The bridge's session tools already constitute the SEP-2567 handle model. The work is consolidation and hardening:

1. **Documentation.** `docs/architecture/weaver-system.md` gains a section declaring `session_id` the canonical cross-call state handle for all revisions, with the transport session explicitly documented as legacy bookkeeping that carries no application state and gates nothing.
2. **Handle validation on every call — immediate, not migration-gated.** **[verified]** Revision 1 framed this as a stateless-revision consequence. The code says otherwise: `handlePost` validates `Mcp-Session-Id` only when the header is present (`streamable_http_server.go:189-201`), so a session-bearing tool call with no header reaches the handler today. Whatever inbound authentication fronts an HTTP deployment is currently the only control. Every handler that accepts `session_id` must validate ownership against the authenticated identity from `auth_forward` context on every invocation. This audit runs now, independent of every phase in §12; any handler that dereferences a session without an ownership check is a finding.
3. **Handle properties.** Session IDs remain UUIDv4 (unguessable). Handles returned to stateless clients should be treated as bearer-scoped, tenant-scoped values, and error responses for wrong-tenant access are indistinguishable from not-found.

### 6.1 Interaction with TER-263 (lazy weaver-creation skill loading)

Lazy skill loading makes `tools/list` output change during a session's lifetime. Under the old model this was per-connection variance; under SEP-2567 list endpoints must not vary per connection, and churn is expressed through freshness metadata instead. The design:

- Core bridge tools (the static `buildToolDefinitions` set) are served with a long `ttlMs` (proposed 300000).
- Skill-derived tools are served with a short `ttlMs` (proposed 15000) while TER-263 loading is possible, and `toolsListChanged` is published on `subscriptions/listen` when a lazy load lands.
- The list itself may vary by authenticated identity (the existing per-tool `visibility` mechanism), which the specification permits; it may not vary by connection.

## 7. Workstream: Server Core

### 7.1 server/discover and _meta middleware

`MCPServer` registers `server/discover` returning `DiscoverResult{ProtocolVersions: ["2026-07-28", "2024-11-05"], Capabilities, ServerInfo}`. This is the mandatory RPC of the new revision and the backward-compatibility probe for clients. The list names only revisions the server actually implements — `2025-11-25` semantics were never implemented server-side, so advertising it would be a false claim; revisions are added to the list only when their conformance cells (§10) exist.

A `_meta` extraction step runs in `HandleMessage` before dispatch: it parses the three identity keys plus `logLevel` and OpenTelemetry keys (`traceparent`, `tracestate`, `baggage`) out of `params._meta` into the request context. Handlers and the observability layer read from context; no handler parses `_meta` itself. Results on stateless requests are stamped with `resultType: "complete"` (or `"input_required"` from MRTR-aware handlers) and `_meta[io.modelcontextprotocol/serverInfo]`.

`handleInitialize` remains registered through the window. `handlePing` answers `MethodNotFound` when the request carried stateless `_meta`, since the method does not exist in that revision.

### 7.2 CacheableResult

`ToolListResult`, `PromptListResult`, `ResourceListResult`, `ReadResourceResult`, and the templates list result gain `ttlMs int64` and `cacheScope string` fields. Because the bridge's tool list varies by identity through `visibility`, `cacheScope` is `"private"` for every bridge deployment; `"public"` is reserved for genuinely tenant-independent servers built on `pkg/mcp/server` and defaults must not permit an intermediary to cache one tenant's tool list for another. This constraint is written into the `MCPServer` provider API so a provider must opt into `public` explicitly.

### 7.3 Deterministic tool ordering

**[verified]** `buildToolDefinitions` returns a slice literal in authored order and `ListTools` returns it verbatim (`bridge.go:278-280`); no sorting exists anywhere in `pkg/mcp/server`. Authored order is stable across calls within one build but not guaranteed across code changes or skill-load interleavings. The list handler sorts by tool name at the response boundary. Stable ordering is a prompt-cache economics change, not a correctness change: clients assembling system prompts from `tools/list` get higher prefix-cache hit rates on Bedrock when the serialization is byte-stable, which reduces consumption-unit cost for every downstream agent.

### 7.4 MRTR on the server

Tool handlers gain the ability to return an interim result:

```go
// Returned by a handler that needs caller input before completing.
type InputRequired struct {
    Requests     []protocol.InputRequest
    RequestState []byte // opaque to the client; see below
}
```

`requestState` is minted by the server and must survive a stateless retry that may land on a different replica. Two tiers:

- **Sealed state.** For confirmations that fit in the token (small, non-secret), `requestState` is the state itself, AEAD-encrypted and authenticated with a key shared across replicas. Replay is bounded by an expiry inside the sealed blob. This tier covers the first consumer below and is the only tier Phase 4 delivers.
- **Durable reference.** For confirmations attached to durable work, `requestState` would be a reference into a durable run store. **[verified]** Revision 1 cited an "ephemeral-runs-durable-ledger architecture" as already providing this; no such architecture exists in this repository (the only "ledger" in code is `sessionToolLedger`, an unrelated tool-visibility map in `pkg/agent/types.go:187`). **Decided 2026-08-17 (D3): deferred.** The Phase 2 idempotency dedupe table is shaped to grow into a run store (`run_dedupe` with `status`/`result` columns), so reviving this tier later is an extension, not new architecture.

The first consumer is the autonomous optimizer: destructive SQL and any plan whose consumption-unit estimate exceeds the tenant's confirmation threshold return `input_required` with a human-readable summary of the action and cost, and proceed only on an affirmative `inputResponse`. This lands the confirm-before-act behavior on stateless remote deployments, which was previously impossible without protocol sessions.

### 7.5 Idempotency keys for broken-stream re-issues

```
   Client                                                                              Server
      │                                                                                   │
  ───────────────────────── Phase 1 — MRTR (multi round-trip request) ────────────────────────────
      │                                                                                   │
      ├─ tools/call reqID=1, _meta[idempotencyKey]=k1 ───────────────────────────────────▶│
      │                                                                                   │
      │◀────────────────────────────────────────────── result: resultType=input_required ─┤
      │                                                                                   │
      │                           ┌───────────────────────────────┐                       │
      │                           │ {inputRequests, requestState} │                       │
      │                           └───────────────────────────────┘                       │
      │                                                                                   │
     ┌─────────────────────────────────────┐                                              │
     │      Client runs InputHandler       │                                              │
     │ (routed through HITL approval gate) │                                              │
     └─────────────────────────────────────┘                                              │
      │                                                                                   │
      ├─ retry tools/call reqID=2, inputResponses, requestState, k1 ─────────────────────▶│
      │                                                                                   │
      │◀──────────────────────────────────────────────────── result: resultType=complete ─┤
      │                                                                                   │
  ───────────────────────────── Phase 2 — broken-stream re-issue ─────────────────────────────────
      │                                                                                   │
      ├─ tools/call reqID=3, _meta[idempotencyKey]=k2 ───────────────────────────────────▶│
      │                                                                                   │
      │                                                       ┌──────────────────────────────────┐
      │                                                       │ Server begins side-effectful run │
      │                                                       └──────────────────────────────────┘
      │                                                                                   │
      │                                   ✕ stream lost — response never arrives ─────────┤
      │                                                                                   │
      ├─ tools/call reqID=4, same idempotencyKey=k2 (re-issue) ──────────────────────────▶│
      │                                                                                   │
      │                                      ┌────────────────────────────────────────────────┐
      │                                      │  Server dedupe map recognizes k2 → joins the   │
      │                                      │ in-flight run instead of starting a second one │
      │                                      └────────────────────────────────────────────────┘
      │                                                                                   │
      │◀─────────────────────────────────── result of original run (resultType=complete) ─┤
      │                                                                                   │
```

§3 established that broken-stream re-issues carry no `requestState` and are indistinguishable from new calls. The mechanism:

- The client stamps every `tools/call` with a fresh UUIDv4 under a reverse-DNS `_meta` key (`com.teradata.loom/idempotencyKey`), and reuses the **same** key when re-issuing after a broken stream. The specification reserves `io.modelcontextprotocol/*` and permits vendor keys, so this rides the existing `_meta` plumbing from §7.1.
- The bridge keeps a short-TTL dedupe map keyed by (authenticated identity, idempotency key). A duplicate arriving while the original run is in flight joins that run and receives its result; a duplicate arriving after completion, within the TTL, receives the cached result. TTL on the order of the longest streaming tool call (proposed 10 minutes) bounds memory.
- Scope: side-effectful bridge tools (`loom_weave`, `loom_build`, session mutation). Read-only tools skip the map.
- Third-party clients that do not send the key get at-least-once semantics, exactly as the specification prescribes; the key is an upgrade for clients that opt in (Loom's own client always does). This is documented in the bridge's tool descriptions so agentic callers can adopt it.

The dedupe decision (this design versus documented at-least-once) is a Phase 2 blocker: stateless admission is what makes re-issues routine.

### 7.6 Schema validation under SEP-2106

SEP-2106 loosens `inputSchema`/`outputSchema` to any JSON Schema 2020-12 keywords and adds `$ref` resolution requirements. `ValidateToolArguments` (`pkg/mcp/protocol/validation.go:24`) is audited against schemas using `$ref` and composition keywords; where full resolution is out of scope, the validator must degrade to accepting (never rejecting) arguments it cannot fully check, so a loosened upstream schema cannot break tool calls.

## 8. Workstream: Extensions, Apps, Tasks, Auth

### 8.1 Extensions capability plumbing

`ClientCapabilities` and `ServerCapabilities` gain `Extensions map[string]json.RawMessage`. The negotiation shim's `StampMeta` starts carrying real capabilities once this lands, replacing the empty `ClientCapabilities{}` placeholder currently stamped.

### 8.2 MCP Apps extension identity

**[verified]** The extension identifier already exists: `ExtensionID = "io.modelcontextprotocol/ui"` at `pkg/mcp/protocol/types_apps.go:22`. The remaining work is declaring it in `ServerCapabilities.Extensions` and auditing the per-tool `_meta` keys against the published extension schema. Functional behavior is expected to be close since the implementation tracked the proposal, but the declaration is what makes Loom's apps discoverable by conformant clients.

### 8.3 Tasks extension

Long-running weaves and workflow executions are exposed through `io.modelcontextprotocol/tasks` as an alternative to POST-response streaming, for clients that cannot hold a stream open. Mapping: a task handle wraps a long-running run; `tasks/get` polls run status and terminal results; `tasks/update` feeds mid-run input, sharing the MRTR `InputRequest` types so a weave that pauses for input is representable in both interaction styles. Streaming remains the default and richer path; tasks are the stateless-client fallback. The old experimental blocking `tasks/result` was never implemented in Loom, so there is no migration debt here, only new surface. The task-handle storage question is the same open run-store decision as §7.4's durable-reference tier (§13).

### 8.4 Authorization

Client side — **decided 2026-08-17 (D4): deferred.** Loom's MCP client has no OAuth machinery today (outbound auth is static bearer headers from manager config, which remain spec-legal), so the client-side items — issuer-keyed credential persistence, RFC 9207 `iss` validation, Resource Indicators (RFC 8707), Client ID Metadata Documents with DCR fallback (`application_type` per SEP-837) — constitute net-new construction, not migration. They move to a dedicated OAuth-client design doc, triggered when the first OAuth-only upstream appears. The CIMD document content and its stable-HTTPS-URL contract remain a small loom-cloud static-hosting deliverable prepared in Phase 7.

Server side: HTTP-exposed bridge deployments publish OAuth 2.0 Protected Resource Metadata (RFC 9728) pointing at the deployment's authorization server (PingFederate in Tera's case). `auth_forward.go` bearer forwarding into looms is unaffected.

## 9. Workstream: Deprecation Removals

**[verified]** Revision 1 froze everything for twelve months. The verification pass showed several rows are unreachable code with no protocol path even in legacy mode; the lifecycle window protects spec-visible behavior, not dead code. The table is therefore split.

### 9.1 Delete now (unreachable code, no observable behavior change)

| Feature | Loom code | Why it is safe now |
|---|---|---|
| SSE resumption | `resumption.go`; the event-buffering write paths in the streamable HTTP server | `Last-Event-ID` appears nowhere in the repo; the server offers no GET stream to resume onto. Write-only buffer |
| Client sampling plumbing | `handleSamplingRequest`, `SetSamplingHandler`, `samplingHandler` field in `client.go`; `SamplingParams`/`SamplingResult` in `types.go` | `SetSamplingHandler` has no callers; the handler is permanently nil and `sampling/createMessage` is already always rejected. Post-deletion rejection (`MethodNotFound`) is equally conformant |
| `LogNotification` construction paths | (none exist) | Never constructed; the type itself is retained only if §5.4 reuses it for the stateless `notifications/message` path, otherwise deleted with this row |
| `LoggingCapability` advertisement | any capability advertisement in server setup | Advertised capability was never backed by an implementation; removing the advertisement corrects an existing misstatement rather than changing behavior |

### 9.2 Freeze now, remove after the window (spec-visible surface)

Nothing in this table is deleted before its date. Everything in it is frozen immediately: no new call sites, no new capabilities layered on top.

| Feature | Loom code | Freeze now | Earliest removal |
|---|---|---|---|
| Legacy HTTP+SSE transport | `transport/http.go`; `manager.go:145`; `factory.go:221`; config values `http`/`sse` | Yes | 2027-07-28 |
| Capability struct fields (Sampling, Roots, Logging) | `SamplingCapability`, `RootsCapability`, `LoggingCapability` fields in `ClientCapabilities`/`ServerCapabilities` | Yes (wire format for legacy handshakes) | 2027-07-28 |
| Roots config surface | `SupportsRoots` config flag | Yes | 2027-07-28 |
| `ping` | server handler, client method | Yes | 2027-07-28 |
| Protocol sessions | `session.go`, `httpSession` machinery, client header echo | Yes | 2027-07-28 |
| `resources/subscribe` | `resources.go:98` | Yes | 2027-07-28 |
| `includeContext` `thisServer`/`allServers` | `SamplingParams.IncludeContext` values | Deleted with sampling plumbing (§9.1) | — |

Loom never depended on MCP sampling for its own inference (`pkg/llm` talks to providers directly), so the sampling deletion removes code without replacing it.

## 10. Workstream: Conformance and Testing

`pkg/mcp/conformance` grows from one file to a dual-revision matrix, following the conformance-suite pattern adopted for the memory subsystem refactor. Axes: revision (legacy handshake, stateless) crossed with transport (stdio, Streamable HTTP) crossed with role (Loom as client, Loom as server). Contents per cell: golden wire-format tests for `_meta` stamping and result envelopes, session admission behavior — including a regression test pinning the header-less admission path that the §6 audit hardens — header requirements and `HeaderMismatch`, MRTR round-trips including round-budget exhaustion and `requestState` tamper rejection, idempotency-key dedupe (duplicate joins in-flight run; duplicate after completion gets cached result; expired key starts fresh), `ttlMs`/`cacheScope` presence and privacy, deterministic tool ordering (byte-identical serialization across repeated calls), and handler idempotency under re-issued requests.

An interop job adds the official Go SDK as a test-only dependency and runs it as the counterpart peer in both directions. This is the cheapest continuous check that `pkg/mcp` tracks the specification rather than our reading of it. One configuration note: the SDK's streamable HTTP transport accepts 2026-07-28 requests only when its `Stateless` option is set, so the harness sets it explicitly.

## 11. Alternative Considered: Adopting the Official Go SDK

The Tier 1 Go SDK shipped 2026-07-28 support with backwards compatibility to `2024-11-05`, and the case for replacing `pkg/mcp` with it is real: the SDK absorbs future revisions, and the deprecation policy plus extensions framework were explicitly designed so implementers on 2026-07-28 avoid rewriting transport and lifecycle code again.

The case against wholesale adoption now: the bridge is deeply integrated with Loom's provider model, auth forwarding, apps compiler, and streaming semantics, and a rip-and-replace would serialize behind this migration rather than run alongside it. The recommendation is a split decision. The server side stays on `pkg/mcp` through this migration. The client side is evaluated for SDK adoption after §4 lands, using the interop suite from §10 as the acceptance gate; if the SDK client covers the manager's needs, `pkg/mcp/client` becomes the first deletion candidate after the window. This decision is revisited in the spec's first quarterly review rather than settled here.

## 12. Sequencing

Each phase has an implementation brief with pinned seam decisions, file-level work items, and the test scenarios that define done: [`docs/planning/mcp-2026-07-28/`](../planning/mcp-2026-07-28/_index.md). This spec stays at design altitude; the briefs are the work orders.

| Phase | Contents | Depends on | Estimate |
|---|---|---|---|
| 0 (landed) | Version registry, `Connect`, `_meta` stamping, envelope parsing, `Initialize` version-check fix. Known gap: `checkResultEnvelope` unwired | — | Done |
| Immediate (no phase) | §6 handle-ownership audit (header-less admission exists today); §9.1 dead-code deletions | — | Small |
| 1 | Manager switch to `Connect` + revision pin + wider fallback (§4.1); wire `checkResultEnvelope` into `CallTool` (§4.2); client headers (§5.1); freeze markers on §9.2 code | 0 | Small |
| 2 | Server `_meta` middleware, `server/discover`, dual-mode admission, `HeaderMismatch` (§5.2, §7.1). **Blocker to resolve first: idempotency-key decision (§7.5)** | 0 | Medium |
| 3 | `CacheableResult`, deterministic ordering, `cacheScope=private` enforcement (§7.2, §7.3); APISIX `Mcp-Method` routing and sticky-session removal in deploy configs | 2 | Small |
| 4 | MRTR types, client retry loop + HITL gate adapter, server `InputRequired`, sealed `requestState` (sealed tier only), optimizer confirmations (§4.2, §7.4) | 2 | Large |
| 5 | `subscriptions/listen` both sides; TER-263 `ttlMs` integration (§4.3, §5.3, §6.1) | 2 | Medium |
| 6 | Extensions field, Apps identity declaration (§8.1–8.2); Tasks extension deferred with the run store (D3, 2026-08-17) | 2 | Small–Medium |
| 7 | Auth: issuer keying, `iss` validation, Resource Indicators, CIMD, `application_type`, PRM (§8.4) | — (parallel) | Medium |
| 8 | Conformance matrix and SDK interop CI (§10) | 1–2, grows with each phase | Medium, ongoing |
| 9 | §9.2 deprecation deletions | Window expiry | Small |

Phases 1 through 3 are the exposure-reduction work and should complete before any upstream server the manager depends on drops legacy support. Phase 4 is the feature payoff and the only phase with novel design risk. Phase 7 has no protocol dependencies and can run whenever auth bandwidth exists.

## 13. Risks and Open Questions

**Broken-stream double execution.** The sharpest consequence of losing resumption: a dropped response stream during a side-effectful call (a `loom_weave` mid-run) followed by the spec-mandated re-issue executes the operation twice unless the §7.5 idempotency key is adopted. No handler-side audit can close this — the information distinguishing retry from new call must arrive on the wire. Decision (dedupe map vs documented at-least-once) blocks Phase 2.

**Handler idempotency debt.** The audit in §6 (item 2) may find session-mutating handlers that assumed single delivery. Each finding is a small fix but the audit itself must be exhaustive; the conformance re-issue tests are the backstop.

**Run store for durable `requestState` and task handles — decided 2026-08-17 (D3): deferred.** §7.4's durable-reference tier and §8.3's task handles both need a durable run store; none exists in this repository, and building one mid-migration was declined. Both features are deferred. The Phase 2 dedupe table carries the growable columns so revival is incremental.

**MRTR latency cost.** Each round trip adds a full client-side model turn in agentic callers. The round budget defaults to 5, and the optimizer's confirmation prompts are designed to resolve in one round. If real traffic shows multi-round exchanges, the confirmation UX gets batched (one `input_required` carrying all pending confirmations) rather than the budget raised.

**`requestState` key management.** The AEAD key for sealed request state must be shared across replicas and rotated. Proposal: derive from the existing deployment secret material with a versioned key ID inside the sealed blob, so rotation invalidates gracefully. This needs a decision before Phase 4 code review.

**notifyCh delivery today.** Server-initiated notifications have no delivery path on HTTP: the only drain of `notifyCh` is the `Serve()` loop, which only the stdio entry point runs (`cmd/loom-mcp/main.go:193`). On HTTP deployments `NotifyResourceListChanged` fills the 16-slot buffer and then drops with warnings (`server.go:340`). `subscriptions/listen` is therefore new capability rather than a migration, and the buffer-fill warning spam is a small live bug fixed in passing during Phase 5. Worth stating in release notes.

**Spec drift.** `pkg/mcp` tracks the specification by hand. The §10 interop job converts drift from a silent risk into a CI failure, and the §11 quarterly review is the standing forum for the SDK-adoption question.

## 14. Compatibility Statement

Loom releases through the deprecation window speak both revision families as client and server. Legacy deployments observe no behavioral change without opting in. The negotiated revision is logged per connection. Deprecated spec-visible features (§9.2) receive no new functionality from the date of this specification and are removed no earlier than 2027-07-28, matching the protocol's lifecycle policy. Unreachable code (§9.1) is deleted immediately, which no client can observe.

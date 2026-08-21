# Phase 6 Brief: Extensions Capability, Apps Identity, Tasks Extension

Parent spec: §8.1, §8.2, §8.3
Depends on: Phase 2. Decision D3 ratified 2026-08-17: **run store deferred — the Tasks portion (Part C) is deferred with it.** Extensions and Apps proceed. When Tasks is revived, it extends the Phase 2 `run_dedupe` table (shaped for this on purpose) rather than introducing new architecture.
Size: Small (Extensions, Apps) + Medium (Tasks, when unblocked)

## Part A — Extensions capability plumbing (§8.1)

1. `Extensions map[string]json.RawMessage` on `ClientCapabilities` and `ServerCapabilities` (`protocol/types.go`), tag `extensions,omitempty`.
2. Client: `StampMeta` call in `sendRequest` starts passing the client's real capabilities instead of the current empty `ClientCapabilities{}` literal (noted as placeholder at Phase 0).
3. Server: `DiscoverResult.Capabilities.Extensions` populated from registered extensions.

## Part B — Apps extension identity (§8.2)

The identifier already exists: `ExtensionID = "io.modelcontextprotocol/ui"` (`pkg/mcp/protocol/types_apps.go:22`). Work:

1. Declare it in `ServerCapabilities.Extensions` when the apps registry (`pkg/mcp/apps`) has content; value object per the published extension schema.
2. Audit every per-tool apps `_meta` key in `pkg/mcp/apps` against the published extension schema; fix drift (the implementation tracked the proposal, so expect small diffs, not a rewrite).

## Part C — Tasks extension (§8.3) — deferred (D3 ratified 2026-08-17)

Deferred with the run store. When revived: `io.modelcontextprotocol/tasks` with `tasks/get` (poll status/terminal result) and `tasks/update` (mid-run input, sharing `InputRequest` types from Phase 4); task handle wraps a run-store entry. The old blocking `tasks/result` was never implemented in Loom — no migration debt, only new surface. Write the detailed work items into this brief when D3 lands.

## Test scenarios

| # | Given | When | Then |
|---|---|---|---|
| 1 | Client with a registered extension | Any stateless request | `_meta` clientCapabilities carries `extensions` with it |
| 2 | Client with none | Request | `extensions` omitted (omitempty), not `{}` |
| 3 | Server with apps content | `server/discover` | `capabilities.extensions["io.modelcontextprotocol/ui"]` present, schema-valid |
| 4 | Server with empty apps registry | `server/discover` | Apps extension absent |
| 5 | Legacy client on handshake path | `initialize` | Unknown `extensions` field ignored; handshake unaffected |
| 6 | Every apps tool's `_meta` | Golden test | Validates against the vendored extension schema |
| 7 | (Tasks, post-D3) long weave via task | `tasks/get` poll loop | pending → terminal result; equals the streamed result of the same weave |
| 8 | (Tasks, post-D3) weave pauses for input | `tasks/update` with `InputResponse` | Run proceeds; same handler path as MRTR retry |

## Acceptance criteria

Standing criteria, plus: the extension schema used in scenario 6 is vendored under `pkg/mcp/conformance/testdata/` with its source URL and retrieval date in a comment.

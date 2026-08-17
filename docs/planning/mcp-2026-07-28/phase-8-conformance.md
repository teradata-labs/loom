# Phase 8 Brief: Conformance Matrix + SDK Interop CI

Parent spec: §10
Depends on: starts alongside Phase 1, grows a cell-set per phase
Size: Medium, ongoing

## Objective

`pkg/mcp/conformance` (today: one test file, no production code) becomes the dual-revision matrix and the continuous check that `pkg/mcp` tracks the specification rather than our reading of it. Phase-specific scenarios live in each phase brief and are implemented with their phase; this brief owns the harness, the cross-cutting scenarios no single phase owns, and the interop job.

## Structure

```
pkg/mcp/conformance/
  harness.go            // fixture servers/clients per (revision, transport, role) cell
  legacy_test.go        // 2024-11-05 handshake family cells
  stateless_test.go     // 2026-07-28 cells
  mixed_test.go         // both families against one server
  interop_test.go       // official Go SDK as counterpart peer (build tag: interop)
  testdata/
    schema.2026-07-28.json   // vendored official schema, source URL + date in header
    golden/                  // wire-shape goldens (Phase 2+ contribute)
```

- Table-driven cells: (revision: legacy, stateless) × (transport: stdio, streamable HTTP) × (role: Loom-as-client, Loom-as-server). Not every scenario runs in every cell; each row declares its cells.
- The vendored `schema.json` is the source of truth for goldens — tests validate marshaled shapes against it, so spec drift fails here first.

## Cross-cutting scenarios (owned by this brief)

| # | Scenario | Cells |
|---|---|---|
| X1 | Mixed mode: legacy client (sessionful) and stateless client interleave 100 requests against one server | server, HTTP |
| X2 | Session janitor expiry occurs mid-sequence; stateless traffic unaffected; legacy client gets 404 on next use, recovers by re-initialize | server, HTTP |
| X3 | Every deprecated legacy method (`ping`, `initialize`, `resources/subscribe`) answered correctly in legacy mode AND `MethodNotFound` (or per-spec equivalent) under stateless `_meta` | server, both transports |
| X4 | `server/discover` idempotent and identical across 10 calls, both modes, unstamped and stamped | server, both |
| X5 | Handler idempotency sweep: every registered bridge tool invoked twice with identical args + same idempotency key; state postcondition identical to single invocation | server, HTTP |
| X6 | Full-matrix `-race` soak: every cell's fixture pair exchanging concurrent traffic, `-count=5` in CI, `-count=50` in the `just race-check` extended run | all |
| X7 | Byte-stability regression: serialized `tools/list` golden unchanged (deliberate changes require regenerating the golden in the same PR) | server, both |
| X8 | Malformed inputs: truncated JSON, `_meta` wrong types, 11MB body (limit is 10MB), unknown `resultType` value from a server | both roles |

## Interop job (official Go SDK)

1. Test-only dependency on `github.com/modelcontextprotocol/go-sdk` behind an `interop` build tag so the main module graph stays clean.
2. **SDK client → Loom server:** stateless mode (the SDK's streamable HTTP transport requires its `Stateless` option set for 2026-07-28 — parent spec §10); exercises discover, tools/list (asserting `ttlMs`/`cacheScope`/ordering survive SDK decoding), tools/call, MRTR round-trip (SDK as the answering client), subscriptions/listen.
3. **Loom client → SDK server:** negotiation lands on 2026-07-28; stamped `_meta` accepted; envelope parsing of SDK results; MRTR against an SDK handler that elicits; legacy fallback against the SDK server pinned to 2024-11-05.
4. CI: a dedicated workflow job `mcp-interop` running `go test -tags "fts5 interop" ./pkg/mcp/conformance/ -run Interop`; failure blocks merge like any other check. Pin the SDK version in go.mod; a scheduled monthly job runs against `@latest` as an early-warning (allowed to fail without blocking, but files an issue).

## Acceptance criteria

Standing criteria, plus: the harness lands with Phase 1 (cells for negotiation scenarios 1–16 of that brief); every later phase PR adds its brief's scenario rows to the matrix in the same PR — a phase is not done while its conformance cells are empty; `just check` runs the matrix (minus `interop` tag); CI gains the interop job by Phase 2's merge.

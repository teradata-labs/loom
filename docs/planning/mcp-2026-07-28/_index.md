# MCP 2026-07-28 Migration — Implementation Briefs

Parent specification: [`docs/architecture/mcp-2026-07-28-migration.md`](../../architecture/mcp-2026-07-28-migration.md)
Official protocol reference: [MCP 2026-07-28 changelog](https://modelcontextprotocol.io/specification/2026-07-28/changelog)
Created: 2026-08-17

The parent spec is the design record: what changes, where, and why. Each brief here is a self-contained work order for one phase: pinned decisions for every seam the spec leaves open, file-level work items, and the test scenarios that define done. A coding agent should be handed exactly three things per phase: the brief, the parent spec, and the official 2026-07-28 specification.

Pinned decisions marked **[proposed]** are best-judgment calls that have not been ratified; veto them before starting the phase, not during review.

## Execution order

| Brief | Contents | Depends on | Status |
|---|---|---|---|
| [Immediate](phase-immediate.md) | Session-ownership enforcement; §9.1 dead-code deletions | nothing | ⚠️ Part A (ownership) open; Part B ✅ done 2026-08-17 (`1a6c4e69`), exported surface later restored-frozen per PR #327 review finding 5 (see brief amendment) |
| [Phase 1](phase-1-client-exposure.md) | Manager → `Connect`, revision pin, wider fallback, envelope wiring, client headers, freeze markers | Phase 0 (landed) | ✅ Done 2026-08-17 (`61f59492`, `3ed4bf51`, `5a147ddd`) incl. all outbound client paths |
| [Phase 2](phase-2-server-dual-mode.md) | `server/discover`, `_meta` middleware, dual-mode admission, idempotency keys | Phase 0; D1 ✅ | ⚠️ Client half ✅ (`a45d0474`: key stamping, stream-loss synthesis + re-issue); server half open |
| [Phase 3](phase-3-caching-ordering.md) | `CacheableResult`, deterministic ordering, deploy config changes | Phase 2 | 📋 Ready after 2 |
| [Phase 4](phase-4-mrtr.md) | MRTR types, client driver + HITL adapter, server `InputRequired`, sealed state | Phase 2; D2 ✅ | ⚠️ Client half ✅ (`64cffb63`: protocol types, driver, elicitation capability, HITL adapter); server half open |
| [Phase 5](phase-5-subscriptions.md) | `subscriptions/listen` both sides; notifyCh fix; TER-263 `ttlMs` | Phase 2 | ⚠️ Client half ✅ (`6c528670`: Subscribe, demux, manager watch loop); server half + notifyCh fix open |
| [Phase 6](phase-6-extensions-tasks.md) | Extensions capability, Apps identity, Tasks extension | Phase 2; D3 ✅ = Tasks deferred | ⚠️ Capabilities `extensions` fields ✅ (in `64cffb63`); Apps declaration + discover wiring open; Part C deferred |
| [Phase 7](phase-7-auth.md) | PRM server-side (D4 ✅ scope b) | none (parallel) | 📋 Ready |
| [Phase 8](phase-8-conformance.md) | Conformance matrix, scenario tables, SDK interop CI | grows with 1–6 | 📋 Starts with Phase 1 |
| Phase 9 | §9.2 deletions at window expiry (2027-07-28+): grep for the Phase 1 freeze-marker string, delete everything it tags, drop the `http`/`sse` transport values | Window expiry | 📋 Not before 2027-07-28 |

## Decisions (ratified 2026-08-17, decider: Ilsun Park)

| ID | Decision | Blocks | Ratified answer |
|---|---|---|---|
| D1 | Idempotency dedupe placement across replicas | Phase 2 — **unblocked** | ✅ (a) Forward key to looms as gRPC metadata; looms dedupes at the state owner. Dedupe table shaped to grow into the D3 run store (see Phase 2 brief) |
| D2 | AEAD key derivation and rotation for sealed `requestState` | Phase 4 review | ✅ HKDF root = `LOOM_SERVER_AUTH_SUPABASE_JWT_SECRET` (already replica-shared in auth-enabled HTTP deployments); JWKS-only deployments set a dedicated `LOOM_MCP_STATE_SECRET` (see Phase 4 brief) |
| D3 | Durable run store for task handles and durable `requestState` | Phase 6 Tasks, §7.4 tier 2 | ✅ (a) Defer; ship sealed-state tier only. D1's dedupe table carries the growable columns so this becomes an extension, not new architecture |
| D4 | OAuth client scope for Phase 7 | Phase 7 — **unblocked** | ✅ (b) Server-side only (RFC 9728 PRM + CIMD hosting deliverable); full OAuth client deferred to its own design doc, triggered by the first OAuth-only upstream |

## Standing acceptance criteria (every phase)

- `just check` passes; verify the final "All checks passed" line directly — never piped through `tail`/`grep`.
- `go test -tags fts5 -race ./pkg/mcp/...` passes. New concurrent structures get dedicated `-race` scenarios listed in the phase brief.
- `gofmt -l` clean on touched files (CI checks gofmt separately from golangci-lint).
- Proto is law: any change to looms RPC behavior that needs new fields goes through `proto/loom/v1/*.proto` + `buf generate` + `buf breaking` first.
- Documentation touched by the phase is updated in the same PR; no claims ahead of implementation.

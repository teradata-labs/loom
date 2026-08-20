# Phase 4 Brief: MRTR — Types, Client Driver, Server InputRequired, Sealed State

Parent spec: §4.2, §7.4, §13 (key management)
Depends on: Phase 2. Decision D2 ratified 2026-08-17 — see the pinned decision below.
Size: Large — the only phase with novel design risk

## Objective

A server handler can pause for caller input; Loom's client can answer such pauses (through the HITL gate) and retry; sealed `requestState` survives replica hops; the optimizer's confirm-before-act ships on it.

## Pinned decisions

- **[proposed] Handler signal is a typed error.** `MethodHandler` stays `func(ctx, id, params) (interface{}, error)` (`server.go:33`) — no signature migration across every handler. A pausing handler returns `&protocol.InputRequiredError{Requests []InputRequest, RequestState []byte}`; `HandleMessage` converts it via `errors.As` into an `InputRequiredResult` (`resultType:"input_required"`). Bridge tool handlers (`func(ctx, args) (*CallToolResult, error)`) use the same error through the `tools/call` dispatcher.
- **[proposed] Retry delivery to handlers is middleware-based.** The Phase 2 `_meta`/params middleware extracts `inputResponses` + `requestState` from a retried request into the context; handlers read them via `protocol.InputFromContext(ctx)`. A handler never parses the retry shape itself.
- **[proposed] Client driver replaces the Phase 1 fail-fast at the same choke point** (`sendRequest` envelope check): on `input_required`, invoke `MRTRConfig.Handler`, re-issue the original request with a **new request ID**, same `idempotencyKey`, plus `inputResponses` and echoed `requestState`; decrement `MaxRounds` (default 5); typed exhaustion error names tool + rounds. `MRTRConfig` lives on `client.Config`; nil handler keeps Phase 1 fail-fast.
- **[proposed] HITL adapter ships in `pkg/mcp/manager`:** `NewHITLInputHandler(gate)` mapping `InputRequest` elicitations onto the existing approval-gate API (the require_approval/HITL machinery under `pkg/server` + `pkg/tools/registry`); deny → handler error → MRTR aborts → original call fails with the denial reason. One policy surface, one audit trail (parent spec §4.2).
- **D2 [ratified 2026-08-17]: AEAD = AES-256-GCM; key = HKDF(root secret, info="loom-mcp-requeststate-v1"); sealed blob = keyID ‖ nonce ‖ ciphertext; plaintext carries expiry (default 10 min) + principal.** Rotation: new keyID; old accepted until its expiry horizon passes. Unseal failures (unknown keyID, tamper, expiry, principal mismatch) are all the same client-visible error. **Root secret:** `LOOM_SERVER_AUTH_SUPABASE_JWT_SECRET` — already consumed by both looms (`cmd/looms/config.go:1116`) and loom-mcp (`cmd/loom-mcp/main.go:289`) and necessarily identical across replicas in HS256-validating deployments; the HKDF info string provides domain separation from its JWT use. Deployments validating via JWKS only (asymmetric, no shared symmetric secret) MUST set a dedicated `LOOM_MCP_STATE_SECRET` instead; startup fails loudly when MRTR is enabled and neither is present. Document both in ENV_VARS.md.
- **Durable-reference tier is out of scope** (decision D3, parent §7.4) — sealed tier only.

## Work items

1. `pkg/mcp/protocol/mrtr.go`: `InputRequest`, `InputResponse`, `InputRequiredResult` mirroring the spec schema exactly (lift field names from the official schema.json; golden-test against it).
2. Server conversion in `HandleMessage`/`HandleMessageStream` per pinned decision; retry middleware.
3. `internal/` sealed-state package per D2 (unexported outside the server).
4. Client driver per pinned decision.
5. HITL adapter per pinned decision.
6. Optimizer consumer: destructive-SQL and cost-threshold confirmations return `InputRequiredError` with a human-readable summary; proceed only on affirmative response. Verify the optimizer's package location and the tenant threshold config source at implementation time — both unverified in this repo as of 2026-08-17; if the threshold config doesn't exist, that's a finding to raise, not a value to invent.

## Test scenarios

| # | Given | When | Then |
|---|---|---|---|
| 1 | Handler pauses with 1 InputRequest | Client with handler | One retry, new request ID, same idempotency key; completes; handler saw `inputResponses` via context |
| 2 | Nil client handler | `input_required` received | Fail-fast typed error (Phase 1 behavior preserved) |
| 3 | Handler pauses every round | MaxRounds=5 | Exhaustion error naming tool + 5 rounds; 5 retries on the wire, no more |
| 4 | `requestState` bit-flipped by client | Retry | Unseal rejection; same error shape as expiry |
| 5 | `requestState` older than expiry | Retry | Rejected; same error shape as tamper |
| 6 | Retry sealed for principal X replayed by principal Y | Retry | Rejected |
| 7 | Unknown keyID (post-rotation) | Retry | Rejected gracefully |
| 8 | Sealed state round-trip across two server instances sharing the secret | Pause on A, retry to B | Completes (replica-hop guarantee) |
| 9 | HITL gate approves / denies (two cases) | MRTR elicitation | Approve → completes; deny → original call fails carrying denial reason |
| 10 | Optimizer: destructive SQL | `tools/call` | `input_required` with human-readable summary; affirmative → executes; negative → nothing executed |
| 11 | Optimizer: cost above threshold | `tools/call` | Confirmation round; below threshold → no round |
| 12 | Legacy-mode connection | Handler that would pause | Handler must not emit `input_required` to a legacy client — define and test the fallback (error instructing interactive use) |
| 13 | Golden: `InputRequiredResult` wire shape | Marshal | Matches official schema.json |
| 14 (race) | Concurrent MRTR exchanges on one client | `-race` | Clean; round budgets independent |
| 15 (race) | Concurrent pause/retry on one server, shared sealed-state key | `-race` | Clean |

## Acceptance criteria

Standing criteria, plus: D2 ratified in review before merge; scenario 10 demonstrated end-to-end on a stateless HTTP deployment (the capability parent §7.4 names as previously impossible); scenarios 4–7 assert byte-identical error responses (indistinguishability).

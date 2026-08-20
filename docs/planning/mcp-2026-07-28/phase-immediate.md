# Immediate Brief: Session-Ownership Enforcement + Dead-Code Deletions

Parent spec: §2 (fact 3), §6 (item 2), §9.1
Depends on: nothing — this work is independent of every migration phase
Size: Small–Medium (ownership), Small (deletions)

## Part A — Session-ownership enforcement

### Why now

`handlePost` validates `Mcp-Session-Id` only when the header is present (`pkg/mcp/transport/streamable_http_server.go:189-201`); a POST omitting it reaches any handler. The transport session never gated anything, so session-handle ownership must be enforced at the state owner. This is a current-state gap, not a migration consequence.

### Architecture fact that shapes the work

Bridge session tools are thin gRPC passthroughs to looms (`pkg/mcp/server/bridge_handlers.go:396-435`, all via `callGRPC` → `b.client.CreateSession` etc.). The caller's edge-validated identity is forwarded as a bearer on outgoing gRPC metadata (`pkg/mcp/server/auth_forward.go`). Therefore enforcement lives in **looms** (the gRPC service), not the bridge.

### Verified precondition

The `sessions` table records no owner: `pkg/storage/sqlite/migrations/000001_initial_schema.up.sql:34-45` has `id, name, agent_id, parent_session_id, context_json, created_at, updated_at, total_cost_usd, total_tokens` — nothing identity-shaped. (Graph memory tables have `owner`/`user_id`; sessions do not.) So this is record-then-enforce, not just enforce.

### Pinned decisions

- **[proposed] Owner is recorded, not proto-visible.** Owner comes from the validated identity in incoming gRPC metadata server-side; no proto field is added to `CreateSessionRequest`. Clients never assert their own identity. No proto change → no `buf breaking` risk.
- **[proposed] Local mode bypass.** When the server runs without authentication (local stdio/single-user deployments), owner is recorded as the empty string and enforcement is skipped. Whenever a validated identity is present, enforcement is mandatory. The bypass is keyed off the server's auth configuration, not off a missing header (a missing header on an auth-enabled deployment is a deny).
- **Wrong-owner is indistinguishable from not-found** (parent spec §6 item 3): same gRPC `NotFound` code, same message shape, no timing-observable difference in the lookup path (fetch row, then compare owner — never "no row for you").

### Work items

**[amended during implementation, 2026-08-17]** The investigation overturned several assumptions: (1) Postgres already has full session scoping (`user_id` column via its migration 000006, RLS policies, `execInTx` identity enforcement) — no Postgres work needed; (2) four gRPC handlers already enforced ownership; the actual gaps were `Weave`/`StreamWeave` (via `findAgentBySession`), `GetConversationHistory`, `SubscribeToSession`, and `AnswerClarificationQuestion`; (3) the SQLite `sessions` schema is owned by `SessionStore.initSchema`'s guarded ALTER map (pkg/agent/session_store.go), not the numbered migrator — a `000009` migration file double-ALTERs and was reverted in favor of the map + base schema; (4) the established owner convention is `user_id` defaulting to `'default-user'` (matching Postgres 000006), not an empty-string owner; the fail-open predicate for unstamped sessions is retained deliberately (documented on `sessionAccessibleBy`) so upgrades don't strand live sessions. `AnswerClarificationQuestion` enforcement deferred: `metaagent.Question` carries no session linkage (threading it is a metaagent surface change), and question IDs are unguessable UUIDs — same property session handles had pre-enforcement. Identity plumbing moved to `pkg/types` (`ContextWithUserID`/`UserIDFromContext`) because `postgres` imports `agent`, with postgres delegating for API stability.

1. ~~Migration `000009_session_owner`~~ → `user_id` column via `SessionStore.initSchema` base schema + guarded ALTER map + index (see amendment above).
2. Locate the identity extraction point in looms (the server-side interceptor that validates the forwarded bearer) and expose the principal on the request context if not already there.
3. `CreateSession` records the principal as owner.
4. Every session-scoped RPC enforces owner match: `GetSession`, `DeleteSession`, `GetConversationHistory`, `AnswerClarification`, `ListSessions` (filter, don't error), and the session-accepting paths of `Weave`/`Build`. Enumerate the full list by grepping looms handlers for `SessionId` — the six bridge passthroughs above are the floor, not the ceiling.
5. Existing rows: `DEFAULT ''` means pre-migration sessions are owned by "local"; on auth-enabled deployments they become inaccessible to everyone. Document this in the migration notes as intended (they were never ownership-scoped, so keeping them reachable would grandfather the vulnerability).

### Test scenarios

| # | Given | When | Then |
|---|---|---|---|
| A1 | Auth enabled; session created by identity X | Y calls `loom_get_session` with X's session_id | `NotFound`, byte-identical shape to a nonexistent id |
| A2 | Auth enabled; session created by identity X | X calls `loom_get_session` | Session returned |
| A3 | Auth enabled | Y calls `loom_delete_session`, `loom_get_conversation_history`, `loom_weave(session_id)` on X's session | `NotFound` each; session unchanged |
| A4 | Auth enabled; X owns 2 sessions, Y owns 1 | X calls `loom_list_sessions` | Exactly X's 2 |
| A5 | Auth disabled (local mode) | Any session op without identity | Works as today |
| A6 | Auth enabled | Request arrives with no identity | Deny (not silently local-mode) |
| A7 | Pre-migration session rows (owner='') | Auth-enabled deployment, any identity accesses | `NotFound` |
| A8 (race) | Two goroutines: X creating a session, Y probing predicted/random ids in a loop | `-race`, 50 iterations | No race; Y never sees X's session |

## Part B — §9.1 dead-code deletions

All rows verified unreachable on 2026-08-17; deleting them changes no observable behavior.

> **[amended 2026-08-18, PR #327 review finding 5]** "Unreachable" is repo-local and cannot cover external importers of exported Go API. Items 1 and 3 were executed as written and then partially reversed: the **exported** symbols (`StreamResumption`; `SamplingHandler`/`SetSamplingHandler` and the sampling/log types) are restored as frozen §9.2 surface with `Deprecated:` markers — functional, source-compatible, removal no earlier than 2027-07-28. Only the unexported wiring (the resumption buffer's transport write paths) stayed deleted. See the migration spec's §9.1 "compatibility boundary" note; do not re-execute the deletions below as written.

### Work items

1. Delete `pkg/mcp/transport/resumption.go` and the event-buffering write paths in the streamable HTTP server (writes at the sites that populate it; grep `StreamResumption`/`AddEvent`). Delete its tests. *(Amended: only the write paths stay deleted; the exported type and its tests are restored-frozen.)*
2. `ServerConfig.EnableResumption` (`pkg/mcp/manager/config.go:65`) is user-visible YAML: keep the field parsed, make it a no-op, log one deprecation warning when set. Field removal happens at Phase 9.
3. Delete client sampling plumbing: `handleSamplingRequest`, `SetSamplingHandler`, the `samplingHandler` field and the `sampling/createMessage` dispatch case in `pkg/mcp/client/client.go`; the delegation in `instrumented_client.go:709`; `SamplingParams`/`SamplingResult` (and `IncludeContext` values) in `pkg/mcp/protocol/types.go`; their tests. Keep `SamplingCapability`/`RootsCapability`/`LoggingCapability` **struct fields** on the capabilities types — they are wire format for legacy handshakes (§9.2). *(Amended: the exported sampling surface is restored-frozen and functional for legacy connections; nothing sampling-related is deleted before the window.)*
4. Delete `LogNotification` (`types.go:250`) — never constructed. Phase 5 recreates a `notifications/message` type if §5.4 needs one; dead code does not wait for it.
5. Remove any `LoggingCapability` advertisement from server setup if present (grep server constructors) — it was never backed by an implementation.

### Test scenarios

| # | Given | When | Then |
|---|---|---|---|
| B1 | Legacy server sends `sampling/createMessage` to the client | Post-deletion | Request rejected with `MethodNotFound`-class error; client does not crash; connection unaffected |
| B2 | Config with `enable_resumption: true` | Manager loads config | Loads fine; one warning logged; behavior identical |
| B3 | Full suite | `go test -tags fts5 -race ./pkg/mcp/...` | Green; no test references deleted symbols |

## Acceptance criteria

Standing criteria from `_index.md`, plus: migration applies and rolls back cleanly on both sqlite and postgres; scenario A1's indistinguishability is asserted by comparing marshaled error responses for wrong-owner vs nonexistent-id.

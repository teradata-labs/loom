# Phase 7 Brief: Authorization

Parent spec: §8.4
Depends on: nothing (parallel). Decision D4 ratified 2026-08-17: scope (b) — unblocked.
Size: Medium (scope b) / Large (scope a)

## Honest precondition

Loom's MCP client has **no OAuth machinery today**: outbound auth is static bearer headers from config (`pkg/mcp/manager/config.go:59` `Headers`). The parent spec's client-side items (issuer-keyed credentials, RFC 9207 `iss` validation, RFC 8707 resource indicators, CIMD) all presuppose an OAuth 2.0 authorization-code client with token persistence — which would be net-new construction, not migration. (`internal/supabaseauth` is the Dreambase/Supabase flow, not reusable for arbitrary MCP authorization servers without evaluation.)

## Decision D4 — scope **[ratified 2026-08-17: (b), decider Ilsun Park — full OAuth client deferred]**

- **(a)** Build the full MCP OAuth client now: authorization-code + PKCE, token store keyed by issuer, `iss` validation, resource indicators, CIMD-preferred registration with DCR fallback (`application_type` set per SEP-837).
- **(b)** Server side only now; client stays on static bearers (which remain spec-legal); full OAuth client becomes its own later design doc.

Rationale for (b): every managed upstream Loom talks to today works with static bearers; the client OAuth surface is the largest untested-new-code item in the whole migration and deserves its own spec rather than riding this one.

## Work items under scope (b)

1. **Protected Resource Metadata (RFC 9728):** HTTP-exposed bridge deployments serve `/.well-known/oauth-protected-resource` naming the deployment's authorization server (PingFederate for Tera deployments). Deployment-config-driven (issuer URL, resource id); endpoint served by the same mux as the MCP endpoint; absent when unconfigured.
2. **CIMD hosting deliverable handed to loom-cloud:** a client metadata document at a stable HTTPS URL. This repo's deliverable is the document content + a documented URL contract, not the hosting.
3. `auth_forward.go` bearer forwarding into looms: unchanged; add a regression test pinning that.

If (a) is chosen instead, write a separate design doc first (token storage, keychain vs file, multi-issuer lifecycle, refresh, revocation) — do not implement from this brief.

## Test scenarios (scope b)

| # | Given | When | Then |
|---|---|---|---|
| 1 | Deployment configured with an authorization server | GET `/.well-known/oauth-protected-resource` | RFC 9728-shaped JSON naming the issuer and resource |
| 2 | No auth configuration | Same GET | 404 |
| 3 | Metadata golden | Marshal | Validates against RFC 9728 required fields |
| 4 | Bearer arrives at bridge edge | Any session-scoped tool call | Same bearer on outgoing looms gRPC metadata (regression-pins `auth_forward`) |
| 5 | PRM endpoint | Concurrent GETs, `-race` | Clean; content stable |

## Acceptance criteria

Standing criteria, plus: D4 recorded in this file with decider and date before implementation starts; if (b), the parent spec §8.4 client-side sentence gains a pointer to the follow-on design doc so the spec stops implying the client work is part of this migration.

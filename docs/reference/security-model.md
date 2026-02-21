
# Security Model Reference

**Version**: v1.1.0

Security model for Loom's multi-tenancy, user identity, and data isolation features.
This document covers the trust model for `x-user-id`, PostgreSQL Row-Level Security (RLS),
admin token authentication, and deployment requirements.

**Available Since**: v1.1.0 (PostgreSQL multi-tenancy)


## Table of Contents

- [Quick Reference](#quick-reference)
- [Trust Model: x-user-id Header](#trust-model-x-user-id-header)
- [What Loom Validates](#what-loom-validates)
- [What Loom Does NOT Do](#what-loom-does-not-do)
- [PostgreSQL Row-Level Security (RLS)](#postgresql-row-level-security-rls)
- [Admin Token Authentication](#admin-token-authentication)
- [Deployment Requirements](#deployment-requirements)
- [Recommended Deployment Patterns](#recommended-deployment-patterns)
- [SQLite Limitations](#sqlite-limitations)
- [Configuration Reference](#configuration-reference)
- [Threat Model](#threat-model)
- [See Also](#see-also)


## Quick Reference

| Aspect | Status | Details |
|--------|--------|---------|
| User authentication | **Not performed by Loom** | Delegated to reverse proxy / API gateway |
| User identity source | `x-user-id` gRPC metadata header | Trusted as-is from caller |
| Identity validation | Input sanitization only | Max 256 chars, no control characters |
| Data isolation (PostgreSQL) | Row-Level Security (RLS) | `SET LOCAL app.current_user_id` per transaction |
| Data isolation (SQLite) | **None** | Single-tenant mode, all data shared |
| Admin authentication | `x-admin-token` gRPC metadata header | Compared against `LOOM_ADMIN_TOKEN` env var |
| Transport security | TLS (configurable) | See `docs/reference/tls.md` |
| JWT verification | **Not performed** | Must be done by upstream proxy |
| OAuth/OIDC | **Not performed** | Must be done by upstream proxy |


## Trust Model: x-user-id Header

Loom uses a **trusted header** model for user identity. The `x-user-id` gRPC metadata
header is the sole source of user identity for all tenant-scoped operations.

### How It Works

1. A client sends a gRPC request with `x-user-id` metadata.
2. Loom's `UserIDUnaryInterceptor` or `UserIDStreamInterceptor` extracts the value.
3. The value is validated for length and character constraints (see [What Loom Validates](#what-loom-validates)).
4. The validated user ID is stored in the Go `context.Context` via `postgres.ContextWithUserID()`.
5. On every database transaction, `execInTx()` calls `SET LOCAL app.current_user_id = $1` to activate RLS policies.

### The Critical Assumption

**Loom trusts whatever value arrives in the `x-user-id` header.**

This means:

- If a caller sends `x-user-id: alice`, Loom treats the request as belonging to user `alice`.
- If a caller sends `x-user-id: bob`, Loom treats the request as belonging to user `bob`.
- Loom does not verify that the caller is actually `alice` or `bob`.
- There is no session token, JWT, cookie, or password check.

This is the standard **API gateway pattern** used by systems like Envoy, Nginx, AWS API Gateway,
and Google Cloud Endpoints. The gateway handles authentication (OAuth, JWT, mTLS, etc.) and
injects a verified identity header before forwarding to the backend service.

### Source Code

- **Interceptors**: `pkg/server/interceptors.go` -- `UserIDUnaryInterceptor`, `UserIDStreamInterceptor`
- **Context propagation**: `pkg/storage/postgres/tenant.go` -- `ContextWithUserID`, `UserIDFromContext`
- **RLS activation**: `pkg/storage/postgres/tenant.go` -- `execInTx` (calls `SET LOCAL`)
- **Server wiring**: `cmd/looms/cmd_serve.go` (search for `UserIDConfig`)


## What Loom Validates

Loom performs **input sanitization** on the `x-user-id` header value. This prevents
malformed or malicious values from reaching the database, but it is not authentication.

### Validation Rules

| Rule | Constraint | gRPC Error Code | Error Message |
|------|-----------|-----------------|---------------|
| Non-empty | Must not be empty string | `UNAUTHENTICATED` (when `require_user_id: true`) | `x-user-id header required` |
| Max length | 256 characters maximum | `INVALID_ARGUMENT` | `user ID exceeds maximum length of 256 characters (got N)` |
| No control characters | No bytes below `0x20` (space) | `INVALID_ARGUMENT` | `user ID contains control character at position N (byte 0xNN)` |

### Allowed Characters

The following are accepted in user ID values:

- Alphanumeric: `a-z`, `A-Z`, `0-9`
- Common delimiters: `-`, `_`, `.`, `/`, `:`, `~`, `@`
- Spaces (byte `0x20` and above)
- Unicode characters (UTF-8 encoded)
- UUID format: `550e8400-e29b-41d4-a716-446655440000`
- Email format: `alice@example.com`
- Hierarchical format: `org/team/user` or `tenant:user:123`

### Rejected Characters

- Null byte (`0x00`)
- Tab (`0x09`)
- Newline (`0x0A`)
- Carriage return (`0x0D`)
- All ASCII control characters (`0x00` through `0x1F`)

### Why This Matters

The validation prevents:
- SQL injection via the `SET LOCAL` parameter (parameterized queries handle this, but defense-in-depth)
- Log injection via control characters in audit logs
- Header smuggling via embedded newlines
- Buffer-related issues via excessively long values

The validation does **not** prevent:
- Identity spoofing (any string that passes validation is accepted)
- Privilege escalation (if a user guesses another user's ID, they access that user's data)


## What Loom Does NOT Do

Loom explicitly does **not** perform the following security functions. These must be
handled by an upstream reverse proxy or API gateway.

| Function | Status | Who Must Provide |
|----------|--------|-----------------|
| Password authentication | Not implemented | Reverse proxy / identity provider |
| JWT verification | Not implemented | Reverse proxy / API gateway |
| OAuth 2.0 / OIDC | Not implemented | Reverse proxy / identity provider |
| mTLS client certificate validation | Available via TLS config, but does not map cert to user ID | Reverse proxy or custom interceptor |
| Session token management | Not implemented | Reverse proxy / application layer |
| Rate limiting per user | Not implemented | Reverse proxy / API gateway |
| IP allowlisting | Not implemented | Reverse proxy / firewall |
| CORS headers | Not implemented | HTTP gateway or reverse proxy |

### Direct Client Access Is Not Secure

If a client connects directly to the Loom gRPC server without a trusted proxy in front:

- Any client can claim to be any user by setting `x-user-id` to any value.
- There is no way for Loom to distinguish legitimate users from impersonators.
- All RLS isolation is bypassed from a security perspective (the data is isolated per user ID,
  but anyone can claim any user ID).

**This is not a bug. It is the expected deployment model.** Loom is a backend service that
sits behind an authenticating proxy.


## PostgreSQL Row-Level Security (RLS)

When using the PostgreSQL storage backend, Loom enforces per-user data isolation at the
database level using Row-Level Security.

### How RLS Works in Loom

1. All tenant-scoped tables have RLS enabled and forced (even for table owners):
   ```sql
   ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
   ALTER TABLE sessions FORCE ROW LEVEL SECURITY;
   ```

2. Each table has a policy that matches rows to the current user:
   ```sql
   CREATE POLICY sessions_user_isolation ON sessions
       USING (user_id = current_setting('app.current_user_id', true))
       WITH CHECK (user_id = current_setting('app.current_user_id', true));
   ```

3. Before every query, `execInTx()` sets the session variable:
   ```sql
   SET LOCAL app.current_user_id = $1;
   ```
   `SET LOCAL` is scoped to the current transaction. It cannot leak to other connections
   or other transactions on the same connection.

### Tables with RLS Policies

| Table | Policy Type | Isolation Key |
|-------|------------|---------------|
| `sessions` | Direct | `user_id` column |
| `messages` | Direct | `user_id` column |
| `artifacts` | Direct | `user_id` column |
| `agent_errors` | Direct | `user_id` column |
| `human_requests` | Direct | `user_id` column |
| `sql_result_metadata` | Direct | `user_id` column |
| `tool_executions` | Inherited | Via `session_id` -> `sessions.user_id` |
| `memory_snapshots` | Inherited | Via `session_id` -> `sessions.user_id` |

### RLS Guarantees

- **SELECT isolation**: User `alice` cannot read rows belonging to user `bob`.
- **INSERT isolation**: User `alice` cannot insert rows with `user_id = 'bob'`
  (the `WITH CHECK` clause prevents this).
- **UPDATE isolation**: User `alice` cannot modify rows belonging to user `bob`.
- **DELETE isolation**: User `alice` cannot delete rows belonging to user `bob`.
- **Transaction scoping**: `SET LOCAL` ensures user ID context does not leak between requests,
  even when connections are pooled.

### FORCE ROW LEVEL SECURITY

All tables use `FORCE ROW LEVEL SECURITY`, which means RLS policies apply even to the
table owner. This prevents accidental bypasses if the application connects as the table
owner. The admin store uses a role with the `BYPASSRLS` privilege to perform cross-tenant
queries.


## Admin Token Authentication

The `AdminService` gRPC service provides cross-tenant operations (list all sessions,
count sessions by user, system statistics). These operations bypass RLS.

### How Admin Auth Works

1. On startup, Loom reads the `LOOM_ADMIN_TOKEN` environment variable.
2. If set, every `AdminService` RPC requires the caller to provide a matching
   `x-admin-token` gRPC metadata header.
3. If `LOOM_ADMIN_TOKEN` is not set, admin endpoints are **unprotected**
   (a warning is logged at startup).

### Admin Token Validation

| Condition | Result | gRPC Error Code |
|-----------|--------|-----------------|
| `LOOM_ADMIN_TOKEN` not set | All admin requests allowed | (none -- no auth check) |
| `LOOM_ADMIN_TOKEN` set, `x-admin-token` header missing | Request rejected | `PERMISSION_DENIED` |
| `LOOM_ADMIN_TOKEN` set, token mismatch | Request rejected | `PERMISSION_DENIED` |
| `LOOM_ADMIN_TOKEN` set, token matches | Request allowed | (none -- success) |

### Admin Database Access

The admin store (`AdminStore`) uses `execInTxNoRLS` for all queries, which does not
call `SET LOCAL app.current_user_id`. This means admin queries are not filtered by RLS.

For full cross-tenant visibility, the admin database role should have the `BYPASSRLS`
privilege. At startup, Loom checks this with:

```sql
SELECT rolbypassrls FROM pg_roles WHERE rolname = current_user
```

If the role lacks `BYPASSRLS`, a warning is logged but the server starts normally.
RLS policies may still allow queries depending on configuration.

### Admin Token Security Considerations

- The admin token is a **static shared secret**. It does not expire or rotate automatically.
- It is passed in gRPC metadata (HTTP/2 headers), so it is protected by TLS in transit.
- Store the token securely. Do not commit it to version control.
- For stronger admin auth, put admin endpoints behind a separate proxy with additional
  authentication (e.g., mTLS, OAuth with admin scope).

### Source Code

- **Admin server**: `pkg/server/admin_server.go` -- `checkAdminAuth()`
- **Admin store**: `pkg/storage/postgres/admin_store.go` -- `ValidatePermissions()`, `execInTxNoRLS`
- **Server wiring**: `cmd/looms/cmd_serve.go` (search for `LOOM_ADMIN_TOKEN`)


## Deployment Requirements

### Minimum Secure Deployment

For multi-tenant deployments with per-user data isolation, the following are **required**:

1. **Reverse proxy or API gateway** that authenticates users and injects the `x-user-id`
   header with a verified identity.
2. **PostgreSQL storage backend** with RLS migrations applied.
3. **TLS** between the proxy and Loom (or mTLS for mutual authentication).
4. **Network isolation** so that clients cannot bypass the proxy and connect directly
   to the Loom gRPC port.

### Reverse Proxy Responsibilities

The proxy must:

| Responsibility | Details |
|---------------|---------|
| Authenticate the user | JWT, OAuth, mTLS, SAML, or any identity protocol |
| Set `x-user-id` header | With the verified user identity (e.g., email, sub claim, UUID) |
| Strip client-provided `x-user-id` | Prevent clients from spoofing identity by sending their own header |
| Forward to Loom gRPC | Over TLS or a trusted network |

### What Happens Without a Proxy

| Missing Component | Risk |
|-------------------|------|
| No reverse proxy | Any client can claim any user identity |
| Proxy does not strip `x-user-id` | Client can override the proxy's injected identity |
| Proxy does not authenticate | Unauthenticated users can access data under any user ID |
| No network isolation | Clients can bypass the proxy entirely |
| No TLS | User ID header transmitted in plaintext, vulnerable to interception |


## Recommended Deployment Patterns

### 1. Envoy Proxy with JWT Authentication

```
Client --> Envoy (JWT verify, inject x-user-id from sub claim) --> Loom gRPC
```

Envoy validates the JWT, extracts the `sub` claim, and sets it as the `x-user-id` header.
Envoy strips any client-provided `x-user-id` before forwarding.

### 2. Nginx with OAuth2 Proxy

```
Client --> OAuth2 Proxy (OIDC login) --> Nginx (set x-user-id from X-Auth-Request-Email) --> Loom gRPC
```

OAuth2 Proxy handles the OIDC flow. Nginx reads the authenticated identity and sets
the `x-user-id` header before proxying to Loom.

### 3. Cloud API Gateway (AWS, GCP, Azure)

```
Client --> API Gateway (JWT/API key auth, inject x-user-id) --> Loom gRPC (via VPC)
```

The cloud gateway authenticates the request and injects the identity header. Loom runs
in a private VPC/subnet that is not directly accessible from the internet.

### 4. mTLS with Client Certificate Mapping

```
Client (with client cert) --> Loom (mTLS, custom interceptor maps cert CN to x-user-id)
```

This requires a custom gRPC interceptor that extracts the Common Name (CN) or Subject
Alternative Name (SAN) from the client certificate and injects it as the user ID. Loom's
built-in mTLS support (see `docs/reference/tls.md`) handles TLS termination, but does not
automatically map certificates to user IDs.

### 5. Single-User Development (No Proxy)

```
Client --> Loom gRPC (require_user_id: false, default_user_id: "dev-user")
```

For local development or single-user testing, set `require_user_id: false` and
`default_user_id` to a known value. All requests use the same identity. This is
not suitable for multi-tenant deployments.


## SQLite Limitations

SQLite does **not** support Row-Level Security. When Loom runs with a SQLite backend:

- `require_user_id` is forced to `false` regardless of configuration.
- All requests use the fixed identity `"default-user"`.
- The `x-user-id` header is ignored.
- All data is accessible to all callers.
- There is no per-user data isolation.

SQLite mode is single-tenant only. For multi-tenant deployments, use the PostgreSQL backend.

See `docs/reference/sqlite-guidance.md` for details on SQLite limitations.


## Configuration Reference

### PostgreSQL Storage Configuration

```yaml
storage:
  backend: POSTGRES
  postgres:
    host: "localhost"
    port: 5432
    database: "loom"
    user: "loom_app"
    ssl_mode: "require"
    require_user_id: true        # Reject requests without x-user-id (default: true)
    default_user_id: ""          # Fallback user ID when require_user_id is false
```

### require_user_id

**Type**: `bool`
**Default**: `true`
**Proto field**: `PostgresStorageConfig.require_user_id` (field 13)

When `true`, requests without an `x-user-id` header receive a `codes.Unauthenticated`
gRPC error. When `false`, requests without the header use `default_user_id` or
`"default-user"` as the identity.

### default_user_id

**Type**: `string`
**Default**: `""` (falls back to `"default-user"`)
**Proto field**: `PostgresStorageConfig.default_user_id` (field 9)

Used when `require_user_id` is `false` and no `x-user-id` header is present.
If this field is also empty, the system uses `"default-user"`.

### LOOM_ADMIN_TOKEN

**Type**: Environment variable (`string`)
**Default**: Not set (admin endpoints unprotected)

When set, all `AdminService` RPCs require a matching `x-admin-token` gRPC metadata header.
When not set, a warning is logged and admin endpoints are accessible without authentication.

```bash
export LOOM_ADMIN_TOKEN="$(openssl rand -hex 32)"
```


## Threat Model

### Threats Mitigated by Loom

| Threat | Mitigation |
|--------|-----------|
| Cross-tenant data access at the database level | PostgreSQL RLS policies enforce per-user row isolation |
| SQL injection via user ID | Parameterized queries (`SET LOCAL app.current_user_id = $1`) |
| Log injection via control characters | User ID validation rejects bytes below `0x20` |
| User ID overflow / buffer issues | 256-character maximum length |
| RLS bypass by table owner | `FORCE ROW LEVEL SECURITY` on all tables |
| Connection pool user ID leakage | `SET LOCAL` is transaction-scoped, not connection-scoped |
| Admin access without token | `x-admin-token` check (when `LOOM_ADMIN_TOKEN` is set) |

### Threats NOT Mitigated by Loom

| Threat | Required Mitigation |
|--------|-------------------|
| User identity spoofing | Authenticating reverse proxy that strips and re-injects `x-user-id` |
| Direct client access bypassing proxy | Network-level isolation (firewall, VPC, private subnet) |
| Admin token theft | Secure secret management, TLS in transit, limited admin network access |
| Admin token expiration/rotation | External secret rotation; Loom reads `LOOM_ADMIN_TOKEN` at startup only |
| Denial of service | Rate limiting at the proxy or load balancer |
| Man-in-the-middle | TLS between all components (see `docs/reference/tls.md`) |
| Privilege escalation via user ID guessing | Use opaque, unguessable user IDs (UUIDs, not sequential integers) |


## See Also

- `docs/reference/tls.md` -- TLS/HTTPS configuration (mTLS, Let's Encrypt, self-signed)
- `docs/reference/sqlite-guidance.md` -- SQLite limitations including multi-tenancy
- `pkg/server/interceptors.go` -- User ID interceptor implementation
- `pkg/server/admin_server.go` -- Admin token authentication implementation
- `pkg/storage/postgres/tenant.go` -- RLS context propagation and transaction wrapping
- `pkg/storage/postgres/migrations/000007_user_rls_policies.up.sql` -- Current RLS policy definitions

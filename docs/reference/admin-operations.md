---
title: "Admin Operations Reference"
weight: 30
---

# Admin Operations Reference

Administrative gRPC service for cross-tenant visibility and system management in multi-tenant PostgreSQL deployments.

**Available since**: v1.1.0
**Backend requirement**: PostgreSQL only (SQLite backends do not expose AdminService)
**Transport**: gRPC only (not registered on the HTTP gateway)

---

## Table of Contents

- [Quick Reference](#quick-reference)
- [Admin Token Authentication](#admin-token-authentication)
  - [How It Works](#how-it-works)
  - [Token Configuration](#token-configuration)
  - [Security Warning: Empty Token](#security-warning-empty-token)
  - [Best Practices for Production](#best-practices-for-production)
  - [Environment Variable Exposure Risks](#environment-variable-exposure-risks)
  - [Token Rotation](#token-rotation)
- [Admin Endpoints](#admin-endpoints)
  - [ListAllSessions](#listallsessions)
  - [CountSessionsByUser](#countsessionsbyuser)
  - [GetSystemStats](#getsystemstats)
- [Error Codes](#error-codes)
- [Examples](#examples)
- [See Also](#see-also)

---

## Quick Reference

| Aspect | Detail |
|--------|--------|
| Service | `AdminService` (proto: `loom.v1.AdminService`) |
| Auth mechanism | Bearer token via `x-admin-token` gRPC metadata header |
| Token source | `LOOM_ADMIN_TOKEN` environment variable |
| Empty token behavior | All admin endpoints are **unprotected** (dev mode) |
| Backend | PostgreSQL only; SQLite backends silently skip registration |
| RLS bypass | All admin endpoints bypass Row-Level Security |
| HTTP gateway | Not available; gRPC only |

| Endpoint | HTTP annotation | Description |
|----------|----------------|-------------|
| `ListAllSessions` | `GET /v1/admin/sessions` | List sessions across all users |
| `CountSessionsByUser` | `GET /v1/admin/sessions:count-by-user` | Session counts grouped by user |
| `GetSystemStats` | `GET /v1/admin/stats` | Aggregate system statistics |

---

## Admin Token Authentication

### How It Works

The AdminService uses a static bearer token for access control. The token is passed by the client as gRPC metadata and compared server-side against the configured value.

**Authentication flow:**

1. Server reads `LOOM_ADMIN_TOKEN` environment variable at startup.
2. If the variable is non-empty, the token is stored in the `AdminServer` instance.
3. On each RPC call, `checkAdminAuth` extracts the `x-admin-token` value from incoming gRPC metadata.
4. If the token matches, the request proceeds. If it does not match, or the header is missing, the server returns `PERMISSION_DENIED`.

**Source**: `pkg/server/admin_server.go`, `checkAdminAuth` method.

### Token Configuration

**Environment variable**: `LOOM_ADMIN_TOKEN`

**Type**: `string`
**Default**: `""` (empty -- auth disabled)
**Required**: No (but strongly recommended for any non-local deployment)

The token is read once at server startup in `cmd/looms/cmd_serve.go` and passed to `NewAdminServer`. Changing the environment variable requires a server restart to take effect.

```bash
# Set the admin token before starting the server
export LOOM_ADMIN_TOKEN="$(openssl rand -hex 32)"
looms serve --config server.yaml
```

### Security Warning: Empty Token

If `LOOM_ADMIN_TOKEN` is not set or is set to an empty string, **all admin endpoints are accessible without any authentication**. The server logs a warning at startup:

```
WARN  LOOM_ADMIN_TOKEN not set; admin endpoints are unprotected
```

This behavior exists for local development convenience. It is not suitable for any deployment where the gRPC port is reachable by untrusted clients. Admin endpoints bypass RLS and expose data across all tenants.

### Best Practices for Production

#### Use a Secrets Manager

Do not store the admin token in plaintext configuration files, shell scripts, or CI/CD pipeline definitions. Use a secrets management system to inject the value at runtime.

**HashiCorp Vault**:
```bash
export LOOM_ADMIN_TOKEN="$(vault kv get -field=admin_token secret/loom)"
looms serve --config server.yaml
```

**Kubernetes Secrets**:
```yaml
apiVersion: v1
kind: Pod
spec:
  containers:
    - name: loom-server
      env:
        - name: LOOM_ADMIN_TOKEN
          valueFrom:
            secretKeyRef:
              name: loom-secrets
              key: admin-token
```

**AWS Secrets Manager**:
```bash
export LOOM_ADMIN_TOKEN="$(aws secretsmanager get-secret-value \
  --secret-id loom/admin-token \
  --query SecretString --output text)"
looms serve --config server.yaml
```

#### Token Length and Entropy

Use tokens with at least 32 characters of cryptographically random data. Short or predictable tokens are vulnerable to brute-force attacks.

**Minimum**: 32 characters
**Recommended**: 64 hex characters (256 bits of entropy)

```bash
# Generate a 64-character hex token (256 bits)
openssl rand -hex 32

# Alternative using /dev/urandom
head -c 32 /dev/urandom | base64 | tr -d '=/+'
```

#### Token Rotation

Rotate the admin token periodically (e.g., every 90 days) and immediately after any suspected compromise. Because the token is read at startup, rotation requires a server restart:

1. Generate a new token in your secrets manager.
2. Restart the Loom server (the new `LOOM_ADMIN_TOKEN` value is read on startup).
3. Update all admin clients to use the new token.
4. Revoke/delete the old token from your secrets manager.

There is no graceful token rotation mechanism (e.g., accepting both old and new tokens during a transition window). Plan for a brief period where admin clients must be updated.

#### Never Log the Token Value

The Loom server does not log the token value. Do not add logging that includes the `LOOM_ADMIN_TOKEN` value or the `x-admin-token` header contents. The server only logs:

- Whether the token is set or empty (at startup).
- Whether authentication succeeded or failed (per request, without echoing the submitted value).

If you wrap Loom in a reverse proxy or logging middleware, verify that request metadata (gRPC headers) is not logged in plaintext.

### Environment Variable Exposure Risks

Environment variables can be read by other processes on the same host:

- **Linux**: `/proc/<PID>/environ` is readable by the process owner (and root).
- **macOS/Linux**: `ps e` or `ps eww` may show environment variables in the process listing.
- **Container runtimes**: `docker inspect` exposes environment variables set via `-e` or `--env`.

Mitigations:

- Run the Loom server under a dedicated service account with restricted file permissions.
- In Kubernetes, use `secretKeyRef` (values are not visible in pod specs via `kubectl describe`).
- Avoid passing the token via command-line arguments (those are visible to all users via `ps`).
- Consider mounting secrets as files and reading them at startup if your threat model requires it (not currently supported by Loom; requires a wrapper script).

---

## Admin Endpoints

All admin endpoints bypass Row-Level Security (RLS). They are intended for platform operators, not end users. The admin database connection is validated at startup to check for appropriate privileges (e.g., `BYPASSRLS` in PostgreSQL). Validation failures are logged as warnings but do not block server startup.

### ListAllSessions

```proto
rpc ListAllSessions(ListAllSessionsRequest) returns (ListAllSessionsResponse)
```

**Description**: Lists sessions across all users, bypassing RLS tenant isolation.

**HTTP annotation**: `GET /v1/admin/sessions` (gRPC-gateway generated, but not registered on the HTTP mux)

**Request**:

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `limit` | `int32` | `50` | Maximum number of sessions to return. Values <= 0 default to 50. |
| `offset` | `int32` | `0` | Number of sessions to skip for pagination. |

**Response**:

| Field | Type | Description |
|-------|------|-------------|
| `sessions` | `repeated Session` | Session objects from all users |
| `total_count` | `int32` | Total number of sessions across all users |

**Errors**:

| gRPC Code | Condition |
|-----------|-----------|
| `PERMISSION_DENIED` | Missing or invalid `x-admin-token` header |
| `UNAVAILABLE` | Admin storage not configured |
| `INTERNAL` | Storage query failed |

### CountSessionsByUser

```proto
rpc CountSessionsByUser(CountSessionsByUserRequest) returns (CountSessionsByUserResponse)
```

**Description**: Returns session counts grouped by user ID. Useful for monitoring tenant activity distribution.

**HTTP annotation**: `GET /v1/admin/sessions:count-by-user` (gRPC-gateway generated, but not registered on the HTTP mux)

**Request**: No fields (empty message).

**Response**:

| Field | Type | Description |
|-------|------|-------------|
| `user_counts` | `map<string, int32>` | Map of user ID to session count |

**Errors**:

| gRPC Code | Condition |
|-----------|-----------|
| `PERMISSION_DENIED` | Missing or invalid `x-admin-token` header |
| `UNAVAILABLE` | Admin storage not configured |
| `INTERNAL` | Storage query failed |

### GetSystemStats

```proto
rpc GetSystemStats(GetSystemStatsRequest) returns (GetSystemStatsResponse)
```

**Description**: Returns aggregate statistics across all users and tenants.

**HTTP annotation**: `GET /v1/admin/stats` (gRPC-gateway generated, but not registered on the HTTP mux)

**Request**: No fields (empty message).

**Response**:

| Field | Type | Description |
|-------|------|-------------|
| `total_sessions` | `int32` | Total sessions across all users |
| `total_messages` | `int64` | Total messages across all users |
| `total_tool_executions` | `int64` | Total tool executions across all users |
| `total_users` | `int32` | Total distinct users |
| `total_cost_usd` | `double` | Total cost in USD across all users |
| `total_tokens` | `int64` | Total tokens consumed across all users |

**Errors**:

| gRPC Code | Condition |
|-----------|-----------|
| `PERMISSION_DENIED` | Missing or invalid `x-admin-token` header |
| `UNAVAILABLE` | Admin storage not configured |
| `INTERNAL` | Storage query failed |

---

## Error Codes

All admin endpoints share the same error model:

| gRPC Code | Meaning | When |
|-----------|---------|------|
| `PERMISSION_DENIED` | Authentication failed | Token not set, token missing from request, or token mismatch |
| `UNAVAILABLE` | Service not ready | `AdminStorage` is nil (storage backend does not support admin operations) |
| `INTERNAL` | Server error | Database query failed |

Error messages do not include the submitted token value. They indicate only the type of failure:

- `"missing metadata; admin token required"` -- no gRPC metadata present
- `"missing x-admin-token header"` -- metadata present but header absent
- `"invalid admin token"` -- header present but value does not match

---

## Examples

### Go gRPC Client

```go
package main

import (
	"context"
	"fmt"
	"log"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func main() {
	conn, err := grpc.NewClient("localhost:50051",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	client := loomv1.NewAdminServiceClient(conn)

	// Attach admin token via gRPC metadata
	ctx := metadata.AppendToOutgoingContext(context.Background(),
		"x-admin-token", "your-secret-token-here",
	)

	// List all sessions (paginated)
	sessions, err := client.ListAllSessions(ctx, &loomv1.ListAllSessionsRequest{
		Limit:  10,
		Offset: 0,
	})
	if err != nil {
		log.Fatalf("ListAllSessions failed: %v", err)
	}
	fmt.Printf("Total sessions: %d\n", sessions.TotalCount)
	for _, s := range sessions.Sessions {
		fmt.Printf("  Session %s (user: %s)\n", s.SessionId, s.UserId)
	}

	// Get system stats
	stats, err := client.GetSystemStats(ctx, &loomv1.GetSystemStatsRequest{})
	if err != nil {
		log.Fatalf("GetSystemStats failed: %v", err)
	}
	fmt.Printf("Users: %d, Sessions: %d, Messages: %d, Tokens: %d\n",
		stats.TotalUsers, stats.TotalSessions, stats.TotalMessages, stats.TotalTokens)
}
```

### grpcurl

```bash
# List all sessions
grpcurl -plaintext \
  -H "x-admin-token: your-secret-token-here" \
  -d '{"limit": 10, "offset": 0}' \
  localhost:50051 loom.v1.AdminService/ListAllSessions

# Count sessions by user
grpcurl -plaintext \
  -H "x-admin-token: your-secret-token-here" \
  localhost:50051 loom.v1.AdminService/CountSessionsByUser

# Get system stats
grpcurl -plaintext \
  -H "x-admin-token: your-secret-token-here" \
  localhost:50051 loom.v1.AdminService/GetSystemStats
```

### Token Validation Failure

```bash
# Missing token -- returns PERMISSION_DENIED
grpcurl -plaintext \
  localhost:50051 loom.v1.AdminService/GetSystemStats

# Output:
# ERROR:
#   Code: PermissionDenied
#   Message: missing metadata; admin token required
```

---

## See Also

- [TLS Reference](tls.md) -- Encrypting gRPC connections (prevents token interception in transit)
- [Streaming Reference](streaming.md) -- LoomService streaming RPCs
- Proto definition: `proto/loom/v1/loom.proto`, lines 584-601 (AdminService)
- Implementation: `pkg/server/admin_server.go`
- Server wiring: `cmd/looms/cmd_serve.go` (search for `LOOM_ADMIN_TOKEN`)

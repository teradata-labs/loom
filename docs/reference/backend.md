
# Backend Reference

Technical specification for Loom backend configuration.

**Version**: v1.2.0
**API Version**: loom/v1
**Configuration Kind**: `Backend`


## Table of Contents

- [Quick Reference](#quick-reference)
- [Backend YAML Schema](#backend-yaml-schema)
- [Backend Types](#backend-types)
  - [Postgres Backend](#postgres-backend)
  - [MySQL Backend](#mysql-backend)
  - [SQLite Backend](#sqlite-backend)
  - [File Backend](#file-backend)
  - [REST API Backend](#rest-api-backend)
  - [GraphQL Backend](#graphql-backend)
  - [gRPC Backend](#grpc-backend)
  - [MCP Backend](#mcp-backend)
  - [Supabase Backend](#supabase-backend)
- [Authentication](#authentication)
- [Common Configuration Fields](#common-configuration-fields)
- [Schema Discovery](#schema-discovery)
- [Tool Generation](#tool-generation)
- [Health Checks](#health-checks)
- [Connection Management](#connection-management)
- [Error Handling](#error-handling)
- [Examples](#examples)


## Quick Reference

| Backend Type | Status | Connection Config | Use Case |
|--------------|--------|-------------------|----------|
| **postgres** | ✅ Implemented | `database` | PostgreSQL databases |
| **mysql** | ✅ Implemented | `database` | MySQL databases |
| **sqlite** | ✅ Implemented | `database` | Local SQLite databases |
| **file** | ✅ Implemented | `database` (dsn = base directory) | Local file systems |
| **rest** | ✅ Implemented | `rest` | REST APIs |
| **graphql** | ⚠️ Config validated, factory pending | `graphql` | GraphQL APIs |
| **grpc** | ⚠️ Config validated, factory pending | `grpc` | gRPC services |
| **mcp** | ✅ Implemented | `mcp` | MCP servers |
| **supabase** | ✅ Implemented | `supabase` | Supabase hosted Postgres |

All 9 types are accepted by the YAML config parser and validator. The graphql and grpc types parse and validate correctly but do not yet have factory implementations to instantiate a running backend.


## Backend YAML Schema

All backends use the Kubernetes-style YAML format with environment variable expansion (`${VAR}`).

### Complete Schema

```yaml
apiVersion: loom/v1        # Required: Must be "loom/v1"
kind: Backend              # Required: Must be "Backend"

# Metadata
name: string               # Required: Backend identifier
description: string        # Optional: Human-readable description

# Backend type
type: string               # Required: postgres|mysql|sqlite|file|rest|graphql|grpc|mcp|supabase

# Type-specific connection configuration (exactly one required)
database:                  # For: postgres, mysql, sqlite, file
  dsn: string             # Required: Connection string (or base dir for file)
  max_connections: int    # Optional: Pool size (default: 10)
  max_idle_connections: int # Optional: Idle connection limit
  connection_timeout_seconds: int # Optional: Timeout (default: 30)
  enable_ssl: bool        # Optional: Enable SSL/TLS
  ssl_cert_path: string   # Optional: Path to SSL CA certificate

rest:                      # For: rest
  base_url: string        # Required: API base URL
  auth: AuthConfig        # Optional: See Authentication section
  headers: map            # Optional: Custom HTTP headers
  timeout_seconds: int    # Optional: Request timeout (default: 30)
  max_retries: int        # Optional: Retry attempts

graphql:                   # For: graphql
  endpoint: string        # Required: GraphQL endpoint URL
  auth: AuthConfig        # Optional: See Authentication section
  headers: map            # Optional: Custom HTTP headers
  timeout_seconds: int    # Optional: Request timeout (default: 30)

grpc:                      # For: grpc
  address: string         # Required: gRPC server address (host:port)
  use_tls: bool           # Optional: Enable TLS
  cert_path: string       # Optional: TLS certificate path
  metadata: map           # Optional: gRPC metadata key-value pairs
  timeout_seconds: int    # Optional: Request timeout (default: 30)

mcp:                       # For: mcp
  command: string         # Required for stdio transport
  args: []string          # Optional: Command arguments
  env: map                # Optional: Environment variables
  transport: string       # Optional: stdio|http|sse (default: stdio)
  url: string             # Required for http/sse transport
  working_dir: string     # Optional: Working directory for subprocess

supabase:                  # For: supabase
  project_ref: string     # Required: Supabase project reference
  api_key: string         # Optional: Supabase anon/public API key
  database_password: string # Required: Database password
  pooler_mode: string     # Optional: session|transaction
  enable_rls: bool        # Optional: Enable Row Level Security
  database: string        # Optional: Database name (default: postgres)
  max_pool_size: int      # Optional: Pool size (default: 10)
  region: string          # Required: Supabase region (e.g., us-east-1)
  pooler_host: string     # Optional: Custom pooler hostname

# Shared optional sections
schema_discovery:
  enabled: bool           # Enable schema discovery
  cache_ttl_seconds: int  # Schema cache TTL
  include_tables: []string # Table whitelist
  exclude_tables: []string # Table blacklist

tool_generation:
  enable_all: bool        # Generate tools for all resources
  tools: []string         # Explicit tool list

health_check:
  enabled: bool           # Enable health checks
  interval_seconds: int   # Check interval
  timeout_seconds: int    # Check timeout
  query: string           # Health check query (SQL backends)
```


## Backend Types

### Postgres Backend

PostgreSQL database backend using the pgx driver.

**Type**: `postgres`
**Status**: ✅ Implemented
**Driver**: `pgx` (pgx/v5/stdlib)

#### Required Fields

| Field | Type | Description |
|-------|------|-------------|
| `type` | `string` | Must be `"postgres"` |
| `database.dsn` | `string` | PostgreSQL connection string |

**DSN format**:
```
postgresql://[username[:password]@]host[:port]/database[?param=value]
```

**DSN parameters**:
- `sslmode` - `disable`, `require`, `verify-ca`, `verify-full`
- `connect_timeout` - Connection timeout in seconds
- `application_name` - Application identifier

#### Optional Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `database.max_connections` | `int` | `10` | Maximum concurrent connections |
| `database.max_idle_connections` | `int` | - | Idle connections retained |
| `database.connection_timeout_seconds` | `int` | `30` | Connection timeout |
| `database.enable_ssl` | `bool` | `false` | Enable SSL/TLS |
| `database.ssl_cert_path` | `string` | - | Path to SSL CA certificate |

#### Example

```yaml
apiVersion: loom/v1
kind: Backend

name: analytics-postgres
description: PostgreSQL analytics database
type: postgres

database:
  dsn: postgresql://analytics_user@db.example.com:5432/analytics?sslmode=require
  max_connections: 20
  max_idle_connections: 5
  connection_timeout_seconds: 30
  enable_ssl: true
  ssl_cert_path: /etc/loom/certs/postgres-ca.crt

schema_discovery:
  enabled: true
  cache_ttl_seconds: 3600
  include_tables:
    - users
    - orders
    - analytics_*
  exclude_tables:
    - temp_*
    - staging_*

tool_generation:
  enable_all: true

health_check:
  enabled: true
  interval_seconds: 60
  timeout_seconds: 5
  query: "SELECT 1"
```


### MySQL Backend

MySQL database backend using the go-sql-driver/mysql driver.

**Type**: `mysql`
**Status**: ✅ Implemented
**Driver**: `mysql` (go-sql-driver/mysql)

#### Required Fields

| Field | Type | Description |
|-------|------|-------------|
| `type` | `string` | Must be `"mysql"` |
| `database.dsn` | `string` | MySQL connection string |

**DSN format**:
```
user:password@tcp(host:port)/database
```

#### Optional Fields

Same as [Postgres optional fields](#optional-fields).

#### Example

```yaml
apiVersion: loom/v1
kind: Backend

name: mysql-backend
description: MySQL database backend
type: mysql

database:
  dsn: ${MYSQL_DSN}
  max_connections: 25
  max_idle_connections: 5
  connection_timeout_seconds: 30

schema_discovery:
  enabled: true
  cache_ttl_seconds: 3600

tool_generation:
  enable_all: true

health_check:
  enabled: true
  interval_seconds: 300
  timeout_seconds: 10
  query: "SELECT 1"
```


### SQLite Backend

Local SQLite database backend. Built with FTS5 support.

**Type**: `sqlite`
**Status**: ✅ Implemented
**Driver**: `sqlite3` (internal `sqlitedriver` package — uses go-sqlcipher with CGO, or modernc.org/sqlite without CGO; requires `-tags fts5` build tag)

#### Required Fields

| Field | Type | Description |
|-------|------|-------------|
| `type` | `string` | Must be `"sqlite"` |
| `database.dsn` | `string` | File path to SQLite database |

**DSN examples**:
```yaml
database:
  dsn: ./loom.db           # Relative path
  dsn: /var/lib/loom/data.db # Absolute path
  dsn: :memory:            # In-memory (testing only)
```

#### Example

```yaml
apiVersion: loom/v1
kind: Backend

name: sqlite-local
description: Local SQLite database for development
type: sqlite

database:
  dsn: ./examples/data/example.db

schema_discovery:
  enabled: true
  cache_ttl_seconds: 600

tool_generation:
  enable_all: true
```


### File Backend

Local file system backend. Uses the `database` config block, where `dsn` specifies the base directory.

**Type**: `file`
**Status**: ✅ Implemented

The base directory is created automatically if it does not exist.

#### Required Fields

| Field | Type | Description |
|-------|------|-------------|
| `type` | `string` | Must be `"file"` |
| `database.dsn` | `string` | Base directory path for file operations |

#### Example

```yaml
apiVersion: loom/v1
kind: Backend

name: file-backend
description: File system backend for reading and analyzing files
type: file

database:
  dsn: ./data
```


### REST API Backend

HTTP-based REST API backend with authentication support.

**Type**: `rest`
**Status**: ✅ Implemented

#### Required Fields

| Field | Type | Description |
|-------|------|-------------|
| `type` | `string` | Must be `"rest"` |
| `rest.base_url` | `string` | API base URL (`http://` or `https://`) |

#### Optional Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `rest.auth` | `AuthConfig` | - | See [Authentication](#authentication) |
| `rest.headers` | `map[string]string` | - | Custom HTTP headers for all requests |
| `rest.timeout_seconds` | `int` | `30` | Request timeout |
| `rest.max_retries` | `int` | - | Maximum retry attempts |

#### Example

```yaml
apiVersion: loom/v1
kind: Backend

name: github-api
description: GitHub REST API for repository management
type: rest

rest:
  base_url: https://api.github.com

  auth:
    type: bearer
    token: ${GITHUB_TOKEN}

  headers:
    Accept: application/vnd.github.v3+json
    User-Agent: loom-agent/1.0

  timeout_seconds: 30
  max_retries: 3

tool_generation:
  tools:
    - list_repositories
    - create_issue
    - get_pull_request

health_check:
  enabled: true
  interval_seconds: 300
  timeout_seconds: 10
```


### GraphQL Backend

GraphQL API backend with authentication support.

**Type**: `graphql`
**Status**: ⚠️ Config validated, factory pending (the YAML config is parsed and validated, but `NewBackend` in the factory does not yet instantiate a GraphQL backend)

#### Required Fields

| Field | Type | Description |
|-------|------|-------------|
| `type` | `string` | Must be `"graphql"` |
| `graphql.endpoint` | `string` | GraphQL endpoint URL |

#### Optional Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `graphql.auth` | `AuthConfig` | - | See [Authentication](#authentication) |
| `graphql.headers` | `map[string]string` | - | Custom HTTP headers |
| `graphql.timeout_seconds` | `int` | `30` | Request timeout |

#### Example

```yaml
apiVersion: loom/v1
kind: Backend

name: graphql-backend
description: GraphQL API backend with authentication
type: graphql

graphql:
  endpoint: ${GRAPHQL_ENDPOINT}

  auth:
    type: bearer
    token: ${GRAPHQL_TOKEN}
    header_name: Authorization

  headers:
    Content-Type: application/json
    Accept: application/json

  timeout_seconds: 30

schema_discovery:
  enabled: true
  cache_ttl_seconds: 3600
```


### gRPC Backend

gRPC service backend with TLS and metadata support.

**Type**: `grpc`
**Status**: ⚠️ Config validated, factory pending (the YAML config is parsed and validated, but `NewBackend` in the factory does not yet instantiate a gRPC backend)

#### Required Fields

| Field | Type | Description |
|-------|------|-------------|
| `type` | `string` | Must be `"grpc"` |
| `grpc.address` | `string` | gRPC server address (`host:port`) |

#### Optional Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `grpc.use_tls` | `bool` | `false` | Enable TLS |
| `grpc.cert_path` | `string` | - | TLS certificate path (resolved relative to YAML file) |
| `grpc.metadata` | `map[string]string` | - | gRPC metadata key-value pairs |
| `grpc.timeout_seconds` | `int` | `30` | Request timeout |

#### Example

```yaml
apiVersion: loom/v1
kind: Backend

name: grpc-backend
description: gRPC service backend
type: grpc

grpc:
  address: grpc.example.com:443
  use_tls: true
  cert_path: /etc/loom/certs/grpc-ca.crt
  metadata:
    x-api-key: ${GRPC_API_KEY}
  timeout_seconds: 30
```


### MCP Backend

Model Context Protocol backend for dynamic tool and resource discovery. Connects to external MCP servers that expose tools via the MCP specification.

**Type**: `mcp`
**Status**: ✅ Implemented
**Transports**: `stdio` (default), `http`, `sse`

#### Required Fields

| Field | Type | Description |
|-------|------|-------------|
| `type` | `string` | Must be `"mcp"` |
| `mcp.command` | `string` | MCP server executable (required when transport is `stdio` or omitted) |
| `mcp.url` | `string` | MCP server URL (required when transport is `http` or `sse`) |

**Transport rules**:
- When `transport` is omitted or `"stdio"`: `command` is required, `url` is ignored
- When `transport` is `"http"` or `"sse"`: `url` is required, `command` is not needed

#### Optional Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `mcp.transport` | `string` | `stdio` | Transport: `stdio`, `http`, or `sse` |
| `mcp.args` | `[]string` | - | Command-line arguments for the MCP server |
| `mcp.env` | `map[string]string` | - | Environment variables passed to MCP server process |
| `mcp.working_dir` | `string` | - | Working directory for the subprocess |
| `mcp.url` | `string` | - | URL for http/sse transport |

#### stdio Example

```yaml
apiVersion: loom/v1
kind: Backend

name: vantage-mcp-backend
description: Teradata Vantage via MCP server
type: mcp

mcp:
  command: vantage-mcp
  transport: stdio
  env: {}
```

#### http/sse Example

```yaml
apiVersion: loom/v1
kind: Backend

name: mcp-http-backend
description: Remote MCP server via HTTP
type: mcp

mcp:
  transport: http
  url: http://localhost:8080/mcp
```

#### Python MCP Server Example

```yaml
apiVersion: loom/v1
kind: Backend

name: python-data-processor
description: Python MCP server for data processing
type: mcp

mcp:
  command: python3
  transport: stdio
  args:
    - -m
    - my_mcp_server
    - --config
    - /etc/mcp/config.json
  env:
    DATA_PATH: /var/data
    LOG_LEVEL: info
  working_dir: /opt/mcp
```


### Supabase Backend

Supabase hosted Postgres backend with native pgxpool, connection pooling via Supavisor, and Row Level Security (RLS) support.

**Type**: `supabase`
**Status**: ✅ Implemented

#### Required Fields

| Field | Type | Description |
|-------|------|-------------|
| `type` | `string` | Must be `"supabase"` |
| `supabase.project_ref` | `string` | Supabase project reference (from dashboard) |
| `supabase.database_password` | `string` | Database password |
| `supabase.region` | `string` | Supabase region (e.g., `us-east-1`) |

#### Optional Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `supabase.api_key` | `string` | - | Supabase anon/public API key (for RLS context) |
| `supabase.pooler_mode` | `string` | unspecified | `session` or `transaction` |
| `supabase.enable_rls` | `bool` | `false` | Enable Row Level Security context injection |
| `supabase.database` | `string` | `postgres` | Database name |
| `supabase.max_pool_size` | `int` | `10` | Maximum connection pool size |
| `supabase.pooler_host` | `string` | - | Custom pooler hostname (defaults to `aws-0-{region}.pooler.supabase.com`) |

#### Example

```yaml
apiVersion: loom/v1
kind: Backend

name: supabase-backend
description: Supabase database backend with RLS and connection pooling
type: supabase

supabase:
  project_ref: ${SUPABASE_PROJECT_REF}
  api_key: ${SUPABASE_ANON_KEY}
  database_password: ${SUPABASE_DB_PASSWORD}
  pooler_mode: transaction
  enable_rls: true
  database: postgres
  max_pool_size: 15
  region: us-east-1

schema_discovery:
  enabled: true
  cache_ttl_seconds: 1800
  exclude_tables:
    - _migration_*

health_check:
  enabled: true
  interval_seconds: 60
  timeout_seconds: 5
  query: "SELECT 1"
```


## Authentication

The `AuthConfig` block is used by `rest` and `graphql` backends.

### Auth Config Fields

| Field | Type | Description |
|-------|------|-------------|
| `type` | `string` | Required. One of: `bearer`, `basic`, `apikey`, `oauth2` |
| `token` | `string` | Required for `bearer` and `apikey` types |
| `username` | `string` | Required for `basic` type |
| `password` | `string` | Required for `basic` type |
| `header_name` | `string` | Optional. Custom header name for `apikey` type |

### Auth Type Examples

**Bearer token**:
```yaml
auth:
  type: bearer
  token: ${API_TOKEN}
```

**Basic auth**:
```yaml
auth:
  type: basic
  username: admin
  password: ${API_PASSWORD}
```

**API key**:
```yaml
auth:
  type: apikey
  token: ${API_KEY}
  header_name: X-API-Key
```

**OAuth2**:
```yaml
auth:
  type: oauth2
  token: ${OAUTH_TOKEN}
```


## Common Configuration Fields

These fields apply to all backend types.

### apiVersion

**Type**: `string`
**Required**: Yes
**Value**: `loom/v1`

Must be exactly `"loom/v1"`. Validation fails for any other value.


### kind

**Type**: `string`
**Required**: Yes
**Value**: `Backend`

Must be exactly `"Backend"`.


### name

**Type**: `string`
**Required**: Yes

Unique backend identifier. Used to reference the backend in agent configurations.


### description

**Type**: `string`
**Required**: No

Human-readable description of the backend's purpose.


### type

**Type**: `string`
**Required**: Yes
**Allowed values**: `postgres` | `mysql` | `sqlite` | `file` | `rest` | `graphql` | `grpc` | `mcp` | `supabase`

Backend implementation type. Determines which connection config block is required.


## Schema Discovery

Automatic discovery of backend schema (tables, columns, types).

### Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `schema_discovery.enabled` | `bool` | - | Enable schema discovery |
| `schema_discovery.cache_ttl_seconds` | `int` | - | Schema cache time-to-live |
| `schema_discovery.include_tables` | `[]string` | - | Table whitelist (glob patterns) |
| `schema_discovery.exclude_tables` | `[]string` | - | Table blacklist (glob patterns) |

### Example

```yaml
schema_discovery:
  enabled: true
  cache_ttl_seconds: 3600
  include_tables:
    - users
    - orders
    - analytics_*
  exclude_tables:
    - temp_*
    - staging_*
```

`exclude_tables` takes priority over `include_tables`.


## Tool Generation

Automatic tool generation from backend resources.

### Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `tool_generation.enable_all` | `bool` | `false` | Generate tools for all discovered resources |
| `tool_generation.tools` | `[]string` | - | Explicit list of tools to generate |

### Example

```yaml
tool_generation:
  enable_all: false
  tools:
    - query_users
    - query_orders
    - list_repositories
```

MCP backends discover tools automatically via the MCP `tools/list` endpoint, so `tool_generation` is typically not needed for MCP.


## Health Checks

Periodic backend health verification.

### Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `health_check.enabled` | `bool` | - | Enable periodic health checks |
| `health_check.interval_seconds` | `int` | - | Interval between checks |
| `health_check.timeout_seconds` | `int` | - | Check timeout |
| `health_check.query` | `string` | - | Health check query (SQL backends) |

### Example

```yaml
health_check:
  enabled: true
  interval_seconds: 60
  timeout_seconds: 5
  query: "SELECT 1"
```


## Connection Management

### Connection Pooling (SQL Backends)

SQL backends (postgres, mysql, sqlite) use Go's `database/sql` connection pooling.

**Configuration**:
```yaml
database:
  max_connections: 20
  max_idle_connections: 5
  connection_timeout_seconds: 30
```

**Defaults applied by Loom**:
- `max_connections`: `10` (if not set or set to 0)
- `connection_timeout_seconds`: `30` (if not set or set to 0)

**Behavior**:
- Connections created on-demand up to `max_connections`
- Initial `Ping` verifies connectivity on backend load
- If `Ping` fails, the database connection is closed and an error is returned
- Driver is inferred from `type`: postgres uses `pgx`, mysql uses `mysql`, sqlite uses `sqlite3`

### Timeout Defaults

The following defaults are applied when timeout is not set or is 0:

| Backend | Default Timeout |
|---------|----------------|
| rest | 30s |
| graphql | 30s |
| grpc | 30s |
| database | 30s (connection timeout) |

### Environment Variable Expansion

All YAML string values support environment variable expansion using `${VAR}` syntax. The expansion uses Go's `os.Expand` with `os.Getenv`, which means only `${VAR}` and `$VAR` forms are supported. Shell-style default values (`${VAR:-default}`) are **not** supported — the entire string including `:-default` is treated as the variable name, which will resolve to an empty string.

```yaml
database:
  dsn: ${POSTGRES_URL}

rest:
  auth:
    token: ${GITHUB_TOKEN}

supabase:
  project_ref: ${SUPABASE_PROJECT_REF}
  database_password: ${SUPABASE_DB_PASSWORD}
```

### File Path Resolution

Relative file paths in `database.ssl_cert_path` and `grpc.cert_path` are resolved relative to the directory containing the backend YAML file.


## Error Handling

### Validation Errors

These errors occur when loading a backend YAML file.

| Error | Cause |
|-------|-------|
| `apiVersion is required` | Missing `apiVersion` field |
| `unsupported apiVersion: X (expected: loom/v1)` | Wrong API version |
| `kind must be 'Backend', got: X` | Wrong `kind` value |
| `name is required` | Missing `name` field |
| `type is required` | Missing `type` field |
| `invalid backend type: X (must be: postgres, mysql, sqlite, file, rest, graphql, grpc, mcp, supabase)` | Type not one of the 9 valid types |
| `database connection config is required for type: X` | Missing `database` block for SQL type (postgres, mysql, sqlite) |
| `database connection config is required for file backend (use dsn to specify base directory)` | Missing `database` block for file type |
| `database.dsn is required` | Missing DSN in database config (SQL types) |
| `database.dsn is required (base directory path for file backend)` | Missing DSN in database config (file type) |
| `rest connection config is required for type: rest` | Missing `rest` block |
| `rest.base_url is required` | Missing base URL in rest config |
| `graphql connection config is required for type: graphql` | Missing `graphql` block |
| `graphql.endpoint is required` | Missing endpoint in graphql config |
| `grpc connection config is required for type: grpc` | Missing `grpc` block |
| `grpc.address is required` | Missing address in grpc config |
| `mcp connection config is required for type: mcp` | Missing `mcp` block |
| `mcp.transport must be 'stdio', 'http', or 'sse'` | Invalid transport value |
| `mcp.command is required for stdio transport` | Missing command when using stdio |
| `mcp.url is required for http/sse transport` | Missing URL when using http or sse |
| `supabase connection config is required for type: supabase` | Missing `supabase` block |
| `supabase.project_ref is required` | Missing project reference |
| `supabase.database_password is required` | Missing database password |
| `supabase.region is required` | Missing region |

### Auth Validation Errors

| Error | Cause |
|-------|-------|
| `type is required` | Missing `auth.type` |
| `invalid auth type: X (must be: bearer, basic, apikey, oauth2)` | Invalid auth type |
| `token is required for auth type: bearer` | Missing token for bearer auth |
| `token is required for auth type: apikey` | Missing token for apikey auth |
| `username and password are required for basic auth` | Missing credentials for basic auth |

### Connection Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| `connection refused` | Backend service not running | Start service, verify host/port |
| `authentication failed` | Invalid credentials | Check username/password in DSN |
| `timeout` | Connection took too long | Increase `connection_timeout_seconds` |
| `too many connections` | Pool exhausted | Increase `max_connections` |
| `ssl required` | Server requires SSL | Set `enable_ssl: true` |


## Examples

### Multi-Backend Project

Project spec referencing multiple backends (backends are configured at the project level, not per-agent):

```yaml
apiVersion: loom/v1
kind: Project
metadata:
  name: multi-backend-project
spec:
  backends:
    - config_file: ./backends/postgres.yaml
    - config_file: ./backends/github-api.yaml
    - config_file: ./backends/vantage-mcp.yaml
  agents:
    - config_file: ./agents/sql-expert.yaml
```

### Development PostgreSQL

```yaml
apiVersion: loom/v1
kind: Backend

name: postgres-dev
description: Development PostgreSQL database
type: postgres

database:
  dsn: postgresql://localhost:5432/loom_dev?sslmode=disable
  max_connections: 10
  connection_timeout_seconds: 5

schema_discovery:
  enabled: true
  cache_ttl_seconds: 600

tool_generation:
  enable_all: true

health_check:
  enabled: true
  interval_seconds: 60
  timeout_seconds: 5
  query: "SELECT 1"
```

### REST API with API Key Auth

```yaml
apiVersion: loom/v1
kind: Backend

name: internal-api
description: Internal service API
type: rest

rest:
  base_url: https://internal.example.com/v1
  auth:
    type: apikey
    token: ${INTERNAL_API_KEY}
    header_name: X-API-Key
  headers:
    Accept: application/json
  timeout_seconds: 15
  max_retries: 3
```


## ExecutionBackend Interface

All backends implement the `fabric.ExecutionBackend` interface defined in `pkg/fabric/interface.go`:

```go
type ExecutionBackend interface {
    Name() string
    ExecuteQuery(ctx context.Context, query string) (*QueryResult, error)
    GetSchema(ctx context.Context, resource string) (*Schema, error)
    ListResources(ctx context.Context, filters map[string]string) ([]Resource, error)
    GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error)
    Ping(ctx context.Context) error
    Capabilities() *Capabilities
    ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error)
    Close() error
}
```

The `Capabilities()` method returns runtime capability information (supports transactions, concurrency, streaming, etc.). This is a runtime interface method, not a YAML configuration field.


## See Also

- [CLI Reference](./cli.md) - Backend management commands
- [MCP Apps Guide](../guides/mcp-apps-guide.md) - MCP setup and usage guide
- [MCP Integration Overview](../guides/integration/mcp-readme.md) - MCP integration documentation
- [LLM Providers](./llm-providers.md) - LLM provider configuration

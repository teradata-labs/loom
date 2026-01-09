---
title: "Backend Reference"
weight: 20
---

# Backend Reference

Complete technical specification for Loom backend configuration and capabilities.

**Version**: v1.0.0-beta.2
**API Version**: loom/v1
**Configuration Kind**: `Backend`

---

## Table of Contents

- [Quick Reference](#quick-reference)
- [Backend YAML Schema](#backend-yaml-schema)
- [Backend Types](#backend-types)
  - [Postgres Backend](#postgres-backend)
  - [SQLite Backend](#sqlite-backend)
  - [REST API Backend](#rest-api-backend)
  - [MCP Backend](#mcp-backend)
  - [File Backend](#file-backend)
  - [MySQL Backend](#mysql-backend-planned)
  - [MongoDB Backend](#mongodb-backend-planned)
  - [GraphQL Backend](#graphql-backend-planned)
- [Common Configuration Fields](#common-configuration-fields)
- [Capabilities](#capabilities)
- [Connection Management](#connection-management)
- [Schema Discovery](#schema-discovery)
- [Tool Generation](#tool-generation)
- [Health Checks](#health-checks)
- [Error Handling](#error-handling)
- [Examples](#examples)

---

## Quick Reference

| Backend Type | Status | Capabilities | Use Case |
|--------------|--------|--------------|----------|
| **postgres** | ✅ Implemented | Query, schema, tools | PostgreSQL databases |
| **sqlite** | ✅ Implemented | Query, schema, tools | Local SQLite databases |
| **rest** | ✅ Implemented | API calls, auth | REST APIs |
| **mcp** | ✅ Implemented | Dynamic tools, resources | MCP servers (Teradata, GitHub, etc.) |
| **file** | ✅ Implemented | File read, directory listing | Local file systems |
| **mysql** | 📋 Planned | Query, schema, tools | MySQL databases |
| **mongodb** | 📋 Planned | Document queries | MongoDB databases |
| **graphql** | 📋 Planned | GraphQL queries | GraphQL APIs |

---

## Backend YAML Schema

All backends use the Kubernetes-style YAML format.

### Complete Schema

```yaml
apiVersion: loom/v1        # Required: API version
kind: Backend              # Required: Resource type

# Metadata section
name: string               # Required: Backend identifier
description: string        # Optional: Human-readable description

# Backend type
type: string               # Required: postgres|sqlite|rest|mcp|file|mysql|mongodb|graphql

# Type-specific configuration
database:                  # For SQL backends (postgres, sqlite, mysql)
  dsn: string             # Required: Connection string
  driver: string          # Optional: Driver name
  max_connections: int    # Optional: Connection pool size
  max_idle_connections: int # Optional: Idle connection limit
  connection_timeout_seconds: int # Optional: Connection timeout
  enable_ssl: bool        # Optional: Enable SSL/TLS
  ssl_cert_path: string   # Optional: SSL certificate path

rest:                      # For REST API backend
  base_url: string        # Required: API base URL
  auth:                   # Optional: Authentication
    type: string          # bearer|basic|api_key
    token: string         # For bearer auth
    username: string      # For basic auth
    password: string      # For basic auth
    key: string           # For api_key auth
    header: string        # For api_key auth (default: X-API-Key)
  headers: map[string]string # Optional: Custom headers
  timeout_seconds: int    # Optional: Request timeout
  max_retries: int        # Optional: Retry attempts

mcp:                       # For MCP backend
  command: string         # Required: MCP server executable
  transport: string       # Required: stdio|http|sse
  env: map[string]string  # Optional: Environment variables
  args: []string          # Optional: Command arguments

# Capabilities configuration
capabilities:
  query_execution: bool   # SQL query execution
  schema_discovery: bool  # Schema introspection
  metadata: bool          # Metadata access
  resource_listing: bool  # Resource enumeration
  custom_operations: bool # Backend-specific operations

# Schema discovery configuration
schema_discovery:
  enabled: bool           # Enable schema discovery
  cache_ttl_seconds: int  # Schema cache TTL
  include_tables: []string # Table whitelist (glob patterns)
  exclude_tables: []string # Table blacklist (glob patterns)

# Tool generation configuration
tool_generation:
  enable_all: bool        # Generate tools for all tables
  tools: []string         # Explicit tool list

# Health check configuration
health_check:
  enabled: bool           # Enable health checks
  interval_seconds: int   # Check interval
  timeout_seconds: int    # Check timeout
  query: string           # Health check query (SQL backends)
```

---

## Backend Types

### Postgres Backend

PostgreSQL database backend with full SQL capabilities.

**Type**: `postgres`
**Status**: ✅ Implemented

#### Required Fields

##### type

**Value**: `"postgres"`

---

##### database.dsn

**Type**: `string`
**Required**: Yes
**Format**: PostgreSQL connection string

**Format**:
```
postgresql://[username[:password]@]host[:port]/database[?param=value]
```

**Parameters**:
- `sslmode` - SSL mode: `disable`, `require`, `verify-ca`, `verify-full`
- `connect_timeout` - Connection timeout in seconds
- `application_name` - Application identifier

**Examples**:
```yaml
database:
  dsn: postgresql://localhost:5432/loom_dev?sslmode=disable
  dsn: postgresql://user:pass@prod.example.com:5432/analytics?sslmode=require
  dsn: ${POSTGRES_URL}  # Environment variable expansion
```

---

#### Optional Fields

##### database.max_connections

**Type**: `int`
**Default**: `10`
**Range**: `1` - `1000`

Maximum concurrent connections in pool.

**Recommendation**:
- Development: `5-10`
- Production (single agent): `20-50`
- Production (multi-agent): `10` per agent

---

##### database.max_idle_connections

**Type**: `int`
**Default**: `2`
**Range**: `1` - `max_connections`

Maximum idle connections retained in pool.

**Recommendation**: `max_connections / 4`

---

##### database.connection_timeout_seconds

**Type**: `int`
**Default**: `30`
**Range**: `1` - `300`

Timeout for establishing new connections.

---

##### database.enable_ssl

**Type**: `bool`
**Default**: `false`

Enable SSL/TLS encryption for database connection.

**Production**: Always set to `true` for production databases.

---

##### database.ssl_cert_path

**Type**: `string`
**Required when**: `enable_ssl: true` and using `verify-ca` or `verify-full`

Path to SSL CA certificate for server verification.

---

#### Complete Postgres Example

```yaml
apiVersion: loom/v1
kind: Backend

name: analytics-postgres
description: Production PostgreSQL analytics database
type: postgres

database:
  dsn: postgresql://analytics_user@prod-db.example.com:5432/analytics?sslmode=require
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
    - products
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

---

### SQLite Backend

Local SQLite database backend for development and testing.

**Type**: `sqlite`
**Status**: ✅ Implemented

#### Required Fields

##### type

**Value**: `"sqlite"`

---

##### database.dsn

**Type**: `string`
**Required**: Yes
**Format**: File path to SQLite database

**Examples**:
```yaml
database:
  dsn: ./loom.db           # Relative path
  dsn: /var/lib/loom/data.db # Absolute path
  dsn: :memory:            # In-memory database (testing only)
```

---

##### database.driver

**Type**: `string`
**Default**: `sqlite3`
**Value**: `"sqlite3"`

SQLite driver name (always `sqlite3`).

---

#### Capabilities

SQLite backend supports:
- ✅ Query execution
- ✅ Schema discovery
- ✅ Metadata access
- ✅ Resource listing
- ❌ Custom operations

---

#### Complete SQLite Example

```yaml
apiVersion: loom/v1
kind: Backend

name: sqlite-backend
description: Local SQLite database for development
type: sqlite

database:
  dsn: ./examples/data/example.db
  driver: sqlite3

capabilities:
  query_execution: true
  schema_discovery: true
  metadata: true
  resource_listing: true
  custom_operations: false
```

---

### REST API Backend

REST API backend for HTTP-based services.

**Type**: `rest`
**Status**: ✅ Implemented

#### Required Fields

##### type

**Value**: `"rest"`

---

##### rest.base_url

**Type**: `string`
**Required**: Yes
**Format**: `http://` or `https://` URL

Base URL for all API requests.

**Examples**:
```yaml
rest:
  base_url: https://api.github.com
  base_url: https://my-api.example.com/v1
  base_url: http://localhost:8080
```

---

#### Optional Fields

##### rest.auth

Authentication configuration.

**Type**: `object`
**Required**: No

**Fields**:
- `type` (string): `bearer` | `basic` | `api_key`
- `token` (string): For `bearer` auth
- `username` (string): For `basic` auth
- `password` (string): For `basic` auth
- `key` (string): For `api_key` auth
- `header` (string): Header name for `api_key` (default: `X-API-Key`)

**Examples**:

Bearer token:
```yaml
rest:
  auth:
    type: bearer
    token: ${GITHUB_TOKEN}
```

Basic auth:
```yaml
rest:
  auth:
    type: basic
    username: admin
    password: "{{keyring:api_password}}"
```

API key:
```yaml
rest:
  auth:
    type: api_key
    key: ${API_KEY}
    header: X-API-Key
```

---

##### rest.headers

**Type**: `map[string]string`
**Required**: No

Custom HTTP headers for all requests.

**Example**:
```yaml
rest:
  headers:
    Accept: application/vnd.github.v3+json
    User-Agent: loom-agent/1.0
    X-Custom-Header: value
```

---

##### rest.timeout_seconds

**Type**: `int`
**Default**: `30`
**Range**: `1` - `300`

Request timeout in seconds.

---

##### rest.max_retries

**Type**: `int`
**Default**: `3`
**Range**: `0` - `10`

Maximum retry attempts for failed requests.

**Retry conditions**: 5xx errors, connection timeouts, network errors
**Backoff**: Exponential with jitter

---

#### Complete REST API Example

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
    - create_pull_request
    - list_issues
    - get_user
    - search_code

health_check:
  enabled: true
  interval_seconds: 300
  timeout_seconds: 10
```

---

### MCP Backend

Model Context Protocol backend for dynamic tool discovery.

**Type**: `mcp`
**Status**: ✅ Implemented
**Protocol Version**: MCP 1.0
**Transports**: `stdio`, `http`, `sse`

MCP backends connect to external MCP servers (Teradata Vantage, GitHub, file systems, etc.) that expose tools and resources dynamically.

#### Required Fields

##### type

**Value**: `"mcp"`

---

##### mcp.command

**Type**: `string`
**Required**: Yes

Path to MCP server executable.

**Examples**:
```yaml
mcp:
  command: vantage-mcp            # From PATH
  command: ~/bin/vantage-mcp      # Absolute path
  command: ./vantage-mcp          # Relative path
  command: python3                # With args (see mcp.args)
```

---

##### mcp.transport

**Type**: `string`
**Required**: Yes
**Allowed values**: `stdio` | `http` | `sse`

Transport mechanism for MCP communication.

**stdio**: JSON-RPC over stdin/stdout (most common)
**http**: HTTP-based JSON-RPC
**sse**: Server-Sent Events

---

#### Optional Fields

##### mcp.env

**Type**: `map[string]string`
**Required**: No

Environment variables passed to MCP server process.

**Environment variable expansion**:
- `${VAR}` - Shell environment variable
- `{{keyring:key_name}}` - Keyring-stored secret

**Example**:
```yaml
mcp:
  env:
    TD_USER: ${TD_USER}
    TD_DEFAULT_HOST: prod.teradata.com
    TD_PASSWORD: "{{keyring:td_password}}"
    LOG_LEVEL: debug
```

---

##### mcp.args

**Type**: `[]string`
**Required**: No

Command-line arguments for MCP server.

**Example**:
```yaml
mcp:
  command: python3
  args:
    - -m
    - my_mcp_server
    - --config
    - /etc/mcp/config.json
```

---

#### Capabilities

MCP backends support dynamic capabilities based on MCP server:
- ✅ Query execution (if server provides tools)
- ✅ Schema discovery (via resources)
- ✅ Metadata access (via resources)
- ✅ Resource listing (MCP resources/list)
- ✅ Custom operations (MCP server-specific)

**Tool discovery**: Automatic via MCP `tools/list` endpoint
**Resource discovery**: Automatic via MCP `resources/list` endpoint

---

#### MCP Backend Examples

**Teradata Vantage via MCP**:

```yaml
apiVersion: loom/v1
kind: Backend

name: vantage-mcp-backend
description: Teradata Vantage via MCP
type: mcp

mcp:
  command: vantage-mcp
  transport: stdio
  env: {}  # Credentials from looms config

capabilities:
  query_execution: true
  schema_discovery: true
  metadata: true
  resource_listing: true
  custom_operations: true
```

**Python MCP Server**:

```yaml
apiVersion: loom/v1
kind: Backend

name: python-mcp
description: Python MCP server for data processing
type: mcp

mcp:
  command: python3
  transport: stdio
  args:
    - -m
    - my_data_server
  env:
    DATA_PATH: /var/data
    LOG_LEVEL: info

capabilities:
  query_execution: false
  schema_discovery: true
  metadata: true
  resource_listing: true
  custom_operations: true
```

**HTTP MCP Server**:

```yaml
apiVersion: loom/v1
kind: Backend

name: http-mcp
description: HTTP-based MCP server
type: mcp

mcp:
  command: mcp-http-client
  transport: http
  args:
    - --url
    - https://mcp.example.com
  env:
    API_KEY: ${MCP_API_KEY}

capabilities:
  query_execution: true
  schema_discovery: true
  metadata: true
  resource_listing: true
  custom_operations: true
```

---

### File Backend

Local file system backend for file operations.

**Type**: `file`
**Status**: ✅ Implemented

#### Required Fields

##### type

**Value**: `"file"`

---

##### database.dsn

**Type**: `string`
**Required**: Yes
**Format**: Directory path (root directory for file operations)

**Examples**:
```yaml
database:
  dsn: ./data              # Relative path
  dsn: /var/lib/loom/files # Absolute path
  dsn: ~/Documents         # Home directory expansion
```

---

#### Capabilities

File backend supports:
- ❌ Query execution (no SQL)
- ✅ Schema discovery (directory listing)
- ✅ Metadata access (file stats)
- ✅ Resource listing (file enumeration)
- ❌ Custom operations

---

#### Complete File Backend Example

```yaml
apiVersion: loom/v1
kind: Backend

name: file-backend
description: Local file system access
type: file

database:
  dsn: ./data

capabilities:
  query_execution: false
  schema_discovery: true
  metadata: true
  resource_listing: true
  custom_operations: false
```

---

### MySQL Backend (Planned)

MySQL database backend.

**Type**: `mysql`
**Status**: 📋 Planned for v1.0
**Current Workaround**: None (use Postgres or SQLite for SQL)

#### Planned Configuration

```yaml
apiVersion: loom/v1
kind: Backend

name: mysql-backend
type: mysql

database:
  dsn: mysql://user:pass@localhost:3306/dbname
  max_connections: 20
  max_idle_connections: 5
  connection_timeout_seconds: 30
  enable_ssl: true

schema_discovery:
  enabled: true
  cache_ttl_seconds: 3600

tool_generation:
  enable_all: true

health_check:
  enabled: true
  interval_seconds: 60
  query: "SELECT 1"
```

---

### MongoDB Backend (Planned)

MongoDB database backend for document operations.

**Type**: `mongodb`
**Status**: 📋 Planned for v1.0
**Current Workaround**: None

#### Planned Configuration

```yaml
apiVersion: loom/v1
kind: Backend

name: mongodb-backend
type: mongodb

database:
  dsn: mongodb://localhost:27017/loom
  max_connections: 10

capabilities:
  query_execution: true
  schema_discovery: true
  metadata: true
  resource_listing: true
  custom_operations: true
```

---

### GraphQL Backend (Planned)

GraphQL API backend.

**Type**: `graphql`
**Status**: 📋 Planned for v1.0
**Current Workaround**: Use REST backend with GraphQL endpoints

#### Planned Configuration

```yaml
apiVersion: loom/v1
kind: Backend

name: graphql-backend
type: graphql

rest:
  base_url: https://api.github.com/graphql
  auth:
    type: bearer
    token: ${GITHUB_TOKEN}
  timeout_seconds: 30
```

---

## Common Configuration Fields

These fields apply to all backend types.

### apiVersion

**Type**: `string`
**Required**: Yes
**Value**: `loom/v1`

Loom API version for backend configuration.

---

### kind

**Type**: `string`
**Required**: Yes
**Value**: `Backend`

Resource type identifier.

---

### name

**Type**: `string`
**Required**: Yes
**Format**: Lowercase alphanumeric with hyphens
**Regex**: `^[a-z][a-z0-9-]*$`

Unique backend identifier used in agent configuration.

**Example**:
```yaml
name: analytics-postgres
name: github-api
name: vantage-mcp-backend
```

---

### description

**Type**: `string`
**Required**: No
**Length**: 10-500 characters

Human-readable description of backend purpose.

**Example**:
```yaml
description: Production PostgreSQL analytics database
description: GitHub REST API for repository management
description: Teradata Vantage via MCP server
```

---

### type

**Type**: `string`
**Required**: Yes
**Allowed values**: `postgres` | `sqlite` | `rest` | `mcp` | `file` | `mysql` | `mongodb` | `graphql`

Backend implementation type.

---

## Capabilities

Backend capabilities define available operations.

### capabilities.query_execution

**Type**: `bool`
**Default**: Varies by backend type

Supports SQL query execution (SQL backends only).

**Supported**:
- ✅ Postgres
- ✅ SQLite
- ✅ MySQL (planned)
- ✅ MCP (if server provides tools)
- ❌ REST
- ❌ File

---

### capabilities.schema_discovery

**Type**: `bool`
**Default**: Varies by backend type

Supports schema introspection (table/column discovery).

**Supported**: All backends

---

### capabilities.metadata

**Type**: `bool`
**Default**: Varies by backend type

Supports metadata access (table stats, column types, constraints).

**Supported**: All backends

---

### capabilities.resource_listing

**Type**: `bool`
**Default**: Varies by backend type

Supports resource enumeration (tables, files, endpoints).

**Supported**: All backends

---

### capabilities.custom_operations

**Type**: `bool`
**Default**: `false`

Supports backend-specific operations (e.g., Teradata-specific SQL extensions).

**Supported**:
- ✅ MCP (server-dependent)
- ❌ Standard SQL backends
- ❌ REST
- ❌ File

---

## Connection Management

### Connection Pooling

SQL backends (Postgres, SQLite, MySQL) use connection pooling for performance.

**Configuration**:
```yaml
database:
  max_connections: 20        # Pool size
  max_idle_connections: 5    # Idle connections retained
  connection_timeout_seconds: 30
```

**Behavior**:
- Connections created on-demand up to `max_connections`
- Idle connections reused for new requests
- Connections closed after idle timeout
- Failed connections retried with exponential backoff

---

### Connection Lifecycle

1. **Initialization**: Connection pool created on backend load
2. **Health Check**: Initial connection verified
3. **Active Use**: Connections acquired from pool
4. **Return**: Connections returned to pool after use
5. **Idle Management**: Idle connections maintained up to limit
6. **Cleanup**: Pool drained on backend shutdown

---

### Connection Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| `connection refused` | Backend service not running | Start service, verify host/port |
| `authentication failed` | Invalid credentials | Check username/password in DSN |
| `timeout` | Connection took too long | Increase `connection_timeout_seconds` |
| `too many connections` | Pool exhausted | Increase `max_connections` or reduce load |
| `ssl required` | Server requires SSL but disabled | Set `enable_ssl: true` |

---

## Schema Discovery

Automatic discovery of backend schema (tables, columns, types).

### schema_discovery.enabled

**Type**: `bool`
**Default**: `true`

Enable automatic schema discovery.

---

### schema_discovery.cache_ttl_seconds

**Type**: `int`
**Default**: `3600` (1 hour)
**Range**: `60` - `86400`

Schema cache time-to-live in seconds.

**Recommendation**:
- Development: `600` (10 minutes)
- Production: `3600` (1 hour)
- Static schemas: `86400` (24 hours)

---

### schema_discovery.include_tables

**Type**: `[]string`
**Required**: No

Table whitelist (glob patterns supported).

**Examples**:
```yaml
schema_discovery:
  include_tables:
    - users           # Exact match
    - orders          # Exact match
    - analytics_*     # Glob pattern
    - prod.*          # Schema-qualified
```

---

### schema_discovery.exclude_tables

**Type**: `[]string`
**Required**: No

Table blacklist (glob patterns supported).

**Examples**:
```yaml
schema_discovery:
  exclude_tables:
    - temp_*          # Temporary tables
    - staging_*       # Staging tables
    - _internal       # Internal tables
    - test_*          # Test tables
```

**Precedence**: `exclude_tables` takes priority over `include_tables`.

---

## Tool Generation

Automatic tool generation from backend capabilities.

### tool_generation.enable_all

**Type**: `bool`
**Default**: `false`

Generate tools for all discovered tables/resources.

**Recommendation**:
- Development: `true` (discover all capabilities)
- Production: `false` (explicit tool list for security)

---

### tool_generation.tools

**Type**: `[]string`
**Required**: No

Explicit list of tools to generate.

**SQL backends**:
```yaml
tool_generation:
  tools:
    - query_users       # Table-specific query tool
    - query_orders
    - get_analytics
```

**REST backends**:
```yaml
tool_generation:
  tools:
    - list_repositories
    - create_issue
    - get_pull_request
```

**MCP backends**: Tools discovered automatically via MCP `tools/list` endpoint.

---

## Health Checks

Periodic backend health verification.

### health_check.enabled

**Type**: `bool`
**Default**: `true`

Enable periodic health checks.

---

### health_check.interval_seconds

**Type**: `int`
**Default**: `60`
**Range**: `10` - `3600`

Interval between health checks.

**Recommendation**:
- Critical backends: `30` (30 seconds)
- Standard backends: `60` (1 minute)
- Low-priority backends: `300` (5 minutes)

---

### health_check.timeout_seconds

**Type**: `int`
**Default**: `5`
**Range**: `1` - `30`

Health check timeout.

---

### health_check.query

**Type**: `string`
**Required for**: SQL backends
**Default**: `"SELECT 1"`

Health check query for SQL backends.

**Examples**:
```yaml
health_check:
  query: "SELECT 1"                 # Generic
  query: "SELECT version()"         # Postgres-specific
  query: "SELECT CURRENT_TIMESTAMP" # MySQL-specific
```

---

## Error Handling

### Common Backend Errors

#### Connection Failed

**Cause**: Cannot connect to backend service

**Error message**:
```
Error: backend connection failed: dial tcp 127.0.0.1:5432: connection refused
```

**Resolution**:
1. Verify backend service running
2. Check host/port in DSN
3. Verify network connectivity
4. Check firewall rules

---

#### Authentication Failed

**Cause**: Invalid credentials

**Error message**:
```
Error: authentication failed: password authentication failed for user "loom"
```

**Resolution**:
1. Verify username/password in DSN
2. Check credentials in keyring: `looms config get database.dsn`
3. Test credentials manually: `psql "postgresql://..."`

---

#### Schema Discovery Failed

**Cause**: Insufficient permissions or missing tables

**Error message**:
```
Error: schema discovery failed: permission denied for schema public
```

**Resolution**:
1. Grant SELECT on `information_schema` tables
2. Verify `include_tables` patterns match actual tables
3. Check `exclude_tables` not blocking all tables

---

#### Tool Generation Failed

**Cause**: Invalid tool configuration

**Error message**:
```
Error: tool generation failed: table 'users' not found in schema
```

**Resolution**:
1. Verify table exists: Run schema discovery
2. Check table name spelling
3. Verify `include_tables`/`exclude_tables` patterns

---

#### Health Check Failed

**Cause**: Backend unhealthy or query failed

**Error message**:
```
Error: health check failed: query timeout after 5s
```

**Resolution**:
1. Check backend service logs
2. Increase `health_check.timeout_seconds`
3. Verify health check query is valid
4. Check network latency

---

## Examples

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
  max_idle_connections: 2
  connection_timeout_seconds: 5
  enable_ssl: false

schema_discovery:
  enabled: true
  cache_ttl_seconds: 600  # 10 minutes

tool_generation:
  enable_all: true  # Discover all tables in dev

health_check:
  enabled: true
  interval_seconds: 60
  timeout_seconds: 5
  query: "SELECT 1"
```

---

### Production PostgreSQL

```yaml
apiVersion: loom/v1
kind: Backend

name: postgres-prod
description: Production PostgreSQL analytics database
type: postgres

database:
  dsn: postgresql://analytics_user@prod-db.example.com:5432/analytics?sslmode=require
  max_connections: 50
  max_idle_connections: 10
  connection_timeout_seconds: 30
  enable_ssl: true
  ssl_cert_path: /etc/loom/certs/postgres-ca.crt

schema_discovery:
  enabled: true
  cache_ttl_seconds: 3600
  include_tables:
    - users
    - orders
    - products
    - analytics_*
  exclude_tables:
    - temp_*
    - staging_*
    - _internal

tool_generation:
  enable_all: false
  tools:
    - query_users
    - query_orders
    - query_products
    - query_analytics_daily

health_check:
  enabled: true
  interval_seconds: 30
  timeout_seconds: 10
  query: "SELECT version()"
```

---

### Local SQLite

```yaml
apiVersion: loom/v1
kind: Backend

name: sqlite-local
description: Local SQLite database for testing
type: sqlite

database:
  dsn: ./test-data.db
  driver: sqlite3

capabilities:
  query_execution: true
  schema_discovery: true
  metadata: true
  resource_listing: true
  custom_operations: false
```

---

### GitHub REST API

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
    User-Agent: loom-agent/0.9.0
  timeout_seconds: 30
  max_retries: 3

tool_generation:
  tools:
    - list_repositories
    - create_issue
    - get_pull_request
    - create_pull_request
    - list_issues

health_check:
  enabled: true
  interval_seconds: 300
  timeout_seconds: 10
```

---

### Teradata Vantage (MCP)

```yaml
apiVersion: loom/v1
kind: Backend

name: vantage-mcp
description: Teradata Vantage via MCP server
type: mcp

mcp:
  command: vantage-mcp
  transport: stdio
  env: {}  # Credentials configured via looms config

capabilities:
  query_execution: true
  schema_discovery: true
  metadata: true
  resource_listing: true
  custom_operations: true
```

---

### Custom Python MCP Server

```yaml
apiVersion: loom/v1
kind: Backend

name: python-data-processor
description: Custom Python MCP server for data processing
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
    CACHE_DIR: /var/cache/mcp
    LOG_LEVEL: info
    MAX_WORKERS: "4"

capabilities:
  query_execution: false
  schema_discovery: true
  metadata: true
  resource_listing: true
  custom_operations: true
```

---

### File System Access

```yaml
apiVersion: loom/v1
kind: Backend

name: file-system
description: Local file system for document analysis
type: file

database:
  dsn: /var/lib/loom/documents

capabilities:
  query_execution: false
  schema_discovery: true
  metadata: true
  resource_listing: true
  custom_operations: false
```

---

### Multi-Backend Configuration

Agent with multiple backends:

```yaml
# Server configuration
agents:
  agents:
    multi-backend-agent:
      name: Multi-Backend Agent
      description: Agent with multiple data sources
      backends:
        - ./backends/postgres.yaml    # Primary SQL backend
        - ./backends/github-api.yaml  # GitHub integration
        - ./backends/vantage-mcp.yaml # Teradata Vantage
      system_prompt: "You have access to PostgreSQL, GitHub API, and Teradata Vantage."
```

---

## See Also

- [CLI Reference](./cli.md) - Backend management commands
- [Configuration Reference](./configuration.md) - Server configuration
- [MCP Integration Guide](../guides/integration/mcp.md) - MCP setup guide
- [LLM Providers](./llm-providers.md) - LLM provider configuration

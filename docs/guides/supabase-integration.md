# Supabase Integration Guide

**Status:** ✅ Implemented (v1.1.0)

Loom integrates with Supabase in two ways:

1. **Execution Backend**: Query Supabase databases from your agents
2. **Storage Backend**: Use Supabase PostgreSQL for Loom's internal storage (sessions, agents, state)

Both use native `pgxpool` with pooler-mode awareness, Row Level Security (RLS) support, and automatic Supabase internal schema filtering.

## Prerequisites

- A [Supabase project](https://supabase.com/dashboard)
- Your project reference (Settings > General > Reference ID)
- Your database password (set during project creation)
- Your project's region (e.g., `us-east-1`)

## Part 1: Supabase as Execution Backend

Use this when you want your agents to query data from Supabase databases.

### Quick Start

### 1. Set environment variables

```bash
export SUPABASE_PROJECT_REF="your-project-ref"
export SUPABASE_DB_PASSWORD="your-database-password"
export SUPABASE_ANON_KEY="your-anon-key"  # optional, for RLS
```

### 2. Create a backend config

Create `backends/supabase.yaml`:

```yaml
apiVersion: loom/v1
kind: Backend
name: my-supabase
type: supabase
description: My Supabase database

supabase:
  project_ref: ${SUPABASE_PROJECT_REF}
  database_password: ${SUPABASE_DB_PASSWORD}
  pooler_mode: transaction
  region: us-east-1
  # Optional: set if auto-constructed hostname doesn't match your project
  # pooler_host: ${SUPABASE_POOLER_HOST}
```

### 3. Reference in your agent config

```yaml
backends:
  - path: backends/supabase.yaml
```

## Configuration Reference

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `project_ref` | Yes | — | Supabase project reference ID |
| `api_key` | No | — | Anon or service_role key (for RLS) |
| `database_password` | Yes | — | Database password |
| `pooler_mode` | No | `session` | `session` or `transaction` |
| `enable_rls` | No | `false` | Enable RLS context injection |
| `database` | No | `postgres` | Database name |
| `max_pool_size` | No | `10` | Maximum connection pool size |
| `region` | Yes | — | AWS region (e.g., `us-east-1`) |
| `pooler_host` | No | auto | Pooler hostname (e.g., `aws-1-us-east-1.pooler.supabase.com`) |

## Connection Modes

Supabase uses [Supavisor](https://supabase.com/docs/guides/database/connecting-to-postgres#connection-pooler) for connection pooling with two modes:

### Session Mode (port 5432)

```yaml
supabase:
  pooler_mode: session
```

- Each client gets a dedicated Postgres connection for the session duration
- Supports prepared statements, `LISTEN/NOTIFY`, advisory locks
- Use when: your workload needs session-level features

### Transaction Mode (port 6543)

```yaml
supabase:
  pooler_mode: transaction
```

- Connections are shared between clients at transaction boundaries
- Higher throughput, better resource utilization
- Does **not** support: prepared statements, `LISTEN/NOTIFY`, `SET` commands that persist across transactions
- Loom automatically configures `QueryExecModeExec` to avoid prepared statements
- Use when: you need maximum concurrency (recommended for most agent workloads)

## Row Level Security (RLS)

When `enable_rls: true`, queries can be scoped to a specific user by injecting JWT claims into the Postgres session context.

### Programmatic Usage

```go
import "github.com/teradata-labs/loom/pkg/backends/supabase"

// Attach JWT claims to context
claims := map[string]interface{}{
    "sub":  "user-123",
    "role": "authenticated",
    "app_metadata": map[string]interface{}{
        "tenant_id": "org-456",
    },
}
ctx = supabase.WithJWT(ctx, claims)

// Execute query — RLS policies will evaluate against the claims
result, err := backend.ExecuteQuery(ctx, "SELECT * FROM documents")
```

### Via Custom Operation

```go
result, err := backend.ExecuteCustomOperation(ctx, "rls_query", map[string]interface{}{
    "query": "SELECT * FROM documents",
    "claims": map[string]interface{}{
        "sub":  "user-123",
        "role": "authenticated",
    },
})
```

### How It Works

When RLS is enabled and JWT claims are present in the context, Loom wraps the query in a transaction that:

1. Calls `set_config('request.jwt.claims', '<json>', true)` to inject claims
2. Calls `set_config('role', 'authenticated', true)` to set the Postgres role
3. Executes the query (RLS policies evaluate against `current_setting('request.jwt.claims')`)
4. Commits the transaction

This mirrors how Supabase's PostgREST handles RLS-scoped requests.

## Schema Filtering

The Supabase backend automatically excludes internal schemas from `ListResources()`:

- `auth`, `storage`, `realtime` — Supabase services
- `supabase_functions`, `supabase_migrations` — Internal management
- `extensions`, `graphql`, `graphql_public` — Extension schemas
- `pgbouncer`, `pgsodium`, `pgsodium_masks`, `vault` — Infrastructure
- `_realtime`, `_analytics` — Internal analytics
- `pg_*`, `information_schema` — Postgres system schemas

Only user-created schemas (typically `public`) are shown.

## Part 2: Supabase as Storage Backend

**Status:** ✅ Implemented (v1.1.0)
**Verified:** PostgreSQL 17.6 on Supabase

Use Supabase PostgreSQL for Loom's internal storage instead of SQLite. This stores sessions, agents, conversation history, and state in your Supabase database.

### Configuration

Set `storage.backend: postgres` and provide your Supabase connection string:

#### Option 1: Environment Variable (Recommended)

```bash
export LOOM_STORAGE_POSTGRES_DSN="postgresql://postgres.PROJECT_REF:PASSWORD@POOLER_HOST:5432/postgres?sslmode=require"
```

For example, if your project ref is `abcdefghijklmnop`, region is `us-east-1`, and pooler prefix is `aws-1`:
```bash
export LOOM_STORAGE_POSTGRES_DSN="postgresql://postgres.PROJECT_REF:DB_PASSWORD@aws-N-REGION.pooler.supabase.com:5432/postgres?sslmode=require"
```

Then in your `looms.yaml`:
```yaml
storage:
  backend: postgres
  postgres:
    dsn: ""  # Will be overridden by LOOM_STORAGE_POSTGRES_DSN
  migration:
    auto_migrate: true  # Run migrations automatically
```

#### Option 2: Config File

```yaml
storage:
  backend: postgres
  postgres:
    dsn: "postgresql://postgres.PROJECT_REF:PASSWORD@POOLER_HOST:5432/postgres?sslmode=require"
    pool:
      max_connections: 10
      min_connections: 2
      max_idle_time_seconds: 300
  migration:
    auto_migrate: true
```

### DSN Format

The Data Source Name (DSN) format for Supabase:

```
postgresql://postgres.PROJECT_REF:PASSWORD@POOLER_HOST:PORT/DATABASE?sslmode=require
```

Components:
- **PROJECT_REF**: Your Supabase project reference (e.g., `abcdefghijklmnop`)
- **PASSWORD**: Your database password (URL-encode special characters)
- **POOLER_HOST**: Your pooler hostname (e.g., `aws-1-us-east-1.pooler.supabase.com`)
- **PORT**: Use `5432` for session mode (required for storage backend)
- **DATABASE**: Typically `postgres`
- **sslmode**: Always use `require` for Supabase

### Finding Your Connection String

1. Navigate to **Project Settings > Database** in your Supabase dashboard
2. Select **Connection string** tab
3. Choose **Session mode** (not Transaction mode)
4. Copy the connection string
5. Replace `[YOUR-PASSWORD]` with your actual password

### Important Notes

- ✅ **Use Session Mode (port 5432)**: The storage backend requires prepared statements, which are only available in session mode
- ✅ **Auto-Migration**: Set `migration.auto_migrate: true` to automatically create/update tables
- ✅ **Environment Variables**: The `LOOM_STORAGE_POSTGRES_DSN` env var overrides `storage.postgres.dsn` in config
- ⚠️ **URL Encoding**: If your password contains special characters, URL-encode them (e.g., `@` → `%40`, `#` → `%23`)

### Pool Configuration

Customize connection pool settings:

```yaml
storage:
  postgres:
    pool:
      max_connections: 10         # Maximum connections (default: 10)
      min_connections: 2           # Minimum idle connections (default: 2)
      max_idle_time_seconds: 300   # Idle connection timeout (default: 300)
      max_lifetime_seconds: 1800   # Connection lifetime (default: 1800)
      health_check_interval_seconds: 30  # Health check interval (default: 30)
```

### Migration

Tables are created automatically when `migration.auto_migrate: true`. The following tables are created:

- `agents` - Agent configurations
- `sessions` - Conversation sessions
- `messages` - Conversation history
- `artifacts` - File storage metadata
- `human_requests` - Human-in-the-loop requests
- `errors` - Error tracking
- `results` - Operation results
- `admin_settings` - Admin configuration

### Environment Variable Mapping

Viper's `SetEnvKeyReplacer` automatically maps nested config keys to environment variables:

| Config Key | Environment Variable |
|------------|---------------------|
| `storage.backend` | `LOOM_STORAGE_BACKEND` |
| `storage.postgres.dsn` | `LOOM_STORAGE_POSTGRES_DSN` |
| `storage.postgres.pool.max_connections` | `LOOM_STORAGE_POSTGRES_POOL_MAX_CONNECTIONS` |
| `storage.migration.auto_migrate` | `LOOM_STORAGE_MIGRATION_AUTO_MIGRATE` |

## Troubleshooting

### Common Issues

#### "Tenant or user not found" Error

**Applies to**: Both execution and storage backends

This error occurs when the pooler hostname doesn't match your project. Supabase projects use different infrastructure prefixes (`aws-0`, `aws-1`, etc.) based on when and where they were created.

**Fix**:
1. Go to **Project Settings > Database > Connection string** in Supabase dashboard
2. Find your exact pooler hostname (e.g., `aws-1-us-east-1.pooler.supabase.com`)
3. For execution backend: Set `pooler_host` in your backend config
4. For storage backend: Use the correct hostname in your DSN

#### Password with Special Characters

**Applies to**: Both backends

Connection fails if password contains special characters that aren't URL-encoded.

**Fix**: URL-encode special characters in passwords:
- `@` → `%40`
- `#` → `%23`
- `!` → `%21`
- `$` → `%24`
- Space → `+` or `%20`

Example:
```bash
# Original password: p@ss#word!
# Encoded: p%40ss%23word%21
export LOOM_STORAGE_POSTGRES_DSN="postgresql://postgres.proj:p%40ss%23word%21@host:5432/postgres?sslmode=require"
```

#### Connection Refused / DNS Resolution Failure

**Applies to**: Both backends

**Checks**:
- Verify `project_ref` matches your project (Settings > General > Reference ID)
- Verify `region` matches your project's region
- Check that your IP is not blocked by Supabase network restrictions
- For storage backend: Ensure you're using port 5432 (session mode), not 6543

#### Prepared Statement Errors

**Applies to**: Execution backend in transaction mode

Error: "prepared statement does not exist"

**Fix**:
- Execution backend: Loom automatically handles this when `pooler_mode: transaction` is set
- Storage backend: Must use session mode (port 5432) - transaction mode not supported for storage

### Execution Backend Issues

#### RLS Returning No Rows

- Verify `enable_rls: true` in backend config
- Verify JWT claims include required `sub` and `role` fields
- Test RLS policies directly in Supabase SQL editor
- Check that the `authenticated` role has appropriate permissions

#### "permission denied for schema" Errors

- Execution backend connects as `postgres` user by default
- For RLS-scoped queries, role temporarily switches to `authenticated`
- Verify your RLS policies grant access to the `authenticated` role

### Storage Backend Issues

#### Migration Failures

Tables not created or migration errors during startup.

**Fix**:
- Ensure `migration.auto_migrate: true` is set
- Verify database user has CREATE TABLE permissions
- Confirm connection uses session mode (port 5432)
- Check logs for specific migration error details

#### "cannot use prepared statements in transaction pooling mode"

**Cause**: Using transaction mode pooler (port 6543) for storage backend

**Fix**: Storage backend requires session mode. Update your DSN to use port 5432:
```bash
# Wrong: port 6543 (transaction mode)
postgresql://postgres.proj:pass@host:6543/postgres

# Correct: port 5432 (session mode)
postgresql://postgres.proj:pass@host:5432/postgres
```

## Next Steps

- [Pattern Library Guide](pattern-library-guide.md) - Configure domain patterns for your agents
- [Learning Agent Guide](learning-agent-guide.md) - Enable self-improvement capabilities
- [Multi-Judge Evaluation](multi-judge-evaluation.md) - Add quality checks to agent responses

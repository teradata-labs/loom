
# Agent Configuration Reference

Complete technical specification for Loom agent configuration and management.

**Version**: v1.0.0-beta.2
**API Version**: loom/v1
**Configuration Kind**: `Agent`


## Table of Contents

- [Quick Reference](#quick-reference)
- [Agent YAML Schema](#agent-yaml-schema)
- [Configuration Fields](#configuration-fields)
  - [Agent Metadata](#agent-metadata)
  - [LLM Configuration](#llm-configuration)
  - [Backend Configuration](#backend-configuration)
  - [Memory Configuration](#memory-configuration)
  - [Guardrails Configuration](#guardrails-configuration)
  - [Pattern Configuration](#pattern-configuration)
  - [Tool Configuration](#tool-configuration)
  - [Observability Configuration](#observability-configuration)
- [Agent Lifecycle](#agent-lifecycle)
- [Hot Reloading](#hot-reloading)
- [Error Handling](#error-handling)
- [Examples](#examples)


## Quick Reference

| Configuration Section | Required | Description |
|----------------------|----------|-------------|
| `name` | Yes | Agent identifier |
| `description` | No | Human-readable description |
| `llm` | Yes | LLM provider and model configuration |
| `backend` | No | Data source backend (SQL, REST, MCP, file) |
| `memory` | No | Session storage configuration |
| `guardrails` | No | Safety constraints and limits |
| `patterns` | No | Pattern library configuration |
| `tools` | No | Tool system configuration |
| `observability` | No | Tracing and monitoring configuration |


## Agent YAML Schema

Agents use Kubernetes-style YAML configuration.

### Complete Schema

```yaml
apiVersion: loom/v1        # Required: API version
kind: Agent                # Required: Resource type

# Metadata
name: string               # Required: Agent identifier
description: string        # Optional: Human-readable description

# LLM configuration
llm:
  provider: string         # Required: anthropic|bedrock|ollama
  model: string            # Required: Model ID
  temperature: float       # Optional: Sampling temperature (0.0-2.0)
  max_tokens: int          # Optional: Max output tokens
  timeout_seconds: int     # Optional: LLM request timeout

# Backend configuration
backend:
  type: string             # Optional: postgres|sqlite|rest|mcp|file
  config_path: string      # Optional: Path to backend config YAML

# Memory configuration
memory:
  type: string             # Optional: memory|sqlite|postgres
  path: string             # For sqlite: database path
  dsn: string              # For postgres: connection string
  max_sessions: int        # Optional: Max concurrent sessions
  session_ttl_seconds: int # Optional: Session expiration

# Guardrails configuration
guardrails:
  max_turns: int           # Optional: Max conversation turns
  max_tool_executions: int # Optional: Max tool calls per turn
  timeout_seconds: int     # Optional: Total conversation timeout
  allowed_domains: []string # Optional: Domain whitelist (REST backends)
  blocked_patterns: []string # Optional: Regex patterns to reject

# Pattern configuration
patterns:
  library_path: string     # Optional: Path to pattern library
  cache_ttl_seconds: int   # Optional: Pattern cache TTL
  enable_hot_reload: bool  # Optional: Reload patterns without restart

# Tool configuration
tools:
  mcp:
    servers: []string      # Optional: MCP server names
    tools: []string        # Optional: Specific tool whitelist
  custom:
    enabled: bool          # Optional: Enable custom tools
    tools: []string        # Optional: Custom tool names

# Observability configuration
observability:
  enabled: bool            # Optional: Enable tracing
  hawk_endpoint: string    # Optional: Hawk service URL
  export_interval_seconds: int # Optional: Trace export interval
  sample_rate: float       # Optional: Sampling rate (0.0-1.0)
```


## Configuration Fields

### Agent Metadata

#### name

**Type**: `string`
**Required**: Yes
**Format**: Lowercase alphanumeric with hyphens
**Regex**: `^[a-z][a-z0-9-]*$`

Unique agent identifier.

**Example**:
```yaml
name: sql-assistant
name: github-bot
name: analytics-agent
```


#### description

**Type**: `string`
**Required**: No
**Length**: 10-500 characters

Human-readable agent description.

**Example**:
```yaml
description: SQL query assistant for analytics database
description: GitHub repository management agent
description: Customer analytics agent with pattern library
```


### LLM Configuration

#### llm.provider

**Type**: `string`
**Required**: Yes
**Allowed values**: `anthropic` | `bedrock` | `ollama`

LLM provider name.

**Available**:
- ✅ `anthropic` - Anthropic Claude API
- ✅ `bedrock` - AWS Bedrock (Anthropic models)
- ✅ `ollama` - Self-hosted Ollama
- 📋 `openai` - OpenAI API (planned)
- 📋 `azure` - Azure OpenAI (planned)

**See also**: [LLM Providers Reference](./llm-providers.md)


#### llm.model

**Type**: `string`
**Required**: Yes

Model identifier specific to provider.

**Anthropic models**:
- `claude-sonnet-4-5-20250929` (200k context)
- `claude-opus-4-6` (200k context)

**Ollama models**:
- `llama3.2:latest` (128k context)
- `mistral:latest` (32k context)

**See also**: [LLM Providers Reference](./llm-providers.md) for complete model lists


#### llm.temperature

**Type**: `float64`
**Default**: `1.0`
**Range**: `0.0` - `2.0`

Sampling temperature for LLM generation.

**Recommendation**:
- Deterministic tasks (SQL, code): `0.0` - `0.3`
- Balanced tasks (Q&A, analysis): `0.7` - `1.0`
- Creative tasks (writing, brainstorming): `1.5` - `2.0`


#### llm.max_tokens

**Type**: `int`
**Default**: Provider-specific
**Range**: `1` - provider max

Maximum tokens to generate in response.

**Anthropic Claude**: `1` - `8192`
**Ollama**: `-1` (unlimited) or `1` - `2048`


#### llm.timeout_seconds

**Type**: `int`
**Default**: `60`
**Range**: `1` - `300`

Timeout for LLM API requests.


### Backend Configuration

#### backend.type

**Type**: `string`
**Required**: No
**Allowed values**: `postgres` | `sqlite` | `rest` | `mcp` | `file`

Backend type for agent data source.

**Available**:
- ✅ `postgres` - PostgreSQL database
- ✅ `sqlite` - SQLite database
- ✅ `rest` - REST API
- ✅ `mcp` - Model Context Protocol server
- ✅ `file` - File system

**See also**: [Backend Reference](./backend.md)


#### backend.config_path

**Type**: `string`
**Required**: When `backend.type` specified

Path to backend configuration YAML file.

**Example**:
```yaml
backend:
  type: postgres
  config_path: ./backends/analytics-postgres.yaml
```


### Memory Configuration

Session and conversation history storage.

#### memory.type

**Type**: `string`
**Default**: `memory`
**Allowed values**: `memory` | `sqlite` | `postgres`

Session storage backend.

**memory**: In-memory storage (ephemeral, lost on restart)
**sqlite**: Local SQLite database (persistent)
**postgres**: PostgreSQL database (persistent, distributed)


#### memory.path

**Type**: `string`
**Required for**: `memory.type: sqlite`

Path to SQLite database file for session storage.

**Example**:
```yaml
memory:
  type: sqlite
  path: ./sessions.db
```


#### memory.dsn

**Type**: `string`
**Required for**: `memory.type: postgres`

PostgreSQL connection string for session storage.

**Format**: Same as backend DSN format

**Example**:
```yaml
memory:
  type: postgres
  dsn: postgresql://localhost:5432/loom_sessions?sslmode=disable
```


#### memory.max_sessions

**Type**: `int`
**Default**: `1000`
**Range**: `1` - `100000`

Maximum concurrent sessions to maintain.


#### memory.session_ttl_seconds

**Type**: `int`
**Default**: `86400` (24 hours)
**Range**: `60` - `2592000` (30 days)

Session time-to-live before automatic expiration.


### Guardrails Configuration

Safety constraints and operational limits.

#### guardrails.max_turns

**Type**: `int`
**Default**: `10`
**Range**: `1` - `100`

Maximum conversation turns before automatic termination.

**Recommendation**:
- Simple tasks: `5-10`
- Complex tasks: `20-30`
- Open-ended conversations: `50-100`


#### guardrails.max_tool_executions

**Type**: `int`
**Default**: `20`
**Range**: `1` - `100`

Maximum tool executions per conversation turn.

**Recommendation**:
- Single-query tasks: `5-10`
- Multi-step workflows: `20-30`
- Complex orchestration: `50-100`


#### guardrails.timeout_seconds

**Type**: `int`
**Default**: `300` (5 minutes)
**Range**: `10` - `3600` (1 hour)

Total conversation timeout.


#### guardrails.allowed_domains

**Type**: `[]string`
**Required**: No

Domain whitelist for REST backend URL validation.

**Example**:
```yaml
guardrails:
  allowed_domains:
    - api.github.com
    - api.example.com
```


#### guardrails.blocked_patterns

**Type**: `[]string`
**Required**: No

Regex patterns to reject in user input.

**Example**:
```yaml
guardrails:
  blocked_patterns:
    - "DROP\\s+TABLE"
    - "DELETE\\s+FROM"
    - "<script"
```


### Pattern Configuration

Pattern library for domain-specific knowledge.

#### patterns.library_path

**Type**: `string`
**Required**: No

Path to pattern library directory.

**Example**:
```yaml
patterns:
  library_path: ./patterns
```

**See also**: [Patterns Reference](./patterns.md)


#### patterns.cache_ttl_seconds

**Type**: `int`
**Default**: `3600` (1 hour)
**Range**: `60` - `86400`

Pattern cache time-to-live.


#### patterns.enable_hot_reload

**Type**: `bool`
**Default**: `false`

Enable pattern hot reload without agent restart.

**Hot reload timing**: 89-143ms


### Tool Configuration

Tool system and MCP integration.

#### tools.mcp.servers

**Type**: `[]string`
**Required**: No

MCP server names to load tools from.

**Example**:
```yaml
tools:
  mcp:
    servers:
      - vantage       # Teradata Vantage MCP
      - github        # GitHub MCP
      - filesystem    # File system MCP
```

**See also**: [MCP Integration Guide](../guides/integration/mcp.md)


#### tools.mcp.tools

**Type**: `[]string`
**Required**: No

Specific tool whitelist from MCP servers.

**Example**:
```yaml
tools:
  mcp:
    servers:
      - vantage
    tools:
      - execute_query
      - list_tables
      - get_schema
```


#### tools.custom.enabled

**Type**: `bool`
**Default**: `false`

Enable custom tool registration.


#### tools.custom.tools

**Type**: `[]string`
**Required**: When `tools.custom.enabled: true`

Custom tool names to register.


### Observability Configuration

Tracing and monitoring integration.

#### observability.enabled

**Type**: `bool`
**Default**: `false`

Enable observability tracing.


#### observability.hawk_endpoint

**Type**: `string`
**Required when**: `observability.enabled: true`

Hawk service endpoint URL.

**Example**:
```yaml
observability:
  enabled: true
  hawk_endpoint: http://localhost:8081
```


#### observability.export_interval_seconds

**Type**: `int`
**Default**: `30`
**Range**: `1` - `300`

Interval for trace batch export.


#### observability.sample_rate

**Type**: `float64`
**Default**: `1.0`
**Range**: `0.0` - `1.0`

Sampling rate for trace collection.

**Recommendation**:
- Development: `1.0` (100%)
- Production low-traffic: `1.0` (100%)
- Production high-traffic: `0.1` (10%)


## Agent Lifecycle

### Creation

1. **Parse Configuration**: YAML validated against schema
2. **Initialize LLM Client**: Provider and model configured
3. **Load Backend**: Backend configuration loaded and validated
4. **Initialize Memory**: Session storage initialized
5. **Load Patterns**: Pattern library loaded (if configured)
6. **Register Tools**: Tools discovered and registered
7. **Start Observability**: Tracing initialized (if enabled)

**Creation time**: 50-200ms (without network calls)


### Active State

Agent processes conversations:
1. **Receive Message**: User input validated
2. **Load Session**: Conversation history retrieved
3. **Execute Turn**: LLM generates response with tool calls
4. **Save Session**: Updated history persisted
5. **Export Traces**: Telemetry data exported (if enabled)

**Turn latency**: 500ms - 5s (depends on LLM provider, tool calls)


### Hot Reload

Configuration changes without restart:
1. **Detect Change**: Configuration file modified
2. **Validate New Config**: Schema validation
3. **Reload Components**: Patterns, guardrails updated
4. **Preserve Sessions**: Active sessions maintained
5. **Apply Changes**: New config active

**Reload time**: 89-143ms (zero downtime)

**Hot-reloadable**:
- ✅ Patterns
- ✅ Guardrails
- ❌ LLM provider/model (requires restart)
- ❌ Backend type (requires restart)
- ❌ Memory type (requires restart)


### Shutdown

Graceful agent shutdown:
1. **Reject New Requests**: No new conversations accepted
2. **Complete Active Turns**: In-flight requests finished
3. **Flush Traces**: Remaining telemetry exported
4. **Close Backend**: Backend connections closed
5. **Close Memory**: Sessions persisted and storage closed

**Shutdown time**: 100-500ms


## Hot Reloading

### Supported Changes

| Configuration | Hot Reload | Restart Required |
|---------------|------------|------------------|
| Patterns | ✅ Yes | No |
| Guardrails (limits) | ✅ Yes | No |
| Guardrails (domains, patterns) | ✅ Yes | No |
| Observability (sample rate) | ✅ Yes | No |
| LLM provider | ❌ No | Yes |
| LLM model | ❌ No | Yes |
| Backend type | ❌ No | Yes |
| Memory type | ❌ No | Yes |


### Triggering Hot Reload

**File system watch** (automatic):
```bash
# Modify agent config
vim agents/sql-assistant.yaml

# Changes detected and applied automatically
```

**Manual trigger** (via CLI):
```bash
# Reload specific agent
looms agent reload sql-assistant

# Reload all agents
looms agent reload --all
```


### Hot Reload Behavior

**Pattern reload**:
1. New patterns loaded from disk
2. Pattern cache invalidated
3. New patterns available immediately
4. Old pattern references remain valid (no breaking changes)

**Timing**: 89-143ms

**Guardrail reload**:
1. New limits validated
2. Active conversations continue with old limits
3. New conversations use new limits

**Timing**: <10ms


## Error Handling

### Configuration Errors

#### Invalid Configuration Syntax

**Cause**: YAML syntax error

**Error message**:
```
Error: invalid agent configuration: yaml: line 12: mapping values are not allowed in this context
```

**Resolution**:
1. Validate YAML syntax: `yamllint agents/agent.yaml`
2. Check indentation (spaces, not tabs)
3. Verify field names match schema


#### Missing Required Field

**Cause**: Required field not provided

**Error message**:
```
Error: invalid agent configuration: field 'llm.provider' is required
```

**Resolution**:
1. Check schema documentation
2. Add missing required field
3. Validate configuration: `looms agent validate agents/agent.yaml`


#### Invalid Field Value

**Cause**: Field value outside allowed range

**Error message**:
```
Error: invalid agent configuration: field 'llm.temperature' must be between 0.0 and 2.0, got: 3.0
```

**Resolution**:
1. Check field constraints in documentation
2. Adjust value to valid range
3. Validate configuration


### Runtime Errors

#### LLM Provider Unavailable

**Cause**: Cannot connect to LLM provider

**Error message**:
```
Error: LLM provider unavailable: failed to connect to api.anthropic.com
```

**Resolution**:
1. Verify API key: `looms config get llm.api_key`
2. Check network connectivity
3. Verify provider status page
4. Check rate limits


#### Backend Connection Failed

**Cause**: Cannot connect to backend service

**Error message**:
```
Error: backend connection failed: dial tcp 127.0.0.1:5432: connection refused
```

**Resolution**:
1. Verify backend service running
2. Check backend configuration
3. Test backend connection: `psql "postgresql://..."`

**See also**: [Backend Reference - Error Handling](./backend.md#error-handling)


#### Pattern Load Failed

**Cause**: Pattern file syntax error

**Error message**:
```
Error: pattern load failed: invalid template syntax in 'sql_optimization.yaml': unexpected "}" at line 15
```

**Resolution**:
1. Validate pattern file: `looms pattern validate patterns/sql_optimization.yaml`
2. Check Go text/template syntax
3. Verify variable names

**See also**: [Patterns Reference - Error Handling](./patterns.md#error-handling)


#### Session Limit Exceeded

**Cause**: Too many concurrent sessions

**Error message**:
```
Error: session limit exceeded: cannot create new session (max: 1000)
```

**Resolution**:
1. Increase `memory.max_sessions`
2. Reduce `memory.session_ttl_seconds`
3. Clean up old sessions: `looms session cleanup`


## Examples

### Minimal Agent

```yaml
apiVersion: loom/v1
kind: Agent

name: simple-agent
description: Minimal agent configuration

llm:
  provider: anthropic
  model: claude-sonnet-4-5-20250929
  temperature: 0.7
```


### SQL Assistant

```yaml
apiVersion: loom/v1
kind: Agent

name: sql-assistant
description: SQL query assistant for PostgreSQL analytics

llm:
  provider: anthropic
  model: claude-sonnet-4-5-20250929
  temperature: 0.0
  max_tokens: 4096

backend:
  type: postgres
  config_path: ./backends/analytics-postgres.yaml

memory:
  type: sqlite
  path: ./sessions/sql-assistant.db

guardrails:
  max_turns: 20
  max_tool_executions: 30
  timeout_seconds: 300
  blocked_patterns:
    - "DROP\\s+TABLE"
    - "DELETE\\s+FROM\\s+(?!temp_)"

patterns:
  library_path: ./patterns/sql
  cache_ttl_seconds: 3600
  enable_hot_reload: true

observability:
  enabled: true
  hawk_endpoint: http://localhost:8081
  sample_rate: 1.0
```


### GitHub Bot

```yaml
apiVersion: loom/v1
kind: Agent

name: github-bot
description: GitHub repository management agent

llm:
  provider: bedrock
  model: anthropic.claude-sonnet-4-5-20250929-v1:0
  temperature: 0.5

backend:
  type: rest
  config_path: ./backends/github-api.yaml

memory:
  type: memory

guardrails:
  max_turns: 15
  max_tool_executions: 20
  allowed_domains:
    - api.github.com

tools:
  mcp:
    servers:
      - github
    tools:
      - list_repositories
      - create_issue
      - create_pull_request
```


### Teradata Vantage Agent

```yaml
apiVersion: loom/v1
kind: Agent

name: vantage-agent
description: Teradata Vantage SQL agent via MCP

llm:
  provider: anthropic
  model: claude-opus-4-6
  temperature: 0.0
  max_tokens: 8192

backend:
  type: mcp
  config_path: ./backends/vantage-mcp.yaml

memory:
  type: postgres
  dsn: postgresql://localhost:5432/loom_sessions?sslmode=disable
  max_sessions: 10000
  session_ttl_seconds: 604800  # 7 days

guardrails:
  max_turns: 30
  max_tool_executions: 50
  timeout_seconds: 600

patterns:
  library_path: ./patterns/teradata
  cache_ttl_seconds: 7200
  enable_hot_reload: true

tools:
  mcp:
    servers:
      - vantage
    tools:
      - execute_query
      - list_tables
      - get_schema
      - explain_query

observability:
  enabled: true
  hawk_endpoint: http://hawk.example.com:8081
  export_interval_seconds: 30
  sample_rate: 0.1  # 10% sampling in production
```


### Multi-Backend Agent

```yaml
apiVersion: loom/v1
kind: Agent

name: multi-backend-agent
description: Agent with multiple data sources

llm:
  provider: ollama
  model: llama3.2:latest
  temperature: 0.7

# Primary backend
backend:
  type: postgres
  config_path: ./backends/analytics-postgres.yaml

# Additional backends via MCP
tools:
  mcp:
    servers:
      - github        # GitHub API
      - filesystem    # Local files
      - vantage       # Teradata Vantage

memory:
  type: sqlite
  path: ./sessions/multi-backend.db

guardrails:
  max_turns: 25
  max_tool_executions: 40

patterns:
  library_path: ./patterns/multi-domain
  enable_hot_reload: true
```


## See Also

- [CLI Reference](./cli.md) - Agent management commands
- [Backend Reference](./backend.md) - Backend configuration
- [Patterns Reference](./patterns.md) - Pattern system
- [LLM Providers](./llm-providers.md) - LLM configuration
- [MCP Integration Guide](../guides/integration/mcp.md) - MCP setup

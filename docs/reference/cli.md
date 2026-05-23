
# Loom CLI Reference

Command reference for `loom` (client) and `looms` (server).

**Version**: v1.2.0


## Table of Contents

### Server Commands (`looms`)
- [looms serve](#looms-serve) - Start gRPC/HTTP server
- [looms config](#looms-config) - Manage server configuration and secrets
- [looms hitl](#looms-hitl) - Human-in-the-loop request management
- [looms upgrade](#looms-upgrade) - Upgrade database schema
- [looms pattern](#looms-pattern) - Create and watch patterns
- [looms workflow](#looms-workflow) - Manage and execute workflow orchestrations
- [looms learning](#looms-learning) - Manage learning agent operations
- [looms eval](#looms-eval) - Run and manage evaluation suites (requires `hawk` build tag)
- [looms judge](#looms-judge) - Multi-dimensional judge evaluation
- [looms validate](#looms-validate) - Validate configuration files
- [looms teleprompter](#looms-teleprompter) - DSPy-style prompt optimization

### Client Commands (`loom`)
- [loom (root)](#loom-root) - Launch TUI chat interface
- [loom chat](#loom-chat) - Send message to agent (CLI only, no TUI)
- [loom agents](#loom-agents) - List available agents/threads
- [loom sessions](#loom-sessions) - Manage conversation sessions
- [loom artifacts](#loom-artifacts) - Manage artifacts
- [loom mcp](#loom-mcp) - Manage MCP servers
- [loom providers](#loom-providers) - Manage LLM provider pool

### Reference
- [Global Server Flags](#global-server-flags) - Flags available on all `looms` commands
- [Global Client Flags](#global-client-flags) - Flags available on all `loom` commands
- [Environment Variables](#environment-variables) - Environment configuration
- [Configuration Files](#configuration-files) - YAML configuration examples
- [Exit Codes](#exit-codes) - Exit code meanings
- [Common Workflows](#common-workflows) - Standard patterns
- [Troubleshooting](#troubleshooting) - Common issues


## Quick Reference

### Server Commands Summary

| Command | Purpose | Key Flags |
|---------|---------|-----------|
| `looms serve` | Start gRPC/HTTP server | (uses global flags) |
| `looms config init` | Generate example config | (interactive) |
| `looms config set` | Set config value | `<key> <value>` |
| `looms config get` | Get config value | `<key>` |
| `looms config show` | Show current config | |
| `looms config set-key` | Store secret in keyring | `<key-name>` |
| `looms config get-key` | Retrieve secret from keyring | `<key-name>` |
| `looms config delete-key` | Delete secret from keyring | `<key-name>` |
| `looms config list-keys` | List available secret keys | |
| `looms hitl list` | List pending HITL requests | `--session`, `--agent`, `--db` |
| `looms hitl show` | Show HITL request details | `<request-id>`, `--db` |
| `looms hitl respond` | Respond to HITL request | `<request-id>`, `--status`, `--message` |
| `looms upgrade` | Upgrade database schema | `--dry-run`, `--no-backup`, `--yes` |
| `looms pattern create` | Create a new pattern | `<name>`, `--thread`, `--file` |
| `looms pattern watch` | Watch pattern updates | `--thread`, `--category` |
| `looms workflow validate` | Validate workflow YAML | `<file>` |
| `looms workflow run` | Execute a workflow | `<file>`, `--threads`, `--dry-run`, `--timeout` |
| `looms workflow list` | List workflow files | `[directory]`, `--dir` |
| `looms learning analyze` | Analyze pattern effectiveness | `--domain`, `--agent`, `--window` |
| `looms learning proposals` | List improvement proposals | `--status`, `--domain`, `--limit` |
| `looms learning apply` | Apply improvement | `<improvement-id>` |
| `looms learning rollback` | Rollback improvement | `<improvement-id>` |
| `looms learning history` | Get improvement history | `--domain`, `--agent`, `--status` |
| `looms learning stream` | Stream pattern metrics | `--domain`, `--agent` |
| `looms learning tune` | Auto-tune pattern priorities | `--domain`, `--strategy`, `--library` |
| `looms eval run` | Run evaluation suite | `<suite-file>`, `--thread`, `--store` |
| `looms eval list` | List evaluation runs | `--suite`, `--store`, `--limit` |
| `looms eval show` | Show eval run details | `<run-id>`, `--store` |
| `looms judge evaluate` | Evaluate with judges | `--agent`, `--judges`, `--prompt`, `--response` |
| `looms judge evaluate-stream` | Streaming judge evaluation | `--agent`, `--judges`, `--prompt`, `--response` |
| `looms judge register` | Register judge from YAML | `<config-file>` |
| `looms judge history` | Get evaluation history | `--agent`, `--judges`, `--pattern` |
| `looms validate file` | Validate a single YAML file | `<path>` |
| `looms validate dir` | Validate all YAML in directory | `<path>` |
| `looms teleprompter bootstrap` | Bootstrap few-shot demos | `--agent`, `--trainset`, `--judges` |
| `looms teleprompter mipro` | MIPRO prompt optimization | `--agent`, `--trainset`, `--instructions`, `--judges`, `--output` |
| `looms teleprompter textgrad` | TextGrad prompt optimization | `--agent`, `--example`, `--variables`, `--judges`, `--output` |
| `looms teleprompter history` | Show compilation history | `--agent`, `--limit` |
| `looms teleprompter rollback` | Rollback a compilation | `<compilation-id>`, `--agent` |
| `looms teleprompter compare` | Compare two compilations (A/B test) | `<compilation-a> <compilation-b>`, `--agent`, `--testset`, `--judges` |

### Client Commands Summary

| Command | Purpose | Key Flags |
|---------|---------|-----------|
| `loom` | Launch TUI | `--thread`, `--server`, `--session` |
| `loom chat` | CLI chat (no TUI) | `--thread`, `--message`, `--stream`, `--timeout` |
| `loom agents` | List agents/threads | |
| `loom sessions list` | List sessions | `--limit`, `--offset` |
| `loom sessions show` | Show session details | `<session-id>` |
| `loom sessions delete` | Delete session | `<session-id>` |
| `loom artifacts list` | List artifacts | `--source`, `--content-type`, `--tags` |
| `loom artifacts search` | Full-text search artifacts | `<query>`, `--limit` |
| `loom artifacts show` | Show artifact details | `<artifact-id-or-name>` |
| `loom artifacts upload` | Upload file as artifact | `<file-path>`, `--purpose`, `--tags` |
| `loom artifacts download` | Download artifact | `<artifact-id-or-name>`, `--output` |
| `loom artifacts delete` | Delete artifact | `<artifact-id-or-name>`, `--hard` |
| `loom artifacts stats` | Show storage statistics | |
| `loom mcp list` | List MCP servers | |
| `loom mcp test` | Test MCP server | `<server-name>` |
| `loom mcp tools` | List tools from MCP server | `<server-name>` |
| `loom providers list` | List LLM providers | |
| `loom providers switch` | Switch active LLM provider | `<provider-name>`, `--session`, `--thread` |


## Global Server Flags

These persistent flags are available on all `looms` commands:

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config` | string | `$LOOM_DATA_DIR/looms.yaml` | Path to config file |
| `--port` | int | `60051` | gRPC server port |
| `--host` | string | `0.0.0.0` | gRPC server host |
| `--http-port` | int | `5006` | HTTP/REST+SSE server port (0=disabled) |
| `--reflection` | bool | `true` | Enable gRPC reflection |
| `--llm-provider` | string | `anthropic` | LLM provider (anthropic, bedrock, ollama, openai, azure-openai, mistral, gemini, huggingface) |
| `--anthropic-key` | string | `""` | Anthropic API key (or use keyring/env) |
| `--anthropic-model` | string | `claude-sonnet-4-5-20250929` | Anthropic model |
| `--temperature` | float64 | `1.0` | LLM temperature |
| `--max-tokens` | int | `4096` | Maximum tokens per request |
| `--db` | string | `$LOOM_DATA_DIR/loom.db` | SQLite database path |
| `--observability` | bool | `true` | Enable observability (use `--observability=false` to disable) |
| `--hawk-endpoint` | string | `""` | Hawk endpoint URL |
| `--hawk-key` | string | `""` | Hawk API key (or use keyring/env) |
| `--log-level` | string | `info` | Log level (debug, info, warn, error) |
| `--log-format` | string | `text` | Log format (text, json) |
| `--yolo` | bool | `false` | Bypass all tool permission prompts |
| `--require-approval` | bool | `false` | Require user approval before executing tools |


## Global Client Flags

These persistent flags are available on all `loom` commands:

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-s`, `--server` | string | `127.0.0.1:60051` | Loom server address |
| `--session` | string | `""` | Resume existing session ID |
| `-t`, `--thread` | string | `""` | Thread ID to connect to |
| `--tls` | bool | `false` | Enable TLS connection |
| `--tls-insecure` | bool | `false` | Skip TLS certificate verification |
| `--tls-ca-file` | string | `""` | Path to CA certificate file |
| `--tls-server-name` | string | `""` | Override TLS server name verification |


## Server Commands (`looms`)

### looms serve

Start the Loom gRPC server. Initializes agents from config, sets up session persistence with SQLite, enables observability, and listens for gRPC and HTTP requests.

**Usage:**
```bash
looms serve [flags]
```

No additional flags beyond the global server flags. Server port, host, HTTP port, LLM provider, database path, and all other settings are controlled via global flags or the config file.

**Examples:**

Start server with defaults (gRPC on :60051, HTTP on :5006):
```bash
looms serve
```

Start with custom ports:
```bash
looms serve --port 9090 --http-port 8888
```

Start with Ollama and debug logging:
```bash
looms serve --llm-provider ollama --log-level debug
```

Start with YOLO mode (skip tool approval prompts):
```bash
looms serve --yolo
```

**When to Use:**
- Starting the server for agent interactions
- Running multiple agents simultaneously
- Development and production deployments


### looms config

Manage Loom Server configuration files and secrets.

**Usage:**
```bash
looms config <subcommand> [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `init` | Generate example configuration file (interactive) |
| `set <key> <value>` | Set a non-sensitive configuration value |
| `get <key>` | Get a configuration value |
| `show` | Display current configuration (merged from all sources) |
| `set-key <key-name>` | Save API key to system keyring (interactive, input hidden) |
| `get-key <key-name>` | Retrieve API key from system keyring (partially masked) |
| `delete-key <key-name>` | Delete API key from system keyring |
| `list-keys` | List available secret key names |

**Examples:**

Initialize config interactively:
```bash
looms config init
```

Set LLM provider to Bedrock:
```bash
looms config set llm.provider bedrock
looms config set llm.bedrock_region us-west-2
looms config set llm.bedrock_model_id anthropic.claude-sonnet-4-5-20250929-v1:0
```

Store API key securely:
```bash
looms config set-key anthropic_api_key
# Prompts: Enter anthropic_api_key (input hidden):
```

View current configuration:
```bash
looms config show
```

**Configuration Hierarchy:**
1. Command-line flags (highest priority)
2. `$LOOM_DATA_DIR/looms.yaml`
3. Default values (lowest priority)

**When to Use:**
- Initial server setup
- Changing LLM providers
- Storing API keys securely in the OS keyring
- Verifying current configuration


### looms hitl

Manage human-in-the-loop (HITL) requests from agents.

**Usage:**
```bash
looms hitl <subcommand> [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `list` | List pending HITL requests |
| `show <request-id>` | Show details of a specific HITL request |
| `respond <request-id>` | Respond to a HITL request |

**Flags (list):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--session` | string | `""` | Filter by session ID |
| `--agent` | string | `""` | Filter by agent ID |
| `--db` | string | `$LOOM_DATA_DIR/hitl.db` | Path to HITL SQLite database |

**Flags (respond):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--status` | string | `approved` | Response status (approved, rejected, responded) |
| `--message` | string | required | Response message |
| `--data` | string | `""` | Response data as JSON (optional) |
| `--by` | string | `""` | Who is responding (default: current user) |
| `--db` | string | `$LOOM_DATA_DIR/hitl.db` | Path to HITL SQLite database |

**Examples:**

List pending requests:
```bash
looms hitl list
looms hitl list --session sess-123
looms hitl list --agent agent-1
```

Show details of a request:
```bash
looms hitl show req-abc123
```

Approve a request:
```bash
looms hitl respond req-abc123 --status approved --message "Yes, proceed"
```

Reject a request:
```bash
looms hitl respond req-abc123 --status rejected --message "No, cancel"
```

Provide input with structured data:
```bash
looms hitl respond req-abc123 --status responded --message "Use PostgreSQL" --data '{"confirmed":true}'
```


### looms upgrade

Upgrade the Loom database schema to the latest version.

**Usage:**
```bash
looms upgrade [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--dry-run` | bool | `false` | Show pending migrations without applying |
| `--backup-only` | bool | `false` | Only create a backup, don't migrate (SQLite only) |
| `--no-backup` | bool | `false` | Skip backup (not recommended) |
| `-y`, `--yes` | bool | `false` | Skip confirmation prompt |

**Examples:**

Show pending migrations without applying:
```bash
looms upgrade --dry-run
```

Upgrade with backup (default behavior):
```bash
looms upgrade
```

Upgrade without prompting:
```bash
looms upgrade --yes
```


### looms pattern

Manage patterns for agents.

**Usage:**
```bash
looms pattern <subcommand> [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `create <pattern-name>` | Create a new pattern at runtime |
| `watch` | Watch for real-time pattern updates |

**Flags (create):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--thread` | string | required | Thread ID to create pattern for |
| `--file` | string | `""` | Path to pattern YAML file |
| `--stdin` | bool | `false` | Read pattern YAML from stdin |
| `--interactive` | bool | `false` | Open editor to create pattern interactively |
| `--server` | string | `localhost:9090` | Loom server address |
| `--timeout` | int | `30` | Request timeout in seconds |

**Flags (watch):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--thread` | string | `""` | Filter by thread ID (optional) |
| `--category` | string | `""` | Filter by pattern category (optional) |
| `--server` | string | `localhost:9090` | Loom server address |

**Examples:**

Create pattern from file:
```bash
looms pattern create my-pattern --thread sql-thread --file pattern.yaml
```

Create pattern from stdin:
```bash
cat pattern.yaml | looms pattern create my-pattern --thread sql-thread --stdin
```

Watch all pattern updates:
```bash
looms pattern watch
```

Watch updates for a specific thread:
```bash
looms pattern watch --thread sql-thread
```


### looms workflow

Manage and execute workflow orchestrations for multi-agent coordination.

Workflows support 6 orchestration patterns: debate, fork-join, pipeline, parallel, conditional, and swarm.

**Usage:**
```bash
looms workflow <subcommand> [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `validate <file>` | Validate a workflow YAML file |
| `run <file>` | Execute a workflow from YAML file |
| `list [directory]` | List available workflow files |

**Flags (run):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--threads` | stringSlice | `[]` | Comma-separated thread IDs to register |
| `--dry-run` | bool | `false` | Validate without executing |
| `--timeout` | int | `3600` | Execution timeout in seconds |

**Flags (list):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-d`, `--dir` | string | `""` | Directory to search (default: auto-detect) |

**Examples:**

Validate a workflow file:
```bash
looms workflow validate architecture-debate.yaml
```

Execute a workflow:
```bash
looms workflow run code-review.yaml
```

Execute with specific threads:
```bash
looms workflow run --threads=architect,pragmatist code-review.yaml
```

Dry-run (validate without executing):
```bash
looms workflow run --dry-run feature-pipeline.yaml
```

List available workflows:
```bash
looms workflow list examples/workflows
```


### looms learning

Manage the Learning Agent (Burler) -- self-improving agent system.

**Usage:**
```bash
looms learning <subcommand> [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `analyze` | Analyze pattern effectiveness |
| `proposals` | List improvement proposals |
| `apply <improvement-id>` | Apply an improvement proposal |
| `rollback <improvement-id>` | Rollback an applied improvement |
| `history` | Get improvement history |
| `stream` | Stream real-time pattern metrics |
| `tune` | Automatically tune pattern priorities |

All subcommands share these common flags:

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--server` | string | `localhost:60051` | Loom server address |
| `--timeout` | int | `30` | Request timeout in seconds |

**Flags (analyze):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--domain` | string | `""` | Filter by domain (optional) |
| `--agent` | string | `""` | Filter by agent ID (optional) |
| `--window` | int | `24` | Time window in hours |

**Flags (proposals):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--domain` | string | `""` | Filter by domain |
| `--agent` | string | `""` | Filter by agent ID |
| `--status` | string | `pending` | Filter by status (pending, applied, rolled_back, rejected). Note: unrecognized values default to pending. |
| `--limit` | int32 | `20` | Maximum number of proposals to show |

**Flags (history):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--domain` | string | `""` | Filter by domain |
| `--agent` | string | `""` | Filter by agent ID |
| `--status` | string | `""` | Filter by status (pending, applied, rolled_back, rejected) |
| `--limit` | int32 | `50` | Maximum number of entries |

**Flags (stream):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--domain` | string | `""` | Filter by domain |
| `--agent` | string | `""` | Filter by agent ID |

**Flags (tune):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--domain` | string | `""` | Filter by domain |
| `--agent` | string | `""` | Filter by agent ID |
| `--strategy` | string | `moderate` | Tuning strategy (conservative, moderate, aggressive) |
| `--dry-run` | bool | `false` | Preview changes without applying them |
| `--library` | string | required | Path to pattern library YAML files |
| `--cost-weight` | float64 | `0.0` | Legacy: Optimization weight for cost (0.0-1.0) |
| `--quality-weight` | float64 | `0.0` | Legacy: Optimization weight for quality (0.0-1.0) |
| `--latency-weight` | float64 | `0.0` | Legacy: Optimization weight for latency (0.0-1.0) |
| `--dimension-quality` | float64 | `0.0` | Judge dimension weight for quality (0.0-1.0) |
| `--dimension-safety` | float64 | `0.0` | Judge dimension weight for safety (0.0-1.0) |
| `--dimension-cost` | float64 | `0.0` | Judge dimension weight for cost (0.0-1.0) |
| `--dimension-domain` | float64 | `0.0` | Judge dimension weight for domain compliance (0.0-1.0) |
| `--dimension-performance` | float64 | `0.0` | Judge dimension weight for performance (0.0-1.0) |
| `--dimension-usability` | float64 | `0.0` | Judge dimension weight for usability (0.0-1.0) |

**Examples:**

Analyze pattern effectiveness:
```bash
looms learning analyze
looms learning analyze --domain=sql --window=48
looms learning analyze --agent=sql-optimizer-abc123
```

List pending improvement proposals:
```bash
looms learning proposals
looms learning proposals --status=applied --domain=sql
```

Apply an improvement:
```bash
looms learning apply abc123-def456-ghi789
```

Rollback an applied improvement:
```bash
looms learning rollback abc123-def456-ghi789
```

Stream real-time pattern metrics:
```bash
looms learning stream
looms learning stream --domain=sql
```

Tune patterns with dimension weights:
```bash
looms learning tune --domain=sql --strategy=moderate --library=/path/to/patterns
looms learning tune --domain=sql --dry-run \
  --dimension-quality=0.6 --dimension-safety=0.3 --dimension-cost=0.1 \
  --library=/path/to/patterns
```


### looms eval

Run and manage evaluation suites.

**Note:** Requires the `hawk` build tag (`go build -tags hawk,fts5`).

**Usage:**
```bash
looms eval <subcommand> [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `run <suite-file>` | Run an evaluation suite |
| `list` | List evaluation runs |
| `show <run-id>` | Show evaluation run details |

**Flags (run):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--thread` | string | `""` | Thread ID to use (default: suite's agent_id) |
| `--store` | string | `./evals.db` | SQLite database path |

**Flags (list):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--suite` | string | `""` | Filter by suite name |
| `--store` | string | `./evals.db` | SQLite database path |
| `--limit` | int | `10` | Maximum number of results |

**Flags (show):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--store` | string | `./evals.db` | SQLite database path |

**Examples:**

Run an eval suite:
```bash
looms eval run examples/dogfooding/config-loader-eval.yaml
```

Run with custom thread ID:
```bash
looms eval run --thread my-thread suite.yaml
```

List recent eval runs for a suite:
```bash
looms eval list --suite config-loader-quality
```

Show specific run details:
```bash
looms eval show 42
```


### looms judge

Multi-dimensional LLM evaluation across 6 dimensions: quality, safety, cost, domain, performance, usability.

**Usage:**
```bash
looms judge <subcommand> [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `evaluate` | Evaluate agent output with judges |
| `evaluate-stream` | Evaluate with streaming progress |
| `register <config-file>` | Register a new judge from YAML config |
| `history` | Get judge evaluation history |

All subcommands share these common flags:

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--server` | string | `localhost:60051` | Loom server address |
| `--timeout` | int | `60` | Request timeout in seconds |

**Flags (evaluate / evaluate-stream):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--agent` | string | required | Agent ID |
| `--prompt` | string | `""` | User prompt/input |
| `--prompt-file` | string | `""` | Read prompt from file |
| `--response` | string | `""` | Agent response/output |
| `--response-file` | string | `""` | Read response from file |
| `--judges` | stringSlice | required | Judge IDs (comma-separated) |
| `--aggregation` | string | `weighted-average` | Strategy: weighted-average, all-must-pass, majority-pass, any-pass, min-score, max-score |
| `--export-to-hawk` | bool | `false` | Export results to Hawk |
| `--fail-fast` | bool | `false` | Abort if any critical judge fails |
| `--pattern` | string | `""` | Pattern used (optional) |

**Flags (register):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--max-attempts` | int | `3` | Maximum retry attempts |
| `--initial-backoff-ms` | int | `1000` | Initial backoff in milliseconds |
| `--max-backoff-ms` | int | `8000` | Maximum backoff in milliseconds |
| `--backoff-multiplier` | float64 | `2.0` | Backoff multiplier |
| `--circuit-breaker` | bool | `true` | Enable circuit breaker |
| `--circuit-breaker-failure-threshold` | int | `5` | Circuit breaker failure threshold |
| `--circuit-breaker-reset-timeout-ms` | int | `60000` | Circuit breaker reset timeout |
| `--circuit-breaker-success-threshold` | int | `2` | Circuit breaker success threshold |

**Flags (history):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--agent` | string | `""` | Filter by agent ID |
| `--pattern` | string | `""` | Filter by pattern name |
| `--judges` | stringSlice | `[]` | Filter by judge ID(s) |
| `--start-time` | string | `""` | Start time (RFC3339 format) |
| `--end-time` | string | `""` | End time (RFC3339 format) |
| `--limit` | int32 | `50` | Maximum number of results |
| `--offset` | int32 | `0` | Offset for pagination |

**Examples:**

Evaluate with quality and safety judges:
```bash
looms judge evaluate \
  --agent=sql-agent \
  --prompt="Generate a SELECT query" \
  --response="SELECT * FROM users" \
  --judges=quality-judge,safety-judge
```

Stream evaluation progress:
```bash
looms judge evaluate-stream \
  --agent=sql-agent \
  --prompt="Query for admins" \
  --response="SELECT * FROM users WHERE role='admin'" \
  --judges=quality-judge,safety-judge,cost-judge \
  --aggregation=weighted-average
```

Register a judge:
```bash
looms judge register config/judges/quality-judge.yaml
```

View evaluation history:
```bash
looms judge history --agent=sql-agent --limit=20
looms judge history --start-time=2026-01-01T00:00:00Z --end-time=2026-01-10T23:59:59Z
```


### looms validate

Validate Loom configuration files (agent configs, backends, patterns, workflows, skills, eval suites).

**Usage:**
```bash
looms validate <subcommand> [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `file <path>` | Validate a single configuration file |
| `dir <path>` | Validate all YAML files in a directory recursively |

The `file` subcommand automatically detects file type by reading the `kind` field in the YAML:
- `Agent` -- Agent configuration
- `AgentTemplate` -- Agent template configuration
- `Workflow` -- Workflow configuration
- `Skill` -- Skill configuration
- `Project` -- Project configuration
- `Backend` -- Backend configuration
- `PatternLibrary` -- Pattern library configuration
- `EvalSuite` -- Evaluation suite configuration

**Examples:**

Validate a single file:
```bash
looms validate file agents/my-agent.yaml
looms validate file workflows/my-workflow.yaml
```

Validate all YAML files in a directory:
```bash
looms validate dir examples/
looms validate dir examples/backends/
```


### looms teleprompter

DSPy-style prompt optimization using multi-judge evaluation.

**Usage:**
```bash
looms teleprompter <subcommand> [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `bootstrap` | Bootstrap few-shot demonstrations using multi-judge evaluation |
| `mipro` | MIPRO (Multi-prompt Instruction Proposal Optimizer) prompt optimization |
| `textgrad` | TextGrad-style prompt optimization using gradient-based feedback (not yet integrated with TeleprompterService) |
| `history` | Show compilation history for an agent |
| `rollback <compilation-id>` | Rollback to a previous compilation |
| `compare <compilation-a> <compilation-b>` | A/B test two compiled versions on a testset |

**Flags (bootstrap):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--agent` | string | required | Agent ID |
| `--trainset` | string | required | Path to training data JSONL file |
| `--judges` | string | required | Comma-separated judge IDs |
| `--output` | string | required | Output file path |
| `--max-demos` | int | `5` | Maximum number of demonstrations to select |
| `--min-confidence` | float64 | `0.8` | Minimum confidence threshold (0.0-1.0) |
| `--dimension-weights` | string | `""` | JSON map of dimension weights |
| `--aggregation` | string | `WEIGHTED_AVERAGE` | Aggregation strategy (WEIGHTED_AVERAGE, ALL_MUST_PASS, MAJORITY_PASS, etc.) |
| `--server` | string | `localhost:60051` | Loom server address |
| `--timeout` | int | `300` | Request timeout in seconds |
| `--export-hawk` | bool | `false` | Export results to Hawk |

**Examples:**

Bootstrap with quality and safety judges:
```bash
looms teleprompter bootstrap \
  --agent=sql-agent \
  --trainset=examples.jsonl \
  --judges=quality-judge,safety-judge \
  --max-demos=5 \
  --min-confidence=0.8 \
  --output=demos.yaml
```

With dimension weights:
```bash
looms teleprompter bootstrap \
  --agent=sql-agent \
  --trainset=examples.jsonl \
  --judges=quality-judge,safety-judge,cost-judge \
  --dimension-weights='{"quality":0.6,"safety":0.3,"cost":0.1}' \
  --max-demos=10 \
  --output=demos.yaml
```


## Client Commands (`loom`)

### loom (root)

Launch the interactive TUI chat interface. Connects to the Loom server via gRPC and provides a Bubbletea-based terminal UI with session management, streaming, and real-time cost tracking.

**Usage:**
```bash
loom [flags]
```

When run without a `--thread` flag, defaults to the built-in "guide" agent that helps users discover and select agents. If the server is not reachable, shows a "no-server" splash with connection instructions.

**Examples:**

Start TUI with auto-select:
```bash
loom
```

Connect to a specific thread:
```bash
loom --thread sql-agent
loom -t file-explorer-abc123
```

Connect to a remote server:
```bash
loom --server 192.168.1.100:60051 --thread sql-agent
```

Resume a previous session:
```bash
loom --thread sql-agent --session sess_abc123
```

Connect with TLS:
```bash
loom --tls --tls-ca-file /path/to/ca.pem --server myserver:60051
```


### loom chat

Send a message to an agent and get a response in the terminal, without launching the TUI.

**Usage:**
```bash
loom chat [message] [flags]
```

The `--thread` flag is required. Message can be provided as an argument, via `--message` flag, or piped from stdin.

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-m`, `--message` | string | `""` | Message to send (if not via args or stdin) |
| `--stream` | bool | `false` | Stream response in real-time (shows progress stages) |
| `--timeout` | duration | `5m` | Timeout for response |

**Examples:**

Send message as argument:
```bash
loom chat --thread sql-agent "show me all tables"
```

Send message via flag:
```bash
loom chat --thread sql-agent --message "explain the schema"
```

Pipe from stdin:
```bash
echo "analyze this query" | loom chat --thread sql-agent
```

Stream response with progress:
```bash
loom chat --thread sql-agent --stream "explain the schema"
```

With custom timeout:
```bash
loom chat --thread sql-agent --timeout 10m "run a long analysis"
```


### loom agents

List all available agents and threads configured on the server.

**Usage:**
```bash
loom agents [flags]
```

**Aliases:** `list`, `ls`, `threads`

No additional flags beyond the global client flags.

**Examples:**

```bash
loom agents
loom ls
loom threads
```

**Expected Output:**
```
Available agents (2):

  sql-agent (SQL Agent)
    Status: running | Active sessions: 3 | Total messages: 42
    Uptime: 2h 15m
    Model: claude-sonnet-4-5-20250929 (anthropic)

  file-explorer
    Status: running
    Uptime: 2h 15m

To connect to an agent:
  loom --thread <agent-id>                # Open TUI
  loom chat --thread <agent-id> 'message' # CLI chat
```


### loom sessions

Manage conversation sessions.

**Usage:**
```bash
loom sessions <subcommand> [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `list` | List conversation sessions |
| `show <session-id>` | Show session details |
| `delete <session-id>` | Delete a session and its conversation history |

**Flags (list):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-n`, `--limit` | int32 | `20` | Maximum number of sessions to return |
| `--offset` | int32 | `0` | Number of sessions to skip |

**Examples:**

List sessions:
```bash
loom sessions list
loom sessions list --limit 50
```

Show session details:
```bash
loom sessions show sess_abc123def456
```

Delete a session:
```bash
loom sessions delete sess_abc123def456
```


### loom artifacts

Manage artifacts (upload, download, search, delete).

**Usage:**
```bash
loom artifacts <subcommand> [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `list` | List artifacts with optional filtering |
| `search <query>` | Search artifacts with FTS5 full-text search |
| `show <id-or-name>` | Show artifact details |
| `upload <file-path>` | Upload a file as an artifact |
| `download <id-or-name>` | Download artifact content |
| `delete <id-or-name>` | Delete an artifact |
| `stats` | Show artifact storage statistics |

**Flags (list):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-n`, `--limit` | int32 | `20` | Maximum number of artifacts to return |
| `--offset` | int32 | `0` | Number of artifacts to skip |
| `--source` | string | `""` | Filter by source (user, generated, agent) |
| `--content-type` | string | `""` | Filter by MIME type |
| `--tags` | stringSlice | `[]` | Filter by tags (comma-separated) |
| `--include-deleted` | bool | `false` | Include soft-deleted artifacts |

**Flags (search):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-n`, `--limit` | int32 | `20` | Maximum number of results to return |

**Flags (upload):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--purpose` | string | `""` | Purpose or description of the artifact |
| `--tags` | stringSlice | `[]` | Tags for the artifact (comma-separated) |

**Flags (download):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-o`, `--output` | string | `""` | Output file path (default: stdout or original filename) |

**Flags (delete):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--hard` | bool | `false` | Permanently delete artifact (cannot be undone) |

**Examples:**

List artifacts:
```bash
loom artifacts list
loom artifacts list --source user --tags sql,report
```

Search artifacts:
```bash
loom artifacts search "sales report"
loom artifacts search "excel AND quarterly"
```

Upload a file:
```bash
loom artifacts upload ~/data.csv
loom artifacts upload report.pdf --purpose "Q4 sales report" --tags excel,sales,2024
```

Download an artifact:
```bash
loom artifacts download art_abc123def456
loom artifacts download data.csv --output ~/Downloads/data.csv
```

Delete an artifact:
```bash
loom artifacts delete art_abc123def456
loom artifacts delete data.csv --hard
```

Show storage stats:
```bash
loom artifacts stats
```


### loom mcp

Manage Model Context Protocol (MCP) servers.

**Usage:**
```bash
loom mcp <subcommand> [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `list` | List configured MCP servers and their status |
| `test <server-name>` | Test MCP server connection |
| `tools <server-name>` | List tools from a specific MCP server |

**Examples:**

List MCP servers:
```bash
loom mcp list
```

Test MCP server connection:
```bash
loom mcp test vantage
```

List tools from a server:
```bash
loom mcp tools github
```


### loom providers

Manage the LLM provider pool.

**Usage:**
```bash
loom providers <subcommand> [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `list` | List available providers in the pool |
| `switch <provider-name>` | Switch to a named provider |

**Flags (switch):**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--session` | string | `""` | Session ID to switch model for |
| `--thread` | string | `""` | Thread (agent) ID |

**Examples:**

List available providers:
```bash
loom providers list
```

**Expected Output:**
```
NAME      PROVIDER    MODEL                           ACTIVE
fast      anthropic   claude-sonnet-4-5-20250929      ✓
cheap     ollama      qwen2.5:7b
```

Switch to a different provider:
```bash
loom providers switch cheap
loom providers switch fast --session sess_abc123
```


## Environment Variables

### Server Environment

```bash
# Data directory (all defaults relative to this)
LOOM_DATA_DIR=~/.loom              # Default data directory

# LLM providers (alternatives to config file / keyring)
ANTHROPIC_API_KEY=sk-ant-...
AWS_REGION=us-west-2
OLLAMA_BASE_URL=http://localhost:11434
```

### Client Environment

```bash
# Data directory
LOOM_DATA_DIR=~/.loom
```


## Configuration Files

### Server Configuration (`$LOOM_DATA_DIR/looms.yaml`)

```yaml
server:
  port: 60051
  host: 0.0.0.0
  enable_reflection: true
  http_port: 5006

llm:
  provider: anthropic
  anthropic_model: claude-sonnet-4-5-20250929
  # anthropic_api_key: set via keyring (looms config set-key anthropic_api_key)
  temperature: 1.0
  max_tokens: 4096
  timeout_seconds: 60

database:
  path: ./loom.db
  driver: sqlite

observability:
  enabled: false
  provider: hawk
  hawk_endpoint: ""

logging:
  level: info
  format: text

tools:
  permissions:
    yolo: false
    require_approval: false

# Multi-agent configuration
agents:
  agents:
    sql-agent:
      name: SQL Agent
      description: Queries databases
      backend_path: ./examples/backends/sqlite.yaml
      system_prompt: |
        Help users query databases. Explain what you're doing before taking action.
      max_turns: 25
      max_tool_executions: 50
      enable_tracing: true
```


## Exit Codes

All commands use standard exit codes:

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |


## Common Workflows

### Initial Setup

```bash
# 1. Initialize server configuration interactively
looms config init

# 2. Store API key securely
looms config set-key anthropic_api_key

# 3. Start the server
looms serve

# 4. In another terminal, list available agents
loom agents

# 5. Connect to an agent
loom --thread sql-agent

# 6. Or use CLI chat
loom chat --thread sql-agent "show me all tables"
```


### Pattern Development

```bash
# 1. Create pattern from file
looms pattern create my-pattern --thread sql-thread --file pattern.yaml

# 2. Watch for pattern updates in real-time
looms pattern watch --thread sql-thread

# 3. Monitor pattern effectiveness
looms learning analyze --domain=sql --window=24

# 4. Tune pattern priorities
looms learning tune --domain=sql --strategy=moderate --library=/path/to/patterns
```


### Evaluation Workflow

```bash
# 1. Run evaluation suite
looms eval run examples/dogfooding/config-loader-eval.yaml

# 2. List eval runs
looms eval list --suite config-loader-quality

# 3. Show run details
looms eval show 42

# 4. Judge-based evaluation
looms judge evaluate \
  --agent=sql-agent \
  --judges=quality-judge,safety-judge \
  --prompt="Generate a SELECT query" \
  --response="SELECT * FROM users"
```


### Database Upgrade

```bash
# 1. Check pending migrations (dry-run)
looms upgrade --dry-run

# 2. Apply migrations (with automatic backup)
looms upgrade --yes
```


## Troubleshooting

### Server Won't Start

**Symptom:** `looms serve` fails with "address already in use"

**Solution:**
```bash
# Check what's using the port
lsof -i :60051

# Kill the process or use a different port
looms serve --port 9090
```


### Cannot Connect to Server

**Symptom:** `loom agents` fails with "Failed to connect to Loom server"

**Solution:**
```bash
# Verify server is running
lsof -i :60051

# Check server address
loom agents --server 127.0.0.1:60051

# Test connectivity
grpcurl -plaintext localhost:60051 list
```


### MCP Server Connection Failed

**Symptom:** `loom mcp test` shows "Not connected"

**Solution:**
```bash
# Test MCP server manually
loom mcp test vantage

# Check server details in output
# Verify the command exists and is executable
which vantage-mcp
```


### No LLM Provider Configured

**Symptom:** `looms serve` fails with "No LLM provider configured"

**Solution:**
```bash
# Run interactive setup
looms config init

# Or set manually
looms config set llm.provider anthropic
looms config set-key anthropic_api_key
```


## See Also

- [Agent Configuration Reference](./agent-configuration.md) - Agent YAML spec
- [Backend Reference](./backend.md) - Backend configuration
- [Pattern System Reference](./patterns.md) - Pattern authoring guide
- [LLM Providers Reference](./llm-providers.md) - LLM configuration

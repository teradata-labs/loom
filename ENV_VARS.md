# Loom Environment Variables

This document describes all environment variables used in the Loom project.

## Table of Contents

- [Core Configuration](#core-configuration)
- [LLM Providers](#llm-providers)
  - [Anthropic](#anthropic)
  - [AWS Bedrock](#aws-bedrock)
  - [Ollama](#ollama)
  - [OpenAI](#openai)
  - [Azure OpenAI](#azure-openai)
  - [Mistral AI](#mistral-ai)
  - [Google Gemini](#google-gemini)
  - [HuggingFace](#huggingface)
- [AWS Credentials](#aws-credentials)
- [Observability (Hawk)](#observability-hawk)
- [Web Search Tools](#web-search-tools)
- [Docker Configuration](#docker-configuration)
- [TLS/ACME Configuration](#tlsacme-configuration)
- [Debug Flags](#debug-flags)
- [System Environment](#system-environment)

---

## Core Configuration

### LOOM_DATA_DIR
**Default:** `$HOME/.loom`
**Used in:** `pkg/config/paths.go`

Specifies the Loom data directory where configuration, databases, and other persistent data are stored.

- If relative path, converted to absolute
- Tilde (`~`) expansion supported
- Takes precedence over default `$HOME/.loom`

**Examples:**
```bash
export LOOM_DATA_DIR=/custom/loom        # Custom absolute path
export LOOM_DATA_DIR=~/my-loom           # Expands to /home/user/my-loom
export LOOM_DATA_DIR=relative/path       # Converted to absolute
```

**Directory Structure:**

Loom creates the following subdirectories within `$LOOM_DATA_DIR/`:

```
$LOOM_DATA_DIR/
├── looms.yaml              # Main configuration file
├── loom.db                 # SQLite database (sessions, artifacts, etc.)
├── agents/                 # Agent configuration files
├── workflows/              # Workflow definitions
├── patterns/               # Pattern library (installed via 'just install-patterns')
├── documentation/          # Loom documentation (installed via 'just install-docs')
├── examples/               # Example configurations
├── artifacts/              # User artifacts and agent outputs
├── scratchpad/             # Agent working directory
├── memory/                 # Agent memory databases
├── tool_results/           # Tool result cache
├── certs/                  # TLS certificates (Let's Encrypt)
└── scheduler.db            # Workflow scheduler database
```

**Migration from $LOOM_DATA_DIR:**

Existing installations using the default location will continue to work. To migrate to a custom location:

```bash
# Stop Loom server
# Move data directory
mv $LOOM_DATA_DIR /var/lib/loom

# Set environment variable (add to ~/.bashrc or ~/.zshrc)
export LOOM_DATA_DIR=/var/lib/loom

# Start Loom server
looms serve
```

**Docker/Container Usage:**

```bash
# Set LOOM_DATA_DIR to a mounted volume
docker run -e LOOM_DATA_DIR=/data/loom -v /host/loom-data:/data/loom loom:latest
```

### LOOM_BIN_DIR
**Default:** `$HOME/.local/bin`
**Used in:** `Justfile` (installation scripts only)
**Type:** Installation-time only (not used at runtime)

Specifies where Loom binaries are installed during `just install` and cleaned during `just clean`.

- Only affects installation scripts, not the running application
- The application never needs to know where its binary is installed
- NOT managed by viper or included in runtime configuration
- Tilde (`~`) expansion supported

**Examples:**
```bash
# Install to custom location
export LOOM_BIN_DIR=/usr/local/bin
just install

# Install to user bin directory (default)
just install  # Uses $HOME/.local/bin

# Clean from custom location
export LOOM_BIN_DIR=/usr/local/bin
just clean
```

**Why not in viper?**
- `LOOM_DATA_DIR` is used **at runtime** by the application to find config/data
- `LOOM_BIN_DIR` is used **at installation time** by build scripts to copy binaries
- The running application never needs to know where its binary is installed

### LOOM_YOLO
**Default:** `false`
**Values:** `true`, `1`, `false`, `0`
**Used in:** `cmd/looms/config.go`

Enables YOLO mode, which bypasses all tool permission prompts for autonomous operation.

```bash
export LOOM_YOLO=true
# or
export LOOM_YOLO=1
```

⚠️ **Warning:** YOLO mode allows agents to execute tools without user approval. Use with caution.

### LOOM_DB_KEY
**Used in:** `pkg/agent/db_config.go`

Encryption key for database encryption (if enabled). Optional - only needed if database encryption is configured.

```bash
export LOOM_DB_KEY=your-encryption-key
```

### LOOM_TOOL_CACHE_DIR
**Used in:** `pkg/storage/persistent_store.go`

Directory for caching tool results and artifacts. If not set, uses system temp directory.

```bash
export LOOM_TOOL_CACHE_DIR=/path/to/cache
```

---

## LLM Providers

### Anthropic

#### ANTHROPIC_API_KEY
**Required for:** Anthropic provider
**Used in:** `cmd/looms/cmd_serve.go`, `pkg/llm/factory/factory.go`, `pkg/agent/registry.go`

API key for Anthropic Claude models.

```bash
export ANTHROPIC_API_KEY=sk-ant-...
```

**Alternative:** Store in system keyring using:
```bash
looms config set-key anthropic_api_key
```

#### ANTHROPIC_DEFAULT_MODEL
**Default:** `claude-sonnet-4-5-20250929`
**Used in:** `pkg/llm/anthropic/client.go`

Override default Anthropic model.

```bash
export ANTHROPIC_DEFAULT_MODEL=claude-opus-4-5-20250929
```

#### ANTHROPIC_API_ENDPOINT
**Default:** Anthropic production API
**Used in:** `pkg/llm/anthropic/client.go`

Custom API endpoint for Anthropic (for proxies or testing).

```bash
export ANTHROPIC_API_ENDPOINT=https://custom-proxy.example.com
```

---

### AWS Bedrock

#### AWS_BEDROCK_MODEL_ID
**Default:** `us.anthropic.claude-sonnet-4-5-20250929-v1:0`
**Used in:** `pkg/llm/bedrock/client.go`

Bedrock model ID. Alternative to `LOOM_LLM_BEDROCK_MODEL_ID`.

```bash
export AWS_BEDROCK_MODEL_ID=anthropic.claude-sonnet-4-5-v2:0
```

#### LOOM_LLM_BEDROCK_MODEL_ID
**Default:** `us.anthropic.claude-sonnet-4-5-20250929-v1:0`
**Used in:** `pkg/llm/bedrock/client.go`

Loom-specific Bedrock model ID (takes precedence over `AWS_BEDROCK_MODEL_ID`).

#### LOOM_LLM_BEDROCK_REGION
**Default:** `us-west-2`
**Used in:** `pkg/llm/bedrock/client.go`

AWS region for Bedrock service. Takes precedence over `AWS_DEFAULT_REGION`.

```bash
export LOOM_LLM_BEDROCK_REGION=us-east-1
```

See [AWS Credentials](#aws-credentials) section for authentication environment variables.

---

### Ollama

#### OLLAMA_ENDPOINT
**Default:** `http://localhost:11434`
**Used in:** `pkg/llm/factory/factory.go`, `pkg/agent/registry.go`

Ollama service endpoint. Alternative to `LOOM_LLM_OLLAMA_ENDPOINT`.

```bash
export OLLAMA_ENDPOINT=http://192.168.1.100:11434
```

#### OLLAMA_BASE_URL
**Default:** `http://localhost:11434`
**Used in:** `pkg/builder/builder.go`

Standard Ollama environment variable. Used if `LOOM_LLM_OLLAMA_ENDPOINT` not set.

#### LOOM_LLM_OLLAMA_ENDPOINT
**Used in:** `pkg/builder/builder.go`

Loom-specific Ollama endpoint (takes precedence over `OLLAMA_BASE_URL`).

#### LOOM_LLM_OLLAMA_MODEL
**Default:** `llama3.1:8b`
**Used in:** `pkg/builder/builder.go`

Ollama model to use.

```bash
export LOOM_LLM_OLLAMA_MODEL=llama3.1:70b
```

---

### OpenAI

#### OPENAI_API_KEY
**Required for:** OpenAI provider
**Used in:** `cmd/looms/cmd_serve.go`, `pkg/llm/factory/factory.go`, `pkg/agent/registry.go`

OpenAI API key.

```bash
export OPENAI_API_KEY=sk-...
```

**Alternative:** Store in keyring:
```bash
looms config set-key openai_api_key
```

#### OPENAI_DEFAULT_MODEL
**Default:** `gpt-4.1`
**Used in:** `pkg/llm/openai/client.go`

Default OpenAI model. Alternative to `LOOM_LLM_OPENAI_MODEL`.

```bash
export OPENAI_DEFAULT_MODEL=gpt-4o
```

#### LOOM_LLM_OPENAI_MODEL
**Used in:** `pkg/llm/openai/client.go`, `pkg/builder/builder.go`

Loom-specific OpenAI model (takes precedence).

#### OPENAI_API_ENDPOINT
**Used in:** `pkg/llm/openai/client.go`

Custom OpenAI API endpoint (for proxies or compatible APIs).

```bash
export OPENAI_API_ENDPOINT=https://api.openai.com/v1
```

#### LOOM_LLM_OPENAI_ENDPOINT
**Used in:** `pkg/llm/openai/client.go`

Loom-specific OpenAI endpoint (takes precedence).

---

### Azure OpenAI

#### AZURE_OPENAI_ENDPOINT
**Required for:** Azure OpenAI provider
**Used in:** `pkg/llm/factory/factory.go`, `pkg/agent/registry.go`

Azure OpenAI service endpoint.

```bash
export AZURE_OPENAI_ENDPOINT=https://your-resource.openai.azure.com
```

#### AZURE_OPENAI_DEPLOYMENT_ID
**Required for:** Azure OpenAI provider
**Used in:** `pkg/llm/factory/factory.go`

Azure OpenAI deployment ID.

```bash
export AZURE_OPENAI_DEPLOYMENT_ID=gpt-4o-deployment
```

#### AZURE_OPENAI_API_KEY
**Used in:** `pkg/llm/factory/factory.go`, `pkg/agent/registry.go`

Azure OpenAI API key (alternative to Entra token).

```bash
export AZURE_OPENAI_API_KEY=your-key
```

**Alternative:** Store in keyring:
```bash
looms config set-key azure_openai_api_key
```

#### AZURE_OPENAI_ENTRA_TOKEN
**Used in:** `pkg/llm/factory/factory.go`

Azure Entra ID token (alternative to API key).

```bash
export AZURE_OPENAI_ENTRA_TOKEN=your-token
```

---

### Mistral AI

#### MISTRAL_API_KEY
**Required for:** Mistral provider
**Used in:** `pkg/llm/factory/factory.go`, `pkg/agent/registry.go`

Mistral AI API key.

```bash
export MISTRAL_API_KEY=your-key
```

**Alternative:** Store in keyring:
```bash
looms config set-key mistral_api_key
```

---

### Google Gemini

#### GEMINI_API_KEY
**Required for:** Gemini provider
**Used in:** `pkg/llm/factory/factory.go`, `pkg/agent/registry.go`

Google Gemini API key.

```bash
export GEMINI_API_KEY=your-key
```

**Alternative:** Store in keyring:
```bash
looms config set-key gemini_api_key
```

---

### HuggingFace

#### HUGGINGFACE_TOKEN
**Required for:** HuggingFace provider
**Used in:** `pkg/llm/factory/factory.go`, `pkg/agent/registry.go`

HuggingFace API token.

```bash
export HUGGINGFACE_TOKEN=hf_...
```

**Alternative:** Store in keyring:
```bash
looms config set-key huggingface_token
```

---

## AWS Credentials

These environment variables are used for AWS Bedrock authentication and other AWS services.

### AWS_REGION
**Used in:** `pkg/llm/factory/factory.go`, `pkg/agent/registry.go`

AWS region for services. Alternative to `AWS_DEFAULT_REGION`.

```bash
export AWS_REGION=us-east-1
```

### AWS_DEFAULT_REGION
**Default:** `us-west-2`
**Used in:** `pkg/llm/bedrock/client.go`, `pkg/agent/semantic_search_conversation_test.go`

Default AWS region (used if `AWS_REGION` not set).

```bash
export AWS_DEFAULT_REGION=us-west-2
```

### AWS_ACCESS_KEY_ID
**Used in:** `pkg/agent/semantic_search_conversation_test.go`

AWS access key ID for authentication.

```bash
export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE
```

⚠️ **Security Note:** Prefer using AWS profiles or IAM roles over explicit credentials.

### AWS_SECRET_ACCESS_KEY
**Used in:** `pkg/agent/semantic_search_conversation_test.go`

AWS secret access key for authentication.

```bash
export AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
```

⚠️ **Security Note:** Never commit this to version control. Use keyring or AWS profiles.

### AWS_SESSION_TOKEN
**Used in:** `pkg/agent/semantic_search_conversation_test.go`

AWS session token for temporary credentials.

```bash
export AWS_SESSION_TOKEN=your-session-token
```

### AWS_PROFILE
**Used in:** `pkg/agent/registry.go`, `pkg/agent/semantic_search_conversation_test.go`

AWS credentials profile name from `~/.aws/credentials`.

```bash
export AWS_PROFILE=bedrock
```

**Recommended:** Use profiles instead of explicit credentials:
```bash
aws configure --profile bedrock
export AWS_PROFILE=bedrock
```

---

## Observability (Hawk)

### HAWK_URL
**Used in:** `pkg/observability/auto_select.go`

Hawk service URL for observability tracing.

```bash
export HAWK_URL=https://hawk.example.com
```

Alternative name for `HAWK_ENDPOINT`.

### HAWK_ENDPOINT
**Used in:** `pkg/evals/hawk_export.go`, `pkg/observability/hawk_judge_exporter.go`

Hawk service endpoint URL.

```bash
export HAWK_ENDPOINT=https://hawk.example.com
```

### HAWK_API_KEY
**Used in:** `pkg/observability/auto_select.go`, `pkg/evals/hawk_export.go`

API key for Hawk service authentication.

```bash
export HAWK_API_KEY=your-hawk-key
```

**Alternative:** Store in keyring:
```bash
looms config set-key hawk_api_key
```

### LOOM_TRACER_MODE
**Default:** `auto`
**Values:** `auto`, `service`, `embedded`, `none`
**Used in:** `pkg/builder/builder.go`

Controls which tracer implementation to use:
- `auto`: Automatically select based on environment
- `service`: Use Hawk service (gRPC)
- `embedded`: Use embedded storage (memory or SQLite)
- `none`: Disable tracing

```bash
export LOOM_TRACER_MODE=embedded
```

### LOOM_TRACER_PREFER_EMBEDDED
**Default:** `true`
**Values:** `true`, `false`
**Used in:** `pkg/builder/builder.go`

When `LOOM_TRACER_MODE=auto`, prefer embedded tracer over service if both available.

```bash
export LOOM_TRACER_PREFER_EMBEDDED=false
```

### LOOM_EMBEDDED_STORAGE
**Default:** `memory`
**Values:** `memory`, `sqlite`
**Used in:** `pkg/builder/builder.go`

Storage backend for embedded tracer.

```bash
export LOOM_EMBEDDED_STORAGE=sqlite
```

### LOOM_EMBEDDED_SQLITE_PATH
**Required when:** `LOOM_EMBEDDED_STORAGE=sqlite`
**Used in:** `pkg/observability/auto_select.go`

Path to SQLite database for embedded tracer.

```bash
export LOOM_EMBEDDED_SQLITE_PATH=/path/to/traces.db
```

---

## Web Search Tools

### TAVILY_API_KEY
**Used in:** `pkg/shuttle/builtin/web_search.go`

Tavily AI Search API key (1000 searches/month free tier).

```bash
export TAVILY_API_KEY=tvly-...
```

**Alternative:** Store in keyring:
```bash
looms config set-key tavily_api_key
```

### BRAVE_API_KEY
**Used in:** `pkg/shuttle/builtin/web_search.go`

Brave Search API key (2000 searches/month free tier). Alternative to `BRAVE_SEARCH_API_KEY`.

```bash
export BRAVE_API_KEY=BSA...
```

### BRAVE_SEARCH_API_KEY
**Used in:** `pkg/shuttle/builtin/web_search.go`

Alternative name for Brave Search API key.

**Alternative:** Store in keyring:
```bash
looms config set-key brave_search_api_key
```

### SERPAPI_KEY
**Used in:** `pkg/shuttle/builtin/web_search.go`

SerpAPI key. Alternative to `SERPAPI_API_KEY`.

```bash
export SERPAPI_KEY=your-key
```

### SERPAPI_API_KEY
**Used in:** `pkg/shuttle/builtin/web_search.go`

Alternative name for SerpAPI key.

**Alternative:** Store in keyring:
```bash
looms config set-key serpapi_key
```

### LOOM_WEB_SEARCH_TIMEOUT_SECONDS
**Default:** `30`
**Used in:** `pkg/shuttle/builtin/web_search.go`

HTTP timeout for web search requests (in seconds).

```bash
export LOOM_WEB_SEARCH_TIMEOUT_SECONDS=60
```

### LOOM_WEB_SEARCH_BRAVE_ENDPOINT
**Default:** `https://api.search.brave.com/res/v1/web/search`
**Used in:** `pkg/shuttle/builtin/web_search.go`

Custom Brave Search API endpoint.

### LOOM_WEB_SEARCH_TAVILY_ENDPOINT
**Default:** `https://api.tavily.com/search`
**Used in:** `pkg/shuttle/builtin/web_search.go`

Custom Tavily API endpoint.

### LOOM_WEB_SEARCH_SERPAPI_ENDPOINT
**Default:** `https://serpapi.com/search`
**Used in:** `pkg/shuttle/builtin/web_search.go`

Custom SerpAPI endpoint.

### LOOM_WEB_SEARCH_DUCKDUCKGO_ENDPOINT
**Default:** `https://api.duckduckgo.com/`
**Used in:** `pkg/shuttle/builtin/web_search.go`

Custom DuckDuckGo API endpoint.

---

## Docker Configuration

### DOCKER_HOST
**Used in:** `pkg/docker/scheduler.go`

Docker daemon host. Supports Unix sockets and TCP.

```bash
export DOCKER_HOST=unix:///var/run/docker.sock
# or
export DOCKER_HOST=tcp://localhost:2375
```

**Auto-detection:** If not set, Loom tries:
- macOS: `$HOME/.orbstack/run/docker.sock`, `$HOME/.docker/run/docker.sock`, `/var/run/docker.sock`
- Linux: `/var/run/docker.sock`

### LOOM_DOCKER_SOCKET_PATHS
**Used in:** `pkg/docker/scheduler.go`

Comma-separated list of Docker socket paths to try (overrides defaults).

```bash
export LOOM_DOCKER_SOCKET_PATHS=/custom/docker.sock,/var/run/docker.sock
```

---

## TLS/ACME Configuration

### LOOM_ACME_PRODUCTION_URL
**Default:** Let's Encrypt production URL
**Used in:** `pkg/tls/letsencrypt.go`

Custom ACME directory URL for production certificates.

```bash
export LOOM_ACME_PRODUCTION_URL=https://acme-v02.api.letsencrypt.org/directory
```

### LOOM_ACME_STAGING_URL
**Default:** Let's Encrypt staging URL
**Used in:** `pkg/tls/letsencrypt.go`

Custom ACME directory URL for staging/testing certificates.

```bash
export LOOM_ACME_STAGING_URL=https://acme-staging-v02.api.letsencrypt.org/directory
```

---

## Debug Flags

### LOOM_DEBUG_BEDROCK
**Default:** `0` (disabled)
**Values:** `1` (enabled), `0` (disabled)
**Used in:** `pkg/llm/bedrock/converse.go`, `pkg/agent/agent.go`

Enable verbose debug logging for AWS Bedrock interactions.

```bash
export LOOM_DEBUG_BEDROCK=1
```

Logs:
- Request/response payloads
- Token usage
- Timing information
- Tool call details

### LOOM_DEBUG_BEDROCK_TOOLS
**Default:** `0` (disabled)
**Values:** `1` (enabled), `0` (disabled)
**Used in:** `pkg/mcp/adapter/shuttle.go`

Enable debug logging for Bedrock tool execution.

```bash
export LOOM_DEBUG_BEDROCK_TOOLS=1
```

---

## System Environment

These are standard Unix/Linux environment variables that Loom respects.

### EDITOR
**Default:** `vi` or `nano` (fallback)
**Used in:** `cmd/looms/cmd_docs.go`, `internal/uiutil/uiutil.go`, `internal/tui/components/chat/editor/editor.go`

Default text editor for interactive editing.

```bash
export EDITOR=vim
# or
export EDITOR=code  # VS Code
# or
export EDITOR=nano
```

### HOME
**Used in:** `pkg/docker/scheduler.go`, `cmd/looms/cmd_serve.go`

User home directory. Used for:
- Docker socket path detection (`$HOME/.orbstack/run/docker.sock`)
- Default config paths
- Path expansion

Typically set by the system. Manual override:
```bash
export HOME=/home/username
```

### USER
**Used in:** `pkg/docker/scheduler.go`, `cmd/looms/cmd_hitl.go`

Current username. Used for:
- Docker socket path detection on macOS (`$HOME/.orbstack/run/docker.sock`)
- HITL (human-in-the-loop) respondent tracking

Typically set by the system. Manual override:
```bash
export USER=username
```

---

## Environment Variable Naming Conventions

Loom uses the following naming conventions:

1. **LOOM_** prefix: Loom-specific configuration
   - Runtime: `LOOM_DATA_DIR`, `LOOM_YOLO`
   - Installation-time: `LOOM_BIN_DIR`

2. **LOOM_LLM_** prefix: LLM provider configuration
   - Example: `LOOM_LLM_BEDROCK_REGION`, `LOOM_LLM_OLLAMA_MODEL`

3. **LOOM_WEB_SEARCH_** prefix: Web search tool configuration
   - Example: `LOOM_WEB_SEARCH_TIMEOUT_SECONDS`

4. **LOOM_DEBUG_** prefix: Debug flags
   - Example: `LOOM_DEBUG_BEDROCK`

5. **Standard names**: Standard environment variables from external tools
   - Example: `AWS_REGION`, `ANTHROPIC_API_KEY`, `DOCKER_HOST`

---

## Priority Order

Configuration sources are applied in this order (highest to lowest priority):

1. **Command-line flags** (e.g., `--anthropic-key`)
2. **Environment variables** (e.g., `ANTHROPIC_API_KEY`)
3. **Configuration file** (`looms.yaml`)
4. **System keyring** (via `looms config set-key`)
5. **Defaults** (hardcoded in application)

**Note:** Installation-time environment variables like `LOOM_BIN_DIR` are not part of this priority order because they are only used by installation scripts (`Justfile`), not by the running application.

---

## Security Best Practices

### Secrets Management

❌ **Don't:**
- Commit secrets to version control
- Store secrets in config files
- Use explicit credentials in CI/CD (prefer IAM roles)

✅ **Do:**
- Use system keyring for local development:
  ```bash
  looms config set-key anthropic_api_key
  ```
- Use environment variables for CI/CD
- Use AWS profiles instead of explicit credentials
- Use IAM roles in cloud environments

### Example: Secure Setup

```bash
# Store API keys in keyring (recommended for local dev)
looms config set-key anthropic_api_key
looms config set-key hawk_api_key

# Use AWS profile (recommended for Bedrock)
aws configure --profile bedrock
export AWS_PROFILE=bedrock

# Customize installation directories (optional)
export LOOM_BIN_DIR=/usr/local/bin      # Where to install binaries
export LOOM_DATA_DIR=~/my-loom-data     # Where to store data/config

# Install with custom paths
just install

# Enable YOLO mode for autonomous operation (optional)
export LOOM_YOLO=true

# Start server (uses LOOM_DATA_DIR at runtime)
looms serve
```

---

## Testing Environment Variables

Many environment variables are used in tests. See test files for examples:

- `pkg/config/paths_test.go` - LOOM_DATA_DIR tests
- `pkg/agent/semantic_search_conversation_test.go` - AWS credentials tests
- `pkg/server/hot_reload_integration_test.go` - ANTHROPIC_API_KEY tests

To run tests with environment variables:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
export AWS_PROFILE=bedrock
just test
```

---

## Troubleshooting

### "API key not configured"

**Problem:** Missing API key for LLM provider.

**Solution:**
```bash
# Check current configuration
looms config list

# Set API key in keyring
looms config set-key anthropic_api_key

# Or use environment variable
export ANTHROPIC_API_KEY=sk-ant-...
```

### "Docker daemon not found"

**Problem:** DOCKER_HOST not found or invalid.

**Solution:**
```bash
# Check Docker status
docker ps

# Set DOCKER_HOST if needed
export DOCKER_HOST=unix:///var/run/docker.sock

# Or specify custom socket paths
export LOOM_DOCKER_SOCKET_PATHS=/custom/docker.sock
```

### "Failed to connect to Hawk"

**Problem:** Hawk observability service not reachable.

**Solution:**
```bash
# Set Hawk endpoint
export HAWK_ENDPOINT=https://hawk.example.com
export HAWK_API_KEY=your-key

# Or disable observability
looms serve --observability=false

# Or use embedded mode
export LOOM_TRACER_MODE=embedded
export LOOM_EMBEDDED_STORAGE=sqlite
export LOOM_EMBEDDED_SQLITE_PATH=./traces.db
```

---

## See Also

- [README.md](README.md) - Project overview
- [CLAUDE.md](CLAUDE.md) - Development guidelines
- Configuration file: `$LOOM_DATA_DIR/looms.yaml`
- Keyring management: `looms config set-key`, `looms config get-key`, `looms config list-keys`
- Build configuration: `Justfile` - Uses `LOOM_BIN_DIR` and `LOOM_DATA_DIR` for installation
